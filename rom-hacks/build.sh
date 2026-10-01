#!/bin/sh
# Builds the app for the device.
#
# sdlui is cgo against SDL2, so this has to run somewhere with aarch64
# SDL2 headers — in practice, on the device itself:
#
#   ssh ark@<device>
#   sudo apt install golang libsdl2-dev libsdl2-ttf-dev
#   cd /roms/tools/rom-hacks && ./build.sh
#
# The bundled bin/xdelta3 is already an aarch64 static binary and is not
# rebuilt here. To rebuild it (from github.com/jmacd/xdelta):
#
#   aarch64-linux-gnu-gcc -O2 -static -DXD3_MAIN=1 \
#     -DSECONDARY_DJW=1 -DSECONDARY_FGK=1 -DSECONDARY_LZMA=1 \
#     -DXD3_USE_LARGEFILE64=1 -DSIZEOF_SIZE_T=8 \
#     -DSIZEOF_UNSIGNED_LONG_LONG=8 -DSIZEOF_UNSIGNED_INT=4 \
#     -DSIZEOF_UNSIGNED_LONG=8 -I<xz>/include \
#     -o bin/xdelta3 xdelta3/xdelta3.c <xz>/lib/liblzma.a -lpthread
#
# SECONDARY_LZMA is not optional: PlayStation patches are routinely
# encoded with "-S lzma", and without it xdelta3 refuses them with
# "unavailable secondary compressor: LZMA". liblzma comes from
# github.com/tukaani-project/xz, cross-built static.
set -e
cd "$(dirname "$0")"
mkdir -p bin
go build -trimpath -ldflags="-s -w" -o bin/rom-hacks-arm64 ./cmd/hacks
echo "built bin/rom-hacks-arm64"
