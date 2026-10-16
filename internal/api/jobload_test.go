package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// jobload_test.go — GET/POST /api/job/load. Store-backed throughout
// (useTestStore): the estimate has no frontmatter spelling at all.

// seedLoadJob wires a temp store and creates one active job, returning it.
func seedLoadJob(t *testing.T, slug string) *store.Store {
	t.Helper()
	st := useTestStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken"}); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return st
}

func postLoad(t *testing.T, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/job/load", bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	jobLoadHandler(rec, req)
	return rec
}

func getLoad(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/job/load"+query, nil)
	rec := httptest.NewRecorder()
	jobLoadHandler(rec, req)
	return rec
}

// loadOnRow reads the estimate back out of the store rather than off the
// response, so every assertion here is about what was actually persisted.
func loadOnRow(t *testing.T, st *store.Store, slug string) store.JobLoad {
	t.Helper()
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug %s: %v", slug, err)
	}
	return j.Load
}

func TestJobLoadPostSetsAndClears(t *testing.T) {
	st := seedLoadJob(t, "triage")

	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 2, "scope": 8, "note": "one decision"}); rec.Code != http.StatusOK {
		t.Fatalf("set: status %d, body %s", rec.Code, rec.Body.String())
	}
	got := loadOnRow(t, st, "triage")
	if got.Unblock != 2 || got.Scope != 8 || got.Note != "one decision" {
		t.Fatalf("stored load = %+v", got)
	}
	if got.At <= 0 {
		t.Fatalf("at = %d, want a server-stamped millis value", got.At)
	}

	// re-estimating replaces rather than accumulating
	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 8, "scope": 2}); rec.Code != http.StatusOK {
		t.Fatalf("re-estimate: status %d, body %s", rec.Code, rec.Body.String())
	}
	got = loadOnRow(t, st, "triage")
	if got.Unblock != 8 || got.Scope != 2 || got.Note != "" {
		t.Fatalf("re-estimated load = %+v", got)
	}

	// both zero is the clear, not a rejected value — and it drops the note
	// with the numbers, since a rationale with nothing to explain is a
	// leftover rather than a smaller estimate
	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 0, "scope": 0, "note": "stale"}); rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got = loadOnRow(t, st, "triage"); (got != store.JobLoad{}) {
		t.Fatalf("cleared load = %+v, want the zero JobLoad", got)
	}
}

// TestJobLoadPostRejectsHalfEstimate is the validation this endpoint exists
// to enforce: roi is scope/unblock, so one number alone has no ratio and
// there is no honest way to coerce it into one.
func TestJobLoadPostRejectsHalfEstimate(t *testing.T) {
	st := seedLoadJob(t, "triage")

	for _, body := range []map[string]any{
		{"slug": "triage", "unblock": 3, "scope": 0},
		{"slug": "triage", "unblock": 0, "scope": 3},
	} {
		rec := postLoad(t, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%v: status %d, want 400", body, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "roi") {
			t.Errorf("%v: error %q, want it to say why a half estimate is refused", body, rec.Body.String())
		}
		if got := loadOnRow(t, st, "triage"); (got != store.JobLoad{}) {
			t.Fatalf("%v was written anyway: %+v", body, got)
		}
	}
}

func TestJobLoadPostRejectsOutOfRange(t *testing.T) {
	st := seedLoadJob(t, "triage")

	for _, body := range []map[string]any{
		{"slug": "triage", "unblock": 11, "scope": 5},
		{"slug": "triage", "unblock": 5, "scope": 11},
		{"slug": "triage", "unblock": -1, "scope": -1},
		{"slug": "triage", "unblock": 200, "scope": 200},
	} {
		rec := postLoad(t, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%v: status %d, want 400", body, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "1..10") {
			t.Errorf("%v: error %q, want it to name the valid range", body, rec.Body.String())
		}
		if got := loadOnRow(t, st, "triage"); (got != store.JobLoad{}) {
			t.Fatalf("%v was written anyway: %+v", body, got)
		}
	}
	// the ends of the range are legal, not off-by-one rejections
	for _, n := range []int{1, 10} {
		if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": n, "scope": n}); rec.Code != http.StatusOK {
			t.Fatalf("unblock/scope %d: status %d, want 200", n, rec.Code)
		}
	}
}

