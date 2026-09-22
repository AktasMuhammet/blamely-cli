#!/usr/bin/env bash
# Blamely CLI installer for Linux — downloads the latest release binary,
# installs to ~/.blamely/bin, updates PATH, and runs `blamely install`.
#
# Usage:
#   curl -sSL https://blamely.ai/blamely-linux-install.sh | bash
#   ./blamely-linux-install.sh
#   BLAMELY_INSTALL_YES=1 ./blamely-linux-install.sh
set -euo pipefail

# BLAMELY_CHANNEL selects the GitHub release to install from. Default `latest`
# (stable). Set BLAMELY_CHANNEL=beta to install the rolling pre-release build:
#   curl -sSL https://blamely.ai/blamely-linux-install.sh | BLAMELY_CHANNEL=beta bash
# On the beta channel the CLI is installed with --skip-plugins and the beta
# .vsix / IntelliJ .zip are sideloaded directly from the beta release.
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

# Blamely's daemon is a PER-USER agent (a `systemd --user` unit in your login
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
    die "Don't pipe the installer through sudo. Re-run it as '$target_user' without sudo:  curl -sSL https://blamely.ai/blamely-linux-install.sh | bash"
  fi
  if [ -z "${BLAMELY_ALLOW_ROOT:-}" ]; then
    die "Run the Blamely installer as your normal user, not as root — the daemon installs per-user. (Set BLAMELY_ALLOW_ROOT=1 to override.)"
  fi
fi

is_tty() { [ -t 0 ] && [ -t 1 ]; }

# Single global temp dir + EXIT trap so cleanup can read the path at script
# exit under `set -u` without a "tmpdir: unbound variable" error.
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

pkg_install() {
  local packages="$1"
  if have apt-get; then
    if have sudo; then
      sudo apt-get update -qq
      sudo DEBIAN_FRONTEND=noninteractive apt-get install -y $packages
    else
      die "sudo required to install: $packages (try: sudo apt-get install -y $packages)"
    fi
  elif have dnf; then
    if have sudo; then
      sudo dnf install -y $packages
    else
      die "sudo required to install: $packages"
    fi
  elif have yum; then
    if have sudo; then
      sudo yum install -y $packages
    else
      die "sudo required to install: $packages"
    fi
  elif have pacman; then
    if have sudo; then
      sudo pacman -Sy --noconfirm $packages
    else
      die "sudo required to install: $packages"
    fi
  elif have zypper; then
    if have sudo; then
      sudo zypper install -y $packages
    else
      die "sudo required to install: $packages"
    fi
  elif have apk; then
    if have sudo; then
      sudo apk add --no-cache $packages
    else
      die "sudo required to install: $packages"
    fi
  else
    die "no supported package manager found — install manually: $packages"
  fi
}

ensure_git() {
  if have git; then
    ok "git ($(git --version | head -1))"
    return 0
  fi
  warn "git is required for Blamely attribution."
  if ! ask_yes "Install git now (may use sudo)?"; then
    die "git is required. Install git and re-run."
  fi
  info "Installing git..."
  pkg_install git
  ok "git installed"
}

ensure_curl() {
  if have curl; then
    return 0
  fi
  if have wget; then
    return 0
  fi
  warn "curl or wget is needed to download the Blamely binary."
  if ! ask_yes "Install curl now (may use sudo)?"; then
    die "curl or wget is required."
  fi
  pkg_install curl
}

ensure_tar() {
  if have tar; then
    return 0
  fi
  warn "tar is required to extract the release archive."
  if ! ask_yes "Install tar now (may use sudo)?"; then
    die "tar is required."
  fi
  pkg_install tar
}

ensure_sqlite3() {
  if have sqlite3; then
    ok "sqlite3 (optional — IntelliJ plugin)"
    return 0
  fi
  warn "sqlite3 is not on PATH (optional). Needed for the IntelliJ plugin's DB reader."
  if ! ask_yes "Install sqlite3 now (may use sudo)?"; then
    info "Skipping sqlite3."
    return 0
  fi
  pkg_install sqlite
  ok "sqlite3 installed"
}

