package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// handofflastsaid_edge_test.go — the EDGE dimension of
// artifacts/50-contract.md (the handoff hook names the agent's last words
// before the compaction): lastSaidBefore's refusals and skip rules (O1),
// runHandoffHook's byte-identical fallbacks and silent arms (O2), and
// handoffLastSaidLine's truncation and clock boundaries (O3). Happy paths
// belong to the sibling blind file (lsLogic*).
//
// Written blind from the contract text: no byte of cmd/kraken/cmd_handoffhook.go
// was read. Every fixture transcript is invented and written into t.TempDir().
//
// Every package-level identifier here is prefixed `lsEdge`.

// --- helpers ---------------------------------------------------------------

// lsEdgeNow is the fixed clock every test passes.
var lsEdgeNow = time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)

// lsEdgeText is one {"type":"text"} content block.
func lsEdgeText(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// lsEdgeTool is one {"type":"tool_use"} content block.
func lsEdgeTool() map[string]any {
	return map[string]any{"type": "tool_use", "id": "toolu_edge", "name": "Bash", "input": map[string]any{"command": "true"}}
}

// lsEdgeJSON marshals v, failing the test on error.
func lsEdgeJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// lsEdgeAsst renders one main-chain assistant record; ts == "" omits the
// timestamp key entirely.
func lsEdgeAsst(t *testing.T, ts string, blocks ...map[string]any) string {
	t.Helper()
	if blocks == nil {
		blocks = []map[string]any{} // marshal as [], never null
	}
	rec := map[string]any{
		"type":    "assistant",
		"message": map[string]any{"role": "assistant", "content": blocks},
	}
	if ts != "" {
		rec["timestamp"] = ts
	}
	return lsEdgeJSON(t, rec)
}

// lsEdgeSay is the common case: one assistant record with one text block.
func lsEdgeSay(t *testing.T, ts, text string) string {
	t.Helper()
	return lsEdgeAsst(t, ts, lsEdgeText(text))
}

// lsEdgeBoundary renders one compact_boundary record.
func lsEdgeBoundary(ts string) string {
	return `{"type":"system","subtype":"compact_boundary","timestamp":"` + ts + `","content":"Conversation compacted"}`
}

// lsEdgeUser renders one user record carrying a text block (shaped like an
// assistant candidate so that only `type` can disqualify it).
func lsEdgeUser(t *testing.T, ts, text string, extra map[string]any) string {
	t.Helper()
	rec := map[string]any{
		"type":      "user",
		"timestamp": ts,
		"message":   map[string]any{"role": "user", "content": []any{lsEdgeText(text)}},
	}
	for k, v := range extra {
		rec[k] = v
	}
	return lsEdgeJSON(t, rec)
}

// lsEdgeWriteTranscript writes the lines, each newline-terminated, to a fresh
// transcript file and returns its path.
func lsEdgeWriteTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

// lsEdgeWriteRaw writes raw bytes as a transcript file.
func lsEdgeWriteRaw(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

// lsEdgeWantText asserts lastSaidBefore returns true with exactly want.
func lsEdgeWantText(t *testing.T, p, want string) lastSaid {
	t.Helper()
	ls, ok := lastSaidBefore(p)
	if !ok {
		t.Fatalf("lastSaidBefore = false, want true with %q", want)
	}
	if ls.Text != want {
		t.Fatalf("lastSaidBefore Text = %q, want %q", ls.Text, want)
	}
	return ls
}

// lsEdgeWantNone asserts lastSaidBefore returns false.
func lsEdgeWantNone(t *testing.T, p, why string) {
	t.Helper()
	if ls, ok := lastSaidBefore(p); ok {
		t.Fatalf("%s: lastSaidBefore = (%+v, true), want false", why, ls)
	}
}

// lsEdgeCaptureStderr runs fn with os.Stderr redirected to a pipe and returns
// whatever was written there.
func lsEdgeCaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stderr = orig }()
		fn()
	}()
	w.Close()
	got := <-done
	r.Close()
	return got
}

// lsEdgeRun runs the hook with lsEdgeNow and returns stdout, failing if
// anything reached stderr or a panic escaped.
func lsEdgeRun(t *testing.T, stdin, dir string) string {
	t.Helper()
	var out bytes.Buffer
	var panicked any
	errOut := lsEdgeCaptureStderr(t, func() {
		defer func() { panicked = recover() }()
		runHandoffHook(strings.NewReader(stdin), &out, dir, lsEdgeNow)
	})
	if panicked != nil {
		t.Fatalf("runHandoffHook panicked: %v", panicked)
	}
	if errOut != "" {
		t.Fatalf("runHandoffHook wrote to stderr: %q", errOut)
	}
	return out.String()
}

// lsEdgeContext decodes exactly one SessionStart object + newline and returns
// its additionalContext.
func lsEdgeContext(t *testing.T, out string) string {
	t.Helper()
	if !strings.HasSuffix(out, "\n") || strings.HasSuffix(out, "\n\n") {
		t.Fatalf("hook output must end in exactly one newline: %q", out)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("hook output is not a JSON object: %v\n%q", err, out)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == nil {
		t.Fatalf("hook output carries more than one JSON value: %q", out)
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName = %q, want SessionStart", got.HookSpecificOutput.HookEventName)
	}
	return got.HookSpecificOutput.AdditionalContext
}

// lsEdgeDocDir returns a dir holding one 80-handoff.md written 5 minutes
// before lsEdgeNow, so the docs section is present in every comparison.
func lsEdgeDocDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "80-handoff.md")
	if err := os.WriteFile(p, []byte("# handoff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mt := lsEdgeNow.Add(-5 * time.Minute)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatal(err)
	}
	return dir
}

const lsEdgeBasePayload = `{"session_id":"s-edge","source":"compact"}`

// lsEdgePayloadRaw is the compact payload with transcript_path set to the RAW
// JSON value given.
func lsEdgePayloadRaw(rawTranscriptPath string) string {
	return `{"session_id":"s-edge","source":"compact","transcript_path":` + rawTranscriptPath + `}`
}

