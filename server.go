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
	"regexp"
	"sort"
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
	mux.HandleFunc("GET /api/buttons", s.handleButtons)
	mux.HandleFunc("PUT /api/buttons", s.handlePutButtons)
	mux.HandleFunc("DELETE /api/buttons", s.handleResetButtons)
	mux.HandleFunc("GET /api/repos", s.handleRepos)
	mux.HandleFunc("GET /api/roots", s.handleRoots)
	mux.HandleFunc("GET /api/browse", s.handleBrowse)
	mux.HandleFunc("GET /api/models", s.handleModels)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/debug/prompts", s.handleDebugPrompts)
	mux.HandleFunc("GET /api/resumable", s.handleResumable)
	mux.HandleFunc("GET /api/sessions", s.handleListSessions)
	mux.HandleFunc("POST /api/sessions", s.handleCreateSession)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.handleRenameSession)
	mux.HandleFunc("POST /api/sessions/{id}/goal", s.handleActivateGoal)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("POST /hook", s.handleHook)
	mux.HandleFunc("POST /api/usage", s.handleUsage)
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
	repos := discoverRepos(s.roots, 3)
	// Offer the home directory as the first choice so a session can be started
	// there directly (it isn't under the configured repo roots otherwise).
	if home := homeDir(); home != "" {
		repos = append([]Repo{{
			Name:    "home",
			Path:    home,
			Display: "~",
			Branch:  gitOut(home, "rev-parse", "--abbrev-ref", "HEAD"),
			Sub:     "start claude in your home directory",
		}}, repos...)
	}
	writeJSON(w, repos)
}

// homeDir is the user's home directory, or "" if it can't be determined.
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, agentFor(AgentKind(r.URL.Query().Get("agent"))).Models())
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

