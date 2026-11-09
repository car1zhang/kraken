package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cmd_load_test.go — `kraken load get`'s testable core (runLoadGet),
// covering the behavior contract cmd_questions_test.go pins for the
// questions family: get prints server JSON verbatim; a 4xx surfaces the
// server's own message; connection-refused names the address; bad args
// produce a usage error. cmdLoad's util.Die branches (no-args, unknown op)
// call os.Exit(1) and are covered through the shared
// TestHelperProcess/runDispatchSubprocess harness.

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

// TestLoad404SurfacesServerMessage: the server's own {"error": "..."}
// sentence comes back verbatim (via qfServerError), never a raw HTTP dump.
func TestLoad404SurfacesServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"no such job: nope"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	_, err := runLoadGet([]string{"nope"})
	if err == nil || !strings.Contains(err.Error(), "no such job: nope") {
		t.Fatalf("err = %v, want the server's own message", err)
	}
	if strings.Contains(err.Error(), "HTTP/1.1") {
		t.Fatalf("err = %v, want no raw HTTP dump", err)
	}
}

// TestLoadConnectionRefusedNamesAddress: an unreachable serve is reported
// with the address this process tried, so a misconfigured
// KRAKEN_SERVE_ADDR is visible in the message rather than a bare refusal.
func TestLoadConnectionRefusedNamesAddress(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	if _, err := runLoadGet([]string{"demo-job"}); err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("err = %v, want it to name %q", err, addr)
	}
}

// TestCmdLoadGetDispatchWritesStdout covers cmdLoad itself: the read op's
// output must be parseable on stdout.
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

func TestCmdLoadNoArgsDispatchDiesNonZero(t *testing.T) {
	stderrOut, code := runDispatchSubprocess(t, "load", nil)
	if code == 0 {
		t.Fatalf("want a non-zero exit, got 0 (stderr=%q)", stderrOut)
	}
	if !strings.Contains(stderrOut, "usage: k load") {
		t.Fatalf("stderr = %q, want a usage message naming the `k load` verb", stderrOut)
	}
}

// TestCmdLoadRetiredOpsDieNonZero: `set` and `clear` were zeus' writes and
// are gone — the estimate is the daemon's Jev judgment now — so a cell that
// still types one gets a refusal naming the op, never a silent success and
// never a request to the server.
func TestCmdLoadRetiredOpsDieNonZero(t *testing.T) {
	for _, op := range []string{"set", "clear", "bogus"} {
		stderrOut, code := runDispatchSubprocess(t, "load", []string{op, "demo-job"})
		if code == 0 {
			t.Fatalf("%s: want a non-zero exit, got 0 (stderr=%q)", op, stderrOut)
		}
		if !strings.Contains(stderrOut, `"`+op+`"`) {
			t.Fatalf("%s: stderr = %q, want it to name the op", op, stderrOut)
		}
	}
}
