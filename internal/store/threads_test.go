package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

// seedThreadJob creates a job to hang threads off, since every threads row
// carries a real jobs FK (ON DELETE CASCADE) — inserting against a
// made-up job id would be refused by _foreign_keys=on.
func seedThreadJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug})
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return id
}

// TestThreadRoundTrip pins that every column survives an add → list → get
// round trip, including the ones that only ever come from the fork itself
// (parent_session, fork_uuid, seed) — a dropped column in threadColumns or
// scanThread's argument order would silently blank or transpose a field, and
// the store is the only place that shape is asserted.
func TestThreadRoundTrip(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "thready")

	want := Thread{
		JobID:         jobID,
		TID:           "t1",
		Title:         "why does the daemon bump twice",
		SessionID:     "sess-abc",
		ParentSession: "zeus-xyz",
		ForkUUID:      "msg-uuid-9",
		CWD:           "/tmp/wt-kraken",
		Pane:          "%7",
		Window:        "@3",
		State:         "live",
		Seed:          "look at watchPRs",
		CreatedAt:     1000,
	}
	if err := s.AddThread(want); err != nil {
		t.Fatalf("AddThread: %v", err)
	}

	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListThreads = %+v, want exactly one row", list)
	}
	if list[0] != want {
		t.Fatalf("ListThreads[0] = %+v, want %+v", list[0], want)
	}

	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if *got != want {
		t.Fatalf("GetThread = %+v, want %+v", *got, want)
	}
}

// TestAddThreadDefaultsStateAndCreatedAt covers the two convenience stamps
// (the AddFeedback precedent): an empty State means "live" — what a freshly
// forked thread always is — and a zero CreatedAt is stamped with now rather
// than stored as the epoch, which anything reading a thread's age would
// render as 1970. A live row's ClosedAt stays 0, the "still live" sentinel.
func TestAddThreadDefaultsStateAndCreatedAt(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "defaults")

	if err := s.AddThread(Thread{JobID: jobID, TID: "t1"}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.State != "live" {
		t.Fatalf("State = %q, want live", got.State)
	}
	if got.CreatedAt == 0 {
		t.Fatalf("CreatedAt = 0, want a stamped timestamp: %+v", got)
	}
	if got.ClosedAt != 0 {
		t.Fatalf("ClosedAt = %d, want 0 on a fresh live thread", got.ClosedAt)
	}
}

// TestNextThreadIDNeverReuses is NextThreadID's whole contract: it counts
// from MAX(numeric suffix)+1 over existing rows, NOT from a count, so a
// deleted thread must not drag the next tid back down onto a name that is
// still in use. With {t1, t2} and t1 deleted, a count-based scheme hands out
// t2 — which the live thread already answers to; MAX-based gives t3.
func TestNextThreadIDNeverReuses(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "counter")

	next, err := s.NextThreadID(jobID)
	if err != nil {
		t.Fatalf("NextThreadID on an empty job: %v", err)
	}
	if next != "t1" {
		t.Fatalf("first NextThreadID = %q, want t1", next)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", CreatedAt: 1}); err != nil {
		t.Fatalf("AddThread t1: %v", err)
	}

	next, err = s.NextThreadID(jobID)
	if err != nil {
		t.Fatalf("NextThreadID after t1: %v", err)
	}
	if next != "t2" {
		t.Fatalf("second NextThreadID = %q, want t2", next)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t2", CreatedAt: 2}); err != nil {
		t.Fatalf("AddThread t2: %v", err)
	}

	// Delete a row out from under it (raw SQL — this package exposes no
	// delete verb for threads, and the point is to simulate a row vanishing
	// by any means). t1 goes, t2 stays live.
	if _, err := s.db.Exec(`DELETE FROM threads WHERE job_id = ? AND tid = 't1'`, jobID); err != nil {
		t.Fatalf("delete t1: %v", err)
	}
	next, err = s.NextThreadID(jobID)
	if err != nil {
		t.Fatalf("NextThreadID after deleting t1: %v", err)
	}
	if next != "t3" {
		t.Fatalf("NextThreadID after deleting t1 = %q, want t3 (a count would collide with the live t2)", next)
	}
	// and the number it hands out is genuinely free
	if err := s.AddThread(Thread{JobID: jobID, TID: next, CreatedAt: 3}); err != nil {
		t.Fatalf("AddThread %s: %v", next, err)
	}
}

