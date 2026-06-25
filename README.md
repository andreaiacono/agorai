# agorai

A tiny local dashboard for juggling several **Claude Code**, **OpenAI Codex**,
and **Gemini CLI** sessions at once.

- **Left panel** — every session with its state and a one-line recap. A
  **per-agent icon** marks each row and the **name** is colour-coded so you can
  triage at a glance, **blinking** until you look:
  - 🔴 **red** — a permission prompt is waiting for your answer
  - 🟢 **green** — it finished its turn / wants input and you haven't checked it yet
  - 🟡 **amber** (steady) — busy working, no action needed
  - ⚪ **grey** — idle and seen (an **ended** session's name is dimmed)
- **Right panel** — the focused session's live terminal (xterm.js). Clicking a
  session focuses the terminal so you can start typing immediately.
- **+ New Session** — choose the **agent** as a radio (Claude / Codex / Gemini —
  nothing is pre-selected, so you pick deliberately), then a **folder chooser**
  opens at your home directory: navigate to any checkout on disk and start there.
  A **Show hidden folders** toggle reveals dot-directories.
- **New PR** — give it a Linear ticket (a bare number is prefixed with `BLUE-`).
  There's no PR yet, so the agent sets up a **fresh checkout + branch** under
  `~/dev/PRs/<ticket>` and produces an implementation plan for the new work.
- **Review PR** — review an existing GitHub PR by number (a bare number assumes
  `light-space/light`) or URL. It reads the linked Linear ticket for context and
  runs **read-only** (no checkout/commits/comments) in a contained workspace.
- **Review my code** — review your **local** working branch before a PR exists:
  the diff of the current branch against its base (committed *and* uncommitted
  changes), with the Linear ticket recovered from the branch name / commit-message
  prefixes for context. Pick the checkout with the folder chooser; read-only.
- **Resume** — re-open a past session (discovered from `~/.claude/projects`
  transcripts) as a hosted, interactive terminal via `claude --resume`. The
  picker lists each session's first prompt, last reply, and age.
- **Model picker** — choose the cloud **model** (Opus / Sonnet / Haiku / Fable,
  or your default) and optionally pin a **version**; the model actually running
  (resolved from the transcript, even behind "default") shows as a chip beside the
  session and is remembered across restarts.
- **Yes / No / Always** buttons answer a permission prompt without switching to
  that session; an **ⓘ** icon carries the full command/context as a tooltip.
- **🔊** (top-right) toggles alert sounds — a tone when a session needs
  permission, another when one finishes while you're looking elsewhere.
- **🐞** (terminal header) shows what the permission-prompt parser sees, for
  debugging a mis-parsed prompt (with a Copy-JSON button).
- **Settings** (⚙ top-right) — set the terminal **scrollback** lines and define
  **environment variables** passed to `claude` at launch (e.g. a `DATABASE_URL`).
  Saved to `~/.agorai/config.json` (mode `0600`, since env may hold secrets).

It's a single Go binary: one process hosts each agent (`claude`, `codex`, or
`gemini`) in a PTY, bridges it to the browser over a WebSocket, and learns when a
session needs you.
The web assets are embedded, so it cross-compiles to a single file you can hand
to a colleague.

## Codex sessions

Pick **Codex** in the agent radio to host an OpenAI `codex` TUI instead of
`claude`. A few differences, handled automatically:

- **No hooks.** Codex has no hook system, so agorai instead tails Codex's
  session **rollout** file (`~/.codex/sessions/.../rollout-*.jsonl`) to know the
  state: a started turn → *working*, a finished turn → *idle*, and a command
  awaiting your approval (a `require_escalated` call) → the red *question* state
  (answer it in the terminal — there are no panel buttons for Codex).
- **Session ids** are minted by Codex, not agorai, so the id is learned from the
  rollout right after spawn; persistence + auto-resume (`codex resume <id>`) work
  the same as for Claude.
- **Folder trust.** agorai appends a trusted `[projects."<dir>"]` entry to
  `~/.codex/config.toml` for the session's directory (idempotent, append-only),
  so Codex doesn't prompt.
- Codex runs with `--no-alt-screen` so its scrollback survives agorai's replay.

## Gemini sessions

