package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// handofflastsaid_contract_test.go — athena-logic's BLIND contract tests for
// the handoff hook's "last words before this compaction" line
// (50-contract.md: O1 lastSaidBefore, O2 runHandoffHook, O3
// handoffLastSaidLine).
//
// Dimension: LOGIC — the stated obligations and happy paths, one answer per
// assertion. Refusals, malformed input and failure fallbacks are athena-edge's.
//
// Every package-level identifier is prefixed `lsLogic`: sibling authors share
// this package's declaration space. Every transcript here is INVENTED.

// lsLogicNow is a fixed clock so every age is exact.
var lsLogicNow = time.Date(2026, 1, 2, 3, 59, 0, 0, time.UTC)

// lsLogicContent is one element of an assistant record's message.content.
type lsLogicContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Name string `json:"name,omitempty"`
}

// lsLogicText is a text content element.
func lsLogicText(s string) lsLogicContent { return lsLogicContent{Type: "text", Text: s} }

// lsLogicToolUse is a non-text content element.
func lsLogicToolUse() lsLogicContent { return lsLogicContent{Type: "tool_use", Name: "Bash"} }

// lsLogicMarshal encodes v as one JSONL line.
func lsLogicMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(b)
}

// lsLogicAssistant is a main-chain assistant record with the given top-level
// timestamp ("" omits the key) and content elements.
func lsLogicAssistant(t *testing.T, ts string, content ...lsLogicContent) string {
	t.Helper()
	rec := map[string]any{
		"type":    "assistant",
		"message": map[string]any{"role": "assistant", "content": content},
	}
	if ts != "" {
		rec["timestamp"] = ts
	}
	return lsLogicMarshal(t, rec)
}

// lsLogicBoundary is a compact_boundary system record.
func lsLogicBoundary(t *testing.T, ts string) string {
	t.Helper()
	return lsLogicMarshal(t, map[string]any{"type": "system", "subtype": "compact_boundary", "timestamp": ts})
}

// lsLogicWriteTranscript writes the lines as a JSONL file in a fresh temp dir
// and returns its absolute path.
func lsLogicWriteTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

// lsLogicMustSay runs lastSaidBefore and fails unless it answers true.
func lsLogicMustSay(t *testing.T, p string) lastSaid {
	t.Helper()
	ls, ok := lastSaidBefore(p)
	if !ok {
		t.Fatalf("lastSaidBefore(%q) = false, want an answer", p)
	}
	return ls
}

