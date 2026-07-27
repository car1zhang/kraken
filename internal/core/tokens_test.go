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
	return mineJSON(t, map[string]any{
		"type": "assistant", "timestamp": mineISO(ts),
		"message": map[string]any{
			"model": "claude-opus-5",
			"usage": map[string]any{
				"input_tokens":                u.in,
				"output_tokens":               u.out,
				"cache_creation_input_tokens": u.cw,
				"cache_read_input_tokens":     u.cr,
			},
			"content": []any{mineText("done")},
		},
	})
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
