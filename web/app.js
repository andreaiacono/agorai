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
        // Mark "unread" (→ blink) only on an actual transition we observe while
        // connected — into a state that wants your attention (input/permission)
        // or a just-finished turn. `prev === undefined` is the first snapshot
        // after a (re)connect/refresh: prime state without re-alerting you to
        // states that already existed.
        const attention =
          s.state === "waiting" || s.state === "perm" || (prev === "working" && s.state === "idle");
        if (prev !== undefined && s.state !== prev && attention && s.id !== state.selected) {
          state.unread.add(s.id);
          if (s.state === "perm") playSound("perm");
          else if (s.state === "idle" && prev === "working") playSound("done");
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

// Small per-agent marks shown left of the session name (claude = coral burst,
// codex = OpenAI-green ring) so you can tell at a glance which CLI a row is.
const CLAUDE_ICON =
  `<svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="#d97757" stroke-width="1.6" stroke-linecap="round">` +
  `<line x1="8" y1="2" x2="8" y2="14"/><line x1="2" y1="8" x2="14" y2="8"/>` +
  `<line x1="3.8" y1="3.8" x2="12.2" y2="12.2"/><line x1="12.2" y1="3.8" x2="3.8" y2="12.2"/></svg>`;
const CODEX_ICON =
  `<svg width="12" height="12" viewBox="0 0 16 16" fill="none" stroke="#10a37f" stroke-width="1.6">` +
  `<circle cx="8" cy="8" r="5.4"/><circle cx="8" cy="8" r="1.6" fill="#10a37f" stroke="none"/></svg>`;
// Gemini = blue four-point spark
const GEMINI_ICON =
  `<svg width="12" height="12" viewBox="0 0 16 16" fill="#4286f5" stroke="none">` +
  `<path d="M8 1 C8.6 4.4 11.6 7.4 15 8 C11.6 8.6 8.6 11.6 8 15 C7.4 11.6 4.4 8.6 1 8 C4.4 7.4 7.4 4.4 8 1 Z"/></svg>`;

const AGENT_ICONS = { claude: CLAUDE_ICON, codex: CODEX_ICON, gemini: GEMINI_ICON };
const AGENT_NAMES = { claude: "Claude", codex: "Codex", gemini: "Gemini" };

function agentIcon(agent) {
  const a = AGENT_ICONS[agent] ? agent : "claude";
  return `<span class="agent-ic" title="${AGENT_NAMES[a]}">${AGENT_ICONS[a]}</span>`;
}

function sessionCard(s) {
  const el = document.createElement("div");
  el.className = "session " + s.state
    + (s.id === state.selected ? " selected" : "")
    + (state.unread.has(s.id) ? " unread" : "");
  el.onclick = () => selectSession(s.id);

  // A "question" badge (it pairs with the answer buttons). The prompt may be a
  // permission request or a "how should I do this?" question — both surface here.
  const badge = s.state === "perm" ? `<span class="badge perm">question</span>` : "";

  // While working, show an animated "Working" with oscillating dots (1→2→3→2…).
  const recap = s.state === "working" ? `Working<span class="dots"></span>` : esc(s.recap);

  el.innerHTML = `
    <div class="row1">
      ${agentIcon(s.agent)}
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

let repos = [];          // from /api/repos        (worktree mode)
let resumables = [];     // from /api/resumable    (resume mode)
let mode = "open";
const overlay = document.getElementById("overlay");

let pickBtn = null; // the pick-button currently driving the picker (for its prompt/name/agents)

async function openModal(initialMode = "open", btn = null) {
  pickBtn = btn;
  overlay.classList.add("open");
  document.getElementById("search").value = "";
  // reset anything a config button may have changed
  document.getElementById("modal").classList.remove("config-mode");
  document.querySelector(".model-row").style.display = "";
  populateAgentOptions(btn ? btn.agents : null);
  resumables = []; // refetched when the Resume tab is opened
  repos = await fetch("/api/repos").then((r) => r.json()).catch(() => []); // for worktree mode
  await setMode(initialMode); // selects the tab + renders the matching list
  if (btn && btn.label) document.getElementById("modal-title").textContent = btn.label; // after setMode (which sets a default)
  document.getElementById(initialMode === "open" ? "browse-path" : "search").focus();
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
  modal.classList.remove("config-mode");
  modal.classList.toggle("wt-mode", m === "worktree");
  modal.classList.toggle("resume-mode", m === "resume");
  modal.classList.toggle("open-mode", m === "open"); // open mode = the folder chooser
  // The agent choice applies everywhere except the unused worktree mode. Refresh
  // the model list so it matches the agent that will actually run.
  if (m === "worktree") setAgent("claude");
  await populateModels();
  document.getElementById("search").placeholder = m === "resume" ? "Filter past sessions…" : "Filter repos…";

  if (m === "resume") {
    resumables = await fetchResumables(); // for the currently selected agent
    renderList();
  } else if (m === "open") {
    // Open mode is a folder chooser starting at the home directory — no list of
    // pre-discovered repos; navigate to whatever checkout you want to open.
    document.getElementById("browse-hidden").checked = false;
    await browseGo("");
  } else {
    renderList();
  }
}

// Resumable sessions are per-agent (claude transcripts vs codex rollouts).
function fetchResumables() {
  return fetch("/api/resumable?agent=" + encodeURIComponent(selectedAgent()))
    .then((r) => r.json()).catch(() => []);
}

/* ---------- config-driven buttons (New PR / Review / custom) ---------- */

// Entry point from the top bar: route to the open picker, resume, or a
// config-driven form depending on the button's mode.
function openButton(b) {
  if (b.mode === "resume") return openModal("resume", b);
  if (b.workspace && b.workspace.pick) return openModal("open", b); // the repo/dir picker
  return openConfig(b);
}

let configBtn = null;     // the button currently shown in the config form
let variantIdx = 0;       // selected variant index (for buttons with variants)

async function openConfig(b) {
  configBtn = b;
  variantIdx = 0;
  mode = "config";
  overlay.classList.add("open");
  const modal = document.getElementById("modal");
  ["wt-mode", "resume-mode", "review-mode", "ticket-mode", "open-mode"].forEach((c) => modal.classList.remove(c));
  modal.classList.add("config-mode");
  document.getElementById("modal-title").textContent = b.label;
  populateAgentOptions(b.agents);
  document.querySelector(".model-row").style.display = b.showModel === false ? "none" : "";
  await populateModels();
  renderConfigForm();
}

// Render the button's allowed agents (all if unspecified) as radio buttons.
function populateAgentOptions(agents) {
  const list = (agents && agents.length) ? agents : Object.keys(AGENT_NAMES);
  const prev = selectedAgent(); // keep the prior pick if it's still allowed
  document.getElementById("agent-opts").innerHTML = list.map((a) =>
    `<label class="agent-opt"><input type="radio" name="agent" value="${esc(a)}" onchange="onAgentChange()"> ${esc(AGENT_NAMES[a] || a)}</label>`
  ).join("");
  // No default selection — the user picks deliberately. Only exception: when a
  // single agent is allowed, select it (and hide the row) so launch isn't blocked.
  if (list.length === 1) setAgent(list[0]);
  else if (list.includes(prev)) setAgent(prev);
  document.querySelector(".agent-row").style.display = list.length > 1 ? "" : "none";
}

function setAgent(a) {
  const el = document.querySelector(`input[name="agent"][value="${a}"]`);
  if (el) el.checked = true;
}

function activeVariant() {
  return configBtn && configBtn.variants && configBtn.variants.length ? configBtn.variants[variantIdx] : null;
}

function onVariantChange(i) {
  variantIdx = i;
  renderConfigForm();
}

function renderConfigForm() {
  const b = configBtn, form = document.getElementById("config-form");
  const variant = activeVariant();
  const inputs = (variant ? variant.inputs : b.inputs) || [];
  let html = "";
  if (b.variants && b.variants.length) {
    html += `<div class="review-source">` + b.variants.map((v, i) =>
      `<label><input type="radio" name="cfg-variant" value="${esc(v.id)}" ${i === variantIdx ? "checked" : ""} onchange="onVariantChange(${i})"> ${esc(v.label)}</label>`
    ).join("") + `</div>`;
  }
  for (const inp of inputs) {
    html += `<label class="field"><span class="field-label">${esc(inp.label || inp.id)}</span>
      <input class="cfg-input" data-id="${esc(inp.id)}" placeholder="${esc(inp.placeholder || "")}" autocomplete="off"></label>`;
  }
  const prompt = (variant ? variant.prompt : b.prompt) || "";
  if (prompt) {
    // Explain each placeholder that appears in the prompt, and where its value comes from.
    const ws = b.workspace || {};
    const wsDesc = ws.dir || (ws.scratch ? "~/.agorai/" + ws.scratch : (ws.pick ? "the directory you pick" : "the working directory"));
    const ph = [];
    for (const i of inputs) if (prompt.includes("{" + i.id + "}")) ph.push(`{${i.id}} → the ${i.label || i.id} field above`);
    if (prompt.includes("{workspace}")) ph.push(`{workspace} → ${wsDesc}`);
    if (prompt.includes("{dir}")) ph.push(`{dir} → that directory's name`);
    const hint = ph.length ? "Placeholders — " + ph.join("; ") : "filled in before launch";
    html += `<label class="field"><span class="field-label">Prompt <small>${esc(hint)} · edit if needed</small></span>
      <textarea class="cfg-prompt" rows="6">${esc(prompt)}</textarea></label>`;
  }
  html += `<button class="review-go" onclick="launchConfig()">Start</button>`;
  form.innerHTML = html;
  const first = form.querySelector(".cfg-input");
  if (first) first.focus();
}