// TestNextThreadIDIsPerJob pins that the counter is scoped to one job: two
// jobs each start at t1, and one job's threads never push another's number
// forward.
func TestNextThreadIDIsPerJob(t *testing.T) {
	s := openTest(t)
	a := seedThreadJob(t, s, "job-a")
	b := seedThreadJob(t, s, "job-b")

	for _, tid := range []string{"t1", "t2", "t3"} {
		if err := s.AddThread(Thread{JobID: a, TID: tid, CreatedAt: 1}); err != nil {
			t.Fatalf("AddThread %s on job-a: %v", tid, err)
		}
	}
	next, err := s.NextThreadID(b)
	if err != nil {
		t.Fatalf("NextThreadID on job-b: %v", err)
	}
	if next != "t1" {
		t.Fatalf("job-b NextThreadID = %q, want t1 (job-a's threads must not count)", next)
	}
}

// TestListThreadsOrdersNumerically is the reason ListThreads orders by `id`
// instead of tid: tid is TEXT, so ORDER BY tid would sort "t10" between "t1"
// and "t2". Ten threads is the smallest fixture that actually catches it.
func TestListThreadsOrdersNumerically(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "many")

	want := []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10"}
	for i, tid := range want {
		if err := s.AddThread(Thread{JobID: jobID, TID: tid, CreatedAt: int64(i + 1)}); err != nil {
			t.Fatalf("AddThread %s: %v", tid, err)
		}
	}

	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != len(want) {
		t.Fatalf("ListThreads returned %d rows, want %d", len(list), len(want))
	}
	for i, w := range want {
		if list[i].TID != w {
			got := make([]string, len(list))
			for j, th := range list {
				got[j] = th.TID
			}
			t.Fatalf("ListThreads order = %v, want %v (lexicographic tid ordering would put t10 second)", got, want)
		}
	}
}

// TestListThreadsIgnoresCreatedAt is why the ORDER BY is `id` alone rather
// than `created_at, id`: created_at is not a trustworthy sort key. It is
// caller-settable (an importer replaying history sets it; so do these
// tests), millisecond-resolution so two rows can tie, and even the stamped
// path reads a clock NTP can step BACKWARDS. Ordering by the AUTOINCREMENT
// id is insertion order by construction, so tid order survives all three:
// here t1..t3 are written with timestamps running the wrong way and t4..t6
// with identical ones, and the listing must still come back t1..t6.
func TestListThreadsIgnoresCreatedAt(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "clock-skew")

	// descending timestamps: a created_at-first sort would reverse these
	for i, tid := range []string{"t1", "t2", "t3"} {
		if err := s.AddThread(Thread{JobID: jobID, TID: tid, CreatedAt: int64(900 - i)}); err != nil {
			t.Fatalf("AddThread %s: %v", tid, err)
		}
	}
	// identical timestamps: no tiebreaker left except id
	for _, tid := range []string{"t4", "t5", "t6"} {
		if err := s.AddThread(Thread{JobID: jobID, TID: tid, CreatedAt: 777}); err != nil {
			t.Fatalf("AddThread %s: %v", tid, err)
		}
	}

	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	want := []string{"t1", "t2", "t3", "t4", "t5", "t6"}
	if len(list) != len(want) {
		t.Fatalf("ListThreads returned %d rows, want %d", len(list), len(want))
	}
	for i, w := range want {
		if list[i].TID != w {
			got := make([]string, len(list))
			for j, th := range list {
				got[j] = th.TID
			}
			t.Fatalf("ListThreads order = %v, want %v (created_at is not a sort key)", got, want)
		}
	}
}

