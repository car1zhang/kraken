package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// kronosremoval_edge_test.go — the EDGE dimension of slice G of
// artifacts/50-contract.md (kronos removal) as it lands in cmd/kraken: the
// dispatch switch and the `_k` completion carrying no trace of the removed
// verbs in ANY spelling, no non-test source file in the package naming
// kronos, and logCellAction's body surviving a slug that needs escaping and
// a serve address nobody is listening on.
//
// Package-level identifiers carry the `edge` prefix.

func edgeKronosRemovalRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// Not just `case "orch":` — the verb must not survive as one arm of a
// multi-value case (`case "orch", "o":`) or anywhere else as a literal.
func TestKronosRemovalEdgeMainHasNoRemovedVerbLiterals(t *testing.T) {
	src := edgeKronosRemovalRead(t, "main.go")
	for _, bad := range []string{
		`case "orch":`, `case "ctx-hook":`, `case "rearm-hook":`,
		`"orch"`, `"ctx-hook"`, `"rearm-hook"`, `"kronos"`,
	} {
		if strings.Contains(src, bad) {
			t.Errorf("main.go still contains %s", bad)
		}
	}
	if strings.Contains(strings.ToLower(src), "kronos") {
		t.Errorf("main.go still mentions kronos (any case)")
	}
	for _, keep := range []string{`case "board":`, `case "cell":`, `case "job":`, `case "slack":`, `case "limit-hook":`} {
		if !strings.Contains(src, keep) {
			t.Errorf("main.go lost %s — the removal took a kept verb with it", keep)
		}
	}
}

func TestKronosRemovalEdgeCompletionHasNoRemovedVerbs(t *testing.T) {
	comp := edgeKronosRemovalRead(t, "_k")
	lower := strings.ToLower(comp)
	for _, bad := range []string{"orch", "kronos", "ctx-hook", "rearm-hook"} {
		if strings.Contains(lower, bad) {
			t.Errorf("_k still contains %q (case-insensitive)", bad)
		}
	}
	for _, keep := range []string{"board", "cell", "slack", "job"} {
		if !strings.Contains(comp, keep) {
			t.Errorf("_k lost %q", keep)
		}
	}
}

// The contract's repo-text row: `rg -i kronos` over cmd/ returns nothing,
// comments included. Scoped here to the package's non-test Go sources plus
// `_k` (the test files of this change necessarily name the token).
func TestKronosRemovalEdgeNoPackageSourceNamesKronos(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || (!strings.HasSuffix(n, ".go") && n != "_k") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		if strings.Contains(strings.ToLower(edgeKronosRemovalRead(t, filepath.Join(".", n))), "kronos") {
			t.Errorf("%s still mentions kronos", n)
		}
	}
}

// A slug carrying JSON-hostile bytes must arrive intact as "msg", with
// "src":"cell" — the body is built as JSON, not by string concatenation.
func TestKronosRemovalEdgeLogCellActionEscapesSlug(t *testing.T) {
	got := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/logevent" {
			raw, _ := io.ReadAll(r.Body)
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				m = map[string]any{"_undecodable": string(raw)}
			}
			got <- m
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	slug := "weird \"slug\"\\n<kronos>\ttab"
	logCellAction("steer", slug)

	select {
	case m := <-got:
		if u, ok := m["_undecodable"]; ok {
			t.Fatalf("logevent body is not JSON: %v", u)
		}
		if m["src"] != "cell" {
			t.Errorf(`src = %v, want "cell"`, m["src"])
		}
		if m["event"] != "steer" {
			t.Errorf(`event = %v, want "steer"`, m["event"])
		}
		if m["msg"] != slug {
			t.Errorf("msg = %q, want the slug byte-for-byte %q", m["msg"], slug)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("logCellAction posted nothing to /api/logevent within 5s")
	}
}

// Nobody listening: the call must come back promptly rather than hang or
// panic. Contract silence — the contract states only the body shape; the
// strict answer asserted is "returns within 10s".
func TestKronosRemovalEdgeLogCellActionNoServerReturns(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	done := make(chan struct{})
	go func() {
		defer close(done)
		logCellAction("submit", "edge-no-server")
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("logCellAction hung with no server listening")
	}
}
