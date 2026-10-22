package core

// Blind contract tests (athena-logic) for slice A of dream-2026-09-27: the
// transcript head probe skips an over-cap line instead of ending the scan.
// Every assertion derives from 50-contract.md; nothing here was written with
// sight of the implementation.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// hplWrite writes lines joined by "\n" (plus a trailing newline when trail is
// true) to name under a fresh temp dir and returns the path.
func hplWrite(t *testing.T, name string, lines []string, trail bool) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	body := strings.Join(lines, "\n")
	if trail {
		body += "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

// hplOpen writes the fixture and opens it; the file is closed at cleanup.
func hplOpen(t *testing.T, lines []string, trail bool) (*os.File, string) {
	t.Helper()
	p := hplWrite(t, "sess.jsonl", lines, trail)
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f, p
}

// hplOverCap is a valid JSONL record longer than agentMarkerBytes.
func hplOverCap(t *testing.T) string {
	t.Helper()
	l := mineJSON(t, map[string]any{"type": "attachment", "message": strings.Repeat("x", agentMarkerBytes+1)})
	if len(l) <= agentMarkerBytes {
		t.Fatalf("over-cap fixture is %d bytes, want > %d", len(l), agentMarkerBytes)
	}
	return l
}

// hplFiller is a small valid record with a distinguishable type.
func hplFiller(t *testing.T, i int) string {
	t.Helper()
	return mineJSON(t, map[string]any{"type": fmt.Sprintf("filler-%d", i)})
}

// hplCollect runs headScan to completion (visit never answers) and returns
// every visited record.
func hplCollect(f *os.File) []headMarker {
	var got []headMarker
	headScan(f, func(rec headMarker) bool {
		got = append(got, rec)
		return false
	})
	return got
}

func hplTypes(recs []headMarker) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Type
	}
	return out
}

// ---- headScan ----

func TestHeadScanVisitsEachRecordOnceInOrderAndDecodeFailureIsAZeroRecord(t *testing.T) {
	f, _ := hplOpen(t, []string{
		tokAgentSetting(t, "s"),
		"this is not json",
		tokAgentName(t, "hephaestus-a"),
	}, true)
	got := hplCollect(f)
	if len(got) != 3 {
		t.Fatalf("visited %d records, want 3: %+v", len(got), got)
	}
	if got[0].Type != "agent-setting" {
		t.Errorf("record 1 Type = %q, want agent-setting", got[0].Type)
	}
	if !reflect.DeepEqual(got[1], headMarker{}) {
		t.Errorf("record 2 (decode failure) = %+v, want zero headMarker", got[1])
	}
	if got[2].AgentName != "hephaestus-a" {
		t.Errorf("record 3 AgentName = %q, want hephaestus-a", got[2].AgentName)
	}
}

func TestHeadScanSkipsOverCapLineWithoutVisitingItAndContinues(t *testing.T) {
	f, _ := hplOpen(t, []string{hplFiller(t, 1), hplOverCap(t), hplFiller(t, 3)}, true)
	got := hplTypes(hplCollect(f))
	want := []string{"filler-1", "filler-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visited types = %v, want %v", got, want)
	}
}

func TestHeadScanStopsAtFirstVisitReturningTrue(t *testing.T) {
	var lines []string
	for i := 1; i <= 5; i++ {
		lines = append(lines, hplFiller(t, i))
	}
	f, _ := hplOpen(t, lines, true)
	var got []string
	headScan(f, func(rec headMarker) bool {
		got = append(got, rec.Type)
		return rec.Type == "filler-2"
	})
	want := []string{"filler-1", "filler-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visited types = %v, want %v", got, want)
	}
}

func TestHeadScanStopsAfterAgentMarkerLinesRecords(t *testing.T) {
	var lines []string
	for i := 1; i <= agentMarkerLines+4; i++ {
		lines = append(lines, hplFiller(t, i))
	}
	f, _ := hplOpen(t, lines, true)
	got := hplTypes(hplCollect(f))
	var want []string
	for i := 1; i <= agentMarkerLines; i++ {
		want = append(want, fmt.Sprintf("filler-%d", i))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visited types = %v, want %v", got, want)
	}
}

