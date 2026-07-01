package main

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Claude exposes account usage limits through a session's statusLine only after
// that session's first API response — so the quota panel sits greyed until you
// interact. This poller fills it at startup by querying the same account-level
// endpoint Claude Code's own `/usage` command uses, and applies the result to
// every live Claude session. Best-effort: the endpoint is undocumented, so any
// failure just falls back to the statusLine-fed behaviour.

const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

const claudeUsagePollInterval = 60 * time.Second

// pollClaudeUsage runs for the process lifetime, refreshing Claude usage limits
// once a minute whenever at least one Claude session is open. It skips the
// network call entirely when no Claude session exists, so it never touches the
// account token unless there's something to show.
func (s *Server) pollClaudeUsage(home string) {
	client := &http.Client{Timeout: 10 * time.Second}
	credPath := filepath.Join(home, ".claude", ".credentials.json")
	for {
		if s.mgr.agentCount(AgentClaude) > 0 {
			if l, ok := fetchClaudeUsage(client, credPath); ok {
				if n := s.mgr.setClaudeLimits(l.Pct5h, l.Reset5h, l.PctWeek, l.ResetWeek); n > 0 {
					s.broadcastSessions()
				}
			}
		}
		time.Sleep(claudeUsagePollInterval)
	}
}

// oauthUsage is the slice of the usage endpoint's response we use: the two
// rolling windows, each with a 0–100 utilization percent and an ISO-8601 reset.
type oauthUsage struct {
	FiveHour *oauthWindow `json:"five_hour"`
	SevenDay *oauthWindow `json:"seven_day"`
}

type oauthWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

// fetchClaudeUsage reads the OAuth token and queries the usage endpoint. Returns
// ok=false on any failure (no/stale token, network error, non-200, unparseable
// body, or no usable windows yet).
func fetchClaudeUsage(client *http.Client, credPath string) (*UsageLimits, bool) {
	token := readClaudeToken(credPath)
	if token == "" {
		return nil, false
	}
	req, err := http.NewRequest(http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var p oauthUsage
	if json.Unmarshal(body, &p) != nil {
		return nil, false
	}
	l := &UsageLimits{}
	if w := p.FiveHour; w != nil {
		l.Pct5h, l.Reset5h = int(math.Round(w.Utilization)), parseResetsAt(w.ResetsAt)
	}
	if w := p.SevenDay; w != nil {
		l.PctWeek, l.ResetWeek = int(math.Round(w.Utilization)), parseResetsAt(w.ResetsAt)
	}
	if l.Reset5h == 0 && l.ResetWeek == 0 {
		return nil, false
	}
	return l, true
}

// readClaudeToken returns the Claude.ai OAuth access token from Claude Code's
// credentials file, or "" if absent, unreadable, or expired. A stale token is
// treated as absent — Claude Code refreshes it on its own use, so the next poll
// picks up the fresh one rather than us trying to refresh it ourselves.
func readClaudeToken(credPath string) string {
	b, err := os.ReadFile(credPath)
	if err != nil {
		return ""
	}
	var c struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"` // unix millis
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(b, &c) != nil {
		return ""
	}
	o := c.ClaudeAiOauth
	if o.AccessToken == "" {
		return ""
	}
	if o.ExpiresAt > 0 && time.Now().UnixMilli() >= o.ExpiresAt {
		return ""
	}
	return o.AccessToken
}

// parseResetsAt converts an ISO-8601 reset timestamp to unix seconds (what the
// frontend's countdown expects), or 0 if empty/unparseable.
func parseResetsAt(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}
