package tools

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
)

// geminiHookPayload is the JSON Gemini CLI sends on stdin for BeforeTool /
// AfterTool hooks (base fields are shared across events).
type geminiHookPayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
}

// RecordGeminiFromStdin handles `blamely record gemini` (Gemini CLI AfterTool).
func RecordGeminiFromStdin(r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	var p geminiHookPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		log.Printf("blamely record gemini: parse hook payload: %v", err)
		return nil
	}

	filePath, ranges, suggested, removed, newFullContent := extractGeminiRanges(p)
	if filePath == "" {
		return nil
	}

	resolved := resolveSymlinks(filePath)
	repoPath, _ := gitutil.RepoID(resolved)
	if repoPath == "" && p.Cwd != "" {
		repoPath, _ = gitutil.RepoID(resolveSymlinks(p.Cwd))
	}
	wt, _ := gitutil.Toplevel(resolved)
	rel := resolved
	if wt != "" {
		if r, err := filepath.Rel(wt, resolved); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}

	// write_file overwrites the whole file with no "before" content of its
	// own — fetch the daemon's cached snapshot (the file's content as of its
	// last recorded edit) so we can still detect lines this write removed.
	if newFullContent != nil {
		if snapshot, ok := fetchSnapshot(repoPath, rel); ok {
			removed = append(removed, RemovedLineHashes(snapshot, *newFullContent)...)
		}
	}

	genType := ReadTranscriptGenType(p.TranscriptPath)
	if genType == "" {
		genType = "cli"
	}

	payload := daemon.EditPayload{
		Tool:           "gemini",
		Confidence:     "high",
		GenType:        genType,
		RepoPath:       repoPath,
		FilePath:       rel,
		SuggestedLines: suggested,
		Lines:          toDaemonRanges(ranges),
		RemovedLines:   toDaemonRemovedLines(removed),
		RawMeta: fmt.Sprintf(`{"session_id":%q,"tool":%q,"transcript_path":%q,"source":"gemini_hook"}`,
			p.SessionID, p.ToolName, p.TranscriptPath),
	}
	applyHookUsage(&payload, hookUsageOptions{
		transcriptPath: p.TranscriptPath,
		sessionID:      p.SessionID,
		tool:           "gemini",
	})
	return postToDaemon(payload)
}

func extractGeminiRanges(p geminiHookPayload) (string, []LineRange, int64, []DeletedLineHash, *string) {
	switch p.ToolName {
	case "write_file":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.FilePath == "" {
			return "", nil, 0, nil, nil
		}
		suggested := int64(countLines(in.Content))
		lr, _ := LineRangeForWholeFile(in.FilePath)
		if lr == nil {
			return in.FilePath, nil, suggested, nil, &in.Content
		}
		return in.FilePath, lr, suggested, nil, &in.Content

	case "replace":
		var in struct {
			FilePath  string `json:"file_path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		if err := json.Unmarshal(p.ToolInput, &in); err != nil || in.FilePath == "" {
			return "", nil, 0, nil, nil
		}
		removed := RemovedLineHashes(in.OldString, in.NewString)
		if strings.TrimSpace(in.NewString) == "" && in.OldString != "" {
			return in.FilePath, nil, int64(countLines(in.OldString)), removed, nil
		}
		lr, _ := LocateNewString(in.FilePath, in.NewString)
		if lr == nil {
			return in.FilePath, nil, CountAddedLines(in.OldString, in.NewString), removed, nil
		}
		ranges, suggested := narrowToChangedLines(in.OldString, in.NewString, *lr)
		return in.FilePath, ranges, suggested, removed, nil
	}

	// Generic: file_path / path + content / new_string / code
	var generic struct {
		FilePath  string `json:"file_path"`
		Path      string `json:"path"`
		NewString string `json:"new_string"`
		Content   string `json:"content"`
		Code      string `json:"code"`
	}
	if err := json.Unmarshal(p.ToolInput, &generic); err != nil {
		return "", nil, 0, nil, nil
	}
	fp := generic.FilePath
	if fp == "" {
		fp = generic.Path
	}
	if fp == "" {
		return "", nil, 0, nil, nil
	}
	body := generic.NewString
	if body == "" {
		body = generic.Content
	}
	if body == "" {
		body = generic.Code
	}
	suggested := int64(countLines(body))
	if body != "" {
		if lr, _ := LocateNewString(fp, body); lr != nil {
			return fp, []LineRange{*lr}, suggested, nil, nil
		}
	}
	return fp, nil, suggested, nil, nil
}

type geminiUsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
	TotalTokenCount         int64 `json:"totalTokenCount"`
}

// ReadGeminiTranscriptUsage scans a Gemini CLI session transcript for the latest
// usageMetadata block (promptTokenCount / candidatesTokenCount).
func ReadGeminiTranscriptUsage(path string) (*TranscriptUsage, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open gemini transcript: %w", err)
	}
	defer f.Close()

	var last *TranscriptUsage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var meta struct {
			UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
			LLMResponse   *struct {
				UsageMetadata *geminiUsageMetadata `json:"usageMetadata"`
			} `json:"llm_response"`
		}
		if json.Unmarshal(line, &meta) != nil {
			continue
		}
		u := meta.UsageMetadata
		if u == nil && meta.LLMResponse != nil {
			u = meta.LLMResponse.UsageMetadata
		}
		if u == nil {
			continue
		}
		in := u.PromptTokenCount
		out := u.CandidatesTokenCount
		if in == 0 && out == 0 && u.TotalTokenCount > 0 {
			// Some transcripts only expose a total; treat it as output-ish signal.
			out = u.TotalTokenCount
		}
		if in == 0 && out == 0 {
			continue
		}
		last = &TranscriptUsage{
			InputTokens:     in,
			OutputTokens:    out,
			CacheReadTokens: u.CachedContentTokenCount,
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan gemini transcript: %w", err)
	}
	return last, nil
}
