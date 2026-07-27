package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// dream_test.go — GET /api/dream. The interesting behaviour is all in what
// the handler DOESN'T own: reports are ordinary artifacts resolved through
// board.go's existing plumbing, so these tests seed artifact rows exactly as
// feedback_test.go does and assert the filter, the ordering, and the
// still-running case.

func seedDreamReport(t *testing.T, st *store.Store, slug, body string) *jobs.Job {
	t.Helper()
	j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "done"})
	if body == "" {
		return j
	}
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: dreamReportName, Bytes: []byte(body), Mtime: 1}}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	return j
}

func getDream(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/dream", nil)
	rec := httptest.NewRecorder()
	dreamHandler(rec, req)
	return rec
}

func decodeDream(t *testing.T, rec *httptest.ResponseRecorder) []webDreamReport {
	t.Helper()
	var resp struct {
		Reports []webDreamReport `json:"reports"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return resp.Reports
}

// TestDreamReportsNewestFirst covers the whole happy path: only dream- jobs
// are picked up, the date comes off the slug, and the order is reverse
// chronological (which a plain reverse string sort gives, since the date is
// zero-padded).
func TestDreamReportsNewestFirst(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	seedDreamReport(t, st, "dream-2026-07-24", "# night one")
	seedDreamReport(t, st, "dream-2026-07-26", "# night three")
	seedDreamReport(t, st, "dream-2026-07-25", "# night two")
	// a non-dream job with an identically-named artifact must not appear
	seedDreamReport(t, st, "some-feature", "# not a dream report")

	rec := getDream(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	got := decodeDream(t, rec)
	if len(got) != 3 {
		t.Fatalf("got %d reports, want 3 (dream- jobs only): %+v", len(got), got)
	}
	wantDates := []string{"2026-07-26", "2026-07-25", "2026-07-24"}
	for i, want := range wantDates {
		if got[i].Date != want {
			t.Fatalf("report %d date = %q, want %q (newest first)", i, got[i].Date, want)
		}
		if got[i].Slug != dreamSlugPrefix+want {
			t.Fatalf("report %d slug = %q", i, got[i].Slug)
		}
	}
	if got[0].Body != "# night three" {
		t.Fatalf("body = %q", got[0].Body)
	}
}

// TestDreamSkipsJobsWithNoReportYet: a dream job whose cell is still running
// has no 00-dream-report.md yet, and an entry with an empty body would render
// as a blank card in the DREAM view rather than as "not finished".
func TestDreamSkipsJobsWithNoReportYet(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	seedDreamReport(t, st, "dream-2026-07-26", "")

	got := decodeDream(t, getDream(t))
	if len(got) != 0 {
		t.Fatalf("got %+v, want no reports for a night still in progress", got)
	}
}

// TestDreamNeverNull: no dreams yet must serve "[]", not "null".
func TestDreamNeverNull(t *testing.T) {
	useTestStore(t)
	rec := getDream(t)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if body := rec.Body.String(); body != "{\"reports\":[]}\n" {
		t.Fatalf("body = %q, want an empty array", body)
	}
}

func TestDreamMethodNotAllowed(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/dream", nil)
	rec := httptest.NewRecorder()
	dreamHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}
