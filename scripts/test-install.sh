#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEMP=$(mktemp -d "${TMPDIR:-/tmp}/codexlink-install-test.XXXXXX")
trap 'rm -rf "$TEMP"' EXIT INT TERM
mkdir -p "$TEMP/bundle" "$TEMP/bin"
printf '#!/bin/sh\nprintf "codexlink-test\\n"\n' > "$TEMP/bundle/codexlink"
printf '#!/bin/sh\nprintf "cloudflared-test\\n"\n' > "$TEMP/bundle/cloudflared"
printf 'license\n' > "$TEMP/bundle/LICENSE"
printf 'readme\n' > "$TEMP/bundle/README.md"
printf 'readme zh\n' > "$TEMP/bundle/README.zh-CN.md"
printf '#!/bin/sh\nexit 0\n' > "$TEMP/bundle/install.sh"
chmod 755 "$TEMP/bundle/codexlink" "$TEMP/bundle/cloudflared" "$TEMP/bundle/install.sh"

checksum() {
  file=$1 output=$2
  if command -v sha256sum >/dev/null 2>&1; then
    digest=$(sha256sum "$file" | awk '{print $1}')
  else
    digest=$(shasum -a 256 "$file" | awk '{print $1}')
  fi
  printf '%s  %s\n' "$digest" "$(basename "$file")" > "$output"
}

archive="$TEMP/codexlink_linux_amd64.tar.gz"
tar -czf "$archive" -C "$TEMP/bundle" codexlink cloudflared LICENSE README.md README.zh-CN.md install.sh
checksum "$archive" "$TEMP/checksum"
CODEXLINK_BUNDLE_FILE="$archive" \
CODEXLINK_CHECKSUM_FILE="$TEMP/checksum" \
CODEXLINK_INSTALL_DIR="$TEMP/bin" \
CODEXLINK_SKIP_GIT=1 \
CODEXLINK_SKIP_PATH_UPDATE=1 \
CODEXLINK_NO_START=1 \
"$ROOT/install.sh"
[ "$("$TEMP/bin/codexlink")" = codexlink-test ]
[ "$("$TEMP/bin/cloudflared")" = cloudflared-test ]
printf '#!/bin/sh\nprintf "old-codexlink\\n"\n' > "$TEMP/bin/codexlink"
printf '#!/bin/sh\nprintf "old-cloudflared\\n"\n' > "$TEMP/bin/cloudflared"
chmod 755 "$TEMP/bin/codexlink" "$TEMP/bin/cloudflared"
CODEXLINK_BUNDLE_FILE="$archive" \
CODEXLINK_CHECKSUM_FILE="$TEMP/checksum" \
CODEXLINK_INSTALL_DIR="$TEMP/bin" \
CODEXLINK_SKIP_GIT=1 \
CODEXLINK_SKIP_PATH_UPDATE=1 \
CODEXLINK_NO_START=1 \
"$ROOT/install.sh" >/dev/null
[ "$("$TEMP/bin/codexlink")" = codexlink-test ]
[ "$("$TEMP/bin/cloudflared")" = cloudflared-test ]
if find "$TEMP/bin" \( -name '*.new.*' -o -name '*.old.*' \) | grep . >/dev/null; then
  echo "installer left transaction files behind" >&2
  exit 1
fi

mkdir -p "$TEMP/start-tmp"
output=$(
  TMPDIR="$TEMP/start-tmp" \
  CODEXLINK_BUNDLE_FILE="$archive" \
  CODEXLINK_CHECKSUM_FILE="$TEMP/checksum" \
  CODEXLINK_INSTALL_DIR="$TEMP/start-bin" \
  CODEXLINK_SKIP_GIT=1 \
  CODEXLINK_SKIP_PATH_UPDATE=1 \
  "$ROOT/install.sh"
)
[ "$(printf '%s\n' "$output" | tail -n 1)" = codexlink-test ]
if find "$TEMP/start-tmp" -mindepth 1 -maxdepth 1 -name 'codexlink-install.*' | grep . >/dev/null; then
  echo "installer leaked its temporary directory before starting CodexLink" >&2
  exit 1
fi

# A checksum-valid package still must not smuggle links or duplicate paths.
mkdir -p "$TEMP/malicious"
cp "$TEMP/bundle"/codexlink "$TEMP/malicious/codexlink"
ln -s codexlink "$TEMP/malicious/cloudflared"
cp "$TEMP/bundle"/LICENSE "$TEMP/malicious/LICENSE"
cp "$TEMP/bundle"/README.md "$TEMP/malicious/README.md"
cp "$TEMP/bundle"/README.zh-CN.md "$TEMP/malicious/README.zh-CN.md"
cp "$TEMP/bundle"/install.sh "$TEMP/malicious/install.sh"
link_archive="$TEMP/link.tar.gz"
tar -czf "$link_archive" -C "$TEMP/malicious" codexlink cloudflared LICENSE README.md README.zh-CN.md install.sh
checksum "$link_archive" "$TEMP/link.sha256"
if CODEXLINK_BUNDLE_FILE="$link_archive" CODEXLINK_CHECKSUM_FILE="$TEMP/link.sha256" \
  CODEXLINK_INSTALL_DIR="$TEMP/rejected" CODEXLINK_SKIP_GIT=1 CODEXLINK_SKIP_PATH_UPDATE=1 \
  CODEXLINK_NO_START=1 "$ROOT/install.sh" >/dev/null 2>&1; then
  echo "installer accepted a symbolic-link package" >&2
  exit 1
fi

duplicate_archive="$TEMP/duplicate.tar.gz"
tar -czf "$duplicate_archive" -C "$TEMP/bundle" codexlink codexlink cloudflared LICENSE README.md README.zh-CN.md install.sh
checksum "$duplicate_archive" "$TEMP/duplicate.sha256"
if CODEXLINK_BUNDLE_FILE="$duplicate_archive" CODEXLINK_CHECKSUM_FILE="$TEMP/duplicate.sha256" \
  CODEXLINK_INSTALL_DIR="$TEMP/rejected" CODEXLINK_SKIP_GIT=1 CODEXLINK_SKIP_PATH_UPDATE=1 \
  CODEXLINK_NO_START=1 "$ROOT/install.sh" >/dev/null 2>&1; then
  echo "installer accepted a duplicate-path package" >&2
  exit 1
fi
