#!/bin/sh
# Installs (or updates) RA Hack and the itch.io app on the device, from the
# release tarballs made by release.sh.
#
# Run it ON THE DEVICE, as the ark user:
#
#   scp rom-hacks-v35.tar.gz leaf-mlp1-poc-v97.tar.gz install-on-device.sh ark@<device>:~/
#   ssh ark@<device>
#   ./install-on-device.sh 35 97        # rom-hacks v35, leaf-mlp1-poc v97
#   ./install-on-device.sh 35 -         # only rom-hacks
#   ./install-on-device.sh - 97         # only the itch.io app
#
# The tarballs are looked for next to this script, then in $HOME. The apps
# are installed in $HOME/rom-hacks and $HOME/leaf-mlp1-poc.
#
# Each app is unpacked and built in a scratch folder first. The installed
# copy is replaced only once the new one has built, so a failed build
# (no network for `go mod tidy`, a compile error) leaves the previous
# version working. EmulationStation is stopped while building, for the
# memory, and is started again on the way out whatever happened.
#
# Settings, the RetroAchievements key and caches live in ~/.local/share,
# outside the app folders, so replacing a folder loses none of them.
set -eu

usage() {
    echo "usage: $0 <rom-hacks-version|-> <leaf-mlp1-poc-version|->"
    echo "  e.g. $0 35 97"
    exit 2
}
[ $# -eq 2 ] || usage
HACKS="$1"
LEAF="$2"
[ "$HACKS" = "-" ] && [ "$LEAF" = "-" ] && usage

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
DEST="${INSTALL_DIR:-$HOME}"

# find_tarball <name> prints the tarball's path, or fails.
find_tarball() {
    for dir in "$SCRIPT_DIR" "$HOME"; do
        if [ -f "$dir/$1" ]; then
            echo "$dir/$1"
            return 0
        fi
    done
    echo "error: $1 not found in $SCRIPT_DIR or $HOME" >&2
    return 1
}

# Check everything before stopping anything.
command -v go >/dev/null 2>&1 || {
    echo "error: go is not installed — run: sudo apt install golang libsdl2-dev libsdl2-ttf-dev" >&2
    exit 1
}
HACKS_TGZ=""
LEAF_TGZ=""
if [ "$HACKS" != "-" ]; then
    HACKS_TGZ="$(find_tarball "rom-hacks-v$HACKS.tar.gz")"
fi
if [ "$LEAF" != "-" ]; then
    LEAF_TGZ="$(find_tarball "leaf-mlp1-poc-v$LEAF.tar.gz")"
fi

echo "installing:"
if [ -n "$HACKS_TGZ" ]; then echo "  RA Hack (rom-hacks)  $HACKS_TGZ"; fi
if [ -n "$LEAF_TGZ" ]; then echo "  itch.io (leaf)       $LEAF_TGZ"; fi
echo "into $DEST"
echo

WORK="$(mktemp -d "$DEST/.install-tmp.XXXXXX")"
ES_STOPPED=0
# STEP names what was being done, so a failure says what state it left.
STEP=""
finish() {
    status=$?
    rm -rf "$WORK"
    if [ "$ES_STOPPED" -eq 1 ]; then
        echo "starting EmulationStation again"
        sudo systemctl start emulationstation || true
    fi
    if [ "$status" -ne 0 ]; then
        echo
        case "$STEP" in
        build:*)
            echo "FAILED building ${STEP#build:} — its previously installed version was left in place." >&2 ;;
        register:*)
            echo "FAILED registering ${STEP#register:} in EmulationStation. The new version is installed;" >&2
            echo "run ./install-tool.sh inside it once the problem above is fixed." >&2 ;;
        *)
            echo "FAILED." >&2 ;;
        esac
    fi
    exit "$status"
}
trap finish EXIT
trap 'exit 130' INT TERM

if [ "${SKIP_ES:-0}" != "1" ]; then
    echo "stopping EmulationStation"
    sudo systemctl stop emulationstation
    ES_STOPPED=1
fi

# install_app <folder> <tarball> <main package> <binary name>
install_app() {
    name="$1"
    tgz="$2"
    pkg="$3"
    bin="$4"

    echo "== $name"
    STEP="build:$name"
    tar xzf "$tgz" -C "$WORK"
    src="$WORK/$name"
    [ -d "$src" ] || { echo "error: $tgz has no $name/ folder" >&2; return 1; }

    # The replace block in go.mod only exists for a build machine that
    # cannot reach golang.org; the device can, so it goes.
    sed -i '/^\/\/ The replace lines below/,/^)/d' "$src/go.mod"
    (cd "$src" && go mod tidy && go build -o "bin/$bin" "$pkg")
    chmod +x "$src"/*.sh
    if [ -f "$src/bin/xdelta3" ]; then chmod +x "$src/bin/xdelta3"; fi

    # Built: now, and only now, replace the installed copy.
    STEP="register:$name"
    rm -rf "${DEST:?}/$name"
    mv "$src" "$DEST/$name"
    (cd "$DEST/$name" && ./install-tool.sh)
    STEP=""
    echo
}

if [ -n "$HACKS_TGZ" ]; then
    install_app rom-hacks "$HACKS_TGZ" ./cmd/hacks rom-hacks-arm64
fi
if [ -n "$LEAF_TGZ" ]; then
    install_app leaf-mlp1-poc "$LEAF_TGZ" ./cmd/poc leaf-mlp1-poc-arm64
fi

echo "done."
