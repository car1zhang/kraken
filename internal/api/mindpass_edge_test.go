package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/store"
)

// mindpass_edge_test.go — the EDGE dimension of 50-contract.md slice C's wire
// half (POST /api/mm/propose, POST /api/mm/confirm, GET /api/mm/evidence and
// the three new GET /api/mm/source arms). Written blind from the frozen
// contract: mmpropose.go, mmevidence.go, mm.go and register.go were never
// read in their edited form. Every request goes through the REAL mux
// (Register), so a route registered on the wrong pattern or not mirror-wrapped
// fails here. Refusals and boundaries only; happy paths are athena-logic's.
//
// Every package-level identifier is prefixed `mpe`.

func mpeStore(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	t.Setenv("KRAKEN_THREAD_SESSION", "")
	t.Setenv("KRAKEN_THREAD_ROLE", "")
	st := useTestStore(t)
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	return st
}

func mpeMux() http.Handler {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func mpeDo(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func mpeObj(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not a JSON object (%d): %q", rec.Code, rec.Body.String())
	}
	return m
}

func mpeItemCount(t *testing.T, st *store.Store) int {
	t.Helper()
	items, err := st.ListMMItems(true)
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}

const mpeLead = "sess-mind-lead"

// mpeMindLead makes `mind_last_run.job` name an active mind job whose session
// is mpeLead — what `mindLeadSession` reads.
func mpeMindLead(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.CreateJob(&store.Job{Slug: "mind-2026-10-21", Repo: "kraken", Body: "b", State: "working", Session: mpeLead}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("mind_last_run", `{"date":"2026-10-21","swept_to":1,"job":"mind-2026-10-21"}`); err != nil {
		t.Fatal(err)
	}
}

const mpeValidProposal = `{"title":"the board owns card order","scope":"universal","repo":"","dir":"","kind":"note","body":"b","quote":"the board owns card order","rung":"plain","source":{"kind":"transcript","session":"s1","uuid":"u1"}}`

// ---------------------------------------------------------------------------
// POST /api/mm/propose
// ---------------------------------------------------------------------------

// "`ErrNotMindLead` → 403 `{"error":"not the mind job's lead session"}`" —
// with nothing stored, for every non-lead caller.
func TestMPEProposeRoute403(t *testing.T) {
	st := mpeStore(t)
	mpeMindLead(t, st)
	if _, err := st.CreateJob(&store.Job{Slug: "other-job", Repo: "kraken", Body: "b", State: "working", Session: "sess-other"}); err != nil {
		t.Fatal(err)
	}
	mux := mpeMux()
	for _, sess := range []string{"", "  ", "sess-unknown", "sess-other"} {
		rec := mpeDo(t, mux, http.MethodPost, "/api/mm/propose", `{"session":"`+sess+`","items":[`+mpeValidProposal+`]}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("propose from %q = %d (%s), want 403", sess, rec.Code, rec.Body.String())
			continue
		}
		if got := mpeObj(t, rec); !reflect.DeepEqual(got, map[string]any{"error": "not the mind job's lead session"}) {
			t.Errorf("propose from %q body = %v", sess, got)
		}
	}
	if n := mpeItemCount(t, st); n != 0 {
		t.Fatalf("refused proposes stored %d items", n)
	}
}

// "`ErrCommitInvalid` → 400 with the sentence" — and a batch with one bad item
// stores nothing.
func TestMPEProposeRoute400StoresNothing(t *testing.T) {
	st := mpeStore(t)
	mpeMindLead(t, st)
	mux := mpeMux()
	bad := strings.Replace(mpeValidProposal, `"kind":"note"`, `"kind":"reading"`, 1)
	bad = strings.Replace(bad, `"quote":"the board owns card order"`, `"quote":"another quote"`, 1)
	rec := mpeDo(t, mux, http.MethodPost, "/api/mm/propose", `{"session":"`+mpeLead+`","items":[`+mpeValidProposal+`,`+bad+`]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid batch = %d (%s), want 400", rec.Code, rec.Body.String())
	}
	if e, _ := mpeObj(t, rec)["error"].(string); strings.TrimSpace(e) == "" {
		t.Fatalf("400 body %s carries no error sentence", rec.Body.String())
	}
	if n := mpeItemCount(t, st); n != 0 {
		t.Fatalf("a refused batch stored %d items", n)
	}
}

// "decoded with `DisallowUnknownFields`" — at the top level and inside an item
// and its source; refused before anything is stored, even from the lead.
func TestMPEProposeRouteRefusesUnknownKeys(t *testing.T) {
	st := mpeStore(t)
	mpeMindLead(t, st)
	mux := mpeMux()
	for _, body := range []string{
		`{"session":"` + mpeLead + `","items":[` + mpeValidProposal + `],"bogus":1}`,
		`{"session":"` + mpeLead + `","thread_session":"x","items":[` + mpeValidProposal + `]}`,
		`{"session":"` + mpeLead + `","items":[` + strings.Replace(mpeValidProposal, `"rung":"plain"`, `"rung":"plain","state":"live"`, 1) + `]}`,
		`{"session":"` + mpeLead + `","items":[` + strings.Replace(mpeValidProposal, `"uuid":"u1"`, `"uuid":"u1","bogus":2`, 1) + `]}`,
	} {
		rec := mpeDo(t, mux, http.MethodPost, "/api/mm/propose", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("unknown key %s = %d (%s), want 400", body, rec.Code, rec.Body.String())
		}
	}
	if n := mpeItemCount(t, st); n != 0 {
		t.Fatalf("refused bodies stored %d items", n)
	}
}

// The route takes a POST body; a GET reaches no handler that writes.
// (Status code is a contract silence — see the report.)
func TestMPEProposeRouteRefusesGET(t *testing.T) {
	st := mpeStore(t)
	mpeMindLead(t, st)
	rec := mpeDo(t, mpeMux(), http.MethodGet, "/api/mm/propose?session="+mpeLead, "")
	if rec.Code < 400 || rec.Code > 499 {
		t.Fatalf("GET /api/mm/propose = %d (%s), want a 4xx refusal", rec.Code, rec.Body.String())
	}
	if n := mpeItemCount(t, st); n != 0 {
		t.Fatalf("GET stored %d items", n)
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/confirm
// ---------------------------------------------------------------------------

// "`id <= 0` → 400 `missing id`".
func TestMPEConfirmRouteMissingID(t *testing.T) {
	mpeStore(t)
	mux := mpeMux()
	for _, body := range []string{`{"id":0}`, `{"id":-4}`, `{}`} {
		rec := mpeDo(t, mux, http.MethodPost, "/api/mm/confirm", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("confirm %s = %d (%s), want 400", body, rec.Code, rec.Body.String())
			continue
		}
		if got := mpeObj(t, rec); !reflect.DeepEqual(got, map[string]any{"error": "missing id"}) {
			t.Errorf("confirm %s body = %v, want {\"error\":\"missing id\"}", body, got)
		}
	}
}

// "unknown → 404".
func TestMPEConfirmRouteUnknownIs404(t *testing.T) {
	mpeStore(t)
	rec := mpeDo(t, mpeMux(), http.MethodPost, "/api/mm/confirm", `{"id":987654}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("confirm unknown = %d (%s), want 404", rec.Code, rec.Body.String())
	}
}

// "`ErrMMItemInvalidated` → 409 `{"error":"mm item is invalidated"}`" — and
// the row stays proposed.
func TestMPEConfirmRouteInvalidatedIs409(t *testing.T) {
	st := mpeStore(t)
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "nightly", State: "proposed", Content: "c", Quote: "q", Title: "t", CreatedAt: 1_770_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InvalidateMMItem(id, 1_770_000_000_001, "removed", ""); err != nil {
		t.Fatal(err)
	}
	rec := mpeDo(t, mpeMux(), http.MethodPost, "/api/mm/confirm", `{"id":`+strconv.FormatInt(id, 10)+`}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("confirm invalidated = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	if got := mpeObj(t, rec); !reflect.DeepEqual(got, map[string]any{"error": "mm item is invalidated"}) {
		t.Fatalf("409 body = %v", got)
	}
	it, err := st.GetMMItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != "proposed" {
		t.Fatalf("state after a refused confirm = %q, want proposed", it.State)
	}
}

// "success and already-live → 200 `{"ok":true,"id":<id>,"state":"live"}`" —
// the already-live arm, twice.
func TestMPEConfirmRouteAlreadyLiveIs200(t *testing.T) {
	st := mpeStore(t)
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "manual", Content: "c", CreatedAt: 1_770_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	mux := mpeMux()
	for i := 0; i < 2; i++ {
		rec := mpeDo(t, mux, http.MethodPost, "/api/mm/confirm", `{"id":`+strconv.FormatInt(id, 10)+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("confirm live #%d = %d (%s), want 200", i+1, rec.Code, rec.Body.String())
		}
		want := map[string]any{"ok": true, "id": float64(id), "state": "live"}
		if got := mpeObj(t, rec); !reflect.DeepEqual(got, want) {
			t.Fatalf("confirm live #%d body = %v, want %v", i+1, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// mirror gate
// ---------------------------------------------------------------------------

// "registered `mirrorGated`"; "a `--mirror` serve answers 403 before the
// handler (the gate wrapper)" — the shared gate's body, whatever the request.
func TestMPEProposeAndConfirmAreMirrorGated(t *testing.T) {
	st := mpeStore(t)
	mpeMindLead(t, st)
	SetMirrorMode(true)
	mux := mpeMux()
	for path, body := range map[string]string{
		"/api/mm/propose": `{"session":"` + mpeLead + `","items":[` + mpeValidProposal + `]}`,
		"/api/mm/confirm": `{"id":1}`,
	} {
		for _, b := range []string{body, `not json`, ``} {
			rec := mpeDo(t, mux, http.MethodPost, path, b)
			if rec.Code != http.StatusForbidden {
				t.Errorf("mirror POST %s %q = %d, want 403", path, b, rec.Code)
				continue
			}
			if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
				t.Errorf("mirror POST %s body = %q, want the gate's sentence", path, got)
			}
		}
	}
	if n := mpeItemCount(t, st); n != 0 {
		t.Fatalf("mirror-mode proposes stored %d items", n)
	}
}

// ---------------------------------------------------------------------------
// GET /api/mm/evidence
// ---------------------------------------------------------------------------

// "`?since=` required (missing/non-numeric/`<= 0` → 400 with a sentence)".
func TestMPEEvidenceRouteBadSince(t *testing.T) {
	mpeStore(t)
	mux := mpeMux()
	for _, q := range []string{"", "?since=", "?since=abc", "?since=0", "?since=-5", "?since=1.5", "?until=5"} {
		rec := mpeDo(t, mux, http.MethodGet, "/api/mm/evidence"+q, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/mm/evidence%s = %d (%s), want 400", q, rec.Code, rec.Body.String())
			continue
		}
		if e, _ := mpeObj(t, rec)["error"].(string); strings.TrimSpace(e) == "" {
			t.Errorf("GET /api/mm/evidence%s 400 carries no sentence: %s", q, rec.Body.String())
		}
	}
}

// "GET only". (Status code is a contract silence — see the report.)
func TestMPEEvidenceRouteRefusesPOST(t *testing.T) {
	mpeStore(t)
	rec := mpeDo(t, mpeMux(), http.MethodPost, "/api/mm/evidence?since=1", `{}`)
	if rec.Code < 400 || rec.Code > 499 {
		t.Fatalf("POST /api/mm/evidence = %d (%s), want a 4xx refusal", rec.Code, rec.Body.String())
	}
}

// "`MMEvidence` … slices never nil" — an empty window marshals arrays, not
// null.
func TestMPEEvidenceRouteEmptyWindowArrays(t *testing.T) {
	mpeStore(t)
	rec := mpeDo(t, mpeMux(), http.MethodGet, "/api/mm/evidence?since=1&until=2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("evidence = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	m := mpeObj(t, rec)
	for _, k := range []string{"jobs", "feedback", "answers"} {
		arr, ok := m[k].([]any)
		if !ok || len(arr) != 0 {
			t.Errorf("%s = %#v, want []", k, m[k])
		}
	}
}

// ---------------------------------------------------------------------------
// GET /api/mm/source — the three new arms when their target is gone
// ---------------------------------------------------------------------------

func mpeSourceItem(t *testing.T, st *store.Store, sourceJSON string) int64 {
	t.Helper()
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "nightly", Content: "c", Quote: "q", Title: "t", SourceJSON: sourceJSON, CreatedAt: 1_770_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mpeSource(t *testing.T, mux http.Handler, id int64) map[string]any {
	t.Helper()
	rec := mpeDo(t, mux, http.MethodGet, "/api/mm/source?id="+strconv.FormatInt(id, 10), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("source %d = %d (%s), want 200 whatever `available` says", id, rec.Code, rec.Body.String())
	}
	return mpeObj(t, rec)
}

// "all three answer HTTP 200 whatever `available` says": `job` to a missing
// slug, `feedback` to a deleted row, `question` to a missing qid and to a
// missing job → `available:false`, the kind echoed.
func TestMPESourceArmsGoneTargetsAreUnavailable200(t *testing.T) {
	st := mpeStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "has-questions", Repo: "kraken", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuestions(id, []store.DocQuestion{{QID: "Q1", Text: "which?", Importance: "P1"}}); err != nil {
		t.Fatal(err)
	}
	fb, err := st.AddFeedback(store.Feedback{JobID: id, JobSlug: "has-questions", Artifact: "a.md", Note: "a note", TS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFeedback(fb); err != nil {
		t.Fatal(err)
	}
	mux := mpeMux()
	for name, c := range map[string]struct{ src, kind string }{
		"job, missing slug":       {`{"kind":"job","slug":"never-existed"}`, "job"},
		"feedback, deleted row":   {`{"kind":"feedback","feedback_id":` + strconv.FormatInt(fb, 10) + `}`, "feedback"},
		"feedback, never existed": {`{"kind":"feedback","feedback_id":999999}`, "feedback"},
		"question, missing qid":   {`{"kind":"question","slug":"has-questions","qid":"Q9"}`, "question"},
		"question, missing job":   {`{"kind":"question","slug":"never-existed","qid":"Q1"}`, "question"},
	} {
		m := mpeSource(t, mux, mpeSourceItem(t, st, c.src))
		if m["available"] != false || m["kind"] != c.kind {
			t.Errorf("%s: source = %v, want {kind:%q, available:false}", name, m, c.kind)
		}
	}
}

// "`changed: <SHA256Hex(text) != src.SHA when src.SHA is non-empty>`" — a job
// pointer carrying no sha never reads as changed.
func TestMPESourceJobArmNoSHAIsNeverChanged(t *testing.T) {
	st := mpeStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: "live-spec", Repo: "kraken", Body: "the spec body"}); err != nil {
		t.Fatal(err)
	}
	m := mpeSource(t, mpeMux(), mpeSourceItem(t, st, `{"kind":"job","slug":"live-spec"}`))
	if m["available"] != true || m["changed"] != false {
		t.Fatalf("source = %v, want available:true changed:false", m)
	}
}
