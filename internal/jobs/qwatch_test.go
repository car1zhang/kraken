package jobs

import (
	"reflect"
	"testing"

	"kraken/internal/store"
)

// questionBumpDecision's contract, row by row. The two seed rows are the
// load-bearing ones, and they answer different hazards: seeding on first
// sight of the ROWS (to the sentinel) is what keeps a job's first answer
// batch from being swallowed as history — the review that forced it
// measured that a lazy seed would have eaten every job's first answers,
// and every bump ever for the ~quarter of jobs whose answers all land in
// one board sitting — while seeding on first sight WITH history (to the
// newest answered_at) is what keeps the first pass after a deploy or kv
// wipe from replaying months of answers into every active cell.

func qrow(qid, answeredAt string) store.DocQuestion {
	return store.DocQuestion{QID: qid, Text: "t", Importance: "P1", AnsweredAt: answeredAt}
}

func TestQuestionBumpDecisionFirstSightOfRowsArmsTheSentinel(t *testing.T) {
	bump, qids, wm := questionBumpDecision([]store.DocQuestion{qrow("Q1", "")}, "")
	if bump || qids != nil {
		t.Fatalf("first sight of unanswered rows must not bump: bump=%v qids=%v", bump, qids)
	}
	if wm != questionBumpSeen {
		t.Fatalf("first sight of the rows arms the sentinel so the FIRST answer bumps, got %q", wm)
	}
	// and the sentinel must order below any real answered_at, or arming it
	// would be the same swallow it exists to prevent
	if !(questionBumpSeen < "2026-01-01T00:00:00Z") {
		t.Fatalf("sentinel %q must sort below every RFC3339 answered_at", questionBumpSeen)
	}
}

func TestQuestionBumpDecisionFirstAnswerAfterSentinelBumps(t *testing.T) {
	bump, qids, wm := questionBumpDecision([]store.DocQuestion{qrow("Q1", "2026-08-14T10:00:00Z")}, questionBumpSeen)
	if !bump || !reflect.DeepEqual(qids, []string{"Q1"}) || wm != "2026-08-14T10:00:00Z" {
		t.Fatalf("the job's first answer must bump off the sentinel: bump=%v qids=%v wm=%q", bump, qids, wm)
	}
}

func TestQuestionBumpDecisionFirstSightWithHistorySeedsSilently(t *testing.T) {
	qs := []store.DocQuestion{qrow("Q1", "2026-08-14T10:00:00Z"), qrow("Q2", "2026-08-14T12:00:00Z")}
	bump, qids, wm := questionBumpDecision(qs, "")
	if bump || qids != nil {
		t.Fatalf("first sight with existing answers must not replay them: bump=%v qids=%v", bump, qids)
	}
	if wm != "2026-08-14T12:00:00Z" {
		t.Fatalf("first sight with history seeds to the newest answered_at, got %q", wm)
	}
}

func TestQuestionBumpDecisionBumpsOnNewAnswer(t *testing.T) {
	qs := []store.DocQuestion{
		qrow("Q3", "2026-08-14T12:00:00Z"), // newer than watermark
		qrow("Q1", "2026-08-14T09:00:00Z"), // older — already delivered
		qrow("Q2", ""),                     // unanswered
	}
	bump, qids, wm := questionBumpDecision(qs, "2026-08-14T10:00:00Z")
	if !bump {
		t.Fatal("an answered_at newer than the watermark must bump")
	}
	if !reflect.DeepEqual(qids, []string{"Q3"}) {
		t.Fatalf("bump names exactly the newly-answered qids, got %v", qids)
	}
	if wm != "2026-08-14T12:00:00Z" {
		t.Fatalf("watermark advances to the newest answered_at, got %q", wm)
	}
}

func TestQuestionBumpDecisionQIDsSortNumerically(t *testing.T) {
	qs := []store.DocQuestion{
		qrow("Q10", "2026-08-14T12:00:00Z"),
		qrow("Q2", "2026-08-14T11:00:00Z"),
		qrow("Q9", "2026-08-14T11:30:00Z"),
	}
	bump, qids, _ := questionBumpDecision(qs, "2026-08-14T10:00:00Z")
	if !bump || !reflect.DeepEqual(qids, []string{"Q2", "Q9", "Q10"}) {
		t.Fatalf("qids sort numerically (Q2 < Q9 < Q10, never lexicographic Q10 < Q2): bump=%v qids=%v", bump, qids)
	}
}

