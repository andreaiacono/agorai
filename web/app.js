/* agorai frontend — talks to the Go server over REST + two WebSockets. */

const FitAddon = window.FitAddon.FitAddon;
const SearchAddon = window.SearchAddon && window.SearchAddon.SearchAddon; // optional (CDN)
const enc = new TextEncoder();

const state = {
  sessions: [],            // latest snapshot from the control WS
  selected: null,          // selected session id
  terms: new Map(),        // id -> { term, fit, ws, pane }
  config: { scrollback: 10000, env: {} },
  unread: new Set(),       // sessions that completed (working→idle) but aren't viewed yet
  prevStates: {},          // id -> last seen state, for transition detection
};

/* ---------- control WebSocket: state in, commands out ---------- */

let controlWs;
function connectControl() {
  controlWs = new WebSocket(`ws://${location.host}/ws/control`);
  controlWs.onmessage = (e) => {
    const msg = JSON.parse(e.data);
    if (msg.type === "sessions") {
      const next = msg.sessions || [];
      for (const s of next) {
        const prev = state.prevStates[s.id];
        // Mark "unread" (→ blink) when a session newly enters a state that wants
        // your attention: needs input, needs permission, or just finished a turn.
        // Only on the transition, and not for the session you're already viewing.
        const attention =
          s.state === "waiting" || s.state === "perm" || (prev === "working" && s.state === "idle");
        if (s.state !== prev && attention && s.id !== state.selected) {
          state.unread.add(s.id);
          // Audible alert for a session you're not watching (skip the first
          // snapshot, where prev is undefined, to avoid a burst on load).
          if (prev !== undefined) {
            if (s.state === "perm") playSound("perm");
            else if (s.state === "idle" && prev === "working") playSound("done");
          }
        }
        if (s.state === "working") state.unread.delete(s.id); // active again, nothing pending
        state.prevStates[s.id] = s.state;
      }
      state.sessions = next;
      renderSessions();
    }
  };
  controlWs.onclose = () => setTimeout(connectControl, 1000); // reconnect
}

function answer(id, option) {
  controlWs.send(JSON.stringify({ type: "answer", session: id, option }));
}

/* ---------- session list (left panel) ---------- */

function renderSessions() {
  const list = document.getElementById("session-list");
  list.innerHTML = "";
  for (const s of state.sessions) {
    list.appendChild(sessionCard(s));
  }
  disposeStaleTerminals();
  // "needs attention" = a pending permission, or anything that wants you and
  // hasn't been looked at yet (unread).
  const needs = state.sessions.filter((s) => s.state === "perm" || state.unread.has(s.id)).length;
  document.getElementById("meta").textContent =
    `${state.sessions.length} session${state.sessions.length === 1 ? "" : "s"}` +
    (needs ? ` · ${needs} need attention` : "");
}

function sessionCard(s) {
  const el = document.createElement("div");
  el.className = "session " + s.state
    + (s.id === state.selected ? " selected" : "")
    + (state.unread.has(s.id) ? " unread" : "");
  el.onclick = () => selectSession(s.id);

  // Only permission gets a badge (it pairs with the answer buttons). The amber
  // dot/name already signals "wants you" for waiting/finished sessions.
  const badge = s.state === "perm" ? `<span class="badge perm">permission</span>` : "";

  // While working, show an animated "Working" with oscillating dots (1→2→3→2…).
  const recap = s.state === "working" ? `Working<span class="dots"></span>` : esc(s.recap);

  el.innerHTML = `
    <div class="row1">
      <span class="dot ${s.state}"></span>
      <span class="name" title="${esc(s.name)}">${esc(s.name)} <span class="branch">· ${esc(s.branch)}</span></span>
      ${badge}
      <span class="x" title="close session">✕</span>
    </div>
    <div class="recap">${recap}</div>`;

  el.querySelector(".x").onclick = (ev) => { ev.stopPropagation(); closeSession(s.id); };
  el.querySelector(".name").ondblclick = (ev) => { ev.stopPropagation(); renameSession(s.id, s.name); };

  if (s.state === "perm") {
    const p = document.createElement("div");
    p.className = "prompt";
    const opts = s.prompt && s.prompt.options;

    if (opts && opts.length) {
      // We parsed real numbered options → render them as buttons.
      const ctxLines = (s.prompt.context || "").split("\n").filter((l) => l && l !== s.prompt.question);
      const full = [...ctxLines, s.prompt.question].filter(Boolean).join("\n");
      const info = full ? `<span class="q-info" title="${esc(full)}">ⓘ</span>` : "";
      const q = s.prompt.question ? `<div class="q" title="${esc(full)}"><span class="q-text">${esc(s.prompt.question)}</span>${info}</div>` : "";
      p.innerHTML = q + `<div class="opts">` + opts.map((o) =>
        `<button class="opt" data-num="${o.num}"><span class="k">${o.num}</span>${esc(o.label)}</button>`
      ).join("") + `</div>`;
      for (const b of p.querySelectorAll("button")) {
        b.onmousedown = (ev) => ev.preventDefault(); // don't steal focus from the terminal
        b.onclick = (ev) => { ev.stopPropagation(); answer(s.id, parseInt(b.dataset.num, 10)); };
      }
    } else {
      // Couldn't parse options (non-standard / multi-question / free-text prompt).
      // Don't guess with fake buttons — tell the user to answer in the terminal.
      p.innerHTML = `<div class="q">Needs your input — click to open and respond in the terminal.</div>`;
    }
    el.appendChild(p);
  }
  return el;
}

