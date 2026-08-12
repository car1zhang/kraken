package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// writeTestJobBody writes a job file with a caller-chosen body (spec text),
// unlike writeTestJob which always seeds "# Spec\n" — needed for the
// empty-spec queue guard tests below.
func writeTestJobBody(t *testing.T, slug, extraFrontmatter, body string) {
	t.Helper()
	src := "---\njob: " + slug + "\ntype: build\nrepo: monolith\n" +
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
// done, matching the pre-Q14 CLI's `k done` rather than the board's (possibly stale) displayed
// column — a never-started job and an already-done one are both refused; a
// merely-working (not yet blocked) job is NOT refused here, since the
// board's column gating is what restricts the drag to BLOCKED, not this
// check (see webDone's doc comment).
func TestWebDoneValidation(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "draft-job", "")
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

// --- webSubmit: the one start surface (POST /api/queue) ---
//
// webQueue/webDequeue are gone, and so are their tests: the pair described a
// reversible flag flip, and there is no flag and nothing to reverse. What
// replaced them is one handler that hands the job to jobs.SubmitJob and
// returns its error verbatim, so the tests here cover the handler's own two
// jobs — resolve the slug, and route through the seam exactly once — while
// SubmitJob's refusal order is pinned in the jobs package where it lives.

// useSubmitSeam swaps submitJobFn for the duration of one test and returns
// the recorder. Without it every case below would need a git worktree, a
// tmux server and the rotation gate.
func useSubmitSeam(t *testing.T, err error) *[]string {
	t.Helper()
	var got []string
	prev := submitJobFn
	submitJobFn = func(j *jobs.Job) error {
		got = append(got, j.Slug)
		return err
	}
	t.Cleanup(func() { submitJobFn = prev })
	return &got
}

func TestWebSubmitCallsTheSeamOnce(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJobBody(t, "submit-ok", "", "Some real spec content.\n")
	got := useSubmitSeam(t, nil)

	if err := webSubmit(webReq{Slug: "submit-ok"}); err != nil {
		t.Fatalf("submit with a real spec should succeed, got %v", err)
	}
	if len(*got) != 1 || (*got)[0] != "submit-ok" {
		t.Fatalf("start calls = %v, want exactly one for submit-ok", *got)
	}
}

func TestWebSubmitSurfacesTheSeamError(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJobBody(t, "submit-fails", "", "a real spec\n")
	useSubmitSeam(t, errors.New("submit-fails has an empty spec — write one before submitting"))

	err := webSubmit(webReq{Slug: "submit-fails"})
	if err == nil || !strings.Contains(err.Error(), "empty spec") {
		t.Fatalf("want SubmitJob's own text surfaced verbatim, got %v", err)
	}
}

func TestWebSubmitUnknownSlug(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	got := useSubmitSeam(t, nil)

	if err := webSubmit(webReq{Slug: "no-such-job"}); err == nil {
		t.Fatal("an unknown slug must be refused")
	}
	if len(*got) != 0 {
		t.Fatalf("start attempted for an unresolved slug: %v", *got)
	}
}

// The submit is mirror-gated. queue/dequeue were the one deliberate
// exception to "a mirror board is read-only" — safe only because flipping a
// sandbox DB field reached nothing outside it. Starting a cell reaches a real
// worktree and a real tmux server, so the exception had to go with them.
func TestSubmitIsMirrorGated(t *testing.T) {
	if !mirrorGatedActions["submit"] {
		t.Fatal("submit is not mirror-gated — a mirror board would start real cells")
	}
	for _, gone := range []string{"queue", "dequeue", "start"} {
		if mirrorGatedActions[gone] {
			t.Errorf("retired action %q is still listed in mirrorGatedActions", gone)
		}
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

// Board order (jobs.LoadBoardOrder/SaveColumnOrder) is store-only now (H1:
// file mode retired outright, no nil-store fallback left in order.go) — so
// every reorder test that actually reaches SaveColumnOrder needs a wired
// store, with the jobs themselves created as store rows to match (LoadJobs
// only reads files when NO store is wired at all).
func TestWebReorderHappyPath(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "reorder-a", Repo: "monolith", Body: "spec\n"})
	mustCreateStoreJob(t, st, store.Job{Slug: "reorder-b", Repo: "monolith", Body: "spec\n"})

	rec := postReorder(t, "draft", []string{"reorder-b", "reorder-a"})
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
	got := jobs.LoadBoardOrder()["draft"]
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
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "reorder-draft", Repo: "monolith", Body: "spec\n"})                     // no state -> draft
	mustCreateStoreJob(t, st, store.Job{Slug: "reorder-working", Repo: "monolith", State: "working", Body: "spec\n"}) // started -> not draft

	rec := postReorder(t, "draft", []string{"reorder-draft", "reorder-working"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	got := jobs.LoadBoardOrder()["draft"]
	if len(got) != 1 || got[0] != "reorder-draft" {
		t.Fatalf("stored order = %v, want only the slug that is actually a draft", got)
	}
}

// The queued column is gone from reorderColumns with the status it named:
// a client that still posts it gets the same 409 any other unknown column
// gets, rather than silently writing an order nothing will ever render.
func TestWebReorderQueuedColumnRetired(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	rec := postReorder(t, "queued", []string{"x"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body %s", rec.Code, rec.Body.String())
	}
	if reorderColumns["queued"] {
		t.Fatal("reorderColumns still accepts queued")
	}
	for _, want := range []string{"draft", "working", "blocked", "done"} {
		if !reorderColumns[want] {
			t.Errorf("reorderColumns lost %q", want)
		}
	}
	if len(reorderColumns) != 4 {
		t.Errorf("reorderColumns = %v, want exactly the four board columns", reorderColumns)
	}
}

// --- POST /api/start, POST /api/dequeue: gone ---
//
// Both routes existed only to move a job into or out of the queued column.
// webStart was the manual start for a job the queue pass would not take
// (a gated prereq, or the cap being full), and webDequeue was the reverse
// drag. Neither has a reachable state left, so both handlers and both routes
// were deleted; TestRegisterRoutes pins that the mux 404s them.

// --- POST /api/stop ---

func TestWebStopRejectsDeadCell(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "stop-not-live", "state: working\n")
	err := webStop(webReq{Slug: "stop-not-live"})
	if err == nil || !strings.Contains(err.Error(), "no live cell") {
		t.Fatalf("expected 'no live cell' error, got %v", err)
	}
}
