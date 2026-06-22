package authorship

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/blamely/blamely/internal/gitutil"
)

// capture.go is the high-level entry every observed edit funnels through (CLI
// hooks, daemon watchers, editor plugin via the daemon). It resolves the
// working-log key from git and routes to the engine in store.go / attribute.go.
// Cross-platform: only git + path/filepath, no OS-specific calls.

// Context is the working-log key for a file: which repo/branch/base_sha it belongs
// to, plus the forward-slashed repo-relative path.
type Context struct {
	RepoRoot string
	Branch   string
	BaseSHA  string
	RelPath  string
}

// ResolveContext derives the Context for an absolute file path using git, or
// ok=false if the path is not inside a work tree. Detached HEAD and the
// pre-first-commit state get stable sentinel keys so the working log still has a
// home (and rotates when a real branch/commit appears).
func ResolveContext(absPath string) (Context, bool) {
	// Canonicalize symlinks on both sides before computing the relative path:
	// `git rev-parse --show-toplevel` returns the real path, so if the repo lives
	// under a symlinked prefix (e.g. macOS /var → /private/var, or any symlinked
	// checkout on Linux/Windows) an un-resolved absPath would make filepath.Rel
	// produce a "../.." path and wrongly look outside the work tree.
	if resolved, err := filepath.EvalSymlinks(absPath); err == nil {
		absPath = resolved
	}
	top, ok := gitutil.Toplevel(absPath)
	if !ok || top == "" {
		return Context{}, false
	}
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = resolved
	}
	rel, err := filepath.Rel(top, absPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return Context{}, false
	}
	branch := gitutil.BranchName(top)
	if branch == "" {
		branch = "DETACHED"
	}
	base := gitutil.HeadSHA(top)
	if base == "" {
		base = "INITIAL" // repo has no commits yet
	}
	return Context{RepoRoot: top, Branch: branch, BaseSHA: base, RelPath: filepath.ToSlash(rel)}, true
}

// RecordEdit captures an observed POST-edit: it reads the file's current content
// and updates its working log, attributing the genuinely-changed lines to author.
// The first observed edit (no stored baseline) diffs against the file's HEAD
// content, so pre-existing committed lines are not mis-credited to the author.
func RecordEdit(absPath string, author Author) (*WorkingLog, error) {
	ctx, ok := ResolveContext(absPath)
	if !ok {
		return nil, fmt.Errorf("authorship: %q is not inside a git work tree", absPath)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	fallback := headContent(ctx.RepoRoot, ctx.BaseSHA, ctx.RelPath)
	return Update(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath, string(data), fallback, author, 0)
}

// CaptureBaseline records the file's CURRENT content as the pre-edit baseline —
// the `record --pre` fallback (Decision B). Call it before a terminal agent writes
// a file the editor isn't tracking live; the next RecordEdit diffs against it.
func CaptureBaseline(absPath string) error {
	ctx, ok := ResolveContext(absPath)
	if !ok {
		return fmt.Errorf("authorship: %q is not inside a git work tree", absPath)
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return err
	}
	return PutBaseline(ctx.RepoRoot, ctx.Branch, ctx.BaseSHA, ctx.RelPath, string(data))
}

// headContent returns relPath's content at baseSHA, or "" if the file is new
// (not in HEAD) or there is no commit yet. Used only as the first-edit fallback
// baseline; a real stored baseline always takes precedence in Update.
func headContent(repoRoot, baseSHA, relPath string) string {
	if baseSHA == "" || baseSHA == "INITIAL" {
		return ""
	}
	// git uses forward slashes in tree paths on every OS.
	out, err := exec.Command("git", "-C", repoRoot, "show", baseSHA+":"+relPath).Output()
	if err != nil {
		return "" // file not present at HEAD → treat as new
	}
	return string(out)
}
