package gitnotes

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/authorship"
)

// Phase 2 dual-run (docs/attribution-v2-design.md §10–12): with BLAMELY_ATTRIBUTION_V2
// on, compare the just-built v1 note against the Attribution v2 working-log
// authorship for the SAME added lines and LOG the divergence. This NEVER changes
// the written note — it is the measurement gate before the Phase 3 flip.

type v2Divergence struct {
	Compared  int // added lines that v2 also had a working-log entry for
	Divergent int // of those, how many disagree on AI-vs-Human
	V1AI      int // v1 AI count over the compared lines
	V2AI      int // v2 AI count over the same lines
	Files     int // files compared
	NoLog     int // files with no v2 working log yet (e.g. editor track pending)
}

// computeV2Divergence reads each committed file's working log (keyed by the
// commit's PARENT — HEAD at edit time) and compares its per-line author type to
// the v1 note's added-line attribution. Pure of logging so it is unit-testable.
func computeV2Divergence(repoPath string, note *Note) v2Divergence {
	var d v2Divergence
	if note == nil {
		return d
	}
	parent := commitParentSHA(repoPath, note.Commit)
	if parent == "" {
		return d // initial commit (or unknown) — no edit-time base to load
	}
	for _, fe := range note.Files {
		types, ok := authorship.AuthorTypesForFile(repoPath, note.Branch, parent, fe.Path)
		if !ok {
			d.NoLog++
			continue
		}
		d.Files++
		for _, r := range fe.Lines {
			if r.Type != "add" {
				continue
			}
			for ln := r.Start; ln <= r.End; ln++ {
				v1 := authorship.Human
				if r.AuthorType == "AI" {
					v1 = authorship.AI
				}
				v2 := authorship.Human
				if t, has := types[ln]; has {
					v2 = t
				}
				d.Compared++
				if v1 == authorship.AI {
					d.V1AI++
				}
				if v2 == authorship.AI {
					d.V2AI++
				}
				if v1 != v2 {
					d.Divergent++
				}
			}
		}
	}
	return d
}

// logV2Divergence runs the comparison (when the flag is on) and logs a one-line
// summary per commit. Best-effort; never affects the note.
func logV2Divergence(repoPath string, note *Note) {
	if note == nil || !authorship.Enabled() {
		return
	}
	d := computeV2Divergence(repoPath, note)
	if d.Compared == 0 && d.NoLog == 0 {
		return
	}
	persistDivergence(repoPath, note.Commit, d)
	sha := note.Commit
	if len(sha) > 8 {
		sha = sha[:8]
	}
	log.Printf("attribution-v2 dual-run %s: added_compared=%d divergent=%d (v1_ai=%d v2_ai=%d) files=%d no_log=%d",
		sha, d.Compared, d.Divergent, d.V1AI, d.V2AI, d.Files, d.NoLog)
}

// DivergenceRecord is one commit's dual-run comparison, appended to the per-repo
// divergence log so the §12 soak gate is measurable (see `blamely attribution-status`).
type DivergenceRecord struct {
	Commit    string `json:"commit"`
	TimeMS    int64  `json:"time_ms"`
	Compared  int    `json:"compared"`
	Divergent int    `json:"divergent"`
	V1AI      int    `json:"v1_ai"`
	V2AI      int    `json:"v2_ai"`
	Files     int    `json:"files"`
	NoLog     int    `json:"no_log"`
}

// DivergenceLogPath is the per-repo JSONL file of dual-run records.
func DivergenceLogPath(repoPath string) string {
	return filepath.Join(repoPath, ".git", "blamely", "v2-divergence.jsonl")
}

// persistDivergence appends one record to the per-repo divergence log (best-effort;
// a logging failure must never affect attribution).
func persistDivergence(repoPath, commit string, d v2Divergence) {
	rec := DivergenceRecord{
		Commit: commit, TimeMS: time.Now().UnixMilli(),
		Compared: d.Compared, Divergent: d.Divergent, V1AI: d.V1AI, V2AI: d.V2AI,
		Files: d.Files, NoLog: d.NoLog,
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	path := DivergenceLogPath(repoPath)
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// DivergenceSummary aggregates the per-repo divergence log for the §12 soak gate.
type DivergenceSummary struct {
	Commits        int
	ComparedLines  int
	DivergentLines int
	NoLogFiles     int
	Recent         []DivergenceRecord // most recent divergent commits (to inspect)
}

// ReadDivergenceSummary parses the per-repo divergence log (JSONL) into a summary so
// the soak is measurable: agreement %, and the divergent commits to inspect (each
// should be v2 correcting v1). Missing log → zero summary, not an error.
func ReadDivergenceSummary(repoPath string, recentN int) (DivergenceSummary, error) {
	var s DivergenceSummary
	f, err := os.Open(DivergenceLogPath(repoPath))
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return s, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var r DivergenceRecord
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		s.Commits++
		s.ComparedLines += r.Compared
		s.DivergentLines += r.Divergent
		s.NoLogFiles += r.NoLog
		if r.Divergent > 0 {
			s.Recent = append(s.Recent, r)
		}
	}
	if recentN >= 0 && len(s.Recent) > recentN {
		s.Recent = s.Recent[len(s.Recent)-recentN:]
	}
	return s, sc.Err()
}

// commitParentSHA returns sha's first parent, or "" for a root commit / on error.
func commitParentSHA(repoPath, sha string) string {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "-q", sha+"^").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
