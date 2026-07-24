package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// questions_test.go — pins the doc-questions hand-rolled parser
// (ParseDocQuestions): strict-parse error cases (with their exact "line N:"
// prefixes), format edge cases (BOM, CRLF, header whitespace, a literal "#"
// or ":" inside a value), the memoized loader's (size, mtime) cache key and
// its no-poison clone contract, and AnswerDocQuestion's surgical patch —
// including the hash round-trip fix, the answered-at injection fix, and
// concurrent-answer safety.

// --- QuestionsPath ---

func TestQuestionsPath(t *testing.T) {
	j := &Job{Slug: "doc-rework"}
	got := QuestionsPath(j)
	wantSuffix := filepath.Join("cells", "doc-rework", "questions.yaml")
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("QuestionsPath = %q, want suffix %q", got, wantSuffix)
	}
	if strings.Contains(got, "artifacts") {
		t.Fatalf("QuestionsPath should live beside artifacts/, not inside it: %q", got)
	}
}

// --- ParseDocQuestions ---

const sampleQuestions = `questions:
  - id: Q1
    text: Should the migration run online?
    context: sqlite lock behavior during migration
    importance: P0
    recommendation: yes, with WAL mode
  - id: Q2
    text: What is the retry policy?
    importance: P1
`

func TestParseDocQuestionsValidRoundTrip(t *testing.T) {
	qs, err := ParseDocQuestions([]byte(sampleQuestions))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := []DocQuestion{
		{
			ID:             "Q1",
			Text:           "Should the migration run online?",
			Context:        "sqlite lock behavior during migration",
			Importance:     "P0",
			Recommendation: "yes, with WAL mode",
		},
		{
			ID:         "Q2",
			Text:       "What is the retry policy?",
			Importance: "P1",
		},
	}
	if !reflect.DeepEqual(qs, want) {
		t.Fatalf("got %+v, want %+v", qs, want)
	}
}

func TestParseDocQuestionsCRLF(t *testing.T) {
	crlf := strings.ReplaceAll(sampleQuestions, "\n", "\r\n")
	got, err := ParseDocQuestions([]byte(crlf))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want, err := ParseDocQuestions([]byte(sampleQuestions))
	if err != nil {
		t.Fatalf("unexpected err on LF fixture: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CRLF parse diverged from LF parse: got %+v, want %+v", got, want)
	}
}

func TestParseDocQuestionsBOM(t *testing.T) {
	data := "\ufeffquestions:\n  - id: Q1\n    text: hi\n    importance: P0\n"
	qs, err := ParseDocQuestions([]byte(data))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(qs) != 1 || qs[0].ID != "Q1" {
		t.Fatalf("leading BOM not stripped before header search: got %+v", qs)
	}
}

func TestParseDocQuestionsHeaderTrailingWhitespace(t *testing.T) {
	data := "questions: \t\n  - id: Q1\n    text: hi\n    importance: P0\n"
	qs, err := ParseDocQuestions([]byte(data))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("trailing spaces/tabs on the header line should be tolerated: got %+v", qs)
	}
}

func TestParseDocQuestionsHeaderMismatchQuotesRawLine(t *testing.T) {
	// leading/trailing whitespace around the mismatched header line: the
	// error must quote the RAW line, spaces and all — a %q of a trimmed
	// line would silently hide the exact whitespace that IS the mismatch.
	data := "  questions:x  \n  - id: Q1\n    text: hi\n    importance: P0\n"
	_, err := ParseDocQuestions([]byte(data))
	if err == nil {
		t.Fatal("want error for malformed header, got nil")
	}
	if !strings.Contains(err.Error(), `"  questions:x  "`) {
		t.Fatalf("header mismatch error should quote the RAW line including surrounding whitespace: %v", err)
	}
}

