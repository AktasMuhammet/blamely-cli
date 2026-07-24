//go:build darwin

package install

import (
	"fmt"
	"os/exec"
	"strings"
)

// daemonManagerState queries launchd for the agent's live state and returns a
// short, human-readable block to print in the install diagnostics. It answers
// the two questions a bare "daemon didn't come up" can't: is the label DISABLED
// in launchd's override database (the silent-no-op case the old `launchctl load`
// couldn't detect), and — if loaded — what does launchd report as its state /
// last exit code. Best-effort: any launchctl failure just yields less output.
func daemonManagerState() string {
	uid := targetUID()
	domain := fmt.Sprintf("gui/%d", uid)
	target := guiDomainTarget(uid)

	var b strings.Builder

	// 1. Disabled-override check. A label listed here as disabled will never
	//    start via load/bootstrap until `launchctl enable` clears it.
	if out, err := exec.Command("launchctl", "print-disabled", domain).CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.Contains(line, launchAgentLabel) {
				continue
			}
			t := strings.TrimSpace(line)
			b.WriteString(fmt.Sprintf("  launchd override: %s\n", t))
			// Value spellings vary by macOS version ("=> true" / "=> disabled").
			if strings.Contains(t, "true") || strings.Contains(t, "disabled") {
				b.WriteString("  → The agent is DISABLED in launchd — it cannot start until re-enabled:\n")
				b.WriteString(fmt.Sprintf("      launchctl enable %s\n", target))
			}
		}
	}

	// 2. Service state / last exit code, if the agent is loaded at all.
	if out, err := exec.Command("launchctl", "print", target).CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "state =") ||
				strings.HasPrefix(t, "pid =") ||
				strings.HasPrefix(t, "last exit code =") ||
				strings.HasPrefix(t, "last exit reason =") {
				b.WriteString(fmt.Sprintf("  launchd: %s\n", t))
			}
		}
	} else {
		b.WriteString(fmt.Sprintf("  launchd: agent %s is not loaded in the %s domain — re-run `blamely install`.\n",
			launchAgentLabel, domain))
	}

	return b.String()
}
