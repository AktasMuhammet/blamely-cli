package gitnotes

// Pushing refs/notes/blamely to a remote (called from the global pre-push hook).
//
// A notes ref is an ordinary commit chain, and servers that police commit
// messages — e.g. a Bitbucket hook rejecting any commit without a Jira issue
// key — check every commit a push introduces, notes commits included. git
// writes those with fixed messages ("Notes added by 'git notes add'", "… by
// 'git commit --amend'", "… by 'git notes copy'", "Merged notes from …"), so
// such a server rejected the notes push outright and attribution never left
// the machine. Worse, the rejected chain kept growing, so even a single
// well-formed commit would have been refused alongside the old ones.
//
// SyncNotes therefore never pushes the local notes history as-is. It works out
// which notes this clone changed since it last agreed with the remote, and
// republishes them on top of the remote tip as ONE notes commit per annotated
// commit: that commit's note is the only change it makes, and its message is
// the annotated commit's own message — which passed the server's rule when the
// code was pushed — followed by git's usual "Notes added by 'git notes add'"
// line. That line must stay: Oobeya tells notes commits apart from code
// commits by "notes added by" (a contains match, so its position is free).
//
// A note is published together with its commit: only notes on commits the
// server has (or receives in this very push) go out. Notes on local-only
// commits — WIP branches, the pre-amend/pre-rebase originals git leaves notes
// on, commits since garbage-collected — stay in one local "pending" commit on
// top, and go out on the push that publishes their commit. On the server such
// a note could only annotate a commit that does not exist there.
//
// Readers resolve notes from the ref's tip tree (git notes show, JGit NoteMap),
// so the rebuilt history changes nothing for them.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/gitutil"
)

// syncScratchBase namespaces the refs one sync run works in. It is outside
// refs/notes/ on purpose: nothing that looks at notes refs — `git notes`,
// notes.displayRef, a refs/notes/* push refspec, or Oobeya reading every ref
// under refs/notes/blamely — should ever see a half-built chain.
const syncScratchBase = "refs/blamely-sync"

// notesSyncMarker closes every pushed notes commit's message. It is git's own
// wording for `git notes add`, kept verbatim so anything matching notes commits
// by message (Oobeya filters on "notes added by") keeps working.
const notesSyncMarker = "Notes added by 'git notes add'"

// networkTimeout bounds the fetch/push calls. Generous on purpose: the first
// push of a big notes ref over a slow link can honestly take a while.
const networkTimeout = 5 * time.Minute

// noWriteFetchHeadFlag is a variable only so tests can stand in for a git that
// predates it (2.29); see fetchRemoteNotes.
var noWriteFetchHeadFlag = "--no-write-fetch-head"

// PrePushInfo is what sync needs from the pre-push hook's stdin.
type PrePushInfo struct {
	// Tips are the local shas being pushed (branch deletions skipped): the
	// server receives those commits in this very push.
	Tips []string
	// PushesNotesRef is set when the user's own push names refs/notes/blamely.
	// git fixed what to send before the hook ran, so that push still carries the
	// stock notes commits a key-checking server rejects.
	PushesNotesRef bool
}

// ParsePrePushStdin reads the pre-push hook's stdin: one
// "<local ref> <local sha> <remote ref> <remote sha>" line per pushed ref.
// Malformed lines are skipped — the hook must never fail on them.
func ParsePrePushStdin(r io.Reader) PrePushInfo {
	var info PrePushInfo
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 4 {
			continue
		}
		if f[2] == NotesRef {
			info.PushesNotesRef = true
		}
		if isHexSHA(f[1]) && strings.Trim(f[1], "0") != "" {
			info.Tips = append(info.Tips, f[1])
		}
	}
	return info
}

// SyncNotes publishes this clone's notes to a remote as described above. remote
// is the remote's name (or a URL when the push named one); url is the URL git
// is pushing to — pre-push's second argument — and may be empty. Both the fetch
// and the push go to that one URL: a remote whose fetch URL differs from its
// push URL (a read mirror) would otherwise hand us a stale tip to build on.
// pushedTips (may be nil) are the commits the surrounding push sends.
//
// A failure caused by another writer moving a ref mid-sync — a teammate's
// notes push, a note written by a concurrent commit — is retried once;
// anything else, such as the server's rule rejecting the push, is final.
func SyncNotes(repo, remote, url string, pushedTips []string) error {
	target := url
	if target == "" {
		target = pushURL(repo, remote)
	}
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if err = syncOnce(repo, remote, target, pushedTips); err == nil || !isRefRace(err) {
			return err
		}
	}
	return err
}

