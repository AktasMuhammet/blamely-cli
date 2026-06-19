package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/store"
)

// The Copilot CLI records one events.jsonl per session under
// ~/.copilot/session-state/<session-id>/. Unlike the Claude/Cursor transcript
// format (role/message.usage), it uses typed events:
//
//	{"type":"assistant.message","data":{"outputTokens":22,"model":"...", ...}}
//	{"type":"tool.execution_complete","data":{"model":"claude-haiku-4.5", ...}}
//
// so scanTranscriptUsage (which expects message.usage) finds nothing here. This
// reader extracts the per-session model + output tokens the CLI hook otherwise
// can't supply. Input/cache token counts aren't present in this log, so only
// Model and OutputTokens are populated.
type copilotCliEvent struct {
	Type string `json:"type"`
	Data struct {
		Model        string `json:"model"`
		OutputTokens int64  `json:"outputTokens"`
	} `json:"data"`
}

// ReadCopilotCliUsage returns the latest model + output-token usage for a Copilot
// CLI session, or nil if the session log is missing/empty. sessionID is the hook
// payload's session id (the session-state subdirectory name).
func ReadCopilotCliUsage(sessionID string) (*TranscriptUsage, error) {
	if sessionID == "" {
		return nil, nil
	}
	base, err := config.CopilotSessionStateDir()
	if err != nil {
		return nil, err
	}
	return readCopilotCliUsageFrom(base, sessionID)
}

func readCopilotCliUsageFrom(baseDir, sessionID string) (*TranscriptUsage, error) {
	if baseDir == "" || sessionID == "" {
		return nil, nil
	}
	f, err := os.Open(filepath.Join(baseDir, sessionID, "events.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open copilot cli events: %w", err)
	}
	defer f.Close()
	return scanCopilotCliUsage(f)
}

// scanCopilotCliUsage takes the most recent non-empty model (from either an
// assistant.message or a tool.execution_complete) and the most recent positive
// output-token count. Returns nil if neither is found.
func scanCopilotCliUsage(r io.Reader) (*TranscriptUsage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	var model string
	var outputTokens int64
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e copilotCliEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch e.Type {
		case "assistant.message":
			if e.Data.Model != "" {
				model = e.Data.Model
			}
			if e.Data.OutputTokens > 0 {
				outputTokens = e.Data.OutputTokens
			}
		case "tool.execution_complete":
			if e.Data.Model != "" {
				model = e.Data.Model
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan copilot cli events: %w", err)
	}
	if model == "" && outputTokens == 0 {
		return nil, nil
	}
	return &TranscriptUsage{Model: model, OutputTokens: outputTokens}, nil
}

// copilotShutdownEvent matches the Copilot CLI's terminal session.shutdown event,
// whose modelMetrics carry the session's CUMULATIVE per-model token spend:
//
//	{"type":"session.shutdown","data":{"modelMetrics":{
//	   "gpt-5-mini":{"usage":{"inputTokens":..,"outputTokens":..,
//	     "cacheReadTokens":..,"cacheWriteTokens":..,"reasoningTokens":..}}}}}
type copilotShutdownEvent struct {
	Type string `json:"type"`
	Data struct {
		ModelMetrics map[string]struct {
			Usage struct {
				InputTokens      int64 `json:"inputTokens"`
				OutputTokens     int64 `json:"outputTokens"`
				CacheReadTokens  int64 `json:"cacheReadTokens"`
				CacheWriteTokens int64 `json:"cacheWriteTokens"`
				ReasoningTokens  int64 `json:"reasoningTokens"`
			} `json:"usage"`
		} `json:"modelMetrics"`
	} `json:"data"`
}

// scanCopilotCliSessionUsage returns the per-model cumulative usage from the last
// session.shutdown in a Copilot CLI events log, keyed by model. nil if the
// session hasn't ended (no shutdown event) yet.
func scanCopilotCliSessionUsage(r io.Reader) (map[string]store.SessionUsage, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<22)
	var out map[string]store.SessionUsage
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e copilotShutdownEvent
		if json.Unmarshal(line, &e) != nil || e.Type != "session.shutdown" {
			continue
		}
		out = make(map[string]store.SessionUsage, len(e.Data.ModelMetrics))
		for model, m := range e.Data.ModelMetrics {
			out[model] = store.SessionUsage{
				InputTokens:      m.Usage.InputTokens,
				OutputTokens:     m.Usage.OutputTokens,
				CacheReadTokens:  m.Usage.CacheReadTokens,
				CacheWriteTokens: m.Usage.CacheWriteTokens,
				ReasoningTokens:  m.Usage.ReasoningTokens,
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan copilot cli session usage: %w", err)
	}
	return out, nil
}
