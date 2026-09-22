#!/usr/bin/env bash
# Blamely CLI installer for macOS — downloads the latest release binary,
# installs to ~/.blamely/bin, updates PATH, and runs `blamely install`.
#
# Usage:
#   curl -sSL https://blamely.ai/blamely-mac-install.sh | bash
#   ./blamely-mac-install.sh
#   BLAMELY_INSTALL_YES=1 ./blamely-mac-install.sh   # auto-accept dependency installs
set -euo pipefail

# BLAMELY_CHANNEL selects the GitHub release to install from. Default `latest`
# (stable). Set BLAMELY_CHANNEL=beta to install the rolling pre-release build:
#   curl -sSL https://blamely.ai/blamely-mac-install.sh | BLAMELY_CHANNEL=beta bash
# On the beta channel the CLI is installed with --skip-plugins and the beta
# .vsix / IntelliJ .zip are sideloaded directly from the beta release (the public
# marketplaces only ever carry stable builds — see run_blamely_install).
CHANNEL="${BLAMELY_CHANNEL:-latest}"
RELEASE_BASE="https://github.com/blamely-ai/blamely/releases/download/${CHANNEL}"
STABLE_DIR="${HOME}/.blamely/bin"
STABLE_BIN="${STABLE_DIR}/blamely"
AUTO_YES="${BLAMELY_INSTALL_YES:-}"

# ANSI-C quoting ($'...') so these hold real ESC bytes — otherwise `printf %s`
# prints the literal backslash sequences instead of coloring the output.
RED=$'\033[31m'
GREEN=$'\033[32m'
YELLOW=$'\033[33m'
BOLD=$'\033[1m'
RESET=$'\033[0m'

info()  { printf '  → %s\n' "$*"; }
ok()    { printf '%s  ✓%s %s\n' "$GREEN" "$RESET" "$*"; }
warn()  { printf '%s  !%s %s\n' "$YELLOW" "$RESET" "$*"; }
die()   { printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }

# Blamely's daemon is a PER-USER agent (a launchd LaunchAgent in your login
# session). Run as root it would install the binary, ~/.blamely, and the daemon
# socket under root's home, out of reach of your editor. So if we were started
# with sudo, drop back to the invoking user and install there instead.
if [ "$(id -u)" = "0" ]; then
  target_user="${SUDO_USER:-}"
  if [ -n "$target_user" ] && [ "$target_user" != "root" ]; then
    if [ -f "$0" ] && [ -r "$0" ]; then
      warn "Running under sudo — re-running as '$target_user' so Blamely installs per-user."
      exec sudo -u "$target_user" -H env \
        BLAMELY_CHANNEL="$CHANNEL" BLAMELY_INSTALL_YES="${AUTO_YES:-}" \
        bash "$0" "$@"
      die "couldn't drop privileges to '$target_user' — re-run without sudo."
    fi
    die "Don't pipe the installer through sudo. Re-run it as '$target_user' without sudo:  curl -sSL https://blamely.ai/blamely-mac-install.sh | bash"
  fi
  if [ -z "${BLAMELY_ALLOW_ROOT:-}" ]; then
    die "Run the Blamely installer as your normal user, not as root — the daemon installs per-user. (Set BLAMELY_ALLOW_ROOT=1 to override.)"
  fi
fi

is_tty() { [ -t 0 ] && [ -t 1 ]; }

# A single global temp dir + EXIT trap. Keeping the path global (not local to
# download_and_install) means the trap can still read it at script exit under
# `set -u` without tripping "tmpdir: unbound variable".
TMP_WORKDIR=""
cleanup() { [ -n "${TMP_WORKDIR:-}" ] && rm -rf "${TMP_WORKDIR}"; }
trap cleanup EXIT

ask_yes() {
  local prompt="$1"
  if [ -n "$AUTO_YES" ]; then
    return 0
  fi
  if ! is_tty; then
    return 1
  fi
  printf '%s [y/N] ' "$prompt"
  local ans
  read -r ans || return 1
  case "$ans" in
    y|Y|yes|YES) return 0 ;;
    *) return 1 ;;
  esac
}

have() { command -v "$1" >/dev/null 2>&1; }

