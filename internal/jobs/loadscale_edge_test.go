package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadscale_edge_test.go — the ABSENCE half of the load-scale split, written
// blind against artifacts/50-contract.md.
//
// The contract splits one standard across two files: the brief carries the two
// disambiguation rules, the clear rule, the verbs and a POINTER, while the two
// 1-10 ladders live in .claude/skills/load/SKILL.md alone. Every assertion in
// this file is about a boundary of that split or of the ladders themselves —
// what must NOT be in the brief, what must NOT be a rung, and the ways a
// markdown table stops being machine-readable. Presence of the happy path is
// someone else's file.
//
// Nothing here reads an implementation file: the brief is reached through
// zeusBrief, and the skill is read as bytes off disk exactly as the colour-rule
// suite reads aim/info.

// edgeLadderOnlyCanaries are the three strings the contract names as the
// canary set (obligation 8 of the zeusBrief row): one from the top of the
// unblock ladder, one from its middle, one from the top of the scope ladder.
// They are ladder rows and nothing else, so their presence in the brief is
// positive evidence that the ladders were copied back in — the same shape of
// absence assertion as TestOnlyDreamNamesTheFeedbackWriteVerb, and for the same
// reason: two present-token tests cannot detect a duplication.
var edgeLadderOnlyCanaries = []string{
	"A day's thought",
	"Genuine review",
	"The largest coordinated effort",
}

// edgeUnblockAnchors and edgeScopeAnchors are the canonical labels, rung 1
// first, transcribed from the contract's "Canonical anchor labels" section.
// Held as Go literals rather than parsed from the skill so this file asks
// whether the skill says the right thing, not whether it agrees with itself.
var edgeUnblockAnchors = []string{
	"Mechanical",
	"Read one page, pick",
	"Answer a few concrete questions",
	"Local smoke, or a credential",
	"Live E2E drive",
	"Genuine review",
	"Decision batch needing new context",
	"Deep decision, real consequences",
	"Many deep decisions in one sitting",
	"A day's thought",
}

var edgeScopeAnchors = []string{
	"One-message close-out",
	"One small round",
	"One focused artifact or round",
	"A single-slice implementation",
	"A moderate slice",
	"A full /aim set draft",
	"A delegated build",
	"Full phase-2 of a substantial spec",
	"A multi-PR program",
	"The largest coordinated effort",
}

// edgeReadLoadSkill reads .claude/skills/load/SKILL.md out of the repo tree.
// The package's tests run with cwd at internal/jobs, so the repo root is two
// levels up. It FAILS rather than skips when the file is missing: the contract
// says absence of a source is a failure, and a suite that goes quiet because it
// could not find its subject is the exact failure mode these absence tests
// exist to rule out.
func edgeReadLoadSkill(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", ".claude", "skills", "load", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the load skill is the only home of the two ladders and the state map; "+
			"without it the brief's pointer leads nowhere", path, err)
	}
	return string(data)
}

// edgeTableRow is one parsed markdown table row: the source line it came from
// (1-based) plus its cells, already trimmed of the leading and trailing pipe.
type edgeTableRow struct {
	line  int
	cells []string
}

// edgeParseRows parses every line that looks like a markdown table row. A row
// is recognized by a leading pipe alone — deliberately loose, because a row
// that has LOST its trailing pipe (the hard-wrap failure below) must still be
// seen here in order to be reported.
func edgeParseRows(src string) []edgeTableRow {
	var rows []edgeTableRow
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		body := strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|")
		cells := strings.Split(body, "|")
		for j := range cells {
			cells[j] = strings.TrimSpace(cells[j])
		}
		rows = append(rows, edgeTableRow{line: i + 1, cells: cells})
	}
	return rows
}

// TestEdgeBriefCarriesNoLadderRow is the canary absence.
//
// The brief and the skill are one standard split across two files, and only the
// skill may carry the ladders — the brief is re-read on every turn and is under
// a hard byte ceiling (TestZeusBriefStaysLean), which is precisely why a
// "helpful" paste of the ten rungs into it is the likely regression. Both job
// variants are checked because Solo gates the crew paragraphs, so a canary
// added inside a non-solo-only block would otherwise be invisible on one path.
func TestEdgeBriefCarriesNoLadderRow(t *testing.T) {
	for _, j := range []*Job{
		{Slug: "edge-load-plain"},
		{Slug: "edge-load-solo", Solo: true},
	} {
		brief := zeusBrief(j)
		for _, canary := range edgeLadderOnlyCanaries {
			if strings.Contains(brief, canary) {
				t.Errorf("%s (solo=%v) brief contains the ladder-only string %q — the two 1-10 ladders live in "+
					".claude/skills/load/SKILL.md and the brief carries only the rules and the pointer; a brief "+
					"that restates a rung is a brief that will drift from the skill zeus actually reads",
					j.Slug, j.Solo, canary)
			}
		}
	}
}

