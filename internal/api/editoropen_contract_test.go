package api

// editoropen_contract_test.go — blind CONTRACT tests for POST /api/editor/open
// (50-contract.md, Slice S).
//
// Authored from the frozen contract alone, before any implementation existed:
// nothing here was derived from reading editor.go or register.go. Every
// package-level identifier is prefixed `contractEditor` so the edge author
// sharing this package cannot collide with it.
//
// The route is driven through the REAL mux (api.Register), the same wiring
// web.Serve uses — the contract fixes the route STRING, not a handler name.
//
// Nothing in this file execs, opens a socket, or touches /Applications: the
// three seams the contract names (interfaceAddrs, lsTreeFiles,
// openInEditorFunc) are swapped in every test, and the filesystem fixtures live
// under the temp HOME useTestStore installs.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/store"
)

// contractEditorLocal is a loopback source, so the local-origin gate passes on
// its own merit rather than through a swapped interfaceAddrs. httptest's
// default RemoteAddr (192.0.2.1) is TEST-NET, which the gate must refuse — so
// every request here sets this explicitly.
const contractEditorLocal = "127.0.0.1:54321"

// contractEditorRepo is the job's repo field AND the basename of its checkout
// under ~/Developer, so jobs.ResolveRepo's exact-match arm resolves it.
const contractEditorRepo = "demorepo"

// contractEditorSeam records what the exec seam was handed. The abs path is the
// whole point: the contract says it is filepath.Join(root, rel), and which root
// got picked is how worktree/fallback/archived resolution is observed. root
// (added with carl's window-per-working-dir amendment) is that same resolved
// root passed alongside, so Zed can anchor the file to a workspace rooted
// there — the seam records it so each test can pin WHICH root rode along.
type contractEditorSeam struct {
	calls int
	root  string
	abs   string
	line  int
	col   int
	err   error
}

// contractEditorFixture is one seeded job: a primary checkout under
// ~/Developer, optionally a cell worktree, and the mux.
type contractEditorFixture struct {
	home     string
	checkout string // ~/Developer/demorepo — the primary, the Q1 fallback root
	worktree string // ~/.kraken/cells/<slug>-<id>/wt-demorepo, "" when torn down
	mux      http.Handler
	seam     *contractEditorSeam
	lsCalls  int
}

func contractEditorMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func contractEditorWrite(t *testing.T, path string) {
	t.Helper()
	contractEditorMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// contractEditorSwapSeams installs all three seams and restores them on
// cleanup. lsTreeFiles defaults to a failing stub: a test whose path contains a
// slash must never reach basename resolution, and a silent nil would hide that.
func contractEditorSwapSeams(t *testing.T, f *contractEditorFixture) {
	t.Helper()
	f.seam = &contractEditorSeam{}

	oldOpen := openInEditorFunc
	openInEditorFunc = func(root, abs string, line, col int) error {
		f.seam.calls++
		f.seam.root, f.seam.abs, f.seam.line, f.seam.col = root, abs, line, col
		return f.seam.err
	}
	oldLS := lsTreeFiles
	lsTreeFiles = func(root, base string) ([]string, error) {
		f.lsCalls++
		t.Errorf("lsTreeFiles(%q, %q) called for a path that contains a slash", root, base)
		return nil, nil
	}
	oldAddrs := interfaceAddrs
	interfaceAddrs = func() ([]net.Addr, error) { return nil, nil }

	t.Cleanup(func() {
		openInEditorFunc = oldOpen
		lsTreeFiles = oldLS
		interfaceAddrs = oldAddrs
	})
}

// contractEditorSetup seeds one live job and returns the fixture. withWorktree
// false leaves the cell dir absent, which is exactly the torn-down cell the
// Q1 fallback answers for.
func contractEditorSetup(t *testing.T, slug string, withWorktree bool) *contractEditorFixture {
	t.Helper()
	SetMirrorMode(false)
	st := useTestStore(t) // sandboxes HOME and wires the store

	f := &contractEditorFixture{home: os.Getenv("HOME")}
	f.checkout = filepath.Join(f.home, "Developer", contractEditorRepo)
	contractEditorMkdirAll(t, filepath.Join(f.checkout, ".git"))

	id, err := st.CreateJob(&store.Job{Slug: slug, Repo: contractEditorRepo, State: "working"})
	if err != nil {
		t.Fatalf("seed job %s: %v", slug, err)
	}
	if withWorktree {
		f.worktree = contractEditorCellWorktree(f.home, slug, id)
		contractEditorMkdirAll(t, f.worktree)
	}

	mux := http.NewServeMux()
	Register(mux)
	f.mux = mux
	contractEditorSwapSeams(t, f)
	return f
}

// contractEditorCellWorktree is where jobs.ExistingWorktree looks: the cell dir
// is <slug>-<store id>, and the worktree inside it is wt-<repo basename>.
func contractEditorCellWorktree(home, slug string, id int64) string {
	return filepath.Join(home, ".kraken", "cells",
		fmt.Sprintf("%s-%d", slug, id), "wt-"+contractEditorRepo)
}

func contractEditorPost(t *testing.T, f *contractEditorFixture, body any, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/editor/open",
		strings.NewReader(string(mustJSON(t, body))))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func contractEditorDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

// contractEditorErrBody asserts a refusal: the status, a JSON {error} that is
// non-empty, and — the load-bearing half — that the exec seam never ran.
func contractEditorErrBody(t *testing.T, f *contractEditorFixture, rec *httptest.ResponseRecorder, want int) string {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, want, rec.Body.String())
	}
	body := contractEditorDecode(t, rec)
	msg, _ := body["error"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Fatalf(`response %v carries no "error" string`, body)
	}
	if f.seam.calls != 0 {
		t.Fatalf("a refusal reached the exec seam (%d calls, abs %q)", f.seam.calls, f.seam.abs)
	}
	return msg
}

