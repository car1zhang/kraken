package web

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// useTestStore opens a temp-file store and wires the jobs package onto it
// for the duration of the test, unwiring on cleanup. Mirrors the same-named
// helper in jobs/store_test.go and api/storemode_test.go — duplicated here
// since it's an unexported test-only helper and web is yet another package.
//
// S13: jobs.UseStore(nil) clears the event-log sink itself
// (jobs/store.go), so a separate util.SetEventSink(nil) here would be
// redundant.
func useTestStore(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	jobs.UseStore(st)
	t.Cleanup(func() {
		jobs.UseStore(nil)
		st.Close()
	})
	return st
}

// TestArtifactHandlerStoreMode covers Q12 end-to-end through the real HTTP
// handler: a "pluto"-home artifact URL resolves from the DB once a store is
// wired and the job has no live cell, while "cell"-home URLs never do (a
// live cell always serves off disk, even store-backed).
func TestArtifactHandlerStoreMode(t *testing.T) {
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "store-art", Repo: "monolith"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(id, []store.ArtifactFile{
		{Path: "report.html", Bytes: []byte("<p>from db</p>"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/store-art/pluto/report.html", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "<p>from db</p>" {
		t.Errorf("body = %q, want the DB-stored bytes", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	// "cell" home must never resolve from the DB, even if a row exists at
	// the same path — the cell dir (empty here) is authoritative while live.
	rec2 := httptest.NewRecorder()
	artifactHandler(rec2, httptest.NewRequest("GET", "/api/artifact/store-art/cell/report.html", nil))
	if rec2.Code != 404 {
		t.Fatalf("cell-home status = %d, want 404 (no file on disk, DB must not be consulted)", rec2.Code)
	}

	// an unsynced path still 404s.
	rec3 := httptest.NewRecorder()
	artifactHandler(rec3, httptest.NewRequest("GET", "/api/artifact/store-art/pluto/missing.html", nil))
	if rec3.Code != 404 {
		t.Fatalf("missing path status = %d, want 404", rec3.Code)
	}
}

// TestUploadGETStoreMode covers deliverable 3 through the real mux route.
func TestUploadGETStoreMode(t *testing.T) {
	st := useTestStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: "store-upload", Repo: "monolith"}); err != nil {
		t.Fatal(err)
	}
	name, _, err := jobs.SaveUpload("store-upload", "png", []byte("\x89PNGfakebytes"))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	uploadGetHandler(rec, httptest.NewRequest("GET", "/api/upload/store-upload/"+name, nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "\x89PNGfakebytes" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png (the sniffed mime stored at upload time)", got)
	}
}
