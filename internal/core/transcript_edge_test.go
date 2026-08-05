package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// transcript_edge_test.go — the EDGE dimension of 50-contract.md's
// core.LastAssistantText: every degrade-to-"" path, every malformed shape,
// and the 256 KiB tail bound. Happy-path joining and last-record-wins belong
// to the sibling blind author.
//
// The function's whole posture is that it NEVER errors and NEVER panics: it
// runs on a hook that is about to be handed to a live conversation, so "I
// could not read that" and "there was nothing to read" must be the same
// answer. Every case below therefore asserts a STRING, never an error.
//
// Package-level identifiers are prefixed `edgeTr` — this package's test
// namespace already carries an `edge*` family from graphsearch/slack/linear
// edge tests, and a second blind author is writing beside this file.

// edgeTrTailBound is this file's OWN copy of the contract's 256 KiB bound.
// Written out rather than read from the implementation on purpose: a fixture
// sized from the constant under test would follow it wherever it drifted, and
// the number is the obligation.
const edgeTrTailBound = 256 * 1024

// edgeTrFile accumulates newline-terminated JSONL lines and knows how many
// bytes it has, which is what lets the tail-bound fixtures place a record on
// a chosen side of the boundary.
type edgeTrFile struct{ b strings.Builder }

func (f *edgeTrFile) add(line string) {
	f.b.WriteString(line)
	f.b.WriteString("\n")
}

func (f *edgeTrFile) size() int { return f.b.Len() }

// padTo appends non-qualifying user records until the file is at least n
// bytes. Lines are ~3.9 KB so no padding line ever approaches bufio's default
// buffer — the long-line case is tested deliberately, never by accident.
func (f *edgeTrFile) padTo(t *testing.T, n int) {
	t.Helper()
	pad := edgeTrUser(t, strings.Repeat("p", 3900))
	for f.size() < n {
		f.add(pad)
	}
}

func (f *edgeTrFile) write(t *testing.T) string {
	t.Helper()
	return edgeTrWrite(t, f.b.String())
}

func edgeTrWrite(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func edgeTrMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(b)
}

// edgeTrAsst builds one assistant record from raw content blocks.
func edgeTrAsst(t *testing.T, blocks ...map[string]any) string {
	t.Helper()
	return edgeTrMarshal(t, map[string]any{
		"type":    "assistant",
		"message": map[string]any{"content": blocks},
	})
}

// edgeTrText builds an assistant record carrying exactly these text blocks.
func edgeTrText(t *testing.T, texts ...string) string {
	t.Helper()
	blocks := make([]map[string]any, 0, len(texts))
	for _, s := range texts {
		blocks = append(blocks, edgeTrTextBlock(s))
	}
	return edgeTrAsst(t, blocks...)
}

func edgeTrTextBlock(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func edgeTrThinkingBlock(s string) map[string]any {
	return map[string]any{"type": "thinking", "thinking": s}
}

func edgeTrToolUseBlock(name string) map[string]any {
	return map[string]any{"type": "tool_use", "id": "toolu_edge", "name": name,
		"input": map[string]any{"path": "/tmp/x"}}
}

// edgeTrUser builds a user record — the padding shape, and the shape that
// must never be mistaken for an assistant's answer.
func edgeTrUser(t *testing.T, text string) string {
	t.Helper()
	return edgeTrMarshal(t, map[string]any{
		"type":    "user",
		"message": map[string]any{"content": text},
	})
}

// --- the constant ---------------------------------------------------------

// TestEdgeTranscriptTailBoundConstant pins the frozen symbol and its value.
// The bound is the whole reason a multi-hundred-MB transcript is safe to hand
// this function on a hook path, and a number that quietly grew would show up
// only as a slow conversation.
func TestEdgeTranscriptTailBoundConstant(t *testing.T) {
	if lastAssistantTailBytes != edgeTrTailBound {
		t.Errorf("lastAssistantTailBytes = %d, want %d (256 KiB)", lastAssistantTailBytes, edgeTrTailBound)
	}
}

// --- unreadable and empty inputs ------------------------------------------

// TestEdgeTranscriptUnreadablePaths covers every way the file can fail to be
// a readable transcript. All of them are "" — the hook must inject nothing
// rather than break the turn, and Claude Code owns this path, so it can
// vanish between the payload being written and the read happening.
func TestEdgeTranscriptUnreadablePaths(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"empty path":          "",
		"missing file":        filepath.Join(dir, "nope.jsonl"),
		"missing parent dir":  filepath.Join(dir, "no", "such", "dir", "t.jsonl"),
		"path is a directory": dir,
	}
	for name, path := range cases {
		if got := LastAssistantText(path); got != "" {
			t.Errorf("LastAssistantText(%s) = %q, want \"\"", name, got)
		}
	}
}

