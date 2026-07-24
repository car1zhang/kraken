package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// captureStdout redirects os.Stdout to a pipe for the duration of fn,
// returning everything written to it — used to assert cmdQuestions/
// cmdFeedback's dispatch layer (not just the run* core funcs) writes GET
// output to stdout, exactly as the "stdout stays parseable" contract
// requires. Shared by cmd_questions_test.go and cmd_feedback_test.go (same
// package, one definition).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// cmd_questions_test.go — `kraken questions get|set|add|update|remove`'s
// testable core (runQuestionsGet/Set/Upsert/Remove), covering the behavior
// contract: get happy path prints server JSON verbatim; set reads stdin and
// POSTs the right body; add/update/remove hit the right op shapes; a 400
// surfaces the server's own message; connection-refused names the address;
// missing args produce a usage error. cmdQuestions' happy-path dispatch (get
// writes stdout, a write leaves stdout empty) is tested in-process below;
// its util.Die branches (no-args, unknown op) call os.Exit(1), which is only
// testable by re-executing the test binary as a subprocess — see
// TestHelperProcess/runDispatchSubprocess, shared with cmd_feedback_test.go.

// TestHelperProcess is not a real test — it's the subprocess entry point
// runDispatchSubprocess re-execs into, since cmdQuestions/cmdFeedback's
// no-args and unknown-op branches call util.Die (os.Exit(1)), which would
// tear down the real `go test` process if called in-process. Gated on
// KRAKEN_HELPER_DISPATCH so a normal test run — which does execute this
// function like any other Test* — is a silent no-op.
func TestHelperProcess(t *testing.T) {
	which := os.Getenv("KRAKEN_HELPER_DISPATCH")
	if which == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	switch which {
	case "questions":
		cmdQuestions(args)
	case "feedback":
		cmdFeedback(args)
	}
}

// runDispatchSubprocess re-execs the test binary to call cmdQuestions(args)
// (which=="questions") or cmdFeedback(args) (which=="feedback") in a real
// child process, returning its stderr and exit code.
func runDispatchSubprocess(t *testing.T, which string, args []string) (stderrOut string, exitCode int) {
	t.Helper()
	cmdArgs := append([]string{"-test.run=^TestHelperProcess$", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "KRAKEN_HELPER_DISPATCH="+which)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return errBuf.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return errBuf.String(), exitErr.ExitCode()
	}
	t.Fatalf("helper subprocess: %v", err)
	return "", -1
}

func TestRunQuestionsGetPrintsServerJSONVerbatim(t *testing.T) {
	want := `{"questions":[{"id":"Q1","text":"why?","importance":"P1"}]}` + "\n"
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path + "?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got, err := runQuestionsGet([]string{"demo-job"})
	if err != nil {
		t.Fatalf("runQuestionsGet: %v", err)
	}
	if string(got) != want {
		t.Fatalf("body = %q, want %q (verbatim passthrough)", got, want)
	}
	if gotPath != "/api/questions?slug=demo-job" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestRunQuestionsGetMissingSlugIsUsageError(t *testing.T) {
	if _, err := runQuestionsGet(nil); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if _, err := runQuestionsGet([]string{"a", "b"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("extra arg: err = %v, want a usage error", err)
	}
}

func TestRunQuestionsSetPostsStdinBody(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/questions" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	stdin := strings.NewReader(`[{"id":"Q1","text":"why?","importance":"P1"}]`)
	if err := runQuestionsSet([]string{"demo-job"}, stdin); err != nil {
		t.Fatalf("runQuestionsSet: %v", err)
	}
	if gotBody["slug"] != "demo-job" {
		t.Fatalf("slug = %v", gotBody["slug"])
	}
	qs, ok := gotBody["questions"].([]any)
	if !ok || len(qs) != 1 {
		t.Fatalf("questions = %v", gotBody["questions"])
	}
	q := qs[0].(map[string]any)
	if q["id"] != "Q1" || q["text"] != "why?" || q["importance"] != "P1" {
		t.Fatalf("question = %v", q)
	}
}

func TestRunQuestionsSetRejectsMalformedStdin(t *testing.T) {
	if err := runQuestionsSet([]string{"demo-job"}, strings.NewReader("not json")); err == nil {
		t.Fatal("want an error for malformed stdin")
	}
	if err := runQuestionsSet([]string{"demo-job"}, strings.NewReader(`{"not":"an array"}`)); err == nil {
		t.Fatal("want an error for a non-array stdin body")
	}
}

func TestRunQuestionsUpsertPostsOpAndQuestion(t *testing.T) {
	for _, op := range []string{"add", "update"} {
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&gotBody)
			w.Write([]byte(`{"ok":true}`))
		}))
		t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

		stdin := strings.NewReader(`{"id":"Q2","text":"how?","importance":"P0"}`)
		if err := runQuestionsUpsert(op, []string{"demo-job"}, stdin); err != nil {
			srv.Close()
			t.Fatalf("op %s: runQuestionsUpsert: %v", op, err)
		}
		srv.Close()

		if gotBody["slug"] != "demo-job" || gotBody["op"] != op {
			t.Fatalf("op %s: body = %v", op, gotBody)
		}
		q, ok := gotBody["question"].(map[string]any)
		if !ok || q["id"] != "Q2" || q["text"] != "how?" {
			t.Fatalf("op %s: question = %v", op, gotBody["question"])
		}
	}
}

