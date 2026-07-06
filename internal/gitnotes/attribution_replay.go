package gitnotes

// Replay-shaped commits: cherry-pick and squash re-commit content that was
// AUTHORED earlier (possibly on another branch), so neither the working log
// (keyed by this branch+parent) nor the time-windowed edit reconcile can see
// the original AI attribution — historically those commits recomputed to
// all-Human (and a cherry-pick with CHERRY_PICK_HEAD present got NO note at
// all, because the in-progress skip suppressed attribution and git does not
// copy notes for cherry-pick).
//
// detectReplayOp identifies such commits and their SOURCE commits; the source
// commits' already-settled notes are then replayed onto the new commit by
// content (line numbers drift across replays; content does not) via
// reconcileAddsFromSourceNotes. Human→AI only, consume-once — the same safety
// posture as the edit reconcile.

import (
	"os/exec"
	"regexp"
	"strings"

	"github.com/blamely/blamely/internal/gitutil"
)

type replayKind int

const (
	replayNone replayKind = iota
	replayCherryPick
	replaySquashMerge
	replayRebaseSquash // set by the post-rewrite path (Phase 2), not detection
)

// replayCtx describes a replay-shaped commit and where its content came from.
type replayCtx struct {
	Kind replayKind
	// SourceSHAs are the commits whose content this commit replays (one for a
	// cherry-pick; every folded commit for a squash). May name commits with no
	// note — those contribute nothing to the source index.
	SourceSHAs []string
}

// cherryPickTrailerRe matches the trailer `git cherry-pick -x` appends.
var cherryPickTrailerRe = regexp.MustCompile(`\(cherry picked from commit ([0-9a-f]{40})\)`)

// squashCommitLineRe matches the per-commit header lines of the DEFAULT
// `git merge --squash` commit message ("Squashed commit of the following:").
var squashCommitLineRe = regexp.MustCompile(`(?m)^commit ([0-9a-f]{40})\b`)

// detectReplayOp classifies sha as a replay-shaped commit. Detection order:
//  1. CHERRY_PICK_HEAD — still present at post-commit time for a clean pick
//     (pinned by TestReplayMarkerTiming_CherryPickHeadPresentAtPostCommit) and
//     contains the source sha.
//  2. The `-x` trailer in the commit message.
//  3. The default squash-merge message ("Squashed commit of the following:"
//     followed by `commit <sha>` blocks). SQUASH_MSG itself is already gone at
//     post-commit time (pinned by TestReplayMarkerTiming_SquashMsgGone...), so
//     the message is the only squash signal.
//
// A rewritten squash message (and a trailer-less pick whose marker is gone)
// yields replayNone — the caller's gated DB fallback and, failing that, Human
// are the (never wrong-direction) degradations.
func detectReplayOp(repoPath, sha string) replayCtx {
	if src, ok := gitutil.CherryPickHead(repoPath); ok {
		return replayCtx{Kind: replayCherryPick, SourceSHAs: []string{src}}
	}
	return replayFromMessage(commitMessage(repoPath, sha))
}

// replayFromMessage classifies a commit message alone (pure; unit-testable):
// the -x trailer beats the squash template when both somehow appear.
func replayFromMessage(msg string) replayCtx {
	if msg == "" {
		return replayCtx{}
	}
	if m := cherryPickTrailerRe.FindAllStringSubmatch(msg, -1); len(m) > 0 {
		srcs := make([]string, 0, len(m))
		for _, g := range m {
			srcs = append(srcs, g[1])
		}
		return replayCtx{Kind: replayCherryPick, SourceSHAs: srcs}
	}
	if strings.Contains(msg, "Squashed commit of the following:") {
		if m := squashCommitLineRe.FindAllStringSubmatch(msg, -1); len(m) > 0 {
			srcs := make([]string, 0, len(m))
			for _, g := range m {
				srcs = append(srcs, g[1])
			}
			return replayCtx{Kind: replaySquashMerge, SourceSHAs: srcs}
		}
	}
	return replayCtx{}
}

