package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// jobload_test.go — GET /api/job/load. Store-backed throughout
// (useTestStore): the estimate has no frontmatter spelling at all, and since
// the daemon's load judge became its one writer the route has no POST, so
// every estimate here is written through the store the way the judge writes
// it (jobs.SetJobLoad / store.SetJobLoad).

// seedLoadJob wires a temp store and creates one active job, returning it.
func seedLoadJob(t *testing.T, slug string) *store.Store {
	t.Helper()
	st := useTestStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken"}); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return st
}

func getLoad(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/job/load"+query, nil)
	rec := httptest.NewRecorder()
	jobLoadHandler(rec, req)
	return rec
}

// writeLoad lands an estimate on the row the way the judge does.
func writeLoad(t *testing.T, st *store.Store, slug string, l store.JobLoad) {
	t.Helper()
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug %s: %v", slug, err)
	}
	if err := st.SetJobLoad(j.ID, l); err != nil {
		t.Fatalf("SetJobLoad %s: %v", slug, err)
	}
}

// TestJobLoadRejectsWrites pins the route's read-only shape: the estimate is
// the daemon's judgment, and a client that still posts one (an old cell's
// `kraken load set`) is refused with the reason rather than written, and
// the row it named is untouched.
func TestJobLoadRejectsWrites(t *testing.T) {
	st := seedLoadJob(t, "triage")
	seeded := store.JobLoad{Unblock: 2, Scope: 8, Note: "seeded", At: 123}
	writeLoad(t, st, "triage", seeded)

	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		req := httptest.NewRequest(method, "/api/job/load", nil)
		rec := httptest.NewRecorder()
		jobLoadHandler(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", method, rec.Code)
		}
	}
	j, err := st.GetJobBySlug("triage")
	if err != nil {
		t.Fatal(err)
	}
	if j.Load != seeded {
		t.Fatalf("row moved under a refused method: %+v", j.Load)
	}
}

