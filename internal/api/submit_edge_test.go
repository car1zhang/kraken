package api

// submit_edge_test.go — BLIND edge tests for "eliminate the queued status"
// (artifacts/50-contract.md, slice S1 API layer). Authored from the frozen
// contract with no sight of the implementation.
//
// Dimension: boundaries, error paths, refusals. Everything here is a request
// the API must turn away — a slug that names nothing, a POST to a mirror
// board, a POST to a route that no longer exists, a reorder naming the
// retired column — plus the one success-shaped case whose payload is an
// error string that must not be rewritten on its way out.
//
// Every package-level identifier below is prefixed `edge`, and every test
// func `TestSubmitEdge`, so this file can sit beside the logic author's twin
// in the same package.

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// --- fixtures -------------------------------------------------------------

// edgeMux builds the real mux api.Register wires, the same one web.Serve
// mounts. Route-existence and gate-ordering claims are only true of the mux —
// calling a handler function directly cannot show that a path 404s or that
// the mirror gate runs before the body is decoded.
func edgeMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func edgePost(t *testing.T, mux http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// edgeSubmitRecorder counts what reached the jobs layer. "Never reached the
// seam" is the load-bearing half of every refusal below: a real
// jobs.SubmitJob would resolve a repo and spawn a cell.
type edgeSubmitRecorder struct {
	calls int
	job   *jobs.Job
}

// edgeStubSubmit swaps the api-layer seam the contract names, restoring it on
// cleanup.
func edgeStubSubmit(t *testing.T, ret error) *edgeSubmitRecorder {
	t.Helper()
	rec := &edgeSubmitRecorder{}
	prev := submitJobFn
	submitJobFn = func(j *jobs.Job) error {
		rec.calls++
		rec.job = j
		return ret
	}
	t.Cleanup(func() { submitJobFn = prev })
	return rec
}

// edgeCreateJob seeds one store-backed job. Deliberately does NOT set any
// retired field, so it keeps compiling once store.Job loses Ready/Prereqs.
func edgeCreateJob(t *testing.T, slug string) {
	t.Helper()
	st := jobs.ActiveStore()
	if st == nil {
		t.Fatal("edgeCreateJob needs a wired store — call useTestStore(t) first")
	}
	j := store.Job{Slug: slug, Repo: "monolith", Body: "\n# Spec\nreal content\n"}
	if _, err := st.CreateJob(&j); err != nil {
		t.Fatalf("create job %s: %v", slug, err)
	}
}

// --- routes that must be GONE --------------------------------------------

// TestSubmitEdgeDequeueAndStartRoutesAreUnregistered pins the deletion at the
// mux, which is the only place it can be pinned: deleting webDequeue/webStart
// while leaving their patterns registered would give a shipped iOS build a
// live endpoint backed by nothing. 404 from an unregistered path is the
// contract's stated answer, and it is distinguishable from every handler
// outcome (200/403/409).
func TestSubmitEdgeDequeueAndStartRoutesAreUnregistered(t *testing.T) {
	SetMirrorMode(false)
	mux := edgeMux(t)

	for _, path := range []string{"/api/dequeue", "/api/start"} {
		t.Run(path, func(t *testing.T) {
			rec := edgePost(t, mux, path, `{"slug":"anything"}`)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("POST %s: status = %d, want 404 — this route is retired, not merely inert (body %s)",
					path, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestSubmitEdgeQueueRouteSurvives is the converse, and it is why the test
// above is not vacuous: /api/queue keeps its URL (Q4 — shipped iOS builds
// post it), so a mux that 404s everything would pass the deletion test while
// breaking the one route that had to stay.
func TestSubmitEdgeQueueRouteSurvives(t *testing.T) {
	SetMirrorMode(false)
	useTestStore(t)
	edgeStubSubmit(t, nil)
	mux := edgeMux(t)

	rec := edgePost(t, mux, "/api/queue", `{"slug":"edge-no-such-job"}`)
	if rec.Code == http.StatusNotFound {
		t.Fatal("POST /api/queue: 404 — the submit URL must survive the rename of its handler")
	}
}

// --- the mirror gate ------------------------------------------------------

// TestSubmitEdgeMirrorGatesSubmit is the guarantee TestTickMirrorNeverStartsACell
// used to hold, moved to where the start now lives. A mirror board's drag
// must never spawn a cell on the machine it is mirroring, and the refusal has
// to land BEFORE the body is decoded — so an unparseable body, an empty body
// and a well-formed one all get the same 403, and the seam is never reached.
func TestSubmitEdgeMirrorGatesSubmit(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	useTestStore(t)
	rec := edgeStubSubmit(t, nil)
	mux := edgeMux(t)

	for _, body := range []string{`{"slug":"edge-mirror"}`, `{`, ``, `{"slug":""}`} {
		resp := edgePost(t, mux, "/api/queue", body)
		if resp.Code != http.StatusForbidden {
			t.Fatalf("body %q: status = %d, want 403 (body %s)", body, resp.Code, resp.Body.String())
		}
		if got := strings.TrimSpace(resp.Body.String()); got != "disabled in mirror mode" {
			t.Fatalf("body %q: response = %q, want %q", body, got, "disabled in mirror mode")
		}
	}
	if rec.calls != 0 {
		t.Fatalf("submitJobFn called %d times under mirror mode, want 0 — the gate runs before the body is decoded", rec.calls)
	}
}

// TestSubmitEdgeMirrorOffLeavesSubmitLive is the other half: the gate must be
// conditional, not a permanent 403. With mirror mode off the same POST has to
// reach the handler's own validation.
func TestSubmitEdgeMirrorOffLeavesSubmitLive(t *testing.T) {
	SetMirrorMode(false)
	useTestStore(t)
	edgeStubSubmit(t, nil)
	mux := edgeMux(t)

	resp := edgePost(t, mux, "/api/queue", `{"slug":"edge-no-such-job"}`)
	if resp.Code == http.StatusForbidden {
		t.Fatalf("POST /api/queue: 403 with mirror mode OFF — the gate must be conditional (body %s)", resp.Body.String())
	}
}

// --- handler refusals -----------------------------------------------------

// TestSubmitEdgeUnknownSlug: a slug naming no job is an error, and it must be
// decided before the seam. The board sends a slug it last saw on a card, so a
// stale drag after an archive lands here — a submit that fell through to the
// jobs layer with a nil job would panic in a handler that has no recover.
func TestSubmitEdgeUnknownSlug(t *testing.T) {
	useTestStore(t)
	rec := edgeStubSubmit(t, nil)

	for _, slug := range []string{"edge-no-such-job", "", "   "} {
		err := webSubmit(webReq{Slug: slug})
		if err == nil {
			t.Fatalf("webSubmit(slug=%q) = nil, want an error", slug)
		}
	}
	if rec.calls != 0 {
		t.Fatalf("submitJobFn called %d times for slugs that name no job, want 0", rec.calls)
	}
}

// TestSubmitEdgeSurfacesSeamErrorVerbatim: the whole point of doing the start
// synchronously is that carl reads the failure on the card. Any wrapping
// ("submit failed: %w", "could not queue job: %w") pushes the actionable half
// of the sentence out of the toast. Checked at the function boundary AND
// through the mux, since the webAction wrapper is what puts it on the wire.
func TestSubmitEdgeSurfacesSeamErrorVerbatim(t *testing.T) {
	SetMirrorMode(false)
	useTestStore(t)
	edgeCreateJob(t, "edge-verbatim")
	want := "repo monolith: worktree /Users/x/.kraken/cells/edge-verbatim-9/wt-monolith already exists"
	rec := edgeStubSubmit(t, errors.New(want))

	err := webSubmit(webReq{Slug: "edge-verbatim"})
	if err == nil {
		t.Fatal("webSubmit = nil, want the seam's error")
	}
	if err.Error() != want {
		t.Fatalf("webSubmit error = %q, want it VERBATIM: %q", err.Error(), want)
	}
	if rec.calls != 1 {
		t.Fatalf("submitJobFn called %d times, want exactly 1", rec.calls)
	}
	if rec.job == nil || rec.job.Slug != "edge-verbatim" {
		t.Fatalf("seam got %+v, want the job named by the request", rec.job)
	}

	mux := edgeMux(t)
	resp := edgePost(t, mux, "/api/queue", `{"slug":"edge-verbatim"}`)
	if resp.Code != http.StatusConflict {
		t.Fatalf("POST /api/queue: status = %d, want 409 (body %s)", resp.Code, resp.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 409 body %s: %v", resp.Body.String(), err)
	}
	if body["error"] != want {
		t.Fatalf("409 error = %q, want the seam's text VERBATIM: %q", body["error"], want)
	}
}

// TestSubmitEdgeSuccessOnlyAfterTheSeamReturnsNil: the response is the
// promise that the cell is up. A handler that answered 200 before (or
// regardless of) the seam would re-introduce exactly the asynchrony this
// change deletes — the board would show a card in working with nothing
// behind it.
func TestSubmitEdgeSuccessOnlyAfterTheSeamReturnsNil(t *testing.T) {
	SetMirrorMode(false)
	useTestStore(t)
	edgeCreateJob(t, "edge-success")
	rec := edgeStubSubmit(t, nil)

	if err := webSubmit(webReq{Slug: "edge-success"}); err != nil {
		t.Fatalf("webSubmit = %v, want nil once the seam returned nil", err)
	}
	if rec.calls != 1 {
		t.Fatalf("submitJobFn called %d times, want exactly 1", rec.calls)
	}
}

// --- reorder --------------------------------------------------------------

// TestSubmitEdgeReorderRefusesQueuedColumn pins reorderColumns from the wire.
// A shipped iOS build and a cached SPA both still know the queued column, and
// a saved order for a column no client renders is a board_order row nothing
// will ever prune (the table has no CHECK constraint) — so the refusal is
// what keeps the migration's DELETE from being undone the first time an old
// client drags a card.
func TestSubmitEdgeReorderRefusesQueuedColumn(t *testing.T) {
	SetMirrorMode(false)
	useTestStore(t)
	mux := edgeMux(t)

	resp := edgePost(t, mux, "/api/reorder", `{"column":"queued","slugs":["edge-a","edge-b"]}`)
	if resp.Code != http.StatusConflict {
		t.Fatalf("reorder column=queued: status = %d, want 409 (body %s)", resp.Code, resp.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 409 body %s: %v", resp.Body.String(), err)
	}
	if !strings.Contains(body["error"], "unknown column") {
		t.Fatalf("reorder error = %q, want it to name the unknown column", body["error"])
	}
}

// TestSubmitEdgeReorderKeepsTheFourLiveColumns is the converse that stops the
// test above passing against a handler that refuses everything: each of the
// four surviving columns must still get past the column check.
func TestSubmitEdgeReorderKeepsTheFourLiveColumns(t *testing.T) {
	SetMirrorMode(false)
	useTestStore(t)
	mux := edgeMux(t)

	for _, col := range []string{"draft", "working", "blocked", "done"} {
		t.Run(col, func(t *testing.T) {
			resp := edgePost(t, mux, "/api/reorder", `{"column":"`+col+`","slugs":[]}`)
			if resp.Code != http.StatusOK {
				t.Fatalf("reorder column=%s: status = %d, want 200 (body %s)", col, resp.Code, resp.Body.String())
			}
		})
	}
}

// TestSubmitEdgeReorderStillRefusesAnOutrightBogusColumn keeps the retired
// name from being special-cased into a one-off: "queued" is refused because
// it is not in reorderColumns, the same way any other string is.
func TestSubmitEdgeReorderStillRefusesAnOutrightBogusColumn(t *testing.T) {
	SetMirrorMode(false)
	useTestStore(t)
	mux := edgeMux(t)

	for _, col := range []string{"bogus", "", "archived", "Queued"} {
		resp := edgePost(t, mux, "/api/reorder", `{"column":"`+col+`","slugs":["x"]}`)
		if resp.Code != http.StatusConflict {
			t.Fatalf("reorder column=%q: status = %d, want 409 (body %s)", col, resp.Code, resp.Body.String())
		}
	}
}