// TestParseDocQuestionsPreviousItemErrorsBeforeNextItemStart covers finding
// #5d: finishItem validates the item that just ended (Q1's own bad
// importance, on Q1's item-start line) BEFORE the new item-start line (X2 —
// itself malformed, missing the required "Q" id prefix) is ever inspected,
// so the error surfaces at Q1's earlier line, not X2's later one.
func TestParseDocQuestionsPreviousItemErrorsBeforeNextItemStart(t *testing.T) {
	data := `questions:
  - id: Q1
    importance: P9
    text: hi
  - id: X2
    text: bye
    importance: P0
`
	_, err := ParseDocQuestions([]byte(data))
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), `line 2: item Q1 bad importance "P9"`) {
		t.Fatalf("want Q1's own earlier bad-importance error, not X2's later malformed id: %v", err)
	}
}

func TestParseDocQuestionsFullLineComments(t *testing.T) {
	// Full-line comments (first non-whitespace char is "#") are still
	// recognized and skipped by line; a "#" living INSIDE a value is a
	// separate matter, covered by TestParseDocQuestionsHashIsLiteralValue.
	data := `# top-level comment before the header
questions:
  - id: Q1
    text: value here

    context: still fine
    importance: P0
# comment between items

  - id: Q2
    text: another question
    importance: P2
`
	qs, err := ParseDocQuestions([]byte(data))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("want 2 questions, got %d: %+v", len(qs), qs)
	}
	if qs[0].Text != "value here" {
		t.Fatalf("text: %q", qs[0].Text)
	}
	if qs[0].Context != "still fine" {
		t.Fatalf("context after blank line: %q", qs[0].Context)
	}
	if qs[1].ID != "Q2" || qs[1].Text != "another question" {
		t.Fatalf("Q2 not parsed after comment line: %+v", qs[1])
	}
}

func TestParseDocQuestionsHashIsLiteralValue(t *testing.T) {
	// The pre-fix parser treated a whitespace-flanked "#" inside a value as
	// an inline comment and silently truncated it — a bare "# yes" answer
	// even re-parsed to "", defeating the already-answered guard. "#" in a
	// value is now always literal content, never a comment.
	cases := map[string]struct {
		data  string
		field string
		want  string
	}{
		"text containing a flanked hash": {
			data:  "questions:\n  - id: Q1\n    text: what about the 3 # 4 combo?\n    importance: P0\n",
			field: "text",
			want:  "what about the 3 # 4 combo?",
		},
		"answer starting with a hash": {
			data:  "questions:\n  - id: Q1\n    text: hi\n    importance: P0\n    answer: # just kidding, no\n",
			field: "answer",
			want:  "# just kidding, no",
		},
		"answer with a hash flanked by spaces mid-value": {
			data:  "questions:\n  - id: Q1\n    text: hi\n    importance: P0\n    answer: yes # actually no\n",
			field: "answer",
			want:  "yes # actually no",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			qs, err := ParseDocQuestions([]byte(c.data))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(qs) != 1 {
				t.Fatalf("want 1 question, got %+v", qs)
			}
			var got string
			switch c.field {
			case "text":
				got = qs[0].Text
			case "answer":
				got = qs[0].Answer
			}
			if got != c.want {
				t.Fatalf("value with # truncated: got %q, want %q", got, c.want)
			}
		})
	}
}

func TestParseDocQuestionsValueContainingColon(t *testing.T) {
	data := "questions:\n  - id: Q1\n    text: ratio is 3:1, confirm?\n    importance: P0\n"
	qs, err := ParseDocQuestions([]byte(data))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(qs) != 1 || qs[0].Text != "ratio is 3:1, confirm?" {
		t.Fatalf("value split on first \":\" only: got %+v", qs)
	}
}

func TestParseDocQuestionsEmpty(t *testing.T) {
	for name, data := range map[string]string{
		"truly empty":        "",
		"whitespace only":    "   \n\n  \t\n",
		"comments only":      "# nothing here\n# still nothing\n",
		"blank and comments": "\n# hi\n\n  \n# bye\n",
	} {
		t.Run(name, func(t *testing.T) {
			qs, err := ParseDocQuestions([]byte(data))
			if err != nil || qs != nil {
				t.Fatalf("%s: want nil,nil got %+v, %v", name, qs, err)
			}
		})
	}
}

