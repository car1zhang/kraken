package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowServe is an httptest server standing in for kraken serve that answers
// every request after delay — the shape of a submit whose cell start is
// still running when a short client deadline fires. KRAKEN_SERVE_ADDR is
// pointed at it for the test's lifetime.
func slowServe(t *testing.T, delay time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	return srv
}

// The submit is the one proxy verb whose request does real work server-side
// (jobs.SubmitJob starts the cell inside POST /api/queue), so its deadline
// must outlive the family's row-write default — a 5s deadline was measured
// printing the timeout error on three of four successful submits in one
// night. The constant is what keeps that from regressing to the default.
func TestCellSubmitDeadlineOutlivesTheFamilyDefault(t *testing.T) {
	if cellSubmitHTTPTimeout <= qfHTTPTimeout {
		t.Fatalf("cellSubmitHTTPTimeout = %s, want it longer than qfHTTPTimeout (%s): a submit runs the whole cell start inside the request", cellSubmitHTTPTimeout, qfHTTPTimeout)
	}
	if cellSubmitHTTPTimeout < 60*time.Second {
		t.Fatalf("cellSubmitHTTPTimeout = %s, want at least a minute: git fetch plus worktree add on a large repo, the dev-env scan and the 12s trust-prompt poll measured past 5s", cellSubmitHTTPTimeout)
	}
}

// runCellSubmit really runs under cellSubmitHTTPTimeout — not under qfPost's
// default — and its expiry names that deadline and the read that answers
// whether the start landed. The deadline is lowered to 50ms against a 300ms
// server so the wiring is exercised without waiting out the real 90s; a
// version of runCellSubmit that fell back onto qfPost would succeed here
// (300ms is well inside 5s) and fail this test.
func TestRunCellSubmitRunsUnderTheSubmitDeadline(t *testing.T) {
	slowServe(t, 300*time.Millisecond)
	saved := cellSubmitHTTPTimeout
	cellSubmitHTTPTimeout = 50 * time.Millisecond
	t.Cleanup(func() { cellSubmitHTTPTimeout = saved })

	err := runCellSubmit("demo")
	if err == nil {
		t.Fatal("want a timeout error: the submit must run under cellSubmitHTTPTimeout, and 50ms cannot outlast a 300ms server")
	}
	for _, want := range []string{"within 50ms", "kraken board get demo", "may already be starting"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v, must not say unreachable — a timed-out submit may have already started the cell", err)
	}
	// The submit's sentence REPLACES the family's generic one: stacked, the
	// output said "did not respond" twice and still told the reader to
	// re-read with a `get` verb the cell family does not have.
	if n := strings.Count(err.Error(), "did not respond"); n != 1 {
		t.Errorf("err = %v, want exactly one expiry sentence, got %d", err, n)
	}
	if strings.Contains(err.Error(), "re-read with `get`") {
		t.Errorf("err = %v, must not carry the generic `get` advice", err)
	}
	if !qfIsTimeout(err) {
		t.Errorf("err = %v, want the transport timeout still reachable through the wrap", err)
	}
}

// qfPostWithTimeout runs under the deadline it is given, and its expiry
// message names THAT deadline — never qfHTTPTimeout — so a reader of the
// error can tell a 5s row-write timeout from a 90s submit timeout.
func TestQfPostWithTimeoutHonoursTheGivenDeadline(t *testing.T) {
	slowServe(t, 300*time.Millisecond)

	_, err := qfPostWithTimeout("/api/queue", map[string]any{"slug": "demo"}, 50*time.Millisecond)
	if err == nil {
		t.Fatal("want a timeout error under a 50ms deadline against a 300ms server")
	}
	if !strings.Contains(err.Error(), "within 50ms") {
		t.Fatalf("err = %v, want the message to name the 50ms deadline that actually expired", err)
	}
	if !strings.Contains(err.Error(), "re-read") {
		t.Fatalf("err = %v, want the may-have-completed re-read advice kept on the longer deadline too", err)
	}

	body, err := qfPostWithTimeout("/api/queue", map[string]any{"slug": "demo"}, 5*time.Second)
	if err != nil {
		t.Fatalf("under a 5s deadline the same 300ms server must succeed, got %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body = %q, want the server's response verbatim", body)
	}
}

// qfTimeoutError — the family default's expiry — still names qfHTTPTimeout,
// so every other proxy verb's error text is byte-identical to before the
// per-call seam existed.
func TestQfTimeoutErrorNamesTheFamilyDefault(t *testing.T) {
	err := qfTimeoutError(fakeTimeoutNetErr{})
	if !strings.Contains(err.Error(), "within "+qfHTTPTimeout.String()) {
		t.Fatalf("err = %v, want qfTimeoutError to name qfHTTPTimeout (%s)", err, qfHTTPTimeout)
	}
}
