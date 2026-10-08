package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/store"
)

// kronosremoval_edge_test.go — the EDGE dimension of slice G of
// artifacts/50-contract.md (kronos removal) as it lands in package api: the
// five removed routes answering 404 for EVERY method (a lingering
// method-scoped pattern would answer 405, not 404), with or without mirror
// mode, plus near-miss paths; and /api/limit-hit refusing the removed agent
// id and a case variant of the surviving one while writing nothing.
//
// Every route is driven through the real mux (Register). Package-level
// identifiers carry the `edge` prefix (this package's test namespace is
// shared with athena-logic's sibling file).

var edgeKronosRemovalRoutes = []string{
	"/api/orch/inbox",
	"/api/orch/ack",
	"/api/orch/context",
	"/api/graph/recall-rearm",
	"/api/kronos",
}

// Near misses: a trailing slash, a sub-path and a query string must not find
// a leftover subtree pattern either.
var edgeKronosRemovalNearMisses = []string{
	"/api/orch/",
	"/api/orch",
	"/api/kronos/",
	"/api/kronos/status",
	"/api/orch/inbox?since=0",
	"/api/kronos?x=1",
}

func edgeKronosRemovalMux(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	st := useTestStore(t)
	mux := http.NewServeMux()
	Register(mux)
	return st, mux
}

func edgeKronosRemovalDo(mux http.Handler, method, target, body string) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func edgeKronosRemovalAll404(t *testing.T, mux http.Handler, paths []string) {
	t.Helper()
	for _, p := range paths {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			body := ""
			if m == http.MethodPost {
				body = `{"ids":[1],"pct":50,"session_id":"s"}`
			}
			rec := edgeKronosRemovalDo(mux, m, p, body)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404 (body %q)", m, p, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestKronosRemovalEdgeRemovedRoutesAre404ForBothMethods(t *testing.T) {
	SetMirrorMode(false)
	_, mux := edgeKronosRemovalMux(t)
	edgeKronosRemovalAll404(t, mux, edgeKronosRemovalRoutes)
}

func TestKronosRemovalEdgeRemovedRouteNearMissesAre404(t *testing.T) {
	SetMirrorMode(false)
	_, mux := edgeKronosRemovalMux(t)
	edgeKronosRemovalAll404(t, mux, edgeKronosRemovalNearMisses)
}

// Mirror mode gates writes with a 403; a removed route must not be reachable
// as a gated route there either — 404, not 403. (The contract table names
// only the plain Register mux; 52-testing-plan.md adds "under both normal and
// --mirror registration", which is what this asserts.)
func TestKronosRemovalEdgeRemovedRoutesAre404InMirrorMode(t *testing.T) {
	_, mux := edgeKronosRemovalMux(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	edgeKronosRemovalAll404(t, mux, edgeKronosRemovalRoutes)
}

// The survivors the contract names must still be registered — a removal that
// took a shared prefix with it would 404 them too.
func TestKronosRemovalEdgeSurvivingRoutesStillRegistered(t *testing.T) {
	SetMirrorMode(false)
	_, mux := edgeKronosRemovalMux(t)
	for _, p := range []string{"/api/job/list", "/api/slack/status", "/api/slack/inbox"} {
		if rec := edgeKronosRemovalDo(mux, http.MethodGet, p, ""); rec.Code == http.StatusNotFound {
			t.Errorf("GET %s = 404, want it still registered (body %q)", p, rec.Body.String())
		}
	}
}

// edgeKronosRemovalNoHit asserts nothing was recorded under a hit key.
func edgeKronosRemovalNoHit(t *testing.T, st *store.Store, key string) {
	t.Helper()
	if v, err := st.GetKV(key); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("kv %q was written by a refused request (value %q, err %v)", key, v, err)
	}
}

// {"agent":"kronos"} is now an id off the closed list: 400, the message
// names the list (iris), and nothing is recorded under limit_hit:kronos.
func TestKronosRemovalEdgeLimitHitKronosAgentRefused(t *testing.T) {
	SetMirrorMode(false)
	st, mux := edgeKronosRemovalMux(t)
	for _, body := range []string{
		`{"agent":"kronos","error":"rate_limit"}`,
		`{"agent":"kronos","error":"rate_limit","details":"resets 4:50am (America/New_York)","session_id":"sess-k"}`,
	} {
		rec := edgeKronosRemovalDo(mux, http.MethodPost, "/api/limit-hit", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST /api/limit-hit %s = %d, want 400 (body %q)", body, rec.Code, rec.Body.String())
		}
		msg := rec.Body.String()
		if !strings.Contains(msg, "iris") {
			t.Errorf("refusal %q does not name the closed list (iris)", msg)
		}
	}
	edgeKronosRemovalNoHit(t, st, "limit_hit:kronos")
}

// The closed list is case-sensitive: "Iris" is not "iris", and the refusal
// must not record under either spelling.
func TestKronosRemovalEdgeLimitHitCaseVariantRefused(t *testing.T) {
	SetMirrorMode(false)
	st, mux := edgeKronosRemovalMux(t)
	for _, id := range []string{"Iris", "IRIS", "KRONOS"} {
		body := `{"agent":"` + id + `","error":"rate_limit"}`
		rec := edgeKronosRemovalDo(mux, http.MethodPost, "/api/limit-hit", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST /api/limit-hit agent %q = %d, want 400 (body %q)", id, rec.Code, rec.Body.String())
		}
		edgeKronosRemovalNoHit(t, st, "limit_hit:"+id)
	}
	edgeKronosRemovalNoHit(t, st, "limit_hit:iris")
	edgeKronosRemovalNoHit(t, st, "limit_hit:kronos")
}

// Counter-check at the same boundary: the surviving id is still accepted, so
// the refusals above are about the list, not about the agent arm being gone.
func TestKronosRemovalEdgeLimitHitIrisStillAccepted(t *testing.T) {
	SetMirrorMode(false)
	st, mux := edgeKronosRemovalMux(t)
	rec := edgeKronosRemovalDo(mux, http.MethodPost, "/api/limit-hit", `{"agent":"iris","error":"rate_limit"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf(`POST /api/limit-hit {"agent":"iris"} = %d, want 200 (body %q)`, rec.Code, rec.Body.String())
	}
	if v, err := st.GetKV("limit_hit:iris"); err != nil || v == "" {
		t.Fatalf("iris hit not recorded (value %q, err %v)", v, err)
	}
	edgeKronosRemovalNoHit(t, st, "limit_hit:kronos")
}
