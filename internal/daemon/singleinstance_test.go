package daemon

import (
	"path/filepath"
	"testing"
)

// TestAcquireInstanceLock_MutualExclusion verifies a second acquirer is rejected
// while the first holds the lock, and that releasing (closing) lets a later
// acquirer take it — the core single-instance behaviour.
func TestAcquireInstanceLock_MutualExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.lock")

	f1, ok, err := acquireInstanceLock(path)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v, want ok=true", ok, err)
	}

	// Second acquire while the first is held must fail (ok=false), not error.
	if f2, ok, err := acquireInstanceLock(path); err != nil {
		t.Fatalf("second acquire errored: %v", err)
	} else if ok {
		f2.Close()
		t.Fatal("second acquire succeeded while lock held — mutual exclusion broken")
	}

	// Release the first; a new acquire should now succeed.
	f1.Close()
	f3, ok, err := acquireInstanceLock(path)
	if err != nil || !ok {
		t.Fatalf("acquire after release: ok=%v err=%v, want ok=true", ok, err)
	}
	f3.Close()
}
