package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCoworkMutationBasenames(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []string
	}{
		{
			"rm spawn",
			`[KERNEL] 2026/06/12 22:48:32 [process:oneshot-4a38] spawn: name=sweet-optimistic-brown cmd=bash args=[-c rm /sessions/sweet-optimistic-brown/mnt/intellij-test/test1.html && echo "done"] home=/sessions/sweet-optimistic-brown uid=1002`,
			[]string{"intellij-test"},
		},
		{
			"read-only command is ignored",
			`[KERNEL] spawn: name=s cmd=bash args=[-c ls /sessions/s/mnt/intellij-test/] home=/sessions/s`,
			nil,
		},
		{
			"not a spawn line",
			`[KERNEL] 2026/06/12 22:48:32 [process:oneshot] removed srt-settings /etc/srt-settings/oneshot.json`,
			nil,
		},
		{
			"write via tee",
			`spawn: cmd=bash args=[-c tee /sessions/x/mnt/my-repo/index.html]`,
			[]string{"my-repo"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := coworkMutationBasenames(c.line)
			if len(got) != len(c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("got %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestParseClaudeDesktopMounts(t *testing.T) {
	mainLog := `2026-06-10 23:51:46 [info] LocalSessions.checkTrust: cwd=/Users/dev/work/intellij-test
2026-06-10 23:52:57 [info] [FileWatching] Starting file watcher for session local_a98: /Users/dev/other/my-repo
2026-06-10 23:52:24 [info] [Spaces] Created space: 2d855a07 (intellij-test)
some unrelated line`
	m := parseClaudeDesktopMounts(mainLog)
	if got := m["intellij-test"]; got != "/Users/dev/work/intellij-test" {
		t.Errorf("intellij-test -> %q", got)
	}
	if got := m["my-repo"]; got != "/Users/dev/other/my-repo" {
		t.Errorf("my-repo -> %q", got)
	}
}

// TestGitDeletedFilesAndHeadHashes builds a throwaway repo, commits a file,
// deletes it, and verifies the deletion is discoverable and its HEAD content is
// hashable — the data the AI-deletion recording depends on.
func TestGitDeletedFilesAndHeadHashes(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "page.html"), []byte("<h1>hi</h1>\n<p>x</p>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "add")
	if err := os.Remove(filepath.Join(root, "page.html")); err != nil {
		t.Fatal(err)
	}

	del := gitDeletedFiles(root)
	if len(del) != 1 || del[0] != "page.html" {
		t.Fatalf("gitDeletedFiles = %v, want [page.html]", del)
	}
	hashes := headFileRemovedHashes(root, "page.html")
	if len(hashes) != 2 { // two non-blank lines
		t.Fatalf("headFileRemovedHashes = %d hashes, want 2", len(hashes))
	}
	for _, h := range hashes {
		if h.ContentSHA == "" {
			t.Error("removed-line hash missing content_sha")
		}
	}
}

func TestCoworkRmTargetsAndSplit(t *testing.T) {
	line := `[KERNEL] 2026/06/12 23:11:06 [process:oneshot-dd9e8dda-4dd8-4996] spawn: name=s cmd=bash args=[-c rm /sessions/s/mnt/intellij-test/form-utils.js && echo "done"] home=/sessions/s`
	tg := coworkRmTargets(line)
	if len(tg) != 1 || tg[0] != "/sessions/s/mnt/intellij-test/form-utils.js" {
		t.Fatalf("targets = %v", tg)
	}
	base, rel, ok := splitVMPath(tg[0])
	if !ok || base != "intellij-test" || rel != "form-utils.js" {
		t.Fatalf("split = %q %q %v", base, rel, ok)
	}
	if id := spawnID(line); id != "oneshot-dd9e8dda-4dd8-4996" {
		t.Errorf("spawnID = %q", id)
	}
	// glob target
	g := coworkRmTargets(`spawn: args=[-c rm /sessions/s/mnt/repo/test*.html]`)
	if len(g) != 1 || g[0] != "/sessions/s/mnt/repo/test*.html" {
		t.Errorf("glob target = %v", g)
	}
}

func TestParseCoworkMountLine(t *testing.T) {
	line := `[KERNEL] 2026/06/13 21:38:56 [coworkd] mounting subpath for user cool-laughing-goldberg (uid=1003, mode=rw): /mnt/.virtiofs-root/shared/Users/abdulkerimatik/development/training/test/claude -> /sessions/cool-laughing-goldberg/mnt/claude`
	base, host, ok := parseCoworkMountLine(line)
	if !ok {
		t.Fatal("expected the mounting line to parse")
	}
	if base != "claude" {
		t.Errorf("base = %q, want claude", base)
	}
	if host != "/Users/abdulkerimatik/development/training/test/claude" {
		t.Errorf("host = %q (virtiofs /shared prefix not stripped)", host)
	}
}

func TestParseCoworkMountLine_NonMountLine(t *testing.T) {
	if _, _, ok := parseCoworkMountLine(`[KERNEL] foo spawn: cmd=bash args=[-c ls]`); ok {
		t.Error("a non-mount line must not parse as a mount")
	}
}

func TestParseCoworkUnmountBase(t *testing.T) {
	line := `[KERNEL] 2026/06/13 21:57:02 [process:oneshot-cffb] unmounting /sessions/cool-laughing-goldberg/mnt/claude`
	base, ok := parseCoworkUnmountBase(line)
	if !ok || base != "claude" {
		t.Fatalf("unmount base = %q ok=%v, want claude", base, ok)
	}
}

// TestClaudeDesktop_MountTracking checks the lifecycle: a mounting line learns
// the mount→host map and marks the repo active; an unmounting line clears it.
func TestClaudeDesktop_MountTracking(t *testing.T) {
	c := &ClaudeDesktopWatcher{
		lastWriteSig: map[string]string{},
		mounts:       map[string]string{},
		active:       map[string]time.Time{},
	}
	mount := `[coworkd] mounting subpath for user s (uid=1, mode=rw): /mnt/.virtiofs-root/shared/Users/x/repo -> /sessions/s/mnt/repo`
	c.handleLine(mount, nil, map[string]bool{})
	c.mu.Lock()
	gotHost := c.mounts["repo"]
	_, gotActive := c.active["/Users/x/repo"]
	c.mu.Unlock()
	if gotHost != "/Users/x/repo" {
		t.Errorf("mount map: repo -> %q, want /Users/x/repo", gotHost)
	}
	if !gotActive {
		t.Error("repo should be active after mount")
	}

	c.handleLine(`[process:oneshot-x] unmounting /sessions/s/mnt/repo`, nil, map[string]bool{})
	c.mu.Lock()
	_, stillActive := c.active["/Users/x/repo"]
	c.mu.Unlock()
	if stillActive {
		t.Error("repo should be inactive after unmount")
	}
}

func TestCurrentMounts_OverrideAndMerge(t *testing.T) {
	// override wins outright
	c := &ClaudeDesktopWatcher{mountsOverride: map[string]string{"x": "/x"}}
	if got := c.currentMounts(); len(got) != 1 || got["x"] != "/x" {
		t.Fatalf("override not honored: %v", got)
	}

	// without override: main.log heuristics + coworkd-learned mounts merged,
	// with the coworkd-learned mapping winning on conflicts.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.log"),
		[]byte("LocalSessions.checkTrust: cwd=/Users/a/fromlog\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2 := &ClaudeDesktopWatcher{
		LogDir: dir,
		mounts: map[string]string{"repo": "/Users/a/repo"},
	}
	m := c2.currentMounts()
	if m["fromlog"] != "/Users/a/fromlog" {
		t.Errorf("main.log mount missing: %v", m)
	}
	if m["repo"] != "/Users/a/repo" {
		t.Errorf("coworkd-learned mount missing: %v", m)
	}
}

func TestParseCoworkUnmountBase_NonUnmount(t *testing.T) {
	for _, line := range []string{
		"[KERNEL] mounting subpath ... -> /sessions/s/mnt/repo", // a MOUNT, not unmount
		"some unrelated line",
	} {
		if _, ok := parseCoworkUnmountBase(line); ok {
			t.Errorf("should not parse as unmount: %q", line)
		}
	}
	if b, ok := parseCoworkUnmountBase("unmounting /sessions/s/mnt/repo"); !ok || b != "repo" {
		t.Errorf("unmount base = %q ok=%v, want repo", b, ok)
	}
}
