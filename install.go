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
	if err := mergeAgoraiHooks(filepath.Join(home, ".claude", "settings.json"), scriptPath, claudeHookEvents); err != nil {
		return err
	}
	// Gemini CLI uses the same hook format with its own event names; wire it too
	// (only if ~/.gemini exists).
	if _, err := os.Stat(filepath.Join(home, ".gemini")); err == nil {
		if err := mergeAgoraiHooks(filepath.Join(home, ".gemini", "settings.json"), scriptPath, geminiHookEvents); err != nil {
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

type hookEvent struct{ name, matcher string }

// Claude and Gemini use the same hook *format* but different event *names*.
// Gemini calls the turn boundaries BeforeAgent/AfterAgent (not UserPromptSubmit/
// Stop) and rejects unknown names, so each gets its own set.
var claudeHookEvents = []hookEvent{
	{"SessionStart", ""},
	{"UserPromptSubmit", ""},
	{"Notification", "idle_prompt|permission_prompt|elicitation_dialog"},
	{"Stop", ""},
	{"SessionEnd", ""},
}
var geminiHookEvents = []hookEvent{
	{"SessionStart", ""},
	{"BeforeAgent", ""}, // ≈ UserPromptSubmit → working
	{"Notification", ""},
	{"AfterAgent", ""}, // ≈ Stop → idle
	{"SessionEnd", ""},
}

// mergeAgoraiHooks rewrites the agorai hook entries in a settings.json: it strips
// every existing agorai entry (so stale/wrong event names are removed) and adds
// the given events. Backs up the original. Idempotent and self-correcting.
func mergeAgoraiHooks(settingsPath, scriptPath string, events []hookEvent) error {
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
	// drop any existing agorai entries (under any event name) before re-adding
	for ev, v := range hooks {
		arr, ok := v.([]any)
		if !ok {
			continue
		}
		kept := arr[:0]
		for _, e := range arr {
			if !entryHasAgorai(e) {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	for _, ev := range events {
		entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": scriptPath}}}
		if ev.matcher != "" {
			entry["matcher"] = ev.matcher
		}
		existing, _ := hooks[ev.name].([]any)
		hooks[ev.name] = append(existing, entry)
	}
	settings["hooks"] = hooks

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, out, 0o644)
}

// ensureGeminiHooks wires the agorai hooks (with gemini's event names) into
// ~/.gemini/settings.json on demand. Idempotent, self-correcting, best-effort.
func ensureGeminiHooks() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	settingsPath := filepath.Join(home, ".gemini", "settings.json")
	// Skip only if the correct (BeforeAgent) hook is already wired.
	if b, err := os.ReadFile(settingsPath); err == nil && geminiHooksCorrect(b) {
		return
	}
	scriptPath, err := writeHookScript(home)
	if err != nil {
		return
	}
	_ = mergeAgoraiHooks(settingsPath, scriptPath, geminiHookEvents)
}

// geminiHooksCorrect reports whether an agorai entry is wired under gemini's
// BeforeAgent event (i.e. already migrated to the correct names).
func geminiHooksCorrect(b []byte) bool {
	var s map[string]any
	if json.Unmarshal(b, &s) != nil {
		return false
	}
	hooks, _ := s["hooks"].(map[string]any)
	arr, _ := hooks["BeforeAgent"].([]any)
	for _, e := range arr {
		if entryHasAgorai(e) {
			return true
		}
	}
	return false
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
