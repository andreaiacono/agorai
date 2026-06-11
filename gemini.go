package main

// geminiAgent is the Google Gemini CLI backend. Like claude it assigns the
// session id at spawn (--session-id) and reports state through hooks — and its
// hooks are Claude-compatible, so it reuses the same /hook forwarder. Recaps
// aren't parsed yet (its chat log is a sparse incremental format), so the panel
// falls back to status labels.
type geminiAgent struct{}

func (geminiAgent) Kind() AgentKind { return AgentGemini }
func (geminiAgent) Command() string { return "gemini" }
func (geminiAgent) AssignsID() bool { return true }

func (geminiAgent) ModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", model}
}

// FreshArgs: assign our id, skip the folder-trust prompt, submit a positional
// prompt (interactive when none).
func (geminiAgent) FreshArgs(sid, model, prompt string) []string {
	args := []string{"--session-id", sid, "--skip-trust"}
	args = append(args, geminiAgent{}.ModelArgs(model)...)
	if prompt != "" {
		args = append(args, prompt)
	}
	return args
}

// PromptArgs: unattended reviews auto-approve via --yolo (read-only rests on the
// prompt, like the other agents).
func (geminiAgent) PromptArgs(sid, model, prompt string, unattended bool) []string {
	args := []string{"--session-id", sid, "--skip-trust"}
	if unattended {
		args = append(args, "--yolo")
	}
	args = append(args, geminiAgent{}.ModelArgs(model)...)
	return append(args, prompt)
}

// ResumeArgs re-opens a session by re-supplying its id.
func (geminiAgent) ResumeArgs(id string) []string {
	return []string{"--session-id", id, "--skip-trust"}
}

func (geminiAgent) Models() []ModelOption { return geminiModels }

func (geminiAgent) ModelLabel(id string) string {
	if id == "" {
		return "default"
	}
	return id
}

func (geminiAgent) PrettyModelID(id string) string { return id }

// Recap from gemini's chat log isn't parsed yet (sparse format); the panel uses
// status labels driven by hooks.
func (geminiAgent) TranscriptPath(string) string     { return "" }
func (geminiAgent) LastLine(string) (string, string) { return "", "" }

var geminiModels = []ModelOption{
	{ID: "", Label: "Default"},
	{ID: "gemini-2.5-pro", Label: "2.5 Pro"},
	{ID: "gemini-2.5-flash", Label: "2.5 Flash"},
}
