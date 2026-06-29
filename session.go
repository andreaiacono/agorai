package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Session states surfaced to the UI.
const (
	StateWorking = "working"
	StateWaiting = "waiting" // idle_prompt — waiting for the user to type
	StatePerm    = "perm"    // permission_prompt — needs a yes/no/always
	StateIdle    = "idle"
	StateDone    = "done"
)

// Session is one hosted `claude` process and everything we know about it.
type Session struct {
	ID       string    // our launch id (passed to the agent as AGORAI_ID)
	ClaudeID string    // the agent's own session_id, learned from the first hook
	agent    AgentKind // which CLI backs this session (set at spawn, immutable)

	mu          sync.Mutex
	name        string
	cwd         string
	branch      string
	model       string // model id passed to --model ("" = default)
	actualModel string // raw model id seen in the transcript — resolves what "default" runs on
	state       string
	recap       string
	ctxTokens   int // live context-window size (tokens) from the transcript; 0 if unknown
	ctxMax      int // model's context-window ceiling; 0 if unknown
	// account usage-limit windows (codex only): used percent + unix reset per window
	limit5hPct, limitWkPct     int
	limit5hReset, limitWkReset int64
	promptQ     string         // parsed question for a permission prompt
	promptCtx   string         // parsed context lines above the question (for the tooltip)
	promptOpts  []PromptOption // parsed options for a permission prompt
	resumeOf    string         // claude id we resumed from; cleaned up once the live id is known
	tailing     bool           // a codex rollout-state tailer is running for this session
	ptmx        *os.File
	cmd         *exec.Cmd
	ring        *ringBuffer
	clients     map[chan []byte]bool // attached terminal WebSockets
}

// SessionDTO is the JSON shape sent to the browser.
type SessionDTO struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Cwd    string      `json:"cwd"`
	Branch string      `json:"branch"`
	State  string      `json:"state"`
	Recap  string      `json:"recap"`
	Model  string      `json:"model"` // display label, e.g. "Opus" / "default"
	Agent  string      `json:"agent"` // which CLI backs it: "claude" | "codex"
	Prompt *PromptInfo `json:"prompt,omitempty"`
	// Context-window fill (claude + codex): tokens in the latest turn and the
	// model's ceiling. 0/omitted when unknown, so the UI hides the gauge.
	CtxTokens int `json:"ctxTokens,omitempty"`
	CtxMax    int `json:"ctxMax,omitempty"`
	// Account usage limits (codex only; absent for claude — not readable from disk).
	Limits *UsageLimits `json:"limits,omitempty"`
}

// UsageLimits is the account's rolling usage windows (codex /status data).
type UsageLimits struct {
	Pct5h     int   `json:"pct5h"`
	Reset5h   int64 `json:"reset5h"`
	PctWeek   int   `json:"pctWeek"`
	ResetWeek int64 `json:"resetWeek"`
}

func (s *Session) dto() SessionDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	var prompt *PromptInfo
	if s.state == StatePerm && len(s.promptOpts) > 0 {
		prompt = &PromptInfo{Question: s.promptQ, Context: s.promptCtx, Options: s.promptOpts}
	}
	// Prefer the model actually seen in the transcript: it shows the real
	// version behind a "default" or alias choice (e.g. "Opus 4.8", not "default").
	a := agentFor(s.agent)
	model := a.ModelLabel(s.model)
	if s.actualModel != "" {
		model = a.PrettyModelID(s.actualModel)
	}
	var limits *UsageLimits
	if s.limit5hReset > 0 || s.limitWkReset > 0 {
		limits = &UsageLimits{Pct5h: s.limit5hPct, Reset5h: s.limit5hReset, PctWeek: s.limitWkPct, ResetWeek: s.limitWkReset}
	}
	return SessionDTO{
		ID: s.ID, Name: s.name, Cwd: s.cwd, Branch: s.branch,
		State: s.state, Recap: s.recap, Model: model, Agent: string(normalizeAgent(s.agent)), Prompt: prompt,
		CtxTokens: s.ctxTokens, CtxMax: s.ctxMax, Limits: limits,
	}
}

