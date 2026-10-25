package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mindpass_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 639, the mind pass), the CLI rows: `kraken mine
// operator`, `kraken mine around`, `kraken mm evidence`, `kraken mm propose`
// and `kraken mm confirm`.
//
// Written from the frozen contract alone; cmd/kraken's working tree was never
// read. The contract freezes the COMMAND LINES, not the run functions behind
// them, so every test drives the real dispatchers (`cmdMine`, `cmdMM`, both
// committed) in a re-exec'd child of the test binary — the committed
// TestHelperProcess pattern, with its own entry point here so stdout and stdin
// are reachable too. Serve calls go to an httptest server via
// KRAKEN_SERVE_ADDR, as the committed CLI tests do.
//
// Dimension: LOGIC — happy paths and request shapes. Usage errors and the
// serve-down arm are athena-edge's. Every package-level identifier here is
// prefixed `mpc`.

// TestMpcHelperProcess is the child entry point (a no-op in a normal run).
func TestMpcHelperProcess(t *testing.T) {
	which := os.Getenv("MPC_HELPER_DISPATCH")
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
	case "mine":
		cmdMine(args)
	case "mm":
		cmdMM(args)
	}
	os.Exit(0)
}

// mpcRunCLI runs `kraken <which> <args…>` in a child, returning its stdout,
// stderr and exit code.
func mpcRunCLI(t *testing.T, which string, args []string, env []string, stdin string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestMpcHelperProcess$", "--"}, args...)...)
	cmd.Env = append(append(os.Environ(), "MPC_HELPER_DISPATCH="+which), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("child: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), errb.String(), code
}

func mpcISO(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

func mpcLine(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mpcUserRec(t *testing.T, uuid, sess string, ts int64, content string, extra map[string]any) string {
	t.Helper()
	m := map[string]any{
		"type": "user", "message": map[string]any{"role": "user", "content": content},
		"uuid": uuid, "timestamp": mpcISO(ts), "origin": map[string]any{"kind": "human"},
		"promptSource": "typed", "sessionId": sess,
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return mpcLine(t, m)
}

func mpcAsstRec(t *testing.T, ts int64, text string) string {
	t.Helper()
	return mpcLine(t, map[string]any{
		"type": "assistant", "timestamp": mpcISO(ts),
		"message": map[string]any{"model": "claude-opus-5", "content": []any{map[string]any{"type": "text", "text": text}}},
	})
}

// mpcTranscript writes lines to <home>/.claude/projects/<project>/<name>.
func mpcTranscript(t *testing.T, home, project, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(home, ".claude", "projects", project, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type mpcOperatorOut struct {
	Since     int64 `json:"since"`
	Until     int64 `json:"until"`
	Scanned   int   `json:"scanned"`
	Skipped   int   `json:"skipped"`
	Truncated int   `json:"truncated"`
	Records   []struct {
		UUID    string `json:"uuid"`
		Form    string `json:"form"`
		Text    string `json:"text"`
		Context struct {
			Before []struct{ Label, Text string } `json:"before"`
			After  []struct{ Label, Text string } `json:"after"`
		} `json:"context"`
	} `json:"records"`
}

// mpcOperatorFixture: one root transcript holding a typed prompt with eight
// assistant blocks before it and three after, then a remote question stamped
// with the operator's login.
func mpcOperatorFixture(t *testing.T) (home string, since int64, at func(int64) int64) {
	t.Helper()
	home = t.TempDir()
	now := time.Now().UnixMilli()
	base := now - 30*60_000
	at = func(n int64) int64 { return base + n*1000 }
	lines := []string{}
	for i := int64(0); i < 8; i++ {
		lines = append(lines, mpcAsstRec(t, at(i), fmt.Sprintf("BEFORE-%d", i)))
	}
	lines = append(lines, mpcUserRec(t, "u1", "s", at(10), "OPERATOR-PROMPT keep the cap at twenty", nil))
	for i := int64(0); i < 3; i++ {
		lines = append(lines, mpcAsstRec(t, at(11+i), fmt.Sprintf("AFTER-%d", i)))
	}
	lines = append(lines, mpcUserRec(t, "u2", "s", at(20), "[from studio-mini · Carl Zhang] REMOTE-QUESTION is the cap per night",
		map[string]any{"promptSource": "sdk", "origin": nil}))
	mpcTranscript(t, home, "-Users-carl-Developer-kraken", "s.jsonl", lines...)
	return home, now - 60*60_000, at
}

func mpcDecodeOperator(t *testing.T, stdout string) mpcOperatorOut {
	t.Helper()
	var got mpcOperatorOut
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not the OperatorResult JSON (%v): %q", err, stdout)
	}
	return got
}

// ---------------------------------------------------------------------------
// kraken mine operator
// ---------------------------------------------------------------------------

// "JSON of `OperatorResult` on stdout …, a one-line summary on stderr (`N
// operator records in M transcripts since … (S skipped, T truncated)`);
// `--login` defaults to `jobs.Operator()`; `--before`/`--after` default 6 and
// 2".
func TestMpcMineOperatorDefaults(t *testing.T) {
	home, since, _ := mpcOperatorFixture(t)
	stdout, stderr, code := mpcRunCLI(t, "mine", []string{"operator", "--since", fmt.Sprint(since)},
		[]string{"HOME=" + home, "KRAKEN_OPERATOR=Carl Zhang"}, "")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	got := mpcDecodeOperator(t, stdout)
	if got.Since != since || got.Scanned != 1 || len(got.Records) != 2 {
		t.Fatalf("result = since %d scanned %d records %+v", got.Since, got.Scanned, got.Records)
	}
	r := got.Records[0]
	if r.UUID != "u1" || r.Text != "OPERATOR-PROMPT keep the cap at twenty" || r.Form != "typed" {
		t.Errorf("records[0] = %+v", r)
	}
	if len(r.Context.Before) != 6 || len(r.Context.After) != 2 {
		t.Errorf("context = %d before / %d after, want the 6/2 defaults", len(r.Context.Before), len(r.Context.After))
	} else if r.Context.Before[0].Text != "BEFORE-2" || r.Context.Before[5].Text != "BEFORE-7" || r.Context.After[1].Text != "AFTER-1" {
		t.Errorf("context = %+v", r.Context)
	}
	// the login default reaches the stamp check
	if r2 := got.Records[1]; r2.UUID != "u2" || r2.Form != "remote" || r2.Text != "REMOTE-QUESTION is the cap per night" {
		t.Errorf("records[1] = %+v, want the operator's own remote question (login from Operator())", r2)
	}
	line := strings.TrimSpace(stderr)
	if strings.Contains(line, "\n") || !strings.HasPrefix(line, "2 operator records in 1 transcripts since ") ||
		!strings.HasSuffix(line, "(0 skipped, 0 truncated)") {
		t.Errorf("stderr = %q, want the one-line summary", stderr)
	}
}

// The flags: `--until`, `--login`, `--before`, `--after`.
func TestMpcMineOperatorFlags(t *testing.T) {
	home, since, at := mpcOperatorFixture(t)
	stdout, stderr, code := mpcRunCLI(t, "mine", []string{"operator", "--since", fmt.Sprint(since),
		"--until", fmt.Sprint(at(15)), "--login", "Somebody Else", "--before", "1", "--after", "0"},
		[]string{"HOME=" + home, "KRAKEN_OPERATOR=Carl Zhang"}, "")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	got := mpcDecodeOperator(t, stdout)
	if got.Until != at(15) || len(got.Records) != 1 || got.Records[0].UUID != "u1" {
		t.Fatalf("result = until %d records %+v, want only u1 under the ceiling", got.Until, got.Records)
	}
	c := got.Records[0].Context
	if len(c.Before) != 1 || c.Before[0].Text != "BEFORE-7" || len(c.After) != 0 {
		t.Errorf("context = %+v, want 1 before / 0 after", c)
	}

	// the remote question survives only under the matching login
	stdout, _, code = mpcRunCLI(t, "mine", []string{"operator", "--since", fmt.Sprint(since), "--login", "Somebody Else"},
		[]string{"HOME=" + home, "KRAKEN_OPERATOR=Carl Zhang"}, "")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got := mpcDecodeOperator(t, stdout); len(got.Records) != 1 {
		t.Errorf("records under another login = %+v, want the remote question dropped", got.Records)
	}
}

// "`--help` prints usage to stdout and exits 0".
func TestMpcMineOperatorHelp(t *testing.T) {
	stdout, _, code := mpcRunCLI(t, "mine", []string{"operator", "--help"}, []string{"HOME=" + t.TempDir()}, "")
	if code != 0 || strings.TrimSpace(stdout) == "" {
		t.Errorf("--help = exit %d stdout %q, want usage on stdout and exit 0", code, stdout)
	}
}

// ---------------------------------------------------------------------------
// kraken mine around
// ---------------------------------------------------------------------------

// "text on stdout: one block per line group, `label` line, text, blank line,
// exactly the `kraken mine reduce` layout, with the hit block's label line
// prefixed `▶ `; `--radius` default 20; `--radius 0` prints the record alone".
func TestMpcMineAroundPrintsTheMarkedWindow(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UnixMilli()
	path := mpcTranscript(t, home, "-Users-carl-Developer-kraken", "a.jsonl",
		mpcUserRec(t, "p0", "a", now-9000, "OPENING", nil),
		mpcAsstRec(t, now-8000, "A1"),
		mpcUserRec(t, "hit", "a", now-7000, "HIT", nil),
		mpcAsstRec(t, now-6000, "A2"),
		mpcAsstRec(t, now-5000, "A3"),
	)
	env := []string{"HOME=" + home}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"around", path, "hit", "--radius", "1"}, "[assistant]\nA1\n\n▶ [user]\nHIT\n\n[assistant]\nA2\n\n"},
		{[]string{"around", path, "hit", "--radius", "0"}, "▶ [user]\nHIT\n\n"},
		{[]string{"around", path, "hit"}, "[user]\nOPENING\n\n[assistant]\nA1\n\n▶ [user]\nHIT\n\n[assistant]\nA2\n\n[assistant]\nA3\n\n"},
	} {
		stdout, stderr, code := mpcRunCLI(t, "mine", c.args, env, "")
		if code != 0 {
			t.Errorf("%v: exit %d stderr %q", c.args, code, stderr)
			continue
		}
		if stdout != c.want {
			t.Errorf("%v: stdout =\n%q\nwant\n%q", c.args, stdout, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// kraken mm evidence / propose / confirm
// ---------------------------------------------------------------------------

type mpcSeen struct {
	mu                  sync.Mutex
	method, path, query string
	body                []byte
}

func mpcServe(t *testing.T, reply string) (*mpcSeen, string) {
	t.Helper()
	seen := &mpcSeen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen.mu.Lock()
		seen.method, seen.path, seen.query, seen.body = r.Method, r.URL.Path, r.URL.RawQuery, b
		seen.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return seen, srv.Listener.Addr().String()
}

// "one `qfGet`, the server JSON printed verbatim to stdout".
func TestMpcMMEvidenceProxiesOneGet(t *testing.T) {
	reply := `{"since":5,"until":9,"jobs":[],"feedback":[],"answers":[]}` + "\n"
	seen, addr := mpcServe(t, reply)
	stdout, stderr, code := mpcRunCLI(t, "mm", []string{"evidence", "--since", "5", "--until", "9"},
		[]string{"HOME=" + t.TempDir(), "KRAKEN_SERVE_ADDR=" + addr}, "")
	if code != 0 {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if stdout != reply {
		t.Errorf("stdout = %q, want the server JSON verbatim", stdout)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	if seen.method != http.MethodGet || seen.path != "/api/mm/evidence" {
		t.Errorf("request = %s %s", seen.method, seen.path)
	}
	q := mpcQuery(seen.query)
	if q["since"] != "5" || q["until"] != "9" {
		t.Errorf("query = %q, want since=5 until=9", seen.query)
	}
}

func mpcQuery(raw string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(raw, "&") {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

// `kraken mm propose|confirm|commit|revise` were retired with the nightly
// mind job and the mind thread: each is now the unknown-op usage error, and
// none reaches kraken serve.
func TestMpcMMRetiredVerbsAreUnknownOps(t *testing.T) {
	for _, op := range []string{"propose", "confirm", "commit", "revise"} {
		t.Run(op, func(t *testing.T) {
			seen, addr := mpcServe(t, `{}`)
			_, stderr, code := mpcRunCLI(t, "mm", []string{op, "7"},
				[]string{"HOME=" + t.TempDir(), "KRAKEN_SERVE_ADDR=" + addr}, "{}")
			if code == 0 || !strings.Contains(stderr, "unknown op") {
				t.Errorf("mm %s: exit %d stderr %q, want the unknown-op usage error", op, code, stderr)
			}
			seen.mu.Lock()
			defer seen.mu.Unlock()
			if seen.path != "" {
				t.Errorf("mm %s reached serve at %s", op, seen.path)
			}
		})
	}
}

// `kraken mm run <id> --answer` prints the blob text alone; `kraken mm replay
// <id> --write` posts {"id","write":true} and prints the server JSON verbatim.
func TestMpcMMRunAndReplayProxy(t *testing.T) {
	seen, addr := mpcServe(t, `{"run":{"id":3},"blob":"{\"ops\":[]}"}`)
	stdout, stderr, code := mpcRunCLI(t, "mm", []string{"run", "3", "--answer"},
		[]string{"HOME=" + t.TempDir(), "KRAKEN_SERVE_ADDR=" + addr}, "")
	if code != 0 {
		t.Fatalf("run: exit %d stderr %q", code, stderr)
	}
	if stdout != "{\"ops\":[]}\n" {
		t.Errorf("run stdout = %q, want the answer text", stdout)
	}
	seen.mu.Lock()
	if seen.method != http.MethodGet || seen.path != "/api/mm/run" || seen.query != "id=3&blob=answer" {
		t.Errorf("run request = %s %s?%s", seen.method, seen.path, seen.query)
	}
	seen.mu.Unlock()

	reply := `{"source_run":3,"diff":{"ops_added":[],"ops_lost":[],"exclusions_changed":[]},"new_run":9}` + "\n"
	seen, addr = mpcServe(t, reply)
	stdout, stderr, code = mpcRunCLI(t, "mm", []string{"replay", "3", "--write"},
		[]string{"HOME=" + t.TempDir(), "KRAKEN_SERVE_ADDR=" + addr}, "")
	if code != 0 {
		t.Fatalf("replay: exit %d stderr %q", code, stderr)
	}
	if stdout != reply {
		t.Errorf("replay stdout = %q, want the server JSON verbatim", stdout)
	}
	seen.mu.Lock()
	defer seen.mu.Unlock()
	var body map[string]any
	if err := json.Unmarshal(seen.body, &body); err != nil || seen.path != "/api/mm/replay" || body["id"] != float64(3) || body["write"] != true {
		t.Errorf("replay request = %s %s, want {id:3, write:true}", seen.path, seen.body)
	}
}
