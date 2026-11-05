package jobs

import (
	"errors"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// jobfreeze_test.go — SetJobFrozen and the thaw a state write performs,
// seen from the jobs layer: the in-memory Job a caller holds has to agree
// with the row after each.

func loadedJob(t *testing.T, slug string) *Job {
	t.Helper()
	j := JobBySlug(LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s is not in LoadJobs", slug)
	}
	return j
}

func TestSetJobFrozenMirrorsIntoJobAndRow(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "hold", State: "blocked", Body: "spec"})

	j := loadedJob(t, "hold")
	if j.FrozenAt != 0 {
		t.Fatalf("fresh job FrozenAt = %d, want 0", j.FrozenAt)
	}
	if err := SetJobFrozen(j, true); err != nil {
		t.Fatalf("SetJobFrozen(true): %v", err)
	}
	if j.FrozenAt == 0 {
		t.Fatal("SetJobFrozen(true) left the in-memory job unfrozen")
	}
	if got := loadedJob(t, "hold").FrozenAt; got != j.FrozenAt {
		t.Fatalf("reloaded FrozenAt = %d, want the stamp %d the caller holds", got, j.FrozenAt)
	}

	if err := SetJobFrozen(j, false); err != nil {
		t.Fatalf("SetJobFrozen(false): %v", err)
	}
	if j.FrozenAt != 0 || loadedJob(t, "hold").FrozenAt != 0 {
		t.Fatalf("after thaw: in-memory %d, row %d, want both 0", j.FrozenAt, loadedJob(t, "hold").FrozenAt)
	}
}

func TestSetJobFrozenRefusesAJobThatIsNotBlocked(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "busy", State: "working", Body: "spec"})

	j := loadedJob(t, "busy")
	err := SetJobFrozen(j, true)
	if !errors.Is(err, store.ErrJobNotBlocked) {
		t.Fatalf("freeze a working job: err = %v, want store.ErrJobNotBlocked", err)
	}
	if j.FrozenAt != 0 {
		t.Fatalf("a refused freeze stamped the in-memory job: %d", j.FrozenAt)
	}
}

// The store's own state is what the freeze is judged against, not the copy
// the caller loaded: a cell that started working after the load is refused.
func TestSetJobFrozenRefusesAStaleBlockedRead(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "woke", State: "blocked", Body: "spec"})

	stale := loadedJob(t, "woke")
	if err := WriteJobState(loadedJob(t, "woke"), "working"); err != nil {
		t.Fatalf("WriteJobState: %v", err)
	}
	if err := SetJobFrozen(stale, true); !errors.Is(err, store.ErrJobNotBlocked) {
		t.Fatalf("freeze on a stale blocked read: err = %v, want store.ErrJobNotBlocked", err)
	}
	if got := loadedJob(t, "woke").FrozenAt; got != 0 {
		t.Fatalf("the working job was frozen: FrozenAt = %d", got)
	}
}

func TestSetJobFrozenWithoutAStore(t *testing.T) {
	UseStore(nil)
	if err := SetJobFrozen(&Job{Slug: "nowhere", State: "blocked"}, true); err == nil {
		t.Fatal("SetJobFrozen with no store wired returned nil; a hold that wrote nothing must not report success")
	}
}

// The requirement itself: a frozen job whose cell is seen working again
// thaws, and reads as an ordinary blocked job the next time it stops. The
// daemon's part in that is one WriteJobState per flip (daemon.go), which is
// what this drives.
func TestFrozenJobThawsWhenItsStateLeavesBlocked(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "watch", State: "blocked", Body: "spec"})

	j := loadedJob(t, "watch")
	if err := SetJobFrozen(j, true); err != nil {
		t.Fatal(err)
	}

	// the cell wakes: the tick loads the job and writes working
	tick := loadedJob(t, "watch")
	if tick.FrozenAt == 0 {
		t.Fatal("the next load lost the hold before anything thawed it")
	}
	if err := WriteJobState(tick, "working"); err != nil {
		t.Fatalf("WriteJobState(working): %v", err)
	}
	if tick.FrozenAt != 0 {
		t.Fatalf("WriteJobState(working) left the in-memory job frozen: %d", tick.FrozenAt)
	}
	if got := loadedJob(t, "watch"); got.State != "working" || got.FrozenAt != 0 {
		t.Fatalf("row after waking: state %q FrozenAt %d, want working/0", got.State, got.FrozenAt)
	}

	// it stops again: blocked, and not frozen
	if err := WriteJobState(loadedJob(t, "watch"), "blocked"); err != nil {
		t.Fatalf("WriteJobState(blocked): %v", err)
	}
	if got := loadedJob(t, "watch"); got.State != "blocked" || got.FrozenAt != 0 {
		t.Fatalf("row after stopping again: state %q FrozenAt %d, want blocked/0", got.State, got.FrozenAt)
	}
}

