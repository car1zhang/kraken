package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	// /api/queue is the SUBMIT (the URL is kept for shipped iOS builds; the
	// action behind it starts the cell synchronously). It used to be the one
	// ungated exception, safe only while it flipped a DB field and nothing
	// else; it now reaches a real worktree and a real tmux server, so it is
	// gated like everything else here.
	"/api/queue", "/api/kill", "/api/stop", "/api/steer",
	"/api/question", "/api/retry", "/api/done", "/api/reactivate",
	"/api/doc-question",
	// feedback/questions: gated inline in their own handlers (not
	// webAction-wrapped, since GET must stay reachable), same "mirror board
	// is read-only, full stop" policy as every other gated route here.
	// threads: same inline gate as feedback/questions (GET must stay
	// reachable — a thread read is a read), and every POST op but rename
	// opens, kills or types into a real tmux window.
	"/api/feedback", "/api/questions", "/api/threads",
	// watchlist: the same inline gate, on both halves of its ownership split
	// — carl's verdict write and the agent's observation write. Their reads
	// (GET /api/watchlist, GET /api/feedback/sweep, GET /api/feedback/snapshot,
	// GET /api/dream) stay open, like every other read on this surface.
	"/api/watchlist", "/api/watchlist/item",
	// job load: the same inline gate, for the same reason its GET must stay
	// reachable. Its sibling /api/job/color is deliberately NOT here — that
	// route's own gate wraps only the /color it types into a live pane, not
	// its write.
	"/api/job/load",
	// job stack: the same inline gate as load (jobstack.go), for the same
	// reason its GET must stay reachable — `kraken stack get` and the board
	// read it. With mirror off the table's {"slug":"nonexistent"} body 400s
	// on the missing op before any store access.
	"/api/job/stack",
	// goals: the same inline gate on both routes (goals.go). /api/goals gates
	// inline because its GET must stay reachable — the phone's drawer and
	// `kraken goals get` are both reads, and a mirror refusing a read would be
	// a new policy rather than this one. /api/job/goal is POST-only and could
	// have taken a mirrorGated wrapper at registration like /api/recall,
	// but gates inline anyway so a caller reaching the handler directly still
	// hits the refusal. Both are here because a mirror board must never write
	// the live deployment's goals or relabel its jobs.
	"/api/goals", "/api/job/goal",
	// artifact library: the same inline gate on both write routes
	// (library.go). /api/library gates inline because its GET must stay
	// reachable — reading the library is a read, and a mirror refusing one
	// would be a new policy rather than this one. /api/library/remove is
	// POST-only and could have taken a mirrorGated wrapper at registration
	// like /api/recall, but gates inline anyway so a caller reaching the
	// handler directly still hits the refusal. Both ride this shared table
	// safely in BOTH sweeps: with mirror off, against this table's
	// {"slug":"nonexistent"} body, the save 400s on the missing `home` and the
	// remove 400s on the unrecognized `slug` field (its `id` check is never
	// reached), each long before any store write — no library row is created
	// or destroyed by this test.
	"/api/library", "/api/library/remove",
	// job rename: the same inline gate (jobrename.go), and it rides this table
	// rather than needing a bespoke test because the gate sits ahead of the body
	// decode — mirror ON gives the shared 403, mirror OFF falls through to the
	// handler's own 400 for the missing "to" field, neither needing a store nor
	// a real job. It is gated at all because it renames a live tmux session and
	// types a notice into the cell's pane.
	"/api/job/rename",
	// The mental model's seven WRITES (mm.go), gated via mirrorGated at
	// registration rather than inline for the reason recall below is: every
	// one is a POST-ONLY path, so there is no GET on the same path
	// that a route wrapper would take down with them. Its two reads —
	// GET /api/mm and GET /api/mm/snapshot — stay open, like every other read
	// on this surface: a mirror board must be able to SHOW the model it may
	// not write.
	//
	// All seven ride this shared table safely in BOTH sweeps: with mirror off,
	// against this table's {"slug":"nonexistent"} body, each one 400s before it
	// ever reaches the store — decision/artifact/element/detail on a missing
	// required field, and code/note/remove on `slug` itself, which is not a
	// field of theirs and so trips DisallowUnknownFields. No mm row is created
	// or invalidated by this test.
	"/api/mm/decision", "/api/mm/artifact", "/api/mm/element", "/api/mm/code",
	"/api/mm/note", "/api/mm/detail", "/api/mm/remove",
	// Mid-conversation recall (recall.go), gated via mirrorGated at
	// registration — a POST-only route, so there is no GET on the same path
	// that a route wrapper would take down with it. It reads like a query and
	// is gated anyway: a firing records the notes it delivered. Safe in the
	// mirror-off sweep: {"slug":"nonexistent"} resolves no job, so the
	// handler answers 200 {"context":""} and writes nothing.
	"/api/recall",
	// Peers surface: registered outside webAction, gated via mirrorGated —
	// send/spawn/kill/open drive real tmux panes and ghostty windows.
	"/api/peers/send", "/api/peers/spawn", "/api/peers/kill", "/api/peers/open",
}