// lsLogicWriteDoc writes an 80-handoff doc into dir aged `age` before
// lsLogicNow and returns its absolute path.
func lsLogicWriteDoc(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("# handoff\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	mt := lsLogicNow.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

// lsLogicPayload builds a hook payload; a nil transcript omits the key.
func lsLogicPayload(t *testing.T, source string, transcript *string) string {
	t.Helper()
	m := map[string]any{"session_id": "sess-ls", "hook_event_name": "SessionStart"}
	if source != "" {
		m["source"] = source
	}
	if transcript != nil {
		m["transcript_path"] = *transcript
	}
	return lsLogicMarshal(t, m)
}

// lsLogicRun runs the hook and returns raw stdout.
func lsLogicRun(payload, dir string) string {
	var out bytes.Buffer
	runHandoffHook(strings.NewReader(payload), &out, dir, lsLogicNow)
	return out.String()
}

// lsLogicHookOut is the frozen output shape.
type lsLogicHookOut struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// lsLogicDecode asserts raw is exactly one JSON object plus a trailing newline
// with hookEventName SessionStart, and returns additionalContext.
func lsLogicDecode(t *testing.T, raw string) string {
	t.Helper()
	if !strings.HasSuffix(raw, "\n") || strings.HasSuffix(raw, "\n\n") {
		t.Fatalf("stdout %q does not end in exactly one newline", raw)
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	var out lsLogicHookOut
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("stdout %q is not a JSON object: %v", raw, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout %q carries more than one JSON value", raw)
	}
	if got := out.HookSpecificOutput.HookEventName; got != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", got)
	}
	return out.HookSpecificOutput.AdditionalContext
}

// lsLogicTail is the frozen sentence after «<FIRST>».
const lsLogicTail = " — that message was delivered; a summary listing it as still to be sent is wrong, so do not send it again unless the work since has changed it."

// lsLogicWantLine spells O3's frozen line for a given WHEN and FIRST.
func lsLogicWantLine(when, first string) string {
	return "Your last words before this compaction, sent " + when + ": «" + first + "»" + lsLogicTail
}

// ---------------------------------------------------------------------------
// O1 — lastSaidBefore
// ---------------------------------------------------------------------------

// O1.4 (no boundary): the answer is the last candidate in the file, and
// lastSaid.At is that record's top-level timestamp parsed as RFC3339.
func TestLsLogicLastSaidNoBoundaryTakesLastCandidate(t *testing.T) {
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T03:00:00.000Z", lsLogicText("first reply")),
		lsLogicMarshal(t, map[string]any{"type": "user", "timestamp": "2026-01-02T03:01:00.000Z", "message": map[string]any{"role": "user", "content": "go on"}}),
		lsLogicAssistant(t, "2026-01-02T03:04:05.000Z", lsLogicText("second reply")),
	)
	ls := lsLogicMustSay(t, p)
	if ls.Text != "second reply" {
		t.Errorf("Text = %q, want %q", ls.Text, "second reply")
	}
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !ls.At.Equal(want) {
		t.Errorf("At = %v, want %v", ls.At, want)
	}
}

// O1.2/O1.4 (amended): boundaries are irrelevant — with two boundaries in the
// file, the answer is the last candidate in the file, the one AFTER the last
// boundary (the shape a second compaction's hook sees, since its own boundary
// is not yet written).
func TestLsLogicLastSaidLastCandidateIgnoresBoundaries(t *testing.T) {
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T01:00:00.000Z", lsLogicText("before first boundary")),
		lsLogicBoundary(t, "2026-01-02T01:30:00.000Z"),
		lsLogicAssistant(t, "2026-01-02T02:00:00.000Z", lsLogicText("between, earlier")),
		lsLogicAssistant(t, "2026-01-02T02:30:00.000Z", lsLogicText("between, later")),
		lsLogicBoundary(t, "2026-01-02T03:00:00.000Z"),
		lsLogicAssistant(t, "2026-01-02T03:30:00.000Z", lsLogicText("after the last boundary")),
	)
	ls := lsLogicMustSay(t, p)
	if ls.Text != "after the last boundary" {
		t.Errorf("Text = %q, want %q (the last candidate in the file)", ls.Text, "after the last boundary")
	}
	if want := time.Date(2026, 1, 2, 3, 30, 0, 0, time.UTC); !ls.At.Equal(want) {
		t.Errorf("At = %v, want %v", ls.At, want)
	}
}

// O1.4 (amended): a candidate after the only boundary is the answer.
func TestLsLogicLastSaidCandidateAfterBoundaryIsTheAnswer(t *testing.T) {
	p := lsLogicWriteTranscript(t,
		lsLogicBoundary(t, "2026-01-02T03:00:00.000Z"),
		lsLogicAssistant(t, "2026-01-02T03:10:00.000Z", lsLogicText("only after")),
	)
	if ls := lsLogicMustSay(t, p); ls.Text != "only after" {
		t.Errorf("Text = %q, want %q", ls.Text, "only after")
	}
}

// O1.4 (amended): a file that ENDS with a boundary (the ordering where this
// compaction's boundary did land before the hook) still answers the last
// candidate, the one before it.
func TestLsLogicLastSaidFileEndingInBoundary(t *testing.T) {
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T02:00:00.000Z", lsLogicText("older")),
		lsLogicBoundary(t, "2026-01-02T02:10:00.000Z"),
		lsLogicAssistant(t, "2026-01-02T03:00:00.000Z", lsLogicText("final words")),
		lsLogicBoundary(t, "2026-01-02T03:50:00.000Z"),
	)
	if ls := lsLogicMustSay(t, p); ls.Text != "final words" {
		t.Errorf("Text = %q, want %q", ls.Text, "final words")
	}
}