// stubFrozenRan swaps the transcript seam for the length of one test.
func stubFrozenRan(t *testing.T, ran bool) *int {
	t.Helper()
	calls := 0
	prev := frozenRanSince
	frozenRanSince = func(*Job, []*core.Peer) bool {
		calls++
		return ran
	}
	t.Cleanup(func() { frozenRanSince = prev })
	return &calls
}

// The between-ticks turn: the cell ran and stopped without any tick reading
// it as working. The hold is cleared on the tick that sees the transcript
// moved, and the operator is told on the next one.
func TestFrozenThawClearsThenNotifiesOneTickLater(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "quick", State: "blocked", Body: "spec"})
	j := loadedJob(t, "quick")
	if err := SetJobFrozen(j, true); err != nil {
		t.Fatal(err)
	}
	stubFrozenRan(t, true)

	// tick 7: seen. Thawed now, nothing announced yet.
	notify, pending := frozenThaw(loadedJob(t, "quick"), true, nil, 0, 7)
	if notify || pending != 7 {
		t.Fatalf("tick that sees the turn = (notify %v, pending %d), want (false, 7)", notify, pending)
	}
	if got := loadedJob(t, "quick"); got.FrozenAt != 0 || got.State != "blocked" {
		t.Fatalf("after the thaw: state %q FrozenAt %d, want blocked/0", got.State, got.FrozenAt)
	}

	// tick 8: still blocked. Announce, once.
	notify, pending = frozenThaw(loadedJob(t, "quick"), true, nil, pending, 8)
	if !notify || pending != 0 {
		t.Fatalf("following tick = (notify %v, pending %d), want (true, 0)", notify, pending)
	}

	// tick 9: an ordinary blocked job. Nothing more.
	notify, pending = frozenThaw(loadedJob(t, "quick"), true, nil, pending, 9)
	if notify || pending != 0 {
		t.Fatalf("tick after the announcement = (notify %v, pending %d), want (false, 0)", notify, pending)
	}
}

// An idle frozen job stays frozen: no activity, no thaw, no announcement.
func TestFrozenThawLeavesAnIdleJobFrozen(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "idle", State: "blocked", Body: "spec"})
	j := loadedJob(t, "idle")
	if err := SetJobFrozen(j, true); err != nil {
		t.Fatal(err)
	}
	calls := stubFrozenRan(t, false)

	for tick := 3; tick <= 6; tick++ {
		notify, pending := frozenThaw(loadedJob(t, "idle"), true, nil, 0, tick)
		if notify || pending != 0 {
			t.Fatalf("tick %d on an idle frozen job = (notify %v, pending %d), want (false, 0)", tick, notify, pending)
		}
	}
	if *calls != 4 {
		t.Fatalf("the transcript was asked %d times over four ticks, want 4", *calls)
	}
	if loadedJob(t, "idle").FrozenAt == 0 {
		t.Fatal("an idle frozen job was thawed")
	}
}

// A job that is not frozen never reaches the transcript at all, and a frozen
// one whose cell is gone is left alone: a dead cell cannot have run.
func TestFrozenThawSkipsUnfrozenAndDeadCells(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "plain", State: "blocked", Body: "spec"})
	mustCreateJob(t, st, store.Job{Slug: "dead", State: "blocked", Body: "spec"})
	dead := loadedJob(t, "dead")
	if err := SetJobFrozen(dead, true); err != nil {
		t.Fatal(err)
	}
	calls := stubFrozenRan(t, true)

	if notify, pending := frozenThaw(loadedJob(t, "plain"), true, nil, 0, 5); notify || pending != 0 {
		t.Fatalf("an unfrozen job = (notify %v, pending %d), want (false, 0)", notify, pending)
	}
	if notify, pending := frozenThaw(loadedJob(t, "dead"), false, nil, 0, 5); notify || pending != 0 {
		t.Fatalf("a frozen job with no live cell = (notify %v, pending %d), want (false, 0)", notify, pending)
	}
	if *calls != 0 {
		t.Fatalf("the transcript was asked %d times, want 0", *calls)
	}
	if loadedJob(t, "dead").FrozenAt == 0 {
		t.Fatal("a frozen job with no live cell was thawed")
	}
}

