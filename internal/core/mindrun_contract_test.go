package core

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

// mindrun_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 674, mind runs), slice B: the two new
// `OperatorOptions` session filters. Written from the frozen contract alone;
// internal/core/operator.go's working tree was never read (its HEAD type
// declarations were read through `git show HEAD:` for the pre-existing
// OperatorOptions/OperatorRecord shape). The transcript harness (mineHome,
// mineWrite, mpcPrompt, mpcAsst, mpcWindow, mpcRun) is the committed one.
//
// Dimension: LOGIC — invariants and happy paths. Every package-level
// identifier here is prefixed `contract`.

const (
	contractLogin    = "Carl Zhang"
	contractProjectA = "-Users-carl-Developer-kraken"
	contractProjectB = "-Users-carl--kraken-cells-demo-12-wt-kraken"
)

// contractSeedThree writes three root transcripts (sessions sa, sb, sc), two
// typed prompts each, all inside the window.
func contractSeedThree(t *testing.T) (since int64) {
	t.Helper()
	projects := mineHome(t)
	since, at := mpcWindow()
	mineWrite(t, projects, contractProjectA, "sa.jsonl",
		mpcPrompt(t, "a1", "sa", at(1), "alpha one is how it works", nil),
		mpcAsst(t, at(2), "ok"),
		mpcPrompt(t, "a2", "sa", at(3), "alpha two is the rule", nil),
	)
	mineWrite(t, projects, contractProjectA, "sb.jsonl",
		mpcPrompt(t, "b1", "sb", at(4), "bravo one is how it works", nil),
		mpcAsst(t, at(5), "ok"),
		mpcPrompt(t, "b2", "sb", at(6), "bravo two is the rule", nil),
	)
	mineWrite(t, projects, contractProjectB, "sc.jsonl",
		mpcPrompt(t, "c1", "sc", at(7), "charlie one is how it works", nil),
		mpcAsst(t, at(8), "ok"),
		mpcPrompt(t, "c2", "sc", at(9), "charlie two is the rule", nil),
	)
	return since
}

func contractSessionsOf(res *OperatorResult) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, r := range res.Records {
		if !seen[r.Session] {
			seen[r.Session] = true
			out = append(out, r.Session)
		}
	}
	sort.Strings(out)
	return out
}

func contractUUIDs(res *OperatorResult) []string {
	out := []string{}
	for _, r := range res.Records {
		out = append(out, r.UUID)
	}
	sort.Strings(out)
	return out
}

// "When non-empty, `OperatorProse` emits only records whose `Session` is in
// the set; `Scanned`/`Skipped` still count every transcript walked".
func TestContractOperatorSessionsKeepsOnlyTheNamedSessions(t *testing.T) {
	since := contractSeedThree(t)
	all := mpcRun(t, OperatorOptions{Since: since, Login: contractLogin})
	if got := contractSessionsOf(all); !reflect.DeepEqual(got, []string{"sa", "sb", "sc"}) {
		t.Fatalf("unfiltered sessions = %v, want sa/sb/sc (fixture sanity)", got)
	}

	res := mpcRun(t, OperatorOptions{Since: since, Login: contractLogin, Sessions: []string{"sa", "sc"}})
	if got := contractUUIDs(res); !reflect.DeepEqual(got, []string{"a1", "a2", "c1", "c2"}) {
		t.Errorf("Sessions [sa sc] uuids = %v, want a1 a2 c1 c2", got)
	}
	if res.Scanned != all.Scanned || res.Skipped != all.Skipped {
		t.Errorf("filtered scanned/skipped = %d/%d, want the unfiltered %d/%d (every transcript walked)",
			res.Scanned, res.Skipped, all.Scanned, all.Skipped)
	}
	if res.Scanned != 3 {
		t.Errorf("Scanned = %d, want 3", res.Scanned)
	}
}

// "Records whose `Session` is in the set are dropped".
func TestContractOperatorExcludeSessionsDropsTheNamedSessions(t *testing.T) {
	since := contractSeedThree(t)
	res := mpcRun(t, OperatorOptions{Since: since, Login: contractLogin, ExcludeSessions: []string{"sb"}})
	if got := contractUUIDs(res); !reflect.DeepEqual(got, []string{"a1", "a2", "c1", "c2"}) {
		t.Errorf("ExcludeSessions [sb] uuids = %v, want a1 a2 c1 c2", got)
	}
	if res.Scanned != 3 {
		t.Errorf("Scanned = %d, want 3 (exclusion still walks every transcript)", res.Scanned)
	}
}

// "applied after `Sessions`": a session both named and excluded is dropped.
func TestContractOperatorExcludeAppliesAfterSessions(t *testing.T) {
	since := contractSeedThree(t)
	res := mpcRun(t, OperatorOptions{Since: since, Login: contractLogin,
		Sessions: []string{"sa", "sb"}, ExcludeSessions: []string{"sb"}})
	if got := contractUUIDs(res); !reflect.DeepEqual(got, []string{"a1", "a2"}) {
		t.Errorf("Sessions [sa sb] minus [sb] uuids = %v, want a1 a2", got)
	}
}

// "both empty means today's behaviour byte for byte": nil and empty slices
// marshal to the identical result.
func TestContractOperatorEmptyFiltersAreTodaysBehaviour(t *testing.T) {
	since := contractSeedThree(t)
	base := mpcRun(t, OperatorOptions{Since: since, Login: contractLogin, Before: 2, After: 2})
	empty := mpcRun(t, OperatorOptions{Since: since, Login: contractLogin, Before: 2, After: 2,
		Sessions: []string{}, ExcludeSessions: []string{}})
	a, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("empty filters changed the result:\nnil:   %s\nempty: %s", a, b)
	}
	if len(base.Records) != 6 {
		t.Errorf("records = %d, want all 6", len(base.Records))
	}
}
