#!/usr/bin/env bash
#
# Build the Blamely CLI Linux packages (.deb + .rpm) from a prebuilt binary,
# using nfpm (https://nfpm.goreleaser.com — one config → both formats).
#
# Requires `nfpm` on PATH (CI installs it; locally: `go install
# github.com/goreleaser/nfpm/v2/cmd/nfpm@latest`).
#
# Usage:
#   packaging/linux/build-linux.sh \
#     --version 1.7.0 --arch amd64 \
#     --binary dist/blamely_v1.7.0_linux_amd64/blamely \
#     --out out [--tag v1.7.0]
#
# Output: <out>/blamely_<tag>_linux_<arch>.deb and .rpm
set -euo pipefail

usage() {
  echo "usage: $0 --version X.Y.Z --arch <amd64|arm64> --binary <path> --out <dir> [--tag vX.Y.Z]" >&2
  exit 2
}

VERSION="" ARCH="" BIN="" OUT="" TAG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) VERSION="${2:-}"; shift 2 ;;
    --arch)    ARCH="${2:-}";    shift 2 ;;
    --binary)  BIN="${2:-}";     shift 2 ;;
    --out)     OUT="${2:-}";     shift 2 ;;
    --tag)     TAG="${2:-}";     shift 2 ;;
    *) echo "unknown arg: $1" >&2; usage ;;
  esac
done
[ -n "$VERSION" ] && [ -n "$ARCH" ] && [ -n "$BIN" ] && [ -n "$OUT" ] || usage
[ -f "$BIN" ] || { echo "error: binary not found: $BIN" >&2; exit 1; }
command -v nfpm >/dev/null 2>&1 || { echo "error: nfpm not found on PATH" >&2; exit 1; }
TAG="${TAG:-v$VERSION}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$OUT"
OUT_ABS="$(cd "$OUT" && pwd)"
BIN_ABS="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")"
SCRIPTS_DIR="$HERE/scripts"

# Render nfpm.yaml's ${...} tokens ourselves (sed with | delimiter handles the
# slashes in paths) rather than relying on a specific nfpm version's env-var
# support. All substituted paths are absolute, so nfpm resolves them from anywhere.
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
RENDERED="$WORK/nfpm.yaml"
sed -e "s|\${BIN}|$BIN_ABS|g" \
    -e "s|\${ARCH}|$ARCH|g" \
    -e "s|\${VERSION}|$VERSION|g" \
    -e "s|\${SCRIPTS_DIR}|$SCRIPTS_DIR|g" \
    "$HERE/nfpm.yaml" > "$RENDERED"

nfpm package --config "$RENDERED" --packager deb \
  --target "$OUT_ABS/blamely_${TAG}_linux_${ARCH}.deb"
nfpm package --config "$RENDERED" --packager rpm \
  --target "$OUT_ABS/blamely_${TAG}_linux_${ARCH}.rpm"

echo "built:"
echo "  $OUT_ABS/blamely_${TAG}_linux_${ARCH}.deb"
echo "  $OUT_ABS/blamely_${TAG}_linux_${ARCH}.rpm"
