package jobs

import (
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// threadlimithit_test.go — a side thread's usage-limit delivery is recorded
// under the thread's own key, resolved on record, and never under the job's
// row the cell's rescue chain reads. A thread shares its cell's account and
// its worktree's StopFailure hook, so its first turn on an exhausted window
// fires the cell's own hook naming the cell's slug; attributed to the cell,
// that delivery drove a rebind and a kill-and-resume of an idle zeus for a
// pane the rescue cannot reach.

// tlhWorld opens a scratch store with one working job and captures every
// notification the thread arm sends.
func tlhWorld(t *testing.T) (*store.Store, *Job, *[]string) {
	t.Helper()
	st := useTestStore(t)
	row := mustCreateJob(t, st, store.Job{Slug: "thread-hit", Repo: "kraken", State: "working"})
	j := &Job{ID: row.ID, Slug: row.Slug, Repo: "kraken", AccountEmail: "slot@example.com"}
	var notes []string
	prev := notifyFn
	notifyFn = func(title, msg string) { notes = append(notes, title+" | "+msg) }
	t.Cleanup(func() { notifyFn = prev })
	return st, j, &notes
}

func tlhThread(t *testing.T, st *store.Store, j *Job, session string) store.Thread {
	t.Helper()
	th, err := st.AddThreadNext(store.Thread{JobID: j.ID, Title: "why", SessionID: session, State: "live"})
	if err != nil {
		t.Fatalf("add thread: %v", err)
	}
	return th
}

func TestRecordLimitHitRoutesASideThreadsDeliveryOffTheJobRow(t *testing.T) {
	st, j, notes := tlhWorld(t)
	th := tlhThread(t, st, j, "thread-sess")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	hit, err := RecordLimitHit(j, "rate_limit", "429 rate_limit_error", "thread-sess", "", now)
	if err != nil {
		t.Fatalf("RecordLimitHit: %v", err)
	}
	if hit.Thread != th.TID || hit.Outcome != "thread-hit" || hit.HandledAt != now.Unix() {
		t.Errorf("thread delivery recorded as %+v, want Thread %q resolved thread-hit at %d", hit, th.TID, now.Unix())
	}
	if hit.SourceAccount != "slot@example.com" || hit.SessionID != "thread-sess" || hit.Deliveries != 1 {
		t.Errorf("thread record lost its provenance: %+v", hit)
	}
	// The job's own row — the one the rescue chain reads — is untouched.
	if got := ReadLimitHit(j); got.At != 0 {
		t.Errorf("job row written for a thread's delivery: %+v", got)
	}
	// The thread's row is where it went, and it reads back resolved.
	stored := readLimitHitByKey(threadLimitHitKey(j.ID, th.TID))
	if stored.At != now.Unix() || stored.Thread != th.TID || stored.HandledAt == 0 {
		t.Errorf("thread row = %+v, want the resolved record", stored)
	}
	if len(*notes) != 1 || !strings.Contains((*notes)[0], "side thread "+th.TID) || !strings.Contains((*notes)[0], j.Slug) {
		t.Errorf("notifications = %q, want exactly one naming the thread and the job", *notes)
	}
}

func TestRecordLimitHitStillRecordsZeusAndTeammateDeliveriesOnTheJobRow(t *testing.T) {
	st, j, notes := tlhWorld(t)
	tlhThread(t, st, j, "thread-sess")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for _, sess := range []string{"zeus-sess", ""} {
		hit, err := RecordLimitHit(j, "rate_limit", "429", sess, "", now)
		if err != nil {
			t.Fatalf("RecordLimitHit(%q): %v", sess, err)
		}
		if hit.Thread != "" || hit.HandledAt != 0 {
			t.Errorf("session %q: recorded as a thread's or resolved: %+v", sess, hit)
		}
	}
	if got := ReadLimitHit(j); got.At != now.Unix() || got.Deliveries != 2 || got.HandledAt != 0 {
		t.Errorf("job row = %+v, want an unresolved record with both deliveries folded", got)
	}
	if len(*notes) != 0 {
		t.Errorf("a cell delivery notified: %q", *notes)
	}
}

func TestRecordLimitHitAttributesAClosedThreadButNeverAnotherJobsThread(t *testing.T) {
	st, j, _ := tlhWorld(t)
	closed := tlhThread(t, st, j, "closed-sess")
	if err := st.CloseThread(j.ID, closed.TID, 1); err != nil {
		t.Fatalf("close thread: %v", err)
	}
	other := mustCreateJob(t, st, store.Job{Slug: "other-job", Repo: "kraken", State: "working"})
	if _, err := st.AddThreadNext(store.Thread{JobID: other.ID, Title: "theirs", SessionID: "other-sess", State: "live"}); err != nil {
		t.Fatalf("add other job's thread: %v", err)
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// Carl killing the thread between the hit and the record does not turn
	// the thread's turn into zeus'.
	hit, err := RecordLimitHit(j, "rate_limit", "429", "closed-sess", "", now)
	if err != nil {
		t.Fatalf("closed thread: %v", err)
	}
	if hit.Thread != closed.TID {
		t.Errorf("closed thread's delivery recorded as %+v, want Thread %q", hit, closed.TID)
	}
	// A session that is a thread of a DIFFERENT job is not this job's thread:
	// it takes the ordinary path, exactly as an unknown session does.
	hit, err = RecordLimitHit(j, "rate_limit", "429", "other-sess", "", now)
	if err != nil {
		t.Fatalf("other job's thread session: %v", err)
	}
	if hit.Thread != "" || hit.HandledAt != 0 {
		t.Errorf("another job's thread session recorded as this job's thread: %+v", hit)
	}
	if got := ReadLimitHit(j); got.At != now.Unix() {
		t.Errorf("job row = %+v, want the ordinary record", got)
	}
}

func TestRecordThreadLimitHitNotifiesOncePerIncident(t *testing.T) {
	st, j, notes := tlhWorld(t)
	th := tlhThread(t, st, j, "thread-sess")
	t0 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	for i, at := range []time.Time{t0, t0.Add(time.Minute), t0.Add(threadLimitHitNotifyGap - time.Second)} {
		hit, err := RecordLimitHit(j, "rate_limit", "429", "thread-sess", "", at)
		if err != nil {
			t.Fatalf("delivery %d: %v", i, err)
		}
		// Resolved on record, so a repeat never folds: each delivery is its
		// own record with Deliveries 1 and the latest time.
		if hit.Deliveries != 1 || hit.At != at.Unix() || hit.Thread != th.TID {
			t.Errorf("delivery %d recorded as %+v", i, hit)
		}
	}
	if len(*notes) != 1 {
		t.Fatalf("retries inside the gap notified %d times, want 1: %q", len(*notes), *notes)
	}
	if _, err := RecordLimitHit(j, "rate_limit", "429", "thread-sess", "", t0.Add(threadLimitHitNotifyGap+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(*notes) != 2 {
		t.Errorf("a delivery past the gap did not notify again: %q", *notes)
	}
}

func TestThreadLimitHitKeyNeverCollidesWithAJobOrStandingKey(t *testing.T) {
	key := threadLimitHitKey(42, "t2")
	if key != "limit_hit:42:t2" {
		t.Errorf("threadLimitHitKey = %q", key)
	}
	if key == limitHitKey(42) || key == standingHitKey("iris") {
		t.Errorf("thread key %q collides with an owner key", key)
	}
}

func TestRecordLimitHitMirrorRecordsAThreadHitWithoutNotifying(t *testing.T) {
	st, j, notes := tlhWorld(t)
	th := tlhThread(t, st, j, "thread-sess")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	hit, err := RecordLimitHitMirror(j, "rate_limit", "429", "thread-sess", "", now)
	if err != nil {
		t.Fatalf("RecordLimitHitMirror: %v", err)
	}
	// The record is the live form's, byte for byte.
	if hit.Thread != th.TID || hit.Outcome != "thread-hit" || hit.HandledAt != now.Unix() {
		t.Errorf("mirror thread record = %+v, want the same resolved record the live serve writes", hit)
	}
	if stored := readLimitHitByKey(threadLimitHitKey(j.ID, th.TID)); stored.At != now.Unix() {
		t.Errorf("mirror did not write the thread row: %+v", stored)
	}
	if got := ReadLimitHit(j); got.At != 0 {
		t.Errorf("mirror wrote the job row for a thread's delivery: %+v", got)
	}
	// And it never acts: no notification, a mirror records only.
	if len(*notes) != 0 {
		t.Errorf("a mirror notified: %q", *notes)
	}
}
