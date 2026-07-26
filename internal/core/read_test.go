package core

import (
	"fmt"
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

// TestLastMessageUUID covers the fork anchor kraken records for a side thread
// (store.Thread.ForkUUID). It is provenance only — never a resume target — so
// every failure mode has to degrade to "" rather than error a spawn carl asked
// for.
//
// The load-bearing property is that the anchor must be MATCHABLE by the only
// thing that ever looks for it: api's threadForkSplit, comparing it against
// the block.UUIDs LastBlocks produces. So every case here that ends in a
// record parseBlocks drops — a `system` record (the common real tail: every
// cell installs a PostToolUse `kraken pr-hook`, which writes one), an
// attachment, a sidechain/meta record, a content-less assistant or user
// record — must scan PAST it to the last record that still yields a block,
// even though those records do carry uuids of their own.
func TestLastMessageUUID(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "last entry's uuid wins",
			lines: []string{
				`{"type":"user","uuid":"aaa","message":{"content":"hi"}}`,
				`{"type":"assistant","uuid":"bbb","message":{"content":[{"type":"text","text":"hey"}]}}`,
			},
			want: "bbb",
		},
		{
			// the shape 336 of 400 real transcripts on this machine end in:
			// a uuid-BEARING system record, which no block ever carries
			name: "skips a trailing system record that carries a uuid",
			lines: []string{
				`{"type":"assistant","uuid":"bbb","message":{"content":[{"type":"text","text":"done"}]}}`,
				`{"type":"system","uuid":"sys-1","subtype":"post_tool_use_hook","content":"kraken pr-hook"}`,
			},
			want: "bbb",
		},
		{
			name: "skips a trailing attachment record",
			lines: []string{
				`{"type":"assistant","uuid":"bbb","message":{"content":[{"type":"text","text":"done"}]}}`,
				`{"type":"attachment","uuid":"att-1","attachment":{"type":"file"}}`,
			},
			want: "bbb",
		},
		{
			// parseBlocks skips sidechain (subagent) and harness-injected
			// meta records, so neither can ever be the anchor either
			name: "skips trailing sidechain and meta records",
			lines: []string{
				`{"type":"user","uuid":"u1","message":{"content":"go"}}`,
				`{"type":"assistant","uuid":"side-1","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent"}]}}`,
				`{"type":"user","uuid":"meta-1","isMeta":true,"message":{"content":"caveat: ..."}}`,
			},
			want: "u1",
		},
		{
			// content-less records survive the type gate but produce no
			// block, so their uuids are just as unmatchable
			name: "skips content-less assistant and user records",
			lines: []string{
				`{"type":"assistant","uuid":"bbb","message":{"content":[{"type":"text","text":"hey"}]}}`,
				`{"type":"assistant","uuid":"empty-1","message":{"content":[]}}`,
				`{"type":"user","uuid":"toolres-1","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
				`{"type":"user","uuid":"reminder-1","message":{"content":[{"type":"text","text":"<system-reminder>be good</system-reminder>"}]}}`,
			},
			want: "bbb",
		},
		{
			// a tool_use block is a real block, and its record IS readable —
			// the split just puts the whole record on the inherited side
			name: "a tool-only assistant record is a valid anchor",
			lines: []string{
				`{"type":"user","uuid":"u1","message":{"content":"read it"}}`,
				`{"type":"assistant","uuid":"tool-1","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}]}}`,
			},
			want: "tool-1",
		},
		{
			name: "skips a truncated trailing line",
			lines: []string{
				`{"type":"assistant","uuid":"ccc","message":{"content":[{"type":"text","text":"hey"}]}}`,
				`{"type":"assis`,
			},
			want: "ccc",
		},
		{
			name:  "no readable message anywhere",
			lines: []string{`{"type":"summary","summary":"s"}`, `{"type":"system","uuid":"sys-1"}`},
			want:  "",
		},
		{
			name:  "empty transcript",
			lines: nil,
			want:  "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LastMessageUUID(writeTranscript(t, tt.lines...)); got != tt.want {
				t.Errorf("LastMessageUUID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLastMessageUUIDMissingFile(t *testing.T) {
	if got := LastMessageUUID(filepath.Join(t.TempDir(), "nope.jsonl")); got != "" {
		t.Errorf("want empty string for a missing transcript, got %q", got)
	}
	// a fresh fork has no transcript path at all yet — the empty path must be
	// just as harmless as a missing file
	if got := LastMessageUUID(""); got != "" {
		t.Errorf("want empty string for an empty path, got %q", got)
	}
}

// TestLastMessageUUIDIsAlwaysAReadableBlock is the producer/consumer pairing
// the two halves of the fork divider live or die by: whatever LastMessageUUID
// hands back at branch time must LATER be findable among the block UUIDs
// LastBlocks produces, or the reading side silently marks nothing inherited
// and the divider never appears. Every tail shape below ends in a record the
// reader drops — the real-world shapes that used to make this fail on ~98% of
// transcripts.
func TestLastMessageUUIDIsAlwaysAReadableBlock(t *testing.T) {
	tails := map[string][]string{
		"system tail":       {`{"type":"system","uuid":"sys-1","content":"kraken pr-hook ran"}`},
		"attachment tail":   {`{"type":"attachment","uuid":"att-1"}`},
		"summary tail":      {`{"type":"summary","summary":"s"}`},
		"sidechain tail":    {`{"type":"assistant","uuid":"side-1","isSidechain":true,"message":{"content":[{"type":"text","text":"sub"}]}}`},
		"empty-content":     {`{"type":"assistant","uuid":"empty-1","message":{"content":[]}}`},
		"tool-result tail":  {`{"type":"user","uuid":"tr-1","message":{"content":[{"type":"tool_result","content":"ok"}]}}`},
		"several bookkeeps": {`{"type":"system","uuid":"sys-1"}`, `{"type":"attachment","uuid":"att-1"}`, `{"type":"system","uuid":"sys-2"}`},
	}
	body := []string{
		`{"type":"user","uuid":"u1","message":{"content":"zeus, do the thing"}}`,
		`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"on it"}]}}`,
		`{"type":"user","uuid":"u2","message":{"content":"status?"}}`,
		`{"type":"assistant","uuid":"a2","message":{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"almost"}]}}`,
	}
	for name, tail := range tails {
		t.Run(name, func(t *testing.T) {
			path := writeTranscript(t, append(append([]string{}, body...), tail...)...)
			anchor := LastMessageUUID(path)
			if anchor == "" {
				t.Fatal("no anchor recorded for a transcript with four readable messages")
			}
			// the anchor is the LAST readable record, not merely some record
			if anchor != "a2" {
				t.Fatalf("anchor = %q, want a2 (the last record that yields blocks)", anchor)
			}
			blocks, _, err := LastBlocks(path, 50)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, b := range blocks {
				if b.UUID == anchor {
					found = true
				}
			}
			if !found {
				t.Fatalf("anchor %q appears in no block LastBlocks produced: %+v", anchor, blocks)
			}
		})
	}
}

// TestLastMessageUUIDGrowsPastASidechainTail: a zeus mid-`/do` writes a tail
// made almost entirely of subagent (sidechain) records and tool results —
// which is exactly when carl branches a thread — so an anchor read from one
// fixed tail slice would come back blank and silently drop the divider. The
// window grows until it finds a readable record, bounded by the same ceiling
// LastBlocks uses.
func TestLastMessageUUIDGrowsPastASidechainTail(t *testing.T) {
	defer swapScanWindows(t, 1<<10, 1<<20)()

	lines := []string{
		`{"type":"user","uuid":"u1","message":{"content":"zeus, do the thing"}}`,
		`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"delegating a slice to hephaestus"}]}}`,
	}
	for i := 0; i < 200; i++ { // ~20 KiB of subagent traffic on top
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","uuid":"side-%d","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent step %d padded out"}]}}`, i, i))
	}
	path := writeTranscript(t, lines...)

	if got := LastMessageUUID(path); got != "a1" {
		t.Fatalf("anchor = %q, want a1 — the scan gave up inside the sidechain tail", got)
	}
}

// ... but the growth is bounded by the same ceiling: past it the anchor is
// blank (the documented graceful path — no divider) rather than a full-file
// read at every branch.
func TestLastMessageUUIDStopsAtScanLimit(t *testing.T) {
	defer swapScanWindows(t, 1<<10, 4<<10)()

	lines := []string{`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"the only readable record"}]}}`}
	for i := 0; i < 400; i++ { // ~40 KiB of sidechain, far past the 4 KiB ceiling
		lines = append(lines, fmt.Sprintf(`{"type":"assistant","uuid":"side-%d","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent step %d padded out"}]}}`, i, i))
	}
	path := writeTranscript(t, lines...)

	if got := LastMessageUUID(path); got != "" {
		t.Fatalf("anchor = %q, want \"\" — the scan ran past its ceiling", got)
	}
}

// TestLastBlocksStopsAtScanLimit: the window doubling must be BOUNDED. A
// forked thread's transcript opens with zeus' whole history copied in, and a
// `/do` phase's sidechain records — which parseBlocks skips — mean a large n
// is often unsatisfiable at any depth, so without a ceiling the scan walks a
// multi-MB file to byte 0 (re-reading and re-parsing at every doubling) on
// every poll. Past the limit the answer is simply shorter, never an error.
func TestLastBlocksStopsAtScanLimit(t *testing.T) {
	defer swapScanWindows(t, 1<<10, 4<<10)()

	var lines []string
	for i := 0; i < 400; i++ { // ~100 bytes each: ~40 KiB, well past the ceiling
		lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"u%d","message":{"content":"message number %d padded out a bit"}}`, i, i))
	}
	path := writeTranscript(t, lines...)

	blocks, counted, err := LastBlocks(path, 400)
	if err != nil {
		t.Fatal(err)
	}
	if counted == 0 || len(blocks) == 0 {
		t.Fatal("returned nothing — a bounded scan must still serve the tail it read")
	}
	if counted >= 400 {
		t.Fatalf("counted %d of 400 — the scan reached the file head despite the ceiling", counted)
	}
	// what it does return is the TAIL, in order, ending at the last message
	if got := blocks[len(blocks)-1].UUID; got != "u399" {
		t.Fatalf("last block uuid = %q, want u399", got)
	}
	// and the ceiling actually bounded the read: ~4 KiB of ~100-byte records
	if len(blocks) > 100 {
		t.Fatalf("read %d blocks — more than a %d-byte ceiling can hold", len(blocks), lastBlocksScanLimit)
	}
}

// TestLastBlocksGrowsWindowWithinLimit is the other half: below the ceiling
// the window still doubles until n is satisfied, so a small n keeps working
// on a file whose first window doesn't hold it.
func TestLastBlocksGrowsWindowWithinLimit(t *testing.T) {
	defer swapScanWindows(t, 1<<10, 1<<20)()

	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, fmt.Sprintf(`{"type":"user","uuid":"u%d","message":{"content":"message number %d padded out a bit"}}`, i, i))
	}
	path := writeTranscript(t, lines...)

	blocks, counted, err := LastBlocks(path, 150)
	if err != nil {
		t.Fatal(err)
	}
	if counted != 150 || len(blocks) != 150 {
		t.Fatalf("counted %d / %d blocks, want 150 — the window stopped growing early", counted, len(blocks))
	}
	if blocks[0].UUID != "u50" || blocks[149].UUID != "u199" {
		t.Fatalf("window = %s…%s, want u50…u199", blocks[0].UUID, blocks[149].UUID)
	}
}

// swapScanWindows shrinks the tail window and the scan ceiling for one test
// and returns the restore func — the same seam lastUserScanLimit's own tests
// use, and the reason both are vars.
func swapScanWindows(t *testing.T, start, limit int64) func() {
	t.Helper()
	oldStart, oldLimit := tailStartWindow, lastBlocksScanLimit
	tailStartWindow, lastBlocksScanLimit = start, limit
	return func() { tailStartWindow, lastBlocksScanLimit = oldStart, oldLimit }
}

// TestLastBlocksCarriesRecordUUID pins block.UUID: every block a record
// yields carries that record's own uuid, INCLUDING the several blocks one
// assistant record can produce (thinking + text + tool_use all share it) and
// the user blocks, which are built by userBlocks and stamped afterwards. This
// is what lets the side-thread read endpoint (api/threads.go) split a fork's
// transcript at store.Thread.ForkUUID — without the stamp there is no way to
// tell inherited history from the thread's own turns.
func TestLastBlocksCarriesRecordUUID(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","uuid":"u1","message":{"content":"hello"}}`,
		`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"hi"},{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/x"}}]}}`,
		`{"type":"user","uuid":"u2","message":{"content":"again"}}`,
	)
	blocks, _, err := LastBlocks(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ kind, uuid string }{
		{"user", "u1"},
		{"thinking", "a1"},
		{"text", "a1"},
		{"tool", "a1"},
		{"user", "u2"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("got %d blocks, want %d: %+v", len(blocks), len(want), blocks)
	}
	for i, w := range want {
		if blocks[i].Kind != w.kind || blocks[i].UUID != w.uuid {
			t.Errorf("block %d = {kind %q, uuid %q}, want {%q, %q}", i, blocks[i].Kind, blocks[i].UUID, w.kind, w.uuid)
		}
	}
}

// A record with no uuid of its own (hand-edited transcript, schema drift)
// must still produce blocks — with a blank UUID, never a dropped block.
func TestLastBlocksUUIDBlankWhenRecordHasNone(t *testing.T) {
	path := writeTranscript(t, `{"type":"user","message":{"content":"no uuid here"}}`)
	blocks, _, err := LastBlocks(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1: %+v", len(blocks), blocks)
	}
	if blocks[0].UUID != "" {
		t.Errorf("UUID = %q, want empty", blocks[0].UUID)
	}
}
