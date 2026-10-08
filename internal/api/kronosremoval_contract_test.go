package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/internal/jobs"
)

// kronosremoval_contract_test.go — athena-logic's BLIND contract tests for
// the kronos removal's API half (artifacts/50-contract.md slice G, rows
// `api.Register` and `POST /api/limit-hit`). Written from the contract text
// alone; no non-test file in this package was read. Dimension: LOGIC — the
// five kronos routes are gone from the registered mux, the routes iris and
// the cells still use are still there, and iris' limit-hit delivery is
// accepted and recorded exactly as before. The kronos-body 400 is athena-edge's.

func contractKronosRemovalMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func contractKronosRemovalDo(t *testing.T, mux http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// contractKronosRemovalNoSlack keeps the developer's shell credentials out of
// the Slack handlers, so the survival probes below measure registration only.
func contractKronosRemovalNoSlack(t *testing.T) {
	t.Helper()
	t.Setenv("SLACK_USER_TOKEN", "")
	t.Setenv("SLACK_D_COOKIE", "")
	t.Setenv("SLACK_USER_ID", "")
}

// Contract: after Register, each of the five kronos routes answers 404.
func TestKronosRemovalContractKronosRoutesAre404(t *testing.T) {
	useTestStore(t)
	mux := contractKronosRemovalMux(t)
	for _, c := range []struct{ method, target, body string }{
		{http.MethodGet, "/api/orch/inbox", ""},
		{http.MethodPost, "/api/orch/ack", `{"ids":[1]}`},
		{http.MethodPost, "/api/orch/context", `{"pct":50}`},
		{http.MethodPost, "/api/graph/recall-rearm", `{"session_id":"s"}`},
		{http.MethodGet, "/api/kronos", ""},
	} {
		rec := contractKronosRemovalDo(t, mux, c.method, c.target, c.body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (route removed); body %s", c.method, c.target, rec.Code, rec.Body.String())
		}
	}
}

// Contract: GET /api/job/list, /api/slack/status, /api/slack/inbox are still
// registered — any status other than 404.
func TestKronosRemovalContractSurvivingRoutesStillRegistered(t *testing.T) {
	useTestStore(t)
	contractKronosRemovalNoSlack(t)
	mux := contractKronosRemovalMux(t)
	for _, target := range []string{"/api/job/list", "/api/slack/status", "/api/slack/inbox"} {
		rec := contractKronosRemovalDo(t, mux, http.MethodGet, target, "")
		if rec.Code == http.StatusNotFound {
			t.Errorf("GET %s = 404 — the route iris/cells depend on was unregistered; body %s", target, rec.Body.String())
		}
	}
}

// Contract: `{"agent":"iris","error":"rate_limit"}` is accepted exactly as
// today — 200, recorded under limit_hit:iris with the delivered fields.
func TestKronosRemovalContractIrisLimitHitAccepted(t *testing.T) {
	st := useTestStore(t)
	mux := contractKronosRemovalMux(t)

	body, _ := json.Marshal(map[string]string{
		"agent":      "iris",
		"error":      "rate_limit",
		"details":    "You've hit your session limit",
		"session_id": "iris-sess",
		"agent_id":   "",
	})
	rec := contractKronosRemovalDo(t, mux, http.MethodPost, "/api/limit-hit", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/limit-hit agent=iris = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	raw, err := st.GetKV("limit_hit:iris")
	if err != nil || raw == "" {
		t.Fatalf("nothing recorded under limit_hit:iris (%q, %v)", raw, err)
	}
	var hit jobs.LimitHit
	if err := json.Unmarshal([]byte(raw), &hit); err != nil {
		t.Fatalf("limit_hit:iris is not a LimitHit: %v (%q)", err, raw)
	}
	if hit.At == 0 {
		t.Error("recorded hit has no At")
	}
	if hit.Error != "rate_limit" {
		t.Errorf("Error = %q, want rate_limit", hit.Error)
	}
	if hit.SessionID != "iris-sess" {
		t.Errorf("SessionID = %q, want iris-sess", hit.SessionID)
	}
	if hit.Deliveries != 1 {
		t.Errorf("Deliveries = %d, want 1", hit.Deliveries)
	}

	// The iris delivery must not have minted a kronos record beside it.
	if v, _ := st.GetKV("limit_hit:kronos"); v != "" {
		t.Errorf("an iris delivery wrote limit_hit:kronos = %q", v)
	}
}

// Contract: the two-field minimal body the contract names verbatim is also
// accepted (200) — a hook that sends only agent + error is today's shape.
func TestKronosRemovalContractIrisMinimalBodyAccepted(t *testing.T) {
	useTestStore(t)
	mux := contractKronosRemovalMux(t)
	rec := contractKronosRemovalDo(t, mux, http.MethodPost, "/api/limit-hit", `{"agent":"iris","error":"rate_limit"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf(`POST {"agent":"iris","error":"rate_limit"} = %d, want 200; body %s`, rec.Code, rec.Body.String())
	}
}
