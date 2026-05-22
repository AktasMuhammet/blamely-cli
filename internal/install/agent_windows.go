//go:build windows

package install

import (
	"errors"
	"fmt"
	"os/exec"
)

const scheduledTaskName = "Blamely Daemon"

func InstallDaemonAgent(binaryPath string) (string, error) {
	// /F = force overwrite; /SC ONLOGON = run at every user logon; /RL LIMITED = user privileges.
	args := []string{
		"/Create", "/F",
		"/TN", scheduledTaskName,
		"/TR", fmt.Sprintf("\"%s\" daemon", binaryPath),
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
	}
	if out, err := exec.Command("schtasks", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("schtasks /Create: %v: %s", err, string(out))
	}
	// Kill any prior running instance so a reinstall picks up the new
	// binary; then start fresh. Both are best-effort.
	_ = exec.Command("schtasks", "/End", "/TN", scheduledTaskName).Run()
	_ = exec.Command("schtasks", "/Run", "/TN", scheduledTaskName).Run()
	return scheduledTaskName, nil
}

func UninstallDaemonAgent() error {
	err := exec.Command("schtasks", "/Delete", "/F", "/TN", scheduledTaskName).Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// schtasks returns non-zero when the task doesn't exist; not fatal.
			return nil
		}
		return err
	}
	return nil
}
