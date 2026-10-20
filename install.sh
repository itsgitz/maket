#!/bin/sh
# Installs the maket CLI from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/itsgitz/maket/main/install.sh | sh
#
# Environment:
#   MAKET_VERSION       Release tag to install, e.g. v0.1.0 (default: latest)
#   MAKET_INSTALL_DIR   Directory to install maket into
#                       (default: /usr/local/bin if writable, else ~/.local/bin)
#   MAKET_DOWNLOAD_BASE Base URL that serves the binary and checksums.txt
#                       (overrides MAKET_VERSION; used for testing)
set -eu

REPO_URL="https://github.com/itsgitz/maket"

say() {
  printf 'maket-install: %s\n' "$*" >&2
}

die() {
  say "$*"
  exit 1
}

# download URL FILE: fetches URL into FILE with curl, or wget when curl is missing.
download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    die "need curl or wget to download maket"
  fi
}

case "$(uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *)
    die "unsupported OS $(uname -s). Windows users should download maket-windows-amd64.exe or maket-windows-arm64.exe from $REPO_URL/releases/latest"
    ;;
esac

case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "unsupported architecture $(uname -m)" ;;
esac

if [ -n "${MAKET_DOWNLOAD_BASE:-}" ]; then
  base=${MAKET_DOWNLOAD_BASE%/}
elif [ -n "${MAKET_VERSION:-}" ]; then
  base="$REPO_URL/releases/download/$MAKET_VERSION"
else
  base="$REPO_URL/releases/latest/download"
fi

asset="maket-$os-$arch"

tmp=$(mktemp -d) || die "cannot create a temporary directory"
trap 'rm -rf "$tmp"' EXIT

say "downloading $asset from $base"
download "$base/$asset" "$tmp/$asset" || die "failed to download $base/$asset"
download "$base/checksums.txt" "$tmp/checksums.txt" || die "failed to download $base/checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
else
  die "need sha256sum or shasum to verify the download"
fi

expected=$(awk -v f="$asset" '{ name = $2; sub(/^\*/, "", name); if (name == f) { print $1; exit } }' "$tmp/checksums.txt")
if [ -z "$expected" ]; then
  die "no checksum for $asset in checksums.txt; nothing installed"
fi
if [ "$actual" != "$expected" ]; then
  die "checksum mismatch for $asset (expected $expected, got $actual); nothing installed"
fi

fallback=0
if [ -n "${MAKET_INSTALL_DIR:-}" ]; then
  dir=$MAKET_INSTALL_DIR
elif [ -w /usr/local/bin ]; then
  dir=/usr/local/bin
else
  dir=$HOME/.local/bin
  fallback=1
fi

mkdir -p "$dir" || die "cannot create $dir"
dest="$dir/maket"

chmod 755 "$tmp/$asset"
mv -f "$tmp/$asset" "$dest" || die "cannot write $dest (run again with MAKET_INSTALL_DIR set to a writable directory)"

version=$("$dest" version) || die "installed binary failed to run: $dest version"
say "Installed maket $version to $dest"

if [ "$fallback" -eq 1 ]; then
  case ":$PATH:" in
    *":$dir:"*) ;;
    *) say "$dir is not on your PATH; add it to your shell profile to run maket" ;;
  esac
fi