/* ---------- terminal (right panel) ---------- */

function selectSession(id) {
  state.selected = id;
  state.unread.delete(id); // viewing it = read; stop blinking
  document.getElementById("empty").style.display = "none";

  // hide other panes, ensure this one exists and is shown
  for (const [tid, t] of state.terms) {
    t.pane.classList.toggle("active", tid === id);
  }
  if (!state.terms.has(id)) {
    mountTerminal(id);
  } else {
    // The window may have been resized while this session was hidden: refit
    // the xterm AND tell the PTY, or claude keeps rendering at the old width.
    const t = state.terms.get(id);
    t.fit.fit();
    sendResize(t.ws, t.term);
  }

  const s = state.sessions.find((x) => x.id === id);
  if (s) {
    document.getElementById("t-name").textContent = s.name;
    document.getElementById("t-cwd").textContent = s.cwd + " · " + s.branch;
    document.getElementById("t-right").textContent = "Model: " + s.model;
  }
  renderSessions();

  // Move the cursor into the terminal so the user can type straight away.
  // Defer so it runs after renderSessions() rebuilt the list (which can steal
  // focus) and after a just-mounted xterm has finished opening.
  const t = state.terms.get(id);
  if (t) requestAnimationFrame(() => t.term.focus());
}

function mountTerminal(id) {
  const host = document.getElementById("term-host");
  const pane = document.createElement("div");
  pane.className = "term-pane active";
  host.appendChild(pane);

  const term = new Terminal({
    scrollback: state.config.scrollback || 10000,  // configurable — see Settings
    fontSize: 13,
    fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, monospace',
    theme: {
      background: "#0a0d12",
      foreground: "#cdd6e0",
      // bright current-match highlight (find selects the active match)
      selectionBackground: "#e3b341",
      selectionForeground: "#0a0d12",
    },
  });
  const fit = new FitAddon();
  term.loadAddon(fit);

  let search = null;
  if (SearchAddon) {
    search = new SearchAddon();
    term.loadAddon(search);
  }

  // Intercept a few key combos before xterm sends them to the PTY:
  //  - Ctrl/Cmd+F      → our find bar (handled at window level), don't reach PTY
  //  - Ctrl/Cmd+C      → copy the selection (swapped: no longer sends ^C)
  //  - Ctrl/Cmd+Shift+C → send the interrupt signal (^C / 0x03) to claude
  term.attachCustomKeyEventHandler((e) => {
    if (e.type !== "keydown") return true;
    const mod = e.ctrlKey || e.metaKey;
    if (!mod) return true;
    if (search && (e.key === "f" || e.key === "F")) return false;
    if (e.key === "c" || e.key === "C") {
      if (e.shiftKey) {
        if (ws.readyState === 1) ws.send(enc.encode("\x03")); // interrupt
      } else {
        const sel = term.getSelection();
        if (sel) navigator.clipboard.writeText(sel).catch(() => {});
      }
      return false; // we handled it; xterm must not also send anything
    }
    return true;
  });

  term.open(pane);
  fit.fit();

  const ws = new WebSocket(`ws://${location.host}/ws/pty/${id}`);
  ws.binaryType = "arraybuffer";
  ws.onmessage = (e) => term.write(new Uint8Array(e.data));
  ws.onopen = () => sendResize(ws, term);

  // keystrokes -> binary frame (text frames are reserved for resize).
  // Strip focus in/out reports (CSI I / CSI O): clicking a button/another row
  // blurs this terminal, and a stray ESC from the focus report can be read by
  // claude as an interrupt ("Interrupted"). They're cosmetic, so drop them.
  term.onData((d) => {
    d = d.replace(/\x1b\[[IO]/g, "");
    if (d && ws.readyState === 1) ws.send(enc.encode(d));
  });

  state.terms.set(id, { term, fit, ws, pane, search });
}

/* ---------- find in terminal ---------- */

// All-match highlight (when the renderer supports decorations); the active match
// is also shown via the bright selection colour set on the terminal theme.
const FIND_DECORATIONS = {
  matchBackground: "#2f6db0",          // all matches: vivid blue
  activeMatchBackground: "#e3b341",    // current match: amber
  matchOverviewRuler: "#4aa3ff",
  activeMatchColorOverviewRuler: "#e3b341",
};

function activeSearch() {
  const t = state.terms.get(state.selected);
  return t ? t.search : null;
}

function openFind() {
  if (!state.selected) return;
  document.getElementById("findbar").classList.add("open");
  const inp = document.getElementById("find-input");
  inp.focus();
  inp.select();
  if (inp.value) doFind("incremental");
}

function closeFind() {
  document.getElementById("findbar").classList.remove("open");
  const s = activeSearch();
  try { if (s) s.clearDecorations(); } catch {}
  const t = state.terms.get(state.selected);
  if (t) t.term.focus();
}

function doFind(dir) {
  const s = activeSearch();
  const count = document.getElementById("find-count");
  const q = document.getElementById("find-input").value;
  if (!s) { count.textContent = "n/a"; return; }
  if (!q) { try { s.clearDecorations(); } catch {} count.textContent = ""; return; }

  const go = (opts) => (dir === "prev"
    ? s.findPrevious(q, opts)
    : s.findNext(q, { ...opts, incremental: dir === "incremental" }));

  try {
    go({ caseSensitive: false, decorations: FIND_DECORATIONS });
  } catch {
    try { go({ caseSensitive: false }); } catch (e) { console.error("find failed", e); }
  }
  // The addon's result count is unreliable, so count matches ourselves (after the
  // selection has settled). Debounced for incremental typing on big buffers.
  clearTimeout(doFind._t);
  doFind._t = setTimeout(updateFindCount, dir === "incremental" ? 120 : 0);
}

// Count total matches in the buffer and which one is currently selected.
function updateFindCount() {
  const t = state.terms.get(state.selected);
  const el = document.getElementById("find-count");
  const q = document.getElementById("find-input").value;
  if (!t || !q) { el.textContent = ""; return; }
  const buf = t.term.buffer.active;
  const needle = q.toLowerCase();
  const sel = t.term.getSelectionPosition(); // buffer coords, or undefined
  let total = 0, cur = 0;
  for (let y = 0; y < buf.length; y++) {
    const line = buf.getLine(y);
    if (!line) continue;
    const text = line.translateToString(true).toLowerCase();
    let x = text.indexOf(needle);
    while (x !== -1) {
      total++;
      if (sel && (y < sel.start.y || (y === sel.start.y && x <= sel.start.x))) cur++;
      x = text.indexOf(needle, x + needle.length);
    }
  }
  el.textContent = total ? `${Math.max(cur, 1)}/${total}` : "0/0";
}

function sendResize(ws, term) {
  if (ws.readyState === 1) ws.send(JSON.stringify({ resize: [term.cols, term.rows] }));
}

// Debounced: resizing fires continuously, and each PTY resize makes claude's
// TUI repaint — a storm of SIGWINCHes litters the scrollback with partial
// frames. Reflow the display live, but tell the PTY only once it settles.
let winResizeTimer = null;
window.addEventListener("resize", () => {
  const t = state.terms.get(state.selected);
  if (!t) return;
  t.fit.fit();
  clearTimeout(winResizeTimer);
  winResizeTimer = setTimeout(() => sendResize(t.ws, t.term), 150);
});

/* ---------- draggable divider between the two panels ---------- */

(function initResizer() {
  const gutter = document.getElementById("gutter");
  const sidebar = document.querySelector(".sidebar");
  const KEY = "agorai.sidebarWidth";

  const saved = parseInt(localStorage.getItem(KEY) || "", 10);
  if (saved) sidebar.style.width = saved + "px";

  let dragging = false;
  gutter.addEventListener("mousedown", (e) => {
    dragging = true;
    document.body.classList.add("resizing");
    e.preventDefault();
  });
  document.addEventListener("mousemove", (e) => {
    if (!dragging) return;
    const w = Math.min(Math.max(e.clientX, 220), window.innerWidth - 320);
    sidebar.style.width = w + "px";
    const t = state.terms.get(state.selected);
    if (t) t.fit.fit(); // reflow the display live; tell the PTY on mouseup
  });
  document.addEventListener("mouseup", () => {
    if (!dragging) return;
    dragging = false;
    document.body.classList.remove("resizing");
    localStorage.setItem(KEY, parseInt(sidebar.style.width, 10));
    const t = state.terms.get(state.selected);
    if (t) { t.fit.fit(); sendResize(t.ws, t.term); }
  });
})();

async function renameSession(id, current) {
  const name = prompt("Rename session", current);
  if (name === null) return;
  const trimmed = name.trim();
  if (trimmed === "" || trimmed === current) return;
  await fetch(`/api/sessions/${id}`, {
    method: "PATCH",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name: trimmed }),
  }).catch(() => {});
  // control WS broadcast refreshes the list with the new name
}

