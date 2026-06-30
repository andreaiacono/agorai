# agorai GNOME Shell extension

A top-bar indicator and desktop notifications for [agorai](../README.md). It
turns the dashboard's at-a-glance triage into something you see without keeping
the browser tab in front of you.

![what it does](../docs/gnome-extension.png)

## What it does

- **Panel badge** — a terminal icon in the top bar with a count of sessions that
  want your attention, tinted by the worst state: red (a session needs
  permission or is waiting for input), green (a turn finished and you haven't
  looked yet), amber (something is mid-turn).
- **Click-to-jump menu** — the dropdown lists every session with a colored state
  dot, its name, and a one-line recap. Click one to open agorai focused on that
  session (via the `#session=<id>` deep link), or "Open agorai" for the
  dashboard.
- **Native notifications** — when a session asks for permission / waits for
  input (critical, sticky) or finishes a turn (normal), you get a GNOME
  notification with an **Open** action that jumps to it. This replaces the
  browser's beep when the tab isn't focused.

It reads the same `/api/sessions` feed the web UI uses, polling every 2 seconds.
No agorai server changes are required beyond the deep-link support already in
the UI; if the server isn't running the indicator shows "agorai not running".

## Requirements

- GNOME Shell 46–50 (developed against 50). The bundled UUID targets these.
- agorai running locally on `http://127.0.0.1:7777` (the default). If you run it
  on another port, edit `BASE` at the top of `extension.js`.

## Install

```bash
./install.sh
```

That symlinks the extension into `~/.local/share/gnome-shell/extensions/` and
enables it. Then reload the Shell so it's picked up:

- **Wayland** (GNOME 50 default): log out and back in.
- **Xorg**: `Alt+F2`, type `r`, Enter.

Enable (if `install.sh` couldn't yet, because the Shell hadn't loaded it):

```bash
gnome-extensions enable agorai@andreaiacono.github.io
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
