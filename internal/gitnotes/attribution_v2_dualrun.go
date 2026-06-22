package gitnotes

import (
	"log"
	"os/exec"
	"strings"

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
	sha := note.Commit
	if len(sha) > 8 {
		sha = sha[:8]
	}
	log.Printf("attribution-v2 dual-run %s: added_compared=%d divergent=%d (v1_ai=%d v2_ai=%d) files=%d no_log=%d",
		sha, d.Compared, d.Divergent, d.V1AI, d.V2AI, d.Files, d.NoLog)
}

// commitParentSHA returns sha's first parent, or "" for a root commit / on error.
func commitParentSHA(repoPath, sha string) string {
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "-q", sha+"^").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
