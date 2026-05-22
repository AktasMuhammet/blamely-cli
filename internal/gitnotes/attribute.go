package gitnotes

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

const NotesRef = "refs/notes/blamely"

// Note is the JSON payload written to refs/notes/blamely for each commit.
type Note struct {
	Schema int    `json:"schema"`
	Commit string `json:"commit"`
	// Branch is the currently-checked-out branch when the commit was made,
	// or "" if HEAD was detached. Useful when the same commit lands on
	// multiple branches (the note is attached to the commit, not the branch).
	Branch string `json:"branch,omitempty"`
	// Message is the full commit message (subject + body) as recorded by git.
	Message string `json:"message,omitempty"`
	// CodingTimeNanos is the wall-clock time from the earliest edit observed
	// for this repo (up to the commit) to the commit timestamp. Zero if no
	// edits were observed in the lookback window (default 8h, see
	// store.SessionDurationNanos).
	CodingTimeNanos int64           `json:"coding_time_nanos,omitempty"`
	Totals          Totals          `json:"totals"`
	ByTool          map[string]Tool `json:"by_tool"`
	ByGenType       ByGenType       `json:"by_gen_type"`
	Files           []FileEntry     `json:"files"`
	GeneratedBy     string          `json:"generated_by"`
}

// ByGenType shows how many lines in the commit were produced by each
// generation method: a chat/panel session, a CLI command, or inline completion.
type ByGenType struct {
	Chat       int `json:"chat"`
	CLI        int `json:"cli"`
	Completion int `json:"completion"`
	Unknown    int `json:"unknown,omitempty"`
}

type Totals struct {
	AILines      int     `json:"ai_lines"`
	HumanLines   int     `json:"human_lines"`
	DeletedLines int     `json:"deleted_lines"`
	Files        int     `json:"files"`
	Tokens       *Tokens `json:"tokens,omitempty"`
	// Models is a per-model line-count rollup. Keys are concrete model
	// identifiers (e.g. "claude-opus-4-7", "gpt-4o"), not provider names.
	// Lines whose source AI didn't expose a model name aren't counted here.
	Models map[string]int `json:"models,omitempty"`
}

type Tool struct {
	Lines int `json:"lines"`
	// SuggestedLines and AcceptedLines are only set for AI tools — they
	// describe the model's original proposal vs. what the user kept. They
	// don't apply to human edits.
	SuggestedLines int64   `json:"suggested_lines,omitempty"`
	AcceptedLines  int     `json:"accepted_lines,omitempty"`
	Model          *string `json:"model,omitempty"`
	Tokens         *Tokens `json:"tokens,omitempty"`
}

type Tokens struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cache_read"`
	CacheWrite int64 `json:"cache_write"`
}

type FileEntry struct {
	Path string `json:"path"`
	// Type is the file-level change kind: ADDED, DELETED, MODIFIED, RENAMED,
	// or COPIED. Surfaced so consumers can render per-file status (e.g. a
	// "+ added" / "- deleted" tag) without re-parsing the diff.
	Type    string      `json:"type,omitempty"`
	// RenamedFrom (resp. CopiedFrom) is set for RENAMED / COPIED files and
	// names the source pre-commit path.
	RenamedFrom string      `json:"renamed_from,omitempty"`
	CopiedFrom  string      `json:"copied_from,omitempty"`
	Added       int         `json:"added"`
	Deleted     int         `json:"deleted"`
	Lines       []LineEntry `json:"lines"`
}

// LineEntry is one line of the commit's attribution. There is exactly one
// LineEntry per added or deleted line (no range collapsing) so downstream
// tools can render or filter at line granularity.
type LineEntry struct {
	Line int    `json:"line"` // post-image for adds, pre-image for deletes
	Type string `json:"type"` // "add" | "delete"
	// AuthorType is the binary AI-or-human classification. "AI" for any AI
	// tool (claude/cursor/codex/copilot/gemini), "Human" for typed-by-user
	// lines, "" for deletions (we don't attribute who deleted what).
	AuthorType string  `json:"author_type,omitempty"`
	Tool       string  `json:"tool"` // attributed tool; "" for deletes (untracked)
	Model      *string `json:"model,omitempty"`
	GenType    *string `json:"gen_type,omitempty"`
	Tokens     *Tokens `json:"tokens,omitempty"`
	EditID     *int64  `json:"edit_id,omitempty"`
}