// O1.3 (amended): a "<synthetic>"-model assistant record (Claude Code's own
// error/limit banner) is not a candidate, so the real text before it is quoted.
func TestLsLogicLastSaidSkipsSyntheticModel(t *testing.T) {
	synth := lsLogicMarshal(t, map[string]any{
		"type": "assistant", "timestamp": "2026-01-02T03:40:00.000Z",
		"message": map[string]any{"model": "<synthetic>", "content": []lsLogicContent{lsLogicText("Invented limit banner · resets later")}},
	})
	realRec := lsLogicMarshal(t, map[string]any{
		"type": "assistant", "timestamp": "2026-01-02T03:30:00.000Z",
		"message": map[string]any{"model": "claude-invented-1", "content": []lsLogicContent{lsLogicText("the real reply")}},
	})
	p := lsLogicWriteTranscript(t, realRec, synth)
	if ls := lsLogicMustSay(t, p); ls.Text != "the real reply" {
		t.Errorf("Text = %q, want %q (a synthetic record is never a candidate)", ls.Text, "the real reply")
	}
}

// O1.1 (amended): a line over the 16 MiB scanner cap makes the WHOLE answer
// false — neither the candidate before it nor the one after is returned.
func TestLsLogicLastSaidOverCapLineIsFalse(t *testing.T) {
	huge := `{"type":"attachment","blob":"` + strings.Repeat("x", 17<<20) + `"}`
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T03:00:00.000Z", lsLogicText("before the huge line")),
		huge,
		lsLogicAssistant(t, "2026-01-02T03:10:00.000Z", lsLogicText("after the huge line")),
	)
	if ls, ok := lastSaidBefore(p); ok {
		t.Errorf("lastSaidBefore = (%q, true), want false for a transcript holding an over-cap line", ls.Text)
	}
}

// O1.6 (amended): system records — compact_boundary or any other subtype —
// and non-system records carrying subtype compact_boundary are never
// candidates and change nothing, so the last candidate wins.
func TestLsLogicLastSaidOnlySystemCompactBoundaryIsABoundary(t *testing.T) {
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T01:00:00.000Z", lsLogicText("early")),
		lsLogicMarshal(t, map[string]any{"type": "system", "subtype": "informational", "timestamp": "2026-01-02T01:30:00.000Z"}),
		lsLogicMarshal(t, map[string]any{"type": "attachment", "subtype": "compact_boundary", "timestamp": "2026-01-02T01:40:00.000Z"}),
		lsLogicAssistant(t, "2026-01-02T02:00:00.000Z", lsLogicText("late")),
	)
	if ls := lsLogicMustSay(t, p); ls.Text != "late" {
		t.Errorf("Text = %q, want %q (no real boundary in the file)", ls.Text, "late")
	}
}

// O1.3: a record's text is its LAST text element with non-empty trimmed text
// — a later whitespace-only text element and a trailing tool_use do not
// count.
func TestLsLogicLastSaidTakesLastNonEmptyTextElement(t *testing.T) {
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T03:04:05.000Z",
			lsLogicText("opening thought"),
			lsLogicToolUse(),
			lsLogicText("closing line"),
			lsLogicText("   \n\t "),
			lsLogicToolUse(),
		),
	)
	if ls := lsLogicMustSay(t, p); ls.Text != "closing line" {
		t.Errorf("Text = %q, want %q", ls.Text, "closing line")
	}
}

// O1.3: an assistant record with no text element (tool_use only) is not a
// candidate, so the earlier record with text is the answer.
func TestLsLogicLastSaidToolOnlyRecordIsNotACandidate(t *testing.T) {
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T03:00:00.000Z", lsLogicText("said this")),
		lsLogicAssistant(t, "2026-01-02T03:05:00.000Z", lsLogicToolUse()),
		lsLogicAssistant(t, "2026-01-02T03:06:00.000Z", lsLogicText("  ")),
	)
	if ls := lsLogicMustSay(t, p); ls.Text != "said this" {
		t.Errorf("Text = %q, want %q", ls.Text, "said this")
	}
}

