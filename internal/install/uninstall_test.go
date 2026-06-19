package install

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestRuntimeArtifacts_RemovesRuntimeKeepsConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // windows home resolution
	bdir := filepath.Join(home, ".blamely")

	// With DB removal (default): includes runtime files, db + sidecars, legacy db.
	got := runtimeArtifacts(false)
	for _, want := range []string{
		filepath.Join(bdir, "daemon.log"),
		filepath.Join(bdir, "daemon.sock"),
		filepath.Join(bdir, "daemon.port"),
		filepath.Join(bdir, "state.json"),
		filepath.Join(bdir, "blamely.db"),
		filepath.Join(bdir, "db.sqlite"),
		filepath.Join(bdir, "db.sqlite-wal"),
		filepath.Join(bdir, "db.sqlite-shm"),
		filepath.Join(bdir, "db.sqlite-journal"),
	} {
		if !contains(got, want) {
			t.Errorf("runtimeArtifacts(false) missing %s", want)
		}
	}
	// NEVER touches the user's config or exclusion list.
	for _, never := range []string{
		filepath.Join(bdir, "config.json"),
		filepath.Join(bdir, "exclude"),
	} {
		if contains(got, never) {
			t.Errorf("runtimeArtifacts must not remove %s", never)
		}
	}

	// keepDB=true preserves the database and its sidecars.
	keep := runtimeArtifacts(true)
	for _, db := range []string{
		filepath.Join(bdir, "db.sqlite"),
		filepath.Join(bdir, "db.sqlite-wal"),
	} {
		if contains(keep, db) {
			t.Errorf("runtimeArtifacts(true) must keep %s", db)
		}
	}
	// But still removes logs/state even when keeping the DB.
	if !contains(keep, filepath.Join(bdir, "daemon.log")) {
		t.Error("runtimeArtifacts(true) should still remove daemon.log")
	}
}

func TestRemoveInstalledBinary_RemovesBinaryAndExtras(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows removal is async (detached script after process exit)")
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "blamely")
	db := filepath.Join(dir, "db.sqlite")
	cfg := filepath.Join(dir, "config.json") // a kept file — must survive
	for _, f := range []string{bin, db, cfg} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := removeInstalledBinary(bin, []string{db}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); !os.IsNotExist(err) {
		t.Error("binary not removed")
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Error("extra file (db.sqlite) not removed")
	}
	if _, err := os.Stat(binDir); !os.IsNotExist(err) {
		t.Error("empty bin dir not removed")
	}
	if _, err := os.Stat(cfg); err != nil {
		t.Error("config.json must be preserved (not in the remove list)")
	}
}
