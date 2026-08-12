package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// graphget_edge_test.go — `k graph get`'s refusals and failure paths
// (50-contract.md, "S3 — CLI verb"), authored blind. The happy path (F123 and
// 123 name the same edge, E456 routes to the episode read, output order
// follows argument order) belongs to the sibling contract suite; what lives
// here is every way the verb can be asked for something it cannot serve, and
// what it must have written to the caller's writer by the time it says so.

// fxeServeStub points KRAKEN_SERVE_ADDR at a test server for one test.
func fxeServeStub(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	return srv
}

// fxeDeadAddr returns an address nothing is listening on: a real listener is
// opened so the port is genuinely allocated, then closed. Pointing
// KRAKEN_SERVE_ADDR here is the "serve is down" case.
func fxeDeadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// Obligation (runGraphGet): "At least one handle required (zero args → usage
// error)." Nothing may be written to the caller's writer, and the process
// must not reach the network at all.
func TestFxeRunGraphGetRequiresAtLeastOneHandle(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", fxeDeadAddr(t)) // any request would fail loudly

	for _, args := range [][]string{nil, {}} {
		var out bytes.Buffer
		err := runGraphGet(args, &out)
		if err == nil {
			t.Errorf("runGraphGet(%v) = nil error, want a usage refusal", args)
		}
		if out.Len() != 0 {
			t.Errorf("runGraphGet(%v) wrote %q before refusing", args, out.String())
		}
	}
}

// Obligation (parseGraphHandle): "Anything else — empty, non-numeric, zero,
// negative, a lone F/E, a float — is an error naming the offending arg."
// Naming it is the load-bearing half: the caller pasted a handle out of an
// injected block, and an error that does not quote it back cannot be acted on.
func TestFxeParseGraphHandleRefusesJunk(t *testing.T) {
	for _, arg := range []string{
		"",                     // empty
		"   ",                  // whitespace only
		"X9",                   // an unknown prefix letter
		"F",                    // a lone prefix
		"E",                    // ... in either case
		"f",                    //
		"e",                    //
		"12.5",                 // a float
		"-3",                   // negative
		"0",                    // zero is not a row id
		"F0",                   // ... prefixed or not
		"E0",                   //
		"F-1",                  // a negative behind a prefix
		"abc",                  // not a number at all
		"1x",                   // trailing junk
		"F1F",                  // a prefix on both ends
		"EE1",                  // a doubled prefix
		"#12",                  // the graph's OTHER handle shape (a missing-node gloss)
		"1 2",                  // two ids in one arg
		"１２３",                  // full-width digits are not ASCII digits
		"99999999999999999999", // int64 overflow
	} {
		_, id, err := parseGraphHandle(arg)
		if err == nil {
			t.Errorf("parseGraphHandle(%q) = nil error, want a usage refusal (id %d)", arg, id)
			continue
		}
		if trimmed := strings.TrimSpace(arg); trimmed != "" && !strings.Contains(err.Error(), trimmed) {
			t.Errorf("parseGraphHandle(%q) error = %q, want it to name the offending arg", arg, err)
		}
	}
}

// Obligation (parseGraphHandle): "Prefixes case-insensitive (f123, e456
// accepted). Whitespace-trimmed." These are the accept side of the same gate:
// a handle copied out of a terminal drags spaces with it.
func TestFxeParseGraphHandleAcceptsCasedAndPaddedForms(t *testing.T) {
	for _, tc := range []struct {
		arg     string
		episode bool
		id      int64
	}{
		{"F123", false, 123},
		{"f123", false, 123},
		{"123", false, 123},
		{"E456", true, 456},
		{"e456", true, 456},
		{" F9 ", false, 9},
		{"\tE7\n", true, 7},
		{"  42  ", false, 42},
	} {
		episode, id, err := parseGraphHandle(tc.arg)
		if err != nil {
			t.Errorf("parseGraphHandle(%q) = %v, want (%v, %d, nil)", tc.arg, err, tc.episode, tc.id)
			continue
		}
		if episode != tc.episode || id != tc.id {
			t.Errorf("parseGraphHandle(%q) = (%v, %d), want (%v, %d)", tc.arg, episode, id, tc.episode, tc.id)
		}
	}
}

// Obligation (graphGetUsage): "The usage line every parse error carries."
// Asserted at the verb's surface, since that is where the sentence has to
// reach stderr — an implementation that attaches it in parseGraphHandle and
// one that wraps in runGraphGet both satisfy this; one that never attaches it
// does not.
func TestFxeRunGraphGetCarriesTheUsageLineOnAParseError(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", fxeDeadAddr(t))

	var out bytes.Buffer
	err := runGraphGet([]string{"X9"}, &out)
	if err == nil {
		t.Fatal("runGraphGet([\"X9\"]) = nil error, want a usage refusal")
	}
	if !strings.Contains(err.Error(), graphGetUsage) {
		t.Errorf("error = %q, want it to carry the usage line %q", err, graphGetUsage)
	}
	if !strings.Contains(err.Error(), "X9") {
		t.Errorf("error = %q, want it to name the offending arg", err)
	}
	if out.Len() != 0 {
		t.Errorf("a refused parse wrote %q to the output writer", out.String())
	}
}