detect_arch() {
  local machine
  machine="$(uname -m)"
  case "$machine" in
    arm64|aarch64) echo "arm64" ;;
    x86_64|amd64)  echo "amd64" ;;
    i686|i386)     die "32-bit Linux is not supported — use amd64 or arm64" ;;
    *) die "unsupported Linux architecture: $machine" ;;
  esac
}

download_and_install() {
  local arch asset url bin
  arch="$(detect_arch)"
  asset="blamely_linux_${arch}.tar.gz"
  url="${RELEASE_BASE}/${asset}"

  info "Downloading ${url} ..."
  TMP_WORKDIR="$(mktemp -d)"

  if have curl; then
    curl -fsSL "$url" -o "${TMP_WORKDIR}/${asset}"
  else
    wget -qO "${TMP_WORKDIR}/${asset}" "$url"
  fi
  tar -xzf "${TMP_WORKDIR}/${asset}" -C "$TMP_WORKDIR"

  bin="$(find "$TMP_WORKDIR" -type f -name blamely 2>/dev/null | head -1)"
  if [ -z "$bin" ]; then
    die "could not find blamely binary inside ${asset}"
  fi

  mkdir -p "$STABLE_DIR"
  cp -f "$bin" "$STABLE_BIN"
  chmod +x "$STABLE_BIN"
  ok "Binary installed: ${STABLE_BIN}"
}

# find_editor_cli echoes the path to an editor's CLI launcher, or nothing.
find_editor_cli() {
  local names="$1" n p
  for n in $names; do
    if p="$(command -v "$n" 2>/dev/null)"; then printf '%s\n' "$p"; return 0; fi
  done
  return 1
}

# sideload_beta_plugins downloads the beta .vsix + IntelliJ .zip from the beta
# release and installs them directly (the marketplaces only carry stable builds).
sideload_beta_plugins() {
  local tmp; tmp="$(mktemp -d)"

  info "Downloading beta VS Code extension..."
  if curl -fsSL "${RELEASE_BASE}/blamely.vsix" -o "$tmp/blamely.vsix"; then
    local cli names
    for names in "code" "cursor" "antigravity-ide antigravity"; do
      if cli="$(find_editor_cli "$names")"; then
        "$cli" --install-extension "$tmp/blamely.vsix" --force >/dev/null 2>&1 \
          && ok "$(basename "$cli") — beta extension installed (reload the window)" \
          || warn "$(basename "$cli") — beta extension install failed"
      fi
    done
  else
    warn "could not download beta .vsix — skipping VS Code-family install"
  fi

  info "Downloading beta IntelliJ plugin..."
  if curl -fsSL "${RELEASE_BASE}/blamely-intellij.zip" -o "$tmp/blamely-intellij.zip"; then
    # Let the CLI place it: its IDE discovery finds Toolbox / /opt installs and
    # not-yet-launched IDEs that a "~/.local/share/JetBrains/<ver>" glob misses.
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
  "$STABLE_BIN" install $install_args
  "$STABLE_BIN" repair >/dev/null 2>&1 || true

  if [ "$CHANNEL" != "latest" ]; then
    sideload_beta_plugins
  fi
  ok "Blamely configured."
}

main() {
  local os
  os="$(uname -s)"
  case "$os" in
    Linux) ;;
    Darwin) die "use blamely-mac-install.sh on macOS" ;;
    *) die "unsupported OS: $os" ;;
  esac

  printf '\n%sBlamely CLI installer (Linux)%s\n' "$BOLD" "$RESET"
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
  printf '  Restart your shell (or re-source your rc file) so PATH includes ~/.blamely/bin.\n\n' "$BOLD" "$RESET"
}

main "$@"
