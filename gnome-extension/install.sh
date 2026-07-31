#!/usr/bin/env bash
# Install the agorai GNOME Shell extension by symlinking it into the user
# extensions dir (so edits here take effect on the next Shell reload) and
# enabling it.
set -euo pipefail

UUID="agorai@andreaiacono.github.io"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/$UUID" && pwd)"
DEST="${XDG_DATA_HOME:-$HOME/.local/share}/gnome-shell/extensions/$UUID"

mkdir -p "$(dirname "$DEST")"
rm -rf "$DEST"
ln -s "$SRC" "$DEST"
echo "Linked $DEST -> $SRC"

# The settings schema must be compiled or getSettings() fails and the extension
# won't load (it reads the notification toggle at enable()).
if command -v glib-compile-schemas >/dev/null 2>&1; then
    glib-compile-schemas "$SRC/schemas" && echo "Compiled settings schema"
fi

if command -v gnome-extensions >/dev/null 2>&1; then
    gnome-extensions enable "$UUID" 2>/dev/null && echo "Enabled $UUID" || \
        echo "Could not enable yet — reload the Shell first, then: gnome-extensions enable $UUID"
fi

cat <<'EOF'

Almost done. GNOME must reload to pick up a newly installed extension:
  - Wayland (GNOME 50 default): log out and back in.
  - Xorg: Alt+F2, type 'r', Enter.

Then enable it (if not already):
  gnome-extensions enable agorai@andreaiacono.github.io

Tail its logs while testing:
  journalctl -f -o cat /usr/bin/gnome-shell | grep -i agorai
EOF