func TestQuestionBumpDecisionNothingNewIsANoop(t *testing.T) {
	qs := []store.DocQuestion{qrow("Q1", "2026-08-14T09:00:00Z")}
	bump, qids, wm := questionBumpDecision(qs, "2026-08-14T10:00:00Z")
	if bump || qids != nil || wm != "2026-08-14T10:00:00Z" {
		t.Fatalf("answers at or below the watermark must change nothing: bump=%v qids=%v wm=%q", bump, qids, wm)
	}
	// exact equality is "already delivered" too, not a re-bump
	bump, _, wm = questionBumpDecision(qs, "2026-08-14T09:00:00Z")
	if bump || wm != "2026-08-14T09:00:00Z" {
		t.Fatalf("an answered_at equal to the watermark must not re-bump: bump=%v wm=%q", bump, wm)
	}
	// a deleted answered row (watermark above everything) must not rewind
	bump, _, wm = questionBumpDecision(qs, "2026-08-15T00:00:00Z")
	if bump || wm != "2026-08-15T00:00:00Z" {
		t.Fatalf("a watermark above every row must hold still: bump=%v wm=%q", bump, wm)
	}
}

// watchQuestions' cheapest observable gates. The bump/send path itself is
// deliberately unexercised here: core.SendToPeer is a package-level func
// with no seam, so driving it needs a live tmux pane — recorded as a known
// coverage gap in the build report rather than papered over with a test
// that cannot fail.
func TestWatchQuestionsGatesBeforeAnyRead(t *testing.T) {
	d := newDaemon()
	// nil job, unparseable job, zero id, wrong state, no live cell, and no
	// wired store: each must return without panicking — there is no store
	// wired in this test, so reaching any read would nil-deref.
	d.watchQuestions(nil, nil, nil)
	d.watchQuestions(&Job{ID: 1, Slug: "x", Err: "boom", State: "working"}, nil, map[string]bool{})
	d.watchQuestions(&Job{ID: 0, Slug: "x", State: "working"}, nil, map[string]bool{})
	d.watchQuestions(&Job{ID: 1, Slug: "x", State: "done"}, nil, map[string]bool{})
	d.watchQuestions(&Job{ID: 1, Slug: "x", State: "working"}, nil, map[string]bool{}) // cell not live
}

func TestWatchQuestionsSeedPersistsWatermark(t *testing.T) {
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "qw-test", Body: "spec", Repo: "kraken", State: "working"})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if err := st.SetQuestions(id, []store.DocQuestion{{QID: "Q1", Text: "t", Importance: "P1"}}); err != nil {
		t.Fatalf("set questions: %v", err)
	}
	d := newDaemon()
	j := &Job{ID: id, Slug: "qw-test", State: "working"}
	live := map[string]bool{CellSession(j): true}

	// pass 1, rows unanswered: arms the sentinel
	d.watchQuestions(j, nil, live)
	wm, err := st.GetKV(questionBumpKey(id))
	if err != nil || wm != questionBumpSeen {
		t.Fatalf("first pass over unanswered rows must arm the sentinel, got %q err=%v", wm, err)
	}

	// carl answers; the decision for the next pass is a bump off the
	// sentinel (asserted on the pure core — the send path has no seam, see
	// the gates test's comment), so the watermark must NOT move until a
	// send lands: with no zeus peer findable, the pass leaves it alone.
	if err := st.AnswerQuestion(id, "Q1", "yes", "2026-08-14T10:00:00Z"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	qs, err := st.ListQuestions(id)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	bump, qids, next := questionBumpDecision(qs, wm)
	if !bump || !reflect.DeepEqual(qids, []string{"Q1"}) || next != "2026-08-14T10:00:00Z" {
		t.Fatalf("post-answer decision must bump off the sentinel: bump=%v qids=%v next=%q", bump, qids, next)
	}
	d.watchQuestions(j, nil, live) // no peers → send path unreachable → no persist
	wm, _ = st.GetKV(questionBumpKey(id))
	if wm != questionBumpSeen {
		t.Fatalf("send-then-persist: with no zeus to send to the watermark must hold at the sentinel, got %q", wm)
	}
}
