package core

// Blind logic tests for slice A of the dream 2026-09-28 contract: a
// transcript record naming another session is owned by the file when it was
// written at or after the file's creation time. Authored from the contract
// text alone (artifacts/50-contract.md), before the implementation existed.
//
// Every record timestamp is derived from the wall clock read at test time,
// because a file's creation time is real: a record meant to predate the file
// is dated two hours before, a record meant to follow creation is dated
// minutes after.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// --- fixture helpers (contractOwnerBorn prefix: shared package with the edge author) ---

const (
	contractOwnerBornMin   = int64(60_000)
	contractOwnerBornSince = int64(6 * time.Hour / time.Millisecond)
)

// contractOwnerBornWithSession stamps `session_id` onto an already-serialized
// record, the snake-case field naming the process that wrote it.
func contractOwnerBornWithSession(t *testing.T, line, sid string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("reparse fixture: %v", err)
	}
	m["session_id"] = sid
	return mineJSON(t, m)
}

// contractOwnerBornTurn is a turn_duration system record with no session_id,
// the real shape of that record.
func contractOwnerBornTurn(t *testing.T, ts, ms int64) string {
	t.Helper()
	return mineJSON(t, map[string]any{"type": "system", "subtype": "turn_duration",
		"durationMs": ms, "timestamp": mineISO(ts)})
}

// contractOwnerBornSum adds the four counters of two passes.
func contractOwnerBornSum(a, b Tokens) [4]int64 {
	return [4]int64{a.In + b.In, a.Out + b.Out, a.CacheWrite + b.CacheWrite, a.CacheRead + b.CacheRead}
}

func contractOwnerBornFour(tk Tokens) [4]int64 {
	return [4]int64{tk.In, tk.Out, tk.CacheWrite, tk.CacheRead}
}

// contractOwnerBornShape is one of the three file shapes, with the lines it
// is written with and the four counters its obligation implies.
type contractOwnerBornShape struct {
	name  string
	stem  string
	lines []string
	want  [4]int64
}

// contractOwnerBornShapes builds the three shapes relative to now (read
// BEFORE any file is created). Each carries a multi-record message group so
// a split between passes can land inside one.
func contractOwnerBornShapes(t *testing.T, now int64) []contractOwnerBornShape {
	t.Helper()
	before := now - 120*contractOwnerBornMin
	after := func(mins int64) int64 { return now + mins*contractOwnerBornMin }

	// Continuation: new file, every record names the launching process'
	// earlier session and is dated after creation.
	cont := []string{
		tokAssistantFull(t, after(2), tokUsage{in: 3, out: 10, cw: 100, cr: 1000}, "msg_c1", "earlier-sess"),
		tokAssistantFull(t, after(2), tokUsage{in: 3, out: 40, cw: 100, cr: 1000}, "msg_c1", "earlier-sess"),
		contractOwnerBornWithSession(t, mineUser(t, after(3), []any{mineToolResult("x", false, "ok")}), "earlier-sess"),
		tokAssistantFull(t, after(4), tokUsage{in: 5, out: 7, cw: 20, cr: 200}, "msg_c2", "earlier-sess"),
	}
	// Fork: records copied from the parent, dated before creation, then the
	// file's own records.
	fork := []string{
		tokAssistantFull(t, before, tokUsage{in: 900, out: 9000, cw: 90000, cr: 900000}, "msg_p1", "parent-sess"),
		tokAssistantFull(t, before+1000, tokUsage{in: 800, out: 8000, cw: 80000, cr: 800000}, "msg_p2", "parent-sess"),
		tokAssistantFull(t, after(2), tokUsage{in: 2, out: 11, cw: 30, cr: 300}, "msg_f1", "fork-shape"),
		tokAssistantFull(t, after(2), tokUsage{in: 2, out: 33, cw: 30, cr: 300}, "msg_f1", "fork-shape"),
		tokAssistantFull(t, after(3), tokUsage{in: 1, out: 4, cw: 5, cr: 6}, "msg_f2", "fork-shape"),
	}
	// Resume: the file's own records, later joined by records a different
	// process wrote, dated after creation.
	res := []string{
		tokAssistantFull(t, after(2), tokUsage{in: 4, out: 12, cw: 40, cr: 400}, "msg_r1", "resume-shape"),
		tokAssistantFull(t, after(3), tokUsage{in: 6, out: 20, cw: 60, cr: 600}, "msg_r2", "other-proc"),
		tokAssistantFull(t, after(3), tokUsage{in: 6, out: 25, cw: 60, cr: 600}, "msg_r2", "other-proc"),
		tokAssistantFull(t, after(4), tokUsage{in: 1, out: 1, cw: 1, cr: 1}, "msg_r3", "other-proc"),
	}
	return []contractOwnerBornShape{
		{"continuation", "cont-shape", cont, [4]int64{3 + 5, 40 + 7, 100 + 20, 1000 + 200}},
		{"fork", "fork-shape", fork, [4]int64{2 + 1, 33 + 4, 30 + 5, 300 + 6}},
		{"resume", "resume-shape", res, [4]int64{4 + 6 + 1, 12 + 25 + 1, 40 + 60 + 1, 400 + 600 + 1}},
	}
}

