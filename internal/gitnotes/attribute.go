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

	"github.com/blamely/blamely/internal/config"
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

// filterConversation applies the user's conversation config to the built turns.
// The master `Conversation` toggle gates everything; within it, user and
// assistant turns are kept independently so a team can, e.g., store the model's
// replies but omit the developer's prompts (or vice versa). Returns nil when
// nothing survives so the `conversation` field is omitted from the note.
func filterConversation(turns []ConvTurn, nc config.NoteConfig) []ConvTurn {
	if !nc.Conversation {
		return nil
	}
	if nc.ConversationUser && nc.ConversationAssistant {
		return turns
	}
	out := make([]ConvTurn, 0, len(turns))
	for _, t := range turns {
		switch strings.ToLower(t.Role) {
		case "user":
			if !nc.ConversationUser {
				continue
			}
		case "assistant":
			if !nc.ConversationAssistant {
				continue
			}
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
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
	Type    string       `json:"type,omitempty"`
	// RenamedFrom (resp. CopiedFrom) is set for RENAMED / COPIED files and
	// names the source pre-commit path.
	RenamedFrom string       `json:"renamed_from,omitempty"`
	CopiedFrom  string       `json:"copied_from,omitempty"`
	Added       int          `json:"added"`
	Deleted     int          `json:"deleted"`
	// Lines holds the attribution as collapsed ranges (schema 2): consecutive
	// lines sharing the same (type, author_type, tool, model, gen_type) are
	// merged into one RangeEntry. Added/Deleted above stay line-accurate.
	Lines []RangeEntry `json:"lines,omitempty"`
	// acc is the per-line accumulation buffer used while building the note; it
	// is collapsed into Lines at flush time and never serialized.
	acc []LineEntry
}

// RangeEntry is a contiguous run of added or deleted lines that share the same
// attribution. For a single line Start == End. Replaces the schema-1 per-line
// LineEntry to keep notes small on large commits (one object per range, not
// per line). Readers of older notes should treat a bare `line` as start==end.
type RangeEntry struct {
	Start int    `json:"start"` // post-image for adds, pre-image for deletes
	End   int    `json:"end"`   // inclusive; == Start for a single line
	Type  string `json:"type"`  // "add" | "delete"
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

// NumLines returns the number of lines covered by the range (inclusive).
func (r RangeEntry) NumLines() int {
	if r.End < r.Start {
		return 0
	}
	return r.End - r.Start + 1
}

// LineEntry is the intermediate per-line attribution accumulated while building
// a file's entry, before collapsing into RangeEntry ranges. Internal only.
type LineEntry struct {
	Line       int
	Type       string
	AuthorType string
	Tool       string
	Model      *string
	GenType    *string
}

// collapseToRanges sorts per-line entries and merges consecutive lines that
// share the same attribution into ranges. Adds and deletes never merge (Type
// differs); a gap in line numbers always starts a new range.
func collapseToRanges(lines []LineEntry) []RangeEntry {
	if len(lines) == 0 {
		return nil
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].Line != lines[j].Line {
			return lines[i].Line < lines[j].Line
		}
		return lines[i].Type < lines[j].Type
	})
	var out []RangeEntry
	for _, le := range lines {
		if n := len(out); n > 0 {
			last := &out[n-1]
			if last.Type == le.Type &&
				last.AuthorType == le.AuthorType &&
				last.Tool == le.Tool &&
				equalStrPtr(last.Model, le.Model) &&
				equalStrPtr(last.GenType, le.GenType) &&
				le.Line == last.End+1 {
				last.End = le.Line
				continue
			}
		}
		out = append(out, RangeEntry{
			Start:      le.Line,
			End:        le.Line,
			Type:       le.Type,
			AuthorType: le.AuthorType,
			Tool:       le.Tool,
			Model:      le.Model,
			GenType:    le.GenType,
		})
	}
	return out
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
	// Attach conversation from the AI SESSIONS that produced this repo's edits in
	// this commit's window. Three scopes combine so a commit's note shows exactly
	// its own conversation and nothing else:
	//   1. repo_path — SessionTranscriptsForPeriod only returns sessions whose
	//      edits are in THIS repo, so working in many repos never cross-bleeds.
	//   2. session_id — sessions are keyed/deduped by AI session_id, so each
	//      distinct conversation that contributed code is included exactly once.
	//   3. time window (prevCommit, commit] — within each session, only the turns
	//      made after the previous commit, so a long session spanning several
	//      commits doesn't repeat its whole history on every commit.
	// transcript_path is written into raw_meta by the Claude hook as of blamely
	// 0.2; older edits silently produce no conversation. Must run BEFORE
	// MarshalIndent/writeNote so it ends up in the persisted note.
	sinceConv := db.PreviousCommitTimestampNanos(repoID, commitNanos)
	const slackNanos = int64(5 * 1e9)
	const maxConvTurns = 12
	if sessions, err := db.SessionTranscriptsForPeriod(repoID, sinceConv, commitNanos+slackNanos); err == nil {
		for _, s := range sessions {
			if len(note.Conversation) >= maxConvTurns {
				break
			}
			turns, err := tools.ReadTranscriptConversationWindow(s.TranscriptPath, maxConvTurns, 300, sinceConv, commitNanos+slackNanos)
			if err != nil {
				continue
			}
			for _, t := range turns {
				if len(note.Conversation) >= maxConvTurns {
					break
				}
				note.Conversation = append(note.Conversation, ConvTurn{Role: t.Role, Text: t.Text})
			}
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

	// Apply the user's note-content config (~/.blamely/config.json). The note is
	// built in full above; here we strip whatever the user disabled, in one
	// auditable place, just before it is persisted. Defaults are all-on, so a
	// missing/partial config leaves the note unchanged. Attribution itself is
	// never affected — only what gets written into the note.
	cfg := config.LoadConfig()
	if !cfg.Note.Message {
		note.Message = ""
	}
	if !cfg.Note.CodingTime {
		note.CodingTimeNanos = 0
	}
	note.Conversation = filterConversation(note.Conversation, cfg.Note)
	if !cfg.Note.FileLines {
		for i := range note.Files {
			note.Files[i].Lines = nil
		}
	}
	if !cfg.Note.Tokens {
		note.Totals.Tokens = nil
		for k, t := range note.ByTool {
			t.Tokens = nil
			note.ByTool[k] = t
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

// inheritBlankLineAttribution reassigns blank (empty/whitespace-only) lines to
// the author of the nearest non-blank line in the SAME file, preferring the
// line above. A blank line has no stable identity — its content_sha isn't
// unique, so once the file drifts it can't be re-located by content and falls
// through to Human even when the AI generated it as part of a block. Inheriting
// from the surrounding block keeps AI-generated blank lines AI, while blank
// lines the user added between human lines stay Human. Only blanks that
// defaulted to Human are touched, and only an AI neighbour promotes them — a
// human neighbour leaves the blank human. `resolved` must already be sorted by
// (file, line).
func inheritBlankLineAttribution(resolved []perLine) {
	for i := range resolved {
		if strings.TrimSpace(resolved[i].content) != "" {
			continue // non-blank lines keep their own (content-matched) attribution
		}
		if resolved[i].tool != "" || resolved[i].genType != store.GenTypeHuman {
			continue // a blank that already matched an AI edit — leave it
		}
		src := -1
		for j := i - 1; j >= 0 && resolved[j].file == resolved[i].file; j-- {
			if strings.TrimSpace(resolved[j].content) != "" {
				src = j
				break
			}
		}
		if src == -1 {
			for j := i + 1; j < len(resolved) && resolved[j].file == resolved[i].file; j++ {
				if strings.TrimSpace(resolved[j].content) != "" {
					src = j
					break
				}
			}
		}
		if src == -1 || resolved[src].tool == "" {
			continue // no neighbour, or the neighbour is human — keep blank human
		}
		resolved[i].tool = resolved[src].tool
		resolved[i].genType = resolved[src].genType
		resolved[i].model = resolved[src].model
	}
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

	inheritBlankLineAttribution(resolved)

	note := &Note{
		Schema:      2,
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
			curFileEntry.acc = append(curFileEntry.acc, LineEntry{
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
		// Collapse per-line accumulation (adds + deletes) into ranges.
		curFileEntry.Lines = collapseToRanges(curFileEntry.acc)
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
		curFileEntry.acc = append(curFileEntry.acc, entry)
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
		acc := make([]LineEntry, 0, len(delLines))
		for _, n := range delLines {
			acc = append(acc, LineEntry{Line: n, Type: "delete", AuthorType: "Human"})
		}
		fe.Lines = collapseToRanges(acc)
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

// coversLine reports whether any edit line covers post-image line n BY POSITION.
// Lines that carry a ContentSHA are skipped here on purpose: their content is
// the authoritative signal, so they must match via coversContentSHA, not by
// line number. Matching a content_sha line by position would re-introduce the
// drift bug — an inserted line landing inside the AI's original range would be
// mislabelled AI, and the AI line pushed out of the range mislabelled human.
// Only ranges WITHOUT a content_sha (inline completions, log/velocity edits)
// are matched positionally. Mirrors the editor gutter's resolve logic.
func coversLine(lines []store.EditLine, n int) bool {
	for _, l := range lines {
		if l.ContentSHA != "" {
			continue
		}
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