func (s *Server) handleResumable(w http.ResponseWriter, r *http.Request) {
	var list []Resumable
	switch normalizeAgent(AgentKind(r.URL.Query().Get("agent"))) {
	case AgentCodex:
		list = scanCodexResumable(resumableScanLimit)
	case AgentGemini:
		list = nil // gemini resume-discovery not implemented yet
	default:
		list = scanResumable(resumableScanLimit)
	}
	writeJSON(w, s.mgr.applyPersistedNames(list))
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

// handleBrowse lists the sub-directories of a path so the picker can offer a
// navigable folder chooser — for opening a repo that isn't under the configured
// roots (e.g. a one-off PR checkout, each in its own directory). Read-only.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		p = homeDir()
	}
	abs, err := filepath.Abs(expandHome(p))
	if err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	abs = filepath.Clean(abs)
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		http.Error(w, "not a directory", http.StatusBadRequest)
		return
	}

	type dirent struct {
		Name string `json:"name"`
		Path string `json:"path"`
		Repo bool   `json:"repo"`
	}
	showHidden := r.URL.Query().Get("hidden") == "1"
	dirs := []dirent{}
	entries, _ := os.ReadDir(abs)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !showHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		full := filepath.Join(abs, e.Name())
		dirs = append(dirs, dirent{Name: e.Name(), Path: full, Repo: isGitRepo(full)})
	}
	// Case-insensitive sort so the listing isn't split into upper- then lower-case
	// blocks (os.ReadDir returns raw byte order).
	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})

	parent := filepath.Dir(abs)
	if parent == abs {
		parent = "" // already at the filesystem root
	}
	writeJSON(w, map[string]any{
		"path":    abs,
		"display": shortenHome(abs),
		"parent":  parent,
		"isRepo":  isGitRepo(abs),
		"dirs":    dirs,
	})
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
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
		q, ctx, opts := parsePermissionPrompt(raw)
		out = append(out, map[string]any{
			"id":       dto.ID,
			"name":     dto.Name,
			"question": q,
			"context":  ctx,
			"options":  opts,
			"stripped": stripANSI(raw),
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleListSessions(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.mgr.List())
}

// handleActivateGoal submits the session's pending /goal condition now (user
// clicked "Start goal" once the plan/clarification phase was done).
func (s *Server) handleActivateGoal(w http.ResponseWriter, r *http.Request) {
	sess := s.mgr.Get(r.PathValue("id"))
	if sess == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	cond := sess.takePendingGoal()
	if cond == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sess.writeInput([]byte("/goal " + cond + "\r"))
	s.broadcastSessions() // the pending-goal chip clears once the DTO no longer carries it
	w.WriteHeader(http.StatusNoContent)
}

type createReq struct {
	Cwd       string    `json:"cwd"`
	Mode      string    `json:"mode"` // "open" | "worktree" | "resume" | "review" | "newdir" | "scratch" | "ticket"
	Name      string    `json:"name"`
	SessionID string    `json:"sessionId"` // for "resume"
	Fork      bool      `json:"fork"`      // resume: branch a copy (for still-running sessions)
	Model     string    `json:"model"`     // "" | "opus" | "sonnet" | "haiku"
	Ticket    string    `json:"ticket"`    // for "review": the Linear ticket number
	Pr        string    `json:"pr"`        // for "review": a GitHub PR (URL or owner/repo#123), detached from a ticket
	Parent    string    `json:"parent"`    // for "newdir": the parent root dir
	Dir       string    `json:"dir"`       // for "newdir": the new folder name
	GitInit   bool      `json:"gitInit"`   // for "newdir": run `git init`
	Agent     AgentKind `json:"agent"`     // "" | "claude" | "codex" | "gemini"
	// config-driven buttons (mode "config"):
	Button     string            `json:"button"`     // button id from /api/buttons
	Variant    string            `json:"variant"`    // chosen variant id (if the button has variants)
	Inputs     map[string]string `json:"inputs"`     // input id → value
	Prompt     string            `json:"prompt"`     // optional edited prompt (overrides the button's template; placeholders still filled)
	Unattended bool              `json:"unattended"` // launch the agent without permission prompts (checkbox; defaults to the button's setting)
	Goal       string            `json:"goal"`       // claude only: an overarching objective, injected as a persistent system prompt
}

// reviewCommentsSuffix is appended to both review prompts: after the analysis,
// produce a copy/paste-friendly list of short comments to post on the PR.
const reviewCommentsSuffix = " Then, at the very end, add a section titled \"Proposed comments\" — a copy/paste-friendly list of the comments you'd post on the PR. Put each comment on its own line, prefixed with its `file:line`. Keep every comment very short and simple, with no surrounding quotes and no line breaks within a comment."

// readOnlyGuardrail is the shared, session-wide read-only policy appended to the
// New PR and Review PR prompts: read freely, but never take a write/outward
// action without an explicit per-action confirmation.
const readOnlyGuardrail = "Guardrail — read-only by default, applies for the WHOLE session and overrides any later ambiguous request: you may READ from GitHub and Linear (look up the ticket, read PR/issue comments, diffs, CI). You may NOT perform any WRITE/outward action without me first confirming that specific action and waiting for an explicit \"yes.\" Write actions include, non-exhaustively: committing, pushing, creating/updating/deleting branches, opening or editing PRs, posting/editing/resolving PR or issue comments or review replies, requesting reviewers, applying labels, and changing Linear ticket state or posting Linear comments. Confirmation for one action NEVER carries over to another or to future similar actions — ask again each time. If I ask you to \"reply to\", \"address\", \"respond to\", \"resolve\", or \"provide replies/fixes for\" comments, that means: make any code fixes locally (still no commit/push without confirmation) and DRAFT the reply text here for my review — it does NOT authorize posting to GitHub/Linear. Only post after I explicitly say to post. When in doubt, produce it here for me to review rather than acting."

// closingInstructions are shared by the New PR and Review PR prompts: keep Linear
// ticket IDs out of code comments, and archive decisions via /lucid-adr at the end.
const closingInstructions = " Do NOT reference Linear ticket IDs in code comments. Finally, if the `/lucid-adr` command (the lucid-adr skill) is available here, run it to archive the key decisions from this work; if it isn't available, skip this step."

// reviewPRPrompt is the initial prompt for a PR review; {pr} is the PR the user
// entered (a number, or a URL / owner/repo#123). A bare number assumes the
// default repo; the linked Linear ticket is pulled from the PR description for
// extra context. Shares readOnlyGuardrail with the New PR prompt.
const reviewPRPrompt = "Please spawn the reviewers for GitHub PR {pr}. If {pr} is only a number, assume the repository is `light-space/light`. First read the PR description (e.g. `gh pr view {pr} --json body,title,url`) and find the Linear ticket it references; use that ticket's description as additional context for the review. Work from read-only `gh` (`gh pr view`, `gh pr diff {pr}`, `gh api` GET) and don't check out the branch. Produce the review analysis here in this session for me to read. " + readOnlyGuardrail + reviewCommentsSuffix + closingInstructions

// reviewMinePrompt reviews the user's *local* working branch (no PR yet): the
// diff of the current branch against its base branch, computed with local git
// (no PR exists, so no `gh`). Runs in the repo the user picks ({dir} is the
// picked directory's name). Mirrors the New PR flow, which produces that work
// as a local clone + branch. Since there's no PR to read the ticket from, the
// Linear ticket is recovered from the branch name / commit-message prefixes.
const reviewMinePrompt = "Please spawn the reviewers to review my local changes in this repository (`{dir}`). There is no PR yet — review the diff between the current branch and its base/source branch. Determine the base branch (the repository's default branch such as `main`, or the branch this one was created from — `git merge-base` / `git log` can help) and review the committed changes (`git diff <base>...HEAD`) together with any uncommitted working-tree changes (`git status`, `git diff`). For context on what the change should achieve, identify the Linear ticket this work belongs to — derive its ID from the current branch name (e.g. `feature/blue-618-…` → `BLUE-618`) or from the commit-message prefixes (e.g. `fix(BLUE-618): …`); if you find one, read the ticket and treat its description as the intended end state to review the code against. Work only with local read-only `git` commands plus reading that ticket — there is no PR, so don't use `gh`; do NOT checkout other branches, edit files, commit, push, or post anything to GitHub or Linear; NEVER commit or push without asking me to confirm first. Only produce the review analysis here in this session for me to read." + reviewCommentsSuffix

// ticketPlanPromptTemplate is the initial prompt for the "New PR" button: there
// is no PR yet — the agent looks up the ticket, sets up a fresh checkout + branch
// under the PRs workspace, and produces an implementation plan for the new work.
// {ticket} and {workspace} are filled in at spawn time. Shares readOnlyGuardrail
// with the Review PR prompt.
const ticketPlanPromptTemplate = "I want to start working on Linear ticket {ticket}. There is no PR for it yet — this is new work. " +
	"First, look up the ticket to understand its requirements. " +
	"Create a new working directory named {ticket} under {workspace} (i.e. {workspace}/{ticket}), check out the repository it targets there (for Light work that's `light-space/light`; clone it first if needed), and create a new git branch for this ticket off the default branch. " +
	"Then give me a clear, actionable implementation plan for building it: the approach, the files to change, edge cases and tests to consider, and the concrete steps in order. " +
	"Also consider whether the feature should emit usage metrics so we can tell whether it's actually being used, and if so call that out in the plan. " +
	"Treat the ticket description as the source of truth for the desired end state. " +
	"Committing, pushing, and creating the PR each require my explicit confirmation first (as the guardrail below states) — never do them on your own. When you do create the PR, its title MUST include the ticket number {ticket} (e.g. a `feat({ticket}): …` or `fix({ticket}): …` prefix). Only once a PR for this work has been created and pushed (with my confirmation) do you then automatically check its CI pipeline: poll the PR's checks (e.g. `gh pr checks`) until the test/CI checks finish, then report that CI is green, or summarize any failures. Checking CI is read-only and needs no confirmation. Don't consider the task complete until CI is green. " +
	readOnlyGuardrail + closingInstructions

// newPRGoalDefault pre-fills the New PR dialog's Goal field: a /goal completion
// condition that keeps Claude iterating review→fix until the code is clean. A
// turn bound keeps the loop from running forever. Editable per launch.
const newPRGoalDefault = "External reviewers have reviewed the latest code and only minor, low-severity issues remain — every critical, high, and medium severity finding they raised has been fixed and re-verified (or explicitly justified as not applicable), and CI is green; or stop after 20 turns."

// scratchWorkspace is a dedicated dir for free sessions not tied to any repo.
func scratchWorkspace() string { return appWorkspace("scratch") }

// appWorkspace returns ~/.agorai/<name>, creating it if needed — a contained
// home for sessions that don't belong to a repo, so folder-trust is accepted
// once per workspace instead of for the whole home dir.
func appWorkspace(name string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "/tmp"
	}
	dir := filepath.Join(home, ".agorai", name)
	if os.MkdirAll(dir, 0o755) != nil {
		return home
	}
	return dir
}

