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

// tokWant checks the four TOTALS and the file count. It deliberately says
// nothing about the zeus half: the split is a partition of these same four, so
// asserting it here would make every one of the tests below — none of which is
// about the split — restate it, and the ones whose fixtures are all lead
// transcripts would just be repeating their own arguments. tokWantZeus is
// where the split is actually pinned.
func tokWant(t *testing.T, got Tokens, in, out, cw, cr int64, sessions int) {
	t.Helper()
	want := Tokens{In: in, Out: out, CacheWrite: cw, CacheRead: cr, Sessions: sessions}
	got.ZeusIn, got.ZeusOut, got.ZeusCacheWrite, got.ZeusCacheRead = 0, 0, 0, 0
	if got != want {
		t.Fatalf("tokens = %+v, want %+v (zeus half checked separately)", got, want)
	}
}

// tokWantZeus checks the LEAD's half of the four, and — since the split is a
// partition and not a second measurement — that it never exceeds the total it
// is a subset of. A consumer derives subagent spend by subtracting, so a zeus
// counter above its total is a negative rendered on somebody's screen.
func tokWantZeus(t *testing.T, got Tokens, in, out, cw, cr int64) {
	t.Helper()
	if got.ZeusIn != in || got.ZeusOut != out || got.ZeusCacheWrite != cw || got.ZeusCacheRead != cr {
		t.Fatalf("zeus half = %d/%d/%d/%d, want %d/%d/%d/%d",
			got.ZeusIn, got.ZeusOut, got.ZeusCacheWrite, got.ZeusCacheRead, in, out, cw, cr)
	}
	if got.ZeusIn > got.In || got.ZeusOut > got.Out ||
		got.ZeusCacheWrite > got.CacheWrite || got.ZeusCacheRead > got.CacheRead {
		t.Fatalf("zeus half exceeds the total it is a subset of: %+v", got)
	}
}

// tokAgentSetting is the record Claude Code writes when an Agent-tool spawn
// carried a `subagent_type` — one of the two signals isAgentTranscript probes
// for, and the WEAKER one: a teammate spawned with a name and a prompt alone
// never gets it. Verbatim in shape from a real teammate transcript.
func tokAgentSetting(t *testing.T, sessID string) string {
	t.Helper()
	return mineJSON(t, map[string]any{
		"type": "agent-setting", "agentSetting": "general-purpose", "sessionId": sessID,
	})
}

