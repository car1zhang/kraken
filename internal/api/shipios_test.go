package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
)

// shipios_test.go — POST /api/ship-ios and GET /api/ship-ios/status.
//
// Every test here MUST stub both seams. preflightShipIOS shells out to git
// and xcodebuild, and spawnShipIOS forks a run that archives the iOS app and
// UPLOADS it to carl's real App Store Connect account — a handler test that
// reached either one for real would burn a build number that can never be
// reused. t.Setenv("HOME", …) is the third leg: it keeps
// ~/.kraken/ship-ios.json out of the real home directory.

var okShipPreflight = jobs.ShipPreflightInfo{
	Repo:         "/tmp/kraken",
	Script:       "/tmp/kraken/" + jobs.ShipScriptRel,
	XcodegenPath: "/opt/homebrew/bin/xcodegen",
}

// shipSpawnRecord captures what the stubbed spawnShipIOS was asked to do.
// The ship spawn takes no arguments (see spawnShipIOS' comment on why the
// script's dangerous flags are terminal-only), so "how many times" is the
// whole assertion.
type shipSpawnRecord struct {
	calls int
}

// stubShipIOS sandboxes HOME and swaps both seams for the duration of the
// test, returning the spawn recorder. spawnErr non-nil exercises the
// spawn-failure branch.
func stubShipIOS(t *testing.T, pfErr, spawnErr error) *shipSpawnRecord {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	oldPre, oldSpawn := preflightShipIOS, spawnShipIOS
	t.Cleanup(func() { preflightShipIOS, spawnShipIOS = oldPre, oldSpawn })

	preflightShipIOS = func() (jobs.ShipPreflightInfo, error) { return okShipPreflight, pfErr }
	rec := &shipSpawnRecord{}
	spawnShipIOS = func() (int, error) {
		rec.calls++
		if spawnErr != nil {
			return 0, spawnErr
		}
		return 4343, nil
	}
	return rec
}

func postShipIOS(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/ship-ios", strings.NewReader(body))
	rec := httptest.NewRecorder()
	shipIOSHandler(rec, req)
	return rec
}

func TestShipIOSRejectsNonPost(t *testing.T) {
	spawns := stubShipIOS(t, nil, nil)
	rec := httptest.NewRecorder()
	shipIOSHandler(rec, httptest.NewRequest(http.MethodGet, "/api/ship-ios", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := decodeErr(t, rec); got != "POST only" {
		t.Errorf("error = %q, want %q", got, "POST only")
	}
	if spawns.calls != 0 {
		t.Errorf("spawns = %d, want 0", spawns.calls)
	}
}

func TestShipIOSBadJSON(t *testing.T) {
	spawns := stubShipIOS(t, nil, nil)
	rec := postShipIOS(t, "{not json")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if got := decodeErr(t, rec); got != "bad json" {
		t.Errorf("error = %q, want %q", got, "bad json")
	}
	if spawns.calls != 0 {
		t.Errorf("spawns = %d, want 0 — a malformed body must never reach the spawn", spawns.calls)
	}
}

// TestShipIOSRefusesOnPreflight pins that the refusal the board shows is
// preflight's own sentence, verbatim — the whole point of sharing
// PreflightShipIOS with the CLI verb is that both refuse in the same words.
func TestShipIOSRefusesOnPreflight(t *testing.T) {
	refusals := []string{
		"ASC_KEY_ID is not set — see ios/TESTFLIGHT.md",
		"ASC_ISSUER_ID is not set — see ios/TESTFLIGHT.md",
		"xcodegen not found",
		"/Users/x/Developer/kraken has uncommitted changes — commit or clean it first",
	}
	for _, msg := range refusals {
		t.Run(msg, func(t *testing.T) {
			spawns := stubShipIOS(t, errors.New(msg), nil)
			rec := postShipIOS(t, "{}")
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rec.Code)
			}
			if got := decodeErr(t, rec); got != msg {
				t.Errorf("error = %q, want %q", got, msg)
			}
			if spawns.calls != 0 {
				t.Errorf("spawns = %d, want 0", spawns.calls)
			}
			// a refusal must leave no claim behind for the next request to
			// trip over — the status file is only written past preflight
			if st := jobs.ReadShipStatus(); st.Phase != jobs.ShipIdle {
				t.Errorf("phase = %q after a refusal, want %q", st.Phase, jobs.ShipIdle)
			}
		})
	}
}