// AttributeAndWrite is the main entry point invoked by the post-commit hook.
// It returns the freshly-written Note so callers (like cmd/blamely) can render
// a summary bar to stdout.
//
// repoPath comes from the hook as `git rev-parse --show-toplevel` (the
// WORKTREE root). We pass it to git for diff/notes operations. For DB
// lookups we use the canonical repo identity from `git rev-parse
// --git-common-dir`, so worktrees of the same repo share attribution.
func AttributeAndWrite(repoPath, sha string) (*Note, error) {
	if wt, ok := gitutil.Toplevel(repoPath); ok {
		repoPath = wt
	}
	repoID, _ := gitutil.RepoID(repoPath)
	if repoID == "" {
		repoID = repoPath
	}

	change, err := DiffCommit(repoPath, sha)
	if err != nil {
		return nil, fmt.Errorf("diff commit: %w", err)
	}
	commitNanos, err := CommitTimestampNanos(repoPath, sha)
	if err != nil {
		return nil, err
	}
	db, err := store.Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	note, err := buildNote(db, repoID, sha, commitNanos, change.Added, change.Deleted, change.Renames, change.FileChanges)
	if err != nil {
		return nil, err
	}
	// Branch + commit message are commit-scoped metadata; capture from git
	// while we still have the working tree handy.
	note.Branch = BranchName(repoPath)
	note.Message = CommitMessage(repoPath, sha)
	// Coding time: earliest observed edit (within an 8h lookback) → commit
	// timestamp. Falls back to 0 if no edits were recorded for this repo.
	note.CodingTimeNanos = db.SessionDurationNanos(repoID, commitNanos)
	// CopiedFrom is set per-file from change.Copies (the rename half is
	// handled inside flushFile via the renames map).
	for i := range note.Files {
		if from, ok := change.Copies[note.Files[i].Path]; ok {
			note.Files[i].CopiedFrom = from
		}
	}
	body, err := json.MarshalIndent(note, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal note: %w", err)
	}
	if err := writeNote(repoPath, sha, body); err != nil {
		return nil, err
	}
	if err := db.MarkCommitNoted(sha, repoID, commitNanos); err != nil {
		return nil, err
	}
	return note, nil
}

// perLine is the intermediate per-line attribution before collapsing into ranges.
type perLine struct {
	file    string
	line    int
	tool    store.Tool
	model   string
	genType store.GenType
	edit    *store.Edit
	// noEdit is true when NO tool's edit row covered this line. Used for the
	// Copilot fold-in: lines with no DB record AND a Copilot session active
	// near the commit are attributed to copilot (confidence=low) rather than
	// staying as human.
	noEdit bool
}