// tokAgentName is the `agent-name` record — the signal that actually carries
// the classification, present on 1,581 of the 1,588 root transcripts on this
// machine. Verbatim in shape from a real one.
func tokAgentName(t *testing.T, name string) string {
	t.Helper()
	return mineJSON(t, map[string]any{"type": "agent-name", "agentName": name, "sessionId": "s"})
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
// message as one record PER CONTENT BLOCK — thinking, text, each tool_use.
// Counting each record inflated real cell dirs by 1.98x; cache-read is per API
// call and cannot legitimately be counted eight times.
//
// THIS FIXTURE IS THE ROOT-TRANSCRIPT SHAPE, and saying so is the point.
// Repeating one usage object verbatim is exactly what a root transcript does
// (0 of 35,374 root groups on this machine vary in any counter), and for years
// it was the ONLY shape any fixture here had — which is how "the same
// cumulative usage on every record" survived as a stated invariant while every
// subagent sidecar ever written contradicted it. The sidecar shape, where
// output_tokens rises across the group, is
// TestSumTokensDeltaTakesTheLastOutputOfAStreamedGroup. Keep both: this one is
// the guard that the fix is a no-op on root transcripts, which is what keeps
// the 118/119 whole-corpus agreement intact.
func TestSumTokensDeltaCountsOneMessageOnce(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	u := tokUsage{in: 2, out: 7479, cw: 309, cr: 430139}
	mineWrite(t, projects, "-proj", "sess.jsonl",
		// one message, four blocks, one usage object repeated verbatim —
		// the ROOT shape; a sidecar's output would rise across these four
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
// Measured on this machine: 9,385 same-id GROUPS whose blocks are separated by 1-5 non-assistant
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
	// A replayed record is the parent's HISTORY: it was written before the
	// fork's file existed, which is what the ownership rule reads. Dating it
	// at `now` would race the file's creation by a millisecond.
	before := now - int64(time.Hour/time.Millisecond)
	dir := filepath.Join(projects, "-proj")
	parent := tokUsage{out: 1000, cr: 90000}
	own := tokUsage{out: 7, cr: 11}

	mineWrite(t, projects, "-proj", "parent.jsonl",
		tokAssistantFull(t, before, parent, "msg_P", "parent"),
	)
	// the fork: the parent's record replayed in, then its own work
	mineWrite(t, projects, "-proj", "fork.jsonl",
		tokAssistantFull(t, before, parent, "msg_P", "parent"),
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
// The boundary is recovered from the file by precedingGroup rather than
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
	// — dated inside the window but before this file existed, as a copied
	// record is
	mineWrite(t, projects, "-proj", "sess.jsonl",
		tokAssistantFull(t, now, u, "msg_A", "sess"),
		tokAssistantFull(t, now, u, "msg_A", "sess"),
		tokAssistantFull(t, now, u, "msg_A", "sess"),
		tokAssistantFull(t, now-int64(time.Hour/time.Millisecond), tokUsage{out: 999, cr: 999}, "msg_X", "somebody-else"),
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

// TestPrecedingGroupReportsExhaustion pins the direction the lookback bounds
// fail in and that failing is never silent. Running out of lookback leaves the
// seed unknown, so the group at the boundary is counted as new — and if it was
// a continuation, that message is counted TWICE. An over-count is the one
// direction nothing downstream can subtract back out, so it must be reported
// rather than hidden behind an empty string that means five other things.
func TestPrecedingGroupReportsExhaustion(t *testing.T) {
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

// TestSumTokensDeltaReportsSeedAndScanErrorsTogether pins that a file whose
// scan ALSO stopped early does not lose its unresolved-seed warning. The two
// describe different inexactness about one file — a floor (the scan stopped)
// versus a possible double-count (the group at the resume boundary could not be
// seeded) — and returning only the scan error left the one file that has both
// reporting only half of what is wrong with it. The counters are unaffected:
// every caller treats every error from here identically, so this is purely
// about what the log says.
func TestSumTokensDeltaReportsSeedAndScanErrorsTogether(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	u := tokUsage{out: 100}

	// Enough intervening records to blow the lookback bound, so the resume
	// cannot seed its group — the same construction as the exhaustion test.
	lines := []string{tokAssistantFull(t, now, u, "msg_A", "sess")}
	for i := 0; i < groupLookbackRecords+6; i++ {
		lines = append(lines, `{"type":"user","message":{"content":"r"}}`)
	}
	path := mineWrite(t, projects, "-proj", "sess.jsonl", lines...)

	_, offs, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// After the boundary: the SAME message (so the seed matters), then a line
	// over the scanner ceiling (so the scan also stops early).
	tokAppend(t, path,
		tokAssistantFull(t, now, u, "msg_A", "sess"),
		`{"type":"assistant","message":{"content":"`+strings.Repeat("x", mineScanBufMax+1)+`"}}`,
		tokAssistant(t, now, tokUsage{out: 7}),
	)
	_, _, err = SumTokensDelta(dir, offs)
	if err == nil {
		t.Fatal("both a truncated scan and an unresolved seed must be reported")
	}
	if !strings.Contains(err.Error(), "group seed") {
		t.Errorf("error = %v\nwant it to still carry the unresolved-seed warning", err)
	}
	if !strings.Contains(err.Error(), "token too long") && !strings.Contains(err.Error(), "partial token scan") {
		t.Errorf("error = %v\nwant it to also carry the scan failure", err)
	}
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

// --- the zeus / subagent split ---

// TestSumTokensDeltaSplitsZeusFromSubagents is the split's base case, with all
// three shapes of file a real cell project dir holds in one directory: the
// lead's own transcript, a TEAMMATE's root transcript (its own claude under
// teammateMode: tmux, marked by the `agent-setting` record), and an in-process
// subagent's sidecar. The four TOTALS must be exactly what they were before
// the split existed — it partitions a sum, it does not change one — and the
// zeus half must carry the lead's contribution and nothing else.
func TestSumTokensDeltaSplitsZeusFromSubagents(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")

	// zeus: a root transcript naming itself with the lead prefix
	mineWrite(t, projects, "-proj", "zeus.jsonl",
		tokAgentName(t, "zeus-demo"),
		mineUser(t, now, "go"),
		tokAssistantFull(t, now, tokUsage{in: 10, out: 100, cw: 1000, cr: 10000}, "msg_Z", "zeus"),
	)
	// a teammate: its own root transcript, carrying both signals
	mineWrite(t, projects, "-proj", "hephaestus.jsonl",
		tokAgentSetting(t, "hephaestus"),
		tokAgentName(t, "hephaestus-demo"),
		tokAssistantFull(t, now, tokUsage{in: 3, out: 30, cw: 300, cr: 3000}, "msg_H", "hephaestus"),
	)
	// an in-process subagent: a sidecar, recognised by its path alone
	mineWrite(t, projects, "-proj", filepath.Join("zeus", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now, tokUsage{in: 5, out: 50, cw: 500, cr: 5000}, "msg_S", ""),
	)

	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 18, 180, 1800, 18000, 3)
	tokWantZeus(t, got, 10, 100, 1000, 10000)
}

// TestSumTokensDeltaCountsASideThreadAsZeus pins the one classification that
// is a judgement call rather than a fact about the file: a side thread is
// `claude --fork-session` of zeus in the same worktree — carl branching off to
// ask about the work — so it speaks in zeus' place and is not delegated work.
//
// A thread DOES carry an `agentName` (`thread-<slug>-<tid>`, minted by
// jobs.ThreadAgentName), which is why ThreadAgentNamePrefix has to be one of
// the lead prefixes: on the name alone a thread looks exactly like a teammate,
// and two real thread transcripts on this machine would otherwise be booked to
// the subagent half in flat contradiction of the rule above.
//
// Only its OWN turns land in the zeus half; the parent records replayed into
// its file are already dropped by tokenRecordOwned.
func TestSumTokensDeltaCountsASideThreadAsZeus(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	parent := tokUsage{out: 1000, cr: 90000}

	// the parent's turn predates the thread's file: a fork copies history
	before := now - int64(time.Hour/time.Millisecond)
	mineWrite(t, projects, "-proj", "zeus.jsonl",
		tokAgentName(t, "zeus-demo"),
		tokAssistantFull(t, before, parent, "msg_P", "zeus"),
	)
	mineWrite(t, projects, "-proj", "thread-t1.jsonl",
		tokAgentName(t, "thread-demo-t1"),
		tokAssistantFull(t, before, parent, "msg_P", "zeus"), // replayed in by the fork
		tokAssistantFull(t, now, tokUsage{out: 7, cr: 11}, "msg_T", "thread-t1"),
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 0, 1007, 0, 90011, 2)
	tokWantZeus(t, got, 0, 1007, 0, 90011)
}

// TestSumTokensDeltaCountsAnUnmarkedTeammateAsSubagent is the regression test
// for the rule's original defect, and it fails against the two-arm version.
//
// `agent-setting` records the spawn's `subagent_type`, NOT the fact of being an
// Agent-tool spawn — a teammate given a `name` and a prompt but no
// `subagent_type` gets no such record. That is exactly what zeus' crew is, so
// the marker alone missed 222 of 1,461 teammate transcripts (15.2%) across 20
// cell dirs on this machine, some written the day before this was fixed, and it
// rendered one real job's lead share 38% too high. The `agentName` arm is what
// catches them: it is present on 1,581 of 1,588 root transcripts, and a name
// that is neither `zeus-…` nor `thread-…` was spawned by the Agent tool.
func TestSumTokensDeltaCountsAnUnmarkedTeammateAsSubagent(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")

	mineWrite(t, projects, "-proj", "zeus.jsonl",
		tokAgentName(t, "zeus-demo"),
		tokAssistantFull(t, now, tokUsage{out: 100, cr: 1000}, "msg_Z", "zeus"),
	)
	// A hermes teammate as Claude Code actually writes one today: a name, a
	// mode, a permission-mode — and no agent-setting record anywhere.
	mineWrite(t, projects, "-proj", "hermes.jsonl",
		`{"type":"custom-title","customTitle":"hermes-demo"}`,
		tokAgentName(t, "hermes-demo"),
		`{"type":"mode","mode":"normal"}`,
		`{"type":"permission-mode","permissionMode":"bypassPermissions"}`,
		tokAssistantFull(t, now, tokUsage{out: 40, cr: 400}, "msg_H", "hermes"),
	)

	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 0, 140, 0, 1400, 2)
	tokWantZeus(t, got, 0, 100, 0, 1000)
}

// TestSumTokensDeltaFindsAnAgentNameOnAFatBriefRecord is the OTHER half of the
// same defect, and the one that survived the first fix.
//
// Only 120 of the 1,588 root transcripts on this machine carry a dedicated
// `agent-name` record. On the other 1,467 the `agentName` rides the session's
// FIRST USER RECORD — the teammate-message brief the lead sent — which is one
// JSONL line with a whole prompt inside it. The field sits near the front of
// that line (byte 431 at the latest) but the LINE ends as far in as 57,148
// bytes, and a decode needs all of it. A head window sized to where the signal
// STARTS therefore truncates the line, fails to decode, finds nothing, and
// books the teammate's entire spend to the lead: measured, that cost 6 hermes
// transcripts totalling 8.5M tokens in one cell dir alone.
func TestSumTokensDeltaFindsAnAgentNameOnAFatBriefRecord(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")

	mineWrite(t, projects, "-proj", "zeus.jsonl",
		tokAgentName(t, "zeus-demo"),
		tokAssistantFull(t, now, tokUsage{out: 100, cr: 1000}, "msg_Z", "zeus"),
	)
	// The real shape: agentName early in the record, a 40 KB brief after it.
	brief := mineJSON(t, map[string]any{
		"parentUuid": nil, "isSidechain": false, "teamName": "session-a92a488f",
		"agentName": "hermes-demo", "type": "user",
		"message": map[string]any{"role": "user",
			"content": "<teammate-message>" + strings.Repeat("brief ", 7000) + "</teammate-message>"},
	})
	if len(brief) < 32<<10 {
		t.Fatalf("fixture is wrong: brief record is only %d bytes, too small to test the bound", len(brief))
	}
	mineWrite(t, projects, "-proj", "hermes.jsonl",
		`{"type":"mode","mode":"normal"}`,
		`{"type":"permission-mode","permissionMode":"bypassPermissions"}`,
		brief,
		tokAssistantFull(t, now, tokUsage{out: 40, cr: 400}, "msg_H", "hermes"),
	)

	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 0, 140, 0, 1400, 2)
	tokWantZeus(t, got, 0, 100, 0, 1000)
}

// TestSumTokensDeltaFindsTheMarkerPastLineOne: neither signal is reliably the
// first record. Measured over the 1,588 root transcripts on this machine,
// `agent-setting` reaches line 2 and `agentName` reaches LINE 5, so a probe
// that only ever read the first record would mis-bin both. agentMarkerLines is
// ~3x that depth precisely because the position is Claude Code's to change.
func TestSumTokensDeltaFindsTheMarkerPastLineOne(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	mineWrite(t, projects, "-proj", "teammate.jsonl",
		`{"type":"custom-title","customTitle":"athena-logic"}`,
		`{"type":"mode","mode":"normal"}`,
		`{"type":"permission-mode","permissionMode":"bypassPermissions"}`,
		`{"type":"pr-link","prNumber":66}`,
		tokAgentName(t, "athena-logic-demo"), // line 5, the deepest ever measured
		tokAssistantFull(t, now, tokUsage{out: 60, cr: 600}, "msg_A", "teammate"),
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 0, 60, 0, 600, 1)
	tokWantZeus(t, got, 0, 0, 0, 0)
}

// TestSumTokensDeltaSplitSurvivesTheResumeBoundary is the split against the
// property the whole file is built on. The classification is recomputed on
// every pass that opens a file and never persisted, so an incremental delta
// must land in the same half its file's first pass did — a marker read once
// and forgotten would send a teammate's later turns to zeus.
func TestSumTokensDeltaSplitSurvivesTheResumeBoundary(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")

	zeus := mineWrite(t, projects, "-proj", "zeus.jsonl",
		tokAgentName(t, "zeus-demo"),
		tokAssistantFull(t, now, tokUsage{out: 100, cr: 1000}, "msg_Z1", "zeus"),
	)
	// Marked by name ONLY, which is the common shape and the one the two-arm
	// rule missed — so this test also covers the fix at the resume boundary.
	agent := mineWrite(t, projects, "-proj", "hephaestus.jsonl",
		tokAgentName(t, "hephaestus-demo"),
		tokAssistantFull(t, now, tokUsage{out: 40, cr: 400}, "msg_H1", "hephaestus"),
	)

	first, offs, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	tokWant(t, first, 0, 140, 0, 1400, 2)
	tokWantZeus(t, first, 0, 100, 0, 1000)

	tokAppend(t, zeus, tokAssistantFull(t, now, tokUsage{out: 5, cr: 50}, "msg_Z2", "zeus"))
	tokAppend(t, agent, tokAssistantFull(t, now, tokUsage{out: 9, cr: 90}, "msg_H2", "hephaestus"))

	second, _, err := SumTokensDelta(dir, offs)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	tokWant(t, second, 0, 14, 0, 140, 2) // the DELTA of both files
	tokWantZeus(t, second, 0, 5, 0, 50)  // ...split exactly as the first pass was
}

// TestSumTokensDeltaSplitOnAPartialScan pins that a floor is still split. A
// transcript that could only be scanned part way contributes what was read
// (a failed read is not a zero), and dropping the split from that path would
// make the two halves stop adding up to the total for precisely the rows that
// are already inexact — a second, silent error on top of a reported one.
func TestSumTokensDeltaSplitOnAPartialScan(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	good := tokAssistantFull(t, now, tokUsage{out: 100, cr: 1000}, "msg_Z", "zeus")
	monster := `{"type":"assistant","message":{"content":"` + strings.Repeat("x", mineScanBufMax+1) + `"}}`
	mineWrite(t, projects, "-proj", "zeus.jsonl", tokAgentName(t, "zeus-demo"), good, monster)

	got, _, err := SumTokensDelta(dir, nil)
	if err == nil {
		t.Fatal("a truncated scan must report itself")
	}
	tokWant(t, got, 0, 100, 0, 1000, 1)
	tokWantZeus(t, got, 0, 100, 0, 1000)
}

// TestIsAgentTranscriptEdges covers the probe's own boundaries directly, since
// the ones that matter are cheap to state and awkward to reach through a whole
// SumTokensDelta: a marker beyond the line bound, a marker beyond the byte
// bound, and the substring trap — a record whose PROSE contains the marker
// text, which a grep-shaped probe would mis-bin as a whole agent transcript.
func TestIsAgentTranscriptEdges(t *testing.T) {
	dir := t.TempDir()
	filler := `{"type":"user","message":{"content":"x"}}`

	write := func(t *testing.T, name string, lines ...string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return p
	}
	probe := func(t *testing.T, path string) bool {
		t.Helper()
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer f.Close()
		return isAgentTranscript(f, path)
	}

	t.Run("marker on the last line within the bound", func(t *testing.T) {
		lines := make([]string, 0, agentMarkerLines)
		for i := 0; i < agentMarkerLines-1; i++ {
			lines = append(lines, filler)
		}
		lines = append(lines, tokAgentSetting(t, "s"))
		if !probe(t, write(t, "within.jsonl", lines...)) {
			t.Fatal("a marker on the last line inside the bound must be found")
		}
	})

	t.Run("marker past the line bound reads as the lead", func(t *testing.T) {
		lines := make([]string, 0, agentMarkerLines+2)
		for i := 0; i < agentMarkerLines+1; i++ {
			lines = append(lines, filler)
		}
		lines = append(lines, tokAgentSetting(t, "s"))
		if probe(t, write(t, "deep.jsonl", lines...)) {
			t.Fatal("the line bound must actually bound the probe")
		}
	})

	t.Run("marker past the byte bound reads as the lead", func(t *testing.T) {
		fat := `{"type":"user","message":{"content":"` + strings.Repeat("x", agentMarkerBytes) + `"}}`
		if probe(t, write(t, "fat.jsonl", fat, tokAgentSetting(t, "s"))) {
			t.Fatal("the byte bound must actually bound the probe")
		}
	})

	t.Run("the marker text inside prose is not a marker", func(t *testing.T) {
		prose := `{"type":"user","message":{"content":"grep for \"type\":\"agent-setting\" in the head"}}`
		if probe(t, write(t, "prose.jsonl", prose, filler)) {
			t.Fatal("the probe decodes JSON precisely so prose cannot mis-bin a transcript")
		}
		named := `{"type":"user","message":{"content":"the record is \"agentName\":\"hephaestus-x\" today"}}`
		if probe(t, write(t, "prose-name.jsonl", named, filler)) {
			t.Fatal("an agentName quoted in prose must not classify the file either")
		}
	})

	// --- the agentName arm: the one that actually does the work ---

	t.Run("agentName classifies with no agent-setting at all", func(t *testing.T) {
		for _, name := range []string{"hephaestus-demo", "athena-logic-demo", "hermes-fig-1", "explore"} {
			if !probe(t, write(t, "n-"+name+".jsonl", tokAgentName(t, name), filler)) {
				t.Errorf("agentName %q must read as a delegated agent", name)
			}
		}
	})

	t.Run("the two lead prefixes read as the lead", func(t *testing.T) {
		// Both are names kraken itself mints — jobs.ZeusName and
		// jobs.ThreadAgentName — and a test on the jobs side pins that they
		// still begin with these constants.
		for _, name := range []string{
			ZeusAgentNamePrefix + "demo",
			ZeusAgentNamePrefix + "token-count-restyling",
			ThreadAgentNamePrefix + "demo-t1",
			ThreadAgentNamePrefix + "asr-architecture-diffs-t12",
		} {
			if probe(t, write(t, "lead-"+name+".jsonl", tokAgentName(t, name), filler)) {
				t.Errorf("agentName %q is a LEAD's and must not read as delegated", name)
			}
		}
	})

	t.Run("a name merely CONTAINING a lead prefix is delegated", func(t *testing.T) {
		// The test is a PREFIX test, not a substring one: a teammate could
		// legitimately be named after the thing it works on.
		for _, name := range []string{"hephaestus-zeus-refactor", "athena-thread-audit"} {
			if !probe(t, write(t, "sub-"+name+".jsonl", tokAgentName(t, name), filler)) {
				t.Errorf("agentName %q only CONTAINS a lead prefix and must read as delegated", name)
			}
		}
	})

	t.Run("no agentName at all reads as the lead", func(t *testing.T) {
		// 7 root transcripts on this machine look like this — manual claudes
		// carl started in a cell worktree — and the lead is correct for all 7.
		if probe(t, write(t, "manual.jsonl", `{"type":"mode","mode":"normal"}`, filler)) {
			t.Fatal("a transcript with neither signal must read as the lead")
		}
		if probe(t, write(t, "blankname.jsonl", `{"type":"agent-name","agentName":""}`, filler)) {
			t.Fatal("an EMPTY agentName is an absent one, not a non-zeus one")
		}
	})

	t.Run("the first answering record wins", func(t *testing.T) {
		// The scan stops at the first record that answers, which is a real
		// trade and worth pinning in both directions rather than leaving to a
		// reader's assumption. It is safe because the order it depends on is
		// measured: across the 1,245 transcripts carrying both signals,
		// `agent-setting` precedes the first `agentName` in EVERY one, so the
		// second case below does not occur on real data — it documents what
		// would happen if Claude Code ever inverted them.
		if !probe(t, write(t, "both-setting-first.jsonl",
			tokAgentSetting(t, "s"), tokAgentName(t, "hephaestus-demo"))) {
			t.Error("agent-setting first must answer: delegated")
		}
		if probe(t, write(t, "both-name-first.jsonl",
			tokAgentName(t, ZeusAgentNamePrefix+"demo"), tokAgentSetting(t, "s"))) {
			t.Error("a lead agentName reached first answers, and the scan stops there")
		}
	})

	t.Run("a single unterminated line still probes", func(t *testing.T) {
		p := filepath.Join(dir, "midwrite.jsonl")
		if err := os.WriteFile(p, []byte(tokAgentSetting(t, "s")), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if !probe(t, p) {
			t.Fatal("a transcript caught before its first newline must still classify")
		}
	})

	t.Run("an empty file reads as the lead", func(t *testing.T) {
		p := filepath.Join(dir, "empty.jsonl")
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if probe(t, p) {
			t.Fatal("a head with nothing in it degrades to the lead, the conservative bucket")
		}
	})
}

// --- the streaming output_tokens rule ---

// TestSumTokensDeltaTakesTheLastOutputOfAStreamedGroup is the sidecar shape
// the first-wins collapse got wrong. Three of the four counters really are
// stamped identically on every record of a message; `output_tokens` is not.
// In a subagent sidecar the leading records carry a streaming PARTIAL and the
// last carries the total — real groups on this machine read `[2, 10728]`,
// `[3, 3241]`, `[5, 184]` — and keeping the first record took the 2. Measured
// over every cell transcript here: 4,022 of 4,444 sidecar groups affected,
// 31,832 output tokens counted where 4,621,522 were spent (6.57x on all
// sidecar output). It falls entirely on delegated work, which is exactly the
// half the board renders as total-minus-zeus.
func TestSumTokensDeltaTakesTheLastOutputOfAStreamedGroup(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	// One message, three blocks: the other three counters repeat verbatim
	// (which is what the corpus shows) while the output streams up.
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 2, cw: 309, cr: 88000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 1204, cw: 309, cr: 88000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 10728, cw: 309, cr: 88000}, "msg_A", ""),
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	// out is the LAST record's; the other three are the first's, counted once.
	tokWant(t, got, 4, 10728, 309, 88000, 1)
}

// TestSumTokensDeltaRepeatRecordCountsOnlyItsOutput is the other side of the
// same rule, and it is here to stop the fix being "simplified" into counting
// every record. A repeat record contributes its output delta and NOTHING else:
// `input_tokens`, `cache_creation_input_tokens` and `cache_read_input_tokens`
// are per API call, 0 of 39,818 multi-record groups on this machine vary in
// any of them, and summing one eight times over-states a real cell by ~2x.
// The fixture gives the repeat records deliberately non-zero values for those
// three so that counting them would be visible rather than a silent no-op.
func TestSumTokensDeltaRepeatRecordCountsOnlyItsOutput(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")
	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now, tokUsage{in: 7, out: 3, cw: 11, cr: 5000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 900, out: 3241, cw: 900, cr: 900}, "msg_A", ""),
	)
	got, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	tokWant(t, got, 7, 3241, 11, 5000, 1)
}

// TestSumTokensDeltaStreamedGroupSurvivesTheResumeBoundary is the interaction
// the design had to be chosen for: a group whose streaming records straddle a
// resume must end up counted once, at its LAST record's output.
//
// The seed carries the group's committed output as well as its id, recovered
// from the one record precedingGroup already reads. That is exact rather than
// conservative because the forward rule is last-wins: what a pass committed
// for a group is always the output of the last record of it that pass read.
// The two failure modes it rules out are both permanent — these counters only
// accumulate, so nothing downstream can subtract an over-count back out:
// seeding the output at zero would book the tail's full cumulative figure on
// top of the head's, and not resuming the group at all would count the whole
// message twice.
func TestSumTokensDeltaStreamedGroupSurvivesTheResumeBoundary(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")

	path := mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 2, cw: 309, cr: 88000}, "msg_A", ""),
		mineUser(t, now, []any{mineToolResult("t1", false, "ok")}),
	)
	first, offs, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	tokWant(t, first, 4, 2, 309, 88000, 1)

	// the rest of the SAME message lands after the boundary, output still rising
	tokAppend(t, path,
		tokAssistantFull(t, now, tokUsage{in: 4, out: 1204, cw: 309, cr: 88000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 10728, cw: 309, cr: 88000}, "msg_A", ""),
	)
	second, offs, err := SumTokensDelta(dir, offs)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// only the OUTPUT the head had not already booked: 10728 - 2.
	tokWant(t, second, 0, 10726, 0, 0, 1)

	// and a genuinely new message after it still counts in full
	tokAppend(t, path, tokAssistantFull(t, now, tokUsage{in: 1, out: 5, cw: 2, cr: 3}, "msg_B", ""))
	third, _, err := SumTokensDelta(dir, offs)
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	tokWant(t, third, 1, 5, 2, 3, 1)
}

