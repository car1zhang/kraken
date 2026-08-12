package api

import (
	"net/http"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// jobrename_edge_test.go — the EDGE dimension of the rename feature's API half
// (50-contract.md, slice B): every refusal shape POST /api/job/rename owes, and
// the refusal half of the dormant path being closed in jobedit.go.
//
// Every request goes through the real mux (api.Register), the wiring web.Serve
// uses — which is also what makes "the route is registered at all" part of the
// coverage, and what keeps these tests independent of the handler function's
// name (a contract silence). Helpers are `edgeRen`-prefixed for the collision
// reason every *_edge_test.go in this package prefixes.

const edgeRenPath = "/api/job/rename"

// edgeRenMux wires a temp store and returns the registered mux.
func edgeRenMux(t *testing.T) (*store.Store, http.Handler) {
	t.Helper()
	st := useTestStore(t)
	mux := http.NewServeMux()
	Register(mux)
	return st, mux
}

// edgeRenSlugOf reads a job's current slug straight from the store, so a
// refusal that renamed anyway cannot hide behind a cached board payload.
func edgeRenSlugOf(t *testing.T, st *store.Store, id int64) string {
	t.Helper()
	row, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", id, err)
	}
	return row.Slug
}

// TestEdgeRenameRouteRefusesNonPost covers "non-POST refused (405)". The route
// takes no GET at all — unlike /api/job/load, whose GET is a real read — so
// every other method is a method error rather than a decode error.
func TestEdgeRenameRouteRefusesNonPost(t *testing.T) {
	_, mux := edgeRenMux(t)

	if rec := edgeGetAPI(t, mux, edgeRenPath); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET %s: status %d, want 405 (body %s)", edgeRenPath, rec.Code, rec.Body.String())
	}
}

