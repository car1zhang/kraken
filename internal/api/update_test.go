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
	"kraken/internal/util"
)

// update_test.go — POST /api/update and GET /api/update/status.
//
// Every test here MUST stub both seams. preflightUpdate shells out to git
// against the real ~/Developer/kraken, and spawnUpdater forks a process that
// rebuilds and reinstalls this machine's kraken — a handler test that
// reached either one for real would not just be slow, it would update the
// developer's laptop. t.Setenv("HOME", …) is the third leg: it keeps
// ~/.kraken/update.json out of the real home directory.

// spawnRecord captures what the stubbed spawnUpdater was asked to do, so a
// test can assert both "exactly once" and "with these flags".
type spawnRecord struct {
	calls int
	force bool
	test  bool
}

// stubUpdate sandboxes HOME and swaps both seams for the duration of the
// test, returning the spawn recorder. spawnErr non-nil exercises the
// spawn-failure branch.
func stubUpdate(t *testing.T, pf jobs.UpdatePreflightInfo, pfErr, spawnErr error) *spawnRecord {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	oldPre, oldSpawn := preflightUpdate, spawnUpdater
	t.Cleanup(func() { preflightUpdate, spawnUpdater = oldPre, oldSpawn })

	preflightUpdate = func() (jobs.UpdatePreflightInfo, error) { return pf, pfErr }
	rec := &spawnRecord{}
	spawnUpdater = func(force, test bool) (int, error) {
		rec.calls++
		rec.force, rec.test = force, test
		if spawnErr != nil {
			return 0, spawnErr
		}
		return 4242, nil
	}
	return rec
}

// stubUpdateAvail seeds the daemon-written availability row. Through the
// seam, not the store: the row's writer lives inside internal/jobs (the
// daemon is its only caller), so there is nothing exported to write it with.
func stubUpdateAvail(t *testing.T, a jobs.UpdateAvail) {
	t.Helper()
	prev := readUpdateAvail
	readUpdateAvail = func() jobs.UpdateAvail { return a }
	t.Cleanup(func() { readUpdateAvail = prev })
}

func postUpdate(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/update", strings.NewReader(body))
	rec := httptest.NewRecorder()
	updateHandler(rec, req)
	return rec
}

// decodeErr (the {"error": …} reader) is shared from feedback_test.go.

var okPreflight = jobs.UpdatePreflightInfo{
	Repo: "/tmp/kraken", Branch: "main", HeadSHA: "abc1234",
	GoPath: "/usr/local/go/bin/go", MakePath: "/usr/bin/make",
}