// TestShipIOSRefusesWhileInFlight is the mutex in action: a non-terminal
// status — written by the FIRST request before its child even existed — is
// what a second request trips over. It matters more here than it does for
// the updater: two concurrent archives would race over the same temp paths
// and the second upload would be rejected for a duplicate build number.
func TestShipIOSRefusesWhileInFlight(t *testing.T) {
	spawns := stubShipIOS(t, nil, nil)
	started := time.Now().UnixMilli()
	if err := jobs.WriteShipStatus(jobs.ShipStatus{Phase: jobs.ShipArchiving, StartedAt: started, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	rec := postShipIOS(t, "{}")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body %s", rec.Code, rec.Body.String())
	}
	want := "ship already in flight (started " + time.UnixMilli(started).Format("15:04:05") + ")"
	if got := decodeErr(t, rec); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	if spawns.calls != 0 {
		t.Errorf("spawns = %d, want 0 — single-flight means the second click spawns nothing", spawns.calls)
	}
}

// TestShipIOSTerminalPhasesDoNotBlock is the converse: a finished run (in
// either of its terminal shapes) must never wedge the button.
func TestShipIOSTerminalPhasesDoNotBlock(t *testing.T) {
	for _, phase := range []string{jobs.ShipIdle, jobs.ShipDone, jobs.ShipFailed} {
		t.Run(phase, func(t *testing.T) {
			spawns := stubShipIOS(t, nil, nil)
			if err := jobs.WriteShipStatus(jobs.ShipStatus{Phase: phase, StartedAt: time.Now().UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			rec := postShipIOS(t, "{}")
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202, body %s", rec.Code, rec.Body.String())
			}
			if spawns.calls != 1 {
				t.Errorf("spawns = %d, want 1", spawns.calls)
			}
		})
	}
}

func TestShipIOSHappyPath(t *testing.T) {
	spawns := stubShipIOS(t, nil, nil)
	rec := postShipIOS(t, "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "POST /api/ship-ios", rec.Body.Bytes(), shipIOSStartRespSpec)
	var resp shipIOSResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Phase != jobs.ShipPreflight {
		t.Errorf("resp = %+v, want {ok:true phase:preflight}", resp)
	}
	if spawns.calls != 1 {
		t.Fatalf("spawns = %d, want exactly 1", spawns.calls)
	}
	// the claim itself: written BEFORE the spawn, with no pid, since the
	// child that will claim it doesn't exist yet
	st := jobs.ReadShipStatus()
	if st.Phase != jobs.ShipPreflight {
		t.Errorf("phase = %q, want %q", st.Phase, jobs.ShipPreflight)
	}
	if st.PID != 0 {
		t.Errorf("pid = %d, want 0 — the handler never owns a pid", st.PID)
	}
	if st.StartedAt == 0 {
		t.Error("started_at = 0, want the claim's timestamp")
	}
	if st.Build != 0 {
		t.Errorf("build = %d, want 0 — only the script knows the build number", st.Build)
	}
}

func TestShipIOSEmptyBodyAccepted(t *testing.T) {
	spawns := stubShipIOS(t, nil, nil)
	rec := postShipIOS(t, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (an empty body is the whole request), body %s", rec.Code, rec.Body.String())
	}
	if spawns.calls != 1 {
		t.Errorf("spawns = %d, want 1", spawns.calls)
	}
}

// TestShipIOSSpawnFailureResetsStatus covers the one path where the handler
// has to undo its own mutex: the claim is written, the fork fails, and
// nothing is ever coming to advance the record. Leaving it at "preflight"
// would show a spinner until the claim grace times out and block the next
// click for just as long.
func TestShipIOSSpawnFailureResetsStatus(t *testing.T) {
	spawns := stubShipIOS(t, nil, errors.New("fork/exec: permission denied"))
	rec := postShipIOS(t, "{}")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec); got != "fork/exec: permission denied" {
		t.Errorf("error = %q, want the spawn error verbatim", got)
	}
	if spawns.calls != 1 {
		t.Errorf("spawns = %d, want 1", spawns.calls)
	}
	st := jobs.ReadShipStatus()
	if st.Phase != jobs.ShipFailed {
		t.Fatalf("phase = %q, want %q — a failed spawn must never stay pinned at preflight", st.Phase, jobs.ShipFailed)
	}
	if st.Error != "fork/exec: permission denied" {
		t.Errorf("status error = %q, want the spawn error", st.Error)
	}
	// and the next click must go through immediately, not wait out the grace
	if !jobs.ShipTerminal(st.Phase) {
		t.Error("failed phase must be terminal so the button unblocks")
	}
}

// --- GET /api/ship-ios/status ---

func TestShipIOSStatusWireShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// no run has ever happened: the absent status file must read as idle,
	// with every key still present and correctly typed
	rec := httptest.NewRecorder()
	shipIOSStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/api/ship-ios/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/ship-ios/status (never run)", rec.Body.Bytes(), webShipIOSSpec)
	var idle webShipIOS
	if err := json.Unmarshal(rec.Body.Bytes(), &idle); err != nil {
		t.Fatal(err)
	}
	if idle.Phase != jobs.ShipIdle {
		t.Errorf("phase = %q, want %q for a missing status file", idle.Phase, jobs.ShipIdle)
	}
	if idle.Error != "" {
		t.Errorf("error = %q, want empty — a missing file is not a failure", idle.Error)
	}

	// a run in progress: the same shape, carrying the record's fields. The
	// test process' own pid stands in for a live ship — anything else and
	// ReadShipStatus' staleness downgrade would (correctly) call the run
	// dead and rewrite the phase to failed.
	if err := jobs.WriteShipStatus(jobs.ShipStatus{
		Phase: jobs.ShipUploading, StartedAt: time.Now().UnixMilli(), PID: os.Getpid(), Build: 137,
	}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	shipIOSStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/api/ship-ios/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/ship-ios/status (in flight)", rec.Body.Bytes(), webShipIOSSpec)
	var live webShipIOS
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if live.Phase != jobs.ShipUploading || live.Build != 137 {
		t.Errorf("got %+v, want the written record's phase/build", live)
	}
}

func TestShipIOSStatusRejectsNonGet(t *testing.T) {
	rec := httptest.NewRecorder()
	shipIOSStatusHandler(rec, httptest.NewRequest(http.MethodPost, "/api/ship-ios/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := decodeErr(t, rec); got != "GET only" {
		t.Errorf("error = %q, want %q", got, "GET only")
	}
}

// --- webBoard's "ship_ios" key ---

// TestWebBoardShipIOSKey checks the board carries the same assembled shape
// the status endpoint does. It builds the key through currentShipIOS rather
// than buildWebBoard, which would drag in tmux/process-table discovery.
func TestWebBoardShipIOSKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	board := webBoard{Jobs: []webJob{}, Contract: apiContract, ShipIOS: currentShipIOS()}
	data, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webBoard (with ship_ios)", data, webBoardSpec)

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	raw, ok := resp["ship_ios"]
	if !ok {
		t.Fatal(`webBoard is missing the "ship_ios" key`)
	}
	assertShape(t, "webBoard.ship_ios (never run)", raw, webShipIOSSpec)

	var got webShipIOS
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Phase != jobs.ShipIdle || got.Error != "" {
		t.Errorf("ship_ios = %+v, want an idle, error-free key with no status file present", got)
	}

	// and it agrees with GET /api/ship-ios/status, byte for byte — one
	// assembly function is the only thing keeping that true
	rec := httptest.NewRecorder()
	shipIOSStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/api/ship-ios/status", nil))
	if !bytes.Equal(bytes.TrimSpace(rec.Body.Bytes()), bytes.TrimSpace(raw)) {
		t.Errorf("status endpoint %s != board key %s", rec.Body.String(), raw)
	}
}

// --- mirror mode ---

// TestShipIOSMirrorGate covers both sides of the gate through the real mux.
// The mirror-off half lives here rather than in mirror_test.go's shared
// sweep because reaching the real handler requires both seams stubbed (see
// the header comment and mirrorGatedGlobalRoutes).
func TestShipIOSMirrorGate(t *testing.T) {
	spawns := stubShipIOS(t, nil, nil)
	mux := http.NewServeMux()
	Register(mux)

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	rec := postJSON(t, mux, "/api/ship-ios", []byte("{}"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mirror on: status = %d, want 403", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
		t.Errorf("mirror on: body = %q", got)
	}
	if spawns.calls != 0 {
		t.Fatalf("mirror on: spawns = %d, want 0 — the gate runs before anything else", spawns.calls)
	}

	SetMirrorMode(false)
	rec = postJSON(t, mux, "/api/ship-ios", []byte("{}"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("mirror off: status = %d, want 202, body %s", rec.Code, rec.Body.String())
	}
	if spawns.calls != 1 {
		t.Errorf("mirror off: spawns = %d, want 1", spawns.calls)
	}

	// the status GET is a read and stays reachable in mirror mode
	SetMirrorMode(true)
	statusRec := httptest.NewRecorder()
	mux.ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/api/ship-ios/status", nil))
	if statusRec.Code != http.StatusOK {
		t.Fatalf("mirror on, status GET: %d, want 200", statusRec.Code)
	}
}
