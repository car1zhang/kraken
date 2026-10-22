package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cmd_ctx_post_test.go — what `kraken ctx-hook --slug` POSTs to
// /api/cell/context: the exact `total_input_tokens` count rides beside the
// percentage, and a null percentage still means no POST at all. Lead-authored
// (direct job, no blind authorship — see 90-build-report.md of job 630).

// ctxPostCapture runs runCtxHook against a capturing server and returns the
// decoded body of the one POST it made, or nil when none arrived.
func ctxPostCapture(t *testing.T, stdin string) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/cell/context" {
			t.Errorf("POST to %s, want /api/cell/context", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("body %q: %v", raw, err)
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	var out bytes.Buffer
	runCtxHook(strings.NewReader(stdin), &out, "ctx-job", "", "")
	if !strings.HasPrefix(out.String(), "ctx-job") {
		t.Errorf("printed %q, want the slug-prefixed line whatever the POST did", out.String())
	}
	return got
}

func TestCtxHookPostCarriesTotalInputTokens(t *testing.T) {
	got := ctxPostCapture(t, `{"session_id":"db31a058","context_window":{"used_percentage":25,"context_window_size":1000000,"total_input_tokens":242390}}`)
	if got == nil {
		t.Fatal("no POST arrived for a payload with a percentage")
	}
	if got["slug"] != "ctx-job" || got["session_id"] != "db31a058" {
		t.Errorf("body = %v, want slug ctx-job and session db31a058", got)
	}
	if got["used_percentage"] != float64(25) || got["context_window_size"] != float64(1_000_000) {
		t.Errorf("body = %v, want used_percentage 25 and context_window_size 1000000", got)
	}
	if got["total_input_tokens"] != float64(242_390) {
		t.Errorf("total_input_tokens = %v, want 242390", got["total_input_tokens"])
	}
}

// A payload with no count (an older Claude Code) posts 0 for it — the hook
// never fabricates a number, and the server reads 0 as "not reported".
func TestCtxHookPostWithoutCountSendsZero(t *testing.T) {
	got := ctxPostCapture(t, `{"session_id":"s","context_window":{"used_percentage":40,"context_window_size":200000}}`)
	if got == nil {
		t.Fatal("no POST arrived")
	}
	if got["total_input_tokens"] != float64(0) {
		t.Errorf("total_input_tokens = %v, want 0 when the payload carries none", got["total_input_tokens"])
	}
}

// A null percentage is "fullness not yet known": no POST, whatever the count
// field says — a count with no percentage would be a report the server
// refuses anyway.
func TestCtxHookNullPercentageNeverPosts(t *testing.T) {
	got := ctxPostCapture(t, `{"session_id":"s","context_window":{"used_percentage":null,"context_window_size":1000000,"total_input_tokens":12}}`)
	if got != nil {
		t.Fatalf("a POST arrived for a null percentage: %v", got)
	}
}
