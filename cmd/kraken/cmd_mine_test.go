package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cmd_mine_test.go — `kraken mine digest|reduce`'s testable core. The two
// halves it has to get right are the --since parse (a verb whose whole job is
// a time window must not silently sweep the wrong one) and the stdout/stderr
// split (stdout is JSON that pipes; everything else goes to stderr).
//
// The digest/reduce reads themselves belong to core.MineTranscripts and
// core.ReduceTranscript, which internal/core/mine_test.go covers; these tests
// exercise the CLI seam by pointing HOME at a temp dir holding a synthetic
// transcript, the same isolation core's own tests use.

func TestParseMineSinceDefaultsTo24h(t *testing.T) {
	before := time.Now().Add(-24 * time.Hour).UnixMilli()
	got, rest, err := parseMineSince(nil)
	if err != nil {
		t.Fatalf("parseMineSince: %v", err)
	}
	after := time.Now().Add(-24 * time.Hour).UnixMilli()
	if got < before || got > after {
		t.Fatalf("since = %d, want a value in [%d,%d] (24h ago)", got, before, after)
	}
	if len(rest) != 0 {
		t.Fatalf("rest = %v, want empty", rest)
	}
}

func TestParseMineSinceBothSpellings(t *testing.T) {
	for _, args := range [][]string{
		{"--since", "1700000000000", "tail"},
		{"--since=1700000000000", "tail"},
	} {
		got, rest, err := parseMineSince(args)
		if err != nil {
			t.Fatalf("parseMineSince(%v): %v", args, err)
		}
		if got != 1700000000000 {
			t.Fatalf("parseMineSince(%v) since = %d, want 1700000000000", args, got)
		}
		if len(rest) != 1 || rest[0] != "tail" {
			t.Fatalf("parseMineSince(%v) rest = %v, want [tail]", args, rest)
		}
	}
}

// TestParseMineSinceRejectsGarbage is the point of parsing the flag at all: a
// typo that fell back to the 24h default would read a window nobody asked
// for, and on this verb that means auditing the wrong night with no sign
// anything went wrong.
func TestParseMineSinceRejectsGarbage(t *testing.T) {
	for _, args := range [][]string{
		{"--since", "yesterday"},
		{"--since=-5"},
		{"--since"},
		{"--since", ""},
	} {
		if _, _, err := parseMineSince(args); err == nil {
			t.Fatalf("parseMineSince(%v) = nil error, want a refusal", args)
		}
	}
}

func TestRunMineDigestRejectsExtraArgs(t *testing.T) {
	err := runMineDigest([]string{"--since", "1", "extra"}, os.Stdout, os.Stderr)
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("err = %v, want a usage error naming the unexpected argument", err)
	}
}

func TestRunMineReduceRequiresExactlyOnePath(t *testing.T) {
	for _, args := range [][]string{nil, {"a", "b"}, {"  "}} {
		if err := runMineReduce(args, os.Stdout, os.Stderr); err == nil {
			t.Fatalf("runMineReduce(%v) = nil error, want a usage error", args)
		}
	}
}