// lsEdgeWantLine builds O3's exact sentence from its two slots.
func lsEdgeWantLine(when, first string) string {
	return "Your last words before this compaction, sent " + when + ": «" + first +
		"» — that message was delivered; a summary listing it as still to be sent is wrong, so do not send it again unless the work since has changed it."
}

// lsEdgeValidTranscript is a well-formed transcript whose answer is "edge last words".
func lsEdgeValidTranscript(t *testing.T) string {
	t.Helper()
	return lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00.000Z", "edge last words"),
		lsEdgeBoundary("2026-01-02T03:30:00.000Z"),
	)
}

// --- O1 rule 5: the false cases --------------------------------------------

func TestLsEdgeLastSaidEmptyPath(t *testing.T) {
	lsEdgeWantNone(t, "", "empty path")
}

func TestLsEdgeLastSaidMissingFile(t *testing.T) {
	lsEdgeWantNone(t, filepath.Join(t.TempDir(), "nope.jsonl"), "missing file")
}

func TestLsEdgeLastSaidDirectory(t *testing.T) {
	// A directory is "cannot be opened" (O1.5) even when it holds a
	// transcript-looking file.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x.jsonl"), []byte(lsEdgeSay(t, "2026-01-02T03:00:00Z", "inside")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lsEdgeWantNone(t, dir, "directory")
}

func TestLsEdgeLastSaidUnreadableFile(t *testing.T) {
	p := lsEdgeValidTranscript(t)
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	if f, err := os.Open(p); err == nil {
		f.Close()
		t.Skip("running with privileges that ignore file modes")
	}
	lsEdgeWantNone(t, p, "unreadable file")
}

func TestLsEdgeLastSaidNoCandidate(t *testing.T) {
	cases := map[string][]string{
		"empty file":      {},
		"boundary only":   {lsEdgeBoundary("2026-01-02T03:00:00Z")},
		"user only":       {lsEdgeUser(t, "2026-01-02T03:00:00Z", "a user line", nil)},
		"tool_use only":   {lsEdgeAsst(t, "2026-01-02T03:00:00Z", lsEdgeTool())},
		"whitespace only": {lsEdgeSay(t, "2026-01-02T03:00:00Z", " \n\t ")},
		"sidechain only": {lsEdgeJSON(t, map[string]any{
			"type": "assistant", "isSidechain": true, "timestamp": "2026-01-02T03:00:00Z",
			"message": map[string]any{"content": []any{lsEdgeText("side words")}},
		})},
	}
	for name, lines := range cases {
		t.Run(name, func(t *testing.T) {
			lsEdgeWantNone(t, lsEdgeWriteTranscript(t, lines...), name)
		})
	}
}

// Every candidate sits AFTER the only boundary: boundaries are irrelevant
// (amended O1.2/O1.4), so the last candidate in the file is the answer.
func TestLsEdgeLastSaidOnlyCandidatesAfterBoundary(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeUser(t, "2026-01-02T02:59:00Z", "do the thing", nil),
		lsEdgeBoundary("2026-01-02T03:00:00Z"),
		lsEdgeSay(t, "2026-01-02T03:10:00Z", "post-compaction words"),
	)
	lsEdgeWantText(t, p, "post-compaction words")
}

// --- amended O1.5: only a REGULAR file is read ----------------------------

// A FIFO must answer false PROMPTLY: stat runs before open, so the hook never
// blocks waiting for a writer that will not come.
func TestLsEdgeLastSaidFIFOIsFalsePromptly(t *testing.T) {
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := syscall.Mkfifo(p, 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	type res struct{ ok bool }
	done := make(chan res, 1)
	go func() {
		_, ok := lastSaidBefore(p)
		done <- res{ok}
	}()
	select {
	case r := <-done:
		if r.ok {
			t.Fatalf("lastSaidBefore(FIFO) = true, want false")
		}
	case <-time.After(3 * time.Second):
		// Unblock the stuck open so the goroutine can finish, then fail.
		if w, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			w.Close()
		}
		t.Fatalf("lastSaidBefore(FIFO) still blocked after 3s; contract: stat before open, a FIFO is false")
	}
}

// A character device is not a regular file → false, without reading it.
func TestLsEdgeLastSaidDeviceIsFalse(t *testing.T) {
	for _, dev := range []string{"/dev/null", "/dev/zero", "/dev/urandom"} {
		if _, err := os.Stat(dev); err != nil {
			continue
		}
		done := make(chan bool, 1)
		go func() {
			_, ok := lastSaidBefore(dev)
			done <- ok
		}()
		select {
		case ok := <-done:
			if ok {
				t.Fatalf("lastSaidBefore(%s) = true, want false", dev)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("lastSaidBefore(%s) did not return within 3s; a device must be refused at stat", dev)
		}
	}
}

// A symlink to a regular transcript stats as a regular file (os.Stat follows
// links) and is read. Assumption: "stat" in O1.5 is a following stat.
func TestLsEdgeLastSaidSymlinkToRegularFileIsRead(t *testing.T) {
	target := lsEdgeValidTranscript(t)
	link := filepath.Join(t.TempDir(), "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	lsEdgeWantText(t, link, "edge last words")
}

func TestLsEdgeLastSaidHostileFilesAreFalse(t *testing.T) {
	junk := make([]byte, 4096)
	for i := range junk {
		junk[i] = byte(i*131 + 7)
	}
	cases := map[string][]byte{
		"one byte":      []byte("{"),
		"only newlines": []byte("\n\n\n\n\n"),
		"binary junk":   junk,
		"nul bytes":     make([]byte, 512),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			p := lsEdgeWriteRaw(t, data)
			var ok bool
			errOut := lsEdgeCaptureStderr(t, func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("lastSaidBefore panicked: %v", r)
					}
				}()
				_, ok = lastSaidBefore(p)
			})
			if ok {
				t.Fatalf("%s: lastSaidBefore = true, want false", name)
			}
			if errOut != "" {
				t.Fatalf("%s: lastSaidBefore wrote to stderr: %q", name, errOut)
			}
		})
	}
}

