package gitnotes

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
	"github.com/blamely/blamely/internal/tools"
)

const NotesRef = "refs/notes/blamely"

// ConvTurn is one user or assistant turn from the AI tool's conversation
// transcript that produced edits for this commit.
type ConvTurn struct {
	Role string `json:"role"` // "user" or "assistant"
	Text string `json:"text"`
}

// Note is the JSON payload written to refs/notes/blamely for each commit.
type Note struct {
	Schema int    `json:"schema"`
	Commit string `json:"commit"`
	// Branch is the currently-checked-out branch when the commit was made,
	// or "" if HEAD was detached. Useful when the same commit lands on
	// multiple branches (the note is attached to the commit, not the branch).
	Branch string `json:"branch,omitempty"`
	// BaseSHA is the work session's divergence point from the default branch
	// (merge-base), or "" on the trunk. Together with Branch it identifies the
	// session whose edits were scoped into this note.
	BaseSHA string `json:"base_sha,omitempty"`
	// Message is the full commit message (subject + body) as recorded by git.
	Message string `json:"message,omitempty"`
	// CodingTimeNanos is the wall-clock time from the earliest edit observed
	// for this repo (up to the commit) to the commit timestamp. Zero if no
	// edits were observed in the lookback window (default 8h, see
	// store.SessionDurationNanos).
	CodingTimeNanos int64           `json:"coding_time_nanos,omitempty"`
	Totals          Totals          `json:"totals"`
	ByTool          map[string]Tool `json:"by_tool"`
	ByGenType    ByGenType   `json:"by_gen_type"`
	Files        []FileEntry `json:"files"`
	GeneratedBy  string      `json:"generated_by"`
	// Conversation holds the last few user/assistant turns from the
	// AI transcript that drove the edits in this commit. Populated only
	// when a transcript_path was captured in the edit's raw_meta.
	Conversation []ConvTurn `json:"conversation,omitempty"`
}

