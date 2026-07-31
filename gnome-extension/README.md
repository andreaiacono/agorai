# agorai GNOME Shell extension

A top-bar indicator and desktop notifications for [agorai](../README.md). It
turns the dashboard's at-a-glance triage into something you see without keeping
the browser tab in front of you.

![what it does](../docs/gnome-extension.png)

## What it does

- **Panel badge** — the agorai icon in the top bar with a count of sessions that
  want your attention, tinted by the worst state: red (a session needs
  permission or is waiting for input), green (a turn finished and you haven't
  looked yet), amber (something is mid-turn).
- **Click-to-jump menu** — the dropdown lists every session with a colored state
  dot, its name, and a one-line recap. Click one and it **raises the dashboard
  window** (if one is open) and switches it to that session; otherwise it opens a
  window on that session. **Open agorai** does the same for the dashboard, and
  **Restart agorai** stops the server and starts a freshly built one.
- **Native notifications** — when a session asks for permission / waits for
  input (critical, sticky) or finishes a turn (normal), you get a GNOME
  notification with an **Open** action that jumps to it. This replaces the
  browser's beep when the tab isn't focused. Toggle them off in the extension's
  **Preferences** (the badge and menu keep working regardless).

It reads the same `/api/sessions` feed the web UI uses, polling every 2 seconds.
If the server isn't running the indicator shows "agorai not running".

Opening a session raises the existing window rather than spawning a browser tab.
That relies on the dashboard window's class being `AgorAI` — launch it as an app
window with that class (e.g. `chromium --app=http://127.0.0.1:7777
--class=AgorAI`). If it's opened some other way, the extension falls back to the
`#session=<id>` deep link in your default browser.

## Requirements

- GNOME Shell 46–50 (developed against 50). The bundled UUID targets these.
- agorai running locally on `http://127.0.0.1:7777` (the default). If you run it
  on another port, edit `BASE` at the top of `extension.js`.

## Install

```bash
./install.sh
```

That symlinks the extension into `~/.local/share/gnome-shell/extensions/`,
compiles its settings schema (needed for the notification toggle), and enables
it. Then reload the Shell so it's picked up:

- **Wayland** (GNOME 50 default): log out and back in.
- **Xorg**: `Alt+F2`, type `r`, Enter.

Enable (if `install.sh` couldn't yet, because the Shell hadn't loaded it):

```bash
gnome-extensions enable agorai@andreaiacono.github.io
```

Open the notification toggle any time with:

```bash
gnome-extensions prefs agorai@andreaiacono.github.io
```

## Develop / debug

Edits to the files here take effect on the next Shell reload (the install is a
symlink). Watch its logs:

```bash
journalctl -f -o cat /usr/bin/gnome-shell | grep -i agorai
```

To uninstall:

```bash
gnome-extensions disable agorai@andreaiacono.github.io
rm ~/.local/share/gnome-shell/extensions/agorai@andreaiacono.github.io
```
