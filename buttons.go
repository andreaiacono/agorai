package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// Button is a configurable session-launch button. The schema is richer than
// what the backend consumes today (inputs/variants/prompt/workspace) — those
// drive the generic create flow being migrated to; for now the frontend uses
// label/icon/mode to render the top bar, and the existing per-mode handlers
// still do the work.
type Button struct {
	ID          string           `json:"id"`
	Label       string           `json:"label"`
	Icon        string           `json:"icon,omitempty"`
	Mode        string           `json:"mode,omitempty"` // legacy modal: open|ticket|review|resume
	Agents      []string         `json:"agents,omitempty"`
	ShowModel   bool             `json:"showModel,omitempty"`
	Workspace   *ButtonWorkspace `json:"workspace,omitempty"`
	Inputs      []ButtonInput    `json:"inputs,omitempty"`
	Variants    []ButtonVariant  `json:"variants,omitempty"`
	Prompt      string           `json:"prompt,omitempty"`
	SessionName string           `json:"sessionName,omitempty"`
	Unattended  bool             `json:"unattended,omitempty"`
	ExcludeEnv  []string         `json:"excludeEnv,omitempty"`
}

type ButtonInput struct {
	ID          string `json:"id"`
	Label       string `json:"label,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Transform   string `json:"transform,omitempty"` // e.g. "blue-prefix"
}

type ButtonVariant struct {
	ID          string        `json:"id"`
	Label       string        `json:"label"`
	Inputs      []ButtonInput `json:"inputs,omitempty"`
	Prompt      string        `json:"prompt,omitempty"`
	SessionName string        `json:"sessionName,omitempty"`
}

type ButtonWorkspace struct {
	Pick    bool   `json:"pick,omitempty"`    // show the repo/home/new-dir/scratch picker
	Dir     string `json:"dir,omitempty"`     // fixed path
	Scratch string `json:"scratch,omitempty"` // dedicated ~/.agorai/<name>
	Trust   bool   `json:"trust,omitempty"`
}

var allAgents = []string{"claude", "codex", "gemini"}

// defaultButtons expresses today's hardcoded buttons in the config schema.
func defaultButtons() []Button {
	return []Button{
		{
			ID: "new", Label: "New Session", Icon: "plus", Mode: "open",
			Agents: allAgents, ShowModel: true,
			Workspace: &ButtonWorkspace{Pick: true},
		},
		{
			ID: "new-pr", Label: "New PR", Icon: "ticket", Mode: "ticket",
			Agents: allAgents, ShowModel: true,
			Workspace: &ButtonWorkspace{Dir: "~/dev/PRs", Trust: true},
			Inputs:    []ButtonInput{{ID: "ticket", Label: "Linear ticket", Placeholder: "e.g. BLUE-900", Required: true, Transform: "blue-prefix"}},
		},
		{
			ID: "review", Label: "Review PR", Icon: "review", Mode: "review",
			Agents: allAgents, ShowModel: true, Unattended: true, ExcludeEnv: []string{"DATABASE_URL"},
			Workspace: &ButtonWorkspace{Scratch: "review"},
			Variants: []ButtonVariant{
				{ID: "ticket", Label: "Linear ticket", Inputs: []ButtonInput{{ID: "ticket", Placeholder: "e.g. BLUE-900", Transform: "blue-prefix"}}},
				{ID: "pr", Label: "GitHub PR", Inputs: []ButtonInput{{ID: "pr", Placeholder: "PR URL or owner/repo#123"}}},
			},
		},
		{
			ID: "resume", Label: "Resume Session", Icon: "resume", Mode: "resume",
		},
	}
}

// loadButtons returns the user's ~/.agorai/buttons.json if present and valid,
// otherwise the built-in defaults.
func loadButtons() []Button {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if b, err := os.ReadFile(filepath.Join(home, ".agorai", "buttons.json")); err == nil {
			var btns []Button
			if json.Unmarshal(b, &btns) == nil && len(btns) > 0 {
				return btns
			}
		}
	}
	return defaultButtons()
}

func (s *Server) handleButtons(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, loadButtons())
}
