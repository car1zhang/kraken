package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// cmd_watchlist_test.go — `kraken watchlist get|add|update|remove`'s testable
// core (runWatchlistGet/Upsert/Remove), mirroring cmd_questions_test.go's
// coverage of the same behavior contract: get prints server JSON re-indented
// (this family's one departure from the questions verbs' verbatim rule — see
// runWatchlistGet);
// add/update read stdin and POST the right op shape; remove takes a bare id;
// a 400 surfaces the server's own message; connection-refused names the
// address; missing args produce a usage error. The dispatch layer's util.Die
// branches go through TestHelperProcess/runDispatchSubprocess (defined in
// cmd_questions_test.go, same package).

// TestRunWatchlistGetReindentsServerJSON pins the get contract: the server's
// one-line list comes back as the SAME JSON document, multi-line — the
// single-line form had passed the Read tool's cap, so every nightly consumer
// was hand-parsing it — with the trailing newline kept.
func TestRunWatchlistGetReindentsServerJSON(t *testing.T) {
	served := `{"watchlist":[{"id":1,"observation":"o","class":"waste","state":"watching","sightings":2,"first_seen":1,"last_seen":2,"updated_at":2}]}` + "\n"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(served))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runWatchlistGet(nil)
	if err != nil {
		t.Fatalf("runWatchlistGet: %v", err)
	}
	if !strings.HasSuffix(string(got), "\n") {
		t.Fatalf("body = %q, want a trailing newline", got)
	}
	if lines := strings.Count(string(got), "\n"); lines < 10 {
		t.Fatalf("body has %d lines, want the re-indented multi-line form:\n%s", lines, got)
	}
	// same document: re-compacting the output reproduces the served bytes
	var buf bytes.Buffer
	if err := json.Compact(&buf, got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, got)
	}
	if buf.String()+"\n" != served {
		t.Fatalf("re-compacted output = %q, want the served document %q", buf.String(), served)
	}
	if gotPath != "/api/watchlist?" {
		t.Fatalf("path = %q, want the unfiltered read", gotPath)
	}
}

// TestIndentJSONDegradesToVerbatimOnNonJSON: the re-indent is a courtesy,
// never a gate — a body that does not parse passes through byte-identical.
// The rows are the shapes a dying kraken serve actually produces (empty,
// newline-only, truncated JSON) plus a proxy-injected error page.
func TestIndentJSONDegradesToVerbatimOnNonJSON(t *testing.T) {
	for _, body := range []string{
		"<html>bad gateway</html>",
		"",
		"\n",
		`{"watchlist":[{"id":1,"observation":"tr`,
	} {
		got, err := indentJSON([]byte(body), nil)
		if err != nil {
			t.Fatalf("indentJSON(%q): %v", body, err)
		}
		if string(got) != body {
			t.Fatalf("body %q came back %q, want the verbatim bytes", body, got)
		}
	}
}