// TestEdgeTranscriptDegenerateBodies covers files that exist but hold nothing
// a reader can use.
func TestEdgeTranscriptDegenerateBodies(t *testing.T) {
	cases := map[string]string{
		"empty file":            "",
		"newlines only":         "\n\n\n",
		"whitespace only":       "   \n\t\n  \n",
		"nul bytes":             "\x00\x00\x00\n",
		"one unterminated line": `{"type":"assistant","message":{"content":[{"type":"text","text":"x"`,
	}
	for name, body := range cases {
		if got := LastAssistantText(edgeTrWrite(t, body)); got != "" {
			t.Errorf("LastAssistantText(%s) = %q, want \"\"", name, got)
		}
	}
}

// --- malformed and non-qualifying records ---------------------------------

// TestEdgeTranscriptMalformedLinesAreSkippedNotFatal is the defensive stance
// stated as a test: the JSONL format is Claude-owned and can carry a torn or
// unknown line at any point. One bad line must not cost the good record that
// follows it.
func TestEdgeTranscriptMalformedLinesAreSkippedNotFatal(t *testing.T) {
	var f edgeTrFile
	f.add(`{not json at all`)
	f.add(`[]`)
	f.add(`"a bare string line"`)
	f.add(`{"type":"assistant","message":{"content":[{"type":"text","text":`)
	f.add(edgeTrText(t, "SURVIVOR"))
	f.add(`}}}} trailing garbage`)
	if got := LastAssistantText(f.write(t)); got != "SURVIVOR" {
		t.Errorf("LastAssistantText = %q, want %q — a malformed neighbour swallowed a good record", got, "SURVIVOR")
	}
}

// TestEdgeTranscriptAllMalformedYieldsEmpty is the same rule at its limit: a
// file of nothing but garbage is "no qualifying record", not a crash.
func TestEdgeTranscriptAllMalformedYieldsEmpty(t *testing.T) {
	var f edgeTrFile
	for i := 0; i < 20; i++ {
		f.add(`{"type":"assistant",,,`)
	}
	if got := LastAssistantText(f.write(t)); got != "" {
		t.Errorf("LastAssistantText over a garbage file = %q, want \"\"", got)
	}
}

// TestEdgeTranscriptThinkingAndToolUseDoNotQualify pins the contract's
// sharpest exclusion. An assistant turn that only thought and called a tool
// said nothing to the human, so using it as the recall query would retrieve
// against the model's private reasoning — and, worse, would silently shadow
// the last thing actually said.
func TestEdgeTranscriptThinkingAndToolUseDoNotQualify(t *testing.T) {
	var f edgeTrFile
	f.add(edgeTrText(t, "THE LAST REAL ANSWER"))
	f.add(edgeTrAsst(t, edgeTrThinkingBlock("private reasoning that must not be the query")))
	f.add(edgeTrAsst(t, edgeTrToolUseBlock("Read")))
	f.add(edgeTrAsst(t, edgeTrThinkingBlock("more reasoning"), edgeTrToolUseBlock("Bash")))
	got := LastAssistantText(f.write(t))
	if got != "THE LAST REAL ANSWER" {
		t.Errorf("LastAssistantText = %q, want %q — a thinking/tool_use-only record was counted", got, "THE LAST REAL ANSWER")
	}
	if strings.Contains(got, "reasoning") {
		t.Errorf("thinking text leaked into the result: %q", got)
	}

	// And on its own, such a record leaves nothing at all.
	var g edgeTrFile
	g.add(edgeTrAsst(t, edgeTrThinkingBlock("only thinking"), edgeTrToolUseBlock("Grep")))
	if got := LastAssistantText(g.write(t)); got != "" {
		t.Errorf("a file of thinking/tool_use-only assistant records = %q, want \"\"", got)
	}
}