// O1.3: isSidechain true is not a candidate; isSidechain false is.
func TestLsLogicLastSaidSkipsSidechain(t *testing.T) {
	side := map[string]any{
		"type": "assistant", "isSidechain": true, "timestamp": "2026-01-02T03:30:00.000Z",
		"message": map[string]any{"content": []lsLogicContent{lsLogicText("subagent chatter")}},
	}
	mainRec := map[string]any{
		"type": "assistant", "isSidechain": false, "timestamp": "2026-01-02T03:20:00.000Z",
		"message": map[string]any{"content": []lsLogicContent{lsLogicText("main chain line")}},
	}
	p := lsLogicWriteTranscript(t, lsLogicMarshal(t, mainRec), lsLogicMarshal(t, side))
	if ls := lsLogicMustSay(t, p); ls.Text != "main chain line" {
		t.Errorf("Text = %q, want %q (a sidechain record is never a candidate)", ls.Text, "main chain line")
	}
}

// O1.6: user and other record types are never candidates, even when they
// carry an assistant-shaped content array with text.
func TestLsLogicLastSaidOtherTypesAreNeverCandidates(t *testing.T) {
	shaped := func(typ string) string {
		return lsLogicMarshal(t, map[string]any{
			"type": typ, "timestamp": "2026-01-02T03:50:00.000Z",
			"message": map[string]any{"content": []lsLogicContent{lsLogicText(typ + " text")}},
		})
	}
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T03:00:00.000Z", lsLogicText("the assistant")),
		shaped("user"),
		shaped("queue-operation"),
		shaped("attachment"),
		shaped("file-history-snapshot"),
		shaped("progress"),
	)
	if ls := lsLogicMustSay(t, p); ls.Text != "the assistant" {
		t.Errorf("Text = %q, want %q", ls.Text, "the assistant")
	}
}

// lastSaid.Text: whitespace-trimmed, otherwise unchanged — inner newlines
// kept, and a long text NOT truncated (truncation is O3's job).
func TestLsLogicLastSaidTextTrimmedNotTruncated(t *testing.T) {
	long := strings.Repeat("word ", 300) + "end"
	body := "head line\n\n" + long
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T03:04:05.000Z", lsLogicText("\n  "+body+"  \n\n")),
	)
	if ls := lsLogicMustSay(t, p); ls.Text != body {
		t.Errorf("Text = %q, want %q (trimmed, inner text unchanged, not truncated)", ls.Text, body)
	}
}

// O1.6 + lastSaid.At: the timestamp comes from the TOP-LEVEL key, not from a
// timestamp nested inside message; an offset timestamp names the same instant
// in UTC.
func TestLsLogicLastSaidTimestampIsTopLevel(t *testing.T) {
	rec := map[string]any{
		"type":      "assistant",
		"timestamp": "2026-01-02T05:04:05+02:00",
		"message": map[string]any{
			"timestamp": "1999-09-09T09:09:09Z",
			"content":   []lsLogicContent{lsLogicText("stamped")},
		},
	}
	p := lsLogicWriteTranscript(t, lsLogicMarshal(t, rec))
	ls := lsLogicMustSay(t, p)
	if want := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC); !ls.At.Equal(want) {
		t.Errorf("At = %v, want %v (the top-level timestamp)", ls.At, want)
	}
	// Contract: "parsed as RFC3339 (UTC)". Pinned as the instant in UTC; the
	// Location itself is checked only via UTC() equality, since the contract
	// does not say whether At must already carry time.UTC.
	if got := ls.At.UTC(); got.Hour() != 3 || got.Minute() != 4 {
		t.Errorf("At.UTC() = %v, want 03:04 UTC", got)
	}
}

// lastSaid.At is zero when the record has no timestamp; the answer is still
// returned.
func TestLsLogicLastSaidMissingTimestampIsZero(t *testing.T) {
	p := lsLogicWriteTranscript(t, lsLogicAssistant(t, "", lsLogicText("unstamped")))
	ls := lsLogicMustSay(t, p)
	if ls.Text != "unstamped" {
		t.Errorf("Text = %q, want unstamped", ls.Text)
	}
	if !ls.At.IsZero() {
		t.Errorf("At = %v, want zero for a missing timestamp", ls.At)
	}
}

// ---------------------------------------------------------------------------
// O3 — handoffLastSaidLine
// ---------------------------------------------------------------------------

