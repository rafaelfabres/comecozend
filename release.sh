#!/bin/sh
# Builds the install tarballs, in the same shape as the originals: one
# top-level folder per app, source only (the binary is built on the
# device with ./build.sh or go build — see each README).
#
# Usage:  ./release.sh <leaf-version> <rom-hacks-version> [out-dir]
#         ./release.sh 97 35            -> dist/leaf-mlp1-poc-v97.tar.gz
#                                          dist/rom-hacks-v35.tar.gz
#
# Packs what is committed (git archive), so stray build outputs and logs
# never end up in a release, and file modes (the .sh launchers, xdelta3)
# are kept.
set -e
[ $# -ge 2 ] || { echo "usage: $0 <leaf-version> <rom-hacks-version> [out-dir]"; exit 1; }
cd "$(dirname "$0")"
OUT="${3:-dist}"
mkdir -p "$OUT"
git archive --format=tar.gz --prefix=leaf-mlp1-poc/ -o "$OUT/leaf-mlp1-poc-v$1.tar.gz" HEAD:leaf-mlp1-poc
git archive --format=tar.gz --prefix=rom-hacks/ -o "$OUT/rom-hacks-v$2.tar.gz" HEAD:rom-hacks
ls -l "$OUT"/*.tar.gz
