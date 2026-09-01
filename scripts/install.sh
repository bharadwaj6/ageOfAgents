#!/usr/bin/env sh
# Install the aoa binary from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/bharadwaj6/ageOfAgents/main/scripts/install.sh | sh
#
# Env: AOA_VERSION (default: latest release), AOA_INSTALL_DIR (default: ~/.local/bin).
set -eu

REPO="bharadwaj6/ageOfAgents"
INSTALL_DIR="${AOA_INSTALL_DIR:-$HOME/.local/bin}"

die() { echo "install.sh: $*" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || die "curl is required"
command -v tar  >/dev/null 2>&1 || die "tar is required"

os="$(uname -s)"
case "$os" in
  Darwin|Linux) ;;
  *) die "no release build for $os — install with: go install github.com/$REPO/cmd/aoa@latest" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  arch="x86_64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) die "unsupported architecture $(uname -m) — install with: go install github.com/$REPO/cmd/aoa@latest" ;;
esac

version="${AOA_VERSION:-}"
if [ -z "$version" ]; then
  version="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)"
  [ -n "$version" ] || die "could not resolve the latest release; set AOA_VERSION=vX.Y.Z"
fi

asset="aoa_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "aoa $version ($os/$arch) -> $INSTALL_DIR"
curl -fsSL -o "$tmp/$asset" "$base/$asset" || die "no such asset: $asset ($version)"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "release $version has no checksums.txt"

# Verify before unpacking: this script is run straight off the network.
if command -v sha256sum >/dev/null 2>&1; then
  sha_check="sha256sum -c -"
elif command -v shasum >/dev/null 2>&1; then
  sha_check="shasum -a 256 -c -"
else
  die "need sha256sum or shasum to verify the download"
fi
(cd "$tmp" && grep " $asset\$" checksums.txt | $sha_check >/dev/null) || die "checksum mismatch for $asset"

tar -xzf "$tmp/$asset" -C "$tmp" aoa
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/aoa" "$INSTALL_DIR/aoa"

echo "installed: $INSTALL_DIR/aoa"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) "$INSTALL_DIR/aoa" version ;;
  *) echo "note: $INSTALL_DIR is not on your PATH — add it, or run $INSTALL_DIR/aoa directly" ;;
esac
