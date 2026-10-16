package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// portability_contract_test.go — athena-logic's BLIND contract tests for the
// portability audit, cmd/kraken half (artifacts/50-contract.md slice E rows
// `cmd/kraken` `main` and the update mac-app phase, slice P row
// `runArtifactsGet`). Written from the contract text alone; main.go,
// cmd_update.go and every other non-test file are only ever read BY THE
// TESTS at run time, never by the author.

func contractPortReadSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// Contract: `main` "calls util.ApplyEnvFile(util.EnvFilePath()) before any
// dispatch". The ordering is integration; the call's presence is pinned here.
func TestContractPortMainAppliesEnvFile(t *testing.T) {
	if src := contractPortReadSource(t, "main.go"); !strings.Contains(src, "util.ApplyEnvFile(util.EnvFilePath())") {
		t.Error("main.go never calls util.ApplyEnvFile(util.EnvFilePath())")
	}
}

// Contract: `const macAppPath = "/Applications/Kraken.app"`;
// "macAppInstalled is true iff path stats as a directory".
func TestContractPortMacAppInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if macAppPath != "/Applications/Kraken.app" {
		t.Errorf("macAppPath = %q, want /Applications/Kraken.app", macAppPath)
	}
	dir := filepath.Join(t.TempDir(), "Kraken.app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !macAppInstalled(dir) {
		t.Errorf("macAppInstalled(existing dir) = false, want true")
	}
	file := filepath.Join(t.TempDir(), "Kraken.app")
	if err := os.WriteFile(file, []byte("not a bundle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if macAppInstalled(file) {
		t.Errorf("macAppInstalled(regular file) = true, want false")
	}
	if macAppInstalled(filepath.Join(t.TempDir(), "absent.app")) {
		t.Errorf("macAppInstalled(missing) = true, want false")
	}
}

// contractPortBoardServer serves a board with two jobs and the artifact bytes
// their URLs point at, and points KRAKEN_SERVE_ADDR at it.
func contractPortBoardServer(t *testing.T) *[]string {
	t.Helper()
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/api/board":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"contract":6,"jobs":[` +
				`{"slug":"other","artifacts":[{"name":"zz.md","url":"/api/artifact/other/pluto/zz.md"}]},` +
				`{"slug":"demo","artifacts":[` +
				`{"name":"10-diagram.html","url":"/api/artifact/demo/pluto/10-diagram.html"},` +
				`{"name":"00-overview.md","url":"/api/artifact/demo/pluto/00-overview.md"}]},` +
				`{"slug":"empty","artifacts":[]},` +
				`{"slug":"demo","archived":true,"display_slug":"demo","artifacts":[` +
				`{"name":"old.md","url":"/api/artifact/demo/archive/old.md"}]}]}`))
		case "/api/artifact/demo/pluto/00-overview.md":
			w.Write([]byte("# Overview\nbytes\x00here\n"))
		case "/api/artifact/demo/archive/old.md":
			w.Write([]byte("archived bytes"))
		case "/api/artifact/demo/pluto/10-diagram.html":
			w.Write([]byte("<svg/>"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	return &hits
}

// Contract: `runArtifactsGet` "len(rest)==1: GET <ServeBaseURL()>/api/board,
// find the job whose slug matches, return one artifact name per line in
// served order (empty output for a job with none)".
func TestContractPortArtifactsGetLists(t *testing.T) {
	hits := contractPortBoardServer(t)
	out, err := runArtifactsGet([]string{"demo"})
	if err != nil {
		t.Fatalf("runArtifactsGet(demo): %v", err)
	}
	if want := "10-diagram.html\n00-overview.md\n"; string(out) != want {
		t.Errorf("listing = %q, want %q (one name per line, served order)", out, want)
	}
	if len(*hits) == 0 || (*hits)[0] != "/api/board" {
		t.Errorf("requests = %v, want GET /api/board", *hits)
	}

	out, err = runArtifactsGet([]string{"empty"})
	if err != nil {
		t.Fatalf("runArtifactsGet(empty): %v", err)
	}
	if len(out) != 0 {
		t.Errorf("listing for a job with no artifacts = %q, want empty", out)
	}
}

// Contract: "len(rest)==2: the entry whose name equals rest[1], GET its url
// resolved against the base, return the body bytes".
func TestContractPortArtifactsGetPrintsBytes(t *testing.T) {
	hits := contractPortBoardServer(t)
	out, err := runArtifactsGet([]string{"demo", "00-overview.md"})
	if err != nil {
		t.Fatalf("runArtifactsGet(demo, 00-overview.md): %v", err)
	}
	if want := "# Overview\nbytes\x00here\n"; string(out) != want {
		t.Errorf("bytes = %q, want %q", out, want)
	}
	found := false
	for _, h := range *hits {
		if h == "/api/artifact/demo/pluto/00-overview.md" {
			found = true
		}
	}
	if !found {
		t.Errorf("requests = %v, want the artifact's url fetched", *hits)
	}
}

// Contract: "unknown slug → error naming it · … no such name → error naming
// both · other arities → a usage error".
func TestContractPortArtifactsGetErrors(t *testing.T) {
	contractPortBoardServer(t)
	if _, err := runArtifactsGet([]string{"no-such-job"}); err == nil || !strings.Contains(err.Error(), "no-such-job") {
		t.Errorf("unknown slug: err = %v, want an error naming no-such-job", err)
	}
	_, err := runArtifactsGet([]string{"demo", "missing.md"})
	if err == nil || !strings.Contains(err.Error(), "demo") || !strings.Contains(err.Error(), "missing.md") {
		t.Errorf("unknown name: err = %v, want an error naming demo and missing.md", err)
	}
	for _, rest := range [][]string{nil, {"a", "b", "c"}} {
		if _, err := runArtifactsGet(rest); err == nil {
			t.Errorf("runArtifactsGet(%q) succeeded, want a usage error", rest)
		}
	}
}

// Contract: "dispatched as kraken artifacts get …, named in usage()".
func TestContractPortArtifactsVerbNamedInUsage(t *testing.T) {
	if src := contractPortReadSource(t, "main.go"); !strings.Contains(src, "artifacts get") {
		t.Error("main.go's usage text never names `artifacts get`")
	}
}

// Contract (review-round-1 amendment): runArtifactsGet "accepts a leading
// --archived flag: with it, the entry chosen is the archived job whose
// archived_name/slug matches; without it, the active job wins when both
// exist; kraken artifacts get --archived <slug> [name] is named in usage()".
func TestContractPortArtifactsGetArchivedFlag(t *testing.T) {
	contractPortBoardServer(t)

	out, err := runArtifactsGet([]string{"demo"})
	if err != nil || string(out) != "10-diagram.html\n00-overview.md\n" {
		t.Errorf("no flag: (%q, %v), want the ACTIVE demo's listing", out, err)
	}
	out, err = runArtifactsGet([]string{"--archived", "demo"})
	if err != nil || string(out) != "old.md\n" {
		t.Errorf("--archived demo: (%q, %v), want \"old.md\\n\"", out, err)
	}
	out, err = runArtifactsGet([]string{"--archived", "demo", "old.md"})
	if err != nil || string(out) != "archived bytes" {
		t.Errorf("--archived demo old.md: (%q, %v), want the archived bytes", out, err)
	}
	if src := contractPortReadSource(t, "main.go"); !strings.Contains(src, "artifacts get --archived") {
		t.Error("main.go's usage text never names `artifacts get --archived`")
	}
}
