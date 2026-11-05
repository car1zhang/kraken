package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ransince_test.go — TranscriptRanSince: "did this agent produce output
// after moment T", read from its transcript.

// ranSinceBase is a fixed instant the fixtures hang their records off.
var ranSinceBase = time.Date(2026, 11, 5, 22, 57, 53, 0, time.UTC)

func ranSinceTS(offset time.Duration) string {
	return ranSinceBase.Add(offset).Format("2006-01-02T15:04:05.000Z")
}

func ranSinceMS(offset time.Duration) int64 {
	return ranSinceBase.Add(offset).UnixMilli()
}

func ranSinceAssistant(offset time.Duration) string {
	return `{"type":"assistant","timestamp":"` + ranSinceTS(offset) + `","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"ACK"}]}}`
}

func ranSinceUser(offset time.Duration, text string) string {
	return `{"type":"user","timestamp":"` + ranSinceTS(offset) + `","isSidechain":false,"message":{"role":"user","content":"` + text + `"}}`
}

// ranSinceFile writes lines to a fresh jsonl file whose mtime is set to
// mtimeOffset past the base, since the fast path reads the mtime.
func ranSinceFile(t *testing.T, mtimeOffset time.Duration, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := ranSinceBase.Add(mtimeOffset)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
	return path
}

// The measured case: an idle agent is frozen, a message lands minutes later
// and the agent answers in a few seconds. The trailing bookkeeping records
// carry no timestamp.
func TestTranscriptRanSinceSeesATurnAfterTheMoment(t *testing.T) {
	path := ranSinceFile(t, 4*time.Minute,
		ranSinceUser(-3*time.Minute, "start"),
		ranSinceAssistant(-3*time.Minute+3*time.Second),
		ranSinceUser(3*time.Minute, "a watch fired"),
		ranSinceAssistant(3*time.Minute+2*time.Second),
		ranSinceAssistant(3*time.Minute+6*time.Second),
		`{"type":"system","timestamp":"`+ranSinceTS(3*time.Minute+6*time.Second)+`","isSidechain":false}`,
		`{"type":"last-prompt"}`,
		`{"type":"custom-title"}`,
	)
	if !TranscriptRanSince(path, ranSinceMS(0)) {
		t.Fatal("an assistant record three minutes after the moment was not seen")
	}
}

// Records appended without the agent running (a typed slash command, a
// hook's attachment, bookkeeping) move the mtime and are not a turn.
func TestTranscriptRanSinceIgnoresAppendsThatAreNotATurn(t *testing.T) {
	path := ranSinceFile(t, 4*time.Minute,
		ranSinceUser(-3*time.Minute, "start"),
		ranSinceAssistant(-3*time.Minute+3*time.Second),
		ranSinceUser(3*time.Minute, "<command-name>/color</command-name>"),
		`{"type":"attachment","timestamp":"`+ranSinceTS(3*time.Minute)+`","isSidechain":false}`,
		`{"type":"system","timestamp":"`+ranSinceTS(3*time.Minute)+`","isSidechain":false}`,
		`{"type":"last-prompt"}`,
	)
	if TranscriptRanSince(path, ranSinceMS(0)) {
		t.Fatal("a user record and bookkeeping after the moment read as the agent having run")
	}
}

// A user record whose TEXT contains the word assistant must not be taken for
// one: the cheap substring test only selects lines worth decoding.
func TestTranscriptRanSinceDecodesTheRecordType(t *testing.T) {
	path := ranSinceFile(t, 4*time.Minute,
		ranSinceAssistant(-time.Minute),
		ranSinceUser(3*time.Minute, `ask the \"assistant\" to continue`),
	)
	if TranscriptRanSince(path, ranSinceMS(0)) {
		t.Fatal("a user record quoting the word assistant read as an assistant record")
	}
}

// A subagent's sidechain record is not the lead's own output.
func TestTranscriptRanSinceIgnoresSidechainRecords(t *testing.T) {
	path := ranSinceFile(t, 4*time.Minute,
		ranSinceAssistant(-time.Minute),
		`{"type":"assistant","timestamp":"`+ranSinceTS(3*time.Minute)+`","isSidechain":true,"message":{"role":"assistant","content":[]}}`,
	)
	if TranscriptRanSince(path, ranSinceMS(0)) {
		t.Fatal("a sidechain assistant record read as the lead having run")
	}
}

// The boundary: a record stamped exactly at the moment is not after it.
func TestTranscriptRanSinceBoundary(t *testing.T) {
	path := ranSinceFile(t, time.Minute, ranSinceAssistant(0))
	if TranscriptRanSince(path, ranSinceMS(0)) {
		t.Error("an assistant record stamped exactly at the moment read as after it")
	}
	if !TranscriptRanSince(path, ranSinceMS(-time.Millisecond)) {
		t.Error("an assistant record one millisecond after the moment was not seen")
	}
}

// An mtime at or before the moment answers without reading: nothing at all
// was written, whatever the lines say.
func TestTranscriptRanSinceMtimeFastPath(t *testing.T) {
	// the record claims to be later than the file's own mtime, which cannot
	// happen for real; it proves the lines were never read
	path := ranSinceFile(t, -time.Second, ranSinceAssistant(time.Hour))
	if TranscriptRanSince(path, ranSinceMS(0)) {
		t.Fatal("a file not modified since the moment read as the agent having run")
	}
}