// The contract's first example, verbatim.
func TestLsLogicLineContractExample(t *testing.T) {
	ls := lastSaid{
		Text: "manual test ready: http://127.0.0.1:1/x (try the thing)\n\nsecond paragraph",
		At:   time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC),
	}
	now := time.Date(2026, 1, 2, 3, 59, 0, 0, time.UTC)
	want := "Your last words before this compaction, sent 55m ago (2026-01-02 03:04 UTC): «manual test ready: http://127.0.0.1:1/x (try the thing)» — that message was delivered; a summary listing it as still to be sent is wrong, so do not send it again unless the work since has changed it."
	if got := handoffLastSaidLine(ls, now); got != want {
		t.Errorf("handoffLastSaidLine =\n%q\nwant\n%q", got, want)
	}
}

// The contract's second example: a zero At reads `at an unknown time`.
func TestLsLogicLineZeroAtIsUnknownTime(t *testing.T) {
	got := handoffLastSaidLine(lastSaid{Text: "doc ready"}, lsLogicNow)
	if want := lsLogicWantLine("at an unknown time", "doc ready"); got != want {
		t.Errorf("handoffLastSaidLine =\n%q\nwant\n%q", got, want)
	}
}

// Exactly one line: no newline anywhere, trailing or inner, even when the
// text has many lines.
func TestLsLogicLineIsOneLine(t *testing.T) {
	got := handoffLastSaidLine(lastSaid{Text: "a\nb\nc", At: lsLogicNow.Add(-time.Minute)}, lsLogicNow)
	if strings.Contains(got, "\n") {
		t.Errorf("handoffLastSaidLine = %q, want no newline", got)
	}
}

// <FIRST> is the first line only, trimmed of surrounding whitespace.
func TestLsLogicLineFirstLineTrimmed(t *testing.T) {
	got := handoffLastSaidLine(lastSaid{Text: "  gate passed \t\nmore detail here"}, lsLogicNow)
	if want := lsLogicWantLine("at an unknown time", "gate passed"); got != want {
		t.Errorf("handoffLastSaidLine =\n%q\nwant\n%q", got, want)
	}
}

// Minutes are whole, floored: 55m59s ago reads 55m.
func TestLsLogicLineMinutesFloor(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 3, 1, 0, time.UTC)
	got := handoffLastSaidLine(lastSaid{Text: "x", At: at}, lsLogicNow)
	if want := lsLogicWantLine("55m ago (2026-01-02 03:03 UTC)", "x"); got != want {
		t.Errorf("handoffLastSaidLine =\n%q\nwant\n%q", got, want)
	}
}

// Hours of age stay in minutes: 2h5m reads 125m.
func TestLsLogicLineLongAgeInMinutes(t *testing.T) {
	at := lsLogicNow.Add(-(2*time.Hour + 5*time.Minute))
	got := handoffLastSaidLine(lastSaid{Text: "x", At: at}, lsLogicNow)
	if want := lsLogicWantLine("125m ago (2026-01-02 01:54 UTC)", "x"); got != want {
		t.Errorf("handoffLastSaidLine =\n%q\nwant\n%q", got, want)
	}
}

// A future At is floored at 0: `0m ago`.
func TestLsLogicLineFutureAtIsZeroMinutes(t *testing.T) {
	at := lsLogicNow.Add(7 * time.Minute)
	got := handoffLastSaidLine(lastSaid{Text: "x", At: at}, lsLogicNow)
	if want := lsLogicWantLine("0m ago (2026-01-02 04:06 UTC)", "x"); got != want {
		t.Errorf("handoffLastSaidLine =\n%q\nwant\n%q", got, want)
	}
}

// <TS> is rendered in UTC whatever location At carries.
func TestLsLogicLineTimestampRenderedInUTC(t *testing.T) {
	zone := time.FixedZone("plus5", 5*3600)
	at := time.Date(2026, 1, 2, 8, 30, 0, 0, zone) // 03:30 UTC
	got := handoffLastSaidLine(lastSaid{Text: "x", At: at}, lsLogicNow)
	if want := lsLogicWantLine("29m ago (2026-01-02 03:30 UTC)", "x"); got != want {
		t.Errorf("handoffLastSaidLine =\n%q\nwant\n%q", got, want)
	}
}

