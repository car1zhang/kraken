package jobs

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// loadscale_contract_test.go — the STATED INVARIANTS of the standardized
// load-estimate scale, written blind from artifacts/50-contract.md before the
// implementation existed.
//
// Three obligations live here, and the third is the one nothing else can hold:
//
//   1. zeusBrief carries the two verbs, the two disambiguation rules, the clear
//      rule, the compact state map and the pointer at the load skill — and does
//      NOT carry the ladders (they live in the skill file, and a copy drifts).
//   2. .claude/skills/load/SKILL.md carries both 1-10 ladders with the canonical
//      anchor per rung, and the state map including the two-asks rule.
//   3. THE CROSS-SOURCE PIN: the skill file's ladders, format.ts's two anchor
//      arrays and Models.swift's two anchor arrays all spell the same twenty
//      labels, byte for byte. Three copies of one vocabulary is the whole
//      feature's failure mode — a board that says "Live E2E drive" where the
//      standard says something else is a board carl cannot read the ladder off.
//      Same shape as TestJobPaletteMatchesCSS/...MatchesSwift, and for the same
//      reason: nothing else can see across the three languages, and every way
//      they drift is silent.
//
// A missing file in the pin is a FAILURE, never a skip: the whole point is that
// all three exist and agree, so "I could not find it" is the loudest possible
// disagreement.

// contractUnblockLabels is the unblock ladder exactly as 50-contract.md § Canonical
// anchor labels states it, rung 1 first. Hardcoded rather than read from any of
// the three sources: this list IS the requirement, and a test that derived it
// from one source would only prove the other two copied that source.
var contractUnblockLabels = []string{
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

// contractScopeLabels is the scope ladder, same source and same rule.
var contractScopeLabels = []string{
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

// contractRepoRoot walks up from the test's cwd to the directory holding go.mod.
// The package's tests run with cwd at internal/jobs, so "../.." is the answer
// today — but the walk survives the package moving, and it fails loudly instead
// of reading a file that isn't there.
func contractRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s — cannot locate the repo root, so the cross-source pin "+
				"cannot read the three files it exists to compare", dir)
		}
		dir = parent
	}
}

// contractReadRepoFile reads a repo-root-relative file, failing rather than
// skipping when it is absent. See the file comment: absence is the loudest
// disagreement, not a reason to pass.
func contractReadRepoFile(t *testing.T, rel ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{contractRepoRoot(t)}, rel...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the standardized load scale requires this file to exist; a missing "+
			"source is a failure of the cross-source pin, never a reason to skip it", path, err)
	}
	return string(data)
}

const contractLoadSkillRel = ".claude/skills/load/SKILL.md"

// contractRung is one parsed ladder row: the rung number and its anchor label.
type contractRung struct {
	n     int
	label string
}

// contractLadderRows pulls every markdown table row whose FIRST cell is an
// integer in 1..10, in source order, returning (n, second cell). The state-map
// table's first cell is a block state rather than a number, and a separator row's
// is `---`, so both fall out without needing to know the file's headings — which
// the contract deliberately does not fix.
func contractLadderRows(src string) []contractRung {
	var out []contractRung
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 2 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(cells[0]))
		if err != nil || n < 1 || n > 10 {
			continue
		}
		out = append(out, contractRung{n: n, label: strings.TrimSpace(cells[1])})
	}
	return out
}

// contractQuoted matches a plain double-quoted string literal with no escapes —
// which is what both the TS and the Swift anchor tables are obliged to use, and
// what every canonical label can be written as (none contains a double quote or
// a backslash).
var contractQuoted = regexp.MustCompile(`"([^"\\]*)"`)

