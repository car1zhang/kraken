package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// cmd_cell_submit_test.go — `kraken cell submit` outlives the synchronous
// start it asks for. POST /api/queue starts the whole cell inside the request
// and a measured start runs longer than the proxy family's 5 s deadline, so
// the submit carries its own: a serve that answers after qfHTTPTimeout has
// passed is still a success, not the "may have completed server-side" error
// the caller cannot act on.

func TestCellSubmitOutlivesTheProxyDeadline(t *testing.T) {
	if cellSubmitHTTPTimeout <= qfHTTPTimeout {
		t.Fatalf("cellSubmitHTTPTimeout %s must exceed qfHTTPTimeout %s", cellSubmitHTTPTimeout, qfHTTPTimeout)
	}
	delay := qfHTTPTimeout + 500*time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		if r.URL.Path == "/api/queue" {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"slug":"child","mtime":1}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	t.Setenv("KRAKEN_JOB_SLUG", "")

	start := time.Now()
	if err := runCellSubmit("child"); err != nil {
		t.Fatalf("runCellSubmit after a %s start: %v", delay, err)
	}
	if took := time.Since(start); took < delay {
		t.Fatalf("submit returned after %s, before the %s start finished", took, delay)
	}
}

// A submit that does time out names the deadline that fired, not the proxy
// family's: a caller who waited 90 s must not read "did not respond within 5s".
func TestCellSubmitTimeoutNamesItsOwnDeadline(t *testing.T) {
	err := qfTimeoutError(fakeTimeoutNetErr{}, cellSubmitHTTPTimeout)
	if !strings.Contains(err.Error(), cellSubmitHTTPTimeout.String()) || strings.Contains(err.Error(), "within "+qfHTTPTimeout.String()) {
		t.Fatalf("submit timeout error names the wrong deadline: %v", err)
	}
}
