package jobs

// Edge tests for contract slice 2, "kronos relay rule": the new paragraph in
// kronosBrief() — its placement under `## The loop` after the sentence ending
// `tell carl, ack it.`, and the unchanged 16000-byte size cap.

import (
	"strings"
	"testing"
)

var edgeKronosRelayLiterals = []string{
	"A row says what happened and nothing downstream of it",
	"`pr-merged` means one PR merged",
	"my inference:",
	"Never put an inference into an ack disposition",
	"fresher than the row",
}

// edgeKronosLoopSection returns the byte range [start, end) of the `## The
// loop` section: from its heading to the next `\n## ` heading (or EOF).
func edgeKronosLoopSection(t *testing.T, brief string) (int, int) {
	t.Helper()
	start := strings.Index(brief, "\n## The loop\n")
	if start < 0 {
		t.Fatalf("kronosBrief() has no `## The loop` heading")
	}
	start++ // point at the '#'
	end := len(brief)
	if next := strings.Index(brief[start+len("## The loop"):], "\n## "); next >= 0 {
		end = start + len("## The loop") + next
	}
	return start, end
}

// TestEdgeKronosRelayRuleLivesInTheLoopAfterAck: every relay literal sits
// inside `## The loop`, after the `tell carl, ack it.` sentence — not in a
// later section, not before the per-row instruction it qualifies.
func TestEdgeKronosRelayRuleLivesInTheLoopAfterAck(t *testing.T) {
	edgeKronosEnv(t)
	brief := kronosBrief()
	start, end := edgeKronosLoopSection(t, brief)
	ack := strings.Index(brief, "tell carl, ack it.")
	if ack < 0 {
		t.Fatalf("kronosBrief() lost the anchor sentence ending `tell carl, ack it.`")
	}
	if ack < start || ack >= end {
		t.Fatalf("anchor `tell carl, ack it.` at %d is outside `## The loop` [%d,%d)", ack, start, end)
	}
	for _, lit := range edgeKronosRelayLiterals {
		i := strings.Index(brief, lit)
		if i < 0 {
			t.Errorf("kronosBrief() missing relay literal %q", lit)
			continue
		}
		if i <= ack {
			t.Errorf("relay literal %q at %d precedes the `tell carl, ack it.` anchor at %d", lit, i, ack)
		}
		if i >= end {
			t.Errorf("relay literal %q at %d falls after `## The loop` ends at %d", lit, i, end)
		}
	}
}

// TestEdgeKronosRelayRuleOrderWithinParagraph: the paragraph's first
// statement — what a row says — precedes the rest of the rule, so the
// inference prohibition is read as qualifying it.
func TestEdgeKronosRelayRuleOrderWithinParagraph(t *testing.T) {
	edgeKronosEnv(t)
	brief := kronosBrief()
	row := strings.Index(brief, "A row says what happened and nothing downstream of it")
	never := strings.Index(brief, "Never put an inference into an ack disposition")
	if row < 0 || never < 0 {
		t.Fatalf("relay literals missing (row=%d never=%d)", row, never)
	}
	if never < row {
		t.Errorf("`Never put an inference…` (%d) precedes `A row says what happened…` (%d)", never, row)
	}
}

// TestEdgeKronosRelayRuleIsCaseExact: the literals are verbatim — in
// particular the `pr-merged` kind is back-quoted and the inference marker is
// lower case with a colon, the exact prefix kronos is told to write.
func TestEdgeKronosRelayRuleIsCaseExact(t *testing.T) {
	edgeKronosEnv(t)
	brief := kronosBrief()
	for _, lit := range []string{"`pr-merged` means one PR merged", "my inference:"} {
		if !strings.Contains(brief, lit) {
			t.Errorf("kronosBrief() lacks case-exact %q", lit)
		}
	}
}

// TestEdgeKronosRelayBriefUnderSizeCap: the added paragraph must not push the
// brief past the 16000-byte cap (tmux's ~16KB command ceiling is the reason
// every staged brief is capped).
func TestEdgeKronosRelayBriefUnderSizeCap(t *testing.T) {
	edgeKronosEnv(t)
	if n := len(kronosBrief()); n > 16000 {
		t.Errorf("len(kronosBrief()) = %d, want <= 16000", n)
	}
}

// TestEdgeKronosRelayBriefDeterministic: two calls in one sandbox give the
// same text, so the placement assertions above are not order-of-call luck.
func TestEdgeKronosRelayBriefDeterministic(t *testing.T) {
	edgeKronosEnv(t)
	if a, b := kronosBrief(), kronosBrief(); a != b {
		t.Errorf("kronosBrief() differs between calls (len %d vs %d)", len(a), len(b))
	}
}