// TestAddThreadRefusesDuplicateTID covers the (job_id, tid) unique index and
// AddThread's clear error for it — the same tid twice within one job is a
// bug in the caller's numbering, never a silent overwrite. The same tid in a
// DIFFERENT job is fine and must still be accepted (the index is composite).
//
// The error must be errors.Is-comparable as ErrThreadIDTaken: the API layer
// turns a collision into a 409 (recoverable — renumber and retry) and any
// other failure into a 500, and it must not have to substring-match an error
// string to tell them apart.
func TestAddThreadRefusesDuplicateTID(t *testing.T) {
	s := openTest(t)
	a := seedThreadJob(t, s, "dup-a")
	b := seedThreadJob(t, s, "dup-b")

	if err := s.AddThread(Thread{JobID: a, TID: "t1", Title: "first"}); err != nil {
		t.Fatalf("AddThread first: %v", err)
	}
	err := s.AddThread(Thread{JobID: a, TID: "t1", Title: "second"})
	if !errors.Is(err, ErrThreadIDTaken) {
		t.Fatalf("AddThread with a duplicate (job_id, tid) = %v, want ErrThreadIDTaken", err)
	}

	// and the original must be untouched, not overwritten by the refused write
	got, gerr := s.GetThread(a, "t1")
	if gerr != nil {
		t.Fatalf("GetThread after refused duplicate: %v", gerr)
	}
	if got.Title != "first" {
		t.Fatalf("refused duplicate clobbered the original: %+v", got)
	}

	if err := s.AddThread(Thread{JobID: b, TID: "t1"}); err != nil {
		t.Fatalf("AddThread t1 on a different job: %v", err)
	}
}

// TestCloseAndReviveThread covers the close/revive cycle: closing clears the
// tmux coordinates (a recycled pane id must never keep pointing at a dead
// thread) and stamps closed_at, while reviving — ClaimClosedThread, then
// SetThreadPane once the new window exists — restores 'live' with fresh
// coordinates and zeroes closed_at, leaving identity/provenance (session_id,
// parent_session, seed) untouched throughout, since reviving resumes the same
// claude session rather than forking a new one.
//
// The intermediate state matters as much as the endpoints: between the two
// calls the row is live-but-paneless (the claim can't know coordinates that
// don't exist yet), which is the same shape a freshly branched row sits in
// and which LiveThreadPanes already excludes.
func TestCloseAndReviveThread(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "cycle")

	if err := s.AddThread(Thread{
		JobID: jobID, TID: "t1", SessionID: "sess-1", ParentSession: "zeus-1",
		Seed: "dig into rotation", Pane: "%4", Window: "@2", CreatedAt: 10,
	}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}

	if err := s.CloseThread(jobID, "t1", 500); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}
	closed, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread after close: %v", err)
	}
	if closed.State != "closed" || closed.Pane != "" || closed.Window != "" || closed.ClosedAt != 500 {
		t.Fatalf("after CloseThread = %+v, want closed with pane/window cleared and closed_at 500", closed)
	}
	if closed.SessionID != "sess-1" || closed.ParentSession != "zeus-1" || closed.Seed != "dig into rotation" {
		t.Fatalf("CloseThread disturbed identity/provenance: %+v", closed)
	}

	if err := s.ClaimClosedThread(jobID, "t1"); err != nil {
		t.Fatalf("ClaimClosedThread: %v", err)
	}
	claimed, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread after claim: %v", err)
	}
	if claimed.State != "live" || claimed.ClosedAt != 0 {
		t.Fatalf("after ClaimClosedThread = %+v, want live with closed_at 0", claimed)
	}
	if claimed.Pane != "" || claimed.Window != "" {
		t.Fatalf("claim kept the stale coordinates %q/%q, want both cleared: %+v",
			claimed.Pane, claimed.Window, claimed)
	}

	if err := s.SetThreadPane(jobID, "t1", "%9", "@5"); err != nil {
		t.Fatalf("SetThreadPane: %v", err)
	}
	live, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread after SetThreadPane: %v", err)
	}
	if live.State != "live" || live.Pane != "%9" || live.Window != "@5" || live.ClosedAt != 0 {
		t.Fatalf("after revive = %+v, want live at %%9/@5 with closed_at 0", live)
	}
	if live.SessionID != "sess-1" || live.ParentSession != "zeus-1" ||
		live.Seed != "dig into rotation" || live.CreatedAt != 10 {
		t.Fatalf("revive disturbed identity/provenance/creation: %+v", live)
	}
}

