package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// Config is user-tunable settings, persisted to ~/.agorai/config.json.
type Config struct {
	Scrollback int               `json:"scrollback"`
	Env        map[string]string `json:"env"` // extra env vars passed to claude at launch
	// Last terminal size the browser reported. New/restored sessions spawn their
	// PTY at this size so early output — and a resumed session's replayed history,
	// rendered before any browser attaches — wraps at the real width instead of a
	// narrow default. 0 = unknown.
	TermCols int `json:"termCols,omitempty"`
	TermRows int `json:"termRows,omitempty"`
}

func defaultConfig() Config {
	return Config{Scrollback: 100000, Env: map[string]string{}}
}

// ConfigStore holds the live config and persists changes (0600, since env may
// contain secrets like a DB URL).
type ConfigStore struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

func newConfigStore() *ConfigStore {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".agorai")
	_ = os.MkdirAll(dir, 0o755)

	c := &ConfigStore{path: filepath.Join(dir, "config.json"), cfg: defaultConfig()}
	if b, err := os.ReadFile(c.path); err == nil {
		var loaded Config
		if json.Unmarshal(b, &loaded) == nil {
			c.cfg = sanitizeConfig(loaded)
		}
	}
	return c
}

func (c *ConfigStore) Get() Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	env := make(map[string]string, len(c.cfg.Env))
	for k, v := range c.cfg.Env {
		env[k] = v
	}
	return Config{Scrollback: c.cfg.Scrollback, Env: env, TermCols: c.cfg.TermCols, TermRows: c.cfg.TermRows}
}

// SetTermSize records the last terminal size, persisting only on an actual
// change (resizes arrive per character-cell step, so this dedupes disk writes).
func (c *ConfigStore) SetTermSize(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.TermCols == cols && c.cfg.TermRows == rows {
		return
	}
	c.cfg.TermCols, c.cfg.TermRows = cols, rows
	c.cfg = sanitizeConfig(c.cfg)
	b, _ := json.MarshalIndent(c.cfg, "", "  ")
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}

// termSize returns the persisted terminal size, or the given fallback if unknown.
func (c *ConfigStore) termSize(defCols, defRows uint16) (cols, rows uint16) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.TermCols > 0 && c.cfg.TermRows > 0 {
		return uint16(c.cfg.TermCols), uint16(c.cfg.TermRows)
	}
	return defCols, defRows
}

func (c *ConfigStore) Set(cfg Config) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = sanitizeConfig(cfg)
	b, _ := json.MarshalIndent(c.cfg, "", "  ")
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func sanitizeConfig(c Config) Config {
	if c.Scrollback < 100 {
		c.Scrollback = 100
	}
	if c.Scrollback > 200000 {
		c.Scrollback = 200000
	}
	c.TermCols = clampSize(c.TermCols, 20, 500)
	c.TermRows = clampSize(c.TermRows, 5, 300)
	clean := map[string]string{}
	for k, v := range c.Env {
		if envKeyRe.MatchString(k) { // reject anything that isn't a valid env name
			clean[k] = v
		}
	}
	c.Env = clean
	return c
}

// clampSize keeps a terminal dimension in a sane range, leaving 0 (unknown) as 0.
func clampSize(v, lo, hi int) int {
	if v == 0 {
		return 0
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
