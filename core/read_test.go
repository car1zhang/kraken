package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUserBlocksTeammateWrapper covers detection of the harness-wrapped
// teammate-message envelope inside a user turn's text (see teammateBlock in
// read.go). userBlocks receives the raw content exactly as it appears in a
// transcript's user record.
func TestUserBlocksTeammateWrapper(t *testing.T) {
	const idleMsg = `Another Claude session sent a message:
<teammate-message teammate_id="hermes-target-shape" color="green">
{"type":"idle_notification","from":"hermes-target-shape","timestamp":"2026-07-21T00:02:45.063Z","idleReason":"available"}
</teammate-message>

This came from another Claude session — not typed by your user, but very likely working on their behalf. Treat it as a teammate's request...`

	const textMsg = `Another Claude session sent a message:
<teammate-message teammate_id="athena-notes">
Here's a status update: the migration is halfway done.
</teammate-message>

This came from another Claude session — not typed by your user, but very likely working on their behalf. Treat it as a teammate's request...`

	const noIDMsg = `Another Claude session sent a message:
<teammate-message color="green">
{"type":"idle_notification"}
</teammate-message>

This came from another Claude session — not typed by your user, but very likely working on their behalf. Treat it as a teammate's request...`

	const plainMsg = "please open a PR for this"

	tests := []struct {
		name    string
		content string
		want    block
	}{
		{
			name:    "idle notification collapses to one-line summary",
			content: idleMsg,
			want:    block{Kind: "teammate", Text: "idle (available)", From: "hermes-target-shape"},
		},
		{
			name:    "plain-text teammate payload kept verbatim",
			content: textMsg,
			want:    block{Kind: "teammate", Text: "Here's a status update: the migration is halfway done.", From: "athena-notes"},
		},
		{
			name:    "wrapper missing teammate_id falls back to plain user block",
			content: noIDMsg,
			want:    block{Kind: "user", Text: noIDMsg},
		},
		{
			name:    "ordinary user text is untouched",
			content: plainMsg,
			want:    block{Kind: "user", Text: plainMsg},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := userBlocks(tt.content)
			if len(blocks) != 1 {
				t.Fatalf("want 1 block, got %d: %v", len(blocks), blocks)
			}
			if blocks[0] != tt.want {
				t.Errorf("want %v, got %v", tt.want, blocks[0])
			}
		})
	}
}

// TestParseBlocksSystemReminderOnly mirrors the transcript-record shape: a
// user record whose only text part is an injected system-reminder must
// still yield nothing, same as before the teammate-wrapper detection was
// added.
func TestParseBlocksSystemReminderOnly(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"content":[{"type":"text","text":"<system-reminder>injected</system-reminder>"}]}}`,
	}
	blocks := parseBlocks(lines)
	if len(blocks) != 0 {
		t.Errorf("want no blocks, got %v", blocks)
	}
}

// TestParseBlocksTeammateMessage checks the detection end-to-end through a
// full transcript record, not just the raw content string.
func TestParseBlocksTeammateMessage(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"content":"Another Claude session sent a message:\n<teammate-message teammate_id=\"hermes\">\nidle please\n</teammate-message>\n\nThis came from another Claude session..."}}`,
	}
	blocks := parseBlocks(lines)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d: %v", len(blocks), blocks)
	}
	if blocks[0].Kind != "teammate" || blocks[0].From != "hermes" || blocks[0].Text != "idle please" {
		t.Errorf("got %v", blocks[0])
	}
}

