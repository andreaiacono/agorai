package main

import "testing"

func TestLooksTyped(t *testing.T) {
	cases := map[string]bool{
		"y":                 true,  // a keystroke
		"\r":                false, // a bare Enter submits nothing on an empty prompt
		"\n":                false, // bare newline, likewise
		"3\r":               true,  // answer a numbered prompt (the digit is content)
		"\x1b[A":            true,  // arrow key (user navigation)
		"\x1b[12;34R":       false, // cursor-position report (terminal reply)
		"\x1b[?62;1;6c":     false, // primary device attributes
		"\x1b[0n":           false, // device status report
		"\x1b[I":            false, // focus-in report
		"\x1b[O":            false, // focus-out report
		"\x1b[6n\x1b[?1;2c": false, // multiple replies, nothing typed
		"\x1b[12;1Ry":       true,  // a report plus a real keystroke
		"\x04":              false, // Ctrl-D (EOF), not content
		"\x03":              false, // Ctrl-C (interrupt), not content
		"\x7f":              false, // Backspace/DEL, not content
	}
	for in, want := range cases {
		if got := looksTyped([]byte(in)); got != want {
			t.Errorf("looksTyped(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestLooksAnswered(t *testing.T) {
	// Like looksTyped, but a bare Enter counts — it accepts a permission menu's
	// highlighted option, so it must clear the prompt promptly.
	cases := map[string]bool{
		"\r":            true,  // Enter accepts the highlighted option
		"\n":            true,  // newline, likewise
		"1\r":           true,  // pick an option
		"\x1b[A":        true,  // arrow navigation
		"\x1b[12;34R":   false, // cursor-position report (terminal reply, not a keystroke)
		"\x1b[?62;1;6c": false, // device attributes
		"\x1b[I":        false, // focus-in report (clicking the terminal doesn't answer)
		"\x1b[6n":       false, // status query reply
		"\x04":          false, // Ctrl-D (EOF) doesn't answer a menu
	}
	for in, want := range cases {
		if got := looksAnswered([]byte(in)); got != want {
			t.Errorf("looksAnswered(%q) = %v, want %v", in, got, want)
		}
	}
}
