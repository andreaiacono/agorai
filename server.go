package main

import (
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed web
var webFS embed.FS

type Server struct {
	mgr   *Manager
	hub   *Hub
	roots []string
	cfg   *ConfigStore
	up    websocket.Upgrader
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/repos", s.handleRepos)
	mux.HandleFunc("GET /api/roots", s.handleRoots)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/debug/prompts", s.handleDebugPrompts)
	mux.HandleFunc("GET /api/resumable", s.handleResumable)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.handleRenameSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /hook", s.handleHook)
	mux.HandleFunc("GET /ws/control", s.handleControlWS)
	mux.HandleFunc("GET /ws/pty/{id}", s.handlePtyWS)

	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// Local single-user tool: same-origin only, so allow the upgrade.
	s.up.CheckOrigin = func(*http.Request) bool { return true }
	return mux
}

// ---- REST ----

func (s *Server) handleRepos(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, discoverRepos(s.roots, 3))
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, models)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.cfg.Get())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var c Config
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.cfg.Set(c) // sanitized inside (scrollback clamped, invalid env keys dropped)
	writeJSON(w, s.cfg.Get())
}

func (s *Server) handleResumable(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, scanResumable(50))
}

// handleRoots lists the configured roots — used as parent-folder options when
// creating a session in a brand-new directory.
func (s *Server) handleRoots(w http.ResponseWriter, _ *http.Request) {
	type root struct {
		Path    string `json:"path"`
		Display string `json:"display"`
	}
	out := make([]root, 0, len(s.roots))
	for _, r := range s.roots {
		out = append(out, root{Path: r, Display: shortenHome(r)})
	}
	writeJSON(w, out)
}