func TestHeadScanRecordBoundCountsSkippedOverCapLine(t *testing.T) {
	lines := []string{hplOverCap(t)}
	for i := 2; i <= agentMarkerLines+4; i++ {
		lines = append(lines, hplFiller(t, i))
	}
	f, _ := hplOpen(t, lines, true)
	got := hplTypes(hplCollect(f))
	var want []string
	for i := 2; i <= agentMarkerLines; i++ {
		want = append(want, fmt.Sprintf("filler-%d", i))
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visited types = %v, want %v (over-cap line counts as record 1)", got, want)
	}
}

func TestHeadScanRecordBoundCountsDecodeFailures(t *testing.T) {
	var lines []string
	for i := 1; i <= agentMarkerLines; i++ {
		lines = append(lines, "garbage")
	}
	lines = append(lines, hplFiller(t, agentMarkerLines+1))
	f, _ := hplOpen(t, lines, true)
	got := hplCollect(f)
	if len(got) != agentMarkerLines {
		t.Fatalf("visited %d records, want %d", len(got), agentMarkerLines)
	}
	for i, r := range got {
		if !reflect.DeepEqual(r, headMarker{}) {
			t.Errorf("record %d = %+v, want zero headMarker", i+1, r)
		}
	}
}

func TestHeadScanVisitsFinalLineWithoutTrailingNewline(t *testing.T) {
	f, _ := hplOpen(t, []string{hplFiller(t, 1), hplFiller(t, 2)}, false)
	got := hplTypes(hplCollect(f))
	want := []string{"filler-1", "filler-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("visited types = %v, want %v", got, want)
	}
}

func TestHeadScanReadsFromByteZeroAndLeavesFileOffsetUnchanged(t *testing.T) {
	f, _ := hplOpen(t, []string{hplFiller(t, 1), hplFiller(t, 2), hplFiller(t, 3)}, true)
	const at = 7
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		t.Fatalf("seek: %v", err)
	}
	got := hplTypes(hplCollect(f))
	want := []string{"filler-1", "filler-2", "filler-3"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("visited types = %v, want %v", got, want)
	}
	off, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("seek current: %v", err)
	}
	if off != at {
		t.Errorf("offset after headScan = %d, want %d", off, at)
	}
}

// One window-crossing fixture for this file: an over-cap line that stops just
// short of agentMarkerWindow, followed by a marker line that begins inside the
// window but ends past it. It must not be decoded by any of the three.
func TestHeadProbeWindowRecordNotEndingWithinWindowIsNotDecoded(t *testing.T) {
	marker := tokAgentName(t, "hephaestus-window")
	prefix := `{"type":"attachment","message":"`
	suffix := `"}`
	// line 1 length L (excl newline); line 2 starts at L+1 = agentMarkerWindow-10,
	// and is far longer than 10 bytes, so it ends past the window.
	L := agentMarkerWindow - 11
	pad := L - len(prefix) - len(suffix)
	big := prefix + strings.Repeat("x", pad) + suffix
	if len(big) != L {
		t.Fatalf("fixture line 1 is %d bytes, want %d", len(big), L)
	}
	if len(big)+1+len(marker) <= agentMarkerWindow {
		t.Fatalf("fixture marker line does not cross the window")
	}
	f, p := hplOpen(t, []string{big, marker}, true)

	if got := hplCollect(f); len(got) != 0 {
		t.Errorf("headScan visited %d records, want 0: %+v", len(got), got)
	}
	if got := headAgentName(f); got != "" {
		t.Errorf("headAgentName = %q, want \"\"", got)
	}
	if isAgentTranscript(f, p) {
		t.Errorf("isAgentTranscript = true, want false")
	}
}

// ---- isAgentTranscript ----