function launchConfig() {
  const b = configBtn, variant = activeVariant();
  const defs = (variant ? variant.inputs : b.inputs) || [];
  const inputs = {};
  for (const el of document.querySelectorAll("#config-form .cfg-input")) {
    inputs[el.dataset.id] = el.value.trim();
  }
  for (const d of defs) {
    if (d.required && !inputs[d.id]) { alert((d.label || d.id) + " is required"); return; }
  }
  const promptEl = document.querySelector("#config-form .cfg-prompt");
  const body = { button: b.id, variant: variant ? variant.id : "", inputs, agent: selectedAgent(), model: selectedModel() };
  if (promptEl) body.prompt = promptEl.value; // user-edited prompt (placeholders still filled server-side)
  createSession(body);
}

/* ---------- folder chooser (open a checkout anywhere on disk) ---------- */

let browsePath = "";   // the folder currently shown / to be opened
let browseParent = ""; // its parent ("" at the filesystem root)

function browseRefresh() { browseGo(browsePath); } // re-list current folder (e.g. after toggling hidden)

async function browseGo(path) {
  const hidden = document.getElementById("browse-hidden").checked ? "&hidden=1" : "";
  const data = await fetch("/api/browse?path=" + encodeURIComponent((path || "").trim()) + hidden)
    .then((r) => (r.ok ? r.json() : null)).catch(() => null);
  if (!data) {
    document.getElementById("browse-list").innerHTML = `<div class="group-label">can't open that folder</div>`;
    return;
  }
  browsePath = data.path;
  browseParent = data.parent;
  document.getElementById("browse-path").value = data.path;
  renderBrowse(data);
}