// mirrorGatedGlobalRoutes are gated exactly like the routes above but must
// NOT ride the shared table: TestMirrorModeOffLeavesGatedRoutesLive sweeps
// that table with mirror mode OFF, and letting POST /api/update reach its
// real handler there would shell out to git against the real checkout and,
// on a clean tree, actually fork a detached updater that rebuilds and
// reinstalls this machine's binary. POST /api/ship-ios is the same hazard
// wearing a different coat — its real handler forks a run that archives the
// iOS app and uploads it to App Store Connect under a build number that can
// never be reused. Their mirror-off liveness is covered in update_test.go
// and shipios_test.go instead, where every seam is stubbed and HOME is a
// temp dir.
var mirrorGatedGlobalRoutes = []string{"/api/update", "/api/ship-ios"}

// mirrorUploadsRoutes mirror mirrorUploadsActions: gated in mirror mode
// unless KRAKEN_UPLOADS_DIR points away from the default uploads dir
// (archiving removes the job's uploads dir).
var mirrorUploadsRoutes = []string{"/api/archive", "/api/unarchive", "/api/delete"}

// retiredRoutes are paths that must no longer be registered at all: both
// existed only to move a job into or out of the queued column, and neither
// has a reachable state left. A mux that still answered them would let a
// stale client take a move the board cannot express.
var retiredRoutes = []string{"/api/dequeue", "/api/start"}

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

	for _, path := range append(append([]string{}, mirrorGatedRoutes...), mirrorGatedGlobalRoutes...) {
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

// TestMirrorModeUploadsRoutesConditional covers the archive/unarchive/delete
// gate's two halves: 403 naming KRAKEN_UPLOADS_DIR while it is unset (or
// names the default dir), and past the gate into the handler's own
// validation once it points at a sandbox.
func TestMirrorModeUploadsRoutesConditional(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	mux := http.NewServeMux()
	Register(mux)

	// past the gate, these handlers reach jobs.LoadJobs/LoadArchivedJobs,
	// which need a wired store even though "nonexistent" never resolves.
	useTestStore(t) // also sets HOME to a temp dir
	for _, val := range []string{"", filepath.Join(os.Getenv("HOME"), ".kraken", "uploads")} {
		t.Setenv("KRAKEN_UPLOADS_DIR", val)
		for _, path := range mirrorUploadsRoutes {
			rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "nonexistent"}))
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "KRAKEN_UPLOADS_DIR") {
				t.Fatalf("KRAKEN_UPLOADS_DIR=%q %s: status %d body %q, want a 403 naming KRAKEN_UPLOADS_DIR", val, path, rec.Code, rec.Body.String())
			}
		}
	}

	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	for _, path := range mirrorUploadsRoutes {
		rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "nonexistent"}))
		if rec.Code == http.StatusForbidden {
			t.Fatalf("sandboxed %s: still 403 — the gate should open with KRAKEN_UPLOADS_DIR set away from the default", path)
		}
	}
}

// TestMirrorModeOffLeavesGatedRoutesLive covers the converse: with mirror
// mode off (the default, and every non-mirror kraken serve/web process),
// the same routes must reach their normal handler — proven here by getting
// past the 403 entirely and into the handler's own 4xx (409 for the
// job-addressing routes, since "nonexistent" never resolves to a real job;
// 400 where the body itself is refused first, as /api/job/rename's missing
// "to" is) rather than being blocked at the gate.
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

// TestRetiredRoutesAreUnregistered replaces the old DB-local-exception test.
// There is no ungated action left: /api/queue is the submit and is gated with
// the rest, while /api/dequeue and /api/start are gone outright. A 404 here
// is the whole assertion — an unregistered path falls through to the mux's
// own NotFound, which is what a stale client must get rather than a silent
// success.
func TestRetiredRoutesAreUnregistered(t *testing.T) {
	SetMirrorMode(false)

	mux := http.NewServeMux()
	Register(mux)

	for _, path := range retiredRoutes {
		t.Run(path, func(t *testing.T) {
			rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "nonexistent"}))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: status = %d, want 404 — the route must not be registered", path, rec.Code)
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