// TestClaimClosedThreadIsExclusive is the whole point of ClaimClosedThread,
// and the bug it was written for: with the old unconditional OpenThread, two
// concurrent revives of one thread both passed their discovery scan, both
// opened a tmux window, and both "succeeded" — leaving two
// `claude -r <same session id>` processes writing one transcript, the
// unrecoverable outcome. net/http serves handlers concurrently and nothing in
// that path holds a lock, so the exclusion has to live in the DATABASE.
//
// N goroutines race one closed row: exactly one nil error, N-1
// ErrThreadNotClosed, and the losers must be able to tell that apart from a
// missing row so they report "already live" rather than "unknown thread".
func TestClaimClosedThreadIsExclusive(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "revive-race")

	if err := s.AddThread(Thread{
		JobID: jobID, TID: "t1", SessionID: "sess-1", State: "closed", ClosedAt: 5,
	}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together, to actually contend
			errs[i] = s.ClaimClosedThread(jobID, "t1")
		}(i)
	}
	close(start)
	wg.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrThreadNotClosed):
			// a loser, as designed — it must spawn nothing
		default:
			t.Fatalf("concurrent ClaimClosedThread %d = %v, want nil or ErrThreadNotClosed", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("%d goroutines won the claim, want exactly 1 (two winners = two claudes on one session id)", winners)
	}

	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread after the race: %v", err)
	}
	if got.State != "live" || got.ClosedAt != 0 {
		t.Fatalf("after the race = %+v, want live with closed_at 0", got)
	}
}

// TestClaimClosedThreadErrors separates the two ways a claim fails, which the
// caller must react to in opposite ways: an unknown tid is a 404 and a caller
// bug, while an already-live row means this caller LOST a race that is
// working as designed and must quietly spawn nothing.
func TestClaimClosedThreadErrors(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "claim-errs")

	if err := s.ClaimClosedThread(jobID, "t7"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClaimClosedThread on a missing tid = %v, want ErrNotFound", err)
	}

	// a live row: claiming it must fail rather than silently re-open it, and
	// must NOT disturb the coordinates its running claude is actually in.
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", Pane: "%4", Window: "@2"}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	err := s.ClaimClosedThread(jobID, "t1")
	if !errors.Is(err, ErrThreadNotClosed) {
		t.Fatalf("ClaimClosedThread on a live tid = %v, want ErrThreadNotClosed", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("ClaimClosedThread on a live tid also matched ErrNotFound: %v", err)
	}
	got, gerr := s.GetThread(jobID, "t1")
	if gerr != nil {
		t.Fatalf("GetThread after refused claim: %v", gerr)
	}
	if got.Pane != "%4" || got.Window != "@2" {
		t.Fatalf("refused claim cleared a live thread's coordinates: %+v", got)
	}
}

// TestSetThreadPaneLeavesStateAlone pins that recording coordinates is ONLY
// that. If SetThreadPane also flipped state it would be the unconditional
// open all over again — the verb two racing revives could both call to
// "succeed" — so a closed row must come back from it still closed, with its
// closed_at intact.
func TestSetThreadPaneLeavesStateAlone(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "set-pane")

	if err := s.AddThread(Thread{
		JobID: jobID, TID: "t1", SessionID: "sess-1", State: "closed", ClosedAt: 77,
	}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	if err := s.SetThreadPane(jobID, "t1", "%9", "@5"); err != nil {
		t.Fatalf("SetThreadPane: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.State != "closed" || got.ClosedAt != 77 {
		t.Fatalf("SetThreadPane changed state: %+v, want still closed at 77", got)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("SetThreadPane disturbed provenance: %+v", got)
	}

	if err := s.SetThreadPane(jobID, "t9", "%1", "@1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetThreadPane on a missing tid = %v, want ErrNotFound", err)
	}
}

// TestCloseThreadStampsNow pins CloseThread's zero-closedAt convenience: a
// caller that doesn't care about the exact instant gets now, not the epoch
// (which would read as "still live" against the ClosedAt == 0 convention).
func TestCloseThreadStampsNow(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "stamp")

	if err := s.AddThread(Thread{JobID: jobID, TID: "t1"}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	if err := s.CloseThread(jobID, "t1", 0); err != nil {
		t.Fatalf("CloseThread: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.ClosedAt == 0 {
		t.Fatalf("ClosedAt = 0 after CloseThread(…, 0), want a stamped timestamp: %+v", got)
	}
}

// TestSetThreadTitle covers the rename verb and its ErrNotFound.
func TestSetThreadTitle(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "titles")

	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", Title: "untitled", SessionID: "sess-1"}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	if err := s.SetThreadTitle(jobID, "t1", "rotation gate spelunking"); err != nil {
		t.Fatalf("SetThreadTitle: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.Title != "rotation gate spelunking" {
		t.Fatalf("Title = %q, want the new one", got.Title)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("SetThreadTitle disturbed other columns: %+v", got)
	}

	if err := s.SetThreadTitle(jobID, "t9", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetThreadTitle on a missing tid = %v, want ErrNotFound", err)
	}
}