// TestEdgeRenameRouteRefusesUnknownFields covers the DisallowUnknownFields
// posture the contract borrows from jobload: a key matching NO field is the
// refusal itself, so a misspelling or a nested body is a 400 rather than a
// request that means something else.
func TestEdgeRenameRouteRefusesUnknownFields(t *testing.T) {
	st, mux := edgeRenMux(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-ren-api", Repo: "monolith"})

	for _, body := range []string{
		`{"slug":"edge-ren-api","to":"edge-ren-api-new","extra":1}`,
		`{"slug":"edge-ren-api","too":"edge-ren-api-new"}`,
		`{"slug":"edge-ren-api","rename":{"to":"edge-ren-api-new"}}`,
		`{"job":"edge-ren-api","to":"edge-ren-api-new"}`,
	} {
		rec := edgePostAPIRaw(t, mux, edgeRenPath, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status %d, want 400 (%s)", body, rec.Code, rec.Body.String())
		}
		if got := edgeRenSlugOf(t, st, j.ID); got != "edge-ren-api" {
			t.Fatalf("body %s renamed the job anyway: slug %q", body, got)
		}
	}
}

// TestEdgeRenameRouteRefusesHalfRequests covers "missing/empty field ...
// refused (400)". Both fields are required, and an EMPTY one is refused rather
// than coerced — an empty `to` is not a clear of anything, and an empty `slug`
// addresses no job.
func TestEdgeRenameRouteRefusesHalfRequests(t *testing.T) {
	st, mux := edgeRenMux(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-ren-half", Repo: "monolith"})

	for _, body := range []string{
		`{}`,
		`null`,
		`{"slug":"edge-ren-half"}`,
		`{"to":"edge-ren-half-new"}`,
		`{"slug":"edge-ren-half","to":""}`,
		`{"slug":"","to":"edge-ren-half-new"}`,
		`{"slug":"","to":""}`,
	} {
		rec := edgePostAPIRaw(t, mux, edgeRenPath, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status %d, want 400 (%s)", body, rec.Code, rec.Body.String())
		}
		if got := edgeRenSlugOf(t, st, j.ID); got != "edge-ren-half" {
			t.Fatalf("body %s renamed the job anyway: slug %q", body, got)
		}
	}

	// malformed JSON is the same 400, not a 500.
	if rec := edgePostAPIRaw(t, mux, edgeRenPath, `{"slug":`); rec.Code != http.StatusBadRequest {
		t.Errorf("truncated json: status %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestEdgeRenameRouteRefusesOversizeBody covers "body capped at 1MB with 413
// beyond (the jobcolor pattern)": the whole legitimate request is two short
// strings, so an oversize one is refused rather than read into memory, and its
// 413 stays distinct from a decode error's 400.
func TestEdgeRenameRouteRefusesOversizeBody(t *testing.T) {
	st, mux := edgeRenMux(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-ren-big", Repo: "monolith"})

	rec := edgePostAPI(t, mux, edgeRenPath, map[string]any{
		"slug": "edge-ren-big",
		"to":   strings.Repeat("z", 2<<20),
	})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413 (%s)", rec.Code, rec.Body.String())
	}
	if got := edgeRenSlugOf(t, st, j.ID); got != "edge-ren-big" {
		t.Errorf("oversize body renamed the job: slug %q", got)
	}
}

// TestEdgeRenameRouteUnknownSlug covers "unknown slug → 404", and the narrower
// half of the same clause: the route "Resolves the job by request slug (current
// slugs only — renaming BY a former name is not supported)", so a FORMER slug
// is a 404 here even though it resolves everywhere else.
func TestEdgeRenameRouteUnknownSlug(t *testing.T) {
	st, mux := edgeRenMux(t)

	if rec := edgePostAPI(t, mux, edgeRenPath, map[string]any{
		"slug": "edge-ren-nobody", "to": "edge-ren-somebody",
	}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug: status %d, want 404 (%s)", rec.Code, rec.Body.String())
	}

	// rename once so a former slug exists, then address the job by it.
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-ren-was", Repo: "monolith"})
	if _, err := jobs.RenameJob(j, "edge-ren-is"); err != nil {
		t.Fatalf("seed rename: %v", err)
	}
	rec := edgePostAPI(t, mux, edgeRenPath, map[string]any{
		"slug": "edge-ren-was", "to": "edge-ren-third",
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("rename BY a former slug: status %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
	if got := edgeRenSlugOf(t, st, j.ID); got != "edge-ren-is" {
		t.Errorf("slug = %q, want edge-ren-is — a 404'd request must change nothing", got)
	}
}

// TestEdgeRenameRouteGateRefusalIs409 covers "a jobs.RenameJob refusal → 409
// {\"error\": \"<the refusal text>\"}": the status separates a refusal from a
// bad request (400) and from a missing job (404), and the body carries the
// gate's own sentence so the client can render it inline.
func TestEdgeRenameRouteGateRefusalIs409(t *testing.T) {
	st, mux := edgeRenMux(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-ren-occupied", Repo: "monolith"})
	mover := mustCreateStoreJob(t, st, store.Job{Slug: "edge-ren-gate", Repo: "monolith"})

	// gate 6 (taken by another active job), reached through the slugified form
	// so the pre-check's rule is exercised end to end.
	rec := edgePostAPI(t, mux, edgeRenPath, map[string]any{
		"slug": "edge-ren-gate", "to": "Edge Ren Occupied",
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("taken target: status %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if got := edgeErrField(t, rec); !strings.Contains(got, "taken") {
		t.Errorf("error = %q, want the gate's own refusal text", got)
	}
	if got := edgeRenSlugOf(t, st, mover.ID); got != "edge-ren-gate" {
		t.Errorf("slug = %q, want edge-ren-gate", got)
	}

	// gate 1 (unusable target) is the same 409 — a refusal is a refusal,
	// whichever gate raised it.
	rec = edgePostAPI(t, mux, edgeRenPath, map[string]any{"slug": "edge-ren-gate", "to": "!!!"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("unusable target: status %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if got := edgeErrField(t, rec); !strings.Contains(got, "slugifies to nothing") {
		t.Errorf("error = %q, want the gate's own refusal text", got)
	}
}

// TestEdgeRenameRouteIsMirrorGated covers "Mirror mode → 403 \"disabled in
// mirror mode\"". This route renames tmux sessions and moves pluto files, so a
// mirror board must never reach it — and the gate runs before anything else,
// which is why the body below names a job that does not exist and the answer
// is still 403 rather than 404.
func TestEdgeRenameRouteIsMirrorGated(t *testing.T) {
	_, mux := edgeRenMux(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	rec := edgePostAPI(t, mux, edgeRenPath, map[string]any{"slug": "nonexistent", "to": "whatever"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); !strings.Contains(got, "disabled in mirror mode") {
		t.Errorf("body = %q, want the shared gate text %q", got, "disabled in mirror mode")
	}
}

// TestEdgeRawSaveRefusesAChangedJobLine closes the dormant path (Q6): a raw
// save whose parsed `job:` line differs from the row's slug is refused with a
// 409 naming /api/job/rename — "never a silent re-key, never a silent ignore".
func TestEdgeRawSaveRefusesAChangedJobLine(t *testing.T) {
	st, mux := edgeRenMux(t)
	j := mustCreateStoreJob(t, st, store.Job{
		Slug: "edge-ren-raw", Repo: "monolith", Body: "\noriginal spec\n",
	})

	changed := "---\njob: edge-ren-raw-renamed\nrepo: monolith\nready: false\nprereqs: []\nsolo: false\n---\n\nedited spec\n"
	rec := edgePostAPI(t, mux, "/api/job/save", map[string]any{
		"slug": "edge-ren-raw", "content": changed, "mtime": j.Rev,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("changed job: line: status %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if got := edgeErrField(t, rec); !strings.Contains(got, "/api/job/rename") {
		t.Errorf("error = %q, want it to name /api/job/rename", got)
	}
	row, err := st.GetJobByID(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Slug != "edge-ren-raw" {
		t.Errorf("slug = %q — the refused save re-keyed the row anyway", row.Slug)
	}
	if row.Rev != j.Rev {
		t.Errorf("rev = %d, want %d — a refused save must write nothing at all", row.Rev, j.Rev)
	}
	if strings.Contains(row.Body, "edited spec") {
		t.Error("the refused save wrote the body")
	}
}

// TestEdgeRawSaveWithAnUnchangedOrEmptyJobLineStillSaves is the other half of
// the same obligation: "an unchanged or empty `job:` line saves exactly as
// before". The empty case matters because ParseJob defaults an absent value to
// the name it was handed — so a client that simply drops the line must not be
// read as asking for a rename, nor refused for asking for nothing.
func TestEdgeRawSaveWithAnUnchangedOrEmptyJobLineStillSaves(t *testing.T) {
	st, mux := edgeRenMux(t)
	j := mustCreateStoreJob(t, st, store.Job{
		Slug: "edge-ren-keep", Repo: "monolith", Body: "\noriginal spec\n",
	})

	rev := j.Rev
	for _, content := range []string{
		"---\njob: edge-ren-keep\nrepo: monolith\nready: false\nprereqs: []\nsolo: false\n---\n\nunchanged line\n",
		"---\njob:\nrepo: monolith\nready: false\nprereqs: []\nsolo: false\n---\n\nempty line\n",
		"---\nrepo: monolith\nready: false\nprereqs: []\nsolo: false\n---\n\nno line at all\n",
	} {
		rec := edgePostAPI(t, mux, "/api/job/save", map[string]any{
			"slug": "edge-ren-keep", "content": content, "mtime": rev,
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("content %q: status %d, want 200 (%s)", content, rec.Code, rec.Body.String())
		}
		row, err := st.GetJobByID(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Slug != "edge-ren-keep" {
			t.Fatalf("content %q re-keyed the row to %q", content, row.Slug)
		}
		if row.Rev <= rev {
			t.Fatalf("content %q did not save: rev %d, want > %d", content, row.Rev, rev)
		}
		rev = row.Rev
	}
}

// TestEdgeJobFieldsFromParsedDropsSlug pins the amendment directly rather than
// through a save: "jobFieldsFromParsed's map no longer contains \"slug\"". It is
// the map every store-mode save writes through, so a lingering key is a silent
// re-key path that the 409 above could be bypassed by (a save whose parsed slug
// matches the row still writes the column, and any later caller reusing this
// map inherits the write).
func TestEdgeJobFieldsFromParsedDropsSlug(t *testing.T) {
	parsed := jobs.ParseJob("edge-ren-fields"+jobs.JobSuffix,
		[]byte("---\njob: edge-ren-fields\nrepo: monolith\nready: false\nprereqs: []\nsolo: false\n---\n\nspec\n"))
	if parsed.Err != "" {
		t.Fatalf("fixture does not parse: %s", parsed.Err)
	}
	fields := jobFieldsFromParsed(parsed)
	if _, ok := fields["slug"]; ok {
		t.Error(`jobFieldsFromParsed still carries "slug" — identity is /api/job/rename's alone`)
	}
	// the keys it must keep are still there (this is a removal, not a
	// rewrite). "ready" left the list at the rebase: schema v20 dropped the
	// column with the queued status, so upstream's own edit removed it from
	// the map — lead's edit, authors gone.
	for _, k := range []string{"repo", "base", "solo", "model", "color", "body", "state", "err"} {
		if _, ok := fields[k]; !ok {
			t.Errorf("jobFieldsFromParsed dropped %q along with slug", k)
		}
	}
}
