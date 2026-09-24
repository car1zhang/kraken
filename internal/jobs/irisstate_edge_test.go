package jobs

import (
	"strings"
	"testing"

	"kraken/internal/core"
)

// Edge/refusal tests for slice 2 of the dream-2026-09-24 contract: the
// "What you know about a job, and what you never add" block in irisBrief().
// Every assertion goes through the function's return value; irisBrief is pure
// and needs no HOME isolation.

const (
	edgeIrisStateHeading   = "## What you know about a job, and what you never add"
	edgeIrisStateBlocked   = "`blocked` means the cell is waiting for carl at a gate"
	edgeIrisStateBoardGet  = "kraken board get <slug>"
	edgeIrisStateReport    = "You report; you never diagnose."
	edgeIrisStateNoKnob    = "never name a knob, a threshold or a remedy you have not read in the code"
	edgeIrisStateAnchor    = "an escalation into a channel with no reader."
	edgeIrisStateCredsHead = "## Credentials in messages"
)

var edgeIrisStateRequired = []string{
	edgeIrisStateHeading,
	edgeIrisStateBlocked,
	edgeIrisStateBoardGet,
	edgeIrisStateReport,
	edgeIrisStateNoKnob,
}

// TestEdgeIrisStateEachSubstringExactlyOnce: each of (a)-(e) appears EXACTLY
// once — absent fails, and so does a duplicated paragraph.
func TestEdgeIrisStateEachSubstringExactlyOnce(t *testing.T) {
	brief := irisBrief()
	for _, want := range edgeIrisStateRequired {
		if n := strings.Count(brief, want); n != 1 {
			t.Errorf("irisBrief() contains %q %d times, want exactly 1", want, n)
		}
	}
}

// TestEdgeIrisStateBlockedBackticksAreLiteral: substring (b) carries literal
// backticks around `blocked`; a plain-text rendering of the same sentence
// is not (b), and must not appear as a second copy of it.
func TestEdgeIrisStateBlockedBackticksAreLiteral(t *testing.T) {
	brief := irisBrief()
	if !strings.Contains(brief, "`blocked`") {
		t.Errorf("irisBrief() lacks the literal backticked token `blocked`")
	}
	if bare := "blocked means the cell is waiting for carl at a gate"; strings.Contains(brief, bare) {
		t.Errorf("irisBrief() contains the un-backticked form %q; the contract's (b) carries literal backticks", bare)
	}
}

// TestEdgeIrisStateHeadingIsOwnMarkdownLine: (a) is a real `## ` heading on
// a line by itself, not prose that quotes the heading text.
func TestEdgeIrisStateHeadingIsOwnMarkdownLine(t *testing.T) {
	brief := irisBrief()
	found := 0
	for _, line := range strings.Split(brief, "\n") {
		if line == edgeIrisStateHeading {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("irisBrief() has %d lines exactly equal to %q, want 1", found, edgeIrisStateHeading)
	}
}

// TestEdgeIrisStateOrderAndPlacement: anchor < (a) < (b) < (c) < (d) < (e) <
// "## Credentials in messages" — so the block is neither before the memory
// section's closing paragraph nor inside the credentials section.
func TestEdgeIrisStateOrderAndPlacement(t *testing.T) {
	brief := irisBrief()
	seq := append([]string{edgeIrisStateAnchor}, edgeIrisStateRequired...)
	seq = append(seq, edgeIrisStateCredsHead)
	prev := -1
	prevName := "(start)"
	for _, s := range seq {
		i := strings.Index(brief, s)
		if i < 0 {
			t.Fatalf("irisBrief() does not contain %q", s)
		}
		if i <= prev {
			t.Errorf("irisBrief(): %q (at %d) does not come after %q (at %d)", s, i, prevName, prev)
		}
		prev, prevName = i, s
	}
	// Every occurrence of the credentials heading must follow (e): a block
	// placed inside the credentials section would put one before it.
	eIdx := strings.Index(brief, edgeIrisStateNoKnob)
	cIdx := strings.Index(brief, edgeIrisStateCredsHead)
	if eIdx >= 0 && cIdx >= 0 && cIdx < eIdx {
		t.Errorf("irisBrief(): (e) sits after %q — the block is inside the credentials section", edgeIrisStateCredsHead)
	}
}

// TestEdgeIrisStateNoHeadingInsideBlock: the two paragraphs sit UNDER the new
// heading — no other markdown heading line intervenes between (a) and (e).
func TestEdgeIrisStateNoHeadingInsideBlock(t *testing.T) {
	brief := irisBrief()
	a := strings.Index(brief, edgeIrisStateHeading)
	e := strings.Index(brief, edgeIrisStateNoKnob)
	if a < 0 || e < 0 || e < a {
		t.Fatalf("irisBrief(): heading at %d, (e) at %d — cannot bound the block", a, e)
	}
	inner := brief[a+len(edgeIrisStateHeading) : e]
	for _, line := range strings.Split(inner, "\n") {
		if strings.HasPrefix(line, "#") {
			t.Errorf("irisBrief(): heading line %q intervenes between (a) and (e)", line)
		}
	}
}

// TestEdgeIrisStateNoQueuedVocabulary: the retired word stays absent.
func TestEdgeIrisStateNoQueuedVocabulary(t *testing.T) {
	if strings.Contains(irisBrief(), "queued") {
		t.Errorf("irisBrief() contains the retired word %q", "queued")
	}
}

// TestEdgeIrisStateStaysUnderCeiling: the 16000-byte ceiling still holds.
func TestEdgeIrisStateStaysUnderCeiling(t *testing.T) {
	if n := len(irisBrief()); n > 16000 {
		t.Fatalf("irisBrief() is %d bytes, ceiling is 16000", n)
	}
}

// TestEdgeIrisStateByteStable: two calls return byte-identical strings.
func TestEdgeIrisStateByteStable(t *testing.T) {
	a, b := irisBrief(), irisBrief()
	if a != b {
		t.Fatalf("irisBrief() is not byte-stable across calls (len %d vs %d)", len(a), len(b))
	}
}

// TestEdgeIrisStatePreexistingObligationsSurvive: the pre-existing clauses
// the contract names as unchanged are still present.
func TestEdgeIrisStatePreexistingObligationsSurvive(t *testing.T) {
	brief := irisBrief()
	for _, want := range []string{"## Source", "kraken slack ack", "you never start", "carl", core.IrisModel} {
		if !strings.Contains(brief, want) {
			t.Errorf("irisBrief() lost pre-existing clause %q", want)
		}
	}
}