// TestLiveThreadPanesSpansJobs pins the daemon's roll-call query: every live
// thread's pane across ALL jobs, with closed threads and empty panes
// excluded — a stale pane id in that set would make the daemon (and anything
// deriving a send/kill target from it) address a pane tmux has since
// recycled for something else.
func TestLiveThreadPanesSpansJobs(t *testing.T) {
	s := openTest(t)
	a := seedThreadJob(t, s, "panes-a")
	b := seedThreadJob(t, s, "panes-b")

	if err := s.AddThread(Thread{JobID: a, TID: "t1", Pane: "%1", Window: "@1"}); err != nil {
		t.Fatalf("AddThread a/t1: %v", err)
	}
	if err := s.AddThread(Thread{JobID: a, TID: "t2", Pane: "%2", Window: "@1"}); err != nil {
		t.Fatalf("AddThread a/t2: %v", err)
	}
	if err := s.AddThread(Thread{JobID: b, TID: "t1", Pane: "%3", Window: "@2"}); err != nil {
		t.Fatalf("AddThread b/t1: %v", err)
	}
	// live but pane-less: a thread whose row exists before its pane does.
	if err := s.AddThread(Thread{JobID: b, TID: "t2"}); err != nil {
		t.Fatalf("AddThread b/t2: %v", err)
	}
	// closed: CloseThread cleared %2, so it must not appear either.
	if err := s.CloseThread(a, "t2", 0); err != nil {
		t.Fatalf("CloseThread a/t2: %v", err)
	}

	panes, err := s.LiveThreadPanes()
	if err != nil {
		t.Fatalf("LiveThreadPanes: %v", err)
	}
	want := map[string]bool{"%1": true, "%3": true}
	if len(panes) != len(want) {
		t.Fatalf("LiveThreadPanes = %v, want %v", panes, want)
	}
	for p := range want {
		if !panes[p] {
			t.Fatalf("LiveThreadPanes = %v, want %v", panes, want)
		}
	}
}

// TestThreadValidation covers the two locally-validated fields — the API
// layer turns these errors straight into a 400, so they must come back as
// errors rather than as a raw CHECK-constraint failure (or, worse, a stored
// tid the numeric ordering can't handle).
func TestThreadValidation(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "validate")

	for _, tid := range []string{"", "t0", "t01", "1", "thread-1", "T1", "t1x", " t1"} {
		if err := s.AddThread(Thread{JobID: jobID, TID: tid}); err == nil {
			t.Fatalf("AddThread with tid %q: want error, got nil", tid)
		}
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", State: "sleeping"}); err == nil {
		t.Fatal("AddThread with state \"sleeping\": want error, got nil")
	}
	// the two real states are both accepted (a closed thread can be inserted
	// directly, e.g. by an importer replaying history).
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", State: "live"}); err != nil {
		t.Fatalf("AddThread state live: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t2", State: "closed", ClosedAt: 9}); err != nil {
		t.Fatalf("AddThread state closed: %v", err)
	}
}

// TestGetThreadNotFound pins the ErrNotFound contract (errors.Is-comparable,
// like every other single-row lookup in this package) and the nil row that
// comes with it — GetThread returns *Thread, so a caller that checks the
// error must never find a zero-value struct waiting behind it.
func TestGetThreadNotFound(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "missing")

	got, err := s.GetThread(jobID, "t7")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetThread on a missing tid = %v, want ErrNotFound", err)
	}
	if got != nil {
		t.Fatalf("GetThread on a missing tid returned %+v, want nil", got)
	}
	if err := s.CloseThread(jobID, "t7", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("CloseThread on a missing tid = %v, want ErrNotFound", err)
	}
	if err := s.ClaimClosedThread(jobID, "t7"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ClaimClosedThread on a missing tid = %v, want ErrNotFound", err)
	}
}