// ---------------------------------------------------------------------------
// The wire shape.
// ---------------------------------------------------------------------------

// TestContractEditorOpenReqKeys pins editorOpenReq's five JSON keys. The board
// and the iOS app address this body by these exact strings; a renamed tag is a
// silently dead click, not a compile error.
func TestContractEditorOpenReqKeys(t *testing.T) {
	raw, err := json.Marshal(editorOpenReq{
		Slug: "j", Archived: true, Path: "internal/api/board.go", Line: 37, Col: 25,
	})
	if err != nil {
		t.Fatalf("marshal editorOpenReq: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	want := map[string]any{
		"slug": "j", "archived": true, "path": "internal/api/board.go",
		"line": float64(37), "col": float64(25),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("editorOpenReq JSON key %q = %v, want %v (whole body %s)", k, got[k], v, raw)
		}
	}
}

// ---------------------------------------------------------------------------
// Happy path and root resolution.
// ---------------------------------------------------------------------------

// TestContractEditorOpenHappyPathUsesTheWorktree: a known slug with a live
// worktree resolves there, the seam is handed filepath.Join(worktree, rel) with
// the line and column intact, and the response is a bare {"ok":true} — no note,
// because nothing degraded.
func TestContractEditorOpenHappyPathUsesTheWorktree(t *testing.T) {
	f := contractEditorSetup(t, "alpha", true)
	contractEditorWrite(t, filepath.Join(f.worktree, "internal", "api", "board.go"))

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "alpha", "path": "internal/api/board.go", "line": 37, "col": 25,
	}, contractEditorLocal)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := contractEditorDecode(t, rec)
	if body["ok"] != true {
		t.Errorf(`response %v is missing "ok": true`, body)
	}
	if note, present := body["note"]; present {
		t.Errorf("a live worktree must carry no note, got %v", note)
	}
	if f.seam.calls != 1 {
		t.Fatalf("exec seam calls = %d, want 1", f.seam.calls)
	}
	if want := filepath.Join(f.worktree, "internal/api/board.go"); f.seam.abs != want {
		t.Errorf("seam abs = %q, want %q", f.seam.abs, want)
	}
	if f.seam.root != f.worktree {
		t.Errorf("seam root = %q, want the worktree %q (the window anchors there)", f.seam.root, f.worktree)
	}
	if f.seam.line != 37 || f.seam.col != 25 {
		t.Errorf("seam line/col = %d/%d, want 37/25", f.seam.line, f.seam.col)
	}
}

