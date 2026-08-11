#!/bin/bash
# Install AgorAI (Claude Code dashboard) as a desktop launcher. Builds the app,
# wires the Claude Code hooks, and drops the launcher into the app grid.
# Re-runnable (rebuilds and re-renders the launcher entries).
#
# On a fresh laptop / VM, clone first and run this from inside the checkout:
#   git clone https://github.com/andreaiacono/agorai.git && agorai/scripts/install.sh
set -e

# Everything is derived from where this script lives, so the checkout can be
# moved or re-cloned anywhere without editing a path here — and the launcher
# can't end up pointing at an older clone that happens to still be on disk.
REPO="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"
LAUNCH="$REPO/scripts/agorai-launch.sh"
ICON="$REPO/web/favicon.svg"
APPS="$HOME/.local/share/applications"

command -v go >/dev/null || { echo "Go is required — install it from https://go.dev/dl, then re-run."; exit 1; }

# 1. build
( cd "$REPO" && go build -o bin/agorai . )

# 2. Claude Code hooks (needs-input blink + live recap); harmless if it fails
"$REPO/bin/agorai" install || true

# 3. desktop launcher (+ a searchable "Restart AgorAI" entry). The .in templates
# carry @LAUNCH@/@ICON@ placeholders because .desktop files take no variables and
# no relative paths — rendering them here is what keeps the absolute paths honest.
mkdir -p "$APPS"
chmod +x "$LAUNCH"
for d in agorai agorai-restart; do
    sed -e "s|@LAUNCH@|$LAUNCH|g" -e "s|@ICON@|$ICON|g" \
        "$REPO/scripts/$d.desktop.in" > "$APPS/$d.desktop"
    chmod 0755 "$APPS/$d.desktop"
done
command -v update-desktop-database >/dev/null && update-desktop-database "$APPS"

# 4. GNOME Shell extension: top-bar indicator, notifications, restart menu.
# Symlinked (not copied) so a later `git pull` updates it in place. -f so a link
# left over from a previous checkout is repointed rather than silently kept.
UUID="agorai@andreaiacono.github.io"
EXTSRC="$REPO/gnome-extension/$UUID"
EXTDIR="$HOME/.local/share/gnome-shell/extensions/$UUID"
if [ -d "$EXTSRC" ]; then
    mkdir -p "$(dirname "$EXTDIR")"
    ln -sfn "$EXTSRC" "$EXTDIR"
    # settings schema must be compiled or the extension can't read its prefs
    command -v glib-compile-schemas >/dev/null && glib-compile-schemas "$EXTSRC/schemas"
    command -v gnome-extensions >/dev/null && gnome-extensions enable "$UUID" 2>/dev/null || true
    echo "  (extension installed — log out/in if it doesn't appear in the top bar)"
fi

echo "✓ AgorAI installed — search 'AgorAI' in the launcher (or run $LAUNCH)"
