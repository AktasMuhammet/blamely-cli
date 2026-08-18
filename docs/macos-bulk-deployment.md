# Deploying Blamely on macOS via MDM (Jamf, Kandji, Intune, …)

Blamely's daemon is a **per-user LaunchAgent** (`com.blamely.daemon` in
`~/Library/LaunchAgents`), not a system daemon. That makes one detail critical
for bulk deployment: the install must run **inside the target user's launchd
session**, or the daemon cannot be started at install time.

## The failure mode this prevents

MDM agents run scripts as **root, outside any user's Aqua (GUI) session**. A
wrapper that only does `sudo -u <user>` gets the right *uid* but stays in the
root/MDM session context. Everything file-based succeeds (binary, hooks,
plist), but registering the LaunchAgent with launchd silently fails — the
install log then shows:

```
⚠  Daemon health check failed — hooks may not be processed.
Log file ~/.blamely/daemon.log is empty or missing — the daemon may not have started at all.
```

…while the policy exits 0. The daemon only appears after the user's next
logout/login (launchd auto-bootstraps `~/Library/LaunchAgents` at GUI login) —
or never, if the agent ended up disabled.

## The correct invocation

Run the installer through `launchctl asuser`, which attaches the process to the
console user's launchd session:

```bash
#!/bin/bash
# MDM policy script — run as root.
console_user="$(stat -f%Su /dev/console)"
if [ -z "$console_user" ] || [ "$console_user" = "root" ]; then
  echo "no console user logged in — Blamely daemon will start at next login"
fi
uid="$(id -u "$console_user")"

curl -fsSL https://blamely.ai/blamely-mac-install.sh -o /tmp/blamely-mac-install.sh
launchctl asuser "$uid" sudo -u "$console_user" -H env BLAMELY_INSTALL_YES=1 \
  bash /tmp/blamely-mac-install.sh
rm -f /tmp/blamely-mac-install.sh
```

Notes:

- **Download the script to disk first.** `curl | bash` under root can't re-exec
  itself per-user (there is no script file to re-run).
- The installer itself also accepts `BLAMELY_TARGET_USER=<name>` when run as
  root with no `SUDO_USER` — it then re-execs per-user (with `launchctl asuser`)
  on its own.
- If the policy runs while **no user is logged into the GUI**, `blamely
  install` reports `Daemon: no GUI login session right now — starts
  automatically at next login`. That is expected and healthy: launchd starts
  the agent at the user's next login.

## Verifying on a target machine

As the logged-in user:

```bash
blamely status                                      # daemon: HEALTHY on ~/.blamely/daemon.sock
launchctl print "gui/$(id -u)/com.blamely.daemon"   # state = running
```

If the daemon is not running:

```bash
# Is the agent disabled in launchd's override DB? (left by an old uninstall, or by policy)
launchctl print-disabled "gui/$(id -u)" | grep -i blamely

# Recover in place:
launchctl enable    "gui/$(id -u)/com.blamely.daemon"
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.blamely.daemon.plist
launchctl kickstart -k "gui/$(id -u)/com.blamely.daemon"
```

## macOS 13+ Background Task Management

On Ventura and later, macOS notifies the user that a new "background item" was
added and lets them disable it in **System Settings → General → Login Items &
Extensions**. For managed fleets, whitelist the agent with an MDM
`com.apple.servicemanagement` payload (rule type `Label`, value
`com.blamely.daemon`) so users can't accidentally turn the daemon off.
