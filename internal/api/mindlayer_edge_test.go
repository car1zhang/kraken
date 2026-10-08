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

// mindlayer_edge_test.go — the EDGE dimension of 50-contract.md slice C (PR 2,
// the mental-model rework) for package api. Written blind from the contract:
// mm.go, the new route files and register.go were not read. Every request
// goes through the REAL mux (Register), so a handler registered on the wrong
// pattern or not mirror-wrapped still fails here. Happy paths are
// athena-logic's.
//
// Every package-level identifier is prefixed `edgeML`.

// edgeMLStore applies the contract's test environment (HOME isolated,
// KRAKEN_DB_PATH / KRAKEN_THREAD_SESSION / KRAKEN_THREAD_ROLE unset), wires a
// temp store, and turns mirror mode off for the test.
func edgeMLStore(t *testing.T) *store.Store {
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

func edgeMLMux() http.Handler {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func edgeMLDo(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func edgeMLJSON(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not a JSON object (%d): %q", rec.Code, rec.Body.String())
	}
	return m
}

func edgeMLJob(t *testing.T, st *store.Store, slug, session string) *store.Job {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken", State: "working", Session: session})
	if err != nil {
		t.Fatalf("create job %q: %v", slug, err)
	}
	j, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return j
}

func edgeMLThread(t *testing.T, st *store.Store, jobID int64, session, role, state string) {
	t.Helper()
	if _, err := st.AddThreadNext(store.Thread{JobID: jobID, Title: "edge", SessionID: session, Role: role, State: state}); err != nil {
		t.Fatalf("AddThreadNext: %v", err)
	}
}

const edgeMLValidItem = `{"title":"a claim","scope":"universal","repo":"","dir":"","kind":"note","body":"b","quote":"q","source":{"kind":"manual"}}`

// ---------------------------------------------------------------------------
// POST /api/mm/capture
// ---------------------------------------------------------------------------

// "a non-POST → 405".
func TestMindLayerEdgeCaptureRouteRefusesNonPOST(t *testing.T) {
	edgeMLStore(t)
	mux := edgeMLMux()
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := edgeMLDo(t, mux, m, "/api/mm/capture", `{"slug":"x","text":"t","source":{"kind":"manual"}}`)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/mm/capture = %d (%s), want 405", m, rec.Code, rec.Body.String())
		}
	}
}

// "Decodes strictly (unknown key → 400)" — at the top level and inside
// `source`.
func TestMindLayerEdgeCaptureRouteRefusesUnknownKeys(t *testing.T) {
	st := edgeMLStore(t)
	edgeMLJob(t, st, "edge-ml-cap", "sess-lead")
	mux := edgeMLMux()
	for _, body := range []string{
		`{"slug":"edge-ml-cap","text":"t","source":{"kind":"manual"},"tid":"t1"}`,
		`{"slug":"edge-ml-cap","text":"t","source":{"kind":"manual"},"thread_session":"s"}`,
		`{"slug":"edge-ml-cap","text":"t","source":{"kind":"manual","bogus":1}}`,
		`{"slug":"edge-ml-cap","txt":"t","source":{"kind":"manual"}}`,
	} {
		rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/capture", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/mm/capture %s = %d (%s), want 400", body, rec.Code, rec.Body.String())
		}
	}
}

// "unknown slug → 404".
func TestMindLayerEdgeCaptureRouteUnknownSlug(t *testing.T) {
	edgeMLStore(t)
	mux := edgeMLMux()
	for _, slug := range []string{"no-such-job", "edge-ml-never-created"} {
		rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/capture", `{"slug":"`+slug+`","text":"t","source":{"kind":"manual"}}`)
		if rec.Code != http.StatusNotFound {
			t.Errorf("POST /api/mm/capture slug %q = %d (%s), want 404", slug, rec.Code, rec.Body.String())
		}
	}
}

