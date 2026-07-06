#!/bin/sh
#
# Linux .deb/.rpm preremove — reverse `blamely install` before files are removed.
#
# Runs as root; like postinstall, it resolves the desktop user and runs
# `blamely uninstall` AS THAT USER so the per-user daemon/hooks are torn down.
#
# IMPORTANT: only uninstall on a REAL removal, not an upgrade — otherwise every
# `apt upgrade` / `dnf update` would rip out the user's daemon and hooks:
#   deb passes  $1 = "remove"  (vs "upgrade")
#   rpm passes  $1 = "0"       (final removal; "1"+ = upgrade)
#
# Best-effort: never block the removal (exit 0).

action="${1:-}"
case "$action" in
  remove|0) ;;                 # real removal → proceed
  *) exit 0 ;;                 # upgrade / anything else → keep the user's setup
esac

BLAMELY="/usr/local/bin/blamely"
log() { echo "blamely: $*"; }
[ -x "$BLAMELY" ] || exit 0

target_user=""
if [ -n "${SUDO_USER:-}" ] && [ "${SUDO_USER}" != "root" ]; then
  target_user="$SUDO_USER"
elif [ -n "${PKEXEC_UID:-}" ]; then
  target_user="$(getent passwd "$PKEXEC_UID" 2>/dev/null | cut -d: -f1)"
fi
if [ -z "$target_user" ]; then
  target_user="$(getent passwd | awk -F: '$3>=1000 && $3<65534 && $6 ~ /^\/home\// {print $1; exit}')"
fi
[ -n "$target_user" ] || exit 0

home="$(getent passwd "$target_user" 2>/dev/null | cut -d: -f6)"
log "reversing Blamely setup for user: $target_user"
if command -v runuser >/dev/null 2>&1; then
  runuser -u "$target_user" -- env NO_COLOR=1 HOME="$home" "$BLAMELY" uninstall || true
else
  su - "$target_user" -c "NO_COLOR=1 HOME='$home' '$BLAMELY' uninstall" || true
fi

exit 0
