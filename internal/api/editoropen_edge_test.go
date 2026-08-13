package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// editoropen_edge_test.go — the EDGE dimension of slice S of
// artifacts/50-contract.md: every refusal POST /api/editor/open owes, each
// asserting BOTH the status and that the exec seam was never reached. That
// second half is the whole point of the file: this is the one route in the
// binary that launches a GUI application on carl's Mac, so a refusal that
// still reached the seam would be discovered as a window opening on a file
// nobody asked for.
//
// Every request goes through the real mux (api.Register), the wiring
// web.Serve uses — the contract freezes the ROUTE, not a handler symbol, so
// the mux is the only surface it names. Nothing here execs, dials, or
// touches /Applications: the three package `var` seams are swapped for the
// duration of each test and the real implementations are never called.
//
// Written blind against the frozen contract. Package-level identifiers are
// `edgeEO`-prefixed — this package's test namespace is shared with the other
// blind author and with two dozen pre-existing files.

const edgeEOPath = "/api/editor/open"

// edgeEOSeams records what the swapped seams saw, so every refusal case can
// assert the negative ("the editor was never asked to open anything") rather
// than only the status code.
type edgeEOSeams struct {
	openCalls int
	lsCalls   int
	lastAbs   string
	lastLine  int
	lastCol   int

	lsOut   []string
	lsErr   error
	openErr error
}

// edgeEOWire brings up a temp store, the real mux, and the three seams. The
// interface-address seam answers with one fixed non-loopback address so the
// local-origin gate is deterministic on any machine (the real
// net.InterfaceAddrs would make this test read differently on a laptop with
// a tailnet up than on CI).
func edgeEOWire(t *testing.T) (*store.Store, http.Handler, *edgeEOSeams) {
	t.Helper()
	SetMirrorMode(false)
	st := useTestStore(t)
	mux := http.NewServeMux()
	Register(mux)

	seams := &edgeEOSeams{}
	oldOpen, oldLs, oldAddrs := openInEditorFunc, lsTreeFiles, interfaceAddrs
	t.Cleanup(func() {
		openInEditorFunc, lsTreeFiles, interfaceAddrs = oldOpen, oldLs, oldAddrs
	})
	openInEditorFunc = func(abs string, line, col int) error {
		seams.openCalls++
		seams.lastAbs, seams.lastLine, seams.lastCol = abs, line, col
		return seams.openErr
	}
	lsTreeFiles = func(root, base string) ([]string, error) {
		seams.lsCalls++
		return seams.lsOut, seams.lsErr
	}
	interfaceAddrs = func() ([]net.Addr, error) { return edgeEOAddrs("100.64.0.1"), nil }
	return st, mux, seams
}

// edgeEOAddrs builds the *net.IPNet values net.InterfaceAddrs really returns.
func edgeEOAddrs(ips ...string) []net.Addr {
	out := make([]net.Addr, 0, len(ips))
	for _, ip := range ips {
		out = append(out, &net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(32, 32)})
	}
	return out
}

func edgeEOPostFrom(t *testing.T, mux http.Handler, remote, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, edgeEOPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// httptest's default RemoteAddr (192.0.2.1) is deliberately NOT local —
	// every request that means to pass the gate has to say so.
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// edgeEOPost is the ordinary local request: loopback source, JSON body.
func edgeEOPost(t *testing.T, mux http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	return edgeEOPostFrom(t, mux, "127.0.0.1:51000", body)
}

// edgeEOError pulls the `error` field out of a refusal body, so a test can
// assert the wire shape (a JSON object with a non-empty error) and not only
// the status line.
func edgeEOError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	if strings.TrimSpace(resp.Error) == "" {
		t.Fatalf("body %q carries no error message", rec.Body.String())
	}
	return resp.Error
}

// edgeEOReset zeroes the recorder between subtests that share it. Without it
// the counts are CUMULATIVE across a t.Run loop, so a single seam call in one
// row reads as a fresh failure in every row after it — nine rows reporting a
// residue of one is noise the arbitration has to unpick by hand.
func edgeEOReset(seams *edgeEOSeams) {
	seams.openCalls, seams.lsCalls = 0, 0
	seams.lastAbs, seams.lastLine, seams.lastCol = "", 0, 0
}

// edgeEOExecQuiet asserts the one silence the contract states unconditionally
// for a refusal: "Validation failures never reach the exec seam." Nothing is
// claimed about resolution work the contract does not speak to.
func edgeEOExecQuiet(t *testing.T, seams *edgeEOSeams, what string) {
	t.Helper()
	if seams.openCalls != 0 {
		t.Fatalf("%s: the editor seam was called %d times — a refused request must never reach it", what, seams.openCalls)
	}
}