// prepareAgent does any per-agent setup needed before spawning in cwd: codex
// gets its folder pre-trusted; gemini gets its (Claude-compatible) hooks wired
// so it reports state. Both are idempotent and best-effort.
func prepareAgent(agent AgentKind, cwd string) {
	switch agent {
	case AgentCodex:
		ensureCodexTrust(cwd)
	case AgentGemini:
		ensureGeminiHooks()
	}
}

// startSession spawns a plain interactive session for the chosen agent and
// writes the {id} response. It handles the two id models: claude takes the id we
// assign (--session-id → adopt now); codex mints its own (learn it after spawn).
// Used by the open / newdir / scratch flows; review and ticket build their own
// claude-specific args.
// goalDirective turns a session "goal" into Claude's /goal slash command — a
// completion condition Claude keeps working toward across turns until a fast
// model judges it met (claude only, v2.1.139+). Empty for other agents / no goal.
// goalCondition is the sanitized /goal completion condition (claude only): one
// line, since a stray newline would submit the slash command early. "" when
// there's no goal or the agent isn't claude.
func goalCondition(agent AgentKind, goal string) string {
	goal = strings.TrimSpace(goal)
	if goal == "" || normalizeAgent(agent) != AgentClaude {
		return ""
	}
	return strings.Join(strings.Fields(goal), " ")
}