// --- O1 rule 1: malformed lines and the 16 MiB ceiling ---------------------

func TestLsEdgeLastSaidSkipsMalformedAndNonObjectLines(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "the real words"),
		"this is not json",
		`{"type":"assistant","message":{"content":[{"type":"text","text":"truncated`,
		`["assistant",{"type":"text","text":"array record"}]`,
		`"a bare string"`,
		`42`,
		`null`,
		``,
		lsEdgeBoundary("2026-01-02T03:05:00Z"),
		"more junk after the boundary }{",
	)
	lsEdgeWantText(t, p, "the real words")
}

// A malformed line between two candidates does not hide the later one.
func TestLsEdgeLastSaidMalformedLineDoesNotEndScan(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "earlier"),
		"}{ garbage",
		lsEdgeSay(t, "2026-01-02T03:01:00Z", "later"),
	)
	lsEdgeWantText(t, p, "later")
}

// A last line with no trailing newline is still a line. (Contract silence:
// "line by line" — assumed a final unterminated line is read like any other.)
func TestLsEdgeLastSaidUnterminatedFinalLine(t *testing.T) {
	data := lsEdgeSay(t, "2026-01-02T03:00:00Z", "first") + "\n" + lsEdgeSay(t, "2026-01-02T03:01:00Z", "unterminated")
	lsEdgeWantText(t, lsEdgeWriteRaw(t, []byte(data)), "unterminated")
}

// A line over 16 MiB makes the WHOLE answer false (amended O1.1: silence
// beats a confident wrong quote) — never the candidate before it, never one
// after it. The giant line is itself a valid candidate.
func TestLsEdgeLastSaidOver16MiBLineEndsScan(t *testing.T) {
	giant := lsEdgeSay(t, "2026-01-02T03:01:00Z", strings.Repeat("x", 17<<20))
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "before the giant"),
		giant,
		lsEdgeSay(t, "2026-01-02T03:02:00Z", "after the giant"),
	)
	lsEdgeWantNone(t, p, "over-16-MiB line mid-file")
}

// Same when the giant line is not JSON at all.
func TestLsEdgeLastSaidOver16MiBJunkLineEndsScan(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "before the junk"),
		strings.Repeat("z", 17<<20),
		lsEdgeSay(t, "2026-01-02T03:02:00Z", "after the junk"),
	)
	lsEdgeWantNone(t, p, "over-16-MiB junk line mid-file")
}

// Same when the giant line is the LAST line, after a real candidate.
func TestLsEdgeLastSaidOver16MiBLastLineIsFalse(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "a real candidate first"),
		strings.Repeat("w", 17<<20),
	)
	lsEdgeWantNone(t, p, "over-16-MiB final line")
}

// A giant FIRST line → false, no panic.
func TestLsEdgeLastSaidOver16MiBFirstLineIsFalse(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:01:00Z", strings.Repeat("y", 17<<20)),
		lsEdgeSay(t, "2026-01-02T03:02:00Z", "unreachable"),
	)
	lsEdgeWantNone(t, p, "giant first line")
}

// --- amended O1 rules 2, 4, 6: boundaries are irrelevant -------------------

// Several boundaries: the last candidate in the FILE wins, after all of them.
func TestLsEdgeLastSaidMultipleBoundariesLastWins(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T01:00:00Z", "before first boundary"),
		lsEdgeBoundary("2026-01-02T01:10:00Z"),
		lsEdgeSay(t, "2026-01-02T02:00:00Z", "between boundaries"),
		lsEdgeBoundary("2026-01-02T02:10:00Z"),
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "after last boundary"),
	)
	lsEdgeWantText(t, p, "after last boundary")
}

// Two adjacent boundaries change nothing: the last candidate in the file.
func TestLsEdgeLastSaidAdjacentBoundaries(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T01:00:00Z", "only candidate before both"),
		lsEdgeBoundary("2026-01-02T01:10:00Z"),
		lsEdgeUser(t, "2026-01-02T01:11:00Z", "summary text", map[string]any{"isCompactSummary": true}),
		lsEdgeBoundary("2026-01-02T02:10:00Z"),
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "after"),
	)
	lsEdgeWantText(t, p, "after")
	// And with nothing after the second boundary, the earlier candidate is
	// still the last one in the file.
	q := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T01:00:00Z", "only candidate before both"),
		lsEdgeBoundary("2026-01-02T01:10:00Z"),
		lsEdgeBoundary("2026-01-02T02:10:00Z"),
	)
	lsEdgeWantText(t, q, "only candidate before both")
}

// A boundary as the very last line.
func TestLsEdgeLastSaidBoundaryIsLastLine(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "one"),
		lsEdgeSay(t, "2026-01-02T03:01:00Z", "two"),
		lsEdgeBoundary("2026-01-02T03:02:00Z"),
	)
	lsEdgeWantText(t, p, "two")
}

// A candidate after the boundary wins — and file order decides, not time: an
// after-boundary candidate with an OLDER timestamp still wins.
func TestLsEdgeLastSaidAfterBoundaryIgnoredDespiteTimestamps(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "pre"),
		lsEdgeBoundary("2026-01-02T03:05:00Z"),
		lsEdgeSay(t, "2027-12-31T23:59:59Z", "post but newer"),
	)
	lsEdgeWantText(t, p, "post but newer")
	q := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "pre, newer stamp"),
		lsEdgeBoundary("2026-01-02T03:05:00Z"),
		lsEdgeSay(t, "2020-01-01T00:00:00Z", "post, older stamp"),
	)
	lsEdgeWantText(t, q, "post, older stamp")
}

// File order beats timestamps before the boundary too: a candidate with an
// OLDER timestamp later in the file is still the answer.
func TestLsEdgeLastSaidFileOrderNotTimestampOrder(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:59:00Z", "newer stamp, earlier line"),
		lsEdgeSay(t, "2026-01-02T01:00:00Z", "older stamp, later line"),
		lsEdgeBoundary("2026-01-02T03:59:30Z"),
	)
	lsEdgeWantText(t, p, "older stamp, later line")
}

