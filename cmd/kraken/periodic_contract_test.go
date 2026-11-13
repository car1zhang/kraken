package main

// periodic_contract_test.go — athena-logic's BLIND tests for 50-contract.md
// (kraken-periodic-jobs), Slice 4: `kraken periodic add|list|run|pause|resume|rm`.
// Written from the frozen contract alone; cmd/kraken/cmd_periodic.go and the
// working-tree main.go were never opened. Dimension: LOGIC (argument parsing
// that succeeds, and each verb's request and output on a 200). Usage errors
// and refusals are athena-edge's.
//
// Isolation: temp HOME, and KRAKEN_SERVE_ADDR pointed at an httptest server
// that records the request — no test reaches a live serve or opens kraken.db.
//
// Every package-level identifier here starts with periodicContract or
// TestPeriodicContract.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------- parsing

// Contract, parsePeriodicAdd: "Reads <name> --prefix <p> --at <HH:MM> [--days
// <list>] [--file <path>]. Each flag is accepted as --flag value and as
// --flag=value, in any order after the name. ... days and file are empty when
// not given. It validates nothing else: the server is the gate."
func TestPeriodicContractParseAdd(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want periodicAddArgs
	}{
		{[]string{"standup", "--prefix", "standup", "--at", "9:00"},
			periodicAddArgs{name: "standup", prefix: "standup", at: "9:00"}},
		{[]string{"standup", "--prefix=standup", "--at=9:00"},
			periodicAddArgs{name: "standup", prefix: "standup", at: "9:00"}},
		{[]string{"standup", "--at", "09:30", "--days", "mon,wed", "--prefix", "su", "--file", "/tmp/t.md"},
			periodicAddArgs{name: "standup", prefix: "su", at: "09:30", days: "mon,wed", file: "/tmp/t.md"}},
		{[]string{"standup", "--file=/tmp/t.md", "--days=weekdays", "--at=7:05", "--prefix=su"},
			periodicAddArgs{name: "standup", prefix: "su", at: "7:05", days: "weekdays", file: "/tmp/t.md"}},
		{[]string{"standup", "--prefix", "su", "--days=sat,sun", "--at", "23:59"},
			periodicAddArgs{name: "standup", prefix: "su", at: "23:59", days: "sat,sun"}},
		// "validates nothing else": an unparseable time and a non-slug prefix
		// pass through for the server to judge.
		{[]string{"x", "--prefix", "Not A Slug", "--at", "25:99", "--days", "someday"},
			periodicAddArgs{name: "x", prefix: "Not A Slug", at: "25:99", days: "someday"}},
	} {
		got, err := parsePeriodicAdd(tc.args)
		if err != nil {
			t.Errorf("parsePeriodicAdd(%q): %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parsePeriodicAdd(%q) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
}

// Contract, parsePeriodicName: "Exactly one non-blank argument, returned
// trimmed."
func TestPeriodicContractParseName(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"standup"}, "standup"},
		{[]string{"  Standup Notes \t"}, "Standup Notes"},
	} {
		got, err := parsePeriodicName(tc.args, "pause")
		if err != nil {
			t.Errorf("parsePeriodicName(%q): %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parsePeriodicName(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------- the proxy

// periodicContractRequest is what the fake serve saw.
type periodicContractRequest struct {
	method, path string
	body         map[string]any
}

// periodicContractServe starts a fake serve answering every request with
// status 200 and the given body, records each request, and points
// KRAKEN_SERVE_ADDR at it under a temp HOME.
func periodicContractServe(t *testing.T, answer string) *[]periodicContractRequest {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	var mu sync.Mutex
	var seen []periodicContractRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		req := periodicContractRequest{method: r.Method, path: r.URL.Path}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &req.body); err != nil {
				t.Errorf("request body is not a JSON object: %q", raw)
			}
		}
		mu.Lock()
		seen = append(seen, req)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	return &seen
}

// periodicContractCaptureStderr redirects os.Stderr to a pipe while fn runs.
func periodicContractCaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stderr = old }()
		fn()
	}()
	w.Close()
	return <-done
}

// periodicContractOneWrite asserts a single POST /api/periodic was sent
// carrying the given string keys.
func periodicContractOneWrite(t *testing.T, seen []periodicContractRequest, want map[string]string) {
	t.Helper()
	if len(seen) != 1 {
		t.Fatalf("requests = %d, want exactly 1", len(seen))
	}
	r := seen[0]
	if r.method != http.MethodPost || r.path != "/api/periodic" {
		t.Fatalf("request = %s %s, want POST /api/periodic", r.method, r.path)
	}
	for k, v := range want {
		if got, ok := r.body[k].(string); !ok || got != v {
			t.Errorf("body[%q] = %#v, want %q (body %v)", k, r.body[k], v, r.body)
		}
	}
}

