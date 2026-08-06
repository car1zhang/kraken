package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stall_edge_test.go — the EDGE dimension of classifyStall's new
// "usage-limit" arm (artifacts/50-contract.md, slice A): the boundaries of
// what counts as a usage limit, the orderings the new test imposes on the
// existing ones, and the shapes that must keep classifying exactly as they
// do today. Written blind from the contract text; the stated invariants and
// happy paths belong to the sibling author, so every package-level
// identifier here carries the `edge` prefix.
//
// Everything goes through LastStall, the exported reader that classifyStall
// serves — a transcript on disk is the only input the classification ever
// really has, and testing through it also pins that the new arm survives
// the record-skipping the reader does on the way.

// edgeStallTranscript writes lines to a fresh temp jsonl and returns its
// path. Deliberately its own helper rather than read_test.go's: this file
// must compile on its own if that one moves.
func edgeStallTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "edge-stall.jsonl")
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// edgeSynthetic wraps text in the synthetic-assistant record shape Claude
// Code writes after its own retries are exhausted — the ONLY record shape
// that can be a stall.
func edgeSynthetic(text string) string {
	return `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"` + text + `"}]}}`
}

// Every usage-limit phrase, in the shape the banner actually arrives: the
// real ones carry NO `api error` prefix, and the arm must classify them
// bare as well as prefixed. Kind must be "usage-limit" and Transient FALSE —
// the whole point of the arm is that a nudge cannot fix an exhausted
// account, so a transient classification would restart today's ten-minute
// eternal nudge under a new name.
//
// Amendment 8 dropped the bare "rate limit" marker this table used to carry
// two cases for; they moved into
// TestEdgeOrgBurstRateLimitStaysTransient below, which asserts the opposite
// answer for the same texts.
func TestEdgeUsageLimitPhrasesClassifyNonTransient(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"usage limit", "API Error: You have exceeded your usage limit"},
		{"session limit banner", "You've hit your session limit · resets 3pm (America/New_York)"},
		{"weekly limit banner", "You've hit your weekly limit · resets Sunday"},
		{"opus limit banner", "You've hit your opus limit · resets Sunday"},
		{"per-model-tier banner", "You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model."},
		{"usage credits banner", "Usage credits are required for this model."},
		// a prefixed limit text must keep classifying too — the marker check
		// runs before the prefix gate, not instead of it
		{"prefixed session limit", "API Error: You've hit your session limit · resets 3pm"},
		// the match is on the LOWERCASED text, so the banner's own casing
		// (which Claude Code renders in sentence case) must not matter
		{"upper case banner", "YOU'VE HIT YOUR SESSION LIMIT"},
		{"mixed case prefix and phrase", "Api Error: You'Ve Hit Your Weekly Limit"},
		// containment rather than word equality still holds: the banner is
		// rendered inside a longer sentence
		{"phrase embedded in a longer sentence", "API Error: heads up — you've hit your opus limit for now, resets Sunday"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LastStall(edgeStallTranscript(t, edgeSynthetic(c.text)))
			if got.Kind != "usage-limit" {
				t.Fatalf("Kind = %q, want %q (got %+v)", got.Kind, "usage-limit", got)
			}
			if got.Transient {
				t.Errorf("Transient = true, want false — an exhausted account is not retryable")
			}
			if got.Text == "" {
				t.Errorf("Text is empty — the reason line renders it")
			}
		})
	}
}

// The new test runs BEFORE the nonTransientAPIErrors check, and that
// ordering is only observable on a text matching both. A limit banner that
// also carries a non-transient marker must come back as a usage limit — the
// rescue path — rather than as a needs-attention api-error a human has to
// fix by hand.
func TestEdgeUsageLimitWinsOverTheNonTransientCheck(t *testing.T) {
	both := "API Error: you've hit your session limit and your credit balance is too low"
	got := LastStall(edgeStallTranscript(t, edgeSynthetic(both)))
	if got.Kind != "usage-limit" {
		t.Fatalf("Kind = %q, want usage-limit — the usage-limit test must run first (got %+v)", got.Kind, got)
	}
	if got.Transient {
		t.Error("Transient = true, want false")
	}
}

