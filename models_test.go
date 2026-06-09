package main

import "testing"

func TestPrettyModelID(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":            "Opus 4.8",
		"claude-sonnet-4-6":          "Sonnet 4.6",
		"claude-haiku-4-5-20251001":  "Haiku 4.5",
		"claude-sonnet-4-5-20250929": "Sonnet 4.5",
		"claude-3-5-haiku-20241022":  "Haiku 3.5",
		"claude-3-5-haiku-latest":    "Haiku 3.5",
		"claude-opus-4-20250514":     "Opus 4",
		"gpt-oss":                    "gpt-oss", // unknown shape stays as-is
		"":                           "",
	}
	for id, want := range cases {
		if got := prettyModelID(id); got != want {
			t.Errorf("prettyModelID(%q) = %q, want %q", id, got, want)
		}
	}
}