func TestParseDocQuestionsErrors(t *testing.T) {
	cases := map[string]struct {
		data       string
		wantSubstr string
	}{
		"missing header": {
			data: `foo: bar
  - id: Q1
    text: hi
    importance: P0
`,
			wantSubstr: `line 1: expected "questions:" header`,
		},
		"unknown key": {
			data: `questions:
  - id: Q1
    text: hi
    importance: P0
    bogus: value
`,
			wantSubstr: `line 5: unknown key "bogus"`,
		},
		"duplicate id": {
			data: `questions:
  - id: Q1
    text: hi
    importance: P0
  - id: Q1
    text: hi again
    importance: P1
`,
			wantSubstr: `line 5: duplicate id "Q1"`,
		},
		"duplicate key within item": {
			data: `questions:
  - id: Q1
    text: hi
    text: again
    importance: P0
`,
			wantSubstr: `line 4: duplicate key "text"`,
		},
		"bad importance": {
			data: `questions:
  - id: Q1
    text: hi
    importance: P9
`,
			wantSubstr: `line 2: item Q1 bad importance "P9"`,
		},
		"missing text": {
			data: `questions:
  - id: Q1
    importance: P0
`,
			wantSubstr: `line 2: item Q1 missing text`,
		},
		"empty text": {
			data: `questions:
  - id: Q1
    text:
    importance: P0
`,
			wantSubstr: `line 2: item Q1 missing text`,
		},
		"misindented continuation (3 spaces)": {
			data: `questions:
  - id: Q1
   text: hi
    importance: P0
`,
			wantSubstr: `line 3: stray/misindented line`,
		},
		"misindented item (1 space)": {
			data: `questions:
 - id: Q1
    text: hi
    importance: P0
`,
			wantSubstr: `line 2: stray/misindented line`,
		},
		"continuation with a tab at the 5th column": {
			data:       "questions:\n  - id: Q1\n    text: hi\n    \timportance: P0\n",
			wantSubstr: `line 4: stray/misindented line`,
		},
		"stray top-level line": {
			data: `questions:
  - id: Q1
    text: hi
    importance: P0
stray line here
`,
			wantSubstr: `line 5: stray/misindented line`,
		},
		"id missing Q prefix": {
			data: `questions:
  - id: X1
    text: hi
    importance: P0
`,
			wantSubstr: `line 2: bad id "X1"`,
		},
		"id not numeric": {
			data: `questions:
  - id: Qabc
    text: hi
    importance: P0
`,
			wantSubstr: `line 2: bad id "Qabc"`,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			qs, err := ParseDocQuestions([]byte(c.data))
			if err == nil {
				t.Fatalf("%s: want error, got qs=%+v", name, qs)
			}
			if qs != nil {
				t.Fatalf("%s: want nil questions on error, got %+v", name, qs)
			}
			if !strings.Contains(err.Error(), c.wantSubstr) {
				t.Fatalf("%s: err = %q, want substring %q", name, err.Error(), c.wantSubstr)
			}
		})
	}
}

func TestParseDocQuestionsIDFormats(t *testing.T) {
	valid := map[string]string{
		"Q1":  "Q1",
		"Q01": "Q01",
		"Q42": "Q42",
	}
	for name, id := range valid {
		t.Run(name, func(t *testing.T) {
			data := "questions:\n  - id: " + id + "\n    text: hi\n    importance: P0\n"
			qs, err := ParseDocQuestions([]byte(data))
			if err != nil {
				t.Fatalf("id %q: unexpected err: %v", id, err)
			}
			if len(qs) != 1 || qs[0].ID != id {
				t.Fatalf("id %q: got %+v", id, qs)
			}
		})
	}
	// The "bad id" (non-Q-prefixed / non-numeric) cases live in
	// TestParseDocQuestionsErrors — not duplicated here.
}

