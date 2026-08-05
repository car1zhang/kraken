package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// limithit_contract_test.go — blind contract tests for POST /api/limit-hit
// (50-contract.md, slice A, row `api.limitHitHandler`). Authored blind from
// the contract text alone, then re-synced to Amendment 5, which rekeyed the
// stored record on the job's immutable ID: the fixtures now hold a *Job
// rather than a slug, since that is what jobs.ReadLimitHit takes. Every
// assertion is the one the blind author wrote.
//
// Dimension: LOGIC — the accepted shape, the recording it performs, the
// dedupe fold seen through the route, the details normalisation, and the
// mirror-gate exemption. The 405/400 refusals are a sibling author's.
//
// The contract froze no request STRUCT for this route, only the five wire
// keys, so every body here is raw JSON rather than a typed value — a test
// naming a struct the contract never froze would be asserting the
// implementation's private shape.
//
// Every package-level identifier here is prefixed `contractLimitHit`.

// contractLimitHitMux registers the whole API surface, which is how the route
// registration itself gets asserted rather than assumed.
func contractLimitHitMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

// contractLimitHitBody renders the five-key body the route accepts.
func contractLimitHitBody(slug, errType, details, sessionID, agentID string) []byte {
	b, _ := json.Marshal(map[string]string{
		"slug":       slug,
		"error":      errType,
		"details":    details,
		"session_id": sessionID,
		"agent_id":   agentID,
	})
	return b
}

// contractLimitHitPost drives the request through the registered mux — the
// same path a real hook process's POST takes.
func contractLimitHitPost(t *testing.T, mux http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/limit-hit", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// contractLimitHitJob wires a store and creates one job, returning the *Job
// itself rather than its slug: Amendment 5 rekeyed the record on the job's
// immutable ID, so jobs.ReadLimitHit takes the job and a slug is no longer
// enough to find the row the handler just wrote.
func contractLimitHitJob(t *testing.T, slug string) *jobs.Job {
	t.Helper()
	useTestStore(t)
	return mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: slug, Repo: "kraken", State: "working"})
}

// TestContractLimitHitRouteIsRegistered: the route must exist on the shared
// mux beside /api/pr-attach. Registered nowhere, every hook delivery on this
// machine 404s silently and the feature is inert with nothing to notice it.
func TestContractLimitHitRouteIsRegistered(t *testing.T) {
	j := contractLimitHitJob(t, "limit-registered")
	rec := contractLimitHitPost(t, contractLimitHitMux(t), contractLimitHitBody(j.Slug, "rate_limit", "", "s1", ""))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("POST /api/limit-hit = 404 — the route is not registered")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
}