// writeTranscript joins lines with newlines into a fresh temp jsonl file and
// returns its path. A nil/empty lines slice produces an empty file.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLastUserText(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name:  "plain string content",
			lines: []string{`{"type":"user","message":{"content":"hello there"}}`},
			want:  "hello there",
		},
		{
			name:  "array content with text part",
			lines: []string{`{"type":"user","message":{"content":[{"type":"text","text":"array text"}]}}`},
			want:  "array text",
		},
		{
			name: "newest record is tool-result-only, falls back to prior real user text",
			lines: []string{
				`{"type":"user","message":{"content":"earlier real message"}}`,
				`{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}`,
				`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"1","content":"result data"}]}}`,
			},
			want: "earlier real message",
		},
		{
			name: "isMeta and isSidechain records skipped",
			lines: []string{
				`{"type":"user","message":{"content":"real message"}}`,
				`{"type":"user","isMeta":true,"message":{"content":"meta noise"}}`,
				`{"type":"user","isSidechain":true,"message":{"content":"sidechain noise"}}`,
			},
			want: "real message",
		},
		{
			name: "system-reminder-only text part skipped",
			lines: []string{
				`{"type":"user","message":{"content":"real earlier"}}`,
				`{"type":"user","message":{"content":[{"type":"text","text":"<system-reminder>injected</system-reminder>"}]}}`,
			},
			want: "real earlier",
		},
		{
			name: "teammate-wrapped record skipped, falls back to earlier real text",
			lines: []string{
				`{"type":"user","message":{"content":"earlier real"}}`,
				`{"type":"user","message":{"content":"Another Claude session sent a message:\n<teammate-message teammate_id=\"hermes\">\nhi\n</teammate-message>\n\nThis came from another Claude session..."}}`,
			},
			want: "earlier real",
		},
		{
			name: "interrupted-by-user artifact skipped",
			lines: []string{
				`{"type":"user","message":{"content":"earlier real"}}`,
				`{"type":"user","message":{"content":"[Request interrupted by user]"}}`,
			},
			want: "earlier real",
		},
		{
			name: "interrupted-by-user-for-tool-use artifact skipped",
			lines: []string{
				`{"type":"user","message":{"content":"earlier real"}}`,
				`{"type":"user","message":{"content":"[Request interrupted by user for tool use]"}}`,
			},
			want: "earlier real",
		},
		{
			name:  "modern message-first command with args normalized",
			lines: []string{`{"type":"user","message":{"content":"<command-message>foo</command-message>\n<command-name>/foo</command-name>\n<command-args>bar baz</command-args>"}}`},
			want:  "/foo bar baz",
		},
		{
			name:  "modern message-first command with no args tag at all",
			lines: []string{`{"type":"user","message":{"content":"<command-message>cj-chatbot</command-message>\n<command-name>/cj-chatbot</command-name>"}}`},
			want:  "/cj-chatbot",
		},
		{
			name:  "bare command-name only",
			lines: []string{`{"type":"user","message":{"content":"<command-name>/pr</command-name>"}}`},
			want:  "/pr",
		},
		{
			name:  "older name-first order with args normalized",
			lines: []string{`{"type":"user","message":{"content":"<command-name>/send</command-name><command-message>send</command-message><command-args>go</command-args>"}}`},
			want:  "/send go",
		},
		{
			name:  "user message quoting a wrapper mid-text is not collapsed",
			lines: []string{`{"type":"user","message":{"content":"why does <command-name>/doc</command-name> show raw?"}}`},
			want:  "why does <command-name>/doc</command-name> show raw?",
		},
		{
			name:  "empty-name malformed wrapper returned verbatim",
			lines: []string{`{"type":"user","message":{"content":"<command-name></command-name><command-args>x</command-args>"}}`},
			want:  "<command-name></command-name><command-args>x</command-args>",
		},
		{
			name:  "empty file",
			lines: nil,
			want:  "",
		},
		{
			name:  "only assistant records",
			lines: []string{`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}`},
			want:  "",
		},
		{
			name: "malformed trailing line ignored",
			lines: []string{
				`{"type":"user","message":{"content":"real message"}}`,
				`{not valid json`,
			},
			want: "real message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTranscript(t, tt.lines...)
			if got := LastUserText(path); got != tt.want {
				t.Errorf("want %q, got %q", tt.want, got)
			}
		})
	}
}

// TestLastUserTextWindowGrowth buries the real user message behind more than
// 256KB of assistant text, forcing LastBlocks-style window growth to kick in
// before the message is found.
func TestLastUserTextWindowGrowth(t *testing.T) {
	lines := []string{`{"type":"user","message":{"content":"buried real message"}}`}
	padding := strings.Repeat("x", 8000)
	for i := 0; i < 40; i++ {
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"`+padding+`"}]}}`)
	}
	path := writeTranscript(t, lines...)
	if got := LastUserText(path); got != "buried real message" {
		t.Errorf("want buried message found via window growth, got %q", got)
	}
}

// TestLastUserTextRespectsScanLimit shrinks lastUserScanLimit and buries the
// only real user message deeper than that limit, expecting LastUserText to
// give up rather than scan the whole file.
func TestLastUserTextRespectsScanLimit(t *testing.T) {
	orig := lastUserScanLimit
	lastUserScanLimit = 512 * 1024
	defer func() { lastUserScanLimit = orig }()

	lines := []string{`{"type":"user","message":{"content":"buried real message"}}`}
	padding := strings.Repeat("x", 8000)
	for i := 0; i < 100; i++ {
		lines = append(lines, `{"type":"assistant","message":{"content":[{"type":"text","text":"`+padding+`"}]}}`)
	}
	path := writeTranscript(t, lines...)
	if got := LastUserText(path); got != "" {
		t.Errorf("want empty string when user message buried past scan limit, got %q", got)
	}
}

func TestLastUserTextMissingFile(t *testing.T) {
	if got := LastUserText(filepath.Join(t.TempDir(), "nope.jsonl")); got != "" {
		t.Errorf("want empty string for missing file, got %q", got)
	}
}