// TestSumTokensDeltaBoundarySeedIgnoresAReplayedRecord pins the one place the
// seed could disagree with the forward tracker. precedingGroup must apply the
// ownership rule exactly as addTokenLine does: a replayed record advances the
// id but commits NO output, so seeding the group's committed figure from its
// usage would make the next pass subtract against a number the total never
// contained — an under-count of a real message.
func TestSumTokensDeltaBoundarySeedIgnoresAReplayedRecord(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	dir := filepath.Join(projects, "-proj")

	path := mineWrite(t, projects, "-proj", "fork.jsonl",
		// a big replayed record from the parent: counted nowhere in this file,
		// and dated before the file existed, as a copied record always is
		tokAssistantFull(t, now-int64(time.Hour/time.Millisecond), tokUsage{out: 9000, cr: 90000}, "msg_P", "parent"),
	)
	first, offs, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	tokWant(t, first, 0, 0, 0, 0, 1)

	// the fork's own next message resumes right after that replayed record
	tokAppend(t, path, tokAssistantFull(t, now, tokUsage{out: 40, cr: 11}, "msg_F", "fork"))
	second, _, err := SumTokensDelta(dir, offs)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	tokWant(t, second, 0, 40, 0, 11, 1)
}

// TestDigestTakesTheLastOutputOfAStreamedGroup is the mine.go half. The dream
// loop ranks the night's sessions off this digest, and digestTranscript
// carried the same first-wins rule inline — so a subagent-heavy session read
// 6.5x low on output and sorted below sessions that had done less.
func TestDigestTakesTheLastOutputOfAStreamedGroup(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(6*time.Hour/time.Millisecond)

	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 2, cw: 309, cr: 88000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 1204, cw: 309, cr: 88000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 10728, cw: 309, cr: 88000}, "msg_A", ""),
	)
	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "agent-1.jsonl")
	if d.Out != 10728 {
		t.Fatalf("digest Out = %d, want 10728 — a group's output is its LAST record's, not its first's", d.Out)
	}
	if d.In != 4 || d.CacheWrite != 309 || d.CacheRead != 88000 {
		t.Fatalf("digest in/cw/cr = %d/%d/%d, want 4/309/88000 — the other three are counted once",
			d.In, d.CacheWrite, d.CacheRead)
	}
	if res.Tokens.Out != 10728 {
		t.Fatalf("MineResult.Tokens.Out = %d, want 10728", res.Tokens.Out)
	}
}

