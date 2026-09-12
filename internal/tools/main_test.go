package tools

import (
	"os"
	"strings"
	"testing"

	"github.com/blamely/blamely/internal/authorship"
	"github.com/blamely/blamely/internal/gitutil"
)

// TestMain wires up the SeedHook that production registers, for the whole package.
//
// In production the git-notes layer sets authorship.SeedHook, so a file's working
// log is seeded from COMMITTED authorship before its first observed edit. In a test
// binary that layer is never linked, SeedHook stays nil, and the entire seeding
// step silently does not happen. A test can then pass while the shipped binary
// fails — which is not hypothetical: authorship.SeedWorkingLog was overwriting the
// pre-edit baseline captured by the pre-hook, the tests here were green, and the
// bug was only caught by running the real binary against a real repo.
//
// The stand-in seeds from the file's content at HEAD, attributed to Human, which is
// what a commit carrying no AI note produces. A test that needs specific committed
// authorship overrides it (withCommitSeedHook).
func TestMain(m *testing.M) {
	authorship.SeedHook = seedFromHeadAsHuman
	os.Exit(m.Run())
}

// seedFromHeadAsHuman is the package's default SeedHook: seed relPath's working log
// from its committed content, all Human. A no-op for a file that is not in the
// commit (a new file has nothing committed to carry forward).
func seedFromHeadAsHuman(repoRoot, branch, baseSHA, relPath string) {
	if baseSHA == "" || baseSHA == "INITIAL" {
		return
	}
	out, err := gitutil.Output(repoRoot, "show", baseSHA+":"+relPath)
	if err != nil {
		return
	}
	content := string(out)
	if strings.TrimSpace(content) == "" {
		return
	}
	n := len(strings.Split(strings.TrimRight(content, "\n"), "\n"))
	_ = authorship.SeedWorkingLog(repoRoot, branch, baseSHA, relPath, content,
		[]authorship.LineAttribution{
			{Start: 1, End: n, Author: authorship.HumanAuthor()},
		}, 0)
}
