package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// portability_edge_test.go — the EDGE dimension of artifacts/50-contract.md
// (kraken-portability-audit) for cmd/kraken: `kraken artifacts get`'s
// testable core (runArtifactsGet) at every arity and every miss, and
// macAppInstalled's stat boundary. Written blind from the contract text.

// edgeArtServer serves a fixed /api/board body and a table of artifact
// bodies, records every path requested, and points KRAKEN_SERVE_ADDR at it.
type edgeArtServer struct {
	mu    sync.Mutex
	paths []string
}

func edgeServe(t *testing.T, boardStatus int, board string, bodies map[string]edgeBody) *edgeArtServer {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := &edgeArtServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, r.URL.Path)
		s.mu.Unlock()
		if r.URL.Path == "/api/board" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(boardStatus)
			w.Write([]byte(board))
			return
		}
		if b, ok := bodies[r.URL.Path]; ok {
			w.WriteHeader(b.status)
			w.Write(b.data)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	return s
}

type edgeBody struct {
	status int
	data   []byte
}

const edgeBoard = `{"jobs":[
 {"slug":"foo-bar","artifacts":[{"name":"wrong.md","url":"/api/artifact/foo-bar/pluto/wrong.md"}]},
 {"slug":"foo","artifacts":[
   {"name":"b.md","url":"/api/artifact/foo/pluto/b.md"},
   {"name":"a.md","url":"/api/artifact/foo/pluto/a.md"},
   {"name":"nested/diagram.svg","url":"/api/artifact/foo/pluto/nested/diagram.svg"},
   {"name":"gone.md","url":"/api/artifact/foo/pluto/gone.md"}
 ]},
 {"slug":"empty-arr","artifacts":[]},
 {"slug":"no-key"}
]}`

