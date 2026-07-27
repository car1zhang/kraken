package store

import (
	"bytes"
	"errors"
	"testing"
)

func TestFeedbackAddStampsSessionAndDefaultsAddressed(t *testing.T) {
	s := openTest(t)
	id, err := s.AddFeedback(Feedback{
		JobSlug: "doc-job", Artifact: "explain.html",
		Snapshot: []byte("<html>v1</html>"), SnapshotExt: "html",
		Note: "confusing diagram", TS: 1, Session: "sess-abc",
	})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	f, err := s.GetFeedback(id)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if f.Session != "sess-abc" {
		t.Fatalf("Session = %q, want sess-abc", f.Session)
	}
	if f.Addressed != "" || f.AddressedAt != "" {
		t.Fatalf("a fresh entry must start unaddressed: %+v", f)
	}
}

func TestGetFeedbackNotFound(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetFeedback(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestUpdateFeedbackNoteReFreezesSnapshot covers Q4: an edit captures the
// artifact as currently served, not the original submit-time bytes — the
// snapshot is overwritten, not appended/versioned.
func TestUpdateFeedbackNoteReFreezesSnapshot(t *testing.T) {
	s := openTest(t)
	id, err := s.AddFeedback(Feedback{
		JobSlug: "doc-job", Artifact: "explain.html",
		Snapshot: []byte("<html>v1</html>"), SnapshotExt: "html",
		Note: "original note", TS: 1, Session: "sess-abc",
	})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	if err := s.UpdateFeedbackNote(id, "edited note", []byte("<html>v2</html>"), "html"); err != nil {
		t.Fatalf("UpdateFeedbackNote: %v", err)
	}
	f, err := s.GetFeedback(id)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if f.Note != "edited note" {
		t.Fatalf("Note = %q, want edited note", f.Note)
	}
	if !bytes.Equal(f.Snapshot, []byte("<html>v2</html>")) {
		t.Fatalf("Snapshot not re-frozen: %q", f.Snapshot)
	}
	// session/ts/addressed must be untouched by an edit
	if f.Session != "sess-abc" || f.TS != 1 {
		t.Fatalf("edit touched fields it shouldn't have: %+v", f)
	}

	if err := s.UpdateFeedbackNote(999, "x", nil, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateFeedbackNote on missing id: want ErrNotFound, got %v", err)
	}
}

func TestAddressFeedbackRoundTrip(t *testing.T) {
	s := openTest(t)
	id, err := s.AddFeedback(Feedback{JobSlug: "doc-job", Artifact: "explain.html", Note: "n", TS: 1})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	if err := s.AddressFeedback(id, "fixed the diagram", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("AddressFeedback: %v", err)
	}
	f, err := s.GetFeedback(id)
	if err != nil {
		t.Fatalf("GetFeedback: %v", err)
	}
	if f.Addressed != "fixed the diagram" || f.AddressedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("addressed fields wrong: %+v", f)
	}
	if f.Note != "n" {
		t.Fatalf("AddressFeedback must not touch note: %+v", f)
	}

	if err := s.AddressFeedback(999, "x", "y"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddressFeedback on missing id: want ErrNotFound, got %v", err)
	}
}

func TestDeleteFeedback(t *testing.T) {
	s := openTest(t)
	id, err := s.AddFeedback(Feedback{JobSlug: "doc-job", Artifact: "explain.html", Note: "n", TS: 1})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	if err := s.DeleteFeedback(id); err != nil {
		t.Fatalf("DeleteFeedback: %v", err)
	}
	if _, err := s.GetFeedback(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetFeedback after delete: want ErrNotFound, got %v", err)
	}
	if err := s.DeleteFeedback(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteFeedback on already-deleted id: want ErrNotFound, got %v", err)
	}
}

// TestListFeedbackMetaOmitsSnapshot pins ListFeedbackMeta's contract: same
// rows and ordering as ListFeedback, Snapshot always nil, everything else
// populated.
func TestListFeedbackMetaOmitsSnapshot(t *testing.T) {
	s := openTest(t)
	jobID, err := s.CreateJob(&Job{Slug: "j1"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	id, err := s.AddFeedback(Feedback{JobID: jobID, JobSlug: "j1", Artifact: "a.html", Snapshot: []byte("bytes"), SnapshotExt: ".html", Note: "n", Session: "sess"})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	metas, err := s.ListFeedbackMeta(jobID)
	if err != nil {
		t.Fatalf("ListFeedbackMeta: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("meta count = %d, want 1", len(metas))
	}
	m := metas[0]
	if m.Snapshot != nil {
		t.Fatalf("meta Snapshot = %q, want nil", m.Snapshot)
	}
	if m.ID != id || m.Artifact != "a.html" || m.SnapshotExt != ".html" || m.Note != "n" || m.Session != "sess" || m.TS == 0 {
		t.Fatalf("meta fields: %+v", m)
	}
}

// TestFeedbackSinceWindow pins the sweep read's window semantics: the floor is
// INCLUSIVE (a row stamped exactly at it is in), rows below it are out, the
// result is oldest-first, and the read is unscoped — so a NULL-job_id fossil
// (a pre-v5 row, or one orphaned by DeleteJob's ON DELETE SET NULL) still
// surfaces, which is the whole reason the dream loop can mine notes whose job
// no longer exists. Snapshot blobs are excluded like ListFeedbackMeta's.
func TestFeedbackSinceWindow(t *testing.T) {
	s := openTest(t)
	jobID, err := s.CreateJob(&Job{Slug: "swept"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	for _, seed := range []struct {
		note string
		ts   int64
	}{
		{"below the floor", 99},
		{"exactly at the floor", 100},
		{"above the floor", 101},
	} {
		if _, err := s.AddFeedback(Feedback{
			JobID: jobID, JobSlug: "swept", Artifact: "a.md",
			Snapshot: []byte("bytes"), SnapshotExt: "md",
			Note: seed.note, TS: seed.ts,
		}); err != nil {
			t.Fatalf("AddFeedback %q: %v", seed.note, err)
		}
	}
	// a fossil with no owning job at all — job_id NULL, inside the window
	if _, err := s.AddFeedback(Feedback{JobSlug: "ghost-job", Artifact: "a.md", Note: "fossil", TS: 150}); err != nil {
		t.Fatalf("AddFeedback fossil: %v", err)
	}

	got, err := s.FeedbackSince(100)
	if err != nil {
		t.Fatalf("FeedbackSince: %v", err)
	}
	want := []string{"exactly at the floor", "above the floor", "fossil"}
	if len(got) != len(want) {
		t.Fatalf("FeedbackSince(100) = %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Note != w {
			t.Fatalf("row %d = %q, want %q (oldest first): %+v", i, got[i].Note, w, got)
		}
	}
	for _, f := range got {
		if f.Snapshot != nil {
			t.Fatalf("FeedbackSince must exclude snapshot blobs: %+v", f)
		}
	}
	if got[2].JobID != 0 {
		t.Fatalf("fossil row should carry JobID 0 (NULL job_id): %+v", got[2])
	}

	// CountFeedbackSince must agree with the list, at several floors.
	for _, floor := range []int64{0, 99, 100, 101, 151, 1_000_000} {
		list, err := s.FeedbackSince(floor)
		if err != nil {
			t.Fatalf("FeedbackSince(%d): %v", floor, err)
		}
		n, err := s.CountFeedbackSince(floor)
		if err != nil {
			t.Fatalf("CountFeedbackSince(%d): %v", floor, err)
		}
		if n != len(list) {
			t.Fatalf("CountFeedbackSince(%d) = %d, want %d (len of FeedbackSince)", floor, n, len(list))
		}
	}
}

// TestFeedbackSinceEmptyTable pins that a sweep over a database with no
// feedback at all is a normal empty read, not an error the nightly pass has to
// special-case.
func TestFeedbackSinceEmptyTable(t *testing.T) {
	s := openTest(t)
	got, err := s.FeedbackSince(0)
	if err != nil {
		t.Fatalf("FeedbackSince on an empty table: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no rows, got %+v", got)
	}
	n, err := s.CountFeedbackSince(0)
	if err != nil {
		t.Fatalf("CountFeedbackSince on an empty table: %v", err)
	}
	if n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
}

// TestFeedbackSinceUsesCreationTimeNotEditTime pins why ts is immutable after
// insert: an edited old note must NOT re-enter a later window, or consecutive
// non-overlapping sweeps would mine the same note twice.
func TestFeedbackSinceUsesCreationTimeNotEditTime(t *testing.T) {
	s := openTest(t)
	id, err := s.AddFeedback(Feedback{JobSlug: "j", Artifact: "a.md", Note: "old note", TS: 10})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	if err := s.UpdateFeedbackNote(id, "edited much later", []byte("v2"), "md"); err != nil {
		t.Fatalf("UpdateFeedbackNote: %v", err)
	}
	got, err := s.FeedbackSince(100)
	if err != nil {
		t.Fatalf("FeedbackSince: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("an edit must not move a row into a later window: %+v", got)
	}
}
