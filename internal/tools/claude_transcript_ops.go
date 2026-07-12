package tools

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/config"
)

// ClaudeCommitFileOps scans the Claude Code transcripts for repoRoot and returns
// the repo-relative file targets the assistant CREATED/WROTE and DELETED via
// tool calls whose timestamp falls in (sinceNanos, untilNanos].
//
// It exists to attribute the atomic "cat > f && git commit" / "rm f && git
// commit" pattern Claude Desktop's local "code" mode uses: those operations
// commit in one bash command, so the post-commit note is written before the
// PostToolUse hook can record the edit (and afterwards the working tree is
// clean) — but the transcript still records exactly which files the AI touched.
// blamely's commit-time attribution uses this to credit those files to the AI.
func ClaudeCommitFileOps(repoRoot string, sinceNanos, untilNanos int64) (written, deleted []string) {
	wset, dset := map[string]bool{}, map[string]bool{}
	// Scan the repo's transcript dir under EVERY Claude config location (default +
	// custom), so a corp CLAUDE_CONFIG_DIR is covered alongside ~/.claude.
	for _, dir := range claudeProjectDirs(repoRoot) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			// A transcript last written before the window opened can't hold ops in it.
			if info, err := e.Info(); err == nil && info.ModTime().UnixNano() < sinceNanos {
				continue
			}
			scanTranscriptOps(filepath.Join(dir, e.Name()), sinceNanos, untilNanos, wset, dset)
		}
	}
	return mapKeys(wset), mapKeys(dset)
}

// MatchesFileOp reports whether a committed repo-relative path corresponds to
// one of the transcript file targets, matching on exact path, basename, or a
// shell glob (so `rm hello*.html` covers hello.html).
func MatchesFileOp(rel string, targets []string) bool {
	base := baseAnySep(rel)
	for _, t := range targets {
		t = strings.TrimPrefix(t, "./")
		if t == rel || t == base || baseAnySep(t) == base {
			return true
		}
		if ok, _ := filepath.Match(t, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(baseAnySep(t), base); ok {
			return true
		}
	}
	return false
}

// claudeProjectDirs returns the repo's transcript dir under EVERY Claude config
// location in the union (~/.claude, $CLAUDE_CONFIG_DIR, and configured extras), so
// a corp/custom config dir is scanned alongside the default.
func claudeProjectDirs(repoRoot string) []string {
	proj := strings.ReplaceAll(filepath.ToSlash(repoRoot), "/", "-")
	bases := config.ClaudeProjectsDirs()
	out := make([]string, 0, len(bases))
	for _, b := range bases {
		out = append(out, filepath.Join(b, proj))
	}
	return out
}

type tsOpEntry struct {
	Timestamp string          `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

type tsOpToolUse struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Input struct {
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
	} `json:"input"`
}

func scanTranscriptOps(path string, since, until int64, wset, dset map[string]bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16<<20)
	for sc.Scan() {
		var ent tsOpEntry
		if json.Unmarshal(sc.Bytes(), &ent) != nil {
			continue
		}
		ts := parseTranscriptTime(ent.Timestamp)
		if ts == 0 || ts <= since || ts > until {
			continue
		}
		// message.content is an array for assistant tool calls (and a bare string
		// for plain text — which just fails to unmarshal and is skipped).
		var msg struct {
			Content []tsOpToolUse `json:"content"`
		}
		if json.Unmarshal(ent.Message, &msg) != nil {
			continue
		}
		for _, u := range msg.Content {
			if u.Type != "tool_use" {
				continue
			}
			switch u.Name {
			case "Write":
				// Whole-file writes only. Edit/MultiEdit are PARTIAL — the
				// PostToolUse hook records those by content, so they must not
				// trigger a whole-file flip here.
				if u.Input.FilePath != "" {
					wset[cleanTarget(u.Input.FilePath)] = true
				}
			case "Bash":
				for _, t := range bashRedirectTargets(u.Input.Command) {
					wset[t] = true
				}
				for _, t := range shellDeleteTargets(u.Input.Command) {
					dset[t] = true
				}
			}
		}
	}
}

var (
	redirectRe     = regexp.MustCompile("(?:[^0-9&]|^)>>?\\s*([^\\s&|;<>'\"`]+)")
	rmArgsRe       = regexp.MustCompile(`(?:^|\s)(?:git\s+)?rm\s+([^&|;\n]+)`)
	heredocStartRe = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
)

// stripHeredocs removes heredoc bodies (`<<EOF … EOF`) from a command so file
// content written via `cat <<EOF > f` isn't mis-parsed as redirect targets — an
// `<html>` line would otherwise look like a `>` redirect. The `> f` on the
// heredoc's opening line is preserved.
func stripHeredocs(cmd string) string {
	for i := 0; i < 20; i++ {
		m := heredocStartRe.FindStringSubmatchIndex(cmd)
		if m == nil {
			break
		}
		delim := cmd[m[2]:m[3]]
		nl := strings.IndexByte(cmd[m[1]:], '\n')
		if nl < 0 {
			cmd = cmd[:m[0]]
			break
		}
		bodyStart := m[1] + nl + 1
		closeRe := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(delim) + `\s*$`)
		rest := ""
		if loc := closeRe.FindStringIndex(cmd[bodyStart:]); loc != nil {
			rest = cmd[bodyStart+loc[1]:]
		}
		cmd = cmd[:m[0]] + cmd[m[1]:bodyStart-1] + " " + rest
	}
	return cmd
}

