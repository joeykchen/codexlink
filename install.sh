#!/bin/sh
set -eu

PRODUCT="codexlink"
REPOSITORY="${CODEXLINK_REPOSITORY:-joeykchen/codexlink}"
INSTALL_DIR="${CODEXLINK_INSTALL_DIR:-$HOME/.local/bin}"
NO_START="${CODEXLINK_NO_START:-0}"
SKIP_GIT="${CODEXLINK_SKIP_GIT:-0}"
SKIP_PATH="${CODEXLINK_SKIP_PATH_UPDATE:-0}"
MAX_ARCHIVE_BYTES=268435456
MAX_ENTRY_BYTES=134217728

say() { printf '%s\n' "$*"; }
fail() { printf 'CodexLink installer: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

normalize_os() {
  case "$(uname -s 2>/dev/null || true)" in
    Darwin) printf darwin ;;
    Linux) printf linux ;;
    *) fail "unsupported operating system" ;;
  esac
}

normalize_arch() {
  case "$(uname -m 2>/dev/null || true)" in
    x86_64|amd64) printf amd64 ;;
    arm64|aarch64) printf arm64 ;;
    *) fail "unsupported CPU architecture" ;;
  esac
}

download() {
  url="$1" destination="$2"
  if have curl; then
    curl --fail --location --silent --show-error --retry 3 --connect-timeout 15 "$url" -o "$destination"
  elif have wget; then
    wget -q --tries=3 --timeout=30 -O "$destination" "$url"
  else
    fail "no system HTTPS downloader is available"
  fi
}

verify_checksum() {
  archive="$1" checksum="$2"
  checksum_bytes="$(wc -c < "$checksum" | tr -d ' ')"
  [ "$checksum_bytes" -le 4096 ] || fail "checksum file is too large"
  expected="$(awk 'NR == 1 { print $1 }' "$checksum" | tr 'A-F' 'a-f')"
  printf '%s' "$expected" | grep -Eq '^[0-9a-f]{64}$' || fail "invalid checksum file"
  if have sha256sum; then
    actual="$(sha256sum "$archive" | awk '{ print $1 }')"
  elif have shasum; then
    actual="$(shasum -a 256 "$archive" | awk '{ print $1 }')"
  else
    fail "no SHA-256 verifier is available"
  fi
  [ "$actual" = "$expected" ] || fail "download checksum mismatch"
}