async function closeSession(id) {
  if (!confirm("Close this session? It won't be resumed on restart.")) return;
  await fetch(`/api/sessions/${id}`, { method: "DELETE" }).catch(() => {});
  // the control WS broadcast will drop it from the list; disposeStaleTerminals cleans up
}

// Tear down xterm instances + sockets for sessions no longer in the list, to
// free memory (and WebGL/DOM resources).
function disposeStaleTerminals() {
  const live = new Set(state.sessions.map((s) => s.id));
  for (const [id, t] of state.terms) {
    if (live.has(id)) continue;
    try { t.ws.close(); } catch {}
    try { t.term.dispose(); } catch {}
    t.pane.remove();
    state.terms.delete(id);
    if (state.selected === id) {
      state.selected = null;
      document.getElementById("empty").style.display = "";
      document.getElementById("t-name").textContent = "AgorAI";
      document.getElementById("t-cwd").textContent = "";
    }
  }
}

/* ---------- new-session modal ---------- */

let repos = [];          // from /api/repos        (open + worktree modes)
let resumables = [];     // from /api/resumable    (resume mode)
let roots = [];          // from /api/roots        (newdir parent options)
let mode = "open";
const overlay = document.getElementById("overlay");

async function openModal(initialMode = "open") {
  overlay.classList.add("open");
  document.getElementById("search").value = "";
  resumables = []; // refetched when the Resume tab is opened
  await populateModels();
  repos = await fetch("/api/repos").then((r) => r.json()).catch(() => []);
  roots = await fetch("/api/roots").then((r) => r.json()).catch(() => []);
  document.getElementById("newdir-parent").innerHTML =
    roots.map((r) => `<option value="${esc(r.path)}">${esc(r.display)}</option>`).join("");
  await setMode(initialMode); // selects the tab + renders the matching list
  document.getElementById("search").focus();
}
function closeModal() { overlay.classList.remove("open"); }

