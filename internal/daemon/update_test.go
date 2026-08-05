package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/updatehint"
)

// pinUpdateHooks installs check/apply stubs for one test and restores whatever
// was there, so the package-level vars can't leak between tests.
func pinUpdateHooks(t *testing.T,
	check func(ctx context.Context) (updatehint.Hint, bool, error),
	apply func(ctx context.Context) error,
) {
	t.Helper()
	prevCheck, prevApply, prevDelay := CheckUpdate, ApplyUpdate, updateCheckInitialDelay
	CheckUpdate, ApplyUpdate, updateCheckInitialDelay = check, apply, time.Millisecond
	t.Cleanup(func() {
		CheckUpdate, ApplyUpdate, updateCheckInitialDelay = prevCheck, prevApply, prevDelay
	})
}

// TestWatchForUpdates_OfflineIsSilent is the corp-network case: api.github.com is
// blocked, so every check errors. Nothing must be recorded and nothing must
// break — an unreachable releases API has to look exactly like "up to date".
func TestWatchForUpdates_OfflineIsSilent(t *testing.T) {
	fakeHome(t)
	checked := make(chan struct{}, 1)
	pinUpdateHooks(t,
		func(ctx context.Context) (updatehint.Hint, bool, error) {
			select {
			case checked <- struct{}{}:
			default:
			}
			return updatehint.Hint{}, false, errors.New("dial tcp: connection refused")
		},
		func(ctx context.Context) error {
			t.Error("apply must never run when the check failed")
			return nil
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { watchForUpdates(ctx); close(done) }()

	select {
	case <-checked:
	case <-time.After(2 * time.Second):
		t.Fatal("the check never ran")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchForUpdates did not exit on context cancel")
	}
	if _, ok := updatehint.Read(); ok {
		t.Error("a failed check must not write a hint")
	}
}

// The shipped default is auto-apply, so a machine with no config file of its own
// installs the update it just found.
func TestRunUpdateCheck_AppliesByDefault(t *testing.T) {
	fakeHome(t)
	applied := false
	pinUpdateHooks(t,
		func(ctx context.Context) (updatehint.Hint, bool, error) {
			return updatehint.Hint{Version: "1.9.0", Tag: "v1.9.0"}, true, nil
		},
		func(ctx context.Context) error { applied = true; return nil },
	)

	runUpdateCheck(context.Background())

	if !applied {
		t.Error("update.auto defaults to on, so an available update must be installed")
	}
	// Once installed, the hint describes the version now running — it must not
	// keep telling the user to update.
	if _, ok := updatehint.Read(); ok {
		t.Error("hint must be cleared after a successful auto-install")
	}
}

// Opting out keeps the notice without the install: this is what a
// change-controlled fleet sets.
func TestRunUpdateCheck_AutoOffOnlyRecordsHint(t *testing.T) {
	fakeHome(t)
	cfg := config.DefaultConfig()
	cfg.Update.Auto = false
	if _, err := config.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	applied := false
	pinUpdateHooks(t,
		func(ctx context.Context) (updatehint.Hint, bool, error) {
			return updatehint.Hint{Version: "1.9.0", Tag: "v1.9.0"}, true, nil
		},
		func(ctx context.Context) error { applied = true; return nil },
	)

	runUpdateCheck(context.Background())

	if applied {
		t.Error("update was applied with update.auto off")
	}
	h, ok := updatehint.Read()
	if !ok {
		t.Fatal("no hint recorded for an available update")
	}
	if h.Version != "1.9.0" || h.CheckedAt.IsZero() {
		t.Errorf("hint = %+v, want version 1.9.0 with a CheckedAt stamp", h)
	}
}

// A failed install must leave the hint in place: the user still needs to know an
// update is waiting, and the previous version is still what's running.
func TestRunUpdateCheck_FailedAutoInstallKeepsHint(t *testing.T) {
	fakeHome(t)
	pinUpdateHooks(t,
		func(ctx context.Context) (updatehint.Hint, bool, error) {
			return updatehint.Hint{Version: "1.9.0", Tag: "v1.9.0"}, true, nil
		},
		func(ctx context.Context) error { return errors.New("checksum mismatch") },
	)

	runUpdateCheck(context.Background())

	if _, ok := updatehint.Read(); !ok {
		t.Error("hint must survive a failed auto-install")
	}
}

func TestRunUpdateCheck_UpToDateClearsStaleHint(t *testing.T) {
	fakeHome(t)
	if err := updatehint.Write(updatehint.Hint{Version: "1.9.0", Tag: "v1.9.0"}); err != nil {
		t.Fatal(err)
	}
	pinUpdateHooks(t,
		func(ctx context.Context) (updatehint.Hint, bool, error) {
			return updatehint.Hint{Version: "1.9.0"}, false, nil
		},
		nil,
	)

	runUpdateCheck(context.Background())

	if _, ok := updatehint.Read(); ok {
		t.Error("hint must be cleared once we are up to date (e.g. after a manual update)")
	}
}

func TestRunUpdateCheck_DisabledByEnvKillSwitch(t *testing.T) {
	fakeHome(t)
	t.Setenv("BLAMELY_NO_UPDATE_CHECK", "1")
	pinUpdateHooks(t,
		func(ctx context.Context) (updatehint.Hint, bool, error) {
			t.Error("check ran despite BLAMELY_NO_UPDATE_CHECK=1")
			return updatehint.Hint{}, false, nil
		},
		nil,
	)
	runUpdateCheck(context.Background())
}
