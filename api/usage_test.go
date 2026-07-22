package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kraken/core"
)

func TestUsageHandlerAvailable(t *testing.T) {
	fixture := core.UsageSnapshot{
		FetchedAt: 1_700_000_000_000,
		Accounts: []core.AccountUsage{
			{Slug: "zzz", Current: false},
			{Slug: "main", Current: true},
			{Slug: "aaa", Current: false},
		},
	}
	oldRead, oldNow := readUsageSnapshot, usageNow
	readUsageSnapshot = func() (core.UsageSnapshot, bool) { return fixture, true }
	usageNow = func() time.Time { return time.UnixMilli(fixture.FetchedAt + 5000) }
	t.Cleanup(func() { readUsageSnapshot, usageNow = oldRead, oldNow })

	w := httptest.NewRecorder()
	usageHandler(w, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got webUsage
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available {
		t.Error("available = false, want true")
	}
	if got.FetchedAt != fixture.FetchedAt {
		t.Errorf("fetched_at = %d, want %d", got.FetchedAt, fixture.FetchedAt)
	}
	if got.AgeSeconds != 5 {
		t.Errorf("age_seconds = %d, want 5", got.AgeSeconds)
	}
	if len(got.Accounts) != 3 {
		t.Fatalf("accounts = %v, want 3 rows", got.Accounts)
	}
	// current-first, then slug ascending
	wantOrder := []string{"main", "aaa", "zzz"}
	for i, slug := range wantOrder {
		if got.Accounts[i].Slug != slug {
			t.Errorf("accounts[%d].Slug = %q, want %q (order = %v)", i, got.Accounts[i].Slug, slug, got.Accounts)
			break
		}
	}
}

func TestUsageHandlerUnavailable(t *testing.T) {
	oldRead := readUsageSnapshot
	readUsageSnapshot = func() (core.UsageSnapshot, bool) { return core.UsageSnapshot{}, false }
	t.Cleanup(func() { readUsageSnapshot = oldRead })

	w := httptest.NewRecorder()
	usageHandler(w, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["available"] != false {
		t.Errorf("available = %v, want false", resp["available"])
	}
	if resp["fetched_at"].(float64) != 0 {
		t.Errorf("fetched_at = %v, want 0", resp["fetched_at"])
	}
	if resp["age_seconds"].(float64) != 0 {
		t.Errorf("age_seconds = %v, want 0", resp["age_seconds"])
	}
	accounts, ok := resp["accounts"].([]any)
	if !ok {
		t.Fatalf("accounts = %#v, not an array", resp["accounts"])
	}
	if len(accounts) != 0 {
		t.Errorf("accounts = %v, want empty", accounts)
	}
	// daemon_ok reads real daemon state — just assert it's present and boolean
	if _, ok := resp["daemon_ok"].(bool); !ok {
		t.Errorf("daemon_ok = %#v, want a bool", resp["daemon_ok"])
	}
}

func TestUsageHandlerMethodNotAllowed(t *testing.T) {
	w := httptest.NewRecorder()
	usageHandler(w, httptest.NewRequest(http.MethodPost, "/api/usage", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405, body = %s", w.Code, w.Body.String())
	}
}
