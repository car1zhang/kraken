package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cmd_graph_test.go — `k graph pending|add|done|search|get|stats`'s testable core,
// covering the same behavior contract cmd_load_test.go pins for the load
// verb: a read prints server JSON verbatim, a write posts the body it was
// handed unchanged, a 4xx surfaces the server's own sentence, and bad args
// produce a usage error rather than a silently-defaulted request.
//
// cmdGraph's util.Die branches (no args, unknown op) are the same os.Exit
// shape every other verb's are and are not re-covered here.

func TestRunGraphPendingPassesLimitAndPrintsVerbatim(t *testing.T) {
	want := `{"episodes":[{"id":1,"kind":"job","ref":"r","content":"c","ts":1}]}` + "\n"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.URL.RawQuery != "" {
			gotPath += "?" + r.URL.RawQuery
		}
		w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runGraphPending(nil)
	if err != nil {
		t.Fatalf("runGraphPending: %v", err)
	}
	if string(got) != want {
		t.Fatalf("body = %q, want %q (verbatim passthrough)", got, want)
	}
	// No flag means no query string at all — the SERVER owns the default, so
	// the client must not bake a number of its own into the URL where the two
	// could drift.
	if gotPath != "/api/graph/pending" {
		t.Fatalf("path = %q, want no limit parameter when none was asked for", gotPath)
	}

	if _, err := runGraphPending([]string{"--limit", "7"}); err != nil {
		t.Fatalf("runGraphPending --limit: %v", err)
	}
	if gotPath != "/api/graph/pending?limit=7" {
		t.Fatalf("path = %q, want limit=7", gotPath)
	}

	if _, err := runGraphPending([]string{"--limit=9"}); err != nil {
		t.Fatalf("runGraphPending --limit=9: %v", err)
	}
	if gotPath != "/api/graph/pending?limit=9" {
		t.Fatalf("path = %q, want limit=9 from the inline form", gotPath)
	}
}

// TestRunGraphPendingRejectsBadArgs: a mistyped flag must be a usage error,
// never a silently ignored argument that fetches the default and looks like
// it worked. No server is started — every case must fail before any request.
func TestRunGraphPendingRejectsBadArgs(t *testing.T) {
	for _, args := range [][]string{
		{"--limt", "5"},
		{"--limit"},
		{"--limit", "0"},
		{"--limit", "-2"},
		{"--limit", "abc"},
		{"5"},
	} {
		if _, err := runGraphPending(args); err == nil {
			t.Errorf("runGraphPending(%q) = nil error, want a usage refusal", args)
		}
	}
}

// TestRunGraphAddForwardsStdinRaw pins the thin-proxy rule: the body on the
// wire is the caller's bytes, not a re-encoding through a local struct. A
// second copy of the wire schema here would drift from internal/api/graph.go
// the moment a field is added there.
func TestRunGraphAddForwardsStdinRaw(t *testing.T) {
	in := `{"episode_id":3,"edges":[{"src":"a","dst":"b","predicate":"p","fact":"f","strength":5}]}`
	resp := `{"nodes":[],"edges":[{"src":"a","dst":"b","id":1,"added":true}],"invalidated":[]}` + "\n"
	var gotBody, gotPath, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(resp))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runGraphAdd(nil, strings.NewReader(in))
	if err != nil {
		t.Fatalf("runGraphAdd: %v", err)
	}
	if string(got) != resp {
		t.Fatalf("stdout = %q, want the server's response verbatim (%q)", got, resp)
	}
	if gotPath != "/api/graph/add" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotType != "application/json" {
		t.Errorf("content-type = %q", gotType)
	}
	// json.RawMessage round-trips through json.Marshal unchanged, so the
	// server sees the caller's own bytes — compared as decoded values so the
	// assertion is about content rather than whitespace.
	var sent, orig any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("server got unparseable body %q: %v", gotBody, err)
	}
	if err := json.Unmarshal([]byte(in), &orig); err != nil {
		t.Fatal(err)
	}
	if b1, _ := json.Marshal(sent); string(b1) != mustMarshal(t, orig) {
		t.Errorf("server received %s, want %s", b1, mustMarshal(t, orig))
	}
}

func mustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRunGraphAddRejectsNonObjectStdin: a friendlier error than the server's
// "bad json" 400 for a plainly malformed stdin, and one that never leaves the
// process — the same local pre-check `k questions add` does.
func TestRunGraphAddRejectsNonObjectStdin(t *testing.T) {
	for _, in := range []string{"", "not json", "null", "[1,2]"} {
		if _, err := runGraphAdd(nil, strings.NewReader(in)); err == nil {
			t.Errorf("runGraphAdd(%q) = nil error, want a refusal", in)
		}
	}
	for _, args := range [][]string{
		{"extra"},
		{"--episode"},
		{"--episode", "0"},
		{"--episode", "abc"},
		{"--json"},
		{"--json", ""},
	} {
		if _, err := runGraphAdd(args, strings.NewReader("{}")); err == nil {
			t.Errorf("runGraphAdd(%q) = nil error, want a usage refusal", args)
		}
	}
}

