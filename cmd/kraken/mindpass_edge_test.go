package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mindpass_edge_test.go — the EDGE dimension of 50-contract.md's CLI rows
// (`kraken mine operator`, `kraken mine around`, `kraken mm confirm|propose|
// evidence`). Written blind from the frozen contract: cmd_mine.go and
// cmd_mm.go were never read in their edited form. The run-function names are
// not frozen, so every case drives the frozen DISPATCH (`cmdMine`, `cmdMM`) in
// a child copy of this test binary — the verbs exit through util.Die, which a
// same-process test cannot observe. Every package-level identifier is
// prefixed `mpe`.

// TestMpeHelperProcess is the child: inert unless MPE_HELPER=1, it runs one
// dispatch with the JSON-encoded MPE_ARGS and exits 0 if the verb returns.
func TestMpeHelperProcess(t *testing.T) {
	if os.Getenv("MPE_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("MPE_ARGS")), &args); err != nil || len(args) == 0 {
		os.Stderr.WriteString("mpe helper: bad MPE_ARGS\n")
		os.Exit(97)
	}
	switch args[0] {
	case "mine":
		cmdMine(args[1:])
	case "mm":
		cmdMM(args[1:])
	default:
		os.Exit(98)
	}
	os.Exit(0)
}

type mpeRun struct {
	stdout, stderr string
	code           int
}

// mpeExec runs `kraken <args>` in the child with HOME isolated and env added.
func mpeExec(t *testing.T, home string, env []string, stdin string, args ...string) mpeRun {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMpeHelperProcess$")
	cmd.Env = append(os.Environ(),
		"MPE_HELPER=1",
		"MPE_ARGS="+string(raw),
		"HOME="+home,
		"KRAKEN_THREAD_SESSION=",
		"KRAKEN_DB_PATH=",
	)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	code := 0
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run child: %v", err)
		}
		code = ee.ExitCode()
	}
	if code == 97 || code == 98 {
		t.Fatalf("helper misuse (exit %d): %s", code, errb.String())
	}
	return mpeRun{stdout: out.String(), stderr: errb.String(), code: code}
}