// TestEdgeLoadSkillLaddersHaveExactlyTenRungs pins both ends of both ladders.
//
// The scale is 1-10 on both axes everywhere else in the system — the API
// refuses anything outside it (internal/api/jobload.go), so an eleventh rung in
// the skill would be a rung zeus could read, set, and have rejected, and a rung
// 0 would collide with the CLEAR (both numbers zero). Ten rows, no more and no
// fewer, on each axis, each rung stated exactly once.
func TestEdgeLoadSkillLaddersHaveExactlyTenRungs(t *testing.T) {
	src := edgeReadLoadSkill(t)
	rows := edgeParseRows(src)

	// Which ladder a row belongs to is decided by its LABEL, not by its
	// position in the file: the contract fixes the second cell, not the order
	// of the two tables, and keying on the label is what lets this test report
	// a missing rung as missing rather than as a count that came out short.
	unblockSeen := map[int]int{}
	scopeSeen := map[int]int{}
	unblockRows, scopeRows := 0, 0

	for _, row := range rows {
		if len(row.cells) < 2 {
			continue
		}
		n, ok := edgeAtoi(row.cells[0])
		if !ok {
			continue
		}
		label := row.cells[1]
		for rung, want := range edgeUnblockAnchors {
			if label == want {
				unblockRows++
				unblockSeen[rung+1]++
				if n != rung+1 {
					t.Errorf("load skill line %d: unblock label %q is on rung %d, canonically rung %d",
						row.line, label, n, rung+1)
				}
			}
		}
		for rung, want := range edgeScopeAnchors {
			if label == want {
				scopeRows++
				scopeSeen[rung+1]++
				if n != rung+1 {
					t.Errorf("load skill line %d: scope label %q is on rung %d, canonically rung %d",
						row.line, label, n, rung+1)
				}
			}
		}
	}

	if unblockRows != 10 {
		t.Errorf("load skill has %d unblock ladder rows, want exactly 10 — the axis is 1-10 and the API "+
			"refuses everything outside it", unblockRows)
	}
	if scopeRows != 10 {
		t.Errorf("load skill has %d scope ladder rows, want exactly 10", scopeRows)
	}
	for rung := 1; rung <= 10; rung++ {
		if unblockSeen[rung] != 1 {
			t.Errorf("unblock rung %d (%q) appears %d times as a table row, want exactly 1",
				rung, edgeUnblockAnchors[rung-1], unblockSeen[rung])
		}
		if scopeSeen[rung] != 1 {
			t.Errorf("scope rung %d (%q) appears %d times as a table row, want exactly 1",
				rung, edgeScopeAnchors[rung-1], scopeSeen[rung])
		}
	}
}

// TestEdgeLoadSkillHasNoRungZeroOrEleven is the out-of-range half, asserted on
// the FIRST CELL of every table row rather than on the ladders' labels — a rung
// that was invented has no canonical label to find it by, so the only way to
// catch it is to look at every numbered row in the file.
//
// Zero is the more dangerous of the two: the all-zero pair is the CLEAR, so a
// "rung 0" row would document a value whose meaning is the absence of an
// estimate as if it were an estimate.
func TestEdgeLoadSkillHasNoRungZeroOrEleven(t *testing.T) {
	src := edgeReadLoadSkill(t)
	for _, row := range edgeParseRows(src) {
		if len(row.cells) == 0 {
			continue
		}
		switch row.cells[0] {
		case "0":
			t.Errorf("load skill line %d is a rung-0 table row — 0 is not a rung; the all-zero pair is the "+
				"CLEAR, and documenting it as a rung invites an estimate that means \"no estimate\"", row.line)
		case "11":
			t.Errorf("load skill line %d is a rung-11 table row — the axis stops at 10 and the API refuses "+
				"anything above it, so an eleventh rung is a value zeus can read and never store", row.line)
		}
	}
}

// TestEdgeLoadSkillCarriesTheTwoRefusalRows pins the two state-map rows that
// are about NOT doing the obvious thing, which is why they are the two most
// likely to be dropped when the map is compressed.
//
// The clear row says an estimate must not exist at all while carl is not the
// blocker — the measured corpus' worst outlier was exactly this, an estimate on
// a working cell reading as a false blocked signal. The two-asks row says which
// of two simultaneous asks to price, and without it the answer is "whichever
// one the agent happened to be looking at", which is the measured u2-5 spread
// on one state.
func TestEdgeLoadSkillCarriesTheTwoRefusalRows(t *testing.T) {
	src := edgeReadLoadSkill(t)
	rows := edgeParseRows(src)

	clearRow := false
	for _, row := range rows {
		for _, cell := range row.cells {
			if strings.Contains(cell, "clear") {
				clearRow = true
			}
		}
	}
	if !clearRow {
		t.Error("load skill's state map has no row naming `clear` — the rule that an estimate exists ONLY " +
			"when carl is the blocker is the one that removes the corpus' largest outlier, and a map that " +
			"lists only the states that DO get a number never says which ones must not")
	}

	if !strings.Contains(src, "Price the EXPENSIVE ask") {
		t.Error("load skill is missing the two-asks rule (\"Price the EXPENSIVE ask\") — with two asks open " +
			"at once and no rule, agents priced whichever ask they were focused on, which is the measured " +
			"u2-5 spread on a single state")
	}
}

// TestEdgeLoadSkillTableRowsAreNotHardWrapped is the machine-readability
// boundary, and it is the one failure here with no visible symptom: a markdown
// table row broken across two source lines still RENDERS acceptably in a
// viewer, but the second line is no longer a row, so the cross-source pin that
// parses the ladders reads nine rungs and the tenth silently disappears.
//
// It is also the repo's standing prose rule (no hard-wrapped prose, one
// paragraph per line) applied where it is mechanically checkable.
func TestEdgeLoadSkillTableRowsAreNotHardWrapped(t *testing.T) {
	src := edgeReadLoadSkill(t)
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		if !strings.HasSuffix(line, "|") {
			t.Errorf("load skill line %d opens a table row but does not close it — the row is wrapped across "+
				"source lines, which leaves its tail as prose that no table parser will read: %q", i+1, line)
		}
	}
}

// edgeAtoi is a strict decimal parse: it accepts a run of digits and nothing
// else, so a header cell like "N", a separator's "---" or a range like "6-8" is
// a non-match rather than a number.
func edgeAtoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, true
}
