package main

// AgentKind identifies which coding-agent CLI backs a session. Today only
// claude is wired end to end; codex arrives in a later phase. The constant is
// defined now so the persistence/DTO plumbing is ready for it.
type AgentKind string

const (
	AgentClaude AgentKind = "claude"
	AgentCodex  AgentKind = "codex"
	AgentGemini AgentKind = "gemini"
)

// normalizeAgent maps an empty or unknown kind to the default (claude), so
// sessions persisted before the field existed keep working.
func normalizeAgent(k AgentKind) AgentKind {
	switch k {
	case AgentCodex, AgentGemini:
		return k
	default:
		return AgentClaude
	}
}

// Agent encapsulates the CLI-specific behaviour that differs between coding
// agents (spawn command, model flags, resume, transcript reading). Isolating it
// behind this interface keeps the rest of agorai agent-agnostic. Phase 1 ships
// only claudeAgent; codexAgent will implement the same interface.
type Agent interface {
	Kind() AgentKind
	// Command is the executable to launch (e.g. "claude").
	Command() string
	// AssignsID reports whether we can choose the session id at spawn time
	// (claude --session-id). If false, agorai learns the id after spawn.
	AssignsID() bool
	// FreshArgs builds the CLI args for a new session. sid is agorai's chosen id
	// (ignored when AssignsID is false); prompt is an optional first message.
	FreshArgs(sid, model, prompt string) []string
	// PromptArgs builds args for a session that submits an initial prompt.
	// unattended = run without asking for approval (used by read-only reviews,
	// where the read-only guarantee rests on the prompt itself).
	PromptArgs(sid, model, prompt string, unattended bool) []string
	// ModelArgs renders the model selection as CLI args ("" = the user default).
	ModelArgs(model string) []string
	// ResumeArgs builds the args to resume a recorded session in place.
	ResumeArgs(sessionID string) []string
	// Models lists the selectable models for this agent.
	Models() []ModelOption
	// ModelLabel renders a configured model id for display.
	ModelLabel(id string) string
	// PrettyModelID resolves a raw transcript model id to a display label.
	PrettyModelID(id string) string
	// TranscriptPath locates a session's transcript by id ("" if not found).
	TranscriptPath(sessionID string) string
	// LastLine returns the last assistant line and the raw model id seen in a
	// transcript (both "" if unavailable).
	LastLine(path string) (recap, model string)
}

// unattendedArgs returns the flag that makes an agent run without asking for
// permission/approval — claude --dangerously-skip-permissions, codex
// --dangerously-bypass-approvals-and-sandbox, gemini --yolo.
func unattendedArgs(agent AgentKind) []string {
	switch normalizeAgent(agent) {
	case AgentCodex:
		return []string{"--dangerously-bypass-approvals-and-sandbox"}
	case AgentGemini:
		return []string{"--yolo"}
	default:
		return []string{"--dangerously-skip-permissions"}
	}
}

// agentFor returns the Agent implementation for a kind. Unknown/empty kinds
// resolve to claude. Codex is not implemented yet, so it also falls back for
// now — wiring it up is the next phase.
func agentFor(kind AgentKind) Agent {
	switch normalizeAgent(kind) {
	case AgentCodex:
		return codexAgent{}
	case AgentGemini:
		return geminiAgent{}
	default:
		return claudeAgent{}
	}
}

// claudeAgent is the Claude Code backend: it delegates to the existing
// claude-specific helpers so behaviour is unchanged.
type claudeAgent struct{}

func (claudeAgent) Kind() AgentKind                 { return AgentClaude }
func (claudeAgent) Command() string                 { return "claude" }
func (claudeAgent) AssignsID() bool                 { return true }
func (claudeAgent) ModelArgs(model string) []string { return modelArgs(model) }

// FreshArgs: claude takes its id up front and submits a positional prompt.
func (claudeAgent) FreshArgs(sid, model, prompt string) []string {
	args := append([]string{"--session-id", sid}, modelArgs(model)...)
	if prompt != "" {
		args = append(args, prompt)
	}
	return args
}

// PromptArgs: unattended reviews skip the permission prompts entirely.
func (claudeAgent) PromptArgs(sid, model, prompt string, unattended bool) []string {
	args := []string{"--session-id", sid}
	if unattended {
		args = append(args, unattendedArgs(AgentClaude)...)
	}
	args = append(args, modelArgs(model)...)
	return append(args, prompt)
}

func (claudeAgent) ResumeArgs(id string) []string      { return []string{"--resume", id} }
func (claudeAgent) Models() []ModelOption              { return models }
func (claudeAgent) ModelLabel(id string) string        { return modelLabel(id) }
func (claudeAgent) PrettyModelID(id string) string     { return prettyModelID(id) }
func (claudeAgent) TranscriptPath(id string) string    { return transcriptPathFor(id) }
func (claudeAgent) LastLine(p string) (string, string) { return lastAssistantInfo(p) }