function browseUp() { if (browseParent) browseGo(browseParent); }

function renderBrowse(data) {
  const list = document.getElementById("browse-list");
  list.innerHTML = "";
  const head = document.createElement("div");
  head.className = "group-label";
  head.textContent = data.display + (data.isRepo ? "  ·  git repo (start here ↓)" : "");
  list.appendChild(head);
  if (!data.dirs.length) {
    const none = document.createElement("div");
    none.className = "group-label";
    none.textContent = "no sub-folders";
    list.appendChild(none);
  }
  for (const d of data.dirs) {
    const el = document.createElement("div");
    el.className = "repo";
    el.onclick = () => browseGo(d.path);
    el.innerHTML = `
      <span class="ico">${d.repo ? "▸" : "📁"}</span>
      <span class="info"><div class="r-name">${esc(d.name)}</div></span>
      <span class="go">›</span>`;
    list.appendChild(el);
  }
}

function launchBrowse() {
  const path = document.getElementById("browse-path").value.trim() || browsePath;
  if (!path) { alert("Choose a folder."); return; }
  createSession({ cwd: path, mode: "browse", name: "", model: selectedModel(), agent: selectedAgent(), button: pickBtn && pickBtn.id });
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
    const branch = r.branch ? `<span class="r-branch">${esc(r.branch)}</span> · ` : "";
    el.innerHTML = `
      <span class="ico">▸</span>
      <span class="info">
        <div class="r-name">${esc(r.name)}</div>
        <div class="r-sub">${esc(r.display)} · ${branch}${esc(r.sub)}</div>
      </span>
      <span class="go">↵</span>`;
    list.appendChild(el);
  }
}

