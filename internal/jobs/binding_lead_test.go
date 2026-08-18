package jobs

import (
	"testing"
	"time"

	"kraken/internal/core"
)

// Lead-authored (sighted) coverage for the join's S9 disclosure: the blind
// authors could not observe bindAccount's wrapper, and the one defect the
// review of it found — a just-stamped sibling counting zero because its cell
// is not up yet — lives exactly at the boundCounts predicate. These pin the
// fix at that seam; bindAccount's remaining glue (LoadJobs → stamp → mirror)
// is six lines the review round reads directly.

func TestLeadLiveOrFreshlyBoundCountsTheBindToSpawnGap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	pred := liveOrFreshlyBound(map[string]bool{}, now)

	fresh := &Job{Slug: "a", AccountSlug: "slot-a", AccountAt: now.Add(-5 * time.Second).UnixMilli()}
	if !pred(fresh) {
		t.Fatalf("a binding stamped seconds ago must count — the bind→spawn gap is the defect this fixes")
	}
	stale := &Job{Slug: "b", AccountSlug: "slot-b", AccountAt: now.Add(-bindGraceWindow - time.Minute).UnixMilli()}
	if pred(stale) {
		t.Fatalf("a stale stamp with no live cell must stop counting — a stranded binding may not hold a slot forever")
	}
	// A future stamp (clock stepped back) counts: under-counting re-creates
	// the double-pick defect, over-counting self-heals. binding.go documents
	// the asymmetry.
	future := &Job{Slug: "c", AccountSlug: "slot-c", AccountAt: now.Add(time.Hour).UnixMilli()}
	if !pred(future) {
		t.Fatalf("a future stamp must read as fresh")
	}
}

func TestLeadSequentialBindsSpreadAcrossSlots(t *testing.T) {
	// The integration obligation (52-testing-plan.md): two near-simultaneous
	// submits bind two different slots when two are eligible. bindMu
	// serializes them, so the property reduces to: the SECOND selection sees
	// the FIRST's stamp through liveOrFreshlyBound even though no cell is up.
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	snap := core.UsageSnapshot{Accounts: []core.AccountUsage{
		{Slug: "slot-a", Email: "a@x.com", Session: &core.UsageWindow{Percent: 10}, FetchedAt: now.UnixMilli()},
		{Slug: "slot-b", Email: "b@x.com", Session: &core.UsageWindow{Percent: 20}, FetchedAt: now.UnixMilli()},
	}}
	allExist := func(string) bool { return true }
	jobs := []*Job{{Slug: "one"}, {Slug: "two"}}

	counts := boundCounts(jobs, liveOrFreshlyBound(map[string]bool{}, now))
	slug1, _, reason := decideBind(snap, counts, "auto", "carl@probook.ai", "", allExist, now)
	if slug1 != "slot-a" {
		t.Fatalf("first bind picked %q (%s), want the emptier slot-a", slug1, reason)
	}
	// The first job's stamp lands before its cell exists — exactly the state
	// the second submit reads under bindMu.
	jobs[0].AccountSlug, jobs[0].AccountEmail, jobs[0].AccountAt = slug1, "a@x.com", now.UnixMilli()

	counts = boundCounts(jobs, liveOrFreshlyBound(map[string]bool{}, now))
	slug2, _, reason := decideBind(snap, counts, "auto", "carl@probook.ai", "", allExist, now)
	if slug2 != "slot-b" {
		t.Fatalf("second bind picked %q (%s), want slot-b — it must see the sibling's fresh stamp", slug2, reason)
	}
}
