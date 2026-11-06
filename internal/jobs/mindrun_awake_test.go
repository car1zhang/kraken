package jobs

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// mindrun_awake_test.go — a mind run on a sleeping machine: the claim waits
// for mindAwakeFloor of ticks with no sleep between them, and the child's
// deadline is measured on the wall clock as well as on the context's.

// awakeT0 is an arbitrary fixture instant with no monotonic reading.
var awakeT0 = time.Date(2030, 1, 2, 3, 0, 0, 0, time.UTC)

func TestAwakeClockUnfedIsUnknown(t *testing.T) {
	var c awakeClock
	if got := c.For(time.Now()); got != awakeUnknown {
		t.Fatalf("unfed clock answers %v, want awakeUnknown", got)
	}
}

// Without monotonic readings (hand-built times) the fallback rule applies:
// a tick-to-tick gap over sleepGap is sleep.
func TestAwakeClockWallGapFallbackCountsFromTheLastSleep(t *testing.T) {
	var c awakeClock
	t0 := awakeT0
	for i := 0; i < 4; i++ { // a dark wake: four ticks over 45s
		c.Note(t0.Add(time.Duration(i) * 15 * time.Second))
	}
	if got := c.For(t0.Add(45 * time.Second)); got != 45*time.Second {
		t.Fatalf("awake for %v after 45s of ticks, want 45s", got)
	}
	t1 := t0.Add(16 * time.Minute) // fifteen minutes of sleep, then ticks again
	c.Note(t1)
	if got := c.For(t1); got != 0 {
		t.Fatalf("awake for %v on the first tick after a gap, want 0", got)
	}
	for i := 1; i <= 24; i++ { // six minutes of steady ticks
		c.Note(t1.Add(time.Duration(i) * 15 * time.Second))
	}
	if got := c.For(t1.Add(6 * time.Minute)); got != 6*time.Minute {
		t.Fatalf("awake for %v after six steady minutes, want 6m", got)
	}
	t2 := t1.Add(6*time.Minute + sleepGap - time.Second) // just under the threshold is not sleep
	c.Note(t2)
	if got := c.For(t2); got < 6*time.Minute {
		t.Fatalf("awake for %v after a sub-threshold gap, want the span kept", got)
	}
	c.Note(t2.Add(-time.Hour)) // a clock that moved backwards restarts the span
	if got := c.For(t2.Add(-time.Hour)); got != 0 {
		t.Fatalf("awake for %v after the clock moved back, want 0", got)
	}
}

// With monotonic readings the sleep itself is measured: a storm of short
// dark wakes never reaches the floor however close its ticks land, and a
// slow tick with no sleep in it keeps the span.
func TestAwakeClockMeasuresSleepNotTickSpacing(t *testing.T) {
	var c awakeClock
	ms := awakeT0.UnixMilli()
	c.Note(awakeT0) // opens the span
	// a storm: ticks 20s apart on the wall clock, 5s of them awake each
	for i := 1; i <= 60; i++ { // twenty minutes of storm
		ms += 20_000
		c.step(ms, 20_000, 5_000, true)
		if got := c.For(time.UnixMilli(ms)); got >= mindAwakeFloor {
			t.Fatalf("storm tick %d: awake for %v, want the floor never reached", i, got)
		}
	}
	// a real wake: 15s ticks, all of it awake, for six minutes
	start := ms
	for i := 1; i <= 24; i++ {
		ms += 15_000
		c.step(ms, 15_000, 15_000, true)
	}
	if got := c.For(time.UnixMilli(ms)); got != time.Duration(ms-start)*time.Millisecond {
		t.Fatalf("awake for %v after six steady minutes, want %v", got, time.Duration(ms-start)*time.Millisecond)
	}
	// a slow tick: 70s on both clocks, over sleepGap, no sleep in it
	ms += 70_000
	c.step(ms, 70_000, 70_000, true)
	if got := c.For(time.UnixMilli(ms)); got < 6*time.Minute {
		t.Fatalf("awake for %v after a slow tick, want the span kept", got)
	}
	// sleep inside the slack keeps the span; sleep just over it restarts it
	ms += 16_000
	c.step(ms, 16_000, 16_000-sleepSlack.Milliseconds(), true)
	if got := c.For(time.UnixMilli(ms)); got < 6*time.Minute {
		t.Fatalf("awake for %v after a sleep inside the slack, want the span kept", got)
	}
	ms += 16_000
	c.step(ms, 16_000, 16_000-sleepSlack.Milliseconds()-1, true)
	if got := c.For(time.UnixMilli(ms)); got != 0 {
		t.Fatalf("awake for %v after a sleep over the slack, want 0", got)
	}
}

