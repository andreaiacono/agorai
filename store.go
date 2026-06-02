package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// persisted is the minimal record needed to bring a session back after a
// restart: claude's session id (to `--resume`) and where to run it.
type persisted struct {
	ClaudeID string `json:"claudeId"`
	Cwd      string `json:"cwd"`
	Name     string `json:"name"`
	Branch   string `json:"branch"`
	Model    string `json:"model"`
}

// Store keeps the set of hosted sessions on disk so they survive a restart.
// It's keyed by claude session id and written atomically on every change.
type Store struct {
	mu    sync.Mutex
	path  string
	items map[string]persisted
}

func newStore() *Store {
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".agorai")
	_ = os.MkdirAll(dir, 0o755)

	st := &Store{path: filepath.Join(dir, "sessions.json"), items: map[string]persisted{}}
	st.load()
	return st
}

func (s *Store) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []persisted
	if json.Unmarshal(b, &list) == nil {
		for _, p := range list {
			if p.ClaudeID != "" {
				s.items[p.ClaudeID] = p
			}
		}
	}
}

// save writes the store atomically. Callers must hold s.mu.
func (s *Store) save() {
	list := make([]persisted, 0, len(s.items))
	for _, p := range s.items {
		list = append(list, p)
	}
	b, _ := json.MarshalIndent(list, "", "  ")
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func (s *Store) upsert(p persisted) {
	if p.ClaudeID == "" {
		return
	}
	s.mu.Lock()
	s.items[p.ClaudeID] = p
	s.save()
	s.mu.Unlock()
}

func (s *Store) remove(claudeID string) {
	if claudeID == "" {
		return
	}
	s.mu.Lock()
	delete(s.items, claudeID)
	s.save()
	s.mu.Unlock()
}

func (s *Store) all() []persisted {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]persisted, 0, len(s.items))
	for _, p := range s.items {
		out = append(out, p)
	}
	return out
}
