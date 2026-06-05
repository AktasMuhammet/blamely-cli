#!/usr/bin/env bash
# Blamely CLI installer for macOS — downloads the latest release binary,
# installs to ~/.blamely/bin, updates PATH, and runs `blamely install`.
#
# Usage:
#   curl -sSL https://blamely.ai/blamely-mac-install.sh | bash
#   ./blamely-mac-install.sh
#   BLAMELY_INSTALL_YES=1 ./blamely-mac-install.sh   # auto-accept dependency installs
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

prepare_macos_binary() {
  # curl downloads get quarantine/provenance xattrs; the release binary is
  # linker-signed, which can stall dyld on first launch. Strip xattrs and
  # re-sign locally so `blamely install` starts immediately.
  local target="$1"
  if [ ! -f "$target" ]; then
    return 0
  fi
  xattr -cr "$target" 2>/dev/null || true
  if have codesign; then
    codesign -s - -f "$target" >/dev/null 2>&1 || \
      warn "could not re-sign binary; first launch may hang while macOS verifies it"
  else
    warn "codesign not found; first launch may hang while macOS verifies the download"
  fi
}

run_with_timeout() {
  local secs="$1"
  shift
  "$@" &
  local pid=$!
  local waited=0
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$waited" -ge "$secs" ]; then
      kill -9 "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
      return 124
    fi
    sleep 1
    waited=$((waited + 1))
  done
  wait "$pid"
}

download_and_install() {
  local arch asset url bin
  arch="$(detect_arch)"
  asset="blamely_darwin_${arch}.tar.gz"
  url="${RELEASE_BASE}/${asset}"

  info "Downloading ${url} ..."
  if ! (
    local tmpdir
    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' EXIT

    curl -fsSL "$url" -o "${tmpdir}/${asset}"
    tar -xzf "${tmpdir}/${asset}" -C "$tmpdir"

    bin="$(find "$tmpdir" -type f -name blamely 2>/dev/null | head -1)"
    if [ -z "$bin" ]; then
      exit 1
    fi

    prepare_macos_binary "$bin"
    mkdir -p "$STABLE_DIR"
    cp -f "$bin" "$STABLE_BIN"
    chmod +x "$STABLE_BIN"
  ); then
    die "could not download or extract blamely from ${asset}"
  fi

  prepare_macos_binary "$STABLE_BIN"
  ok "Binary installed: ${STABLE_BIN}"
}

run_blamely_install() {
  if [ ! -x "$STABLE_BIN" ]; then
    die "blamely binary is missing or not executable: ${STABLE_BIN}"
  fi

  info "Running blamely install (hooks, daemon, PATH)..."
  set +e
  run_with_timeout 180 "$STABLE_BIN" install
  local rc=$?
  set -e
  if [ "$rc" -eq 124 ]; then
    die "blamely install timed out. Try:\n  xattr -cr ${STABLE_BIN}\n  codesign -s - -f ${STABLE_BIN}\n  ${STABLE_BIN} install"
  fi
  if [ "$rc" -ne 0 ]; then
    if [ "$rc" -eq 137 ]; then
      die "blamely install was killed (signal 9). Try: xattr -cr ${STABLE_BIN} && codesign -s - -f ${STABLE_BIN} && ${STABLE_BIN} install"
    fi
    die "blamely install failed (exit ${rc}). Run: ${STABLE_BIN} doctor"
  fi
  "$STABLE_BIN" repair >/dev/null 2>&1 || true
  ok "Blamely configured."
}

main() {
  if [ "$(uname -s)" != "Darwin" ]; then
    die "this installer is for macOS only (use blamely-linux-install.sh on Linux)"
  fi

  printf '\n%sBlamely CLI installer (macOS)%s\n\n' "$BOLD" "$RESET"

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
