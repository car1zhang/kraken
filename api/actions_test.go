package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/jobs"
)

// writeTestJobBody writes a job file with a caller-chosen body (spec text),
// unlike writeTestJob which always seeds "# Spec\n" — needed for the
// empty-spec queue guard tests below.
func writeTestJobBody(t *testing.T, slug, extraFrontmatter, body string) {
	t.Helper()
	src := "---\njob: " + slug + "\ntype: build\nrepo: monolith\nready: false\n" +
		extraFrontmatter + "---\n\n" + body
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), slug+jobs.JobSuffix), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWebAnswerQuestionBadDigit(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "q-bad-digit", "state: blocked\n")
	for _, bad := range []string{"", "0", "10", "x", "1 2", " "} {
		if err := webAnswerQuestion(webReq{Slug: "q-bad-digit", Text: bad}); err == nil {
			t.Errorf("digit %q should be rejected", bad)
		} else if !strings.Contains(err.Error(), "single digit") {
			t.Errorf("digit %q: unexpected error %v", bad, err)
		}
	}
}

func TestWebAnswerQuestionNotLive(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	// state: blocked but no matching tmux session exists in the test
	// environment, so this must fail past digit validation on liveness
	writeTestJob(t, "q-not-live", "state: blocked\n")
	err := webAnswerQuestion(webReq{Slug: "q-not-live", Text: "1"})
	if err == nil || !strings.Contains(err.Error(), "no live cell") {
		t.Fatalf("expected 'no live cell' error, got %v", err)
	}
}

// TestAnswerableSingleDigit covers the guard webAnswerQuestion uses to refuse
// multi-question/multi-select prompts: those can't be expressed as one digit,
// so they must be rejected before any digit is ever sent or state written.
func TestAnswerableSingleDigit(t *testing.T) {
	cases := []struct {
		name string
		q    *jobs.PendingQuestion
		want bool
	}{
		{"nil question", nil, false},
		{"single, not multi-select", &jobs.PendingQuestion{Questions: []jobs.QuestionItem{{Question: "q"}}}, true},
		{"single, multi-select", &jobs.PendingQuestion{Questions: []jobs.QuestionItem{{Question: "q", MultiSelect: true}}}, false},
		{"multiple questions", &jobs.PendingQuestion{Questions: []jobs.QuestionItem{{Question: "q1"}, {Question: "q2"}}}, false},
		{"zero questions", &jobs.PendingQuestion{Questions: nil}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := answerableSingleDigit(c.q); got != c.want {
				t.Errorf("answerableSingleDigit(%+v) = %v, want %v", c.q, got, c.want)
			}
		})
	}
}

// TestWebDoneValidation covers webDone's server-side floor: started and not
// done, matching cmdDone rather than the board's (possibly stale) displayed
// column — a never-started job and an already-done one are both refused; a
// merely-working (not yet blocked) job is NOT refused here, since the
// board's column gating is what restricts the drag to BLOCKED, not this
// check (see webDone's doc comment).
func TestWebDoneValidation(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "draft-job", "ready: false\n")
	if err := webDone(webReq{Slug: "draft-job"}); err == nil || !strings.Contains(err.Error(), "hasn't started") {
		t.Fatalf("expected 'hasn't started' error, got %v", err)
	}
	writeTestJob(t, "done-job", "state: done\n")
	if err := webDone(webReq{Slug: "done-job"}); err == nil || !strings.Contains(err.Error(), "already done") {
		t.Fatalf("expected 'already done' error, got %v", err)
	}
}

func TestWebSteerNotLive(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "steer-not-live", "state: blocked\n")
	err := webSteer(webReq{Slug: "steer-not-live", Text: "pivot to plan B"})
	if err == nil || !strings.Contains(err.Error(), "no live cell") {
		t.Fatalf("expected 'no live cell' error, got %v", err)
	}
}

func TestWebSteerEmptyMessage(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "steer-empty", "state: blocked\n")
	if err := webSteer(webReq{Slug: "steer-empty", Text: "   "}); err == nil {
		t.Fatal("empty steering message should be rejected")
	}
}

// --- webQueue: server-side empty-spec guard ---

func TestWebQueueEmptySpec(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJobBody(t, "queue-empty-spec", "", "")
	if err := webQueue(webReq{Slug: "queue-empty-spec"}); err == nil || !strings.Contains(err.Error(), "empty spec") {
		t.Fatalf("expected 'empty spec' error, got %v", err)
	}
}

func TestWebQueueWithSpec(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJobBody(t, "queue-with-spec", "", "Some real spec content.\n")
	if err := webQueue(webReq{Slug: "queue-with-spec"}); err != nil {
		t.Fatalf("queue with a real spec should succeed, got %v", err)
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), "queue-with-spec")
	if j == nil || !j.Ready {
		t.Fatalf("expected job to be re-parsed as ready, got %+v", j)
	}
}

// --- webDequeue: queued→draft reverse move ---

