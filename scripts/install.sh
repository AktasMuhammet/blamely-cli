#!/bin/sh
# Blamely build + install / uninstall helper for local development.
#
# Usage:
#   ./scripts/install.sh            build + install
#   ./scripts/install.sh uninstall  uninstall + remove binary
#   ./scripts/install.sh rebuild    rebuild without re-running install
#   ./scripts/install.sh repair     remove stale legacy hooks only
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

build_binary() {
    info "Building blamely from source (stripped, trim-path)..."
    cd "$REPO_ROOT"
    mkdir -p "$STABLE_DIR"
    # -s  strip the Go symbol table
    # -w  strip DWARF debug info
    # -trimpath  remove absolute build paths (no /Users/<you>/… embedded)
    # -buildvcs=false  don't stamp git metadata into the binary
    # Together these produce a smaller, harder-to-introspect binary. Source
    # files are unchanged; rebuild without these flags for normal debugging.
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$STABLE_BIN" ./cmd/blamely
    chmod +x "$STABLE_BIN"
    ok "Binary built: $STABLE_BIN ($(du -h "$STABLE_BIN" | cut -f1))"
}

# ── commands ─────────────────────────────────────────────────────────────────

do_install() {
    require_go
    build_binary

    info "Running blamely install..."
    "$STABLE_BIN" install
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

case "${1:-install}" in
    install)   do_install ;;
    rebuild)   do_rebuild ;;
    uninstall) do_uninstall ;;
    repair)    do_repair ;;
    *)
        echo "Usage: $0 [install|rebuild|uninstall|repair]"
        exit 1
        ;;
esac
