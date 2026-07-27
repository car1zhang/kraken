package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixture helpers ---

// tokUsage is the `usage` object claude stamps on an assistant record, in
// the fixture's own shape so a test reads as the four wire names rather than
// as four positional ints.
type tokUsage struct{ in, out, cw, cr int64 }

// tokAssistant is one assistant record carrying a usage object. Timestamped
// so digestTranscript's window gate admits it — SumTokensDelta ignores
// timestamps entirely, but the same fixture is reused for the digest tests
// below.
func tokAssistant(t *testing.T, ts int64, u tokUsage) string {
	t.Helper()
	return tokAssistantFull(t, ts, u, "", "")
}

// tokAssistantFull is tokAssistant with the two identity fields the dedupe
// rules turn on: msgID is `message.id` (the API message N records share) and
// sessID is `session_id` (the session that actually made the call — absent
// means "this file's own", which is what sidecars look like).
func tokAssistantFull(t *testing.T, ts int64, u tokUsage, msgID, sessID string) string {
	t.Helper()
	msg := map[string]any{
		"model": "claude-opus-5",
		"usage": map[string]any{
			"input_tokens":                u.in,
			"output_tokens":               u.out,
			"cache_creation_input_tokens": u.cw,
			"cache_read_input_tokens":     u.cr,
		},
		"content": []any{mineText("done")},
	}
	if msgID != "" {
		msg["id"] = msgID
	}
	rec := map[string]any{"type": "assistant", "timestamp": mineISO(ts), "message": msg}
	if sessID != "" {
		rec["session_id"] = sessID
	}
	return mineJSON(t, rec)
}

// tokAppend appends already-serialized jsonl lines (each newline-terminated)
// to an existing transcript, which is what makes a test an INCREMENTAL one
// rather than a re-read.
func tokAppend(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func tokWant(t *testing.T, got Tokens, in, out, cw, cr int64, sessions int) {
	t.Helper()
	want := Tokens{In: in, Out: out, CacheWrite: cw, CacheRead: cr, Sessions: sessions}
	if got != want {
		t.Fatalf("tokens = %+v, want %+v", got, want)
	}
}

// --- tests ---

// TestProjectDirNameIsTheUnexportedMunger pins that the exported wrapper is
// the same rule Claude Code's own dir naming follows — every non-alphanumeric
// rune becoming '-' — and, more importantly, that it is the SAME
// implementation as the unexported projectDirName rather than a second copy
// of it that could drift.
func TestProjectDirNameIsTheUnexportedMunger(t *testing.T) {
	for _, cwd := range []string{
		"/Users/carl/.kraken/cells/dream-2026-07-26/wt-kraken",
		"/Users/carl/Developer/monolith",
		"",
	} {
		if got, want := ProjectDirName(cwd), projectDirName(cwd); got != want {
			t.Fatalf("ProjectDirName(%q) = %q, want %q", cwd, got, want)
		}
	}
	if got := ProjectDirName("/Users/carl/.kraken/cells/a-b/wt-kraken"); got != "-Users-carl--kraken-cells-a-b-wt-kraken" {
		t.Fatalf("ProjectDirName = %q", got)
	}
}

// TestSumTokensDeltaSumsRootAndSidecars is the base case: the four counters
// come from the usage object of every assistant record, subagent sidecars
// count (they hold ~24% of a cell's tool calls and carry their own usage),
// and Sessions is the FILE count.
func TestSumTokensDeltaSumsRootAndSidecars(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")

	mineWrite(t, projects, "-proj", "sess.jsonl",
		mineUser(t, now, "go"),
		tokAssistant(t, now, tokUsage{in: 10, out: 100, cw: 1000, cr: 10000}),
		tokAssistant(t, now, tokUsage{in: 1, out: 2, cw: 3, cr: 4}),
	)
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistant(t, now, tokUsage{in: 5, out: 50, cw: 500, cr: 5000}),
	)

	got, offsets, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 16, 152, 1503, 15004, 2)
	if len(offsets) != 2 {
		t.Fatalf("offsets = %v, want one entry per transcript file", offsets)
	}
	for path, off := range offsets {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if off != info.Size() {
			t.Errorf("%s: offset %d, want the whole file (%d)", filepath.Base(path), off, info.Size())
		}
	}
}

