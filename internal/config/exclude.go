package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExcludeList matches repo-relative file paths against patterns loaded from
// ~/.blamely/exclude. The diff parser consults it on every `diff --git`
// header and skips the whole file (no FileChanges, no Added, no Deleted)
// when a pattern matches. The result is identical to the file not existing
// in the diff at all — it never appears in attribution or reports.
type ExcludeList struct {
	rules []excludeRule
}

// excludeRule is one parsed line. Exactly one field is populated; the
// matcher checks them all per call.
type excludeRule struct {
	component      string // bare name: match if any '/'-separated path component equals this
	glob           string // basename glob: match if filepath.Match(glob, basename)
	anchoredPrefix string // `/foo` or `/foo/`: path equals this or starts with prefix+'/'
	anySlashPath   string // `foo/bar` (non-anchored, contains slash): match anywhere in path
}

// LoadExcludeList reads ~/.blamely/exclude. If the file doesn't exist it is
// created with DefaultExcludeContent. Parse errors on individual lines are
// surfaced but never fatal — bad lines are skipped, good lines applied.
func LoadExcludeList() (*ExcludeList, error) {
	path, err := ExcludeFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read exclude file %s: %w", path, err)
		}
		// First run: lay down the default so the user can edit it. We
		// proceed with the default content in-memory even if the write
		// fails (e.g. read-only home) — exclusion should still work.
		if writeErr := writeDefaultExclude(path); writeErr != nil {
			return parseExclude(DefaultExcludeContent), nil
		}
		data = []byte(DefaultExcludeContent)
	}
	return parseExclude(string(data)), nil
}

// LoadExcludeListFrom loads from an explicit path. Used by tests and by the
// install step that needs to verify the freshly-written file.
func LoadExcludeListFrom(path string) (*ExcludeList, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseExclude(string(data)), nil
}

// LoadExcludeListForRepo merges the global ~/.blamely/exclude with the
// repo's own .gitignore and .git/info/exclude so attribution skips both
// the user's universal defaults AND whatever the repo already declares as
// ignored. Anything that .gitignore would keep out of a fresh clone is
// also kept out of Blamely's reports.
//
// Negation (`!pattern`) lines in .gitignore aren't honoured — we treat
// every pattern as a positive exclude. Sub-directory .gitignore files
// aren't walked either; the top-level file and the global list together
// cover the build outputs and lockfiles 99% of projects care about.
func LoadExcludeListForRepo(repoPath string) (*ExcludeList, error) {
	global, err := LoadExcludeList()
	if err != nil {
		return nil, err
	}
	merged := append([]excludeRule(nil), global.rules...)
	if repoPath != "" {
		for _, rel := range []string{
			filepath.Join(repoPath, ".gitignore"),
			filepath.Join(repoPath, ".git", "info", "exclude"),
		} {
			merged = append(merged, loadRulesFromFile(rel)...)
		}
	}
	return &ExcludeList{rules: merged}, nil
}

func loadRulesFromFile(path string) []excludeRule {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseExclude(string(data)).rules
}

func writeDefaultExclude(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(DefaultExcludeContent), 0o644)
}

// EnsureDefaultExcludeFile writes the default exclude file to its standard
// location if it doesn't already exist. Returns the absolute path and
// whether the file was newly created. Called from `blamely install` so
// fresh installs always have a sensible default the user can edit.
func EnsureDefaultExcludeFile() (path string, created bool, err error) {
	path, err = ExcludeFile()
	if err != nil {
		return "", false, err
	}
	if _, err := os.Stat(path); err == nil {
		return path, false, nil
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	if err := writeDefaultExclude(path); err != nil {
		return "", false, err
	}
	return path, true, nil
}

func parseExclude(content string) *ExcludeList {
	list := &ExcludeList{}
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Gitignore negation (`!pattern`) re-includes a previously-excluded
		// path. We don't model un-exclude, so the safest behaviour is to
		// drop these lines rather than build a useless rule.
		if strings.HasPrefix(line, "!") {
			continue
		}
		if r, ok := parseExcludeLine(line); ok {
			list.rules = append(list.rules, r)
		}
	}
	return list
}

// parseExcludeLine turns one non-blank, non-comment line into a rule.
// Returns (zero, false) for unparseable lines (e.g. just "/").
func parseExcludeLine(line string) (excludeRule, bool) {
	anchored := strings.HasPrefix(line, "/")
	if anchored {
		line = strings.TrimPrefix(line, "/")
	}
	// Trailing slash is informational ("this is a directory") — we treat
	// `target` and `target/` identically.
	line = strings.TrimSuffix(line, "/")
	if line == "" {
		return excludeRule{}, false
	}

	hasGlob := strings.ContainsAny(line, "*?[")
	hasSlash := strings.Contains(line, "/")

	switch {
	case anchored:
		// `/foo` or `/foo/`: matches at repo root only. We don't need to
		// distinguish file from dir — the matcher checks both forms.
		return excludeRule{anchoredPrefix: line}, true
	case hasGlob && !hasSlash:
		// Pure basename glob: `*.class`, `*.min.js`.
		return excludeRule{glob: line}, true
	case !hasSlash:
		// Bare name: matches as any '/'-separated path component.
		return excludeRule{component: line}, true
	default:
		// Non-anchored slash pattern (e.g. `foo/bar`). Matches anywhere
		// in the path — `app/foo/bar/x.txt` qualifies, but so does
		// `foo/bar/x.txt` at the root.
		return excludeRule{anySlashPath: line}, true
	}
}

// Match reports whether the given repo-relative path is excluded. Path
// separators are normalised to '/' so the same patterns work on Windows.
// We explicitly replace '\\' (not just rely on filepath.ToSlash, which is
// a no-op on POSIX) so a Windows-style path supplied by a non-Windows host
// — e.g. test fixtures — still matches.
func (e *ExcludeList) Match(repoRelPath string) bool {
	if e == nil || len(e.rules) == 0 || repoRelPath == "" {
		return false
	}
	p := strings.ReplaceAll(filepath.ToSlash(repoRelPath), "\\", "/")
	p = strings.TrimPrefix(p, "./")
	base := pathBase(p)
	components := strings.Split(p, "/")

	for _, r := range e.rules {
		switch {
		case r.component != "":
			for _, c := range components {
				if c == r.component {
					return true
				}
			}
		case r.glob != "":
			if ok, _ := filepath.Match(r.glob, base); ok {
				return true
			}
		case r.anchoredPrefix != "":
			// Anchored at root: equals the prefix or starts with prefix+'/'.
			if p == r.anchoredPrefix || strings.HasPrefix(p, r.anchoredPrefix+"/") {
				return true
			}
		case r.anySlashPath != "":
			// Non-anchored slash path: appears as a sub-sequence of the
			// '/'-bounded path. Wrapping both sides in '/' avoids matching
			// a prefix substring (e.g. `foo/bar` should not match
			// `foobar/x`).
			if strings.Contains("/"+p+"/", "/"+r.anySlashPath+"/") {
				return true
			}
		}
	}
	return false
}

// Patterns returns the raw rule list for tests / introspection.
func (e *ExcludeList) Patterns() int {
	if e == nil {
		return 0
	}
	return len(e.rules)
}

func pathBase(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
