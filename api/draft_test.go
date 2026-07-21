package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"kraken/jobs"
)

// resetDraftStore swaps in a fresh, empty draft store pinned to now — tests
// share the package-level webDraftStore singleton, so each test needs its
// own isolated instance rather than leaking entries/timestamps between runs.
func resetDraftStore(t *testing.T, now time.Time) {
	t.Helper()
	orig := webDraftStore
	webDraftStore = &draftStore{now: func() time.Time { return now }, entries: map[string]draftEntry{}}
	t.Cleanup(func() { webDraftStore = orig })
}

func draftGetReq(slug string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/draft?slug="+slug, nil)
}

func draftPostReq(t *testing.T, body any) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/api/draft", bytes.NewReader(mustJSON(t, body)))
}

func TestDraftGetUnknownSlugEmpty(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	resetDraftStore(t, time.UnixMilli(1000))
	writeTestJob(t, "draft-unknown", "")

	rec := httptest.NewRecorder()
	draftHandler(rec, draftGetReq("draft-unknown"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp webDraft
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	if resp.Slug != "draft-unknown" || resp.Text != "" || resp.UpdatedAt != 0 {
		t.Errorf("resp = %+v, want empty draft", resp)
	}
}

func TestDraftRoundTrip(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	resetDraftStore(t, time.UnixMilli(5000))
	writeTestJob(t, "draft-roundtrip", "")

	rec := httptest.NewRecorder()
	draftHandler(rec, draftPostReq(t, webReq{Slug: "draft-roundtrip", Text: "hello world"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("post status = %d, body %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	draftHandler(rec, draftGetReq("draft-roundtrip"))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp webDraft
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	if resp.Text != "hello world" || resp.UpdatedAt != 5000 {
		t.Errorf("resp = %+v, want text %q at 5000", resp, "hello world")
	}
}

// TestDraftSecondPostOverwrites pins last-write-wins: a later POST replaces
// both the text and the updated_at stamp, no merge or history kept.
func TestDraftSecondPostOverwrites(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	resetDraftStore(t, time.UnixMilli(1000))
	writeTestJob(t, "draft-overwrite", "")

	rec := httptest.NewRecorder()
	draftHandler(rec, draftPostReq(t, webReq{Slug: "draft-overwrite", Text: "first"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("post 1 status = %d", rec.Code)
	}

	webDraftStore.now = func() time.Time { return time.UnixMilli(2000) }
	rec = httptest.NewRecorder()
	draftHandler(rec, draftPostReq(t, webReq{Slug: "draft-overwrite", Text: "second"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("post 2 status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	draftHandler(rec, draftGetReq("draft-overwrite"))
	var resp webDraft
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	if resp.Text != "second" || resp.UpdatedAt != 2000 {
		t.Errorf("resp = %+v, want \"second\" at 2000 (last write wins)", resp)
	}
}

func TestDraftPostEmptyTextClears(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	resetDraftStore(t, time.UnixMilli(1000))
	writeTestJob(t, "draft-clear", "")

	rec := httptest.NewRecorder()
	draftHandler(rec, draftPostReq(t, webReq{Slug: "draft-clear", Text: "something"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("post status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	draftHandler(rec, draftPostReq(t, webReq{Slug: "draft-clear", Text: ""}))
	if rec.Code != http.StatusOK {
		t.Fatalf("clear post status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	draftHandler(rec, draftGetReq("draft-clear"))
	var resp webDraft
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	if resp.Text != "" || resp.UpdatedAt != 0 {
		t.Errorf("resp = %+v, want cleared", resp)
	}
}

func TestDraftGetMissingSlug(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	resetDraftStore(t, time.UnixMilli(1000))

	rec := httptest.NewRecorder()
	draftHandler(rec, httptest.NewRequest(http.MethodGet, "/api/draft", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDraftPostBadJSON(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	resetDraftStore(t, time.UnixMilli(1000))

	rec := httptest.NewRecorder()
	draftHandler(rec, httptest.NewRequest(http.MethodPost, "/api/draft", bytes.NewReader([]byte("not json"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestDraftGetClearsWhenJobGone covers the lazy-cleanup path: a slug with a
// stored draft but no matching active job (deleted, archived, merged away)
// reads back empty and the stale entry is evicted from the store.
func TestDraftGetClearsWhenJobGone(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	resetDraftStore(t, time.UnixMilli(1000))
	writeTestJob(t, "draft-vanish", "")

	rec := httptest.NewRecorder()
	draftHandler(rec, draftPostReq(t, webReq{Slug: "draft-vanish", Text: "stale"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("post status = %d", rec.Code)
	}
	if e := webDraftStore.get("draft-vanish"); e.Text != "stale" {
		t.Fatalf("draft not stored before job removal: %+v", e)
	}

	j := jobs.JobBySlug(jobs.LoadJobs(), "draft-vanish")
	if j == nil {
		t.Fatal("test job missing before removal")
	}
	if err := os.Remove(j.File); err != nil {
		t.Fatal(err)
	}

	rec = httptest.NewRecorder()
	draftHandler(rec, draftGetReq("draft-vanish"))
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp webDraft
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	if resp.Text != "" {
		t.Errorf("resp = %+v, want empty text after job removal", resp)
	}
	if e := webDraftStore.get("draft-vanish"); e.Text != "" {
		t.Errorf("store still holds entry after lazy clear: %+v", e)
	}
}
