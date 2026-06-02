/* agorai frontend — talks to the Go server over REST + two WebSockets. */

const FitAddon = window.FitAddon.FitAddon;
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
  const needs = state.sessions.filter((s) => s.state === "waiting" || s.state === "perm").length;
  document.getElementById("meta").textContent =
    `${state.sessions.length} session${state.sessions.length === 1 ? "" : "s"}` +
    (needs ? ` · ${needs} need input` : "");
}

function sessionCard(s) {
  const el = document.createElement("div");
  el.className = "session " + s.state
    + (s.id === state.selected ? " selected" : "")
    + (state.unread.has(s.id) ? " unread" : "");
  el.onclick = () => selectSession(s.id);

  const badge =
    s.state === "perm" ? `<span class="badge perm">permission</span>`
    : s.state === "waiting" ? `<span class="badge input">input</span>`
    : "";

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
      const q = s.prompt.question ? `<div class="q">${esc(s.prompt.question)}</div>` : "";
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
    state.terms.get(id).fit.fit();
  }

  const s = state.sessions.find((x) => x.id === id);
  if (s) {
    document.getElementById("t-name").textContent = s.name;
    document.getElementById("t-cwd").textContent = s.cwd + " · " + s.branch;
    document.getElementById("t-right").textContent = "Model: " + s.model;
  }
  renderSessions();
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
    theme: { background: "#0a0d12", foreground: "#cdd6e0" },
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
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

  state.terms.set(id, { term, fit, ws, pane });
}

function sendResize(ws, term) {
  if (ws.readyState === 1) ws.send(JSON.stringify({ resize: [term.cols, term.rows] }));
}

window.addEventListener("resize", () => {
  const t = state.terms.get(state.selected);
  if (t) { t.fit.fit(); sendResize(t.ws, t.term); }
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
let mode = "open";
const overlay = document.getElementById("overlay");

async function openModal(initialMode = "open") {
  overlay.classList.add("open");
  document.getElementById("search").value = "";
  resumables = []; // refetched when the Resume tab is opened
  await populateModels();
  repos = await fetch("/api/repos").then((r) => r.json()).catch(() => []);
  await setMode(initialMode); // selects the tab + renders the matching list
  document.getElementById("search").focus();
}
function closeModal() { overlay.classList.remove("open"); }

const MODAL_TITLES = {
  open: "New <span>session</span>",
  worktree: "New session in <span>new branch</span>",
  resume: "Resume <span>session</span>",
  review: "Review <span>PR</span>",
};

async function setMode(m) {
  mode = m;
  document.getElementById("modal-title").innerHTML = MODAL_TITLES[m] || MODAL_TITLES.open;
  const modal = document.getElementById("modal");
  modal.classList.toggle("wt-mode", m === "worktree");
  modal.classList.toggle("resume-mode", m === "resume");
  modal.classList.toggle("review-mode", m === "review");
  document.getElementById("search").placeholder = m === "resume" ? "Filter past sessions…" : "Filter repos…";

  if (m === "review") {
    document.getElementById("review-ticket").value = "";
  }
  if (m === "resume" && !resumables.length) {
    resumables = await fetch("/api/resumable").then((r) => r.json()).catch(() => []);
  }
  renderList();
}

function launchReview() {
  const ticket = document.getElementById("review-ticket").value.trim();
  if (!ticket) { alert("Enter a Linear ticket number."); return; }
  createSession({ mode: "review", ticket, model: selectedModel() });
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
async function populateModels() {
  if (modelsLoaded) return;
  const list = await fetch("/api/models").then((r) => r.json()).catch(() => []);
  document.getElementById("model-sel").innerHTML =
    list.map((m) => `<option value="${esc(m.id)}">${esc(m.label)}</option>`).join("");
  modelsLoaded = true;
}
function selectedModel() {
  return document.getElementById("model-sel").value;
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

document.addEventListener("keydown", (e) => { if (e.key === "Escape") { closeModal(); closeSettings(); } });

/* ---------- settings ---------- */

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

/* ---------- util ---------- */

function esc(s) {
  return String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));
}

loadConfig();
connectControl();
