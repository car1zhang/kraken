package jobs

import (
	"errors"
	"testing"
)

// The daemon's interrupted-teardown finisher must not resume a teardown this
// process is still running: the board's Done kills the cell session first
// and clears the marker last, and a tick between the two read that span as
// an interrupted teardown and ran a second teardownCell plus a second mind
// run. Two mechanisms close it, and these pin both: the claim (held, the
// finisher and any second Done stand down; released, the finisher resumes;
// one job's claim never covers another), and the finisher's fresh re-read
// (a snapshot still carrying the marker after the claim was released is not
// resumed unless the row read back still carries it).

func TestResumeTeardownStandsDownWhileThisProcessTearsDown(t *testing.T) {
	j := &Job{ID: 4101, Slug: "race", Teardown: "done:accepted"}
	if !resumeTeardownWanted(j, false) {
		t.Fatal("a marked job with no teardown in flight must be resumed")
	}
	release, ok := claimTeardown(j.ID)
	if !ok {
		t.Fatal("the first claim must succeed")
	}
	if resumeTeardownWanted(j, false) {
		t.Fatal("the finisher resumed a teardown this process is still running")
	}
	release()
	if !resumeTeardownWanted(j, false) {
		t.Fatal("after the release the finisher must resume the marker again")
	}
}

func TestClaimTeardownRefusesASecondClaimUntilReleased(t *testing.T) {
	release, ok := claimTeardown(4102)
	if !ok {
		t.Fatal("first claim refused")
	}
	if _, ok := claimTeardown(4102); ok {
		t.Fatal("a second Done or Kill of a job under teardown must be refused, not run alongside")
	}
	release()
	release() // idempotent
	if teardownInFlight(4102) {
		t.Fatal("a released claim must not linger")
	}
	again, ok := claimTeardown(4102)
	if !ok {
		t.Fatal("after the release the job must be claimable again")
	}
	again()
}

func TestResumeTeardownClaimIsPerJob(t *testing.T) {
	a := &Job{ID: 4103, Slug: "a", Teardown: "kill"}
	b := &Job{ID: 4104, Slug: "b", Teardown: "kill"}
	release, _ := claimTeardown(a.ID)
	defer release()
	if resumeTeardownWanted(a, false) {
		t.Fatal("job a is in flight and must not be resumed")
	}
	if !resumeTeardownWanted(b, false) {
		t.Fatal("job b carries no claim and must be resumed")
	}
}

func TestResumeTeardownRefusesNoMarkerAndMirror(t *testing.T) {
	if resumeTeardownWanted(&Job{ID: 4105, Slug: "clean"}, false) {
		t.Fatal("a job with no teardown marker has nothing to resume")
	}
	if resumeTeardownWanted(&Job{ID: 4106, Slug: "m", Teardown: "done"}, true) {
		t.Fatal("a --mirror serve never finishes a teardown")
	}
	if resumeTeardownWanted(nil, false) {
		t.Fatal("nil job")
	}
}

// finishTeardownSeams builds the finisher's two seams for one test: reload
// answers with fresh (or err), resume records what it was handed.
type finishTeardownSeams struct {
	fresh     *Job
	reloadErr error
	resumeErr error
	reloads   int
	resumed   []*Job
}

func (s *finishTeardownSeams) reload(int64) (*Job, error) {
	s.reloads++
	return s.fresh, s.reloadErr
}

func (s *finishTeardownSeams) resume(j *Job) error {
	s.resumed = append(s.resumed, j)
	return s.resumeErr
}

// The race the claim alone misses: the tick snapshots a marked row, Done
// finishes (clears the marker, releases the claim) before the loop reaches
// the job, and the stale snapshot still says "marked" with nothing holding
// the claim. The fresh read is what stands the finisher down.
func TestFinishInterruptedTeardownIgnoresAStaleSnapshot(t *testing.T) {
	snapshot := &Job{ID: 4107, Slug: "stale", Teardown: "done:accepted"}
	release, _ := claimTeardown(snapshot.ID)
	release() // Done ran to completion between the tick's load and now
	s := &finishTeardownSeams{fresh: &Job{ID: 4107, Slug: "stale", State: "done"}}
	marker, resumed, err := finishInterruptedTeardown(snapshot, s.reload, s.resume)
	if err != nil || resumed || marker != "" {
		t.Fatalf("stale snapshot resumed: marker=%q resumed=%v err=%v", marker, resumed, err)
	}
	if s.reloads != 1 || len(s.resumed) != 0 {
		t.Fatalf("reloads=%d resumed=%d, want one reload and no resume", s.reloads, len(s.resumed))
	}
	if teardownInFlight(snapshot.ID) {
		t.Fatal("the finisher must release its claim when it resumes nothing")
	}
}

