package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recall_test.go — POST /api/graph/recall's wire behavior. The retrieval,
// gating and dedup semantics behind it are jobs.Recall's and are pinned in
// internal/jobs; what this route owns is the method gate, the strict decode,
// and the promise that a well-formed ask is ALWAYS a 200 whose empty context
// is an answer ("inject nothing") rather than an error — the hook on the
// other end treats any non-200 as silence, so an error status here could
// only ever suppress a delivery, never surface a problem.

func TestGraphRecallRefusesNonPOST(t *testing.T) {
	useTestStore(t)
	rec := httptest.NewRecorder()
	graphRecallHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/recall", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestGraphRecallRefusesMalformedBodies(t *testing.T) {
	useTestStore(t)
	cases := []struct {
		name string
		body string
	}{
		{"not json", `{`},
		// DisallowUnknownFields via decodeGraphBody: a key matching no field
		// is a refusal, not a request that quietly means something else.
		{"unknown field", `{"session_id":"s","querry":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postGraph(graphRecallHandler, "/api/graph/recall", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d (%s), want 400", c.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestGraphRecallAnswersEmptyContextAs200(t *testing.T) {
	useTestStore(t)
	// A well-formed ask against a graph with nothing to say: the route's
	// contract is 200 {"context":""}, never an error status — empty is the
	// common answer, not a failure.
	rec := postGraph(graphRecallHandler, "/api/graph/recall",
		`{"session_id":"sess-1","event":"Stop","prompt":"anything at all","transcript_path":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"context"`) {
		t.Fatalf("response carries no context key: %s", rec.Body.String())
	}
}
