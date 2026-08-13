package kraken

// skillsrefs_contract_test.go — blind CONTRACT pins for Slice K of the
// clickable-code-refs feature (50-contract.md, "Slice K frozen literals").
//
// The contract's own words: "the table is the frozen surface". So this file
// asserts that table and nothing else — no prose shape, no heading, no
// ordering. Those belong to the linter's fixture manifest and to the existing
// skills suite (TestSkillsOwnTheCellContract, TestContractSectionsReferencedBySkills),
// which the implementer keeps green independently.
//
// It reads the embedded FS the way skills_embed_test.go does: skillsFS lives in
// package main, and reading it here is what makes a rename or a dropped
// `//go:embed` path fail at test time rather than at click time inside a cell.
//
// Every package-level identifier is prefixed `contractRefs` so the edge author
// sharing this package cannot collide with it.

import (
	"io/fs"
	"strings"
	"testing"
)

// contractRefsPin is one row of the frozen-literals table: a file inside the
// embedded skills tree, and every literal substring it must contain.
type contractRefsPin struct {
	path     string
	literals []string
	why      string
}

// contractRefsTable transcribes the contract's table verbatim. The `why` is
// what a failure prints — the point is that whoever breaks a pin learns which
// behaviour they just retired, not merely that a string moved.
var contractRefsTable = []contractRefsPin{
	{
		path: ".claude/skills/aim/assets/hermes-contract.md",
		literals: []string{
			"data-code",
			`data-code="path:line"`,
		},
		why: "hermes learns the one sanctioned metadata attribute, and its canonical example, from this file alone",
	},
	{
		path:     ".claude/skills/aim/SKILL.md",
		literals: []string{"box → kind → code ref"},
		why:      "zeus's delegation rule extends the pair to a triple; without this string briefs carry no refs and every diagram ships inert",
	},
	{
		path:     ".claude/skills/info/SKILL.md",
		literals: []string{"box → kind → code ref"},
		why:      "info carries its own copy of the same rule — the two files deliberately duplicate the prose",
	},
	{
		path:     ".claude/skills/do/SKILL.md",
		literals: []string{"data-code"},
		why:      "the hephaestus link-maintenance rule: the implementer that moves cited code updates the citation, attribute values included",
	},
	{
		path: ".claude/skills/aim/assets/lint_svg.py",
		literals: []string{
			"CODE_REF_FORMAT",
			"CODE_REF_MISSING",
			"CODE_REF_BASENAME",
			"--code-refs",
		},
		why: "the three finding codes and the opt-in flag are the linter's whole code-ref surface",
	},
	{
		path: ".claude/skills/aim/assets/lint-fixtures/manifest.json",
		literals: []string{
			"pass-code-refs.svg",
			"fail-code-ref-format.svg",
		},
		why: "both new fixtures must be in the manifest or --selftest never exercises them",
	},
}

func contractRefsRead(t *testing.T, path string) string {
	t.Helper()
	data, err := fs.ReadFile(skillsFS, path)
	if err != nil {
		t.Fatalf("read embedded %s: %v — the skills slice's file is missing from the binary", path, err)
	}
	return string(data)
}

// TestContractSkillsCarryCodeRefLiterals is the whole of the ★ pinning the
// contract asks for: each listed file contains each listed literal substring.
func TestContractSkillsCarryCodeRefLiterals(t *testing.T) {
	for _, pin := range contractRefsTable {
		t.Run(pin.path, func(t *testing.T) {
			body := contractRefsRead(t, pin.path)
			for _, lit := range pin.literals {
				if !strings.Contains(body, lit) {
					t.Errorf("%s does not contain %q — %s", pin.path, lit, pin.why)
				}
			}
		})
	}
}