// TestBothReadersShareOneGroupRule is a SHAPE test, not a counting one. The
// first-wins rule used to be written out twice — once in addTokenLine, once
// inline in mine.go's digestTranscript loop — and that is the whole reason a
// premise contradicted by every subagent sidecar ever written read as ground
// truth for as long as it did: the two readers agreed with each other, so any
// test comparing them passed, and neither agreed with the transcripts.
//
// So this pins that the per-job sum and the per-session digest produce the
// SAME four counters for the same streamed group. It would have passed before
// the fix too (both were wrong identically), which is exactly why it is not
// offered as a regression test — it is here to fail if someone re-inlines one
// of the two call sites and then changes only that one.
func TestBothReadersShareOneGroupRule(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(6*time.Hour/time.Millisecond)
	dir := filepath.Join(projects, "-proj")

	mineWrite(t, projects, "-proj", filepath.Join("sess", "subagents", "agent-1.jsonl"),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 2, cw: 309, cr: 88000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 1204, cw: 309, cr: 88000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 4, out: 10728, cw: 309, cr: 88000}, "msg_A", ""),
		tokAssistantFull(t, now, tokUsage{in: 1, out: 5, cw: 2, cr: 3}, "msg_B", ""),
	)

	sum, _, err := SumTokensDelta(dir, nil)
	if err != nil {
		t.Fatalf("SumTokensDelta: %v", err)
	}
	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "agent-1.jsonl")

	if sum.In != d.In || sum.Out != d.Out || sum.CacheWrite != d.CacheWrite || sum.CacheRead != d.CacheRead {
		t.Fatalf("the two readers disagree: sum=%d/%d/%d/%d digest=%d/%d/%d/%d — "+
			"they must share tokenGroup.commit, not each carry a copy of the rule",
			sum.In, sum.Out, sum.CacheWrite, sum.CacheRead, d.In, d.Out, d.CacheWrite, d.CacheRead)
	}
	// and the shared answer is the RIGHT one, so agreeing on a wrong rule
	// cannot satisfy this test the way it could satisfy a bare comparison
	tokWant(t, sum, 5, 10733, 311, 88003, 1)
}