// TestThreadsCascadeOnJobDelete pins the FK's ON DELETE CASCADE: threads are
// wholly owned by their job (unlike feedback, which survives as a fossil via
// ON DELETE SET NULL), so hard-deleting the job must take its thread rows
// with it rather than orphan them into LiveThreadPanes' roll call forever.
func TestThreadsCascadeOnJobDelete(t *testing.T) {
	s := openTest(t)
	doomed := seedThreadJob(t, s, "doomed")
	survivor := seedThreadJob(t, s, "survivor")

	if err := s.AddThread(Thread{JobID: doomed, TID: "t1", Pane: "%1"}); err != nil {
		t.Fatalf("AddThread doomed: %v", err)
	}
	if err := s.AddThread(Thread{JobID: survivor, TID: "t1", Pane: "%2"}); err != nil {
		t.Fatalf("AddThread survivor: %v", err)
	}

	if err := s.DeleteJob(doomed); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	list, err := s.ListThreads(doomed)
	if err != nil {
		t.Fatalf("ListThreads after DeleteJob: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("threads survived their job's delete: %+v", list)
	}
	panes, err := s.LiveThreadPanes()
	if err != nil {
		t.Fatalf("LiveThreadPanes: %v", err)
	}
	if panes["%1"] {
		t.Fatalf("deleted job's pane still in the live roll call: %v", panes)
	}
	if !panes["%2"] {
		t.Fatalf("surviving job's pane missing from the roll call: %v", panes)
	}
}

// TestAddThreadNextAllocates is the creation path's contract: the tid comes
// from the store, not the caller, and the row that comes back is the row the
// database actually holds. Sequential calls hand out t1 then t2 (never the
// same number twice), and any TID the caller pre-set is ignored — leaving it
// to the caller is exactly the race AddThreadNext exists to remove.
func TestAddThreadNextAllocates(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "alloc")

	first, err := s.AddThreadNext(Thread{JobID: jobID, Title: "first", Seed: "look at watchPRs"})
	if err != nil {
		t.Fatalf("AddThreadNext first: %v", err)
	}
	if first.TID != "t1" {
		t.Fatalf("first AddThreadNext tid = %q, want t1", first.TID)
	}

	// a caller-set TID must be overwritten, not honored: "t9" here would
	// otherwise leave a gap and desync the counter from the rows.
	second, err := s.AddThreadNext(Thread{JobID: jobID, TID: "t9", Title: "second"})
	if err != nil {
		t.Fatalf("AddThreadNext second: %v", err)
	}
	if second.TID != "t2" {
		t.Fatalf("second AddThreadNext tid = %q, want t2 (a caller-set TID must be ignored)", second.TID)
	}

	// the returned row is the stored row, field for field
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread t1: %v", err)
	}
	if *got != first {
		t.Fatalf("GetThread = %+v, want the returned row %+v", *got, first)
	}
	if first.CreatedAt == 0 || first.State != "live" {
		t.Fatalf("AddThreadNext returned an unstamped row: %+v", first)
	}

	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 2 || list[0].TID != "t1" || list[1].TID != "t2" {
		t.Fatalf("ListThreads = %+v, want exactly t1 and t2", list)
	}
}

// TestAddThreadNextIsAtomic pins the reason AddThreadNext exists at all: the
// allocate and the insert are ONE transaction, so a concurrent branch can
// never be handed a tid another branch is already inserting under. With the
// old NextThreadID-then-AddThread pair, N concurrent callers all read the
// same MAX and all but one lost — and the caller (jobs.BranchThread) stages
// its spawn files under the tid BEFORE inserting, so the losers' writes
// stomped the winner's and a thread came up briefed with another's seed.
//
// Every goroutine must therefore succeed, and the tids must be exactly
// t1..tN with no duplicate and no gap.
func TestAddThreadNextIsAtomic(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "concurrent")

	const n = 8
	var wg sync.WaitGroup
	tids := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release them together, to actually contend
			th, err := s.AddThreadNext(Thread{JobID: jobID, Title: fmt.Sprintf("branch %d", i)})
			tids[i], errs[i] = th.TID, err
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[string]bool{}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent AddThreadNext %d: %v", i, err)
		}
		if seen[tids[i]] {
			t.Fatalf("tid %q handed out twice: %v", tids[i], tids)
		}
		seen[tids[i]] = true
	}
	for i := 1; i <= n; i++ {
		if want := fmt.Sprintf("t%d", i); !seen[want] {
			t.Fatalf("tid %s missing — allocation left a gap: %v", want, tids)
		}
	}

	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != n {
		t.Fatalf("ListThreads returned %d rows, want %d — an insert was lost", len(list), n)
	}
}

