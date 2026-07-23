package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// prscan_test.go — the transcript-scanning PR backstop's pure pieces:
// scanTranscriptPRURLs (never touches the daemon, gh, or a live cell) and
// transcriptPRURLs' size+mtime memoization, mirroring status.go's
// pendingQuestion cache test. Since the scan is create-call-scoped, most
// cases here pair a `gh pr create` tool_use line with its tool_result line,
// the same way real transcripts do.
//
// Fixture lines are built via json.Marshal (never raw string-splicing) so a
// command or content value containing a literal `"` — e.g. `gh pr create
// --body "see …/pull/99"` — still produces valid, parseable JSON instead of
// silently corrupting the line and making the case pass vacuously.

const prScanPrefix = "github.com/carl/kraken"

func prScanWritePRTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func prScanMarshalLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// prScanToolUseLine is an assistant tool_use content block: a Bash call (or
// any other tool, via name) with the given command. Marshaled, not spliced,
// so a command containing quotes (e.g. `gh pr create --body "see …"`) still
// produces a valid JSON line.
func prScanToolUseLine(t *testing.T, id, name, command string) string {
	t.Helper()
	return prScanMarshalLine(t, map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role": "assistant",
			"content": []map[string]any{{
				"type": "tool_use",
				"id":   id,
				"name": name,
				"input": map[string]any{
					"command": command,
				},
			}},
		},
	})
}

// prScanToolResultLine is a user tool_result content block whose content is
// the plain-string form.
func prScanToolResultLine(t *testing.T, id, content string) string {
	t.Helper()
	return prScanMarshalLine(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     content,
			}},
		},
	})
}

// prScanToolResultBlocksLine is a user tool_result whose content is the
// list-of-text-blocks form.
func prScanToolResultBlocksLine(t *testing.T, id string, texts ...string) string {
	t.Helper()
	var blocks []map[string]any
	for _, txt := range texts {
		blocks = append(blocks, map[string]any{"type": "text", "text": txt})
	}
	return prScanMarshalLine(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     blocks,
			}},
		},
	})
}

// prScanToolResultWithToolUseResultLine is a user tool_result carrying an
// empty content block but a top-level toolUseResult object (the
// {stdout,stderr} shape).
func prScanToolResultWithToolUseResultLine(t *testing.T, id, stdout, stderr string) string {
	t.Helper()
	return prScanMarshalLine(t, map[string]any{
		"type": "user",
		"message": map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type":        "tool_result",
				"tool_use_id": id,
				"content":     "",
			}},
		},
		"toolUseResult": map[string]any{
			"stdout": stdout,
			"stderr": stderr,
		},
	})
}

