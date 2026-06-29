package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// codexAgent is the OpenAI Codex CLI backend. Unlike claude it can't be told a
// session id at spawn (AssignsID == false); agorai learns the id from Codex's
// rollout files after spawn. State/recap come from those rollout JSONL files.
type codexAgent struct{}

func (codexAgent) Kind() AgentKind { return AgentCodex }
func (codexAgent) Command() string { return "codex" }
func (codexAgent) AssignsID() bool { return false }

func (codexAgent) ModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"-m", model}
}

// FreshArgs: codex ignores the agorai sid (it mints its own). --no-alt-screen
// keeps the TUI inline so its scrollback survives in agorai's replay buffer.
func (codexAgent) FreshArgs(_, model, prompt string) []string {
	args := []string{"--no-alt-screen"}
	args = append(args, codexAgent{}.ModelArgs(model)...)
	if prompt != "" {
		args = append(args, prompt)
	}
	return args
}

// PromptArgs: unattended reviews bypass approvals AND the sandbox — `-a never`
// alone keeps the sandbox on, which blocks `gh`'s network access (the diff
// fails with "error connecting to api.github.com"). This is codex's equivalent
// of claude's --dangerously-skip-permissions; the read-only guarantee rests on
// the prompt, same as the claude path.
func (codexAgent) PromptArgs(_, model, prompt string, unattended bool) []string {
	args := []string{"--no-alt-screen"}
	if unattended {
		args = append(args, unattendedArgs(AgentCodex)...)
	}
	args = append(args, codexAgent{}.ModelArgs(model)...)
	return append(args, prompt)
}

// ResumeArgs resumes a recorded session by its codex session id (UUID).
func (codexAgent) ResumeArgs(id string) []string { return []string{"resume", id} }

func (codexAgent) Models() []ModelOption { return codexModels }

func (codexAgent) ModelLabel(id string) string {
	if id == "" {
		return "default"
	}
	return id
}

func (codexAgent) PrettyModelID(id string) string { return id }

func (codexAgent) TranscriptPath(id string) string { return codexTranscriptPath(id) }

func (codexAgent) LastLine(path string) (string, string) {
	_, recap, _, model := codexState(path)
	return recap, model
}

// codexModels lists the selectable models. Kept minimal for now — an empty id
// means "use whatever codex is configured to use". A richer list comes later.
var codexModels = []ModelOption{
	{ID: "", Label: "Default"},
}

// ensureCodexTrust makes sure codex won't show its folder-trust prompt for cwd
// by appending a trusted [projects."<cwd>"] table to ~/.codex/config.toml if one
// isn't already there. Append-only and idempotent (skips if already present), so
// it won't disturb the user's existing config.
func ensureCodexTrust(cwd string) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || cwd == "" {
		return
	}
	cfg := filepath.Join(home, ".codex", "config.toml")
	body, err := os.ReadFile(cfg)
	if err != nil {
		return // no config yet — codex will create one; trust handled on first run
	}
	clean := filepath.Clean(cwd)
	header := "[projects.\"" + clean + "\"]"
	if strings.Contains(string(body), header) {
		return // already has an entry for this path
	}
	block := "\n" + header + "\ntrust_level = \"trusted\"\n"
	f, err := os.OpenFile(cfg, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(block)
}

func codexSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".codex", "sessions")
}

