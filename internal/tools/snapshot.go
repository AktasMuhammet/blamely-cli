package tools

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/daemon"
)

// fetchSnapshot retrieves the daemon's cached "before" content for repo/file —
// the file's content as of the last recorded edit, or its content at HEAD if
// this is the file's first recorded edit (see (*daemon.Server).snapshot).
// Used by whole-file-overwrite tools (Write/write_file) and Copilot's
// textEditGroup edits, which carry no "before" content of their own. ok=false
// on any error (daemon down, no cached snapshot and no HEAD) — callers should
// skip removed-line attribution in that case, same as before this feature
// existed.
func fetchSnapshot(repoPath, filePath string) (content string, ok bool) {
	q := url.Values{"repo": {repoPath}, "file": {filePath}}

	var client *http.Client
	var reqURL string
	if sock, serr := daemon.ReadSocket(); serr == nil {
		client = daemon.UnixHTTPClient(sock)
		reqURL = "http://unix/snapshot?" + q.Encode()
	} else if port, perr := daemon.ReadPort(); perr == nil {
		client = &http.Client{Timeout: 2 * time.Second}
		reqURL = fmt.Sprintf("http://127.0.0.1:%d/snapshot?%s", port, q.Encode())
	} else {
		return "", false
	}

	resp, err := client.Get(reqURL)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", false
	}

	var out struct {
		Content string `json:"content"`
		Found   bool   `json:"found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false
	}
	return out.Content, out.Found
}

// removedLinesForTextEditRange computes removed-line hashes for an editor
// text-edit that replaces lines [startLine, endLine) of snapshot (1-based,
// VS Code's half-open range convention: endLine is exclusive) with newText.
// Used by Copilot's textEditGroup, whose edits carry only the new text and a
// line range, not the replaced text. Returns nil if snapshot is empty or the
// range is out of bounds.
func removedLinesForTextEditRange(snapshot string, startLine, endLine int, newText string) []DeletedLineHash {
	if snapshot == "" || startLine < 1 || endLine <= startLine {
		return nil
	}
	lines := strings.Split(snapshot, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	if startLine > len(lines) {
		return nil
	}
	last := endLine - 1
	if last > len(lines) {
		last = len(lines)
	}
	old := strings.Join(lines[startLine-1:last], "\n")
	return RemovedLineHashes(old, newText)
}
