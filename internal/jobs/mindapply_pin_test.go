package jobs

import (
	"strings"
	"testing"

	"kraken/internal/store"
)

// Regression pin for review round 1's blocking finding: validation checked
// each op only against the store as it stood before applying, so two
// supersedes of ONE target both passed, the second inserted its replacement
// row and then SupersedeMMItem refused the (by then dead) target — leaving a
// live orphan and a half-applied answer. One claim per target per answer: the
// whole answer is refused and nothing is written.
func TestPinApplyMindOpsRefusesTwoClaimsOnOneTarget(t *testing.T) {
	st := useTestStore(t)
	target, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "manual", Content: "the old belief", Title: "old", Scope: "universal", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	item := func(quote string) *MMProposal {
		return &MMProposal{Title: "a new claim " + quote, Scope: "universal", Kind: "decision",
			Body: "the operator said so", Quote: quote, Source: CaptureSource{Kind: "manual"}}
	}
	run := store.MindRun{ID: 7, Trigger: "daily"}
	for name, ops := range map[string][]MindOp{
		"two supersedes": {
			{Op: "supersede", Target: target, Item: item("first replacement"), Quote: "first replacement"},
			{Op: "supersede", Target: target, Item: item("second replacement"), Quote: "second replacement"},
		},
		"retire then supersede": {
			{Op: "retire", Target: target, Quote: "gone"},
			{Op: "supersede", Target: target, Item: item("late replacement"), Quote: "late replacement"},
		},
	} {
		before, _ := st.ListMMItems(true)
		_, err := ApplyMindOps(st, run, MindAnswer{Ops: ops}, nil, 100)
		if err == nil {
			t.Fatalf("%s: ApplyMindOps = nil error, want the whole answer refused", name)
		}
		if !strings.Contains(err.Error(), "target") {
			t.Errorf("%s: error %q does not name the target", name, err)
		}
		after, _ := st.ListMMItems(true)
		if len(after) != len(before) {
			t.Fatalf("%s: %d rows before, %d after — a refused answer wrote rows", name, len(before), len(after))
		}
		if got, _ := st.GetMMItem(target); got.InvalidatedAt != 0 {
			t.Fatalf("%s: the target was invalidated by a refused answer", name)
		}
	}
}
