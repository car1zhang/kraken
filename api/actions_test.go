package api

import (
	"strings"
	"testing"

	"kraken/jobs"
)

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