func TestScanTranscriptPRURLs(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{
			name: "create call result URL attaches (string content)",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", "gh pr create --title x"),
				prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/42"),
			},
			want: []string{"https://github.com/carl/kraken/pull/42"},
		},
		{
			name: "create call result URL attaches (list-of-text-blocks content)",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", "gh pr create --title x"),
				prScanToolResultBlocksLine(t, "t1", "Warning: 1 uncommitted change", "https://github.com/carl/kraken/pull/43"),
			},
			want: []string{"https://github.com/carl/kraken/pull/43"},
		},
		{
			name: "create call result URL attaches (toolUseResult stdout only)",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", "gh pr create --title x"),
				prScanToolResultWithToolUseResultLine(t, "t1", "https://github.com/carl/kraken/pull/44", ""),
			},
			want: []string{"https://github.com/carl/kraken/pull/44"},
		},
		{
			name: "foreign tool_result ignored (fixture/log-dump incident shape)",
			lines: []string{
				prScanToolUseLine(t, "t9", "Read", "test_fixture.json"),
				prScanToolResultLine(t, "t9", "fixture content quoting https://github.com/carl/kraken/pull/7"),
			},
			want: nil,
		},
		{
			name: "URL in create command but not result yields nothing",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", `gh pr create --body "see https://github.com/carl/kraken/pull/99"`),
				prScanToolResultLine(t, "t1", "Warning: 1 uncommitted change"),
			},
			want: nil,
		},
		{
			name: "gh pr create merely quoted in a grep command is not an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", `grep -n "gh pr create" jobs/prpoll.go`),
				prScanToolResultLine(t, "t1", "prpoll.go:42: exec.Command(\"gh\", \"pr\", \"create\", ...)\nhttps://github.com/carl/kraken/pull/50"),
			},
			want: nil,
		},
		{
			name: "leading env-assignment prefix form is still an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", "GH_TOKEN=x gh pr create --title x"),
				prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/51"),
			},
			want: []string{"https://github.com/carl/kraken/pull/51"},
		},
		{
			// THE regression case: the old \S+ env-value match broke on the
			// space inside $(...), missing 11/126 real creates in this shape.
			name: "env-value command substitution containing a space is still an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", `GH_TOKEN=$(gh auth token --user car1zhang) gh pr create --title "x"`),
				prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/60"),
			},
			want: []string{"https://github.com/carl/kraken/pull/60"},
		},
		{
			name: "cd into a quoted var then && env-substitution create is an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", `cd "$WT" && GH_TOKEN=$(gh auth token -u car1zhang) gh pr create --fill`),
				prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/61"),
			},
			want: []string{"https://github.com/carl/kraken/pull/61"},
		},
		{
			name: "piped push then && env-substitution create is an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", `git push -u origin carl/x 2>&1 | tail -2 && GH_TOKEN=$(gh auth token --user carl-probook) gh pr create --title y`),
				prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/62"),
			},
			want: []string{"https://github.com/carl/kraken/pull/62"},
		},
		{
			name: "grep -n quoting the literal is still not an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", `grep -n "gh pr create" jobs/prpoll.go`),
				prScanToolResultLine(t, "t1", "prpoll.go:42: exec.Command(\"gh\", \"pr\", \"create\", ...)\nhttps://github.com/carl/kraken/pull/63"),
			},
			want: nil,
		},
		{
			name: "rg quoting the literal is not an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", `rg 'gh pr create'`),
				prScanToolResultLine(t, "t1", "jobs/prscan.go:38:// gh pr create\nhttps://github.com/carl/kraken/pull/64"),
			},
			want: nil,
		},
		{
			name: "echo quoting the literal is not an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", `echo "gh pr create"`),
				prScanToolResultLine(t, "t1", "gh pr create\nhttps://github.com/carl/kraken/pull/65"),
			},
			want: nil,
		},
		{
			name: "comment-line mention at start of command is not an invocation",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", "# gh pr create\necho done"),
				prScanToolResultLine(t, "t1", "done\nhttps://github.com/carl/kraken/pull/66"),
			},
			want: nil,
		},
		{
			name: "gh pr createx is not an invocation (word-boundary check)",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", "gh pr createx --title x"),
				prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/67"),
			},
			want: nil,
		},
		{
			name: "interleaved create calls: all results, first-seen order, deduped",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", "gh pr create --title first"),
				prScanToolUseLine(t, "t2", "Bash", "gh pr create --title second"),
				prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/5"),
				prScanToolResultLine(t, "t2", "https://github.com/carl/kraken/pull/6"),
				prScanToolUseLine(t, "t3", "Bash", "gh pr create --title again"),
				prScanToolResultLine(t, "t3", "https://github.com/carl/kraken/pull/5"),
			},
			want: []string{"https://github.com/carl/kraken/pull/5", "https://github.com/carl/kraken/pull/6"},
		},
		{
			name: "foreign-repo URL in a create result filtered when prefix set",
			lines: []string{
				prScanToolUseLine(t, "t1", "Bash", "gh pr create --title x"),
				prScanToolResultLine(t, "t1", "https://github.com/other/repo/pull/9"),
			},
			want: nil,
		},
		{
			name:  "malformed JSON lines skipped without error",
			lines: []string{"not json at all", `{"broken`, ""},
			want:  nil,
		},
		{
			name: "malformed line alongside a real create call still yields its URL",
			lines: []string{
				"not json at all",
				prScanToolUseLine(t, "t1", "Bash", "gh pr create --title x"),
				`{"broken`,
				prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/8"),
			},
			want: []string{"https://github.com/carl/kraken/pull/8"},
		},
		{
			// Guards against b.ID == "" ever entering the pending set: if it
			// did, an unrelated tool_result whose own tool_use_id is also
			// missing/empty would spuriously pair with it via pending[""] and
			// get scanned, misattaching whatever own-repo URL it happens to
			// carry.
			name: "empty-id create call and empty-id tool_result never pair up",
			lines: []string{
				prScanToolUseLine(t, "", "Bash", "gh pr create --title x"),
				prScanToolResultLine(t, "", "https://github.com/carl/kraken/pull/9"),
			},
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := prScanWritePRTranscript(t, c.lines...)
			got := scanTranscriptPRURLs(path, prScanPrefix)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestScanTranscriptPRURLsEmptyPrefixMatchesAnyRepo(t *testing.T) {
	// Empty prefix matching any repo is acceptable HERE — unlike the hook,
	// which fails open on an unreadable origin remote — because its
	// production callers (scanTranscriptPRs and repairPRs in daemon.go) both
	// enforce the own-repo-only, no-fail-open rule by requiring a resolved
	// prefix: neither ever calls in with "". This test documents the
	// primitive's own behavior in isolation.
	path := prScanWritePRTranscript(t,
		prScanToolUseLine(t, "t1", "Bash", "gh pr create --title x"),
		prScanToolResultLine(t, "t1", "https://github.com/other/repo/pull/9"),
	)
	got := scanTranscriptPRURLs(path, "")
	want := []string{"https://github.com/other/repo/pull/9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestScanTranscriptPRURLsMissingFile(t *testing.T) {
	if got := scanTranscriptPRURLs(filepath.Join(t.TempDir(), "nope.jsonl"), prScanPrefix); got != nil {
		t.Errorf("missing transcript should scan to nil, got %v", got)
	}
}

func TestScanTranscriptPRURLsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := scanTranscriptPRURLs(path, prScanPrefix); got != nil {
		t.Errorf("empty transcript should scan to nil, got %v", got)
	}
}

func TestScanTranscriptPRURLsHugeLine(t *testing.T) {
	// bufio.Scanner's default 64KB buffer would choke on this without an
	// explicit larger buffer; pad well past it, same as
	// TestPendingQuestionHugeLine in status_test.go.
	pad := strings.Repeat("x", 200*1024)
	path := prScanWritePRTranscript(t,
		prScanToolUseLine(t, "t1", "Bash", "gh pr create --title x"),
		prScanToolResultLine(t, "t1", pad+" https://github.com/carl/kraken/pull/5 "+pad),
	)
	got := scanTranscriptPRURLs(path, prScanPrefix)
	want := []string{"https://github.com/carl/kraken/pull/5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestScanTranscriptPRURLsOverSixteenMBLineTruncatesButKeepsPriorURLs(t *testing.T) {
	// Documented truncation behavior: sc.Buffer caps at 16MB (matching
	// scanPendingQuestion's ceiling), so a line beyond that ends the scan
	// right there — best-effort, not the sole record. A URL seen before the
	// oversized line stands; a create whose result lands after it is lost.
	oversized := strings.Repeat("x", 17*1024*1024)
	path := prScanWritePRTranscript(t,
		prScanToolUseLine(t, "t1", "Bash", "gh pr create --title first"),
		prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/1"),
		prScanToolUseLine(t, "t2", "Bash", "gh pr create --title second"),
		"// "+oversized,
		prScanToolResultLine(t, "t2", "https://github.com/carl/kraken/pull/2"),
	)
	got := scanTranscriptPRURLs(path, prScanPrefix)
	want := []string{"https://github.com/carl/kraken/pull/1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTranscriptPRURLsMemoizedByStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")

	pair1 := prScanToolUseLine(t, "t1", "Bash", "gh pr create --title first") + "\n" +
		prScanToolResultLine(t, "t1", "https://github.com/carl/kraken/pull/1") + "\n"
	pair2 := prScanToolUseLine(t, "t2", "Bash", "gh pr create --title second") + "\n" +
		prScanToolResultLine(t, "t2", "https://github.com/carl/kraken/pull/2") + "\n"
	one := pair1
	two := pair1 + pair2
	// pad "one" to the same byte length as "two" so a cache hit is
	// distinguishable from a genuine rescan: only (size, mtime) changes
	// between the two writes below, not this one. The padding is trailing
	// whitespace outside any JSON line, so it decodes to nothing.
	if len(two) < len(one) {
		t.Fatal("test fixture assumption violated: two should be longer")
	}
	one += strings.Repeat(" ", len(two)-len(one))
	if len(one) != len(two) {
		t.Fatalf("padding failed: %d != %d", len(one), len(two))
	}

	mtime := time.Now()
	if err := os.WriteFile(path, []byte(one), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	got1 := transcriptPRURLs(path, prScanPrefix)
	want1 := []string{"https://github.com/carl/kraken/pull/1"}
	if !reflect.DeepEqual(got1, want1) {
		t.Fatalf("got %v, want %v", got1, want1)
	}

	// overwrite with the second URL added but keep size and mtime identical:
	// the cache must return the stale one-URL result, proving it didn't rescan.
	if err := os.WriteFile(path, []byte(two), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	got2 := transcriptPRURLs(path, prScanPrefix)
	if !reflect.DeepEqual(got2, want1) {
		t.Fatalf("unchanged (size, mtime) should return the cached result, got %v", got2)
	}

	// bump the mtime: the cache must invalidate and rescan, now seeing both URLs.
	newMtime := mtime.Add(time.Second)
	if err := os.Chtimes(path, newMtime, newMtime); err != nil {
		t.Fatal(err)
	}
	got3 := transcriptPRURLs(path, prScanPrefix)
	want3 := []string{"https://github.com/carl/kraken/pull/1", "https://github.com/carl/kraken/pull/2"}
	if !reflect.DeepEqual(got3, want3) {
		t.Fatalf("changed mtime should invalidate the cache and rescan, got %v", got3)
	}
}

// --- prScanContentText / prScanResultText: direct table tests ---

func TestPrScanContentText(t *testing.T) {
	cases := []struct {
		name    string
		content string // raw JSON
		want    string
	}{
		{"empty", "", ""},
		{"string form", `"hello world"`, "hello world"},
		{"list form, single text block", `[{"type":"text","text":"hello"}]`, "hello\n"},
		{"list form, multiple text blocks joined by newline", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb\n"},
		{"list form, non-text block skipped", `[{"type":"image","text":"ignored"},{"type":"text","text":"kept"}]`, "kept\n"},
		{"neither shape decodes to empty", `12345`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := prScanContentText(json.RawMessage(c.content))
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestPrScanResultText(t *testing.T) {
	cases := []struct {
		name          string
		content       string // raw JSON
		toolUseResult string // raw JSON
		want          string
	}{
		{
			name:          "content only",
			content:       `"hello"`,
			toolUseResult: "",
			want:          "hello\n\n",
		},
		{
			name:          "stdout/stderr only",
			content:       "",
			toolUseResult: `{"stdout":"out","stderr":"err"}`,
			want:          "\nout\nerr",
		},
		{
			name:          "content and stdout joined with newline, no splice",
			content:       `"foo"`,
			toolUseResult: `{"stdout":"bar","stderr":""}`,
			want:          "foo\nbar\n",
		},
		{
			name:          "boundary never splices: content and stdout land on distinct lines",
			content:       `"prefix-"`,
			toolUseResult: `{"stdout":"suffix","stderr":""}`,
			// Without the "\n" join these would concatenate into
			// "prefix-suffix", which could fuse two half-URLs (one ending a
			// content block, one starting stdout) into a single lookalike
			// match that neither half actually contains.
			want: "prefix-\nsuffix\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := prScanResultText(json.RawMessage(c.content), json.RawMessage(c.toolUseResult))
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
