#!/bin/bash
set -euo pipefail

if [[ $# != 1 || ! -d $1 ]]; then
    printf 'Usage: bash %s EXISTING_OUTPUT_DIRECTORY\n' "$0" >&2
    exit 2
fi

export PATH=/usr/bin:/bin:/usr/sbin:/sbin
if [[ $(uname -s) != Darwin || $(uname -m) != arm64 || $(id -u) == 0 ]]; then
    printf 'Build as a regular user on Apple Silicon macOS.\n' >&2
    exit 1
fi
xcrun --find clang >/dev/null
umask 077

version=8.12.0
sha256=b0100a879b2d45dc97d8fda941127609d648f8913008db1b65a2f72a8ae36c8b
source_name="redis-$version"
bundle="$source_name-darwin-arm64-core"
out=$(cd -- "$1" && pwd -P)
work=$(mktemp -d "$out/.redis-build.XXXXXXXX")
trap 'rm -rf -- "$work"' EXIT

curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
    "https://download.redis.io/releases/$source_name.tar.gz" -o "$work/source.tar.gz"
printf '%s  %s\n' "$sha256" "$work/source.tar.gz" | shasum -a 256 -c -
tar -xzf "$work/source.tar.gz" -C "$work"

export MACOSX_DEPLOYMENT_TARGET=15.0
make -C "$work/$source_name/src" -j "$(sysctl -n hw.logicalcpu)" \
    CC=clang CXX=clang++ MALLOC=libc BUILD_TLS=no redis-server redis-cli

mkdir -p "$work/$bundle/bin"
install -m 0755 "$work/$source_name/src/redis-server" "$work/$source_name/src/redis-cli" "$work/$bundle/bin"
install -m 0644 "$work/$source_name/LICENSE.txt" "$work/$bundle/"
COPYFILE_DISABLE=1 tar -czf "$work/$bundle.tar.gz" -C "$work" "$bundle"
ln "$work/$bundle.tar.gz" "$out/$bundle.tar.gz"
shasum -a 256 "$out/$bundle.tar.gz"