func TestWebDequeue(t *testing.T) {
	cases := []struct {
		name        string
		slug        string
		frontmatter string
		asDraft     bool   // true: use writeTestJobBody (ready: false); false: writeTestJob (ready: true)
		wantErrSub  string // empty means success
	}{
		{"queued job dequeues", "dequeue-ready", "", false, ""},
		{"draft job refused", "dequeue-draft", "", true, "queued"},
		{"started job refused", "dequeue-working", "state: working\n", false, "kill"},
		{"done job refused", "dequeue-done", "state: done\n", false, "queued"},
		{"unparseable job refused", "dequeue-bad", "ready: false\n", false, "unparseable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
			if c.asDraft {
				writeTestJobBody(t, c.slug, c.frontmatter, "some spec\n")
			} else {
				writeTestJob(t, c.slug, c.frontmatter)
			}
			err := webDequeue(webReq{Slug: c.slug})
			if c.wantErrSub == "" {
				if err != nil {
					t.Fatalf("expected dequeue to succeed, got %v", err)
				}
				j := jobs.JobBySlug(jobs.LoadJobs(), c.slug)
				if j == nil || j.Ready {
					t.Fatalf("expected ready to flip false on disk, got %+v", j)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErrSub) {
				t.Fatalf("expected error containing %q, got %v", c.wantErrSub, err)
			}
		})
	}
}

// TestWebQueueDequeueRoundTrip exercises queue→dequeue→queue through the
// real job file, confirming each move's on-disk effect survives a re-parse
// before the next move is attempted.
func TestWebQueueDequeueRoundTrip(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJobBody(t, "roundtrip", "", "a real spec\n")
	if err := webQueue(webReq{Slug: "roundtrip"}); err != nil {
		t.Fatalf("initial queue: %v", err)
	}
	if j := jobs.JobBySlug(jobs.LoadJobs(), "roundtrip"); j == nil || !j.Ready {
		t.Fatalf("expected ready after queue, got %+v", j)
	}
	if err := webDequeue(webReq{Slug: "roundtrip"}); err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if j := jobs.JobBySlug(jobs.LoadJobs(), "roundtrip"); j == nil || j.Ready {
		t.Fatalf("expected ready false after dequeue, got %+v", j)
	}
	if err := webQueue(webReq{Slug: "roundtrip"}); err != nil {
		t.Fatalf("re-queue: %v", err)
	}
	if j := jobs.JobBySlug(jobs.LoadJobs(), "roundtrip"); j == nil || !j.Ready {
		t.Fatalf("expected ready true after re-queue, got %+v", j)
	}
}

// --- POST /api/reorder ---

func postReorder(t *testing.T, column string, slugs []string) *httptest.ResponseRecorder {
	t.Helper()
	body := mustJSON(t, reorderReq{Column: column, Slugs: slugs})
	req := httptest.NewRequest(http.MethodPost, "/api/reorder", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webReorderHandler(rec, req)
	return rec
}

func TestWebReorderHappyPath(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	writeTestJob(t, "reorder-a", "")
	writeTestJob(t, "reorder-b", "")

	rec := postReorder(t, "queued", []string{"reorder-b", "reorder-a"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp["ok"] {
		t.Fatalf("expected ok: true, got %v", resp)
	}
	got := jobs.LoadBoardOrder()["queued"]
	want := []string{"reorder-b", "reorder-a"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("stored order = %v, want %v", got, want)
	}
}

func TestWebReorderUnknownColumn(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	rec := postReorder(t, "bogus", []string{"x"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp["error"], "unknown column") {
		t.Fatalf("error = %q, want mention of unknown column", resp["error"])
	}
}

// TestWebReorderDropsDepartedSlug: a slug posted for a column it doesn't
// actually occupy (a concurrent transition, or a stale board) is silently
// dropped from the saved order rather than rejecting the whole reorder.
func TestWebReorderDropsDepartedSlug(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	writeTestJobBody(t, "reorder-queued", "", "spec\n") // ready: false in writeTestJobBody -> draft
	writeTestJob(t, "reorder-actually-queued", "")      // ready: true -> queued

	rec := postReorder(t, "queued", []string{"reorder-actually-queued", "reorder-queued"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := jobs.LoadBoardOrder()["queued"]
	if len(got) != 1 || got[0] != "reorder-actually-queued" {
		t.Fatalf("stored order = %v, want only the actually-queued slug", got)
	}
}

// --- POST /api/start ---

func TestWebStartRejectsNonQueued(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJobBody(t, "start-draft", "", "spec\n")
	err := webStart(webReq{Slug: "start-draft"})
	if err == nil || !strings.Contains(err.Error(), "only queued jobs can be started") {
		t.Fatalf("expected 'only queued jobs' error, got %v", err)
	}
}

// --- POST /api/stop ---

func TestWebStopRejectsDeadCell(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "stop-not-live", "state: working\n")
	err := webStop(webReq{Slug: "stop-not-live"})
	if err == nil || !strings.Contains(err.Error(), "no live cell") {
		t.Fatalf("expected 'no live cell' error, got %v", err)
	}
}
