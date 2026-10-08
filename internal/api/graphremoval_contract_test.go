package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// graphremoval_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, slice B's `api` rows (PR 1, knowledge-graph
// removal). Written from the frozen contract alone; no non-test file under
// internal/ was read. The mux harness (Register, SetMirrorMode, useTestStore,
// mustJSON) was learned from sibling _test.go files.
//
// Dimension: LOGIC — the renamed recall route's happy path and its stated
// gates, the graph routes gone from the mux, the mm wire shape after removal.
// Malformed-body boundaries beyond the one unknown-key clause are athena-edge's.
//
// Every package-level identifier here is prefixed `contract`.

// contractGRServe isolates the env per the contract's Test environment section,
// wires a scratch store, and returns the real mux.
func contractGRServe(t *testing.T) http.Handler {
	t.Helper()
	useTestStore(t) // sets HOME to a temp dir
	for _, k := range []string{"OPENAI_API_KEY", "KRAKEN_DB_PATH"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func contractGRDo(mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var rd *bytes.Reader
	if body == "" {
		rd = bytes.NewReader(nil)
	} else {
		rd = bytes.NewReader([]byte(body))
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

const contractGRRecallBody = `{"session_id":"sess-contract","event":"UserPromptSubmit","prompt":"hello","transcript_path":"","slug":"nobody"}`

// "`POST /api/recall` ... a well-formed body → 200 `{"context": "<string>"}`
// with `""` legal."
func TestGraphRemovalRecallRouteAnswers200WithContextString(t *testing.T) {
	mux := contractGRServe(t)
	rec := contractGRDo(mux, http.MethodPost, "/api/recall", contractGRRecallBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/recall = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	raw, ok := body["context"]
	if !ok {
		t.Fatalf("response has no `context` key: %s", rec.Body.String())
	}
	if _, ok := raw.(string); !ok {
		t.Fatalf("`context` = %#v, want a string", raw)
	}
}

// "Non-POST → 405".
func TestGraphRemovalRecallRouteRefusesNonPOST(t *testing.T) {
	mux := contractGRServe(t)
	rec := contractGRDo(mux, http.MethodGet, "/api/recall", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/recall = %d, want 405", rec.Code)
	}
}

// "a body with an unknown key → 400".
func TestGraphRemovalRecallRouteRefusesUnknownKey(t *testing.T) {
	mux := contractGRServe(t)
	rec := contractGRDo(mux, http.MethodPost, "/api/recall", `{"session_id":"s","querry":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown key = %d (%s), want 400", rec.Code, rec.Body.String())
	}
}

// "registered `mirrorGated` ... Under `--mirror` → 403 like every other gated write."
func TestGraphRemovalRecallRouteIsMirrorGated(t *testing.T) {
	mux := contractGRServe(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	rec := contractGRDo(mux, http.MethodPost, "/api/recall", contractGRRecallBody)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST /api/recall under mirror = %d, want 403", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
		t.Errorf("mirror refusal body = %q, want the shared %q", got, "disabled in mirror mode")
	}
}

// "`/api/graph`, `/api/graph/stats`, ... `/api/graph/recall-rearm` — unregistered —
// Each answers 404 from the mux (any method)."
func TestGraphRemovalGraphRoutesAre404(t *testing.T) {
	mux := contractGRServe(t)
	routes := []string{
		"/api/graph", "/api/graph/stats", "/api/graph/node", "/api/graph/fact",
		"/api/graph/episode", "/api/graph/pending", "/api/graph/search",
		"/api/graph/recall", "/api/graph/add", "/api/graph/episode-done",
		"/api/graph/recall-rearm",
	}
	for _, p := range routes {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			body := ""
			if m == http.MethodPost {
				body = `{}`
			}
			if rec := contractGRDo(mux, m, p, body); rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", m, p, rec.Code)
			}
		}
	}
}

// "`GET /api/mm` — Every item object still carries an integer `episode_id` key
// ... `apiContract` stays 6." / "`POST /api/mm/*` create responses — Carry `id`;
// the `episode` key is absent."
func TestGraphRemovalMMWireKeepsEpisodeIDDropsEpisode(t *testing.T) {
	if apiContract != 6 {
		t.Errorf("apiContract = %d, want 6", apiContract)
	}
	mux := contractGRServe(t)
	rec := contractGRDo(mux, http.MethodPost, "/api/mm/note", string(mustJSON(t, map[string]any{"text": "the graph is gone"})))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/mm/note = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	idf, ok := created["id"].(float64)
	if !ok || idf <= 0 {
		t.Fatalf("create response `id` = %#v, want a row id: %s", created["id"], rec.Body.String())
	}
	if _, present := created["episode"]; present {
		t.Errorf("create response still carries `episode`: %s", rec.Body.String())
	}

	rec = contractGRDo(mux, http.MethodGet, "/api/mm", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm = %d", rec.Code)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, it := range list.Items {
		if n, _ := it["id"].(float64); n == idf {
			found = true
			ep, ok := it["episode_id"]
			if !ok {
				t.Fatalf("item lacks `episode_id`: %v", it)
			}
			f, ok := ep.(float64)
			if !ok || f != float64(int64(f)) {
				t.Fatalf("`episode_id` = %#v, want an integer", ep)
			}
			if f != 0 {
				t.Errorf("`episode_id` = %v on a post-removal item, want 0 (nothing writes it)", f)
			}
		}
	}
	if !found {
		t.Fatalf("created item %v absent from GET /api/mm: %s", idf, rec.Body.String())
	}
}
