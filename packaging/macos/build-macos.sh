#!/usr/bin/env bash
#
# Build a native macOS installer for the Blamely CLI: a component .pkg wrapped in
# a double-clickable .dmg.
#
# The .pkg installs `blamely` to /usr/local/bin (on the default PATH) and its
# postinstall script runs `blamely install` AS THE LOGGED-IN USER — Blamely is a
# per-user tool (per-user launchd agent + git/AI hooks under ~/.blamely), so the
# root-run installer must target the human at the GUI, not root.
#
# There is no Apple Developer ID, so the binary is only AD-HOC signed (required on
# Apple Silicon or the loader kills it with "Killed: 9"). The .pkg/.dmg are
# unsigned/un-notarized → Gatekeeper shows a warning on first open; see
# packaging/TRUST.md for the "right-click → Open" path. Hooks are left for a real
# Developer ID: sign the .pkg with `productsign` and notarize with `notarytool`
# right after productbuild/hdiutil below.
#
# Usage:
#   packaging/macos/build-macos.sh \
#     --version 1.7.0 --arch arm64 \
#     --binary dist/blamely_v1.7.0_darwin_arm64/blamely \
#     --out out [--tag v1.7.0] [--sqlite /usr/bin/sqlite3]
#
# --version : CFBundle version for the pkg (no leading v).
# --arch    : amd64 | arm64 (used only in the output filename).
# --binary  : path to the built blamely binary to package.
# --out     : output directory for the .pkg and .dmg.
# --tag     : filename tag (default "v<version>"), so names match the release
#             assets: blamely_<tag>_macos_<arch>.{pkg,dmg}.
# --sqlite  : OPTIONAL. macOS already ships /usr/bin/sqlite3 (the IDE plugins'
#             DB reader), so this is normally unnecessary; pass it only to bundle
#             a specific sqlite3 into /usr/local/bin.
set -euo pipefail

usage() {
  echo "usage: $0 --version X.Y.Z --arch <amd64|arm64> --binary <path> --out <dir> [--tag vX.Y.Z] [--sqlite <path>]" >&2
  exit 2
}

VERSION="" ARCH="" BIN="" OUT="" TAG="" SQLITE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift 2 ;;
    --arch)    ARCH="${2:-}";    shift 2 ;;
    --binary)  BIN="${2:-}";     shift 2 ;;
    --out)     OUT="${2:-}";     shift 2 ;;
    --tag)     TAG="${2:-}";     shift 2 ;;
    --sqlite)  SQLITE="${2:-}";  shift 2 ;;
    *) echo "unknown arg: $1" >&2; usage ;;
  esac
done
[ -n "$VERSION" ] && [ -n "$ARCH" ] && [ -n "$BIN" ] && [ -n "$OUT" ] || usage
[ -f "$BIN" ] || { echo "error: binary not found: $BIN" >&2; exit 1; }
TAG="${TAG:-v$VERSION}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# 1. Stage the payload rooted at / (blamely → /usr/local/bin/blamely).
PAYLOAD="$WORK/payload"
mkdir -p "$PAYLOAD/usr/local/bin"
# ditto --noextattr --norsrc --noqtn copies WITHOUT extended attributes / resource
# forks / quarantine, so pkgbuild doesn't archive stray "._" AppleDouble companions.
ditto --noextattr --norsrc --noqtn "$BIN" "$PAYLOAD/usr/local/bin/blamely"
chmod 0755 "$PAYLOAD/usr/local/bin/blamely"
# Ad-hoc sign so the dynamic loader trusts the binary (mandatory on Apple Silicon).
codesign --force --sign - "$PAYLOAD/usr/local/bin/blamely" \
  || echo "warn: ad-hoc codesign failed (first launch may be blocked)" >&2
if [ -n "$SQLITE" ] && [ -f "$SQLITE" ]; then
  ditto --noextattr --norsrc --noqtn "$SQLITE" "$PAYLOAD/usr/local/bin/sqlite3"
  chmod 0755 "$PAYLOAD/usr/local/bin/sqlite3"
  codesign --force --sign - "$PAYLOAD/usr/local/bin/sqlite3" || true
fi

mkdir -p "$OUT"
PKG_NAME="blamely_${TAG}_macos_${ARCH}.pkg"
DMG_NAME="blamely_${TAG}_macos_${ARCH}.dmg"

# 2. Component pkg with the postinstall script (runs `blamely install` as the user).
COMPONENT="$WORK/blamely-component.pkg"
pkgbuild \
  --root "$PAYLOAD" \
  --scripts "$HERE/scripts" \
  --identifier ai.blamely.cli \
  --version "$VERSION" \
  --install-location / \
  "$COMPONENT"

# 3. Product (distribution) pkg — the double-clickable installer.
#    (Add `productsign --sign "Developer ID Installer: …"` here once a cert exists.)
productbuild --package "$COMPONENT" "$OUT/$PKG_NAME"

# 4. Wrap the pkg in a compressed .dmg so it's a single downloadable disk image.
DMGROOT="$WORK/dmg"
mkdir -p "$DMGROOT"
cp "$OUT/$PKG_NAME" "$DMGROOT/"
hdiutil create -volname "Blamely CLI" -srcfolder "$DMGROOT" \
  -ov -format UDZO "$OUT/$DMG_NAME" >/dev/null

echo "built:"
echo "  $OUT/$PKG_NAME"
echo "  $OUT/$DMG_NAME"
