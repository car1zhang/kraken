package jobs

import (
	"strings"
	"testing"

	"kraken/internal/core"
)

// irisstate_contract_test.go — blind LOGIC tests for 50-contract.md
// "slice 2: iris brief, state and diagnosis". Every assertion goes through
// irisBrief()'s return value; iris.go's source is never read as text.

// contractIrisStateRequired are the five substrings the contract freezes, in
// the order it requires them to appear: (a) the heading, (b) the blocked
// definition, (c) the board read, (d) the report-not-diagnose rule, (e) the
// no-invented-remedy rule.
var contractIrisStateRequired = []string{
	"## What you know about a job, and what you never add",
	"`blocked` means the cell is waiting for carl at a gate",
	"kraken board get <slug>",
	"You report; you never diagnose.",
	"never name a knob, a threshold or a remedy you have not read in the code",
}

// contractIrisStateBefore is the anchor the whole block must sit AFTER (the
// end of the memory section's second paragraph).
const contractIrisStateBefore = "an escalation into a channel with no reader."

// contractIrisStateAfter is the anchor the whole block must sit BEFORE.
const contractIrisStateAfter = "## Credentials in messages"

// TestContractIrisStateEachSubstringExactlyOnce: each of (a)-(e) is present
// EXACTLY once — absent fails, and a duplicate (e.g. the heading pasted
// twice) fails too.
func TestContractIrisStateEachSubstringExactlyOnce(t *testing.T) {
	brief := irisBrief()
	for _, want := range contractIrisStateRequired {
		if n := strings.Count(brief, want); n != 1 {
			t.Errorf("irisBrief() contains %q %d times, want exactly 1", want, n)
		}
	}
}

// TestContractIrisStateOrdering: (a) before (b) before (c) before (d)
// before (e).
func TestContractIrisStateOrdering(t *testing.T) {
	brief := irisBrief()
	prev := -1
	prevWant := ""
	for _, want := range contractIrisStateRequired {
		i := strings.Index(brief, want)
		if i < 0 {
			t.Fatalf("irisBrief() is missing %q", want)
		}
		if i <= prev {
			t.Errorf("%q (at %d) does not come after %q (at %d)", want, i, prevWant, prev)
		}
		prev, prevWant = i, want
	}
}

// TestContractIrisStateBlockBetweenAnchors: the whole block — heading
// through the last required substring — sits after the memory section's
// closing sentence and before the credentials heading.
func TestContractIrisStateBlockBetweenAnchors(t *testing.T) {
	brief := irisBrief()
	for _, anchor := range []string{contractIrisStateBefore, contractIrisStateAfter} {
		if n := strings.Count(brief, anchor); n != 1 {
			t.Fatalf("anchor %q appears %d times in irisBrief(), want exactly 1 (the placement is only well-defined against a unique anchor)", anchor, n)
		}
	}
	lo := strings.Index(brief, contractIrisStateBefore) + len(contractIrisStateBefore)
	hi := strings.Index(brief, contractIrisStateAfter)
	if lo > hi {
		t.Fatalf("anchor %q ends (at %d) after %q starts (at %d)", contractIrisStateBefore, lo, contractIrisStateAfter, hi)
	}
	for _, want := range contractIrisStateRequired {
		i := strings.Index(brief, want)
		if i < 0 {
			t.Errorf("irisBrief() is missing %q", want)
			continue
		}
		if i < lo {
			t.Errorf("%q (at %d) appears before the end of %q (at %d)", want, i, contractIrisStateBefore, lo)
		}
		if i+len(want) > hi {
			t.Errorf("%q (at %d) does not end before %q (at %d)", want, i, contractIrisStateAfter, hi)
		}
	}
}

// TestContractIrisStatePreexistingObligationsHold: the slice changes nothing
// else — the three rules (never start, ack, Source provenance), the resolved
// IrisModel, the name carl, the 16000-byte ceiling, and no `queued`.
func TestContractIrisStatePreexistingObligationsHold(t *testing.T) {
	brief := irisBrief()
	for _, want := range []string{
		"you never start",
		"kraken slack ack",
		"## Source",
		core.IrisModel,
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("irisBrief() no longer contains %q", want)
		}
	}
	if !strings.Contains(strings.ToLower(brief), "carl") {
		t.Error("irisBrief() never names carl")
	}
	if n := len(brief); n > 16000 {
		t.Errorf("len(irisBrief()) = %d, want <= 16000", n)
	}
	if strings.Contains(brief, "queued") {
		t.Error("irisBrief() contains the retired word \"queued\"")
	}
}