func mpeISO(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

func mpeLine(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// mpeTranscript writes one root transcript under home's ~/.claude/projects:
// an assistant section, then a typed operator prompt (uuid u-hit), then a
// second assistant section. Returns the path and the prompt's timestamp.
func mpeTranscript(t *testing.T, home string) (string, int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	dir := filepath.Join(home, ".claude", "projects", "-Users-carl-Developer-kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	asst := func(ts int64, text string) string {
		return mpeLine(t, map[string]any{"type": "assistant", "timestamp": mpeISO(ts),
			"message": map[string]any{"model": "claude-opus-5", "content": []any{map[string]any{"type": "text", "text": text}}}})
	}
	lines := []string{
		asst(now-5000, "PRECEDING ASSISTANT WORDS"),
		mpeLine(t, map[string]any{"type": "user", "timestamp": mpeISO(now - 4000), "uuid": "u-hit", "sessionId": "sess-a",
			"origin": map[string]any{"kind": "human"}, "promptSource": "typed",
			"message": map[string]any{"role": "user", "content": "PROMPT-ALONE"}}),
		asst(now-3000, "FOLLOWING ASSISTANT WORDS"),
	}
	p := filepath.Join(dir, "sess-a.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p, now - 4000
}

// ---------------------------------------------------------------------------
// kraken mine operator
// ---------------------------------------------------------------------------

// "`--since` is REQUIRED (missing → usage error, exit non-zero)".
func TestMpeMineOperatorRequiresSince(t *testing.T) {
	home := t.TempDir()
	mpeTranscript(t, home)
	r := mpeExec(t, home, nil, "", "mine", "operator")
	if r.code == 0 {
		t.Fatalf("`mine operator` without --since exited 0 (stdout %q)", r.stdout)
	}
	if strings.Contains(r.stdout, `"records"`) {
		t.Fatalf("a refused run still printed a result: %q", r.stdout)
	}
}

// "unknown flags are errors naming the usage".
func TestMpeMineOperatorUnknownFlag(t *testing.T) {
	home := t.TempDir()
	r := mpeExec(t, home, nil, "", "mine", "operator", "--since", "1", "--bogus")
	if r.code == 0 {
		t.Fatalf("unknown flag exited 0 (stdout %q)", r.stdout)
	}
	if !strings.Contains(strings.ToLower(r.stderr), "usage") || !strings.Contains(r.stderr, "operator") {
		t.Fatalf("unknown-flag error %q does not name the usage", r.stderr)
	}
}

// "`--help` prints usage to stdout and exits 0".
func TestMpeMineOperatorHelp(t *testing.T) {
	r := mpeExec(t, t.TempDir(), nil, "", "mine", "operator", "--help")
	if r.code != 0 {
		t.Fatalf("--help exited %d (stderr %q)", r.code, r.stderr)
	}
	if !strings.Contains(r.stdout, "mine operator") {
		t.Fatalf("--help stdout %q does not carry the usage", r.stdout)
	}
}

// `OperatorOptions`: "negative values read as 0" — `--before -1` is a radius
// of zero, not a refusal: the record comes back with an empty `before`.
func TestMpeMineOperatorNegativeBeforeReadsAsZero(t *testing.T) {
	home := t.TempDir()
	_, ts := mpeTranscript(t, home)
	since := strconv.FormatInt(ts-60_000, 10)
	r := mpeExec(t, home, nil, "", "mine", "operator", "--since", since, "--login", "carl@probook.ai", "--before", "-1")
	if r.code != 0 {
		t.Fatalf("--before -1 exited %d (stderr %q)", r.code, r.stderr)
	}
	var res struct {
		Records []struct {
			UUID    string `json:"uuid"`
			Context struct {
				Before []json.RawMessage `json:"before"`
			} `json:"context"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil {
		t.Fatalf("stdout is not the OperatorResult JSON: %v\n%s", err, r.stdout)
	}
	if len(res.Records) != 1 || res.Records[0].UUID != "u-hit" {
		t.Fatalf("records = %+v, want the one typed prompt", res.Records)
	}
	if len(res.Records[0].Context.Before) != 0 {
		t.Fatalf("before = %d blocks with --before -1, want 0", len(res.Records[0].Context.Before))
	}
}

// ---------------------------------------------------------------------------
// kraken mine around
// ---------------------------------------------------------------------------

// `kraken mine around <transcript path> <uuid>` — one positional is a refusal.
func TestMpeMineAroundNeedsTwoPositionals(t *testing.T) {
	home := t.TempDir()
	p, _ := mpeTranscript(t, home)
	if r := mpeExec(t, home, nil, "", "mine", "around", p); r.code == 0 {
		t.Fatalf("`mine around <path>` alone exited 0 (stdout %q)", r.stdout)
	}
	if r := mpeExec(t, home, nil, "", "mine", "around"); r.code == 0 {
		t.Fatalf("`mine around` with no positionals exited 0 (stdout %q)", r.stdout)
	}
}

// "an unknown uuid exits non-zero with a sentence naming the uuid and the
// path".
func TestMpeMineAroundUnknownUUID(t *testing.T) {
	home := t.TempDir()
	p, _ := mpeTranscript(t, home)
	r := mpeExec(t, home, nil, "", "mine", "around", p, "u-nowhere-to-be-found")
	if r.code == 0 {
		t.Fatalf("unknown uuid exited 0 (stdout %q)", r.stdout)
	}
	if !strings.Contains(r.stderr, "u-nowhere-to-be-found") || !strings.Contains(r.stderr, p) {
		t.Fatalf("stderr %q does not name both the uuid and the path", r.stderr)
	}
}

// "an unreadable path exits non-zero".
func TestMpeMineAroundUnreadablePath(t *testing.T) {
	home := t.TempDir()
	r := mpeExec(t, home, nil, "", "mine", "around", filepath.Join(home, "missing.jsonl"), "u-hit")
	if r.code == 0 {
		t.Fatalf("missing path exited 0 (stdout %q)", r.stdout)
	}
}

// "`--radius 0` prints the record alone", its label line prefixed `▶ `.
func TestMpeMineAroundRadiusZeroPrintsTheRecordAlone(t *testing.T) {
	home := t.TempDir()
	p, _ := mpeTranscript(t, home)
	r := mpeExec(t, home, nil, "", "mine", "around", p, "u-hit", "--radius", "0")
	if r.code != 0 {
		t.Fatalf("--radius 0 exited %d (stderr %q)", r.code, r.stderr)
	}
	if strings.Contains(r.stdout, "PRECEDING") || strings.Contains(r.stdout, "FOLLOWING") {
		t.Fatalf("--radius 0 printed a neighbour:\n%s", r.stdout)
	}
	if !strings.HasPrefix(r.stdout, "▶ ") || !strings.Contains(r.stdout, "PROMPT-ALONE") {
		t.Fatalf("--radius 0 output does not open on the ▶-marked record:\n%s", r.stdout)
	}
	if n := strings.Count(r.stdout, "▶ "); n != 1 {
		t.Fatalf("%d hit markers, want 1:\n%s", n, r.stdout)
	}
}

// ---------------------------------------------------------------------------
// kraken mm confirm / propose / evidence
// ---------------------------------------------------------------------------

// mpeServer is a stand-in serve that records every hit and answers status
// with body.
func mpeServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var hits atomic.Int32
	var last atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		b, _ := io.ReadAll(r.Body)
		last.Store(r.Method + " " + r.URL.Path + " " + string(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits, &last
}

func mpeAddr(srv *httptest.Server) string { return strings.TrimPrefix(srv.URL, "http://") }

// The mind-run verbs refuse a bad id or flag LOCALLY — no request reaches
// serve.
func TestMpeMMRunVerbsBadArgsRefusedLocally(t *testing.T) {
	srv, hits, _ := mpeServer(t, http.StatusOK, `{}`)
	for _, args := range [][]string{
		{"mm", "run", "abc"}, {"mm", "run"}, {"mm", "run", "3", "--report", "--answer"},
		{"mm", "replay", "abc"}, {"mm", "replay"}, {"mm", "replay", "3", "--force"},
		{"mm", "runs", "--n", "x"}, {"mm", "runs", "--bogus"}, {"mm", "marks", "extra"},
	} {
		r := mpeExec(t, t.TempDir(), []string{"KRAKEN_SERVE_ADDR=" + mpeAddr(srv)}, "", args...)
		if r.code == 0 {
			t.Errorf("%v exited 0 (stdout %q)", args, r.stdout)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("local refusals reached serve %d times", n)
	}
}

// "a 4xx is a non-zero exit carrying the server sentence" — a replay of a
// pruned bundle.
func TestMpeMMReplay409CarriesTheServerSentence(t *testing.T) {
	srv, hits, last := mpeServer(t, http.StatusConflict, `{"error":"mind run 3's bundle was pruned on 2026-01-01"}`)
	r := mpeExec(t, t.TempDir(), []string{"KRAKEN_SERVE_ADDR=" + mpeAddr(srv)}, "", "mm", "replay", "3")
	if r.code == 0 {
		t.Fatalf("a 409 exited 0 (stdout %q)", r.stdout)
	}
	if !strings.Contains(r.stderr, "bundle was pruned") {
		t.Fatalf("stderr %q does not carry the server sentence", r.stderr)
	}
	if hits.Load() != 1 {
		t.Fatalf("serve hit %d times, want 1", hits.Load())
	}
	if got, _ := last.Load().(string); !strings.HasPrefix(got, "POST /api/mm/replay ") || !strings.Contains(got, `"write":false`) {
		t.Fatalf("request = %q, want a POST to /api/mm/replay with write false", got)
	}
}

// "a serve-down call is a non-zero exit with the `qfUnreachableError`
// sentence, never `{}`".
func TestMpeMMEvidenceServeDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := mpeAddr(srv)
	srv.Close() // the port is now closed: serve is down
	r := mpeExec(t, t.TempDir(), []string{"KRAKEN_SERVE_ADDR=" + addr}, "", "mm", "evidence", "--since", "1", "--until", "2")
	if r.code == 0 {
		t.Fatalf("serve-down evidence exited 0 (stdout %q)", r.stdout)
	}
	if strings.TrimSpace(r.stdout) == "{}" {
		t.Fatal("serve-down evidence printed {}")
	}
	if !strings.Contains(r.stderr, "kraken serve unreachable") {
		t.Fatalf("stderr %q lacks the qfUnreachableError sentence", r.stderr)
	}
}