// TestRunGraphAddReadsJSONFileAndStampsEpisode covers the spelling
// .claude/skills/graph/SKILL.md hands the extraction agent —
// `k graph add --episode <id> --json <path>` — and the one key this verb is
// allowed to touch. A payload with no episode_id of its own must arrive
// carrying the flag's, since that provenance is what ties every edge back to
// the span it came from.
func TestRunGraphAddReadsJSONFileAndStampsEpisode(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"nodes":[],"edges":[],"invalidated":[]}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte(
		`{"nodes":[{"name":"tokensPass","type":"component"}],"edges":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := runGraphAdd([]string{"--episode", "42", "--json", path}, strings.NewReader("")); err != nil {
		t.Fatalf("runGraphAdd: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("request body %q: %v", gotBody, err)
	}
	if n, ok := sent["episode_id"].(float64); !ok || int(n) != 42 {
		t.Errorf("episode_id = %v, want 42 from the flag", sent["episode_id"])
	}
	// The rest of the payload must survive untouched — this verb is a proxy,
	// not a second copy of the wire schema.
	nodes, ok := sent["nodes"].([]any)
	if !ok || len(nodes) != 1 {
		t.Fatalf("nodes = %v, want the file's single node passed through", sent["nodes"])
	}

	// A payload that already names an episode, with the flag also given: the
	// flag is the more specific instruction and wins.
	if err := os.WriteFile(path, []byte(`{"episode_id":7,"nodes":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runGraphAdd([]string{"--episode=42", "--json=" + path}, strings.NewReader("")); err != nil {
		t.Fatalf("runGraphAdd (inline flags): %v", err)
	}
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatal(err)
	}
	if n, ok := sent["episode_id"].(float64); !ok || int(n) != 42 {
		t.Errorf("episode_id = %v, want the flag (42) to win over the payload's 7", sent["episode_id"])
	}

	// No flag: the payload's own episode_id is left exactly as written.
	if _, err := runGraphAdd([]string{"--json", path}, strings.NewReader("")); err != nil {
		t.Fatalf("runGraphAdd (no --episode): %v", err)
	}
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatal(err)
	}
	if n, ok := sent["episode_id"].(float64); !ok || int(n) != 7 {
		t.Errorf("episode_id = %v, want the payload's own 7 untouched", sent["episode_id"])
	}

	if _, err := runGraphAdd([]string{"--json", filepath.Join(t.TempDir(), "missing.json")}, strings.NewReader("")); err == nil {
		t.Error("runGraphAdd with an unreadable --json = nil error, want the read failure surfaced")
	}
}

// TestRunGraphDone covers `k graph done <episode id>`: one round trip, the id
// in the body, and a usage refusal for everything that is not a positive
// whole number.
func TestRunGraphDone(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	id, err := runGraphDone([]string{"12"})
	if err != nil {
		t.Fatalf("runGraphDone: %v", err)
	}
	if id != 12 {
		t.Errorf("returned id = %d, want 12 (the confirmation line prints it)", id)
	}
	if gotPath != "/api/graph/episode-done" {
		t.Errorf("path = %q", gotPath)
	}
	if strings.TrimSpace(gotBody) != `{"id":12}` {
		t.Errorf("body = %q, want {\"id\":12}", gotBody)
	}

	for _, args := range [][]string{nil, {}, {"0"}, {"-1"}, {"abc"}, {"1", "2"}} {
		if _, err := runGraphDone(args); err == nil {
			t.Errorf("runGraphDone(%q) = nil error, want a usage refusal", args)
		}
	}
}

// TestRunGraphSearchPrintsTriples: the verb renders the triple text, not the
// JSON envelope — its reader is a human or an agent pasting facts into
// context, and either would have to strip the envelope by hand.
func TestRunGraphSearchPrintsTriples(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Write([]byte(`{"nodes":[{"id":1,"name":"kraken","type":"system","description":"the CLI"}],"facts":[{"id":1}],"triples":"kraken -[spawns]-> zeus"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runGraphSearch([]string{"how does a cell spawn", "--limit", "5"})
	if err != nil {
		t.Fatalf("runGraphSearch: %v", err)
	}
	// Entities first (what the skill's canonicalization step reads), then the
	// facts — and no JSON envelope in sight.
	want := "entities:\n  kraken (system) — the CLI\n\nfacts:\nkraken -[spawns]-> zeus\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(gotBody), &sent); err != nil {
		t.Fatalf("request body %q: %v", gotBody, err)
	}
	if sent["query"] != "how does a cell spawn" {
		t.Errorf("query = %v", sent["query"])
	}
	if n, ok := sent["limit"].(float64); !ok || int(n) != 5 {
		t.Errorf("limit = %v, want 5", sent["limit"])
	}
}

// TestRunGraphSearchEmptyResultSaysSo: silence is indistinguishable from a
// broken pipe, so a query that matched nothing gets a sentence.
func TestRunGraphSearchEmptyResultSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"nodes":[],"facts":[],"triples":""}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runGraphSearch([]string{"nothing matches this"})
	if err != nil {
		t.Fatalf("runGraphSearch: %v", err)
	}
	if !strings.Contains(got, "nothing matched") {
		t.Fatalf("output = %q, want an explicit empty-result line", got)
	}
}

func TestRunGraphSearchRejectsBadArgs(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"   "},
		{"--limit", "5"}, // a flag where the query positional belongs
	} {
		if _, err := runGraphSearch(args); err == nil {
			t.Errorf("runGraphSearch(%q) = nil error, want a usage refusal", args)
		}
	}
}

// TestRunGraphSurfacesServerError pins the "never a raw HTTP dump" half of
// the contract: a 4xx becomes the server's own sentence.
func TestRunGraphSurfacesServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"edges[0]: strength 0 out of range"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	_, err := runGraphAdd(nil, strings.NewReader(`{"edges":[{"src":"a"}]}`))
	if err == nil {
		t.Fatal("runGraphAdd = nil error on a 400")
	}
	if err.Error() != "edges[0]: strength 0 out of range" {
		t.Fatalf("error = %q, want the server's own sentence", err)
	}
}

func TestRunGraphStatsRejectsArgs(t *testing.T) {
	if _, err := runGraphStats([]string{"extra"}); err == nil {
		t.Error("runGraphStats with an argument = nil error, want a usage refusal")
	}
}
