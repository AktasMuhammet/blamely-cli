package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// TranscriptUsage extracts model + token usage from the last assistant turn
// of a Claude Code transcript JSONL.
type TranscriptUsage struct {
	Model            string
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

// ConvTurn is one user or assistant turn from a Claude Code transcript.
type ConvTurn struct {
	Role string // "user" or "assistant"
	Text string // first text content block, truncated
}

// transcriptEntry covers two transcript JSONL formats:
//
//   Claude Code:  {"type":"user"|"assistant", "message":{...}}
//   Cursor:       {"role":"user"|"assistant", "message":{...}}
//
// Both are emitted as JSONL; we merge role/type so callers see a unified view.
type transcriptEntry struct {
	// Claude Code uses "type"; Cursor uses "role" — we read both.
	Type string `json:"type"`
	Role string `json:"role"`

	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

// role returns the normalised "user" or "assistant" string for either format.
func (e *transcriptEntry) role() string {
	if e.Type != "" {
		return e.Type
	}
	return e.Role
}

// ReadTranscriptConversation returns up to maxTurns user/assistant turns from
// the transcript JSONL, taking the last maxTurns turns. Each turn's text is
// capped at maxChars characters. Tool-use and system entries are skipped.
func ReadTranscriptConversation(path string, maxTurns, maxChars int) ([]ConvTurn, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	return scanConversation(f, maxTurns, maxChars)
}

func scanConversation(r io.Reader, maxTurns, maxChars int) ([]ConvTurn, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	var turns []ConvTurn
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		role := e.role()
		if role != "user" && role != "assistant" {
			continue
		}
		// Extract the first text block from content.
		text := ""
		for _, c := range e.Message.Content {
			if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
				text = c.Text
				break
			}
		}
		if text == "" {
			continue
		}
		if maxChars > 0 && len(text) > maxChars {
			text = text[:maxChars] + "…"
		}
		turns = append(turns, ConvTurn{Role: role, Text: strings.TrimSpace(text)})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	// Return only the last maxTurns turns.
	if maxTurns > 0 && len(turns) > maxTurns {
		turns = turns[len(turns)-maxTurns:]
	}
	return turns, nil
}

func ReadTranscriptUsage(path string) (*TranscriptUsage, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer f.Close()
	return scanTranscriptUsage(f)
}

func scanTranscriptUsage(r io.Reader) (*TranscriptUsage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	var last *TranscriptUsage
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		if e.role() != "assistant" || e.Message.Model == "" {
			continue
		}
		if e.Message.Usage.InputTokens == 0 && e.Message.Usage.OutputTokens == 0 {
			continue
		}
		last = &TranscriptUsage{
			Model:            e.Message.Model,
			InputTokens:      e.Message.Usage.InputTokens,
			OutputTokens:     e.Message.Usage.OutputTokens,
			CacheReadTokens:  e.Message.Usage.CacheReadInputTokens,
			CacheWriteTokens: e.Message.Usage.CacheCreationInputTokens,
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan transcript: %w", err)
	}
	return last, nil
}
