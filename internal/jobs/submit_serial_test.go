package jobs

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// submit_serial_test.go — regression pins for the double-submit race. Two
// near-simultaneous POSTs of one job (the editor's ⌘⏎ used to fire two nested
// keydown handlers) both passed SubmitJob's check-then-act validation and
// raced inside StartCell's `git worktree add`; the loser answered first with
// a git-flavoured 409 that painted a false "start refused" over a cell that
// was in fact coming up. SubmitJob now serializes per job id, so the loser
// waits and then gets StartCell's honest already-live refusal.
//
// Identifiers are prefixed `serial` — this package's test binary is shared
// with the blind-authored contract/edge suites.

// serialStubStartCell swaps the start seam for one that reports every entry
// on `entered` and holds inside the seam until `release` closes. The entry
// channel is what makes overlap OBSERVABLE: under the old lock-free code a
// second concurrent SubmitJob reaches the seam while the first is still
// parked in it, so a second value arrives on `entered`; under the fix it
// cannot arrive until the first call has returned.
func serialStubStartCell(t *testing.T, entered chan<- string, release <-chan struct{}) {
	t.Helper()
	prev := submitStartCell
	submitStartCell = func(j *Job, repo string) error {
		slug := ""
		if j != nil {
			slug = j.Slug
		}
		entered <- slug
		<-release
		return nil
	}
	t.Cleanup(func() { submitStartCell = prev })
}

// serialFakeRepo mirrors the blind suites' fake-checkout helper: a `.git`
// entry under the sandboxed $HOME is all ResolveRepo needs.
func serialFakeRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(util.Home(), "Developer", name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

// serialLoadJob re-reads a slug into its own *Job, the shape production has:
// each HTTP request parses its own copy, so the two racing submits never
// share a pointer and a state write by one is invisible to the other.
func serialLoadJob(t *testing.T, slug string) *Job {
	t.Helper()
	j := JobBySlug(LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not found", slug)
	}
	return j
}

// TestSubmitSerialSameJobNeverOverlapsInTheSeam: two concurrent SubmitJob
// calls on ONE job enter the start seam strictly one after the other. The
// first caller is parked inside the seam; if the second can enter while it is
// parked, `entered` yields a second value and the race the lock exists to
// close is back.
func TestSubmitSerialSameJobNeverOverlapsInTheSeam(t *testing.T) {
	st := useTestStore(t)
	serialFakeRepo(t, "serial-repo")
	// Buffered so a seam entry can never block a goroutine on send, and
	// released via sync.Once from t.Cleanup as well as the happy path: on a
	// Fatalf exit an unclosed `release` would park both submit goroutines
	// forever with the job's mutex HELD, and submitLocks is package-global
	// while useTestStore's fresh DB restarts ids at 1 — so a later test
	// submitting id 1 would hang to the package timeout instead of showing
	// this test's real assertion failure.
	entered := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)
	serialStubStartCell(t, entered, release)

	mustCreateJob(t, st, store.Job{
		Slug: "serial-same", Repo: "serial-repo", Body: "spec\n",
	})

	done := make(chan error, 2)
	// Loads happen HERE, on the test goroutine: serialLoadJob ends in Fatalf,
	// which from a spawned goroutine only Goexits that goroutine and turns a
	// "job not found" into a misleading timeout message.
	for i := 0; i < 2; i++ {
		j := serialLoadJob(t, "serial-same")
		go func() { done <- SubmitJob(j) }()
	}

	// The first submit is now parked inside the seam.
	<-entered

	// The second submit must NOT reach the seam while the first is parked.
	// A short listening window is the unavoidable cost of asserting a
	// negative; under the unfixed code the second entry arrives within
	// microseconds, so 100ms is generous.
	select {
	case slug := <-entered:
		t.Fatalf("second SubmitJob entered the start seam for %q while the first was still inside it — submits are not serialized", slug)
	case <-time.After(100 * time.Millisecond):
	}

	releaseAll()
	// Both calls finish; the second enters the seam only after the first
	// returned (its entry drains here, after release).
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("second SubmitJob never reached the seam after the first released")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("a SubmitJob call never returned")
		}
	}
}

// TestSubmitSerialDifferentJobsDoNotShareTheLock: the lock is per job id, so
// one job's multi-second StartCell must not delay a different job's submit.
// Job A is parked inside the seam; job B's whole submit completes while A is
// still parked.
func TestSubmitSerialDifferentJobsDoNotShareTheLock(t *testing.T) {
	st := useTestStore(t)
	serialFakeRepo(t, "serial-repo-b")
	// Same buffered-channel and Cleanup-release discipline as the test above,
	// and for the same reason: a Fatalf exit must not leave a goroutine
	// parked in the seam holding a mutex a later test's job id collides with.
	entered := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseAll)
	serialStubStartCell(t, entered, release)

	for _, slug := range []string{"serial-a", "serial-b"} {
		mustCreateJob(t, st, store.Job{Slug: slug, Repo: "serial-repo-b", Body: "spec\n"})
	}

	// Loads on the test goroutine — serialLoadJob can Fatalf, which is only
	// sound from the goroutine running the test.
	jA := serialLoadJob(t, "serial-a")
	jB := serialLoadJob(t, "serial-b")

	aDone := make(chan error, 1)
	go func() { aDone <- SubmitJob(jA) }()
	if got := <-entered; got != "serial-a" {
		t.Fatalf("first seam entry was %q, want serial-a", got)
	}

	// A is parked inside the seam. B must get all the way in regardless.
	bDone := make(chan error, 1)
	go func() { bDone <- SubmitJob(jB) }()
	select {
	case got := <-entered:
		if got != "serial-b" {
			t.Fatalf("second seam entry was %q, want serial-b", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job B's submit never reached the seam while job A was parked — the submit lock is not per-job")
	}

	releaseAll()
	for _, ch := range []chan error{aDone, bDone} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("SubmitJob = %v, want nil", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a SubmitJob call never returned")
		}
	}
}

// TestSubmitSerialRefusalPathReleasesTheLock guards the boring but
// load-bearing property that the mutex is released on every exit path — a
// refusal (here: empty spec) must unlock too, or the job's next submit hangs
// forever. sync.Mutex has no owner check, so a leaked lock would deadlock the
// NEXT request rather than panic.
func TestSubmitSerialRefusalPathReleasesTheLock(t *testing.T) {
	st := useTestStore(t)
	serialFakeRepo(t, "serial-repo-c")

	mustCreateJob(t, st, store.Job{Slug: "serial-empty", Repo: "serial-repo-c", Body: ""})
	j := serialLoadJob(t, "serial-empty")
	if err := SubmitJob(j); err == nil {
		t.Fatal("SubmitJob on an empty spec = nil, want a refusal")
	}

	done := make(chan struct{})
	go func() {
		_ = SubmitJob(j)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second sequential SubmitJob hung — the refusal path leaked the lock")
	}
}
