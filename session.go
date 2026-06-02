package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

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
	ID       string // our launch id (passed to claude as AGORAI_ID)
	ClaudeID string // claude's own session_id, learned from the first hook

	mu       sync.Mutex
	name     string
	cwd      string
	branch   string
	model    string // model id passed to --model ("" = default)
	state     string
	recap     string
	promptQ   string         // parsed question for a permission prompt
	promptOpts []PromptOption // parsed options for a permission prompt
	resumeOf  string         // claude id we resumed from; cleaned up once the live id is known
	ptmx     *os.File
	cmd      *exec.Cmd
	ring     *ringBuffer
	clients  map[chan []byte]bool // attached terminal WebSockets
}

// SessionDTO is the JSON shape sent to the browser.
type SessionDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Cwd    string `json:"cwd"`
	Branch string `json:"branch"`
	State  string      `json:"state"`
	Recap  string      `json:"recap"`
	Model  string      `json:"model"` // display label, e.g. "Opus" / "default"
	Prompt *PromptInfo `json:"prompt,omitempty"`
}

func (s *Session) dto() SessionDTO {
	s.mu.Lock()
	defer s.mu.Unlock()
	var prompt *PromptInfo
	if s.state == StatePerm && len(s.promptOpts) > 0 {
		prompt = &PromptInfo{Question: s.promptQ, Options: s.promptOpts}
	}
	return SessionDTO{
		ID: s.ID, Name: s.name, Cwd: s.cwd, Branch: s.branch,
		State: s.state, Recap: s.recap, Model: modelLabel(s.model), Prompt: prompt,
	}
}

func (s *Session) setPrompt(question string, opts []PromptOption) {
	s.mu.Lock()
	s.promptQ = question
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
func (m *Manager) buildEnv(launchID string) []string {
	base := map[string]string{}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			base[kv[:i]] = kv[i+1:]
		}
	}
	base["AGORAI_ID"] = launchID
	base["TERM"] = "xterm-256color"
	if m.cfg != nil {
		for k, v := range m.cfg.Get().Env {
			base[k] = v
		}
	}
	out := make([]string, 0, len(base))
	for k, v := range base {
		out = append(out, k+"="+v)
	}
	return out
}

// Spawn starts a new `claude` process in cwd and begins streaming its output.
// extraArgs are passed to the claude CLI (e.g. "--resume", "<id>").
func (m *Manager) Spawn(cwd, name, branch string, extraArgs ...string) (*Session, error) {
	id := newID()

	cmd := exec.Command("claude", extraArgs...)
	cmd.Dir = cwd
	cmd.Env = m.buildEnv(id)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 24, Cols: 80})

	s := &Session{
		ID:      id,
		name:    name,
		cwd:     cwd,
		branch:  branch,
		state:   StateIdle, // hooks flip to working on UserPromptSubmit
		recap:   "Starting…",
		ptmx:    ptmx,
		cmd:     cmd,
		ring:    newRingBuffer(256 * 1024),
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
		p := persisted{ClaudeID: claudeID, Cwd: s.cwd, Name: s.name, Branch: s.branch, Model: s.model}
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
	}
	s.close()
	m.changed()
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
		args := append([]string{"--resume", p.ClaudeID}, modelArgs(p.Model)...)
		s, err := m.Spawn(p.Cwd, p.Name, p.Branch, args...)
		if err != nil {
			m.store.remove(p.ClaudeID)
			continue
		}
		s.setModel(p.Model)
		s.ClaudeID = p.ClaudeID // same id continues; keep it recognized
		// Seed the recap from the transcript so a restored session shows its last
		// line immediately (not "Starting…"), even with no hooks installed.
		if tp := transcriptPathFor(p.ClaudeID); tp != "" {
			s.setRecap(lastAssistantLine(tp))
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
	p := persisted{ClaudeID: claudeID, Cwd: s.cwd, Name: s.name, Branch: s.branch, Model: s.model}
	s.mu.Unlock()
	if m.store != nil {
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
	p := persisted{ClaudeID: s.ClaudeID, Cwd: s.cwd, Name: name, Branch: s.branch, Model: s.model}
	s.mu.Unlock()
	if p.ClaudeID != "" && m.store != nil {
		m.store.upsert(p)
	}
	m.changed()
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
		r.buf = r.buf[len(r.buf)-r.max:]
	}
}

func (r *ringBuffer) Bytes() []byte {
	return append([]byte(nil), r.buf...)
}
