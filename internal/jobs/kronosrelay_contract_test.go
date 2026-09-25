package jobs

// Contract slice 2 (kronos relay rule): pins the new paragraph in
// kronosBrief() under `## The loop` — a row says only what happened, an
// inference is labelled and never becomes an ack disposition — and that the
// brief's size cap still holds with it in.

import (
	"strings"
	"testing"
)

func TestContractKronosRelayRuleLiterals(t *testing.T) {
	useTestStore(t)
	brief := kronosBrief()
	for _, want := range []string{
		"A row says what happened and nothing downstream of it",
		"`pr-merged` means one PR merged",
		"my inference:",
		"Never put an inference into an ack disposition",
		"fresher than the row",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("kronosBrief() lacks %q", want)
		}
	}
}

// The paragraph sits under `## The loop`, after the sentence ending
// `tell carl, ack it.` — so it must fall between that heading and the next one.
func TestContractKronosRelayRuleUnderTheLoop(t *testing.T) {
	useTestStore(t)
	brief := kronosBrief()
	loop := strings.Index(brief, "\n## The loop\n")
	if loop < 0 {
		t.Fatal("kronosBrief() has no `## The loop` heading")
	}
	rest := brief[loop+1:]
	next := strings.Index(rest[len("## The loop\n"):], "\n## ")
	section := rest
	if next >= 0 {
		section = rest[:len("## The loop\n")+next]
	}
	anchor := strings.Index(section, "tell carl, ack it.")
	rule := strings.Index(section, "A row says what happened and nothing downstream of it")
	if anchor < 0 {
		t.Fatal("`## The loop` section no longer contains `tell carl, ack it.`")
	}
	if rule < 0 {
		t.Fatal("relay rule is not inside the `## The loop` section")
	}
	if rule < anchor {
		t.Errorf("relay rule (offset %d) precedes `tell carl, ack it.` (offset %d)", rule, anchor)
	}
}

func TestContractKronosRelayRuleSizeCap(t *testing.T) {
	useTestStore(t)
	if n := len(kronosBrief()); n > 16000 {
		t.Errorf("kronosBrief() is %d bytes, cap is 16000", n)
	}
}