// Note's monotonic branch end to end: readings from time.Now move both
// clocks together, so a long gap between them is a slow tick, not sleep.
func TestAwakeClockNoteWithRealReadingsKeepsASlowTick(t *testing.T) {
	var c awakeClock
	n := time.Now()
	c.Note(n)
	c.Note(n.Add(70 * time.Second))
	if got := c.For(n.Add(70 * time.Second)); got != 70*time.Second {
		t.Fatalf("awake for %v after a 70s tick on real readings, want 70s", got)
	}
}

func TestHasMonotonic(t *testing.T) {
	if hasMonotonic(awakeT0) {
		t.Fatal("a hand-built time reports a monotonic reading")
	}
	if !hasMonotonic(time.Now()) {
		t.Fatal("time.Now() reports no monotonic reading")
	}
}

func awakeTestEnv(t *testing.T) (launches *atomic.Int32) {
	t.Helper()
	launches = new(atomic.Int32)
	restore := SetMindLauncherForTest(func(ctx context.Context, argv []string, dir string, env []string, stdin string) (string, string, error) {
		launches.Add(1)
		return contractMRAnswerJSON(t, nil, nil), "", nil
	})
	t.Cleanup(restore)
	prevGo := mindRunGo
	mindRunGo = func(f func()) { f() }
	t.Cleanup(func() { mindRunGo = prevGo })
	return launches
}

// mindRunPass holds a queued run while the awake span is under the floor and
// claims it once the span reaches it.
func TestMindRunPassHoldsUntilAwakeFloor(t *testing.T) {
	st := contractMREnv(t)
	launches := awakeTestEnv(t)
	edgeMRPendingMark(t, st, "a mark, so the daily bundle has something to judge and the launcher is reached")
	d := &daemonState{}
	t0 := dreamNoon()
	d.Awake.Note(t0) // the daemon's first tick after a wake
	d.mindRunPass(st, t0)
	runs, _ := st.ListMindRuns(0, 0)
	if len(runs) != 1 || runs[0].State != "queued" {
		t.Fatalf("after the first tick runs = %+v, want one queued daily run held", runs)
	}
	if !d.MindHeld {
		t.Fatal("MindHeld = false while the run is held")
	}
	if launches.Load() != 0 {
		t.Fatalf("launched %d times under the floor, want 0", launches.Load())
	}
	var now time.Time
	for i := 1; i*15 <= int(mindAwakeFloor.Seconds()); i++ { // ticks up to the floor
		now = t0.Add(time.Duration(i) * 15 * time.Second)
		d.Awake.Note(now)
		d.mindRunPass(st, now)
	}
	runs, _ = st.ListMindRuns(0, 0)
	if len(runs) != 1 || runs[0].State != "done" {
		t.Fatalf("at the floor runs = %+v, want the daily run done", runs)
	}
	if d.MindHeld {
		t.Fatal("MindHeld = true after the gate opened")
	}
	if launches.Load() != 1 {
		t.Fatalf("launched %d times, want 1", launches.Load())
	}
}

// A never-fed clock holds nothing: the pass claims on its first call.
func TestMindRunPassUnfedAwakeClockHoldsNothing(t *testing.T) {
	st := contractMREnv(t)
	launches := awakeTestEnv(t)
	edgeMRPendingMark(t, st, "a mark, so the daily bundle has something to judge and the launcher is reached")
	d := &daemonState{}
	d.mindRunPass(st, dreamNoon())
	runs, _ := st.ListMindRuns(0, 0)
	if len(runs) != 1 || runs[0].State != "done" {
		t.Fatalf("runs = %+v, want the daily run done on an unfed clock", runs)
	}
	if launches.Load() != 1 {
		t.Fatalf("launched %d times, want 1", launches.Load())
	}
}