// A first line of exactly 240 runes is kept whole, no ellipsis.
func TestLsLogicLine240RunesKept(t *testing.T) {
	first := strings.Repeat("é", 240)
	got := handoffLastSaidLine(lastSaid{Text: first + "\nrest"}, lsLogicNow)
	if want := lsLogicWantLine("at an unknown time", first); got != want {
		t.Errorf("handoffLastSaidLine kept %d bytes, want the 240-rune line whole", len(got))
	}
}

// A first line of 241 runes is cut to 240 runes plus one `…`, counted in
// RUNES (multi-byte runes would cut far shorter if bytes were counted).
func TestLsLogicLine241RunesCut(t *testing.T) {
	first := strings.Repeat("日", 241)
	got := handoffLastSaidLine(lastSaid{Text: first}, lsLogicNow)
	if want := lsLogicWantLine("at an unknown time", strings.Repeat("日", 240)+"…"); got != want {
		t.Errorf("handoffLastSaidLine =\n%q\nwant the first 240 runes + …", got)
	}
}

// ---------------------------------------------------------------------------
// O2 — runHandoffHook
// ---------------------------------------------------------------------------

// lsLogicAnswerTranscript writes a transcript whose answer is known.
func lsLogicAnswerTranscript(t *testing.T) (string, lastSaid) {
	t.Helper()
	p := lsLogicWriteTranscript(t,
		lsLogicAssistant(t, "2026-01-02T02:20:00.000Z", lsLogicText("an earlier reply")),
		lsLogicBoundary(t, "2026-01-02T02:30:00.000Z"),
		lsLogicAssistant(t, "2026-01-02T03:04:00.000Z", lsLogicText("doc ready: 00-overview.md <see it>\nsecond line")),
	)
	return p, lastSaid{Text: "doc ready: 00-overview.md <see it>\nsecond line", At: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)}
}

// O2.1, no docs: context is exactly rules + "\n\n" + the last-said line.
func TestLsLogicHookNoDocsRulesThenLine(t *testing.T) {
	p, ls := lsLogicAnswerTranscript(t)
	text := lsLogicDecode(t, lsLogicRun(lsLogicPayload(t, "compact", &p), t.TempDir()))
	want := handoffStandingRules + "\n\n" + handoffLastSaidLine(ls, lsLogicNow)
	if text != want {
		t.Errorf("additionalContext =\n%q\nwant\n%q", text, want)
	}
	// And the line itself is the one O3 freezes for this transcript.
	if wantLine := lsLogicWantLine("55m ago (2026-01-02 03:04 UTC)", "doc ready: 00-overview.md <see it>"); !strings.HasSuffix(text, wantLine) {
		t.Errorf("additionalContext %q does not end with %q", text, wantLine)
	}
}

// O2.1, with docs: rules + "\n\n" + line + the baseline docs section exactly.
// The baseline section is obtained by running the hook WITHOUT a transcript
// (O2.2: that output is the baseline) and stripping the rules prefix.
func TestLsLogicHookWithDocsRulesLineThenDocs(t *testing.T) {
	dir := t.TempDir()
	newer := lsLogicWriteDoc(t, dir, "80-handoff-hephaestus.md", 4*time.Minute)
	older := lsLogicWriteDoc(t, dir, "80-handoff.md", 40*time.Minute)
	p, ls := lsLogicAnswerTranscript(t)

	baseline := lsLogicDecode(t, lsLogicRun(lsLogicPayload(t, "compact", nil), dir))
	if !strings.HasPrefix(baseline, handoffStandingRules) {
		t.Fatalf("baseline %q does not open with the standing rules", baseline)
	}
	docsSection := strings.TrimPrefix(baseline, handoffStandingRules)
	const header = "\n\nHandoff docs in this cell's artifacts dir, newest first:\n"
	if !strings.HasPrefix(docsSection, header) || !strings.HasSuffix(docsSection, handoffClosing) {
		t.Fatalf("baseline docs section %q is not header + lines + handoffClosing", docsSection)
	}

	text := lsLogicDecode(t, lsLogicRun(lsLogicPayload(t, "compact", &p), dir))
	line := handoffLastSaidLine(ls, lsLogicNow)
	if want := handoffStandingRules + "\n\n" + line + docsSection; text != want {
		t.Errorf("additionalContext =\n%q\nwant\n%q", text, want)
	}

	// Order by position: rules, then the line, then the docs header, then the
	// docs newest first.
	iRules := strings.Index(text, handoffStandingRules)
	iLine := strings.Index(text, line)
	iHeader := strings.Index(text, header)
	iNew := strings.Index(text, newer)
	iOld := strings.Index(text, older)
	if !(iRules == 0 && iRules < iLine && iLine < iHeader && iHeader < iNew && iNew < iOld) {
		t.Errorf("positions rules=%d line=%d header=%d newer=%d older=%d, want strictly increasing from 0", iRules, iLine, iHeader, iNew, iOld)
	}
}