func TestJobLoadGet(t *testing.T) {
	st := seedLoadJob(t, "triage")

	decodeLoad := func(t *testing.T, rec *httptest.ResponseRecorder) *webJobLoad {
		t.Helper()
		var envelope struct {
			Load *webJobLoad `json:"load"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return envelope.Load
	}

	// absent: the key is present and null, so "no estimate" is stated rather
	// than left to be inferred from a missing key
	rec := getLoad(t, "?slug=triage")
	if rec.Code != http.StatusOK {
		t.Fatalf("unestimated: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeLoad(t, rec); got != nil {
		t.Fatalf("unestimated load = %+v, want null", got)
	}

	writeLoad(t, st, "triage", store.JobLoad{Unblock: 2, Scope: 8, Note: "why", At: 1769040000000})
	rec = getLoad(t, "?slug=triage")
	if rec.Code != http.StatusOK {
		t.Fatalf("estimated: status %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeLoad(t, rec)
	if got == nil || got.Unblock != 2 || got.Scope != 8 || got.Note != "why" || got.At != 1769040000000 {
		t.Fatalf("estimated load = %+v", got)
	}

	// a clear (the zero row) puts it back to null rather than to a zeroed
	// object
	writeLoad(t, st, "triage", store.JobLoad{})
	if got := decodeLoad(t, getLoad(t, "?slug=triage")); got != nil {
		t.Fatalf("cleared load = %+v, want null", got)
	}
}

func TestJobLoadGetMissingAndUnknownSlug(t *testing.T) {
	seedLoadJob(t, "triage")
	if rec := getLoad(t, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing slug: status %d, want 400", rec.Code)
	}
	if rec := getLoad(t, "?slug=nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug: status %d, want 404", rec.Code)
	}
}

// TestJobLoadWriteLeavesRevAndModTime pins the reason the judge's write
// bypasses WriteJobFields at every layer above the store: it lands after
// every turn, so a bumped updated_at would reset the card's age on the board
// and a bumped rev would stale an open editor save.
func TestJobLoadWriteLeavesRevAndModTime(t *testing.T) {
	st := seedLoadJob(t, "triage")
	before, err := st.GetJobBySlug("triage")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), "triage")
	if j == nil {
		t.Fatal("fixture job vanished")
	}
	if err := jobs.SetJobLoad(j, store.JobLoad{Unblock: 4, Scope: 6, Note: "jev: Local smoke, or a credential · A full /aim set draft", At: 1769040000000}); err != nil {
		t.Fatalf("SetJobLoad: %v", err)
	}
	after, err := st.GetJobBySlug("triage")
	if err != nil {
		t.Fatalf("GetJobBySlug after set: %v", err)
	}
	if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("rev/updated_at moved: %d/%d -> %d/%d", before.Rev, before.UpdatedAt, after.Rev, after.UpdatedAt)
	}
}

// TestArchivedWebJobWithholdsLoad pins the board's archived-card decision.
// Without it, adding `Load: webJobLoadFrom(a.Load)` to archivedWebJob —
// undoing a decision documented in thirteen lines there and a sentence in
// CLAUDE.md — is a silent, green one-line edit.
//
// It asserts BOTH halves of that decision on the same row, so the test cannot
// pass merely because the fixture had no estimate to serve: the row's stored
// estimate is real (webJobLoadFrom returns an object for it) and the archived
// card still carries no Load. Tokens is asserted alongside it as the control,
// since the archived card's whole point is that these two neighbouring keys
// are treated OPPOSITELY.
func TestArchivedWebJobWithholdsLoad(t *testing.T) {
	st := seedLoadJob(t, "shelved")
	sr, err := st.GetJobBySlug("shelved")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	estimate := store.JobLoad{Unblock: 2, Scope: 8, Note: "why", At: 1769040000000}
	if err := st.SetJobLoad(sr.ID, estimate); err != nil {
		t.Fatalf("SetJobLoad: %v", err)
	}
	if _, err := st.ArchiveJob(sr.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	archived := jobs.LoadArchivedJobs()
	if len(archived) != 1 {
		t.Fatalf("LoadArchivedJobs = %d jobs, want 1", len(archived))
	}
	a := archived[0]
	// the row really does still carry the estimate — archiving preserves it,
	// so withholding below is a choice rather than an empty column
	if a.Load != estimate {
		t.Fatalf("archived job's load = %+v, want %+v (the columns survive archiving)", a.Load, estimate)
	}
	if webJobLoadFrom(a.Load) == nil {
		t.Fatal("webJobLoadFrom returned nil for a real estimate — the fixture proves nothing")
	}

	tokens := map[int64]store.JobTokens{a.ID: {JobID: a.ID, In: 1, Out: 2}}
	wj := archivedWebJob(a, feedbackIndex(st), artifactPathsIndex(st), tokens, nil)
	if wj.Load != nil {
		t.Fatalf("archived card carries load = %+v, want no key at all", wj.Load)
	}
	if wj.Tokens == nil {
		t.Fatal("archived card dropped tokens — the control for this test just broke")
	}
}

// TestActiveJobCardCarriesLoad is the other half: the estimate a blocked card
// renders reaches the wire at all. buildWebBoard's active loop cannot be
// called from a test — buildWebBoard drags in tmux/process-table discovery
// (LiveSessions and DiscoverAll), the same reason update_test.go and
// shipios_test.go build their board keys without it — so this pins the
// conversion that loop performs, store row through jobs.Job to the wire
// object, on a real round trip rather than a literal.
func TestActiveJobCardCarriesLoad(t *testing.T) {
	st := seedLoadJob(t, "triage")
	writeLoad(t, st, "triage", store.JobLoad{Unblock: 2, Scope: 8, Note: "why", At: 1769040000000})
	j := jobs.JobBySlug(jobs.LoadJobs(), "triage")
	if j == nil {
		t.Fatal("fixture job vanished")
	}
	got := webJobLoadFrom(j.Load)
	if got == nil {
		t.Fatal("an estimated active job produced no wire object")
	}
	if got.Unblock != 2 || got.Scope != 8 || got.Note != "why" || got.At != 1769040000000 {
		t.Fatalf("wire object = %+v", got)
	}
}

// TestJobLoadUpdatesInMemoryJob covers jobs.SetJobLoad's other half: the
// caller's *Job must reflect the write without a reload.
func TestJobLoadUpdatesInMemoryJob(t *testing.T) {
	seedLoadJob(t, "triage")
	j := jobs.JobBySlug(jobs.LoadJobs(), "triage")
	if j == nil {
		t.Fatal("fixture job vanished")
	}
	want := store.JobLoad{Unblock: 3, Scope: 5, Note: "warm", At: 1769040000000}
	if err := jobs.SetJobLoad(j, want); err != nil {
		t.Fatalf("SetJobLoad: %v", err)
	}
	if j.Load != want {
		t.Fatalf("in-memory load = %+v, want %+v", j.Load, want)
	}
}