func TestIsAgentTranscriptSubagentsPathArmAnswersTrueBeforeReading(t *testing.T) {
	// Content alone would answer false (a lead name); the path arm must win.
	dir := filepath.Join(t.TempDir(), "sess", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "agent-1.jsonl")
	if err := os.WriteFile(p, []byte(tokAgentName(t, "zeus-foo")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !isAgentTranscript(f, p) {
		t.Fatalf("isAgentTranscript on a subagents/ path = false, want true")
	}
	off, _ := f.Seek(0, io.SeekCurrent)
	if off != 0 {
		t.Errorf("offset = %d, want 0", off)
	}
}

func TestIsAgentTranscriptAgentSettingRecordIsTrue(t *testing.T) {
	f, p := hplOpen(t, []string{hplFiller(t, 1), tokAgentSetting(t, "s")}, true)
	if !isAgentTranscript(f, p) {
		t.Fatal("isAgentTranscript = false, want true")
	}
}

func TestIsAgentTranscriptDelegatedAgentNameIsTrue(t *testing.T) {
	f, p := hplOpen(t, []string{hplFiller(t, 1), tokAgentName(t, "hephaestus-a")}, true)
	if !isAgentTranscript(f, p) {
		t.Fatal("isAgentTranscript = false, want true")
	}
}

func TestIsAgentTranscriptLeadAgentNameIsFalse(t *testing.T) {
	for _, name := range []string{"zeus-foo", "thread-foo-3"} {
		f, p := hplOpen(t, []string{tokAgentName(t, name)}, true)
		if isAgentTranscript(f, p) {
			t.Errorf("isAgentTranscript(%q) = true, want false", name)
		}
	}
}

func TestIsAgentTranscriptFirstAnsweringRecordEndsScan(t *testing.T) {
	// lead name first answers false; the agent-setting after it is never reached.
	f, p := hplOpen(t, []string{tokAgentName(t, "zeus-foo"), tokAgentSetting(t, "s")}, true)
	if isAgentTranscript(f, p) {
		t.Errorf("lead name then agent-setting = true, want false")
	}
	// agent-setting first answers true; the lead name after it is never reached.
	f2, p2 := hplOpen(t, []string{tokAgentSetting(t, "s"), tokAgentName(t, "zeus-foo")}, true)
	if !isAgentTranscript(f2, p2) {
		t.Errorf("agent-setting then lead name = false, want true")
	}
}

func TestIsAgentTranscriptNoMarkerIsFalse(t *testing.T) {
	f, p := hplOpen(t, []string{hplFiller(t, 1), hplFiller(t, 2), "garbage"}, true)
	if isAgentTranscript(f, p) {
		t.Fatal("isAgentTranscript = true, want false")
	}
}

func TestIsAgentTranscriptMarkerTextInProseIsNotAMarker(t *testing.T) {
	prose := mineJSON(t, map[string]any{
		"type":    "user",
		"content": `see {"type":"agent-setting","agentSetting":"x"} and {"agentName":"hephaestus-a"}`,
	})
	f, p := hplOpen(t, []string{prose}, true)
	if isAgentTranscript(f, p) {
		t.Fatal("isAgentTranscript = true for marker text inside prose, want false")
	}
}

func TestIsAgentTranscriptFindsMarkerAfterOverCapLine(t *testing.T) {
	f, p := hplOpen(t, []string{hplOverCap(t), tokAgentName(t, "hephaestus-a")}, true)
	if !isAgentTranscript(f, p) {
		t.Fatal("isAgentTranscript = false, want true (marker follows a skipped over-cap line)")
	}
}

func TestIsAgentTranscriptMarkerOnRecordAgentMarkerLinesIsFound(t *testing.T) {
	lines := []string{hplOverCap(t)}
	for i := 2; i < agentMarkerLines; i++ {
		lines = append(lines, hplFiller(t, i))
	}
	lines = append(lines, tokAgentSetting(t, "s")) // record agentMarkerLines
	f, p := hplOpen(t, lines, true)
	if !isAgentTranscript(f, p) {
		t.Fatal("marker on record agentMarkerLines (over-cap line counted) = false, want true")
	}
}

func TestIsAgentTranscriptMarkerPastRecordBoundCountingOverCapIsNotFound(t *testing.T) {
	lines := []string{hplOverCap(t)}
	for i := 2; i <= agentMarkerLines; i++ {
		lines = append(lines, hplFiller(t, i))
	}
	lines = append(lines, tokAgentSetting(t, "s")) // record agentMarkerLines+1
	f, p := hplOpen(t, lines, true)
	if isAgentTranscript(f, p) {
		t.Fatal("marker on record agentMarkerLines+1 (over-cap line counted) = true, want false")
	}
}

func TestIsAgentTranscriptLeavesFileOffsetUnchanged(t *testing.T) {
	f, p := hplOpen(t, []string{hplFiller(t, 1), tokAgentName(t, "hephaestus-a")}, true)
	const at = 3
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if !isAgentTranscript(f, p) {
		t.Errorf("isAgentTranscript = false, want true (scan reads from byte 0)")
	}
	off, _ := f.Seek(0, io.SeekCurrent)
	if off != at {
		t.Errorf("offset = %d, want %d", off, at)
	}
}

// ---- headAgentName ----

func TestHeadAgentNameReturnsFirstNameAndAgentSettingDoesNotEndScan(t *testing.T) {
	f, _ := hplOpen(t, []string{
		tokAgentSetting(t, "s"),
		tokAgentName(t, "hephaestus-a"),
		tokAgentName(t, "athena-b"),
	}, true)
	if got := headAgentName(f); got != "hephaestus-a" {
		t.Fatalf("headAgentName = %q, want hephaestus-a", got)
	}
}

func TestHeadAgentNameReturnsLeadNameToo(t *testing.T) {
	f, _ := hplOpen(t, []string{tokAgentName(t, "zeus-foo")}, true)
	if got := headAgentName(f); got != "zeus-foo" {
		t.Fatalf("headAgentName = %q, want zeus-foo", got)
	}
}

func TestHeadAgentNameNoNameIsEmpty(t *testing.T) {
	f, _ := hplOpen(t, []string{tokAgentSetting(t, "s"), hplFiller(t, 2), "garbage"}, true)
	if got := headAgentName(f); got != "" {
		t.Fatalf("headAgentName = %q, want \"\"", got)
	}
}

func TestHeadAgentNameFindsNameAfterOverCapLine(t *testing.T) {
	f, _ := hplOpen(t, []string{hplOverCap(t), tokAgentName(t, "hephaestus-a")}, true)
	if got := headAgentName(f); got != "hephaestus-a" {
		t.Fatalf("headAgentName = %q, want hephaestus-a", got)
	}
}

func TestHeadAgentNameNamePastRecordBoundIsEmpty(t *testing.T) {
	lines := []string{hplOverCap(t)}
	for i := 2; i <= agentMarkerLines; i++ {
		lines = append(lines, hplFiller(t, i))
	}
	lines = append(lines, tokAgentName(t, "hephaestus-a")) // record agentMarkerLines+1
	f, _ := hplOpen(t, lines, true)
	if got := headAgentName(f); got != "" {
		t.Fatalf("headAgentName = %q, want \"\" (name past record bound)", got)
	}
}

func TestHeadAgentNameDecodesLineExactlyAtCeiling(t *testing.T) {
	prefix := `{"type":"agent-name","agentName":"hephaestus-a","pad":"`
	suffix := `"}`
	line := prefix + strings.Repeat("x", agentMarkerBytes-len(prefix)-len(suffix)) + suffix
	if len(line) != agentMarkerBytes {
		t.Fatalf("fixture is %d bytes, want %d", len(line), agentMarkerBytes)
	}
	f, _ := hplOpen(t, []string{line}, true)
	if got := headAgentName(f); got != "hephaestus-a" {
		t.Fatalf("headAgentName = %q, want hephaestus-a (line == agentMarkerBytes is decoded)", got)
	}
}

func TestHeadAgentNameLeavesFileOffsetUnchanged(t *testing.T) {
	f, _ := hplOpen(t, []string{hplFiller(t, 1), tokAgentName(t, "hephaestus-a")}, true)
	const at = 4
	if _, err := f.Seek(at, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if got := headAgentName(f); got != "hephaestus-a" {
		t.Errorf("headAgentName = %q, want hephaestus-a (scan reads from byte 0)", got)
	}
	off, _ := f.Seek(0, io.SeekCurrent)
	if off != at {
		t.Errorf("offset = %d, want %d", off, at)
	}
}
