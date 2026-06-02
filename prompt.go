package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// PromptOption is one selectable choice in a permission/question prompt.
type PromptOption struct {
	Num   int    `json:"num"`
	Label string `json:"label"`
}

// PromptInfo is the parsed prompt shown on the session row.
type PromptInfo struct {
	Question string         `json:"question"`
	Options  []PromptOption `json:"options"`
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// cursorFwdRe matches horizontal cursor moves (forward / column-absolute). Claude
// uses these to lay out the prompt box instead of literal spaces, so we turn them
// back into a space before stripping the rest — otherwise words run together.
var cursorFwdRe = regexp.MustCompile(`\x1b\[[0-9]*[CG]`)

// optRe matches a numbered option line. Whitespace after the "." is optional
// because the rendered prompt often has none (the spacing was cursor moves).
var optRe = regexp.MustCompile(`^[>❯*]?\s*([1-9])[.)]\s*(.+)$`)

func stripANSI(b []byte) string {
	s := cursorFwdRe.ReplaceAllString(string(b), " ")
	return ansiRe.ReplaceAllString(s, "")
}

// cleanLine drops box-drawing characters and trims whitespace.
func cleanLine(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '│', '|', '╭', '╮', '╰', '╯', '─', '┌', '┐', '└', '┘', '├', '┤', '┃', '━', '╴', '╵':
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// parsePermissionPrompt extracts the numbered options (and a best-effort
// question line) from the most recently drawn prompt box in raw PTY bytes.
// Heuristic: each redraw restarts at "1.", so resetting on "1." keeps the
// latest frame. Returns nil options if nothing looks like a prompt.
func parsePermissionPrompt(raw []byte) (string, []PromptOption) {
	text := strings.ReplaceAll(stripANSI(raw), "\r", "\n")
	lines := strings.Split(text, "\n")

	opts := map[int]string{}
	question, lastText := "", ""
	for _, ln := range lines {
		c := cleanLine(ln)
		if c == "" {
			continue
		}
		m := optRe.FindStringSubmatch(c)
		if m == nil {
			lastText = c // remember the most recent non-option line (the question)
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if n == 1 {
			opts = map[int]string{} // a new frame started
			question = lastText
		}
		opts[n] = strings.TrimSpace(m[2])
	}
	if len(opts) == 0 {
		return "", nil
	}

	nums := make([]int, 0, len(opts))
	for n := range opts {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]PromptOption, 0, len(nums))
	for _, n := range nums {
		out = append(out, PromptOption{Num: n, Label: truncate(opts[n], 90)})
	}
	return question, out
}