// ... and the same ordering on the transient side: a text carrying a
// usage-limit phrase and otherwise nothing that would make it non-transient
// is still a usage limit rather than the transient default, or the nudge
// loop comes straight back.
func TestEdgeUsageLimitWinsOverTheTransientDefault(t *testing.T) {
	got := LastStall(edgeStallTranscript(t, edgeSynthetic("API Error: 429 — you've hit your session limit, please retry later")))
	if got.Kind != "usage-limit" || got.Transient {
		t.Fatalf("= %+v, want Kind=usage-limit Transient=false", got)
	}
}

// Amendment 8's other half, and the boundary the retired marker sat on: an
// ORGANISATION burst 429 is a throughput ceiling, not an exhausted account.
// It passes in seconds and a retry fixes it, so it must stay
// api-error/transient and keep its auto-retry — classifying it usage-limit
// removed that retry and, for the cells already running when this ships (no
// StopFailure hook until a cell is respawned), replaced it with nothing at
// all. The bare "rate limit" marker matched every one of these.
func TestEdgeOrgBurstRateLimitStaysTransient(t *testing.T) {
	cases := map[string]string{
		"api 429 body":     `API Error: 429 {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"Number of requests has exceeded your rate limit\"}}`,
		"bare rate limit":  "API Error: 429 rate limit exceeded",
		"rate limited":     "API Error: 429 rate limited, retry after 3600s",
		"rate_limit_error": "API Error: rate_limit_error — slow down",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			got := LastStall(edgeStallTranscript(t, edgeSynthetic(text)))
			if got.Kind != "api-error" {
				t.Fatalf("Kind = %q, want api-error — a burst 429 is not an exhausted account (got %+v)", got.Kind, got)
			}
			if !got.Transient {
				t.Errorf("Transient = false, want true — a burst 429 is fixed by the auto-retry")
			}
		})
	}
}

// The two entry conditions are a conjunction, and each half failing on its
// own must fall through to exactly what it classifies as today.
func TestEdgeUsageLimitEntryConditions(t *testing.T) {
	cases := []struct {
		name          string
		line          string
		wantKind      string
		wantTransient bool
	}{
		{
			// no prefix and no limit marker: an unrecognised synthetic text
			// is not a stall. (A synthetic WITH a marker and no prefix now
			// classifies — that is the reachability fix — so the fall-through
			// is pinned on a genuinely marker-less text.)
			name:     "no prefix and no limit marker",
			line:     edgeSynthetic("Stream disconnected before completion"),
			wantKind: "",
		},
		{
			name:     "phrase present, prefix present, but the record is a real model's",
			line:     `{"type":"assistant","message":{"model":"claude-fable-5","content":[{"type":"text","text":"API Error: you've hit your session limit"}]}}`,
			wantKind: "",
		},
		{
			// a SUBAGENT's limit is not zeus' stall — same exclusion the
			// existing api-error arm has, and it must not be lost by the new
			// test being tried first
			name:     "sidechain synthetic usage limit is not zeus' stall",
			line:     `{"type":"assistant","isSidechain":true,"message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: you've hit your session limit"}]}}`,
			wantKind: "",
		},
		{
			// prefix present, but none of the five phrases: unchanged
			name:          "api error with no limit phrase stays a transient api-error",
			line:          edgeSynthetic("API Error: Unable to connect to API (ENOTFOUND)"),
			wantKind:      "api-error",
			wantTransient: true,
		},
		{
			name:          "api error whose non-transient marker is not a limit phrase is unchanged",
			line:          edgeSynthetic("API Error: Credit balance is too low"),
			wantKind:      "api-error",
			wantTransient: false,
		},
		{
			// overloaded is the capacity 529, not a quota: it must keep its
			// transient api-error classification and its nudge
			name:          "plain overloaded capacity error stays transient",
			line:          edgeSynthetic("API Error: 529 overloaded_error"),
			wantKind:      "api-error",
			wantTransient: true,
		},
		{
			// near-miss wording: none of the five phrases appear, so this is
			// an ordinary api-error however much it smells like a quota
			name:          "a limit-shaped sentence that matches no phrase is unchanged",
			line:          edgeSynthetic("API Error: request limit for this organization was reached"),
			wantKind:      "api-error",
			wantTransient: true,
		},
		{
			name:     "a user record mentioning a usage limit is not a stall",
			line:     `{"type":"user","message":{"content":"API Error: you've hit your usage limit"}}`,
			wantKind: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LastStall(edgeStallTranscript(t, c.line))
			if got.Kind != c.wantKind {
				t.Fatalf("Kind = %q, want %q (got %+v)", got.Kind, c.wantKind, got)
			}
			if c.wantKind == "api-error" && got.Transient != c.wantTransient {
				t.Errorf("Transient = %v, want %v", got.Transient, c.wantTransient)
			}
		})
	}
}

