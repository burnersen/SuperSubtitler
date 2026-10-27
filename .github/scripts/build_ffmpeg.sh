#!/usr/bin/env bash
# SuperSubtitler (https://github.com/burnersen/SuperSubtitler)
# Copyright (C) 2026 burnersen
# SPDX-License-Identifier: GPL-3.0-only
#
# Builds the lean FFmpeg SuperSubtitler uses: FFmpeg's own code only (plus
# zlib for MKV files with compressed tracks), LGPL, no GPL or non-free parts,
# cross-compiled for Windows x64 with mingw-w64. SuperSubtitler only reads
# audio, mixes channels and muxes MKV/SRT with stream copy - nothing here
# needs x264, x265 or any other external library.
#
# Usage: build_ffmpeg.sh <workdir> <outdir>
# Output: <outdir>/ffmpeg-<ver>-supersubtitler-win64-lgpl.zip plus the exact
# source archives it was built from (they go into the mirror next to it, as
# the LGPL asks of anyone who passes the binaries on).
set -euo pipefail

FF_VERSION=8.1.2
FF_SHA256=464beb5e7bf0c311e68b45ae2f04e9cc2af88851abb4082231742a74d97b524c
ZLIB_VERSION=1.3.1
ZLIB_SHA256=9a93b2b7dfdac77ceba5a558a580e74667dd6fede4585b91eefb60f03b72df23

WORK=$(realpath -m "${1:-./ffbuild}")
OUT=$(realpath -m "${2:-./ffout}")
HOST=x86_64-w64-mingw32
NAME=ffmpeg-${FF_VERSION}-supersubtitler-win64-lgpl
mkdir -p "$WORK" "$OUT"
cd "$WORK"

fetch() { # url file sha256
  [ -f "$2" ] || curl -fsSL -o "$2" "$1"
  echo "$3  $2" | sha256sum -c -
}
fetch "https://ffmpeg.org/releases/ffmpeg-${FF_VERSION}.tar.xz" "ffmpeg-${FF_VERSION}.tar.xz" "$FF_SHA256"
fetch "https://zlib.net/fossils/zlib-${ZLIB_VERSION}.tar.gz" "zlib-${ZLIB_VERSION}.tar.gz" "$ZLIB_SHA256"

DEPS=$WORK/deps
rm -rf "zlib-${ZLIB_VERSION}" "ffmpeg-${FF_VERSION}" "$DEPS"
mkdir -p "$DEPS/include" "$DEPS/lib"

tar xzf "zlib-${ZLIB_VERSION}.tar.gz"
make -C "zlib-${ZLIB_VERSION}" -f win32/Makefile.gcc PREFIX=${HOST}- libz.a >/dev/null
cp "zlib-${ZLIB_VERSION}"/{zlib.h,zconf.h} "$DEPS/include/"
cp "zlib-${ZLIB_VERSION}/libz.a" "$DEPS/lib/"

tar xJf "ffmpeg-${FF_VERSION}.tar.xz"
cd "ffmpeg-${FF_VERSION}"
CONFIGURE=(
  --target-os=mingw32 --arch=x86_64 --cross-prefix=${HOST}- --enable-cross-compile
  --pkg-config=false
  --disable-autodetect --enable-w32threads --enable-zlib
  --disable-shared --enable-static
  --disable-network --disable-doc --disable-ffplay --disable-debug
  --extra-cflags="-I$DEPS/include" --extra-ldflags="-L$DEPS/lib -static"
)
./configure "${CONFIGURE[@]}" >"$WORK/configure.log" 2>&1 || { tail -40 "$WORK/configure.log"; exit 1; }
grep -E "^License:" "$WORK/configure.log" | tee "$WORK/license.txt"
grep -q "LGPL version 2.1 or later" "$WORK/license.txt" || { echo "not an LGPL build"; exit 1; }
make -j"$(nproc)" ffmpeg.exe ffprobe.exe >"$WORK/make.log" 2>&1 || { tail -40 "$WORK/make.log"; exit 1; }
${HOST}-strip ffmpeg.exe ffprobe.exe

# The package: bin/ffmpeg.exe + bin/ffprobe.exe, the licence texts and how it
# was built. Fixed timestamps and order, so the zip only changes with its content.
PKG=$WORK/$NAME
rm -rf "$PKG"; mkdir -p "$PKG/bin"
cp ffmpeg.exe ffprobe.exe "$PKG/bin/"
cp COPYING.LGPLv2.1 "$PKG/COPYING.LGPLv2.1.txt"
cp LICENSE.md "$PKG/FFMPEG-LICENSE.md"
cp "$WORK/zlib-${ZLIB_VERSION}/LICENSE" "$PKG/ZLIB-LICENSE.txt"
{
  echo "FFmpeg ${FF_VERSION} for SuperSubtitler - lean LGPL build for Windows x64"
  echo
  echo "Built from the official, unmodified sources (both are in the same release):"
  echo "  ffmpeg-${FF_VERSION}.tar.xz  sha256 ${FF_SHA256}"
  echo "  zlib-${ZLIB_VERSION}.tar.gz   sha256 ${ZLIB_SHA256}"
  echo "Build script: .github/scripts/build_ffmpeg.sh in https://github.com/burnersen/SuperSubtitler"
  echo "Compiler: $(${HOST}-gcc --version | head -1)"
  echo "$(cat "$WORK/license.txt")"
  echo
  echo "configure ${CONFIGURE[*]}" | sed "s|$DEPS|<deps>|g"
} > "$PKG/BUILD.txt"
find "$PKG" -exec touch -d "@0" {} +
(cd "$WORK" && rm -f "$OUT/$NAME.zip" && find "$NAME" -type f | LC_ALL=C sort | zip -X -q "$OUT/$NAME.zip" -@)
cp "$WORK/ffmpeg-${FF_VERSION}.tar.xz" "$WORK/zlib-${ZLIB_VERSION}.tar.gz" "$OUT/"
ls -l "$OUT"
sha256sum "$OUT"/*