// Only type=system + subtype=compact_boundary is a boundary.
func TestLsEdgeLastSaidNearMissBoundariesAreNotBoundaries(t *testing.T) {
	nearMisses := map[string]string{
		"system other subtype": `{"type":"system","subtype":"informational","timestamp":"2026-01-02T03:01:00Z"}`,
		"system no subtype":    `{"type":"system","timestamp":"2026-01-02T03:01:00Z"}`,
		"user with subtype":    `{"type":"user","subtype":"compact_boundary","timestamp":"2026-01-02T03:01:00Z"}`,
		"attachment subtype":   `{"type":"attachment","subtype":"compact_boundary","timestamp":"2026-01-02T03:01:00Z"}`,
		"nested boundary":      `{"type":"attachment","message":{"type":"system","subtype":"compact_boundary"}}`,
		"array shaped":         `["system","compact_boundary"]`,
		"subtype case":         `{"type":"system","subtype":"COMPACT_BOUNDARY"}`,
	}
	for name, line := range nearMisses {
		t.Run(name, func(t *testing.T) {
			p := lsEdgeWriteTranscript(t,
				lsEdgeSay(t, "2026-01-02T03:00:00Z", "before near miss"),
				line,
				lsEdgeSay(t, "2026-01-02T03:02:00Z", "after near miss"),
			)
			// No real boundary → the last candidate in the file.
			lsEdgeWantText(t, p, "after near miss")
		})
	}
}

// --- O1 rule 3: candidate shape ---------------------------------------------

// Assistant records that do not qualify are skipped in favour of an earlier
// candidate.
func TestLsEdgeLastSaidNonQualifyingAssistantSkipped(t *testing.T) {
	cases := map[string]string{
		"tool_use only":           lsEdgeAsst(t, "2026-01-02T03:01:00Z", lsEdgeTool()),
		"whitespace text":         lsEdgeAsst(t, "2026-01-02T03:01:00Z", lsEdgeText("   \n\t  ")),
		"empty text":              lsEdgeAsst(t, "2026-01-02T03:01:00Z", lsEdgeText("")),
		"whitespace text + tool":  lsEdgeAsst(t, "2026-01-02T03:01:00Z", lsEdgeText("\n"), lsEdgeTool()),
		"thinking only":           lsEdgeAsst(t, "2026-01-02T03:01:00Z", map[string]any{"type": "thinking", "thinking": "hidden thoughts"}),
		"empty content array":     lsEdgeAsst(t, "2026-01-02T03:01:00Z"),
		"content is a string":     `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":{"content":"plain string content"}}`,
		"content is an object":    `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":{"content":{"type":"text","text":"object content"}}}`,
		"content is null":         `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":{"content":null}}`,
		"no message":              `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z"}`,
		"message not an object":   `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":"hello"}`,
		"text block missing text": `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"type":"text"}]}}`,
		"text not a string":       `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"type":"text","text":42}]}}`,
		"block without type":      `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"text":"untyped block"}]}}`,
		"non-object elements":     `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":{"content":["bare string element",7,null]}}`,
		"sidechain true": lsEdgeJSON(t, map[string]any{
			"type": "assistant", "isSidechain": true, "timestamp": "2026-01-02T03:01:00Z",
			"message": map[string]any{"content": []any{lsEdgeText("sidechain words")}},
		}),
		// Amended O1.3: any isSidechain value other than absent or the JSON
		// false disqualifies, including wrongly typed ones. Assumption: an
		// explicit null is a present value, not "absent".
		"sidechain string true":  `{"type":"assistant","isSidechain":"true","timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"type":"text","text":"typed wrong"}]}}`,
		"sidechain string false": `{"type":"assistant","isSidechain":"false","timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"type":"text","text":"typed wrong"}]}}`,
		"sidechain number 0":     `{"type":"assistant","isSidechain":0,"timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"type":"text","text":"typed wrong"}]}}`,
		"sidechain null":         `{"type":"assistant","isSidechain":null,"timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"type":"text","text":"typed wrong"}]}}`,
		"sidechain object":       `{"type":"assistant","isSidechain":{},"timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"type":"text","text":"typed wrong"}]}}`,
		// Amended O1.3: a synthetic banner is never the agent's words.
		"synthetic model": `{"type":"assistant","timestamp":"2026-01-02T03:01:00Z","message":{"model":"<synthetic>","content":[{"type":"text","text":"Prompt is too long"}]}}`,
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			p := lsEdgeWriteTranscript(t,
				lsEdgeSay(t, "2026-01-02T03:00:00Z", "the earlier candidate"),
				line,
				lsEdgeBoundary("2026-01-02T03:02:00Z"),
			)
			lsEdgeWantText(t, p, "the earlier candidate")
		})
	}
}

// Regression (review round 1, A1): a synthetic banner as the LAST assistant
// record — the shape that precedes an emergency compaction — never displaces
// the agent's own last words, whether or not a boundary follows. A real model
// id, a missing model, and a model that merely CONTAINS "synthetic" all count.
func TestLsEdgeLastSaidSyntheticBannerSkipped(t *testing.T) {
	synth := func(text string) string {
		return `{"type":"assistant","timestamp":"2026-01-02T03:05:00Z","message":{"model":"<synthetic>","content":[{"type":"text","text":` + lsEdgeJSON(t, text) + `}]}}`
	}
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "manual test ready: http://127.0.0.1:1/x"),
		synth("Prompt is too long"),
		synth("API Error: an invented failure"),
	)
	lsEdgeWantText(t, p, "manual test ready: http://127.0.0.1:1/x")
	q := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "doc ready"),
		synth("You've reached an invented limit."),
		lsEdgeBoundary("2026-01-02T03:06:00Z"),
	)
	lsEdgeWantText(t, q, "doc ready")
	// Only synthetic records → no candidate.
	lsEdgeWantNone(t, lsEdgeWriteTranscript(t, synth("Prompt is too long")), "synthetic only")
	// Non-synthetic model strings are ordinary candidates.
	for _, model := range []string{"claude-invented-1", "synthetic", "<synthetic> ", "<SYNTHETIC>"} {
		r := lsEdgeWriteTranscript(t,
			lsEdgeSay(t, "2026-01-02T03:00:00Z", "earlier"),
			`{"type":"assistant","timestamp":"2026-01-02T03:05:00Z","message":{"model":`+lsEdgeJSON(t, model)+`,"content":[{"type":"text","text":"real words"}]}}`,
		)
		lsEdgeWantText(t, r, "real words")
	}
}

