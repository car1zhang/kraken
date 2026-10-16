package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// --- the nightly corroboration pass (kickDream) ---

// resetDreamGuards clears the in-memory date guard before and after a test.
// It is process-global (that is the point of it), so a test that left today's
// date in it would silently suppress every later test's first pass.
func resetDreamGuards(t *testing.T) {
	t.Helper()
	lastDreamDate.Store("")
	t.Cleanup(func() { lastDreamDate.Store("") })
	// kickDream skips the night when no kraken checkout resolves; give the
	// cadence tests one (a bare .git dir is all isGitRepo checks).
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
}

// dreamStub replaces the dreamPass seam — nothing here sweeps, mines, or
// creates a job unless the test asked it to. It records every call, signals
// each one on fired (buffered, so the stub never blocks a test that is not
// listening yet), and returns whatever outcome the test wants the cadence to
// react to.
type dreamStub struct {
	calls   atomic.Int32
	fired   chan struct{}
	block   chan struct{} // when non-nil, the stub waits on it before returning
	gotPrev atomic.Value  // DreamRun: the watermark the cadence handed the pass
	next    DreamRun
	ok      bool
}

func stubDreamPass(t *testing.T, next DreamRun, ok bool) *dreamStub {
	t.Helper()
	resetDreamGuards(t)
	s := &dreamStub{fired: make(chan struct{}, 8), next: next, ok: ok}
	prevSeam := dreamPass
	dreamPass = func(now time.Time, prev DreamRun) (*Job, DreamRun, bool) {
		s.calls.Add(1)
		s.gotPrev.Store(prev)
		s.fired <- struct{}{}
		if s.block != nil {
			<-s.block
		}
		return nil, s.next, s.ok
	}
	t.Cleanup(func() { dreamPass = prevSeam })
	return s
}

func (s *dreamStub) waitFired(t *testing.T) {
	t.Helper()
	select {
	case <-s.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("dream pass never fired")
	}
}

// seedDreamRun writes the watermark directly, the way a previous night's pass
// would have left it.
func seedDreamRun(t *testing.T, r DreamRun) {
	t.Helper()
	if err := writeDreamRun(r); err != nil {
		t.Fatal(err)
	}
}

func today(now time.Time) string { return now.Format(dreamDateLayout) }

// dreamNoon anchors the cadence tests at midday local. They reason in offsets
// of a few hours, and a suite run at 21:00 would otherwise have "+6h" cross
// midnight and legitimately fire a second night — a flake keyed to the wall
// clock of whoever ran the tests.
func dreamNoon() time.Time {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 12, 0, 0, 0, time.Local)
}

// A store with no watermark row at all reads as "never run", which is
// genuinely due — the first night after kraken learns to dream must fire.
func TestKickDreamFiresWhenNeverRun(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	s := stubDreamPass(t, DreamRun{Date: today(now), SweptTo: now.UnixMilli()}, true)

	kickDream(now)
	s.waitFired(t)

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times with no watermark, want 1", n)
	}
	if got := ReadDreamRun(); got.Date != today(now) {
		t.Fatalf("watermark = %+v, want date %s", got, today(now))
	}
}

// Same local date as the watermark: nothing fires. This is the gate that
// makes "at most once per night" true, and it is a DATE comparison rather
// than an elapsed duration on purpose.
func TestKickDreamSkipsSameLocalDate(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	s := stubDreamPass(t, DreamRun{}, true)
	seedDreamRun(t, DreamRun{Date: today(now), SweptTo: now.Add(-time.Hour).UnixMilli(), Job: "dream-" + today(now)})

	kickDream(now)
	kickDream(now.Add(3 * time.Hour))
	kickDream(now.Add(6 * time.Hour))

	if n := s.calls.Load(); n != 0 {
		t.Fatalf("pass fired %d times on a date already swept, want 0", n)
	}
}

// The next local date fires — including the case the wall-clock rule exists
// for: a Mac asleep across midnight, waking hours later, gets its pass on the
// first tick after it wakes rather than skipping the night.
func TestKickDreamFiresOnNextLocalDate(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	yesterday := now.AddDate(0, 0, -1)
	s := stubDreamPass(t, DreamRun{Date: today(now), SweptTo: now.UnixMilli()}, true)
	seedDreamRun(t, DreamRun{Date: today(yesterday), SweptTo: yesterday.UnixMilli(), Job: "dream-" + today(yesterday)})

	kickDream(now)
	s.waitFired(t)

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times on a new local date, want 1", n)
	}
}