ensure_git() {
  if have git; then
    ok "git ($(git --version | head -1))"
    return 0
  fi
  warn "git is required for Blamely attribution (commits, hooks, git notes)."
  if ! ask_yes "Install Git now?"; then
    die "git is required. Install from https://git-scm.com/download/mac and re-run."
  fi
  if have brew; then
    info "Installing git via Homebrew..."
    brew install git
  elif have xcode-select; then
    info "Opening Xcode Command Line Tools installer..."
    xcode-select --install 2>/dev/null || true
    die "Finish the Xcode CLT install dialog, then re-run this script."
  else
    die "Install Git manually: https://git-scm.com/download/mac"
  fi
  ok "git installed"
}

ensure_curl() {
  if have curl; then
    return 0
  fi
  warn "curl is needed to download the Blamely binary."
  if ! ask_yes "Install curl now (via Homebrew)?"; then
    die "curl is required. Install curl or download the release archive manually."
  fi
  if have brew; then
    brew install curl
  else
    die "Install curl manually, then re-run."
  fi
}

ensure_tar() {
  if have tar; then
    return 0
  fi
  die "tar is required to extract the release archive."
}

ensure_sqlite3() {
  if have sqlite3; then
    ok "sqlite3 (optional — IntelliJ plugin reads the local DB via the CLI)"
    return 0
  fi
  warn "sqlite3 is not on PATH (optional). The IntelliJ plugin uses the system sqlite3 CLI."
  if ! ask_yes "Install sqlite3 now (via Homebrew)?"; then
    info "Skipping sqlite3 — CLI and VS Code work without it."
    return 0
  fi
  if have brew; then
    brew install sqlite
    ok "sqlite3 installed"
  else
    warn "Install sqlite3 manually if you use the IntelliJ plugin."
  fi
}

detect_arch() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    arm64|aarch64) echo "arm64" ;;
    x86_64|amd64)  echo "amd64" ;;
    *) die "unsupported macOS architecture: $machine" ;;
  esac
}

# prepare_macos_binary clears the com.apple.quarantine attribute Gatekeeper
# stamps on downloads (it's what makes the first launch get "Killed: 9") and
# ad-hoc re-signs the binary so the loader trusts it. Both are best-effort.
prepare_macos_binary() {
  local target="$1"
  xattr -d com.apple.quarantine "$target" 2>/dev/null || true
  xattr -cr "$target" 2>/dev/null || true
  if have codesign; then
    codesign -s - -f "$target" >/dev/null 2>&1 || \
      warn "could not re-sign binary; first launch may be blocked by macOS"
  else
    warn "codesign not found; first launch may be blocked by macOS Gatekeeper"
  fi
}

download_and_install() {
  local arch asset url bin
  arch="$(detect_arch)"
  asset="blamely_darwin_${arch}.tar.gz"
  url="${RELEASE_BASE}/${asset}"

  info "Downloading ${url} ..."
  TMP_WORKDIR="$(mktemp -d)"

  curl -fsSL "$url" -o "${TMP_WORKDIR}/${asset}"
  tar -xzf "${TMP_WORKDIR}/${asset}" -C "$TMP_WORKDIR"

  bin="$(find "$TMP_WORKDIR" -type f -name blamely 2>/dev/null | head -1)"
  if [ -z "$bin" ]; then
    die "could not find blamely binary inside ${asset}"
  fi

  mkdir -p "$STABLE_DIR"
  cp -f "$bin" "$STABLE_BIN"
  chmod +x "$STABLE_BIN"
  prepare_macos_binary "$STABLE_BIN"
  ok "Binary installed: ${STABLE_BIN}"
}

# find_editor_cli echoes the path to an editor's CLI launcher, or nothing.
# $1 = space-separated CLI names to try on PATH; $2 = .app bundle for the fallback.
find_editor_cli() {
  local names="$1" app="$2" n p
  for n in $names; do
    if p="$(command -v "$n" 2>/dev/null)"; then printf '%s\n' "$p"; return 0; fi
  done
  for n in $names; do
    p="/Applications/$app.app/Contents/Resources/app/bin/$n"
    [ -x "$p" ] && { printf '%s\n' "$p"; return 0; }
  done
  return 1
}

