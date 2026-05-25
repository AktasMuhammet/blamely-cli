package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseExclude_Match_ComponentName(t *testing.T) {
	// `target` (no slash, no glob) matches any path component named target.
	list := parseExclude("target\nnode_modules\n")
	cases := map[string]bool{
		"target/foo.class":             true,
		"app/target/build.log":         true,
		"src/main/java/Foo.java":       false,
		"node_modules/pkg/index.js":    true,
		"src/node_modules_test/foo.go": false, // substring shouldn't match
		"":                             false,
	}
	for path, want := range cases {
		if got := list.Match(path); got != want {
			t.Errorf("Match(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestParseExclude_Match_BasenameGlob(t *testing.T) {
	list := parseExclude("*.class\n*.min.js\n")
	cases := map[string]bool{
		"Foo.class":                       true,
		"src/main/java/Foo.class":         true,
		"src/main/java/Foo.classification": false, // .classification ≠ .class
		"vendor/jquery.min.js":            true,
		"src/app.js":                      false,
	}
	for path, want := range cases {
		if got := list.Match(path); got != want {
			t.Errorf("Match(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestParseExclude_Match_AnchoredPrefix(t *testing.T) {
	// Leading `/` anchors at repo root.
	list := parseExclude("/target\n/build/\n")
	cases := map[string]bool{
		"target":                  true, // exact match
		"target/Foo.class":        true, // prefix match
		"app/target/Foo.class":    false, // anchored — must start at root
		"build/x":                 true,
		"src/build/x":             false,
	}
	for path, want := range cases {
		if got := list.Match(path); got != want {
			t.Errorf("Match(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestParseExclude_Match_TrailingSlashSameAsBareName(t *testing.T) {
	// `target/` should behave identically to `target` — both are dir-style
	// component matches.
	bare := parseExclude("target\n")
	withSlash := parseExclude("target/\n")
	for _, p := range []string{"target/foo", "app/target/foo", "src/file.go"} {
		if bare.Match(p) != withSlash.Match(p) {
			t.Errorf("trailing slash changed semantics for %q: bare=%v withSlash=%v",
				p, bare.Match(p), withSlash.Match(p))
		}
	}
}

func TestParseExclude_SkipsCommentsBlanksAndNegation(t *testing.T) {
	list := parseExclude(`
# comment
   # indented comment
target
!keepme

build/
`)
	// 2 valid rules expected: target and build.
	if got := list.Patterns(); got != 2 {
		t.Errorf("rule count: want 2, got %d", got)
	}
	if !list.Match("target/x") || !list.Match("build/y") {
		t.Error("expected target/x and build/y to be excluded")
	}
	// `!keepme` was dropped, so a path containing `keepme` is NOT matched
	// by an inverse rule (we just don't model negation).
	if list.Match("keepme/x") {
		t.Error("did not expect keepme/x to match — `!` lines are skipped")
	}
}

func TestParseExclude_WindowsPathsNormalised(t *testing.T) {
	list := parseExclude("node_modules\n*.class\n")
	if !list.Match(`app\node_modules\foo.js`) {
		t.Error("expected backslash-separated path to match node_modules")
	}
	if !list.Match(`src\main\Foo.class`) {
		t.Error("expected backslash-separated path to match *.class")
	}
}

func TestLoadExcludeListForRepo_MergesGitignore(t *testing.T) {
	// Sandbox HOME so LoadExcludeList writes/reads our temp dir, not the
	// developer's real ~/.blamely.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows fallback path

	repo := t.TempDir()
	// Repo .gitignore adds project-specific excludes.
	gitignore := "secret/\n*.local\n"
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(gitignore), 0o644); err != nil {
		t.Fatal(err)
	}

	list, err := LoadExcludeListForRepo(repo)
	if err != nil {
		t.Fatalf("LoadExcludeListForRepo: %v", err)
	}

	// Global default catches node_modules; repo .gitignore catches secret/
	// and *.local.
	cases := map[string]bool{
		"node_modules/index.js": true, // from global default
		"secret/foo.txt":        true, // from .gitignore
		"app/config.local":      true, // from .gitignore
		"src/main.go":           false,
	}
	for path, want := range cases {
		if got := list.Match(path); got != want {
			t.Errorf("merged Match(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestEnsureDefaultExcludeFile_CreatesOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	path, created, err := EnsureDefaultExcludeFile()
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if !created {
		t.Error("first call: created should be true")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file should exist: %v", err)
	}

	// Mutate the file so we can prove the second call doesn't overwrite.
	customContent := []byte("# custom user content\nmyapp/build/\n")
	if err := os.WriteFile(path, customContent, 0o644); err != nil {
		t.Fatal(err)
	}

	_, created2, err := EnsureDefaultExcludeFile()
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if created2 {
		t.Error("second call: created should be false (file already exists)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(customContent) {
		t.Error("custom content was overwritten — install should never clobber user edits")
	}
}

func TestLoadExcludeList_ReturnsDefaultContentOnFirstRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	list, err := LoadExcludeList()
	if err != nil {
		t.Fatalf("LoadExcludeList: %v", err)
	}
	// The default content excludes target/ and node_modules/ — sanity-check
	// that we at least got the defaults parsed into rules.
	if !list.Match("target/foo.class") {
		t.Error("default exclude list should match target/foo.class")
	}
	if !list.Match("node_modules/pkg/x.js") {
		t.Error("default exclude list should match node_modules/...")
	}
	// And the file should have been written to disk for the user to edit.
	path, _ := ExcludeFile()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("default file should have been created: %v", err)
	}
}
