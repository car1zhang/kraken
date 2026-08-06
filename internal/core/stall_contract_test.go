package core

import (
	"strings"
	"testing"
)

// stall_contract_test.go — blind contract tests for the `usage-limit` arm
// `core.classifyStall` gains (50-contract.md, slice A, row `core.classifyStall`).
// Written from the contract text alone, against the exported reader
// (LastStall) rather than the unexported classifier, so the assertions are
// about the classification a transcript tail produces — which is what every
// caller in the binary actually sees.
//
// Dimension: LOGIC — the stated invariants and the happy paths (the five
// trigger phrases, the ordering against the nonTransientAPIErrors check, the
// unchanged classification of every other text). Boundaries and refusals are
// a sibling author's.
//
// Every package-level identifier here is prefixed `contractStall` — read_test.go
// and the other *_contract_test.go files in this package share one declaration
// space.

// contractStallSynthetic renders one synthetic assistant record carrying text.
// `<synthetic>` is the model marker Claude Code stamps on the post-retry
// records classifyStall reads; the existing read_test.go fixtures use the same
// shape.
func contractStallSynthetic(text string) string {
	return `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"` + text + `"}]}}`
}

// contractStallRealModel renders an ordinary (non-synthetic) assistant record.
func contractStallRealModel(text string) string {
	return `{"type":"assistant","message":{"model":"claude-fable-5","content":[{"type":"text","text":"` + text + `"}]}}`
}

// TestContractStallUsageLimitPhrases pins the trigger set: a synthetic
// record whose LOWERCASED text contains any of the listed phrases classifies
// as Stall{Kind: "usage-limit", Transient: false} — with NO requirement of
// an `api error` prefix, because the banners Claude Code actually writes
// carry none. Every fixture below is a real recorded banner shape, verbatim
// modulo the reset time; a test that edits its fixture toward what a gate
// expects (the previous revision manufactured an "API Error: " prefix onto
// each of these) is asserting the gate rather than the banner, which is how
// the arm shipped unreachable.
//
// Amendment 8 retired the bare "rate limit" marker — it also matched an
// organisation burst 429, which is genuinely transient and whose unattended
// auto-retry the usage-limit arm would have removed. The retired marker's
// own obligation is pinned by TestContractStallBareRateLimitStaysTransient
// below.
func TestContractStallUsageLimitPhrases(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"usage limit", "API Error: You have exceeded your usage limit for this account"},
		{"hit your session limit", "You've hit your session limit · resets 5pm (America/New_York)"},
		{"hit your weekly limit", "You've hit your weekly limit · resets Monday"},
		{"hit your opus limit", "You've hit your Opus limit · resets in 3 hours"},
		{"per-model-tier banner", "You've reached your Fable 5 limit. Run /usage-credits to continue or switch models with /model."},
		{"usage credits required", "Usage credits are required for this model."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LastStall(writeTranscript(t, contractStallSynthetic(c.text)))
			if got.Kind != "usage-limit" {
				t.Fatalf("Kind = %q, want %q (got %+v)", got.Kind, "usage-limit", got)
			}
			if got.Transient {
				t.Errorf("Transient = true, want false — an exhausted account is not a retryable blip")
			}
		})
	}
}

// TestContractStallUsageLimitMixedCase is the casing half of the same row
// stated on its own: the containment test is on the LOWERCASED text, so a
// banner shouting its phrase in caps still classifies.
func TestContractStallUsageLimitMixedCase(t *testing.T) {
	for _, text := range []string{
		"API Error: USAGE LIMIT REACHED",
		"Api Error: You've Hit Your Weekly Limit",
		"API ERROR: You've HIT YOUR SESSION LIMIT · resets 5pm",
	} {
		got := LastStall(writeTranscript(t, contractStallSynthetic(text)))
		if got.Kind != "usage-limit" {
			t.Errorf("%q: Kind = %q, want usage-limit", text, got.Kind)
		}
	}
}

// TestContractStallUsageLimitBeatsNonTransientCheck pins the ORDERING
// obligation in the contract row — "this test runs BEFORE the
// nonTransientAPIErrors check". The only way to observe an ordering from
// outside is a text that satisfies both tests at once: it must come back
// `usage-limit`, not `api-error`. Reversed, this text would classify
// `api-error` and the rescue would never fire on it.
func TestContractStallUsageLimitBeatsNonTransientCheck(t *testing.T) {
	const text = "API Error: Credit balance is too low — you have also hit your session limit"
	got := LastStall(writeTranscript(t, contractStallSynthetic(text)))
	if got.Kind != "usage-limit" {
		t.Fatalf("Kind = %q, want usage-limit — the nonTransientAPIErrors check pre-empted the usage-limit arm", got.Kind)
	}
	if got.Transient {
		t.Errorf("Transient = true, want false")
	}
}

// TestContractStallUsageLimitBeatsTransientDefault is the same ordering from
// the other side: a banner that trips NO entry in nonTransientAPIErrors would
// otherwise fall through to the transient api-error default and be nudged
// every ten minutes forever — the eternal-nudge behaviour this arm exists to
// end. The text carries only a usage-limit marker, so the transient default
// is the only thing it can be losing to.
func TestContractStallUsageLimitBeatsTransientDefault(t *testing.T) {
	got := LastStall(writeTranscript(t, contractStallSynthetic("API Error: You've hit your session limit · resets 5pm")))
	if got.Kind != "usage-limit" || got.Transient {
		t.Fatalf("got %+v, want Kind=usage-limit Transient=false", got)
	}
}

