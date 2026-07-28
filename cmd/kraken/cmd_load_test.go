package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cmd_load_test.go — `kraken load get|set|clear`'s testable core
// (runLoadGet/parseLoadSet/runLoadSet/runLoadClear), covering the same
// behavior contract cmd_questions_test.go pins for the questions family: get
// prints server JSON verbatim; a write posts the right body and leaves stdout
// empty; a 4xx surfaces the server's own message; connection-refused names
// the address; bad args produce a usage error naming the valid range.
// cmdLoad's util.Die branches (no-args, unknown op) call os.Exit(1) and are
// covered through the shared TestHelperProcess/runDispatchSubprocess harness.

func TestRunLoadGetPrintsServerJSONVerbatim(t *testing.T) {
	want := `{"load":{"unblock":2,"scope":8,"note":"why","at":1769040000000}}` + "\n"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runLoadGet([]string{"demo-job"})
	if err != nil {
		t.Fatalf("runLoadGet: %v", err)
	}
	if string(got) != want {
		t.Fatalf("body = %q, want %q (verbatim passthrough)", got, want)
	}
	if gotPath != "/api/job/load?slug=demo-job" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestRunLoadGetMissingSlugIsUsageError(t *testing.T) {
	if _, err := runLoadGet(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if _, err := runLoadGet([]string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("extra arg: err = %v, want a usage error", err)
	}
}

func TestParseLoadSetAcceptsBothFlagForms(t *testing.T) {
	for _, args := range [][]string{
		{"demo-job", "--unblock", "2", "--scope", "8", "--note", "one decision"},
		{"demo-job", "--unblock=2", "--scope=8", "--note=one decision"},
		{"demo-job", "--scope=8", "--note", "one decision", "--unblock", "2"},
	} {
		slug, l, err := parseLoadSet(args)
		if err != nil {
			t.Fatalf("%v: parseLoadSet: %v", args, err)
		}
		if slug != "demo-job" || l.unblock != 2 || l.scope != 8 || l.note != "one decision" {
			t.Fatalf("%v: slug %q, fields %+v", args, slug, l)
		}
	}

	// --note is genuinely optional; the numbers are the payload
	if _, l, err := parseLoadSet([]string{"demo-job", "--unblock", "1", "--scope", "10"}); err != nil || l.note != "" {
		t.Fatalf("fields %+v, err %v", l, err)
	}
}

// TestParseLoadSetRejectsBadFlags covers the whole local gate: a missing
// value, a non-numeric value, an out-of-range number, a half estimate, and an
// unrecognized argument must each be a usage error naming the range rather
// than garbage sent to the server.
func TestParseLoadSetRejectsBadFlags(t *testing.T) {
	cases := map[string][]string{
		"no args":          nil,
		"slug only":        {"demo-job"},
		"missing value":    {"demo-job", "--unblock", "2", "--scope"},
		"non-numeric":      {"demo-job", "--unblock", "two", "--scope", "8"},
		"zero":             {"demo-job", "--unblock", "0", "--scope", "8"},
		"over ten":         {"demo-job", "--unblock", "2", "--scope", "11"},
		"negative":         {"demo-job", "--unblock", "-3", "--scope", "8"},
		"half estimate":    {"demo-job", "--unblock", "2"},
		"unknown flag":     {"demo-job", "--unblock", "2", "--scope", "8", "--bogus", "x"},
		"stray positional": {"demo-job", "--unblock", "2", "--scope", "8", "extra"},
		"flag as slug":     {"--unblock", "2", "--scope", "8"},
	}
	for name, args := range cases {
		_, _, err := parseLoadSet(args)
		if err == nil {
			t.Errorf("%s (%v): want an error", name, args)
			continue
		}
		if !strings.Contains(err.Error(), "1-10") {
			t.Errorf("%s: err = %v, want it to name the valid range", name, err)
		}
	}
}

func TestRunLoadSetPostsBody(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	if err := runLoadSet("demo-job", loadFields{unblock: 2, scope: 8, note: "one decision"}); err != nil {
		t.Fatalf("runLoadSet: %v", err)
	}
	if gotPath != "/api/job/load" || gotMethod != http.MethodPost {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	if gotBody["slug"] != "demo-job" || gotBody["note"] != "one decision" {
		t.Fatalf("body = %v", gotBody)
	}
	unblock, _ := gotBody["unblock"].(float64)
	scope, _ := gotBody["scope"].(float64)
	if unblock != 2 || scope != 8 {
		t.Fatalf("numbers = %v/%v", gotBody["unblock"], gotBody["scope"])
	}
	// `at` is the server's to stamp — sending one would let an estimate claim
	// to be fresher than the write that made it
	if _, ok := gotBody["at"]; ok {
		t.Fatalf("body carries an `at`: %v", gotBody)
	}
}

func TestRunLoadClearPostsZeroPair(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	if err := runLoadClear("demo-job"); err != nil {
		t.Fatalf("runLoadClear: %v", err)
	}
	unblock, _ := gotBody["unblock"].(float64)
	scope, _ := gotBody["scope"].(float64)
	if gotBody["slug"] != "demo-job" || unblock != 0 || scope != 0 {
		t.Fatalf("body = %v", gotBody)
	}
}

func TestParseLoadClearRejectsBadArgs(t *testing.T) {
	for _, args := range [][]string{nil, {""}, {"a", "b"}} {
		if _, err := parseLoadClear(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("args %v: err = %v, want a usage error", args, err)
		}
	}
}

// TestLoad400SurfacesServerMessage: the server's own {"error": "..."}
// sentence comes back verbatim (via qfServerError), never a raw HTTP dump —
// the whole point of this verb versus zeus hand-rolling curl.
func TestLoad400SurfacesServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"half estimate (unblock 2, scope 0): roi is scope/unblock"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	err := runLoadSet("demo-job", loadFields{unblock: 2, scope: 8})
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "half estimate") {
		t.Fatalf("err = %v, want the server's own message surfaced verbatim", err)
	}
	if strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "HTTP") {
		t.Fatalf("err = %v, must not be a raw HTTP status dump when the server gave a message", err)
	}
}

func TestLoadConnectionRefusedNamesAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // now guaranteed nobody is listening there
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	if _, err := runLoadGet([]string{"demo-job"}); err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("get: err = %v, want it to name the address %q it tried", err, addr)
	}
	if err := runLoadClear("demo-job"); err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("clear: err = %v, want it to name %q", err, addr)
	}
}

// TestCmdLoadGetDispatchWritesStdout / ...SetDispatchLeavesStdoutEmpty cover
// cmdLoad itself: the read op's output must be parseable on stdout, and a
// write's confirmation must go to stderr only.
func TestCmdLoadGetDispatchWritesStdout(t *testing.T) {
	want := `{"load":null}` + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got := captureStdout(t, func() { cmdLoad([]string{"get", "demo-job"}) })
	if got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestCmdLoadSetDispatchLeavesStdoutEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got := captureStdout(t, func() { cmdLoad([]string{"set", "demo-job", "--unblock", "2", "--scope", "8"}) })
	if got != "" {
		t.Fatalf("stdout = %q, want empty for a write op", got)
	}
}

func TestCmdLoadNoArgsDispatchDiesNonZero(t *testing.T) {
	stderrOut, code := runDispatchSubprocess(t, "load", nil)
	if code == 0 {
		t.Fatalf("want a non-zero exit, got 0 (stderr=%q)", stderrOut)
	}
	if !strings.Contains(stderrOut, "usage: k load") {
		t.Fatalf("stderr = %q, want a usage message naming the `k load` verbs", stderrOut)
	}
}

func TestCmdLoadUnknownOpDispatchDiesNonZero(t *testing.T) {
	stderrOut, code := runDispatchSubprocess(t, "load", []string{"bogus", "demo-job"})
	if code == 0 {
		t.Fatalf("want a non-zero exit, got 0 (stderr=%q)", stderrOut)
	}
	if !strings.Contains(stderrOut, `"bogus"`) {
		t.Fatalf("stderr = %q, want it to name the unknown op", stderrOut)
	}
}

// TestCmdLoadBadFlagsDispatchDiesNonZero: a mistyped flag must never reach
// the server — cmdLoad dies before any HTTP call, so no server is started
// here and a request would fail loudly rather than silently succeed.
func TestCmdLoadBadFlagsDispatchDiesNonZero(t *testing.T) {
	stderrOut, code := runDispatchSubprocess(t, "load", []string{"set", "demo-job", "--unblock", "99", "--scope", "8"})
	if code == 0 {
		t.Fatalf("want a non-zero exit, got 0 (stderr=%q)", stderrOut)
	}
	if !strings.Contains(stderrOut, "1-10") {
		t.Fatalf("stderr = %q, want it to name the valid range", stderrOut)
	}
}