// codexTranscriptPath finds the rollout file for a session id. Rollout files are
// named rollout-<timestamp>-<uuid>.jsonl under ~/.codex/sessions/YYYY/MM/DD/.
func codexTranscriptPath(id string) string {
	dir := codexSessionsDir()
	if dir == "" || id == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*", "rollout-*-"+id+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// codexSessionMeta is the first line of a rollout file.
type codexRolloutLine struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexMetaPayload struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

// newestCodexSessionID returns the id of the most recent rollout whose cwd
// matches and whose file was created at/after `after`, skipping ids already
// claimed by a live session. Used to learn a freshly spawned session's id.
func newestCodexSessionID(cwd string, after time.Time, claimed func(string) bool) string {
	dir := codexSessionsDir()
	if dir == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*", "rollout-*.jsonl"))
	want := filepath.Clean(cwd)

	type cand struct {
		id  string
		mod time.Time
	}
	var cands []cand
	for _, p := range matches {
		st, err := os.Stat(p)
		if err != nil || st.ModTime().Before(after.Add(-2*time.Second)) {
			continue
		}
		id, mcwd := codexRolloutMeta(p)
		if id == "" || !samePath(mcwd, want) {
			continue
		}
		if claimed != nil && claimed(id) {
			continue
		}
		cands = append(cands, cand{id, st.ModTime()})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].mod.After(cands[j].mod) })
	return cands[0].id
}

