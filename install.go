package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

//go:embed hooks/agorai.sh
var hookFS embed.FS

// installHooks writes the hook forwarder script and merges the agorai hook
// entries into ~/.claude/settings.json (backing up the original first). It is
// idempotent: re-running it won't duplicate entries.
func installHooks() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	claudeDir := filepath.Join(home, ".claude")

	// 1. write the hook script
	hooksDir := filepath.Join(claudeDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}
	script, err := hookFS.ReadFile("hooks/agorai.sh")
	if err != nil {
		return err
	}
	scriptPath := filepath.Join(hooksDir, "agorai.sh")
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		return err
	}

	// 2. merge settings.json
	settingsPath := filepath.Join(claudeDir, "settings.json")
	settings := map[string]any{}
	if b, err := os.ReadFile(settingsPath); err == nil {
		if len(b) > 0 {
			if err := json.Unmarshal(b, &settings); err != nil {
				return fmt.Errorf("existing settings.json is not valid JSON: %w", err)
			}
		}
		_ = os.WriteFile(settingsPath+".agorai.bak", b, 0o644) // backup
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}

	add := func(event, matcher string) {
		existing, _ := hooks[event].([]any)
		// If an agorai entry already exists for this event, just keep its matcher
		// current (e.g. a new notification type was added) instead of duplicating.
		for _, e := range existing {
			if entryHasAgorai(e) {
				if m, ok := e.(map[string]any); ok {
					if matcher != "" {
						m["matcher"] = matcher
					} else {
						delete(m, "matcher")
					}
				}
				return
			}
		}
		entry := map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": scriptPath}},
		}
		if matcher != "" {
			entry["matcher"] = matcher
		}
		hooks[event] = append(existing, entry)
	}
	add("SessionStart", "")
	add("UserPromptSubmit", "")
	add("Notification", "idle_prompt|permission_prompt|elicitation_dialog")
	add("Stop", "")
	add("SessionEnd", "")
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0o644)
}

func entryHasAgorai(e any) bool {
	m, ok := e.(map[string]any)
	if !ok {
		return false
	}
	hs, _ := m["hooks"].([]any)
	for _, h := range hs {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if c, _ := hm["command"].(string); strings.Contains(c, "agorai") {
			return true
		}
	}
	return false
}