// isRefRace reports whether err is git refusing a ref update because the ref
// moved since we read it — the only failure a retry can fix. (git runs with
// LC_ALL=C here, so these messages are never translated.)
func isRefRace(err error) bool {
	s := err.Error()
	for _, m := range []string{"non-fast-forward", "fetch first", "stale info", "cannot lock ref", "but expected"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// noteChange is one annotated commit's note as this sync publishes it.
type noteChange struct {
	target string // the annotated commit
	blob   string // the note's new blob; "" deletes the note
}

func syncOnce(repo, remote, target string, pushedTips []string) error {
	local := revParse(repo, NotesRef)
	if local == "" {
		return nil // nothing committed with Blamely yet
	}

	// Scratch refs of an earlier run that was killed (Ctrl-C mid-push) are
	// dead weight; clear them, then work under a per-process namespace.
	clearScratchRefs(repo, syncScratchBase)
	scratch := fmt.Sprintf("%s/%d", syncScratchBase, os.Getpid())
	defer clearScratchRefs(repo, scratch)

	remoteTip, err := fetchRemoteNotes(repo, target, scratch+"/remote")
	if err != nil {
		return err
	}
	switch {
	case remoteTip == local:
		return nil
	case remoteTip != "" && isAncestor(repo, local, remoteTip):
		// Remote is simply ahead (a teammate's notes): take it, nothing to push.
		return updateRef(repo, NotesRef, remoteTip, local)
	}

	// What this clone changed since it last agreed with the remote: the notes
	// that differ between the merge base and our tip. Compared as target→blob
	// maps, not path diffs — two trees can fan out differently, and a path diff
	// would then report every moved note as deleted and re-added.
	base := ""
	if remoteTip != "" {
		base = mergeBase(repo, local, remoteTip)
	}
	baseNotes, err := notesMap(repo, base)
	if err != nil {
		return err
	}
	localNotes, err := notesMap(repo, local)
	if err != nil {
		return err
	}
	remoteNotes, err := notesMap(repo, remoteTip)
	if err != nil {
		return err
	}
	var changes []noteChange
	for t, blob := range localNotes {
		if baseNotes[t] != blob {
			changes = append(changes, noteChange{t, blob})
		}
	}
	for t := range baseNotes {
		if _, ok := localNotes[t]; !ok {
			changes = append(changes, noteChange{t, ""})
		}
	}
	// Drop what the remote already says (e.g. a teammate published the same).
	kept := changes[:0]
	for _, c := range changes {
		if remoteNotes[c.target] != c.blob {
			kept = append(kept, c)
		}
	}
	changes = kept

	// Split into notes whose commit the server has or receives now (published,
	// one notes commit each, oldest commit first) and the rest (kept pending).
	commits := readCommits(repo, targetsOf(changes))
	known := serverKnown(repo, commits, pushedTips, remote)
	var publish, pending []noteChange
	for _, c := range changes {
		if _, ok := known[c.target]; ok {
			publish = append(publish, c)
		} else {
			pending = append(pending, c)
		}
	}
	// Oldest first, in history order: a commit's note never lands before its
	// parents' (committer dates can tie, or lie).
	sort.SliceStable(publish, func(i, j int) bool { return known[publish[i].target] > known[publish[j].target] })
	sort.Slice(pending, func(i, j int) bool { return pending[i].target < pending[j].target })

	if len(publish) == 0 && (remoteTip == "" || isAncestor(repo, remoteTip, local)) {
		return nil // nothing to publish, and nothing of the remote's to take in
	}

	// Build the published chain on the remote tip, then — if anything is left —
	// the local pending commit on top of it, in one fast-import run.
	// Each notes commit's message is the annotated commit's message, as is,
	// followed by the marker. Nothing about it is checked or rewritten.
	var msgs []string
	for _, c := range publish {
		msgs = append(msgs, commits[c.target].msg+"\n\n"+notesSyncMarker+"\n")
	}
	publishRef, localRef := scratch+"/publish", scratch+"/local"
	if err := buildNotesCommits(repo, remoteTip, publish, msgs, pending, publishRef, localRef); err != nil {
		return err
	}
	publishTip, newLocal := remoteTip, revParse(repo, localRef)
	if len(publish) > 0 {
		publishTip = revParse(repo, publishRef)
	}
	if newLocal == "" {
		newLocal = publishTip
	}
	if newLocal == "" {
		return errors.New("building notes commits produced no commit")
	}

	// CAS: a note a concurrent commit wrote meanwhile makes this fail (and the
	// retry picks it up) instead of being overwritten.
	if err := updateRef(repo, NotesRef, newLocal, local); err != nil {
		return err
	}
	if len(publish) == 0 {
		return nil
	}

	// Push the chain's tip by commit, not by ref name, and to the URL, not the
	// remote name: pushing via the name makes git also "update by push" the
	// local ref a notes fetch refspec maps — without the CAS above, dropping a
	// note written during the push.
	//
	// --no-verify is REQUIRED: core.hooksPath is global, so without it this
	// nested push would re-trigger the pre-push hook that called us, forever.
	_, err = runGit(networkTimeout, repo, nil, nil, "push", "--quiet", "--no-verify", target, publishTip+":"+NotesRef)
	return err
}

// buildNotesCommits writes, with one `git fast-import` run, a chain of notes
// commits on parent ("" = a new root) — one per publish entry, carrying that
// change alone with msgs[i] — to publishRef, and, when pending is not empty, one
// more commit on top of it holding every pending change to localRef.
func buildNotesCommits(repo, parent string, publish []noteChange, msgs []string, pending []noteChange, publishRef, localRef string) error {
	ident, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "var", "GIT_COMMITTER_IDENT")
	if err != nil {
		return err
	}
	committer := "committer " + strings.TrimSpace(ident) + "\n"

	var s strings.Builder
	from := ""
	if parent != "" {
		from = "from " + parent + "\n"
	}
	mark := 0
	writeCommit := func(ref, msg string, notes []noteChange) {
		mark++
		fmt.Fprintf(&s, "commit %s\nmark :%d\n%sdata %d\n%s\n%s", ref, mark, committer, len(msg), msg, from)
		for _, n := range notes {
			blob := n.blob
			if blob == "" {
				blob = strings.Repeat("0", len(n.target)) // deletes the note
			}
			fmt.Fprintf(&s, "N %s %s\n", blob, n.target)
		}
		s.WriteString("\n")
		// The next commit builds on this one. (A ref written in this same stream
		// cannot be named yet — fast-import updates refs only at the end — so
		// chain by mark.)
		from = fmt.Sprintf("from :%d\n", mark)
	}
	for i, c := range publish {
		writeCommit(publishRef, msgs[i], []noteChange{c})
	}
	if len(pending) > 0 {
		// Local only: it never leaves this clone as-is.
		writeCommit(localRef, notesSyncMarker+"\n", pending)
	}
	if s.Len() == 0 {
		return nil
	}
	s.WriteString("done\n")
	_, err = runGit(gitutil.DefaultTimeout, repo, strings.NewReader(s.String()), nil,
		"fast-import", "--quiet", "--done")
	return err
}

// fetchRemoteNotes fetches the remote's refs/notes/blamely into dst and returns
// its tip, or "" when the remote has none (git's "couldn't find remote ref"),
// in one round trip. dst is deleted again right away; the objects stay.
//
// --refmap= stops git from ALSO applying the remote's configured fetch
// refspecs. With the common `+refs/notes/*:refs/notes/*` one, this very fetch
// would otherwise force-overwrite the local refs/notes/blamely with the
// remote's and silently drop every note not pushed yet. --no-tags and
// --recurse-submodules=no keep remote.<name>.tagOpt and submodule.recurse from
// turning it into a tag or submodule fetch that can fail for reasons of its own.
//
// --no-write-fetch-head keeps the fetch from overwriting .git/FETCH_HEAD, which
// records what the user's own last `git fetch` brought in — the thing a
// following `git merge FETCH_HEAD` or a paused `git pull` works from. A push
// has no business changing it. git only knows the flag since 2.29; an older
// git rejects it as an unknown option, and then the fetch runs without it and
// FETCH_HEAD is put back by hand.
func fetchRemoteNotes(repo, target, dst string) (string, error) {
	args := func(extra ...string) []string {
		a := append([]string{"fetch", "--quiet", "--no-tags", "--recurse-submodules=no", "--refmap="}, extra...)
		return append(a, target, "+"+NotesRef+":"+dst)
	}
	_, err := runGit(networkTimeout, repo, nil, nil, args(noWriteFetchHeadFlag)...)
	if err != nil && isUnknownOption(err) {
		err = fetchRestoringFetchHead(repo, args())
	}
	if err != nil {
		if strings.Contains(err.Error(), "couldn't find remote ref") {
			return "", nil
		}
		return "", err
	}
	tip := revParse(repo, dst)
	clearScratchRefs(repo, dst)
	return tip, nil
}

// isUnknownOption reports whether err is git's option parser rejecting a flag:
// it exits 129 for that ("error: unknown option `no-write-fetch-head'").
func isUnknownOption(err error) bool {
	var ge *gitError
	return errors.As(err, &ge) && ge.exitCode == 129 && strings.Contains(ge.stderr, "unknown option")
}

// fetchRestoringFetchHead is the fetch for a git without --no-write-fetch-head:
// it lets the fetch write FETCH_HEAD, then restores the user's copy (or removes
// the file if there was none), whether or not the fetch succeeded.
func fetchRestoringFetchHead(repo string, args []string) error {
	path := gitPath(repo, "FETCH_HEAD")
	saved, readErr := os.ReadFile(path)
	_, err := runGit(networkTimeout, repo, nil, nil, args...)
	if path != "" {
		switch {
		case readErr == nil:
			_ = os.WriteFile(path, saved, 0o644)
		case os.IsNotExist(readErr):
			_ = os.Remove(path)
		}
	}
	return err
}

// notesMap reads a notes commit's tree as annotated sha → note blob (empty map
// for ""). Fanned-out paths (ab/cdef…) are folded back into the sha.
func notesMap(repo, commit string) (map[string]string, error) {
	m := map[string]string{}
	if commit == "" {
		return m, nil
	}
	out, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "ls-tree", "-r", commit)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		meta, path, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 || f[1] != "blob" {
			continue
		}
		if sha := strings.ReplaceAll(path, "/", ""); isHexSHA(sha) {
			m[sha] = f[2]
		}
	}
	return m, nil
}

