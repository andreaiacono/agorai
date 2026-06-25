package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
			ID: "new-pr", Label: "New PR", Icon: "ticket", Mode: "config",
			Agents: allAgents, ShowModel: true,
			Workspace:   &ButtonWorkspace{Dir: "~/dev/PRs", Trust: true},
			Inputs:      []ButtonInput{{ID: "ticket", Label: "Linear ticket", Placeholder: "e.g. BLUE-900", Required: true, Transform: "blue-prefix"}},
			Prompt:      strings.NewReplacer("$TICKET", "{ticket}", "$DIR", "{workspace}").Replace(ticketPlanPromptTemplate),
			SessionName: "Ticket {ticket}",
		},
		{
			ID: "review", Label: "Review PR", Icon: "review", Mode: "config",
			Agents: allAgents, ShowModel: true, Unattended: true, ExcludeEnv: []string{"DATABASE_URL"},
			Workspace:   &ButtonWorkspace{Scratch: "review"},
			Inputs:      []ButtonInput{{ID: "pr", Label: "GitHub PR", Placeholder: "PR number (e.g. 7098) or URL", Required: true}},
			Prompt:      strings.ReplaceAll(reviewPRPrompt, "$PR", "{pr}"),
			SessionName: "Review {pr}",
		},
		{
			ID: "review-mine", Label: "Review my code", Icon: "review", Mode: "open",
			Agents: allAgents, ShowModel: true, Unattended: true, ExcludeEnv: []string{"DATABASE_URL"},
			Workspace:   &ButtonWorkspace{Pick: true}, // pick the local checkout to review
			Prompt:      reviewMinePrompt,
			SessionName: "Review {dir}",
		},
		{
			ID: "resume", Label: "Resume Session", Icon: "resume", Mode: "resume",
		},
	}
}

func buttonsPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".agorai", "buttons.json")
}

// loadButtons returns the user's ~/.agorai/buttons.json if present and valid,
// otherwise the built-in defaults.
func loadButtons() []Button {
	if p := buttonsPath(); p != "" {
		if b, err := os.ReadFile(p); err == nil {
			var btns []Button
			if json.Unmarshal(b, &btns) == nil && len(btns) > 0 {
				return btns
			}
		}
	}
	return defaultButtons()
}

// saveButtons writes the buttons array to ~/.agorai/buttons.json.
func saveButtons(btns []Button) error {
	p := buttonsPath()
	if p == "" {
		return os.ErrInvalid
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(btns, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

func (s *Server) handleButtons(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, loadButtons())
}

func (s *Server) handlePutButtons(w http.ResponseWriter, r *http.Request) {
	var btns []Button
	if err := json.NewDecoder(r.Body).Decode(&btns); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveButtons(btns); err != nil {
		http.Error(w, "save: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, btns)
}

// handleResetButtons removes the override file, reverting to the built-in defaults.
func (s *Server) handleResetButtons(w http.ResponseWriter, _ *http.Request) {
	if p := buttonsPath(); p != "" {
		_ = os.Remove(p)
	}
	writeJSON(w, defaultButtons())
}

func findButton(id string) *Button {
	for i, b := range loadButtons() {
		if b.ID == id {
			return &loadButtons()[i] // re-load to get an addressable copy
		}
	}
	return nil
}

func (b *Button) variant(id string) *ButtonVariant {
	for i := range b.Variants {
		if b.Variants[i].ID == id {
			return &b.Variants[i]
		}
	}
	if len(b.Variants) > 0 {
		return &b.Variants[0] // default to the first
	}
	return nil
}

// applyTransform applies a named input transform (kept small + explicit).
func applyTransform(name, val string) string {
	switch name {
	case "blue-prefix":
		if val != "" && isAllDigits(val) {
			return "BLUE-" + val
		}
	}
	return val
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// fillTemplate replaces {key} with vals[key].
func fillTemplate(tpl string, vals map[string]string) string {
	for k, v := range vals {
		tpl = strings.ReplaceAll(tpl, "{"+k+"}", v)
	}
	return tpl
}

// resolveWorkspace turns a button's workspace spec into an absolute cwd, creating
// the dir as needed. Returns "" for a pick workspace (handled by the open flow).
func resolveWorkspace(ws *ButtonWorkspace) (string, bool) {
	if ws == nil {
		return "", false
	}
	switch {
	case ws.Scratch != "":
		return appWorkspace(ws.Scratch), true
	case ws.Dir != "":
		dir := expandHome(ws.Dir)
		if os.MkdirAll(dir, 0o755) != nil {
			return "", false
		}
		return dir, true
	}
	return "", false
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