// TestContractEditorOpenWorktreeGoneFallsBackWithNote is Q1's answer, whole:
// a torn-down cell still OPENS — off the primary checkout — and the 200 carries
// a note that says so and names the path it fell back to. A 409 here, or a
// silent success, are the two wrong answers.
func TestContractEditorOpenWorktreeGoneFallsBackWithNote(t *testing.T) {
	f := contractEditorSetup(t, "beta", false)
	contractEditorWrite(t, filepath.Join(f.checkout, "internal", "api", "board.go"))

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "beta", "path": "internal/api/board.go", "line": 37,
	}, contractEditorLocal)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := contractEditorDecode(t, rec)
	if body["ok"] != true {
		t.Errorf(`response %v is missing "ok": true`, body)
	}
	note, _ := body["note"].(string)
	if !strings.Contains(note, "worktree gone") {
		t.Errorf("note = %q, want it to contain %q", note, "worktree gone")
	}
	if !strings.Contains(note, f.checkout) {
		t.Errorf("note = %q, want it to name the checkout path %q", note, f.checkout)
	}
	if f.seam.calls != 1 {
		t.Fatalf("exec seam calls = %d, want 1", f.seam.calls)
	}
	if want := filepath.Join(f.checkout, "internal/api/board.go"); f.seam.abs != want {
		t.Errorf("seam abs = %q, want the primary checkout %q", f.seam.abs, want)
	}
	if f.seam.root != f.checkout {
		t.Errorf("seam root = %q, want the primary checkout %q (the fallback window anchors there)", f.seam.root, f.checkout)
	}
}

// ---------------------------------------------------------------------------
// Basename resolution (Q2's answer).
// ---------------------------------------------------------------------------

// contractEditorLsReturns re-swaps the ls seam to a fixed answer and records
// what it was asked for.
func contractEditorLsReturns(t *testing.T, f *contractEditorFixture, out []string, err error) *struct{ root, base string } {
	t.Helper()
	seen := &struct{ root, base string }{}
	old := lsTreeFiles
	lsTreeFiles = func(root, base string) ([]string, error) {
		f.lsCalls++
		seen.root, seen.base = root, base
		return out, err
	}
	t.Cleanup(func() { lsTreeFiles = old })
	return seen
}

// TestContractEditorOpenBasenameUniqueMatch: a path with no "/" is resolved
// against the tracked tree, and exactly one hit becomes the path — the whole
// reason G5 basename refs are clickable at all rather than degrading to plain.
func TestContractEditorOpenBasenameUniqueMatch(t *testing.T) {
	f := contractEditorSetup(t, "gamma", true)
	seen := contractEditorLsReturns(t, f, []string{"internal/jobs/cell.go"}, nil)

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "gamma", "path": "cell.go", "line": 553,
	}, contractEditorLocal)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if f.lsCalls != 1 {
		t.Fatalf("lsTreeFiles calls = %d, want 1", f.lsCalls)
	}
	if seen.root != f.worktree {
		t.Errorf("lsTreeFiles root = %q, want the resolved worktree %q", seen.root, f.worktree)
	}
	if seen.base != "cell.go" {
		t.Errorf("lsTreeFiles base = %q, want %q", seen.base, "cell.go")
	}
	if f.seam.calls != 1 {
		t.Fatalf("exec seam calls = %d, want 1", f.seam.calls)
	}
	if want := filepath.Join(f.worktree, "internal/jobs/cell.go"); f.seam.abs != want {
		t.Errorf("seam abs = %q, want the unique match %q", f.seam.abs, want)
	}
	if f.seam.line != 553 {
		t.Errorf("seam line = %d, want 553", f.seam.line)
	}
}

// TestContractEditorOpenBasenameAmbiguousNamesTheCount: N>1 is a 409 that says
// how many — "never a wrong-file link" is the rule, and a count is what makes
// the refusal actionable rather than mysterious.
func TestContractEditorOpenBasenameAmbiguousNamesTheCount(t *testing.T) {
	f := contractEditorSetup(t, "delta", true)
	contractEditorLsReturns(t, f, []string{
		"internal/api/board.go", "internal/jobs/board.go", "web/board.go",
	}, nil)

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "delta", "path": "board.go", "line": 12,
	}, contractEditorLocal)

	msg := contractEditorErrBody(t, f, rec, http.StatusConflict)
	if !strings.Contains(msg, "3") {
		t.Errorf("error = %q, want it to name the match count 3", msg)
	}
}

// TestContractEditorOpenBasenameNoMatch: zero hits is a 409 too, never a
// speculative join under the root.
func TestContractEditorOpenBasenameNoMatch(t *testing.T) {
	f := contractEditorSetup(t, "epsilon", true)
	contractEditorLsReturns(t, f, nil, nil)

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "epsilon", "path": "nowhere.go", "line": 1,
	}, contractEditorLocal)

	contractEditorErrBody(t, f, rec, http.StatusConflict)
}