let modelList = [];
function selectedAgent() {
  return document.querySelector('input[name="agent"]:checked')?.value || ""; // "" = nothing picked
}
async function populateModels() {
  // Models are per-agent. Until an agent is picked there's nothing to list.
  const agent = selectedAgent();
  const sel = document.getElementById("model-sel");
  const ver = document.getElementById("model-ver");
  if (!agent) {
    modelList = [];
    sel.innerHTML = `<option value="">— pick an agent —</option>`;
    sel.disabled = true;
    ver.innerHTML = `<option value="">latest</option>`;
    ver.disabled = true;
    return;
  }
  sel.disabled = false;
  modelList = await fetch("/api/models?agent=" + encodeURIComponent(agent))
    .then((r) => r.json()).catch(() => []);
  sel.innerHTML = modelList.map((m) => `<option value="${esc(m.id)}">${esc(m.label)}</option>`).join("");
  onModelChange();
}
async function onAgentChange() {
  await populateModels(); // swap the model list to match the chosen agent
  if (mode === "resume") { // re-list past sessions for the newly selected agent
    resumables = await fetchResumables();
    renderList();
  }
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
  createSession({ cwd: r.path, mode, name: r.name, model: selectedModel(), agent: selectedAgent(), button: pickBtn && pickBtn.id });
}
function launchResume(r) {
  const fork = document.getElementById("fork-chk").checked;
  createSession({ mode: "resume", sessionId: r.sessionId, fork, model: selectedModel(), agent: selectedAgent() });
}