// bashRedirectTargets returns the files a command writes via `>`/`>>` redirects,
// skipping device/stream targets like /dev/null and 2>&1.
func bashRedirectTargets(cmd string) []string {
	cmd = stripHeredocs(cmd)
	var out []string
	for _, m := range redirectRe.FindAllStringSubmatch(cmd, -1) {
		t := cleanTarget(m[1])
		if t == "" || isShellAbsPath(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// isShellAbsPath reports whether a shell token is an absolute path — Unix
// ("/...", which also covers /dev/null-style stream targets) or Windows
// drive-letter ("C:\..." / "c:/..."). Deliberately not filepath.IsAbs:
// transcripts may be parsed on a different OS than they were written on, and
// the check must behave the same everywhere.
func isShellAbsPath(t string) bool {
	if strings.HasPrefix(t, "/") {
		return true
	}
	return len(t) > 2 && t[1] == ':' && (t[2] == '\\' || t[2] == '/') &&
		('a' <= t[0] && t[0] <= 'z' || 'A' <= t[0] && t[0] <= 'Z')
}

// winDeleteRe matches Windows file-deletion verbs — cmd `del`/`erase` and
// PowerShell `Remove-Item` — so an agent shell deletion on Windows is caught
// alongside Unix `rm`.
var winDeleteRe = regexp.MustCompile(`(?i)(?:^|[\s&|;(])(?:del|erase|remove-item)\s+([^&|;\n]+)`)

// shellDeleteTargets returns the files a shell command removes, covering Unix
// `rm`/`git rm` (bashRmTargets) and the Windows `del`/`erase`/`Remove-Item`
// verbs. Used to attribute deletions an AI tool performs through its shell
// (e.g. Codex's exec_command) rather than a structured file-op.
func shellDeleteTargets(cmd string) []string {
	out := bashRmTargets(cmd)
	for _, m := range winDeleteRe.FindAllStringSubmatch(cmd, -1) {
		// Windows: backslash is a PATH SEPARATOR, not an escape (`C:\proj\x.html`).
		for _, tok := range splitShellFields(m[1], false) {
			tok = cleanTarget(tok)
			// The "/" prefix filters cmd-style switches (`del /f /q x.html`),
			// NOT absolute paths — Windows absolute targets are kept on
			// purpose (see TestShellDeleteTargets).
			if tok == "" || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, "/") {
				continue
			}
			out = append(out, tok)
		}
	}
	return out
}

// bashRmTargets returns the files an `rm`/`git rm` deletes (every non-flag
// argument up to the next shell separator).
func bashRmTargets(cmd string) []string {
	var out []string
	for _, m := range rmArgsRe.FindAllStringSubmatch(cmd, -1) {
		for _, tok := range splitShellFields(m[1], true) {
			tok = cleanTarget(tok)
			if tok == "" || strings.HasPrefix(tok, "-") {
				continue
			}
			out = append(out, tok)
		}
	}
	return out
}

func cleanTarget(s string) string {
	return strings.Trim(strings.TrimSpace(s), `'"`)
}

// splitShellFields splits a command-argument string into shell-style fields,
// keeping single/double-quoted spans (and backslash-escaped spaces) together so
// a path containing spaces — e.g. `'login page.html'` — stays ONE token instead
// of being shredded by whitespace into `login` and `page.html` (which match no
// file at HEAD, so the deletion silently falls back to Human). Quotes and
// escapes are removed from the returned tokens. This is a minimal tokenizer, not
// a full shell parser: it covers the quoting forms tools emit for file paths.
//
// bashEscapes controls UNQUOTED backslash handling: true for POSIX shells (so
// `login\ page.html` is one token), false for Windows del/Remove-Item where a
// backslash is a path separator (`C:\proj\x.html` must stay literal).
func splitShellFields(s string, bashEscapes bool) []string {
	var out []string
	var cur strings.Builder
	inSingle, inDouble, hasTok := false, false, false
	flush := func() {
		if hasTok {
			out = append(out, cur.String())
			cur.Reset()
			hasTok = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inSingle:
			if c == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(c)
			}
		case inDouble:
			if c == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
				i++
				cur.WriteByte(s[i])
			} else if c == '"' {
				inDouble = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inSingle, hasTok = true, true
		case c == '"':
			inDouble, hasTok = true, true
		case bashEscapes && c == '\\' && i+1 < len(s) && shellEscapable(s[i+1]):
			i++
			cur.WriteByte(s[i])
			hasTok = true
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
			hasTok = true
		}
	}
	flush()
	return out
}

// shellEscapable reports whether a backslash preceding b is a POSIX shell escape
// worth honouring (drop the backslash, keep b literal). We deliberately do NOT
// treat a backslash before an ordinary character as an escape: on Windows an
// `rm`/`del` target is an absolute path like `C:\proj\register.html`, and
// consuming those separators would merge it into one token
// (`C:projregister.html`) whose basename no longer matches the deleted file —
// so the deletion silently falls back to Human. Only genuinely shell-meaningful
// escapes (an escaped space in a path, an escaped quote or metacharacter) are
// honoured; a backslash before a path character is kept literal.
func shellEscapable(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\r', '\'', '"', '\\', '&', '|', ';', '$', '(', ')', '`', '*', '?':
		return true
	}
	return false
}

// baseAnySep is filepath.Base that splits on BOTH separators regardless of host
// OS, so a Windows-style target (`C:\proj\x.html`) yields `x.html` even when the
// matching runs on a non-Windows host.
func baseAnySep(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func parseTranscriptTime(s string) int64 {
	if s == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UnixNano()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixNano()
	}
	return 0
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
