package core

import (
	"testing"
)

// operator_absorbed_test.go: a prompt typed while a turn is running and
// handed to that turn leaves a `queued_command` attachment and a queue
// removal with reason absorbed_mid_turn, and no `user` record. OperatorProse
// admits it through that pair. Every value in these fixtures is invented.

// absEnqueue, absRemove and absAttachment are the three records one absorbed
// prompt leaves. cmd is the queue command uuid the removal and the
// attachment share.
func absEnqueue(t *testing.T, sess string, ts int64, text string) string {
	t.Helper()
	return mineJSON(t, map[string]any{
		"type": "queue-operation", "operation": "enqueue",
		"timestamp": mineISO(ts), "sessionId": sess, "content": text,
	})
}

func absRemove(t *testing.T, sess string, ts int64, text, cmd, reason string) string {
	t.Helper()
	m := map[string]any{
		"type": "queue-operation", "operation": "remove",
		"timestamp": mineISO(ts), "sessionId": sess, "content": text,
		"commandUuid": cmd,
	}
	if reason != "" {
		m["reason"] = reason
	}
	return mineJSON(t, m)
}

func absAttachment(t *testing.T, uuid, sess string, ts int64, text, cmd string, extra map[string]any) string {
	t.Helper()
	att := map[string]any{
		"type": "queued_command", "prompt": text, "source_uuid": cmd,
		"commandMode": "prompt", "origin": map[string]any{"kind": "human"},
	}
	m := map[string]any{
		"type": "attachment", "uuid": uuid, "timestamp": mineISO(ts),
		"isSidechain": false, "session_id": sess, "sessionId": sess,
		"attachment": att,
	}
	for k, v := range extra {
		switch k {
		case "originKind":
			att["origin"] = map[string]any{"kind": v}
		case "commandMode":
			att["commandMode"] = v
		default:
			m[k] = v
		}
	}
	return mineJSON(t, m)
}

// absorbed is the usual three-record trio in the order the harness writes it.
func absorbed(t *testing.T, uuid, sess string, ts int64, text, cmd string) []string {
	t.Helper()
	return []string{
		absEnqueue(t, sess, ts, text),
		absRemove(t, sess, ts+1, text, cmd, absorbedReason),
		absAttachment(t, uuid, sess, ts, text, cmd, nil),
	}
}

func TestOperatorProseAdmitsAnAbsorbedMidTurnPrompt(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	lines := []string{
		mpcPrompt(t, "u-1", "sess-a", at(1), "FIRST-PROMPT start the build", nil),
		mpcAsst(t, at(2), "building"),
	}
	lines = append(lines, absorbed(t, "att-1", "sess-a", at(3), "MID-TURN use the wider cap", "cmd-1")...)
	lines = append(lines, mpcAsst(t, at(4), "switching to the wider cap"))
	path := mineWrite(t, projects, mpcPlainProject, "sess-a.jsonl", lines...)

	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin, Before: 5, After: 5})
	got := mpcTexts(res)
	if len(got) != 2 || got[0] != "FIRST-PROMPT start the build" || got[1] != "MID-TURN use the wider cap" {
		t.Fatalf("records = %q, want the typed prompt then the absorbed one", got)
	}
	r := mpcByUUID(t, res, "att-1")
	if r.Form != "queued" {
		t.Errorf("Form = %q, want queued", r.Form)
	}
	if r.Session != "sess-a" || r.TranscriptPath != path || r.TS != at(3) {
		t.Errorf("record = %+v, want session sess-a, path %s, ts %d", r, path, at(3))
	}
	// The reducer emitted the same text as [user mid-turn]; it is the
	// record's own section and must not be quoted back as its context.
	for _, b := range append(append([]ContextBlock{}, r.Context.Before...), r.Context.After...) {
		if b.Label == "[user mid-turn]" && b.Text == "MID-TURN use the wider cap" {
			t.Errorf("context repeats the record's own mid-turn section: %+v", r.Context)
		}
	}
	if len(r.Context.After) == 0 || r.Context.After[0].Text != "switching to the wider cap" {
		t.Errorf("Context.After = %+v, want the agent's reply first", r.Context.After)
	}
}

