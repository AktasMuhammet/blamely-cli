package tools

// Conversation + token-usage extraction for the VS Code / Cursor chat-session
// JSONL files that CopilotChatWatcher tags onto edits via raw_meta's
// chat_session_path. These files are NOT the Claude/Cursor role+type transcript
// format that transcript.go handles — they use a delta encoding:
//
//	{"kind":0,"v":{...full snapshot incl. requests[] and inputState...}}
//	{"kind":2,"k":["requests"],"v":[{...new request, incl. message.text...}]}
//	{"kind":2,"k":["requests",N,"response"],"v":[{kind?,value}...]}  // streamed
//	{"kind":1,"k":["requests",N,"result"],"v":{usage:{promptTokens,...}}}
//	{"kind":1,"k":["inputState","selectedModel"],"v":{identifier:"copilot/…"}}
//
// kind=0 = snapshot, kind=1 = set value at key path, kind=2 = append at key path.
// We replay the log into a per-request accumulator, then surface:
//   - the user turn (request.message.text)
//   - the assistant turn (response parts with no "kind" — the visible markdown;
//     "thinking" and tool-invocation parts are skipped)
//   - per-request token usage (result.usage.promptTokens / completionTokens)

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// chatRequest is one user→assistant exchange reconstructed from the delta log.
type chatRequest struct {
	timestampMillis  int64 // request.timestamp (epoch ms); 0 if unknown
	userText         string
	assistant        strings.Builder
	promptTokens     int64
	completionTokens int64
}

// rawChatRequest is the subset of a request object we decode from snapshots and
// kind=2 ["requests"] appends.
type rawChatRequest struct {
	Timestamp int64 `json:"timestamp"`
	Message   struct {
		Text string `json:"text"`
	} `json:"message"`
	Response []chatResponsePart `json:"response"`
	Result   *chatResult        `json:"result"`
}

type chatResult struct {
	Usage chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens     int64 `json:"promptTokens"`
	CompletionTokens int64 `json:"completionTokens"`
}

// chatResponsePart is one streamed chunk of an assistant reply. Plain text
// chunks have no "kind"; typed chunks ("thinking", "toolInvocationSerialized",
// …) we skip so they don't pollute the conversation snippet.
type chatResponsePart struct {
	Kind  string          `json:"kind"`
	Value json.RawMessage `json:"value"`
}

// appendVisibleText appends the human-readable assistant text from a response
// part to b. Only parts with an empty "kind" and a JSON-string value count.
func (p chatResponsePart) appendVisibleText(b *strings.Builder) {
	if p.Kind != "" {
		return
	}
	var s string
	if json.Unmarshal(p.Value, &s) == nil && s != "" {
		b.WriteString(s)
	}
}

// chatSession replays the delta log of a single chat-session JSONL.
type chatSession struct {
	requests []*chatRequest
	model    string // last-seen display model (provider prefix stripped)
}

func parseChatSession(path string) (*chatSession, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open chat session: %w", err)
	}
	defer f.Close()

	cs := &chatSession{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<24)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var cl chatLine
		if err := json.Unmarshal(line, &cl); err != nil {
			continue
		}
		cs.applyLine(cl)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan chat session: %w", err)
	}
	return cs, nil
}

func (cs *chatSession) applyLine(cl chatLine) {
	switch cl.Kind {
	case 0:
		// Snapshot: seed requests[] and the selected model.
		var snap struct {
			Requests   []rawChatRequest `json:"requests"`
			InputState struct {
				SelectedModel json.RawMessage `json:"selectedModel"`
			} `json:"inputState"`
		}
		if json.Unmarshal(cl.V, &snap) != nil {
			return
		}
		for _, r := range snap.Requests {
			cs.requests = append(cs.requests, newChatRequest(r))
		}
		if m := extractSelectedModel(snap.InputState.SelectedModel); m != "" {
			cs.model = displayModel(m)
		}
	case 1:
		if keyPathHasSuffix(cl.K, "selectedModel") {
			if m := extractSelectedModel(cl.V); m != "" {
				cs.model = displayModel(m)
			}
			return
		}
		// ["requests",N,"result"] — capture usage.
		if n, ok := requestIndex(cl.K, "result"); ok {
			var res chatResult
			if json.Unmarshal(cl.V, &res) == nil {
				if r := cs.req(n); r != nil {
					r.promptTokens = res.Usage.PromptTokens
					r.completionTokens = res.Usage.CompletionTokens
				}
			}
		}
	case 2:
		// ["requests"] — one or more new request objects appended.
		if keyPathHasSuffix(cl.K, "requests") {
			var batch []rawChatRequest
			if json.Unmarshal(cl.V, &batch) == nil {
				for _, r := range batch {
					cs.requests = append(cs.requests, newChatRequest(r))
				}
			}
			return
		}
		// ["requests",N,"response"] — streamed assistant chunks.
		if n, ok := requestIndex(cl.K, "response"); ok {
			var parts []chatResponsePart
			if json.Unmarshal(cl.V, &parts) == nil {
				if r := cs.req(n); r != nil {
					for _, p := range parts {
						p.appendVisibleText(&r.assistant)
					}
				}
			}
		}
	}
}