// scanCodexResumable returns the most recent codex sessions on disk (newest
// first), parsing at most `limit` for title/recap — the codex equivalent of
// scanResumable for the Resume picker.
func scanCodexResumable(limit int) []Resumable {
	dir := codexSessionsDir()
	if dir == "" {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*", "*", "*", "rollout-*.jsonl"))

	type fileInfo struct {
		path string
		mod  time.Time
	}
	files := make([]fileInfo, 0, len(paths))
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			files = append(files, fileInfo{p, st.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })

	out := make([]Resumable, 0, limit)
	for _, f := range files {
		if len(out) >= limit {
			break
		}
		if r, ok := parseCodexRollout(f.path, f.mod); ok {
			out = append(out, r)
		}
	}
	return out
}

func findCodexResumable(id string) (Resumable, bool) {
	for _, r := range scanCodexResumable(500) {
		if r.SessionID == id {
			return r, true
		}
	}
	return Resumable{}, false
}

// parseCodexRollout reads a rollout into a Resumable: id/cwd from session_meta,
// the title from the first real user prompt (skipping codex's injected AGENTS /
// environment blocks), the recap from the last agent message.
func parseCodexRollout(path string, mod time.Time) (Resumable, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Resumable{}, false
	}
	defer f.Close()

	var id, cwd, firstPrompt, lastAgent string
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			var l codexRolloutLine
			if json.Unmarshal([]byte(line), &l) == nil {
				switch l.Type {
				case "session_meta":
					var m codexMetaPayload
					if json.Unmarshal(l.Payload, &m) == nil {
						id, cwd = m.ID, m.Cwd
					}
				case "response_item":
					var p struct {
						Role    string `json:"role"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					}
					if json.Unmarshal(l.Payload, &p) == nil && p.Role == "user" && firstPrompt == "" {
						text := ""
						for _, c := range p.Content {
							text += c.Text
						}
						if t := strings.TrimSpace(text); t != "" && !isInjectedBlock(t) {
							firstPrompt = t
						}
					}
				case "event_msg":
					var p struct {
						Type    string `json:"type"`
						Message string `json:"message"`
					}
					if json.Unmarshal(l.Payload, &p) == nil && p.Type == "agent_message" && strings.TrimSpace(p.Message) != "" {
						lastAgent = p.Message
					}
				}
			}
		}
		if err != nil {
			break
		}
	}

	if id == "" || cwd == "" {
		return Resumable{}, false
	}
	title := truncate(oneLine(firstPrompt), 70)
	if title == "" {
		title = filepath.Base(cwd)
	}
	recap := truncate(oneLine(lastAgent), 80)
	if recap == "" {
		recap = "(no reply yet)"
	}
	return Resumable{
		SessionID: id,
		Cwd:       cwd,
		Display:   shortenHome(cwd),
		Title:     title,
		Recap:     recap,
		Modified:  mod.Unix(),
		Age:       humanizeSince(mod),
	}, true
}

// isInjectedBlock reports whether a user message is codex's auto-injected
// context (AGENTS.md, environment, instruction blocks) rather than a real
// prompt — those make poor session titles.
func isInjectedBlock(text string) bool {
	return strings.HasPrefix(text, "#") || strings.HasPrefix(text, "<")
}

// samePath reports whether two paths point at the same directory, tolerating
// symlink differences (codex may record a canonicalised cwd).
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && ra == rb
}

// codexRolloutMeta reads a rollout's session_meta (first line) → (id, cwd).
func codexRolloutMeta(path string) (id, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	r := bufio.NewReader(f)
	line, _ := r.ReadString('\n')
	var l codexRolloutLine
	if json.Unmarshal([]byte(line), &l) != nil || l.Type != "session_meta" {
		return "", ""
	}
	var m codexMetaPayload
	if json.Unmarshal(l.Payload, &m) != nil {
		return "", ""
	}
	return m.ID, m.Cwd
}

// codexTailer derives codex session state from the rollout JSONL. Codex has no
// hooks, so this is how agorai knows what a codex session is doing:
//
//	state   — StateWorking | StatePerm | StateIdle
//	recap   — last agent message (or the approval question when paused)
//	question— the approval justification when state is StatePerm
//	model   — model id from the latest turn_context
//
// A turn is bounded by task_started…task_complete. An approval pauses the turn:
// the agent emits a function_call needing escalation (sandbox_permissions ==
// "require_escalated") and waits — no function_call_output, no task_complete —
// so a still-pending escalated call means we're waiting for the user.
//
// The tailer reads the rollout **incrementally** (only bytes appended since the
// last poll) and keeps the turn state across reads, so a turn's task_started
// marker is never lost — a heavy turn (big diffs, long tool output) can push it
// far beyond any fixed tail window, which would otherwise read as idle.
type codexTailer struct {
	path         string
	offset       int64
	buf          []byte // partial trailing line carried to the next read
	turnActive   bool
	lastAgent    string
	model        string
	ctxTokens    int // last turn's input (context) tokens, from token_count events
	ctxMax       int // model_context_window reported by codex (exact ceiling)
	// account usage limits from token_count.rate_limits (the data behind `/status`):
	limit5hPct, limitWkPct     int   // used percent of the 5h (primary) and weekly (secondary) windows
	limit5hReset, limitWkReset int64 // unix resets_at for each window (0 = no data)
	pending      map[string]string // escalated calls awaiting approval: call_id → justification
	pendingOrder []string
}

// codexWindow is one usage-limit window from token_count.rate_limits.
type codexWindow struct {
	UsedPercent float64 `json:"used_percent"`
	ResetsAt    int64   `json:"resets_at"`
}

func newCodexTailer(path string) *codexTailer {
	return &codexTailer{path: path, pending: map[string]string{}}
}

// poll reads any bytes appended since the last call and returns the derived state.
func (t *codexTailer) poll() (state, recap, question, model string) {
	if f, err := os.Open(t.path); err == nil {
		if fi, err := f.Stat(); err == nil && fi.Size() < t.offset {
			t.reset() // file rotated/truncated — rebuild from the start
		}
		if _, err := f.Seek(t.offset, io.SeekStart); err == nil {
			if data, err := io.ReadAll(f); err == nil {
				t.offset += int64(len(data))
				t.consume(data)
			}
		}
		f.Close()
	}
	return t.derive()
}

func (t *codexTailer) reset() {
	t.offset, t.buf = 0, nil
	t.turnActive, t.lastAgent = false, ""
	t.ctxTokens, t.ctxMax = 0, 0
	t.limit5hPct, t.limitWkPct, t.limit5hReset, t.limitWkReset = 0, 0, 0, 0
	t.pending, t.pendingOrder = map[string]string{}, nil
}

func (t *codexTailer) consume(data []byte) {
	t.buf = append(t.buf, data...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := t.buf[:i]
		t.buf = t.buf[i+1:]
		t.apply(line)
	}
}

func (t *codexTailer) apply(ln []byte) {
	var l codexRolloutLine
	if json.Unmarshal(ln, &l) != nil {
		return
	}
	switch l.Type {
	case "turn_context":
		var p struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(l.Payload, &p) == nil && p.Model != "" {
			t.model = p.Model
		}
	case "response_item":
		var p struct {
			Type      string `json:"type"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		}
		if json.Unmarshal(l.Payload, &p) != nil {
			return
		}
		switch p.Type {
		case "function_call":
			if just, escalated := codexEscalation(p.Arguments); escalated {
				if _, seen := t.pending[p.CallID]; !seen {
					t.pendingOrder = append(t.pendingOrder, p.CallID)
				}
				t.pending[p.CallID] = just
			}
		case "function_call_output":
			delete(t.pending, p.CallID) // resolved (approved or denied)
		}
	case "event_msg":
		var p struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(l.Payload, &p) != nil {
			return
		}
		switch p.Type {
		case "task_started":
			t.turnActive = true
		case "agent_message":
			if strings.TrimSpace(p.Message) != "" {
				t.lastAgent = p.Message
			}
		case "task_complete":
			t.turnActive = false
			t.pending = map[string]string{}
			t.pendingOrder = nil
		case "token_count":
			// Codex reports the live context size + exact ceiling, and the account
			// usage windows (primary = 5h, secondary = weekly), per turn.
			var tc struct {
				Info struct {
					LastTokenUsage struct {
						InputTokens int `json:"input_tokens"`
					} `json:"last_token_usage"`
					ModelContextWindow int `json:"model_context_window"`
				} `json:"info"`
				RateLimits struct {
					Primary   *codexWindow `json:"primary"`
					Secondary *codexWindow `json:"secondary"`
				} `json:"rate_limits"`
			}
			if json.Unmarshal(l.Payload, &tc) == nil {
				if tc.Info.LastTokenUsage.InputTokens > 0 {
					t.ctxTokens = tc.Info.LastTokenUsage.InputTokens
				}
				if tc.Info.ModelContextWindow > 0 {
					t.ctxMax = tc.Info.ModelContextWindow
				}
				if w := tc.RateLimits.Primary; w != nil {
					t.limit5hPct, t.limit5hReset = int(w.UsedPercent+0.5), w.ResetsAt
				}
				if w := tc.RateLimits.Secondary; w != nil {
					t.limitWkPct, t.limitWkReset = int(w.UsedPercent+0.5), w.ResetsAt
				}
			}
		}
	}
}

func (t *codexTailer) derive() (state, recap, question, model string) {
	recap = truncate(oneLine(t.lastAgent), 90)
	// An unresolved escalated call → waiting on the user.
	for i := len(t.pendingOrder) - 1; i >= 0; i-- {
		if just, ok := t.pending[t.pendingOrder[i]]; ok {
			return StatePerm, recap, truncate(oneLine(just), 200), t.model
		}
	}
	if t.turnActive {
		return StateWorking, recap, "", t.model
	}
	return StateIdle, recap, "", t.model
}

// codexState is a one-shot read (recap/model for LastLine and recap seeding); it
// processes a bounded tail since it doesn't need the precise turn boundary.
func codexState(path string) (state, recap, question, model string) {
	t := newCodexTailer(path)
	for _, ln := range tailLines(path, 256*1024) {
		t.apply([]byte(ln))
	}
	return t.derive()
}

// codexEscalation reports whether an exec_command's JSON arguments request an
// escalation (need approval), and returns the justification shown to the user.
func codexEscalation(arguments string) (justification string, escalated bool) {
	var a struct {
		SandboxPermissions string `json:"sandbox_permissions"`
		Justification      string `json:"justification"`
	}
	if json.Unmarshal([]byte(arguments), &a) != nil {
		return "", false
	}
	if a.SandboxPermissions != "require_escalated" {
		return "", false
	}
	return a.Justification, true
}

// tailLines reads up to the last n bytes of a file and returns its complete
// lines (dropping a partial leading line when truncated).
func tailLines(path string, n int64) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if st.Size() > n {
		start = st.Size() - n
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, _ := io.ReadAll(f)
	lines := strings.Split(string(data), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	return lines
}