// goalDirective wraps the condition as the /goal slash command to submit.
func goalDirective(agent AgentKind, goal string) string {
	if c := goalCondition(agent, goal); c != "" {
		return "/goal " + c
	}
	return ""
}

func (s *Server) startSession(w http.ResponseWriter, agent AgentKind, cwd, name, branch, model string, unattended bool, goal string) {
	agent = normalizeAgent(agent)
	a := agentFor(agent)
	prepareAgent(agent, cwd)
	sid := newUUID() // used only when the agent accepts an assigned id (claude)
	// No competing prompt here, so the goal IS the first message: `/goal <cond>`
	// sets the condition and starts the loop immediately.
	args := a.FreshArgs(sid, model, goalDirective(agent, goal))
	if unattended {
		args = append(args, unattendedArgs(agent)...)
	}
	sess, err := s.mgr.SpawnAs(agent, cwd, name, branch, args...)
	if err != nil {
		http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sess.setModel(model)
	if a.AssignsID() {
		s.mgr.adopt(sess, sid)
	} else {
		// Codex assigns its own id and reports state via its rollout file, not
		// hooks. Save a placeholder now so it survives a restart even before its
		// id is known, then supervise it to learn the id + drive the state.
		s.mgr.persistPlaceholder(sess)
		go s.superviseCodex(sess, cwd, time.Now())
	}
	writeJSON(w, map[string]string{"id": sess.ID})
}

// startPrompted spawns the chosen agent with an initial prompt (used by Review
// and New-PR/ticket). unattended runs reviews without approval prompts;
// excludeEnv drops vars (reviews drop DATABASE_URL). Handles both id models.
func (s *Server) startPrompted(w http.ResponseWriter, agent AgentKind, cwd, name, branch, model, prompt, recap string, unattended bool, excludeEnv []string, goal string) {
	agent = normalizeAgent(agent)
	a := agentFor(agent)
	prepareAgent(agent, cwd)
	sid := newUUID()
	args := a.PromptArgs(sid, model, prompt, unattended)
	sess, err := s.mgr.SpawnAsWithoutEnv(agent, cwd, name, branch, excludeEnv, args...)
	if err != nil {
		http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
		return
	}
	sess.setModel(model)
	sess.setRecap(recap)
	// Keep the template prompt as turn 1. The goal becomes a *pending* condition the
	// user activates with a button once the plan looks right — we can't auto-detect
	// when the plan phase ends (a clarifying question fires the same Stop hook as a
	// finished plan), so injecting it automatically would fire mid-conversation.
	if c := goalCondition(agent, goal); c != "" {
		sess.setPendingGoal(c)
	}
	if a.AssignsID() {
		s.mgr.adopt(sess, sid)
	} else {
		s.mgr.persistPlaceholder(sess)
		go s.superviseCodex(sess, cwd, time.Now())
	}
	writeJSON(w, map[string]string{"id": sess.ID})
}

// startPicked spawns a session in a cwd chosen via the repo/dir picker. If the
// originating pick-button carries a prompt/name template, those are applied
// (with {workspace}/{dir} placeholders); otherwise it's a plain session named
// after the dir. This is what makes the picker config-expressible.
func (s *Server) startPicked(w http.ResponseWriter, req createReq, model, cwd, name, branch string) {
	// The checkbox is an opt-in (always starts off); OR it with the button's own
	// setting so reviews still run unattended even with the box unchecked.
	unattended := req.Unattended
	if btn := findButton(req.Button); btn != nil {
		unattended = unattended || btn.Unattended
		vals := map[string]string{"workspace": cwd, "dir": filepath.Base(cwd)}
		if btn.SessionName != "" {
			name = fillTemplate(btn.SessionName, vals)
		}
		if btn.Prompt != "" {
			s.startPrompted(w, req.Agent, cwd, name, branch, model, fillTemplate(btn.Prompt, vals), name+"…", unattended, btn.ExcludeEnv, req.Goal)
			return
		}
	}
	s.startSession(w, req.Agent, cwd, name, branch, model, unattended, req.Goal)
}

// handleConfigLaunch spawns a session from a configurable button (/api/buttons):
// it resolves the active variant's inputs, applies transforms, fills the prompt
// and session-name templates, resolves the workspace, and spawns the chosen
// agent. Replaces the bespoke ticket/review handlers.
func (s *Server) handleConfigLaunch(w http.ResponseWriter, req createReq, model string) {
	btn := findButton(req.Button)
	if btn == nil {
		http.Error(w, "unknown button", http.StatusBadRequest)
		return
	}

	inputs, prompt, sessionName := btn.Inputs, btn.Prompt, btn.SessionName
	if len(btn.Variants) > 0 {
		v := btn.variant(req.Variant)
		if v == nil {
			http.Error(w, "unknown variant", http.StatusBadRequest)
			return
		}
		inputs, prompt, sessionName = v.Inputs, v.Prompt, v.SessionName
	}
	if req.Prompt != "" {
		prompt = req.Prompt // user edited the prompt in the modal; placeholders still get filled
	}

	vals := map[string]string{}
	for _, in := range inputs {
		val := applyTransform(in.Transform, strings.TrimSpace(req.Inputs[in.ID]))
		if in.Required && val == "" {
			http.Error(w, in.ID+" is required", http.StatusBadRequest)
			return
		}
		vals[in.ID] = val
	}

	cwd, ok := resolveWorkspace(btn.Workspace)
	if !ok {
		http.Error(w, "this button needs a fixed or scratch workspace", http.StatusBadRequest)
		return
	}
	vals["workspace"] = cwd

	prompt = fillTemplate(prompt, vals)
	name := fillTemplate(sessionName, vals)
	if name == "" {
		name = btn.Label
	}

	// Opt-in checkbox OR the button's own setting (reviews stay unattended).
	unattended := req.Unattended || btn.Unattended
	if prompt != "" {
		s.startPrompted(w, req.Agent, cwd, name, "", model, prompt, name+"…", unattended, btn.ExcludeEnv, req.Goal)
		return
	}
	s.startSession(w, req.Agent, cwd, name, "", model, unattended, req.Goal)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	model := req.Model
	if !modelAllowedFor(req.Agent, model) {
		model = "" // ignore anything not in the chosen agent's list
	}

	// Config-driven button with its own workspace (New PR / Review / custom):
	// resolve inputs/prompt/workspace from /api/buttons. Pick-workspace buttons
	// (New Session) instead fall through to the picker handlers below, which call
	// startPicked to apply the button's prompt/name to the chosen dir.
	if req.Button != "" {
		if btn := findButton(req.Button); btn != nil && (btn.Workspace == nil || !btn.Workspace.Pick) {
			s.handleConfigLaunch(w, req, model)
			return
		}
	}

	// Resume takes its cwd from our own on-disk scan (trusted), never the client,
	// so it bypasses the under-roots guard but only for a real local transcript.
	if req.Mode == "resume" {
		// Codex resumes by id (no fork) and reports state via its rollout.
		if normalizeAgent(req.Agent) == AgentCodex {
			res, ok := findCodexResumable(req.SessionID)
			if !ok {
				http.Error(w, "unknown session", http.StatusNotFound)
				return
			}
			ensureCodexTrust(res.Cwd)
			branch := gitOut(res.Cwd, "rev-parse", "--abbrev-ref", "HEAD")
			sess, err := s.mgr.SpawnAs(AgentCodex, res.Cwd, res.Title, branch, agentFor(AgentCodex).ResumeArgs(req.SessionID)...)
			if err != nil {
				http.Error(w, "spawn: "+err.Error(), http.StatusInternalServerError)
				return
			}
			sess.setModel(model)
			sess.setRecap(res.Recap)
			s.mgr.adopt(sess, req.SessionID) // the id is known up front for a resume
			go s.superviseCodex(sess, res.Cwd, time.Now())
			writeJSON(w, map[string]string{"id": sess.ID})
			return
		}

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

	// Note: the Review and "New PR" flows are now config-driven buttons handled
	// by handleConfigLaunch above (mode "config").

	// Scratch: a free session not tied to any repo/directory — runs in a
	// dedicated workspace under ~/.agorai so nothing needs to be picked.
	if req.Mode == "scratch" {
		s.startPicked(w, req, model, scratchWorkspace(), "Scratch", "")
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
		s.startPicked(w, req, model, cwd, dir, branch)
		return
	}

	// Browse: an existing directory the user navigated to with the folder chooser.
	// They picked it explicitly in the UI, so it's allowed even outside the
	// configured roots (every PR checkout lives in its own directory) — but it
	// must exist and be a real directory.
	if req.Mode == "browse" {
		cwd := filepath.Clean(expandHome(strings.TrimSpace(req.Cwd)))
		if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
			http.Error(w, "not a directory", http.StatusBadRequest)
			return
		}
		branch := gitOut(cwd, "rev-parse", "--abbrev-ref", "HEAD")
		s.startPicked(w, req, model, cwd, filepath.Base(cwd), branch)
		return
	}

	// The home directory is an explicit, always-offered choice; allow it through
	// even though it isn't under the configured repo roots.
	if filepath.Clean(req.Cwd) != homeDir() && !underRoots(req.Cwd, s.roots) {
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

	s.startPicked(w, req, model, cwd, name, branch)
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

	// A hook whose session_id isn't this session's own claude id comes from a
	// background/subagent process that merely inherited our AGORAI_ID (subagents,
	// or a `claude` spawned inside the session). Such events must not drive — or
	// be reflected in — the main row, or a finishing background job leaves the
	// session stuck on "working".
	if sess.ClaudeID != "" && p.SessionID != "" && p.SessionID != sess.ClaudeID {
		return
	}

	// Background subagents inherit the main process's AGORAI_ID, so their hook
	// events bind to this same session. An event that would dismiss the prompt
	// (a turn finishing, going idle, a new prompt submitted) must not clear an
	// unanswered permission prompt that's still on the terminal — only a real
	// answer (panel button or typing) should. A fresh permission_prompt is
	// exempt: it replaces the prompt on screen and must refresh the options.
	dismissesPrompt := p.HookEventName == "UserPromptSubmit" || p.HookEventName == "Stop" ||
		(p.HookEventName == "Notification" &&
			(p.NotificationType == "idle_prompt" || p.NotificationType == "elicitation_dialog"))
	if dismissesPrompt && s.promptStillOnScreen(sess) {
		return // leave the prompt (and its recap) untouched
	}

	// The recap is the last assistant line of the chat. Fall back to a status
	// label only when the transcript has nothing to show yet. The transcript
	// also reveals the real model id (what "default" resolves to).
	recap := ""
	if p.TranscriptPath != "" {
		var actualModel string
		recap, actualModel = agentFor(sess.agent).LastLine(p.TranscriptPath) // claude/gemini parse their own log
		if actualModel != "" {
			sess.setActualModel(actualModel)
		}
		// Context-window fill for the panel gauge (claude transcripts carry per-turn usage).
		if normalizeAgent(sess.agent) == AgentClaude {
			sess.setContext(claudeContextOf(p.TranscriptPath))
		}
	}

	switch {
	case p.HookEventName == "UserPromptSubmit", p.HookEventName == "BeforeAgent": // gemini: BeforeAgent
		sess.setState(StateWorking, "Working…")
	case p.HookEventName == "AfterAgent": // gemini: turn finished (≈ Stop)
		sess.setState(StateIdle, fallback(recap, "Finished — waiting for next instruction"))
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

// startCodexTailers supervises every restored codex session (called once at
// startup, after RestoreAll). They already have an id, so it tails immediately.
func (s *Server) startCodexTailers() {
	for _, dto := range s.mgr.List() {
		sess := s.mgr.Get(dto.ID)
		if sess != nil && sess.agent == AgentCodex {
			// after=now: a session resumed by id already has it (the learn step is
			// skipped); a pending one must only match a rollout created from here
			// on, not a stale one left in the same dir by an earlier session.
			go s.superviseCodex(sess, sess.workdir(), time.Now())
		}
	}
}

// superviseCodex is the codex equivalent of the claude hook stream: codex has
// no hooks, so one long-lived goroutine (a) learns the session id once codex
// writes its rollout — which it does lazily, often only after the first prompt,
// so we keep trying for the session's whole life rather than giving up — and
// (b) tails that rollout, mapping task/approval events to the panel state.
func (s *Server) superviseCodex(sess *Session, cwd string, after time.Time) {
	if !sess.beginTailing() {
		return // already supervised
	}
	var lastSig string
	var tailer *codexTailer // built once the rollout path is known; keeps turn state across polls
	for {
		if s.mgr.Get(sess.ID) == nil || sess.currentState() == StateDone {
			return
		}

		id := sess.claudeID()
		if id == "" {
			// Codex mints its own id; learn it from the rollout it creates.
			if learned := newestCodexSessionID(cwd, after, s.mgr.hasClaudeID); learned != "" {
				s.mgr.adopt(sess, learned)
				id = learned
				s.broadcastSessions()
			}
		}

		if id != "" {
			if path := codexTranscriptPath(id); path != "" {
				if tailer == nil || tailer.path != path {
					tailer = newCodexTailer(path)
				}
				state, recap, question, model := tailer.poll()
				if model != "" {
					sess.setActualModel(model)
				}
				if tailer.ctxTokens > 0 && tailer.ctxMax > 0 {
					sess.setContext(tailer.ctxTokens, tailer.ctxMax) // exact ceiling from codex
				}
				if tailer.limit5hReset > 0 || tailer.limitWkReset > 0 {
					sess.setLimits(tailer.limit5hPct, tailer.limit5hReset, tailer.limitWkPct, tailer.limitWkReset)
				}

				// On an approval, parse the on-screen prompt so the panel can show
				// real answer buttons (codex's approval is a numbered select, just
				// like claude's); fall back to the rollout justification for text.
				shown := recap
				var opts []PromptOption
				if state == StatePerm {
					q, ctx, o := parsePermissionPrompt(sess.recentBytes(16 * 1024))
					opts = o
					sess.setPrompt(fallback(q, question), ctx, o)
					shown = fallback(question, q)
				} else {
					sess.setPrompt("", "", nil)
				}

				sig := state + "\x00" + shown + "\x00" + strconv.Itoa(len(opts))
				if sig != lastSig {
					sess.setState(state, fallback(shown, codexStatusFor(state)))
					lastSig = sig
					s.broadcastSessions()
				}
			}
		}
		time.Sleep(600 * time.Millisecond)
	}
}

func codexStatusFor(state string) string {
	switch state {
	case StateWorking:
		return "Working…"
	case StatePerm:
		return "Needs your approval — open to respond"
	default:
		return "Finished — waiting for next instruction"
	}
}

// promptStillOnScreen reports whether the session is showing an unanswered
// permission prompt — it's in the perm state and the prompt's options are still
// parseable on the terminal. Used to ignore background-agent hook events that
// would otherwise dismiss a prompt the user hasn't answered yet.
func (s *Server) promptStillOnScreen(sess *Session) bool {
	if sess.currentState() != StatePerm {
		return false
	}
	_, _, opts := parsePermissionPrompt(sess.recentBytes(16 * 1024))
	return len(opts) > 0
}

// parsePromptSoon reads the session's recent output and extracts the prompt's
// real options. The box may not be fully drawn when the hook fires, so it polls
// briefly. Falls through silently if nothing parseable shows up (the UI then
// uses generic Yes/No/Always buttons).
func (s *Server) parsePromptSoon(sess *Session) {
	for i := 0; i < 8; i++ {
		q, ctx, opts := parsePermissionPrompt(sess.recentBytes(16 * 1024))
		if len(opts) > 0 {
			sess.setPrompt(q, ctx, opts)
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
		late, actualModel := lastAssistantInfo(path)
		if actualModel != "" {
			sess.setActualModel(actualModel)
		}
		if late != "" && late != prev {
			sess.setRecap(late)
			sess.setContext(claudeContextOf(path)) // refresh the gauge with the final turn
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
	firstResize := true
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
				if firstResize {
					// This viewer just replayed the ring buffer; force claude to
					// repaint its live region even if the size didn't change.
					firstResize = false
					sess.resizeRepaint(msg.Resize[1], msg.Resize[0])
				} else {
					sess.resize(msg.Resize[1], msg.Resize[0])
				}
			}
			continue
		}
		sess.writeInput(data)
		// Keep the panel state in sync with terminal input that claude won't
		// report via a hook:
		//  - typing into a prompting/waiting session = answering it → working
		//  - a lone Esc into a working session = interrupting the turn → idle
		//    (claude fires no Stop hook on interrupt, so we'd stay stuck "working")
		switch st := sess.currentState(); {
		// A permission menu is answered by ANY keystroke — a number, an arrow, or a
		// bare Enter accepting the highlighted option — so clear it on the first real
		// input. (looksTyped would ignore a bare-Enter accept and leave it lingering.)
		case st == StatePerm && looksAnswered(data):
			sess.setState(StateWorking, "Working…")
			s.broadcastSessions()
		// A free-text prompt needs actual content — a bare Enter submits nothing.
		case st == StateWaiting && looksTyped(data):
			sess.setState(StateWorking, "Working…")
			s.broadcastSessions()
		case st == StateWorking && isEscInterrupt(data):
			sess.setState(StateIdle, "Interrupted — waiting for next instruction")
			s.broadcastSessions()
		}
	}
}

// isEscInterrupt reports whether a PTY input frame is a bare Escape key — what
// claude treats as "interrupt the current turn". A lone 0x1b is the Esc key;
// arrow/function keys send multi-byte sequences that also start with 0x1b, so
// we require the frame to be exactly one ESC byte to avoid false positives.
func isEscInterrupt(data []byte) bool {
	return len(data) == 1 && data[0] == 0x1b
}

// termReportRe matches the report sequences a terminal sends in reply to the
// app's queries — cursor-position (…R), device-attributes (…c), device-status
// (…n), and focus in/out (…I / …O). When agorai forces a repaint on attach,
// xterm answers Claude's queries through the same input channel; those replies
// must NOT be mistaken for the user typing.
var termReportRe = regexp.MustCompile("\x1b\\[[0-9;?>=]*[RcnIO]")

// looksTyped reports whether a PTY input frame contains a genuine *content*
// keystroke — something left once terminal report sequences are stripped, and
// not just a bare Enter/newline. A lone Enter submits nothing on an empty prompt,
// so it must not flip a waiting session to "working"; real input already flips on
// its first content character (the Enter only ends it).
func looksTyped(data []byte) bool {
	for _, b := range termReportRe.ReplaceAll(data, nil) {
		if b != '\r' && b != '\n' {
			return true
		}
	}
	return false
}

// looksAnswered reports whether a PTY input frame is a genuine user keystroke —
// anything left once terminal report replies are stripped, INCLUDING a bare
// Enter. Used for permission menus, where Enter accepts the highlighted option
// (so unlike a free-text prompt, a lone Enter does answer it).
func looksAnswered(data []byte) bool {
	return len(termReportRe.ReplaceAll(data, nil)) > 0
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
