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
		// a pulled-back teammate envelope keeps the teammate label
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 2000), "operation": "enqueue", "content": "<teammate-message teammate_id=\"hephaestus-a\">QUEUED-TEAMMATE-TEXT</teammate-message>"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 1900), "operation": "popAll", "content": "<teammate-message teammate_id=\"hephaestus-a\">QUEUED-TEAMMATE-TEXT</teammate-message>"}),
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
	if !strings.Contains(got, "[teammate hephaestus-a mid-turn, pulled back]\nQUEUED-TEAMMATE-TEXT") {
		t.Errorf("pulled-back teammate envelope not routed to the teammate label:\n%s", got)
	}
	if strings.Contains(got, "[user mid-turn, pulled back]\n<teammate-message") {
		t.Errorf("pulled-back teammate envelope attributed to the operator:\n%s", got)
	}
	if n := strings.Count(got, "pulled back]"); n != 3 {
		t.Errorf("pulled-back sections: %d, want exactly 3:\n%s", n, got)
	}
}

// TestReduceTranscriptPulledBackAfterDelivery: a text queued and DELIVERED,
// then re-queued verbatim (collapsed by the seen-set, never on the page) and
// pulled back, then re-queued and delivered again. The popAll retracts an
// enqueue that never rendered, so printing a retraction would land it under
// the earlier steer the agent did receive; it emits nothing and releases both
// ledgers, so the final re-send renders once as a mid-turn steer and its own
// delivered user record is consumed rather than rendered a second time: two
// messages, two sections.
func TestReduceTranscriptPulledBackAfterDelivery(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineUser(t, now-9000, "the opening ask"),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 8000), "operation": "enqueue", "content": "GO-ON-STEER"}),
		mineUser(t, now-7000, "GO-ON-STEER"),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 6000), "operation": "enqueue", "content": "GO-ON-STEER"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 5900), "operation": "popAll", "content": "GO-ON-STEER"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 5000), "operation": "enqueue", "content": "GO-ON-STEER"}),
		mineUser(t, now-4000, "GO-ON-STEER"),
	)

	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)

	if strings.Contains(got, "pulled back]") {
		t.Errorf("a popAll of an already-delivered text printed a retraction:\n%s", got)
	}
	if n := strings.Count(got, "[user mid-turn]\nGO-ON-STEER"); n != 2 {
		t.Errorf("two queued-and-delivered messages rendered %d mid-turn sections, want 2:\n%s", n, got)
	}
	if n := strings.Count(got, "GO-ON-STEER"); n != 2 {
		t.Errorf("the steer text appears %d times, want 2 (each delivery consumed by its enqueue):\n%s", n, got)
	}
}