func commitMessage(repoPath, sha string) string {
	out, err := exec.Command("git", "-C", repoPath, "log", "-1", "--format=%B", sha).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// sourceAttrUnit is one consume-once unit of AI attribution harvested from a
// source commit's note: the content hashes of ONE AI-attributed added line and
// the tool identity that authored it.
type sourceAttrUnit struct {
	sha     string // sha256 of the exact line content
	norm    string // whitespace-normalized sha (reformat drift fallback)
	tool    string
	model   *string
	genType *string
	used    bool
}

// sourceNoteIndex holds, per repo-relative file path, the consume-once budget
// of AI-attributed line contents across all source commits' notes.
type sourceNoteIndex struct {
	byFile map[string][]*sourceAttrUnit
}

func (ix *sourceNoteIndex) empty() bool { return ix == nil || len(ix.byFile) == 0 }

// match consumes and returns the attribution of one unit whose content matches:
// exact sha first across all units, then the normalized fallback — mirroring
// pickAddEdit's two-pass order. Blank content never matches.
func (ix *sourceNoteIndex) match(file, content string) (*sourceAttrUnit, bool) {
	if ix.empty() || strings.TrimSpace(content) == "" {
		return nil, false
	}
	units := ix.byFile[file]
	if len(units) == 0 {
		return nil, false
	}
	want := sha256HexStr([]byte(content))
	for _, u := range units {
		if !u.used && u.sha == want {
			u.used = true
			return u, true
		}
	}
	wantNorm := sha256HexNormStr(content)
	if wantNorm == "" {
		return nil, false
	}
	for _, u := range units {
		if !u.used && u.norm == wantNorm {
			u.used = true
			return u, true
		}
	}
	return nil, false
}

// sourceNoteContentIndex builds the consume-once content index from the source
// commits' notes: for every AI-attributed ADDED range in each source note, the
// content of those lines (read from the source commit's post-image) becomes one
// budget unit each. Commits without a note (or unreadable files) contribute
// nothing — degradation is always toward Human, never over-attribution.
func sourceNoteContentIndex(repoPath string, srcSHAs []string) *sourceNoteIndex {
	ix := &sourceNoteIndex{byFile: map[string][]*sourceAttrUnit{}}
	for _, src := range srcSHAs {
		note := loadNoteForSeed(repoPath, src)
		if note == nil {
			continue
		}
		for fi := range note.Files {
			fe := &note.Files[fi]
			// Collect the AI add ranges first so the file content is only
			// resolved when the note actually attributes something to AI.
			var aiRanges []RangeEntry
			for _, r := range fe.Lines {
				if r.Type == "add" && r.AuthorType == "AI" {
					aiRanges = append(aiRanges, r)
				}
			}
			if len(aiRanges) == 0 {
				continue
			}
			content, ok := showFileAt(repoPath, src, fe.Path)
			if !ok {
				continue
			}
			lines := strings.Split(content, "\n")
			for _, r := range aiRanges {
				for ln := r.Start; ln <= r.End; ln++ {
					if ln < 1 || ln > len(lines) {
						continue
					}
					text := lines[ln-1]
					if strings.TrimSpace(text) == "" {
						continue // blanks carry no signal; inheritBlankAddRanges re-covers them
					}
					u := &sourceAttrUnit{
						sha:  sha256HexStr([]byte(text)),
						norm: sha256HexNormStr(text),
						tool: r.Tool,
					}
					// Fresh copies — never alias the parsed source note.
					if r.Model != nil {
						m := *r.Model
						u.model = &m
					}
					if r.GenType != nil {
						g := *r.GenType
						u.genType = &g
					}
					ix.byFile[fe.Path] = append(ix.byFile[fe.Path], u)
				}
			}
		}
	}
	return ix
}

// reconcileAddsFromSourceNotes upgrades Human-attributed added lines whose
// committed content matches an unconsumed unit of the source commits' AI
// attribution. Human→AI only; blank-line inheritance and range re-collapse
// mirror reconcileAddsFromEdits exactly.
func reconcileAddsFromSourceNotes(ix *sourceNoteIndex, note *Note, added []AddedLine) {
	if ix.empty() || note == nil || len(added) == 0 {
		return
	}
	contentByLine := map[string]map[int]string{}
	for _, a := range added {
		if contentByLine[a.File] == nil {
			contentByLine[a.File] = map[int]string{}
		}
		contentByLine[a.File][a.LineNum] = a.Content
	}

	changedAny := false
	for fi := range note.Files {
		fe := &note.Files[fi]
		lineContent := contentByLine[fe.Path]
		if len(lineContent) == 0 || len(ix.byFile[fe.Path]) == 0 {
			continue
		}
		var rewritten []RangeEntry
		changed := false
		for _, r := range fe.Lines {
			if r.Type != "add" {
				rewritten = append(rewritten, r)
				continue
			}
			for ln := r.Start; ln <= r.End; ln++ {
				single := RangeEntry{Start: ln, End: ln, Type: "add", AuthorType: r.AuthorType, Tool: r.Tool, Model: r.Model, GenType: r.GenType}
				if single.AuthorType != "AI" {
					if u, ok := ix.match(fe.Path, lineContent[ln]); ok {
						single.AuthorType = "AI"
						single.Tool = u.tool
						single.Model = u.model
						single.GenType = u.genType
						changed = true
					}
				}
				rewritten = append(rewritten, single)
			}
		}
		if changed {
			inheritBlankAddRanges(rewritten, lineContent)
			fe.Lines = collapseAddRanges(rewritten)
			changedAny = true
		}
	}
	if changedAny {
		recomputeAddedAggregates(note)
	}
}
