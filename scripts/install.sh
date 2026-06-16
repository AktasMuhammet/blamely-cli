#!/bin/sh
# Blamely build + install / uninstall helper for local development.
#
# Usage:
#   ./scripts/install.sh                  build + install (CLI + hooks only)
#   ./scripts/install.sh --with-plugins   also install the VS Code/JetBrains IDE plugins
#   ./scripts/install.sh --obfuscate      build with garble (release-style obfuscation)
#   ./scripts/install.sh uninstall        uninstall + remove binary
#   ./scripts/install.sh rebuild          rebuild without re-running install
#   ./scripts/install.sh repair           remove stale legacy hooks only
#
# IDE plugins are skipped by default here: re-downloading the marketplace
# build on every local install is slow and can clobber a sideloaded dev build
# of the plugin you're working on. Pass --with-plugins to include them — the
# distributed installers and release pipeline always do.
set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STABLE_DIR="$HOME/.blamely/bin"
STABLE_BIN="$STABLE_DIR/blamely"

# ── helpers ──────────────────────────────────────────────────────────────────

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
ok()  { printf '\033[32m  ✓\033[0m %s\n' "$*"; }
info(){ printf '  → %s\n' "$*"; }

require_go() {
    command -v go >/dev/null 2>&1 || die "Go is not installed. Install from https://go.dev/dl/"
}

# Garble version pinned to match the release pipeline (.github/workflows/release.yml).
# garble couples to the Go toolchain — bump in lockstep with Go if it refuses to run.
GARBLE_VERSION="v0.16.0"

# Resolve a garble binary into $GARBLE: prefer one already installed (GOPATH/bin or
# PATH), else install the pinned version. GOPATH/bin is often not on PATH, so we
# look there explicitly rather than relying on `command -v` alone.
resolve_garble() {
    local gp
    gp="$(go env GOPATH 2>/dev/null)/bin/garble"
    if command -v garble >/dev/null 2>&1; then GARBLE="garble"; return; fi
    if [ -x "$gp" ]; then GARBLE="$gp"; return; fi
    info "Installing garble ${GARBLE_VERSION} (binary obfuscator)..."
    go install "mvdan.cc/garble@${GARBLE_VERSION}" || die "failed to install garble"
    if command -v garble >/dev/null 2>&1; then GARBLE="garble"
    elif [ -x "$gp" ]; then GARBLE="$gp"
    else die "garble installed but not found on PATH or at $gp"; fi
}

build_binary() {
    cd "$REPO_ROOT"
    mkdir -p "$STABLE_DIR"
    if [ "$OBFUSCATE" = "1" ]; then
        resolve_garble
        info "Building blamely with garble (obfuscated, mirrors release build)..."
        # Same flags as the release pipeline:
        #   GOGARBLE='*'    garble everything (incl. deps); stdlib is never touched
        #   -literals       encrypt string constants in the binary
        #   -tiny           strip extra runtime metadata (no panic line numbers)
        #   CGO_ENABLED=0   static binary (modernc.org/sqlite is pure Go)
        GOGARBLE='*' CGO_ENABLED=0 "$GARBLE" -literals -tiny build \
            -trimpath -buildvcs=false -ldflags="-s -w" -o "$STABLE_BIN" ./cmd/blamely
    else
        info "Building blamely from source (stripped, trim-path)..."
        # -s  strip the Go symbol table
        # -w  strip DWARF debug info
        # -trimpath  remove absolute build paths (no /Users/<you>/… embedded)
        # -buildvcs=false  don't stamp git metadata into the binary
        # Together these produce a smaller, harder-to-introspect binary. Source
        # files are unchanged; rebuild without these flags for normal debugging.
        go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$STABLE_BIN" ./cmd/blamely
    fi
    chmod +x "$STABLE_BIN"
    ok "Binary built: $STABLE_BIN ($(du -h "$STABLE_BIN" | cut -f1))"
}

# ── commands ─────────────────────────────────────────────────────────────────

do_install() {
    require_go
    build_binary

    info "Running blamely install..."
    if [ "$WITH_PLUGINS" = "1" ]; then
        "$STABLE_BIN" install
    else
        "$STABLE_BIN" install --skip-plugins
        info "Skipped IDE/editor plugin install (local dev default) — pass --with-plugins to include it."
    fi
    ok "Blamely installed."

    # Clean up any stale legacy hooks (no-op if there are none).
    "$STABLE_BIN" repair >/dev/null 2>&1 || true

    echo ""
    echo "  Run \033[1mblamely status\033[0m to verify the daemon is running."
    echo "  Run \033[1mgit commit\033[0m in any repo — you should see the AI/Human bar."
    echo "  Restart your shell (or \`source\` your rc) so the PATH entry takes effect."
}

do_rebuild() {
    require_go
    build_binary
    echo ""
    echo "  Rebuilt only — install state was not touched."
}

do_uninstall() {
    if [ -x "$STABLE_BIN" ]; then
        info "Running blamely uninstall..."
        "$STABLE_BIN" uninstall || true
        ok "Blamely configuration removed."
    elif command -v blamely >/dev/null 2>&1; then
        info "Running blamely uninstall via PATH..."
        blamely uninstall || true
        ok "Blamely configuration removed."
    else
        echo "  blamely binary not found — skipping uninstall step."
        echo "  Manually remove ~/.blamely and run:"
        echo "    git config --global --unset core.hooksPath"
    fi

    rm -f "$STABLE_BIN"
    rmdir "$STABLE_DIR" 2>/dev/null || true
    ok "Binary removed."

    echo ""
    echo "  Blamely uninstalled. Attribution history is kept at ~/.blamely/db.sqlite."
    echo "  Remove it manually if you want to wipe all history:"
    echo "    rm -rf ~/.blamely"
}

do_repair() {
    if [ -x "$STABLE_BIN" ]; then
        "$STABLE_BIN" repair
    elif command -v blamely >/dev/null 2>&1; then
        blamely repair
    else
        die "blamely binary not found. Run: ./scripts/install.sh"
    fi
}

# ── main ─────────────────────────────────────────────────────────────────────

# --with-plugins can appear anywhere (`./scripts/install.sh --with-plugins` or
# `./scripts/install.sh install --with-plugins`); whatever's left is the subcommand.
WITH_PLUGINS=0
OBFUSCATE=0
SUBCMD="install"
for arg in "$@"; do
    case "$arg" in
        --with-plugins)        WITH_PLUGINS=1 ;;
        --obfuscate|--release) OBFUSCATE=1 ;;
        *)                     SUBCMD="$arg" ;;
    esac
done

case "$SUBCMD" in
    install)   do_install ;;
    rebuild)   do_rebuild ;;
    uninstall) do_uninstall ;;
    repair)    do_repair ;;
    *)
        echo "Usage: $0 [install|rebuild|uninstall|repair] [--with-plugins]"
        exit 1
        ;;
esac
