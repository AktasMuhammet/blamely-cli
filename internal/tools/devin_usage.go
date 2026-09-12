package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/blamely/blamely/internal/config"
)

// Devin CLI writes one ATIF transcript per session under
// <devin config dir>/cli/transcripts/<session_id>.json, keyed by the same
// human-readable session id ("axiomatic-jeans") its hook payload carries. The
// file is rewritten after every agent step, so it is already on disk — with the
// current model — by the time a PostToolUse hook fires.
//
// The shape (ATIF-v1.x, trimmed to what we read):
//
//	{
//	  "session_id": "axiomatic-jeans",
//	  "agent": {"name": "devin", "model_name": "SWE-2 High"},
//	  "steps": [{
//	    "source": "agent",
//	    "model_name": "swe-2-high",
//	    "metrics": {"prompt_tokens": 11819, "completion_tokens": 141, "cached_tokens": 4620},
//	    "extra": {"generation_model": "swe-2-high"}
//	  }],
//	  "final_metrics": {...}
//	}
//
// Two model spellings appear: agent.model_name is the display label ("SWE-2
// High") and each step carries the resolved model uid ("swe-2-high" — the same
// value Devin logs as resolved_model_uid). The uid is preferred because every
// other tool records a machine identifier (claude-opus-4-8, gpt-5-codex), and
// because it is per step, so a mid-session model switch is attributed to the
// lines it actually produced. The display label is a fallback for a transcript
// with no agent step yet.
type devinTranscript struct {
	Agent struct {
		ModelName string `json:"model_name"`
	} `json:"agent"`
	Steps []devinTranscriptStep `json:"steps"`
}

type devinTranscriptStep struct {
	Source    string `json:"source"`
	ModelName string `json:"model_name"`
	Metrics   *struct {
		PromptTokens     int64 `json:"prompt_tokens"`
		CompletionTokens int64 `json:"completion_tokens"`
		CachedTokens     int64 `json:"cached_tokens"`
	} `json:"metrics"`
	Extra struct {
		GenerationModel string `json:"generation_model"`
	} `json:"extra"`
}

// devinTranscriptsDir is indirected so tests can point the reader at a temp dir.
var devinTranscriptsDir = config.DevinTranscriptsDir

// ReadDevinTranscriptUsage returns the latest model + token usage for a Devin
// CLI session, or nil if the transcript is missing or carries neither.
// sessionID is the hook payload's session_id (the transcript's basename).
func ReadDevinTranscriptUsage(sessionID string) (*TranscriptUsage, error) {
	if sessionID == "" {
		return nil, nil
	}
	base, err := devinTranscriptsDir()
	if err != nil {
		return nil, err
	}
	return readDevinTranscriptUsageFrom(base, sessionID)
}

func readDevinTranscriptUsageFrom(baseDir, sessionID string) (*TranscriptUsage, error) {
	// The id comes from an external process; refuse anything that is not a
	// plain file name so it cannot escape the transcripts directory.
	if baseDir == "" || sessionID == "" || filepath.Base(sessionID) != sessionID || sessionID == "." || sessionID == ".." {
		return nil, nil
	}
	f, err := os.Open(filepath.Join(baseDir, sessionID+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open devin transcript: %w", err)
	}
	defer f.Close()
	return scanDevinTranscriptUsage(f)
}

// devinModelFallback pulls the last per-step model uid out of a transcript that
// does not parse as a whole — Devin rewrites the file in place after each step,
// so a hook can race a partial write. Losing the token counts for one edit is
// acceptable; losing the model name is what users notice.
var devinModelFallback = regexp.MustCompile(`"(?:generation_model|model_name)"\s*:\s*"([^"]+)"`)

// scanDevinTranscriptUsage takes the model and metrics of the most recent agent
// step. prompt_tokens is the full context sent for that inference (cached part
// included), completion_tokens the generated output, cached_tokens the portion
// of the prompt served from cache — mapped onto Input/Output/CacheRead. Devin
// reports no cache-write figure.
func scanDevinTranscriptUsage(r io.Reader) (*TranscriptUsage, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read devin transcript: %w", err)
	}
	var t devinTranscript
	if err := json.Unmarshal(data, &t); err != nil {
		if m := devinModelFallback.FindAllSubmatch(data, -1); len(m) > 0 {
			return &TranscriptUsage{Model: string(m[len(m)-1][1])}, nil
		}
		return nil, nil
	}

	u := &TranscriptUsage{}
	for i := len(t.Steps) - 1; i >= 0; i-- {
		s := t.Steps[i]
		if s.Source != "" && s.Source != "agent" {
			continue
		}
		if u.Model == "" {
			if s.ModelName != "" {
				u.Model = s.ModelName
			} else if s.Extra.GenerationModel != "" {
				u.Model = s.Extra.GenerationModel
			}
		}
		if s.Metrics != nil && u.InputTokens == 0 && u.OutputTokens == 0 {
			u.InputTokens = s.Metrics.PromptTokens
			u.OutputTokens = s.Metrics.CompletionTokens
			u.CacheReadTokens = s.Metrics.CachedTokens
		}
		if u.Model != "" && (u.InputTokens > 0 || u.OutputTokens > 0) {
			break
		}
	}
	if u.Model == "" {
		u.Model = t.Agent.ModelName
	}
	if u.Model == "" && u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 {
		return nil, nil
	}
	return u, nil
}