type commitInfo struct {
	time int64 // committer date, unix seconds
	msg  string
}

// readCommits reads the committer date and message of every sha that names a
// commit here, in one `git cat-file --batch` call. The raw object is parsed
// directly, so no log.* setting (log.showSignature prints signature checks
// into `git log` output) can get in the way. Missing objects are skipped
// without a lazy fetch in a partial clone: a gc'd note target was never on the
// server anyway.
func readCommits(repo string, shas []string) map[string]commitInfo {
	info := map[string]commitInfo{}
	if len(shas) == 0 {
		return info
	}
	out, err := runGit(gitutil.DefaultTimeout, repo, strings.NewReader(strings.Join(shas, "\n")+"\n"),
		[]string{"GIT_NO_LAZY_FETCH=1"}, "cat-file", "--batch")
	if err != nil {
		return info
	}
	for len(out) > 0 {
		header, rest, _ := strings.Cut(out, "\n")
		f := strings.Fields(header) // "<sha> <type> <size>" or "<sha> missing"
		if len(f) != 3 {
			out = rest
			continue
		}
		size, err := strconv.Atoi(f[2])
		if err != nil || size > len(rest) {
			break
		}
		body := rest[:size]
		out = strings.TrimPrefix(rest[size:], "\n")
		if f[1] != "commit" {
			continue
		}
		headers, msg, _ := strings.Cut(body, "\n\n")
		var t int64
		for _, h := range strings.Split(headers, "\n") {
			if strings.HasPrefix(h, "committer ") {
				if hf := strings.Fields(h); len(hf) >= 2 {
					t, _ = strconv.ParseInt(hf[len(hf)-2], 10, 64)
				}
			}
		}
		info[f[0]] = commitInfo{time: t, msg: strings.TrimRight(msg, "\n")}
	}
	return info
}