// TestSumTokensDeltaIsIncremental is the property the whole design rests on:
// a second call with the first call's offsets sums ONLY what was appended
// since, and a third call over an unchanged file sums nothing at all.
func TestSumTokensDeltaIsIncremental(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	path := mineWrite(t, projects, "-proj", "sess.jsonl",
		tokAssistant(t, now, tokUsage{in: 10, out: 100, cw: 1000, cr: 10000}),
	)

	first, offsets, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	tokWant(t, first, 10, 100, 1000, 10000, 1)

	tokAppend(t, path, tokAssistant(t, now, tokUsage{in: 1, out: 2, cw: 3, cr: 4}))

	second, offsets2, err := SumTokensDelta(dir, offsets)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	tokWant(t, second, 1, 2, 3, 4, 1) // the DELTA, not the total
	if offsets2[path] <= offsets[path] {
		t.Fatalf("offset did not advance: %d -> %d", offsets[path], offsets2[path])
	}

	third, offsets3, err := SumTokensDelta(dir, offsets2)
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	tokWant(t, third, 0, 0, 0, 0, 1)
	if offsets3[path] != offsets2[path] {
		t.Fatalf("settled offset moved: %d -> %d", offsets2[path], offsets3[path])
	}
}

// TestSumTokensDeltaRereadsTruncatedFile pins the one place append-only is
// allowed to be violated: a file SHORTER than its recorded offset cannot be
// the file that was summed, so it is re-read whole rather than skipped (which
// would strand it forever) or read from the stale offset (which would seek
// past its end).
func TestSumTokensDeltaRereadsTruncatedFile(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	path := mineWrite(t, projects, "-proj", "sess.jsonl",
		tokAssistant(t, now, tokUsage{in: 10, out: 100, cw: 1000, cr: 10000}),
		tokAssistant(t, now, tokUsage{in: 10, out: 100, cw: 1000, cr: 10000}),
	)
	_, offsets, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}

	// replaced by a shorter file — a new session reusing the name
	if err := os.WriteFile(path, []byte(tokAssistant(t, now, tokUsage{in: 7, out: 8, cw: 9, cr: 11})+"\n"), 0o644); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, offsets2, err := SumTokensDelta(dir, offsets)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	tokWant(t, got, 7, 8, 9, 11, 1)
	info, _ := os.Stat(path)
	if offsets2[path] != info.Size() {
		t.Fatalf("offset = %d, want the whole re-read file (%d)", offsets2[path], info.Size())
	}
}

// TestSumTokensDeltaStopsAtAPartialLine covers a transcript caught mid-write:
// the final line has no newline yet, so it must NOT be counted and the offset
// must stop before it — otherwise recording the file size would skip the rest
// of that record forever once it lands.
func TestSumTokensDeltaStopsAtAPartialLine(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	complete := tokAssistant(t, now, tokUsage{in: 10, out: 100, cw: 1000, cr: 10000})
	half := tokAssistant(t, now, tokUsage{in: 1, out: 2, cw: 3, cr: 4})

	path := filepath.Join(dir, "sess.jsonl")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// the second record is cut off mid-JSON, exactly as a flush in progress
	// leaves it
	if err := os.WriteFile(path, []byte(complete+"\n"+half[:len(half)/2]), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, offsets, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 10, 100, 1000, 10000, 1)
	if want := int64(len(complete) + 1); offsets[path] != want {
		t.Fatalf("offset = %d, want %d (end of the last COMPLETE line)", offsets[path], want)
	}

	// the rest of the record lands: the next pass picks up the whole thing
	tokAppend(t, path, half[len(half)/2:])
	got2, _, err := SumTokensDelta(dir, offsets)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	tokWant(t, got2, 1, 2, 3, 4, 1)
}

