#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
DIST=${DIST_DIR:-"$ROOT/dist"}
VERSION=${VERSION:-$(tr -d '\r\n' < "$ROOT/VERSION")}
VERSION_PACKAGE=github.com/joeykchen/codexlink/internal/buildinfo.Version
LDFLAGS=${LDFLAGS:-"-s -w -X $VERSION_PACKAGE=$VERSION"}

rm -rf "$DIST"
mkdir -p "$DIST"

build() {
  os=$1
  arch=$2
  ext=$3
  name="codexlink_${VERSION}_${os}_${arch}${ext}"
  echo "building $name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch \
    go build -trimpath -ldflags "$LDFLAGS" -o "$DIST/$name" ./cmd/codexlink
}

cd "$ROOT"
build linux amd64 ""
build linux arm64 ""
build darwin amd64 ""
build darwin arm64 ""
build windows amd64 ".exe"

if command -v sha256sum >/dev/null 2>&1; then
  (cd "$DIST" && sha256sum codexlink_* > SHA256SUMS)
elif command -v shasum >/dev/null 2>&1; then
  (cd "$DIST" && shasum -a 256 codexlink_* > SHA256SUMS)
fi
