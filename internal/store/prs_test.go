package store

import "testing"

func TestUpsertPRDedupAndStateUpdate(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "pr-job"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	url := "https://github.com/example/kraken/pull/1"

	if err := s.UpsertPR(id, url, "open"); err != nil {
		t.Fatalf("UpsertPR 1: %v", err)
	}
	if err := s.UpsertPR(id, url, "merged"); err != nil {
		t.Fatalf("UpsertPR 2: %v", err)
	}

	prs, err := s.ListPRs(id)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("expected dedup to one row, got %+v", prs)
	}
	if prs[0].State != "merged" {
		t.Fatalf("state not updated: %+v", prs[0])
	}
}

// TestUpsertPRBumpsJobRev and TestRemovePRBumpsJobRev cover L2: a PR
// mutation must be visible to the editor's optimistic-lock rev check
// exactly like any other job field change — otherwise a save started
// against a rev that predates a PR attach/remove can clobber it.
func TestUpsertPRBumpsJobRev(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "pr-rev-job"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPR(id, "https://github.com/example/kraken/pull/3", "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rev != before.Rev+1 {
		t.Fatalf("rev after UpsertPR = %d, want %d", after.Rev, before.Rev+1)
	}
}

func TestRemovePRBumpsJobRev(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "pr-rev-remove"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	url := "https://github.com/example/kraken/pull/4"
	if err := s.UpsertPR(id, url, "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePR(id, url); err != nil {
		t.Fatalf("RemovePR: %v", err)
	}
	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rev != before.Rev+1 {
		t.Fatalf("rev after RemovePR = %d, want %d", after.Rev, before.Rev+1)
	}
}

func TestRemovePR(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "pr-remove"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	url := "https://github.com/example/kraken/pull/2"
	if err := s.UpsertPR(id, url, "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if err := s.RemovePR(id, url); err != nil {
		t.Fatalf("RemovePR: %v", err)
	}
	prs, err := s.ListPRs(id)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 0 {
		t.Fatalf("pr not removed: %+v", prs)
	}
}