// `ErrCaptureNoCell` → 409: a known job whose cell is not live.
func TestMindLayerEdgeCaptureRouteDeadCellIs409(t *testing.T) {
	st := edgeMLStore(t)
	edgeMLJob(t, st, "edge-ml-cap-dead", "sess-lead")
	rec := edgeMLDo(t, edgeMLMux(), http.MethodPost, "/api/mm/capture", `{"slug":"edge-ml-cap-dead","text":"some text","source":{"kind":"manual"}}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST /api/mm/capture (dead cell) = %d (%s), want 409", rec.Code, rec.Body.String())
	}
}

// `mirrorGated`: under mirror mode every new mm write route refuses with the
// shared 403 before decoding, whatever the body.
func TestMindLayerEdgeWriteRoutesAreMirrorGated(t *testing.T) {
	edgeMLStore(t)
	SetMirrorMode(true)
	mux := edgeMLMux()
	for _, path := range []string{"/api/mm/capture", "/api/mm/commit", "/api/mm/revise", "/api/mm/flag", "/api/mm/flag/raise", "/api/mm/flag/dismiss", "/api/mm/flag/resolve"} {
		for _, body := range []string{`{"slug":"nonexistent"}`, `not json`, ``} {
			rec := edgeMLDo(t, mux, http.MethodPost, path, body)
			if rec.Code != http.StatusForbidden {
				t.Errorf("mirror POST %s %q = %d, want 403", path, body, rec.Code)
				continue
			}
			if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
				t.Errorf("mirror POST %s body = %q, want %q", path, got, "disabled in mirror mode")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/commit
// ---------------------------------------------------------------------------

// "`ErrNotMindThread` → 403 `{"error":"not a mind thread"}`" — for an ask
// thread, a closed mind thread, an empty and an unknown session; nothing stored.
func TestMindLayerEdgeCommitRoute403(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-commit", "sess-lead")
	edgeMLThread(t, st, j.ID, "sess-ask", "ask", "live")
	edgeMLThread(t, st, j.ID, "sess-mind-closed", "mind", "closed")
	mux := edgeMLMux()
	for _, sess := range []string{"sess-ask", "sess-mind-closed", "", "sess-unknown", "sess-lead"} {
		rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/commit", `{"thread_session":"`+sess+`","item":`+edgeMLValidItem+`}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("commit from %q = %d (%s), want 403", sess, rec.Code, rec.Body.String())
			continue
		}
		if got := edgeMLJSON(t, rec); !reflect.DeepEqual(got, map[string]any{"error": "not a mind thread"}) {
			t.Errorf("commit from %q body = %v, want {\"error\":\"not a mind thread\"}", sess, got)
		}
	}
	items, _ := st.ListMMItems(true)
	if len(items) != 0 {
		t.Fatalf("refused commits stored %d items", len(items))
	}
}

// "`ErrCommitInvalid` → 400 with the reason" — from a live mind thread.
func TestMindLayerEdgeCommitRoute400(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-commit400", "sess-lead")
	edgeMLThread(t, st, j.ID, "sess-mind", "mind", "live")
	mux := edgeMLMux()
	long := strings.Repeat("x", 121)
	for _, item := range []string{
		`{"title":"` + long + `","scope":"universal","kind":"note","body":"b","source":{"kind":"manual"}}`,
		`{"title":"t","scope":"universal","kind":"note","body":"","source":{"kind":"manual"}}`,
		`{"title":"t","scope":"everywhere","kind":"note","body":"b","source":{"kind":"manual"}}`,
		`{"title":"t","scope":"universal","kind":"opinion","body":"b","source":{"kind":"manual"}}`,
		`{"title":"t","scope":"repo","repo":"","kind":"note","body":"b","source":{"kind":"manual"}}`,
		`{"title":"t","scope":"universal","kind":"note","body":"b","source":{"kind":"code"}}`,
	} {
		rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/commit", `{"thread_session":"sess-mind","item":`+item+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("commit %s = %d (%s), want 400", item, rec.Code, rec.Body.String())
			continue
		}
		if e, _ := edgeMLJSON(t, rec)["error"].(string); e == "" {
			t.Errorf("400 for %s carries no error reason: %s", item, rec.Body.String())
		}
	}
	items, _ := st.ListMMItems(true)
	if len(items) != 0 {
		t.Fatalf("refused commits stored %d items", len(items))
	}
}

// /api/mm/revise: "unknown id → 404; invalidated item → 409".
func TestMindLayerEdgeReviseRouteUnknownAndInvalidated(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-revise", "sess-lead")
	edgeMLThread(t, st, j.ID, "sess-mind", "mind", "live")
	mux := edgeMLMux()

	rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/revise", `{"thread_session":"sess-mind","id":424242,"item":`+edgeMLValidItem+`}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("revise unknown id = %d (%s), want 404", rec.Code, rec.Body.String())
	}

	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "capture", Title: "old", Scope: "universal", Content: "old", CreatedAt: 1})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	if err := st.InvalidateMMItem(id, 2, "removed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	rec = edgeMLDo(t, mux, http.MethodPost, "/api/mm/revise", `{"thread_session":"sess-mind","id":`+strconv.FormatInt(id, 10)+`,"item":`+edgeMLValidItem+`}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("revise invalidated = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	if got, _ := st.GetMMItem(id); got.Title != "old" {
		t.Errorf("a refused revise rewrote the title to %q", got.Title)
	}
}

