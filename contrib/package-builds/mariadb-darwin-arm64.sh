#!/bin/bash
set -euo pipefail

if [[ ( $# != 2 && $# != 3 ) || ! -d $1 || $2 != /* || ! -x $2 ]]; then
    printf 'Usage: bash %s EXISTING_OUTPUT_DIRECTORY /absolute/path/to/cmake [BUILD_DIRECTORY_TO_RESUME]\n' "$0" >&2
    exit 2
fi

export PATH=/usr/bin:/bin:/usr/sbin:/sbin
if [[ $(uname -s) != Darwin || $(uname -m) != arm64 || $(id -u) == 0 ]]; then
    printf 'Build as a regular user on Apple Silicon macOS.\n' >&2
    exit 1
fi
xcrun --find clang >/dev/null
umask 077

version=11.8.9
source_name="mariadb-$version"
bundle="$source_name-darwin-arm64"
prefix=/opt/lenv-mariadb
out=$(cd -- "$1" && pwd -P)
cmake=$2
if [[ -e "$out/$bundle.tar.gz" ]]; then
    printf 'Archive already exists in %s\n' "$out" >&2
    exit 1
fi
if [[ $# == 3 ]]; then
    work=$(cd -- "$3" && pwd -P)
    if [[ ${work%/*} != "$out" || ${work##*/} != .mariadb-build.* || ! -f "$work/.configured" ]]; then
        printf 'Resume requires a configured .mariadb-build directory inside %s\n' "$out" >&2
        exit 1
    fi
else
    work=$(mktemp -d "$out/.mariadb-build.XXXXXXXX")
fi
printf 'Build directory: %s\n' "$work" >&2
trap 'exit 143' TERM
trap 'exit 130' INT
trap 'if [[ $? == 0 ]]; then rm -rf -- "$work"; else printf "Build files retained in %s\n" "$work" >&2; fi' EXIT

download() {
    local url=$1 sum=$2 file=$3
    curl --fail --silent --show-error --location --retry 3 --retry-all-errors \
        --proto '=https' --proto-redir '=https' \
        "$url" -o "$file"
    printf '%s  %s\n' "$sum" "$file" | shasum -a 256 -c -
}

export MACOSX_DEPLOYMENT_TARGET=15.0
jobs=$(sysctl -n hw.logicalcpu)
if [[ $# == 2 ]]; then
download "https://archive.mariadb.org/$source_name/source/$source_name.tar.gz" \
    2e015b7e91004142eda8b3a20d3ceb875cf2ae55e72603e7222cd497d4e56fc6 "$work/source.tar.gz"
download 'https://github.com/fmtlib/fmt/releases/download/12.2.0/fmt-12.2.0.zip' \
    a2f4a8d51178f954e4c339007f77edd76ba0cb2e36f87a48e5a5403d9be5878f "$work/fmt.zip"
mkdir -p "$work/build/extra/pcre2/src"
download 'https://github.com/PCRE2Project/pcre2/releases/download/pcre2-10.47/pcre2-10.47.zip' \
    d74c183c86c77248ad50017c7f45bae8f88106a6cca5d87ad09917e1c6fb0784 \
    "$work/build/extra/pcre2/src/pcre2-10.47.zip"
tar -xzf "$work/source.tar.gz" -C "$work"
unzip -q "$work/fmt.zip" -d "$work"

# Preserve an explicit OFF instead of selecting GnuTLS for Connector/C.
patch -d "$work/$source_name" -p1 <<'PATCH'
--- a/cmake/mariadb_connector_c.cmake
+++ b/cmake/mariadb_connector_c.cmake
@@ -12,2 +12,2 @@
-IF(NOT CONC_WITH_SSL)
+IF(NOT DEFINED CONC_WITH_SSL)
   IF(SSL_DEFINES MATCHES "WOLFSSL")
PATCH
"$cmake" -S "$work/$source_name" -B "$work/build" -G 'Unix Makefiles' \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_INSTALL_PREFIX="$prefix" \
    -DCMAKE_OSX_ARCHITECTURES=arm64 -DCMAKE_OSX_DEPLOYMENT_TARGET=15.0 \
    '-DCMAKE_IGNORE_PREFIX_PATH=/opt/homebrew;/usr/local;/opt/local' \
    '-DCMAKE_INSTALL_RPATH=@loader_path/../lib;@loader_path/..' \
    -DWITH_SSL=bundled -DCONC_WITH_SSL=OFF \
    -DWITH_ZLIB=bundled -DWITH_PCRE=bundled \
    -DWITH_LIBFMT=system -DLIBFMT_INCLUDE_DIR="$work/fmt-12.2.0/include" \
    -DWITH_UNIT_TESTS=OFF -DWITH_WSREP=OFF -DWITH_DTRACE=OFF \
    -DWITH_JEMALLOC=OFF -DWITHOUT_MROONGA=ON -DPLUGIN_ROCKSDB=NO \
    -DINSTALL_LAYOUT=STANDALONE -DINSTALL_SBINDIR=bin
touch "$work/.configured"
fi
"$cmake" --build "$work/build" --parallel "$jobs"
DESTDIR="$work/stage" "$cmake" --install "$work/build" --strip

mv "$work/stage$prefix" "$work/$bundle"
COPYFILE_DISABLE=1 tar -czf "$work/$bundle.tar.gz" -C "$work" "$bundle"
ln "$work/$bundle.tar.gz" "$out/$bundle.tar.gz"
shasum -a 256 "$out/$bundle.tar.gz"