// TestSumTokensDeltaSkipsMalformedAndNonAssistant is the defensive contract
// for data another program owns: blank lines, garbage lines, records with no
// usage at all, and non-assistant records all cost only themselves, and none
// of them errors.
func TestSumTokensDeltaSkipsMalformedAndNonAssistant(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	mineWrite(t, projects, "-proj", "sess.jsonl",
		``,
		`{"type":"mode","mode":"normal"}`,
		`{not json at all`,
		// a user record that echoes a usage object must NOT be counted
		mineJSON(t, map[string]any{"type": "user", "timestamp": mineISO(now),
			"message": map[string]any{"usage": map[string]any{"output_tokens": 9999}}}),
		mineAssistant(t, now, "claude-opus-5", mineText("no usage object here")),
		tokAssistant(t, now, tokUsage{in: 1, out: 2, cw: 3, cr: 4}),
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 1, 2, 3, 4, 1)
}

// TestSumTokensDeltaMissingDirIsNotAnError pins the normal state for a job
// whose cell never came up: no project dir, zero tokens, zero files, no
// error. An error here would make the daemon pass log once per tick forever.
func TestSumTokensDeltaMissingDirIsNotAnError(t *testing.T) {
	projects := mineHome(t)
	got, offsets, err := SumTokensDelta(filepath.Join(projects, "-nope"), nil)
	if err != nil {
		t.Fatalf("SumTokensDelta on a missing dir: %v", err)
	}
	tokWant(t, got, 0, 0, 0, 0, 0)
	if len(offsets) != 0 {
		t.Fatalf("offsets = %v, want empty", offsets)
	}
}