// --- tokenRecord ---

// TestContractOwnerBornTokenRecordDecodesTimestamp: decoding a record line
// fills Timestamp with the record's timestamp text.
func TestContractOwnerBornTokenRecordDecodesTimestamp(t *testing.T) {
	ts := "2026-09-28T14:03:05.123Z"
	var rec tokenRecord
	line := `{"type":"assistant","session_id":"abc","timestamp":"` + ts + `","message":{"id":"m"}}`
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if rec.Timestamp != ts {
		t.Fatalf("Timestamp = %q, want %q", rec.Timestamp, ts)
	}
	if rec.SessionID != "abc" {
		t.Fatalf("SessionID = %q, want abc", rec.SessionID)
	}
}

// --- tokenRecordOwned ---

// TestContractOwnerBornEmptySessionIsOwned pins A1.
func TestContractOwnerBornEmptySessionIsOwned(t *testing.T) {
	born := time.Now().UnixMilli()
	for _, own := range []fileOwner{{ID: "file-sess", Born: born}, {ID: "file-sess", Born: 0}} {
		for _, ts := range []string{mineISO(born - 120*contractOwnerBornMin), mineISO(born + 2*contractOwnerBornMin)} {
			if !tokenRecordOwned(tokenRecord{SessionID: "", Timestamp: ts}, own) {
				t.Errorf("record with no session_id at %s, owner %+v: not owned, want owned", ts, own)
			}
		}
	}
}

// TestContractOwnerBornOwnSessionIsOwnedEvenBeforeBirth pins A2.
func TestContractOwnerBornOwnSessionIsOwnedEvenBeforeBirth(t *testing.T) {
	born := time.Now().UnixMilli()
	own := fileOwner{ID: "file-sess", Born: born}
	for _, ts := range []int64{born - 120*contractOwnerBornMin, born + 2*contractOwnerBornMin} {
		if !tokenRecordOwned(tokenRecord{SessionID: "file-sess", Timestamp: mineISO(ts)}, own) {
			t.Errorf("own-session record at %s: not owned, want owned", mineISO(ts))
		}
	}
}

// TestContractOwnerBornForeignAfterBirthIsOwned pins A3's ordinary path.
func TestContractOwnerBornForeignAfterBirthIsOwned(t *testing.T) {
	born := time.Now().UnixMilli()
	own := fileOwner{ID: "file-sess", Born: born}
	rec := tokenRecord{SessionID: "other-sess", Timestamp: mineISO(born + 2*contractOwnerBornMin)}
	if !tokenRecordOwned(rec, own) {
		t.Fatal("foreign record dated after the file's creation: not owned, want owned")
	}
}

// TestContractOwnerBornForeignBeforeBirthIsNotOwned pins A4.
func TestContractOwnerBornForeignBeforeBirthIsNotOwned(t *testing.T) {
	born := time.Now().UnixMilli()
	own := fileOwner{ID: "file-sess", Born: born}
	rec := tokenRecord{SessionID: "parent-sess", Timestamp: mineISO(born - 120*contractOwnerBornMin)}
	if tokenRecordOwned(rec, own) {
		t.Fatal("foreign record dated before the file's creation: owned, want not owned")
	}
}

// TestContractOwnerBornForeignWithUnknownBirthIsNotOwned pins A5.
func TestContractOwnerBornForeignWithUnknownBirthIsNotOwned(t *testing.T) {
	now := time.Now().UnixMilli()
	own := fileOwner{ID: "file-sess", Born: 0}
	for _, ts := range []int64{now - 120*contractOwnerBornMin, now + 2*contractOwnerBornMin} {
		if tokenRecordOwned(tokenRecord{SessionID: "other-sess", Timestamp: mineISO(ts)}, own) {
			t.Errorf("foreign record at %s with Born unknown: owned, want not owned", mineISO(ts))
		}
	}
}