func TestOperatorProseAbsorbedPromptNeedsItsRemoval(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	const text = "QUEUED-PROMPT delivered at the turn's end"
	mineWrite(t, projects, mpcPlainProject, "sess-b.jsonl",
		mpcPrompt(t, "u-1", "sess-b", at(1), "FIRST-PROMPT", nil),
		absEnqueue(t, "sess-b", at(2), text),
		// delivered at the turn's end: an attachment, a plain dequeue, and
		// the `user` record that already carries the prompt
		absAttachment(t, "att-2", "sess-b", at(2), text, "cmd-2", nil),
		absRemove(t, "sess-b", at(3), text, "cmd-2", ""),
		mpcPrompt(t, "u-2", "sess-b", at(3), text, map[string]any{"promptSource": "queued"}),
		// a removal for some other command must not vouch for att-3
		absAttachment(t, "att-3", "sess-b", at(4), "ORPHAN-ATTACHMENT", "cmd-3", nil),
		absRemove(t, "sess-b", at(5), "something else", "cmd-other", absorbedReason),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	got := mpcTexts(res)
	if len(got) != 2 || got[0] != "FIRST-PROMPT" || got[1] != text {
		t.Fatalf("records = %q, want the queued prompt once and no orphan", got)
	}
	if r := mpcByUUID(t, res, "u-2"); r.Form != "queued" {
		t.Errorf("the delivered copy's Form = %q, want queued", r.Form)
	}
}

func TestOperatorProseAbsorbedRemovalMayFollowTheAttachment(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	const text = "LATE-REMOVAL keep the old name"
	mineWrite(t, projects, mpcPlainProject, "sess-c.jsonl",
		absAttachment(t, "att-4", "sess-c", at(1), text, "cmd-4", nil),
		absEnqueue(t, "sess-c", at(1), text),
		absRemove(t, "sess-c", at(2), text, "cmd-4", absorbedReason),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	if got := mpcTexts(res); len(got) != 1 || got[0] != text {
		t.Fatalf("records = %q, want the absorbed prompt", got)
	}
}

func TestOperatorProseDropsAbsorbedTextsThatAreNotTheOperators(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	var lines []string
	add := func(uuid, cmd, text string, extra map[string]any) {
		lines = append(lines,
			absEnqueue(t, "sess-d", at(1), text),
			absRemove(t, "sess-d", at(2), text, cmd, absorbedReason),
			absAttachment(t, uuid, "sess-d", at(1), text, cmd, extra),
		)
	}
	add("att-n1", "cmd-n1", "you stalled (zeus hit an API error) — pick up where you left off", nil)
	add("att-n2", "cmd-n2", "[kraken pr-watch — automated notice] a PR has merged", nil)
	add("att-n3", "cmd-n3", "<task-notification><task-id>t1</task-id></task-notification>",
		map[string]any{"originKind": "task-notification", "commandMode": "task-notification"})
	add("att-n4", "cmd-n4", "TEAMMATE-TEXT in a sidechain", map[string]any{"isSidechain": true})
	add("att-n5", "cmd-n6", "FORK-REPLAY of the parent's prompt", map[string]any{"session_id": "sess-parent"})
	add("att-ok", "cmd-ok", "OPERATOR-WORDS rename the flag", nil)
	mineWrite(t, projects, mpcPlainProject, "sess-d.jsonl", lines...)

	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	if got := mpcTexts(res); len(got) != 1 || got[0] != "OPERATOR-WORDS rename the flag" {
		t.Fatalf("records = %q, want only the operator's absorbed prompt", got)
	}
}

func TestOperatorProseAbsorbedPromptLeavesTheZeusFirstPromptSkipAlone(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	lines := []string{`{"type":"agent-name","agentName":"zeus-demo-12"}`}
	// absorbed before any `user` record: kept, and the staged spec that
	// follows is still the file's first prompt
	lines = append(lines, absorbed(t, "att-z", "zeus", at(1), "EARLY-STEER skip the mock", "cmd-z")...)
	lines = append(lines,
		mpcPrompt(t, "z1", "zeus", at(2), "STAGED-JOB-SPEC build the thing", nil),
		mpcPrompt(t, "z2", "zeus", at(3), "OPERATOR-STEER use the smaller cap", nil),
	)
	mineWrite(t, projects, mpcCellProject, "zeus.jsonl", lines...)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	got := mpcTexts(res)
	if len(got) != 2 || got[0] != "EARLY-STEER skip the mock" || got[1] != "OPERATOR-STEER use the smaller cap" {
		t.Fatalf("records = %q, want the absorbed steer and the later steer, never the staged spec", got)
	}
}

func TestOperatorProseAbsorbedPromptObeysTheWindowAndEmitsOnce(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	var lines []string
	lines = append(lines, absorbed(t, "att-old", "sess-e", since-5000, "BEFORE-THE-WINDOW", "cmd-old")...)
	lines = append(lines, absorbed(t, "att-in", "sess-e", at(1), "IN-THE-WINDOW", "cmd-in")...)
	lines = append(lines, absorbed(t, "att-late", "sess-e", at(60), "AFTER-THE-WINDOW", "cmd-late")...)
	mineWrite(t, projects, mpcPlainProject, "sess-e.jsonl", lines...)
	// the same attachment uuid replayed in a second file with no snake
	// session_id: the first file in sorted path order owns it
	mineWrite(t, projects, mpcPlainProject, "sess-f.jsonl",
		absRemove(t, "sess-f", at(2), "IN-THE-WINDOW", "cmd-in", absorbedReason),
		absAttachment(t, "att-in", "sess-f", at(1), "IN-THE-WINDOW", "cmd-in", map[string]any{"session_id": nil}),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Until: at(30), Login: mpcLogin})
	if got := mpcTexts(res); len(got) != 1 || got[0] != "IN-THE-WINDOW" {
		t.Fatalf("records = %q, want the in-window prompt once", got)
	}
	if r := mpcByUUID(t, res, "att-in"); r.Session != "sess-e" {
		t.Errorf("session = %q, want sess-e (first in sorted path order)", r.Session)
	}
}
