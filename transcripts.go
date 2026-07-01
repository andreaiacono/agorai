package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// resumableScanLimit caps how many past sessions the Resume picker lists
// (newest first). There's no date cutoff — this count is the only bound, so it
// governs how far back the dialog's filter box can reach. Each listed session is
// fully parsed for its title/recap, so this trades dialog-open cost for reach.
const resumableScanLimit = 200

// Resumable is a past Claude Code session found on disk that can be re-opened
// (hosted) via `claude --resume <id>`.
type Resumable struct {
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Display   string `json:"display"` // home-shortened cwd
	Title     string `json:"title"`   // first user prompt, truncated
	Recap     string `json:"recap"`   // last assistant line, truncated
	Modified  int64  `json:"modified"`
	Age       string `json:"age"`
}

func transcriptsDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

// scanResumable returns the most recently active sessions on disk, newest first,
// parsing at most `limit` of them for title/recap.
func scanResumable(limit int) []Resumable {
	paths, _ := filepath.Glob(filepath.Join(transcriptsDir(), "*", "*.jsonl"))

	type fileInfo struct {
		path string
		mod  time.Time
	}
	files := make([]fileInfo, 0, len(paths))
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			files = append(files, fileInfo{p, st.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })

	out := make([]Resumable, 0, limit)
	for _, f := range files {
		if len(out) >= limit {
			break
		}
		if r, ok := parseTranscript(f.path, f.mod); ok {
			out = append(out, r)
		}
	}
	return out
}

// transcriptPathFor locates a session's transcript by id, regardless of how the
// project dir name is encoded (the session id is unique, so we glob for it).
func transcriptPathFor(sessionID string) string {
	matches, _ := filepath.Glob(filepath.Join(transcriptsDir(), "*", sessionID+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func findResumable(sessionID string) (Resumable, bool) {
	for _, r := range scanResumable(500) {
		if r.SessionID == sessionID {
			return r, true
		}
	}
	return Resumable{}, false
}

type transcriptEntry struct {
	Type    string          `json:"type"`
	Cwd     string          `json:"cwd"`
	Message json.RawMessage `json:"message"`
}

type transcriptMessage struct {
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Usage   struct {
		InputTokens         int `json:"input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func parseTranscript(path string, mod time.Time) (Resumable, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Resumable{}, false
	}
	defer f.Close()

	var cwd, firstUser, lastAssistant string
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n') // ReadString grows past any line length
		if len(line) > 0 {
			var e transcriptEntry
			if json.Unmarshal([]byte(line), &e) == nil {
				if cwd == "" && e.Cwd != "" {
					cwd = e.Cwd
				}
				if len(e.Message) > 0 {
					var m transcriptMessage
					if json.Unmarshal(e.Message, &m) == nil {
						text := extractText(m.Content)
						role := m.Role
						if role == "" {
							role = e.Type
						}
						switch role {
						case "user":
							if firstUser == "" && strings.TrimSpace(text) != "" {
								firstUser = text
							}
						case "assistant":
							if strings.TrimSpace(text) != "" {
								lastAssistant = text
							}
						}
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}

	if cwd == "" {
		return Resumable{}, false // can't host it without a real cwd
	}

	title := truncate(oneLine(firstUser), 70)
	if title == "" {
		title = filepath.Base(cwd)
	}
	recap := truncate(oneLine(lastAssistant), 80)
	if recap == "" {
		recap = "(no assistant reply yet)"
	}

	return Resumable{
		SessionID: strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Cwd:       cwd,
		Display:   shortenHome(cwd),
		Title:     title,
		Recap:     recap,
		Modified:  mod.Unix(),
		Age:       humanizeSince(mod),
	}, true
}

// lastAssistantInfo returns the last assistant text (one line, truncated) and
// the raw model id of the last assistant message — the latter reveals what
// model a "default" session actually runs on. It reads only the tail of the
// file so it stays cheap on long sessions (an assistant turn is virtually
// always within the last window).
func lastAssistantInfo(path string) (string, string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return "", ""
	}
	const window = 256 * 1024
	start := int64(0)
	if st.Size() > window {
		start = st.Size() - window
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", ""
	}
	data, _ := io.ReadAll(f)

	lines := strings.Split(string(data), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // drop the partial first line
	}

	last, model := "", ""
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e transcriptEntry
		if json.Unmarshal([]byte(line), &e) != nil || len(e.Message) == 0 {
			continue
		}
		var m transcriptMessage
		if json.Unmarshal(e.Message, &m) != nil {
			continue
		}
		role := m.Role
		if role == "" {
			role = e.Type
		}
		if role == "assistant" {
			if t := strings.TrimSpace(extractText(m.Content)); t != "" {
				last = t
			}
			if m.Model != "" {
				model = m.Model
			}
		}
	}
	return truncate(oneLine(last), 90), model
}

// claudeContextTokens returns the live context-window size from the latest
// assistant turn's usage (input + cache-read + cache-creation) and that turn's
// model id, read from the tail so it stays cheap on long sessions. 0/"" if
// unknown. This is the "context fill" the panel shows — there is no account-level
// quota readable from disk (rate-limit state lives only in API response headers).
func claudeContextTokens(path string) (int, string) {
	ctx, model := 0, ""
	for _, line := range tailLines(path, 256*1024) {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e transcriptEntry
		if json.Unmarshal([]byte(line), &e) != nil || len(e.Message) == 0 {
			continue
		}
		var m transcriptMessage
		if json.Unmarshal(e.Message, &m) != nil {
			continue
		}
		role := m.Role
		if role == "" {
			role = e.Type
		}
		if role == "assistant" {
			if t := m.Usage.InputTokens + m.Usage.CacheReadTokens + m.Usage.CacheCreationTokens; t > 0 {
				ctx = t // keep the most recent assistant turn's context size
			}
			if m.Model != "" {
				model = m.Model
			}
		}
	}
	return ctx, model
}

// claudeContextWindowFor maps a claude model id to its context-window ceiling.
// Transcripts don't carry the window (unlike codex), so derive it from the model
// rather than the token count — a token-based guess made the gauge inconsistent
// (a 97k session read 200k→49% while a 245k session read 1M→24%). Current-gen
// models (4.x+, e.g. opus-4-8) run a 1M window here; the older 3.x/2.x line is
// 200k. Unknown → 1M (matches this environment's models).
func claudeContextWindowFor(model string) int {
	m := strings.ToLower(model)
	if strings.Contains(m, "claude-3") || strings.Contains(m, "claude-2") {
		return 200_000
	}
	return 1_000_000
}

// claudeContextOf returns (tokens, ceiling) for the latest claude turn — the pair
// the session stores for its gauge. Both 0 when the transcript has no usage yet.
func claudeContextOf(path string) (int, int) {
	t, model := claudeContextTokens(path)
	if t == 0 {
		return 0, 0
	}
	return t, claudeContextWindowFor(model)
}

// extractText pulls plain text out of a message's content, which may be a raw
// string or an array of typed blocks.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n])) + "…"
}

func humanizeSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return itoa(int(d.Hours())) + "h ago"
	case d < 7*24*time.Hour:
		return itoa(int(d.Hours()/24)) + "d ago"
	default:
		return itoa(int(d.Hours()/(24*7))) + "w ago"
	}
}

func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
