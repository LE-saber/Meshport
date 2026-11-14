#!/bin/sh
# Installs Meshport for the current user (no root needed).
#   ./install.sh            binary + desktop entry + icon
#   ./install.sh --service  also enable the headless systemd user service
#   ./install.sh --uninstall
set -eu
here=$(cd "$(dirname "$0")" && pwd)
bin="$HOME/.local/bin"
apps="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
icons="${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor"
units="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"

if [ "${1:-}" = "--uninstall" ]; then
    systemctl --user disable --now meshport.service 2>/dev/null || true
    rm -f "$bin/meshport" "$apps/meshport.desktop" "$icons/256x256/apps/meshport.png" "$icons/scalable/apps/meshport.svg" "$units/meshport.service"
    rm -f "${XDG_CONFIG_HOME:-$HOME/.config}/autostart/meshport.desktop"
    echo "Meshport removed. Your data stays in ${XDG_CONFIG_HOME:-$HOME/.config}/meshport"
    exit 0
fi

mkdir -p "$bin" "$apps" "$icons/256x256/apps" "$icons/scalable/apps"
install -m 0755 "$here/meshport" "$bin/meshport"
sed "s|^Exec=meshport|Exec=$bin/meshport|" "$here/meshport.desktop" > "$apps/meshport.desktop"
install -m 0644 "$here/meshport.png" "$icons/256x256/apps/meshport.png"
install -m 0644 "$here/meshport.svg" "$icons/scalable/apps/meshport.svg"
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$apps" || true

if [ "${1:-}" = "--service" ]; then
    mkdir -p "$units"
    install -m 0644 "$here/meshport.service" "$units/meshport.service"
    systemctl --user daemon-reload
    systemctl --user enable --now meshport.service
    command -v loginctl >/dev/null 2>&1 && loginctl enable-linger "$(id -un)" 2>/dev/null || true
    echo "Headless service started. Register this device with:"
    echo "  $bin/meshport setup --server https://mesh.example.com --enroll mpe_..."
else
    echo "Installed. Start Meshport from the application menu or run: $bin/meshport"
fi
case ":$PATH:" in *":$bin:"*) ;; *) echo "Note: add $bin to your PATH" ;; esac