// isSidechain explicitly false is the same as absent.
func TestLsEdgeLastSaidSidechainFalseCounts(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "earlier"),
		lsEdgeJSON(t, map[string]any{
			"type": "assistant", "isSidechain": false, "timestamp": "2026-01-02T03:01:00Z",
			"message": map[string]any{"content": []any{lsEdgeText("main chain, explicit false")}},
		}),
		lsEdgeBoundary("2026-01-02T03:02:00Z"),
	)
	lsEdgeWantText(t, p, "main chain, explicit false")
}

// Several text blocks in one record: the LAST non-empty one is the text, and
// trailing whitespace-only text blocks and tool_use blocks do not displace it.
func TestLsEdgeLastSaidLastNonEmptyTextBlockWins(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeAsst(t, "2026-01-02T03:00:00Z",
			lsEdgeText("first block"),
			lsEdgeTool(),
			lsEdgeText("middle block"),
			lsEdgeText("  last real block  "),
			lsEdgeText("   "),
			lsEdgeTool(),
			lsEdgeText(""),
		),
		lsEdgeBoundary("2026-01-02T03:02:00Z"),
	)
	lsEdgeWantText(t, p, "last real block")
}

// Text is trimmed but otherwise unchanged and NOT truncated — interior
// newlines and a long body survive.
func TestLsEdgeLastSaidTextTrimmedNotTruncated(t *testing.T) {
	long := strings.Repeat("é", 1000)
	raw := "\n\t  first line\n\n" + long + "\n  \n"
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", raw),
		lsEdgeBoundary("2026-01-02T03:02:00Z"),
	)
	lsEdgeWantText(t, p, "first line\n\n"+long)
}

// --- O1 rule 6 / lastSaid.At: timestamps ------------------------------------

func TestLsEdgeLastSaidBadTimestampsGiveZeroAtButCount(t *testing.T) {
	cases := map[string]string{
		"missing":     lsEdgeAsst(t, "", lsEdgeText("stampless")),
		"unparseable": lsEdgeAsst(t, "yesterday-ish", lsEdgeText("stampless")),
		"empty":       `{"type":"assistant","timestamp":"","message":{"content":[{"type":"text","text":"stampless"}]}}`,
		"number":      `{"type":"assistant","timestamp":1767323045,"message":{"content":[{"type":"text","text":"stampless"}]}}`,
		"null":        `{"type":"assistant","timestamp":null,"message":{"content":[{"type":"text","text":"stampless"}]}}`,
		"object":      `{"type":"assistant","timestamp":{"t":1},"message":{"content":[{"type":"text","text":"stampless"}]}}`,
		"date only":   lsEdgeAsst(t, "2026-01-02", lsEdgeText("stampless")),
	}
	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			p := lsEdgeWriteTranscript(t,
				lsEdgeSay(t, "2026-01-02T03:00:00Z", "earlier stamped"),
				line,
				lsEdgeBoundary("2026-01-02T03:02:00Z"),
			)
			ls := lsEdgeWantText(t, p, "stampless")
			if !ls.At.IsZero() {
				t.Fatalf("At = %v, want zero for a %s timestamp", ls.At, name)
			}
		})
	}
}

// Only the TOP-LEVEL timestamp is read; a timestamp nested in message does
// not stand in for a missing one.
func TestLsEdgeLastSaidNestedTimestampIgnored(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		`{"type":"assistant","message":{"timestamp":"2026-01-02T03:00:00Z","content":[{"type":"text","text":"nested stamp"}]}}`,
		lsEdgeBoundary("2026-01-02T03:02:00Z"),
	)
	ls := lsEdgeWantText(t, p, "nested stamp")
	if !ls.At.IsZero() {
		t.Fatalf("At = %v, want zero: only the top-level timestamp key counts", ls.At)
	}
}

// An offset timestamp is the same instant, reported in UTC.
func TestLsEdgeLastSaidOffsetTimestampIsUTC(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T05:04:05.250+02:00", "offset stamp"),
		lsEdgeBoundary("2026-01-02T03:10:00Z"),
	)
	ls := lsEdgeWantText(t, p, "offset stamp")
	want := time.Date(2026, 1, 2, 3, 4, 5, 250_000_000, time.UTC)
	if !ls.At.Equal(want) {
		t.Fatalf("At = %v, want %v", ls.At, want)
	}
	if ls.At.Location() != time.UTC {
		t.Fatalf("At location = %v, want UTC", ls.At.Location())
	}
}

// --- O1 rule 6: user and other record types never count ---------------------

func TestLsEdgeLastSaidUserRecordsNeverCount(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "assistant said this"),
		lsEdgeUser(t, "2026-01-02T03:01:00Z", "user said this later", nil),
		lsEdgeBoundary("2026-01-02T03:02:00Z"),
		lsEdgeUser(t, "2026-01-02T03:02:01Z", "This session is being continued from a previous conversation...", map[string]any{"isCompactSummary": true}),
	)
	lsEdgeWantText(t, p, "assistant said this")
}