func TestTranscriptRanSinceUnreadableInputsAreFalse(t *testing.T) {
	if TranscriptRanSince("", ranSinceMS(0)) {
		t.Error("an empty path read as true")
	}
	if TranscriptRanSince(filepath.Join(t.TempDir(), "missing.jsonl"), ranSinceMS(0)) {
		t.Error("a missing file read as true")
	}
	path := ranSinceFile(t, time.Minute, ranSinceAssistant(30*time.Second))
	if TranscriptRanSince(path, 0) {
		t.Error("a zero moment read as true: there is no hold to have run since")
	}
	garbage := ranSinceFile(t, time.Minute, `{"type":"assistant","timestamp":`, `not json at all`)
	if TranscriptRanSince(garbage, ranSinceMS(0)) {
		t.Error("an undecodable assistant line read as true")
	}
}

// The answer is cached on (size, mtime, moment), and a grown file is read
// again: the same path first says no, then yes once the turn is appended.
func TestTranscriptRanSinceRereadsAGrownFile(t *testing.T) {
	path := ranSinceFile(t, time.Minute,
		ranSinceAssistant(-time.Minute),
		ranSinceUser(30*time.Second, "a watch fired"),
	)
	since := ranSinceMS(0)
	if TranscriptRanSince(path, since) {
		t.Fatal("no turn yet, read as true")
	}
	if TranscriptRanSince(path, since) {
		t.Fatal("the cached answer for an unchanged file flipped to true")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(ranSinceAssistant(40*time.Second) + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	mt := ranSinceBase.Add(2 * time.Minute)
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
	if !TranscriptRanSince(path, since) {
		t.Fatal("the appended turn was not seen: a stale cached answer was served for a grown file")
	}
	// a later moment on the same unchanged file is a different question
	if TranscriptRanSince(path, ranSinceMS(time.Minute)) {
		t.Fatal("a moment after the last assistant record read as true")
	}
}

// A turn whose last assistant record is larger than the first tail window:
// the window begins inside that record, its partial line is dropped, and no
// other assistant record is in view. The read has to grow rather than
// conclude nothing ran.
func TestTranscriptRanSinceSeesARecordLargerThanTheFirstWindow(t *testing.T) {
	huge := `{"type":"assistant","timestamp":"` + ranSinceTS(3*time.Minute) +
		`","isSidechain":false,"message":{"role":"assistant","content":[{"type":"text","text":"` +
		strings.Repeat("x", ranSinceWindow+(100<<10)) + `"}]}}`
	path := ranSinceFile(t, 4*time.Minute,
		ranSinceAssistant(-time.Minute),
		ranSinceUser(3*time.Minute-time.Second, "a watch fired"),
		huge,
		`{"type":"system","timestamp":"`+ranSinceTS(3*time.Minute)+`","isSidechain":false}`,
	)
	if !TranscriptRanSince(path, ranSinceMS(0)) {
		t.Fatal("an assistant record larger than the first tail window was not seen")
	}
}

// Growing must not turn "no assistant record anywhere" into a true: a file
// read to its head with nothing in it is still false.
func TestTranscriptRanSinceReadsToTheHeadAndStaysFalse(t *testing.T) {
	filler := `{"type":"attachment","timestamp":"` + ranSinceTS(3*time.Minute) + `","pad":"` +
		strings.Repeat("y", 300<<10) + `"}`
	path := ranSinceFile(t, 4*time.Minute, filler, filler, filler)
	if TranscriptRanSince(path, ranSinceMS(0)) {
		t.Fatal("a transcript with no assistant record at all read as the agent having run")
	}
}

// And an OLD assistant record found only after growing still decides false.
func TestTranscriptRanSinceGrowsToAnOlderRecordAndStops(t *testing.T) {
	filler := `{"type":"attachment","timestamp":"` + ranSinceTS(3*time.Minute) + `","pad":"` +
		strings.Repeat("y", 300<<10) + `"}`
	path := ranSinceFile(t, 4*time.Minute,
		ranSinceAssistant(-time.Minute),
		filler, filler, filler,
	)
	if TranscriptRanSince(path, ranSinceMS(0)) {
		t.Fatal("only an assistant record from before the moment exists, read as true")
	}
}

// A codex rollout has no assistant-record vocabulary here: its mtime moving
// past the moment is the whole answer.
func TestTranscriptRanSinceCodexRolloutUsesMtime(t *testing.T) {
	// IsCodexRollout wants a "sessions" path component and a rollout-*.jsonl
	// base name.
	dir := filepath.Join(t.TempDir(), "sessions", "2026", "11", "05")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "rollout-2026-11-05T22-00-00-0199aaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"response_item"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsCodexRollout(path) {
		t.Fatalf("fixture %s is not recognised as a codex rollout", path)
	}
	after := ranSinceBase.Add(time.Minute)
	if err := os.Chtimes(path, after, after); err != nil {
		t.Fatal(err)
	}
	if !TranscriptRanSince(path, ranSinceMS(0)) {
		t.Error("a rollout modified after the moment read as false")
	}
	before := ranSinceBase.Add(-time.Minute)
	if err := os.Chtimes(path, before, before); err != nil {
		t.Fatal(err)
	}
	if TranscriptRanSince(path, ranSinceMS(0)) {
		t.Error("a rollout not modified since the moment read as true")
	}
}