// TestRunMineDigestWritesJSONToStdout: the output contract. stdout carries
// the JSON and nothing else — a caller pipes it — while the human-facing
// totals go to stderr where they cannot corrupt it.
func TestRunMineDigestWritesJSONToStdout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeTestTranscript(t, home, "proj", "sess-1", 1_700_000_100_000)

	stdout, stderr := captureMine(t, func(out, errOut *os.File) error {
		return runMineDigest([]string{"--since", "1700000000000"}, out, errOut)
	})

	var got struct {
		Since     int64 `json:"since"`
		Scanned   int   `json:"scanned"`
		Truncated int   `json:"truncated"`
		Tokens    struct {
			In, Out, CacheWrite, CacheRead int64
			Sessions                       int
		} `json:"tokens"`
		Digests []struct {
			SessionID  string         `json:"SessionID"`
			ToolCounts map[string]int `json:"ToolCounts"`
		} `json:"digests"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON (%v): %q", err, stdout)
	}
	if got.Since != 1700000000000 {
		t.Fatalf("since = %d, want the flag's value echoed back", got.Since)
	}
	if got.Scanned != 1 || len(got.Digests) != 1 {
		t.Fatalf("scanned = %d with %d digests, want exactly the one transcript", got.Scanned, len(got.Digests))
	}
	if got.Digests[0].ToolCounts["Bash"] != 1 {
		t.Fatalf("ToolCounts = %v, want the one Bash call counted", got.Digests[0].ToolCounts)
	}
	// The top-level roll-up is what a nightly pass reads first. It is listed by
	// hand in runMineDigest's anonymous output struct, so a new MineResult field
	// is dropped silently unless someone adds it there — which already happened
	// to this one. This assertion is the thing that would have caught it.
	if got.Tokens.Out != 22 || got.Tokens.CacheWrite != 33 || got.Tokens.CacheRead != 44 || got.Tokens.In != 11 {
		t.Fatalf("top-level tokens = %+v, want the record's usage rolled up", got.Tokens)
	}
	if got.Tokens.Sessions != 1 {
		t.Fatalf("tokens.Sessions = %d, want 1 (the one transcript)", got.Tokens.Sessions)
	}
	if !strings.Contains(stderr, "22 out") {
		t.Fatalf("stderr = %q, want the token totals named", stderr)
	}
	if !strings.Contains(stderr, "mined 1 transcripts") {
		t.Fatalf("stderr = %q, want the totals line", stderr)
	}
	if strings.Contains(stdout, "mined 1 transcripts") {
		t.Fatalf("the totals line leaked into stdout, which must stay pipeable JSON: %q", stdout)
	}
}

// TestRunMineReduceEmptyWindowFails: an empty reduction is byte-identical at
// the shell to a successful one when stdout is redirected to a file, and its
// common cause is a dropped --since whose 24h default excluded the whole
// session — so it is an ERROR, not a stderr aside: agents staged 0-byte
// "reduced views" off the old zero exit and read them as success.
func TestRunMineReduceEmptyWindowFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeTestTranscript(t, home, "proj", "sess-1", 1_700_000_100_000)

	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	outCh := make(chan string, 1)
	go func() { outCh <- readAll(outR) }()
	// A floor far past every record in the file.
	reduceErr := runMineReduce([]string{"--since", "1800000000000", path}, outW, outW)
	outW.Close()
	stdout := <-outCh
	outR.Close()
	if reduceErr == nil {
		t.Fatal("want an error for an all-excluded window, got nil")
	}
	if !strings.Contains(reduceErr.Error(), "0 bytes") || !strings.Contains(reduceErr.Error(), "--since") {
		t.Fatalf("error = %q, want the 0-bytes explanation naming --since", reduceErr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want nothing for an all-excluded window", stdout)
	}
}

// TestRunMineHelpIsUsageNotAPath: `--help`/`-h`/`help` must print usage to
// stdout and succeed, per the package convention cmd_account.go states — it
// used to fall through to the positional slot and die with "open transcript:
// open --help: no such file or directory".
func TestRunMineHelpIsUsageNotAPath(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}, {"--since", "0", "--help"}} {
		stdout, _ := captureMine(t, func(out, errOut *os.File) error {
			return runMineReduce(args, out, errOut)
		})
		if !strings.Contains(stdout, "usage:") {
			t.Fatalf("runMineReduce(%v) stdout = %q, want a usage line", args, stdout)
		}
	}
	stdout, _ := captureMine(t, func(out, errOut *os.File) error {
		return runMineDigest([]string{"--help"}, out, errOut)
	})
	if !strings.Contains(stdout, "usage:") {
		t.Fatalf("runMineDigest(--help) stdout = %q, want a usage line", stdout)
	}
}

func TestRunMineReduceEmitsProseAndCalls(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := writeTestTranscript(t, home, "proj", "sess-1", 1_700_000_100_000)

	stdout, _ := captureMine(t, func(out, errOut *os.File) error {
		return runMineReduce([]string{"--since", "1700000000000", path}, out, errOut)
	})
	for _, want := range []string{"[user]", "hello there", "[tool] Bash("} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("reduction missing %q:\n%s", want, stdout)
		}
	}
}

// TestCmdMineUsageErrors covers the two util.Die branches, which must run in
// a subprocess: os.Exit(1) in-process would tear down `go test` itself.
func TestCmdMineUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"unknown op", []string{"bogus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stderr, code := runDispatchSubprocess(t, "mine", tc.args)
			if code == 0 {
				t.Fatalf("exit = 0, want non-zero for %v", tc.args)
			}
			if !strings.Contains(stderr, "usage: k mine") {
				t.Fatalf("stderr = %q, want the usage line", stderr)
			}
		})
	}
}

// --- helpers ---

// writeTestTranscript lays one synthetic session down where MineTranscripts
// looks for it and returns its path.
func writeTestTranscript(t *testing.T, home, project, session string, ts int64) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stamp := time.UnixMilli(ts).UTC().Format(time.RFC3339Nano)
	lines := []string{
		`{"type":"user","timestamp":"` + stamp + `","message":{"content":"hello there"}}`,
		`{"type":"assistant","timestamp":"` + stamp + `","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"echo hi"}}],` +
			`"usage":{"input_tokens":11,"output_tokens":22,"cache_creation_input_tokens":33,"cache_read_input_tokens":44}}}`,
	}
	path := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// captureMine runs fn with real os.Pipe-backed *os.File stdout/stderr — the
// signature runMineDigest/runMineReduce take — and returns what each got.
func captureMine(t *testing.T, fn func(stdout, stderr *os.File) error) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	// Read both pipes concurrently: a writer that fills the 64 KB pipe buffer
	// would otherwise block forever against a reader that only starts after
	// fn returns.
	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() { outCh <- readAll(outR) }()
	go func() { errCh <- readAll(errR) }()

	fnErr := fn(outW, errW)
	outW.Close()
	errW.Close()
	gotOut, gotErr := <-outCh, <-errCh
	outR.Close()
	errR.Close()
	if fnErr != nil {
		t.Fatalf("mine call: %v", fnErr)
	}
	return gotOut, gotErr
}

func readAll(f *os.File) string {
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			return sb.String()
		}
	}
}
