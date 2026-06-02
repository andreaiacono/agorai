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
}

func defaultConfig() Config {
	return Config{Scrollback: 10000, Env: map[string]string{}}
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
	return Config{Scrollback: c.cfg.Scrollback, Env: env}
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
	clean := map[string]string{}
	for k, v := range c.Env {
		if envKeyRe.MatchString(k) { // reject anything that isn't a valid env name
			clean[k] = v
		}
	}
	c.Env = clean
	return c
}