// --- LoadDocQuestions ---

func TestLoadDocQuestionsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "questions.yaml")
	qs, err := LoadDocQuestions(path)
	if err != nil || qs != nil {
		t.Fatalf("missing file: want nil,nil got %+v, %v", qs, err)
	}
}

func TestLoadDocQuestionsMemoizedByStat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "questions.yaml")

	fixtureA := "questions:\n  - id: Q1\n    text: aaa\n    importance: P0\n"
	fixtureB := "questions:\n  - id: Q1\n    text: bbb\n    importance: P0\n"
	if len(fixtureA) != len(fixtureB) {
		t.Fatal("test fixture assumption violated: fixtures must be the SAME size")
	}

	mtime := time.Now()
	if err := os.WriteFile(path, []byte(fixtureA), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	qs1, err := LoadDocQuestions(path)
	if err != nil || len(qs1) != 1 || qs1[0].Text != "aaa" {
		t.Fatalf("initial load: got %+v, %v", qs1, err)
	}

	// Overwrite with different content of the SAME size, pinning the SAME
	// mtime: the (size, mtime) cache key is unchanged, so this must be an
	// honest cache HIT — the stale "aaa" text, not the on-disk "bbb" —
	// proving LoadDocQuestions actually consults the cache instead of
	// re-reading the file on every call.
	if err := os.WriteFile(path, []byte(fixtureB), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	qs2, err := LoadDocQuestions(path)
	if err != nil || len(qs2) != 1 || qs2[0].Text != "aaa" {
		t.Fatalf("same (size,mtime): want stale cached %q, got %+v, %v", "aaa", qs2, err)
	}

	// Now bump ONLY the mtime (the file on disk is still fixtureB, the same
	// size as fixtureA): the cache key must include mtime, not just size, so
	// this must invalidate the cache and return the fresh "bbb" content.
	bumped := mtime.Add(time.Second)
	if err := os.Chtimes(path, bumped, bumped); err != nil {
		t.Fatal(err)
	}
	qs3, err := LoadDocQuestions(path)
	if err != nil || len(qs3) != 1 || qs3[0].Text != "bbb" {
		t.Fatalf("bumped mtime: want fresh %q, got %+v, %v", "bbb", qs3, err)
	}
}

// TestLoadDocQuestionsCacheHitReturnsClone pins the cache-HIT return path
// specifically: LoadDocQuestions' very first (fresh-parse) return is
// already a slices.Clone of what it stores, so mutating THAT copy can never
// poison the cache regardless of whether the cache-hit path clones — a
// 2-load fresh-then-hit sequence can't tell the two apart. The 3-load
// sequence below (fresh -> hit+mutate -> hit) mutates a CACHE-HIT return
// value and then re-reads via another cache hit, so reverting the
// cache-hit clone (returning the cache entry's own backing slice) fails
// this test.
func TestLoadDocQuestionsCacheHitReturnsClone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "questions.yaml")
	content := "questions:\n  - id: Q1\n    text: hi\n    importance: P0\n  - id: Q2\n    text: bye\n    importance: P1\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	// load 1: fresh parse, populates the cache.
	if _, err := LoadDocQuestions(path); err != nil {
		t.Fatal(err)
	}

	// load 2: a cache HIT (unchanged size/mtime) — mutate what it hands back.
	qs2, err := LoadDocQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	qs2[0].Text = "MUTATED"

	// load 3: another cache hit — must come back unmutated.
	qs3, err := LoadDocQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs3) != 2 || qs3[0].Text != "hi" {
		t.Fatalf("cache hit poisoned by a prior cache-hit caller's mutation: got %+v", qs3)
	}
}

