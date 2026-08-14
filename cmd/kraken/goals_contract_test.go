package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// goals_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S1's CLI row: `kraken goals get [slug]`
// (cmdGoals / runGoalsGet).
//
// Added in round 3 to close round 2's disclosed gap #12. Still written from
// the contract text alone: cmd/kraken/cmd_goals.go, main.go's dispatch case
// and `_k` were never read. The harness idioms come from the PRE-EXISTING
// questions-verb tests at the base commit (cmd_questions_test.go), which the
// contract names as this verb's precedent — KRAKEN_SERVE_ADDR + httptest, and
// captureStdout for the "stdout stays parseable" half.
//
// Dimension: LOGIC — the four stated obligations of the row: verbatim body on
// stdout, the ?slug= query param, a non-zero-exit error path that names the
// address when serve is unreachable, and usage on stderr for bad args.
//
// This file declares its OWN subprocess helper rather than reusing
// cmd_questions_test.go's runDispatchSubprocess: that helper switches on a
// fixed set of verb names, and nothing in the contract obliges anyone to have
// added "goals" to it. Depending on that would be asserting an unstated
// requirement about a file I do not own.
//
// Every package-level identifier here is prefixed `contractGoals`.

// contractGoalsServe stands a fake kraken serve up, points KRAKEN_SERVE_ADDR
// at it, and records the path+query each request arrived on.
func contractGoalsServe(t *testing.T, status int, body string) *string {
	t.Helper()
	got := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.URL.Path
		if r.URL.RawQuery != "" {
			*got += "?" + r.URL.RawQuery
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	return got
}

// contractGoalsDeadAddr returns an address nothing is listening on, by
// binding a port and immediately releasing it.
func contractGoalsDeadAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// TestContractGoalsHelperProcess is not a real test — it is the subprocess
// entry point contractGoalsDispatchSubprocess re-execs into, since cmdGoals'
// bad-args branch exits non-zero and would tear down `go test` if called in
// process. Gated on its own env var, so an ordinary run (which executes this
// function like any other Test*) is a silent no-op.
func TestContractGoalsHelperProcess(t *testing.T) {
	if os.Getenv("KRAKEN_CONTRACT_GOALS_DISPATCH") == "" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	cmdGoals(args)
}

// contractGoalsDispatchSubprocess re-execs the test binary to call
// cmdGoals(args) in a real child process, returning its stdout, stderr and
// exit code.
func contractGoalsDispatchSubprocess(t *testing.T, args []string) (stdoutOut, stderrOut string, exitCode int) {
	t.Helper()
	cmdArgs := append([]string{"-test.run=^TestContractGoalsHelperProcess$", "--"}, args...)
	cmd := exec.Command(os.Args[0], cmdArgs...)
	cmd.Env = append(os.Environ(), "KRAKEN_CONTRACT_GOALS_DISPATCH=1")
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err == nil {
		return outBuf.String(), errBuf.String(), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return outBuf.String(), errBuf.String(), exitErr.ExitCode()
	}
	t.Fatalf("helper subprocess: %v", err)
	return "", "", -1
}

// ---------------------------------------------------------------------------
// the happy path
// ---------------------------------------------------------------------------

// TestContractGoalsGetPrintsTheServerBodyVerbatim is the verb's whole job:
// proxy GET /api/goals and put the server's bytes on stdout unmodified. A
// re-marshal, a pretty-print or a summary line would each still "work" for a
// human and break the one caller that matters — an agent piping stdout into a
// JSON parser.
func TestContractGoalsGetPrintsTheServerBodyVerbatim(t *testing.T) {
	const want = `{"goals":{"week":[{"id":1,"scope":"week","period":"2026-08-10","text":"ship it","done":false,"position":1}],"day":[]}}` + "\n"
	gotPath := contractGoalsServe(t, http.StatusOK, want)

	var err error
	out := captureStdout(t, func() { err = runGoalsGet("") })
	if err != nil {
		t.Fatalf("runGoalsGet(\"\"): %v", err)
	}
	if out != want {
		t.Errorf("stdout = %q, want the server body verbatim %q", out, want)
	}
	if *gotPath != "/api/goals" {
		t.Errorf("requested path = %q, want /api/goals with no query", *gotPath)
	}
}

// TestContractGoalsGetSendsTheSlugAsAQueryParam pins the "(with `?slug=` when
// given)" clause — the wire-pinned spelling, not a path segment and not a
// POST body.
func TestContractGoalsGetSendsTheSlugAsAQueryParam(t *testing.T) {
	const want = `{"goals":{"week":[],"day":[]},"job_goal":{"id":7,"text":"the linked goal","done":false}}` + "\n"
	gotPath := contractGoalsServe(t, http.StatusOK, want)

	var err error
	out := captureStdout(t, func() { err = runGoalsGet("demo-job") })
	if err != nil {
		t.Fatalf("runGoalsGet(\"demo-job\"): %v", err)
	}
	if out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
	if *gotPath != "/api/goals?slug=demo-job" {
		t.Errorf("requested path = %q, want /api/goals?slug=demo-job", *gotPath)
	}
}

// TestContractGoalsGetAddsNothingToABodyWithoutATrailingNewline is "verbatim"
// taken literally, isolated into its own case so a failure here is a failure
// about ONE byte rather than about the passthrough as a whole: the verb
// forwards what the server sent, it does not normalise it.
func TestContractGoalsGetAddsNothingToABodyWithoutATrailingNewline(t *testing.T) {
	const want = `{"goals":{"week":[],"day":[]}}`
	contractGoalsServe(t, http.StatusOK, want)

	var err error
	out := captureStdout(t, func() { err = runGoalsGet("") })
	if err != nil {
		t.Fatalf("runGoalsGet: %v", err)
	}
	if out != want {
		t.Errorf("stdout = %q, want the body verbatim %q (no added newline)", out, want)
	}
}

// TestContractCmdGoalsGetDispatchWritesStdout covers cmdGoals itself, not just
// runGoalsGet: the dispatch case has to exist and has to route both arities of
// `kraken goals get [slug]` to the same passthrough.
func TestContractCmdGoalsGetDispatchWritesStdout(t *testing.T) {
	const want = `{"goals":{"week":[],"day":[]}}` + "\n"
	gotPath := contractGoalsServe(t, http.StatusOK, want)

	if out := captureStdout(t, func() { cmdGoals([]string{"get"}) }); out != want {
		t.Errorf("stdout for `goals get` = %q, want %q", out, want)
	}
	if *gotPath != "/api/goals" {
		t.Errorf("requested path = %q, want /api/goals", *gotPath)
	}
	if out := captureStdout(t, func() { cmdGoals([]string{"get", "demo-job"}) }); out != want {
		t.Errorf("stdout for `goals get demo-job` = %q, want %q", out, want)
	}
	if *gotPath != "/api/goals?slug=demo-job" {
		t.Errorf("requested path = %q, want /api/goals?slug=demo-job", *gotPath)
	}
}

// ---------------------------------------------------------------------------
// the error paths
// ---------------------------------------------------------------------------

// TestContractGoalsGetUnreachableServeNamesTheAddress is the questions-verb
// contract the row explicitly inherits: `kraken serve` being down must fail
// loudly with the address that was tried, never print an empty object and
// exit 0. An agent that cannot tell "no goals" from "no daemon" would quietly
// work against a stale picture.
func TestContractGoalsGetUnreachableServeNamesTheAddress(t *testing.T) {
	addr := contractGoalsDeadAddr(t)
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	var err error
	out := captureStdout(t, func() { err = runGoalsGet("") })
	if err == nil {
		t.Fatal("runGoalsGet returned nil with serve unreachable")
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("err = %v, want it to name the address %q it tried", err, addr)
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty — a failed read must not put anything parseable on stdout", out)
	}
}

// TestContractGoalsGetNon200IsAnError: the route answers 404 for an unknown
// slug, and "errors to stderr with non-zero exit" means that has to come back
// as an error rather than as a 404 body printed to stdout as if it were data.
func TestContractGoalsGetNon200IsAnError(t *testing.T) {
	contractGoalsServe(t, http.StatusNotFound, `{"error":"job not found: no-such-job"}`+"\n")

	var err error
	out := captureStdout(t, func() { err = runGoalsGet("no-such-job") })
	if err == nil {
		t.Fatal("runGoalsGet returned nil for a 404")
	}
	if out != "" {
		t.Errorf("stdout = %q, want empty — an error body is not the goals JSON", out)
	}
}

// TestContractCmdGoalsBadArgsPrintUsageToStderrAndExitNonZero covers the row's
// last clause across the three shapes of "bad args": no verb at all, an
// unknown verb, and `get` with more bare arguments than it takes. Driven
// through a real subprocess because the branch exits.
func TestContractCmdGoalsBadArgsPrintUsageToStderrAndExitNonZero(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"get", "one", "two"}} {
		stdoutOut, stderrOut, code := contractGoalsDispatchSubprocess(t, args)
		if code == 0 {
			t.Errorf("args %v: exit 0, want non-zero (stderr=%q)", args, stderrOut)
		}
		if !strings.Contains(strings.ToLower(stderrOut), "usage") {
			t.Errorf("args %v: stderr = %q, want a usage message", args, stderrOut)
		}
		if stdoutOut != "" {
			t.Errorf("args %v: stdout = %q, want empty — usage goes to stderr", args, stdoutOut)
		}
	}
}
