#!/usr/bin/env bash
# Blamely CLI installer for Linux — downloads the latest release binary,
# installs to ~/.blamely/bin, updates PATH, and runs `blamely install`.
#
# Usage:
#   curl -sSL https://blamely.ai/blamely-linux-install.sh | bash
#   ./blamely-linux-install.sh
#   BLAMELY_INSTALL_YES=1 ./blamely-linux-install.sh
set -euo pipefail

RELEASE_BASE="https://github.com/blamely-ai/blamely/releases/download/latest"
STABLE_DIR="${HOME}/.blamely/bin"
STABLE_BIN="${STABLE_DIR}/blamely"
AUTO_YES="${BLAMELY_INSTALL_YES:-}"

RED=$'\033[31m'
GREEN=$'\033[32m'
YELLOW=$'\033[33m'
BOLD=$'\033[1m'
RESET=$'\033[0m'

info()  { printf '  → %s\n' "$*"; }
ok()    { printf "${GREEN}  ✓${RESET} %s\n" "$*"; }
warn()  { printf "${YELLOW}  !${RESET} %s\n" "$*"; }
die()   { printf "${RED}error:${RESET} %s\n" "$*" >&2; exit 1; }

is_tty() { [ -t 0 ] && [ -t 1 ]; }

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
  if ! (
    local tmpdir
    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' EXIT

    if have curl; then
      curl -fsSL "$url" -o "${tmpdir}/${asset}"
    else
      wget -qO "${tmpdir}/${asset}" "$url"
    fi
    tar -xzf "${tmpdir}/${asset}" -C "$tmpdir"

    bin="$(find "$tmpdir" -type f -name blamely 2>/dev/null | head -1)"
    if [ -z "$bin" ]; then
      exit 1
    fi

    mkdir -p "$STABLE_DIR"
    cp -f "$bin" "$STABLE_BIN"
    chmod +x "$STABLE_BIN"
  ); then
    die "could not download or extract blamely from ${asset}"
  fi

  ok "Binary installed: ${STABLE_BIN}"
}

run_blamely_install() {
  info "Running blamely install (hooks, daemon, PATH)..."
  set +e
  "$STABLE_BIN" install
  local rc=$?
  set -e
  if [ "$rc" -ne 0 ]; then
    die "blamely install failed (exit ${rc}). Run: ${STABLE_BIN} doctor"
  fi
  "$STABLE_BIN" repair >/dev/null 2>&1 || true
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

  printf '\n%sBlamely CLI installer (Linux)%s\n\n' "$BOLD" "$RESET"

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