// A sleep gap inside the awake span restarts the hold.
func TestMindRunPassSleepGapRestartsTheHold(t *testing.T) {
	st := contractMREnv(t)
	launches := awakeTestEnv(t)
	edgeMRPendingMark(t, st, "a mark, so the daily bundle has something to judge and the launcher is reached")
	d := &daemonState{}
	t0 := dreamNoon()
	for i := 0; i*15 < int(mindAwakeFloor.Seconds())-30; i++ { // almost at the floor
		now := t0.Add(time.Duration(i) * 15 * time.Second)
		d.Awake.Note(now)
		d.mindRunPass(st, now)
	}
	t1 := t0.Add(mindAwakeFloor + 20*time.Minute) // asleep for twenty minutes
	d.Awake.Note(t1)
	d.mindRunPass(st, t1)
	if launches.Load() != 0 {
		t.Fatalf("launched %d times on the first tick after a sleep gap, want 0", launches.Load())
	}
	runs, _ := st.ListMindRuns(0, 0)
	if len(runs) != 1 || runs[0].State != "queued" {
		t.Fatalf("runs = %+v, want the daily run still queued", runs)
	}
}

// The child's deadline is measured on the wall clock too: a context whose
// own clock never reaches MindRunTimeout still ends the run when the wall
// clock has moved past it, and the note says which clock fired.
func TestMindRunTimeoutOnTheWallClock(t *testing.T) {
	st := edgeMREnv(t)
	prevTimeout, prevTick, prevNow := MindRunTimeout, mindWallTick, mindWallNow
	MindRunTimeout = time.Hour // the context's clock never gets there
	mindWallTick = 5 * time.Millisecond
	var wall atomic.Int64
	wall.Store(1_000_000)
	mindWallNow = func() int64 { return wall.Load() }
	t.Cleanup(func() { MindRunTimeout, mindWallTick, mindWallNow = prevTimeout, prevTick, prevNow })

	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		// the machine "sleeps": the wall clock jumps past the deadline while
		// the context's own deadline is still an hour away
		wall.Add(MindRunTimeout.Milliseconds() + 1)
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(10 * time.Second):
			return edgeMREmptyAnswer, "", nil
		}
	})
	run := edgeMRDaily(t, st)
	edgeMRPendingMark(t, st, "a mark, so the bundle has something to judge and the launcher is reached")
	start := time.Now()
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun past the wall-clock deadline = nil error")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("RunMindRun took %v, want it cut by the wall clock", el)
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "failed" || !strings.HasPrefix(r.Note, "timed out") || !strings.Contains(r.Note, "wall clock") {
		t.Fatalf("run = state %s note %q, want failed with a note starting \"timed out\" and naming the wall clock", r.State, r.Note)
	}
}

// A run that finishes inside both deadlines is untouched by the wall-clock
// goroutine, and a finished answer is kept even when a deadline passed while
// it was being written.
func TestMindRunInsideBothDeadlinesIsNotCut(t *testing.T) {
	st := edgeMREnv(t)
	prevTick := mindWallTick
	mindWallTick = 5 * time.Millisecond
	t.Cleanup(func() { mindWallTick = prevTick })
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		time.Sleep(30 * time.Millisecond)
		return edgeMREmptyAnswer, "", nil
	})
	run := edgeMRDaily(t, st)
	edgeMRPendingMark(t, st, "a mark, so the bundle has something to judge and the launcher is reached")
	if err := RunMindRun(st, run); err != nil {
		t.Fatalf("RunMindRun inside both deadlines: %v", err)
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "done" {
		t.Fatalf("run = state %s note %q, want done", r.State, r.Note)
	}
}

func TestMindRunFinishedAnswerSurvivesALateDeadline(t *testing.T) {
	st := edgeMREnv(t)
	prevTimeout, prevTick, prevNow := MindRunTimeout, mindWallTick, mindWallNow
	MindRunTimeout = time.Hour
	mindWallTick = 5 * time.Millisecond
	var wall atomic.Int64
	wall.Store(1_000_000)
	mindWallNow = func() int64 { return wall.Load() }
	t.Cleanup(func() { MindRunTimeout, mindWallTick, mindWallNow = prevTimeout, prevTick, prevNow })
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		wall.Add(MindRunTimeout.Milliseconds() + 1) // the deadline passes …
		time.Sleep(30 * time.Millisecond)           // … the goroutine cancels …
		return edgeMREmptyAnswer, "", nil           // … and the child still answered
	})
	run := edgeMRDaily(t, st)
	edgeMRPendingMark(t, st, "a mark, so the bundle has something to judge and the launcher is reached")
	if err := RunMindRun(st, run); err != nil {
		t.Fatalf("RunMindRun with a finished answer after the deadline: %v", err)
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "done" {
		t.Fatalf("run = state %s note %q, want done", r.State, r.Note)
	}
}