// serverKnown reports which of commits the server has already — reachable from
// the remote's tracking refs — or receives in this push (reachable from the
// pushed tips). Their messages passed, or are passing, the server's rule. Each
// is mapped to its position in a topological walk from those tips (0 =
// newest), so callers can put parents before children.
func serverKnown(repo string, commits map[string]commitInfo, tips []string, remote string) map[string]int {
	known := map[string]int{}
	if len(commits) == 0 {
		return known
	}
	oldest := int64(-1)
	for _, c := range commits {
		if oldest < 0 || c.time < oldest {
			oldest = c.time
		}
	}
	// Only history back to the oldest candidate matters; --max-age keeps the
	// walk from covering a big repo's entire past. A day of slack absorbs clock
	// skew between the machines that made the commits.
	args := []string{"rev-list", "--topo-order", "--max-age=" + strconv.FormatInt(oldest-24*60*60, 10), "--remotes=" + remote}
	args = append(args, tips...)
	out, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, args...)
	if err != nil {
		return known
	}
	for i, sha := range strings.Fields(out) {
		if _, ok := commits[sha]; ok {
			known[sha] = i
		}
	}
	return known
}

func targetsOf(changes []noteChange) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.target
	}
	return out
}

// pushURL resolves where a push to remote goes (its pushurl, else its url); a
// remote given as a URL or path is returned unchanged.
func pushURL(repo, remote string) string {
	if out, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "remote", "get-url", "--push", remote); err == nil {
		if u := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0]); u != "" {
			return u
		}
	}
	return remote
}