// --- fileBorn ---

// TestContractOwnerBornFileBornIsCreationTime pins A7.
func TestContractOwnerBornFileBornIsCreationTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "born.jsonl")
	lo := time.Now().UnixMilli()
	f, err := os.Create(path)
	hi := time.Now().UnixMilli()
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	got := fileBorn(f)
	if got < lo-1000 || got > hi+1000 {
		t.Fatalf("fileBorn = %d, want within [%d, %d]", got, lo-1000, hi+1000)
	}
}

// TestContractOwnerBornAppendDoesNotMoveBirth pins A8: appending later leaves
// the value unchanged, whether read through the same handle or a fresh one.
func TestContractOwnerBornAppendDoesNotMoveBirth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "born.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.WriteString("{\"type\":\"user\"}\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	first := fileBorn(f)
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	time.Sleep(1100 * time.Millisecond)
	tokAppend(t, path, `{"type":"user"}`, `{"type":"assistant"}`)

	g, err := os.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer g.Close()
	if second := fileBorn(g); second != first {
		t.Fatalf("fileBorn after append = %d, want unchanged %d", second, first)
	}

	a, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer a.Close()
	if _, err := a.WriteString("{\"type\":\"user\"}\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	if third := fileBorn(a); third != first {
		t.Fatalf("fileBorn through the appending handle = %d, want unchanged %d", third, first)
	}
}

// --- SumTokensDelta ---

// contractOwnerBornSumOne writes one shape into its own project dir and sums it.
func contractOwnerBornSumOne(t *testing.T, projects string, s contractOwnerBornShape) Tokens {
	t.Helper()
	project := "-" + s.stem
	mineWrite(t, projects, project, s.stem+".jsonl", s.lines...)
	got, _, err := SumTokensDelta(filepath.Join(projects, project), nil)
	if err != nil {
		t.Fatalf("%s: SumTokensDelta: %v", s.name, err)
	}
	return got
}

// TestContractOwnerBornContinuationCountsEveryMessage pins A10.
func TestContractOwnerBornContinuationCountsEveryMessage(t *testing.T) {
	projects := mineHome(t)
	s := contractOwnerBornShapes(t, time.Now().UnixMilli())[0]
	got := contractOwnerBornSumOne(t, projects, s)
	if contractOwnerBornFour(got) != s.want {
		t.Fatalf("continuation = %v, want %v (every message's four counters)", contractOwnerBornFour(got), s.want)
	}
}

// TestContractOwnerBornForkCountsOnlyItsOwn pins A11.
func TestContractOwnerBornForkCountsOnlyItsOwn(t *testing.T) {
	projects := mineHome(t)
	s := contractOwnerBornShapes(t, time.Now().UnixMilli())[1]
	got := contractOwnerBornSumOne(t, projects, s)
	if contractOwnerBornFour(got) != s.want {
		t.Fatalf("fork = %v, want %v (own records only, copied ones contribute nothing)", contractOwnerBornFour(got), s.want)
	}
}

// TestContractOwnerBornResumeCountsBothProcesses pins A12.
func TestContractOwnerBornResumeCountsBothProcesses(t *testing.T) {
	projects := mineHome(t)
	s := contractOwnerBornShapes(t, time.Now().UnixMilli())[2]
	got := contractOwnerBornSumOne(t, projects, s)
	if contractOwnerBornFour(got) != s.want {
		t.Fatalf("resume = %v, want %v (own and later foreign messages)", contractOwnerBornFour(got), s.want)
	}
}

