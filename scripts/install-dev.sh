#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
INSTALL_DIR="${CODEXLINK_INSTALL_DIR:-$HOME/.local/bin}"
if ! command -v go >/dev/null 2>&1; then
  printf '%s\n' "Developer installation requires the Go toolchain; use ./install.sh for the dependency-free user installation." >&2
  exit 1
fi
mkdir -p "$INSTALL_DIR"
cd "$ROOT"
go build -trimpath -o "$INSTALL_DIR/codexlink" ./cmd/codexlink
printf 'Installed developer build: %s\n' "$INSTALL_DIR/codexlink"
