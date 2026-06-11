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

// PromptInfo is the parsed prompt shown on the session row. Context carries
// the lines above the question (tool header, command, description) so the UI
// can show the full story in a tooltip — the question alone is often just a
// generic "Do you want to proceed?".
type PromptInfo struct {
	Question string         `json:"question"`
	Context  string         `json:"context,omitempty"`
	Options  []PromptOption `json:"options"`
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// cursorFwdRe matches horizontal cursor moves (forward / column-absolute). Claude
// uses these to lay out the prompt box instead of literal spaces, so we turn them
// back into a space before stripping the rest — otherwise words run together.
var cursorFwdRe = regexp.MustCompile(`\x1b\[[0-9]*[CG]`)

// optRe matches a numbered option line. Whitespace after the "." is optional
// because the rendered prompt often has none (the spacing was cursor moves).
// The leading marker class includes the cursors both claude (❯) and codex (›)
// put on the selected option.
var optRe = regexp.MustCompile(`^[>❯›*]?\s*([1-9])[.)]\s*(.+)$`)

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

// promptFrame is one rendering of the prompt box found in the PTY bytes; a
// redraw restarts the option numbering at "1.", which starts a new frame.
type promptFrame struct {
	question string
	context  string
	opts     map[int]string
}

// chromeRe matches terminal-UI chrome lines that carry no prompt context
// (key hints, footers, spinner status) and shouldn't land in the tooltip.
var chromeRe = regexp.MustCompile(`^(Esc to cancel|⏵|⎿)|\(ctrl\+. to |\(shift\+tab to |tokens\)$|^Waiting…$`)

// wordRe demands a run of 3+ letters somewhere in the line. Partial repaints
// shred text into letter confetti ("h t", "✢ r e", "· O h") that still contains
// letters — requiring a real word filters that debris out.
var wordRe = regexp.MustCompile(`\pL{3,}`)

// isContextLine reports whether a cleaned line is worth keeping as prompt
// context: it must contain an actual word (drops spinner/number/letter debris
// from partial repaints) and not be UI chrome.
func isContextLine(c string) bool {
	if chromeRe.MatchString(c) {
		return false
	}
	return wordRe.MatchString(c)
}

// frameComplete reports whether opts form a contiguous 1..n block with n >= 2 —
// the shape of a fully rendered Claude prompt (there's always at least Yes/No).
func frameComplete(opts map[int]string) bool {
	if len(opts) < 2 {
		return false
	}
	for i := 1; i <= len(opts); i++ {
		if _, ok := opts[i]; !ok {
			return false
		}
	}
	return true
}

// frameConsistent reports whether partial looks like a mangled re-render of
// full: every option it did capture exists in full with the same label (or a
// prefix of it, since a partial repaint can also truncate a label's tail).
func frameConsistent(partial, full map[int]string) bool {
	for n, label := range partial {
		if full[n] != label && !strings.HasPrefix(full[n], label) {
			return false
		}
	}
	return true
}

// parsePermissionPrompt extracts the numbered options (plus a best-effort
// question line and the context lines above it) from the most recently drawn
// prompt box in raw PTY bytes. Returns nil options if nothing looks like a prompt.
func parsePermissionPrompt(raw []byte) (string, string, []PromptOption) {
	text := strings.ReplaceAll(stripANSI(raw), "\r", "\n")
	lines := strings.Split(text, "\n")

	var frames []promptFrame
	lastText := ""
	var recent []string // recent context-worthy lines, feeds the tooltip
	for _, ln := range lines {
		c := cleanLine(ln)
		if c == "" {
			continue
		}
		m := optRe.FindStringSubmatch(c)
		if m == nil {
			lastText = c // remember the most recent non-option line (the question)
			if isContextLine(c) {
				recent = append(recent, c)
				if len(recent) > 6 {
					recent = recent[1:]
				}
			}
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if n == 1 || len(frames) == 0 {
			// Re-renders repeat lines (the question can appear several times in
			// recent), so drop every copy of the question and squash consecutive
			// duplicates — the question itself is not context.
			var ctxLines []string
			for _, l := range recent {
				if l == lastText || (len(ctxLines) > 0 && ctxLines[len(ctxLines)-1] == l) {
					continue
				}
				ctxLines = append(ctxLines, l)
			}
			frames = append(frames, promptFrame{question: lastText, context: strings.Join(ctxLines, "\n"), opts: map[int]string{}})
			recent = nil
		}
		frames[len(frames)-1].opts[n] = strings.TrimSpace(m[2])
	}
	if len(frames) == 0 {
		return "", "", nil
	}

	// The latest frame normally wins, but a repaint while the prompt sits on
	// screen can eat the "." after an option number, so that line no longer
	// parses and the newest frame comes out with fewer options than are really
	// shown. If the newest frame is incomplete, fall back to the nearest earlier
	// complete frame it's consistent with — the same prompt, fully rendered.
	best := frames[len(frames)-1]
	if !frameComplete(best.opts) {
		for i := len(frames) - 2; i >= 0; i-- {
			if frameComplete(frames[i].opts) && frameConsistent(best.opts, frames[i].opts) {
				best = frames[i]
				break
			}
		}
	}

	nums := make([]int, 0, len(best.opts))
	for n := range best.opts {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	out := make([]PromptOption, 0, len(nums))
	for _, n := range nums {
		out = append(out, PromptOption{Num: n, Label: truncate(best.opts[n], 90)})
	}
	return best.question, truncate(best.context, 600), out
}
