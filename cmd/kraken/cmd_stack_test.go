package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cmd_stack_test.go — `kraken stack add|rm|get|clear`'s testable core
// (runStackGet/parseStackAdd/parseStackRm/runStackOp), the same contract
// cmd_load_test.go pins: get prints server JSON verbatim; a write posts the
// per-op body; a 4xx surfaces the server's own sentence; connection-refused
// names the address; bad args are usage errors.

func TestRunStackGetPrintsServerJSONVerbatim(t *testing.T) {
	want := `{"stack":[{"url":"http://127.0.0.1:7878","label":"board","status":"up","at":1769040000000}]}` + "\n"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runStackGet([]string{"demo-job"})
	if err != nil {
		t.Fatalf("runStackGet: %v", err)
	}
	if string(got) != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
	if gotPath != "/api/job/stack?slug=demo-job" {
		t.Fatalf("path = %q", gotPath)
	}
	for _, args := range [][]string{nil, {"a", "b"}, {""}} {
		if _, err := runStackGet(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("%v: err = %v, want usage error", args, err)
		}
	}
}

func TestParseStackAddShapes(t *testing.T) {
	for _, args := range [][]string{
		{"demo-job", "http://127.0.0.1:7878", "--label", "board"},
		{"demo-job", "http://127.0.0.1:7878", "--label=board"},
	} {
		slug, l, err := parseStackAdd(args)
		if err != nil || slug != "demo-job" || l.url != "http://127.0.0.1:7878" || l.label != "board" {
			t.Fatalf("%v: slug %q link %+v err %v", args, slug, l, err)
		}
	}
	if _, l, err := parseStackAdd([]string{"demo-job", "HTTPS://x.ngrok.app"}); err != nil || l.label != "" {
		t.Fatalf("unlabelled: %+v %v", l, err)
	}
	bad := map[string][]string{
		"no args":      nil,
		"slug only":    {"demo-job"},
		"no scheme":    {"demo-job", "localhost:5173"},
		"ftp scheme":   {"demo-job", "ftp://localhost:21"},
		"label no val": {"demo-job", "http://localhost:1", "--label"},
		"unknown flag": {"demo-job", "http://localhost:1", "--name", "x"},
		"stray arg":    {"demo-job", "http://localhost:1", "extra"},
		"flag as slug": {"--label", "x", "http://localhost:1"},
		"empty slug":   {"", "http://localhost:1"},
	}
	for name, args := range bad {
		if _, _, err := parseStackAdd(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("%s (%v): err = %v, want a usage error", name, args, err)
		}
	}
}

func TestParseStackRmAndSlugOnly(t *testing.T) {
	slug, tok, err := parseStackRm([]string{"demo-job", "board"})
	if err != nil || slug != "demo-job" || tok != "board" {
		t.Fatalf("rm: %q %q %v", slug, tok, err)
	}
	for _, args := range [][]string{nil, {"demo-job"}, {"demo-job", "a", "b"}, {"demo-job", ""}} {
		if _, _, err := parseStackRm(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Errorf("rm %v: err = %v, want usage", args, err)
		}
	}
	if s, err := parseStackSlugOnly([]string{"x"}, "clear"); err != nil || s != "x" {
		t.Fatalf("clear: %q %v", s, err)
	}
	if _, err := parseStackSlugOnly(nil, "clear"); err == nil || !strings.Contains(err.Error(), "k stack clear") {
		t.Fatalf("clear no args: %v", err)
	}
}

func TestRunStackOpPostsPerOpBody(t *testing.T) {
	var gotBody map[string]any
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	if err := runStackOp("demo-job", "add", "http://localhost:5173", "ui"); err != nil {
		t.Fatalf("runStackOp: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/job/stack" {
		t.Fatalf("%s %s", gotMethod, gotPath)
	}
	want := map[string]any{"slug": "demo-job", "op": "add", "url": "http://localhost:5173", "label": "ui"}
	for k, v := range want {
		if gotBody[k] != v {
			t.Errorf("body[%s] = %v, want %v", k, gotBody[k], v)
		}
	}
	if len(gotBody) != len(want) {
		t.Errorf("body has %d keys %v, want exactly %d", len(gotBody), gotBody, len(want))
	}
}

func TestRunStackOpSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error":"demo-job has no running cell to link a stack to (state draft)"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	err := runStackOp("demo-job", "add", "http://localhost:1", "")
	if err == nil || err.Error() != "demo-job has no running cell to link a stack to (state draft)" {
		t.Fatalf("err = %v, want the server's own sentence", err)
	}
}

func TestRunStackOpUnreachableNamesAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)
	err = runStackOp("demo-job", "clear", "", "")
	if err == nil || !strings.Contains(err.Error(), addr) || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v, want unreachable naming %s", err, addr)
	}
}
