package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// cmd_spawn_test.go — the X-Kraken-From-Cell header on `kraken job draft` and
// `kraken cell submit` (qfclient.go's fromCellHeaders): sent with the trimmed
// KRAKEN_JOB_SLUG inside a cell, absent outside one. The fake serve records the
// headers of EVERY request by path, since runCellSubmit's audit POST to
// /api/logevent follows the submit and a last-request recorder would only ever
// show that one.

type spawnServe struct {
	mu      sync.Mutex
	headers map[string]http.Header
}

func (f *spawnServe) start(t *testing.T) {
	t.Helper()
	f.headers = map[string]http.Header{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		f.mu.Lock()
		f.headers[r.URL.Path] = r.Header.Clone()
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"slug":"child","mtime":1}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
}

func (f *spawnServe) header(t *testing.T, path string) http.Header {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	h, ok := f.headers[path]
	if !ok {
		paths := make([]string, 0, len(f.headers))
		for p := range f.headers {
			paths = append(paths, p)
		}
		t.Fatalf("serve never saw %s; saw %v", path, paths)
	}
	return h
}

const spawnDraftText = "---\njob: child\nrepo: kraken\n---\n\nspec\n"

func TestSpawnHeaderOnDraftAndSubmitInsideCell(t *testing.T) {
	f := &spawnServe{}
	f.start(t)
	t.Setenv("KRAKEN_JOB_SLUG", "  parent-cell ")

	if _, err := runJobDraft("child", 0, strings.NewReader(spawnDraftText)); err != nil {
		t.Fatalf("runJobDraft: %v", err)
	}
	if err := runCellSubmit("child"); err != nil {
		t.Fatalf("runCellSubmit: %v", err)
	}
	for _, path := range []string{"/api/job/draft", "/api/queue"} {
		if got := f.header(t, path).Get(fromCellHeader); got != "parent-cell" {
			t.Errorf("%s: %s = %q, want parent-cell (trimmed)", path, fromCellHeader, got)
		}
	}
}

func TestSpawnHeaderAbsentOutsideCell(t *testing.T) {
	f := &spawnServe{}
	f.start(t)
	t.Setenv("KRAKEN_JOB_SLUG", "")
	os.Unsetenv("KRAKEN_JOB_SLUG")

	if _, err := runJobDraft("child", 0, strings.NewReader(spawnDraftText)); err != nil {
		t.Fatalf("runJobDraft: %v", err)
	}
	if err := runCellSubmit("child"); err != nil {
		t.Fatalf("runCellSubmit: %v", err)
	}
	for _, path := range []string{"/api/job/draft", "/api/queue"} {
		if h := f.header(t, path); len(h.Values(fromCellHeader)) != 0 {
			t.Errorf("%s carried %s outside a cell: %q", path, fromCellHeader, h.Get(fromCellHeader))
		}
	}
	// a whitespace-only export reads as absent too
	t.Setenv("KRAKEN_JOB_SLUG", "   ")
	if got := fromCellHeaders(); got != nil {
		t.Errorf("fromCellHeaders with a blank export = %v, want nil", got)
	}
}
