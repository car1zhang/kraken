package jobs

import (
	"errors"
	"fmt"
	"sync"
)

// teardownsInFlight holds the ids of jobs whose teardown THIS process is
// running right now — from just before DoneJob's or KillJob's teardown-intent
// write to the function's return, and around the daemon finisher's own
// resume. The daemon's interrupted-teardown finisher (daemon.go) treats a
// surviving `teardown:` marker on a job whose tmux session is gone as a
// teardown that DIED mid-way and resumes it; but the board's own Done kills
// the session as its first destructive step and clears the marker as its
// last, and a tick landing in between reads exactly that shape. Measured
// over this machine's logs, nearly every firing of the finisher landed
// within seconds of a board Done — the finisher has
// almost never finished a real interrupted teardown, and each firing ran a
// second teardownCell against a tree the first was still removing, plus
// (since the mind run queues on Done) a second mind run over the same
// bundle.
//
// The gate is in-process on purpose: Done, Kill and the tick all run inside
// the one `kraken serve`, so a claim here covers every live teardown, and a
// process that crashes mid-teardown takes the claim with it — which is the
// one case the finisher exists for, and the next serve's tick still resumes
// it. Keyed on the immutable job id like submitLocks; the release is deferred
// so a refused or failed teardown never leaves a stale claim behind.
//
// The claim alone is not enough, because the tick decides on a job row it
// loaded at its HEAD and reaches a given job only after discovery and every
// earlier job's work: a Done that finishes inside that latency clears the
// marker and releases the claim, and the loop then meets a stale snapshot
// that still carries the marker with nothing holding the claim. So the
// finisher claims first and then RE-READS the row: Done clears the marker
// before it releases, so a fresh row that still carries one after a
// successful claim can only belong to a teardown that died or failed — both
// are what the finisher exists to finish. Done and Kill re-read for the
// same reason in the other direction: their caller's row was loaded before
// the claim, and a teardown that claimed and released entirely inside that
// gap has already finished the job.
var teardownsInFlight sync.Map // job id (int64) → struct{}

// claimTeardown takes job id's teardown claim for this process. ok is false
// when another teardown of the same job holds it — a Done racing a Kill, a
// double-fired Done, or the finisher mid-resume — and the returned release is
// then a no-op. Callers defer the release immediately; releasing twice is
// harmless.
func claimTeardown(id int64) (release func(), ok bool) {
	if _, held := teardownsInFlight.LoadOrStore(id, struct{}{}); held {
		return func() {}, false
	}
	return func() { teardownsInFlight.Delete(id) }, true
}

// teardownInFlight reports whether this process holds job id's teardown claim.
func teardownInFlight(id int64) bool {
	_, ok := teardownsInFlight.Load(id)
	return ok
}

// errTeardownInProgress is what Done or Kill answers while the same job's
// teardown is already running in this process.
func errTeardownInProgress(j *Job) error {
	return fmt.Errorf("teardown of %s is already in progress (a Done or Kill is running, or the daemon is finishing an interrupted one) — retry in a moment", j.Slug)
}

// resumeTeardownWanted is the daemon finisher's first look at one job whose
// cell session is gone, on the row snapshot the tick loaded: true only for a
// job carrying a teardown marker that no teardown in this process holds the
// claim for. mirror is the --mirror serve, which never finishes a real
// teardown. It needs no tmux or store, so the snapshot half of the race is
// testable directly; finishInterruptedTeardown is the half that re-reads.
func resumeTeardownWanted(j *Job, mirror bool) bool {
	if j == nil || j.Teardown == "" || mirror {
		return false
	}
	return !teardownInFlight(j.ID)
}

// finishInterruptedTeardown is the finisher proper: claim the job's teardown
// (a held claim means a Done, a Kill or an earlier resume is running — skip
// this tick, the next one reloads), re-read the row through reload, and
// resume only when the FRESH row still carries a marker — a snapshot's marker
// alone is not evidence, since a Done can finish between the tick's load and
// this call. marker is the fresh row's marker, for the caller's log line;
// resumed says whether resume ran and succeeded. A reload failure resumes
// nothing: a destructive step never runs on a row this process could not
// read back. The seams exist so the race is testable without a store or tmux;
// the daemon passes reloadJob and ResumeTeardown.
func finishInterruptedTeardown(snapshot *Job, reload func(id int64) (*Job, error), resume func(*Job) error) (marker string, resumed bool, err error) {
	release, ok := claimTeardown(snapshot.ID)
	if !ok {
		return "", false, nil
	}
	defer release()
	fresh, err := reload(snapshot.ID)
	if err != nil {
		return "", false, fmt.Errorf("reload job %s before resuming its teardown: %w", snapshot.Slug, err)
	}
	if fresh == nil || fresh.Teardown == "" {
		return "", false, nil
	}
	marker = fresh.Teardown
	if err := resume(fresh); err != nil {
		return marker, false, err
	}
	return marker, true, nil
}

// refuseStaleTeardown is Done's and Kill's fresh look, taken right after the
// claim: the caller's row was loaded before it, and a teardown of the same
// job that claimed and released entirely inside that gap — a double-fired
// Done, the finisher resuming a marker — has already finished the job, so a
// Done or Kill whose fresh row reads done, or whose fresh row is no longer
// started, has nothing left to tear down and must not write a marker, run a
// second teardownCell, or (Done) queue a second mind run: a Kill landing
// after a finished done: marker would otherwise send a done job back to
// draft, and a Done landing after a finished Kill would mark a drafted job
// done, which is why both rows refuse both verbs. A reload failure, or no
// row at all, refuses too: nothing destructive runs on a row this process
// could not read back. verb is "done" or "kill" and names the refused caller
// in the error text.
func refuseStaleTeardown(j *Job, verb string, reload func(id int64) (*Job, error)) error {
	fresh, err := reload(j.ID)
	if err != nil {
		return fmt.Errorf("cannot re-read job %s before tearing it down: %w", j.Slug, err)
	}
	switch {
	case fresh == nil:
		return fmt.Errorf("job %s has no row to tear down — refusing %s", j.Slug, verb)
	case fresh.State == "done":
		return fmt.Errorf("job %s is already done — refusing %s: another teardown finished it", j.Slug, verb)
	case !fresh.Started():
		// both verbs: a Kill that landed first returned the job to draft,
		// and a Done let through here would mark that drafted job done —
		// the same precondition the board's Done route already applies
		return fmt.Errorf("job %s is not started — refusing %s: another teardown already returned it to draft", j.Slug, verb)
	}
	return nil
}

// reloadJob reads one job row back from the store by id, the finisher's and
// Done's fresh look. No store wired is an error, never a nil job: a caller
// must not read "no row" as "nothing to resume" on a process that cannot see
// rows. It is not mmJobByID (mm.go), which maps ErrNotFound to a nil job for
// a reader that may legitimately outlive the row; here a row that cannot be
// read back must refuse, not pass.
func reloadJob(id int64) (*Job, error) {
	st := ActiveStore()
	if st == nil {
		return nil, errors.New("no store")
	}
	sr, err := st.GetJobByID(id)
	if err != nil {
		return nil, err
	}
	return loadJobFull(st, sr)
}