const MODAL_TITLES = {
  open: "New <span>session</span>",
  worktree: "New session in <span>new branch</span>",
  resume: "Resume <span>session</span>",
  review: "Review <span>PR</span>",
  ticket: "New session for <span>ticket</span>",
};

async function setMode(m) {
  mode = m;
  document.getElementById("modal-title").innerHTML = MODAL_TITLES[m] || MODAL_TITLES.open;
  const modal = document.getElementById("modal");
  modal.classList.toggle("wt-mode", m === "worktree");
  modal.classList.toggle("resume-mode", m === "resume");
  modal.classList.toggle("review-mode", m === "review");
  modal.classList.toggle("ticket-mode", m === "ticket");
  // reset the new-directory sub-state whenever the mode changes
  modal.classList.remove("newdir-mode");
  document.getElementById("newdir-chk").checked = false;
  document.getElementById("newdir-name").value = "";
  document.getElementById("search").placeholder = m === "resume" ? "Filter past sessions…" : "Filter repos…";

  if (m === "review") {
    document.querySelector('input[name="review-source"][value="ticket"]').checked = true;
    setReviewSource("ticket");
  }
  if (m === "ticket") {
    document.getElementById("ticket-input").value = "";
  }
  if (m === "resume" && !resumables.length) {
    resumables = await fetch("/api/resumable").then((r) => r.json()).catch(() => []);
  }
  renderList();
}