Pick **Gemini** to host the `gemini` CLI. It uses the same Claude-compatible hook
*format* but Gemini's own event *names* — `BeforeAgent`/`AfterAgent` for the turn
boundaries (≈ Claude's `UserPromptSubmit`/`Stop`), plus `SessionStart` /
`Notification` / `SessionEnd`. So:

- `agorai install` also wires `~/.gemini/settings.json` (only if `~/.gemini`
  exists), and a spawned Gemini session **self-heals** its hook entries on launch.
- Recaps are read from Gemini's chat JSONL (`~/.gemini/tmp/.../chats/*.jsonl`).
- Session ids are assigned by agorai up front (`--session-id`), like Claude, so
  persistence + auto-resume work the same way.

## Custom buttons (`~/.agorai/buttons.json`)

The top-bar launch buttons are **config-driven**. Built-in defaults ship the
**New Session / New PR / Review PR / Review my code / Resume** buttons; drop a
`~/.agorai/buttons.json` to replace them (and add your own). It's served at
`GET /api/buttons` and the UI renders the top bar + each modal from it. The **▦**
button (top-right) opens a **visual manager** to create/edit/delete buttons
without hand-editing JSON; **Reset to defaults** drops the file.

A button:

```jsonc
{
  "id": "tests", "label": "Write tests", "icon": "plus",
  "agents": ["claude", "codex", "gemini"],   // agent radio options (omit = all)
  "showModel": true,
  "workspace": { "pick": true },             // show the folder chooser …
  // … or  { "dir": "~/dev/PRs", "trust": true }   (fixed dir)
  // … or  { "scratch": "review" }                  (~/.agorai/<name>)
  "inputs": [                                // text fields (prompt buttons)
    { "id": "area", "label": "Area", "placeholder": "e.g. billing", "required": true,
      "transform": "blue-prefix" }           // blue-prefix: prepend BLUE- to a bare number
  ],
  "prompt": "Write tests for {area} in {workspace}…",   // {input} / {workspace} / {dir}
  "sessionName": "Tests {area}",
  "unattended": true,                        // run without approval prompts (reviews)
  "excludeEnv": ["DATABASE_URL"]             // drop env vars (reviews)
}
```

- **`variants`** — a radio that swaps inputs + prompt + name (for a button that
  offers a couple of modes); same fields as a button.
- **`workspace.pick`** shows the **folder chooser**; a pick button may still carry
  a `prompt`/`sessionName` (filled with `{workspace}` = the chosen path, `{dir}` =
  its folder name).
- **Editable prompt** — any button with a `prompt` shows it in a textarea in the
  modal, with a hint listing each placeholder's source, so you can tweak it before
  launching (placeholders are still filled in).
- Adding/changing a button is just a JSON edit (or the ▦ manager) — no rebuild.
  **Resume** stays a built-in special (it lists past sessions rather than taking
  inputs).

## Run

```
make tidy      # first time: resolve deps (needs network; writes go.sum)
make run       # starts on http://127.0.0.1:7777
```

Then open <http://127.0.0.1:7777>. The New-Session picker is a **folder chooser**
that opens at your home directory, so you can start a session in any checkout. The
`-roots` flag still scopes repo auto-discovery and the cwd guard used by the other
flows (a root may be a repo itself or a tree scanned up to 3 levels deep):

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
merges rather than overwrites, and is safe to re-run). It also wires
`~/.gemini/settings.json` with Gemini's event names when `~/.gemini` exists;
**Codex needs no hooks**. Restart any running sessions afterward so they pick up
the hook.

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
- **Security** — binds to `127.0.0.1` only. The New-Session **folder chooser**
  lets you open any existing directory you navigate to; the other flows (Review /
  New-PR) run in fixed server-chosen workspaces under `~/.agorai` or `~/dev/PRs`.
  It's a single-user localhost tool — don't expose the port without adding auth.
- **Offline / vendoring** — `index.html` loads xterm.js from a CDN. For a truly
  offline single-binary build, download `xterm.min.js`, `xterm.min.css`,
  `addon-fit.min.js`, and `addon-search.min.js` into `web/` and point the tags at
  them; they'll be embedded.
- **Find in terminal** — `Ctrl/Cmd+F` opens a find bar for the focused session
  (Enter / Shift+Enter step through matches). Uses xterm's search addon; the
  browser's native find can't see the terminal buffer.
```