// The reason line built from this Text goes into a job's blocked detail and
// a tmux pane, so the Text is one-lined and truncated at 120 runes exactly
// as the api-error arm's is. A real limit banner is long and multi-line.
func TestEdgeUsageLimitTextIsOneLinedAndTruncated(t *testing.T) {
	long := "API Error: You've hit your session limit · resets at 4:00 PM. " +
		strings.Repeat("Upgrade to a higher plan or wait for the window to reset. ", 6)
	// the transcript is JSON, so the newlines have to be encoded ones
	line := `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"` +
		strings.ReplaceAll(long, ". ", ".\\n") + `"}]}}`

	got := LastStall(edgeStallTranscript(t, line))
	if got.Kind != "usage-limit" {
		t.Fatalf("Kind = %q, want usage-limit (got %+v)", got.Kind, got)
	}
	if strings.ContainsAny(got.Text, "\n\r") {
		t.Errorf("Text carries a line break: %q", got.Text)
	}
	if n := len([]rune(got.Text)); n > 120 {
		t.Errorf("Text is %d runes, want at most 120: %q", n, got.Text)
	}
}

// Degradation: the new arm must survive the same tail shapes the existing
// one does — a trailing bookkeeping record and a truncated final line are
// both what a transcript caught mid-write looks like, and a limit missed
// there is a cell that never gets rescued.
func TestEdgeUsageLimitSurvivesTrailingJunk(t *testing.T) {
	limit := edgeSynthetic("API Error: You've hit your session limit · resets 3pm")

	t.Run("trailing summary and blank line", func(t *testing.T) {
		got := LastStall(edgeStallTranscript(t, limit, `{"type":"summary","summary":"s"}`, ``))
		if got.Kind != "usage-limit" {
			t.Fatalf("Kind = %q, want usage-limit (got %+v)", got.Kind, got)
		}
	})

	t.Run("truncated trailing line", func(t *testing.T) {
		got := LastStall(edgeStallTranscript(t, limit, `{"type":"assis`))
		if got.Kind != "usage-limit" {
			t.Fatalf("Kind = %q, want usage-limit (got %+v)", got.Kind, got)
		}
	})

	t.Run("plain string content instead of an array", func(t *testing.T) {
		line := `{"type":"assistant","message":{"model":"<synthetic>","content":"API Error: you've hit your weekly limit"}}`
		got := LastStall(edgeStallTranscript(t, line))
		if got.Kind != "usage-limit" {
			t.Fatalf("Kind = %q, want usage-limit (got %+v)", got.Kind, got)
		}
	})
}

// A later, ordinary turn means the limit is over: the classification is of
// the LAST stall-eligible record, so a usage limit followed by a real
// model's reply must not keep reporting the cell as limited.
func TestEdgeUsageLimitDoesNotOutliveTheTurn(t *testing.T) {
	got := LastStall(edgeStallTranscript(t,
		edgeSynthetic("API Error: You've hit your session limit"),
		`{"type":"assistant","message":{"model":"claude-fable-5","content":[{"type":"text","text":"back on it"}]}}`,
	))
	if got.Kind != "" {
		t.Fatalf("Kind = %q, want \"\" — the cell answered after the limit (got %+v)", got.Kind, got)
	}
}