// Contract: "other arities → a usage error".
func TestEdgeRunArtifactsGetArity(t *testing.T) {
	s := edgeServe(t, 200, edgeBoard, nil)
	for _, rest := range [][]string{nil, {}, {"foo", "a.md", "extra"}, {"foo", "a.md", "x", "y"}} {
		out, err := runArtifactsGet(rest)
		if err == nil {
			t.Errorf("runArtifactsGet(%q) = %q, nil; want a usage error", rest, out)
			continue
		}
		if !strings.Contains(strings.ToLower(err.Error()), "usage") {
			t.Errorf("runArtifactsGet(%q) error %q is not a usage error", rest, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.paths) != 0 {
		t.Errorf("a usage error still made requests: %v", s.paths)
	}
}

// Contract: "unknown slug → error naming it" — and a slug that is only a
// SUBSTRING of a served slug is unknown (match is on the slug, not a search).
func TestEdgeRunArtifactsGetUnknownSlug(t *testing.T) {
	edgeServe(t, 200, edgeBoard, nil)
	for _, slug := range []string{"nope", "fo", "oo-bar", "FOO"} {
		_, err := runArtifactsGet([]string{slug})
		if err == nil {
			t.Errorf("slug %q: want an error", slug)
			continue
		}
		if !strings.Contains(err.Error(), slug) {
			t.Errorf("slug %q: error %q does not name it", slug, err)
		}
	}
}

// The listing for `foo` is foo's alone — never foo-bar's, which shares a
// prefix — in served order, one name per line.
func TestEdgeRunArtifactsGetListsExactSlugInServedOrder(t *testing.T) {
	edgeServe(t, 200, edgeBoard, nil)
	out, err := runArtifactsGet([]string{"foo"})
	if err != nil {
		t.Fatalf("runArtifactsGet(foo): %v", err)
	}
	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := []string{"b.md", "a.md", "nested/diagram.svg", "gone.md"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("listing = %q, want %q one per line in served order", out, want)
	}
	if strings.Contains(string(out), "wrong.md") {
		t.Errorf("listing for foo carries foo-bar's artifact")
	}
}

// Contract: "empty output for a job with none" — both wire shapes of "none".
func TestEdgeRunArtifactsGetJobWithNone(t *testing.T) {
	edgeServe(t, 200, edgeBoard, nil)
	for _, slug := range []string{"empty-arr", "no-key"} {
		out, err := runArtifactsGet([]string{slug})
		if err != nil {
			t.Errorf("%s: err = %v, want nil", slug, err)
		}
		if len(out) != 0 {
			t.Errorf("%s: output = %q, want empty", slug, out)
		}
	}
}

// Contract: "no such name → error naming both" — including a name that
// exists on ANOTHER job, and a case variant.
func TestEdgeRunArtifactsGetUnknownName(t *testing.T) {
	edgeServe(t, 200, edgeBoard, nil)
	for _, name := range []string{"missing.md", "wrong.md", "A.md", "diagram.svg"} {
		_, err := runArtifactsGet([]string{"foo", name})
		if err == nil {
			t.Errorf("name %q: want an error", name)
			continue
		}
		if !strings.Contains(err.Error(), "foo") || !strings.Contains(err.Error(), name) {
			t.Errorf("name %q: error %q does not name both the slug and the name", name, err)
		}
	}
	if _, err := runArtifactsGet([]string{"nope", "a.md"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("unknown slug with a name: err = %v, want one naming the slug", err)
	}
}

// Contract: "GET its url resolved against the base, return the body bytes" —
// a nested name, byte-exact for binary content.
func TestEdgeRunArtifactsGetBytesAreExact(t *testing.T) {
	bin := []byte{0x00, 0xFF, '\n', '\r', 0x10, 0x00}
	s := edgeServe(t, 200, edgeBoard, map[string]edgeBody{
		"/api/artifact/foo/pluto/nested/diagram.svg": {200, bin},
	})
	out, err := runArtifactsGet([]string{"foo", "nested/diagram.svg"})
	if err != nil {
		t.Fatalf("runArtifactsGet: %v", err)
	}
	if !bytes.Equal(out, bin) {
		t.Fatalf("bytes = %v, want %v exactly", out, bin)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paths[len(s.paths)-1] != "/api/artifact/foo/pluto/nested/diagram.svg" {
		t.Errorf("last request = %q, want the artifact url", s.paths[len(s.paths)-1])
	}
}

// A listed artifact whose url then answers 404 is not "these are the bytes":
// the body of an error page must not be returned as the artifact.
func TestEdgeRunArtifactsGetArtifactFetchFailureIsAnError(t *testing.T) {
	edgeServe(t, 200, edgeBoard, map[string]edgeBody{
		"/api/artifact/foo/pluto/gone.md": {404, []byte("404 page not found")},
	})
	out, err := runArtifactsGet([]string{"foo", "gone.md"})
	if err == nil {
		t.Fatalf("a 404 artifact fetch returned %q, nil; want an error", out)
	}
}

// The board itself failing (a 500, garbage JSON, nothing listening) is an
// error at both arities, never an empty listing.
func TestEdgeRunArtifactsGetBoardFailures(t *testing.T) {
	t.Run("500", func(t *testing.T) {
		edgeServe(t, 500, `{"error":"boom"}`, nil)
		if out, err := runArtifactsGet([]string{"foo"}); err == nil {
			t.Errorf("board 500: got %q, nil", out)
		}
	})
	t.Run("garbage", func(t *testing.T) {
		edgeServe(t, 200, `not json`, nil)
		if out, err := runArtifactsGet([]string{"foo"}); err == nil {
			t.Errorf("garbage board: got %q, nil", out)
		}
	})
	t.Run("down", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		srv := httptest.NewServer(http.NotFoundHandler())
		addr := srv.Listener.Addr().String()
		srv.Close()
		t.Setenv("KRAKEN_SERVE_ADDR", addr)
		if out, err := runArtifactsGet([]string{"foo"}); err == nil {
			t.Errorf("serve down: got %q, nil", out)
		}
		if out, err := runArtifactsGet([]string{"foo", "a.md"}); err == nil {
			t.Errorf("serve down, with a name: got %q, nil", out)
		}
	})
}

// Contract: `const macAppPath = "/Applications/Kraken.app"`;
// "macAppInstalled is true iff path stats as a directory".
func TestEdgeMacAppInstalledStatBoundary(t *testing.T) {
	if macAppPath != "/Applications/Kraken.app" {
		t.Errorf("macAppPath = %q", macAppPath)
	}
	dir := t.TempDir()
	app := filepath.Join(dir, "Kraken.app")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "Kraken-file.app")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(dir, "link-to-dir.app")
	if err := os.Symlink(app, linkDir); err != nil {
		t.Fatal(err)
	}
	linkFile := filepath.Join(dir, "link-to-file.app")
	if err := os.Symlink(file, linkFile); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling.app")
	if err := os.Symlink(filepath.Join(dir, "nowhere"), dangling); err != nil {
		t.Fatal(err)
	}
	unreadableParent := filepath.Join(dir, "locked")
	if err := os.MkdirAll(filepath.Join(unreadableParent, "Kraken.app"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want bool
	}{
		{"", false},
		{filepath.Join(dir, "missing.app"), false},
		{file, false},
		{app, true},
		{app + "/", true},
		{linkDir, true},   // stat follows the link
		{linkFile, false}, // to a file
		{dangling, false},
	}
	for _, c := range cases {
		if got := macAppInstalled(c.path); got != c.want {
			t.Errorf("macAppInstalled(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	if os.Geteuid() != 0 {
		if err := os.Chmod(unreadableParent, 0); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(unreadableParent, 0o755)
		if macAppInstalled(filepath.Join(unreadableParent, "Kraken.app")) {
			t.Errorf("macAppInstalled = true for a path that cannot be stat'd")
		}
	}
}

// ---------------------------------------------------------------------------
// Review-round-1 amendments — `--archived`
// ---------------------------------------------------------------------------

// The archived generation is listed FIRST on purpose: "without it, the active
// job wins when both exist" must hold whatever the board's order.
const edgeBoardArchived = `{"jobs":[
 {"slug":"foo","archived":true,"display_slug":"foo","artifacts":[{"name":"old.md","url":"/api/artifact/foo/archive/old.md"}]},
 {"slug":"foo","artifacts":[{"name":"new.md","url":"/api/artifact/foo/pluto/new.md"}]},
 {"slug":"only-archived","archived":true,"artifacts":[{"name":"x.md","url":"/api/artifact/only-archived/archive/x.md"}]},
 {"slug":"only-active","artifacts":[{"name":"y.md","url":"/api/artifact/only-active/pluto/y.md"}]}
]}`

func TestEdgeRunArtifactsGetActiveWinsWithoutFlag(t *testing.T) {
	edgeServe(t, 200, edgeBoardArchived, nil)
	out, err := runArtifactsGet([]string{"foo"})
	if err != nil {
		t.Fatalf("runArtifactsGet(foo): %v", err)
	}
	if strings.TrimRight(string(out), "\n") != "new.md" {
		t.Fatalf("listing = %q, want the ACTIVE job's new.md", out)
	}
}

// Amended: "with it, the entry chosen is the archived job whose
// archived_name/slug matches".
func TestEdgeRunArtifactsGetArchivedFlagPicksTheArchivedJob(t *testing.T) {
	s := edgeServe(t, 200, edgeBoardArchived, map[string]edgeBody{
		"/api/artifact/foo/archive/old.md": {200, []byte("archived bytes")},
	})
	out, err := runArtifactsGet([]string{"--archived", "foo"})
	if err != nil {
		t.Fatalf("--archived foo: %v", err)
	}
	if strings.TrimRight(string(out), "\n") != "old.md" {
		t.Fatalf("--archived listing = %q, want old.md", out)
	}
	b, err := runArtifactsGet([]string{"--archived", "foo", "old.md"})
	if err != nil || string(b) != "archived bytes" {
		t.Fatalf("--archived foo old.md = (%q, %v), want the archived bytes", b, err)
	}
	if _, err := runArtifactsGet([]string{"--archived", "foo", "new.md"}); err == nil {
		t.Errorf("--archived foo new.md returned the ACTIVE job's artifact")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.paths {
		if p == "/api/artifact/foo/pluto/new.md" {
			t.Errorf("--archived fetched the active job's artifact")
		}
	}
}

// --archived with only an ACTIVE match is a miss naming the slug; an
// archived-only job is still reachable with the flag.
func TestEdgeRunArtifactsGetArchivedFlagMisses(t *testing.T) {
	edgeServe(t, 200, edgeBoardArchived, nil)
	if _, err := runArtifactsGet([]string{"--archived", "only-active"}); err == nil || !strings.Contains(err.Error(), "only-active") {
		t.Errorf("--archived only-active: err = %v, want a miss naming the slug", err)
	}
	if out, err := runArtifactsGet([]string{"--archived", "only-archived"}); err != nil || strings.TrimRight(string(out), "\n") != "x.md" {
		t.Errorf("--archived only-archived = (%q, %v), want x.md", out, err)
	}
}

// The flag is LEADING only, and it does not count toward the arity:
// `--archived` alone, or with three more args, is a usage error; a
// non-leading `--archived` is an artifact name, not a flag.
func TestEdgeRunArtifactsGetArchivedFlagArity(t *testing.T) {
	s := edgeServe(t, 200, edgeBoardArchived, nil)
	for _, rest := range [][]string{{"--archived"}, {"--archived", "foo", "a", "b"}} {
		_, err := runArtifactsGet(rest)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "usage") {
			t.Errorf("runArtifactsGet(%q): err = %v, want a usage error", rest, err)
		}
	}
	s.mu.Lock()
	n := len(s.paths)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf("usage errors still made %d request(s)", n)
	}
	_, err := runArtifactsGet([]string{"foo", "--archived"})
	if err == nil || !strings.Contains(err.Error(), "--archived") {
		t.Errorf("non-leading --archived: err = %v, want a no-such-artifact error naming it", err)
	}
}

// Amended: "`kraken artifacts get --archived <slug> [name]` is named in
// usage()" — checked in main.go's help text.
func TestEdgeUsageNamesArchivedArtifactsGet(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "artifacts get --archived <slug> [name]") && !strings.Contains(string(src), "artifacts get [--archived] <slug> [name]") {
		t.Errorf("main.go's usage() does not name `artifacts get --archived <slug> [name]`")
	}
}
