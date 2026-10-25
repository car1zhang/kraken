package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFetchServeUsageReadsTheRunningServe pins why `k account` asks serve rather
// than reading the snapshot itself: this process wires no store, so the
// local read falls back to ~/.kraken/usage.json, which serve never writes
// once the snapshot lives in kraken.db. GET /api/usage is the one answer
// that is always current while serve is up.
func TestFetchServeUsageReadsTheRunningServe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/usage" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"available":true,"fetched_at":1700000000000,"accounts":[{"slug":"main","email":"someone@example.com"}],"codex_fetched_at":1700000001000,"codex_accounts":[{"slug":"codex-one"}]}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	u, ok := fetchServeUsage()
	if !ok {
		t.Fatal("fetchServeUsage: ok = false against a live serve")
	}
	if !u.Available || u.FetchedAt != 1700000000000 {
		t.Errorf("fetchServeUsage: available=%v fetched_at=%d, want true/1700000000000", u.Available, u.FetchedAt)
	}
	if len(u.Accounts) != 1 || u.Accounts[0].Slug != "main" || u.Accounts[0].Email != "someone@example.com" {
		t.Errorf("fetchServeUsage: accounts = %+v, want the one main row", u.Accounts)
	}
	if u.CodexFetchedAt != 1700000001000 || len(u.CodexAccounts) != 1 || u.CodexAccounts[0].Slug != "codex-one" {
		t.Errorf("fetchServeUsage: codex half = %d/%+v, want 1700000001000/one codex-one row", u.CodexFetchedAt, u.CodexAccounts)
	}
}

// TestFetchServeUsageRefusesUnusableAnswers — no serve, a non-200, or a body
// that does not decode all read as "ask the local snapshot instead", never
// as a crash or a half-filled answer.
func TestFetchServeUsageRefusesUnusableAnswers(t *testing.T) {
	// a listener that is closed before the call: connection refused
	dead := httptest.NewServer(http.NotFoundHandler())
	addr := dead.Listener.Addr().String()
	dead.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)
	if _, ok := fetchServeUsage(); ok {
		t.Error("fetchServeUsage: ok = true with no serve listening")
	}

	for name, h := range map[string]http.HandlerFunc{
		"non-200": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
		"bad json": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"available":`))
		},
	} {
		srv := httptest.NewServer(h)
		t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
		if _, ok := fetchServeUsage(); ok {
			t.Errorf("%s: fetchServeUsage: ok = true, want false", name)
		}
		srv.Close()
	}
}