// TestContractOwnerBornResumedForkSkipsOnlyTheCopies pins A13.
func TestContractOwnerBornResumedForkSkipsOnlyTheCopies(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	before := now - 120*contractOwnerBornMin
	after := func(mins int64) int64 { return now + mins*contractOwnerBornMin }
	mineWrite(t, projects, "-rf", "resumed-fork.jsonl",
		tokAssistantFull(t, before, tokUsage{in: 700, out: 7000, cw: 70000, cr: 700000}, "msg_p", "parent-sess"),
		tokAssistantFull(t, after(2), tokUsage{in: 1, out: 10, cw: 100, cr: 1000}, "msg_own", "resumed-fork"),
		tokAssistantFull(t, after(5), tokUsage{in: 2, out: 20, cw: 200, cr: 2000}, "msg_later", "resumer-sess"),
	)
	got, _, err := SumTokensDelta(filepath.Join(projects, "-rf"), nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	if want := [4]int64{3, 30, 300, 3000}; contractOwnerBornFour(got) != want {
		t.Fatalf("resumed fork = %v, want %v (own + later, never the copy)", contractOwnerBornFour(got), want)
	}
}

// TestContractOwnerBornForeignGroupDedupes pins A14: a message.id spread over
// several owned records naming another session contributes input and both
// cache counters once, and output as the last record's value.
func TestContractOwnerBornForeignGroupDedupes(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	at := now + 2*contractOwnerBornMin
	mineWrite(t, projects, "-grp", "grp-file.jsonl",
		tokAssistantFull(t, at, tokUsage{in: 9, out: 2, cw: 500, cr: 40000}, "msg_G", "earlier-sess"),
		contractOwnerBornWithSession(t, mineUser(t, at, []any{mineToolResult("t1", false, "ok")}), "earlier-sess"),
		tokAssistantFull(t, at, tokUsage{in: 9, out: 180, cw: 500, cr: 40000}, "msg_G", "earlier-sess"),
		tokAssistantFull(t, at, tokUsage{in: 9, out: 3241, cw: 500, cr: 40000}, "msg_G", "earlier-sess"),
	)
	got, _, err := SumTokensDelta(filepath.Join(projects, "-grp"), nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	if want := [4]int64{9, 3241, 500, 40000}; contractOwnerBornFour(got) != want {
		t.Fatalf("foreign group = %v, want %v (once; output = last record's)", contractOwnerBornFour(got), want)
	}
}

// TestContractOwnerBornTwoPassesEqualOne pins A15: for each shape, every
// split at a record boundary summed in two passes equals one pass. Each split
// gets a freshly created file, since the creation time is real.
func TestContractOwnerBornTwoPassesEqualOne(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	for _, s := range contractOwnerBornShapes(t, now) {
		one := contractOwnerBornSumOne(t, projects, s)
		if contractOwnerBornFour(one) != s.want {
			t.Fatalf("%s one pass = %v, want %v", s.name, contractOwnerBornFour(one), s.want)
		}
		for k := 1; k < len(s.lines); k++ {
			project := "-" + s.stem + "-split-" + string(rune('a'+k))
			path := mineWrite(t, projects, project, s.stem+".jsonl", s.lines[:k]...)
			dir := filepath.Join(projects, project)
			first, offs, err := SumTokensDelta(dir, nil)
			if err != nil {
				t.Fatalf("%s split %d first pass: %v", s.name, k, err)
			}
			tokAppend(t, path, s.lines[k:]...)
			second, _, err := SumTokensDelta(dir, offs)
			if err != nil {
				t.Fatalf("%s split %d second pass: %v", s.name, k, err)
			}
			if got := contractOwnerBornSum(first, second); got != s.want {
				t.Errorf("%s split after record %d: two passes = %v, one pass = %v", s.name, k, got, s.want)
			}
		}
	}
}

// TestContractOwnerBornSidecarWithoutSessionCountsFully pins A16.
func TestContractOwnerBornSidecarWithoutSessionCountsFully(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	mineWrite(t, projects, "-side", filepath.Join("lead-sess", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now-120*contractOwnerBornMin, tokUsage{in: 1, out: 10, cw: 100, cr: 1000}, "msg_s1", ""),
		tokAssistantFull(t, now+2*contractOwnerBornMin, tokUsage{in: 2, out: 20, cw: 200, cr: 2000}, "msg_s2", ""),
	)
	got, _, err := SumTokensDelta(filepath.Join(projects, "-side"), nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	if want := [4]int64{3, 30, 300, 3000}; contractOwnerBornFour(got) != want {
		t.Fatalf("sidecar = %v, want %v (no session_id: contributes fully)", contractOwnerBornFour(got), want)
	}
}

// --- MineTranscripts ---

// TestContractOwnerBornDigestTokensMatchTheSum pins A17: per shape, the
// digest's four counters equal SumTokensDelta over a dir holding only that
// file, and the value the shape's obligation implies.
func TestContractOwnerBornDigestTokensMatchTheSum(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	shapes := contractOwnerBornShapes(t, now)
	for _, s := range shapes {
		mineWrite(t, projects, "-"+s.stem, s.stem+".jsonl", s.lines...)
	}
	res, err := MineTranscripts(now - contractOwnerBornSince)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	for _, s := range shapes {
		sum, _, err := SumTokensDelta(filepath.Join(projects, "-"+s.stem), nil)
		if err != nil {
			t.Fatalf("%s: SumTokensDelta: %v", s.name, err)
		}
		d := mineFindDigest(t, res, s.stem+".jsonl")
		dig := [4]int64{d.In, d.Out, d.CacheWrite, d.CacheRead}
		if dig != contractOwnerBornFour(sum) {
			t.Errorf("%s: digest %v != SumTokensDelta %v", s.name, dig, contractOwnerBornFour(sum))
		}
		if dig != s.want {
			t.Errorf("%s: digest %v, want %v", s.name, dig, s.want)
		}
	}
}

// contractOwnerBornForeignSignals is the four mechanical signals A18 names,
// written by session sid at times after creation.
func contractOwnerBornForeignSignals(t *testing.T, sid string, at func(int64) int64, idp string) []string {
	t.Helper()
	return []string{
		contractOwnerBornWithSession(t, mineAssistant(t, at(10), "claude-opus-5",
			mineToolUse(idp+"1", "Bash", map[string]any{"command": "go vet ./..."})), sid),
		contractOwnerBornWithSession(t, mineUser(t, at(11), []any{mineToolResult(idp+"1", true, "boom")}), sid),
		contractOwnerBornWithSession(t, mineUser(t, at(12), "[Request interrupted by user]"), sid),
		contractOwnerBornTurn(t, at(13), 4_200), // no session_id: inherits sid
	}
}

// TestContractOwnerBornDigestCountsLaterForeignSignals pins A18 on the
// continuation and resume shapes.
func TestContractOwnerBornDigestCountsLaterForeignSignals(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	after := func(mins int64) int64 { return now + mins*contractOwnerBornMin }

	mineWrite(t, projects, "-sig", "sig-cont.jsonl",
		contractOwnerBornForeignSignals(t, "earlier-sess", after, "c")...)

	resume := []string{
		contractOwnerBornWithSession(t, mineAssistant(t, after(2), "claude-opus-5",
			mineToolUse("own1", "Read", map[string]any{"file_path": "/tmp/a.go"})), "sig-resume"),
		contractOwnerBornWithSession(t, mineUser(t, after(3), []any{mineToolResult("own1", false, "ok")}), "sig-resume"),
	}
	resume = append(resume, contractOwnerBornForeignSignals(t, "other-proc", after, "r")...)
	mineWrite(t, projects, "-sig", "sig-resume.jsonl", resume...)

	res, err := MineTranscripts(now - contractOwnerBornSince)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	for _, name := range []string{"sig-cont.jsonl", "sig-resume.jsonl"} {
		d := mineFindDigest(t, res, name)
		if d.ToolCounts["Bash"] != 1 {
			t.Errorf("%s: ToolCounts[Bash] = %d, want 1", name, d.ToolCounts["Bash"])
		}
		if d.ErrorCounts["Bash"] != 1 {
			t.Errorf("%s: ErrorCounts[Bash] = %d, want 1", name, d.ErrorCounts["Bash"])
		}
		if d.Interrupts != 1 {
			t.Errorf("%s: Interrupts = %d, want 1", name, d.Interrupts)
		}
		if len(d.TurnDurations) != 1 || d.TurnDurations[0] != 4_200 {
			t.Errorf("%s: TurnDurations = %v, want [4200]", name, d.TurnDurations)
		}
	}
	if d := mineFindDigest(t, res, "sig-resume.jsonl"); d.ToolCounts["Read"] != 1 {
		t.Errorf("sig-resume.jsonl: ToolCounts[Read] = %d, want 1 (the file's own call)", d.ToolCounts["Read"])
	}
}

// TestContractOwnerBornDigestSkipsCopiedSignals pins A19: in a fork, the
// copied records' tool calls, errors, interrupts, synthetic records,
// output-ceiling stops and turn durations are not counted; the file's own are.
func TestContractOwnerBornDigestSkipsCopiedSignals(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	before := func(secs int64) int64 { return now - 120*contractOwnerBornMin + secs*1000 }
	after := func(mins int64) int64 { return now + mins*contractOwnerBornMin }
	p := "parent-sess"
	mineWrite(t, projects, "-sig", "sig-fork.jsonl",
		contractOwnerBornWithSession(t, mineAssistant(t, before(1), "claude-opus-5",
			mineToolUse("p1", "Bash", map[string]any{"command": "go vet ./..."})), p),
		contractOwnerBornWithSession(t, mineUser(t, before(2), []any{mineToolResult("p1", true, "boom")}), p),
		contractOwnerBornWithSession(t, mineUser(t, before(3), "[Request interrupted by user]"), p),
		contractOwnerBornWithSession(t, mineAssistant(t, before(4), "<synthetic>", mineText("API Error")), p),
		contractOwnerBornWithSession(t, mineJSON(t, map[string]any{"type": "assistant", "timestamp": mineISO(before(5)),
			"message": map[string]any{"id": "msg-ceil", "model": "claude-opus-5", "stop_reason": "max_tokens",
				"content": []any{mineText("cut")}}}), p),
		contractOwnerBornTurn(t, before(6), 99_000), // inherits parent, dated before creation
		// the fork's own work
		contractOwnerBornWithSession(t, mineAssistant(t, after(2), "claude-opus-5",
			mineToolUse("f1", "Read", map[string]any{"file_path": "/tmp/a.go"})), "sig-fork"),
		contractOwnerBornWithSession(t, mineUser(t, after(3), []any{mineToolResult("f1", true, "gone")}), "sig-fork"),
		contractOwnerBornTurn(t, after(4), 1_000), // inherits the fork's own session
	)
	res, err := MineTranscripts(now - contractOwnerBornSince)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "sig-fork.jsonl")
	if d.ToolCounts["Bash"] != 0 || d.ToolCounts["Read"] != 1 {
		t.Errorf("ToolCounts = %v, want only Read:1", d.ToolCounts)
	}
	if d.ErrorCounts["Bash"] != 0 || d.ErrorCounts["Read"] != 1 {
		t.Errorf("ErrorCounts = %v, want only Read:1", d.ErrorCounts)
	}
	if d.Interrupts != 0 {
		t.Errorf("Interrupts = %d, want 0", d.Interrupts)
	}
	if d.SyntheticErrors != 0 {
		t.Errorf("SyntheticErrors = %d, want 0", d.SyntheticErrors)
	}
	if d.MaxTokensStops != 0 {
		t.Errorf("MaxTokensStops = %d, want 0", d.MaxTokensStops)
	}
	if len(d.TurnDurations) != 1 || d.TurnDurations[0] != 1_000 {
		t.Errorf("TurnDurations = %v, want [1000] (the fork's own only)", d.TurnDurations)
	}
}

// TestContractOwnerBornUnstampedRecordInheritsWithItsOwnTime pins A20: a
// record with no session_id is judged as if it named the most recent
// session_id seen earlier, using ITS OWN timestamp. After copied parent
// records, an unstamped record dated before creation is not counted, while
// one dated after creation is.
func TestContractOwnerBornUnstampedRecordInheritsWithItsOwnTime(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	before := now - 120*contractOwnerBornMin
	after := func(mins int64) int64 { return now + mins*contractOwnerBornMin }
	p := "parent-sess"
	mineWrite(t, projects, "-inh", "inherit-file.jsonl",
		contractOwnerBornWithSession(t, mineAssistant(t, before, "claude-opus-5",
			mineToolUse("p1", "Bash", map[string]any{"command": "ls"})), p),
		mineUser(t, before+1000, "[Request interrupted by user]"), // inherits parent, before creation: not counted
		contractOwnerBornTurn(t, before+2000, 77_000),             // inherits parent, before creation: not counted
		contractOwnerBornTurn(t, after(2), 5_000),                 // inherits parent, AFTER creation: counted
		mineUser(t, after(3), "[Request interrupted by user]"),    // inherits parent, after creation: counted
	)
	res, err := MineTranscripts(now - contractOwnerBornSince)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "inherit-file.jsonl")
	if d.ToolCounts["Bash"] != 0 {
		t.Errorf("ToolCounts[Bash] = %d, want 0 (copied)", d.ToolCounts["Bash"])
	}
	if d.Interrupts != 1 {
		t.Errorf("Interrupts = %d, want 1 (only the one dated after creation)", d.Interrupts)
	}
	if len(d.TurnDurations) != 1 || d.TurnDurations[0] != 5_000 {
		t.Errorf("TurnDurations = %v, want [5000] (only the one dated after creation)", d.TurnDurations)
	}
}
