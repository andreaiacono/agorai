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

	q, _, opts := parsePermissionPrompt(raw)
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
	_, _, opts := parsePermissionPrompt(raw)
	if len(opts) != 2 {
		t.Fatalf("want 2 options, got %d: %+v", len(opts), opts)
	}
}

func TestParsePermissionPrompt_lastFrameWins(t *testing.T) {
	// two redraws; the latest frame's labels must win
	raw := []byte("Q?\r\n 1. Old A\r\n 2. Old B\r\n" +
		"\x1b[3A" + // cursor up (stripped)
		"Q?\r\n 1. New A\r\n 2. New B\r\n")
	_, _, opts := parsePermissionPrompt(raw)
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
	q, _, opts := parsePermissionPrompt(raw)
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
	_, _, opts := parsePermissionPrompt(raw)
	if len(opts) != 3 {
		t.Fatalf("want 3, got %d: %+v", len(opts), opts)
	}
	if opts[0].Label != "Yes" || opts[2].Label != "No" {
		t.Errorf("labels wrong: %+v", opts)
	}
}

func TestParsePermissionPrompt_spacelessTwo(t *testing.T) {
	raw := []byte("Doyouwanttoproceed?\r\r\n❯1.Yes\r\r\n2.No\r\r\n")
	_, _, opts := parsePermissionPrompt(raw)
	if len(opts) != 2 {
		t.Fatalf("want 2 (yes/no), got %d: %+v", len(opts), opts)
	}
}

func TestParsePermissionPrompt_mangledRedraw(t *testing.T) {
	// real shape seen in the wild: a complete render followed by a repaint that
	// ate the "." after options 2 and 3, so only "1. Yes" parses in the newest
	// frame — the parser must fall back to the earlier complete frame
	raw := []byte("Fetch unresolved review threads on PR 6934\r\r\n" +
		"Do you want to proceed?\r\r\n" +
		"❯ 1. Yes\r\r\n" +
		"  2. Yes, and always allow access to tmp/ from this project\r\r\n" +
		"  3. No\r\r\n" +
		"Esc to cancel · Tab to amend · ctrl+e to explain\r" +
		"Do you want to proceed?\r ❯ 1. Yes\r   2 Yes, and always allow access to tmp/ from this project\r 3 No\r")
	q, ctx, opts := parsePermissionPrompt(raw)
	if len(opts) != 3 {
		t.Fatalf("want 3 options, got %d: %+v", len(opts), opts)
	}
	if !strings.Contains(opts[1].Label, "always allow access to tmp/") {
		t.Errorf("option 2 wrong: %+v", opts[1])
	}
	if opts[2].Label != "No" {
		t.Errorf("option 3 wrong: %+v", opts[2])
	}
	if !strings.Contains(q, "proceed") {
		t.Errorf("question not captured: %q", q)
	}
	if !strings.Contains(ctx, "Fetch unresolved review threads") {
		t.Errorf("context not captured: %q", ctx)
	}
	if strings.Contains(ctx, "Esc to cancel") {
		t.Errorf("context kept UI chrome: %q", ctx)
	}
	if strings.Contains(ctx, "proceed") {
		t.Errorf("context must not repeat the question line: %q", ctx)
	}
}