// The inversion this feature's guard exists for. kickUpdateAvail degrades an
// unrecorded watermark to "always due" and the cost is an extra git fetch;
// here "always due" would create a job row and spawn a cell every 15s tick.
// A pass whose watermark never lands must therefore be stopped by the
// in-memory guard alone — exactly once, not once per tick.
func TestKickDreamBackstopsAStoreThatNeverRecords(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	// A pass that succeeds but records nothing: the stored watermark stays at
	// its zero value, which reads as "never run" and is always due.
	s := stubDreamPass(t, DreamRun{}, true)

	kickDream(now)
	s.waitFired(t)
	kickDream(now.Add(time.Minute))
	kickDream(now.Add(2 * time.Minute))
	kickDream(now.Add(time.Hour))

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times with an unrecorded watermark, want 1 — the in-memory guard is the only gate left", n)
	}
}

// A watermark that cannot be READ at all (no store wired, a failed query)
// degrades to "already ran today", the opposite of the availability check's
// degradation — see readDreamRun.
func TestKickDreamSkipsWhenWatermarkUnreadable(t *testing.T) {
	resetDreamGuards(t)
	// Unwiring the store leaves the package globals (store handle, event
	// sink, in-process daemon status) blank for whatever runs next, so it
	// has to be undone. The suite is green today only because of test
	// ordering, which is not a guarantee.
	t.Cleanup(func() { UseStore(nil) })
	UseStore(nil) // no store wired: readDreamRun cannot answer
	s := stubDreamPass(t, DreamRun{}, true)

	kickDream(time.Now())

	if n := s.calls.Load(); n != 0 {
		t.Fatalf("pass fired %d times with an unreadable watermark, want 0", n)
	}
}

// Two due calls with the first still in flight must produce ONE pass: neither
// watermark has moved yet, so the date gates let the second one through and
// the CAS is what has to stop it.
func TestKickDreamSingleFlightsConcurrentCalls(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	s := stubDreamPass(t, DreamRun{Date: today(now)}, true)
	s.block = make(chan struct{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		kickDream(now)
	}()
	s.waitFired(t) // the first pass is now inside the stub, holding the flag
	kickDream(now)
	close(s.block)
	<-done

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times with one already in flight, want exactly 1", n)
	}
}

// A failed pass advances NEITHER watermark, so the next tick retries rather
// than losing the night.
func TestKickDreamFailedPassLeavesWatermarkUnadvanced(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	s := stubDreamPass(t, DreamRun{Date: today(now), SweptTo: now.UnixMilli()}, false)

	kickDream(now)
	s.waitFired(t)

	if got := ReadDreamRun(); got.Date != "" || got.SweptTo != 0 {
		t.Fatalf("watermark = %+v after a failed pass, want the zero value", got)
	}
	kickDream(now.Add(time.Minute))
	s.waitFired(t)
	if n := s.calls.Load(); n != 2 {
		t.Fatalf("pass fired %d times, want 2 — a failed pass must be retried on the next tick", n)
	}
}

// The pass must be HANDED the watermark the cadence already read, never
// re-read it: a second read that failed would degrade SweptTo to 0, collapse
// the window to the last 24h, and then mark the wider window swept.
func TestKickDreamHandsThePassTheWatermarkItRead(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	// the shape a slug collision or a failed create leaves behind: a floor
	// deliberately older than 24h, because that window was never swept
	floor := now.AddDate(0, 0, -3).UnixMilli()
	s := stubDreamPass(t, DreamRun{Date: today(now), SweptTo: now.UnixMilli()}, true)
	seedDreamRun(t, DreamRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: floor})

	kickDream(now)
	s.waitFired(t)

	got, _ := s.gotPrev.Load().(DreamRun)
	if got.SweptTo != floor {
		t.Fatalf("pass got prev.SweptTo %d, want the stored floor %d — a re-read would have collapsed the window to 24h", got.SweptTo, floor)
	}
}

