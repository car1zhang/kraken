package api

// jobrename_contract_test.go — blind LOGIC tests for the api half of the
// rename feature (50-contract.md, Slice B): POST /api/job/rename's happy
// shape, webJobFor's former-slug fallback, and the "unchanged job: line still
// saves" half of the dormant-path closure.
//
// Authored from the contract alone, before any implementation existed. Every
// package-level identifier declared here is prefixed `contractRename` so an
// edge author sharing this package cannot collide with it.
//
// The route is driven through the REAL mux (api.Register), the same wiring
// web.Serve uses — the contract fixes the route STRING, not the handler
// function's name.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

func contractRenameMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

// contractRenameSeed inserts one active job row into the wired store and
// returns it.
func contractRenameSeed(t *testing.T, st *store.Store, slug string) *store.Job {
	t.Helper()
	j := &store.Job{Slug: slug, Repo: "monolith"}
	if _, err := st.CreateJob(j); err != nil {
		t.Fatalf("seed job %s: %v", slug, err)
	}
	return j
}

func contractRenamePost(t *testing.T, mux http.Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/job/rename", strings.NewReader(string(mustJSON(t, body))))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func contractRenameDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

// TestContractRenameRouteHappyShape: the response is {"ok": true, "slug":
// "<new slug>"} with 200, the slug is the SLUGIFIED target (clients re-point
// their jobKey off this value), and "skipped" is omitted when nothing was
// skipped.
func TestContractRenameRouteHappyShape(t *testing.T) {
	st := useTestStore(t)
	contractRenameSeed(t, st, "alpha")
	mux := contractRenameMux(t)

	rec := contractRenamePost(t, mux, map[string]any{"slug": "alpha", "to": "Omega Two"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	body := contractRenameDecode(t, rec)
	if ok, _ := body["ok"].(bool); !ok {
		t.Errorf(`response %v is missing "ok": true`, body)
	}
	if got, _ := body["slug"].(string); got != "omega-two" {
		t.Errorf(`response slug = %v, want "omega-two"`, body["slug"])
	}
	if _, present := body["skipped"]; present {
		t.Errorf(`response carries "skipped" with nothing skipped: %v`, body)
	}

	if jobs.LoadJobBySlug("omega-two") == nil {
		t.Error("the job does not resolve under its new slug after a 200")
	}
	if _, err := st.GetJobBySlug("omega-two"); err != nil {
		t.Errorf("store row was not renamed: %v", err)
	}
}

// TestContractRenameRouteRefusalIs409 pins the refusal mapping: a
// jobs.RenameJob refusal comes back as 409 with the refusal text under
// "error", not a 500 and not a silent 200.
func TestContractRenameRouteRefusalIs409(t *testing.T) {
	st := useTestStore(t)
	contractRenameSeed(t, st, "alpha")
	contractRenameSeed(t, st, "omega")
	mux := contractRenameMux(t)

	rec := contractRenamePost(t, mux, map[string]any{"slug": "alpha", "to": "omega"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409; body %s", rec.Code, rec.Body.String())
	}
	if msg, _ := contractRenameDecode(t, rec)["error"].(string); msg == "" {
		t.Error("409 carries no error text — the client renders the refusal inline")
	}
	if _, err := st.GetJobBySlug("alpha"); err != nil {
		t.Errorf("the refused rename moved the row anyway: %v", err)
	}
}

// TestContractRenameRouteUnknownSlugIs404: the request slug resolves against
// CURRENT slugs only, and an unknown one is a 404 rather than a 409.
func TestContractRenameRouteUnknownSlugIs404(t *testing.T) {
	useTestStore(t)
	mux := contractRenameMux(t)

	rec := contractRenamePost(t, mux, map[string]any{"slug": "nope", "to": "omega"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", rec.Code, rec.Body.String())
	}
}

// TestContractRenameRouteIsMirrorGated: this route renames tmux sessions and
// moves pluto files, so a mirror board must never reach it — 403 with the
// package's one gate sentence.
func TestContractRenameRouteIsMirrorGated(t *testing.T) {
	st := useTestStore(t)
	contractRenameSeed(t, st, "alpha")
	mux := contractRenameMux(t)

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	rec := contractRenamePost(t, mux, map[string]any{"slug": "alpha", "to": "omega"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "disabled in mirror mode") {
		t.Errorf("403 body = %q, want the shared gate sentence", rec.Body.String())
	}
	if _, err := st.GetJobBySlug("alpha"); err != nil {
		t.Errorf("a mirror-gated request renamed the row anyway: %v", err)
	}
}

// TestContractRenameRouteIsPostOnly: a non-POST is refused with 405.
func TestContractRenameRouteIsPostOnly(t *testing.T) {
	st := useTestStore(t)
	contractRenameSeed(t, st, "alpha")
	mux := contractRenameMux(t)

	req := httptest.NewRequest(http.MethodGet, "/api/job/rename?slug=alpha&to=omega", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status %d, want 405; body %s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetJobBySlug("alpha"); err != nil {
		t.Errorf("a GET renamed the row: %v", err)
	}
}

// The rename slice's own contract claim — everything it adds is additive, no
// key retyped, renamed or removed — used to be pinned here as apiContract ==
// 5. The pin was retired at the rebase that joined this feature with the
// queued-status retirement (#188), which bumped the contract to 6 for its own
// breaking change: an equality here re-breaks on every unrelated bump while
// saying nothing about the rename (the exact fragility
// recall_contract_test.go documents retiring), and TestAPIContractUnchanged
// in contract_test.go is the repo's one deliberate pin of the current value.
// Lead's edit, authors gone.

// TestContractRenameWebJobForFallsBackToFormerSlug: the CLI verb routes
// resolve a renamed job by its OLD name, which is what keeps zeus' frozen
// brief verbs and the baked pr-hook/limit-hook commands working. GET
// /api/job/load is the cheapest read that goes through webJobFor.
func TestContractRenameWebJobForFallsBackToFormerSlug(t *testing.T) {
	st := useTestStore(t)
	contractRenameSeed(t, st, "alpha")
	mux := contractRenameMux(t)

	if rec := contractRenamePost(t, mux, map[string]any{"slug": "alpha", "to": "omega"}); rec.Code != http.StatusOK {
		t.Fatalf("rename: status %d, body %s", rec.Code, rec.Body.String())
	}

	get := func(slug string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/job/load?slug="+slug, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := get("alpha"); code != http.StatusOK {
		t.Errorf("GET by the OLD slug: status %d, want 200 — the former-slug fallback is what keeps baked commands resolving", code)
	}
	if code := get("omega"); code != http.StatusOK {
		t.Errorf("GET by the new slug: status %d, want 200", code)
	}
	if code := get("never-held"); code != http.StatusNotFound {
		t.Errorf("GET by a slug nobody held: status %d, want 404", code)
	}
}

// TestContractRenameJobFieldsFromParsedDropsSlug closes the dormant path at
// its source: the raw save's field map may no longer carry "slug", so a
// parsed job: line can never re-key a row behind the rename route's back.
func TestContractRenameJobFieldsFromParsedDropsSlug(t *testing.T) {
	parsed := jobs.ParseJob("alpha"+jobs.JobSuffix, []byte("---\njob: something-else\nrepo: monolith\nready: true\n---\n\n# Spec\n"))
	fields := jobFieldsFromParsed(parsed)
	if _, present := fields["slug"]; present {
		t.Fatal(`jobFieldsFromParsed still carries "slug" — a raw save can silently re-key a job`)
	}
	if got := fields["repo"]; got != "monolith" {
		t.Errorf("repo = %v, want monolith — only slug is dropped", got)
	}
}

// TestContractRenameRawSaveWithAnUnchangedJobLineStillSaves is the other half
// of the closure: only a CHANGED job: line is refused, so an ordinary raw
// save (unchanged line, or no value at all) keeps working exactly as before.
func TestContractRenameRawSaveWithAnUnchangedJobLineStillSaves(t *testing.T) {
	st := useTestStore(t)
	contractRenameSeed(t, st, "alpha")

	save := func(t *testing.T, jobLine, spec string) {
		t.Helper()
		content := "---\n" + jobLine + "repo: monolith\nready: true\n---\n\n" + spec + "\n"
		req := httptest.NewRequest(http.MethodPost, "/api/job/save",
			strings.NewReader(string(mustJSON(t, map[string]any{"slug": "alpha", "content": content, "mtime": 0}))))
		rec := httptest.NewRecorder()
		webJobSave(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("save with job line %q: status %d, body %s", jobLine, rec.Code, rec.Body.String())
		}
		j := jobs.LoadJobBySlug("alpha")
		if j == nil {
			t.Fatalf("save with job line %q: the job no longer resolves under alpha", jobLine)
		}
		if !strings.Contains(j.Body, spec) {
			t.Errorf("save with job line %q did not persist the body: %q", jobLine, j.Body)
		}
		if j.Slug != "alpha" {
			t.Errorf("save with job line %q re-keyed the job to %q", jobLine, j.Slug)
		}
	}

	save(t, "job: alpha\n", "# Unchanged line")
	save(t, "job:\n", "# Empty line")
	save(t, "", "# No line at all")
}
