//go:build !darwin

package install

// daemonManagerState is darwin-only (launchd override/state introspection). On
// other platforms the systemd/schtasks recovery hints in diagnoseDaemon already
// point the user at the right query commands, so this is a no-op.
func daemonManagerState() string { return "" }
