#!/bin/sh
# Port-style launcher, following the same convention as PortMaster/ArkOS
# ports: cd into this script's own directory so relative asset paths work
# regardless of where the SD card mounts, then exec the binary.
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR" || exit 1

export POC_FONT_PATH="$DIR/assets/font.ttf"

LOGDIR="$DIR/logs"
mkdir -p "$LOGDIR"
./bin/leaf-mlp1-poc-arm64 >"$LOGDIR/run.log" 2>&1