// TestContractEditorOpenSlashedPathMustExist: a path WITH a slash skips
// basename resolution entirely and is stat'd under the root — a miss is a 409.
// The lsTreeFiles stub installed by contractEditorSwapSeams fails the test if
// this path reaches it.
func TestContractEditorOpenSlashedPathMustExist(t *testing.T) {
	f := contractEditorSetup(t, "zeta", true)

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "zeta", "path": "internal/api/absent.go", "line": 1,
	}, contractEditorLocal)

	contractEditorErrBody(t, f, rec, http.StatusConflict)
	if f.lsCalls != 0 {
		t.Errorf("a slashed path reached basename resolution (%d ls calls)", f.lsCalls)
	}
}

// ---------------------------------------------------------------------------
// Job resolution.
// ---------------------------------------------------------------------------

// TestContractEditorOpenUnknownSlug: 409, and nothing execs.
func TestContractEditorOpenUnknownSlug(t *testing.T) {
	f := contractEditorSetup(t, "eta", true)

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "no-such-job", "path": "internal/api/board.go", "line": 1,
	}, contractEditorLocal)

	contractEditorErrBody(t, f, rec, http.StatusConflict)
}

// TestContractEditorOpenArchivedResolvesTheArchiveNamespace: with archived:true
// the slug is resolved through jobs.ArchivedJobByName, NEVER through a live job
// that happens to hold the same slug (archiving frees the slug, so both
// generations legitimately exist at once). The two generations have different
// cell ids, hence different worktrees — which root the seam is handed is the
// only thing that can tell them apart.
func TestContractEditorOpenArchivedResolvesTheArchiveNamespace(t *testing.T) {
	SetMirrorMode(false)
	st := useTestStore(t)

	f := &contractEditorFixture{home: os.Getenv("HOME")}
	f.checkout = filepath.Join(f.home, "Developer", contractEditorRepo)
	contractEditorMkdirAll(t, filepath.Join(f.checkout, ".git"))

	liveID, err := st.CreateJob(&store.Job{Slug: "theta", Repo: contractEditorRepo, State: "working"})
	if err != nil {
		t.Fatalf("seed live job: %v", err)
	}
	archivedID, err := st.CreateArchivedJob(&store.Job{
		Slug: "theta", ArchivedName: "theta", ArchivedAt: 1000,
		Repo: contractEditorRepo, State: "done",
	})
	if err != nil {
		t.Fatalf("seed archived job: %v", err)
	}
	if liveID == archivedID {
		t.Fatalf("the two generations share id %d — this test cannot distinguish them", liveID)
	}

	liveWT := contractEditorCellWorktree(f.home, "theta", liveID)
	archivedWT := contractEditorCellWorktree(f.home, "theta", archivedID)
	contractEditorWrite(t, filepath.Join(liveWT, "internal", "api", "board.go"))
	contractEditorWrite(t, filepath.Join(archivedWT, "internal", "api", "board.go"))
	f.worktree = archivedWT

	mux := http.NewServeMux()
	Register(mux)
	f.mux = mux
	contractEditorSwapSeams(t, f)

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "theta", "archived": true,
		"path": "internal/api/board.go", "line": 37,
	}, contractEditorLocal)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if f.seam.calls != 1 {
		t.Fatalf("exec seam calls = %d, want 1", f.seam.calls)
	}
	if want := filepath.Join(archivedWT, "internal/api/board.go"); f.seam.abs != want {
		t.Errorf("seam abs = %q, want the ARCHIVED generation's worktree %q", f.seam.abs, want)
	}
	if f.seam.root != archivedWT {
		t.Errorf("seam root = %q, want the ARCHIVED generation's worktree %q — a live-generation root would anchor this file into the wrong window", f.seam.root, archivedWT)
	}
	if strings.HasPrefix(f.seam.abs, liveWT) {
		t.Errorf("archived:true resolved the LIVE same-slug job (%q)", f.seam.abs)
	}
}

// ---------------------------------------------------------------------------
// The exec seam's two outcomes.
// ---------------------------------------------------------------------------

// TestContractEditorOpenSeamErrorIs409: a seam failure (in production, Zed's
// CLI missing) surfaces as 409 {error} carrying the seam's own text, not a 500
// and not a silent ok.
func TestContractEditorOpenSeamErrorIs409(t *testing.T) {
	f := contractEditorSetup(t, "iota", true)
	contractEditorWrite(t, filepath.Join(f.worktree, "internal", "api", "board.go"))
	f.seam.err = errors.New("zed cli not found at /Applications/Zed.app/Contents/MacOS/cli")

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "iota", "path": "internal/api/board.go", "line": 37,
	}, contractEditorLocal)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	body := contractEditorDecode(t, rec)
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "zed cli not found") {
		t.Errorf("error = %q, want it to carry the seam's own message", msg)
	}
	if body["ok"] == true {
		t.Errorf("a seam failure answered ok:true (%v)", body)
	}
}

