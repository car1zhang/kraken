package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/util"
)

// cmd_jobs_logevent_test.go — cmdLogEvent's dual-mode contract: POST to
// kraken serve's /api/logevent when reachable, falling back to the direct
// util.LogEvent (JSONL) write otherwise. cmdLogEvent itself is the only part
// of cmd_jobs.go this slice owns; this is a new file, not a change to any
// other function there.

func TestCmdLogEventPostsToServerWhenAvailable(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())

	var gotBody []byte
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/logevent" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	cmdLogEvent([]string{"install", "make-install", "installed", "to", "~/.local/bin"})

	if hits != 1 {
		t.Fatalf("want exactly 1 POST to /api/logevent, got %d", hits)
	}
	var req map[string]string
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req["src"] != "install" || req["event"] != "make-install" || req["msg"] != "installed to ~/.local/bin" {
		t.Fatalf("posted body = %+v", req)
	}

	// the server "logged" it, not this process directly — no local JSONL
	// file should have been written.
	if files := util.ListLogFiles(); len(files) != 0 {
		t.Fatalf("want no local log files written when the server handled it, got %+v", files)
	}
}

func TestCmdLogEventFallsBackWhenServerUnreachable(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	cmdLogEvent([]string{"cli", "some-event", "hello", "world"})

	files := util.ListLogFiles()
	if len(files) != 1 {
		t.Fatalf("want 1 local log file from the fallback write, got %d", len(files))
	}
	entries, err := util.ReadLogFile(files[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Src != "cli" || entries[0].Event != "some-event" || entries[0].Msg != "hello world" {
		t.Fatalf("entries = %+v", entries)
	}
}

// withPrHookStdin redirects os.Stdin to a pipe carrying data for the
// duration of fn — cmdPrHook (via jobs.PrHook) reads its payload from
// stdin, same as a real Claude Code PostToolUse hook invocation; without
// this, reading an untouched os.Stdin in a test process would block
// indefinitely rather than fail fast.
func withPrHookStdin(t *testing.T, data string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(data); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	fn()
}

// TestCmdPrHookServeAddrFlagPropagates covers E7 end-to-end at the CLI
// dispatch layer: `kraken pr-hook --serve-addr <addr> --db-path <path>
// <slug>` (the exact command shape cell.go's prHookCommand now installs
// into a cell's hook) must route the underlying jobs.PrHook POST to that
// addr and set KRAKEN_DB_PATH for the rest of this process — not the
// KRAKEN_SERVE_ADDR env var callers would otherwise have to pre-set by
// hand, which a real hook invocation (a brand-new OS process spawned by
// Claude Code) never gets to do.
func TestCmdPrHookServeAddrFlagPropagates(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "") // must NOT be relied on; the flag alone must work
	t.Cleanup(func() {
		os.Unsetenv("KRAKEN_SERVE_ADDR")
		os.Unsetenv("KRAKEN_DB_PATH")
	})

	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/pr-attach" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dbPath := filepath.Join(t.TempDir(), "custom-kraken.db")
	payload := `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/9\n"}}`
	withPrHookStdin(t, payload, func() {
		cmdPrHook([]string{"--serve-addr", srv.Listener.Addr().String(), "--db-path", dbPath, "flag-propagate-job"})
	})

	if hits != 1 {
		t.Fatalf("want exactly 1 POST to /api/pr-attach at the --serve-addr target, got %d", hits)
	}
	if got := os.Getenv("KRAKEN_DB_PATH"); got != dbPath {
		t.Fatalf("KRAKEN_DB_PATH = %q, want %q (set for the rest of this process by --db-path)", got, dbPath)
	}
}

// TestCmdPrHookMalformedArgsSilentlyIgnored confirms cmdPrHook's contract
// (must never fail zeus' tool call) survives the new flag parsing: no
// slug, an unterminated flag, or more than one bare arg are all silently
// ignored rather than panicking or hanging on stdin.
func TestCmdPrHookMalformedArgsSilentlyIgnored(t *testing.T) {
	cases := [][]string{
		nil,
		{"--serve-addr"},                // flag with no value
		{"--db-path"},                   // flag with no value
		{"one", "two"},                  // more than one bare arg
		{"--serve-addr", "x", "a", "b"}, // flag consumed, still two bare args
	}
	for _, args := range cases {
		cmdPrHook(args) // must return promptly without touching stdin
	}
}
