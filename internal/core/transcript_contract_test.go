package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// transcript_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice A's reader half (core.LastAssistantText). Written from
// the contract text alone, with no sight of internal/core/transcript.go.
//
// Dimension: LOGIC — which record wins, what a record's text IS, and the
// "never an error, never a panic" posture that the whole feature's
// degrade-to-nothing promise rests on. Byte boundaries (the 256 KiB tail seek,
// the oversized line) are athena-edge's ground; only the constant itself is
// pinned here, because a caller of this function cannot see the seek but the
// contract names the number.
//
// Every package-level identifier is prefixed `contractTranscript` — two
// sibling authors share this package namespace and `contract…` alone is
// already taken here.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractTranscriptFile writes lines as a JSONL file in a temp dir and
// returns its path. Lines are written verbatim so a test can include a
// deliberately malformed one.
func contractTranscriptFile(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := ""
	if len(lines) > 0 {
		body = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// contractTranscriptJSON serializes one record.
func contractTranscriptJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(b)
}

// contractTranscriptAssistant builds the record shape the contract names:
// {"type":"assistant","message":{"content":[…]}}.
func contractTranscriptAssistant(t *testing.T, blocks ...any) string {
	t.Helper()
	return contractTranscriptJSON(t, map[string]any{
		"type":    "assistant",
		"message": map[string]any{"content": blocks},
	})
}

// contractTranscriptStringAssistant builds the defensive alternate shape: a
// message.content that is a plain JSON string rather than a block list.
func contractTranscriptStringAssistant(t *testing.T, s string) string {
	t.Helper()
	return contractTranscriptJSON(t, map[string]any{
		"type":    "assistant",
		"message": map[string]any{"content": s},
	})
}

func contractTranscriptText(s string) map[string]any {
	return map[string]any{"type": "text", "text": s}
}

func contractTranscriptThinking(s string) map[string]any {
	return map[string]any{"type": "thinking", "thinking": s}
}

func contractTranscriptToolUse(name string) map[string]any {
	return map[string]any{"type": "tool_use", "id": "toolu_x", "name": name, "input": map[string]any{"a": 1}}
}

func contractTranscriptUser(t *testing.T, s string) string {
	t.Helper()
	return contractTranscriptJSON(t, map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": s},
	})
}

// ---------------------------------------------------------------------------
// which record wins
// ---------------------------------------------------------------------------

// TestContractLastAssistantTextTakesTheLastQualifyingRecord is the function's
// whole reason for existing: the Stop hook fires after a turn, and the query
// the recall route runs is what claude JUST said — not what it said first.
func TestContractLastAssistantTextTakesTheLastQualifyingRecord(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptAssistant(t, contractTranscriptText("first answer")),
		contractTranscriptUser(t, "a follow-up question"),
		contractTranscriptAssistant(t, contractTranscriptText("second answer")),
		contractTranscriptUser(t, "another follow-up"),
		contractTranscriptAssistant(t, contractTranscriptText("third and final answer")),
	)
	if got := LastAssistantText(path); got != "third and final answer" {
		t.Errorf("LastAssistantText = %q, want the LAST assistant record's text", got)
	}
}

// TestContractLastAssistantTextIgnoresLaterNonAssistantRecords pins that the
// search is for the last ASSISTANT record, not the last record: a transcript
// almost always ends with something else (a user turn, a summary record).
func TestContractLastAssistantTextIgnoresLaterNonAssistantRecords(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptAssistant(t, contractTranscriptText("the answer")),
		contractTranscriptUser(t, "thanks"),
		contractTranscriptJSON(t, map[string]any{"type": "system", "content": "compacted"}),
	)
	if got := LastAssistantText(path); got != "the answer" {
		t.Errorf("LastAssistantText = %q, want %q — records after it are not assistant records", got, "the answer")
	}
}

// TestContractLastAssistantTextSkipsRecordsWithNoTextBlock pins the
// "qualifying" half of the rule. A turn that ends in a tool call is the most
// common shape in these transcripts, and treating it as the last assistant
// record would make the query empty exactly when the conversation is busiest.
func TestContractLastAssistantTextSkipsRecordsWithNoTextBlock(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptAssistant(t, contractTranscriptText("the last thing actually said")),
		contractTranscriptAssistant(t, contractTranscriptThinking("weighing the options")),
		contractTranscriptAssistant(t, contractTranscriptToolUse("Bash")),
		contractTranscriptAssistant(t, contractTranscriptThinking("more"), contractTranscriptToolUse("Read")),
	)
	if got := LastAssistantText(path); got != "the last thing actually said" {
		t.Errorf("LastAssistantText = %q, want %q — thinking/tool_use-only records do not qualify",
			got, "the last thing actually said")
	}
}

// TestContractLastAssistantTextSkipsAnEmptyTextBlock pins "at least one
// NON-EMPTY text content block": an empty text block is not something said.
func TestContractLastAssistantTextSkipsAnEmptyTextBlock(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptAssistant(t, contractTranscriptText("real content here")),
		contractTranscriptAssistant(t, contractTranscriptText("")),
	)
	if got := LastAssistantText(path); got != "real content here" {
		t.Errorf("LastAssistantText = %q, want %q — a record whose only text block is empty does not qualify",
			got, "real content here")
	}
}

// ---------------------------------------------------------------------------
// what a record's text IS
// ---------------------------------------------------------------------------