// TestEdgeTranscriptEmptyTextBlocksDoNotQualify pins "at least one NON-EMPTY
// text content block". A record whose text blocks are all empty strings is
// the streaming shape of a turn that has not said anything yet; counting it
// would make the query "" and inject against nothing.
func TestEdgeTranscriptEmptyTextBlocksDoNotQualify(t *testing.T) {
	var f edgeTrFile
	f.add(edgeTrText(t, "REAL ANSWER"))
	f.add(edgeTrText(t, ""))
	f.add(edgeTrText(t, "", ""))
	f.add(edgeTrAsst(t)) // no blocks at all
	if got := LastAssistantText(f.write(t)); got != "REAL ANSWER" {
		t.Errorf("LastAssistantText = %q, want %q — an all-empty-text record was counted as qualifying", got, "REAL ANSWER")
	}
}

// TestEdgeTranscriptPlainStringContentCounts is the documented defensive
// carve-out: message.content is sometimes a bare JSON string rather than a
// block array. The format is Claude-owned, so a shape change here must
// degrade to reading the text, not to reading nothing.
func TestEdgeTranscriptPlainStringContentCounts(t *testing.T) {
	var f edgeTrFile
	f.add(`{"type":"assistant","message":{"content":"a plain string answer"}}`)
	if got := LastAssistantText(f.write(t)); got != "a plain string answer" {
		t.Errorf("LastAssistantText with string content = %q, want the string", got)
	}
	// An EMPTY plain string is still nothing said.
	var g edgeTrFile
	g.add(edgeTrText(t, "EARLIER"))
	g.add(`{"type":"assistant","message":{"content":""}}`)
	if got := LastAssistantText(g.write(t)); got != "EARLIER" {
		t.Errorf("an empty plain-string content was counted: got %q, want %q", got, "EARLIER")
	}
}

// TestEdgeTranscriptStructurallyOddRecords covers the shapes a schema drift
// would most plausibly produce. None of them may panic, and none may be
// mistaken for an answer.
func TestEdgeTranscriptStructurallyOddRecords(t *testing.T) {
	for name, line := range map[string]string{
		"no message key":       `{"type":"assistant"}`,
		"message is a string":  `{"type":"assistant","message":"oops"}`,
		"content is a number":  `{"type":"assistant","message":{"content":42}}`,
		"content is an object": `{"type":"assistant","message":{"content":{"type":"text","text":"nested"}}}`,
		"content is null":      `{"type":"assistant","message":{"content":null}}`,
		"blocks are strings":   `{"type":"assistant","message":{"content":["just a string"]}}`,
		"block missing type":   `{"type":"assistant","message":{"content":[{"text":"untyped"}]}}`,
		"no type key":          `{"message":{"content":[{"type":"text","text":"typeless record"}]}}`,
	} {
		var f edgeTrFile
		f.add(line)
		got := LastAssistantText(f.write(t))
		if got != "" {
			t.Errorf("LastAssistantText(%s) = %q, want \"\" — an odd shape was read as an answer", name, got)
		}
	}
}

// TestEdgeTranscriptIgnoresNonAssistantRecords pins the type filter. A user
// record is the human's own message; recalling against it would be retrieving
// against the prompt twice on a Stop hook, and a system/summary record is not
// speech at all.
func TestEdgeTranscriptIgnoresNonAssistantRecords(t *testing.T) {
	var f edgeTrFile
	f.add(edgeTrText(t, "ASSISTANT ANSWER"))
	f.add(edgeTrUser(t, "the human said something after"))
	f.add(`{"type":"system","content":"a system notice"}`)
	f.add(`{"type":"summary","summary":"a summary line"}`)
	f.add(`{"type":"Assistant","message":{"content":[{"type":"text","text":"WRONG CASE"}]}}`)
	got := LastAssistantText(f.write(t))
	if got != "ASSISTANT ANSWER" {
		t.Errorf("LastAssistantText = %q, want %q", got, "ASSISTANT ANSWER")
	}
}

// TestEdgeTranscriptFinalLineWithoutNewline covers the file as it actually
// exists mid-turn: Claude Code appends, and a read can land between the last
// record's bytes and its terminator. That record is complete JSON and must be
// read.
func TestEdgeTranscriptFinalLineWithoutNewline(t *testing.T) {
	body := edgeTrUser(t, "hello") + "\n" + edgeTrText(t, "UNTERMINATED FINAL")
	if got := LastAssistantText(edgeTrWrite(t, body)); got != "UNTERMINATED FINAL" {
		t.Errorf("LastAssistantText = %q, want %q — a final line with no newline was dropped", got, "UNTERMINATED FINAL")
	}
}

