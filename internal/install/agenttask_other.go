//go:build !windows

package install

// CheckAutostartTasks has nothing to report off Windows: launchd and systemd
// rewrite their unit files as plain files owned by the user, so a registration
// cannot get stuck holding an old command the way a Scheduled Task created from
// an elevated session does.
func CheckAutostartTasks(string) []AgentTaskIssue { return nil }