func TestUpdateRejectsNonPost(t *testing.T) {
	spawns := stubUpdate(t, okPreflight, nil, nil)
	rec := httptest.NewRecorder()
	updateHandler(rec, httptest.NewRequest(http.MethodGet, "/api/update", nil))
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

func TestUpdateBadJSON(t *testing.T) {
	spawns := stubUpdate(t, okPreflight, nil, nil)
	rec := postUpdate(t, "{not json")
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

// TestUpdateRefusesOnPreflight pins that the refusal the board shows is
// preflight's own sentence, verbatim — the whole point of sharing
// PreflightUpdate with the CLI verb is that both refuse in the same words.
func TestUpdateRefusesOnPreflight(t *testing.T) {
	refusals := []string{
		"/Users/x/Developer/kraken is on carl/foo, not main",
		"/Users/x/Developer/kraken has uncommitted changes — commit or clean it first",
		"go not found — checked /usr/local/go/bin, /opt/homebrew/bin",
		"make not found — checked /usr/local/go/bin, /opt/homebrew/bin",
	}
	for _, msg := range refusals {
		t.Run(msg, func(t *testing.T) {
			spawns := stubUpdate(t, jobs.UpdatePreflightInfo{}, errors.New(msg), nil)
			rec := postUpdate(t, "{}")
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
			if st := jobs.ReadUpdateStatus(); st.Phase != jobs.UpdateIdle {
				t.Errorf("phase = %q after a refusal, want %q", st.Phase, jobs.UpdateIdle)
			}
		})
	}
}

// TestUpdateRefusesWhileInFlight is the mutex in action: a non-terminal
// status — written by the FIRST request before its child even existed — is
// what a second request trips over.
func TestUpdateRefusesWhileInFlight(t *testing.T) {
	spawns := stubUpdate(t, okPreflight, nil, nil)
	started := time.Now().UnixMilli()
	if err := jobs.WriteUpdateStatus(jobs.UpdateStatus{Phase: jobs.UpdateBuilding, StartedAt: started, FromSHA: "abc1234"}); err != nil {
		t.Fatal(err)
	}
	rec := postUpdate(t, "{}")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body %s", rec.Code, rec.Body.String())
	}
	want := "update already in flight (started " + time.UnixMilli(started).Format("15:04:05") + ")"
	if got := decodeErr(t, rec); got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	if spawns.calls != 0 {
		t.Errorf("spawns = %d, want 0 — single-flight means the second click spawns nothing", spawns.calls)
	}
}

// TestUpdateTerminalPhasesDoNotBlock is the converse: a finished run (in any
// of its terminal shapes) must never wedge the button.
func TestUpdateTerminalPhasesDoNotBlock(t *testing.T) {
	for _, phase := range []string{jobs.UpdateIdle, jobs.UpdateDone, jobs.UpdateFailed, jobs.UpdateUpToDate} {
		t.Run(phase, func(t *testing.T) {
			spawns := stubUpdate(t, okPreflight, nil, nil)
			if err := jobs.WriteUpdateStatus(jobs.UpdateStatus{Phase: phase, StartedAt: time.Now().UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			rec := postUpdate(t, "{}")
			if rec.Code != http.StatusAccepted {
				t.Fatalf("status = %d, want 202, body %s", rec.Code, rec.Body.String())
			}
			if spawns.calls != 1 {
				t.Errorf("spawns = %d, want 1", spawns.calls)
			}
		})
	}
}

func TestUpdateHappyPath(t *testing.T) {
	spawns := stubUpdate(t, okPreflight, nil, nil)
	rec := postUpdate(t, "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body %s", rec.Code, rec.Body.String())
	}
	var resp updateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Phase != jobs.UpdatePreflight || resp.FromSHA != "abc1234" {
		t.Errorf("resp = %+v, want {ok:true phase:preflight from_sha:abc1234}", resp)
	}
	if spawns.calls != 1 {
		t.Fatalf("spawns = %d, want exactly 1", spawns.calls)
	}
	if spawns.force || spawns.test {
		t.Errorf("force/test = %v/%v, want false/false for an empty body", spawns.force, spawns.test)
	}
	// the claim itself: written BEFORE the spawn, with no pid, since the
	// child that will claim it doesn't exist yet
	st := jobs.ReadUpdateStatus()
	if st.Phase != jobs.UpdatePreflight {
		t.Errorf("phase = %q, want %q", st.Phase, jobs.UpdatePreflight)
	}
	if st.FromSHA != "abc1234" {
		t.Errorf("from_sha = %q, want abc1234", st.FromSHA)
	}
	if st.PID != 0 {
		t.Errorf("pid = %d, want 0 — the handler never owns a pid", st.PID)
	}
	if st.StartedAt == 0 {
		t.Error("started_at = 0, want the claim's timestamp")
	}
}

func TestUpdateEmptyBodyAccepted(t *testing.T) {
	spawns := stubUpdate(t, okPreflight, nil, nil)
	rec := postUpdate(t, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (an empty body means plain update), body %s", rec.Code, rec.Body.String())
	}
	if spawns.calls != 1 {
		t.Errorf("spawns = %d, want 1", spawns.calls)
	}
}

func TestUpdateForwardsFlags(t *testing.T) {
	spawns := stubUpdate(t, okPreflight, nil, nil)
	rec := postUpdate(t, `{"force":true,"test":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202, body %s", rec.Code, rec.Body.String())
	}
	if !spawns.force || !spawns.test {
		t.Errorf("force/test = %v/%v, want true/true", spawns.force, spawns.test)
	}
}

// TestUpdateSpawnFailureResetsStatus covers the one path where the handler
// has to undo its own mutex: the claim is written, the fork fails, and
// nothing is ever coming to advance the record. Leaving it at "preflight"
// would show a spinner for 60s (staleUpdate's grace) and block the next
// click for just as long.
func TestUpdateSpawnFailureResetsStatus(t *testing.T) {
	spawns := stubUpdate(t, okPreflight, nil, errors.New("fork/exec: permission denied"))
	rec := postUpdate(t, "{}")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec); got != "fork/exec: permission denied" {
		t.Errorf("error = %q, want the spawn error verbatim", got)
	}
	if spawns.calls != 1 {
		t.Errorf("spawns = %d, want 1", spawns.calls)
	}
	st := jobs.ReadUpdateStatus()
	if st.Phase != jobs.UpdateFailed {
		t.Fatalf("phase = %q, want %q — a failed spawn must never stay pinned at preflight", st.Phase, jobs.UpdateFailed)
	}
	if st.Error != "fork/exec: permission denied" {
		t.Errorf("status error = %q, want the spawn error", st.Error)
	}
	// and the next click must go through immediately, not wait out the grace
	if !jobs.UpdateTerminal(st.Phase) {
		t.Error("failed phase must be terminal so the button unblocks")
	}
}

// --- the update log ---

// The log is opened by the PARENT before Start, so truncating it would let
// two spawns landing close together each blow away a log the other child is
// already writing — and with separate open file descriptions and independent
// offsets, what survives is interleaved garbage. It is also the only
// forensic record of why a build failed, so losing it is expensive.
func TestOpenUpdateLogAppendsInsteadOfTruncating(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := openUpdateLog([]string{"update", "--detached"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteString("build output from the first run\n"); err != nil {
		t.Fatal(err)
	}
	second, err := openUpdateLog([]string{"update", "--detached", "--force"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.WriteString("build output from the second run\n"); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second.Close()

	data, err := os.ReadFile(jobs.UpdateLogPath())
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"build output from the first run",
		"build output from the second run",
		"update --detached --force", // the banner names the run's flags
	} {
		if !strings.Contains(body, want) {
			t.Errorf("log %q missing %q", body, want)
		}
	}
	if n := strings.Count(body, "=== kraken "); n != 2 {
		t.Errorf("found %d run banners, want 2 — each run must be findable in the log", n)
	}
	if strings.Contains(body, "\x00") {
		t.Error("log contains NUL padding — a truncate raced a live writer's offset")
	}
}

// --- GET /api/update/status ---

func TestUpdateStatusWireShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// no run has ever happened: the absent status file must read as idle,
	// with every key still present and correctly typed
	rec := httptest.NewRecorder()
	updateStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/update/status (never run)", rec.Body.Bytes(), webUpdateSpec)
	var idle webUpdate
	if err := json.Unmarshal(rec.Body.Bytes(), &idle); err != nil {
		t.Fatal(err)
	}
	if idle.Phase != jobs.UpdateIdle {
		t.Errorf("phase = %q, want %q for a missing status file", idle.Phase, jobs.UpdateIdle)
	}
	if idle.Error != "" {
		t.Errorf("error = %q, want empty — a missing file is not a failure", idle.Error)
	}
	if idle.BuildSHA != util.Build() {
		t.Errorf("build_sha = %q, want %q", idle.BuildSHA, util.Build())
	}

	// a run in progress: the same shape, carrying the record's fields
	// the test process' own pid stands in for a live updater — anything
	// else and ReadUpdateStatus' staleness downgrade would (correctly) call
	// the run dead and rewrite the phase to failed
	if err := jobs.WriteUpdateStatus(jobs.UpdateStatus{
		Phase: jobs.UpdateBuilding, StartedAt: time.Now().UnixMilli(), PID: os.Getpid(),
		FromSHA: "abc1234", ToSHA: "def5678",
	}); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	updateStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/update/status (in flight)", rec.Body.Bytes(), webUpdateSpec)
	var live webUpdate
	if err := json.Unmarshal(rec.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if live.Phase != jobs.UpdateBuilding || live.FromSHA != "abc1234" || live.ToSHA != "def5678" {
		t.Errorf("got %+v, want the written record's phase/shas", live)
	}
}

// A finished run makes the availability count stale — it is only refreshed
// every ten minutes — so the board must not keep advertising "3 behind" on
// the very screen the user just watched update.
func TestCurrentUpdateZeroesBehindAfterAFinishedRun(t *testing.T) {
	checked := time.Now().Add(-5 * time.Minute).UnixMilli()
	cases := []struct {
		name       string
		phase      string
		finishedAt int64
		want       int
	}{
		{"done after the last check", jobs.UpdateDone, checked + 60_000, 0},
		{"uptodate after the last check", jobs.UpdateUpToDate, checked + 60_000, 0},
		// A run that finished BEFORE the check can't overrule it: the check
		// is the fresher fact, and it may well have found new commits since.
		{"done before the last check keeps the count", jobs.UpdateDone, checked - 60_000, 3},
		// An in-flight or failed run proves nothing about currency.
		{"failed run keeps the count", jobs.UpdateFailed, checked + 60_000, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			st := jobs.UpdateStatus{Phase: tc.phase, StartedAt: tc.finishedAt, UpdatedAt: tc.finishedAt}
			if err := jobs.WriteUpdateStatus(st); err != nil {
				t.Fatal(err)
			}
			// WriteUpdateStatus stamps UpdatedAt with now, so the fixture's
			// own timestamp has to be written straight to the file.
			data, err := json.Marshal(st)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(jobs.UpdateStatusPath(), data, 0o644); err != nil {
				t.Fatal(err)
			}
			stubUpdateAvail(t, jobs.UpdateAvail{Behind: 3, CheckedAt: checked})

			if got := currentUpdate().Behind; got != tc.want {
				t.Fatalf("behind = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestUpdateStatusRejectsNonGet(t *testing.T) {
	rec := httptest.NewRecorder()
	updateStatusHandler(rec, httptest.NewRequest(http.MethodPost, "/api/update/status", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := decodeErr(t, rec); got != "GET only" {
		t.Errorf("error = %q, want %q", got, "GET only")
	}
}

// --- webBoard's "update" key ---

// TestWebBoardUpdateKey checks the board carries the same assembled shape
// the status endpoint does. It builds the key through currentUpdate rather
// than buildWebBoard, which would drag in tmux/process-table discovery.
func TestWebBoardUpdateKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	board := webBoard{Jobs: []webJob{}, Contract: apiContract, Update: currentUpdate()}
	data, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	assertShape(t, "webBoard (with update)", data, webBoardSpec)

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatal(err)
	}
	raw, ok := resp["update"]
	if !ok {
		t.Fatal(`webBoard is missing the "update" key`)
	}
	assertShape(t, "webBoard.update (never run)", raw, webUpdateSpec)

	var got webUpdate
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Phase != jobs.UpdateIdle || got.Error != "" {
		t.Errorf("update = %+v, want an idle, error-free key with no status file present", got)
	}

	// and it agrees with GET /api/update/status, byte for byte — one
	// assembly function is the only thing keeping that true
	rec := httptest.NewRecorder()
	updateStatusHandler(rec, httptest.NewRequest(http.MethodGet, "/api/update/status", nil))
	if !bytes.Equal(bytes.TrimSpace(rec.Body.Bytes()), bytes.TrimSpace(raw)) {
		t.Errorf("status endpoint %s != board key %s", rec.Body.String(), raw)
	}
}

// --- mirror mode ---

// TestUpdateMirrorGate covers both sides of the gate through the real mux.
// The mirror-off half lives here rather than in mirror_test.go's shared
// sweep because reaching the real handler requires both seams stubbed (see
// the header comment).
func TestUpdateMirrorGate(t *testing.T) {
	spawns := stubUpdate(t, okPreflight, nil, nil)
	mux := http.NewServeMux()
	Register(mux)

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	rec := postJSON(t, mux, "/api/update", []byte("{}"))
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
	rec = postJSON(t, mux, "/api/update", []byte("{}"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("mirror off: status = %d, want 202, body %s", rec.Code, rec.Body.String())
	}
	if spawns.calls != 1 {
		t.Errorf("mirror off: spawns = %d, want 1", spawns.calls)
	}

	// the status GET is a read and stays reachable in mirror mode
	SetMirrorMode(true)
	req := httptest.NewRequest(http.MethodGet, "/api/update/status", nil)
	statusRec := httptest.NewRecorder()
	mux.ServeHTTP(statusRec, req)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("mirror on, status GET: %d, want 200", statusRec.Code)
	}
}
