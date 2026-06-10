package main

import (
	"os"
	"path/filepath"
	"testing"
)

// a minimal rollout mirroring the real ~/.codex/sessions format
const sampleRollout = `{"timestamp":"2026-06-10T10:15:54.272Z","type":"session_meta","payload":{"id":"019eb108-086c-7ad2-b7fc-80c060ae7680","cwd":"/tmp/codex-probe","cli_version":"0.139.0","source":"exec","model_provider":"openai"}}
{"timestamp":"2026-06-10T10:15:54.272Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"timestamp":"2026-06-10T10:15:55.0Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"/tmp/codex-probe","model":"gpt-5.5","approval_policy":"never"}}
{"timestamp":"2026-06-10T10:15:55.1Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}
{"timestamp":"2026-06-10T10:15:55.5Z","type":"event_msg","payload":{"type":"agent_message","message":"all done here","phase":"final_answer"}}
{"timestamp":"2026-06-10T10:15:55.5Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"all done here"}]}}
{"timestamp":"2026-06-10T10:15:55.6Z","type":"event_msg","payload":{"type":"task_complete"}}
`

func writeRollout(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCodexState_finished(t *testing.T) {
	p := writeRollout(t, "rollout-done.jsonl", sampleRollout)
	state, recap, question, model := codexState(p)
	if state != StateIdle {
		t.Errorf("state = %q, want idle", state)
	}
	if recap != "all done here" {
		t.Errorf("recap = %q", recap)
	}
	if question != "" {
		t.Errorf("question = %q, want empty", question)
	}
	if model != "gpt-5.5" {
		t.Errorf("model = %q", model)
	}
}

// a turn paused on an approval: function_call needs escalation, no output, no
// task_complete → StatePerm with the justification as the question.
const approvalRollout = `{"type":"session_meta","payload":{"id":"abc","cwd":"/tmp/x"}}
{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}
{"type":"turn_context","payload":{"turn_id":"t1","model":"gpt-5.5"}}
{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call_1","arguments":"{\"cmd\":\"printf hi > /tmp/x.txt\",\"sandbox_permissions\":\"require_escalated\",\"justification\":\"Allow writing to /tmp/x.txt?\"}"}}
`

func TestCodexState_waitingForApproval(t *testing.T) {
	p := writeRollout(t, "rollout-approve.jsonl", approvalRollout)
	state, _, question, _ := codexState(p)
	if state != StatePerm {
		t.Fatalf("state = %q, want perm", state)
	}
	if question != "Allow writing to /tmp/x.txt?" {
		t.Errorf("question = %q", question)
	}
}

func TestCodexState_approvalResolved(t *testing.T) {
	// same approval, then the output arrives and the turn completes → idle
	resolved := approvalRollout +
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"call_1"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"agent_message","message":"wrote it"}}` + "\n" +
		`{"type":"event_msg","payload":{"type":"task_complete"}}` + "\n"
	p := writeRollout(t, "rollout-resolved.jsonl", resolved)
	state, recap, _, _ := codexState(p)
	if state != StateIdle {
		t.Errorf("state = %q, want idle", state)
	}
	if recap != "wrote it" {
		t.Errorf("recap = %q", recap)
	}
}

func TestCodexState_working(t *testing.T) {
	// task started, no completion, no pending approval → working
	body := `{"type":"session_meta","payload":{"id":"a","cwd":"/x"}}
{"type":"event_msg","payload":{"type":"task_started"}}
{"type":"turn_context","payload":{"model":"gpt-5.5"}}
`
	p := writeRollout(t, "rollout-working.jsonl", body)
	if state, _, _, _ := codexState(p); state != StateWorking {
		t.Errorf("state = %q, want working", state)
	}
}

func TestCodexRolloutMeta(t *testing.T) {
	p := writeRollout(t, "rollout-x.jsonl", sampleRollout)
	id, cwd := codexRolloutMeta(p)
	if id != "019eb108-086c-7ad2-b7fc-80c060ae7680" {
		t.Errorf("id = %q", id)
	}
	if cwd != "/tmp/codex-probe" {
		t.Errorf("cwd = %q", cwd)
	}
}

func TestCodexFreshArgs(t *testing.T) {
	a := codexAgent{}
	// no model → no -m, but keep --no-alt-screen
	if got := a.FreshArgs("ignored-sid", "", ""); len(got) != 1 || got[0] != "--no-alt-screen" {
		t.Errorf("FreshArgs(no model) = %v", got)
	}
	// with model → -m appended, sid ignored (codex mints its own)
	got := a.FreshArgs("ignored-sid", "gpt-5.5", "do a thing")
	want := []string{"--no-alt-screen", "-m", "gpt-5.5", "do a thing"}
	if len(got) != len(want) {
		t.Fatalf("FreshArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("FreshArgs = %v, want %v", got, want)
		}
	}
}