// contractBracketStrings finds `anchor` in src, then the first `[` AFTER the
// declaration's `=`, then reads to its matching `]` and returns every
// double-quoted string inside, in order. One helper for both languages because a
// TS array literal and a Swift one are the same bytes at this resolution.
//
// The `=` matters: both declarations carry a TYPE between the name and the
// value, and both types contain brackets — `readonly string[]` in TypeScript,
// `[String]` in Swift. Scanning from the name would parse the annotation, find
// no strings in it, and report an empty ladder against a file that is perfectly
// correct.
func contractBracketStrings(t *testing.T, src, anchor, where string) []string {
	t.Helper()
	i := strings.Index(src, anchor)
	if i < 0 {
		t.Fatalf("%s declares no %s — the cross-source pin has nothing to compare", where, anchor)
	}
	eq := strings.Index(src[i:], "=")
	if eq < 0 {
		t.Fatalf("%s: %s is never assigned a value", where, anchor)
	}
	open := strings.Index(src[i+eq:], "[")
	if open < 0 {
		t.Fatalf("%s: no array literal after %s", where, anchor)
	}
	open += i + eq
	depth := 0
	end := -1
	for j := open; j < len(src); j++ {
		switch src[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				end = j
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		t.Fatalf("%s: unterminated array literal after %s", where, anchor)
	}
	var out []string
	for _, m := range contractQuoted.FindAllStringSubmatch(src[open:end], -1) {
		out = append(out, m[1])
	}
	return out
}

// contractEqualLabels compares one source's twenty (or ten) labels against the
// canonical list, reporting the first disagreement per index rather than a wall
// of diff — the labels are long and one wrong word is the realistic failure.
func contractEqualLabels(t *testing.T, got, want []string, source, axis string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s %s ladder has %d labels, want %d — every source carries all ten rungs or the "+
			"ladder is unusable at the rungs it drops", source, axis, len(got), len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s %s rung %d = %q, contract says %q — the three sources must agree byte for byte",
				source, axis, i+1, got[i], want[i])
		}
	}
}

// TestContractZeusBriefLoadScale pins the brief half of the split: the rules and
// the pointer ride in the brief (read on every turn), the ladders do not.
//
// Solo is checked alongside plain for the reason TestZeusBriefLoadEstimate
// already gives — the estimate is about what CARL spends to unblock the cell,
// which has nothing to do with whether the cell delegates.
func TestContractZeusBriefLoadScale(t *testing.T) {
	t.Setenv("KRAKEN_OPERATOR", "Ada")
	for _, j := range []*Job{
		{Slug: "scale-plain"},
		{Slug: "scale-solo", Solo: true},
	} {
		brief := zeusBrief(j)

		// (1) both verbs, with the slug already substituted — the brief holds the
		// value, so it spells the real slug where a skill would leave <slug>.
		for _, want := range []string{
			"kraken load set " + j.Slug,
			"kraken load clear " + j.Slug,
			"--unblock",
			"--scope",
		} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s brief missing %q — the verb is unusable without it", j.Slug, want)
			}
		}

		// (2) the two pins the existing suite already holds, kept so this change
		// cannot quietly retire the 1-10 ruler while rewriting the paragraph.
		for _, want := range []string{
			"1-10 is the effort Ada spends",
			"a one-message reply",
		} {
			if !strings.Contains(brief, want) {
				t.Errorf("%s brief dropped the kept pin %q", j.Slug, want)
			}
		}

		// (3)-(5) the three rules. Each is anchored on its shortest distinctive
		// fragment: what these protect is the CLAIM, and a whole sentence is a
		// build break waiting on a harmless reword.
		for _, want := range []struct{ token, why string }{
			{"ACTUAL move", "rule 1: unblock prices what carl will really do"},
			{"ships PRs on trust", "rule 1's evidence — why a review gate is a 1 and not a 6"},
			{"the reply BUYS", "rule 2: scope is strictly the work still ahead"},
			{"never the work under review", "rule 2's other half — the feature behind the gate is not the scope"},
			{"only when Ada is the blocker", "the clear rule, with teeth"},
			// Contract amendments after review round 1, appended by the lead —
			// the blind files were authored against the pre-amendment text, so
			// obligations 6-legend and 7b would otherwise be pinned by nothing.
			{"unblock/scope", "obligation 6's legend: which side of a state-map slash is which — read backwards, 4/1 becomes a 16x ranking error"},
			{"estimate honestly", "obligation 7b: the anti-inflation rule the rewrite briefly dropped"},
		} {
			if !strings.Contains(brief, want.token) {
				t.Errorf("%s brief missing %q (%s)", j.Slug, want.token, want.why)
			}
		}

		// (6) the compact state map. The PAIRS are what make it a lookup rather
		// than a re-derivation, and re-deriving per cell is the entire measured
		// defect (53-measured-baseline.md).
		for _, pair := range []struct{ token, state string }{
			{"1/1", "review-only gate, and PR awaiting checks"},
			{"4/1", "manual-test gate, local install and drive"},
			{"5/2", "manual-test gate, live E2E environment"},
			{"2/3", "/info round, go-deeper pick"},
			{"3/6", "/aim spec gate, P0s blocking /do"},
		} {
			if !strings.Contains(brief, pair.token) {
				t.Errorf("%s brief state map missing the pair %q (%s)", j.Slug, pair.token, pair.state)
			}
		}
		low := strings.ToLower(brief)
		for _, state := range []string{"review-only", "install", "live e2e", "spec gate"} {
			if !strings.Contains(low, state) {
				t.Errorf("%s brief state map never names the %q state — a pair with no state beside it "+
					"is a number zeus cannot look anything up in", j.Slug, state)
			}
		}

		// (7) the pointer. Without it the ladders are unreachable: zeus reads
		// skills BY PATH, since a personal skill of the same name shadows one.
		if !strings.Contains(brief, contractLoadSkillRel) {
			t.Errorf("%s brief does not point at %s — the ladders live there and nothing else names them",
				j.Slug, contractLoadSkillRel)
		}

		// (8) the ladders themselves must NOT be here. Three canaries, one from
		// the top of each ladder and one mid-unblock: this is the absence half of
		// the split, the same pattern TestZeusBriefDoesNotRestateSkills applies to
		// every other procedure, and without it the ceiling is the only thing
		// standing between the brief and a re-inlined twenty-row table.
		for _, canary := range []string{
			"A day's thought",
			"Genuine review",
			"The largest coordinated effort",
		} {
			if strings.Contains(brief, canary) {
				t.Errorf("%s brief restates the ladder row %q, which %s owns — the brief carries the "+
					"rules and the pointer, never the twenty rungs (a copy drifts, and whichever one "+
					"zeus reads first is the one it obeys)", j.Slug, canary, contractLoadSkillRel)
			}
		}
	}
}

