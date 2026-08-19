#!/bin/sh
# Blamely build + install / uninstall helper for local development.
#
# Usage:
#   ./scripts/install.sh                  build + install (CLI + hooks only)
#   ./scripts/install.sh --with-plugins   also install the PUBLISHED VS Code/JetBrains plugins
#   ./scripts/install.sh uninstall        uninstall + remove binary
#   ./scripts/install.sh rebuild          rebuild CLI + both plugins FROM SOURCE, no install
#   ./scripts/install.sh rebuild --skip-plugins   rebuild the CLI only
#   ./scripts/install.sh repair           remove stale legacy hooks only
#
# The two plugin paths differ on purpose, and the difference is the whole point
# of this script:
#
#   rebuild            builds ../vscode-plugin and ../intellij-plugin FROM THIS
#                      CHECKOUT and sideloads those artifacts. This is the dev
#                      loop: the plugins you get are the code you are editing.
#   install --with-plugins
#                      installs the PUBLISHED builds (Open VSX / JetBrains
#                      Marketplace), matching what end users receive. It will
#                      overwrite a sideloaded dev build — which is why plain
#                      `install` skips plugins entirely.
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
    cd "$REPO_ROOT"
    mkdir -p "$STABLE_DIR"
    info "Building blamely from source (stripped, trim-path — same flags as the release pipeline)..."
    # -s  strip the Go symbol table
    # -w  strip DWARF debug info
    # -trimpath  remove absolute build paths (no /Users/<you>/… embedded)
    # -buildvcs=false  don't stamp git metadata into the binary
    # Standard release hygiene, NOT obfuscation — Blamely is open source and
    # ships unobfuscated everywhere. Rebuild without these flags for debugging.
    go build -trimpath -buildvcs=false -ldflags="-s -w" -o "$STABLE_BIN" ./cmd/blamely
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

# ── local plugin builds ──────────────────────────────────────────────────────
#
# Both plugins live in sibling checkouts next to this one. They are optional:
# a missing directory or toolchain warns and moves on rather than failing the
# rebuild, so someone with only the Go toolchain can still rebuild the CLI.

VSCODE_PLUGIN_DIR="$(cd "$REPO_ROOT/.." 2>/dev/null && pwd)/vscode-plugin"
JETBRAINS_PLUGIN_DIR="$(cd "$REPO_ROOT/.." 2>/dev/null && pwd)/intellij-plugin"

warn() { printf '\033[33m  !\033[0m %s\n' "$*"; }

# run_quiet <logfile> <label> <command...>
#
# Runs a build with BOTH streams captured, and shows the log's tail only when it
# fails. The plugin builds are noisy on stderr even when they succeed — the
# JetBrains gradle build spawns a headless IDE that dumps stack traces at SEVERE
# on every run — so letting stderr through makes a clean rebuild look broken.
# Returns the command's exit status.
run_quiet() {
    _log="$1"; _label="$2"; shift 2
    if "$@" >"$_log" 2>&1; then
        return 0
    fi
    warn "$_label failed — last 20 lines of $_log:"
    tail -20 "$_log" | sed 's/^/      /'
    return 1
}

# build_vscode_plugin compiles ../vscode-plugin into a .vsix and installs it
# into every VS Code-family editor the CLI detects (VS Code, Cursor,
# Antigravity, …) via `blamely install-vsix`.
build_vscode_plugin() {
    if [ ! -d "$VSCODE_PLUGIN_DIR" ]; then
        warn "VS Code plugin: $VSCODE_PLUGIN_DIR not found — skipped."
        return 0
    fi
    if ! command -v npm >/dev/null 2>&1; then
        warn "VS Code plugin: npm not installed — skipped."
        return 0
    fi

    info "Building the VS Code extension from source..."
    VSCODE_LOG="${TMPDIR:-/tmp}/blamely-vscode-build.log"
    run_quiet "$VSCODE_LOG" "VS Code plugin build" sh -c '
        cd "$1" || exit 1
        # `npm ci` needs a lockfile in sync with package.json; fall back to
        # `npm install` so a locally bumped version does not block the build.
        if [ ! -d node_modules ]; then
            npm ci || npm install || exit 1
        fi
        # Drop stale packages first, so the glob below cannot pick an older one.
        rm -f ./*.vsix
        npm run vsix
    ' _ "$VSCODE_PLUGIN_DIR" \
        || { warn "Skipped. Re-run by hand with: (cd $VSCODE_PLUGIN_DIR && npm run vsix)"; return 0; }

    VSIX="$(ls -t "$VSCODE_PLUGIN_DIR"/*.vsix 2>/dev/null | head -1)"
    if [ -z "$VSIX" ]; then
        warn "VS Code plugin: build produced no .vsix — skipped."
        return 0
    fi
    ok "VSIX built: $VSIX"

    info "Installing it into every detected editor..."
    "$STABLE_BIN" install-vsix "$VSIX" || warn "VS Code plugin: install failed."
}

# build_jetbrains_plugin compiles ../intellij-plugin into a distribution zip and
# installs it into every JetBrains IDE the CLI detects. The gradle build is the
# slow half of a rebuild — a few minutes on a cold cache.
build_jetbrains_plugin() {
    if [ ! -d "$JETBRAINS_PLUGIN_DIR" ]; then
        warn "JetBrains plugin: $JETBRAINS_PLUGIN_DIR not found — skipped."
        return 0
    fi
    if ! command -v java >/dev/null 2>&1; then
        warn "JetBrains plugin: no JDK on PATH — skipped."
        return 0
    fi

    info "Building the JetBrains plugin from source (gradle — this is the slow one)..."
    JETBRAINS_LOG="${TMPDIR:-/tmp}/blamely-jetbrains-build.log"
    run_quiet "$JETBRAINS_LOG" "JetBrains plugin build" \
        sh -c 'cd "$1" && ./build.sh' _ "$JETBRAINS_PLUGIN_DIR" \
        || { warn "Skipped. Re-run by hand with: (cd $JETBRAINS_PLUGIN_DIR && ./build.sh)"; return 0; }

    ZIP="$(ls -t "$JETBRAINS_PLUGIN_DIR"/build/distributions/*.zip 2>/dev/null | head -1)"
    if [ -z "$ZIP" ]; then
        warn "JetBrains plugin: build produced no zip — skipped."
        return 0
    fi
    ok "Plugin zip built: $ZIP"

    info "Installing it into every detected JetBrains IDE..."
    "$STABLE_BIN" install-jetbrains-zip "$ZIP" || warn "JetBrains plugin: install failed."
}

do_rebuild() {
    require_go
    build_binary

    if [ "$SKIP_PLUGINS" = "1" ]; then
        echo ""
        echo "  CLI rebuilt only (--skip-plugins) — install state was not touched."
        return 0
    fi

    build_vscode_plugin
    build_jetbrains_plugin

    echo ""
    echo "  Rebuilt CLI + plugins from source — install state was not touched."
    echo "  Reload your editors (JetBrains IDEs need a full restart) to pick the plugins up."
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
SKIP_PLUGINS=0
SUBCMD="install"
for arg in "$@"; do
    case "$arg" in
        --with-plugins) WITH_PLUGINS=1 ;;
        --skip-plugins) SKIP_PLUGINS=1 ;;
        *)              SUBCMD="$arg" ;;
    esac
done

case "$SUBCMD" in
    install)   do_install ;;
    rebuild)   do_rebuild ;;
    uninstall) do_uninstall ;;
    repair)    do_repair ;;
    *)
        echo "Usage: $0 [install|rebuild|uninstall|repair] [--with-plugins] [--skip-plugins]"
        exit 1
        ;;
esac