// TestLoadDocQuestionsEvictsCacheOnDelete pins actual eviction, not just the
// missing-file response: LoadDocQuestions' os.Stat-fails-IsNotExist branch
// returns nil,nil unconditionally, without ever consulting the cache — so a
// plain delete-then-load sequence passes whether or not the eviction code
// exists at all. This recreates the file at the EXACT (size, mtime) the
// stale cache entry was keyed on, with DIFFERENT content: if delete didn't
// evict the entry, this load would return the stale pre-delete content
// instead of what's actually on disk now.
func TestLoadDocQuestionsEvictsCacheOnDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "questions.yaml")
	original := "questions:\n  - id: Q1\n    text: hi\n    importance: P0\n"
	replacement := "questions:\n  - id: Q2\n    text: yo\n    importance: P0\n"
	if len(original) != len(replacement) {
		t.Fatal("test fixture assumption violated: fixtures must be the SAME size")
	}

	mtime := time.Now()
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDocQuestions(path); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if qs, err := LoadDocQuestions(path); err != nil || qs != nil {
		t.Fatalf("deleted file: want nil,nil got %+v, %v", qs, err)
	}

	// recreate at the identical (size, mtime) the pre-delete cache entry was
	// keyed on, but with different content.
	if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	qs, err := LoadDocQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].ID != "Q2" || qs[0].Text != "yo" {
		t.Fatalf("stale cache entry not evicted on delete: got %+v", qs)
	}
}

// --- AnswerDocQuestion ---

const answerFixture = `questions:
  - id: Q1
    text: Should the migration run online?
    context: sqlite lock behavior
    importance: P0
  - id: Q2
    text: What is the retry policy?
    importance: P1
`

