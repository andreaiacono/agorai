package main

import (
	"regexp"
	"strconv"
	"strings"
)

// ModelOption is a selectable cloud model family. ID is passed to
// `claude --model` ("" means the user's configured default). The family
// aliases (opus/sonnet/haiku) track the latest version; Versions lists
// specific pinned versions the user can pick instead.
type ModelOption struct {
	ID       string         `json:"id"`
	Label    string         `json:"label"`
	Versions []ModelVersion `json:"versions,omitempty"`
}

// ModelVersion is a specific pinned version of a model family. ID is the
// dateless model id the API/CLI accept (e.g. claude-opus-4-8). Edit this
// list as Anthropic ships new versions — it's the single source of truth.
type ModelVersion struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

var models = []ModelOption{
	{ID: "", Label: "Default"},
	{ID: "claude-fable-5", Label: "Fable 5"}, // Anthropic's latest; pinned id (no alias yet)
	{ID: "opus", Label: "Opus", Versions: []ModelVersion{
		{ID: "claude-opus-4-8", Label: "4.8"},
		{ID: "claude-opus-4-7", Label: "4.7"},
		{ID: "claude-opus-4-6", Label: "4.6"},
		{ID: "claude-opus-4-5", Label: "4.5"},
		{ID: "claude-opus-4-1", Label: "4.1"},
	}},
	{ID: "sonnet", Label: "Sonnet", Versions: []ModelVersion{
		{ID: "claude-sonnet-4-6", Label: "4.6"},
		{ID: "claude-sonnet-4-5", Label: "4.5"},
		{ID: "claude-sonnet-4-0", Label: "4.0"},
	}},
	{ID: "haiku", Label: "Haiku", Versions: []ModelVersion{
		{ID: "claude-haiku-4-5", Label: "4.5"},
		{ID: "claude-3-5-haiku-latest", Label: "3.5"},
	}},
}

// modelAllowed guards against arbitrary strings reaching the claude CLI args.
func modelAllowed(id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
		for _, v := range m.Versions {
			if v.ID == id {
				return true
			}
		}
	}
	return false
}

// modelAllowedFor validates a model id against the chosen agent's list (claude
// also accepts pinned versions).
func modelAllowedFor(agent AgentKind, id string) bool {
	if normalizeAgent(agent) == AgentClaude {
		return modelAllowed(id)
	}
	for _, m := range agentFor(agent).Models() {
		if m.ID == id {
			return true
		}
	}
	return false
}

func modelArgs(id string) []string {
	if id == "" {
		return nil
	}
	return []string{"--model", id}
}

var modelDateRe = regexp.MustCompile(`-(\d{8}|latest)$`)

// prettyModelID turns a raw model id from a transcript (e.g. "claude-opus-4-8"
// or "claude-3-5-haiku-20241022") into a display label like "Opus 4.8". This is
// how the UI can show the real model behind "default". Falls back to the raw id
// when the shape is unknown.
func prettyModelID(id string) string {
	if id == "" {
		return ""
	}
	for _, m := range models {
		for _, v := range m.Versions {
			if v.ID == id {
				return m.Label + " " + v.Label
			}
		}
	}
	s := strings.TrimPrefix(id, "claude-")
	if s == id {
		return id // not a claude model id — show as-is
	}
	s = modelDateRe.ReplaceAllString(s, "")
	// Two layouts exist: <family>-<major>-<minor> (new) and <major>-<minor>-<family> (old).
	family := ""
	var nums []string
	for _, p := range strings.Split(s, "-") {
		if _, err := strconv.Atoi(p); err == nil {
			nums = append(nums, p)
		} else if family == "" {
			family = p
		}
	}
	if family == "" {
		return id
	}
	label := strings.ToUpper(family[:1]) + family[1:]
	if len(nums) > 0 {
		label += " " + strings.Join(nums, ".")
	}
	return label
}

func modelLabel(id string) string {
	if id == "" {
		return "default"
	}
	for _, m := range models {
		if m.ID == id {
			return m.Label
		}
		for _, v := range m.Versions {
			if v.ID == id {
				return m.Label + " " + v.Label
			}
		}
	}
	return id
}