function reviewSource() {
  const el = document.querySelector('input[name="review-source"]:checked');
  return el ? el.value : "ticket";
}

function setReviewSource(src) {
  const label = document.getElementById("review-input-label");
  const hint = document.getElementById("review-input-hint");
  const input = document.getElementById("review-ticket");
  if (src === "pr") {
    label.textContent = "GitHub PR";
    input.placeholder = "PR URL or owner/repo#123";
    hint.textContent = "The PR is reviewed directly via `gh` — no Linear ticket needed.";
    input.value = "";
  } else {
    label.textContent = "Linear ticket";
    input.placeholder = "e.g. BLUE-900";
    hint.textContent = "The repository is taken from the PR in the ticket — no directory needed.";
    // Pre-fill the usual project prefix so you only type the number.
    input.value = "BLUE-";
  }
  input.focus();
  input.setSelectionRange(input.value.length, input.value.length); // cursor at end
}

function launchTicket() {
  let ticket = document.getElementById("ticket-input").value.trim();
  if (!ticket) { alert("Enter a Linear ticket number."); return; }
  if (/^\d+$/.test(ticket)) ticket = "BLUE-" + ticket; // bare number → default project
  createSession({ mode: "ticket", ticket, model: selectedModel() });
}

function launchReview() {
  const source = reviewSource();
  const value = document.getElementById("review-ticket").value.trim();
  if (!value) {
    alert(source === "pr" ? "Enter a GitHub PR (URL or owner/repo#123)." : "Enter a Linear ticket number.");
    return;
  }
  if (source === "pr") {
    createSession({ mode: "review", pr: value, model: selectedModel() });
  } else {
    createSession({ mode: "review", ticket: value, model: selectedModel() });
  }
}

function launchScratch() {
  createSession({ mode: "scratch", model: selectedModel() });
}

function toggleNewDir() {
  const on = document.getElementById("newdir-chk").checked;
  document.getElementById("modal").classList.toggle("newdir-mode", on);
  if (on) document.getElementById("newdir-name").focus();
}

function launchNewDir() {
  const parent = document.getElementById("newdir-parent").value;
  const dir = document.getElementById("newdir-name").value.trim();
  const gitInit = document.getElementById("newdir-git").checked;
  if (!parent) { alert("Choose a parent folder."); return; }
  if (!dir) { alert("Enter a folder name."); return; }
  createSession({ mode: "newdir", parent, dir, gitInit, model: selectedModel() });
}

function renderList() {
  const q = document.getElementById("search").value.toLowerCase();
  const list = document.getElementById("repo-list");
  list.innerHTML = "";

  if (mode === "resume") {
    const match = resumables.filter((r) => (r.title + r.display + r.recap).toLowerCase().includes(q));
    list.innerHTML = `<div class="group-label">${q ? "Matches" : "Recent sessions"}</div>`;
    if (!match.length) { list.innerHTML += `<div class="group-label">nothing on disk</div>`; return; }
    for (const r of match) {
      const el = document.createElement("div");
      el.className = "repo";
      el.onclick = () => launchResume(r);
      el.innerHTML = `
        <span class="ico">⟲</span>
        <span class="info">
          <div class="r-name">${esc(r.title)}</div>
          <div class="r-sub">${esc(r.display)} · ${esc(r.recap)}</div>
        </span>
        <span class="r-age">${esc(r.age)}</span>`;
      list.appendChild(el);
    }
    return;
  }

  const match = repos.filter((r) => (r.name + r.display).toLowerCase().includes(q));
  list.innerHTML = `<div class="group-label">${q ? "Matches" : "Repos"}</div>`;
  if (!match.length) { list.innerHTML += `<div class="group-label">no match</div>`; return; }
  for (const r of match) {
    const el = document.createElement("div");
    el.className = "repo";
    el.onclick = () => launchRepo(r);
    el.innerHTML = `
      <span class="ico">▸</span>
      <span class="info">
        <div class="r-name">${esc(r.name)}</div>
        <div class="r-sub">${esc(r.display)} · <span class="r-branch">${esc(r.branch)}</span> · ${esc(r.sub)}</div>
      </span>
      <span class="go">↵</span>`;
    list.appendChild(el);
  }
}