// --- the tick's own wiring ---

func TestTickRunsDreamPass(t *testing.T) {
	useTestStore(t)
	stubNoUsagePass(t)
	stubAvailCheck(t, UpdateAvail{})
	now := dreamNoon()
	s := stubDreamPass(t, DreamRun{Date: today(now)}, true)

	d := newDaemon()
	if _, err := d.tick(false); err != nil {
		t.Fatalf("tick: %v", err)
	}
	s.waitFired(t)
	waitAvailIdle(t)
}

// A mirror tick must never run it: creating a real job row is exactly the
// class of side effect the read-only scheduler variant exists to avoid.
func TestTickMirrorSkipsDreamPass(t *testing.T) {
	useTestStore(t)
	s := stubDreamPass(t, DreamRun{}, true)

	d := newDaemon()
	if _, err := d.tick(true); err != nil {
		t.Fatalf("mirror tick: %v", err)
	}

	if n := s.calls.Load(); n != 0 {
		t.Fatalf("dream pass fired %d times under --mirror, want 0", n)
	}
}

// --- the pass itself (runDreamPass) ---

// The common case, and it must be genuinely free: nothing happened in the
// window, so no job, no cell, no notification — just an advanced watermark
// and one dream-skip event.
func TestDreamPassSkipsAnEmptyNight(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()

	created := kickDream(now)

	if created != nil {
		t.Fatalf("created job %s on an empty night, want none", created.Slug)
	}
	if jobs := LoadJobs(); len(jobs) != 0 {
		t.Fatalf("board holds %d jobs after an empty night, want 0", len(jobs))
	}
	got := ReadDreamRun()
	if got.Date != today(now) {
		t.Fatalf("watermark = %+v, want date %s advanced even on a skip", got, today(now))
	}
	if got.SweptTo == 0 || got.Job != "" {
		t.Fatalf("watermark = %+v, want a swept_to floor and no job slug", got)
	}
	if !hasEvent(t, st, "dream-skip") {
		t.Fatal("want a dream-skip event, got none")
	}
}

// A transcript scan that could not answer is NOT a quiet night. Advancing
// SweptTo past a window nobody read loses that window's evidence permanently
// — the floor never moves backwards — so an unreadable scan with no feedback
// to fall back on must pin the watermark for a retry instead.
func TestDreamPassPinsWatermarkWhenTranscriptScanFails(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	blindProjectsDir(t)

	created := kickDream(now)

	if created != nil {
		t.Fatalf("created %s off an unreadable scan, want none", created.Slug)
	}
	if got := ReadDreamRun(); got.Date != "" || got.SweptTo != 0 {
		t.Fatalf("watermark = %+v, want it pinned — the window was never read", got)
	}
	if hasEvent(t, st, "dream-skip") {
		t.Fatal("logged dream-skip asserting 0 sessions, but the scan never answered")
	}
	// and the next tick retries rather than losing the night
	if created := kickDream(now.Add(time.Minute)); created != nil {
		t.Fatalf("unexpected job %s", created.Slug)
	}
	if got := ReadDreamRun(); got.Date != "" {
		t.Fatalf("watermark = %+v after a retry, want it still pinned", got)
	}
}