// TestContractLimitHitRecordsTheDelivery is the happy path end to end: an
// accepted delivery is recorded through jobs.RecordLimitHit, so the stored
// record carries every field the body named.
func TestContractLimitHitRecordsTheDelivery(t *testing.T) {
	j := contractLimitHitJob(t, "limit-record")
	mux := contractLimitHitMux(t)

	rec := contractLimitHitPost(t, mux, contractLimitHitBody(j.Slug, "rate_limit", "You've hit your session limit", "zeus-sess", "agent-7"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}

	hit := jobs.ReadLimitHit(j)
	if hit.At == 0 {
		t.Fatal("nothing recorded — ReadLimitHit returned the zero value")
	}
	if hit.Error != "rate_limit" {
		t.Errorf("Error = %q, want rate_limit", hit.Error)
	}
	if hit.Details != "You've hit your session limit" {
		t.Errorf("Details = %q", hit.Details)
	}
	if hit.SessionID != "zeus-sess" {
		t.Errorf("SessionID = %q, want zeus-sess", hit.SessionID)
	}
	if hit.AgentID != "agent-7" {
		t.Errorf("AgentID = %q, want agent-7", hit.AgentID)
	}
	if hit.Deliveries != 1 {
		t.Errorf("Deliveries = %d, want 1", hit.Deliveries)
	}
	if hit.HandledAt != 0 {
		t.Errorf("HandledAt = %d, want 0 — a fresh delivery is unresolved", hit.HandledAt)
	}
}

// TestContractLimitHitFoldsTheTeammateHerd is the reason the dedupe lives in
// the route rather than the hook: every claude in the cell shares one settings
// file, so one limit fires N hooks within seconds. All of them are accepted
// with a 200 — a suppressed duplicate is not an error — and the job carries
// ONE hit.
func TestContractLimitHitFoldsTheTeammateHerd(t *testing.T) {
	j := contractLimitHitJob(t, "limit-herd")
	mux := contractLimitHitMux(t)

	for i := 0; i < 4; i++ {
		rec := contractLimitHitPost(t, mux, contractLimitHitBody(j.Slug, "rate_limit", "session limit", "sess", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("delivery %d: status = %d, want 200; body %s", i+1, rec.Code, rec.Body.String())
		}
	}
	hit := jobs.ReadLimitHit(j)
	if hit.Deliveries != 4 {
		t.Errorf("Deliveries = %d, want 4 — the herd did not fold into one hit", hit.Deliveries)
	}
	if hit.HandledAt != 0 {
		t.Errorf("HandledAt = %d, want 0", hit.HandledAt)
	}
}

// TestContractLimitHitRecordsEveryErrorType: the route records deliveries the
// rescue never acts on, because that record is how a future error-type mapping
// change gets noticed. Refusing them would delete the only evidence.
func TestContractLimitHitRecordsEveryErrorType(t *testing.T) {
	for _, errType := range []string{"authentication_failed", "billing_error", "overloaded", "max_output_tokens", "some_future_type"} {
		t.Run(errType, func(t *testing.T) {
			j := contractLimitHitJob(t, "limit-type-"+strings.ReplaceAll(errType, "_", "-"))
			rec := contractLimitHitPost(t, contractLimitHitMux(t), contractLimitHitBody(j.Slug, errType, "d", "s", ""))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 — a non-rate_limit delivery is recorded, not refused; body %s", rec.Code, rec.Body.String())
			}
			if got := jobs.ReadLimitHit(j); got.Error != errType {
				t.Errorf("stored Error = %q, want the delivered %q verbatim", got.Error, errType)
			}
		})
	}
}

// TestContractLimitHitTruncatesDetails: `details` is stored one-lined and
// truncated through core.TruncateOneLine at 200 runes RATHER THAN REFUSED —
// the error type is the payload, and losing a delivery over a verbose message
// would be the worse answer. Expected values are computed with the function
// the contract names, so this asserts the argument rather than re-deriving
// TruncateOneLine's own behaviour.
func TestContractLimitHitTruncatesDetails(t *testing.T) {
	long := "You've hit your session limit\nresets 5pm\r\n" + strings.Repeat("padding text ", 40)
	j := contractLimitHitJob(t, "limit-truncate")

	rec := contractLimitHitPost(t, contractLimitHitMux(t), contractLimitHitBody(j.Slug, "rate_limit", long, "s", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a long message must be truncated, not refused; body %s", rec.Code, rec.Body.String())
	}
	got := jobs.ReadLimitHit(j).Details
	if want := core.TruncateOneLine(long, 200); got != want {
		t.Errorf("Details = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("Details = %q, want a single line", got)
	}
}

// TestContractLimitHitIsNotMirrorGated: "registered ... not mirror-gated". A
// mirror daemon must RECORD hits (it just never rescues), so the route has to
// stay live while mirror mode is on — a 403 here would silently drop the
// evidence the whole feature is built on.
func TestContractLimitHitIsNotMirrorGated(t *testing.T) {
	j := contractLimitHitJob(t, "limit-mirror")
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	rec := contractLimitHitPost(t, contractLimitHitMux(t), contractLimitHitBody(j.Slug, "rate_limit", "d", "s", ""))
	if rec.Code == http.StatusForbidden {
		t.Fatalf("status = 403 in mirror mode — the route must record hits even on a mirror")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if got := jobs.ReadLimitHit(j); got.At == 0 {
		t.Error("nothing recorded in mirror mode")
	}
}

// TestContractLimitHitHandlerReachableDirectly pins the frozen symbol itself:
// limitHitHandler is an http.HandlerFunc-shaped function, callable without the
// mux — the shape every other handler in this package is tested at, and the
// one the register table depends on.
func TestContractLimitHitHandlerReachableDirectly(t *testing.T) {
	j := contractLimitHitJob(t, "limit-direct")
	req := httptest.NewRequest(http.MethodPost, "/api/limit-hit", bytes.NewReader(contractLimitHitBody(j.Slug, "rate_limit", "d", "s", "a")))
	rec := httptest.NewRecorder()
	limitHitHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	if got := jobs.ReadLimitHit(j); got.Error != "rate_limit" {
		t.Errorf("stored record = %+v", got)
	}
}