func writeQuestions(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "questions.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAnswerDocQuestionPatchesAndPreservesRest(t *testing.T) {
	path := writeQuestions(t, answerFixture)

	if err := AnswerDocQuestion(path, "Q1", "Yes, online is fine.\nDouble checked.", "2026-07-23T00:00:00Z"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `questions:
  - id: Q1
    text: Should the migration run online?
    context: sqlite lock behavior
    importance: P0
    answer: Yes, online is fine. Double checked.
    answered-at: 2026-07-23T00:00:00Z
  - id: Q2
    text: What is the retry policy?
    importance: P1
`
	if string(got) != want {
		t.Fatalf("file mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnswerDocQuestionReplacesPresentButEmpty(t *testing.T) {
	fixture := `questions:
  - id: Q1
    text: Should the migration run online?
    importance: P0
    answer:
`
	path := writeQuestions(t, fixture)

	if err := AnswerDocQuestion(path, "Q1", "yes", "2026-07-23T00:00:00Z"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `questions:
  - id: Q1
    text: Should the migration run online?
    importance: P0
    answer: yes
    answered-at: 2026-07-23T00:00:00Z
`
	if string(got) != want {
		t.Fatalf("file mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnswerDocQuestionLastItemNoTrailingNewline(t *testing.T) {
	fixture := `questions:
  - id: Q1
    text: first
    importance: P0
  - id: Q2
    text: last one, no trailing newline
    importance: P1`
	path := writeQuestions(t, fixture)

	if err := AnswerDocQuestion(path, "Q2", "yes", "2026-07-23T00:00:00Z"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `questions:
  - id: Q1
    text: first
    importance: P0
  - id: Q2
    text: last one, no trailing newline
    importance: P1
    answer: yes
    answered-at: 2026-07-23T00:00:00Z`
	if string(got) != want {
		t.Fatalf("file mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAnswerDocQuestionUnknownID(t *testing.T) {
	path := writeQuestions(t, answerFixture)
	if err := AnswerDocQuestion(path, "Q99", "answer", "ts"); err == nil {
		t.Fatal("want error for unknown id, got nil")
	}
}

func TestAnswerDocQuestionAlreadyAnswered(t *testing.T) {
	path := writeQuestions(t, answerFixture)
	if err := AnswerDocQuestion(path, "Q1", "first answer", "2026-07-23T00:00:00Z"); err != nil {
		t.Fatalf("first answer: unexpected err: %v", err)
	}
	if err := AnswerDocQuestion(path, "Q1", "second answer", "2026-07-23T00:01:00Z"); err == nil {
		t.Fatal("want error answering an already-answered question, got nil")
	}
}

func TestAnswerDocQuestionMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "questions.yaml")
	err := AnswerDocQuestion(path, "Q1", "answer", "2026-07-23T00:00:00Z")
	if err == nil {
		t.Fatal("want error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "no questions file") {
		t.Fatalf("want a domain error naming the missing questions file, got: %v", err)
	}
}

func TestAnswerDocQuestionRejectsEmptyAnswer(t *testing.T) {
	for name, answer := range map[string]string{
		"empty":           "",
		"whitespace only": "   \t  ",
	} {
		t.Run(name, func(t *testing.T) {
			path := writeQuestions(t, answerFixture)
			if err := AnswerDocQuestion(path, "Q1", answer, "2026-07-23T00:00:00Z"); err == nil {
				t.Fatal("want error for empty/whitespace-only answer, got nil")
			}
		})
	}
}

func TestAnswerDocQuestionAnsweredAtFlattened(t *testing.T) {
	// A newline embedded in answeredAt must not be able to inject a
	// fabricated item into the file: it gets flattened to a single line,
	// same as the answer itself.
	path := writeQuestions(t, answerFixture)
	evil := "2026-07-23T00:00:00Z" + "\n  - id: Q9\n    text: evil"
	if err := AnswerDocQuestion(path, "Q1", "yes", evil); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	qs, err := ParseDocQuestions(data)
	if err != nil {
		t.Fatalf("re-parse after answered-at injection attempt: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("answered-at injection fabricated extra items: got %d questions, want 2: %+v", len(qs), qs)
	}
}

func TestAnswerDocQuestionHashRoundTrip(t *testing.T) {
	// Parse -> Answer -> Parse must round-trip an answer containing a
	// whitespace-flanked "#" exactly; the pre-fix parser's inline-comment
	// stripping truncated these (a bare "# ..." answer even re-parsed to
	// "", defeating the already-answered guard).
	cases := map[string]string{
		"hash flanked by spaces mid-value": "yes # actually maybe not",
		"answer starting with a hash":      "# just kidding, no",
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeQuestions(t, answerFixture)
			if err := AnswerDocQuestion(path, "Q1", answer, "2026-07-23T00:00:00Z"); err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			qs, err := ParseDocQuestions(data)
			if err != nil {
				t.Fatalf("re-parse after answer: %v", err)
			}
			var got string
			for _, q := range qs {
				if q.ID == "Q1" {
					got = q.Answer
				}
			}
			if got != answer {
				t.Fatalf("answer round-trip: got %q, want %q", got, answer)
			}
		})
	}
}

func TestAnswerDocQuestionConcurrentAnswersAllSurvive(t *testing.T) {
	const n = 20
	var sb strings.Builder
	sb.WriteString("questions:\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "  - id: Q%d\n    text: question %d\n    importance: P0\n", i, i)
	}
	path := writeQuestions(t, sb.String())

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i-1] = AnswerDocQuestion(path, fmt.Sprintf("Q%d", i), fmt.Sprintf("answer %d", i), "2026-07-23T00:00:00Z")
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Q%d: unexpected err: %v", i+1, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	qs, err := ParseDocQuestions(data)
	if err != nil {
		t.Fatalf("re-parse after concurrent answers: %v", err)
	}
	if len(qs) != n {
		t.Fatalf("lost update: want %d questions, got %d: %+v", n, len(qs), qs)
	}
	for i, q := range qs {
		want := fmt.Sprintf("answer %d", i+1)
		if q.Answer != want {
			t.Fatalf("Q%d: answer = %q, want %q (lost update)", i+1, q.Answer, want)
		}
	}
}
