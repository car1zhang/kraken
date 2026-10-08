package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// graphremoval_edge_test.go — the EDGE dimension of 50-contract.md slice B for
// package api: POST /api/recall's refusals (method, unknown key, oversize,
// bad JSON, mirror, no store), every retired /api/graph* path answering 404
// under any method and either mirror setting, and GET /api/mm still carrying a
// legacy row's stored episode_id. Written blind from the contract: helpers.go,
// recall.go, mm.go and register.go were not read. Happy paths are
// athena-logic's.
//
// Every package-level identifier is prefixed `edgeGR`. Every request goes
// through the REAL mux (Register), so a handler that exists but is registered
// on the wrong pattern, or not mirror-wrapped, still fails here.

func edgeGREnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("KRAKEN_DB_PATH", "")
}

func edgeGRMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func edgeGRDo(t *testing.T, mux http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body == nil {
		rdr = bytes.NewReader(nil)
	} else {
		rdr = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func edgeGRMirror(t *testing.T, on bool) {
	t.Helper()
	SetMirrorMode(on)
	t.Cleanup(func() { SetMirrorMode(false) })
}

// ---------------------------------------------------------------------------
// POST /api/recall
// ---------------------------------------------------------------------------

// "`POST /api/recall` … Non-POST → 405".
func TestGraphRemovalEdgeRecallRouteRefusesNonPOST(t *testing.T) {
	edgeGREnv(t)
	useTestStore(t)
	edgeGRMirror(t, false)
	mux := edgeGRMux(t)
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := edgeGRDo(t, mux, m, "/api/recall", []byte(`{"session_id":"s","event":"UserPromptSubmit"}`))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/recall = %d (%s), want 405", m, rec.Code, rec.Body.String())
		}
	}
}

// "a body with an unknown key → 400" — including an unknown key riding beside
// every one of the five valid ones, and the retired PostToolUse-era names an
// even older hook might have sent.
func TestGraphRemovalEdgeRecallRouteRefusesUnknownKeys(t *testing.T) {
	edgeGREnv(t)
	useTestStore(t)
	edgeGRMirror(t, false)
	mux := edgeGRMux(t)
	for _, body := range []string{
		`{"session_id":"s","event":"UserPromptSubmit","prompt":"p","transcript_path":"","slug":"x","querry":"typo"}`,
		`{"query":"what the graph search route took"}`,
		`{"session_id":"s","agent":{"nested":true}}`,
		`{"Session_ID":"s","limit":5}`,
	} {
		rec := edgeGRDo(t, mux, http.MethodPost, "/api/recall", []byte(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/recall %s = %d (%s), want 400", body, rec.Code, rec.Body.String())
			continue
		}
		var e map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
			t.Errorf("400 body is not JSON: %q", rec.Body.String())
			continue
		}
		if s, _ := e["error"].(string); s == "" {
			t.Errorf("400 body carries no `error` string: %s", rec.Body.String())
		}
	}
}

// `decodeStrictBody`: ">1 MiB → 413; bad JSON → 400", reached through the
// recall route that uses it.
func TestGraphRemovalEdgeRecallRouteOversizeAndBadJSON(t *testing.T) {
	edgeGREnv(t)
	useTestStore(t)
	edgeGRMirror(t, false)
	mux := edgeGRMux(t)

	huge := `{"session_id":"s","event":"UserPromptSubmit","prompt":"` + strings.Repeat("a", (1<<20)+4096) + `"}`
	if rec := edgeGRDo(t, mux, http.MethodPost, "/api/recall", []byte(huge)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST /api/recall with a >1 MiB body = %d, want 413", rec.Code)
	}
	for _, bad := range []string{`{`, `not json`, `{"session_id":`, `[]`, `"a string"`} {
		if rec := edgeGRDo(t, mux, http.MethodPost, "/api/recall", []byte(bad)); rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/recall %q = %d (%s), want 400", bad, rec.Code, rec.Body.String())
		}
	}
}

// "a well-formed body → 200 `{"context": "<string>"}` with `""` legal." The
// smallest well-formed bodies — `{}`, an all-blank request, a non-UPS event —
// are each a 200 whose context is the empty string, never an error status.
func TestGraphRemovalEdgeRecallRouteMinimalBodiesAre200Empty(t *testing.T) {
	edgeGREnv(t)
	useTestStore(t)
	edgeGRMirror(t, false)
	mux := edgeGRMux(t)
	for _, body := range []string{
		`{}`,
		`{"session_id":"","event":"","prompt":"","transcript_path":"","slug":""}`,
		`{"session_id":"s","event":"Stop","slug":"no-such-job"}`,
		`{"session_id":"s","event":"UserPromptSubmit","slug":"no-such-job","prompt":null}`,
	} {
		rec := edgeGRDo(t, mux, http.MethodPost, "/api/recall", []byte(body))
		if rec.Code != http.StatusOK {
			t.Errorf("POST /api/recall %s = %d (%s), want 200", body, rec.Code, rec.Body.String())
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Errorf("200 body is not JSON: %q", rec.Body.String())
			continue
		}
		ctx, ok := got["context"].(string)
		if !ok {
			t.Errorf("POST /api/recall %s: `context` = %#v, want a string", body, got["context"])
			continue
		}
		if ctx != "" {
			t.Errorf("POST /api/recall %s: context = %q, want \"\"", body, ctx)
		}
	}
}