let modelsLoaded = false;
let modelList = [];
async function populateModels() {
  if (modelsLoaded) return;
  modelList = await fetch("/api/models").then((r) => r.json()).catch(() => []);
  document.getElementById("model-sel").innerHTML =
    modelList.map((m) => `<option value="${esc(m.id)}">${esc(m.label)}</option>`).join("");
  modelsLoaded = true;
  onModelChange();
}
function onModelChange() {
  const family = document.getElementById("model-sel").value;
  const ver = document.getElementById("model-ver");
  const m = modelList.find((x) => x.id === family);
  const versions = (m && m.versions) || [];
  ver.innerHTML = `<option value="">latest</option>` +
    versions.map((v) => `<option value="${esc(v.id)}">${esc(v.label)}</option>`).join("");
  ver.disabled = !versions.length;
}
function selectedModel() {
  // A pinned version (full model id) wins over the family alias.
  return document.getElementById("model-ver").value || document.getElementById("model-sel").value;
}

function launchRepo(r) {
  createSession({ cwd: r.path, mode, name: r.name, model: selectedModel() });
}
function launchResume(r) {
  const fork = document.getElementById("fork-chk").checked;
  createSession({ mode: "resume", sessionId: r.sessionId, fork, model: selectedModel() });
}

async function createSession(body) {
  closeModal();
  const res = await fetch("/api/sessions", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  if (!res.ok) { alert("Could not start session: " + (await res.text())); return; }
  const { id } = await res.json();
  // the control WS will deliver the new session shortly; select it once it lands
  const trySelect = () => {
    if (state.sessions.find((s) => s.id === id)) selectSession(id);
    else setTimeout(trySelect, 100);
  };
  trySelect();
}

document.addEventListener("keydown", (e) => { if (e.key === "Escape") { closeModal(); closeSettings(); closeDebug(); } });

/* ---------- settings ---------- */

/* ---------- prompt debug ---------- */

let debugDump = null;

async function openDebug() {
  const body = document.getElementById("debug-body");
  body.innerHTML = `<div class="debug-empty">Loading…</div>`;
  document.getElementById("debug-overlay").classList.add("open");

  let dump = [];
  try { dump = await fetch("/api/debug/prompts").then((r) => r.json()); } catch { /* leave empty */ }
  debugDump = dump;
  if (!dump || !dump.length) {
    body.innerHTML = `<div class="debug-empty">No session is currently asking permission.</div>`;
    return;
  }
  body.innerHTML = dump.map((d) => `
    <div class="debug-sess">
      <div class="debug-name">${esc(d.name)} <span class="debug-id">${esc(d.id)}</span></div>
      <div class="debug-kv"><b>question</b> ${esc(d.question || "—")}</div>
      <div class="debug-kv"><b>context</b> <pre class="debug-ctx">${esc(d.context || "—")}</pre></div>
      <div class="debug-kv"><b>options</b> ${(d.options || []).map((o) => `<span class="debug-opt">${o.num}. ${esc(o.label)}</span>`).join(" ") || "—"}</div>
      <details><summary>stripped output (${(d.stripped || "").length} chars)</summary><pre class="debug-raw">${esc(d.stripped)}</pre></details>
    </div>`).join("");
}

function closeDebug() { document.getElementById("debug-overlay").classList.remove("open"); }

function copyDebug() {
  if (debugDump) navigator.clipboard.writeText(JSON.stringify(debugDump, null, 2));
}

const settingsOverlay = document.getElementById("settings-overlay");

async function loadConfig() {
  const c = await fetch("/api/config").then((r) => r.json()).catch(() => null);
  if (c) state.config = { scrollback: c.scrollback || 10000, env: c.env || {} };
}

async function openSettings() {
  await loadConfig();
  document.getElementById("set-scrollback").value = state.config.scrollback;
  const rows = document.getElementById("env-rows");
  rows.innerHTML = "";
  const entries = Object.entries(state.config.env);
  if (!entries.length) addEnvRow();
  else for (const [k, v] of entries) addEnvRow(k, v);
  settingsOverlay.classList.add("open");
}
function closeSettings() { settingsOverlay.classList.remove("open"); }

function addEnvRow(k = "", v = "") {
  const row = document.createElement("div");
  row.className = "env-row";
  row.innerHTML = `<input class="env-k" placeholder="KEY" spellcheck="false">
    <input class="env-v" placeholder="value" spellcheck="false">
    <button class="env-x" type="button" title="remove">✕</button>`;
  row.querySelector(".env-k").value = k;
  row.querySelector(".env-v").value = v;
  row.querySelector(".env-x").onclick = () => row.remove();
  document.getElementById("env-rows").appendChild(row);
}

async function saveSettings() {
  const scrollback = parseInt(document.getElementById("set-scrollback").value, 10) || 10000;
  const env = {};
  for (const row of document.querySelectorAll(".env-row")) {
    const k = row.querySelector(".env-k").value.trim();
    const v = row.querySelector(".env-v").value;
    if (k) env[k] = v;
  }
  const body = { scrollback, env };
  const saved = await fetch("/api/config", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  }).then((r) => r.json()).catch(() => null);
  if (saved) state.config = { scrollback: saved.scrollback, env: saved.env || {} };
  closeSettings();
}

