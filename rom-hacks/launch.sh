#!/bin/sh
# Port-style launcher, same convention as PortMaster/ArkOS ports: cd into
# this script's own directory so relative paths work regardless of where
# the SD card mounts, then exec the binary.
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1

export POC_FONT_PATH="$DIR/assets/font.ttf"
# The bundled xdelta3 handles the minority of patches Go cannot decode
# in-process. Putting bin/ on PATH is all the app needs to find it.
export PATH="$DIR/bin:$PATH"

LOGDIR="$DIR/logs"
mkdir -p "$LOGDIR"
./bin/rom-hacks-arm64 >"$LOGDIR/run.log" 2>&1
