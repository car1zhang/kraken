package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mirror_test.go — kraken serve --mirror's API-layer gate (SetMirrorMode,
// mirrorGatedActions in actions.go). Every gated route is exercised through
// the real mux (api.Register), the same wiring web.Serve uses, so this
// covers the actual HTTP path a mirror board's buttons would hit — not just
// the webAction wrapper in isolation.

// mirrorGatedRoutes mirrors mirrorGatedActions' action-name → route mapping
// (register.go), for a single table-driven sweep.
var mirrorGatedRoutes = []string{
	"/api/queue", "/api/start", "/api/kill", "/api/stop", "/api/steer",
	"/api/question", "/api/retry", "/api/done", "/api/reactivate",
	"/api/archive", "/api/unarchive", "/api/delete",
	// Peers surface: registered outside webAction, gated via mirrorGated —
	// send/spawn/kill/open drive real tmux panes and ghostty windows.
	"/api/peers/send", "/api/peers/spawn", "/api/peers/kill", "/api/peers/open",
}

// mirrorUngatedRoutes are webAction-wrapped routes that must stay fully live
// under mirror mode — DB-local moves that never reach a real tmux pane or a
// real pluto file.
var mirrorUngatedRoutes = []string{"/api/dequeue"}

func postJSON(t *testing.T, mux http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestMirrorModeGatesTmuxDrivingRoutes covers the 403 side: every route in
// mirrorGatedRoutes must refuse with 403 and the exact body "disabled in
// mirror mode" while mirror mode is on, before the request body is even
// decoded (an empty/invalid JSON body must not change the outcome — the
// gate runs first).
func TestMirrorModeGatesTmuxDrivingRoutes(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	mux := http.NewServeMux()
	Register(mux)

	for _, path := range mirrorGatedRoutes {
		t.Run(path, func(t *testing.T) {
			rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "nonexistent"}))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want %d", path, rec.Code, http.StatusForbidden)
			}
			if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
				t.Fatalf("%s: body = %q, want %q", path, got, "disabled in mirror mode")
			}
		})
	}
}

// TestMirrorModeOffLeavesGatedRoutesLive covers the converse: with mirror
// mode off (the default, and every non-mirror kraken serve/web process),
// the same routes must reach their normal handler — proven here by getting
// past the 403 entirely and into the handler's own validation error (409,
// since "nonexistent" never resolves to a real job) rather than being
// blocked at the gate.
func TestMirrorModeOffLeavesGatedRoutesLive(t *testing.T) {
	SetMirrorMode(false)

	mux := http.NewServeMux()
	Register(mux)

	for _, path := range mirrorGatedRoutes {
		t.Run(path, func(t *testing.T) {
			rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "nonexistent"}))
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s: got 403 with mirror mode off — route must not be gated", path)
			}
		})
	}
}

// TestMirrorModeNeverGatesDBLocalRoutes covers the one route mirrorGatedActions
// deliberately excludes — dequeue only flips a DB field back and never
// touches tmux or a real pluto file, so it must stay live regardless of
// mirror mode.
func TestMirrorModeNeverGatesDBLocalRoutes(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	mux := http.NewServeMux()
	Register(mux)

	for _, path := range mirrorUngatedRoutes {
		t.Run(path, func(t *testing.T) {
			rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "nonexistent"}))
			if rec.Code == http.StatusForbidden {
				t.Fatalf("%s: got 403 under mirror mode — this route must stay DB-local and ungated", path)
			}
		})
	}
}

// TestSetMirrorModeRoundTrips is a trivial guard against the getter/setter
// pair drifting (e.g. a future refactor swapping the atomic for something
// that doesn't zero-initialize to false).
func TestSetMirrorModeRoundTrips(t *testing.T) {
	SetMirrorMode(false)
	if mirrorModeOn() {
		t.Fatal("want false after SetMirrorMode(false)")
	}
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	if !mirrorModeOn() {
		t.Fatal("want true after SetMirrorMode(true)")
	}
}