// TestContractLoadSkillFile pins the skill file's own obligations: it is
// frontmatter-addressable as `load`, it carries both ladders row by row, and it
// carries the state map — including the two-asks rule, which is the one state
// the measured corpus showed agents resolving by whichever ask they happened to
// be looking at.
func TestContractLoadSkillFile(t *testing.T) {
	src := contractReadRepoFile(t, ".claude", "skills", "load", "SKILL.md")

	// (1) frontmatter. The name is how the skill is addressable at all; a file
	// at the right path with the wrong name is a skill nothing can invoke.
	if !strings.HasPrefix(src, "---") {
		t.Errorf("%s does not open with YAML frontmatter", contractLoadSkillRel)
	}
	head := src
	if end := strings.Index(src[3:], "\n---"); end >= 0 {
		head = src[:end+3]
	}
	if !regexp.MustCompile(`(?m)^name:\s*load\s*$`).MatchString(head) {
		t.Errorf("%s frontmatter does not declare `name: load`", contractLoadSkillRel)
	}

	// (2)+(3) a row per rung on both ladders, the canonical label in the second
	// cell. Asserted per rung rather than by parsing the whole table, so a
	// failure names the rung that is wrong.
	for _, ladder := range []struct {
		axis   string
		labels []string
	}{
		{"unblock", contractUnblockLabels},
		{"scope", contractScopeLabels},
	} {
		for i, label := range ladder.labels {
			row := regexp.MustCompile(`\|\s*` + strconv.Itoa(i+1) + `\s*\|\s*` + regexp.QuoteMeta(label) + `\s*\|`)
			if !row.MatchString(src) {
				t.Errorf("%s has no %s ladder row `| %d | %s |` — a rung with no anchor is a rung two "+
					"cells will price differently", contractLoadSkillRel, ladder.axis, i+1, label)
			}
		}
	}

	// (4) the state map. Every recurring state of 54-anchor-scale.md § State map
	// is named, so the value is looked up rather than judged. Matched
	// case-insensitively on the shortest stable fragment of each row label —
	// the wording is the implementer's, the STATE is the contract's.
	low := strings.ToLower(src)
	for _, state := range []struct{ token, why string }{
		{"kraken load clear", "working / teammates running / nothing needed yet"},
		{"pr awaiting checks", "the merge-only state"},
		{"review-only", "manual-test gate carl ships on trust"},
		{"install", "manual-test gate, local install and drive"},
		{"live e2e", "the one gate that genuinely costs him"},
		{"/info", "the go-deeper pick"},
		{"spec gate", "the one state where scope is legitimately big"},
		{"credential", "a 4 regardless of what it unblocks"},
		{"mid-build", "recommendation attached is the difference between 3 and 7"},
		{"two asks", "the state agents priced by whichever ask they focused on"},
	} {
		if !strings.Contains(low, state.token) {
			t.Errorf("%s state map never names %q (%s) — an unlisted state falls back to the ladders, "+
				"which is the per-cell re-derivation this standard exists to remove",
				contractLoadSkillRel, state.token, state.why)
		}
	}
	// the two-asks RULE, byte-exact: the contract names this phrase specifically,
	// because naming the state without the rule leaves it undecided.
	if !strings.Contains(src, "Price the EXPENSIVE ask") {
		t.Errorf("%s does not state the two-asks rule (`Price the EXPENSIVE ask`)", contractLoadSkillRel)
	}
}