func buildNote(db *store.DB, repoPath, sha string, commitNanos int64, added []AddedLine, deleted map[string][]int, renames map[string]string, fileChanges map[string]FileChangeType) (*Note, error) {
	// No time lower bound: a squash/rebase/cherry-pick may pull in edits
	// from months ago. The commit_ts is the only upper bound that matters.
	const sinceNanos = int64(0)

	// Group changed lines by file so we hit the DB once per file.
	byFile := map[string][]int{}
	for _, a := range added {
		byFile[a.File] = append(byFile[a.File], a.LineNum)
	}

	resolved := make([]perLine, 0, len(added))
	for file, lineNos := range byFile {
		edits, err := db.EditsForFileSince(repoPath, file, sinceNanos)
		if err != nil {
			return nil, err
		}
		// If this file is the destination of a rename, also pull edits that
		// were recorded under the old name so `git mv` doesn't lose history.
		if from, ok := renames[file]; ok && from != "" {
			oldEdits, err := db.EditsForFileSince(repoPath, from, sinceNanos)
			if err != nil {
				return nil, err
			}
			edits = mergeEditsByTimeDesc(edits, oldEdits)
		}
		// edits are newest-first. For each line, pick the latest edit covering it.
		// git commit timestamps are second-precision; edit timestamps are
		// nanosecond-precision. Allow up to 5s of post-commit slack so an
		// edit recorded in the same wall-clock second as the commit still
		// counts. (The post-commit hook fires immediately after the commit.)
		const sameSecondSlackNanos = int64(5 * 1e9)
		for _, ln := range lineNos {
			// noEdit=true means no tool's edit row covers this line.
			// We flip it to false as soon as we find a matching edit.
			p := perLine{file: file, line: ln, tool: store.ToolHuman, noEdit: true}
			for i := range edits {
				e := &edits[i]
				if e.TimestampNanos > commitNanos+sameSecondSlackNanos {
					continue
				}
				if !coversLine(e.Lines, ln) {
					continue
				}
				p.tool = e.Tool
				p.genType = e.GenType
				if e.Model.Valid {
					p.model = e.Model.String
				}
				p.edit = e
				p.noEdit = false
				break
			}
			resolved = append(resolved, p)
		}
	}

	// Copilot fold-in: lines with no DB record at all (noEdit=true) that fall
	// inside a window where a Copilot session was active get attributed to
	// copilot (confidence=low) rather than silently crediting the human.
	//
	// Window: 5 minutes on either side of the commit timestamp. The pre-hook
	// watcher's storage-touch marker fires only every 5 seconds and may lag
	// real activity, so 60s was too tight in practice.
	//
	// Note: lines already matched to a hook-sourced copilot edit (the real
	// per-line attribution path) have noEdit=false here, so the fold-in
	// can't double-credit them.
	const copilotWindowNanos = int64(5 * 60 * 1e9)
	if db.HasCopilotSessionNear(commitNanos, copilotWindowNanos) {
		for i := range resolved {
			if resolved[i].noEdit {
				resolved[i].tool = store.ToolCopilot
				// noEdit → no PostToolUse hook fired → this is an inline completion
				resolved[i].genType = store.GenTypeCompletion
			}
		}
	}

	sort.Slice(resolved, func(i, j int) bool {
		if resolved[i].file != resolved[j].file {
			return resolved[i].file < resolved[j].file
		}
		return resolved[i].line < resolved[j].line
	})

	note := &Note{
		Schema:      1,
		Commit:      sha,
		ByTool:      map[string]Tool{},
		GeneratedBy: "blamely 0.1.0",
	}

	// Aggregate per-tool and build files/lines.
	type toolAgg struct {
		lines      int
		confidence store.Confidence
		model      string
		tokens     Tokens
		hasTokens  bool
	}
	agg := map[store.Tool]*toolAgg{}
	seenEditTokens := map[int64]bool{}

	var totalTokens Tokens
	var hasTotalTokens bool

	var curFile string
	var curFileEntry *FileEntry
	var fileCount int

	// Helper: count added lines per file from the resolved list.
	addedPerFile := map[string]int{}
	for _, p := range resolved {
		addedPerFile[p.file]++
	}

	// suggestedPerTool sums each edit's SuggestedLines once per edit so the
	// same edit doesn't double-count when it touches multiple lines.
	suggestedPerTool := map[store.Tool]int64{}
	seenEditSuggested := map[int64]bool{}

	flushFile := func() {
		if curFileEntry == nil {
			return
		}
		curFileEntry.Added = addedPerFile[curFileEntry.Path]
		// Deleted line numbers: look up by post-commit path (direct match), then
		// by the pre-commit name for renames (e.g. `git mv old.go new.go`).
		var delLines []int
		if d, ok := deleted[curFileEntry.Path]; ok {
			delLines = d
		} else if old, ok := renames[curFileEntry.Path]; ok {
			delLines = deleted[old]
		}
		// If a line number already has an "add" entry, the delete at the same
		// number is a "modified" record viewed from the pre-image side. Drop
		// the delete to avoid duplicate-looking entries; the add already
		// represents the change at that line.
		addedLineSet := map[int]bool{}
		for _, l := range curFileEntry.Lines {
			if l.Type == "add" {
				addedLineSet[l.Line] = true
			}
		}
		var keptDel []int
		for _, n := range delLines {
			if addedLineSet[n] {
				continue
			}
			keptDel = append(keptDel, n)
			curFileEntry.Lines = append(curFileEntry.Lines, LineEntry{
				Line: n,
				Type: "delete",
			})
		}
		curFileEntry.Deleted = len(keptDel)
		note.Totals.DeletedLines += curFileEntry.Deleted
		// File-level change kind from the diff parser.
		if kind, ok := fileChanges[curFileEntry.Path]; ok {
			curFileEntry.Type = string(kind)
		}
		if from, ok := renames[curFileEntry.Path]; ok {
			curFileEntry.RenamedFrom = from
		}
		// Sort all lines (adds + deletes) by line number for stable rendering.
		sort.SliceStable(curFileEntry.Lines, func(i, j int) bool {
			return curFileEntry.Lines[i].Line < curFileEntry.Lines[j].Line
		})
		note.Files = append(note.Files, *curFileEntry)
		fileCount++
	}

	for _, p := range resolved {
		if p.file != curFile {
			flushFile()
			curFile = p.file
			curFileEntry = &FileEntry{Path: p.file}
		}

		a, ok := agg[p.tool]
		if !ok {
			a = &toolAgg{confidence: confidenceFor(p.tool, p.edit)}
			agg[p.tool] = a
		}
		a.lines++
		if p.model != "" && a.model == "" {
			a.model = p.model
		}

		var tokensForLine *Tokens
		var editIDForLine *int64
		if p.edit != nil {
			editID := p.edit.ID
			editIDForLine = &editID
			if !seenEditTokens[editID] {
				seenEditTokens[editID] = true
				if hasUsage(p.edit) {
					t := Tokens{
						Input:      nullInt64(p.edit.InputTokens),
						Output:     nullInt64(p.edit.OutputTokens),
						CacheRead:  nullInt64(p.edit.CacheReadTokens),
						CacheWrite: nullInt64(p.edit.CacheWriteTokens),
					}
					tokensForLine = &t
					a.tokens.Input += t.Input
					a.tokens.Output += t.Output
					a.tokens.CacheRead += t.CacheRead
					a.tokens.CacheWrite += t.CacheWrite
					a.hasTokens = true
					totalTokens.Input += t.Input
					totalTokens.Output += t.Output
					totalTokens.CacheRead += t.CacheRead
					totalTokens.CacheWrite += t.CacheWrite
					hasTotalTokens = true
				}
			}
			if !seenEditSuggested[editID] && p.edit.SuggestedLines > 0 {
				seenEditSuggested[editID] = true
				suggestedPerTool[p.tool] += p.edit.SuggestedLines
			}
		}

		entry := LineEntry{
			Line:       p.line,
			Type:       "add",
			AuthorType: authorTypeFor(p.tool),
			Tool:       string(p.tool),
			Model:      strPtr(p.model),
			Tokens:     tokensForLine,
			EditID:     editIDForLine,
		}
		if gt := string(p.genType); gt != "" && gt != string(store.GenTypeUnknown) {
			entry.GenType = strPtr(gt)
		}
		curFileEntry.Lines = append(curFileEntry.Lines, entry)
	}
	flushFile()

	// Files with ONLY deletions (no added lines): the loop above iterates
	// `resolved`, which is built from added lines, so a pure-deletion file
	// would never get a FileEntry. Walk the deleted map and add entries for
	// any file we haven't already emitted. Pre-rename paths are skipped
	// because the post-rename path picks up their deletions via flushFile's
	// renames lookup.
	emitted := map[string]bool{}
	for _, f := range note.Files {
		emitted[f.Path] = true
	}
	renamedFrom := map[string]bool{}
	for _, from := range renames {
		renamedFrom[from] = true
	}
	for path, delLines := range deleted {
		if emitted[path] || renamedFrom[path] || len(delLines) == 0 {
			continue
		}
		fe := FileEntry{Path: path, Deleted: len(delLines)}
		for _, n := range delLines {
			fe.Lines = append(fe.Lines, LineEntry{Line: n, Type: "delete"})
		}
		sort.SliceStable(fe.Lines, func(i, j int) bool {
			return fe.Lines[i].Line < fe.Lines[j].Line
		})
		note.Files = append(note.Files, fe)
		note.Totals.DeletedLines += fe.Deleted
		fileCount++
		emitted[path] = true
	}
	// Files in fileChanges that have neither additions nor deletions (pure
	// rename / pure copy). Without this pass the note would omit them
	// entirely and `git mv foo bar` would leave no trace.
	for path, kind := range fileChanges {
		if emitted[path] || renamedFrom[path] {
			continue
		}
		fe := FileEntry{Path: path, Type: string(kind)}
		if from, ok := renames[path]; ok {
			fe.RenamedFrom = from
		}
		note.Files = append(note.Files, fe)
		fileCount++
	}
	// Keep the files list stable for downstream readers.
	sort.SliceStable(note.Files, func(i, j int) bool {
		return note.Files[i].Path < note.Files[j].Path
	})

	// Populate by_tool and totals. Tools with zero attributed lines are omitted
	// unless they have a non-zero suggested-lines count (still useful signal).
	for tool, a := range agg {
		if a.lines == 0 && suggestedPerTool[tool] == 0 {
			continue
		}
		t := Tool{
			Lines: a.lines,
			Model: strPtr(a.model),
		}
		// suggested/accepted are AI-only metrics; the human edit row doesn't
		// have a "model proposal" to compare against.
		if tool != store.ToolHuman {
			t.AcceptedLines = a.lines
			t.SuggestedLines = suggestedPerTool[tool]
		}
		if a.hasTokens {
			tk := a.tokens
			t.Tokens = &tk
		}
		note.ByTool[string(tool)] = t
		if tool == store.ToolHuman {
			note.Totals.HumanLines += a.lines
		} else {
			note.Totals.AILines += a.lines
		}
	}
	note.Totals.Files = fileCount
	if hasTotalTokens {
		tk := totalTokens
		note.Totals.Tokens = &tk
	}

	// Models rollup: per-model line counts derived from the resolved per-line
	// data. Keys are concrete model identifiers ("claude-opus-4-7", etc.),
	// not provider names. Lines whose source AI didn't expose a model aren't
	// counted here.
	for _, p := range resolved {
		if p.model == "" {
			continue
		}
		if note.Totals.Models == nil {
			note.Totals.Models = map[string]int{}
		}
		note.Totals.Models[p.model]++
	}

	// Populate by_gen_type from the resolved per-line data.
	for _, p := range resolved {
		if p.tool == store.ToolHuman {
			continue // human lines don't count toward AI gen_type
		}
		switch p.genType {
		case store.GenTypeChat:
			note.ByGenType.Chat++
		case store.GenTypeCLI:
			note.ByGenType.CLI++
		case store.GenTypeCompletion:
			note.ByGenType.Completion++
		default:
			note.ByGenType.Unknown++
		}
	}

	return note, nil
}