func TestLsEdgeLastSaidOtherTypesNeverCount(t *testing.T) {
	others := []string{"queue-operation", "attachment", "file-history-snapshot", "summary", "system", "ASSISTANT", "assistant ", ""}
	for _, typ := range others {
		t.Run("type="+typ, func(t *testing.T) {
			line := lsEdgeJSON(t, map[string]any{
				"type": typ, "timestamp": "2026-01-02T03:01:00Z",
				"message": map[string]any{"role": "assistant", "content": []any{lsEdgeText("imposter text")}},
			})
			p := lsEdgeWriteTranscript(t,
				lsEdgeSay(t, "2026-01-02T03:00:00Z", "genuine"),
				line,
				lsEdgeBoundary("2026-01-02T03:02:00Z"),
			)
			lsEdgeWantText(t, p, "genuine")
		})
	}
	// A record with no type key at all.
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "genuine"),
		`{"timestamp":"2026-01-02T03:01:00Z","message":{"content":[{"type":"text","text":"typeless"}]}}`,
	)
	lsEdgeWantText(t, p, "genuine")
}

// --- O2: runHandoffHook fallbacks -------------------------------------------

// transcript_path absent / empty / not a string → byte-identical to the
// no-transcript run, with docs and without.
func TestLsEdgeHookNonStringTranscriptPathIsBaseline(t *testing.T) {
	for _, withDocs := range []bool{true, false} {
		dir := t.TempDir()
		if withDocs {
			dir = lsEdgeDocDir(t)
		}
		base := lsEdgeRun(t, lsEdgeBasePayload, dir)
		if base == "" {
			t.Fatalf("baseline compact run wrote nothing (withDocs=%v)", withDocs)
		}
		valid := lsEdgeValidTranscript(t)
		cases := map[string]string{
			"empty string":         `""`,
			"number":               `42`,
			"object":               `{"path":` + lsEdgeJSON(t, valid) + `}`,
			"array":                `[` + lsEdgeJSON(t, valid) + `]`,
			"null":                 `null`,
			"bool":                 `true`,
			"missing file":         lsEdgeJSON(t, filepath.Join(t.TempDir(), "absent.jsonl")),
			"directory":            lsEdgeJSON(t, t.TempDir()),
			"no answer":            lsEdgeJSON(t, lsEdgeWriteTranscript(t, lsEdgeBoundary("2026-01-02T03:00:00Z"), lsEdgeUser(t, "2026-01-02T03:01:00Z", "user only", nil))),
			"one byte file":        lsEdgeJSON(t, lsEdgeWriteRaw(t, []byte("x"))),
			"only newlines file":   lsEdgeJSON(t, lsEdgeWriteRaw(t, []byte("\n\n\n"))),
			"binary junk file":     lsEdgeJSON(t, lsEdgeWriteRaw(t, []byte{0xff, 0xfe, 0x00, '{', 0x80, '\n', 0xc3, 0x28, '"', '\n'})),
			"whitespace only path": `"   "`,
		}
		for name, raw := range cases {
			t.Run(name, func(t *testing.T) {
				got := lsEdgeRun(t, lsEdgePayloadRaw(raw), dir)
				if got != base {
					t.Fatalf("transcript_path %s (withDocs=%v):\n got %q\nwant %q (byte-identical to the no-transcript run)", name, withDocs, got, base)
				}
			})
		}
		// Absent key, with other unrelated keys present.
		got := lsEdgeRun(t, `{"session_id":"s-edge","source":"compact","cwd":"/tmp/x","hook_event_name":"SessionStart"}`, dir)
		if got != base {
			t.Fatalf("absent transcript_path (withDocs=%v):\n got %q\nwant %q", withDocs, got, base)
		}
	}
}

// The source gate runs first: a valid transcript does not rescue a
// non-compact source.
func TestLsEdgeHookNonCompactSourceSilentEvenWithTranscript(t *testing.T) {
	valid := lsEdgeJSON(t, lsEdgeValidTranscript(t))
	dir := lsEdgeDocDir(t)
	for _, src := range []string{`"resume"`, `"startup"`, `"clear"`, `""`, `"COMPACT"`, `42`, `null`, `{"x":1}`} {
		t.Run(src, func(t *testing.T) {
			payload := `{"session_id":"s-edge","source":` + src + `,"transcript_path":` + valid + `}`
			if out := lsEdgeRun(t, payload, dir); out != "" {
				t.Fatalf("source %s with a valid transcript wrote %q, want nothing", src, out)
			}
		})
	}
}

// An ABSENT source is compact (baseline gate unchanged), so the line appears.
func TestLsEdgeHookAbsentSourceStillInjectsLine(t *testing.T) {
	p := lsEdgeValidTranscript(t)
	ctx := lsEdgeContext(t, lsEdgeRun(t, `{"session_id":"s-edge","transcript_path":`+lsEdgeJSON(t, p)+`}`, t.TempDir()))
	ls, ok := lastSaidBefore(p)
	if !ok {
		t.Fatal("fixture transcript has no answer")
	}
	if want := handoffStandingRules + "\n\n" + handoffLastSaidLine(ls, lsEdgeNow); ctx != want {
		t.Fatalf("absent source context =\n%q\nwant\n%q", ctx, want)
	}
}

func TestLsEdgeHookBadStdinSilent(t *testing.T) {
	valid := lsEdgeJSON(t, lsEdgeValidTranscript(t))
	dir := lsEdgeDocDir(t)
	cases := map[string]string{
		"empty":      "",
		"whitespace": " \n\t",
		"not json":   "transcript_path=" + valid,
		"truncated":  `{"source":"compact","transcript_path":` + valid,
		"array":      `[{"source":"compact","transcript_path":` + valid + `}]`,
		"string":     lsEdgeJSON(t, valid),
		"null":       "null",
	}
	for name, stdin := range cases {
		t.Run(name, func(t *testing.T) {
			if out := lsEdgeRun(t, stdin, dir); out != "" {
				t.Fatalf("stdin %q wrote %q, want nothing", stdin, out)
			}
		})
	}
}

