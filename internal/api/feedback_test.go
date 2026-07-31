package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// feedback_test.go — coverage for feedback.go's POST /api/feedback dispatch
// (create/edit/delete/addressed) and GET /api/feedback, following
// docquestions_test.go's store-backed harness pattern (useTestStore +
// mustCreateStoreJob). B7: this file (plus questions_test.go) is what
// contract_test.go's "op dispatch and validation are covered by
// feedback_test.go/questions_test.go instead" comment refers to.

func postFeedback(t *testing.T, req feedbackReq) *httptest.ResponseRecorder {
	t.Helper()
	body := mustJSON(t, req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/feedback", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	feedbackHandler(rec, httpReq)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body %q: %v", rec.Body.String(), err)
	}
	return resp["error"]
}

func strPtr(s string) *string { return &s }

// seedPlutoArtifact wires up a done (non-live) job with one artifact
// served from the artifacts table (Q12: ArtifactBytes resolves it for any
// non-"cell" home) — the shared fixture every create/edit test below builds
// on.
func seedPlutoArtifact(t *testing.T, st *store.Store, slug, name, bytes string) *jobs.Job {
	t.Helper()
	j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "monolith", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: name, Bytes: []byte(bytes), Mtime: 1}}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	return j
}

// --- create ---

func TestFeedbackCreate(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-create", "00-overview.md", "v1 bytes")

	rec := postFeedback(t, feedbackReq{Slug: "fb-create", Home: "pluto", Name: "00-overview.md", Feedback: "  looks good  "})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	fs, err := st.ListFeedback(j.ID)
	if err != nil || len(fs) != 1 {
		t.Fatalf("ListFeedback: %+v, err %v", fs, err)
	}
	f := fs[0]
	if f.Note != "looks good" {
		t.Fatalf("note not trimmed: %q", f.Note)
	}
	if f.JobID != j.ID {
		t.Fatalf("JobID = %d, want %d (non-blocking#3: must be the resolved job's own id)", f.JobID, j.ID)
	}
	if f.JobSlug != "fb-create" {
		t.Fatalf("JobSlug = %q, want fb-create", f.JobSlug)
	}
	if string(f.Snapshot) != "v1 bytes" {
		t.Fatalf("snapshot = %q, want the artifact's current bytes", f.Snapshot)
	}
}