func TestRunQuestionsRemovePostsQID(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	if err := runQuestionsRemove([]string{"demo-job", "Q3"}); err != nil {
		t.Fatalf("runQuestionsRemove: %v", err)
	}
	if gotBody["slug"] != "demo-job" || gotBody["op"] != "remove" || gotBody["qid"] != "Q3" {
		t.Fatalf("body = %v", gotBody)
	}
}

func TestRunQuestionsRemoveMissingArgsIsUsageError(t *testing.T) {
	cases := [][]string{nil, {"demo-job"}, {"demo-job", ""}, {"demo-job", "Q1", "extra"}}
	for _, args := range cases {
		if err := runQuestionsRemove(args); err == nil || !strings.Contains(err.Error(), "usage:") {
			t.Fatalf("args %v: err = %v, want a usage error", args, err)
		}
	}
}

// TestQuestions400SurfacesServerMessage covers every write path's 400
// contract: the server's own {"error": "..."} message comes back verbatim
// (via qfServerError), not a raw HTTP dump — the whole point of these verbs
// versus zeus hand-rolling curl.
func TestQuestions400SurfacesServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad question id \"Qx\": want Q<digits>"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	err := runQuestionsUpsert("add", []string{"demo-job"}, strings.NewReader(`{"id":"Qx"}`))
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "bad question id") {
		t.Fatalf("err = %v, want the server's own message surfaced verbatim", err)
	}
	if strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "HTTP") {
		t.Fatalf("err = %v, must not be a raw HTTP status dump when the server gave a message", err)
	}
}

// TestQuestionsConnectionRefusedNamesAddress: kraken serve down must fail
// loudly with the address it tried, never silently succeed.
func TestQuestionsConnectionRefusedNamesAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // now guaranteed nobody is listening there

	t.Setenv("KRAKEN_SERVE_ADDR", addr)
	_, err = runQuestionsGet([]string{"demo-job"})
	if err == nil {
		t.Fatal("want an error when serve is unreachable")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Fatalf("err = %v, want it to name the address %q it tried", err, addr)
	}
	if !strings.Contains(err.Error(), "kraken serve") {
		t.Fatalf("err = %v, want it to mention kraken serve must be running", err)
	}
}

// TestQfPostConnectionRefused exercises qfPost's (not just qfGet's) same
// unreachable-address contract, since runQuestionsSet/Upsert/Remove all
// route through it.
func TestQfPostConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	if err := runQuestionsRemove([]string{"demo-job", "Q1"}); err == nil || !strings.Contains(err.Error(), addr) {
		t.Fatalf("err = %v, want it to name %q", err, addr)
	}
}

func TestQfDecodeErrorFallsBackToStatusLine(t *testing.T) {
	err := qfDecodeError(http.StatusInternalServerError, []byte("not json"))
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want a status-line fallback mentioning 500", err)
	}
}

