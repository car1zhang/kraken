package jobs

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// loadscale_test.go — the ruler's shape, and the cross-source pin.
//
// The two ladders live in Go (loadscale.go) because the judge reads them
// there; the web board (format.ts) and the iOS app (Models.swift) carry the
// same twenty labels for their drilldowns. Nothing but a test can see across
// the three languages, and every way they drift is silent: a board that says
// "Live E2E drive" where the ruler means something else prints the wrong
// meaning beside a real-looking digit. Same shape as
// TestJobPaletteMatchesCSS/...MatchesSwift, for the same reason. A missing
// file is a FAILURE, never a skip.

// loadScaleRepoRoot walks up from the test's cwd to the directory holding
// go.mod; the walk survives the package moving and fails loudly rather than
// reading a file that is not there.
func loadScaleRepoRoot(t *testing.T) string {
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
			t.Fatalf("no go.mod above %s — cannot locate the repo root", dir)
		}
		dir = parent
	}
}

func loadScaleReadRepoFile(t *testing.T, rel ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{loadScaleRepoRoot(t)}, rel...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the cross-source pin requires this file; a missing source is a failure, never a skip", path, err)
	}
	return string(data)
}

// loadScaleQuoted matches a plain double-quoted string literal with no
// escapes — what both the TS and the Swift tables use, and what every label
// can be written as (none contains a double quote or a backslash).
var loadScaleQuoted = regexp.MustCompile(`"([^"\\]*)"`)

// loadScaleBracketStrings finds `anchor` in src, then the first `[` AFTER the
// declaration's `=`, reads to its matching `]` and returns every quoted
// string inside, in order. The `=` matters: both declarations carry a TYPE
// between the name and the value, and both types contain brackets
// (`readonly string[]`, `[String]`).
func loadScaleBracketStrings(t *testing.T, src, anchor, where string) []string {
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
	depth, end := 0, -1
	for j := open; j < len(src) && end < 0; j++ {
		switch src[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				end = j
			}
		}
	}
	if end < 0 {
		t.Fatalf("%s: unterminated array literal after %s", where, anchor)
	}
	var out []string
	for _, m := range loadScaleQuoted.FindAllStringSubmatch(src[open:end], -1) {
		out = append(out, m[1])
	}
	return out
}

func ladderLabels(l [10]LoadRung) []string {
	out := make([]string, len(l))
	for i, r := range l {
		out[i] = r.Label
	}
	return out
}

func loadScaleEqualLabels(t *testing.T, got, want []string, source, axis string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s %s ladder has %d labels, want %d", source, axis, len(got), len(want))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s %s rung %d = %q, the ruler says %q — the three sources must agree byte for byte", source, axis, i+1, got[i], want[i])
		}
	}
}

// TestLoadLaddersAreWellFormed pins the ruler's own shape: ten rungs per
// axis, every label and recognizer non-empty, labels distinct within an
// axis (the note names a rung by label, so two rungs sharing one would read
// as one estimate).
func TestLoadLaddersAreWellFormed(t *testing.T) {
	for _, axis := range []struct {
		name   string
		ladder [10]LoadRung
	}{{"unblock", UnblockLadder}, {"scope", ScopeLadder}} {
		seen := map[string]bool{}
		for i, r := range axis.ladder {
			if strings.TrimSpace(r.Label) == "" || strings.TrimSpace(r.Recognize) == "" {
				t.Errorf("%s rung %d has an empty label or recognizer", axis.name, i+1)
			}
			if seen[r.Label] {
				t.Errorf("%s label %q appears twice", axis.name, r.Label)
			}
			seen[r.Label] = true
		}
	}
}

// TestLoadAnchorLabelsAgreeAcrossSources is the cross-source pin: one
// vocabulary, three languages.
func TestLoadAnchorLabelsAgreeAcrossSources(t *testing.T) {
	wantUnblock, wantScope := ladderLabels(UnblockLadder), ladderLabels(ScopeLadder)

	ts := loadScaleReadRepoFile(t, "web", "_ui", "src", "lib", "format.ts")
	loadScaleEqualLabels(t, loadScaleBracketStrings(t, ts, "UNBLOCK_ANCHORS", "format.ts"), wantUnblock, "format.ts", "unblock")
	loadScaleEqualLabels(t, loadScaleBracketStrings(t, ts, "SCOPE_ANCHORS", "format.ts"), wantScope, "format.ts", "scope")

	swift := loadScaleReadRepoFile(t, "ios", "Kraken", "Models.swift")
	i := strings.Index(swift, "LoadAnchors")
	if i < 0 {
		t.Fatal("ios/Kraken/Models.swift declares no LoadAnchors — the iOS drilldown has no vocabulary to render")
	}
	swift = swift[i:]
	loadScaleEqualLabels(t, loadScaleBracketStrings(t, swift, "unblock", "Models.swift LoadAnchors"), wantUnblock, "Models.swift", "unblock")
	loadScaleEqualLabels(t, loadScaleBracketStrings(t, swift, "scope", "Models.swift LoadAnchors"), wantScope, "Models.swift", "scope")
}

// TestLoadDirectiveCarriesTheRulesAndStateMap pins what the judge is handed
// beside the message: the two pricing rules, the state map's pairs with the
// states named beside them, and the two-asks rule. The ladders are NOT in
// it — they ride as the rubric's questions — so a canary from each is
// asserted absent.
func TestLoadDirectiveCarriesTheRulesAndStateMap(t *testing.T) {
	for _, want := range []string{
		"ACTUAL move", "ship PRs on trust", "the reply BUYS", "never the work under review",
		"1/1", "4/1", "5/2", "2/3", "3/6-8",
		"review-only", "local install", "live E2E", "spec gate", "credential", "mid-build", "EXPENSIVE ask",
	} {
		if !strings.Contains(loadDirective, want) {
			t.Errorf("directive missing %q", want)
		}
	}
	for _, canary := range []string{"A day's thought", "Genuine review", "The largest coordinated effort"} {
		if strings.Contains(loadDirective, canary) {
			t.Errorf("directive restates ladder rung %q — the rungs are the rubric's questions, not the directive's text", canary)
		}
	}
}

// TestLoadSkillIsGone: the skill file that carried the ruler for zeus is
// retired with zeus' write, and the skills bundle must not keep a copy
// nothing reads (a cell that found one would take it as a verb to run).
func TestLoadSkillIsGone(t *testing.T) {
	path := filepath.Join(loadScaleRepoRoot(t), ".claude", "skills", "load")
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("%s still exists — the load ruler lives in loadscale.go and the estimate is the daemon's judgment", path)
	}
}
