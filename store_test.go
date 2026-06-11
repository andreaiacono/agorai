package main

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{path: filepath.Join(t.TempDir(), "sessions.json"), items: map[string]persisted{}}
}

func TestStorePendingRoundTrip(t *testing.T) {
	s := newTestStore(t)
	// a codex placeholder keyed by the launch id, plus a normal claude record
	s.upsert(persisted{ClaudeID: "launch-abc", Cwd: "/x", Name: "Scratch", Agent: AgentCodex, Pending: true})
	s.upsert(persisted{ClaudeID: "uuid-1", Cwd: "/y", Name: "Repo", Model: "opus"})

	reloaded := &Store{path: s.path, items: map[string]persisted{}}
	reloaded.load()
	got := map[string]persisted{}
	for _, p := range reloaded.all() {
		got[p.ClaudeID] = p
	}
	if p := got["launch-abc"]; !p.Pending || p.Agent != AgentCodex {
		t.Errorf("pending codex record lost its fields: %+v", p)
	}
	if p := got["uuid-1"]; p.Pending || p.Agent != "" || p.Model != "opus" {
		t.Errorf("claude record wrong: %+v", p)
	}
}

func TestStoreUpsertRejectsEmptyKey(t *testing.T) {
	s := newTestStore(t)
	s.upsert(persisted{ClaudeID: "", Cwd: "/x"}) // no key → must be ignored
	if len(s.all()) != 0 {
		t.Errorf("empty-key record should not be stored: %+v", s.all())
	}
}

func TestStoreRemoveByLaunchID(t *testing.T) {
	s := newTestStore(t)
	s.upsert(persisted{ClaudeID: "launch-abc", Agent: AgentCodex, Pending: true})
	s.remove("launch-abc") // adopt removes the placeholder by launch id
	if len(s.all()) != 0 {
		t.Errorf("placeholder not removed: %+v", s.all())
	}
}
