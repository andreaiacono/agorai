package main

import (
	"strings"
	"testing"
)

func TestParsePermissionPrompt_threeOptions(t *testing.T) {
	raw := []byte("\x1b[2m╭───────────────────────────╮\x1b[0m\r\n" +
		"\x1b[1m│\x1b[0m Do you want to run this command? \x1b[1m│\x1b[0m\r\n" +
		"\x1b[36m│ ❯ 1. Yes\x1b[0m                       │\r\n" +
		"│   2. Yes, and don't ask again for npm │\r\n" +
		"│   3. No, tell Claude what to do differently │\r\n" +
		"\x1b[2m╰───────────────────────────╯\x1b[0m\r\n")

	q, opts := parsePermissionPrompt(raw)
	if len(opts) != 3 {
		t.Fatalf("want 3 options, got %d: %+v", len(opts), opts)
	}
	if opts[0].Num != 1 || !strings.HasPrefix(opts[0].Label, "Yes") {
		t.Errorf("opt1 wrong: %+v", opts[0])
	}
	if opts[2].Num != 3 || !strings.Contains(opts[2].Label, "No") {
		t.Errorf("opt3 wrong: %+v", opts[2])
	}
	if !strings.Contains(q, "run this command") {
		t.Errorf("question not captured: %q", q)
	}
}

func TestParsePermissionPrompt_twoOptions(t *testing.T) {
	raw := []byte("Proceed?\r\n  1. Yes\r\n  2. No\r\n")
	_, opts := parsePermissionPrompt(raw)
	if len(opts) != 2 {
		t.Fatalf("want 2 options, got %d: %+v", len(opts), opts)
	}
}

func TestParsePermissionPrompt_lastFrameWins(t *testing.T) {
	// two redraws; the latest frame's labels must win
	raw := []byte("Q?\r\n 1. Old A\r\n 2. Old B\r\n" +
		"\x1b[3A" + // cursor up (stripped)
		"Q?\r\n 1. New A\r\n 2. New B\r\n")
	_, opts := parsePermissionPrompt(raw)
	if len(opts) != 2 || opts[0].Label != "New A" || opts[1].Label != "New B" {
		t.Fatalf("expected latest frame, got %+v", opts)
	}
}

func TestParsePermissionPrompt_realBashApproval(t *testing.T) {
	// the exact shape seen for `nc` approval (command-specific option 2)
	raw := []byte("\x1b[1mBash command\x1b[0m\r\n" +
		"  timeout 120 nc -l 127.0.0.1 8089\r\n\r\n" +
		"Do you want to proceed?\r\n" +
		"\x1b[36m❯ 1. Yes\x1b[0m\r\n" +
		"  2. Yes, and don't ask again for: timeout 120 nc *\r\n" +
		"  3. No\r\n")
	q, opts := parsePermissionPrompt(raw)
	if len(opts) != 3 {
		t.Fatalf("want 3, got %d: %+v", len(opts), opts)
	}
	if !strings.Contains(opts[1].Label, "timeout 120 nc") {
		t.Errorf("option 2 lost the command-specific text: %q", opts[1].Label)
	}
	if !strings.Contains(q, "proceed") {
		t.Errorf("question not captured: %q", q)
	}
}

func TestParsePermissionPrompt_spacelessThree(t *testing.T) {
	// the real shape: cursor-positioned, so spaces are gone and no space after "."
	raw := []byte("Doyouwanttoproceed?\r\r\n❯1.Yes\r\r\n2.Yes,andalwaysallow\r\r\n3.No\r\r\n")
	_, opts := parsePermissionPrompt(raw)
	if len(opts) != 3 {
		t.Fatalf("want 3, got %d: %+v", len(opts), opts)
	}
	if opts[0].Label != "Yes" || opts[2].Label != "No" {
		t.Errorf("labels wrong: %+v", opts)
	}
}

func TestParsePermissionPrompt_spacelessTwo(t *testing.T) {
	raw := []byte("Doyouwanttoproceed?\r\r\n❯1.Yes\r\r\n2.No\r\r\n")
	_, opts := parsePermissionPrompt(raw)
	if len(opts) != 2 {
		t.Fatalf("want 2 (yes/no), got %d: %+v", len(opts), opts)
	}
}

func TestParsePermissionPrompt_none(t *testing.T) {
	_, opts := parsePermissionPrompt([]byte("just some normal output\nno prompt here\n"))
	if opts != nil {
		t.Fatalf("expected nil, got %+v", opts)
	}
}