// clearScratchRefs deletes every ref under prefix, a ref-name prefix ending at
// a path component (for-each-ref semantics), or the one ref it names.
func clearScratchRefs(repo, prefix string) {
	out, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "for-each-ref", "--format=%(refname)", prefix)
	if err != nil {
		return
	}
	var del strings.Builder
	for _, ref := range strings.Fields(out) {
		fmt.Fprintf(&del, "delete %s\n", ref)
	}
	if del.Len() > 0 {
		_, _ = runGit(gitutil.DefaultTimeout, repo, strings.NewReader(del.String()), nil, "update-ref", "--stdin")
	}
}

// gitPath resolves a path inside the repo's git dir (`git rev-parse
// --git-path`), absolute; "" if git cannot say.
func gitPath(repo, name string) string {
	out, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "rev-parse", "--git-path", name)
	p := strings.TrimSpace(out)
	if err != nil || p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(repo, p) // relative to -C repo, i.e. to repo
	}
	return p
}

// updateRef moves ref to newSHA only if it still points at oldSHA ("" = must
// not exist yet), so a concurrent writer is never silently overwritten.
func updateRef(repo, ref, newSHA, oldSHA string) error {
	_, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "update-ref", ref, newSHA, oldSHA)
	return err
}

func revParse(repo, rev string) string {
	out, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "rev-parse", "--verify", "--quiet", rev)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// mergeBase returns the best common ancestor of a and b, or "" if they share
// no history.
func mergeBase(repo, a, b string) string {
	out, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "merge-base", a, b)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func isAncestor(repo, ancestor, descendant string) bool {
	_, err := runGit(gitutil.DefaultTimeout, repo, nil, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	return err == nil
}

func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// gitError is a failed git command: its exit code and trimmed stderr.
type gitError struct {
	cmd      string
	exitCode int
	stderr   string
}

func (e *gitError) Error() string { return "git " + e.cmd + ": " + e.stderr }

// runGit runs `git -C repo args...` with the given deadline, stdin and extra
// environment, returning stdout. git's messages are pinned to English
// (LC_ALL=C): the sync matches a few of them ("couldn't find remote ref",
// "fetch first"), and a translated git — Git for Windows, Linux distributions
// — would otherwise defeat every such check. Server "remote:" lines are not
// translated locally either way. On failure the error carries git's stderr,
// so a server's rejection reason (e.g. a missing issue key) reaches the user.
func runGit(timeout time.Duration, repo string, stdin *strings.Reader, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := gitutil.Command(ctx, repo, args...)
	cmd.Env = append(append(os.Environ(), "LC_ALL=C", "LANGUAGE="), env...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		ge := &gitError{cmd: args[0], exitCode: -1, stderr: strings.TrimSpace(stderr.String())}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			ge.exitCode = ee.ExitCode()
		}
		if ge.stderr == "" {
			ge.stderr = err.Error()
		}
		return "", ge
	}
	return string(out), nil
}
