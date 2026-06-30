package main

import (
	"embed"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed hooks/agorai-statusline.sh
var statuslineFS embed.FS

// setupClaudeUsage wires claude's statusLine to agorai's forwarder so account
// usage limits (5h / 7d) flow into the dashboard. It writes the forwarder
// script, builds a --settings overlay that points statusLine at it, and reads
// the user's own statusLine command (which the overlay replaces) so the
// forwarder can re-run it and keep the terminal view unchanged. Returns the
// overlay path and the user's statusLine command. Best-effort: any error means
// "no Claude usage limits", not a fatal startup failure.
func setupClaudeUsage(home string) (settingsPath, userStatusline string, err error) {
	hooksDir := filepath.Join(home, ".claude", "hooks")
	if err = os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", "", err
	}
	script, err := statuslineFS.ReadFile("hooks/agorai-statusline.sh")
	if err != nil {
		return "", "", err
	}
	scriptPath := filepath.Join(hooksDir, "agorai-statusline.sh")
	if err = os.WriteFile(scriptPath, script, 0o755); err != nil {
		return "", "", err
	}

	userStatusline = readUserStatusline(filepath.Join(home, ".claude", "settings.json"))

	overlay := map[string]any{
		"statusLine": map[string]any{
			"type":    "command",
			"command": scriptPath,
			"padding": 0,
		},
	}
	out, err := json.MarshalIndent(overlay, "", "  ")
	if err != nil {
		return "", "", err
	}
	agoraiDir := filepath.Join(home, ".agorai")
	if err = os.MkdirAll(agoraiDir, 0o755); err != nil {
		return "", "", err
	}
	settingsPath = filepath.Join(agoraiDir, "claude-statusline-settings.json")
	if err = os.WriteFile(settingsPath, out, 0o644); err != nil {
		return "", "", err
	}
	return settingsPath, userStatusline, nil
}

// readUserStatusline returns the user's configured statusLine.command (empty if
// none / unreadable), so agorai's overlay can chain to it.
func readUserStatusline(settingsPath string) string {
	b, err := os.ReadFile(settingsPath)
	if err != nil {
		return ""
	}
	var s struct {
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s.StatusLine.Command
}

// statuslinePayload is the slice of Claude's statusLine JSON we care about: the
// rolling rate-limit windows. rate_limits is present only for Claude.ai
// subscribers (Pro/Max) after the first API response, and each window may be
// independently absent.
type statuslinePayload struct {
	RateLimits struct {
		FiveHour *usageWindow `json:"five_hour"`
		SevenDay *usageWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

type usageWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// handleUsage receives a session's statusLine payload (forwarded by the
// agorai-statusline.sh script, keyed by AGORAI_ID) and records its usage limits.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	w.WriteHeader(http.StatusNoContent) // ack fast; the statusline doesn't wait on us

	var p statuslinePayload
	if json.Unmarshal(body, &p) != nil {
		return
	}
	sess := s.mgr.Get(id)
	if sess == nil {
		return // a session started outside agorai, or unknown id
	}

	var pct5h, pctWk int
	var reset5h, resetWk int64
	if w := p.RateLimits.FiveHour; w != nil {
		pct5h, reset5h = int(math.Round(w.UsedPercentage)), w.ResetsAt
	}
	if w := p.RateLimits.SevenDay; w != nil {
		pctWk, resetWk = int(math.Round(w.UsedPercentage)), w.ResetsAt
	}
	if reset5h == 0 && resetWk == 0 {
		return // no usable limits yet (before the first API response, or an API-key user)
	}

	sess.setLimits(pct5h, reset5h, pctWk, resetWk)
	s.broadcastSessions() // push the freshly-known limits to clients live
}
