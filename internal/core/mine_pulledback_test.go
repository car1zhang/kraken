package core

import (
	"strings"
	"testing"
	"time"
)

// TestReduceTranscriptMarksPulledBackSteers: a message queued mid-turn and
// then pulled back into the input box before the turn ended is stored as an
// enqueue followed by a `popAll` queue-operation carrying the same text, with
// no queued_command attachment and no delivered user record after it. The
// enqueue is already on the page as [user mid-turn] when the popAll arrives,
// so the reducer emits the retraction as its own labelled section rather than
// leaving a steer on the page that the agent never received. The pulled-back
// text is released from the seen-set, so the operator's re-send of the same
// words renders again as a real steer, while an edited re-send renders on its
// own as before. A popAll whose text was never emitted stays dropped.
func TestReduceTranscriptMarksPulledBackSteers(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineUser(t, now-9000, "the opening ask"),
		// pulled back, then re-sent edited
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 8000), "operation": "enqueue", "content": "RETRACTED-STEER"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 7900), "operation": "popAll", "content": "RETRACTED-STEER"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 7800), "operation": "enqueue", "content": "RETRACTED-STEER, EDITED AND RE-SENT"}),
		mineJSON(t, map[string]any{"type": "attachment", "timestamp": mineISO(now - 7800), "attachment": map[string]any{"type": "queued_command", "prompt": "RETRACTED-STEER, EDITED AND RE-SENT", "origin": map[string]any{"kind": "human"}}}),
		// pulled back, then re-sent verbatim: the re-send is a new message
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 6000), "operation": "enqueue", "content": "VERBATIM-RESENT-STEER"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 5900), "operation": "popAll", "content": "VERBATIM-RESENT-STEER"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 5800), "operation": "enqueue", "content": "VERBATIM-RESENT-STEER"}),
		// a popAll of a queued task-notification was never emitted and stays dropped
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 4000), "operation": "enqueue", "content": "<task-notification>\n<task-id>a1</task-id>\nNOTIFICATION-NOISE"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 3900), "operation": "popAll", "content": "<task-notification>\n<task-id>a1</task-id>\nNOTIFICATION-NOISE"}),
		// a popAll with no enqueue before it has nothing to retract
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 3000), "operation": "popAll", "content": "NEVER-QUEUED-TEXT"}),
	)

	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)

	retracted := "[user mid-turn]\nRETRACTED-STEER\n\n[user mid-turn, pulled back]\nRETRACTED-STEER\n\n[user mid-turn]\nRETRACTED-STEER, EDITED AND RE-SENT"
	if !strings.Contains(got, retracted) {
		t.Errorf("pulled-back steer not marked between its enqueue and the edited re-send:\n%s", got)
	}
	if n := strings.Count(got, "[user mid-turn]\nVERBATIM-RESENT-STEER"); n != 2 {
		t.Errorf("verbatim re-send after a pull-back rendered %d times, want 2 (the pull-back releases the text from the seen-set):\n%s", n, got)
	}
	if n := strings.Count(got, "[user mid-turn, pulled back]\nVERBATIM-RESENT-STEER"); n != 1 {
		t.Errorf("verbatim pull-back marked %d times, want 1:\n%s", n, got)
	}
	for _, unwanted := range []string{"NOTIFICATION-NOISE", "NEVER-QUEUED-TEXT"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("reduced output leaked %q:\n%s", unwanted, got)
		}
	}
	if n := strings.Count(got, "pulled back]"); n != 2 {
		t.Errorf("pulled-back sections: %d, want exactly 2:\n%s", n, got)
	}
}
