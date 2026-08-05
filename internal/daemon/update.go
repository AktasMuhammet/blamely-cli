package daemon

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/blamely/blamely/internal/config"
	"github.com/blamely/blamely/internal/updatehint"
)

// CheckUpdate resolves whether a newer blamely release exists, returning the
// hint to record and whether it is actually newer than the running version.
//
// ApplyUpdate downloads and installs that release.
//
// Both are assigned by cmd/blamely at startup rather than called directly,
// because internal/daemon must NOT import internal/install — install already
// imports daemon, so the direct call would be an import cycle. This is the same
// indirection Watchers and DBWatcherFactories already use.
var (
	CheckUpdate func(ctx context.Context) (updatehint.Hint, bool, error)
	ApplyUpdate func(ctx context.Context) error
)

// updateCheckInitialDelay keeps the first check away from daemon startup, where
// it would compete with the watchers coming up and with the installer's health
// wait. A var so tests don't have to wait it out.
var updateCheckInitialDelay = 5 * time.Minute

// watchForUpdates periodically asks whether a newer blamely exists, records the
// answer as a hint for the CLI to surface, and — unless update.auto has been
// turned off — installs it.
//
// Every failure — offline, proxy, blocked api.github.com, rate limit — is a
// silent return: nothing in the attribution or git path depends on this
// goroutine, and a machine that can't reach the network must behave exactly like
// one that is up to date.
func watchForUpdates(ctx context.Context) {
	if CheckUpdate == nil {
		return
	}
	if !sleepCtx(ctx, updateCheckInitialDelay) {
		return
	}
	for {
		runUpdateCheck(ctx)
		interval := time.Duration(config.LoadConfig().UpdateIntervalHours()) * time.Hour
		if !sleepCtx(ctx, interval) {
			return
		}
	}
}

func runUpdateCheck(ctx context.Context) {
	if updateCheckDisabled() {
		return
	}
	hint, newer, err := CheckUpdate(ctx)
	if err != nil {
		// One line per interval, at most. An unreachable releases API is the
		// normal state on a locked-down corporate network, not an incident.
		log.Printf("update check: %v", err)
		return
	}
	if !newer {
		_ = updatehint.Clear()
		return
	}
	hint.CheckedAt = time.Now()
	if err := updatehint.Write(hint); err != nil {
		log.Printf("update check: record hint: %v", err)
	}
	if !config.LoadConfig().Update.Auto || ApplyUpdate == nil {
		log.Printf("update available: %s (run `blamely update` to install)", hint.Version)
		return
	}
	log.Printf("update available: %s — installing", hint.Version)
	if err := ApplyUpdate(ctx); err != nil {
		log.Printf("update install failed: %v", err)
		return
	}
	// The new binary is in place; the hint no longer describes anything the user
	// needs to act on. (This process is about to be restarted by the installer.)
	_ = updatehint.Clear()
}

// updateCheckDisabled mirrors install.UpdateCheckDisabled. It re-reads the env
// and config here rather than calling that function because of the import cycle
// described on CheckUpdate above.
func updateCheckDisabled() bool {
	if v := strings.TrimSpace(os.Getenv("BLAMELY_NO_UPDATE_CHECK")); v != "" && v != "0" {
		return true
	}
	return !config.LoadConfig().Update.Check
}

// sleepCtx waits for d, returning false if the context is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
