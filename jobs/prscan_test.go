package jobs

import (
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
// pendingQuestion cache test.

const prScanPrefix = "github.com/carl/kraken"

func writePRTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assistantTextLine(text string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
}

func toolResultTextLine(text string) string {
	return `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"` + text + `"}]}}`
}

func TestScanTranscriptPRURLs(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  []string
	}{
		{
			name:  "URL in assistant text line",
			lines: []string{assistantTextLine("opened https://github.com/carl/kraken/pull/42")},
			want:  []string{"https://github.com/carl/kraken/pull/42"},
		},
		{
			name:  "URL inside a tool_result JSON line",
			lines: []string{toolResultTextLine("gh pr create output:\\nhttps://github.com/carl/kraken/pull/7")},
			want:  []string{"https://github.com/carl/kraken/pull/7"},
		},
		{
			name:  "other-repo URL excluded",
			lines: []string{assistantTextLine("see https://github.com/other/repo/pull/9")},
			want:  nil,
		},
		{
			name: "dedup across repeated mentions",
			lines: []string{
				assistantTextLine("opened https://github.com/carl/kraken/pull/1"),
				assistantTextLine("as discussed, https://github.com/carl/kraken/pull/1 is ready"),
			},
			want: []string{"https://github.com/carl/kraken/pull/1"},
		},
		{
			name: "first-seen order",
			lines: []string{
				assistantTextLine("first https://github.com/carl/kraken/pull/2"),
				assistantTextLine("then https://github.com/carl/kraken/pull/1"),
				assistantTextLine("again https://github.com/carl/kraken/pull/2"),
			},
			want: []string{"https://github.com/carl/kraken/pull/2", "https://github.com/carl/kraken/pull/1"},
		},
		{
			name:  "empty transcript",
			lines: []string{""},
			want:  nil,
		},
		{
			name:  "garbage non-JSON line",
			lines: []string{"not json at all, no url here either"},
			want:  nil,
		},
		{
			name:  "garbage line still yields a raw URL match",
			lines: []string{"not json but has https://github.com/carl/kraken/pull/3 anyway"},
			want:  []string{"https://github.com/carl/kraken/pull/3"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writePRTranscript(t, c.lines...)
			got := scanTranscriptPRURLs(path, prScanPrefix)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestScanTranscriptPRURLsMissingFile(t *testing.T) {
	if got := scanTranscriptPRURLs(filepath.Join(t.TempDir(), "nope.jsonl"), prScanPrefix); got != nil {
		t.Errorf("missing transcript should scan to nil, got %v", got)
	}
}

func TestScanTranscriptPRURLsHugeLine(t *testing.T) {
	// bufio.Scanner's default 64KB buffer would choke on this without an
	// explicit larger buffer; pad well past it, same as
	// TestPendingQuestionHugeLine in status_test.go.
	pad := strings.Repeat("x", 200*1024)
	huge := toolResultTextLine(pad + " https://github.com/carl/kraken/pull/5 " + pad)
	path := writePRTranscript(t, huge)
	got := scanTranscriptPRURLs(path, prScanPrefix)
	want := []string{"https://github.com/carl/kraken/pull/5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestScanTranscriptPRURLsEmptyPrefixMatchesNothingUseful(t *testing.T) {
	// scanTranscriptPRURLs itself follows matchPRURLs' general semantics
	// (empty prefix matches everything) — the "own-repo only, no fail-open"
	// rule is enforced by the caller (scanTranscriptPRs) requiring a
	// resolved, non-empty prefix before ever calling in, not by this
	// function refusing an empty one.
	path := writePRTranscript(t, assistantTextLine("https://github.com/other/repo/pull/9"))
	got := scanTranscriptPRURLs(path, "")
	want := []string{"https://github.com/other/repo/pull/9"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTranscriptPRURLsMemoizedByStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess.jsonl")

	one := assistantTextLine("https://github.com/carl/kraken/pull/1") + "\n"
	two := assistantTextLine("https://github.com/carl/kraken/pull/1") + "\n" + assistantTextLine("https://github.com/carl/kraken/pull/2") + "\n"
	// pad "one" to the same byte length as "two" so a cache hit is
	// distinguishable from a genuine rescan: only (size, mtime) changes
	// between the two writes below, not this one.
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
