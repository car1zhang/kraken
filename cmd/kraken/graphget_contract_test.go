package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// graphget_contract_test.go — the LOGIC pins for slice S3 of
// artifacts/50-contract.md: `k graph get <handle>...`, the verb that
// dereferences an injected [F# E#] handle.
//
// The verb is a thin proxy — parse, one qfGet per handle, write the body
// through — so every case here drives it against a stub server that RECORDS
// what it was asked for. That is the right altitude: the bundle's contents are
// pinned server-side in internal/api, and what this slice owns is the mapping
// from a handle to a URL and the promise that the body reaches stdout
// untouched.
//
// Assertions are on runGraphGet's returned error and the bytes it wrote. Exit
// codes belong to cmdGraph and util.Die and are not in-process-testable.
//
// Usage errors, unparseable handles, and the serve-down path are the sibling
// edge suite's.

// fxcGraphStub is a kraken serve stand-in that remembers every request URI in
// arrival order. The mutex is belt and braces — the verb's round trips are
// sequential — but the handler runs on the server's goroutine, so the slice is
// genuinely shared.
type fxcGraphStub struct {
	mu   sync.Mutex
	reqs []string
}

// fxcStubServe stands the stub up and points the CLI at it, the convention
// every serve-backed verb's tests use (KRAKEN_SERVE_ADDR → jobs.ServeBaseURL).
// body is called with the request URI so a case can tell one response from
// another without threading state through the test.
func fxcStubServe(t *testing.T, body func(uri string) string) *fxcGraphStub {
	t.Helper()
	s := &fxcGraphStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uri := r.URL.RequestURI()
		s.mu.Lock()
		s.reqs = append(s.reqs, uri)
		s.mu.Unlock()
		w.Write([]byte(body(uri)))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	return s
}

func (s *fxcGraphStub) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.reqs...)
}

// fxcEchoBody is the default stub response: a one-object body naming the URI
// that produced it, so output ORDER and request order are checkable against
// each other rather than separately.
func fxcEchoBody(uri string) string { return `{"asked":"` + uri + `"}` }

// TestFxcParseGraphHandleAcceptsTheThreeForms pins the handle grammar: `F123`
// and a bare `123` are the same edge, `E456` is an episode, both prefixes are
// case-insensitive, and surrounding whitespace is trimmed.
//
// The bare form matters because it is what a human types after reading an id
// off a board or a log; the prefixed form is what the injected line hands
// them. Both have to reach the same place or the block's handle is a format
// people have to translate.
func TestFxcParseGraphHandleAcceptsTheThreeForms(t *testing.T) {
	for _, c := range []struct {
		in          string
		wantEpisode bool
		wantID      int64
	}{
		{"F123", false, 123},
		{"123", false, 123},
		{"E456", true, 456},
		{"f123", false, 123},
		{"e456", true, 456},
		{" F123 ", false, 123},
		{"\t123\n", false, 123},
		{" e456\t", true, 456},
		{"F1", false, 1},
		{"E1091", true, 1091},
		{"148111", false, 148111},
	} {
		t.Run(fmt.Sprintf("%q", c.in), func(t *testing.T) {
			episode, id, err := parseGraphHandle(c.in)
			if err != nil {
				t.Fatalf("parseGraphHandle(%q) errored: %v", c.in, err)
			}
			if episode != c.wantEpisode {
				t.Errorf("parseGraphHandle(%q) episode = %v, want %v", c.in, episode, c.wantEpisode)
			}
			if id != c.wantID {
				t.Errorf("parseGraphHandle(%q) id = %d, want %d", c.in, id, c.wantID)
			}
		})
	}
}

// TestFxcGraphGetFPrefixAndBareIdHitTheSameURL is the parse rule stated as the
// only thing that can be observed from outside the process: the two spellings
// produce byte-identical requests.
//
// Asserted through two separate runs against one stub rather than by comparing
// parseGraphHandle's return values, because the claim being made is about the
// URL the server sees — the place a divergence would actually hurt.
func TestFxcGraphGetFPrefixAndBareIdHitTheSameURL(t *testing.T) {
	stub := fxcStubServe(t, fxcEchoBody)

	for _, arg := range []string{"F123", "123"} {
		var out bytes.Buffer
		if err := runGraphGet([]string{arg}, &out); err != nil {
			t.Fatalf("runGraphGet(%q): %v", arg, err)
		}
	}

	got := stub.paths()
	const want = "/api/graph/fact?id=123"
	if len(got) != 2 {
		t.Fatalf("stub saw %d requests, want 2: %v", len(got), got)
	}
	for i, p := range got {
		if p != want {
			t.Errorf("request %d = %q, want %q", i, p, want)
		}
	}
	if got[0] != got[1] {
		t.Errorf("F123 asked for %q and bare 123 asked for %q — the two spellings name the same edge", got[0], got[1])
	}
}

