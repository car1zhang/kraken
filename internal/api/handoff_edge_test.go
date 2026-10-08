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

// handoff_edge_test.go — the EDGE dimension of slice B of
// artifacts/50-contract.md (the handoff skill build), package api half: every
// refusal cellContextHandler states, each asserted as a status code, an
// `error` string, and "nothing was recorded". The happy path belongs to the
// sibling blind file.
//
// Written blind: no byte of the slice-B edits to internal/api (orch.go,
// cellctx.go, register.go) was read while authoring this; orch.go and
// graph.go were read for the since-removed kronos route and decodeGraphBody.
//
// The handler is called DIRECTLY (the contract freezes the symbol), which
// also keeps the mirror gate out of these assertions.
//
// Every package-level identifier here is prefixed `edge`.

const (
	edgeCellCtxSlug    = "edge-cellctx"
	edgeCellCtxSession = "zeus-sess-edge"
)

// edgeCellCtxEnv wires a temp store holding one job and returns it with the
// job's id.
func edgeCellCtxEnv(t *testing.T) (*store.Store, int64) {
	t.Helper()
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: edgeCellCtxSlug, State: "working"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	return st, id
}

func edgeCellCtxCall(t *testing.T, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/cell/context", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	cellContextHandler(rec, req)
	return rec
}

// edgeCellCtxErr asserts the code and that the body is a JSON object with a
// non-empty `error` string.
func edgeCellCtxErr(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d: %s", rec.Code, code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("refusal body is not a JSON object: %v\n%s", err, rec.Body.String())
	}
	if s, _ := out["error"].(string); strings.TrimSpace(s) == "" {
		t.Fatalf("refusal body carries no `error` string: %s", rec.Body.String())
	}
}

// edgeCellCtxNothingRecorded asserts no report exists for the job/session.
func edgeCellCtxNothingRecorded(t *testing.T, st *store.Store, id int64, session string) {
	t.Helper()
	if c, ok := jobs.ReadCellContext(st, id, session); ok {
		t.Fatalf("a refused request recorded %+v", c)
	}
}

func TestEdgeCellContextMethodNotAllowed(t *testing.T) {
	st, id := edgeCellCtxEnv(t)
	valid := `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","used_percentage":50,"context_window_size":200000}`
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(m, func(t *testing.T) {
			rec := edgeCellCtxCall(t, m, valid)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s = %d, want 405: %s", m, rec.Code, rec.Body.String())
			}
			edgeCellCtxNothingRecorded(t, st, id, edgeCellCtxSession)
		})
	}
}

func TestEdgeCellContextBadRequests(t *testing.T) {
	cases := map[string]string{
		"blank slug":            `{"slug":"","session_id":"` + edgeCellCtxSession + `","used_percentage":50,"context_window_size":1}`,
		"whitespace slug":       `{"slug":"   ","session_id":"` + edgeCellCtxSession + `","used_percentage":50,"context_window_size":1}`,
		"absent slug":           `{"session_id":"` + edgeCellCtxSession + `","used_percentage":50,"context_window_size":1}`,
		"blank session":         `{"slug":"` + edgeCellCtxSlug + `","session_id":"","used_percentage":50,"context_window_size":1}`,
		"whitespace session":    `{"slug":"` + edgeCellCtxSlug + `","session_id":" \t ","used_percentage":50,"context_window_size":1}`,
		"absent session":        `{"slug":"` + edgeCellCtxSlug + `","used_percentage":50,"context_window_size":1}`,
		"absent percentage":     `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","context_window_size":1}`,
		"null percentage":       `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","used_percentage":null,"context_window_size":1}`,
		"percentage 101":        `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","used_percentage":101,"context_window_size":1}`,
		"percentage 100.5":      `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","used_percentage":100.5,"context_window_size":1}`,
		"percentage -1":         `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","used_percentage":-1,"context_window_size":1}`,
		"unknown field":         `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","used_percentage":50,"context_window_size":1,"agent":"zeus"}`,
		"kronos-shaped nesting": `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","context_window":{"used_percentage":50}}`,
		"not json":              `slug=edge-cellctx`,
		"empty body":            ``,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			st, id := edgeCellCtxEnv(t)
			rec := edgeCellCtxCall(t, http.MethodPost, body)
			edgeCellCtxErr(t, rec, http.StatusBadRequest)
			edgeCellCtxNothingRecorded(t, st, id, edgeCellCtxSession)
		})
	}
}

func TestEdgeCellContextUnknownSlug404(t *testing.T) {
	st, id := edgeCellCtxEnv(t)
	body := `{"slug":"no-such-job-edge","session_id":"` + edgeCellCtxSession + `","used_percentage":50,"context_window_size":1}`
	rec := edgeCellCtxCall(t, http.MethodPost, body)
	edgeCellCtxErr(t, rec, http.StatusNotFound)
	edgeCellCtxNothingRecorded(t, st, id, edgeCellCtxSession)
}

// The range's two ends are accepted (0..100 inclusive): only OUT of range is
// refused.
func TestEdgeCellContextAcceptsRangeEnds(t *testing.T) {
	for _, pct := range []string{"0", "100"} {
		t.Run(pct, func(t *testing.T) {
			st, id := edgeCellCtxEnv(t)
			body := `{"slug":"` + edgeCellCtxSlug + `","session_id":"` + edgeCellCtxSession + `","used_percentage":` + pct + `,"context_window_size":1}`
			rec := edgeCellCtxCall(t, http.MethodPost, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("pct %s = %d, want 200: %s", pct, rec.Code, rec.Body.String())
			}
			if _, ok := jobs.ReadCellContext(st, id, edgeCellCtxSession); !ok {
				t.Fatalf("pct %s accepted but not recorded", pct)
			}
		})
	}
}
