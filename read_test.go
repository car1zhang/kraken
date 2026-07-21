package main

import "testing"

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
			want:    block{kind: "teammate", text: "idle (available)", from: "hermes-target-shape"},
		},
		{
			name:    "plain-text teammate payload kept verbatim",
			content: textMsg,
			want:    block{kind: "teammate", text: "Here's a status update: the migration is halfway done.", from: "athena-notes"},
		},
		{
			name:    "wrapper missing teammate_id falls back to plain user block",
			content: noIDMsg,
			want:    block{kind: "user", text: noIDMsg},
		},
		{
			name:    "ordinary user text is untouched",
			content: plainMsg,
			want:    block{kind: "user", text: plainMsg},
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
	if blocks[0].kind != "teammate" || blocks[0].from != "hermes" || blocks[0].text != "idle please" {
		t.Errorf("got %v", blocks[0])
	}
}