# sideload_beta_plugins downloads the beta .vsix + IntelliJ .zip from the beta
# release and installs them directly — the marketplaces only carry stable builds,
# so `blamely install` (marketplace path) can't deliver a beta plugin.
sideload_beta_plugins() {
  local tmp; tmp="$(mktemp -d)"

  info "Downloading beta VS Code extension..."
  if curl -fsSL "${RELEASE_BASE}/blamely.vsix" -o "$tmp/blamely.vsix"; then
    local cli entry names app
    for entry in "code|Visual Studio Code" "cursor|Cursor" "antigravity-ide antigravity|Antigravity IDE"; do
      names="${entry%%|*}"; app="${entry##*|}"
      if cli="$(find_editor_cli "$names" "$app")"; then
        if "$cli" --install-extension "$tmp/blamely.vsix" --force >/dev/null 2>&1; then
          ok "$app — beta extension installed (reload the window)"
        else
          warn "$app — beta extension install failed"
        fi
      fi
    done
  else
    warn "could not download beta .vsix — skipping VS Code-family install"
  fi

  info "Downloading beta IntelliJ plugin..."
  if curl -fsSL "${RELEASE_BASE}/blamely-intellij.zip" -o "$tmp/blamely-intellij.zip"; then
    # Let the CLI place it: its IDE discovery finds Toolbox / Applications installs
    # and not-yet-launched IDEs that a "~/Library/.../JetBrains/<ver>" glob misses.
    "$STABLE_BIN" install-jetbrains-zip "$tmp/blamely-intellij.zip" \
      || warn "beta IntelliJ plugin install reported an issue"
  else
    warn "could not download beta IntelliJ plugin — skipping"
  fi

  rm -rf "$tmp"
}

run_blamely_install() {
  # On the beta channel, skip the CLI's marketplace plugin install — beta plugins
  # are sideloaded from the beta release instead (see sideload_beta_plugins).
  local install_args=""
  [ "$CHANNEL" != "latest" ] && install_args="--skip-plugins"

  info "Running blamely install (hooks, daemon, PATH)..."
  local rc=0
  "$STABLE_BIN" install $install_args || rc=$?
  if [ "$rc" -ne 0 ]; then
    # 137 = 128 + SIGKILL(9): macOS Gatekeeper killed the binary. Re-sign and
    # retry once before giving up.
    if [ "$rc" -eq 137 ]; then
      warn "macOS blocked the binary (Killed: 9). Re-signing and retrying..."
      prepare_macos_binary "$STABLE_BIN"
      rc=0
      "$STABLE_BIN" install $install_args || rc=$?
    fi
  fi
  if [ "$rc" -ne 0 ]; then
    die "blamely install failed (exit ${rc}). Try manually:
  xattr -cr \"${STABLE_BIN}\"
  codesign -s - -f \"${STABLE_BIN}\"
  \"${STABLE_BIN}\" install"
  fi
  "$STABLE_BIN" repair >/dev/null 2>&1 || true

  if [ "$CHANNEL" != "latest" ]; then
    sideload_beta_plugins
  fi
  ok "Blamely configured."
}

main() {
  if [ "$(uname -s)" != "Darwin" ]; then
    die "this installer is for macOS only (use blamely-linux-install.sh on Linux)"
  fi

  printf '\n%sBlamely CLI installer (macOS)%s\n' "$BOLD" "$RESET"
  if [ "$CHANNEL" != "latest" ]; then
    printf '%s  channel: %s (pre-release)%s\n' "$YELLOW" "$CHANNEL" "$RESET"
  fi
  printf '\n'

  ensure_curl
  ensure_tar
  ensure_git
  ensure_sqlite3
  download_and_install
  run_blamely_install

  printf '\n'
  printf '  Run %sblamely status%s to verify the daemon.\n' "$BOLD" "$RESET"
  printf '  Run %sblamely doctor%s for a full self-check.\n' "$BOLD" "$RESET"
  printf '  Restart your shell (or %ssource ~/.zshrc%s) so PATH includes ~/.blamely/bin.\n\n' "$BOLD" "$RESET"
}

main "$@"
