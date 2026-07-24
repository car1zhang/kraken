package store

import (
	"errors"
	"reflect"
	"testing"
)

// jobedit_test.go — SaveJobEdit (L10): one atomic transaction covering the
// field update, prereqs replace, and PR sync a single editor save needs.

func TestSaveJobEditAppliesAllThreeAtomically(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "edit-all"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	newRev, err := s.SaveJobEdit(id, 1,
		map[string]any{"state": "working"},
		[]Prereq{{Slug: "dep-a", Gated: true}}, true,
		[]PR{{URL: "https://github.com/x/y/pull/1", State: "open"}}, true,
	)
	if err != nil {
		t.Fatalf("SaveJobEdit: %v", err)
	}
	// rev 1→2 from the field update, then 2→3 from the PR upsert (a new
	// row, L2's rev bump) — newRev must reflect the FINAL committed rev,
	// not just the field update's own intermediate return value.
	if newRev != 3 {
		t.Fatalf("newRev = %d, want 3 (field update + PR upsert both bump rev)", newRev)
	}

	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "working" || got.Rev != newRev {
		t.Fatalf("job fields not applied, or returned newRev disagrees with the row: got=%+v newRev=%d", got, newRev)
	}
	prereqs, err := s.ListPrereqs(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prereqs, []Prereq{{Slug: "dep-a", Gated: true}}) {
		t.Fatalf("prereqs not applied: %+v", prereqs)
	}
	prs, err := s.ListPRs(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].URL != "https://github.com/x/y/pull/1" {
		t.Fatalf("prs not applied: %+v", prs)
	}
}

// TestSaveJobEditStaleRevRollsBackEverything covers the whole point of
// L10's single transaction: a stale rev must refuse the field update AND
// leave prereqs/PRs completely untouched — not partially applied.
func TestSaveJobEditStaleRevRollsBackEverything(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "edit-stale"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.ReplacePrereqs(id, []Prereq{{Slug: "orig-dep"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPR(id, "https://github.com/x/y/pull/9", "open"); err != nil {
		t.Fatal(err)
	}

	_, err = s.SaveJobEdit(id, 1, // rev is now 2 (from the UpsertPR bump) — 1 is stale
		map[string]any{"state": "working"},
		[]Prereq{{Slug: "new-dep"}}, true,
		[]PR{{URL: "https://github.com/x/y/pull/99", State: "open"}}, true,
	)
	if !errors.Is(err, ErrStaleRev) {
		t.Fatalf("err = %v, want ErrStaleRev", err)
	}

	prereqs, err := s.ListPrereqs(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prereqs, []Prereq{{Slug: "orig-dep"}}) {
		t.Fatalf("prereqs changed despite the stale-rev refusal: %+v", prereqs)
	}
	prs, err := s.ListPRs(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].URL != "https://github.com/x/y/pull/9" {
		t.Fatalf("prs changed despite the stale-rev refusal: %+v", prs)
	}
}

// TestSaveJobEditSkipsPrereqsAndPRsWhenToldTo covers the unparseable-save
// contract (api/jobedit.go's webJobSave): syncPrereqs=false/syncPRs=false
// must leave both completely untouched, not replace them with a same-value
// no-op.
func TestSaveJobEditSkipsPrereqsAndPRsWhenToldTo(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "edit-skip"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.ReplacePrereqs(id, []Prereq{{Slug: "keep-me"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPR(id, "https://github.com/x/y/pull/5", "open"); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}

	newRev, err := s.SaveJobEdit(id, before.Rev,
		map[string]any{"body": "garbage save"},
		nil, false,
		nil, false,
	)
	if err != nil {
		t.Fatalf("SaveJobEdit: %v", err)
	}
	if newRev != before.Rev+1 {
		t.Fatalf("newRev = %d, want %d", newRev, before.Rev+1)
	}

	prereqs, err := s.ListPrereqs(id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prereqs, []Prereq{{Slug: "keep-me"}}) {
		t.Fatalf("prereqs touched despite syncPrereqs=false: %+v", prereqs)
	}
	prs, err := s.ListPRs(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].URL != "https://github.com/x/y/pull/5" {
		t.Fatalf("prs touched despite syncPRs=false: %+v", prs)
	}
}

// TestSaveJobEditPRSyncRemovesUnlisted confirms the PR-sync pass still
// removes a PR row not named in the new set — same contract as the old
// syncStorePRs (api/jobedit.go, pre-L10) had.
func TestSaveJobEditPRSyncRemovesUnlisted(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "edit-prsync"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertPR(id, "https://github.com/x/y/pull/1", "open"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPR(id, "https://github.com/x/y/pull/2", "open"); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}

	// new set: pull/2 (unchanged) + pull/3 (new); pull/1 must be removed.
	_, err = s.SaveJobEdit(id, before.Rev, map[string]any{"body": "x"}, nil, false,
		[]PR{{URL: "https://github.com/x/y/pull/2", State: "open"}, {URL: "https://github.com/x/y/pull/3", State: "open"}}, true)
	if err != nil {
		t.Fatalf("SaveJobEdit: %v", err)
	}

	prs, err := s.ListPRs(id)
	if err != nil {
		t.Fatal(err)
	}
	urls := map[string]bool{}
	for _, p := range prs {
		urls[p.URL] = true
	}
	if len(prs) != 2 || !urls["https://github.com/x/y/pull/2"] || !urls["https://github.com/x/y/pull/3"] || urls["https://github.com/x/y/pull/1"] {
		t.Fatalf("prs after sync = %+v, want pull/2 and pull/3 only", prs)
	}
}
