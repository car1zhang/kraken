package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// cmd_feedback_test.go — `kraken feedback get|addressed`'s testable core
// (runFeedbackGet/runFeedbackAddressed), same contract coverage as
// cmd_questions_test.go: get happy path prints server JSON verbatim;
// addressed posts the right op shape from either trailing args or stdin; a
// 400 surfaces the server's message; connection-refused names the address;
// missing args produce a usage error.

func TestRunFeedbackGetPrintsServerJSONVerbatim(t *testing.T) {
	want := `{"feedback":[{"id":1,"artifact":"00-overview.md","note":"typo","at":1234}]}` + "\n"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runFeedbackGet([]string{"demo-job"})
	if err != nil {
		t.Fatalf("runFeedbackGet: %v", err)
	}
	if string(got) != want {
		t.Fatalf("body = %q, want %q (verbatim passthrough)", got, want)
	}
	if gotPath != "/api/feedback?slug=demo-job" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestRunFeedbackGetMissingSlugIsUsageError(t *testing.T) {
	if _, err := runFeedbackGet(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

func TestRunFeedbackAddressedFromArgs(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/feedback" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	if err := runFeedbackAddressed([]string{"demo-job", "7", "fixed", "the", "typo"}, strings.NewReader("")); err != nil {
		t.Fatalf("runFeedbackAddressed: %v", err)
	}
	if gotBody["slug"] != "demo-job" || gotBody["id"] != float64(7) || gotBody["addressed"] != "fixed the typo" {
		t.Fatalf("body = %v", gotBody)
	}
}

func TestRunFeedbackAddressedFromStdinWhenTextOmitted(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	stdin := strings.NewReader("  addressed via stdin, full prose with \"quotes\" andünïcode  \n")
	if err := runFeedbackAddressed([]string{"demo-job", "7"}, stdin); err != nil {
		t.Fatalf("runFeedbackAddressed: %v", err)
	}
	if gotBody["addressed"] != `addressed via stdin, full prose with "quotes" andünïcode` {
		t.Fatalf("addressed = %v", gotBody["addressed"])
	}
}

// TestRunFeedbackAddressedClearPostsEmptyAddressed pins the --clear form
// (non-blocking#5): ios/API.md documents {id, addressed:""} as the way to
// clear an addressed note and reopen the entry, but an empty text is
// otherwise a usage error here — so without an explicit flag that documented
// form is unreachable from zeus' only surface. --clear is positional-
// agnostic and must post an explicitly EMPTY addressed (the key present with
// an empty value — the API distinguishes that from the key being absent,
// which routes to the edit form instead).
func TestRunFeedbackAddressedClearPostsEmptyAddressed(t *testing.T) {
	for _, args := range [][]string{
		{"--clear", "demo-job", "7"},
		{"demo-job", "7", "--clear"},
	} {
		var gotBody map[string]any
		var sawAddressedKey bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var raw map[string]json.RawMessage
			json.NewDecoder(r.Body).Decode(&raw)
			_, sawAddressedKey = raw["addressed"]
			gotBody = map[string]any{}
			for k, v := range raw {
				var val any
				json.Unmarshal(v, &val)
				gotBody[k] = val
			}
			w.Write([]byte(`{"ok":true}`))
		}))
		t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

		if err := runFeedbackAddressed(args, strings.NewReader("")); err != nil {
			srv.Close()
			t.Fatalf("args %v: runFeedbackAddressed: %v", args, err)
		}
		srv.Close()
		if !sawAddressedKey {
			t.Fatalf("args %v: no addressed key on the wire — the clear form needs the key PRESENT, body %v", args, gotBody)
		}
		if gotBody["slug"] != "demo-job" || gotBody["id"] != float64(7) || gotBody["addressed"] != "" {
			t.Fatalf("args %v: body = %v, want {slug, id, addressed:\"\"}", args, gotBody)
		}
	}
}