// ByGenType shows how many lines in the commit were produced by each
// generation method: a chat/panel session, a CLI command, an inline
// completion, or typed by the human author.
type ByGenType struct {
	Chat       int `json:"chat"`
	CLI        int `json:"cli"`
	Completion int `json:"completion"`
	// Human is the count of human-typed lines. Lives here (not in ByTool)
	// because humans aren't a tool.
	Human   int `json:"human"`
	Unknown int `json:"unknown,omitempty"`
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
	// or pasted lines, "" for deletions (we don't attribute who deleted what).
	AuthorType string `json:"author_type,omitempty"`
	// Tool is the attributed AI tool. Omitted for human-typed lines (humans
	// aren't a tool) and for deletes (we don't attribute removals).
	Tool    string  `json:"tool,omitempty"`
	Model   *string `json:"model,omitempty"`
	GenType *string `json:"gen_type,omitempty"`
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
	// Attach conversation from any AI transcript files linked to edits in this
	// commit's session. transcript_path is written into raw_meta by the Claude
	// hook as of blamely 0.2; older edits silently produce no conversation.
	// Must run BEFORE MarshalIndent/writeNote so it ends up in the persisted note.
	sinceConv := db.PreviousCommitTimestampNanos(repoID, commitNanos)
	const slackNanos = int64(5 * 1e9)
	if paths, err := db.TranscriptPathsForPeriod(repoID, sinceConv, commitNanos+slackNanos); err == nil {
		for _, tp := range paths {
			turns, err := tools.ReadTranscriptConversation(tp, 10, 300)
			if err != nil || len(turns) == 0 {
				continue
			}
			for _, t := range turns {
				note.Conversation = append(note.Conversation, ConvTurn{Role: t.Role, Text: t.Text})
			}
			break // one transcript per commit is enough
		}
	}

	// Persist the user prompts of each session in this commit window into the
	// prompts table (keyed by session_id), so the conversation survives transcript
	// rotation/deletion and can be shown without re-reading the transcript. Best
	// effort: failures here never block note writing.
	if sessions, err := db.SessionTranscriptsForPeriod(repoID, sinceConv, commitNanos+slackNanos); err == nil {
		for _, s := range sessions {
			turns, err := tools.ReadTranscriptConversation(s.TranscriptPath, 200, 4000)
			if err != nil || len(turns) == 0 {
				continue
			}
			userPrompts := make([]string, 0, len(turns))
			for _, t := range turns {
				if t.Role == "user" && t.Text != "" {
					userPrompts = append(userPrompts, t.Text)
				}
			}
			if len(userPrompts) > 0 {
				_ = db.UpsertUserPrompts(s.SessionID, repoID, s.Tool, userPrompts, commitNanos)
			}
		}
	}

	// Copilot / Cursor chat panels persist a delta-encoded chat JSONL rather
	// than a Claude-style transcript. Pull its conversation (if no Claude
	// transcript already supplied one) and its per-session token usage, which
	// is the only place Copilot/Cursor token counts are observable locally.
	until := commitNanos + slackNanos
	if refs, err := db.ChatSessionPathsForPeriod(repoID, sinceConv, until); err == nil {
		for _, ref := range refs {
			if len(note.Conversation) == 0 {
				if turns, err := tools.ReadChatSessionConversation(ref.Path, 10, 300, sinceConv, until); err == nil {
					for _, t := range turns {
						note.Conversation = append(note.Conversation, ConvTurn{Role: t.Role, Text: t.Text})
					}
				}
			}
			if usage, err := tools.ReadChatSessionUsage(ref.Path, sinceConv, until); err == nil {
				applyChatUsage(note, ref.Tool, usage)
			}
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

	// HEAD advanced — invalidate cached session identity so post-commit edits
	// resolve to a new work session on this branch.
	daemon.InvalidateSessionCache(repoID)
	daemon.InvalidateSessionCache(repoPath)

	return note, nil
}

// applyChatUsage folds a chat session's summed token usage into the note for
// the owning tool. It only applies when that tool actually has attributed lines
// in this commit (a chat session that produced no committed code shouldn't add
// tokens). The chat JSONL exposes only prompt/completion counts, mapped to
// input/output; there is no cache-token split. Also backfills the tool's model
// from the session when the edit rows didn't carry one.
func applyChatUsage(note *Note, tool store.Tool, u *tools.TranscriptUsage) {
	if u == nil {
		return
	}
	key := string(tool)
	t, ok := note.ByTool[key]
	if !ok {
		return
	}
	if t.Tokens == nil {
		t.Tokens = &Tokens{}
	}
	t.Tokens.Input += u.InputTokens
	t.Tokens.Output += u.OutputTokens
	if t.Model == nil && u.Model != "" {
		m := u.Model
		t.Model = &m
	}
	note.ByTool[key] = t

	if note.Totals.Tokens == nil {
		note.Totals.Tokens = &Tokens{}
	}
	note.Totals.Tokens.Input += u.InputTokens
	note.Totals.Tokens.Output += u.OutputTokens
}

// perLine is the intermediate per-line attribution before collapsing into ranges.
type perLine struct {
	file    string
	line    int
	content string // raw line text from the diff (for ContentSHA fallback)
	tool    store.Tool
	model   string
	genType store.GenType
	edit    *store.Edit
}

func buildNote(db *store.DB, repoPath, sha string, commitNanos int64, added []AddedLine, deleted map[string][]int, renames map[string]string, fileChanges map[string]FileChangeType) (*Note, error) {
	// Scope AI edits by WORK SESSION (branch + HEAD-at-edit-time), not by a
	// timestamp window. At commit time the session is the one keyed by this
	// commit's parent — the HEAD tip while those edits were being made.
	branch := gitutil.BranchName(repoPath)
	baseSHA := gitutil.ParentCommitSHA(repoPath, sha)
	// sessionID is "" when the branch is unresolvable (detached HEAD / not a
	// repo). EditsForFileInSession still pulls session_id IS NULL rows, which is
	// where detached/legacy edits live, so they attribute by line as before.
	var sessionID string
	if branch != "" {
		if id, err := db.ResolveSession(repoPath, branch, baseSHA); err == nil {
			sessionID = id
		}
	}

	// Intra-session lower bound: an edit already consumed by an earlier commit on
	// this branch must not re-claim a line in a later commit (sessions can span
	// many commits). Returns 0 for the first commit → includes all prior edits.
	// This bounds only the PRIMARY match; the content_sha fallback is unbounded.
	sinceNanos := db.PreviousCommitTimestampNanos(repoPath, commitNanos)

	// Group changed lines by file so we hit the DB once per file.
	// Also carry the line content for ContentSHA-based fallback attribution
	// (so AI edits survive line-number drift from human edits made after apply).
	type fileLineKey struct{ file string; lineNum int }
	lineContent := map[fileLineKey]string{}
	byFile := map[string][]int{}
	for _, a := range added {
		byFile[a.File] = append(byFile[a.File], a.LineNum)
		if a.Content != "" {
			lineContent[fileLineKey{a.File, a.LineNum}] = a.Content
		}
	}

	// editsForFile pulls the session-scoped edits for a file (primary), plus the
	// repo-wide edits for the same file (content_sha fallback). The fallback is
	// what re-attributes cherry-picked/squashed AI code: the new commit has a
	// fresh SHA/timestamp and a different session, but the line content — and
	// thus its content_sha — is unchanged.
	editsForFile := func(file string) (sessionEdits, anyEdits []store.Edit, err error) {
		if sessionEdits, err = db.EditsForFileInSession(repoPath, file, sessionID); err != nil {
			return nil, nil, err
		}
		if anyEdits, err = db.EditsForFileAny(repoPath, file); err != nil {
			return nil, nil, err
		}
		// Destination of a rename: also pull edits recorded under the old name
		// so `git mv` doesn't lose history.
		if from, ok := renames[file]; ok && from != "" {
			if old, err := db.EditsForFileInSession(repoPath, from, sessionID); err == nil {
				sessionEdits = mergeEditsByTimeDesc(sessionEdits, old)
			}
			if old, err := db.EditsForFileAny(repoPath, from); err == nil {
				anyEdits = mergeEditsByTimeDesc(anyEdits, old)
			}
		}
		return sessionEdits, anyEdits, nil
	}

	resolved := make([]perLine, 0, len(added))
	for file, lineNos := range byFile {
		sessionEdits, anyEdits, err := editsForFile(file)
		if err != nil {
			return nil, err
		}
		// git commit timestamps are second-precision; edit timestamps are
		// nanosecond-precision. Allow up to 5s of post-commit slack so an
		// edit recorded in the same wall-clock second as the commit still
		// counts. (The post-commit hook fires immediately after the commit.)
		const sameSecondSlackNanos = int64(5 * 1e9)
		for _, ln := range lineNos {
			// Default: human-typed code. tool="" + gen_type=human is the
			// canonical representation; humans aren't a tool.
			content := lineContent[fileLineKey{file, ln}]
			p := perLine{file: file, line: ln, content: content, tool: "", genType: store.GenTypeHuman}
			// Primary: a session edit covering this line by range OR content_sha,
			// bounded below by the previous commit (intra-session separator).
			e := pickEdit(sessionEdits, ln, content, sinceNanos, commitNanos+sameSecondSlackNanos, true)
			if e == nil {
				// Fallback: any edit for this file whose content_sha matches this
				// line's content (line numbers ignored, time-unbounded below —
				// survives cherry-pick/squash).
				e = pickEdit(anyEdits, ln, content, 0, commitNanos+sameSecondSlackNanos, false)
			}
			if e != nil {
				// Normalise legacy rows: tool="human" pre-dates the split.
				if e.Tool == store.ToolHuman {
					p.tool, p.genType = "", store.GenTypeHuman
				} else {
					p.tool, p.genType = e.Tool, e.GenType
				}
				if e.Model.Valid {
					p.model = e.Model.String
				}
				p.edit = e
			}
			resolved = append(resolved, p)
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
		BaseSHA:     baseSHA,
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
		// by the pre-commit name for renames (e.g. `git mv old.go new.go`). The
		// diff parser already filters out modification deletes (paired -/+
		// inside the same hunk) so everything left here is a genuine deletion.
		var delLines []int
		if d, ok := deleted[curFileEntry.Path]; ok {
			delLines = d
		} else if old, ok := renames[curFileEntry.Path]; ok {
			delLines = deleted[old]
		}
		for _, n := range delLines {
			curFileEntry.Lines = append(curFileEntry.Lines, LineEntry{
				Line:       n,
				Type:       "delete",
				AuthorType: "Human",
			})
		}
		curFileEntry.Deleted = len(delLines)
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

		if p.edit != nil {
			editID := p.edit.ID
			if !seenEditTokens[editID] {
				seenEditTokens[editID] = true
				if hasUsage(p.edit) {
					t := Tokens{
						Input:      nullInt64(p.edit.InputTokens),
						Output:     nullInt64(p.edit.OutputTokens),
						CacheRead:  nullInt64(p.edit.CacheReadTokens),
						CacheWrite: nullInt64(p.edit.CacheWriteTokens),
					}
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
		// Surface the file-level kind so reports can render `[DELETED]`
		// next to the path. Fall back to "DELETED" when the diff parser
		// didn't put the file in fileChanges (defensive — diff sections
		// for removed files normally do).
		kind := fileChanges[path]
		if kind == "" {
			kind = FileDeleted
		}
		fe := FileEntry{Path: path, Type: string(kind), Deleted: len(delLines)}
		for _, n := range delLines {
			fe.Lines = append(fe.Lines, LineEntry{Line: n, Type: "delete", AuthorType: "Human"})
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
		// AI/Human bar: empty tool = human-typed (always); copypaste lands
		// on the Human side because we can't prove the clipboard content
		// came from an AI.
		if isNonAITool(tool) {
			note.Totals.HumanLines += a.lines
		} else {
			note.Totals.AILines += a.lines
		}
		// by_tool lists AI tools (and copypaste, which is a distinct
		// not-typed-not-AI bucket). Humans aren't a tool, so we never emit
		// an entry for tool="" — those lines surface via
		// totals.human_lines and by_gen_type.human instead.
		if tool == "" {
			continue
		}
		t := Tool{
			Lines: a.lines,
			Model: strPtr(a.model),
		}
		// suggested/accepted are AI-only metrics. Clipboard-paste lines
		// have no model proposal to compare against.
		if !isNonAITool(tool) {
			t.AcceptedLines = a.lines
			t.SuggestedLines = suggestedPerTool[tool]
		}
		if a.hasTokens {
			tk := a.tokens
			t.Tokens = &tk
		}
		note.ByTool[string(tool)] = t
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
	// `copypaste` keeps living under by_tool — it's a distinct bucket and
	// would double-count if also classified here.
	for _, p := range resolved {
		if p.tool == store.ToolCopyPaste {
			continue
		}
		switch p.genType {
		case store.GenTypeChat:
			note.ByGenType.Chat++
		case store.GenTypeCLI:
			note.ByGenType.CLI++
		case store.GenTypeCompletion:
			note.ByGenType.Completion++
		case store.GenTypeHuman:
			note.ByGenType.Human++
		default:
			note.ByGenType.Unknown++
		}
	}
	// Deleted lines are always a human action: the user chose to remove them.
	// They don't appear in `resolved` (which only covers added lines), so we
	// add them to by_gen_type.human here after the totals are finalised.
	note.ByGenType.Human += note.Totals.DeletedLines

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

// pickEdit returns the first edit (the slice is confidence-then-recency ordered)
// recorded within (minNanos, maxNanos] that claims the given line. A content_sha
// match always counts; a line-number range match counts only when allowLineMatch
// is true.
//
// The primary (session-scoped) call passes minNanos = previous-commit timestamp
// so an edit already consumed by an earlier commit on this branch can't re-claim
// a line in a later commit. The cross-session fallback passes minNanos = 0 and
// allowLineMatch = false: it relies on content alone (line numbers are unreliable
// across cherry-pick/squash) and must NOT be time-bounded, because the original
// AI edit predates the rewritten commit's timestamp.
func pickEdit(edits []store.Edit, ln int, content string, minNanos, maxNanos int64, allowLineMatch bool) *store.Edit {
	for i := range edits {
		e := &edits[i]
		if e.TimestampNanos <= minNanos || e.TimestampNanos > maxNanos {
			continue
		}
		if coversContentSHA(e.Lines, content) || (allowLineMatch && coversLine(e.Lines, ln)) {
			return e
		}
	}
	return nil
}

func coversLine(lines []store.EditLine, n int) bool {
	for _, l := range lines {
		if n >= l.StartLine && n <= l.EndLine {
			return true
		}
	}
	return false
}

// coversContentSHA returns true when any edit line's stored ContentSHA matches
// the SHA-256 of the committed line's content. This is the fallback for when
// line numbers drifted after the AI applied the edit (e.g. the human added
// lines earlier in the file before committing). Only called when coversLine
// already returned false, and only when the edit has per-line hashes (chat
// edits from the textEditGroup path always carry them; log/velocity edits don't).
// Empty content or an edit with no ContentSHAs is never a match.
func coversContentSHA(lines []store.EditLine, content string) bool {
	if content == "" {
		return false
	}
	want := sha256HexStr([]byte(content))
	for _, l := range lines {
		if l.ContentSHA != "" && l.ContentSHA == want {
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
// rendered in the note's per-line entries. Empty tool means human-typed
// (the canonical form for human edits). Legacy ToolHuman maps to Human too
// so commits made before the human/tool split still render correctly.
// `copypaste` rolls up to "Human" because we can't prove the clipboard
// source was an AI; the distinct tool name in by_tool keeps the nuance.
func authorTypeFor(t store.Tool) string {
	switch t {
	case "", store.ToolHuman, store.ToolCopyPaste:
		return "Human"
	default:
		return "AI"
	}
}

// isNonAITool returns true for tool identifiers that should NOT count toward
// the AI side of the binary AI-vs-Human bar (and shouldn't get
// suggested/accepted-line metrics). Currently: human-typed lines (tool="")
// — plus the legacy ToolHuman value — and clipboard-paste lines of unknown
// origin.
func isNonAITool(t store.Tool) bool {
	return t == "" || t == store.ToolHuman || t == store.ToolCopyPaste
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

func sha256HexStr(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