// TestAddThreadClosedStampsClosedAt covers the closed-on-insert path. A row
// stored as state='closed' with closed_at=0 contradicts the column's own
// documented meaning ("0 means still live"), so a consumer deriving liveness
// from ClosedAt == 0 would read it as live and a UI would render the epoch.
// The same insert must also drop pane/window — precisely what CloseThread
// clears, and for the same reason: tmux recycles pane ids, so coordinates on
// a closed row point at a stranger's pane.
func TestAddThreadClosedStampsClosedAt(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "born-closed")

	if err := s.AddThread(Thread{
		JobID: jobID, TID: "t1", State: "closed",
		Pane: "%4", Window: "@2", SessionID: "sess-1",
	}); err != nil {
		t.Fatalf("AddThread closed: %v", err)
	}
	got, err := s.GetThread(jobID, "t1")
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if got.ClosedAt == 0 {
		t.Fatalf("ClosedAt = 0 on a closed row, want a stamp (0 means still live): %+v", got)
	}
	if got.Pane != "" || got.Window != "" {
		t.Fatalf("closed row kept tmux coordinates %q/%q, want both cleared: %+v", got.Pane, got.Window, got)
	}
	if got.SessionID != "sess-1" {
		t.Fatalf("closed-on-insert disturbed provenance: %+v", got)
	}

	// an explicit ClosedAt is honored as-is, not overwritten by now
	if err := s.AddThread(Thread{JobID: jobID, TID: "t2", State: "closed", ClosedAt: 42}); err != nil {
		t.Fatalf("AddThread closed with explicit ClosedAt: %v", err)
	}
	got, err = s.GetThread(jobID, "t2")
	if err != nil {
		t.Fatalf("GetThread t2: %v", err)
	}
	if got.ClosedAt != 42 {
		t.Fatalf("ClosedAt = %d, want the caller's 42", got.ClosedAt)
	}

	// and a live row is left entirely alone by that path
	if err := s.AddThread(Thread{JobID: jobID, TID: "t3", Pane: "%9", Window: "@1"}); err != nil {
		t.Fatalf("AddThread live: %v", err)
	}
	got, err = s.GetThread(jobID, "t3")
	if err != nil {
		t.Fatalf("GetThread t3: %v", err)
	}
	if got.ClosedAt != 0 || got.Pane != "%9" || got.Window != "@1" {
		t.Fatalf("live insert was treated as closed: %+v", got)
	}
}

// TestThreadIDSuffixIsBounded is the wedge guard. The shape regexp
// (^t[1-9][0-9]*$) bounds nothing numerically, so an oversized tid like
// t99999999999999999999 used to store fine — and then NextThreadID's
// CAST(... AS INTEGER) CLAMPED it to int64 max (SQLite does not error on
// that cast), Go's max+1 wrapped negative, and the job was handed
// "t-9223372036854775808": a tid that fails validation forever, so that job
// could never create another thread. Bounding the suffix at write time is
// what makes NextThreadID's arithmetic total.
func TestThreadIDSuffixIsBounded(t *testing.T) {
	s := openTest(t)
	jobID := seedThreadJob(t, s, "bounded")

	for _, tid := range []string{
		"t99999999999999999999",               // clamps the CAST to int64 max
		"t9223372036854775807",                // int64 max exactly: max+1 wraps
		"t9223372036854775808",                // one past int64 max: ParseInt overflows
		fmt.Sprintf("t%d", maxThreadSuffix+1), // just past the cap
	} {
		if err := s.AddThread(Thread{JobID: jobID, TID: tid}); err == nil {
			t.Fatalf("AddThread with an out-of-range tid %q: want error, got nil", tid)
		}
	}

	// the boundary value itself is storable — the cap is inclusive
	big := fmt.Sprintf("t%d", maxThreadSuffix)
	if err := s.AddThread(Thread{JobID: jobID, TID: big}); err != nil {
		t.Fatalf("AddThread with the boundary tid %s: %v", big, err)
	}
	// ...and having stored it, the counter reports an honest error rather
	// than handing out a negative tid nothing can ever use.
	if _, err := s.NextThreadID(jobID); err == nil {
		t.Fatal("NextThreadID past the cap: want error, got nil")
	}

	// a normal job is entirely unaffected
	other := seedThreadJob(t, s, "unbounded-neighbor")
	if err := s.AddThread(Thread{JobID: other, TID: "t1"}); err != nil {
		t.Fatalf("AddThread t1: %v", err)
	}
	next, err := s.NextThreadID(other)
	if err != nil {
		t.Fatalf("NextThreadID: %v", err)
	}
	if next != "t2" {
		t.Fatalf("NextThreadID = %q, want t2", next)
	}
}
