#!/bin/sh
# Registers this app under Tools in EmulationStation — the same place
# PortMaster lives — so it can be started from the menu instead of over SSH.
#
# How the hand-off works: EmulationStation launches a port, waits for it to
# exit, then takes the screen back on its own. So nothing here stops or starts
# ES — that is exactly what makes "close the app, land back in the menu" work
# without any extra wiring.
#
# Usage:  ./install-port.sh            (installs to /roms/tools)
#         TARGET_DIR=/roms/ports ./install-tool.sh   (just that one)
set -e

APP_DIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="$APP_DIR/bin/leaf-mlp1-poc-arm64"
# Tools and Ports both by default: Ports is usually fewer button presses away than Tools,
# and the same launcher in both costs nothing.
TARGETS="${TARGET_DIR:-/roms/tools /roms/ports}"

if [ ! -x "$BINARY" ]; then
    echo "error: $BINARY not found or not executable."
    echo "build it first:  go build -o bin/leaf-mlp1-poc-arm64 ./cmd/poc"
    exit 1
fi

INSTALLED=0
for dir in $TARGETS; do
    [ -d "$dir" ] || continue
    LAUNCHER="$dir/Itch.io.sh"
    cat > "$LAUNCHER" <<EOF
#!/bin/sh
# itch.io homebrew browser — launched by EmulationStation from Tools.
# Logs go next to the app so a failed launch can be diagnosed after the fact.
APP_DIR="$APP_DIR"
LOG_DIR="\$HOME/.local/share/leaf-itchio"
mkdir -p "\$LOG_DIR"
cd "\$APP_DIR" || exit 1
exec ./bin/leaf-mlp1-poc-arm64 >>"\$LOG_DIR/run.log" 2>&1
EOF
    chmod +x "$LAUNCHER"
    echo "installed: $LAUNCHER"
    INSTALLED=$((INSTALLED + 1))
done

if [ "$INSTALLED" -eq 0 ]; then
    echo "error: none of these folders exist: $TARGETS"
    echo "check where this firmware keeps them:"
    echo "    grep -n 'roms/tools\\|roms/ports' /etc/emulationstation/es_systems.cfg"
    exit 1
fi
echo
echo "It will appear under Tools and Ports in EmulationStation."
echo "If it does not show up,"
echo "restart EmulationStation so it rescans:"
echo "    sudo systemctl restart emulationstation"
echo
echo "Logs:  ~/.local/share/leaf-itchio/run.log"