// ---------------------------------------------------------------------------
// flags
// ---------------------------------------------------------------------------

// /api/mm/flag/raise and /dismiss: "`ErrNotLead` → 403; non-pending → 409".
func TestMindLayerEdgeFlagRaiseDismissRouteMapping(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-raise", "sess-lead")
	id, err := st.AddMMFlag(store.MMFlag{JobID: j.ID, Slug: j.Slug, Session: "sess-mate", Note: "n", CreatedAt: 1})
	if err != nil {
		t.Fatalf("AddMMFlag: %v", err)
	}
	ids := strconv.FormatInt(id, 10)
	mux := edgeMLMux()
	for _, path := range []string{"/api/mm/flag/raise", "/api/mm/flag/dismiss"} {
		rec := edgeMLDo(t, mux, http.MethodPost, path, `{"session":"sess-mate","id":`+ids+`,"note":"x"}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s from a non-lead = %d (%s), want 403", path, rec.Code, rec.Body.String())
		}
	}
	if f, _ := st.GetMMFlag(id); f.State != "pending" {
		t.Fatalf("refused raise/dismiss moved the flag to %q", f.State)
	}
	if rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/flag/raise", `{"session":"sess-lead","id":`+ids+`,"note":"ok"}`); rec.Code != http.StatusOK {
		t.Fatalf("raise from the lead = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/mm/flag/raise", "/api/mm/flag/dismiss"} {
		rec := edgeMLDo(t, mux, http.MethodPost, path, `{"session":"sess-lead","id":`+ids+`,"note":"again"}`)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s of a raised flag = %d (%s), want 409", path, rec.Code, rec.Body.String())
		}
	}
}

// /api/mm/flag/resolve: a pending flag and a second resolution → 409 (slice
// B's "error (409)"); a resolution outside drop/keep is refused (4xx — the
// contract does not name the code) and changes nothing.
func TestMindLayerEdgeFlagResolveRouteRefusals(t *testing.T) {
	st := edgeMLStore(t)
	j := edgeMLJob(t, st, "edge-ml-resolve", "sess-lead")
	pending, err := st.AddMMFlag(store.MMFlag{JobID: j.ID, Slug: j.Slug, Note: "p", CreatedAt: 1})
	if err != nil {
		t.Fatalf("AddMMFlag: %v", err)
	}
	raised, err := st.AddMMFlag(store.MMFlag{JobID: j.ID, Slug: j.Slug, Note: "r", CreatedAt: 2})
	if err != nil {
		t.Fatalf("AddMMFlag: %v", err)
	}
	if err := st.RaiseMMFlag(raised, 3, "sess-lead", "ok"); err != nil {
		t.Fatalf("RaiseMMFlag: %v", err)
	}
	mux := edgeMLMux()

	rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/flag/resolve", `{"id":`+strconv.FormatInt(pending, 10)+`,"resolution":"keep"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("resolve a pending flag = %d (%s), want 409", rec.Code, rec.Body.String())
	}

	before, _ := st.GetMMFlag(raised)
	for _, r := range []string{"maybe", "", "DROP"} {
		rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/flag/resolve", `{"id":`+strconv.FormatInt(raised, 10)+`,"resolution":"`+r+`"}`)
		if rec.Code < 400 || rec.Code >= 500 {
			t.Errorf("resolve with %q = %d (%s), want a 4xx refusal", r, rec.Code, rec.Body.String())
		}
	}
	if after, _ := st.GetMMFlag(raised); !reflect.DeepEqual(after, before) {
		t.Fatalf("bad resolutions changed the flag:\n got %+v\nwant %+v", after, before)
	}

	if rec := edgeMLDo(t, mux, http.MethodPost, "/api/mm/flag/resolve", `{"id":`+strconv.FormatInt(raised, 10)+`,"resolution":"keep"}`); rec.Code != http.StatusOK {
		t.Fatalf("resolve keep = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	rec = edgeMLDo(t, mux, http.MethodPost, "/api/mm/flag/resolve", `{"id":`+strconv.FormatInt(raised, 10)+`,"resolution":"drop"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("a second resolution = %d (%s), want 409", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// reads
// ---------------------------------------------------------------------------

// `GET /api/mm/show`: "unknown → 404".
func TestMindLayerEdgeShowUnknownIs404(t *testing.T) {
	edgeMLStore(t)
	mux := edgeMLMux()
	for _, q := range []string{"?id=424242", "?id=999999999"} {
		if rec := edgeMLDo(t, mux, http.MethodGet, "/api/mm/show"+q, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/mm/show%s = %d (%s), want 404", q, rec.Code, rec.Body.String())
		}
	}
}

// `GET /api/mm/source`: "`transcript` → … `available:false` when the file or
// uuid is not found"; "Never a 5xx for a missing source." The session names no
// transcript under the isolated HOME.
func TestMindLayerEdgeSourceMissingTranscriptIsUnavailable(t *testing.T) {
	st := edgeMLStore(t)
	id, err := st.AddMMItem(store.MMItem{
		Kind: "reading", Source: "capture", Title: "t", Scope: "universal", Content: "c", CreatedAt: 1,
		SourceJSON: `{"kind":"transcript","session":"0f0e0d0c-0000-4000-8000-000000000001","uuid":"u-missing"}`,
	})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	rec := edgeMLDo(t, edgeMLMux(), http.MethodGet, "/api/mm/source?id="+strconv.FormatInt(id, 10), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm/source (missing transcript) = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	m := edgeMLJSON(t, rec)
	if m["kind"] != "transcript" {
		t.Errorf("kind = %v, want transcript", m["kind"])
	}
	if m["available"] != false {
		t.Errorf("available = %v, want false", m["available"])
	}
	for _, k := range []string{"kind", "available", "changed", "text", "ext"} {
		if _, ok := m[k]; !ok {
			t.Errorf("source response lacks key %q: %v", k, m)
		}
	}
}

// `item`/`manual` → `available:false` (never a 5xx).
func TestMindLayerEdgeSourceItemAndManualAreUnavailable(t *testing.T) {
	st := edgeMLStore(t)
	mux := edgeMLMux()
	for _, sj := range []string{`{"kind":"item","item_id":3}`, `{"kind":"manual"}`} {
		id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "capture", Title: "t", Scope: "universal", Content: "c", CreatedAt: 1, SourceJSON: sj})
		if err != nil {
			t.Fatalf("AddMMItem: %v", err)
		}
		rec := edgeMLDo(t, mux, http.MethodGet, "/api/mm/source?id="+strconv.FormatInt(id, 10), "")
		if rec.Code != http.StatusOK {
			t.Errorf("source %s = %d (%s), want 200", sj, rec.Code, rec.Body.String())
			continue
		}
		if m := edgeMLJSON(t, rec); m["available"] != false {
			t.Errorf("source %s available = %v, want false", sj, m["available"])
		}
	}
}

// `GET /api/job/transcript`: "unknown slug → 404".
func TestMindLayerEdgeJobTranscriptUnknownSlug(t *testing.T) {
	edgeMLStore(t)
	mux := edgeMLMux()
	for _, q := range []string{"?slug=no-such-job", "?slug=edge-ml-never-created&n=5"} {
		if rec := edgeMLDo(t, mux, http.MethodGet, "/api/job/transcript"+q, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/job/transcript%s = %d (%s), want 404", q, rec.Code, rec.Body.String())
		}
	}
}

// A job with no stamped session: 200 `{"session":"","blocks":[…empty]}`
// ("a job with no session … returns an empty slice and `""`"), whatever n.
func TestMindLayerEdgeJobTranscriptNoSession(t *testing.T) {
	st := edgeMLStore(t)
	edgeMLJob(t, st, "edge-ml-nosess", "")
	mux := edgeMLMux()
	for _, q := range []string{"", "&n=0", "&n=-3", "&n=9999"} {
		rec := edgeMLDo(t, mux, http.MethodGet, "/api/job/transcript?slug=edge-ml-nosess"+q, "")
		if rec.Code != http.StatusOK {
			t.Errorf("transcript (no session%s) = %d (%s), want 200", q, rec.Code, rec.Body.String())
			continue
		}
		m := edgeMLJSON(t, rec)
		if m["session"] != "" {
			t.Errorf("session = %v, want \"\"", m["session"])
		}
		blocks, present := m["blocks"]
		if !present {
			t.Errorf("response lacks `blocks`: %v", m)
			continue
		}
		if arr, ok := blocks.([]any); blocks != nil && (!ok || len(arr) != 0) {
			t.Errorf("blocks = %v, want empty", blocks)
		}
	}
}
