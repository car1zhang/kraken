package jobs

import (
	"testing"

	"kraken/internal/store"
)

// TestBlockedNotifyBody pins the one daemon-side sign of a question round:
// the "blocked" notification names an unanswered P0, and nothing else
// changes its body.
func TestBlockedNotifyBody(t *testing.T) {
	const generic = "blocked — see the board"
	q := func(id, imp, answer string) store.DocQuestion {
		return store.DocQuestion{QID: id, Importance: imp, Answer: answer}
	}
	for _, c := range []struct {
		name string
		rows []store.DocQuestion
		want string
	}{
		{"no rows", nil, generic},
		{"only P1 open", []store.DocQuestion{q("Q1", "P1", "")}, generic},
		{"P0 answered", []store.DocQuestion{q("Q1", "P0", "yes")}, generic},
		{"P0 answered with whitespace only is still open", []store.DocQuestion{q("Q1", "P0", "  ")}, "waiting on Q1 (P0) — answer on the board, then steer"},
		{"one P0 open", []store.DocQuestion{q("Q2", "P1", ""), q("Q7", "P0", "")}, "waiting on Q7 (P0) — answer on the board, then steer"},
		{"two P0 open, row order kept", []store.DocQuestion{q("Q7", "P0", ""), q("Q3", "P0", ""), q("Q9", "P0", "done")}, "waiting on Q7, Q3 (P0) — answer on the board, then steer"},
		{"unknown importance is never P0", []store.DocQuestion{q("Q1", "p0", ""), q("Q2", "", "")}, generic},
	} {
		if got := blockedNotifyBody(c.rows); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// TestJobQuestionRowsDegradesToNil: with no store wired the helper reads as
// no rows, so the notification keeps its generic body rather than failing.
func TestJobQuestionRowsDegradesToNil(t *testing.T) {
	prev := ActiveStore()
	UseStore(nil)
	t.Cleanup(func() { UseStore(prev) })
	if rows := jobQuestionRows(&Job{ID: 1, Slug: "x"}); rows != nil {
		t.Errorf("want nil rows with no store, got %v", rows)
	}
	if rows := jobQuestionRows(nil); rows != nil {
		t.Errorf("want nil rows for a nil job, got %v", rows)
	}
}