// TestContractAnchorLabelsAgreeAcrossSources is the cross-source pin: one
// vocabulary, three languages, and nothing but this test able to see all three.
//
// The board renders the anchor beside the number so carl reads meaning without
// memorizing the ladder. That promise is only kept while the word the board
// prints is the word the standard defines — so a drift here does not break a
// build, it silently prints the wrong meaning, which is exactly the failure mode
// the palette tests exist for.
func TestContractAnchorLabelsAgreeAcrossSources(t *testing.T) {
	// (a) the skill file, parsed as ladder rows.
	rows := contractLadderRows(contractReadRepoFile(t, ".claude", "skills", "load", "SKILL.md"))
	if len(rows) != 20 {
		t.Fatalf("%s yields %d numbered ladder rows, want exactly 20 (ten unblock then ten scope)",
			contractLoadSkillRel, len(rows))
	}
	var skillUnblock, skillScope []string
	for i, r := range rows {
		wantN := i%10 + 1
		if r.n != wantN {
			t.Fatalf("%s numbered row %d is rung %d, want %d — the two ladders must each run 1..10 in order",
				contractLoadSkillRel, i+1, r.n, wantN)
		}
		if i < 10 {
			skillUnblock = append(skillUnblock, r.label)
		} else {
			skillScope = append(skillScope, r.label)
		}
	}
	contractEqualLabels(t, skillUnblock, contractUnblockLabels, contractLoadSkillRel, "unblock")
	contractEqualLabels(t, skillScope, contractScopeLabels, contractLoadSkillRel, "scope")

	// (b) the TypeScript tables.
	ts := contractReadRepoFile(t, "web", "_ui", "src", "lib", "format.ts")
	contractEqualLabels(t, contractBracketStrings(t, ts, "UNBLOCK_ANCHORS", "format.ts"),
		contractUnblockLabels, "format.ts", "unblock")
	contractEqualLabels(t, contractBracketStrings(t, ts, "SCOPE_ANCHORS", "format.ts"),
		contractScopeLabels, "format.ts", "scope")

	// (c) the Swift tables, scoped to LoadAnchors so an unrelated `unblock`
	// elsewhere in Models.swift cannot be mistaken for the table.
	swift := contractReadRepoFile(t, "ios", "Kraken", "Models.swift")
	i := strings.Index(swift, "LoadAnchors")
	if i < 0 {
		t.Fatal("ios/Kraken/Models.swift declares no LoadAnchors — the iOS drilldown has no vocabulary to render")
	}
	swift = swift[i:]
	contractEqualLabels(t, contractBracketStrings(t, swift, "unblock", "Models.swift LoadAnchors"),
		contractUnblockLabels, "Models.swift", "unblock")
	contractEqualLabels(t, contractBracketStrings(t, swift, "scope", "Models.swift LoadAnchors"),
		contractScopeLabels, "Models.swift", "scope")
}
