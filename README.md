# agorai

A tiny local dashboard for juggling several **Claude Code** sessions at once.

- **Left panel** — every session with its state and a one-line recap. The dot +
  name are colour-coded so you can triage at a glance, and **blink** until you
  look:
  - 🔴 **red** — a permission prompt is waiting for your answer
  - 🟢 **green** — it finished its turn / wants input and you haven't checked it yet
  - 🟡 **amber** (steady) — busy working, no action needed
  - ⚪ **grey** — idle and seen, or ended
- **Right panel** — the focused session's live terminal (xterm.js). Clicking a
  session focuses the terminal so you can start typing immediately.
- **+ New Session** — pick a repo (discovered under your roots), create a **new
  directory** for it, or **start without a repo** (runs in a dedicated
  `~/.agorai/scratch` workspace).
- **New PR** — give it a Linear ticket (a bare number is prefixed with `BLUE-`);
  Claude checks out the ticket's PR under `~/dev/PRs/<ticket>` and produces an
  implementation plan.
- **Review PR** — review a PR by Linear ticket or by GitHub PR reference. Runs
  read-only (no checkout/commits/comments) in a contained workspace.
- **Resume** — re-open a past session (discovered from `~/.claude/projects`
  transcripts) as a hosted, interactive terminal via `claude --resume`. The
  picker lists each session's first prompt, last reply, and age.
- **Model picker** — choose the cloud **model** (Opus / Sonnet / Haiku, or your
  default) and optionally pin a **version**; the model actually running (resolved
  from the transcript, even behind "default") shows as a chip beside the session
  and is remembered across restarts.
- **Yes / No / Always** buttons answer a permission prompt without switching to
  that session; an **ⓘ** icon carries the full command/context as a tooltip.
- **🔊** (top-right) toggles alert sounds — a tone when a session needs
  permission, another when one finishes while you're looking elsewhere.
- **🐞** (terminal header) shows what the permission-prompt parser sees, for
  debugging a mis-parsed prompt (with a Copy-JSON button).
- **Settings** (⚙ top-right) — set the terminal **scrollback** lines and define
  **environment variables** passed to `claude` at launch (e.g. a `DATABASE_URL`).
  Saved to `~/.agorai/config.json` (mode `0600`, since env may hold secrets).

It's a single Go binary: one process hosts each `claude` in a PTY, bridges it to
the browser over a WebSocket, and listens for Claude Code hook events to know
when a session needs you. The web assets are embedded, so it cross-compiles to a
single file you can hand to a colleague.

## Run

```
make tidy      # first time: resolve deps (needs network; writes go.sum)
make run       # starts on http://127.0.0.1:7777
```

Then open <http://127.0.0.1:7777>. By default the New-Session picker lists a few
specific repos (see `defaultRoots` in `main.go`). Point it at different roots —
a root may be a repo itself or a tree to scan (up to 3 levels deep) for repos:

```
go run . -roots ~/dev,~/work,~/src
```

## Wire up the hooks (required for "needs input" + live recaps)

agorai learns that a session is waiting (and updates recaps) via Claude Code
hooks. Persistence does **not** need them, but the blink/needs-input state,
permission prompts, and live recap updates do.

**Easiest — one command:**

```
agorai install
```

This writes `~/.claude/hooks/agorai.sh` and merges the hook entries into
`~/.claude/settings.json` (it backs up the original to `settings.json.agorai.bak`,
merges rather than overwrites, and is safe to re-run). Restart any running
sessions afterward so they pick up the hook.

**Or do it manually:**

1. Copy the script somewhere on disk and make it executable:

   ```
   cp hooks/agorai.sh ~/.claude/hooks/agorai.sh && chmod +x ~/.claude/hooks/agorai.sh
   ```

2. Add to `~/.claude/settings.json`:

   ```json
   {
     "hooks": {
       "SessionStart":     [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/agorai.sh" }] }],
       "UserPromptSubmit": [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/agorai.sh" }] }],
       "Notification": [{ "matcher": "idle_prompt|permission_prompt|elicitation_dialog", "hooks": [{ "type": "command", "command": "~/.claude/hooks/agorai.sh" }] }],
       "Stop":         [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/agorai.sh" }] }],
       "SessionEnd":   [{ "hooks": [{ "type": "command", "command": "~/.claude/hooks/agorai.sh" }] }]
     }
   }
   ```

## Persistence (sessions survive a restart)

When agorai exits, its child `claude` processes exit too — so it can't keep the
live processes running. Instead it **remembers** each hosted session and
**auto-resumes** them on startup:

- agorai assigns each session's id itself (`claude --session-id <uuid>`) and
  records it in `~/.agorai/sessions.json` **at spawn time** — so persistence does
  **not** depend on the hooks being installed.
- On startup, agorai re-runs `claude --resume <id>` for each record (in its
  worktree, if it had one) and re-seeds its recap from the transcript. Records
  whose dir/transcript are gone are pruned.
- Click the **✕** on a session to close it and drop it from persistence, or end
  it inside claude (the `SessionEnd` hook prunes it). Either way it won't come
  back next launch.
- **Rename** a session by double-clicking its name in the list; the new name is
  persisted too.

Because restored sessions reload history, the live process is new but the
conversation is intact. Don't run the same session in two places at once.

The script forwards each hook's JSON plus the `AGORAI_ID` env var (which agorai
set when it spawned that `claude`) to the server, which maps it back to the
right session row.

## Hand it to a Mac colleague

```
make mac           # builds bin/agorai-darwin-arm64 and -amd64
```

Send them the matching binary. First launch on macOS: if it was *downloaded*,
clear the Gatekeeper quarantine once with `xattr -d com.apple.quarantine ./agorai-darwin-arm64`
(not needed if copied via scp/git). They still need `claude` installed and the
hooks configured as above.

## Notes / rough edges

- **Permission prompts** are parsed from the terminal (`prompt.go`,
  `parsePermissionPrompt`): the real numbered options become the panel buttons,
  and answering sends `<n>\r` to claude's numbered select. Parsing is heuristic
  and best-effort — if the prompt UI changes, use the **🐞** debug view to see
  what it parsed and adjust.
- **Recaps** show the last assistant line of the chat, read from the transcript
  (`transcript_path`) on each hook event (only the tail is read, so it stays
  cheap on long sessions). They fall back to a status label when the transcript
  has nothing yet (e.g. a brand-new session shows "Starting…" until its first reply).
- **Memory** — `scrollback` defaults to 10k lines/terminal (~10–15 MB), tunable
  in Settings (clamped 100–200k). Env-var changes apply to newly spawned/restarted
  sessions; scrollback changes apply to newly opened terminals. One
  xterm instance is kept per session to preserve scrollback when switching; if
  you run many sessions, drop background terminals and replay from the
  server-side ring buffer instead.
- **Security** — binds to `127.0.0.1` only and validates that a client-supplied
  `cwd` is under an allowed root (Review / New-PR / scratch sessions instead run
  in fixed server-chosen workspaces under `~/.agorai` or `~/dev/PRs`). Don't
  expose the port without adding auth.
- **Offline / vendoring** — `index.html` loads xterm.js from a CDN. For a truly
  offline single-binary build, download `xterm.min.js`, `xterm.min.css`,
  `addon-fit.min.js`, and `addon-search.min.js` into `web/` and point the tags at
  them; they'll be embedded.
- **Find in terminal** — `Ctrl/Cmd+F` opens a find bar for the focused session
  (Enter / Shift+Enter step through matches). Uses xterm's search addon; the
  browser's native find can't see the terminal buffer.
```
