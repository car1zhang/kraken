package api

import (
	"testing"

	"kraken/internal/jobs"
)

// Lead-authored (sighted) pin for the wire presence rule the blind round
// disclosed as unreachable (join note S8): buildWebBoard is untestable, so
// webAccountFor is the observable symbol for "built for every NON-archived
// job with AccountSlug != \"\" regardless of live/display state". The
// archived half has its own blind coverage (TestEdgeAccountArchivedCardWithholdsTheKey).
func TestLeadWebAccountForServesEveryStateAndOnlyBoundJobs(t *testing.T) {
	bound := &jobs.Job{Slug: "csr-latency-fix", AccountSlug: "dorisexplorer4-gmail-com", AccountEmail: "dorisexplorer4@gmail.com", AccountAt: 1755480000000}
	// The no-flicker rule: the payload ignores display state entirely — a card
	// moves between working and blocked tick to tick, and the key must not
	// flicker with it. State lives on jobs.Job.State.
	for _, state := range []string{"", "draft", "working", "blocked", "done"} {
		bound.State = state
		got := webAccountFor(bound)
		if got == nil {
			t.Fatalf("state %q: bound job served no account object", state)
		}
		if got.Slug != bound.AccountSlug || got.Email != bound.AccountEmail || got.At != bound.AccountAt {
			t.Fatalf("state %q: got %+v", state, got)
		}
	}
	// Degraded bindings are still bindings: the slug alone decides presence.
	degraded := &jobs.Job{Slug: "x", AccountSlug: "slot-a"}
	if got := webAccountFor(degraded); got == nil || got.Slug != "slot-a" || got.Email != "" || got.At != 0 {
		t.Fatalf("degraded binding: got %+v", webAccountFor(degraded))
	}
	if webAccountFor(&jobs.Job{Slug: "unbound"}) != nil {
		t.Fatalf("unbound job served an account object")
	}
	if webAccountFor(nil) != nil {
		t.Fatalf("nil job served an account object")
	}
}
