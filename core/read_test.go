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

// TestLastStall covers the api-error/interrupted classification rules in
// LastStall, including entries buried behind trailing bookkeeping records
// and a truncated final line (both realistic transcript-tail shapes).
func TestLastStall(t *testing.T) {
	const syntheticENOTFOUND = `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Unable to connect to API (ENOTFOUND)"}]}}`
	const syntheticCreditBalance = `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: Credit balance is too low"}]}}`
	const sidechainOrdinaryText = `{"type":"assistant","isSidechain":true,"message":{"model":"claude-fable-5","content":[{"type":"text","text":"Sure, here's what I found."}]}}`
	const realModelOrdinaryText = `{"type":"assistant","message":{"model":"claude-fable-5","content":[{"type":"text","text":"Sure, here's the fix."}]}}`

	tests := []struct {
		name          string
		lines         []string
		wantKind      string
		wantTransient bool
		wantTextHas   string // substring, checked only if non-empty
	}{
		{
			name:          "synthetic api-error is transient",
			lines:         []string{syntheticENOTFOUND},
			wantKind:      "api-error",
			wantTransient: true,
			wantTextHas:   "ENOTFOUND",
		},
		{
			name:          "synthetic api-error credit balance is non-transient",
			lines:         []string{syntheticCreditBalance},
			wantKind:      "api-error",
			wantTransient: false,
		},
		{
			name:     "user esc-interrupt",
			lines:    []string{`{"type":"user","message":{"content":"[Request interrupted by user]"}}`},
			wantKind: "interrupted",
		},
		{
			name:     "user esc-interrupt during tool use",
			lines:    []string{`{"type":"user","message":{"content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`},
			wantKind: "interrupted",
		},
		{
			name:     "real model ordinary text is not a stall",
			lines:    []string{`{"type":"assistant","message":{"model":"claude-fable-5","content":[{"type":"text","text":"Sure, here's the fix."}]}}`},
			wantKind: "",
		},
		{
			name:     "real model merely mentioning API Error is not a stall",
			lines:    []string{`{"type":"assistant","message":{"model":"claude-fable-5","content":[{"type":"text","text":"ApiErrorRe = \"API Error|overloaded|rate limit\""}]}}`},
			wantKind: "",
		},
		{
			name:     "user entry with only a tool_result has no text",
			lines:    []string{`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"curl: (7) connection refused"}]}]}}`},
			wantKind: "",
		},
		{
			name: "stall survives trailing summary and blank line",
			lines: []string{
				syntheticENOTFOUND,
				`{"type":"summary","summary":"some summary"}`,
				``,
			},
			wantKind:      "api-error",
			wantTransient: true,
		},
		{
			name: "stall survives a truncated trailing line",
			lines: []string{
				syntheticCreditBalance,
				`{"type":"assis`,
			},
			wantKind:      "api-error",
			wantTransient: false,
		},
		{
			name:     "empty file",
			lines:    []string{},
			wantKind: "",
		},
		{
			name: "trailing sidechain entry is skipped, api-error before it still wins",
			lines: []string{
				syntheticENOTFOUND,
				sidechainOrdinaryText,
			},
			wantKind:      "api-error",
			wantTransient: true,
			wantTextHas:   "ENOTFOUND",
		},
		{
			name: "trailing sidechain api-error is not zeus's stall",
			lines: []string{
				realModelOrdinaryText,
				`{"type":"assistant","isSidechain":true,"message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: subagent hit a wall"}]}}`,
			},
			wantKind: "",
		},
		{
			name:          "synthetic api-error with plain string content",
			lines:         []string{`{"type":"assistant","message":{"model":"<synthetic>","content":"API Error: Unable to connect to API (ENOTFOUND)"}}`},
			wantKind:      "api-error",
			wantTransient: true,
			wantTextHas:   "ENOTFOUND",
		},
		{
			name: "trailing entry with non-object message is skipped past",
			lines: []string{
				syntheticCreditBalance,
				`{"type":"assistant","message":"not an object"}`,
			},
			wantKind:      "api-error",
			wantTransient: false,
		},
		{
			name:          "mixed-case Api Error prefix still matches",
			lines:         []string{`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"Api Error: something went sideways"}]}}`},
			wantKind:      "api-error",
			wantTransient: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "transcript.jsonl")
			if err := os.WriteFile(path, []byte(strings.Join(tt.lines, "\n")), 0o644); err != nil {
				t.Fatalf("write transcript: %v", err)
			}
			got := LastStall(path)
			if got.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q (got %+v)", got.Kind, tt.wantKind, got)
			}
			if tt.wantKind == "api-error" && got.Transient != tt.wantTransient {
				t.Errorf("Transient = %v, want %v", got.Transient, tt.wantTransient)
			}
			if tt.wantTextHas != "" && !strings.Contains(got.Text, tt.wantTextHas) {
				t.Errorf("Text = %q, want substring %q", got.Text, tt.wantTextHas)
			}
		})
	}

	t.Run("nonexistent path", func(t *testing.T) {
		got := LastStall(filepath.Join(t.TempDir(), "missing.jsonl"))
		if got.Kind != "" {
			t.Fatalf("got %+v", got)
		}
	})

	// A transcript bigger than the 256 KiB tail window exercises the
	// start>0 path: the seek lands mid-line in a giant padding record,
	// whose partial first line must be dropped so the trailing synthetic
	// api-error still parses cleanly.
	t.Run("stall found past a partial first line in a large transcript", func(t *testing.T) {
		pad := strings.Repeat("x", 300*1024)
		padding := `{"type":"progress","note":"` + pad + `"}`
		dir := t.TempDir()
		path := filepath.Join(dir, "transcript.jsonl")
		content := strings.Join([]string{padding, syntheticENOTFOUND}, "\n")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		got := LastStall(path)
		if got.Kind != "api-error" {
			t.Fatalf("Kind = %q, want api-error (got %+v)", got.Kind, got)
		}
		if !got.Transient {
			t.Errorf("Transient = %v, want true", got.Transient)
		}
	})
}