// TestContractLastAssistantTextJoinsTextBlocksInOrder pins the join: "\n",
// in order, text blocks only. A record commonly carries several.
func TestContractLastAssistantTextJoinsTextBlocksInOrder(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptAssistant(t,
			contractTranscriptText("alpha"),
			contractTranscriptText("beta"),
			contractTranscriptText("gamma"),
		),
	)
	if got := LastAssistantText(path); got != "alpha\nbeta\ngamma" {
		t.Errorf("LastAssistantText = %q, want %q (text blocks joined with a newline, in order)", got, "alpha\nbeta\ngamma")
	}
}

// TestContractLastAssistantTextDropsInterleavedNonTextBlocks pins that the
// join skips thinking and tool_use rather than rendering them: a query made
// of the model's reasoning trace and a JSON tool input is a query about
// nothing.
func TestContractLastAssistantTextDropsInterleavedNonTextBlocks(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptAssistant(t,
			contractTranscriptThinking("private reasoning that must not leak into the query"),
			contractTranscriptText("visible one"),
			contractTranscriptToolUse("Grep"),
			contractTranscriptText("visible two"),
		),
	)
	got := LastAssistantText(path)
	if got != "visible one\nvisible two" {
		t.Errorf("LastAssistantText = %q, want %q", got, "visible one\nvisible two")
	}
	if strings.Contains(got, "private reasoning") || strings.Contains(got, "Grep") {
		t.Errorf("thinking/tool_use content leaked into the text: %q", got)
	}
}

// TestContractLastAssistantTextAcceptsStringContent pins the defensive
// alternate shape the contract names: message.content as a plain JSON string
// counts as that text. The format is Claude-owned and has carried both.
func TestContractLastAssistantTextAcceptsStringContent(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptStringAssistant(t, "said as a bare string"),
	)
	if got := LastAssistantText(path); got != "said as a bare string" {
		t.Errorf("LastAssistantText = %q, want %q — a string message.content counts", got, "said as a bare string")
	}
}

// TestContractLastAssistantTextStringContentStillLosesToALaterRecord pins
// that the string form is a normal qualifying record, not a special case that
// short-circuits the last-wins rule.
func TestContractLastAssistantTextStringContentStillLosesToALaterRecord(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptStringAssistant(t, "the older string form"),
		contractTranscriptAssistant(t, contractTranscriptText("the newer block form")),
	)
	if got := LastAssistantText(path); got != "the newer block form" {
		t.Errorf("LastAssistantText = %q, want the later record %q", got, "the newer block form")
	}
}

// ---------------------------------------------------------------------------
// degrade to ""
// ---------------------------------------------------------------------------

// TestContractLastAssistantTextEmptyWhenNothingQualifies pins the three
// "" cases that are a fact about the FILE rather than about a failure: an
// empty file, a file with no assistant record, and a file whose only
// assistant records carry no text.
func TestContractLastAssistantTextEmptyWhenNothingQualifies(t *testing.T) {
	cases := map[string][]string{
		"empty file":          nil,
		"no assistant record": {contractTranscriptUser(t, "hello"), contractTranscriptUser(t, "anyone there")},
		"no text block": {
			contractTranscriptAssistant(t, contractTranscriptThinking("only thinking")),
			contractTranscriptAssistant(t, contractTranscriptToolUse("Bash")),
		},
	}
	for name, lines := range cases {
		path := contractTranscriptFile(t, lines...)
		if got := LastAssistantText(path); got != "" {
			t.Errorf("%s: LastAssistantText = %q, want \"\"", name, got)
		}
	}
}

// TestContractLastAssistantTextEmptyForAnUnreadablePath pins the posture the
// whole feature rests on: a Stop hook fires with a transcript_path that may
// not exist (a fresh session, a torn-down cell, a payload field claude did not
// send), and the answer is "", never an error and never a panic.
func TestContractLastAssistantTextEmptyForAnUnreadablePath(t *testing.T) {
	dir := t.TempDir()
	for _, path := range []string{
		"",
		filepath.Join(dir, "does-not-exist.jsonl"),
		filepath.Join(dir, "no", "such", "dir", "session.jsonl"),
		dir, // a directory, not a file
	} {
		if got := LastAssistantText(path); got != "" {
			t.Errorf("LastAssistantText(%q) = %q, want \"\"", path, got)
		}
	}
}

// TestContractLastAssistantTextSkipsMalformedLines pins "malformed JSON lines
// are skipped, not fatal" from the side that matters: a good record BEFORE a
// junk line must still be found, and a good record AFTER one must still win.
// A live transcript is appended to while this reads it, so the last line is
// routinely a half-written one.
func TestContractLastAssistantTextSkipsMalformedLines(t *testing.T) {
	path := contractTranscriptFile(t,
		contractTranscriptAssistant(t, contractTranscriptText("older good record")),
		`{"type":"assistant","message":{"content":[{"type":"text","tex`,
		contractTranscriptAssistant(t, contractTranscriptText("newer good record")),
		`not json at all`,
		``,
	)
	if got := LastAssistantText(path); got != "newer good record" {
		t.Errorf("LastAssistantText = %q, want %q — malformed lines are skipped, not fatal", got, "newer good record")
	}
}

// TestContractLastAssistantTailBytesIsTheStatedBound pins the constant the
// contract names. The seek behavior itself is athena-edge's ground; the number
// is pinned here because it is the difference between a bounded read and one
// that pulls a multi-hundred-MB transcript into a hook that blocks a turn.
func TestContractLastAssistantTailBytesIsTheStatedBound(t *testing.T) {
	if lastAssistantTailBytes != 256*1024 {
		t.Errorf("lastAssistantTailBytes = %d, want %d (256 KiB)", lastAssistantTailBytes, 256*1024)
	}
}
