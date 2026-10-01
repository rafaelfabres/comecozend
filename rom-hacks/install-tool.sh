#!/bin/sh
# Registers this app under Tools in EmulationStation, the same place
# PortMaster lives, so it can be started from the menu instead of over SSH.
#
# EmulationStation launches a tool, waits for it to exit, then takes the
# screen back on its own. Nothing here stops or starts ES — that is what
# makes "close the app, land back in the menu" work with no extra wiring.
#
# By default it registers in BOTH /roms/tools and /roms/ports, because
# Ports is usually two button presses closer than Tools on these menus and
# there is no cost to having the same launcher in both.
#
# Usage:  ./install-tool.sh
#         TARGET_DIR=/roms/ports ./install-tool.sh   (just that one)
set -e

APP_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="$APP_DIR/bin/rom-hacks-arm64"
TARGETS="${TARGET_DIR:-/roms/tools /roms/ports}"

if [ ! -x "$BINARY" ]; then
    echo "error: $BINARY not found or not executable."
    echo "build it first:  ./build.sh"
    exit 1
fi

# Launchers from earlier versions, under the old name.
for old in "/roms/tools/ROM Hacks.sh" "/roms/ports/ROM Hacks.sh"; do
    if [ -f "$old" ]; then
        rm -f "$old"
        echo "removed the old launcher at $old"
    fi
done

INSTALLED=0
for dir in $TARGETS; do
    if [ ! -d "$dir" ]; then
        continue
    fi
    LAUNCHER="$dir/RA Hack.sh"
    cat > "$LAUNCHER" <<INNER
#!/bin/sh
# RA Hack - RetroAchievements ROM hacks, launched by EmulationStation.
APP_DIR="$APP_DIR"
exec "\$APP_DIR/launch.sh"
INNER
    chmod +x "$LAUNCHER"
    echo "installed: $LAUNCHER"
    INSTALLED=$((INSTALLED + 1))
done

if [ "$INSTALLED" -eq 0 ]; then
    echo "error: none of these folders exist: $TARGETS"
    echo "check where this firmware keeps them:"
    echo "    grep -n 'roms/tools\\|roms/ports' /etc/emulationstation/es_systems.cfg"
    echo "then re-run with, for example:  TARGET_DIR=/roms/ports ./install-tool.sh"
    exit 1
fi
echo
echo "before first use, save your RetroAchievements API key:"
echo "    $BINARY --ra-login <username> <web-api-key>"
echo "(the key is at https://retroachievements.org/controlpanel.php)"