// Obligation (runGraphGet): "Serve-down and timeout errors come from qfGet
// unchanged (they already name the address and exit within qfHTTPTimeout, the
// sibling verbs' 5s bound)." A dereference runs inside an agent's tool turn,
// so a dead serve must fail fast and say WHERE it tried.
func TestFxeRunGraphGetServeDownNamesTheAddressWithinTheTimeout(t *testing.T) {
	addr := fxeDeadAddr(t)
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	var out bytes.Buffer
	start := time.Now()
	err := runGraphGet([]string{"F1"}, &out)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("runGraphGet against a dead serve = nil error")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("error = %q, want it to name the address it tried (%s)", err, addr)
	}
	if elapsed >= qfHTTPTimeout {
		t.Errorf("took %s, want a failure inside the %s bound", elapsed, qfHTTPTimeout)
	}
	if out.Len() != 0 {
		t.Errorf("a failed round trip wrote %q to the output writer", out.String())
	}
}

// Obligation (runGraphGet): "The FIRST failure (usage, 4xx, serve down)
// returns the error — bodies already written stay written." The server's own
// refusal sentence is what surfaces, and the handle AFTER the failing one is
// never requested.
func TestFxeRunGraphGetStopsAtTheFirstServerFailureAndKeepsEarlierBodies(t *testing.T) {
	const firstBody = `{"fact":{"id":1,"fact":"the first fact"}}`
	var calls atomic.Int32
	fxeServeStub(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Write([]byte(firstBody))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"no fact with id 2"}`))
	})

	var out bytes.Buffer
	err := runGraphGet([]string{"F1", "F2", "F3"}, &out)
	if err == nil {
		t.Fatal("a mid-list 404 returned nil error")
	}
	if !strings.Contains(err.Error(), "no fact with id 2") {
		t.Errorf("error = %q, want the server's own sentence", err)
	}
	if got, want := out.String(), firstBody+"\n"; got != want {
		t.Errorf("output = %q, want the first handle's body kept verbatim (%q)", got, want)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("%d requests made, want 2 — the handle after the failing one must never be fetched", n)
	}
}

// Obligation (runGraphGet): "For each arg IN ORDER: parse the handle, qfGet
// ... The FIRST failure (usage, 4xx, serve down) returns the error — bodies
// already written stay written." A usage error in the MIDDLE of the list is
// the same rule: the handles before it were already served, and the ones after
// it are never requested.
func TestFxeRunGraphGetStopsAtAMidListUsageErrorAndKeepsEarlierBodies(t *testing.T) {
	const firstBody = `{"fact":{"id":1}}`
	var calls atomic.Int32
	fxeServeStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(firstBody))
	})

	var out bytes.Buffer
	err := runGraphGet([]string{"F1", "X9", "F3"}, &out)
	if err == nil {
		t.Fatal("a mid-list junk handle returned nil error")
	}
	if !strings.Contains(err.Error(), "X9") {
		t.Errorf("error = %q, want it to name the offending arg", err)
	}
	if got, want := out.String(), firstBody+"\n"; got != want {
		t.Errorf("output = %q, want the already-served body kept (%q)", got, want)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("%d requests made, want 1 — parsing is per-arg in order, so F3 is never reached", n)
	}
}

// Obligation (runGraphGet): "write the server's response body to out verbatim
// (never re-encoded), appending a single "\n" only when the body lacks a
// trailing one." Two newlines make an agent's JSON parser see a blank record;
// none at all runs two bundles together on one line.
func TestFxeRunGraphGetAppendsExactlyOneTrailingNewline(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"body without a trailing newline", `{"fact":{"id":1}}`, `{"fact":{"id":1}}` + "\n"},
		{"body with a trailing newline", `{"fact":{"id":1}}` + "\n", `{"fact":{"id":1}}` + "\n"},
		{"body with two trailing newlines", `{"fact":{"id":1}}` + "\n\n", `{"fact":{"id":1}}` + "\n\n"},
		// CONTRACT SILENCE: a 2xx with an EMPTY body is not described. It
		// lacks a trailing newline, so the stated rule appends one; asserted
		// here as exactly that rather than as "" or as anything tolerant.
		{"empty body", "", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fxeServeStub(t, func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tc.body))
			})
			var out bytes.Buffer
			if err := runGraphGet([]string{"F1"}, &out); err != nil {
				t.Fatalf("runGraphGet: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Errorf("output = %q, want %q", got, tc.want)
			}
		})
	}
}

// Obligation (runGraphGet): "write the server's response body to out verbatim
// (never re-encoded)". A body that is not JSON at all — some other process
// answered the port with a 200 — still goes through untouched rather than
// being parsed, reformatted, or refused.
func TestFxeRunGraphGetWritesANonJSONBodyVerbatim(t *testing.T) {
	const body = "not json at all, just bytes  \t with odd spacing"
	fxeServeStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})

	var out bytes.Buffer
	if err := runGraphGet([]string{"7"}, &out); err != nil {
		t.Fatalf("runGraphGet: %v", err)
	}
	if got, want := out.String(), body+"\n"; got != want {
		t.Errorf("output = %q, want the body byte-for-byte plus one newline (%q)", got, want)
	}
}

// Obligation (runGraphGet): "exactly one HTTP round trip per handle through
// qfGet ... Never opens kraken.db". A repeated handle is still one request
// each — no caching, no dedupe — and the writer carries both bodies.
func TestFxeRunGraphGetMakesOneRoundTripPerHandle(t *testing.T) {
	const body = `{"fact":{"id":1}}`
	var calls atomic.Int32
	fxeServeStub(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(body))
	})

	var out bytes.Buffer
	if err := runGraphGet([]string{"F1", "F1"}, &out); err != nil {
		t.Fatalf("runGraphGet: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("%d requests for two handles, want 2 — one round trip per handle, repeats included", n)
	}
	if got, want := out.String(), body+"\n"+body+"\n"; got != want {
		t.Errorf("output = %q, want both bodies (%q)", got, want)
	}
}