// TestSumTokensDeltaPartialScanErrorsWithAFloor is the bug class this repo
// has shipped three times: a scan that stops early must NOT present its
// partial counters as a total. A line past the 16 MB scanner ceiling ends the
// scan, so the counters before it are a floor — returned, but with an error
// saying so — and the offset stays at the last complete line it did count, so
// the next pass retries from there rather than skipping the gap.
func TestSumTokensDeltaPartialScanErrorsWithAFloor(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	good := tokAssistant(t, now, tokUsage{in: 10, out: 100, cw: 1000, cr: 10000})
	monster := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", mineScanBufMax+1) + `"}}`
	after := tokAssistant(t, now, tokUsage{in: 1, out: 2, cw: 3, cr: 4})
	mineWrite(t, projects, "-proj", "sess.jsonl", good, monster, after)

	got, offsets, err := SumTokensDelta(dir, nil)
	if err == nil {
		t.Fatal("a truncated scan must report itself — silence here is the whole bug")
	}
	if !strings.Contains(err.Error(), "partial token scan") {
		t.Errorf("error = %v, want it to say the scan was partial", err)
	}
	// the counters it DID reach are still worth having
	tokWant(t, got, 10, 100, 1000, 10000, 1)
	path := filepath.Join(dir, "sess.jsonl")
	if want := int64(len(good) + 1); offsets[path] != want {
		t.Fatalf("offset = %d, want %d — the last line actually counted", offsets[path], want)
	}
}

// TestSumTokensDeltaOffsetsDropVanishedFiles pins how a caller learns a
// transcript is gone: it is simply absent from the returned map, which is
// what lets the store's replace-all write keep the watermark table bounded.
func TestSumTokensDeltaOffsetsDropVanishedFiles(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	keep := mineWrite(t, projects, "-proj", "a.jsonl", tokAssistant(t, now, tokUsage{out: 1}))
	gone := mineWrite(t, projects, "-proj", "b.jsonl", tokAssistant(t, now, tokUsage{out: 1}))

	_, offsets, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if len(offsets) != 2 {
		t.Fatalf("offsets = %v, want 2", offsets)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatalf("remove: %v", err)
	}
	_, offsets2, err := SumTokensDelta(dir, offsets)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, ok := offsets2[gone]; ok {
		t.Errorf("a vanished transcript must drop out of the offsets: %v", offsets2)
	}
	if _, ok := offsets2[keep]; !ok {
		t.Errorf("the surviving transcript lost its offset: %v", offsets2)
	}
}

// TestDigestCarriesTokenCounters pins the mine.go half: the four counters ride
// along in digestTranscript's existing forward pass, are gated on the window
// like every other count there, and roll up into MineResult.Tokens.
func TestDigestCarriesTokenCounters(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(6*time.Hour/time.Millisecond)
	old := since - 60_000

	mineWrite(t, projects, "-proj", "sess.jsonl",
		tokAssistant(t, old, tokUsage{in: 999, out: 999, cw: 999, cr: 999}), // before the window
		tokAssistant(t, now, tokUsage{in: 10, out: 100, cw: 1000, cr: 10000}),
	)
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistant(t, now, tokUsage{in: 5, out: 50, cw: 500, cr: 5000}),
	)

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	root := mineFindDigest(t, res, "sess.jsonl")
	if root.In != 10 || root.Out != 100 || root.CacheWrite != 1000 || root.CacheRead != 10000 {
		t.Fatalf("root digest tokens = %d/%d/%d/%d, want the in-window record only",
			root.In, root.Out, root.CacheWrite, root.CacheRead)
	}
	side := mineFindDigest(t, res, "agent-1.jsonl")
	if side.Out != 50 {
		t.Fatalf("sidecar digest Out = %d, want 50", side.Out)
	}
	want := Tokens{In: 15, Out: 150, CacheWrite: 1500, CacheRead: 15000, Sessions: 2}
	if res.Tokens != want {
		t.Fatalf("MineResult.Tokens = %+v, want %+v", res.Tokens, want)
	}
}

// TestSumTokensDeltaLargeFileIsStreamed is a guard on the size discipline the
// package header states: transcripts are read with a bounded scanner, never
// io.ReadAll. A file well past any reasonable buffer must sum correctly.
func TestSumTokensDeltaLargeFileIsStreamed(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	lines := make([]string, 0, 2000)
	for i := 0; i < 2000; i++ {
		lines = append(lines, tokAssistant(t, now, tokUsage{out: 1}))
	}
	mineWrite(t, projects, "-proj", "sess.jsonl", lines...)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	if got.Out != 2000 {
		t.Fatalf("Out = %d, want 2000 (%s)", got.Out, fmt.Sprintf("%d lines", len(lines)))
	}
}

// --- group dedupe and fork-replay (fix round 1) ---

// TestSumTokensDeltaCountsOneMessageOnce is the first of the two rules that
// separate this from a naive per-record sum. Claude Code writes one API
// message as one record PER CONTENT BLOCK — thinking, text, each tool_use —
// and stamps the SAME cumulative usage on every one of them. Counting each
// record inflated real cell dirs by 1.98x; cache-read is per API call and
// cannot legitimately be counted eight times.
func TestSumTokensDeltaCountsOneMessageOnce(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	u := tokUsage{in: 2, out: 7479, cw: 309, cr: 430139}
	mineWrite(t, projects, "-proj", "sess.jsonl",
		// one message, four blocks, one usage object repeated verbatim
		tokAssistantFull(t, now, u, "msg_A", ""),
		tokAssistantFull(t, now, u, "msg_A", ""),
		tokAssistantFull(t, now, u, "msg_A", ""),
		tokAssistantFull(t, now, u, "msg_A", ""),
		// a second, distinct message must still be counted
		tokAssistantFull(t, now, tokUsage{in: 1, out: 2, cw: 3, cr: 4}, "msg_B", ""),
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 3, 7481, 312, 430143, 1)
}

// TestSumTokensDeltaGroupSurvivesToolResults pins that the group tracker
// SKIPS non-assistant records rather than being reset by them: a message's
// blocks are routinely interleaved with the tool_result user records they
// produced, and treating those as a boundary counts the message twice.
// Measured on this machine: 9,385 same-id pairs separated by 1-5 non-assistant
// records.
func TestSumTokensDeltaGroupSurvivesToolResults(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	u := tokUsage{out: 100, cr: 5000}
	mineWrite(t, projects, "-proj", "sess.jsonl",
		tokAssistantFull(t, now, u, "msg_A", ""),
		mineUser(t, now, []any{mineToolResult("t1", false, "ok")}),
		mineUser(t, now, []any{mineToolResult("t2", false, "ok")}),
		tokAssistantFull(t, now, u, "msg_A", ""), // same message, still one group
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 0, 100, 0, 5000, 1)
}

// TestSumTokensDeltaSkipsForkReplay is the second rule. A kraken side thread
// is `claude --fork-session` in the same worktree, so the fork's transcript is
// SEEDED with the parent's records, `usage` and all, in the SAME project dir.
// `session_id` (snake) names the session that actually made the call and
// survives the fork; `sessionId` (camel) is rewritten and is useless here.
func TestSumTokensDeltaSkipsForkReplay(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	parent := tokUsage{out: 1000, cr: 90000}
	own := tokUsage{out: 7, cr: 11}

	mineWrite(t, projects, "-proj", "parent.jsonl",
		tokAssistantFull(t, now, parent, "msg_P", "parent"),
	)
	// the fork: the parent's record replayed in, then its own work
	mineWrite(t, projects, "-proj", "fork.jsonl",
		tokAssistantFull(t, now, parent, "msg_P", "parent"),
		tokAssistantFull(t, now, own, "msg_F", "fork"),
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	// parent's message once (from parent.jsonl), the fork's own once
	tokWant(t, got, 0, 1007, 0, 90011, 2)
}

// TestSumTokensDeltaSidecarsHaveNoSessionID pins the "absent means own" half
// of the ownership rule. Subagent sidecars carry no `session_id` at all
// (verified: 159 of 159 records on a real cell), and they hold ~24% of a
// cell's work — treating absent as foreign would discard far more than the
// double-count it fixed.
func TestSumTokensDeltaSidecarsHaveNoSessionID(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now, tokUsage{out: 50, cr: 500}, "msg_S", ""),
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 0, 50, 0, 500, 1)
}

// TestSumTokensDeltaGroupSurvivesTheResumeBoundary is the interaction between
// the group rule and incrementality, and it is the one that actually broke in
// development: a message split across two passes must still be counted once.
// The boundary is recovered from the file by precedingGroupID rather than
// persisted, and the walk has to skip the tool_result records in between —
// looking back exactly one record double-counted a real transcript.
func TestSumTokensDeltaGroupSurvivesTheResumeBoundary(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	u := tokUsage{out: 7704, cr: 88000}

	head := []string{
		tokAssistantFull(t, now, u, "msg_A", ""),
		mineUser(t, now, []any{mineToolResult("t1", false, "ok")}),
	}
	path := mineWrite(t, projects, "-proj", "sess.jsonl", head...)

	first, offs, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	tokWant(t, first, 0, 7704, 0, 88000, 1)

	// the rest of the SAME message lands after the boundary
	tokAppend(t, path,
		tokAssistantFull(t, now, u, "msg_A", ""),
		tokAssistantFull(t, now, u, "msg_A", ""),
	)
	second, offs, err := SumTokensDelta(dir, offs)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	tokWant(t, second, 0, 0, 0, 0, 1)

	// and a genuinely new message after it still counts
	tokAppend(t, path, tokAssistantFull(t, now, tokUsage{out: 5}, "msg_B", ""))
	third, _, err := SumTokensDelta(dir, offs)
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	tokWant(t, third, 0, 5, 0, 0, 1)
}

// TestSumTokensDeltaWrongTypedUsageFieldKeepsTheRest: encoding/json fills in
// every field it can and reports a type error at the end, so a record with one
// bad counter must still contribute its three good ones. Discarding the whole
// record was a silent under-count with no signal at all.
func TestSumTokensDeltaWrongTypedUsageFieldKeepsTheRest(t *testing.T) {
	projects := mineHome(t)
	dir := filepath.Join(projects, "-proj")
	mineWrite(t, projects, "-proj", "sess.jsonl",
		`{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":"7","output_tokens":11,`+
			`"cache_creation_input_tokens":13,"cache_read_input_tokens":17}}}`,
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 0, 11, 13, 17, 1)
}

