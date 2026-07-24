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
