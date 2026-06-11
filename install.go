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
// entries into the Claude (and, if present, Gemini) settings.json — both use
// the same Claude-compatible hook format. Idempotent: re-running won't
// duplicate entries.
func installHooks() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	scriptPath, err := writeHookScript(home)
	if err != nil {
		return err
	}
	if err := mergeAgoraiHooks(filepath.Join(home, ".claude", "settings.json"), scriptPath); err != nil {
		return err
	}
	// Gemini CLI uses Claude-compatible hooks; wire them too so its sessions
	// report state. Only if ~/.gemini exists (gemini is installed/used).
	if _, err := os.Stat(filepath.Join(home, ".gemini")); err == nil {
		if err := mergeAgoraiHooks(filepath.Join(home, ".gemini", "settings.json"), scriptPath); err != nil {
			return err
		}
	}
	return nil
}

// writeHookScript writes the embedded forwarder to ~/.claude/hooks/agorai.sh and
// returns its path (shared by every agent's hooks).
func writeHookScript(home string) (string, error) {
	hooksDir := filepath.Join(home, ".claude", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", err
	}
	script, err := hookFS.ReadFile("hooks/agorai.sh")
	if err != nil {
		return "", err
	}
	scriptPath := filepath.Join(hooksDir, "agorai.sh")
	if err := os.WriteFile(scriptPath, script, 0o755); err != nil {
		return "", err
	}
	return scriptPath, nil
}

// mergeAgoraiHooks merges the agorai hook entries into a settings.json, backing
// up the original. Idempotent.
func mergeAgoraiHooks(settingsPath, scriptPath string) error {
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return err
	}
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

// ensureGeminiHooks wires the agorai hooks into ~/.gemini/settings.json on demand
// (so gemini sessions report state without a manual `agorai install`). Idempotent
// and best-effort.
func ensureGeminiHooks() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	settingsPath := filepath.Join(home, ".gemini", "settings.json")
	if b, err := os.ReadFile(settingsPath); err == nil && entryHasAgorai(geminiHooksPresent(b)) {
		return // already wired
	}
	scriptPath, err := writeHookScript(home)
	if err != nil {
		return
	}
	_ = mergeAgoraiHooks(settingsPath, scriptPath)
}

// geminiHooksPresent returns the first agorai hook entry found in a settings
// blob (or nil), so ensureGeminiHooks can skip work when already wired.
func geminiHooksPresent(b []byte) any {
	var s map[string]any
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	hooks, _ := s["hooks"].(map[string]any)
	for _, v := range hooks {
		if arr, ok := v.([]any); ok {
			for _, e := range arr {
				if entryHasAgorai(e) {
					return e
				}
			}
		}
	}
	return nil
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