// ---------------------------------------------------------------------------
// The local-origin gate (Q4's answer) and the mirror gate.
// ---------------------------------------------------------------------------

// contractEditorAddr is one interface address in the shape net.InterfaceAddrs
// actually returns (an *net.IPNet, i.e. "ip/mask"), not a bare IP string.
func contractEditorAddr(cidr string) net.Addr {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return &net.IPNet{IP: ip, Mask: ipnet.Mask}
}

// TestContractEditorOpenRemoteOriginIs403BeforeAnyResolution: a phone tap must
// not open Zed. The refusal is 403 with a JSON {error}, and — the half that
// matters — it lands before ANY resolution work, so neither seam is touched.
func TestContractEditorOpenRemoteOriginIs403BeforeAnyResolution(t *testing.T) {
	f := contractEditorSetup(t, "kappa", true)
	contractEditorWrite(t, filepath.Join(f.worktree, "internal", "api", "board.go"))
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{contractEditorAddr("100.64.0.1/32")}, nil
	}

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "kappa", "path": "internal/api/board.go", "line": 37,
	}, "203.0.113.9:41234")

	msg := contractEditorErrBody(t, f, rec, http.StatusForbidden)
	if msg == "disabled in mirror mode" {
		t.Fatalf("that is the mirror gate's refusal, not the local-origin gate's")
	}
	if f.lsCalls != 0 {
		t.Errorf("the refused request still did resolution work (%d ls calls)", f.lsCalls)
	}
}

// TestContractEditorOpenLoopbackPasses: loopback passes with no help from
// interfaceAddrs at all (the stub returns nothing).
func TestContractEditorOpenLoopbackPasses(t *testing.T) {
	f := contractEditorSetup(t, "lambda", true)
	contractEditorWrite(t, filepath.Join(f.worktree, "internal", "api", "board.go"))

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "lambda", "path": "internal/api/board.go", "line": 37,
	}, contractEditorLocal)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if f.seam.calls != 1 {
		t.Errorf("exec seam calls = %d, want 1", f.seam.calls)
	}
}

// TestContractEditorOpenOwnInterfaceAddressPasses: the Mac's own tailnet
// self-address is the other passing case — the macOS shell app and a Mac
// browser reaching the board over tailscale are still carl at the keyboard.
func TestContractEditorOpenOwnInterfaceAddressPasses(t *testing.T) {
	f := contractEditorSetup(t, "mu", true)
	contractEditorWrite(t, filepath.Join(f.worktree, "internal", "api", "board.go"))
	interfaceAddrs = func() ([]net.Addr, error) {
		return []net.Addr{contractEditorAddr("100.64.0.7/32")}, nil
	}

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "mu", "path": "internal/api/board.go", "line": 37,
	}, "100.64.0.7:52101")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if f.seam.calls != 1 {
		t.Errorf("exec seam calls = %d, want 1", f.seam.calls)
	}
}

// TestContractEditorOpenMirrorModeIs403: the route rides the existing gate, so
// a mirror board answers with the gate's own refusal — asserted from a LOOPBACK
// source, so only the mirror gate can be what produced the 403.
func TestContractEditorOpenMirrorModeIs403(t *testing.T) {
	f := contractEditorSetup(t, "nu", true)
	contractEditorWrite(t, filepath.Join(f.worktree, "internal", "api", "board.go"))
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	rec := contractEditorPost(t, f, map[string]any{
		"slug": "nu", "path": "internal/api/board.go", "line": 37,
	}, contractEditorLocal)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
		t.Errorf("body = %q, want %q", got, "disabled in mirror mode")
	}
	if f.seam.calls != 0 {
		t.Errorf("a mirror-gated request reached the exec seam (%d calls)", f.seam.calls)
	}
}

// TestContractEditorOpenRejectsNonPOST: the repo's requireJSONPost convention,
// which answers a wrong method with 405.
func TestContractEditorOpenRejectsNonPOST(t *testing.T) {
	f := contractEditorSetup(t, "xi", true)

	req := httptest.NewRequest(http.MethodGet, "/api/editor/open", nil)
	req.RemoteAddr = contractEditorLocal
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405 (body %s)", rec.Code, rec.Body.String())
	}
	if f.seam.calls != 0 {
		t.Errorf("a GET reached the exec seam (%d calls)", f.seam.calls)
	}
}
