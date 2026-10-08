package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recall_test.go — POST /api/recall's wire behavior. What Recall delivers
// (goals, notes) is jobs.Recall's and is pinned in internal/jobs; what this
// route owns is the method gate, the strict decode, and the promise that a
// well-formed ask is ALWAYS a 200 whose empty context is an answer ("inject
// nothing") rather than an error — the hook on the other end treats any
// non-200 as silence, so an error status here could only ever suppress a
// delivery, never surface a problem.

func postRecall(body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	recallHandler(rec, httptest.NewRequest(http.MethodPost, "/api/recall", bytes.NewReader([]byte(body))))
	return rec
}

func TestRecallRefusesNonPOST(t *testing.T) {
	useTestStore(t)
	rec := httptest.NewRecorder()
	recallHandler(rec, httptest.NewRequest(http.MethodGet, "/api/recall", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestRecallRefusesMalformedBodies(t *testing.T) {
	useTestStore(t)
	cases := []struct {
		name string
		body string
	}{
		{"not json", `{`},
		// DisallowUnknownFields via decodeStrictBody: a key matching no field
		// is a refusal, not a request that quietly means something else.
		{"unknown field", `{"session_id":"s","querry":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postRecall(c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d (%s), want 400", c.name, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestRecallAnswersEmptyContextAs200(t *testing.T) {
	useTestStore(t)
	// A well-formed ask with nothing to say: the route's contract is 200
	// {"context":""}, never an error status — empty is the common answer,
	// not a failure. All five request keys are accepted.
	rec := postRecall(`{"session_id":"sess-1","event":"Stop","prompt":"anything at all","transcript_path":"","slug":""}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("recall = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"context"`) {
		t.Fatalf("response carries no context key: %s", rec.Body.String())
	}
}
