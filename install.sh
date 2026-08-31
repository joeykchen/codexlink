#!/bin/sh
set -eu

PRODUCT="codexlink"
REPOSITORY="${CODEXLINK_REPOSITORY:-joeykchen/codexlink}"
INSTALL_DIR="${CODEXLINK_INSTALL_DIR:-$HOME/.local/bin}"
START_AFTER_INSTALL="${CODEXLINK_NO_START:-0}"
SKIP_GIT="${CODEXLINK_SKIP_GIT:-0}"
SKIP_PATH="${CODEXLINK_SKIP_PATH_UPDATE:-0}"

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
  expected="$(awk 'NR == 1 { print $1 }' "$checksum")"
  [ -n "$expected" ] || fail "empty checksum file"
  if have sha256sum; then
    actual="$(sha256sum "$archive" | awk '{ print $1 }')"
  elif have shasum; then
    actual="$(shasum -a 256 "$archive" | awk '{ print $1 }')"
  else
    fail "no SHA-256 verifier is available"
  fi
  [ "$actual" = "$expected" ] || fail "download checksum mismatch"
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

main() {
  os="$(normalize_os)"
  arch="$(normalize_arch)"
  extension="tar.gz"
  asset="${PRODUCT}_${os}_${arch}.${extension}"
  temporary="$(mktemp -d "${TMPDIR:-/tmp}/codexlink-install.XXXXXX")"
  trap 'rm -rf "$temporary"' EXIT INT TERM
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
  mkdir -p "$temporary/unpacked" "$INSTALL_DIR"
  tar -xzf "$archive" -C "$temporary/unpacked"
  [ -f "$temporary/unpacked/codexlink" ] || fail "package is missing codexlink"
  [ -f "$temporary/unpacked/cloudflared" ] || fail "package is missing cloudflared"

  for binary in codexlink cloudflared; do
    next="$INSTALL_DIR/.${binary}.new.$$"
    cp "$temporary/unpacked/$binary" "$next"
    chmod 755 "$next"
    mv -f "$next" "$INSTALL_DIR/$binary"
  done

  persist_path
  export PATH="$INSTALL_DIR:$PATH"
  install_git_automatically "$os"
  say "✓ CodexLink installed in $INSTALL_DIR"
  say "✓ cloudflared installed with CodexLink"
  say "✓ no Go, Homebrew, ripgrep, or manual package installation is required"

  if [ "$START_AFTER_INSTALL" != "1" ]; then
    exec "$INSTALL_DIR/codexlink" "$@"
  fi
}

main "$@"