func TestQfPostRejectsUnmarshalableBody(t *testing.T) {
	if _, err := qfPost("/api/questions", make(chan int)); err == nil {
		t.Fatal("want an error encoding an unmarshalable body")
	}
}

// TestCmdQuestionsGetDispatchWritesStdout covers cmdQuestions itself (not
// just runQuestionsGet): the "get" op's dispatch case must write the
// response to stdout unmodified, since a caller piping the command's output
// depends on stdout carrying nothing but the JSON.
func TestCmdQuestionsGetDispatchWritesStdout(t *testing.T) {
	want := `{"questions":[]}` + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(want))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got := captureStdout(t, func() { cmdQuestions([]string{"get", "demo-job"}) })
	if got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

// TestCmdQuestionsRemoveDispatchLeavesStdoutEmpty covers the write-op half
// of the "stdout stays parseable" contract: a write's confirmation goes to
// stderr only, so a caller piping stdout never sees it mixed in.
func TestCmdQuestionsRemoveDispatchLeavesStdoutEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())

	got := captureStdout(t, func() { cmdQuestions([]string{"remove", "demo-job", "Q1"}) })
	if got != "" {
		t.Fatalf("stdout = %q, want empty for a write op", got)
	}
}

// TestCmdQuestionsNoArgsDispatchDiesNonZero and
// TestCmdQuestionsUnknownOpDispatchDiesNonZero cover cmdQuestions' two
// util.Die branches via the subprocess helper above — no kraken serve is
// ever contacted since both branches die before any HTTP call.
func TestCmdQuestionsNoArgsDispatchDiesNonZero(t *testing.T) {
	stderrOut, code := runDispatchSubprocess(t, "questions", nil)
	if code == 0 {
		t.Fatalf("want a non-zero exit, got 0 (stderr=%q)", stderrOut)
	}
	if !strings.Contains(stderrOut, "usage: k questions") {
		t.Fatalf("stderr = %q, want a usage message naming the `k questions` verbs", stderrOut)
	}
}

func TestCmdQuestionsUnknownOpDispatchDiesNonZero(t *testing.T) {
	stderrOut, code := runDispatchSubprocess(t, "questions", []string{"bogus", "demo-job"})
	if code == 0 {
		t.Fatalf("want a non-zero exit, got 0 (stderr=%q)", stderrOut)
	}
	if !strings.Contains(stderrOut, `"bogus"`) {
		t.Fatalf("stderr = %q, want it to name the unknown op", stderrOut)
	}
}

// TestRunQuestionsSetRejectsEmptyStdin and
// TestRunQuestionsSetRejectsNullStdin cover F8: bare `null` unmarshals into
// a nil slice without an error, so the "is it valid JSON" probe alone lets
// a pointless round trip through — a nil probe must be rejected explicitly,
// same as a truly empty/malformed body.
func TestRunQuestionsSetRejectsEmptyStdin(t *testing.T) {
	if err := runQuestionsSet([]string{"demo-job"}, strings.NewReader("")); err == nil {
		t.Fatal("want an error for empty stdin")
	}
}

func TestRunQuestionsSetRejectsNullStdin(t *testing.T) {
	err := runQuestionsSet([]string{"demo-job"}, strings.NewReader("null"))
	if err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("err = %v, want an error naming null stdin", err)
	}
}

func TestRunQuestionsUpsertRejectsNullStdin(t *testing.T) {
	err := runQuestionsUpsert("add", []string{"demo-job"}, strings.NewReader("null"))
	if err == nil || !strings.Contains(err.Error(), "null") {
		t.Fatalf("err = %v, want an error naming null stdin", err)
	}
}

