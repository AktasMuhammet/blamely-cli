package gitnotes

import (
	"os/exec"
	"strings"

	"github.com/blamely/blamely/internal/procattr"
)

// commitParentSHA returns sha's first parent, or "" for a root commit / on error.
// The Attribution flips key the working log by the commit's PARENT (HEAD at edit
// time), so they resolve it through here.
func commitParentSHA(repoPath, sha string) string {
	out, err := procattr.Hide(exec.Command("git", "-C", repoPath, "rev-parse", "--verify", "-q", sha+"^")).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