// mergeEditsByTimeDesc merges two slices of edits already sorted newest-first
// into one slice in the same order. Used to fold edits recorded under a
// pre-rename path into the per-file query without losing the newest-wins
// semantics the join relies on.
func mergeEditsByTimeDesc(a, b []store.Edit) []store.Edit {
	out := make([]store.Edit, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].TimestampNanos >= b[j].TimestampNanos {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

func coversLine(lines []store.EditLine, n int) bool {
	for _, l := range lines {
		if n >= l.StartLine && n <= l.EndLine {
			return true
		}
	}
	return false
}

func confidenceFor(tool store.Tool, e *store.Edit) store.Confidence {
	if e != nil {
		return e.Confidence
	}
	return defaultConf(tool)
}

func defaultConf(t store.Tool) store.Confidence {
	if t == store.ToolCopilot {
		return store.ConfidenceLow
	}
	return store.ConfidenceHigh
}

func hasUsage(e *store.Edit) bool {
	return e.InputTokens.Valid || e.OutputTokens.Valid ||
		e.CacheReadTokens.Valid || e.CacheWriteTokens.Valid
}

func nullInt64(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

func writeNote(repoPath, sha string, body []byte) error {
	cmd := exec.Command("git", "-C", repoPath, "notes", "--ref="+NotesRef, "add", "-f", "-F", "-", sha)
	cmd.Stdin = strings.NewReader(string(body))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git notes add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// authorTypeFor maps an internal tool identifier to the binary author bucket
// rendered in the note's per-line entries. Deletions (Tool == "") return "".
func authorTypeFor(t store.Tool) string {
	switch t {
	case "":
		return ""
	case store.ToolHuman:
		return "Human"
	default:
		return "AI"
	}
}
func equalStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func equalInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
