package install

import (
	"errors"
	"fmt"
)

// ErrNoGUISession is returned by InstallDaemonAgent (darwin) when the launch
// agent was written but could not be started because the target user has no
// GUI (Aqua) login session — the normal case for MDM/SSH bulk installs pushed
// while the user isn't logged in. It is NOT a failure: launchd bootstraps
// everything in ~/Library/LaunchAgents at the next GUI login, so callers
// should report "will start at next login" and skip the daemon health wait
// (which would only ever time out and print misleading diagnostics).
var ErrNoGUISession = errors.New("no GUI login session for target user")

// AgentTaskIssue is an autostart entry that IS registered but does not run what
// the running build would register — the shape of "a fix we shipped never
// reached this machine". See CheckAutostartTasks (Windows) for how it happens
// and why it was previously invisible.
type AgentTaskIssue struct {
	// Task is the autostart entry's name (a Scheduled Task name on Windows).
	Task string
	// Registered is the command line the entry currently runs.
	Registered string
	// Expected is the command line this build would register instead.
	Expected string
	// Fix is the exact command a user (or their IT) can run to repair it.
	Fix string
}

// reportAutostartIssues prints, right under the green "Daemon agent" row, any
// autostart entry that was registered by someone we can't overwrite and so still
// runs an older command line.
//
// Install used to report unqualified success here, because registering the agent
// "succeeded" — it fell back to a path that left the stale task in place. That
// made the one thing the user needs to know (this machine did NOT get the fix)
// the one thing nothing printed. The install itself is not failed by this: hooks,
// binary and database are all in place, and the daemon does run; only its
// autostart entry is stale.
//
// It also lands in ~/.blamely/last-install.log, which is what the Windows
// installer shows on its final page and what support asks for.
func reportAutostartIssues(binaryPath string) {
	for _, i := range CheckAutostartTasks(binaryPath) {
		fail("Autostart entry", fmt.Sprintf("%q still runs %s", i.Task, i.Registered))
		info("  expected", i.Expected)
		info("  fix", i.Fix)
	}
}