// TestQfPostRejectsOversizedBody covers F7: an oversized body must fail
// locally, before any network call, naming the size limit rather than
// round-tripping to get back a generic "bad json" a caller can't act on.
// No server is started — qfPost's size check runs before dialing out.
func TestQfPostRejectsOversizedBody(t *testing.T) {
	huge := strings.Repeat("a", qfMaxBodyBytes+1)
	_, err := qfPost("/api/questions", map[string]string{"slug": huge})
	if err == nil {
		t.Fatal("want an error for an oversized body")
	}
	if strings.Contains(err.Error(), "bad json") {
		t.Fatalf("err = %v, must name the size limit, not look like a generic malformed-body error", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(qfMaxBodyBytes)) {
		t.Fatalf("err = %v, want it to name the byte limit", err)
	}
}

// fakeTTYFile fakes an interactive-terminal stdin cheaply for
// stdinIsInteractiveTTY, without opening a real pty: it satisfies qfStatter
// (F6) and reports a char-device Mode(), same shape *os.File reports when
// os.Stdin is a real terminal.
type fakeTTYFile struct{ *strings.Reader }

func (fakeTTYFile) Stat() (os.FileInfo, error) { return fakeTTYInfo{}, nil }

type fakeTTYInfo struct{}

func (fakeTTYInfo) Name() string       { return "fake-tty" }
func (fakeTTYInfo) Size() int64        { return 0 }
func (fakeTTYInfo) Mode() os.FileMode  { return os.ModeCharDevice }
func (fakeTTYInfo) ModTime() time.Time { return time.Time{} }
func (fakeTTYInfo) IsDir() bool        { return false }
func (fakeTTYInfo) Sys() any           { return nil }

func TestStdinIsInteractiveTTY(t *testing.T) {
	if stdinIsInteractiveTTY(strings.NewReader("x")) {
		t.Fatal("a plain strings.Reader (no Stat method) must never read as an interactive TTY")
	}
	if !stdinIsInteractiveTTY(fakeTTYFile{strings.NewReader("")}) {
		t.Fatal("a fake char-device Stat() must read as an interactive TTY")
	}
}

// TestStdinIsInteractiveTTYDevNullNotInteractive covers non-blocking#3:
// /dev/null is itself a character device (same Mode() bit a real pty
// reports), so `k questions set slug < /dev/null` must NOT be misreported
// as an interactive terminal — it must fall through to the empty-stdin
// path instead of refusing with "refusing to wait on an interactive
// terminal".
func TestStdinIsInteractiveTTYDevNullNotInteractive(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer f.Close()
	if stdinIsInteractiveTTY(f) {
		t.Fatal("/dev/null must not read as an interactive TTY")
	}
}

func TestRunQuestionsSetRefusesInteractiveTTY(t *testing.T) {
	err := runQuestionsSet([]string{"demo-job"}, fakeTTYFile{strings.NewReader("")})
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error refusing to block on a TTY", err)
	}
}

func TestRunQuestionsUpsertRefusesInteractiveTTY(t *testing.T) {
	err := runQuestionsUpsert("add", []string{"demo-job"}, fakeTTYFile{strings.NewReader("")})
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("err = %v, want a usage error refusing to block on a TTY", err)
	}
}

// fakeTimeoutNetErr is a minimal net.Error whose Timeout() reports true —
// used to test qfIsTimeout/qfTimeoutError (F4) without waiting out a real
// qfHTTPTimeout.
type fakeTimeoutNetErr struct{}

func (fakeTimeoutNetErr) Error() string   { return "fake: i/o timeout" }
func (fakeTimeoutNetErr) Timeout() bool   { return true }
func (fakeTimeoutNetErr) Temporary() bool { return true }

func TestQfIsTimeoutDetectsWrappedNetTimeout(t *testing.T) {
	wrapped := fmt.Errorf("Get %q: %w", "http://127.0.0.1:1/x", fakeTimeoutNetErr{})
	if !qfIsTimeout(wrapped) {
		t.Fatal("want a wrapped net.Error with Timeout()==true detected as a timeout")
	}
	if qfIsTimeout(errors.New("connection refused")) {
		t.Fatal("a plain non-net error must never read as a timeout")
	}
}

func TestQfTimeoutErrorMessage(t *testing.T) {
	err := qfTimeoutError(fakeTimeoutNetErr{})
	if strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v, must not say unreachable — a timed-out write may have already landed", err)
	}
	if !strings.Contains(err.Error(), "re-read") {
		t.Fatalf("err = %v, want it to suggest re-reading before retrying rather than assuming failure", err)
	}
}
