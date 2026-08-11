#!/usr/bin/env bash
# AgorAI launcher — ensure the server is running, then open the dashboard.
# Wired to agorai.desktop, but also runnable directly. Safe to run repeatedly:
# it never starts a second server on the port.
#
#   AGORAI_APP=0         open a normal tab in the default browser instead of an app window
#   AGORAI_BROWSER=...   force a specific browser for the app window
#   AGORAI_WINDOW=W,H    initial window size (default below); the browser then
#                        remembers whatever you resize it to
set -uo pipefail

# The checkout is wherever this script lives, so moving or re-cloning the repo
# needs no edit here — and a stale hardcoded path can't silently point the
# launcher at an old clone. readlink -f resolves a symlinked invocation.
REPO="$(cd "$(dirname "$(readlink -f "${BASH_SOURCE[0]}")")/.." && pwd)"
BIN="$REPO/bin/agorai"
HOST=127.0.0.1
PORT=7777
URL="http://$HOST:$PORT"
LOG="$HOME/.agorai/agorai.log"
# Initial window geometry (logical px). Kept short enough to fit the 1920x1200
# laptop panel with the top bar and decorations, not just the 3440x1440 display.
WINDOW="${AGORAI_WINDOW:-1200,800}"

# GUI launches inherit a minimal PATH; add the usual Go + browser locations.
export PATH="$PATH:/usr/lib/go-1.26/bin:/usr/local/go/bin:$HOME/go/bin:/snap/bin"

mkdir -p "$HOME/.agorai"

# A running server already owns the port — probe it without any dependencies.
port_up() { (exec 3<>"/dev/tcp/$HOST/$PORT") 2>/dev/null; }

# `--restart` stops the running server first so the block below rebuilds and
# starts a fresh one (sessions come back — agorai re-resumes them on startup).
if [ "${1:-}" = "--restart" ]; then
    pkill -f "$BIN" 2>/dev/null || true
    for _ in $(seq 1 25); do port_up || break; sleep 0.2; done
fi

# An argument may name the page to open (e.g. a #session= deep link from the
# GNOME extension), so it lands in the app window like every other launch. Only
# our own dashboard URLs are accepted — the .desktop takes a %u, so anything on
# the system could otherwise ask us to open an arbitrary page.
TARGET="$URL"
case "${1:-}" in
    "$URL"*) TARGET="$1" ;;
esac

if ! port_up; then
    # Only rebuild when we're about to start one, so the launched server is current
    # (a server that's already up wouldn't pick up a rebuild anyway). Best-effort.
    if command -v go >/dev/null 2>&1 && [ -d "$REPO" ]; then
        (cd "$REPO" && go build -o "$BIN" .) >>"$LOG" 2>&1 || true
    fi
    if [ ! -x "$BIN" ]; then
        command -v notify-send >/dev/null && notify-send "AgorAI" "Not installed — run: $REPO/scripts/install.sh"
        exit 1
    fi
    echo "--- $(date) starting agorai ---" >>"$LOG"
    setsid "$BIN" >>"$LOG" 2>&1 </dev/null &   # detached: outlives this launcher
    for _ in $(seq 1 60); do port_up && break; sleep 0.2; done
fi

# Open the dashboard as a chromeless app window (its own dock entry), unless
# AGORAI_APP=0, in which case fall through to the default browser.
if [ "${AGORAI_APP:-1}" = "1" ]; then
    # --class pins the window's WM_CLASS / Wayland app_id, so it matches
    # StartupWMClass in agorai.desktop and the window picks up the AgorAI icon
    # instead of a generic browser one.
    for b in "${AGORAI_BROWSER:-}" brave-browser google-chrome google-chrome-stable chromium chromium-browser microsoft-edge; do
        [ -n "$b" ] && command -v "$b" >/dev/null 2>&1 || continue
        setsid "$b" --app="$TARGET" --class=AgorAI --window-size="$WINDOW" >/dev/null 2>&1 </dev/null &
        exit 0
    done
fi
setsid xdg-open "$TARGET" >/dev/null 2>&1 </dev/null &
