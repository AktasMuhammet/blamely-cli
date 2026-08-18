package gitutil

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxChildRepoDepth bounds how deep DiscoverRepos descends looking for nested
// repositories. Depth 1 covers the common `workspace/{backend,frontend}` case;
// 3 also covers a grouping level (`workspace/services/api`) without turning the
// scan into a full filesystem walk.
const maxChildRepoDepth = 3

// maxChildRepos caps how many repositories a single scan reports. A directory
// holding dozens of clones is a checkout root, not a project — aggregating all
// of them would be slow and meaningless, so we stop rather than guess.
const maxChildRepos = 25

// maxScanDirs bounds the total directories a scan may open, so a pathological
// tree (thousands of shallow directories) can't stall a hook or a report.
const maxScanDirs = 4000

// skipScanDirs are directory names never worth descending into when looking for
// nested repos: dependency and build trees hold no source we attribute, but can
// hold enormous numbers of directories (and even vendored .git dirs).
var skipScanDirs = map[string]bool{
	"node_modules": true, "vendor": true, "target": true, "build": true,
	"dist": true, "out": true, "bin": true, "obj": true, "coverage": true,
	"venv": true, "Pods": true, "DerivedData": true, "__pycache__": true,
	"tmp": true, "temp": true,
}

// DiscoverRepos returns the git repository roots relevant to `dir`.
//
// When `dir` is itself inside a work tree the answer is that one repo — the
// historical behaviour of every caller, unchanged.
//
// When it is NOT (the case that motivated this: an agent or an IDE opened at
// `~/Project`, whose `backend/` and `frontend/` are separate clones), `git
// rev-parse` is no help: it only ever searches UPWARD. So we look DOWN instead,
// a bounded scan for repos nested beneath `dir`, and callers operate on each.
// Without this, everything keyed off the cwd — the shell-write capture paths and
// every repo-scoped report — silently finds nothing at all.
//
// Returns nil when `dir` is neither in a repo nor above any. The result is
// sorted, so callers render in a stable order.
func DiscoverRepos(dir string) []string {
	if dir == "" {
		return nil
	}
	if root, ok := Toplevel(dir); ok {
		return []string{root}
	}
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	var (
		found   []string
		scanned int
	)
	var walk func(d string, depth int)
	walk = func(d string, depth int) {
		if depth > maxChildRepoDepth || len(found) >= maxChildRepos || scanned >= maxScanDirs {
			return
		}
		scanned++
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		// A .git entry (directory for a clone, file for a worktree/submodule)
		// makes this a repo root: record it and do NOT descend — nested repos
		// inside a work tree are submodules, already covered by their parent.
		for _, e := range entries {
			if e.Name() == ".git" {
				if root, ok := Toplevel(d); ok {
					found = append(found, root)
				}
				return
			}
		}
		for _, e := range entries {
			// IsDir is false for a symlink, which is what we want: following one
			// can escape the scan root or loop, and a symlinked repo is still
			// reachable by its real path.
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if strings.HasPrefix(name, ".") || skipScanDirs[name] {
				continue
			}
			walk(filepath.Join(d, name), depth+1)
		}
	}
	walk(dir, 0)
	if len(found) == 0 {
		return nil
	}
	sort.Strings(found)
	return dedupeStrings(found)
}

func dedupeStrings(in []string) []string {
	out := in[:0]
	var prev string
	for i, s := range in {
		if i > 0 && s == prev {
			continue
		}
		prev = s
		out = append(out, s)
	}
	return out
}