validate_archive() {
  archive="$1" scratch="$2"
  bytes="$(wc -c < "$archive" | tr -d ' ')"
  [ "$bytes" -le "$MAX_ARCHIVE_BYTES" ] || fail "package exceeds the 256 MiB safety limit"

  names="$scratch/archive.names"
  details="$scratch/archive.details"
  LC_ALL=C tar -tzf "$archive" > "$names" || fail "package is not a readable tar.gz archive"
  LC_ALL=C tar -tvzf "$archive" > "$details" || fail "package metadata cannot be read"

  count=0
  seen='|'
  while IFS= read -r name || [ -n "$name" ]; do
    [ -n "$name" ] || fail "package contains an empty path"
    case "$name" in
      codexlink|cloudflared|LICENSE|README.md|README.zh-CN.md|install.sh) ;;
      *) fail "package contains unexpected path: $name" ;;
    esac
    case "$seen" in
      *"|$name|"*) fail "package contains duplicate path: $name" ;;
    esac
    seen="$seen$name|"
    count=$((count + 1))
  done < "$names"
  [ "$count" -eq 6 ] || fail "package must contain exactly six files"

  for required in codexlink cloudflared LICENSE README.md README.zh-CN.md install.sh; do
    case "$seen" in
      *"|$required|"*) ;;
      *) fail "package is missing $required" ;;
    esac
  done

  detail_count=0
  while IFS= read -r detail || [ -n "$detail" ]; do
    [ "${detail%"${detail#?}"}" = "-" ] || fail "package contains a link or non-regular file"
    detail_count=$((detail_count + 1))
  done < "$details"
  [ "$detail_count" -eq 6 ] || fail "package metadata does not match its file list"
}

as_root() {
  if [ "$(id -u)" -eq 0 ]; then "$@"; return; fi
  if have sudo; then sudo "$@"; return; fi
  return 1
}

install_git_automatically() {
  [ "$SKIP_GIT" = "1" ] && return 0
  have git && return 0
  os="$1"
  say "· Git is missing; CodexLink is provisioning it automatically"
  if [ "$os" = darwin ]; then
    if have brew; then brew install git && return 0; fi
    if have xcode-select; then
      xcode-select --install >/dev/null 2>&1 || true
      i=0
      while [ "$i" -lt 180 ]; do
        have git && return 0
        sleep 5
        i=$((i + 1))
      done
    fi
  else
    if have apt-get; then as_root env DEBIAN_FRONTEND=noninteractive apt-get update -y && as_root env DEBIAN_FRONTEND=noninteractive apt-get install -y git && return 0; fi
    if have dnf; then as_root dnf install -y git && return 0; fi
    if have yum; then as_root yum install -y git && return 0; fi
    if have apk; then as_root apk add --no-cache git && return 0; fi
    if have pacman; then as_root pacman -Sy --noconfirm git && return 0; fi
    if have zypper; then as_root zypper --non-interactive install git && return 0; fi
  fi
  say "! Git could not be provisioned automatically; non-Git CodexLink features remain available"
}

persist_path() {
  [ "$SKIP_PATH" = "1" ] && return 0
  case ":${PATH:-}:" in *:"$INSTALL_DIR":*) return 0 ;; esac
  shell_name="$(basename "${SHELL:-sh}")"
  case "$shell_name" in
    zsh) rc="$HOME/.zshrc" ;;
    bash) rc="$HOME/.bashrc" ;;
    fish)
      mkdir -p "$HOME/.config/fish"
      rc="$HOME/.config/fish/config.fish"
      line="fish_add_path \"$INSTALL_DIR\""
      grep -F "$line" "$rc" >/dev/null 2>&1 || printf '\n%s\n' "$line" >> "$rc"
      return 0
      ;;
    *) rc="$HOME/.profile" ;;
  esac
  marker="# CodexLink managed PATH"
  line="export PATH=\"$INSTALL_DIR:\$PATH\""
  if ! grep -F "$marker" "$rc" >/dev/null 2>&1; then
    printf '\n%s\n%s\n' "$marker" "$line" >> "$rc"
  fi
}

install_binaries() (
  codex_source="$1"
  codex_target="$2"
  tunnel_source="$3"
  tunnel_target="$4"
  codex_next="$codex_target.new.$$"
  tunnel_next="$tunnel_target.new.$$"
  codex_backup="$codex_target.old.$$"
  tunnel_backup="$tunnel_target.old.$$"
  codex_backed_up=0
  tunnel_backed_up=0
  codex_installed=0
  tunnel_installed=0
  transaction_complete=0

  finish_transaction() {
    status=$?
    trap - EXIT INT TERM
    set +e
    rollback_failed=0
    if [ "$transaction_complete" -ne 1 ]; then
      if [ "$codex_installed" -eq 1 ] && ! rm -f "$codex_target"; then rollback_failed=1; fi
      if [ "$tunnel_installed" -eq 1 ] && ! rm -f "$tunnel_target"; then rollback_failed=1; fi
      if [ "$codex_backed_up" -eq 1 ]; then
        if mv -f "$codex_backup" "$codex_target"; then codex_backed_up=0; else rollback_failed=1; fi
      fi
      if [ "$tunnel_backed_up" -eq 1 ]; then
        if mv -f "$tunnel_backup" "$tunnel_target"; then tunnel_backed_up=0; else rollback_failed=1; fi
      fi
    fi
    rm -f "$codex_next" "$tunnel_next"
    if [ "$transaction_complete" -eq 1 ]; then
      rm -f "$codex_backup" "$tunnel_backup"
    else
      [ "$codex_backed_up" -eq 1 ] || rm -f "$codex_backup"
      [ "$tunnel_backed_up" -eq 1 ] || rm -f "$tunnel_backup"
    fi
    if [ "$rollback_failed" -ne 0 ]; then
      printf 'CodexLink installer: installation rollback was incomplete; preserved .old.%s backup files\n' "$$" >&2
      [ "$status" -ne 0 ] || status=1
    fi
    exit "$status"
  }
  trap finish_transaction EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  [ ! -d "$codex_target" ] || fail "$codex_target is a directory"
  [ ! -d "$tunnel_target" ] || fail "$tunnel_target is a directory"
  rm -f "$codex_next" "$tunnel_next" "$codex_backup" "$tunnel_backup"
  cp "$codex_source" "$codex_next"
  cp "$tunnel_source" "$tunnel_next"
  chmod 755 "$codex_next" "$tunnel_next"

  if [ -e "$codex_target" ] || [ -L "$codex_target" ]; then
    mv "$codex_target" "$codex_backup"
    codex_backed_up=1
  fi
  if [ -e "$tunnel_target" ] || [ -L "$tunnel_target" ]; then
    mv "$tunnel_target" "$tunnel_backup"
    tunnel_backed_up=1
  fi

  mv "$codex_next" "$codex_target"
  codex_installed=1
  mv "$tunnel_next" "$tunnel_target"
  tunnel_installed=1
  transaction_complete=1
)