// With an answer and NO docs, the context is exactly rules + blank line + the
// last-said line — no trailing docs header, no closing, no trailing newline.
func TestLsEdgeHookLineWithoutDocsHasNoTail(t *testing.T) {
	p := lsEdgeValidTranscript(t)
	ls, ok := lastSaidBefore(p)
	if !ok {
		t.Fatal("fixture transcript has no answer")
	}
	for name, dir := range map[string]string{
		"empty dir":   t.TempDir(),
		"missing dir": filepath.Join(t.TempDir(), "nope"),
		"empty arg":   "",
	} {
		t.Run(name, func(t *testing.T) {
			ctx := lsEdgeContext(t, lsEdgeRun(t, lsEdgePayloadRaw(lsEdgeJSON(t, p)), dir))
			if want := handoffStandingRules + "\n\n" + handoffLastSaidLine(ls, lsEdgeNow); ctx != want {
				t.Fatalf("context =\n%q\nwant\n%q", ctx, want)
			}
		})
	}
}

// The line rides between the rules and the UNCHANGED docs section: removing
// "\n\n"+line from the transcript run reproduces the baseline context exactly.
func TestLsEdgeHookDocsSectionUnchangedByLine(t *testing.T) {
	dir := lsEdgeDocDir(t)
	p := lsEdgeValidTranscript(t)
	ls, ok := lastSaidBefore(p)
	if !ok {
		t.Fatal("fixture transcript has no answer")
	}
	base := lsEdgeContext(t, lsEdgeRun(t, lsEdgeBasePayload, dir))
	got := lsEdgeContext(t, lsEdgeRun(t, lsEdgePayloadRaw(lsEdgeJSON(t, p)), dir))
	line := handoffLastSaidLine(ls, lsEdgeNow)
	if !strings.HasPrefix(got, handoffStandingRules+"\n\n"+line+"\n\nHandoff docs in this cell's artifacts dir, newest first:\n") {
		t.Fatalf("context does not open rules + line + docs header:\n%q", got)
	}
	if stripped := strings.Replace(got, "\n\n"+line, "", 1); stripped != base {
		t.Fatalf("context minus the line =\n%q\nwant baseline\n%q", stripped, base)
	}
}

// SetEscapeHTML(false) survives: «, », —, <, >, & appear raw in the bytes.
func TestLsEdgeHookOutputBytesUnescaped(t *testing.T) {
	p := lsEdgeWriteTranscript(t,
		lsEdgeSay(t, "2026-01-02T03:00:00Z", "ready <b>now</b> & a>b"),
		lsEdgeBoundary("2026-01-02T03:02:00Z"),
	)
	out := lsEdgeRun(t, lsEdgePayloadRaw(lsEdgeJSON(t, p)), t.TempDir())
	for _, s := range []string{"«", "»", "—", "<b>now</b>", "& a>b"} {
		if !strings.Contains(out, s) {
			t.Fatalf("raw output lacks unescaped %q:\n%s", s, out)
		}
	}
	for _, esc := range []string{`\u003c`, `\u003e`, `\u0026`, `\u00ab`, `\u00bb`, `\u2014`} {
		if strings.Contains(out, esc) {
			t.Fatalf("raw output carries escape %s:\n%s", esc, out)
		}
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("output must be one JSON object plus one newline, got %d newlines:\n%q", strings.Count(out, "\n"), out)
	}
}

// A transcript whose only line is over 16 MiB is a "false" transcript: the
// hook falls back to the baseline bytes, with no panic and no stderr.
func TestLsEdgeHookGiantLineTranscriptIsBaseline(t *testing.T) {
	dir := lsEdgeDocDir(t)
	base := lsEdgeRun(t, lsEdgeBasePayload, dir)
	p := lsEdgeWriteTranscript(t, lsEdgeSay(t, "2026-01-02T03:00:00Z", strings.Repeat("q", 17<<20)), lsEdgeBoundary("2026-01-02T03:02:00Z"))
	if got := lsEdgeRun(t, lsEdgePayloadRaw(lsEdgeJSON(t, p)), dir); got != base {
		t.Fatalf("giant-line transcript:\n got %q\nwant baseline %q", got, base)
	}
}

// --- O3: handoffLastSaidLine boundaries ---------------------------------------

var lsEdgeAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const lsEdgeWhenAt = "(2026-01-02 03:04 UTC)"

func lsEdgeLine(t *testing.T, ls lastSaid, now time.Time) string {
	t.Helper()
	got := handoffLastSaidLine(ls, now)
	if strings.Contains(got, "\n") {
		t.Fatalf("handoffLastSaidLine returned more than one line: %q", got)
	}
	return got
}

