package store

import (
	"errors"
	"testing"
)

// jobstack_test.go — schema v35's job_stacks table and its six verbs
// (jobstack.go). The obligations are 52-testing-plan.md's store row: a re-add
// keeps status and at; remove matches url then label; clear leaves other
// jobs alone; every verb on an unknown job is ErrNotFound; and no verb bumps
// rev or updated_at.

func seedStackJob(t *testing.T, s *Store, slug string) *Job {
	t.Helper()
	if _, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken", Body: "b", State: "working"}); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	j, err := s.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug %s: %v", slug, err)
	}
	return j
}

func TestUpsertStackKeepsStatusRefreshesAtOnReAdd(t *testing.T) {
	s := openTest(t)
	j := seedStackJob(t, s, "stack-a")

	if err := s.UpsertStack(j.ID, "http://127.0.0.1:7878", "board", 1000); err != nil {
		t.Fatalf("UpsertStack: %v", err)
	}
	if err := s.SetStackStatus(j.ID, "http://127.0.0.1:7878", "up", 1500); err != nil {
		t.Fatalf("SetStackStatus: %v", err)
	}
	// re-add with a new label and a later time: label and at take the new
	// values (the cell re-registered its stack), status and probed_at are kept
	if err := s.UpsertStack(j.ID, "http://127.0.0.1:7878", "web", 2000); err != nil {
		t.Fatalf("UpsertStack re-add: %v", err)
	}
	got, err := s.ListStacks(j.ID)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1 (re-add must not duplicate)", len(got))
	}
	l := got[0]
	if l.Label != "web" || l.Status != "up" || l.At != 2000 || l.ProbedAt != 1500 {
		t.Fatalf("row = %+v, want label web, status up, at 2000 (refreshed), probed_at 1500", l)
	}
}

func TestListStacksKeepsRegistrationOrder(t *testing.T) {
	s := openTest(t)
	j := seedStackJob(t, s, "stack-order")
	for i, u := range []string{"http://localhost:5173", "http://127.0.0.1:7878", "https://x.ngrok.app"} {
		if err := s.UpsertStack(j.ID, u, "", int64(i)); err != nil {
			t.Fatalf("UpsertStack %s: %v", u, err)
		}
	}
	// re-label the first: it must stay first
	if err := s.UpsertStack(j.ID, "http://localhost:5173", "ui", 99); err != nil {
		t.Fatalf("relabel: %v", err)
	}
	got, _ := s.ListStacks(j.ID)
	if len(got) != 3 || got[0].URL != "http://localhost:5173" || got[0].Label != "ui" || got[2].URL != "https://x.ngrok.app" {
		t.Fatalf("order = %+v", got)
	}
	if got[0].Status != StackStatusUnknown {
		t.Fatalf("fresh status = %q, want %q", got[0].Status, StackStatusUnknown)
	}
}

func TestRemoveStackMatchesURLThenLabel(t *testing.T) {
	s := openTest(t)
	j := seedStackJob(t, s, "stack-rm")
	s.UpsertStack(j.ID, "http://localhost:5173", "ui", 1)
	s.UpsertStack(j.ID, "http://127.0.0.1:7878", "board", 1)

	if err := s.RemoveStack(j.ID, "ui"); err != nil {
		t.Fatalf("RemoveStack by label: %v", err)
	}
	if got, _ := s.ListStacks(j.ID); len(got) != 1 || got[0].Label != "board" {
		t.Fatalf("after label remove: %+v", got)
	}
	if err := s.RemoveStack(j.ID, "http://127.0.0.1:7878"); err != nil {
		t.Fatalf("RemoveStack by url: %v", err)
	}
	if got, _ := s.ListStacks(j.ID); len(got) != 0 {
		t.Fatalf("after url remove: %+v", got)
	}
	if err := s.RemoveStack(j.ID, "nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove of absent token = %v, want ErrNotFound", err)
	}
}

func TestClearStacksLeavesOtherJobs(t *testing.T) {
	s := openTest(t)
	a := seedStackJob(t, s, "stack-clear-a")
	b := seedStackJob(t, s, "stack-clear-b")
	s.UpsertStack(a.ID, "http://localhost:1", "", 1)
	s.UpsertStack(b.ID, "http://localhost:2", "", 1)

	if err := s.ClearStacks(a.ID); err != nil {
		t.Fatalf("ClearStacks: %v", err)
	}
	// clearing an already-empty list is a success
	if err := s.ClearStacks(a.ID); err != nil {
		t.Fatalf("ClearStacks (empty): %v", err)
	}
	if got, _ := s.ListStacks(a.ID); len(got) != 0 {
		t.Fatalf("a after clear: %+v", got)
	}
	if got, _ := s.ListStacks(b.ID); len(got) != 1 {
		t.Fatalf("b after a's clear: %+v (must be untouched)", got)
	}
	// the teardown verb needs no existing job
	if err := s.DeleteStacksForJob(99999); err != nil {
		t.Fatalf("DeleteStacksForJob unknown job = %v, want nil", err)
	}
}

func TestStackVerbsOnUnknownJobAreNotFound(t *testing.T) {
	s := openTest(t)
	if err := s.UpsertStack(4242, "http://localhost:1", "", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpsertStack = %v, want ErrNotFound", err)
	}
	if err := s.RemoveStack(4242, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RemoveStack = %v, want ErrNotFound", err)
	}
	if err := s.ClearStacks(4242); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClearStacks = %v, want ErrNotFound", err)
	}
	if got, err := s.ListStacks(4242); err != nil || len(got) != 0 {
		t.Fatalf("ListStacks unknown = %v, %v; want empty, nil", got, err)
	}
}

func TestSetStackStatusOnMissingRowIsNoop(t *testing.T) {
	s := openTest(t)
	j := seedStackJob(t, s, "stack-status")
	if err := s.SetStackStatus(j.ID, "http://localhost:9", "down", 5); err != nil {
		t.Fatalf("SetStackStatus on absent row = %v, want nil", err)
	}
	if got, _ := s.ListStacks(j.ID); len(got) != 0 {
		t.Fatalf("status write must not create a row: %+v", got)
	}
}

// TestStackVerbsBumpNeitherRevNorUpdatedAt is the load-bearing one: the
// header of jobstack.go states the rule, this is what holds it.
func TestStackVerbsBumpNeitherRevNorUpdatedAt(t *testing.T) {
	s := openTest(t)
	j := seedStackJob(t, s, "stack-rev")
	before, _ := s.GetJobBySlug("stack-rev")

	s.UpsertStack(j.ID, "http://localhost:5173", "ui", 1)
	s.SetStackStatus(j.ID, "http://localhost:5173", "up", 2)
	s.UpsertStack(j.ID, "http://localhost:5173", "web", 3)
	s.RemoveStack(j.ID, "web")
	s.UpsertStack(j.ID, "http://localhost:5173", "ui", 4)
	s.ClearStacks(j.ID)

	after, _ := s.GetJobBySlug("stack-rev")
	if after.Rev != before.Rev {
		t.Fatalf("rev %d -> %d: a stack write must not bump the editor's lock", before.Rev, after.Rev)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("updated_at %v -> %v: a stack write must not reset the card's age", before.UpdatedAt, after.UpdatedAt)
	}
}