// TestSumTokensDeltaUnreadableSubtreeIsReported closes the watermark-loss
// double count. A subtree that cannot be listed leaves its files absent from
// the returned offsets — indistinguishable from deleted — and a caller that
// replaced its watermarks with that would re-sum them from zero into
// accumulate-only counters. The walk error is now on the error channel so
// refreshJobTokens can carry the unseen watermarks forward. The ROOT being
// absent stays silent: that is just a job whose cell never came up.
func TestSumTokensDeltaUnreadableSubtreeIsReported(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	mineWrite(t, projects, "-proj", "sess.jsonl", tokAssistant(t, now, tokUsage{out: 1}))
	sub := filepath.Join(dir, "sess", "subagents")
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistant(t, now, tokUsage{out: 500}))

	if err := os.Chmod(sub, 0o000); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test needs")
	}

	_, offs, err := SumTokensDelta(dir, nil)
	if err == nil {
		t.Fatal("an unlistable subtree must reach the error channel — silence here is what deletes watermarks")
	}
	if _, ok := offs[filepath.Join(sub, "agent-1.jsonl")]; ok {
		t.Fatal("fixture wrong: the sidecar should not have been readable")
	}
}

// TestDigestAppliesTheSameDedupeRules pins that mine.go's digest — what the
// nightly pass ranks a night's waste by — applies BOTH rules the per-job sum
// does. A digest that inflated tool-heavy sessions more than prose-heavy ones
// would skew exactly the axis the dream skill asks a reader to compare on.
func TestDigestAppliesTheSameDedupeRules(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(6*time.Hour/time.Millisecond)
	u := tokUsage{out: 100, cr: 9000}

	// three records of one message, plus one replayed in from another session
	mineWrite(t, projects, "-proj", "sess.jsonl",
		tokAssistantFull(t, now, u, "msg_A", "sess"),
		tokAssistantFull(t, now, u, "msg_A", "sess"),
		tokAssistantFull(t, now, u, "msg_A", "sess"),
		tokAssistantFull(t, now, tokUsage{out: 999, cr: 999}, "msg_X", "somebody-else"),
	)
	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "sess.jsonl")
	if d.Out != 100 || d.CacheRead != 9000 {
		t.Fatalf("digest Out=%d CacheRead=%d, want the message counted once and the replay skipped",
			d.Out, d.CacheRead)
	}
	if res.Tokens.Out != 100 {
		t.Fatalf("MineResult.Tokens.Out = %d, want 100", res.Tokens.Out)
	}
}