func TestLsEdgeLineExactly240RunesUnchanged(t *testing.T) {
	for name, unit := range map[string]string{"ascii": "a", "two-byte": "é", "three-byte": "日", "four-byte": "𝄞"} {
		t.Run(name, func(t *testing.T) {
			first := strings.Repeat(unit, 240)
			got := lsEdgeLine(t, lastSaid{Text: first + "\nsecond line", At: lsEdgeAt}, lsEdgeAt)
			if want := lsEdgeWantLine("0m ago "+lsEdgeWhenAt, first); got != want {
				t.Fatalf("240-rune first line altered:\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestLsEdgeLine241RunesCutTo240PlusEllipsis(t *testing.T) {
	for name, unit := range map[string]string{"ascii": "a", "two-byte": "é", "three-byte": "日", "four-byte": "𝄞"} {
		t.Run(name, func(t *testing.T) {
			got := lsEdgeLine(t, lastSaid{Text: strings.Repeat(unit, 241), At: lsEdgeAt}, lsEdgeAt)
			if want := lsEdgeWantLine("0m ago "+lsEdgeWhenAt, strings.Repeat(unit, 240)+"…"); got != want {
				t.Fatalf("241-rune first line:\n got %q\nwant %q", got, want)
			}
		})
	}
}

// Rune counting on mixed widths: 239 ASCII + one 4-byte rune is 240 runes
// (243 bytes) and must NOT be cut; one more ASCII rune makes it 241.
func TestLsEdgeLineMixedWidthCountsRunes(t *testing.T) {
	first := strings.Repeat("b", 239) + "😀"
	if got, want := lsEdgeLine(t, lastSaid{Text: first, At: lsEdgeAt}, lsEdgeAt), lsEdgeWantLine("0m ago "+lsEdgeWhenAt, first); got != want {
		t.Fatalf("240-rune mixed line altered:\n got %q\nwant %q", got, want)
	}
	over := first + "c"
	if got, want := lsEdgeLine(t, lastSaid{Text: over, At: lsEdgeAt}, lsEdgeAt), lsEdgeWantLine("0m ago "+lsEdgeWhenAt, first+"…"); got != want {
		t.Fatalf("241-rune mixed line:\n got %q\nwant %q", got, want)
	}
}

// The first line is trimmed BEFORE it is measured: 240 runes padded with
// spaces is not over the limit.
func TestLsEdgeLineTrimBeforeMeasure(t *testing.T) {
	core := strings.Repeat("é", 240)
	got := lsEdgeLine(t, lastSaid{Text: "   " + core + " \t\nrest", At: lsEdgeAt}, lsEdgeAt)
	if want := lsEdgeWantLine("0m ago "+lsEdgeWhenAt, core); got != want {
		t.Fatalf("padded 240-rune line:\n got %q\nwant %q", got, want)
	}
}

// Only the FIRST line is truncated; a giant second line does not matter.
func TestLsEdgeLineLongLaterLinesIgnored(t *testing.T) {
	got := lsEdgeLine(t, lastSaid{Text: "short head\n" + strings.Repeat("z", 5000), At: lsEdgeAt}, lsEdgeAt)
	if want := lsEdgeWantLine("0m ago "+lsEdgeWhenAt, "short head"); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// Contract silence: a first line that is only whitespace. Read literally,
// <FIRST> is "everything before the first \n, trimmed" = "", so the guillemets
// enclose nothing — assumed; no fallback to a later line is specified.
func TestLsEdgeLineWhitespaceOnlyFirstLine(t *testing.T) {
	got := lsEdgeLine(t, lastSaid{Text: " \t \nthe second line", At: lsEdgeAt}, lsEdgeAt)
	if want := lsEdgeWantLine("0m ago "+lsEdgeWhenAt, ""); got != want {
		t.Fatalf("whitespace-only first line:\n got %q\nwant %q", got, want)
	}
}

func TestLsEdgeLineNoNewlineUsesWholeText(t *testing.T) {
	got := lsEdgeLine(t, lastSaid{Text: "  doc ready: 00-overview.md  ", At: lsEdgeAt}, lsEdgeAt)
	if want := lsEdgeWantLine("0m ago "+lsEdgeWhenAt, "doc ready: 00-overview.md"); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// A CRLF first line: the \r is whitespace and is trimmed with the rest.
func TestLsEdgeLineCRLFTrimmed(t *testing.T) {
	got := lsEdgeLine(t, lastSaid{Text: "gate line\r\nnext", At: lsEdgeAt}, lsEdgeAt)
	if want := lsEdgeWantLine("0m ago "+lsEdgeWhenAt, "gate line"); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestLsEdgeLineAgeBoundaries(t *testing.T) {
	cases := map[string]struct {
		now  time.Time
		want string
	}{
		"same instant":       {lsEdgeAt, "0m ago"},
		"59s":                {lsEdgeAt.Add(59 * time.Second), "0m ago"},
		"exactly 1m":         {lsEdgeAt.Add(time.Minute), "1m ago"},
		"59m59s floors":      {lsEdgeAt.Add(59*time.Minute + 59*time.Second), "59m ago"},
		"3 days in minutes":  {lsEdgeAt.Add(72 * time.Hour), "4320m ago"},
		"now 30s before At":  {lsEdgeAt.Add(-30 * time.Second), "0m ago"},
		"now 2h before At":   {lsEdgeAt.Add(-2 * time.Hour), "0m ago"},
		"now a year earlier": {lsEdgeAt.AddDate(-1, 0, 0), "0m ago"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := lsEdgeLine(t, lastSaid{Text: "words", At: lsEdgeAt}, c.now)
			if want := lsEdgeWantLine(c.want+" "+lsEdgeWhenAt, "words"); got != want {
				t.Fatalf("got %q\nwant %q", got, want)
			}
		})
	}
}

// <TS> is At in UTC, minute precision, whatever zone At carries and whatever
// zone now carries.
func TestLsEdgeLineTimestampAlwaysUTC(t *testing.T) {
	zone := time.FixedZone("edge+0530", 5*3600+30*60)
	at := time.Date(2026, 1, 2, 8, 34, 59, 999, zone) // = 03:04:59.000000999 UTC
	now := time.Date(2026, 1, 2, 3, 14, 0, 0, time.UTC).In(time.FixedZone("edge-0800", -8*3600))
	got := lsEdgeLine(t, lastSaid{Text: "zoned", At: at}, now)
	if want := lsEdgeWantLine("9m ago (2026-01-02 03:04 UTC)", "zoned"); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// Zero At → the literal "at an unknown time", whatever now is.
func TestLsEdgeLineZeroAt(t *testing.T) {
	for name, now := range map[string]time.Time{"real now": lsEdgeNow, "zero now": {}} {
		t.Run(name, func(t *testing.T) {
			got := lsEdgeLine(t, lastSaid{Text: "unstamped\nmore"}, now)
			if want := lsEdgeWantLine("at an unknown time", "unstamped"); got != want {
				t.Fatalf("got %q\nwant %q", got, want)
			}
			if strings.Contains(got, "ago") || strings.Contains(got, "UTC") {
				t.Fatalf("zero At must not render an age or a timestamp: %q", got)
			}
		})
	}
}

// Empty Text (only reachable by a direct call) does not panic and yields «».
// Contract silence: assumed the same literal reading as the whitespace case.
func TestLsEdgeLineEmptyText(t *testing.T) {
	got := lsEdgeLine(t, lastSaid{}, lsEdgeNow)
	if want := lsEdgeWantLine("at an unknown time", ""); got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}