// periodicContractOneLine asserts out is exactly one non-empty line.
func periodicContractOneLine(t *testing.T, label, out string) {
	t.Helper()
	trimmed := strings.TrimRight(out, "\n")
	if strings.TrimSpace(trimmed) == "" || strings.Contains(trimmed, "\n") {
		t.Fatalf("%s: stderr = %q, want one confirmation line", label, out)
	}
}

const periodicContractRowAnswer = `{"ok":true,"periodic":{"name":"standup","prefix":"standup","at":"09:00","days":[],"cadence":"daily 09:00","enabled":true,"builtin":false,"template":"t","last_date":"","last_slug":"","last_note":"","last_at":0,"next_due":0,"created_at":1}}` + "\n"

// Contract, Slice 4: "list prints the server's JSON to stdout unchanged", via
// GET /api/periodic ("each a proxy to /api/periodic").
func TestPeriodicContractListVerbatim(t *testing.T) {
	answer := `{"periodic":[{"name":"dream","prefix":"dream","at":"00:00","days":[],"cadence":"daily 00:00","enabled":true,"builtin":true,"template":"","last_date":"2026-11-12","last_slug":"dream-2026-11-12","last_note":"","last_at":0,"next_due":0,"created_at":0}]}` + "\n"
	seen := periodicContractServe(t, answer)
	got := captureStdout(t, func() { cmdPeriodic([]string{"list"}) })
	if got != answer {
		t.Fatalf("stdout = %q, want the server's JSON unchanged %q", got, answer)
	}
	if len(*seen) != 1 || (*seen)[0].method != http.MethodGet || (*seen)[0].path != "/api/periodic" {
		t.Fatalf("requests = %+v, want one GET /api/periodic", *seen)
	}
}

// Contract, Slice 4: pause / resume / run / rm are proxies to POST
// /api/periodic with that op and the name (rm is op remove, the route's only
// delete op); "a write prints one confirmation line to stderr".
func TestPeriodicContractNameVerbs(t *testing.T) {
	for _, tc := range []struct {
		verb, op, answer string
	}{
		{"pause", "pause", periodicContractRowAnswer},
		{"resume", "resume", periodicContractRowAnswer},
		{"run", "run", `{"ok":true,"slug":"standup-2026-11-13-run","started":true,"note":""}` + "\n"},
		{"rm", "remove", `{"ok":true}` + "\n"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			seen := periodicContractServe(t, tc.answer)
			stderr := periodicContractCaptureStderr(t, func() {
				cmdPeriodic([]string{tc.verb, "standup"})
			})
			periodicContractOneWrite(t, *seen, map[string]string{"op": tc.op, "name": "standup"})
			periodicContractOneLine(t, tc.verb, stderr)
		})
	}
}

// Contract, cmdPeriodic: "add reads the template from --file"; the request is
// POST /api/periodic op add carrying name, prefix, at, days and the template.
func TestPeriodicContractAddFromFile(t *testing.T) {
	seen := periodicContractServe(t, periodicContractRowAnswer)
	tmpl := "---\nrepo: kraken\n---\nRun {{slug}} on {{date}}.\n"
	file := filepath.Join(t.TempDir(), "standup.job.md")
	if err := os.WriteFile(file, []byte(tmpl), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := periodicContractCaptureStderr(t, func() {
		cmdPeriodic([]string{"add", "standup", "--prefix", "su", "--at", "09:00", "--days", "mon,fri", "--file", file})
	})
	periodicContractOneWrite(t, *seen, map[string]string{
		"op": "add", "name": "standup", "prefix": "su", "at": "09:00", "days": "mon,fri", "template": tmpl,
	})
	periodicContractOneLine(t, "add", stderr)
}

// Contract, cmdPeriodic: add reads the template "from stdin when no file is
// given".
func TestPeriodicContractAddFromStdin(t *testing.T) {
	seen := periodicContractServe(t, periodicContractRowAnswer)
	tmpl := "---\nrepo: kraken\n---\nFrom stdin, {{slug}}.\n"
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(tmpl); err != nil {
		t.Fatal(err)
	}
	w.Close()
	oldIn := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldIn; r.Close() })

	periodicContractCaptureStderr(t, func() {
		cmdPeriodic([]string{"add", "nightly", "--prefix=nightly", "--at=23:30"})
	})
	periodicContractOneWrite(t, *seen, map[string]string{
		"op": "add", "name": "nightly", "prefix": "nightly", "at": "23:30", "days": "", "template": tmpl,
	})
}