/* ---------- sound alerts ---------- */

let soundOn = localStorage.getItem("agorai.sound") !== "off";
let audioCtx;

// Browsers require a user gesture before audio can play; unlock on first input.
function unlockAudio() {
  try {
    audioCtx = audioCtx || new (window.AudioContext || window.webkitAudioContext)();
    if (audioCtx.state === "suspended") audioCtx.resume();
  } catch {}
}
window.addEventListener("click", unlockAudio, { once: true });
window.addEventListener("keydown", unlockAudio, { once: true });

// Play a short sequence of tones via the Web Audio API (no audio files).
function beep(freqs, dur, gainVal) {
  if (!audioCtx) return;
  let t = audioCtx.currentTime;
  for (const f of freqs) {
    const osc = audioCtx.createOscillator();
    const g = audioCtx.createGain();
    osc.type = "sine";
    osc.frequency.value = f;
    g.gain.setValueAtTime(gainVal, t);
    g.gain.exponentialRampToValueAtTime(0.0001, t + dur);
    osc.connect(g);
    g.connect(audioCtx.destination);
    osc.start(t);
    osc.stop(t + dur);
    t += dur;
  }
}

function playSound(type) {
  if (!soundOn || !audioCtx) return;
  if (audioCtx.state === "suspended") audioCtx.resume();
  if (type === "perm") beep([880, 1175], 0.12, 0.06);  // urgent two-note rise
  else beep([784], 0.2, 0.05);                          // soft single "done" tone
}

function toggleSound() {
  soundOn = !soundOn;
  localStorage.setItem("agorai.sound", soundOn ? "on" : "off");
  document.getElementById("sound-btn").textContent = soundOn ? "🔊" : "🔇";
  if (soundOn) { unlockAudio(); playSound("done"); } // confirm with a sample
}

/* ---------- util ---------- */

function esc(s) {
  return String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

// Ctrl/Cmd+F opens the terminal find bar (capture phase, before xterm/browser).
window.addEventListener("keydown", (e) => {
  if ((e.ctrlKey || e.metaKey) && (e.key === "f" || e.key === "F")) {
    e.preventDefault();
    openFind();
  }
}, true);

document.getElementById("find-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter") { e.preventDefault(); doFind(e.shiftKey ? "prev" : "next"); }
  else if (e.key === "Escape") { e.preventDefault(); closeFind(); }
});

document.getElementById("sound-btn").textContent = soundOn ? "🔊" : "🔇";
loadConfig();
connectControl();