// TestGroupCommitIsTheOneRule exercises tokenGroup.commit directly, because it
// is now the single definition both readers depend on and the table is the
// cheapest place to state its whole contract.
func TestGroupCommitIsTheOneRule(t *testing.T) {
	u := func(in, out, cw, cr int64) tokenUsage {
		return tokenUsage{Input: in, Output: out, CacheCreation: cw, CacheRead: cr}
	}

	t.Run("a new group contributes all four", func(t *testing.T) {
		var g tokenGroup
		if got := g.commit("m1", u(1, 2, 3, 4), true); got != u(1, 2, 3, 4) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a repeat contributes only the output delta", func(t *testing.T) {
		var g tokenGroup
		g.commit("m1", u(4, 2, 309, 88000), true)
		// the other three are a verbatim repeat and must not be counted again;
		// deliberately non-zero here so counting them would be visible
		if got := g.commit("m1", u(4, 10728, 309, 88000), true); got != u(0, 10726, 0, 0) {
			t.Fatalf("got %+v, want output delta only", got)
		}
	})

	t.Run("an ineligible record commits nothing but advances the id", func(t *testing.T) {
		var g tokenGroup
		if got := g.commit("m1", u(9, 9, 9, 9), false); got != (tokenUsage{}) {
			t.Fatalf("got %+v, want zeros", got)
		}
		if g.id != "m1" {
			t.Fatalf("id = %q — the tracker follows the file's record sequence, not the counted subset", g.id)
		}
		// and because nothing was committed, a same-id record after it must
		// contribute its output in FULL rather than subtracting against a
		// figure no total contains
		if got := g.commit("m1", u(0, 40, 0, 0), true); got.Output != 40 {
			t.Fatalf("got %+v, want the full 40", got)
		}
	})

	// The three flip shapes, spelled out because the middle one is the defect
	// both reviewers found latent in the first version of this helper and the
	// other two are what an incautious fix for it would break.
	t.Run("eligible then INELIGIBLE then eligible does not over-count", func(t *testing.T) {
		var g tokenGroup
		g.commit("m1", u(0, 2, 0, 0), true) // committed 2
		// a replayed record of the SAME message: contributes nothing, and must
		// leave the group's committed figure ALONE. Zeroing it here made the
		// next record contribute its whole cumulative output on top of the 2 —
		// a true 10,728 summed to 10,730, an over-count, which is the one
		// direction accumulate-only counters can never recover from.
		if got := g.commit("m1", u(0, 5000, 0, 0), false); got != (tokenUsage{}) {
			t.Fatalf("got %+v, want zeros", got)
		}
		if got := g.commit("m1", u(0, 10728, 0, 0), true); got.Output != 10726 {
			t.Fatalf("got output %d, want the delta 10726 (2 + 10726 = the true 10728, not 10730)", got.Output)
		}
	})

	t.Run("an INELIGIBLE record starting a new group clears the old figure", func(t *testing.T) {
		var g tokenGroup
		g.commit("m1", u(0, 500, 0, 0), true) // a previous group committed 500
		// this record opens a DIFFERENT message, so the 500 must not carry
		// over: leaving it makes the next record subtract against it and
		// contribute 30-500, summing a true 530 to 30.
		g.commit("m2", u(0, 9, 0, 0), false)
		if got := g.commit("m2", u(0, 30, 0, 0), true); got.Output != 30 {
			t.Fatalf("got output %d, want the full 30 — a new group starts from nothing", got.Output)
		}
	})

	t.Run("a group straddling the gate keeps its partial", func(t *testing.T) {
		// eligible then INELIGIBLE: the digest's window gate closing partway
		// through a message. The total keeps what the eligible records
		// established and the trailing ones add nothing.
		var g tokenGroup
		total := g.commit("m1", u(0, 2, 0, 0), true).Output
		total += g.commit("m1", u(0, 1204, 0, 0), true).Output
		total += g.commit("m1", u(0, 10728, 0, 0), false).Output
		if total != 1204 {
			t.Fatalf("total = %d, want 1204", total)
		}
	})

	t.Run("an INELIGIBLE record with an empty id still clears", func(t *testing.T) {
		// The matrix cell the guard newly depends on. `repeat` is
		// `id != "" && id == g.id`, and it is the FIRST conjunct that forces
		// the clear here: an unidentifiable record can never be a continuation
		// of anything, so it must end the group. Drop that conjunct — an
		// inviting simplification, since the second looks sufficient — and two
		// consecutive blank ids compare equal, the clear is skipped, and the
		// next record deltas against a figure belonging to a different message.
		var g tokenGroup
		total := g.commit("", u(0, 500, 0, 0), true).Output
		g.commit("", u(0, 9, 0, 0), false)
		total += g.commit("", u(0, 30, 0, 0), true).Output
		if total != 530 {
			t.Fatalf("total = %d, want 530 — a blank id never groups, so the 30 is a fresh contribution "+
				"(a repeat test without the id != \"\" conjunct yields 30: 500 + (30-500))", total)
		}
	})

	t.Run("a decreasing tail walks the total back down", func(t *testing.T) {
		// The one arithmetic path in commit with no live example: 0 groups in
		// the corpus are non-monotonic. Under last-wins a group whose tail
		// reports less than its head is still worth its TAIL, so the delta is
		// negative and the running total comes back down to it. Clamping this
		// at zero would quietly turn last-wins into max-wins for exactly the
		// shape that distinguishes them.
		var g tokenGroup
		total := g.commit("m1", u(0, 900, 0, 0), true).Output
		d := g.commit("m1", u(0, 400, 0, 0), true)
		if d.Output != -500 {
			t.Fatalf("delta = %d, want -500", d.Output)
		}
		if total += d.Output; total != 400 {
			t.Fatalf("total = %d, want the tail's 400", total)
		}
		// and the group's committed figure follows the tail, so a further
		// record deltas against 400 rather than against the high-water 900
		if got := g.commit("m1", u(0, 450, 0, 0), true); got.Output != 50 {
			t.Fatalf("got %d, want 50 — last-wins tracks the tail, not the maximum", got.Output)
		}
	})

	t.Run("an empty id never groups", func(t *testing.T) {
		var g tokenGroup
		g.commit("", u(0, 7, 0, 0), true)
		if got := g.commit("", u(0, 11, 0, 0), true); got != u(0, 11, 0, 0) {
			t.Fatalf("got %+v — an unidentifiable record is counted on its own", got)
		}
	})
}