async function createSession(body) {
  if (!body.agent) { alert("Pick an agent first."); return; } // no default — choose deliberately
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

document.addEventListener("keydown", (e) => { if (e.key === "Escape") { closeModal(); closeSettings(); closeDebug(); closeButtonsManager(); } });

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

/* ---------- launch-buttons manager ---------- */

let managedButtons = [];
const MGR_AGENTS = ["claude", "codex", "gemini"];
const MGR_ICONS = ["plus", "ticket", "review", "resume", ""];

async function openButtonsManager() {
  const btns = await fetch("/api/buttons").then((r) => r.json()).catch(() => []);
  managedButtons = JSON.parse(JSON.stringify(btns)); // edit a copy
  renderButtonsMgr();
  document.getElementById("buttons-overlay").classList.add("open");
}
function closeButtonsManager() { document.getElementById("buttons-overlay").classList.remove("open"); }

function blankButton() {
  return { id: "btn-" + Date.now().toString(36), label: "New button", icon: "plus",
    agents: [...MGR_AGENTS], showModel: true, workspace: { pick: true }, inputs: [], prompt: "" };
}
function addManagedButton() { managedButtons.push(blankButton()); renderButtonsMgr(); }
function deleteManagedButton(i) { managedButtons.splice(i, 1); renderButtonsMgr(); }
function moveMB(i, d) {
  const j = i + d;
  if (j < 0 || j >= managedButtons.length) return;
  [managedButtons[i], managedButtons[j]] = [managedButtons[j], managedButtons[i]];
  renderButtonsMgr();
}

// field binders (mutate the in-memory copy; saved on Save)
function updateMB(i, field, value) { managedButtons[i][field] = value; }
function updateMBBool(i, field, checked) { managedButtons[i][field] = checked; }
function toggleMBAgent(i, agent, checked) {
  const cur = new Set((managedButtons[i].agents && managedButtons[i].agents.length) ? managedButtons[i].agents : MGR_AGENTS);
  checked ? cur.add(agent) : cur.delete(agent);
  managedButtons[i].agents = MGR_AGENTS.filter((a) => cur.has(a));
}
function setMBWorkspace(i) {
  const card = document.getElementById("mb-" + i);
  const t = card.querySelector(".mb-ws-type").value, v = card.querySelector(".mb-ws-val").value.trim();
  const b = managedButtons[i];
  if (t === "pick") b.workspace = { pick: true };
  else if (t === "dir") b.workspace = { dir: v };
  else if (t === "scratch") b.workspace = { scratch: v };
  else delete b.workspace;
}
function addMBInput(i) { (managedButtons[i].inputs = managedButtons[i].inputs || []).push({ id: "", placeholder: "" }); renderButtonsMgr(); }
function removeMBInput(i, j) { managedButtons[i].inputs.splice(j, 1); renderButtonsMgr(); }
function updateMBInput(i, j, field, value) { managedButtons[i].inputs[j][field] = value; }
function updateMBInputBool(i, j, field, checked) { managedButtons[i].inputs[j][field] = checked; }

function renderButtonsMgr() {
  const wrap = document.getElementById("buttons-mgr");
  wrap.innerHTML = managedButtons.map((b, i) => {
    const agents = (b.agents && b.agents.length) ? b.agents : MGR_AGENTS;
    const ws = b.workspace || {};
    const wsType = ws.pick ? "pick" : ws.dir ? "dir" : ws.scratch ? "scratch" : "";
    const wsVal = ws.dir || ws.scratch || "";
    const inputs = (b.inputs || []).map((inp, j) => `
      <div class="mb-input">
        <input placeholder="id" value="${esc(inp.id || "")}" oninput="updateMBInput(${i},${j},'id',this.value)">
        <input placeholder="placeholder" value="${esc(inp.placeholder || "")}" oninput="updateMBInput(${i},${j},'placeholder',this.value)">
        <select onchange="updateMBInput(${i},${j},'transform',this.value)"><option value="">no transform</option><option value="blue-prefix" ${inp.transform === "blue-prefix" ? "selected" : ""}>blue-prefix</option></select>
        <label><input type="checkbox" ${inp.required ? "checked" : ""} onchange="updateMBInputBool(${i},${j},'required',this.checked)"> req</label>
        <button class="env-x" type="button" onclick="removeMBInput(${i},${j})">✕</button>
      </div>`).join("");
    const variantNote = (b.variants && b.variants.length)
      ? `<div class="mb-note">Has ${b.variants.length} variant(s) — edit those in <code>~/.agorai/buttons.json</code>.</div>` : "";
    return `<div class="mb-card" id="mb-${i}">
      <div class="mb-head">
        <input class="mb-label" value="${esc(b.label || "")}" oninput="updateMB(${i},'label',this.value)" placeholder="Label">
        <span class="mb-move">
          <button class="env-x" type="button" onclick="moveMB(${i},-1)">↑</button>
          <button class="env-x" type="button" onclick="moveMB(${i},1)">↓</button>
          <button class="env-x" type="button" onclick="deleteManagedButton(${i})" title="delete">✕</button>
        </span>
      </div>
      <div class="mb-row">
        <label>Icon <select onchange="updateMB(${i},'icon',this.value)">${MGR_ICONS.map((ic) => `<option value="${ic}" ${b.icon === ic ? "selected" : ""}>${ic || "none"}</option>`).join("")}</select></label>
        <label><input type="checkbox" ${b.showModel !== false ? "checked" : ""} onchange="updateMBBool(${i},'showModel',this.checked)"> model picker</label>
        <label><input type="checkbox" ${b.unattended ? "checked" : ""} onchange="updateMBBool(${i},'unattended',this.checked)"> unattended</label>
      </div>
      <div class="mb-row">Agents: ${MGR_AGENTS.map((a) => `<label><input type="checkbox" ${agents.includes(a) ? "checked" : ""} onchange="toggleMBAgent(${i},'${a}',this.checked)"> ${AGENT_NAMES[a]}</label>`).join(" ")}</div>
      <div class="mb-row">
        <label>Workspace <select class="mb-ws-type" onchange="setMBWorkspace(${i})">
          <option value="pick" ${wsType === "pick" ? "selected" : ""}>repo picker</option>
          <option value="dir" ${wsType === "dir" ? "selected" : ""}>fixed dir</option>
          <option value="scratch" ${wsType === "scratch" ? "selected" : ""}>scratch name</option>
          <option value="" ${wsType === "" ? "selected" : ""}>none</option></select></label>
        <input class="mb-ws-val" placeholder="~/dev/PRs  or  review" value="${esc(wsVal)}" oninput="setMBWorkspace(${i})">
      </div>
      <div class="mb-inputs"><div class="mb-sub">Inputs</div>${inputs}<button class="add-env" type="button" onclick="addMBInput(${i})">+ input</button></div>
      <label class="mb-field">Prompt <small>placeholders: {input} {workspace} {dir}</small>
        <textarea class="mb-prompt" oninput="updateMB(${i},'prompt',this.value)" rows="3" placeholder="(optional) initial prompt">${esc(b.prompt || "")}</textarea></label>
      <label class="mb-field">Session name <input value="${esc(b.sessionName || "")}" oninput="updateMB(${i},'sessionName',this.value)" placeholder="e.g. Ticket {ticket}"></label>
      ${variantNote}
    </div>`;
  }).join("");
}

async function saveManagedButtons() {
  const res = await fetch("/api/buttons", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(managedButtons) });
  if (!res.ok) { alert("Save failed: " + (await res.text())); return; }
  closeButtonsManager();
  renderTopBar();
}
async function resetManagedButtons() {
  if (!confirm("Reset to the built-in default buttons? Your ~/.agorai/buttons.json will be removed.")) return;
  managedButtons = await fetch("/api/buttons", { method: "DELETE" }).then((r) => r.json()).catch(() => []);
  renderButtonsMgr();
  renderTopBar();
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
renderTopBar();
loadConfig();
connectControl();

// Glyphs for the named icons used in buttons.json.
const BUTTON_ICONS = { plus: "+", ticket: "✦", review: "⊚", resume: "↻" };

// Render the top-bar launch buttons from /api/buttons (built-in defaults,
// overridable by ~/.agorai/buttons.json). Each button still opens its existing
// modal via `mode` for now; the modal-from-config migration comes next.
async function renderTopBar() {
  const bar = document.getElementById("newbar");
  let buttons = [];
  try { buttons = await fetch("/api/buttons").then((r) => r.json()); } catch {}
  if (!buttons.length) return;
  bar.innerHTML = "";
  for (const b of buttons) {
    const el = document.createElement("button");
    el.className = "new-btn";
    const ico = BUTTON_ICONS[b.icon] || "";
    el.innerHTML = (ico ? `<b>${esc(ico)}</b> ` : "") + esc(b.label);
    el.onclick = () => openButton(b);
    bar.appendChild(el);
  }
}