// TestFeedbackCreateUsesResolvedJobSlugNotRawRequestSlug pins
// non-blocking#3: the row's job_slug must come from the RESOLVED job (the
// same identity board.go's jobFeedback reads by), not the raw request
// string — jobs.JobBySlug tolerates a trailing ".job.md" on the request
// slug (JobBySlug strips it before matching), so a create posted with that
// suffix must still store job_slug as the bare slug, or no board read would
// ever find the row.
func TestFeedbackCreateUsesResolvedJobSlugNotRawRequestSlug(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-resolved-slug", "a.md", "v1")

	rec := postFeedback(t, feedbackReq{Slug: "fb-resolved-slug.job.md", Home: "pluto", Name: "a.md", Feedback: "note"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	fs, err := st.ListFeedback(j.ID)
	if err != nil || len(fs) != 1 {
		t.Fatalf("feedback not stored under the resolved bare slug: %+v, err %v", fs, err)
	}
	if fs[0].JobSlug != "fb-resolved-slug" {
		t.Fatalf("JobSlug = %q, want the resolved job's own slug, not the raw request string", fs[0].JobSlug)
	}
}

func TestFeedbackCreateEmptyNote(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	seedPlutoArtifact(t, st, "fb-empty-note", "a.md", "x")

	rec := postFeedback(t, feedbackReq{Slug: "fb-empty-note", Home: "pluto", Name: "a.md", Feedback: "   "})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// TestFeedbackCreateNoteTooLong pins feedbackNoteMaxLen's exact boundary
// (non-blocking#8): 8000 runes succeeds, 8001 is refused with a 400 before
// ever reaching the store.
func TestFeedbackCreateNoteTooLong(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	seedPlutoArtifact(t, st, "fb-toolong", "a.md", "x")

	ok := strings.Repeat("n", feedbackNoteMaxLen)
	rec := postFeedback(t, feedbackReq{Slug: "fb-toolong", Home: "pluto", Name: "a.md", Feedback: ok})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d-char note should be accepted, got status %d body %s", feedbackNoteMaxLen, rec.Code, rec.Body.String())
	}

	tooLong := strings.Repeat("n", feedbackNoteMaxLen+1)
	rec2 := postFeedback(t, feedbackReq{Slug: "fb-toolong", Home: "pluto", Name: "a.md", Feedback: tooLong})
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("%d-char note: status %d, want 400, body %s", feedbackNoteMaxLen+1, rec2.Code, rec2.Body.String())
	}
}

func TestFeedbackCreateMissingHomeOrName(t *testing.T) {
	useTestStore(t)
	rec := postFeedback(t, feedbackReq{Slug: "whatever", Feedback: "note"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec); !strings.Contains(got, "unrecognized") {
		t.Fatalf("error = %q", got)
	}
}

func TestFeedbackCreateUnknownJob(t *testing.T) {
	useTestStore(t)
	rec := postFeedback(t, feedbackReq{Slug: "no-such-job", Home: "pluto", Name: "a.md", Feedback: "note"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestFeedbackCreateUnknownArtifact(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "fb-no-artifact", Repo: "monolith", State: "done"})

	rec := postFeedback(t, feedbackReq{Slug: "fb-no-artifact", Home: "pluto", Name: "missing.md", Feedback: "note"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// --- edit ---

func TestFeedbackEditUpdatesNoteAndRefreezesSnapshot(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-edit", "a.md", "v1")

	rec := postFeedback(t, feedbackReq{Slug: "fb-edit", Home: "pluto", Name: "a.md", Feedback: "first"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID

	// the artifact changes on disk/DB before the edit — Q4 says the edit
	// must re-freeze from what's CURRENTLY served, not the original bytes.
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "a.md", Bytes: []byte("v2"), Mtime: 2}}, nil); err != nil {
		t.Fatal(err)
	}

	rec2 := postFeedback(t, feedbackReq{Slug: "fb-edit", ID: id, Feedback: "  second  "})
	if rec2.Code != http.StatusOK {
		t.Fatalf("edit: status %d, body %s", rec2.Code, rec2.Body.String())
	}

	got, err := st.GetFeedback(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "second" {
		t.Fatalf("note = %q, want trimmed \"second\"", got.Note)
	}
	if string(got.Snapshot) != "v2" {
		t.Fatalf("snapshot = %q, want re-frozen v2 (Q4)", got.Snapshot)
	}
}

// TestFeedbackEditKeepsSnapshotWhenArtifactUnresolvable is B7's required
// "edit re-freeze fallback that must NEVER null a snapshot" case: once the
// artifact can no longer be resolved at all (the cell's torn down, no
// pluto/archive copy survived), an edit must keep the row's PRIOR snapshot
// bytes rather than blanking it.
func TestFeedbackEditKeepsSnapshotWhenArtifactUnresolvable(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-edit-fallback", "a.md", "v1")

	rec := postFeedback(t, feedbackReq{Slug: "fb-edit-fallback", Home: "pluto", Name: "a.md", Feedback: "first"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID
	if string(fs[0].Snapshot) != "v1" {
		t.Fatalf("seed snapshot = %q, want v1", fs[0].Snapshot)
	}

	// the artifact vanishes entirely — true-mirror sync with an empty set.
	if err := st.SyncArtifacts(j.ID, nil, nil); err != nil {
		t.Fatal(err)
	}

	rec2 := postFeedback(t, feedbackReq{Slug: "fb-edit-fallback", ID: id, Feedback: "second"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("edit: status %d, body %s", rec2.Code, rec2.Body.String())
	}

	got, err := st.GetFeedback(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "second" {
		t.Fatalf("note not updated: %+v", got)
	}
	if string(got.Snapshot) != "v1" {
		t.Fatalf("snapshot must survive an unresolvable artifact, got %q, want v1", got.Snapshot)
	}
}

func TestFeedbackEditEmptyNote(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-edit-empty", "a.md", "v1")
	postFeedback(t, feedbackReq{Slug: "fb-edit-empty", Home: "pluto", Name: "a.md", Feedback: "first"})
	fs, _ := st.ListFeedback(j.ID)

	rec := postFeedback(t, feedbackReq{Slug: "fb-edit-empty", ID: fs[0].ID, Feedback: "   "})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

func TestFeedbackEditUnknownID(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "fb-edit-unknown", Repo: "monolith", State: "done"})

	rec := postFeedback(t, feedbackReq{Slug: "fb-edit-unknown", ID: 999, Feedback: "x"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// --- B1: cross-slug refusal on edit/delete/addressed ---

func TestFeedbackEditRefusesCrossSlug(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-cross-a", "a.md", "v1")
	mustCreateStoreJob(t, st, store.Job{Slug: "fb-cross-b", Repo: "monolith", State: "done"})

	postFeedback(t, feedbackReq{Slug: "fb-cross-a", Home: "pluto", Name: "a.md", Feedback: "note"})
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID

	rec := postFeedback(t, feedbackReq{Slug: "fb-cross-b", ID: id, Feedback: "hijacked"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-slug edit: status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetFeedback(id)
	if err != nil || got.Note != "note" {
		t.Fatalf("row mutated by a cross-slug edit: %+v (err %v)", got, err)
	}
}

func TestFeedbackDeleteRefusesCrossSlug(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-cross-del-a", "a.md", "v1")
	mustCreateStoreJob(t, st, store.Job{Slug: "fb-cross-del-b", Repo: "monolith", State: "done"})

	postFeedback(t, feedbackReq{Slug: "fb-cross-del-a", Home: "pluto", Name: "a.md", Feedback: "note"})
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID

	rec := postFeedback(t, feedbackReq{Slug: "fb-cross-del-b", ID: id, Delete: true})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-slug delete: status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetFeedback(id); err != nil {
		t.Fatalf("row deleted by a cross-slug delete request: %v", err)
	}
}

func TestFeedbackAddressedRefusesCrossSlug(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-cross-addr-a", "a.md", "v1")
	mustCreateStoreJob(t, st, store.Job{Slug: "fb-cross-addr-b", Repo: "monolith", State: "done"})

	postFeedback(t, feedbackReq{Slug: "fb-cross-addr-a", Home: "pluto", Name: "a.md", Feedback: "note"})
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID

	rec := postFeedback(t, feedbackReq{Slug: "fb-cross-addr-b", ID: id, Addressed: strPtr("fixed it")})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-slug addressed: status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetFeedback(id)
	if err != nil || got.Addressed != "" {
		t.Fatalf("row addressed by a cross-slug request: %+v (err %v)", got, err)
	}
}

func TestFeedbackEditRefusesUnknownSlug(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-slugless", "a.md", "v1")
	postFeedback(t, feedbackReq{Slug: "fb-slugless", Home: "pluto", Name: "a.md", Feedback: "note"})
	fs, _ := st.ListFeedback(j.ID)

	rec := postFeedback(t, feedbackReq{Slug: "totally-nonexistent", ID: fs[0].ID, Feedback: "hijack attempt"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// --- delete ---

func TestFeedbackDelete(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-delete", "a.md", "v1")
	postFeedback(t, feedbackReq{Slug: "fb-delete", Home: "pluto", Name: "a.md", Feedback: "note"})
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID

	rec := postFeedback(t, feedbackReq{Slug: "fb-delete", ID: id, Delete: true})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetFeedback(id); err == nil {
		t.Fatal("row still present after delete")
	}
}

func TestFeedbackDeleteUnknownID(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "fb-delete-unknown", Repo: "monolith", State: "done"})

	rec := postFeedback(t, feedbackReq{Slug: "fb-delete-unknown", ID: 999, Delete: true})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// --- addressed (the nightly dream pass' form) ---

func TestFeedbackAddressedSetsAndClears(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-addressed", "a.md", "v1")
	postFeedback(t, feedbackReq{Slug: "fb-addressed", Home: "pluto", Name: "a.md", Feedback: "note"})
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID

	rec := postFeedback(t, feedbackReq{Slug: "fb-addressed", ID: id, Addressed: strPtr("  fixed it  ")})
	if rec.Code != http.StatusOK {
		t.Fatalf("set addressed: status %d, body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetFeedback(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Addressed != "fixed it" {
		t.Fatalf("addressed = %q, want trimmed \"fixed it\"", got.Addressed)
	}
	if got.AddressedAt == "" {
		t.Fatal("addressed_at not stamped")
	}
	if got.Note != "note" {
		t.Fatalf("addressed form must not touch note, got %q", got.Note)
	}

	// non-blocking#9: an explicitly empty addressed clears it — the key is
	// PRESENT (Addressed is a non-nil pointer to ""), distinct from the key
	// being absent entirely (which routes to the edit form instead).
	rec2 := postFeedback(t, feedbackReq{Slug: "fb-addressed", ID: id, Addressed: strPtr("")})
	if rec2.Code != http.StatusOK {
		t.Fatalf("clear addressed: status %d, body %s", rec2.Code, rec2.Body.String())
	}
	got2, err := st.GetFeedback(id)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Addressed != "" || got2.AddressedAt != "" {
		t.Fatalf("addressed not cleared: %+v", got2)
	}
}

// TestFeedbackRequestWithoutAddressedKeyIsEditNotAddressed pins the
// dispatch distinction non-blocking#9 fixes: {id, addressed:""} used to
// fall into the edit branch (wrong field named "feedback is empty") because
// the old plain-string Addressed field couldn't tell "absent" from "empty"
// apart. Omitting the key entirely must still route to edit, and an edit
// with no feedback text must 400 as an edit ("feedback is empty"), not
// silently succeed as an address-clear.
func TestFeedbackRequestWithoutAddressedKeyIsEditNotAddressed(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-no-addressed-key", "a.md", "v1")
	postFeedback(t, feedbackReq{Slug: "fb-no-addressed-key", Home: "pluto", Name: "a.md", Feedback: "note"})
	fs, _ := st.ListFeedback(j.ID)

	rec := postFeedback(t, feedbackReq{Slug: "fb-no-addressed-key", ID: fs[0].ID, Feedback: "  "})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (edit form, empty note), body %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec); !strings.Contains(got, "feedback is empty") {
		t.Fatalf("error = %q, want an edit-form empty-note error", got)
	}
}

// TestFeedbackAddressedWithFeedbackRejected pins the two-forms-at-once
// refusal: {id, addressed, feedback} matches the addressed branch, which
// writes ONLY the zeus-owned columns — so honoring it would return 200
// having silently discarded the note edit the same request asked for.
func TestFeedbackAddressedWithFeedbackRejected(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-both-forms", "a.md", "v1")
	postFeedback(t, feedbackReq{Slug: "fb-both-forms", Home: "pluto", Name: "a.md", Feedback: "original note"})
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID

	rec := postFeedback(t, feedbackReq{
		Slug: "fb-both-forms", ID: id,
		Addressed: strPtr("did the thing"), Feedback: "edited note",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	// and neither field may have been written
	got, err := st.GetFeedback(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "original note" || got.Addressed != "" {
		t.Fatalf("refused request still wrote: %+v", got)
	}
}

// TestFeedbackAddressedTooLong pins feedbackNoteMaxLen on the ADDRESSED
// field too (non-blocking#4): addressed rides every /api/board poll via
// ListFeedbackMeta exactly like note does, and its writer is zeus (an LLM),
// so it needs the same cap. Boundary: 8000 runes accepted, 8001 refused —
// and the clear form (an explicitly empty value) stays legal.
func TestFeedbackAddressedTooLong(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "fb-addressed-toolong", "a.md", "v1")
	postFeedback(t, feedbackReq{Slug: "fb-addressed-toolong", Home: "pluto", Name: "a.md", Feedback: "note"})
	fs, _ := st.ListFeedback(j.ID)
	id := fs[0].ID

	ok := strings.Repeat("a", feedbackNoteMaxLen)
	rec := postFeedback(t, feedbackReq{Slug: "fb-addressed-toolong", ID: id, Addressed: &ok})
	if rec.Code != http.StatusOK {
		t.Fatalf("%d-rune addressed should be accepted, got status %d body %s", feedbackNoteMaxLen, rec.Code, rec.Body.String())
	}

	tooLong := strings.Repeat("a", feedbackNoteMaxLen+1)
	rec2 := postFeedback(t, feedbackReq{Slug: "fb-addressed-toolong", ID: id, Addressed: &tooLong})
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("%d-rune addressed: status %d, want 400, body %s", feedbackNoteMaxLen+1, rec2.Code, rec2.Body.String())
	}
	got, err := st.GetFeedback(id)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(got.Addressed)) != feedbackNoteMaxLen {
		t.Fatalf("oversize addressed was stored anyway (len %d)", len([]rune(got.Addressed)))
	}

	// the cap counts RUNES, not bytes: 8000 multi-byte runes (32000 bytes)
	// is still within the cap, which is the whole reason the doc wording
	// matters (non-blocking#6)
	multibyte := strings.Repeat("\u00e9", feedbackNoteMaxLen)
	rec3 := postFeedback(t, feedbackReq{Slug: "fb-addressed-toolong", ID: id, Addressed: &multibyte})
	if rec3.Code != http.StatusOK {
		t.Fatalf("%d-rune multibyte addressed: status %d, want 200, body %s", feedbackNoteMaxLen, rec3.Code, rec3.Body.String())
	}

	// clearing is still legal — an empty value is a deliberate clear, not a
	// cap violation
	rec4 := postFeedback(t, feedbackReq{Slug: "fb-addressed-toolong", ID: id, Addressed: strPtr("")})
	if rec4.Code != http.StatusOK {
		t.Fatalf("clear addressed: status %d, body %s", rec4.Code, rec4.Body.String())
	}
}

func TestFeedbackAddressedUnknownID(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "fb-addressed-unknown", Repo: "monolith", State: "done"})

	rec := postFeedback(t, feedbackReq{Slug: "fb-addressed-unknown", ID: 999, Addressed: strPtr("x")})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// --- request-shape errors ---

func TestFeedbackPostBadJSON(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	feedbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// TestFeedbackPostOversizeBody413 covers non-blocking#7: a body over the
// 1MB MaxBytesReader cap is a 413, distinct from an ordinary malformed-JSON
// 400.
func TestFeedbackPostOversizeBody413(t *testing.T) {
	useTestStore(t)
	huge := `{"slug":"x","home":"pluto","name":"a.md","feedback":"` + strings.Repeat("a", 2<<20) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/feedback", strings.NewReader(huge))
	rec := httptest.NewRecorder()
	feedbackHandler(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413, body %s", rec.Code, rec.Body.String())
	}
}

// --- GET /api/feedback ---

// TestFeedbackGetEmptyArray pins B3: a job with no feedback rows yet must
// serve {"feedback":[]}, never {"feedback":null}.
func TestFeedbackGetEmptyArray(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "fb-get-empty", Repo: "monolith", State: "done"})

	req := httptest.NewRequest(http.MethodGet, "/api/feedback?slug=fb-get-empty", nil)
	rec := httptest.NewRecorder()
	feedbackHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"feedback":[]}` {
		t.Fatalf("body = %q, want an empty array, never null", got)
	}
}

// TestFeedbackGetUnknownJob404 covers non-blocking#6: a typo'd slug must
// 404, not read as "no feedback yet" (200 {"feedback":null}) — the cell
// verbs rely on a non-zero exit to distinguish the two.
func TestFeedbackGetUnknownJob404(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/feedback?slug=no-such-job", nil)
	rec := httptest.NewRecorder()
	feedbackHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestFeedbackGetMissingSlug(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/feedback", nil)
	rec := httptest.NewRecorder()
	feedbackHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

func TestFeedbackGetPopulated(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	seedPlutoArtifact(t, st, "fb-get-populated", "a.md", "v1")
	postFeedback(t, feedbackReq{Slug: "fb-get-populated", Home: "pluto", Name: "a.md", Feedback: "note"})

	req := httptest.NewRequest(http.MethodGet, "/api/feedback?slug=fb-get-populated", nil)
	rec := httptest.NewRecorder()
	feedbackHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Feedback []webFeedback `json:"feedback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Feedback) != 1 || resp.Feedback[0].Note != "note" {
		t.Fatalf("feedback = %+v", resp.Feedback)
	}
}

// --- job_id keying: reads and ownership survive a live slug reuse ---
//
// The verification pass on the first job_id fix (schema v5) found it was
// WRITE-side only: ArchiveJob/UnarchiveJob rekey job_slug via job_id, but
// every READ path (ListFeedback/ListFeedbackMeta) and the ownership check
// (lookupOwnedFeedback) still keyed on job_slug — the very string that two
// generations of a reused slug share for as long as the newer one stays
// active. The tests below exercise exactly that window: gen1 archived under
// the bare slug (no numbering needed yet — the first archival of any slug
// never collides), gen2 immediately reusing the freed slug as a brand new
// ACTIVE job, both cards' feedback rows sharing an identical job_slug column
// value the whole time.

// seedArchivedThenReuse builds the collision fixture shared by the tests
// below: gen1 (artifact "a.md") archives under the bare slug, then gen2
// (artifact "b.md") is created fresh under the SAME slug string, live and
// active, while gen1 stays archived. Returns gen1's job, gen1's archived
// name (== slug, since it's the first archival), and gen2's job.
func seedArchivedThenReuse(t *testing.T, st *store.Store, slug string) (gen1 *jobs.Job, archivedName string, gen2 *jobs.Job) {
	t.Helper()
	gen1 = seedPlutoArtifact(t, st, slug, "a.md", "gen1 bytes")
	name, err := st.ArchiveJob(gen1.ID)
	if err != nil {
		t.Fatalf("ArchiveJob gen1: %v", err)
	}
	if name != slug {
		t.Fatalf("archived name = %q, want the bare slug %q (first archival, no collision yet)", name, slug)
	}
	gen2 = seedPlutoArtifact(t, st, slug, "b.md", "gen2 bytes")
	return gen1, name, gen2
}

// TestFeedbackReadsSegregateActiveFromArchivedGenerationSharingSlug is the
// read-side repro: with gen1 archived and gen2 active, BOTH under the
// literal slug string "shared-slug", the active job's GET must return only
// gen2's rows and the archived card's own read (board.go's jobFeedback,
// exercised directly here since it's the same api-package function the
// board build calls) must return only gen1's — never either bleeding into
// the other.
func TestFeedbackReadsSegregateActiveFromArchivedGenerationSharingSlug(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	gen1, archivedName, gen2 := seedArchivedThenReuse(t, st, "shared-slug")

	if rec := postFeedback(t, feedbackReq{Slug: "shared-slug", Home: "archive", Name: "a.md", Feedback: "gen1 note"}); rec.Code != http.StatusOK {
		t.Fatalf("create on archived gen1: status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec := postFeedback(t, feedbackReq{Slug: "shared-slug", Home: "pluto", Name: "b.md", Feedback: "gen2 note"}); rec.Code != http.StatusOK {
		t.Fatalf("create on active gen2: status %d, body %s", rec.Code, rec.Body.String())
	}

	// (a) the active job's GET returns ONLY gen2.
	req := httptest.NewRequest(http.MethodGet, "/api/feedback?slug=shared-slug", nil)
	rec := httptest.NewRecorder()
	feedbackHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Feedback []webFeedback `json:"feedback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Feedback) != 1 || resp.Feedback[0].Note != "gen2 note" {
		t.Fatalf("active job's GET /api/feedback = %+v, want only gen2's row", resp.Feedback)
	}

	// (b) the archived card's own read (board.go's jobFeedback) returns
	// ONLY gen1.
	archived := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), archivedName)
	if archived == nil {
		t.Fatalf("archived job %q not found", archivedName)
	}
	archFeedback := jobFeedback(st, archived.ID)
	if len(archFeedback) != 1 || archFeedback[0].Note != "gen1 note" {
		t.Fatalf("archived card's jobFeedback = %+v, want only gen1's row", archFeedback)
	}

	// sanity: two genuinely distinct job rows, both still readable straight
	// by id even though their job_slug columns are identical strings.
	if gen1.ID == gen2.ID {
		t.Fatalf("gen1/gen2 must be distinct job rows, both got id %d", gen1.ID)
	}
	if fs1, err := st.ListFeedback(gen1.ID); err != nil || len(fs1) != 1 || fs1[0].Note != "gen1 note" {
		t.Fatalf("store-level ListFeedback(gen1.ID) = %+v, err %v", fs1, err)
	}
	if fs2, err := st.ListFeedback(gen2.ID); err != nil || len(fs2) != 1 || fs2[0].Note != "gen2 note" {
		t.Fatalf("store-level ListFeedback(gen2.ID) = %+v, err %v", fs2, err)
	}
}

// TestFeedbackMutationRefusesCrossGenerationSameSlug is the ownership-side
// repro: gen1 (archived) and gen2 (active) share the literal slug string
// "shared-mut-slug". An edit/delete/addressed request naming the ACTIVE
// job's slug but gen1's row id must 404 — the pre-fix job_slug-string
// comparison in lookupOwnedFeedback let it through, since
// feedbackJobKey(gen2) == gen1's stored job_slug for as long as gen2 stays
// active.
func TestFeedbackMutationRefusesCrossGenerationSameSlug(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	gen1, _, _ := seedArchivedThenReuse(t, st, "shared-mut-slug")

	if rec := postFeedback(t, feedbackReq{Slug: "shared-mut-slug", Home: "archive", Name: "a.md", Feedback: "gen1 note"}); rec.Code != http.StatusOK {
		t.Fatalf("create on archived gen1: status %d, body %s", rec.Code, rec.Body.String())
	}
	fs1, err := st.ListFeedback(gen1.ID)
	if err != nil || len(fs1) != 1 {
		t.Fatalf("gen1 feedback = %+v, err %v", fs1, err)
	}
	gen1RowID := fs1[0].ID

	// edit
	rec := postFeedback(t, feedbackReq{Slug: "shared-mut-slug", ID: gen1RowID, Feedback: "hijacked"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-generation edit (same slug string): status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
	// delete
	rec = postFeedback(t, feedbackReq{Slug: "shared-mut-slug", ID: gen1RowID, Delete: true})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-generation delete (same slug string): status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
	// addressed
	rec = postFeedback(t, feedbackReq{Slug: "shared-mut-slug", ID: gen1RowID, Addressed: strPtr("fixed it")})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-generation addressed (same slug string): status %d, want 404, body %s", rec.Code, rec.Body.String())
	}

	got, err := st.GetFeedback(gen1RowID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "gen1 note" || got.Addressed != "" {
		t.Fatalf("gen1's row was mutated by a same-slug-string request targeting the active generation: %+v", got)
	}
}

// --- GET /api/feedback/sweep (the dream loop's cross-job read) ---

func getFeedbackSweep(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/feedback/sweep"+query, nil)
	rec := httptest.NewRecorder()
	feedbackSweepHandler(rec, req)
	return rec
}

// TestFeedbackSweepIsCrossJobAndFloored pins the two things that make this a
// sweep rather than a second /api/feedback: it spans jobs (each row naming
// its own), and `since` is an inclusive floor.
func TestFeedbackSweepIsCrossJobAndFloored(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	a := mustCreateStoreJob(t, st, store.Job{Slug: "sweep-a", Repo: "monolith"})
	b := mustCreateStoreJob(t, st, store.Job{Slug: "sweep-b", Repo: "monolith"})
	if _, err := st.AddFeedback(store.Feedback{JobID: a.ID, JobSlug: "sweep-a", Artifact: "00-overview.md", Note: "old", TS: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: a.ID, JobSlug: "sweep-a", Artifact: "10-x.md", Note: "at the floor", TS: 2000}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: b.ID, JobSlug: "sweep-b", Artifact: "50-y.md", Note: "other job", TS: 3000}); err != nil {
		t.Fatal(err)
	}

	rec := getFeedbackSweep(t, "?since=2000")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Feedback []webSweepFeedback `json:"feedback"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Feedback) != 2 {
		t.Fatalf("swept %d rows, want the 2 at/after the floor: %+v", len(resp.Feedback), resp.Feedback)
	}
	if resp.Feedback[0].Job != "sweep-a" || resp.Feedback[1].Job != "sweep-b" {
		t.Fatalf("rows must name their own job and stay ts-ordered: %+v", resp.Feedback)
	}
	if resp.Feedback[0].SnapshotURL != feedbackSnapshotURL(resp.Feedback[0].ID) {
		t.Fatalf("snapshot_url = %q", resp.Feedback[0].SnapshotURL)
	}
}

// TestFeedbackSweepNeverNull: an empty window must serve "[]", the same B3
// convention feedbackGet follows.
func TestFeedbackSweepNeverNull(t *testing.T) {
	useTestStore(t)
	rec := getFeedbackSweep(t, "?since=99999999")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"feedback":[]`) {
		t.Fatalf("body = %s, want an empty array", rec.Body.String())
	}
}

// TestFeedbackSweepRequiresNumericSince pins the deliberate absence of a
// server-side default window: a missing or unparseable floor is a 400, never
// a silent "everything" that would re-mine months of settled notes.
func TestFeedbackSweepRequiresNumericSince(t *testing.T) {
	useTestStore(t)
	for _, q := range []string{"", "?since=", "?since=yesterday", "?since=12.5"} {
		rec := getFeedbackSweep(t, q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: status %d, want 400, body %s", q, rec.Code, rec.Body.String())
		}
	}
}

func TestFeedbackSweepMethodNotAllowed(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/feedback/sweep?since=0", nil)
	rec := httptest.NewRecorder()
	feedbackSweepHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

// --- GET /api/feedback/snapshot (the frozen blob's first read path) ---

func getFeedbackSnapshot(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/feedback/snapshot"+query, nil)
	rec := httptest.NewRecorder()
	feedbackSnapshotHandler(rec, req)
	return rec
}

func TestFeedbackSnapshotServesFrozenBytes(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := seedPlutoArtifact(t, st, "snap-job", "00-overview.md", "the bytes carl saw")
	if rec := postFeedback(t, feedbackReq{Slug: "snap-job", Home: "pluto", Name: "00-overview.md", Feedback: "note"}); rec.Code != http.StatusOK {
		t.Fatalf("create: status %d, body %s", rec.Code, rec.Body.String())
	}
	fs, err := st.ListFeedback(j.ID)
	if err != nil || len(fs) != 1 {
		t.Fatalf("ListFeedback: %+v, err %v", fs, err)
	}

	rec := getFeedbackSnapshot(t, "?id="+strconv.FormatInt(fs[0].ID, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "the bytes carl saw" {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment;") {
		t.Fatalf("Content-Disposition = %q, want an attachment (a snapshot must never be a top-level document)", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

// TestFeedbackSnapshotHTMLIsInert is the security-shaped half of this route:
// an .html artifact's frozen bytes are untrusted, replayed from the board's
// OWN origin — so they must never be served as a document type a browser
// would execute.
func TestFeedbackSnapshotHTMLIsInert(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id, err := st.AddFeedback(store.Feedback{
		JobSlug: "x", Artifact: "diagram.html", Note: "n",
		Snapshot: []byte("<script>alert(1)</script>"), SnapshotExt: "html",
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := getFeedbackSnapshot(t, "?id="+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); strings.Contains(ct, "html") {
		t.Fatalf("Content-Type = %q — an html snapshot must be declawed to text/plain", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Fatalf("Content-Security-Policy = %q, want a sandbox", csp)
	}
}

// TestFeedbackSnapshotEmptyBlobIs404 pins the distinction the brief calls
// for: "this row has no frozen copy" is not "here is an empty frozen copy".
func TestFeedbackSnapshotEmptyBlobIs404(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id, err := st.AddFeedback(store.Feedback{JobSlug: "x", Artifact: "a.md", Note: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if rec := getFeedbackSnapshot(t, "?id="+strconv.FormatInt(id, 10)); rec.Code != http.StatusNotFound {
		t.Fatalf("empty blob: status %d, want 404", rec.Code)
	}
	if rec := getFeedbackSnapshot(t, "?id="+strconv.FormatInt(id+999, 10)); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status %d, want 404", rec.Code)
	}
}

func TestFeedbackSnapshotBadIDIs400(t *testing.T) {
	useTestStore(t)
	for _, q := range []string{"", "?id=", "?id=abc"} {
		if rec := getFeedbackSnapshot(t, q); rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: status %d, want 400", q, rec.Code)
		}
	}
}

// TestSnapshotExtOKRejectsHostileExtensions: snapshot_ext is stored data that
// this handler interpolates into a filename and a mime lookup, so anything
// that isn't a plain alphanumeric extension is simply dropped.
func TestSnapshotExtOKRejectsHostileExtensions(t *testing.T) {
	for _, ok := range []string{"md", "png", "html", "JPEG"} {
		if !snapshotExtOK(ok) {
			t.Errorf("snapshotExtOK(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "../etc", `md"`, "a/b", "waytoolongextension", "m d"} {
		if snapshotExtOK(bad) {
			t.Errorf("snapshotExtOK(%q) = true, want false", bad)
		}
	}
}