// --- fix round 2 ---

// TestSumTokensDeltaUnstattableFileIsReported is the sibling of
// TestSumTokensDeltaUnreadableSubtreeIsReported and closes the same hole from
// the other side: a file that exists but cannot be STAT'D (an un-traversable
// parent, an lstat losing a race with a rename) is absent from the returned
// offsets while still being on disk. Silent, a replace-all caller deletes its
// watermark and re-sums the whole file into accumulate-only counters the
// moment it becomes readable again.
func TestSumTokensDeltaUnstattableFileIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test needs")
	}
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	mineWrite(t, projects, "-proj", "sess.jsonl", tokAssistant(t, now, tokUsage{out: 1}))
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistant(t, now, tokUsage{out: 500}))
	sub := filepath.Join(dir, "sess", "subagents")

	// 0444: listable (so WalkDir's ReadDir succeeds and the walk callback gets
	// no error) but NOT traversable, so the child's lstat fails. That is the
	// d.Info() branch specifically, not the subtree-walk branch.
	if err := os.Chmod(sub, 0o444); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o755) })

	_, offs, err := SumTokensDelta(dir, nil)
	if err == nil {
		t.Fatal("an unstattable file must reach the error channel — silence here is what deletes watermarks")
	}
	if !strings.Contains(err.Error(), "stat") {
		t.Errorf("error = %v, want it to name the stat failure", err)
	}
	if _, ok := offs[filepath.Join(sub, "agent-1.jsonl")]; ok {
		t.Fatal("fixture wrong: the sidecar should not have been stattable")
	}
}