// handleDebugPrompts dumps, for every session currently asking permission, what
// the prompt parser sees — the parsed options plus the ANSI-stripped recent
// output — so a mis-parse can be diagnosed without running the server here.
func (s *Server) handleDebugPrompts(w http.ResponseWriter, _ *http.Request) {
	out := []map[string]any{}
	for _, dto := range s.mgr.List() {
		if dto.State != StatePerm {
			continue
		}
		sess := s.mgr.Get(dto.ID)
		if sess == nil {
			continue
		}
		raw := sess.recentBytes(16 * 1024)
		q, opts := parsePermissionPrompt(raw)
		out = append(out, map[string]any{
			"id":       dto.ID,
			"name":     dto.Name,
			"question": q,
			"options":  opts,
			"stripped": stripANSI(raw),
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.mgr.List())
}

type createReq struct {
	Cwd       string `json:"cwd"`
	Mode      string `json:"mode"` // "open" | "worktree" | "resume" | "review"
	Name      string `json:"name"`
	SessionID string `json:"sessionId"` // for "resume"
	Fork      bool   `json:"fork"`      // resume: branch a copy (for still-running sessions)
	Model     string `json:"model"`     // "" | "opus" | "sonnet" | "haiku"
	Ticket    string `json:"ticket"`    // for "review": the Linear ticket number
	Parent    string `json:"parent"`    // for "newdir": the parent root dir
	Dir       string `json:"dir"`       // for "newdir": the new folder name
	GitInit   bool   `json:"gitInit"`   // for "newdir": run `git init`
}

// reviewPromptTemplate is the initial prompt for a review session; $TICKET is
// replaced with the user-supplied ticket and passed to claude as its first message.
const reviewPromptTemplate = "Please spawn the reviewers for the PR contained in the linear ticket $TICKET, using the ticket description as a base for analysing the PR content. The repository to review is the one the PR belongs to — determine it from the ticket/PR, don't assume any local directory. Please don't checkout the branch, just work with `gh`."

// reviewWorkspace is a dedicated dir for review sessions (gh-only, repo-agnostic).
func reviewWorkspace() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/tmp"
	}
	dir := filepath.Join(home, ".agorai", "review")
	if os.MkdirAll(dir, 0o755) != nil {
		return home
	}
	return dir
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	model := req.Model
	if !modelAllowed(model) {
		model = "" // ignore anything not in our list (it becomes a CLI arg)
	}

	// Resume takes its cwd from our own on-disk scan (trusted), never the client,
	// so it bypasses the under-roots guard but only for a real local transcript.
	if req.Mode == "resume" {
		res, ok := findResumable(req.SessionID)
		if !ok {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
		branch := gitOut(res.Cwd, "rev-parse", "--abbrev-ref", "HEAD")

		// The id we persist (and later --resume) depends on whether we fork:
		//  - continue in place → the session keeps its id (req.SessionID)
		//  - fork a copy → we mint a new id and force it with --session-id
		resumeID := req.SessionID
		args := []string{"--resume", req.SessionID}
		if req.Fork {
			resumeID = newUUID()
			args = append(args, "--fork-session", "--session-id", resumeID)
		}
		args = append(args, modelArgs(model)...)
		// Name resumed sessions by their first prompt (res.Title), not the repo
		// folder — otherwise two sessions resumed from the same repo collide.
		sess, err := s.mgr.Spawn(res.Cwd, res.Title, branch, args...)
		if err != nil {
			http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
			return
		}
		sess.setModel(model)
		sess.setRecap(res.Recap) // show the last chat line right away, not "Starting…"
		s.mgr.adopt(sess, resumeID)
		writeJSON(w, map[string]string{"id": sess.ID})
		return
	}

	// Review: spawn a session in the chosen repo with a templated initial prompt
	// (passed to claude as its first message via the positional prompt arg).
	if req.Mode == "review" {
		ticket := strings.TrimSpace(req.Ticket)
		if ticket == "" {
			http.Error(w, "ticket required", http.StatusBadRequest)
			return
		}
		// No repo to choose — the review works via `gh` and finds the PR's repo
		// from the ticket. Spawn in a dedicated workspace (not the whole home dir)
		// so its folder-trust stays contained and is accepted just once.
		cwd := reviewWorkspace()
		prompt := strings.ReplaceAll(reviewPromptTemplate, "$TICKET", ticket)

		sid := newUUID()
		args := append([]string{"--session-id", sid}, modelArgs(model)...)
		args = append(args, prompt) // positional → claude submits it as the first message

		sess, err := s.mgr.Spawn(cwd, "Review "+ticket, "", args...)
		if err != nil {
			http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
			return
		}
		sess.setModel(model)
		sess.setRecap("Reviewing " + ticket + "…")
		s.mgr.adopt(sess, sid)
		writeJSON(w, map[string]string{"id": sess.ID})
		return
	}

	// New directory: create a fresh folder under a root, optionally `git init`,
	// and run the session there.
	if req.Mode == "newdir" {
		dir := strings.TrimSpace(req.Dir)
		if dir == "" || strings.ContainsAny(dir, `/\`) || dir == "." || dir == ".." {
			http.Error(w, "invalid directory name", http.StatusBadRequest)
			return
		}
		if !underRoots(req.Parent, s.roots) {
			http.Error(w, "parent not under an allowed root", http.StatusForbidden)
			return
		}
		cwd := filepath.Join(filepath.Clean(req.Parent), dir)
		if !underRoots(cwd, s.roots) {
			http.Error(w, "invalid path", http.StatusForbidden)
			return
		}
		if _, err := os.Stat(cwd); err == nil {
			http.Error(w, "that directory already exists", http.StatusConflict)
			return
		}
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			http.Error(w, "mkdir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if req.GitInit {
			_ = exec.Command("git", "-C", cwd, "init", "-b", "main").Run()
		}
		branch := gitOut(cwd, "rev-parse", "--abbrev-ref", "HEAD")
		sid := newUUID()
		args := append([]string{"--session-id", sid}, modelArgs(model)...)
		sess, err := s.mgr.Spawn(cwd, dir, branch, args...)
		if err != nil {
			http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
			return
		}
		sess.setModel(model)
		s.mgr.adopt(sess, sid)
		writeJSON(w, map[string]string{"id": sess.ID})
		return
	}

	if !underRoots(req.Cwd, s.roots) {
		http.Error(w, "cwd not under an allowed root", http.StatusForbidden)
		return
	}

	cwd := filepath.Clean(req.Cwd)
	name := req.Name
	if name == "" {
		name = filepath.Base(cwd)
	}
	branch := gitOut(cwd, "rev-parse", "--abbrev-ref", "HEAD")

	if req.Mode == "worktree" {
		wtPath, wtBranch, err := addWorktree(cwd, name)
		if err != nil {
			http.Error(w, "worktree: "+err.Error(), http.StatusInternalServerError)
			return
		}
		cwd, branch, name = wtPath, wtBranch, name+" · worktree"
	}

	// Assign the session id ourselves so we can persist it now (and --resume it
	// later) without depending on a hook to tell us what id claude chose.
	sid := newUUID()
	args := append([]string{"--session-id", sid}, modelArgs(model)...)
	sess, err := s.mgr.Spawn(cwd, name, branch, args...)
	if err != nil {
		http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sess.setModel(model)
	s.mgr.adopt(sess, sid)
	writeJSON(w, map[string]string{"id": sess.ID})
}

// addWorktree creates a fresh worktree + branch beside the repo so parallel
// sessions in the same repo don't fight over the working tree.
func addWorktree(repo, name string) (path, branch string, err error) {
	suffix := newID()[:6]
	branch = "cc/" + name + "-" + suffix
	path = filepath.Join(filepath.Dir(repo), "."+name+"-agorai-"+suffix)

	cmd := exec.Command("git", "-C", repo, "worktree", "add", path, "-b", branch)
	if out, e := cmd.CombinedOutput(); e != nil {
		return "", "", &cmdError{string(out), e}
	}
	return path, branch, nil
}

type cmdError struct {
	out string
	err error
}

func (e *cmdError) Error() string {
	if e.out != "" {
		return strings.TrimSpace(e.out)
	}
	return e.err.Error()
}

// ---- hook intake ----

type hookPayload struct {
	HookEventName    string `json:"hook_event_name"`
	NotificationType string `json:"notification_type"`
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
}

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	launchID := r.Header.Get("X-Agorai-Id")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	w.WriteHeader(http.StatusNoContent) // ack fast; the hook doesn't wait on us

	var p hookPayload
	if json.Unmarshal(body, &p) != nil {
		return
	}

	// Bind claude's session_id to the session we launched, then update state.
	sess := s.mgr.bind(launchID, p.SessionID)
	if sess == nil {
		return // a session started outside agorai, or unknown id
	}

	// The recap is the last assistant line of the chat. Fall back to a status
	// label only when the transcript has nothing to show yet.
	recap := ""
	if p.TranscriptPath != "" {
		recap = lastAssistantLine(p.TranscriptPath)
	}

	switch {
	case p.HookEventName == "UserPromptSubmit":
		sess.setState(StateWorking, "Working…")
	case p.HookEventName == "Notification" && p.NotificationType == "permission_prompt":
		sess.setState(StatePerm, fallback(recap, "Wants permission to run a command"))
		go s.parsePromptSoon(sess) // extract the actual options from the screen
	case p.HookEventName == "Notification" && p.NotificationType == "idle_prompt":
		sess.setState(StateWaiting, fallback(recap, "Waiting for your input"))
	case p.HookEventName == "Notification" && p.NotificationType == "elicitation_dialog":
		// A multi-question / free-text dialog (e.g. AskUserQuestion). We can't
		// render it as buttons, so just flag it for attention — answer in the
		// terminal.
		sess.setState(StateWaiting, fallback(recap, "Has a question for you — open to respond"))
	case p.HookEventName == "Stop":
		sess.setState(StateIdle, fallback(recap, "Finished — waiting for next instruction"))
		// Claude often hasn't flushed the final assistant message to the
		// transcript by the time Stop fires, so `recap` here can be one turn
		// stale. Re-read shortly after to catch the just-written line.
		if p.TranscriptPath != "" {
			go s.refreshRecapSoon(sess, p.TranscriptPath, recap)
		}
	case p.HookEventName == "SessionEnd":
		// The user ended it on purpose — mark done and stop resurrecting it.
		sess.setState(StateDone, fallback(recap, "Session ended"))
		s.mgr.forget(sess.ClaudeID)
	case p.HookEventName == "SessionStart":
		sess.setRecap(recap) // resumed sessions have history — show it instead of "Starting…"
	default:
		sess.setRecap(recap) // unknown event: refresh recap, leave state alone
	}
	s.broadcastSessions()
}

// parsePromptSoon reads the session's recent output and extracts the prompt's
// real options. The box may not be fully drawn when the hook fires, so it polls
// briefly. Falls through silently if nothing parseable shows up (the UI then
// uses generic Yes/No/Always buttons).
func (s *Server) parsePromptSoon(sess *Session) {
	for i := 0; i < 8; i++ {
		q, opts := parsePermissionPrompt(sess.recentBytes(16 * 1024))
		if len(opts) > 0 {
			sess.setPrompt(q, opts)
			s.broadcastSessions()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// refreshRecapSoon polls the transcript briefly after a Stop and updates the
// recap once the final assistant line has been flushed (i.e. differs from what
// we read at Stop time).
func (s *Server) refreshRecapSoon(sess *Session, path, prev string) {
	for i := 0; i < 6; i++ {
		time.Sleep(300 * time.Millisecond)
		if late := lastAssistantLine(path); late != "" && late != prev {
			sess.setRecap(late)
			s.broadcastSessions()
			return
		}
	}
}

func fallback(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

func (s *Server) handleRenameSession(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	s.mgr.Rename(r.PathValue("id"), strings.TrimSpace(body.Name))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	s.mgr.Remove(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// ---- terminal WebSocket (per session, raw bytes) ----

func (s *Server) handlePtyWS(w http.ResponseWriter, r *http.Request) {
	sess := s.mgr.Get(r.PathValue("id"))
	if sess == nil {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ch := sess.addClient()
	defer sess.removeClient(ch)

	// single writer goroutine: PTY output -> browser
	go func() {
		for data := range ch {
			if conn.WriteMessage(websocket.BinaryMessage, data) != nil {
				return
			}
		}
	}()

	// reader loop: browser -> PTY. Keystrokes arrive as binary; text frames are
	// control messages (resize).
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.TextMessage {
			var msg struct {
				Resize []uint16 `json:"resize"` // [cols, rows]
			}
			if json.Unmarshal(data, &msg) == nil && len(msg.Resize) == 2 {
				sess.resize(msg.Resize[1], msg.Resize[0])
			}
			continue
		}
		sess.writeInput(data)
		// Typing into a prompting/waiting session is the user answering it in the
		// terminal — clear the stale state so the panel updates.
		if st := sess.currentState(); st == StatePerm || st == StateWaiting {
			sess.setState(StateWorking, "Working…")
			s.broadcastSessions()
		}
	}
}

// ---- control WebSocket (shared: state out, commands in) ----

// legacyChoiceNum maps the old yes/no/always choices to option numbers, kept as
// a fallback. Answers now carry the parsed option number directly; the keystroke
// sent to claude's (numbered-select) prompt is "<n>\r".
func legacyChoiceNum(choice string) int {
	switch choice {
	case "yes":
		return 1
	case "always":
		return 2
	case "no":
		return 3
	}
	return 0
}

func (s *Server) handleControlWS(w http.ResponseWriter, r *http.Request) {
	conn, err := s.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	client := s.hub.add()
	defer s.hub.remove(client)

	// single writer goroutine for this connection
	go func() {
		for b := range client.send {
			if conn.WriteMessage(websocket.TextMessage, b) != nil {
				return
			}
		}
	}()

	// initial snapshot
	client.send <- mustJSON(map[string]any{"type": "sessions", "sessions": s.mgr.List()})

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			Type    string `json:"type"`
			Session string `json:"session"`
			Option  int    `json:"option"` // the prompt option number to select
			Choice  string `json:"choice"` // legacy fallback (yes/no/always)
		}
		if json.Unmarshal(data, &msg) != nil || msg.Type != "answer" {
			continue
		}
		n := msg.Option
		if n == 0 {
			n = legacyChoiceNum(msg.Choice)
		}
		if n >= 1 && n <= 9 {
			if sess := s.mgr.Get(msg.Session); sess != nil {
				sess.writeInput([]byte(strconv.Itoa(n) + "\r"))
				sess.setState(StateWorking, "Working…")
				s.broadcastSessions()
			}
		}
	}
}

func (s *Server) broadcastSessions() {
	s.hub.broadcast(mustJSON(map[string]any{"type": "sessions", "sessions": s.mgr.List()}))
}

// ---- control hub ----

type controlClient struct {
	send chan []byte
}

type Hub struct {
	mu      sync.Mutex
	clients map[*controlClient]bool
}

func newHub() *Hub { return &Hub{clients: map[*controlClient]bool{}} }

func (h *Hub) add() *controlClient {
	c := &controlClient{send: make(chan []byte, 16)}
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
	return c
}

func (h *Hub) remove(c *controlClient) {
	h.mu.Lock()
	if h.clients[c] {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

func (h *Hub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- b:
		default: // slow client: drop this update, it'll get the next snapshot
		}
	}
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("json marshal: %v", err)
		return []byte("{}")
	}
	return b
}