// O2.1: an absent source still reads as compact, so the line is injected.
func TestLsLogicHookAbsentSourceInjectsLine(t *testing.T) {
	p, ls := lsLogicAnswerTranscript(t)
	text := lsLogicDecode(t, lsLogicRun(lsLogicPayload(t, "", &p), t.TempDir()))
	if want := handoffStandingRules + "\n\n" + handoffLastSaidLine(ls, lsLogicNow); text != want {
		t.Errorf("additionalContext =\n%q\nwant\n%q", text, want)
	}
}

// O2.2: a transcript_path whose transcript yields no answer, and an empty
// transcript_path, both give output byte-identical to the payload without the
// key — with docs and without. Assumption: the baseline ignores the
// transcript_path key, so "the baseline's output for the same payload" is the
// output for the payload minus that key.
func TestLsLogicHookNoAnswerIsByteIdenticalToBaseline(t *testing.T) {
	// No candidate anywhere: a boundary, a user record, a tool-only
	// assistant record.
	noAnswer := lsLogicWriteTranscript(t,
		lsLogicBoundary(t, "2026-01-02T03:00:00.000Z"),
		lsLogicMarshal(t, map[string]any{"type": "user", "timestamp": "2026-01-02T03:05:00.000Z", "message": map[string]any{"role": "user", "content": "go"}}),
		lsLogicAssistant(t, "2026-01-02T03:10:00.000Z", lsLogicToolUse()),
	)
	empty := ""
	withDocs := t.TempDir()
	lsLogicWriteDoc(t, withDocs, "80-handoff.md", 9*time.Minute)

	for name, dir := range map[string]string{"no docs": t.TempDir(), "docs": withDocs} {
		base := lsLogicRun(lsLogicPayload(t, "compact", nil), dir)
		if base == "" {
			t.Fatalf("%s: baseline emitted nothing", name)
		}
		for label, tp := range map[string]*string{"no-answer transcript": &noAnswer, "empty path": &empty} {
			if got := lsLogicRun(lsLogicPayload(t, "compact", tp), dir); got != base {
				t.Errorf("%s, %s: output\n%q\nwant byte-identical baseline\n%q", name, label, got, base)
			}
		}
	}
}

// O2.3: the source gate runs first — a non-compact source emits nothing even
// with a transcript that has an answer.
func TestLsLogicHookSourceGateRunsFirst(t *testing.T) {
	p, _ := lsLogicAnswerTranscript(t)
	if got := lsLogicRun(lsLogicPayload(t, "startup", &p), t.TempDir()); got != "" {
		t.Errorf("source startup emitted %q, want nothing", got)
	}
}

// O2.4: the raw bytes carry «, », — and < unescaped (SetEscapeHTML(false)),
// and the output is one JSON object plus one newline.
func TestLsLogicHookOutputUnescaped(t *testing.T) {
	p, _ := lsLogicAnswerTranscript(t)
	raw := lsLogicRun(lsLogicPayload(t, "compact", &p), t.TempDir())
	lsLogicDecode(t, raw)
	for _, want := range []string{"«doc ready: 00-overview.md <see it>»", "» — that message was delivered"} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw stdout %q does not carry %q unescaped", raw, want)
		}
	}
	// Needles are the six-character escape sequences, built from a lone
	// backslash so no source tooling can decode them into the characters.
	bs := "\\"
	for _, esc := range []string{bs + "u003c", bs + "u00ab", bs + "u00bb", bs + "u2014"} {
		if strings.Contains(raw, esc) {
			t.Errorf("raw stdout %q carries the escape %s", raw, esc)
		}
	}
}