// Feedback alone still carries the night: an unreadable transcript scan is a
// blind spot, not a veto, once there is positive evidence in hand.
func TestDreamPassRunsOnFeedbackDespiteBlindScan(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	blindProjectsDir(t)
	if _, err := st.AddFeedback(store.Feedback{JobSlug: "x", Note: "n", TS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	created := kickDream(now)

	if created == nil || created.Slug != "dream-"+today(now) {
		t.Fatalf("created = %+v, want tonight's job", created)
	}
}

// One feedback row in the window is enough evidence to be worth a night.
func TestDreamPassCreatesJobFromFeedback(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	job := mustCreateJob(t, st, store.Job{Slug: "some-job", Repo: "kraken", Body: "x"})
	if _, err := st.AddFeedback(store.Feedback{
		JobID: job.ID, JobSlug: job.Slug, Artifact: "00-overview.md",
		Note: "this diagram is wrong", TS: now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	created := kickDream(now)

	slug := "dream-" + today(now)
	if created == nil || created.Slug != slug {
		t.Fatalf("created = %+v, want a job named %s", created, slug)
	}
	if created.State != "" {
		t.Fatalf("a freshly created dream job must be a draft, got state %q — the tick submits it, and SubmitJob refuses an already-started job", created.State)
	}
	if strings.TrimSpace(created.Body) == "" {
		t.Fatal("dream job body is empty — SubmitJob would refuse it as an empty spec and the night would be lost")
	}
	if created.Repo != "kraken" {
		t.Fatalf("repo = %q, want kraken", created.Repo)
	}
	// exactly one, and it is on the board
	var found int
	for _, j := range LoadJobs() {
		if j.Slug == slug {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("board holds %d jobs named %s, want exactly 1", found, slug)
	}
	// the tick hands this very value straight to SubmitJob (startCreatedJob),
	// so every refusal SubmitJob makes has to be false of it. Checking the
	// refusals rather than calling SubmitJob keeps a real StartCell — git
	// worktree, tmux, rotation gate — out of this test.
	if created.Err != "" {
		t.Fatalf("created job carries a parse error %q — SubmitJob refuses it as unparseable", created.Err)
	}
	if created.Started() {
		t.Fatalf("created job reads as started (state %q) — SubmitJob refuses it", created.State)
	}
	got := ReadDreamRun()
	if got.Job != slug || got.SweptTo == 0 {
		t.Fatalf("watermark = %+v, want the created slug and an advanced floor", got)
	}
	if !hasEvent(t, st, "dream-job-created") {
		t.Fatal("want a dream-job-created event, got none")
	}
}

// A hand-made job of the same name is the only way the date-derived slug can
// collide. Log and skip — never retry, never mutate the slug — and leave
// SweptTo where it is, since no sweep happened.
func TestDreamPassSkipsWhenSlugTaken(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	slug := "dream-" + today(now)
	mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", Body: "hand-made"})
	if _, err := st.AddFeedback(store.Feedback{JobSlug: "x", Note: "n", TS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	floor := now.Add(-2 * time.Hour).UnixMilli()
	seedDreamRun(t, DreamRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: floor})

	created := kickDream(now)

	if created != nil {
		t.Fatalf("created %s over a taken slug, want none", created.Slug)
	}
	got := ReadDreamRun()
	if got.Date != today(now) {
		t.Fatalf("watermark date = %q, want today (the night is decided, just skipped)", got.Date)
	}
	if got.SweptTo != floor {
		t.Fatalf("swept_to = %d, want the untouched floor %d — no sweep happened", got.SweptTo, floor)
	}
}

// --- the generated job text ---

// Whatever dreamJobContent renders has to survive ParseJob with no Err, or
// the board renders the night's job with an error badge and it never starts.
func TestDreamJobContentParsesClean(t *testing.T) {
	now := dreamNoon()
	slug := "dream-" + today(now)
	content := dreamJobContent(slug, now.Add(-24*time.Hour).UnixMilli(), now.UnixMilli(), false)
	j := ParseJob(slug+JobSuffix, []byte(content))

	if j.Err != "" {
		t.Fatalf("dream job content does not parse: %s\n%s", j.Err, content)
	}
	if j.Slug != slug || j.Repo != "kraken" || j.Solo {
		t.Fatalf("parsed = slug %q repo %q solo %v, want %s/kraken/false", j.Slug, j.Repo, j.Solo, slug)
	}
	// the two retired keys must not come back: the tick submits this job
	// itself, and a ready: line in generated text is the kind of thing a
	// later edit copies forward from an old sample.
	for _, gone := range []string{"ready:", "prereqs:"} {
		if strings.Contains(content, gone) {
			t.Errorf("generated dream job text still carries %q:\n%s", gone, content)
		}
	}
	if strings.TrimSpace(j.Body) == "" {
		t.Fatal("body is empty")
	}
	// the two things the skill cannot know: where to find itself, and the window
	if !strings.Contains(j.Body, ".claude/skills/dream/SKILL.md") {
		t.Fatal("body must point zeus at the repo-owned skill by path")
	}
	if !strings.Contains(j.Body, "kraken feedback sweep --since") {
		t.Fatal("body must state the sweep window's floor")
	}
}

// --- the session side of the gate ---

// anyTranscriptSince is the cheap "was there a session?" check: it stats
// directory entries and never opens a file. HOME is sandboxed by
// useTestStore, so the fixtures below are the whole world.
func TestAnyTranscriptSince(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	// no projects dir at all: claude has never run, which is a REAL "no
	// sessions" (ok=true) rather than an unanswered question — anything else
	// would pin the watermark forever on such a machine.
	if found, ok := anyTranscriptSince(now.Add(-time.Hour).UnixMilli()); found || !ok {
		t.Fatalf("missing projects dir = (%v, %v), want (false, true)", found, ok)
	}
	writeTranscriptFixture(t, "-Users-carl-Developer-kraken", "a.jsonl", now.Add(-30*time.Minute))
	if found, ok := anyTranscriptSince(now.Add(-time.Hour).UnixMilli()); !found || !ok {
		t.Fatalf("fresh transcript = (%v, %v), want (true, true)", found, ok)
	}
	if found, ok := anyTranscriptSince(now.Add(-time.Minute).UnixMilli()); found || !ok {
		t.Fatalf("transcript older than the floor = (%v, %v), want (false, true)", found, ok)
	}
}

// An unreadable projects dir must report ok=false — "I could not answer" —
// never the (false, true) that would let the caller advance the sweep floor
// past a window nobody read.
func TestAnyTranscriptSinceReportsAnUnreadableScan(t *testing.T) {
	useTestStore(t)
	blindProjectsDir(t)

	found, ok := anyTranscriptSince(dreamNoon().Add(-time.Hour).UnixMilli())

	if found || ok {
		t.Fatalf("unreadable projects dir = (%v, %v), want (false, false)", found, ok)
	}
}

// A positive is self-evidencing: one readable project dir holding a fresh
// transcript answers the question regardless of an unreadable sibling.
func TestAnyTranscriptSinceTrustsAPositiveOverABlindSibling(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	writeTranscriptFixture(t, "-Users-carl-Developer-kraken", "a.jsonl", now.Add(-30*time.Minute))
	blind := filepath.Join(util.Home(), ".claude", "projects", "zz-unreadable")
	if err := os.MkdirAll(blind, 0o755); err != nil {
		t.Fatal(err)
	}
	chmodForTest(t, blind, 0o000)

	found, ok := anyTranscriptSince(now.Add(-time.Hour).UnixMilli())

	if !found || !ok {
		t.Fatalf("= (%v, %v), want (true, true) — a positive stands on its own", found, ok)
	}
}

// --- helpers ---

func hasEvent(t *testing.T, st *store.Store, id string) bool {
	t.Helper()
	events, err := st.ListEvents(200, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Event == id {
			return true
		}
	}
	return false
}

// blindProjectsDir makes ~/.claude/projects exist but be unreadable — the
// permissions blip / snapshot race the (found, ok) split exists for. It has to
// EXIST: a missing dir is a legitimate "no sessions", not a failure.
func blindProjectsDir(t *testing.T) {
	t.Helper()
	dir := filepath.Join(util.Home(), ".claude", "projects")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	chmodForTest(t, dir, 0o000)
}

// chmodForTest restores the mode on cleanup — t.TempDir's own RemoveAll needs
// to be able to walk back in, and a 0000 dir left behind fails the test run
// with a cleanup error rather than an assertion.
func chmodForTest(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny access")
	}
	if err := os.Chmod(dir, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

func writeTranscriptFixture(t *testing.T, project, name string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(util.Home(), ".claude", "projects", project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// --- watermark corruption and window clamping (review follow-ups) ---

// A present-but-unparseable watermark must not kill the loop. The only writer
// of that key sits downstream of the gate, so treating garbage as "unreadable"
// would wedge the pass forever with nothing logged — see readDreamRun.
func TestReadDreamRunResetsUnparseableWatermark(t *testing.T) {
	st := useTestStore(t)
	for _, raw := range []string{"", "not json", "{", `{"date":`} {
		if err := st.SetKV(dreamRunKVKey, raw); err != nil {
			t.Fatal(err)
		}
		run, ok := readDreamRun()
		if !ok {
			t.Fatalf("readDreamRun(%q) reported unreadable — a garbage row must reset, not wedge the loop", raw)
		}
		if run != (DreamRun{}) {
			t.Fatalf("readDreamRun(%q) = %+v, want the zero value (a reset)", raw, run)
		}
	}
}

// The corrupt row having reset, the very next kick must actually fire —
// otherwise "self-repairing" is a comment rather than a behaviour.
func TestKickDreamFiresAfterAnUnparseableWatermark(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	if err := st.SetKV(dreamRunKVKey, "not json"); err != nil {
		t.Fatal(err)
	}
	s := stubDreamPass(t, DreamRun{Date: today(dreamNoon())}, true)

	kickDream(dreamNoon())

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times after a corrupt watermark, want 1 — the reset must be followed by a real pass", n)
	}
}

// A stale floor is clamped to dreamMaxWindow, and the clamp is DISCLOSED in
// the job body. A truncated sweep that reads as a complete one is the failure
// this feature can least afford.
func TestDreamPassClampsAndDisclosesAWideWindow(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	job := mustCreateJob(t, st, store.Job{Slug: "some-job", Repo: "kraken", Body: "x"})
	if _, err := st.AddFeedback(store.Feedback{
		JobID: job.ID, JobSlug: job.Slug, Artifact: "00-overview.md",
		Note: "n", TS: now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	// 30 days stale — the "serve was off for a month" shape.
	stale := now.Add(-30 * 24 * time.Hour).UnixMilli()

	created, next, ok := runDreamPass(now, DreamRun{Date: "2026-06-26", SweptTo: stale})

	if !ok || created == nil {
		t.Fatalf("runDreamPass = (%v, %+v, %v), want a created job", created, next, ok)
	}
	floor := now.Add(-dreamMaxWindow).UnixMilli()
	if !strings.Contains(created.Body, time.UnixMilli(floor).Format(time.RFC3339)) {
		t.Errorf("body does not name the clamped floor %s:\n%s", time.UnixMilli(floor).Format(time.RFC3339), created.Body)
	}
	if !strings.Contains(created.Body, "This window was clamped") {
		t.Errorf("body does not disclose the clamp — a truncated sweep must never read as complete:\n%s", created.Body)
	}
}

// A negative floor (corruption, a hand edit) must not render a 1969 date or a
// negative --since into the spec zeus follows.
func TestDreamPassRejectsANegativeFloor(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	job := mustCreateJob(t, st, store.Job{Slug: "some-job", Repo: "kraken", Body: "x"})
	if _, err := st.AddFeedback(store.Feedback{
		JobID: job.ID, JobSlug: job.Slug, Artifact: "a.md",
		Note: "n", TS: now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	created, _, ok := runDreamPass(now, DreamRun{Date: "2026-07-01", SweptTo: -1})

	if !ok || created == nil {
		t.Fatal("want a created job")
	}
	if strings.Contains(created.Body, "1969") || strings.Contains(created.Body, "--since -") {
		t.Errorf("negative floor leaked into the spec body:\n%s", created.Body)
	}
}

// An unclamped ordinary window must NOT carry the clamp disclosure — a
// warning that fires every night is a warning nobody reads.
func TestDreamPassDoesNotDiscloseAClampItDidNotMake(t *testing.T) {
	st := useTestStore(t)
	resetDreamGuards(t)
	now := dreamNoon()
	job := mustCreateJob(t, st, store.Job{Slug: "some-job", Repo: "kraken", Body: "x"})
	if _, err := st.AddFeedback(store.Feedback{
		JobID: job.ID, JobSlug: job.Slug, Artifact: "a.md",
		Note: "n", TS: now.Add(-time.Hour).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}

	created, _, ok := runDreamPass(now, DreamRun{Date: "2026-07-25", SweptTo: now.Add(-24 * time.Hour).UnixMilli()})

	if !ok || created == nil {
		t.Fatal("want a created job")
	}
	if strings.Contains(created.Body, "clamped") {
		t.Errorf("a one-day window claims to be clamped:\n%s", created.Body)
	}
}