func TestFinishInterruptedTeardownResumesAFreshlyMarkedRow(t *testing.T) {
	snapshot := &Job{ID: 4108, Slug: "dead", Teardown: "done:accepted"}
	fresh := &Job{ID: 4108, Slug: "dead", Teardown: "done:accepted"}
	s := &finishTeardownSeams{fresh: fresh}
	marker, resumed, err := finishInterruptedTeardown(snapshot, s.reload, s.resume)
	if err != nil || !resumed || marker != "done:accepted" {
		t.Fatalf("marker=%q resumed=%v err=%v, want the fresh marker resumed", marker, resumed, err)
	}
	if len(s.resumed) != 1 || s.resumed[0] != fresh {
		t.Fatal("the resume must run on the FRESH row, never the snapshot")
	}
	if teardownInFlight(snapshot.ID) {
		t.Fatal("the claim must be released after the resume")
	}
}

func TestFinishInterruptedTeardownSkipsWhileTheClaimIsHeld(t *testing.T) {
	snapshot := &Job{ID: 4109, Slug: "busy", Teardown: "kill"}
	release, _ := claimTeardown(snapshot.ID) // a Done or Kill mid-flight
	defer release()
	s := &finishTeardownSeams{fresh: &Job{ID: 4109, Slug: "busy", Teardown: "kill"}}
	marker, resumed, err := finishInterruptedTeardown(snapshot, s.reload, s.resume)
	if err != nil || resumed || marker != "" || s.reloads != 0 || len(s.resumed) != 0 {
		t.Fatalf("held claim: marker=%q resumed=%v err=%v reloads=%d resumes=%d", marker, resumed, err, s.reloads, len(s.resumed))
	}
	if !teardownInFlight(snapshot.ID) {
		t.Fatal("the finisher must not release a claim it did not take")
	}
}

func TestFinishInterruptedTeardownFailsClosedOnReloadError(t *testing.T) {
	snapshot := &Job{ID: 4110, Slug: "unread", Teardown: "done"}
	s := &finishTeardownSeams{reloadErr: errors.New("db locked")}
	_, resumed, err := finishInterruptedTeardown(snapshot, s.reload, s.resume)
	if err == nil || resumed || len(s.resumed) != 0 {
		t.Fatalf("a row this process could not read back must not be torn down: resumed=%v err=%v", resumed, err)
	}
	if teardownInFlight(snapshot.ID) {
		t.Fatal("the claim must be released after a reload failure")
	}
}

// The other direction: Done's and Kill's caller loaded the row before the
// claim, so a teardown that claimed and released inside that gap has already
// finished the job, and the fresh read after the claim is what refuses the
// late one — a done row refuses a Done, an unstarted row refuses a Kill, a
// row this process cannot read back refuses both.
func TestRefuseStaleTeardown(t *testing.T) {
	j := &Job{ID: 4112, Slug: "late", State: "working"}
	fresh := func(state string) func(int64) (*Job, error) {
		return func(int64) (*Job, error) { return &Job{ID: 4112, Slug: "late", State: state}, nil }
	}
	if err := refuseStaleTeardown(j, "done", fresh("working")); err != nil {
		t.Fatalf("a working row must let Done through: %v", err)
	}
	if err := refuseStaleTeardown(j, "done", fresh("done")); err == nil {
		t.Fatal("a Done whose fresh row is already done must be refused")
	}
	if err := refuseStaleTeardown(j, "kill", fresh("blocked")); err != nil {
		t.Fatalf("a started row must let Kill through: %v", err)
	}
	if err := refuseStaleTeardown(j, "kill", fresh("")); err == nil {
		t.Fatal("a Kill whose fresh row is back at draft must be refused")
	}
	if err := refuseStaleTeardown(j, "kill", fresh("done")); err == nil {
		t.Fatal("a Kill landing after a finished done: marker would send a done job back to draft; it must be refused")
	}
	if err := refuseStaleTeardown(j, "done", fresh("")); err == nil {
		t.Fatal("a Done landing after a finished Kill would mark a drafted job done; it must be refused")
	}
	broken := func(int64) (*Job, error) { return nil, errors.New("db locked") }
	if err := refuseStaleTeardown(j, "done", broken); err == nil {
		t.Fatal("a row this process cannot read back must refuse the teardown")
	}
	gone := func(int64) (*Job, error) { return nil, nil }
	if err := refuseStaleTeardown(j, "kill", gone); err == nil {
		t.Fatal("no row, nothing to tear down")
	}
}

func TestFinishInterruptedTeardownReportsAResumeFailureAndReleases(t *testing.T) {
	snapshot := &Job{ID: 4111, Slug: "stuck", Teardown: "done:accepted"}
	s := &finishTeardownSeams{fresh: &Job{ID: 4111, Slug: "stuck", Teardown: "done:accepted"}, resumeErr: errors.New("git worktree remove: boom")}
	marker, resumed, err := finishInterruptedTeardown(snapshot, s.reload, s.resume)
	if err == nil || resumed || marker != "done:accepted" {
		t.Fatalf("marker=%q resumed=%v err=%v", marker, resumed, err)
	}
	if teardownInFlight(snapshot.ID) {
		t.Fatal("the claim must be released after a failed resume so the next tick can retry")
	}
}