main() {
  os="$(normalize_os)"
  arch="$(normalize_arch)"
  asset="${PRODUCT}_${os}_${arch}.tar.gz"
  temporary="$(mktemp -d "${TMPDIR:-/tmp}/codexlink-install.XXXXXX")"
  cleanup_temporary() {
    status=$?
    trap - EXIT INT TERM
    rm -rf "$temporary"
    exit "$status"
  }
  trap cleanup_temporary EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  archive="$temporary/$asset"
  checksum="$archive.sha256"

  if [ -n "${CODEXLINK_BUNDLE_FILE:-}" ]; then
    cp "$CODEXLINK_BUNDLE_FILE" "$archive"
    cp "${CODEXLINK_CHECKSUM_FILE:?CODEXLINK_CHECKSUM_FILE is required with CODEXLINK_BUNDLE_FILE}" "$checksum"
  else
    if [ -n "${CODEXLINK_VERSION:-}" ]; then
      base="https://github.com/$REPOSITORY/releases/download/v$CODEXLINK_VERSION"
    else
      base="https://github.com/$REPOSITORY/releases/latest/download"
    fi
    say "CodexLink: downloading the self-contained $os/$arch package"
    download "$base/$asset" "$archive"
    download "$base/$asset.sha256" "$checksum"
  fi

  verify_checksum "$archive" "$checksum"
  validate_archive "$archive" "$temporary"
  mkdir -p "$temporary/unpacked" "$INSTALL_DIR"
  # Limit each extracted file before tar writes it, then verify the exact
  # expanded set again before anything reaches the install directory.
  (ulimit -f 262144 && tar -xzf "$archive" -C "$temporary/unpacked") || fail "package extraction failed or exceeded the per-file safety limit"
  total=0
  for file in codexlink cloudflared LICENSE README.md README.zh-CN.md install.sh; do
    source="$temporary/unpacked/$file"
    [ -f "$source" ] && [ ! -L "$source" ] || fail "package is missing regular file $file"
    size="$(wc -c < "$source" | tr -d ' ')"
    [ "$size" -le "$MAX_ENTRY_BYTES" ] || fail "package entry is too large: $file"
    total=$((total + size))
  done
  [ "$total" -le "$MAX_ARCHIVE_BYTES" ] || fail "expanded package exceeds the 256 MiB safety limit"
  install_binaries \
    "$temporary/unpacked/codexlink" "$INSTALL_DIR/codexlink" \
    "$temporary/unpacked/cloudflared" "$INSTALL_DIR/cloudflared"

  persist_path
  export PATH="$INSTALL_DIR:$PATH"
  install_git_automatically "$os"
  say "✓ CodexLink installed in $INSTALL_DIR"
  say "✓ cloudflared installed with CodexLink"
  say "✓ no Go, Homebrew, ripgrep, or manual package installation is required"

  if [ "$NO_START" != "1" ]; then
    rm -rf "$temporary"
    trap - EXIT INT TERM
    exec "$INSTALL_DIR/codexlink" "$@"
  fi
}

main "$@"