// TestFxcGraphGetEPrefixRoutesToTheEpisodePath pins the second route: an `E`
// handle is an episode id and goes to the episode read, never to the fact one.
// The two id spaces are unrelated, so a misrouted handle does not fail — it
// returns a DIFFERENT record, which is the failure mode with no alarm on it.
func TestFxcGraphGetEPrefixRoutesToTheEpisodePath(t *testing.T) {
	stub := fxcStubServe(t, fxcEchoBody)

	var out bytes.Buffer
	if err := runGraphGet([]string{"E456"}, &out); err != nil {
		t.Fatalf("runGraphGet(E456): %v", err)
	}

	got := stub.paths()
	if len(got) != 1 {
		t.Fatalf("stub saw %d requests, want 1: %v", len(got), got)
	}
	if want := "/api/graph/episode?id=456"; got[0] != want {
		t.Errorf("E456 asked for %q, want %q", got[0], want)
	}
}

// TestFxcGraphGetOutputOrderMatchesArgumentOrder pins the multi-handle
// contract: one round trip per handle, in argument order, with the bodies
// concatenated in that same order.
//
// Order is the whole readability of `k graph get F1 E2 3`: the reader is
// matching objects back to the handles they typed, and a reordered answer is
// one they cannot.
func TestFxcGraphGetOutputOrderMatchesArgumentOrder(t *testing.T) {
	stub := fxcStubServe(t, fxcEchoBody)

	var out bytes.Buffer
	if err := runGraphGet([]string{"F1", "E2", "3"}, &out); err != nil {
		t.Fatalf("runGraphGet: %v", err)
	}

	wantPaths := []string{
		"/api/graph/fact?id=1",
		"/api/graph/episode?id=2",
		"/api/graph/fact?id=3",
	}
	got := stub.paths()
	if len(got) != len(wantPaths) {
		t.Fatalf("stub saw %d requests, want %d: %v", len(got), len(wantPaths), got)
	}
	for i := range wantPaths {
		if got[i] != wantPaths[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], wantPaths[i])
		}
	}

	var want strings.Builder
	for _, p := range wantPaths {
		want.WriteString(fxcEchoBody(p))
		want.WriteString("\n")
	}
	if out.String() != want.String() {
		t.Errorf("output =\n%q\nwant the three bodies in argument order\n%q", out.String(), want.String())
	}
}

// TestFxcGraphGetWritesTheServerBodyVerbatim pins the passthrough rule: the
// body is written "verbatim (never re-encoded)", with a single "\n" appended
// only when it lacks a trailing one.
//
// Never re-encoding is what keeps `k graph get` honest as a debugging tool —
// the bytes on stdout are the bytes the server sent, so a shape question can
// be answered from the verb's output rather than only from the server's code.
// The newline rule is the one byte it is allowed to add, and only to stop two
// bundles running together on one line.
func TestFxcGraphGetWritesTheServerBodyVerbatim(t *testing.T) {
	for _, c := range []struct {
		name string
		body string
		want string
	}{
		{
			// webJSON encodes with json.Encoder, so a real kraken serve body
			// already ends in a newline: nothing is added.
			"body already ends with a newline",
			`{"fact":{"id":7,"fact":"a sentence, kept whole"}}` + "\n",
			`{"fact":{"id":7,"fact":"a sentence, kept whole"}}` + "\n",
		},
		{
			"body lacks a trailing newline",
			`{"fact":{"id":7}}`,
			`{"fact":{"id":7}}` + "\n",
		},
		{
			// Whitespace, key order and unicode all survive: "verbatim" is a
			// promise about bytes, not about equivalent JSON.
			"formatting and unicode survive",
			"{\n  \"fact\": {\n    \"fact\": \"an em dash — and “curly quotes”\"\n  }\n}\n",
			"{\n  \"fact\": {\n    \"fact\": \"an em dash — and “curly quotes”\"\n  }\n}\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			fxcStubServe(t, func(string) string { return c.body })

			var out bytes.Buffer
			if err := runGraphGet([]string{"F7"}, &out); err != nil {
				t.Fatalf("runGraphGet: %v", err)
			}
			if out.String() != c.want {
				t.Errorf("output = %q, want %q", out.String(), c.want)
			}
			if strings.HasSuffix(out.String(), "\n\n") {
				t.Errorf("output ends with a doubled newline: %q", out.String())
			}
		})
	}
}

// TestFxcGraphGetUsageStringIsFrozen pins graphGetUsage verbatim. It is the
// one line a caller who got the handle wrong ever sees, and it has to name all
// three accepted spellings — a usage line that mentioned only F<edge-id> would
// send a reader who has an episode id straight back into the same error.
func TestFxcGraphGetUsageStringIsFrozen(t *testing.T) {
	const want = "usage: k graph get <handle>... (F<edge-id>, bare <edge-id>, or E<episode-id>)"
	if graphGetUsage != want {
		t.Fatalf("graphGetUsage = %q, want %q", graphGetUsage, want)
	}
}
