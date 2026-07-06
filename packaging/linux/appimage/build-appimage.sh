#!/usr/bin/env bash
#
# Build a Blamely CLI AppImage — a single self-contained, distro-independent
# executable — from a prebuilt binary.
#
# Requires `appimagetool` on PATH (CI downloads it; locally grab the AppImage from
# https://github.com/AppImage/appimagetool/releases and put it on PATH).
#
# NOTE: an AppImage is a portable runner, not an installer; it does NOT auto-wire
# the daemon/hooks (users run `./blamely_*.AppImage install` once).
#
# Usage:
#   packaging/linux/appimage/build-appimage.sh \
#     --arch amd64 --binary dist/blamely_v1.7.0_linux_amd64/blamely \
#     --out out [--tag v1.7.0]
#
# Output: <out>/blamely_<tag>_linux_<arch>.AppImage
set -euo pipefail

usage() {
  echo "usage: $0 --arch <amd64|arm64> --binary <path> --out <dir> [--tag vX.Y.Z]" >&2
  exit 2
}

ARCH="" BIN="" OUT="" TAG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --arch)   ARCH="${2:-}"; shift 2 ;;
    --binary) BIN="${2:-}";  shift 2 ;;
    --out)    OUT="${2:-}";  shift 2 ;;
    --tag)    TAG="${2:-}";  shift 2 ;;
    *) echo "unknown arg: $1" >&2; usage ;;
  esac
done
[ -n "$ARCH" ] && [ -n "$BIN" ] && [ -n "$OUT" ] || usage
[ -f "$BIN" ] || { echo "error: binary not found: $BIN" >&2; exit 1; }
command -v appimagetool >/dev/null 2>&1 || { echo "error: appimagetool not found on PATH" >&2; exit 1; }
TAG="${TAG:-vdev}"

# Map Go arch → the arch string appimagetool stamps into the image.
case "$ARCH" in
  amd64) AI_ARCH="x86_64" ;;
  arm64) AI_ARCH="aarch64" ;;
  *) echo "error: unsupported arch: $ARCH" >&2; exit 1 ;;
esac

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Assemble the AppDir: AppRun + .desktop + icon + the binary under usr/bin.
APPDIR="$WORK/Blamely.AppDir"
mkdir -p "$APPDIR/usr/bin"
cp "$BIN" "$APPDIR/usr/bin/blamely"
chmod 0755 "$APPDIR/usr/bin/blamely"
cp "$HERE/AppRun" "$APPDIR/AppRun";           chmod 0755 "$APPDIR/AppRun"
cp "$HERE/blamely.desktop" "$APPDIR/blamely.desktop"
cp "$HERE/blamely.png" "$APPDIR/blamely.png"
cp "$HERE/blamely.png" "$APPDIR/.DirIcon"

mkdir -p "$OUT"
OUT_ABS="$(cd "$OUT" && pwd)"
OUTFILE="$OUT_ABS/blamely_${TAG}_linux_${ARCH}.AppImage"

# ARCH tells appimagetool which runtime to embed. --no-appstream skips optional
# AppStream metadata validation (we ship none).
ARCH="$AI_ARCH" appimagetool --no-appstream "$APPDIR" "$OUTFILE"

echo "built: $OUTFILE"
