package main

import "testing"

const sampleGeminiChat = `{"sessionId":"1119e246-5f32-4449-8fdf-d13ac5e7b6a7","projectHash":"abc","startTime":"2026-06-11T07:36:28.278Z","kind":"main"}
{"$set":{"messages":[{"id":"m0","type":"user","content":[{"text":"<session_context>setup</session_context>"}]}]}}
{"id":"u1","type":"user","content":[{"text":"Run the shell command: echo hello"}]}
{"id":"g1","type":"gemini","content":"","thoughts":[],"model":"gemini-3-flash-preview","toolCalls":[{"name":"run_shell"}]}
{"id":"g2","type":"gemini","content":"Done — it printed hello.","model":"gemini-3-flash-preview"}
`

func TestGeminiLastLine(t *testing.T) {
	p := writeRollout(t, "session-2026-06-11T07-36-1119e246.jsonl", sampleGeminiChat)
	recap, model := geminiLastLine(p)
	if recap != "Done — it printed hello." {
		t.Errorf("recap = %q (should be the last non-empty gemini message)", recap)
	}
	if model != "gemini-3-flash-preview" {
		t.Errorf("model = %q", model)
	}
}
