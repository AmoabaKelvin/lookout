#!/usr/bin/env sh
set -eu

# Cross-compiles lookout for the Linux architectures we distribute and writes
# the binaries + a sha256 checksum file into dist/.
#
# Usage:
#   sh build.sh            # version defaults to the current git tag/commit
#   VERSION=v1.0.0 sh build.sh

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT_DIR="dist"
BINARY="lookout"

# CGO is disabled so the binaries are fully static (no libc dependency on the
# target), which is what lets one file run across distros of the same arch.
export CGO_ENABLED=0

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

for arch in amd64 arm64; do
  out="$OUT_DIR/${BINARY}-linux-${arch}"
  echo "building $out (version $VERSION)"
  GOOS=linux GOARCH="$arch" go build \
    -ldflags "-s -w -X main.version=$VERSION" \
    -o "$out" .
done

# Checksums let install.sh verify downloads weren't corrupted or tampered with.
# Linux has sha256sum; macOS has shasum. Both emit the same "<hash>  <file>" format.
echo "writing checksums"
if command -v sha256sum >/dev/null 2>&1; then
  ( cd "$OUT_DIR" && sha256sum "${BINARY}-linux-"* > checksums.txt )
else
  ( cd "$OUT_DIR" && shasum -a 256 "${BINARY}-linux-"* > checksums.txt )
fi

echo
echo "done. artifacts in $OUT_DIR/:"
ls -1 "$OUT_DIR"