func TestParsePermissionPrompt_damagedRedrawStillLooksComplete(t *testing.T) {
	// A repaint ate the "." from "3. No" and a letter out of the question. The
	// newest frame still looks complete (contiguous 1..2), so completeness alone
	// can't spot the damage — the parser must notice the earlier frame renders the
	// same prompt more fully, or the No button vanishes and the question reads
	// "Do you want to proc ed?".
	raw := []byte("This command requires approval\r\r\n" +
		"Do you want to proceed?\r\r\n" +
		"❯ 1. Yes\r\r\n" +
		"  2. Yes, and don’t ask again for: nohup npx vite preview --port 4328\r\r\n" +
		"  3. No\r\r\n" +
		"Esc to cancel · Tab to amend · ctrl+e to explain\r" +
		"Do you want to proc ed?\r ❯ 1. Yes\r   2. Yes, and don’t ask again for: nohup npx vite preview --port 4328\r   3 No\r")
	q, _, opts := parsePermissionPrompt(raw)
	if len(opts) != 3 {
		t.Fatalf("want 3 options, got %d: %+v", len(opts), opts)
	}
	if opts[2].Label != "No" {
		t.Errorf("option 3 wrong: %+v", opts[2])
	}
	if q != "Do you want to proceed?" {
		t.Errorf("want the intact question, got %q", q)
	}
}

func TestParsePermissionPrompt_mangledRedrawOfDifferentPrompt(t *testing.T) {
	// an incomplete newest frame must NOT inherit options from an older frame
	// whose labels don't agree (that's a different prompt, not a re-render)
	raw := []byte("Run rm -rf?\r\n❯ 1. Allow\r\n  2. Deny\r\n" +
		"Do you want to proceed?\r\n❯ 1. Yes\r\n  2 No\r\n")
	_, _, opts := parsePermissionPrompt(raw)
	if len(opts) != 1 || opts[0].Label != "Yes" {
		t.Fatalf("expected only the newest frame's option, got %+v", opts)
	}
}

func TestParsePermissionPrompt_contextSkipsRepaintDebris(t *testing.T) {
	// real shape: repaint confetti ("h t", "✢ r e") and a re-rendered question
	// (appears twice with no option line between) must not pollute the context
	raw := []byte("h t\r\nc s\r\n✢ r e\r\n· O h\r\n" +
		"←  ☐ Refactor  ☐ Accrual offset  ☐ Product FK ✔ Submit →\r\n" +
		"How should I proceed with the big refactor?\r\n" +
		"How should I proceed with the big refactor?\r\n" +
		"❯ 1. Yes\r\n  2. No\r\n")
	q, ctx, opts := parsePermissionPrompt(raw)
	if len(opts) != 2 {
		t.Fatalf("want 2 options, got %d: %+v", len(opts), opts)
	}
	if q != "How should I proceed with the big refactor?" {
		t.Errorf("question wrong: %q", q)
	}
	if ctx != "←  ☐ Refactor  ☐ Accrual offset  ☐ Product FK ✔ Submit →" {
		t.Errorf("context wrong (debris or repeated question kept): %q", ctx)
	}
}

func TestParsePermissionPrompt_codexApproval(t *testing.T) {
	// codex's approval is a numbered select like claude's, but marks the selected
	// option with "›" instead of "❯" and lists hotkeys in parentheses
	raw := []byte("Would you like to run the following command?\r\n" +
		"Reason: fetch PR metadata for the review\r\n" +
		"$ gh pr view 6934 --json number,title\r\n" +
		"› 1. Yes, proceed (y)\r\n" +
		"  2. Yes, and don't ask again for commands that start with `gh pr view` (p)\r\n" +
		"  3. No, and tell Codex what to do differently (esc)\r\n" +
		"Press enter to confirm or esc to cancel\r\n")
	_, _, opts := parsePermissionPrompt(raw)
	if len(opts) != 3 {
		t.Fatalf("want 3 options, got %d: %+v", len(opts), opts)
	}
	if !strings.HasPrefix(opts[0].Label, "Yes, proceed") {
		t.Errorf("opt1 wrong: %+v", opts[0])
	}
	if !strings.Contains(opts[2].Label, "No") {
		t.Errorf("opt3 wrong: %+v", opts[2])
	}
}

func TestParsePermissionPrompt_none(t *testing.T) {
	_, _, opts := parsePermissionPrompt([]byte("just some normal output\nno prompt here\n"))
	if opts != nil {
		t.Fatalf("expected nil, got %+v", opts)
	}
}