func newChatRequest(r rawChatRequest) *chatRequest {
	cr := &chatRequest{timestampMillis: r.Timestamp, userText: strings.TrimSpace(r.Message.Text)}
	for _, p := range r.Response {
		p.appendVisibleText(&cr.assistant)
	}
	if r.Result != nil {
		cr.promptTokens = r.Result.Usage.PromptTokens
		cr.completionTokens = r.Result.Usage.CompletionTokens
	}
	return cr
}

func (cs *chatSession) req(n int) *chatRequest {
	if n < 0 || n >= len(cs.requests) {
		return nil
	}
	return cs.requests[n]
}

// requestIndex returns N when k == ["requests", N, suffix].
func requestIndex(k json.RawMessage, suffix string) (int, bool) {
	if len(k) == 0 {
		return 0, false
	}
	var path []json.RawMessage
	if err := json.Unmarshal(k, &path); err != nil || len(path) < 3 {
		return 0, false
	}
	var head, tail string
	if json.Unmarshal(path[0], &head) != nil || head != "requests" {
		return 0, false
	}
	if json.Unmarshal(path[len(path)-1], &tail) != nil || tail != suffix {
		return 0, false
	}
	var n int
	if json.Unmarshal(path[1], &n) != nil {
		return 0, false
	}
	return n, true
}

// ReadChatSessionConversation returns up to maxTurns user/assistant turns from
// a chat-session JSONL, taking the last maxTurns. Each turn's text is capped at
// maxChars. Empty turns (no user text and no visible assistant text) are skipped.
func ReadChatSessionConversation(path string, maxTurns, maxChars int) ([]ConvTurn, error) {
	if path == "" {
		return nil, nil
	}
	cs, err := parseChatSession(path)
	if err != nil {
		return nil, err
	}
	var turns []ConvTurn
	cap := func(s string) string {
		s = strings.TrimSpace(s)
		if maxChars > 0 && len(s) > maxChars {
			return s[:maxChars] + "…"
		}
		return s
	}
	for _, r := range cs.requests {
		if u := cap(r.userText); u != "" {
			turns = append(turns, ConvTurn{Role: "user", Text: u})
		}
		if a := cap(r.assistant.String()); a != "" {
			turns = append(turns, ConvTurn{Role: "assistant", Text: a})
		}
	}
	if maxTurns > 0 && len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	return turns, nil
}

// ReadChatSessionUsage sums token usage across the chat session's requests,
// restricted to requests whose timestamp falls within [sinceNanos, untilNanos]
// (pass 0,0 to include every request). promptTokens maps to Input and
// completionTokens to Output; the chat JSONL exposes no cache-token split.
// Returns nil when the session has no usable token data in the window.
func ReadChatSessionUsage(path string, sinceNanos, untilNanos int64) (*TranscriptUsage, error) {
	if path == "" {
		return nil, nil
	}
	cs, err := parseChatSession(path)
	if err != nil {
		return nil, err
	}
	var u TranscriptUsage
	u.Model = cs.model
	var any bool
	for _, r := range cs.requests {
		if untilNanos > 0 && r.timestampMillis > 0 {
			reqNanos := r.timestampMillis * int64(1e6)
			if reqNanos < sinceNanos || reqNanos > untilNanos {
				continue
			}
		}
		if r.promptTokens == 0 && r.completionTokens == 0 {
			continue
		}
		u.InputTokens += r.promptTokens
		u.OutputTokens += r.completionTokens
		any = true
	}
	if !any {
		return nil, nil
	}
	return &u, nil
}
