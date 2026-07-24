package install

import "errors"

// ErrNoGUISession is returned by InstallDaemonAgent (darwin) when the launch
// agent was written but could not be started because the target user has no
// GUI (Aqua) login session — the normal case for MDM/SSH bulk installs pushed
// while the user isn't logged in. It is NOT a failure: launchd bootstraps
// everything in ~/Library/LaunchAgents at the next GUI login, so callers
// should report "will start at next login" and skip the daemon health wait
// (which would only ever time out and print misleading diagnostics).
var ErrNoGUISession = errors.New("no GUI login session for target user")
