package kraken

import (
	"strings"
	"testing"
)

// skillsrefs_edge_test.go — the EDGE dimension of slice K's ★ rows
// (artifacts/50-contract.md). Slice K is CONTENT, so its edge is negative
// space: the two places where the contract says an edit AMENDS an existing
// sentence rather than replacing it, and where the amendment is one plausible
// keystroke away from deleting the thing it was supposed to extend.
//
// Only those two are pinned here. Everything else about this slice — the new
// permissions, the new rule copies, the linter's three finding codes — is
// PRESENCE, which the sibling blind file and the linter's own fixture manifest
// own; a negative-space test invented past what the contract states would be a
// guess wearing an assertion's clothes.
//
// The reads go through readEmbedded (skills_embed_test.go), the same embedded
// FS every other skill pin in this package reads: what ships inside the binary
// is what materializes into a cell, and a working-tree file that never made
// the embed is exactly the failure this whole family of tests exists for.

// TestEdgeContractStillForbidsInventedAttributes is the carve-out's other
// half. The contract's "never invent … attributes" clause is what keeps hermes
// from decorating fragments with markup nobody asked for, and this change
// EDITS that very sentence to admit `data-code`. The plausible wrong edit is
// not a typo — it is deleting the clause outright, because the sentence now
// "allows" something and reads as if it contradicts itself. That deletion
// leaves every ★ presence pin green (data-code is still named, the new
// subsection is still there) while the guardrail the carve-out was carved out
// OF is gone, and the symptom appears months later as fragments carrying
// invented attributes no linter rule covers.
func TestEdgeContractStillForbidsInventedAttributes(t *testing.T) {
	contract := readEmbedded(t, "aim/assets/hermes-contract.md")
	if !strings.Contains(contract, "never invent") {
		t.Error("aim/assets/hermes-contract.md no longer contains \"never invent\" — the data-code " +
			"permission AMENDS that clause, it does not replace it. With the clause deleted, hermes " +
			"has permission to add data-code and no rule against adding anything else")
	}
}

// TestEdgeAimStillPinsTheBoxKindPair guards the same shape one file over.
// aim/SKILL.md's delegation rule extends `box → kind` to `box → kind → code
// ref`, and the literal `box → kind` is pinned by TestSkillsOwnTheCellContract
// — so the extension must be written as an EXTENSION, with the old pair
// surviving as a prefix of the new triple. An edit that respells the rule
// (`box → kind and code ref`, `box/kind/code-ref`) reads identically to a
// human and takes the older test down with it, which is a red suite blaming
// the wrong file. Asserting it here, beside the new rule, is what says the
// two claims are one claim.
func TestEdgeAimStillPinsTheBoxKindPair(t *testing.T) {
	aim := readEmbedded(t, "aim/SKILL.md")
	if !strings.Contains(aim, "box → kind") {
		t.Error("aim/SKILL.md no longer contains the literal \"box → kind\" — the code-ref rule " +
			"EXTENDS that pair into a triple, so the pair has to survive inside it verbatim " +
			"(TestSkillsOwnTheCellContract pins the same substring)")
	}
}