func (s *Session) setPrompt(question, context string, opts []PromptOption) {
	s.mu.Lock()
	s.promptQ = question
	s.promptCtx = context
	s.promptOpts = opts
	s.mu.Unlock()
}

// recentBytes returns up to the last n bytes of buffered PTY output.
func (s *Session) recentBytes(n int) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.ring.buf
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return append([]byte(nil), b...)
}

func (s *Session) setActualModel(id string) {
	s.mu.Lock()
	s.actualModel = id
	s.mu.Unlock()
}

func (s *Session) setContext(tokens, max int) {
	s.mu.Lock()
	s.ctxTokens, s.ctxMax = tokens, max
	s.mu.Unlock()
}

func (s *Session) setLimits(pct5h int, reset5h int64, pctWk int, resetWk int64) {
	s.mu.Lock()
	s.limit5hPct, s.limit5hReset, s.limitWkPct, s.limitWkReset = pct5h, reset5h, pctWk, resetWk
	s.mu.Unlock()
}

// claudeID returns the agent's learned session id (race-free).
func (s *Session) claudeID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ClaudeID
}

// workdir returns the session's working directory.
func (s *Session) workdir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

// beginTailing claims the codex state-tailer slot, returning false if a tailer
// is already running for this session (so it isn't started twice).
func (s *Session) beginTailing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tailing {
		return false
	}
	s.tailing = true
	return true
}

func (s *Session) setModel(id string) {
	s.mu.Lock()
	s.model = id
	s.mu.Unlock()
}

func (s *Session) currentState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Session) setRecap(r string) {
	if r == "" {
		return
	}
	s.mu.Lock()
	s.recap = r
	s.mu.Unlock()
}

func (s *Session) setState(state, recap string) {
	s.mu.Lock()
	s.state = state
	if recap != "" {
		s.recap = recap
	}
	s.mu.Unlock()
}

// addClient registers a terminal WebSocket and immediately queues the scrollback
// snapshot so a (re)connecting browser sees recent output. The snapshot is sent
// under the lock so it can't be interleaved after live output.
func (s *Session) addClient() chan []byte {
	ch := make(chan []byte, 256)
	s.mu.Lock()
	s.clients[ch] = true
	if snap := s.ring.Bytes(); len(snap) > 0 {
		ch <- snap // buffered (cap 256); a single element never blocks
	}
	s.mu.Unlock()
	return ch
}

func (s *Session) removeClient(ch chan []byte) {
	s.mu.Lock()
	if s.clients[ch] {
		delete(s.clients, ch)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *Session) writeInput(p []byte) {
	s.mu.Lock()
	f := s.ptmx
	s.mu.Unlock()
	if f != nil {
		_, _ = f.Write(p)
	}
}

// close terminates the underlying claude process and its PTY.
func (s *Session) close() {
	s.mu.Lock()
	f := s.ptmx
	c := s.cmd
	s.ptmx = nil
	s.mu.Unlock()
	if c != nil && c.Process != nil {
		_ = c.Process.Signal(syscall.SIGTERM)
	}
	if f != nil {
		_ = f.Close()
	}
}

func (s *Session) setResumeOf(id string) {
	s.mu.Lock()
	s.resumeOf = id
	s.mu.Unlock()
}

func (s *Session) resize(rows, cols uint16) {
	s.mu.Lock()
	f := s.ptmx
	s.mu.Unlock()
	if f != nil {
		_ = pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols})
	}
}

// resizeRepaint sets the PTY size like resize, but when the size is unchanged
// it briefly wiggles the width to force a SIGWINCH. A freshly attached viewer
// has just replayed old buffered bytes — the TUI's live region in that replay
// is stale, and only a repaint by claude renders it cleanly.
func (s *Session) resizeRepaint(rows, cols uint16) {
	s.mu.Lock()
	f := s.ptmx
	s.mu.Unlock()
	if f == nil {
		return
	}
	if cur, err := pty.GetsizeFull(f); err == nil && cur.Rows == rows && cur.Cols == cols && cols > 1 {
		_ = pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols - 1})
		time.Sleep(50 * time.Millisecond) // let the TUI process the shrink first
	}
	_ = pty.Setsize(f, &pty.Winsize{Rows: rows, Cols: cols})
}

