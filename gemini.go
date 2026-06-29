package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// geminiAgent is the Google Gemini CLI backend. Like claude it assigns the
// session id at spawn (--session-id) and reports state through hooks — and its
// hooks are Claude-compatible, so it reuses the same /hook forwarder. Recaps
// aren't parsed yet (its chat log is a sparse incremental format), so the panel
// falls back to status labels.
type geminiAgent struct{}

func (geminiAgent) Kind() AgentKind { return AgentGemini }
func (geminiAgent) Command() string { return "gemini" }
func (geminiAgent) AssignsID() bool { return true }

func (geminiAgent) ModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return []string{"--model", model}
}

// FreshArgs: assign our id, skip the folder-trust prompt, submit a positional
// prompt (interactive when none).
func (geminiAgent) FreshArgs(sid, model, prompt string) []string {
	args := []string{"--session-id", sid, "--skip-trust"}
	args = append(args, geminiAgent{}.ModelArgs(model)...)
	if prompt != "" {
		args = append(args, prompt)
	}
	return args
}

// PromptArgs: unattended reviews auto-approve via --yolo (read-only rests on the
// prompt, like the other agents).
func (geminiAgent) PromptArgs(sid, model, prompt string, unattended bool) []string {
	args := []string{"--session-id", sid, "--skip-trust"}
	if unattended {
		args = append(args, unattendedArgs(AgentGemini)...)
	}
	args = append(args, geminiAgent{}.ModelArgs(model)...)
	return append(args, prompt)
}

// ResumeArgs re-opens a session by re-supplying its id.
func (geminiAgent) ResumeArgs(id string) []string {
	return []string{"--session-id", id, "--skip-trust"}
}

func (geminiAgent) Models() []ModelOption { return geminiModels }

func (geminiAgent) ModelLabel(id string) string {
	if id == "" {
		return "default"
	}
	return id
}

func (geminiAgent) PrettyModelID(id string) string { return id }

// TranscriptPath finds gemini's chat log for a session id. Files are named
// session-<ts>-<first8 of id>.jsonl under ~/.gemini/tmp/<project>/chats/.
func (geminiAgent) TranscriptPath(id string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || len(id) < 8 {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".gemini", "tmp", "*", "chats", "session-*-"+id[:8]+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func (geminiAgent) LastLine(path string) (string, string) { return geminiLastLine(path) }

// geminiLastLine returns the last assistant ("gemini") message text and the real
// model from gemini's chat JSONL. Records are either a session-meta line, a $set
// snapshot ({"$set":{"messages":[…]}}), or individual message records
// ({type, content, model, …}); an assistant record's content is a plain string.
func geminiLastLine(path string) (recap, model string) {
	last := ""
	consider := func(typ, text, mdl string) {
		if typ == "gemini" {
			if strings.TrimSpace(text) != "" {
				last = text
			}
			if mdl != "" {
				model = mdl
			}
		}
	}
	for _, ln := range tailLines(path, 256*1024) {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var rec struct {
			Type    string          `json:"type"`
			Content json.RawMessage `json:"content"`
			Model   string          `json:"model"`
			Set     *struct {
				Messages []struct {
					Type    string          `json:"type"`
					Content json.RawMessage `json:"content"`
					Model   string          `json:"model"`
				} `json:"messages"`
			} `json:"$set"`
		}
		if json.Unmarshal([]byte(ln), &rec) != nil {
			continue
		}
		if rec.Set != nil {
			for _, m := range rec.Set.Messages {
				consider(m.Type, geminiContentText(m.Content), m.Model)
			}
		}
		consider(rec.Type, geminiContentText(rec.Content), rec.Model)
	}
	return truncate(oneLine(last), 90), model
}

// geminiContentText extracts text from a message's content, which is a plain
// string for assistant messages and an array of {text} parts for user messages.
func geminiContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		out := ""
		for _, p := range parts {
			out += p.Text
		}
		return out
	}
	return ""
}

var geminiModels = []ModelOption{
	{ID: "", Label: "Default"},
	{ID: "gemini-2.5-pro", Label: "2.5 Pro"},
	{ID: "gemini-2.5-flash", Label: "2.5 Flash"},
}