// edgeEOQuiet is the stronger form: the request died before EITHER resolution
// seam ran. It applies to every refusal shape the contract names by name.
func edgeEOQuiet(t *testing.T, seams *edgeEOSeams, what string) {
	t.Helper()
	edgeEOExecQuiet(t, seams, what)
	if seams.lsCalls != 0 {
		t.Fatalf("%s: the ls-tree seam was called %d times — a refused request must never reach it", what, seams.lsCalls)
	}
}

// edgeEORepoRoot makes $HOME/Developer/<name> a git checkout, which is what
// jobs.ResolveRepo resolves a job's repo field against. useTestStore has
// already pointed HOME at a temp dir, so this never touches the real one.
func edgeEORepoRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join(os.Getenv("HOME"), "Developer", name)
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("make repo %s: %v", root, err)
	}
	return root
}

// --- the local-origin gate --------------------------------------------------

// TestEdgeEditorOpenRefusesNonLocalSource covers Q4's answer: a phone tap must
// not open Zed. The body names a slug that does not exist, so the ONLY way
// this can answer 403 rather than the 409 an unknown slug earns is if the gate
// runs before any resolution work — which is the obligation.
func TestEdgeEditorOpenRefusesNonLocalSource(t *testing.T) {
	_, mux, seams := edgeEOWire(t)

	rec := edgeEOPostFrom(t, mux, "203.0.113.9:44444",
		`{"slug":"edge-eo-absent","path":"internal/api/board.go","line":37}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	edgeEOError(t, rec)
	edgeEOQuiet(t, seams, "non-local source")
}

// TestEdgeEditorOpenPassesLoopbackAndSelf is the gate's other edge: the two
// sources the contract says DO pass reach the handler's own resolution and
// fail there instead (409 for the unknown slug), never at the gate.
func TestEdgeEditorOpenPassesLoopbackAndSelf(t *testing.T) {
	_, mux, seams := edgeEOWire(t)

	for _, remote := range []string{
		"127.0.0.1:51000", // IPv4 loopback
		"[::1]:51000",     // IPv6 loopback
		"100.64.0.1:5000", // one of the host's own interface addresses
	} {
		rec := edgeEOPostFrom(t, mux, remote,
			`{"slug":"edge-eo-absent","path":"internal/api/board.go","line":37}`)
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: status = %d, want 409 — this source passes the gate and dies on the unknown slug (body %s)",
				remote, rec.Code, rec.Body.String())
		}
	}
	edgeEOQuiet(t, seams, "unknown slug from a local source")
}

// --- path validation --------------------------------------------------------

// TestEdgeEditorOpenRefusesTraversalShapes sweeps the traversal matrix against
// a slug that DOES resolve, so a 409 here is the path validation answering and
// not the job lookup. Nothing may reach either seam.
func TestEdgeEditorOpenRefusesTraversalShapes(t *testing.T) {
	st, mux, seams := edgeEOWire(t)
	edgeEORepoRoot(t, "edgeeorepo")
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-eo-paths", Repo: "edgeeorepo"})

	for _, c := range []struct {
		what string
		path string
		// execOnly marks a row whose INPUT is an extrapolation of a contract
		// clause rather than a shape the contract names. The contract fixes
		// the 409 and fixes that the exec seam stays silent; it does not say
		// whether such a path is trimmed before the emptiness check, so
		// whether basename resolution runs first is not an obligation and is
		// not asserted. Every other row here is a shape the contract names
		// outright, and keeps the stronger both-seams-silent assertion.
		execOnly bool
	}{
		{what: "empty path", path: ""},
		{what: "whitespace-only path", path: "   ", execOnly: true},
		{what: "absolute path", path: "/etc/passwd"},
		{what: "absolute path that looks repo-relative", path: "/internal/api/board.go"},
		{what: "leading .. segment", path: "../outside.go"},
		{what: "interior .. segment", path: "internal/../../outside.go"},
		{what: "leading . segment", path: "./internal/api/board.go"},
		{what: "interior . segment", path: "internal/./api/board.go"},
		{what: "dot-prefixed first segment", path: ".git/config"},
		{what: "dot-prefixed interior segment", path: "internal/.secret/board.go"},
	} {
		t.Run(c.what, func(t *testing.T) {
			// the recorder is the parent's, so each row starts from zero
			edgeEOReset(seams)

			body, err := json.Marshal(map[string]any{"slug": "edge-eo-paths", "path": c.path})
			if err != nil {
				t.Fatal(err)
			}
			rec := edgeEOPost(t, mux, string(body))
			if rec.Code != http.StatusConflict {
				t.Fatalf("path %q: status = %d, want 409 (body %s)", c.path, rec.Code, rec.Body.String())
			}
			edgeEOError(t, rec)
			if c.execOnly {
				edgeEOExecQuiet(t, seams, "path "+c.what)
				return
			}
			edgeEOQuiet(t, seams, "path "+c.what)
		})
	}
}

// TestEdgeEditorOpenRefusesNegativePositions covers the numeric half of the
// same clause: a negative line or column is a 409, not a coerced zero.
func TestEdgeEditorOpenRefusesNegativePositions(t *testing.T) {
	st, mux, seams := edgeEOWire(t)
	edgeEORepoRoot(t, "edgeeorepo")
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-eo-nums", Repo: "edgeeorepo"})

	for _, body := range []string{
		`{"slug":"edge-eo-nums","path":"internal/api/board.go","line":-1}`,
		`{"slug":"edge-eo-nums","path":"internal/api/board.go","line":37,"col":-1}`,
		`{"slug":"edge-eo-nums","path":"internal/api/board.go","line":-37,"col":-25}`,
	} {
		rec := edgeEOPost(t, mux, body)
		if rec.Code != http.StatusConflict {
			t.Errorf("body %s: status = %d, want 409 (%s)", body, rec.Code, rec.Body.String())
		}
	}
	edgeEOQuiet(t, seams, "negative line/col")
}

// --- job resolution ---------------------------------------------------------

func TestEdgeEditorOpenRefusesUnknownSlug(t *testing.T) {
	_, mux, seams := edgeEOWire(t)

	rec := edgeEOPost(t, mux, `{"slug":"edge-eo-nobody","path":"internal/api/board.go","line":1}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	edgeEOError(t, rec)
	edgeEOQuiet(t, seams, "unknown slug")
}

// TestEdgeEditorOpenArchivedNeverBorrowsALiveJob pins the namespace split:
// with `archived:true` the lookup goes to the archive namespace, so a LIVE job
// of the same name must not satisfy it. The live job exists and is perfectly
// resolvable — a 200 here would mean the archived flag was ignored and the
// editor opened a file in the wrong tree.
func TestEdgeEditorOpenArchivedNeverBorrowsALiveJob(t *testing.T) {
	st, mux, seams := edgeEOWire(t)
	edgeEORepoRoot(t, "edgeeorepo")
	mustCreateStoreJob(t, st, store.Job{Slug: "edge-eo-twin", Repo: "edgeeorepo"})

	rec := edgeEOPost(t, mux, `{"slug":"edge-eo-twin","archived":true,"path":"internal/api/board.go","line":1}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	edgeEOError(t, rec)
	edgeEOQuiet(t, seams, "archived:true against a live slug")
}

// --- basename resolution ----------------------------------------------------

// edgeEOBasenameJob wires a job whose worktree is gone, so root resolution
// falls back to the primary checkout — the cheapest way to reach basename
// resolution without a real cell on disk.
func edgeEOBasenameJob(t *testing.T, st *store.Store, slug string) string {
	t.Helper()
	root := edgeEORepoRoot(t, "edgeeorepo")
	mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "edgeeorepo"})
	return root
}

func TestEdgeEditorOpenBasenameWithNoMatchIsRefused(t *testing.T) {
	st, mux, seams := edgeEOWire(t)
	edgeEOBasenameJob(t, st, "edge-eo-base0")
	seams.lsOut = nil

	rec := edgeEOPost(t, mux, `{"slug":"edge-eo-base0","path":"cell.go","line":553}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	edgeEOError(t, rec)
	if seams.lsCalls != 1 {
		t.Errorf("ls-tree seam called %d times, want 1 — a slash-less path resolves by basename", seams.lsCalls)
	}
	if seams.openCalls != 0 {
		t.Errorf("editor seam called %d times — an unresolvable basename must never open anything", seams.openCalls)
	}
}

// TestEdgeEditorOpenAmbiguousBasenameNamesTheCount is Q2's answer: never a
// wrong-file link. Two matches is a refusal that says HOW MANY, so the reader
// knows to write the full path rather than that "it didn't work".
func TestEdgeEditorOpenAmbiguousBasenameNamesTheCount(t *testing.T) {
	st, mux, seams := edgeEOWire(t)
	edgeEOBasenameJob(t, st, "edge-eo-base2")
	seams.lsOut = []string{"internal/jobs/cell.go", "internal/core/cell.go"}

	rec := edgeEOPost(t, mux, `{"slug":"edge-eo-base2","path":"cell.go","line":553}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if msg := edgeEOError(t, rec); !strings.Contains(msg, "2") {
		t.Errorf("error = %q, want it to name the match count (2)", msg)
	}
	if seams.openCalls != 0 {
		t.Errorf("editor seam called %d times — an ambiguous basename must never open anything", seams.openCalls)
	}
}

// TestEdgeEditorOpenSlashedPathSkipsBasenameResolution pins the other half of
// the same rule: a path with a `/` is taken literally and stat'd under the
// root, so a missing file is a 409 that never consults the tree index.
func TestEdgeEditorOpenSlashedPathSkipsBasenameResolution(t *testing.T) {
	st, mux, seams := edgeEOWire(t)
	edgeEOBasenameJob(t, st, "edge-eo-slash")
	// a match the basename path WOULD have found, to make the skip visible
	seams.lsOut = []string{"internal/api/board.go"}

	rec := edgeEOPost(t, mux, `{"slug":"edge-eo-slash","path":"internal/api/board.go","line":37}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 for a file that is not on disk (body %s)", rec.Code, rec.Body.String())
	}
	edgeEOError(t, rec)
	if seams.lsCalls != 0 {
		t.Errorf("ls-tree seam called %d times — a path containing / must skip basename resolution", seams.lsCalls)
	}
	if seams.openCalls != 0 {
		t.Errorf("editor seam called %d times — a missing file must never open anything", seams.openCalls)
	}
}

// --- the exec seam ----------------------------------------------------------

// TestEdgeEditorOpenSurfacesSeamError is also this file's CONTROL: it is the
// one case where the seam is reached, which is what makes every
// "seam never called" assertion above mean something.
func TestEdgeEditorOpenSurfacesSeamError(t *testing.T) {
	st, mux, seams := edgeEOWire(t)
	root := edgeEOBasenameJob(t, st, "edge-eo-seamerr")
	if err := os.MkdirAll(filepath.Join(root, "internal", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "api", "board.go"), []byte("package api\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seams.openErr = errors.New("zed cli not found at /Applications/Zed.app/Contents/MacOS/cli")

	rec := edgeEOPost(t, mux, `{"slug":"edge-eo-seamerr","path":"internal/api/board.go","line":37}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if msg := edgeEOError(t, rec); !strings.Contains(msg, "zed cli not found") {
		t.Errorf("error = %q, want the seam's own message surfaced", msg)
	}
	if seams.openCalls != 1 {
		t.Fatalf("editor seam called %d times, want exactly 1 — this is the control case", seams.openCalls)
	}
	if want := filepath.Join(root, "internal", "api", "board.go"); seams.lastAbs != want {
		t.Errorf("seam received abs = %q, want %q (filepath.Join(root, rel))", seams.lastAbs, want)
	}
}

// --- transport-level refusals -----------------------------------------------

// TestEdgeEditorOpenRefusesNonPost: the route takes no GET at all — there is
// nothing to read here, only a side effect to trigger — so every other method
// gets the repo's requireJSONPost method rejection.
func TestEdgeEditorOpenRefusesNonPost(t *testing.T) {
	_, mux, seams := edgeEOWire(t)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, edgeEOPath, nil)
		req.RemoteAddr = "127.0.0.1:51000"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status = %d, want 405 (body %s)", method, rec.Code, rec.Body.String())
		}
	}
	edgeEOQuiet(t, seams, "non-POST method")
}

// TestEdgeEditorOpenRefusesOversizeBody covers the 1MB cap. The contract fixes
// the LIMIT but not the status code it answers with, so this asserts the two
// things the limit is actually for: the request does not succeed, and nothing
// downstream of the decode ever ran.
func TestEdgeEditorOpenRefusesOversizeBody(t *testing.T) {
	_, mux, seams := edgeEOWire(t)

	// the padding rides a DECLARED field, so the only thing that can refuse
	// this request is its size
	padding := strings.Repeat("a", (1<<20)+4096)
	body := `{"slug":"` + padding + `","path":"internal/api/board.go","line":1}`
	rec := edgeEOPost(t, mux, body)
	if rec.Code == http.StatusOK {
		t.Fatalf("a %d-byte body was accepted — the 1MB cap did not apply", len(body))
	}
	edgeEOQuiet(t, seams, "oversize body")
}

// TestEdgeEditorOpenSeamsAreTheOnlyBoundary is a standing guard on this
// file's own promise: every test above swaps the three package seams and
// restores them, so the suite never execs Zed, never shells out to git, and
// never dials anything. If a fourth escape hatch appears it belongs here too.
func TestEdgeEditorOpenSeamsAreTheOnlyBoundary(t *testing.T) {
	_, _, seams := edgeEOWire(t)
	if openInEditorFunc == nil || lsTreeFiles == nil || interfaceAddrs == nil {
		t.Fatal("a seam is nil after wiring — the swap did not take")
	}
	// the swap is real: calling through the vars hits the recorders, not the
	// process boundary
	if err := openInEditorFunc("/tmp/edge-eo-nothing.go", 1, 0); err != nil {
		t.Fatalf("swapped seam returned %v", err)
	}
	if seams.openCalls != 1 {
		t.Fatalf("swapped seam recorded %d calls, want 1", seams.openCalls)
	}
	if _, err := jobs.ResolveRepo(""); err == nil {
		t.Error("jobs.ResolveRepo(\"\") should error — a job with no repo has no root")
	}
}
