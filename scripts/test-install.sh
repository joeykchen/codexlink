#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
TEMP=$(mktemp -d "${TMPDIR:-/tmp}/codexlink-install-test.XXXXXX")
trap 'rm -rf "$TEMP"' EXIT INT TERM
mkdir -p "$TEMP/bundle" "$TEMP/bin"
printf '#!/bin/sh\nprintf "codexlink-test\\n"\n' > "$TEMP/bundle/codexlink"
printf '#!/bin/sh\nprintf "cloudflared-test\\n"\n' > "$TEMP/bundle/cloudflared"
chmod 755 "$TEMP/bundle/codexlink" "$TEMP/bundle/cloudflared"
tar -czf "$TEMP/codexlink_linux_amd64.tar.gz" -C "$TEMP/bundle" codexlink cloudflared
if command -v sha256sum >/dev/null 2>&1; then
  digest=$(sha256sum "$TEMP/codexlink_linux_amd64.tar.gz" | awk '{print $1}')
else
  digest=$(shasum -a 256 "$TEMP/codexlink_linux_amd64.tar.gz" | awk '{print $1}')
fi
printf '%s  %s\n' "$digest" codexlink_linux_amd64.tar.gz > "$TEMP/checksum"
CODEXLINK_BUNDLE_FILE="$TEMP/codexlink_linux_amd64.tar.gz" \
CODEXLINK_CHECKSUM_FILE="$TEMP/checksum" \
CODEXLINK_INSTALL_DIR="$TEMP/bin" \
CODEXLINK_SKIP_GIT=1 \
CODEXLINK_SKIP_PATH_UPDATE=1 \
CODEXLINK_NO_START=1 \
"$ROOT/install.sh"
[ "$("$TEMP/bin/codexlink")" = codexlink-test ]
[ "$("$TEMP/bin/cloudflared")" = cloudflared-test ]