// TestPrecedingGroupIDReportsExhaustion pins the direction the lookback bounds
// fail in and that failing is never silent. Running out of lookback leaves the
// seed unknown, so the group at the boundary is counted as new — and if it was
// a continuation, that message is counted TWICE. An over-count is the one
// direction nothing downstream can subtract back out, so it must be reported
// rather than hidden behind an empty string that means five other things.
func TestPrecedingGroupIDReportsExhaustion(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	u := tokUsage{out: 100}

	// A group split across the boundary by `gap` intervening records: within
	// the bounds it must dedupe silently; past them it must be reported.
	run := func(t *testing.T, gap int, filler string, wantReported bool) {
		t.Helper()
		lines := []string{tokAssistantFull(t, now, u, "msg_A", "sess")}
		for i := 0; i < gap; i++ {
			lines = append(lines, filler)
		}
		path := mineWrite(t, projects, "-proj", "sess.jsonl", lines...)
		_, offs, err := SumTokensDelta(dir, nil)
		if err != nil {
			t.Fatalf("first pass: %v", err)
		}
		tokAppend(t, path, tokAssistantFull(t, now, u, "msg_A", "sess")) // SAME message
		tk, _, err := SumTokensDelta(dir, offs)
		if wantReported {
			if err == nil {
				t.Errorf("gap=%d: exhaustion must be reported", gap)
			}
			return
		}
		if err != nil {
			t.Errorf("gap=%d: unexpected error %v", gap, err)
		}
		if tk.Out != 0 {
			t.Errorf("gap=%d: resumed delta Out=%d, want 0 — the message continues, it is not new", gap, tk.Out)
		}
	}

	small := `{"type":"user","message":{"content":"r"}}`
	t.Run("within the record bound", func(t *testing.T) { run(t, groupLookbackRecords-33, small, false) })
	t.Run("past the record bound", func(t *testing.T) { run(t, groupLookbackRecords+6, small, true) })

	// The byte bound is the one that actually binds: the skipped records are
	// tool_results and this repo records real lines at 8.3 MB.
	fat := `{"type":"user","message":{"content":"` + strings.Repeat("x", 8<<20) + `"}}`
	t.Run("past the byte bound", func(t *testing.T) { run(t, 8, fat, true) })
}

// TestSidecarOwnershipUsesTheParentSession closes the one-sided ownership
// rule. A sidecar's stem is `agent-<id>`, a value no `session_id` can equal,
// so before this it was carried ENTIRELY by the "absent is owned" arm — true
// today but a format kraken does not control. If Claude Code ever stamps
// sidecars with their parent's session id, the stem would reject every one of
// them and silently drop ~24% of a cell's work.
func TestSidecarOwnershipUsesTheParentSession(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	mineWrite(t, projects, "-proj", "parentsess.jsonl",
		tokAssistantFull(t, now, tokUsage{out: 100}, "msg_P", "parentsess"))

	for _, tc := range []struct {
		name string
		sess string
	}{
		{"sidecar with no session_id (today)", ""},
		{"sidecar stamped with its parent's session_id", "parentsess"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mineWrite(t, projects, "-proj", filepath.Join("parentsess", "subagents", "agent-1.jsonl"),
				tokAssistantFull(t, now, tokUsage{out: 40}, "msg_S", tc.sess))
			tk, _, err := SumTokensDelta(dir, nil)
			if err != nil {
				t.Fatalf("SumTokensDelta: %v", err)
			}
			if tk.Out != 140 {
				t.Fatalf("Out = %d, want 140 — the sidecar's work belongs to this cell either way", tk.Out)
			}
		})
	}
}
