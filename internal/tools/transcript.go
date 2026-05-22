package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
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

// transcript JSONL entries look like:
// {"type":"assistant","message":{"model":"claude-opus-4-7","usage":{...}}, ...}
// We want the last `assistant` entry with a populated `message.usage`.
type transcriptEntry struct {
	Type    string `json:"type"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
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
		if e.Type != "assistant" || e.Message.Model == "" {
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
