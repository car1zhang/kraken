package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/store"
)

// handoff_contract_test.go — athena-logic's BLIND contract test for the
// handoff build's slice B wire half (50-contract.md § "Slice B",
// cellContextHandler): the happy path of POST /api/cell/context.
//
// Dimension: LOGIC. The refusal matrix (405, 400s, 404, unknown fields) is
// athena-edge's.
//
// Through the real mux (Register), as the kronos route tests do. Every
// package-level identifier is prefixed `logic`.

func logicCellCtxPost(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/cell/context", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestLogicCellContextRouteRecordsTheReport(t *testing.T) {
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "logic-cellctx"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"slug":                "logic-cellctx",
		"session_id":          "sess-a",
		"used_percentage":     77.5,
		"context_window_size": 1_000_000,
	})
	rec := logicCellCtxPost(t, string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s — want 200", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if len(got) != 1 || got["ok"] != true {
		t.Errorf("body = %s, want {\"ok\":true}", rec.Body.String())
	}

	key := fmt.Sprintf("cell_ctx:%d:sess-a", id)
	raw, err := st.GetKV(key)
	if err != nil {
		t.Fatalf("GetKV %s after an accepted report: %v", key, err)
	}
	var c struct {
		SessionID string  `json:"session_id"`
		Pct       float64 `json:"pct"`
		Window    int64   `json:"window"`
		At        int64   `json:"at"`
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	if c.SessionID != "sess-a" || c.Pct != 77.5 || c.Window != 1_000_000 || c.At == 0 {
		t.Errorf("stored %q, want session sess-a, pct 77.5, window 1000000, a non-zero at", raw)
	}
}