// TestJobLoadPostTruncatesAndOneLinesNote: an over-long or multi-line note is
// clipped, never a reason to lose the numbers it was explaining.
func TestJobLoadPostTruncatesAndOneLinesNote(t *testing.T) {
	st := seedLoadJob(t, "triage")

	long := strings.Repeat("z", 500)
	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 2, "scope": 8, "note": long}); rec.Code != http.StatusOK {
		t.Fatalf("long note: status %d, body %s", rec.Code, rec.Body.String())
	}
	got := loadOnRow(t, st, "triage")
	if n := len([]rune(got.Note)); n > jobLoadNoteMax {
		t.Fatalf("note is %d runes, want <= %d", n, jobLoadNoteMax)
	}
	if got.Unblock != 2 || got.Scope != 8 {
		t.Fatalf("numbers lost to a long note: %+v", got)
	}

	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 2, "scope": 8, "note": "first\nsecond\tthird"}); rec.Code != http.StatusOK {
		t.Fatalf("multiline note: status %d", rec.Code)
	}
	got = loadOnRow(t, st, "triage")
	if strings.ContainsAny(got.Note, "\n\t") {
		t.Fatalf("note = %q, want it one-lined", got.Note)
	}
}

func TestJobLoadPostUnknownJob(t *testing.T) {
	seedLoadJob(t, "triage")
	if rec := postLoad(t, map[string]any{"slug": "nope", "unblock": 2, "scope": 8}); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestJobLoadPostRejectsBadJSON(t *testing.T) {
	seedLoadJob(t, "triage")
	req := httptest.NewRequest(http.MethodPost, "/api/job/load", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	jobLoadHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

// TestJobLoadPostRefusesBodiesCarryingNeitherNumber is the reason unblock and
// scope are POINTERS. With plain ints an absent key and an explicit 0 are the
// same value, and since 0/0 is the CLEAR, a body that named NEITHER number
// would have decoded to "wipe this job's estimate" and answered 200 —
// destroying a row while describing something else, and leaving no trace,
// since the write bumps neither rev nor updated_at.
//
// FOUR of the seven bodies below would have done that: the nested shape, the
// note-only edit, the bare slug, and the explicit nulls. The other three
// would not, and they ride this table because the pointer check is now the
// single arm that answers all seven — one-number-alone (two rows) was already
// a 400 as a half estimate, which TestJobLoadPostRejectsHalfEstimate covers
// from the other direction, and bare `null` was a 404 on the empty slug it
// decoded to. Every row here is a body a real caller could plausibly send.
func TestJobLoadPostRefusesBodiesCarryingNeitherNumber(t *testing.T) {
	st := seedLoadJob(t, "triage")
	seeded := store.JobLoad{Unblock: 2, Scope: 8, Note: "seeded", At: 123}

	reseed := func(t *testing.T) {
		t.Helper()
		j, err := st.GetJobBySlug("triage")
		if err != nil {
			t.Fatalf("GetJobBySlug: %v", err)
		}
		if err := st.SetJobLoad(j.ID, seeded); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Raw bodies rather than map fixtures: the whole point is what the JSON
	// literally does or does not carry, which a Go map cannot express.
	for _, body := range []string{
		`{"slug":"triage","estimate":{"unblock":2,"scope":8}}`,
		`{"slug":"triage","note":"just fixing the wording"}`,
		`{"slug":"triage"}`,
		`{"slug":"triage","unblock":null,"scope":null}`,
		`{"slug":"triage","unblock":2}`,
		`{"slug":"triage","scope":8}`,
		`null`,
	} {
		reseed(t)
		req := httptest.NewRequest(http.MethodPost, "/api/job/load", strings.NewReader(body))
		rec := httptest.NewRecorder()
		jobLoadHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
		if got := loadOnRow(t, st, "triage"); got != seeded {
			t.Fatalf("%s: row was written anyway: %+v", body, got)
		}
	}
}

// TestJobLoadPostRejectsUnknownFields pins DisallowUnknownFields, and pins
// that the refusal does NOT echo the offending key — the key comes from the
// body, so quoting it would let a client choose the response size.
//
// It also asserts the MESSAGE, not just the 400. The handler tells an
// unknown-field error apart from malformed JSON by a substring test on
// encoding/json's prose (there is no typed error for it), and its comment
// says a stdlib rewording would silently degrade the message to the generic
// "bad json". Without this assertion that degradation is invisible — the
// status and the body-length bound both hold either way.
func TestJobLoadPostRejectsUnknownFields(t *testing.T) {
	st := seedLoadJob(t, "triage")

	for _, body := range []string{
		`{"slug":"triage","unblock":2,"scope":8,"at":999}`,
		`{"slug":"triage","unblock":2,"scope":8,"unblok":3}`,
		`{"slug":"triage","unblock":2,"scope":8,"` + strings.Repeat("z", 4000) + `":1}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/job/load", strings.NewReader(body))
		rec := httptest.NewRecorder()
		jobLoadHandler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400 for an unknown field", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "unrecognized field") {
			t.Errorf("400 body = %s, want the unknown-field message rather than the generic decode error", rec.Body.String())
		}
		if n := rec.Body.Len(); n > 400 {
			t.Errorf("400 body is %d bytes — the rejected key is being echoed", n)
		}
		if got := loadOnRow(t, st, "triage"); (got != store.JobLoad{}) {
			t.Fatalf("unknown-field body wrote an estimate: %+v", got)
		}
	}
}

// TestJobLoadPostAcceptsCaseVariantKeys is the honest other half of the
// comment on DisallowUnknownFields: encoding/json matches struct fields
// case-insensitively and the option only fires when NOTHING matches, so a
// case variant is a valid request rather than a refusal. Pinned so the
// comment's claim is checkable and so a future switch to a stricter decoder
// is a visible decision rather than a silent behaviour change.
func TestJobLoadPostAcceptsCaseVariantKeys(t *testing.T) {
	st := seedLoadJob(t, "triage")

	req := httptest.NewRequest(http.MethodPost, "/api/job/load", strings.NewReader(`{"Slug":"triage","Unblock":2,"SCOPE":8}`))
	rec := httptest.NewRecorder()
	jobLoadHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 — a case variant matches its field, it is not an unknown key (body %s)", rec.Code, rec.Body.String())
	}
	got := loadOnRow(t, st, "triage")
	if got.Unblock != 2 || got.Scope != 8 {
		t.Fatalf("stored load = %+v, want the values to land under the right fields", got)
	}
}

// TestJobLoadPostRefusesOversizeBody matches the package norm (peers.go,
// feedback.go, questions.go, jobcolor.go): the whole legitimate request is
// two small numbers and one short line, so a huge one is refused rather than
// read into memory, and its 413 stays distinct from a decode error's 400.
func TestJobLoadPostRefusesOversizeBody(t *testing.T) {
	st := seedLoadJob(t, "triage")

	rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 2, "scope": 8, "note": strings.Repeat("z", 2<<20)})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", rec.Code)
	}
	if got := loadOnRow(t, st, "triage"); (got != store.JobLoad{}) {
		t.Fatalf("oversize body wrote an estimate: %+v", got)
	}
}

func TestJobLoadRejectsOtherMethods(t *testing.T) {
	seedLoadJob(t, "triage")
	req := httptest.NewRequest(http.MethodDelete, "/api/job/load?slug=triage", nil)
	rec := httptest.NewRecorder()
	jobLoadHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

func TestJobLoadGet(t *testing.T) {
	seedLoadJob(t, "triage")

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

	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 2, "scope": 8, "note": "why"}); rec.Code != http.StatusOK {
		t.Fatalf("set: status %d, body %s", rec.Code, rec.Body.String())
	}
	rec = getLoad(t, "?slug=triage")
	if rec.Code != http.StatusOK {
		t.Fatalf("estimated: status %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeLoad(t, rec)
	if got == nil || got.Unblock != 2 || got.Scope != 8 || got.Note != "why" || got.At <= 0 {
		t.Fatalf("estimated load = %+v", got)
	}

	// a clear puts it back to null rather than to a zeroed object
	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 0, "scope": 0}); rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d", rec.Code)
	}
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

// TestJobLoadWriteLeavesRevAndModTime pins the reason this write bypasses
// WriteJobFields at every layer above the store: zeus rewrites the estimate
// every turn, so a bumped updated_at would reset the card's age on the board
// and a bumped rev would stale an open editor save.
func TestJobLoadWriteLeavesRevAndModTime(t *testing.T) {
	st := seedLoadJob(t, "triage")
	before, err := st.GetJobBySlug("triage")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}

	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 4, "scope": 6}); rec.Code != http.StatusOK {
		t.Fatalf("set: status %d, body %s", rec.Code, rec.Body.String())
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
	wj := archivedWebJob(a, st, tokens, nil)
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
	seedLoadJob(t, "triage")
	if rec := postLoad(t, map[string]any{"slug": "triage", "unblock": 2, "scope": 8, "note": "why"}); rec.Code != http.StatusOK {
		t.Fatalf("set: status %d, body %s", rec.Code, rec.Body.String())
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), "triage")
	if j == nil {
		t.Fatal("fixture job vanished")
	}
	got := webJobLoadFrom(j.Load)
	if got == nil {
		t.Fatal("an estimated active job produced no wire object")
	}
	if got.Unblock != 2 || got.Scope != 8 || got.Note != "why" || got.At <= 0 {
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