// TestContractStallBareRateLimitStaysTransient is Amendment 8's own
// obligation, and it is the exact inverse of the case the amendment retired:
// a bare "rate limit" text is an organisation BURST 429 — Anthropic throttling
// a moment of traffic, not an exhausted account — so it must keep classifying
// as a TRANSIENT api-error. Classified usage-limit it would be routed to
// RetryableHealth=false, and the daemon's unattended auto-retry, which is the
// thing that actually clears a burst, would never fire.
func TestContractStallBareRateLimitStaysTransient(t *testing.T) {
	for _, text := range []string{
		"API Error: 429 rate limit exceeded, please try again later",
		"API Error: 429 {\\\"type\\\":\\\"error\\\",\\\"error\\\":{\\\"type\\\":\\\"rate_limit_error\\\"}}",
		"Api Error: Rate Limit exceeded",
	} {
		got := LastStall(writeTranscript(t, contractStallSynthetic(text)))
		if got.Kind != "api-error" {
			t.Errorf("%q: Kind = %q, want api-error — a burst 429 is not an exhausted account", text, got.Kind)
			continue
		}
		if !got.Transient {
			t.Errorf("%q: Transient = false, want true — the auto-retry is what clears a burst", text)
		}
	}
}

// TestContractStallUsageLimitTextIsOneLined covers the Text half of the row
// in its ROBUST form: whatever the classifier carries through, it is one line
// (a stall reason is rendered into a single board/pane line) and non-empty.
func TestContractStallUsageLimitTextIsOneLined(t *testing.T) {
	const text = "API Error: You've hit your session limit for Claude Opus 5 on this account, and the window will not reset for another four hours; usage limit reached"
	got := LastStall(writeTranscript(t, contractStallSynthetic(text)))
	if got.Kind != "usage-limit" {
		t.Fatalf("Kind = %q, want usage-limit", got.Kind)
	}
	if got.Text == "" {
		t.Fatal("Text is empty — the reason line would carry nothing")
	}
	if strings.ContainsAny(got.Text, "\n\r") {
		t.Errorf("Text = %q, want a single line", got.Text)
	}
}

// TestContractStallUsageLimitTextIsTruncatedAt120 is the LITERAL form of the
// same obligation: the contract row states `Text: TruncateOneLine(text, 120)`.
// The expected value is computed with the very function the contract names, so
// this asserts the argument (the record's own text, at limit 120) rather than
// re-deriving TruncateOneLine's behaviour.
func TestContractStallUsageLimitTextIsTruncatedAt120(t *testing.T) {
	const text = "API Error: You've hit your session limit for Claude Opus 5 on this account, and the window will not reset for another four hours; usage limit reached"
	got := LastStall(writeTranscript(t, contractStallSynthetic(text)))
	if want := TruncateOneLine(text, 120); got.Text != want {
		t.Errorf("Text = %q, want %q", got.Text, want)
	}
}

// TestContractStallOtherTextsUnchanged is the regression half of the row:
// "every other synthetic api-error text keeps classifying exactly as it does
// today, Transient included; a non-synthetic record and a non-`api error`
// prefix still return the zero Stall". Each line below is one of those clauses.
func TestContractStallOtherTextsUnchanged(t *testing.T) {
	cases := []struct {
		name          string
		line          string
		wantKind      string
		wantTransient bool
	}{
		{
			name:          "transient api error still transient api-error",
			line:          contractStallSynthetic("API Error: Unable to connect to API (ENOTFOUND)"),
			wantKind:      "api-error",
			wantTransient: true,
		},
		{
			name:     "non-transient api error still non-transient api-error",
			line:     contractStallSynthetic("API Error: Credit balance is too low"),
			wantKind: "api-error",
		},
		{
			name:     "interrupted turn unchanged",
			line:     `{"type":"user","message":{"content":"[Request interrupted by user]"}}`,
			wantKind: "interrupted",
		},
		{
			// the arm is gated on the synthetic model marker: zeus writing
			// the words in ordinary prose is not a stall
			name:     "a real model merely saying usage limit is not a stall",
			line:     contractStallRealModel("the docs say a usage limit maps to rate limit"),
			wantKind: "",
		},
		{
			// ...and a synthetic record with neither the api-error prefix
			// nor a limit marker is still nothing. (The previous revision's
			// case here put a real limit banner behind this "" expectation,
			// which pinned the unreachable-arm defect as the requirement.)
			name:     "synthetic with no prefix and no limit marker is not a stall",
			line:     contractStallSynthetic("Connection lost — retrying in 5s"),
			wantKind: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LastStall(writeTranscript(t, c.line))
			if got.Kind != c.wantKind {
				t.Fatalf("Kind = %q, want %q (got %+v)", got.Kind, c.wantKind, got)
			}
			if c.wantKind == "api-error" && got.Transient != c.wantTransient {
				t.Errorf("Transient = %v, want %v", got.Transient, c.wantTransient)
			}
		})
	}
}

// TestContractStallGainsNoField pins the row's first sentence — "Gains no
// field" — in the only way a test can: the zero Stall a non-stall produces
// must still compare equal to a hand-built zero value, which stops being true
// the moment an uncompared field is added and set.
func TestContractStallGainsNoField(t *testing.T) {
	got := LastStall(writeTranscript(t, contractStallRealModel("all good")))
	if got != (Stall{}) {
		t.Errorf("non-stall record produced %+v, want the zero Stall", got)
	}
}
