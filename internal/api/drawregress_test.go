package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// drawregress_test.go pins the drawings route's answer to a store failure:
// a 500, never the 400 the board reads as a refusal and drops its ops for.

// TestDrawRegressStoreFailureIs500 makes the kv row unreadable the way
// jobs' TestDrawRegressReadErrorRefuses does: kv becomes a view over the real
// table whose value column raises a SQL error on read.
func TestDrawRegressStoreFailureIs500(t *testing.T) {
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	st := useTestStore(t)
	mux := http.NewServeMux()
	Register(mux)
	saved := `{"a":{"kind":"line","pts":[0,0,1,1],"size":2}}`
	if err := st.SetKV("map_drawings", saved); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, q := range []string{
		`ALTER TABLE kv RENAME TO kv_real`,
		`CREATE VIEW kv AS SELECT key, json(value || 'x') AS value FROM kv_real`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	body := `{"set":{"b":{"kind":"line","pts":[5,5,6,6],"size":2}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/map/drawings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("POST with an unreadable row answered %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, rec.Body.String())
	}
	if msg, _ := resp["error"].(string); msg == "" {
		t.Errorf("body %s carries no error text", rec.Body.String())
	}
	var got string
	if err := st.DB().QueryRow(`SELECT value FROM kv_real WHERE key = 'map_drawings'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != saved {
		t.Errorf("the saved row changed to %s, want %s", got, saved)
	}
}
