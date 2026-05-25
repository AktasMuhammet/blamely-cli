package tools

// VelocityWatcher uses fsnotify to detect AI inline completions — the "ghost
// text" suggestions that Copilot and Cursor Tab insert when the user presses
// Tab/Enter. These completions fire no PostToolUse hook; the only observable
// signal is a sudden large burst of text appearing in a file.
//
// Detection heuristic:
//   1. Watch every git repo the daemon has already seen (repo_path rows in DB).
//   2. On each file-change event, diff the on-disk content against the last
//      known hash. If ≥ minCompletionLines new lines were added in ≤ maxElapsed,
//      it's a completion candidate.
//   3. Attribute to `copilot, gen_type=completion` if a Copilot session marker
//      was recorded within the last copilotWindow, else to `cursor,
//      gen_type=completion` if Cursor.app is running, else drop the event
//      (a fast human typist should not be misattributed).
//
// Confidence is intentionally "low" — heuristics can misfire.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/daemon"
	"github.com/blamely/blamely/internal/gitutil"
	"github.com/blamely/blamely/internal/store"
)

const (
	minCompletionLines = 3           // fewer added lines → probably manual
	maxElapsed         = time.Second // completion inserts appear almost instantly
	copilotWindow      = 120 * time.Second
	velocityCooldown   = 2 * time.Second // ignore subsequent events on the same file
)

// VelocityWatcher detects AI inline completions via fsnotify.
type VelocityWatcher struct {
	// DB is required for querying Copilot session markers and known repos.
	DB *store.DB
}

func (v *VelocityWatcher) Name() string { return "velocity" }

type fileRecord struct {
	sha      string    // sha256 of last known content
	lastSeen time.Time // time of last event on this file
}

func (v *VelocityWatcher) Run(ctx context.Context, sink daemon.Sink) error {
	if v.DB == nil {
		return fmt.Errorf("velocity watcher: DB is required")
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("velocity watcher: %w", err)
	}
	defer w.Close()

	mu := sync.Mutex{}
	fileState := map[string]*fileRecord{}

	// Seed initial repo dirs from the DB (repos we already know about).
	watchedDirs := map[string]bool{}
	v.addKnownRepos(w, watchedDirs)

	// Refresh watched dirs every 30s to pick up newly-seen repos.
	refreshTick := time.NewTicker(30 * time.Second)
	defer refreshTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-refreshTick.C:
			v.addKnownRepos(w, watchedDirs)

		case evt, ok := <-w.Events:
			if !ok {
				return nil
			}
			if evt.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			go v.handleChange(ctx, evt.Name, sink, &mu, fileState)

		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("velocity watcher error: %v", err)
		}
	}
}

func (v *VelocityWatcher) addKnownRepos(w *fsnotify.Watcher, watched map[string]bool) {
	repos, err := v.DB.KnownRepoPaths()
	if err != nil {
		return
	}
	for _, repo := range repos {
		if watched[repo] {
			continue
		}
		if err := w.Add(repo); err == nil {
			watched[repo] = true
		}
	}
}

func (v *VelocityWatcher) handleChange(
	ctx context.Context,
	path string,
	sink daemon.Sink,
	mu *sync.Mutex,
	state map[string]*fileRecord,
) {
	// Only track source files likely to be in a git repo.
	if !looksLikeSourceFile(path) {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	newSHA := sha256Sum(data)
	now := time.Now()

	mu.Lock()
	rec := state[path]
	if rec == nil {
		// First time seeing this file — prime without emitting.
		state[path] = &fileRecord{sha: newSHA, lastSeen: now}
		mu.Unlock()
		return
	}
	if rec.sha == newSHA {
		mu.Unlock()
		return // no actual content change
	}
	elapsed := now.Sub(rec.lastSeen)
	oldSHA := rec.sha
	rec.sha = newSHA
	rec.lastSeen = now
	mu.Unlock()

	if elapsed < velocityCooldown || elapsed > maxElapsed {
		return // too slow to be a completion, or too soon after the last event
	}
	_ = oldSHA

	addedLines := countNewLines(data)
	if addedLines < minCompletionLines {
		return
	}

	abs := path
	if r, err := filepath.EvalSymlinks(path); err == nil {
		abs = r
	}
	repoID, _ := gitutil.RepoID(abs)
	wt, _ := gitutil.Toplevel(abs)
	rel := abs
	if wt != "" {
		if r, err := filepath.Rel(wt, abs); err == nil && !strings.HasPrefix(r, "..") {
			rel = r
		}
	}

	tool, genType := v.classifySource(ctx, repoID)
	if tool == "" {
		return // can't attribute — skip
	}
	lr, err := LineRangeForWholeFile(abs)
	if err != nil || lr == nil {
		return
	}

	ev := daemon.Event{
		When:       now,
		Tool:       tool,
		Confidence: "low",
		GenType:    genType,
		RepoPath:   repoID,
		FilePath:   rel,
		Lines:      []daemon.LineRange{{Start: lr.Start, End: lr.End, ContentSHA: lr.ContentSHA}},
		RawMeta:    `{"source":"velocity_detector"}`,
	}
	if err := sink.Record(ev); err != nil {
		log.Printf("velocity sink: %v", err)
	}
}

// classifySource decides which AI tool caused the velocity event.
// Returns ("", "") if we can't make a reliable attribution.
func (v *VelocityWatcher) classifySource(ctx context.Context, repoID string) (tool, genType string) {
	// 1. Copilot: was a Copilot session active recently?
	if v.DB.HasCopilotSessionNear(repoID, time.Now().UnixNano(), int64(copilotWindow)) {
		return "copilot", "completion"
	}
	// 2. Cursor tab completion: is Cursor.app running?
	if cursorIsRunning() {
		return "cursor", "completion"
	}
	return "", ""
}

func cursorIsRunning() bool {
	// Check for Cursor process. pgrep is cross-platform enough for our needs.
	switch {
	case commandExists("pgrep"):
		err := exec.Command("pgrep", "-x", "Cursor").Run()
		return err == nil
	default:
		return false
	}
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func looksLikeSourceFile(p string) bool {
	// Skip hidden files, binaries, and common non-code extensions.
	base := filepath.Base(p)
	if strings.HasPrefix(base, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(p))
	skip := map[string]bool{
		".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
		".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".exe": true,
		".bin": true, ".dll": true, ".so": true, ".dylib": true, ".db": true,
		".sqlite": true, ".sqlite3": true,
	}
	return !skip[ext]
}

func countNewLines(data []byte) int {
	count := 0
	for _, b := range data {
		if b == '\n' {
			count++
		}
	}
	return count
}

func sha256Sum(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// homeDir returns the user's home directory (best-effort).
func homeDir() string {
	h, _ := config.Home()
	return h
}
