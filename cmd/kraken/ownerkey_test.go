package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"kraken/internal/jobs"
)

// ownerkey_test.go — every way the CLI and the cell hooks call kraken serve
// carries the owner key (jobs.ServeClient), which the gate needs before it
// treats a local caller as the owner.

// TestCLICallersSendTheOwnerKey drives each CLI-side helper against a fake
// serve and checks the header arrived on every path.
func TestCLICallersSendTheOwnerKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	key, err := jobs.EnsureOwnerKey()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = r.Header.Get(jobs.OwnerKeyHeader)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	if _, err := qfGet("/api/q-get"); err != nil {
		t.Errorf("qfGet: %v", err)
	}
	if _, err := qfPost("/api/q-post", map[string]string{}); err != nil {
		t.Errorf("qfPost: %v", err)
	}
	if err := hookPost("/api/hook-post", map[string]string{}); err != nil {
		t.Errorf("hookPost: %v", err)
	}
	if _, err := artifactsFetch(srv.URL + "/api/artifact-fetch"); err != nil {
		t.Errorf("artifactsFetch: %v", err)
	}
	cmdLogEvent([]string{"test", "owner-key", "probe"})

	for _, p := range []string{"/api/q-get", "/api/q-post", "/api/hook-post", "/api/artifact-fetch", "/api/logevent"} {
		if got := seen[p]; got != key {
			t.Errorf("%s: owner key header %q, want the key", p, got)
		}
	}
}

// TestServeCallersUseServeClient is the guard for callers this package's
// tests cannot drive (the recall and limit hooks run inside a claude hook): a
// non-test file that addresses kraken serve (ServeBaseURL) must not build its
// own http.Client, which would go out without the key.
func TestServeCallersUseServeClient(t *testing.T) {
	root := filepath.Join("..", "..")
	var files []string
	for _, dir := range []string{"cmd", "internal"} {
		filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
	}
	checked := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		if !strings.Contains(src, "ServeBaseURL()") || strings.HasSuffix(f, "ownerkey.go") {
			continue
		}
		checked++
		if strings.Contains(src, "http.Client{") {
			t.Errorf("%s calls kraken serve but builds its own http.Client; use jobs.ServeClient so the owner key goes with it", f)
		}
	}
	if checked < 5 {
		t.Errorf("only %d files address ServeBaseURL; the guard lost its targets", checked)
	}
}

// `k board open` opens the owner link (serve's /api/owner with the key) in
// the browser, prints it with --print or when the browser cannot be opened,
// and fails with the file's path when serve has never written a key.
func TestBoardOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_SERVE_ADDR", "127.0.0.1:7878")
	var opened []string
	orig := boardOpenURL
	t.Cleanup(func() { boardOpenURL = orig })
	boardOpenURL = func(u string) error { opened = append(opened, u); return nil }

	if _, err := runBoardOpen(nil); err == nil || !strings.Contains(err.Error(), jobs.OwnerKeyPath()) {
		t.Errorf("no key file: err %v, want one naming %s", err, jobs.OwnerKeyPath())
	}
	key, err := jobs.EnsureOwnerKey()
	if err != nil {
		t.Fatal(err)
	}
	link := "http://127.0.0.1:7878/api/owner?key=" + key

	out, err := runBoardOpen(nil)
	if err != nil || len(opened) != 1 || opened[0] != link || strings.Contains(out, key) {
		t.Errorf("open: out %q err %v opened %v; want the link opened and the key not printed", out, err, opened)
	}
	out, err = runBoardOpen([]string{"--print"})
	if err != nil || strings.TrimSpace(out) != link || len(opened) != 1 {
		t.Errorf("--print: out %q err %v opened %v; want the link printed and nothing opened", out, err, opened)
	}
	boardOpenURL = func(string) error { return os.ErrNotExist }
	out, err = runBoardOpen(nil)
	if err != nil || !strings.Contains(out, link) {
		t.Errorf("browser failed: out %q err %v; want the link printed to open by hand", out, err)
	}
	if _, err := runBoardOpen([]string{"--bogus"}); err == nil {
		t.Error("unknown flag: want an error")
	}
}

// Every hook parser takes --owner-key-file (hookCommandFlags writes it for a
// parallel serve's cells) and turns it into the env var jobs.OwnerKeyPath
// reads. A parser that refused it would make the hook run nothing.
func TestHookParsersTakeTheOwnerKeyFile(t *testing.T) {
	const env = "KRAKEN_OWNER_KEY_FILE"
	check := func(label string) {
		t.Helper()
		if got := os.Getenv(env); got != "/iso/.kraken/owner-key" {
			t.Errorf("%s: %s = %q, want the flag's value", label, env, got)
		}
		os.Setenv(env, "")
	}
	t.Setenv(env, "")
	t.Setenv("KRAKEN_SERVE_ADDR", "")
	ctxHookFlags([]string{"--slug", "s", "--owner-key-file", "/iso/.kraken/owner-key"})
	check("ctx-hook")
	if slug, ok := hookArgs([]string{"--owner-key-file", "/iso/.kraken/owner-key", "s"}); !ok || slug != "s" {
		t.Errorf("pr-hook: slug %q ok %v, want s true", slug, ok)
	}
	check("pr-hook")
	if slug, _, ok := limitHookArgs([]string{"--owner-key-file", "/iso/.kraken/owner-key", "s"}); !ok || slug != "s" {
		t.Errorf("limit-hook: slug %q ok %v, want s true", slug, ok)
	}
	check("limit-hook")

	// recall-hook reads stdin, so it is held by source: each parser that
	// takes --serve-addr takes --owner-key-file too
	for _, f := range []string{"cmd_ctx.go", "cmd_jobs.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		addr, key := strings.Count(string(b), `case "--serve-addr":`), strings.Count(string(b), `case "--owner-key-file":`)
		if addr == 0 || addr != key {
			t.Errorf("%s: %d parsers take --serve-addr, %d take --owner-key-file", f, addr, key)
		}
	}
}
