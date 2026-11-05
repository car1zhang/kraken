package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// activity_regress_test.go — regression pins for review round one of the
// agent map (contract G1/G2, amendment 6): a prompt that merely quotes the
// teammate tag leaks none of its text into a hear event, a separator inside
// quotes starts no command, and the incremental TailEvents answers exactly
// what a fresh read of the whole file answers.

func regressWrite(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l); err != nil {
			t.Fatal(err)
		}
	}
}

func userLine(uuid, ts, text string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "uuid": uuid, "timestamp": ts, "message": map[string]any{"content": text}})
	return string(b) + "\n"
}

func callLine(id, ts, cmd string) string {
	b, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": ts, "message": map[string]any{
		"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}}}}})
	return string(b) + "\n"
}

func resultLine(uuid, ts, id string) string {
	b, _ := json.Marshal(map[string]any{"type": "user", "uuid": uuid, "timestamp": ts, "message": map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"}}}})
	return string(b) + "\n"
}

// dropEventsState forgets path's incremental state, so the next TailEvents
// is a fresh read of the whole file.
func dropEventsState(path string) {
	eventsStatesMu.Lock()
	delete(eventsStates, path)
	eventsStatesMu.Unlock()
}

func TestActivityRegressQuotedTeammateTagLeaksNothing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	prompts := []string{
		`can you check why <teammate-message teammate_id=" breaks the parser when my private note follows`,
		`quote: <teammate-message teammate_id="this whole private sentence" end`,
		`<teammate-message teammate_id="has spaces in it">x</teammate-message>`,
		`[from mac · cell two words] private`,
	}
	for i, txt := range prompts {
		regressWrite(t, p, userLine(fmt.Sprintf("u%d", i), "2026-11-05T10:00:00Z", txt))
	}
	evs, err := TailEvents(p, 10)
	if err != nil || len(evs) != len(prompts) {
		t.Fatalf("events %d err %v", len(evs), err)
	}
	for i, e := range evs {
		if e.From != "you" || e.Label != "prompt from you" || e.Who != "" {
			t.Errorf("prompt %d: %+v, want a plain prompt from you", i, e)
		}
	}
	regressWrite(t, p, userLine("ok", "2026-11-05T10:00:01Z", `  <teammate-message teammate_id="athena-logic" color="blue">body</teammate-message>`))
	evs, _ = TailEvents(p, 1)
	if len(evs) != 1 || evs[0].From != "teammate" || evs[0].Who != "athena-logic" || evs[0].Label != "from athena-logic" {
		t.Fatalf("a real delivery: %+v", evs)
	}
}

func TestActivityRegressRelayedTeammateDelivery(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	regressWrite(t, p,
		userLine("r1", "2026-11-05T10:00:00Z", "Another Claude session sent a message:\n<teammate-message teammate_id=\"hephaestus-go\" color=\"blue\">body</teammate-message>"),
		userLine("r2", "2026-11-05T10:00:01Z", "Another Claude session sent a message:\nplease read my private note <teammate-message teammate_id=\"private-words\">x</teammate-message>"),
		userLine("r3", "2026-11-05T10:00:02Z", "Another Claude session sent a message:<teammate-message teammate_id=\"nospace\">x</teammate-message>"),
	)
	evs, err := TailEvents(p, 10)
	if err != nil || len(evs) != 3 {
		t.Fatalf("events %d err %v", len(evs), err)
	}
	if e := evs[0]; e.From != "teammate" || e.Who != "hephaestus-go" || e.Label != "from hephaestus-go" {
		t.Errorf("relayed delivery: %+v", e)
	}
	for _, e := range evs[1:] {
		if e.From != "you" || e.Label != "prompt from you" || e.Who != "" {
			t.Errorf("%s: %+v, want a plain prompt from you", e.ID, e)
		}
	}
}

func TestActivityRegressQuotedSeparatorsStartNoCommand(t *testing.T) {
	for _, cmd := range []string{
		`echo "note; make sure the tokens rotate"`,
		`echo "status && git secretword rest"`,
		`printf 'x; kraken mm secretone secrettwo'`,
		`echo "unclosed; make secret`,
		`echo 'unclosed | git secretword`,
	} {
		in, _ := json.Marshal(map[string]string{"command": cmd})
		act, label := ClassifyToolCall("Bash", in)
		if act != "other" || label != "shell" {
			t.Errorf("%q -> %s %q, want other shell", cmd, act, label)
		}
	}
	// a quoted word is never a program or a subcommand, wherever it sits
	for _, tc := range [][3]string{
		{`git "secretword" x`, "review", "git"},
		{`kraken "mm" "secret"`, "home", "kraken"},
		{`"make" build`, "other", "shell"},
	} {
		in, _ := json.Marshal(map[string]string{"command": tc[0]})
		act, label := ClassifyToolCall("Bash", in)
		if act != tc[1] || label != tc[2] {
			t.Errorf("%q -> %s %q, want %s %q", tc[0], act, label, tc[1], tc[2])
		}
	}
}

// Review round two: a tab-indented `<<-` terminator, an unterminated
// heredoc, a partly quoted token and ANSI-C quoting each leaked a payload or
// quoted word into a Bash label.
func TestActivityRegressHeredocAndQuotingLeakNothing(t *testing.T) {
	for _, tc := range [][3]string{
		{"cat <<-EOF > /tmp/x\n\tfix; make secretword\n\tgit secrettwo\n\tEOF", "read", "cat"},
		{"cat <<EOF\nmake secretword\n", "read", "cat"},
		{"cat <<'EOF'\nmake secretword\nEOF\nmake build", "build", "make build"},
		{`git x"secretword"`, "review", "git"},
		{`echo $'a\'; make secretword'`, "other", "shell"},
	} {
		in, _ := json.Marshal(map[string]string{"command": tc[0]})
		act, label := ClassifyToolCall("Bash", in)
		if act != tc[1] || label != tc[2] {
			t.Errorf("%q -> %s %q, want %s %q", tc[0], act, label, tc[1], tc[2])
		}
	}
}

// Review round three: a heredoc inside a double-quoted `$(…)` whose body holds
// an odd number of `"` closed the outer string early and leaked the body's
// tail; and a `<<<` here-string or an arithmetic `<<` read as a heredoc
// swallowed the commands after it.
func TestActivityRegressSubstitutionHeredocAndShifts(t *testing.T) {
	for _, tc := range [][3]string{
		{"echo \"$(cat <<'EOF'\nfix 5\" display; git secretword\nEOF\n)\"", "other", "shell"},
		{"printf '%s' \"$(cat <<'EOF'\nsay \"hi; kraken mm secretone secrettwo\nEOF\n)\"", "other", "shell"},
		{"echo \"$(cat <<'EOF'\nan odd \" quote\nmake secretword\nEOF\n)\"", "other", "shell"},
		{"git commit -m \"$(cat <<'EOF'\nfix: the \" thing; make secretword\ngit secrettwo\nEOF\n)\"", "ship", "git commit"},
		{"kraken s zeus-other \"$(cat <<'EOF'\n5\" wide; go test ./...\nEOF\n)\"", "talk", "kraken s"},
		{"grep x <<< \"$v\"\ngo test ./...", "test", "go test"},
		{"grep x <<<secret\ngo test ./...", "test", "go test"},
		{"echo $((1<<3))\ngo test ./...", "test", "go test"},
	} {
		in, _ := json.Marshal(map[string]string{"command": tc[0]})
		act, label := ClassifyToolCall("Bash", in)
		if act != tc[1] || label != tc[2] {
			t.Errorf("%q -> %s %q, want %s %q", tc[0], act, label, tc[1], tc[2])
		}
	}
}

// Review round two: a file rewritten in place (same inode) to LARGER
// content resumed at the old offset and kept a stale event.
func TestActivityRegressSameInodeRewriteReadsAfresh(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	regressWrite(t, p, callLine("old1", "2026-11-05T10:00:00Z", "ls"))
	if evs, _ := TailEvents(p, 24); len(evs) != 1 || evs[0].ID != "old1" {
		t.Fatalf("before: %+v", evs)
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(callLine("new1", "2026-11-05T11:00:00Z", "pwd") + callLine("new2", "2026-11-05T11:00:01Z", "make build"))
	f.Close()
	inc, _ := TailEvents(p, 24)
	dropEventsState(p)
	fresh, _ := TailEvents(p, 24)
	if len(inc) != 2 || inc[0].ID != "new1" || !reflect.DeepEqual(inc, fresh) {
		t.Fatalf("after rewrite: %+v, fresh %+v", inc, fresh)
	}
}

func TestActivityRegressIncrementalEqualsFresh(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	regressWrite(t, p,
		userLine("u0", "2026-11-05T10:00:00Z", "start"),
		callLine("t1", "2026-11-05T10:00:01Z", "go test ./..."),
	)
	evs, _ := TailEvents(p, 24)
	if len(evs) != 2 || !evs[1].Open {
		t.Fatalf("first read: %+v", evs)
	}
	// the open call's result arrives, a record lands half-written, then whole
	half := resultLine("u1", "2026-11-05T10:00:04Z", "t1")
	regressWrite(t, p, half[:len(half)/2])
	if evs, _ = TailEvents(p, 24); !evs[1].Open {
		t.Fatalf("a half-written result closed the call: %+v", evs[1])
	}
	regressWrite(t, p, half[len(half)/2:], callLine("t2", "2026-11-05T10:00:05Z", "make build"))
	inc, _ := TailEvents(p, 24)
	if inc[1].Open || inc[1].DurMS != 3000 {
		t.Fatalf("the appended result did not close the call: %+v", inc[1])
	}
	dropEventsState(p)
	fresh, _ := TailEvents(p, 24)
	if !reflect.DeepEqual(inc, fresh) {
		t.Fatalf("incremental %+v\nfresh %+v", inc, fresh)
	}
	// more events than the state keeps, read in several appends
	for i := 0; i < 3*eventsKeep; i++ {
		regressWrite(t, p, callLine(fmt.Sprintf("b%d", i), "2026-11-05T10:01:00Z", "ls"))
		if i%97 == 0 {
			TailEvents(p, 24)
		}
	}
	inc, _ = TailEvents(p, 200)
	dropEventsState(p)
	fresh, _ = TailEvents(p, 200)
	if len(inc) != 200 || !reflect.DeepEqual(inc, fresh) {
		t.Fatalf("after many appends: incremental %d events, equal %v", len(inc), reflect.DeepEqual(inc, fresh))
	}
	// truncated and rewritten: read afresh, nothing stale survives
	if err := os.WriteFile(p, []byte(callLine("only", "2026-11-05T11:00:00Z", "pwd")), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, _ = TailEvents(p, 24)
	if len(evs) != 1 || evs[0].ID != "only" {
		t.Fatalf("after truncation: %+v", evs)
	}
	if evs[0].Act != "other" || evs[0].Label != "shell" {
		t.Fatalf("after truncation: %+v", evs[0])
	}
}