// TestRunWatchlistGetStateFilter: the one optional positional this verb
// family takes. There is deliberately no slug anywhere — watchlist rows are
// global.
func TestRunWatchlistGetStateFilter(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Write([]byte(`{"watchlist":[]}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	if _, err := runWatchlistGet([]string{"shipped"}); err != nil {
		t.Fatalf("runWatchlistGet: %v", err)
	}
	if gotPath != "/api/watchlist?state=shipped" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestRunWatchlistGetBadArgsIsUsageError(t *testing.T) {
	for _, args := range [][]string{{""}, {"watching", "extra"}} {
		if _, err := runWatchlistGet(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("args %v: err = %v, want a usage error", args, err)
		}
	}
}

func TestRunWatchlistUpsertPostsOpAndItem(t *testing.T) {
	for _, op := range []string{"add", "update"} {
		var gotBody map[string]any
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.Write([]byte(`{"ok":true,"id":7}`))
		}))
		t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

		stdin := strings.NewReader(`{"observation":"zeus re-reads the same file","class":"waste"}`)
		body, err := runWatchlistUpsert(op, nil, stdin)
		srv.Close()
		if err != nil {
			t.Fatalf("op %s: runWatchlistUpsert: %v", op, err)
		}
		// the agent's write goes to the ITEM route, never to carl's verdict route
		if gotPath != "/api/watchlist/item" {
			t.Fatalf("op %s: path = %q, want the agent's route", op, gotPath)
		}
		if gotBody["op"] != op {
			t.Fatalf("op %s: body = %v", op, gotBody)
		}
		item, ok := gotBody["item"].(map[string]any)
		if !ok || item["observation"] != "zeus re-reads the same file" || item["class"] != "waste" {
			t.Fatalf("op %s: item = %v", op, gotBody["item"])
		}
		if id := watchlistRespID(body); id != 7 {
			t.Fatalf("op %s: id = %d, want 7", op, id)
		}
	}
}

// TestRunWatchlistUpsertForwardsRawJSON pins the thin-proxy rule: the verb
// does NOT model the wire schema, so a key it has never heard of (including
// a carl-owned one, which the SERVER is what refuses) rides through untouched
// rather than being silently dropped client-side.
func TestRunWatchlistUpsertForwardsRawJSON(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true,"id":1}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	stdin := strings.NewReader(`{"id":3,"observation":"o","class":"waste","verdict":"fine"}`)
	if _, err := runWatchlistUpsert("update", nil, stdin); err != nil {
		t.Fatalf("runWatchlistUpsert: %v", err)
	}
	item := gotBody["item"].(map[string]any)
	if _, ok := item["verdict"]; !ok {
		t.Fatal("verdict was dropped client-side — the server must be the one that refuses it, so the agent is told")
	}
}

func TestRunWatchlistUpsertRejectsMalformedStdin(t *testing.T) {
	if _, err := runWatchlistUpsert("add", nil, strings.NewReader("not json")); err == nil {
		t.Fatal("want an error for malformed stdin")
	}
	if _, err := runWatchlistUpsert("add", nil, strings.NewReader(`[{"observation":"o"}]`)); err == nil {
		t.Fatal("want an error for a JSON array (this verb takes one object)")
	}
	if _, err := runWatchlistUpsert("add", nil, strings.NewReader("null")); err == nil {
		t.Fatal("want an error for a JSON null body")
	}
}

// TestRunWatchlistUpsertRejectsSlugArg is the shape-of-the-family assertion:
// unlike questions/feedback, these verbs take NO positional at all, so a
// caller that pasted a slug in out of habit is told rather than having it
// ignored.
func TestRunWatchlistUpsertRejectsSlugArg(t *testing.T) {
	stdin := strings.NewReader(`{"observation":"o","class":"waste"}`)
	if _, err := runWatchlistUpsert("add", []string{"demo-job"}, stdin); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error naming the global watchlist", err)
	}
}

func TestRunWatchlistRemovePostsID(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	if err := runWatchlistRemove([]string{"12"}); err != nil {
		t.Fatalf("runWatchlistRemove: %v", err)
	}
	if gotBody["op"] != "remove" {
		t.Fatalf("body = %v", gotBody)
	}
	if id, ok := gotBody["id"].(float64); !ok || int64(id) != 12 {
		t.Fatalf("id = %v, want the numeric 12", gotBody["id"])
	}
}

func TestRunWatchlistRemoveBadArgs(t *testing.T) {
	for _, args := range [][]string{nil, {""}, {"1", "2"}} {
		if err := runWatchlistRemove(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("args %v: err = %v, want a usage error", args, err)
		}
	}
	if err := runWatchlistRemove([]string{"Q1"}); err == nil || strings.Contains(err.Error(), "usage:") {
		t.Fatalf("non-numeric id: err = %v, want a parse error naming the id", err)
	}
}

// TestWatchlist400SurfacesServerMessage: a refusal must reach the agent as
// the server's own sentence — the whole point of the carl-owned-key 400.
func TestWatchlist400SurfacesServerMessage(t *testing.T) {
	const msg = "verdict is carl's, not yours — drop it"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"` + msg + `"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	_, err := runWatchlistUpsert("update", nil, strings.NewReader(`{"id":1,"verdict":"fine"}`))
	if err == nil || err.Error() != msg {
		t.Fatalf("err = %v, want exactly the server's message %q", err, msg)
	}
}

func TestWatchlistConnectionRefusedNamesAddress(t *testing.T) {
	// bind then immediately close, so the port is (almost certainly) dead
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	_, err = runWatchlistGet(nil)
	if err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("err = %v, want a message naming %s", err, addr)
	}
}

func TestCmdWatchlistGetDispatchWritesStdout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"watchlist":[]}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	want := "{\n  \"watchlist\": []\n}\n"
	if got := captureStdout(t, func() { cmdWatchlist([]string{"get"}) }); got != want {
		t.Fatalf("stdout = %q, want the re-indented form %q", got, want)
	}
}

// TestCmdWatchlistAddDispatchLeavesStdoutEmpty pins the other half of the
// "stdout stays parseable" contract: a write confirms on STDERR only.
func TestCmdWatchlistAddDispatchLeavesStdoutEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true,"id":4}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(`{"observation":"o","class":"waste"}`); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })

	if got := captureStdout(t, func() { cmdWatchlist([]string{"add"}) }); got != "" {
		t.Fatalf("stdout = %q, want empty (writes confirm on stderr)", got)
	}
}

func TestCmdWatchlistNoArgsDispatchDiesNonZero(t *testing.T) {
	stderr, code := runDispatchSubprocess(t, "watchlist", nil)
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "usage: k watchlist") {
		t.Fatalf("stderr = %q, want a usage line", stderr)
	}
}

func TestCmdWatchlistUnknownOpDispatchDiesNonZero(t *testing.T) {
	stderr, code := runDispatchSubprocess(t, "watchlist", []string{"bump"})
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "unknown op") {
		t.Fatalf("stderr = %q, want the unknown-op message", stderr)
	}
}

// TestRunWatchlistUpsertRefusesInteractiveTTY: a human running `k watchlist
// add` by hand must get a usage error, not a process that blocks forever
// waiting on a terminal nobody is going to type JSON into.
func TestRunWatchlistUpsertRefusesInteractiveTTY(t *testing.T) {
	_, err := runWatchlistUpsert("add", nil, fakeTTYFile{strings.NewReader("")})
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("err = %v, want the interactive-terminal refusal", err)
	}
}