// --- the 256 KiB tail bound -----------------------------------------------

// TestEdgeTranscriptRecordBuriedBeforeTheTailIsNotFound is the bound's
// load-bearing half, and the one an unbounded implementation passes every
// other test without. A qualifying record sitting at the head of a large file
// must NOT be found: the function is specified to examine the final 256 KiB,
// and reading a multi-hundred-MB transcript on a hook path is the failure
// this bound exists to prevent.
func TestEdgeTranscriptRecordBuriedBeforeTheTailIsNotFound(t *testing.T) {
	var f edgeTrFile
	f.add(edgeTrText(t, "BURIED-MARKER"))
	f.padTo(t, edgeTrTailBound+128*1024) // ~128 KiB of padding beyond the window
	path := f.write(t)
	if got := LastAssistantText(path); got != "" {
		t.Errorf("LastAssistantText over a %d-byte file = %q, want \"\" — the read reached past the %d-byte tail",
			f.size(), got, edgeTrTailBound)
	}
}

// TestEdgeTranscriptRecordInsideTheTailIsFound is the other half: the bound
// must not be so eager that it misses the record it was pointed at. Same file
// size, record at the end instead of the start.
func TestEdgeTranscriptRecordInsideTheTailIsFound(t *testing.T) {
	var f edgeTrFile
	f.padTo(t, edgeTrTailBound+128*1024)
	f.add(edgeTrText(t, "INSIDE-MARKER"))
	if got := LastAssistantText(f.write(t)); got != "INSIDE-MARKER" {
		t.Errorf("LastAssistantText = %q, want %q — a record inside the tail window was missed", got, "INSIDE-MARKER")
	}
}

// TestEdgeTranscriptLongLinesDoNotBreakTheScan pins the scanner-buffer rule.
// bufio.Scanner's default token limit is 64 KiB and a real assistant record
// blows past it routinely (the crew's own briefs are tens of KB); a scanner
// left at the default stops at the first such line and returns false, so
// every record AFTER it silently disappears. Asserting on the record that
// follows the long one is what catches that — asserting on the long record
// itself would not.
func TestEdgeTranscriptLongLinesDoNotBreakTheScan(t *testing.T) {
	var f edgeTrFile
	f.padTo(t, 300*1024)
	f.add(edgeTrText(t, "LONGLINE-MARKER "+strings.Repeat("L", 200*1024)))
	f.add(edgeTrUser(t, "a short record between the long one and the answer"))
	f.add(edgeTrText(t, "AFTERLONG"))
	if got := LastAssistantText(f.write(t)); got != "AFTERLONG" {
		t.Errorf("LastAssistantText = %q, want %q — a >64 KiB line stopped the scan (buffer left at bufio's default?)",
			edgeTrShort(got), "AFTERLONG")
	}
}

// TestEdgeTranscriptStraddlingRecordIsNeverReturned covers the seek boundary
// landing INSIDE a record's line. The bytes visible after the seek are a
// suffix of one JSON object, which is not a record; it must be discarded, the
// scan must continue past it, and its text must never surface as an answer —
// a half-record read as a whole one would hand the recall query a fragment of
// something the assistant said 300 KB ago.
func TestEdgeTranscriptStraddlingRecordIsNeverReturned(t *testing.T) {
	var f edgeTrFile
	f.padTo(t, 50*1024)
	f.add(edgeTrText(t, "STRADDLE-MARKER "+strings.Repeat("S", 100*1024)))
	f.padTo(t, f.size()+200*1024) // pushes the tail boundary into the record above
	f.add(edgeTrText(t, "TAILTEXT"))
	got := LastAssistantText(f.write(t))
	if got != "TAILTEXT" {
		t.Errorf("LastAssistantText = %q, want %q — the scan did not recover past the straddling record",
			edgeTrShort(got), "TAILTEXT")
	}
	if strings.Contains(got, "STRADDLE-MARKER") || strings.Contains(got, "SSSS") {
		t.Errorf("a straddling record's text was returned: %q", edgeTrShort(got))
	}
}

// edgeTrShort keeps a failure message readable when the value under test is
// a few hundred kilobytes of padding.
func edgeTrShort(s string) string {
	if len(s) <= 120 {
		return s
	}
	return s[:120] + "…(" + strconv.Itoa(len(s)) + " bytes)"
}