// The announcement needs the IMMEDIATELY following tick, like holdBlocked: a
// pending tick left behind by a skipped roll call confirms nothing later.
func TestFrozenThawAnnouncesOnlyOnTheAdjacentTick(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "gap", State: "blocked", Body: "spec"})
	stubFrozenRan(t, true)

	notify, pending := frozenThaw(loadedJob(t, "gap"), true, nil, 7, 12)
	if notify || pending != 0 {
		t.Fatalf("a pending tick five ticks old = (notify %v, pending %d), want (false, 0)", notify, pending)
	}
}

// Frozen again in the tick between: the operator has just looked at it, so
// the pending announcement is dropped and the new hold stands.
func TestFrozenThawDropsTheAnnouncementWhenRefrozen(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "again", State: "blocked", Body: "spec"})
	j := loadedJob(t, "again")
	if err := SetJobFrozen(j, true); err != nil {
		t.Fatal(err)
	}
	stubFrozenRan(t, true)

	_, pending := frozenThaw(loadedJob(t, "again"), true, nil, 0, 7)
	if err := SetJobFrozen(loadedJob(t, "again"), true); err != nil {
		t.Fatalf("re-freeze: %v", err)
	}
	notify, next := frozenThaw(loadedJob(t, "again"), true, nil, pending, 8)
	if notify || next != 0 {
		t.Fatalf("re-frozen job on the confirming tick = (notify %v, pending %d), want (false, 0)", notify, next)
	}
	if loadedJob(t, "again").FrozenAt == 0 {
		t.Fatal("the confirming tick cleared the operator's new hold")
	}
}

// The tick decides from a job it loaded earlier. If the operator took the
// job off hold and put it back on since that load, the stamp the tick holds
// is superseded: the new hold stands, nothing is pending, nothing is logged
// as a thaw.
func TestFrozenThawLeavesAHoldPlacedSinceTheTickLoadedTheJob(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "raced", State: "blocked", Body: "spec"})
	if err := st.SetJobFrozen(loadedJob(t, "raced").ID, 1000); err != nil {
		t.Fatal(err)
	}
	stale := loadedJob(t, "raced") // what the tick holds: FrozenAt 1000
	if err := st.SetJobFrozen(stale.ID, 2000); err != nil {
		t.Fatal(err)
	}
	stubFrozenRan(t, true)

	notify, pending := frozenThaw(stale, true, nil, 0, 7)
	if notify || pending != 0 {
		t.Fatalf("thaw against a superseded hold = (notify %v, pending %d), want (false, 0)", notify, pending)
	}
	if got := loadedJob(t, "raced").FrozenAt; got != 2000 {
		t.Fatalf("the operator's newer hold was cleared: FrozenAt = %d, want 2000", got)
	}
}

// A blocked → blocked write (nothing does one today, but the field writer
// allows it) and a write of some other field leave the hold alone.
func TestFrozenSurvivesWritesThatStayBlocked(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "still", State: "blocked", Body: "spec"})

	j := loadedJob(t, "still")
	if err := SetJobFrozen(j, true); err != nil {
		t.Fatal(err)
	}
	stamp := j.FrozenAt
	if err := WriteJobFields(j, "session", "abc"); err != nil {
		t.Fatalf("WriteJobFields(session): %v", err)
	}
	if err := WriteJobState(j, "blocked"); err != nil {
		t.Fatalf("WriteJobState(blocked): %v", err)
	}
	if j.FrozenAt != stamp || loadedJob(t, "still").FrozenAt != stamp {
		t.Fatalf("hold moved: in-memory %d, row %d, want %d", j.FrozenAt, loadedJob(t, "still").FrozenAt, stamp)
	}
}

// Done and a kill back to draft both leave blocked, so both thaw.
func TestFrozenClearedByDoneAndDraft(t *testing.T) {
	for _, state := range []string{"done", ""} {
		st := useTestStore(t)
		mustCreateJob(t, st, store.Job{Slug: "end", State: "blocked", Body: "spec"})
		j := loadedJob(t, "end")
		if err := SetJobFrozen(j, true); err != nil {
			t.Fatal(err)
		}
		if err := WriteJobState(j, state); err != nil {
			t.Fatalf("WriteJobState(%q): %v", state, err)
		}
		if j.FrozenAt != 0 || loadedJob(t, "end").FrozenAt != 0 {
			t.Fatalf("state %q left the hold: in-memory %d, row %d", state, j.FrozenAt, loadedJob(t, "end").FrozenAt)
		}
		UseStore(nil)
		st.Close()
	}
}