// readLoop pumps PTY output into the ring buffer and fans it out to attached
// clients. Slow clients drop frames rather than stalling the others.
func (s *Session) readLoop(onChange func()) {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.mu.Lock()
			s.ring.Write(data)
			// NB: we do NOT infer "working" from output — a resumed session
			// renders its UI on open, which isn't work. State is driven by hooks
			// (UserPromptSubmit → working, Stop → idle).
			for ch := range s.clients {
				select {
				case ch <- data:
				default:
				}
			}
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	s.setState(StateDone, "Session ended")
	onChange()
}

// Manager owns all sessions and notifies onChange whenever the set or any
// session's state changes.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	order    []string
	store    *Store
	cfg      *ConfigStore
	onChange func()
}

func NewManager(store *Store, cfg *ConfigStore) *Manager {
	return &Manager{sessions: map[string]*Session{}, store: store, cfg: cfg}
}

// buildEnv assembles the child process environment: the inherited environment,
// our correlation id, a sane TERM, and the user-configured extra vars (which
// win on conflict). Deduped so the configured value is the one that takes.
func (m *Manager) buildEnv(launchID string, exclude ...string) []string {
	base := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			base[kv[:i]] = kv[i+1:]
		}
	}
	if m.cfg != nil {
		for k, v := range m.cfg.Get().Env {
			base[k] = v
		}
	}
	for _, k := range exclude { // strip after merging both sources (e.g. DATABASE_URL)
		delete(base, k)
	}
	base["AGORAI_ID"] = launchID
	base["TERM"] = "xterm-256color"
	out := make([]string, 0, len(base))
	for k, v := range base {
		out = append(out, k+"="+v)
	}
	return out
}

// Spawn starts a new claude process in cwd and begins streaming its output.
// extraArgs are passed to the claude CLI (e.g. "--resume", "<id>").
func (m *Manager) Spawn(cwd, name, branch string, extraArgs ...string) (*Session, error) {
	return m.spawn(AgentClaude, "", cwd, name, branch, nil, extraArgs...)
}

// SpawnWithoutEnv is like Spawn but drops the given env vars (e.g. review
// sessions run without DATABASE_URL so they can't query the DB).
func (m *Manager) SpawnWithoutEnv(cwd, name, branch string, excludeEnv []string, extraArgs ...string) (*Session, error) {
	return m.spawn(AgentClaude, "", cwd, name, branch, excludeEnv, extraArgs...)
}

// SpawnAs starts a session backed by the given agent (claude or codex).
func (m *Manager) SpawnAs(agent AgentKind, cwd, name, branch string, extraArgs ...string) (*Session, error) {
	return m.spawn(agent, "", cwd, name, branch, nil, extraArgs...)
}

// SpawnAsWithoutEnv is SpawnAs but drops the given env vars (e.g. reviews run
// without DATABASE_URL).
func (m *Manager) SpawnAsWithoutEnv(agent AgentKind, cwd, name, branch string, excludeEnv []string, extraArgs ...string) (*Session, error) {
	return m.spawn(agent, "", cwd, name, branch, excludeEnv, extraArgs...)
}

// hasClaudeID reports whether a live session already owns this agent session id
// — used so the codex id-learner doesn't claim a rollout another session took.
func (m *Manager) hasClaudeID(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range m.sessions {
		if s.ClaudeID == id {
			return true
		}
	}
	return false
}

// spawn starts a session. id is the agorai launch id (== the DTO id); pass "" for
// a new session, or a persisted LaunchID on restore so the id — and thus the
// client's saved row order — stays stable across restarts.
func (m *Manager) spawn(agent AgentKind, id, cwd, name, branch string, excludeEnv []string, extraArgs ...string) (*Session, error) {
	agent = normalizeAgent(agent)
	if id == "" {
		id = newID()
	}

	cmd := exec.Command(agentFor(agent).Command(), extraArgs...)
	cmd.Dir = cwd
	cmd.Env = m.buildEnv(id, excludeEnv...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80})

	s := &Session{
		ID:      id,
		agent:   agent,
		name:    name,
		cwd:     cwd,
		branch:  branch,
		state:   StateIdle, // hooks flip to working on UserPromptSubmit
		recap:   "Starting…",
		ptmx:    ptmx,
		cmd:     cmd,
		ring:    newRingBuffer(m.ringBytes()),
		clients: map[chan []byte]bool{},
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.order = append(m.order, id)
	m.mu.Unlock()

	go s.readLoop(m.changed)
	m.changed()
	return s, nil
}

