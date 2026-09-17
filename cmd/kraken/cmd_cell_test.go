package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
		t.Fatalf("cellSubmitHTTPTimeout = %s, want at least a minute: git worktree add on a large repo plus the dev-env scan and tmux spawn measured past 5s", cellSubmitHTTPTimeout)
	}
}

// qfPostWithTimeout runs under the deadline it is given, and its expiry
// message names THAT deadline — never qfHTTPTimeout — so a reader of the
// error can tell a 5s row-write timeout from a 90s submit timeout.
func TestQfPostWithTimeoutHonoursTheGivenDeadline(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer slow.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(slow.URL, "http://"))

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

// qfPost itself is unchanged: it is qfPostWithTimeout under qfHTTPTimeout,
// and its expiry message still names qfHTTPTimeout.
func TestQfPostKeepsTheFamilyDefaultDeadline(t *testing.T) {
	err := qfTimeoutError(fakeTimeoutNetErr{})
	if !strings.Contains(err.Error(), "within "+qfHTTPTimeout.String()) {
		t.Fatalf("err = %v, want qfTimeoutError to name qfHTTPTimeout (%s)", err, qfHTTPTimeout)
	}
}
