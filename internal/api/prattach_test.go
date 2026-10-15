package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

func TestPrAttachHandlerHappyPath(t *testing.T) {
	// HOME is sandboxed so the job's `repo: monolith` resolves to no checkout
	// and the route's repo check fails OPEN: this test is about the attach
	// and its dedup, not about the repo gate (prattach_*_test.go cover that),
	// and against the real ~/Developer/monolith a carl/kraken URL is a 409.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "pr-attach-happy", "")

	body := mustJSON(t, prAttachReq{Slug: "pr-attach-happy", URL: "https://github.com/carl/kraken/pull/9"})
	req := httptest.NewRequest(http.MethodPost, "/api/pr-attach", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	prAttachHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp["ok"] || !resp["attached"] {
		t.Fatalf("resp = %+v, want ok+attached", resp)
	}

	j := jobs.JobBySlug(jobs.LoadJobs(), "pr-attach-happy")
	if j == nil || len(j.PRs) != 1 || j.PRs[0].URL != "https://github.com/carl/kraken/pull/9" {
		t.Fatalf("job PRs after attach: %+v", j)
	}

	// re-attaching the same URL is a no-op (attached=false), same dedup
	// contract as appendJobPR itself.
	rec2 := httptest.NewRecorder()
	prAttachHandler(rec2, httptest.NewRequest(http.MethodPost, "/api/pr-attach", bytes.NewReader(body)))
	var resp2 map[string]bool
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if !resp2["ok"] || resp2["attached"] {
		t.Fatalf("dedup resp = %+v, want ok=true attached=false", resp2)
	}
}

func TestPrAttachHandlerStoreMode(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "pr-attach-store", Repo: "monolith"})

	body := mustJSON(t, prAttachReq{Slug: "pr-attach-store", URL: "https://github.com/carl/kraken/pull/3"})
	req := httptest.NewRequest(http.MethodPost, "/api/pr-attach", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	prAttachHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), "pr-attach-store")
	if j == nil || len(j.PRs) != 1 || j.PRs[0].URL != "https://github.com/carl/kraken/pull/3" {
		t.Fatalf("job PRs after store-mode attach: %+v", j)
	}
}

func TestPrAttachHandlerRefusals(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "pr-attach-refuse", "")

	cases := []struct {
		name       string
		method     string
		body       any
		wantStatus int
	}{
		{"GET refused", http.MethodGet, nil, http.StatusMethodNotAllowed},
		{"missing slug", http.MethodPost, prAttachReq{URL: "https://github.com/carl/kraken/pull/1"}, http.StatusBadRequest},
		{"bad url shape", http.MethodPost, prAttachReq{Slug: "pr-attach-refuse", URL: "not-a-url"}, http.StatusBadRequest},
		{"unknown job", http.MethodPost, prAttachReq{Slug: "no-such-job", URL: "https://github.com/carl/kraken/pull/1"}, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r *http.Request
			if c.body != nil {
				r = httptest.NewRequest(c.method, "/api/pr-attach", bytes.NewReader(mustJSON(t, c.body)))
			} else {
				r = httptest.NewRequest(c.method, "/api/pr-attach", nil)
			}
			rec := httptest.NewRecorder()
			prAttachHandler(rec, r)
			if rec.Code != c.wantStatus {
				t.Errorf("status = %d, want %d, body %s", rec.Code, c.wantStatus, rec.Body.String())
			}
		})
	}
}