// "Under `--mirror` → 403 like every other gated write." The gate runs before
// the decode, so even a garbage or oversize body gets the mirror refusal.
func TestGraphRemovalEdgeRecallRouteMirrorRefusesBeforeDecode(t *testing.T) {
	edgeGREnv(t)
	useTestStore(t)
	edgeGRMirror(t, true)
	mux := edgeGRMux(t)
	for _, body := range []string{`{"session_id":"s","event":"UserPromptSubmit"}`, `{`, `{"unknown":1}`, ``} {
		rec := edgeGRDo(t, mux, http.MethodPost, "/api/recall", []byte(body))
		if rec.Code != http.StatusForbidden {
			t.Errorf("mirror POST /api/recall %q = %d (%s), want 403", body, rec.Code, rec.Body.String())
			continue
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
			t.Errorf("mirror POST /api/recall body = %q, want %q", got, "disabled in mirror mode")
		}
	}
}

// "`api.activeStore` … nil store → 500 `{"error":"no store wired"}`; used by
// jobdraft, slack, orch, recall handlers." Reached through the recall route
// with no store wired.
func TestGraphRemovalEdgeRecallRouteWithNoStoreIs500(t *testing.T) {
	edgeGREnv(t)
	jobs.UseStore(nil)
	edgeGRMirror(t, false)
	mux := edgeGRMux(t)
	rec := edgeGRDo(t, mux, http.MethodPost, "/api/recall", []byte(`{"session_id":"s","event":"UserPromptSubmit","slug":"x"}`))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST /api/recall with no store = %d (%s), want 500", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("500 body is not JSON: %q", rec.Body.String())
	}
	if got["error"] != "no store wired" {
		t.Fatalf("500 body = %s, want {\"error\":\"no store wired\"}", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// retired /api/graph* routes
// ---------------------------------------------------------------------------

// "`/api/graph`, `/api/graph/stats`, … `/api/graph/recall-rearm` —
// unregistered — Each answers 404 from the mux (any method)." Swept under
// every method and BOTH mirror settings: a route left registered behind a
// mirrorGated wrapper would answer 403 under mirror rather than 404.
func TestGraphRemovalEdgeRetiredGraphRoutesAre404(t *testing.T) {
	retired := []string{
		"/api/graph", "/api/graph/stats", "/api/graph/node", "/api/graph/fact",
		"/api/graph/episode", "/api/graph/pending", "/api/graph/search",
		"/api/graph/recall", "/api/graph/add", "/api/graph/episode-done",
		"/api/graph/recall-rearm",
	}
	methods := []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead}
	for _, mirror := range []bool{false, true} {
		edgeGREnv(t)
		useTestStore(t)
		SetMirrorMode(mirror)
		mux := edgeGRMux(t)
		for _, p := range retired {
			for _, m := range methods {
				for _, q := range []string{"", "?id=1&q=x"} {
					rec := edgeGRDo(t, mux, m, p+q, []byte(`{"query":"x","session_id":"s"}`))
					if rec.Code != http.StatusNotFound {
						t.Errorf("mirror=%v %s %s%s = %d, want 404", mirror, m, p, q, rec.Code)
					}
				}
			}
		}
		SetMirrorMode(false)
	}
}

// ---------------------------------------------------------------------------
// GET /api/mm — episode_id after the removal
// ---------------------------------------------------------------------------

func edgeGRMMItems(t *testing.T, mux http.Handler) []map[string]any {
	t.Helper()
	rec := edgeGRDo(t, mux, http.MethodGet, "/api/mm", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm = %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET /api/mm: %v\n%s", err, rec.Body.String())
	}
	return body.Items
}

// "`GET /api/mm` … Every item object still carries an integer `episode_id`
// key"; "`ListMMItems`/`GetMMItem` return existing rows with their stored
// value, new rows with 0"; "`POST /api/mm/*` create responses … Carry `id`;
// the `episode` key is absent." A legacy row (episode_id stamped by raw SQL,
// as the removed SetMMItemEpisode once did) keeps its stored value on the
// wire; a new row reads 0, and its create response carries no `episode`.
func TestGraphRemovalEdgeMMListKeepsLegacyEpisodeID(t *testing.T) {
	edgeGREnv(t)
	st := useTestStore(t)
	edgeGRMirror(t, false)
	mux := edgeGRMux(t)

	create := func(text string) int64 {
		t.Helper()
		rec := edgeGRDo(t, mux, http.MethodPost, "/api/mm/note", mustJSON(t, map[string]any{"text": text, "context": ""}))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST /api/mm/note = %d (%s)", rec.Code, rec.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode create: %v", err)
		}
		if _, has := body["episode"]; has {
			t.Errorf("create response still carries `episode`: %s", rec.Body.String())
		}
		id, ok := body["id"].(float64)
		if !ok || id <= 0 {
			t.Fatalf("create response has no id: %s", rec.Body.String())
		}
		return int64(id)
	}
	legacy := create("edge legacy note")
	fresh := create("edge fresh note")
	if _, err := st.DB().Exec(`UPDATE mm_items SET episode_id = ? WHERE id = ?`, int64(9001), legacy); err != nil {
		t.Fatalf("stamp legacy episode_id: %v", err)
	}

	want := map[int64]float64{legacy: 9001, fresh: 0}
	seen := map[int64]bool{}
	for _, it := range edgeGRMMItems(t, mux) {
		raw, has := it["episode_id"]
		if !has {
			t.Errorf("item %v carries no episode_id key", it["id"])
			continue
		}
		ep, ok := raw.(float64)
		if !ok || ep != float64(int64(ep)) {
			t.Errorf("item %v episode_id = %#v, want an integer", it["id"], raw)
			continue
		}
		idf, _ := it["id"].(float64)
		id := int64(idf)
		if w, tracked := want[id]; tracked {
			seen[id] = true
			if ep != w {
				t.Errorf("item %d episode_id = %v, want %v", id, ep, w)
			}
		}
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("item %d missing from GET /api/mm", id)
		}
	}
}
