#!/bin/sh
#
# Linux .deb/.rpm postinstall — finish Blamely's PER-USER setup.
#
# The package manager runs this as root, but Blamely's daemon (`systemd --user`)
# and git/AI hooks live under the user's home (~/.blamely). So we resolve the
# desktop user and run `blamely install` AS THAT USER (via runuser/su → non-root
# euid → install.DropToInvokingUserIfRoot() is a no-op → per-user install).
#
# Output (blamely's step-by-step report of detected IDEs + installed plugins)
# streams straight to the terminal during `apt install` / `dnf install`.
#
# Best-effort: never fail the package install (exit 0).

BLAMELY="/usr/local/bin/blamely"
log() { echo "blamely: $*"; }

# Resolve the target user: whoever ran sudo/pkexec, else the first real login user.
target_user=""
if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
  target_user="$SUDO_USER"
elif [ -n "${PKEXEC_UID:-}" ]; then
  target_user="$(getent passwd "$PKEXEC_UID" 2>/dev/null | cut -d: -f1)"
fi
if [ -z "$target_user" ]; then
  # First non-system account (uid 1000..64533) with a /home directory.
  target_user="$(getent passwd | awk -F: '$3>=1000 && $3<65534 && $6 ~ /^\/home\// {print $1; exit}')"
fi

if [ -z "$target_user" ]; then
  log "could not determine a desktop user; skipping per-user setup."
  log "run once as your user:  blamely install"
  exit 0
fi
if [ ! -x "$BLAMELY" ]; then
  log "error: $BLAMELY missing; cannot complete setup."
  exit 0
fi

home="$(getent passwd "$target_user" 2>/dev/null | cut -d: -f6)"
log "configuring Blamely for user: $target_user"

# Run install as the user. Prefer runuser (util-linux); fall back to su. NO_COLOR
# keeps the terminal output clean; HOME is set so ~/.blamely resolves correctly.
if command -v runuser >/dev/null 2>&1; then
  runuser -u "$target_user" -- env NO_COLOR=1 HOME="$home" "$BLAMELY" install \
    || log "blamely install returned non-zero — re-run manually:  blamely install"
else
  su - "$target_user" -c "NO_COLOR=1 HOME='$home' '$BLAMELY' install" \
    || log "blamely install returned non-zero — re-run manually:  blamely install"
fi

exit 0