func (m *Manager) Get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// bind records claude's session_id against the session we launched, the first
// time a hook reports it, and persists the session so it survives a restart.
// Returns the session (or nil if the launch id is unknown).
func (m *Manager) bind(launchID, claudeID string) *Session {
	m.mu.Lock()
	s := m.sessions[launchID]
	newly := s != nil && s.ClaudeID == "" && claudeID != ""
	if newly {
		s.ClaudeID = claudeID
	}
	m.mu.Unlock()

	if newly && m.store != nil {
		s.mu.Lock()
		p := persisted{ClaudeID: claudeID, LaunchID: s.ID, Cwd: s.cwd, Name: s.name, Branch: s.branch, Model: s.model, Agent: s.agent}
		oldID := s.resumeOf
		s.mu.Unlock()

		m.store.upsert(p)
		if oldID != "" && oldID != claudeID {
			m.store.remove(oldID) // resume forked a new id; drop the stale one
		}
	}
	return s
}

// Remove kills a session and forgets it (so a restart won't bring it back).
func (m *Manager) Remove(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	if s != nil {
		delete(m.sessions, id)
		for i, oid := range m.order {
			if oid == id {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
	}
	m.mu.Unlock()

	if s == nil {
		return
	}
	if m.store != nil {
		m.store.remove(s.ClaudeID)
		m.store.remove(s.resumeOf)
		m.store.remove(s.ID) // codex placeholder records are keyed by the launch id
	}
	s.close()
	m.changed()
}

// persistPlaceholder saves a session before its real id is known (codex mints
// its id lazily). The record is keyed by the agorai launch id and marked
// Pending, so a restart restores it as a fresh session until the real id is
// learned (adopt then replaces it with a resumable record).
func (m *Manager) persistPlaceholder(s *Session) {
	if m.store == nil {
		return
	}
	s.mu.Lock()
	p := persisted{ClaudeID: s.ID, LaunchID: s.ID, Cwd: s.cwd, Name: s.name, Branch: s.branch, Model: s.model, Agent: s.agent, Pending: true}
	s.mu.Unlock()
	m.store.upsert(p)
}

// applyPersistedNames overrides each resumable's title with the name agorai has
// on record for that session (set at spawn, updated on rename), so the Resume
// picker shows your renamed label when it knows the session.
func (m *Manager) applyPersistedNames(rs []Resumable) []Resumable {
	if m.store == nil {
		return rs
	}
	for i := range rs {
		if p, ok := m.store.lookup(rs[i].SessionID); ok && p.Name != "" {
			rs[i].Title = p.Name
		}
	}
	return rs
}

// forget drops a session from persistence (e.g. it ended cleanly) without
// killing it; the row stays until the process exits or the user closes it.
func (m *Manager) forget(claudeID string) {
	if m.store != nil {
		m.store.remove(claudeID)
	}
}

// RestoreAll re-resumes every persisted session at startup. Entries whose
// working dir or transcript is gone fail to spawn and are pruned.
func (m *Manager) RestoreAll() int {
	n := 0
	for _, p := range m.store.all() {
		a := agentFor(p.Agent)

		// A pending record never learned a resumable id (a codex session with no
		// activity yet). Bring it back as a fresh session in the same dir, and
		// re-save the placeholder under the new launch id (drop the old one).
		if p.Pending {
			s, err := m.spawn(p.Agent, p.LaunchID, p.Cwd, p.Name, p.Branch, nil, a.FreshArgs("", p.Model, "")...)
			m.store.remove(p.ClaudeID)
			if err != nil {
				continue
			}
			s.setModel(p.Model)
			m.persistPlaceholder(s)
			n++
			continue
		}

		args := append(a.ResumeArgs(p.ClaudeID), a.ModelArgs(p.Model)...)
		s, err := m.spawn(p.Agent, p.LaunchID, p.Cwd, p.Name, p.Branch, nil, args...)
		if err != nil {
			m.store.remove(p.ClaudeID)
			continue
		}
		s.setModel(p.Model)
		s.ClaudeID = p.ClaudeID // same id continues; keep it recognized
		// Stamp the launch id so it's reused (and the row order stays stable) on the
		// next restart — restored claude sessions don't re-bind, so do it here. Also
		// migrates pre-LaunchID records (where spawn just minted a fresh one).
		if p.LaunchID != s.ID {
			p.LaunchID = s.ID
			m.store.upsert(p)
		}
		// Seed the recap from the transcript so a restored session shows its last
		// line immediately (not "Starting…"), even with no hooks installed.
		if tp := a.TranscriptPath(p.ClaudeID); tp != "" {
			recap, _ := a.LastLine(tp)
			s.setRecap(recap)
			if normalizeAgent(p.Agent) == AgentClaude {
				s.setContext(claudeContextOf(tp))
			}
		}
		n++
	}
	return n
}

func (m *Manager) List() []SessionDTO {
	m.mu.Lock()
	order := append([]string(nil), m.order...)
	sessions := m.sessions
	m.mu.Unlock()

	out := make([]SessionDTO, 0, len(order))
	for _, id := range order {
		if s := sessions[id]; s != nil {
			out = append(out, s.dto())
		}
	}
	return out
}

func (m *Manager) changed() {
	if m.onChange != nil {
		m.onChange()
	}
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// newUUID returns a random UUID v4 string, suitable for `claude --session-id`.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// adopt records claude's (agorai-assigned) session id and persists the session
// immediately, so it survives a restart without waiting for a hook.
func (m *Manager) adopt(s *Session, claudeID string) {
	s.mu.Lock()
	s.ClaudeID = claudeID
	p := persisted{ClaudeID: claudeID, LaunchID: s.ID, Cwd: s.cwd, Name: s.name, Branch: s.branch, Model: s.model, Agent: s.agent}
	launchID := s.ID
	s.mu.Unlock()
	if m.store != nil {
		m.store.remove(launchID) // drop the spawn-time placeholder (no-op for claude)
		m.store.upsert(p)
	}
}

// Rename changes a session's display name and re-persists it.
func (m *Manager) Rename(id, name string) {
	if name == "" {
		return
	}
	s := m.Get(id)
	if s == nil {
		return
	}
	s.mu.Lock()
	s.name = name
	p := persisted{ClaudeID: s.ClaudeID, LaunchID: s.ID, Cwd: s.cwd, Name: name, Branch: s.branch, Model: s.model, Agent: s.agent}
	s.mu.Unlock()
	if p.ClaudeID != "" && m.store != nil {
		m.store.upsert(p)
	}
	m.changed()
}

// ringBytes sizes the per-session replay buffer from the configured scrollback.
// Output is byte-heavy (ANSI + redraws), so we keep ~512 bytes per line, bounded
// to keep memory sane. This is what gets replayed into a (re)connecting terminal.
func (m *Manager) ringBytes() int {
	sb := 10000
	if m.cfg != nil {
		if v := m.cfg.Get().Scrollback; v > 0 {
			sb = v
		}
	}
	n := sb * 512
	const min, max = 512 << 10, 24 << 20 // 512KB … 24MB
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

// ringBuffer keeps at most max bytes of recent PTY output for replay on connect.
type ringBuffer struct {
	buf []byte
	max int
}

func newRingBuffer(max int) *ringBuffer { return &ringBuffer{max: max} }

func (r *ringBuffer) Write(p []byte) {
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		cut := len(r.buf) - r.max
		// Never start the kept window mid escape-sequence or mid UTF-8 rune — a
		// replay beginning there garbles everything after it. Advance the cut to
		// just past the next newline so replay starts on a clean line (bounded,
		// in case a pathological chunk has no newline at all).
		if i := bytes.IndexByte(r.buf[cut:min(cut+4096, len(r.buf))], '\n'); i >= 0 {
			cut += i + 1
		}
		r.buf = r.buf[cut:]
	}
}

func (r *ringBuffer) Bytes() []byte {
	return append([]byte(nil), r.buf...)
}
