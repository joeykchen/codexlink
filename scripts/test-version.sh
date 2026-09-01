#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
VERSION=$(tr -d '\r\n' < "$ROOT/VERSION")
TEMP=$(mktemp -d "${TMPDIR:-/tmp}/codexlink-version-test.XXXXXX")
trap 'rm -rf "$TEMP"' EXIT INT TERM
cd "$ROOT"
go build -trimpath -ldflags "-X github.com/joeykchen/codexlink/internal/buildinfo.Version=$VERSION" -o "$TEMP/codexlink" ./cmd/codexlink
actual=$($TEMP/codexlink version)
expected="CodexLink $VERSION"
[ "$actual" = "$expected" ] || { printf 'version mismatch: got %s, want %s\n' "$actual" "$expected" >&2; exit 1; }