// --clear says "reopen this entry" and trailing text says "mark it addressed
// with this text" — the two contradict, so the request is refused rather
// than silently preferring one.
func TestRunFeedbackAddressedClearWithTextIsError(t *testing.T) {
	err := runFeedbackAddressed([]string{"--clear", "demo-job", "7", "some", "text"}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

func TestRunFeedbackAddressedBadArgs(t *testing.T) {
	cases := [][]string{
		nil,
		{"demo-job"},
		{"demo-job", "not-a-number"},
	}
	for _, args := range cases {
		if err := runFeedbackAddressed(args, strings.NewReader("text")); err == nil {
			t.Fatalf("args %v: want an error", args)
		}
	}
}

func TestRunFeedbackAddressedEmptyTextIsError(t *testing.T) {
	// no trailing args, and stdin is blank: neither source supplies text.
	if err := runFeedbackAddressed([]string{"demo-job", "7"}, strings.NewReader("   \n")); err == nil {
		t.Fatal("want an error for empty addressed text")
	}
}

func TestFeedback400SurfacesServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"unknown feedback"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	err := runFeedbackAddressed([]string{"demo-job", "999", "done"}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "unknown feedback") {
		t.Fatalf("err = %v, want the server's own message surfaced verbatim", err)
	}
}

// TestCmdFeedbackGetDispatchWritesStdout mirrors
// TestCmdQuestionsGetDispatchWritesStdout for the feedback verb's dispatch
// layer.
func TestCmdFeedbackGetDispatchWritesStdout(t *testing.T) {
	want := `{"feedback":[]}` + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got := captureStdout(t, func() { cmdFeedback([]string{"get", "demo-job"}) })
	if got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestFeedbackConnectionRefusedNamesAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	t.Setenv("KRAKEN_SERVE_ADDR", addr)
	_, err = runFeedbackGet([]string{"demo-job"})
	if err == nil {
		t.Fatal("want an error when serve is unreachable")
	}
	if !strings.Contains(err.Error(), addr) || !strings.Contains(err.Error(), "kraken serve") {
		t.Fatalf("err = %v, want it to name %q and mention kraken serve", err, addr)
	}
}

// TestRunFeedbackAddressedRejectsWhitespaceSlug covers F10: unlike
// runFeedbackGet, runFeedbackAddressed never validated its slug at all
// before this fix — a whitespace-only slug would make a real HTTP call for
// an empty/mangled slug.
func TestRunFeedbackAddressedRejectsWhitespaceSlug(t *testing.T) {
	if err := runFeedbackAddressed([]string{"   ", "7", "done"}, strings.NewReader("")); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error for a whitespace-only slug", err)
	}
}

// TestCmdFeedbackAddressedDispatchLeavesStdoutEmpty mirrors
// TestCmdQuestionsRemoveDispatchLeavesStdoutEmpty for the feedback verb.
func TestCmdFeedbackAddressedDispatchLeavesStdoutEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got := captureStdout(t, func() { cmdFeedback([]string{"addressed", "demo-job", "7", "done"}) })
	if got != "" {
		t.Fatalf("stdout = %q, want empty for a write op", got)
	}
}

// TestCmdFeedbackNoArgsDispatchDiesNonZero and
// TestCmdFeedbackUnknownOpDispatchDiesNonZero cover cmdFeedback's two
// util.Die branches, using the subprocess helper defined in
// cmd_questions_test.go (same package).
func TestCmdFeedbackNoArgsDispatchDiesNonZero(t *testing.T) {
	stderrOut, code := runDispatchSubprocess(t, "feedback", nil)
	if code == 0 {
		t.Fatalf("want a non-zero exit, got 0 (stderr=%q)", stderrOut)
	}
	if !strings.Contains(stderrOut, "usage: k feedback") {
		t.Fatalf("stderr = %q, want a usage message naming the `k feedback` verbs", stderrOut)
	}
}

func TestCmdFeedbackUnknownOpDispatchDiesNonZero(t *testing.T) {
	stderrOut, code := runDispatchSubprocess(t, "feedback", []string{"bogus", "demo-job"})
	if code == 0 {
		t.Fatalf("want a non-zero exit, got 0 (stderr=%q)", stderrOut)
	}
	if !strings.Contains(stderrOut, `"bogus"`) {
		t.Fatalf("stderr = %q, want it to name the unknown op", stderrOut)
	}
}

// TestRunFeedbackAddressedRefusesInteractiveTTY covers F6 for the one
// stdin-reading path in cmd_feedback.go — only reached when text is
// omitted from the trailing args.
func TestRunFeedbackAddressedRefusesInteractiveTTY(t *testing.T) {
	err := runFeedbackAddressed([]string{"demo-job", "7"}, fakeTTYFile{strings.NewReader("")})
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error refusing to block on a TTY", err)
	}
}
