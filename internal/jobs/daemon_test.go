package jobs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// daemon_test.go — the PR tracker's pure reconciliation. The tracker's whole
// safety story is here: recorded history never shrinks, notifications fire
// exactly on the transition to merged, and an unchanged fetch is a no-op.

// TestPruneArtifactSyncStateDropsDoneArchivedAndVanished covers:
// an ArtifactSyncState entry survives only for slugs currently started and
// not done; done/vanished slugs get pruned so a later slug-reuse can't
// inherit a stale transition state.
func TestPruneArtifactSyncStateDropsDoneArchivedAndVanished(t *testing.T) {
	d := newDaemon()
	d.ArtifactSyncState["still-live"] = "working"
	d.ArtifactSyncState["now-done"] = "working"
	d.ArtifactSyncState["never-started"] = "blocked"
	d.ArtifactSyncState["vanished"] = "blocked" // archived/deleted: absent from jobs entirely

	jobs := []*Job{
		{Slug: "still-live", State: "working"},
		{Slug: "now-done", State: "done"},
		{Slug: "never-started", State: ""},
	}
	d.pruneArtifactSyncState(jobs)

	if _, ok := d.ArtifactSyncState["still-live"]; !ok {
		t.Fatal("still-live entry pruned, want it kept")
	}
	if _, ok := d.ArtifactSyncState["now-done"]; ok {
		t.Fatal("now-done entry survived prune, want it dropped")
	}
	if _, ok := d.ArtifactSyncState["never-started"]; ok {
		t.Fatal("never-started entry survived prune, want it dropped")
	}
	if _, ok := d.ArtifactSyncState["vanished"]; ok {
		t.Fatal("vanished-slug entry survived prune, want it dropped")
	}
	if len(d.ArtifactSyncState) != 1 {
		t.Fatalf("want exactly 1 surviving entry, got %+v", d.ArtifactSyncState)
	}
}

// TestDaemonGuardErr covers the single-instance startup guard underlying
// kraken serve's GuardServeStart: refuses only when daemon.json names a
// DIFFERENT, currently-alive pid.
func TestDaemonGuardErr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := daemonGuardErr(); err != nil {
		t.Fatalf("no daemon.json yet: want nil, got %v", err)
	}

	// our own pid: never a conflict, even "alive"
	writeDaemonStatusFile(DaemonStatus{PID: os.Getpid()})
	if err := daemonGuardErr(); err != nil {
		t.Fatalf("status naming our own pid: want nil, got %v", err)
	}

	// a dead pid (0 never passes syscall.Kill's alive check): not a conflict
	writeDaemonStatusFile(DaemonStatus{PID: 0})
	if err := daemonGuardErr(); err != nil {
		t.Fatalf("status naming pid 0: want nil, got %v", err)
	}

	// GuardServeStart is a thin wrapper over the same check
	if err := GuardServeStart(); err != nil {
		t.Fatalf("GuardServeStart with no live conflicting pid: want nil, got %v", err)
	}
}

// TestDaemonGuardErrIgnoresRecycledNonKrakenPid covers E14: a daemon.json
// naming a pid that's genuinely alive but is NOT actually kraken (a real
// PID-recycling scenario — the daemon that wrote the file died, and the OS
// has since handed its pid to an unrelated process) must NOT refuse.
// os.Getppid() (the `go test` runner) is guaranteed alive for this test's
// duration and is never going to be named "kraken", making it a reliable
// stand-in for "some other, unrelated live process holding a recycled pid."
func TestDaemonGuardErrIgnoresRecycledNonKrakenPid(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	writeDaemonStatusFile(DaemonStatus{PID: os.Getppid()})
	if err := daemonGuardErr(); err != nil {
		t.Fatalf("status naming a live but non-kraken pid: want nil (not a real conflict), got %v", err)
	}
}

// TestDaemonConflictErrMentionsRemedy covers E14's error-text requirement:
// a genuine refusal must name the stale-state remedy (rm
// ~/.kraken/daemon.json) so a stuck guard always has an obvious way out —
// tested directly against the pure formatter daemonGuardErr delegates to,
// since triggering the real refusal branch needs a second live pid this
// process can prove is actually named "kraken".
func TestDaemonConflictErrMentionsRemedy(t *testing.T) {
	err := daemonConflictErr(4242)
	if !strings.Contains(err.Error(), "~/.kraken/daemon.json") {
		t.Fatalf("error = %q, want it to mention ~/.kraken/daemon.json", err.Error())
	}
	if !strings.Contains(err.Error(), "4242") {
		t.Fatalf("error = %q, want it to name the conflicting pid", err.Error())
	}
}

// TestProcessIsKrakenFalseForUnrelatedProcess pins ProcessIsKraken's core
// contract directly: a real, alive, definitely-not-kraken pid (the `go
// test` runner) must read false.
func TestProcessIsKrakenFalseForUnrelatedProcess(t *testing.T) {
	if ProcessIsKraken(os.Getppid()) {
		t.Fatal("want false for the go test runner's own pid")
	}
}

// TestTickFirstTickSyncsAlreadyBlockedJobArtifacts covers E11: a job that
// was ALREADY blocked before this process started (e.g. `kraken serve`
// restarted while a cell sat blocked) must have its artifacts synced on
// the very first tick, even though no working→blocked EDGE is observed
// this tick (ArtifactSyncState starts blank, so the transition check alone
// would silently miss it). No live tmux session exists in this test
// environment, so the job reads as blocked (deriveState: started but not
// live → blocked) — exactly the restart-while-blocked scenario.
func TestTickFirstTickSyncsAlreadyBlockedJobArtifacts(t *testing.T) {
	st := useTestStore(t)
	// state derivation (and this test's new sync code) only runs once
	// ResolveRepo succeeds, so this needs a REAL (fake, but real on disk)
	// git repo under ~/Developer — a nonexistent repo name (the usual
	// pattern elsewhere in this file) would `continue` before ever
	// reaching it.
	repoDir := filepath.Join(util.Home(), "Developer", "fake-repo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sj := mustCreateJob(t, st, store.Job{
		Slug: "already-blocked", Repo: "fake-repo",
		State: "blocked",
	})
	j := JobBySlug(LoadJobs(), sj.Slug)
	if j == nil {
		t.Fatal("job not found after create")
	}
	// seed the cell's artifacts dir with a real file for the sync to pick up.
	artDir := CellArtifacts(j)
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artDir, "report.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := newDaemon()
	if _, err := d.tick(false); err != nil {
		t.Fatalf("tick: %v", err)
	}

	arts, err := st.ListArtifacts(sj.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 || arts[0].Path != "report.md" {
		t.Fatalf("want report.md synced on the first tick despite no observed transition, got %+v", arts)
	}
	if d.ArtifactSyncState[j.Slug] != "blocked" {
		t.Fatalf("ArtifactSyncState[%s] = %q, want seeded to blocked", j.Slug, d.ArtifactSyncState[j.Slug])
	}
}

// TestWriteDaemonStatusFileRoundTrips confirms RunServeTicks' new
// daemon.json write is readable back exactly the way RunDaemon's
// always was.
func TestWriteDaemonStatusFileRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := DaemonStatus{PID: 4242, StartedAt: 100, LastTick: 200, Cells: 3}
	writeDaemonStatusFile(want)
	got, ok := readDaemonStatusFile()
	if !ok {
		t.Fatal("want a status after writeDaemonStatusFile, got none")
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestStopServeTicksIdempotent confirms a second (or concurrent-in-spirit)
// call never panics on a double channel close — a repeated signal, or a
// test re-running in the same process, must be safe.
func TestStopServeTicksIdempotent(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("StopServeTicks panicked on repeat calls: %v", r)
		}
	}()
	StopServeTicks()
	StopServeTicks()
	select {
	case <-serveStop:
	default:
		t.Fatal("want serveStop closed after StopServeTicks")
	}
}

func TestAutoArchivePass(t *testing.T) {
	st := useTestStore(t)
	old := time.Now().Add(-25 * time.Hour).UnixMilli()
	mustCreateJob(t, st, store.Job{Slug: "stale", Repo: "x", State: "done", UpdatedAt: old})
	mustCreateJob(t, st, store.Job{Slug: "fresh", Repo: "x", State: "done"})

	jobs := LoadJobs()
	archived := autoArchivePass(jobs, map[string]bool{}, time.Now())

	if len(archived) != 1 || archived[0] != "stale" {
		t.Fatalf("archived: got %v, want [stale]", archived)
	}
	if a := ArchivedJobByName(LoadArchivedJobs(), "stale"); a == nil {
		t.Fatal("stale job didn't land in the archive")
	}
	remaining := LoadJobs()
	if len(remaining) != 1 || remaining[0].Slug != "fresh" {
		t.Fatalf("active jobs after sweep: got %+v, want just fresh", remaining)
	}
}

func TestPRTrackable(t *testing.T) {
	cases := []struct {
		name string
		job  *Job
		want bool
	}{
		{
			name: "working job",
			job:  &Job{State: "working"},
			want: true,
		},
		{
			name: "blocked job",
			job:  &Job{State: "blocked"},
			want: true,
		},
		{
			name: "done job stays tracked",
			job:  &Job{State: "done"},
			want: true,
		},
		{
			name: "never-started draft with no PR history",
			job:  &Job{State: ""},
			want: false,
		},
		{
			name: "killed back to draft keeps its PR history tracked",
			job:  &Job{State: "", PRs: []JobPR{{URL: "https://x/pull/1", State: "merged"}}},
			want: true,
		},
		{
			// Q14: there is no job-level type gate any more — a purely
			// explanatory job simply never opens a PR, so a done job with
			// recorded PRs stays tracked regardless of what kind of job it
			// was (previously excluded here by a "type == info" check).
			name: "done job with PRs stays tracked",
			job:  &Job{State: "done", PRs: []JobPR{{URL: "https://x/pull/9", State: "open"}}},
			want: true,
		},
		{
			name: "re-queued killed job keeps tracking",
			job:  &Job{State: "queued", PRs: []JobPR{{URL: "https://x/pull/2", State: "open"}}},
			want: true,
		},
		{
			name: "unparseable job never tracked",
			job:  &Job{State: "working", Err: "duplicate slug"},
			want: false,
		},
	}
	for _, c := range cases {
		if got := prTrackable(c.job); got != c.want {
			t.Errorf("%s: prTrackable = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWatchPRsSkipsUnparseableJob is the eligibility-gate regression for
// watchPRs (reviewer finding: watchPRs must not run for a job that failed to
// parse cleanly). Without the gate, updatePRWatermark's patchPRs pass
// re-reads the file, reparses it — and because the parser is strict, a
// hand-edit typo in one prs: entry (e.g. "state: opne") both marks the whole
// job Err AND silently drops that entry from the in-memory list — then
// rewrites the WHOLE prs: block from that reduced list. The typo'd entry
// would vanish from disk forever, and the very same rewrite clears the
// job's Err on the next parse (the offending line is gone, so nothing is
// left to error on). The gate (reusing prTrackable, exactly like the
// tracker itself) must make watchPRs return before it ever touches gh or
// the file. Proven for real: a fake `gh` on PATH and a real (if empty) git
// checkout so ResolveRepo succeeds — if the gate didn't fire, this test
// would observe both the gh call and the file rewrite.
func TestWatchPRsSkipsUnparseableJob(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_PLUTO_DIR", filepath.Join(home, "pluto"))
	if err := os.MkdirAll(PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}

	// a real (if empty) git checkout so ResolveRepo can resolve "testrepo" —
	// this must succeed, so a failure to reach gh proves the ELIGIBILITY
	// GATE fired, not merely that the repo couldn't be found
	repoDir := filepath.Join(home, "Developer", "testrepo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	// fake gh on PATH: if watchPRs ever invokes it, that alone proves the
	// gate is missing (and would go on to corrupt the fixture the same way
	// the real bug does)
	fakeBin := t.TempDir()
	ghCalled := filepath.Join(fakeBin, "gh-was-called")
	script := "#!/bin/sh\ntouch " + ghCalled + "\n" +
		`echo '{"state":"open","headRefOid":"sha1","comments":[{"createdAt":"2024-01-01T00:00:00Z"}],"reviews":[],"statusCheckRollup":[]}'` + "\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))

	// the exact scenario from the finding: a hand-edit typo ("state: opne")
	// in one prs: entry marks the whole job Err — and, being strict, the
	// parser already drops that entry from j.PRs in memory, though its raw
	// text still sits untouched on disk unless a write happens.
	content := "---\n" +
		"job: typojob\n" +
		"type: build\n" +
		"repo: testrepo\n" +
		"ready: false\n" +
		"state: working\n" +
		"prs:\n" +
		"  - url: https://github.com/x/x/pull/1\n" +
		"    state: open\n" +
		"  - url: https://github.com/x/x/pull/2\n" +
		"    state: opne\n" + // typo — marks the whole job Err, drops this entry from j.PRs
		"---\n\n# Spec\n"
	path := filepath.Join(PlutoDir(), "typojob.job.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	j := ParseJob("typojob.job.md", []byte(content))
	j.File = path
	if j.Err == "" {
		t.Fatal("fixture setup: expected the typo'd prs entry to mark the job Err")
	}
	if len(j.PRs) != 1 || j.PRs[0].URL != "https://github.com/x/x/pull/1" {
		t.Fatalf("fixture setup: expected only pull/1 to survive strict parsing, got %+v", j.PRs)
	}

	d := newDaemon()
	live := map[string]bool{CellSession(j): true}
	d.watchPRs(j, nil, live)

	if _, err := os.Stat(ghCalled); err == nil {
		t.Fatal("watchPRs called gh for an unparseable (Err != \"\") job — the eligibility gate did not fire")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != content {
		t.Fatalf("unparseable job's file was rewritten (watermark write not gated):\nbefore: %s\nafter:  %s", content, string(after))
	}
}

// fakeProbe counts invocations and returns whatever seq says (defaulting to
// the last value once seq runs out), so a test can script a probe sequence
// (offline, offline, online, ...) and still assert how many dials happened.
type fakeProbe struct {
	seq   []bool
	calls int
}

func (f *fakeProbe) check() bool {
	f.calls++
	if f.calls-1 < len(f.seq) {
		return f.seq[f.calls-1]
	}
	if len(f.seq) == 0 {
		return true
	}
	return f.seq[len(f.seq)-1]
}

// memoized wraps a fakeProbe the way tick() does: at most one real dial per
// simulated tick, regardless of how many stalled cells consult it.
func memoized(f *fakeProbe) func() bool {
	var probed, online bool
	return func() bool {
		if !probed {
			online = f.check()
			probed = true
		}
		return online
	}
}

func TestGateRetryOnlinePokeDue(t *testing.T) {
	d := newDaemon()
	now := time.Now()
	f := &fakeProbe{seq: []bool{true}}

	poke, title, _ := d.gateRetry(now, "retry:x", memoized(f))

	if !poke {
		t.Fatal("want poke=true when online and poke due")
	}
	if title != "" {
		t.Fatalf("want no notification, got %q", title)
	}
	if f.calls != 1 {
		t.Fatalf("want 1 probe call, got %d", f.calls)
	}
	if d.offline {
		t.Fatal("want offline=false")
	}
	// backoff stamping is the caller's job (tick), not gateRetry's — confirm
	// gateRetry itself never writes to Poked.
	if len(d.Poked) != 0 {
		t.Fatalf("gateRetry must not stamp Poked itself, got %v", d.Poked)
	}
}

func TestGateRetryOfflineSuppressesPokeAndNotifiesOnce(t *testing.T) {
	d := newDaemon()
	now := time.Now()

	// tick 1: offline — poke suppressed, offline flagged, one notification.
	f1 := &fakeProbe{seq: []bool{false}}
	poke, title, msg := d.gateRetry(now, "retry:x", memoized(f1))
	if poke {
		t.Fatal("want poke=false while offline")
	}
	if !d.offline {
		t.Fatal("want offline=true")
	}
	if title == "" || msg == "" {
		t.Fatal("want an offline notification on the transition")
	}

	// tick 2: still offline — no poke, no second notification (no flap spam).
	f2 := &fakeProbe{seq: []bool{false}}
	poke, title, _ = d.gateRetry(now, "retry:x", memoized(f2))
	if poke {
		t.Fatal("want poke=false on tick 2")
	}
	if title != "" {
		t.Fatalf("want no second offline notification, got %q", title)
	}
	if !d.offline {
		t.Fatal("want offline still true")
	}
}

func TestGateRetryReconnectClearsOfflineAndPurgesBackoff(t *testing.T) {
	d := newDaemon()
	now := time.Now()

	// go offline first, same as the previous test.
	f1 := &fakeProbe{seq: []bool{false}}
	d.gateRetry(now, "retry:x", memoized(f1))
	if !d.offline {
		t.Fatal("setup: want offline=true")
	}
	// simulate a suppressed poke for a second slug too, so purge-on-reconnect
	// is exercised against more than one retry: key.
	d.Poked["retry:y"] = now.Add(-time.Hour).Unix()

	// reconnect — offline clears, retry: backoff purged, one notification,
	// and THIS tick's poke is allowed through immediately.
	f2 := &fakeProbe{seq: []bool{true}}
	poke, title, msg := d.gateRetry(now, "retry:x", memoized(f2))
	if !poke {
		t.Fatal("want poke=true on the reconnect tick")
	}
	if d.offline {
		t.Fatal("want offline=false after reconnect")
	}
	if title == "" || msg == "" {
		t.Fatal("want a back-online notification")
	}
	if d.Notified["offline"] != 0 {
		t.Fatal("want the offline Notified key cleared so a future outage re-notifies")
	}
	if _, ok := d.Poked["retry:y"]; ok {
		t.Fatal("want retry: backoff purged on reconnect")
	}
}

func TestGateRetryProbeNotInvokedWhenNothingDue(t *testing.T) {
	d := newDaemon()
	now := time.Now()
	// poke not due (just stamped) and not offline: gateRetry must not touch
	// checkOnline at all.
	d.Poked["retry:x"] = now.Unix()
	f := &fakeProbe{seq: []bool{true}}

	poke, title, _ := d.gateRetry(now, "retry:x", memoized(f))

	if poke {
		t.Fatal("want poke=false when backoff hasn't elapsed")
	}
	if title != "" {
		t.Fatalf("want no notification, got %q", title)
	}
	if f.calls != 0 {
		t.Fatalf("want probe never invoked, got %d calls", f.calls)
	}
}

// TestGateRetryProbeConsultedWhenOfflineEvenIfNotDue covers the other half
// of the not-due gate: unlike the case above, an ALREADY-offline daemon must
// still consult the probe on a not-due poke, so a reconnect is noticed
// promptly instead of waiting for the next due poke.
func TestGateRetryProbeConsultedWhenOfflineEvenIfNotDue(t *testing.T) {
	d := newDaemon()
	now := time.Now()
	d.offline = true
	d.Notified["offline"] = now.Add(-time.Hour).Unix() // already notified on a prior tick
	d.Poked["retry:x"] = now.Unix()                    // just poked — not due
	f := &fakeProbe{seq: []bool{false}}

	poke, title, _ := d.gateRetry(now, "retry:x", memoized(f))

	if poke {
		t.Fatal("want poke=false when backoff hasn't elapsed")
	}
	if title != "" {
		t.Fatalf("want no notification (already flagged offline, already notified), got %q", title)
	}
	if f.calls != 1 {
		t.Fatalf("want probe consulted once because d.offline, got %d calls", f.calls)
	}
}

// TestGateRetryMultiCellTickSharesProbeAndNotifiesOnce simulates a single
// tick with two stalled cells (distinct rkeys) both consulting the SAME
// memoized checkOnline closure the way tick() builds it — the probe must
// dial at most once for the whole tick, and only the first call (the one
// that actually flips offline) may produce a notification.
func TestGateRetryMultiCellTickSharesProbeAndNotifiesOnce(t *testing.T) {
	d := newDaemon()
	now := time.Now()
	f := &fakeProbe{seq: []bool{false}} // offline
	check := memoized(f)

	poke1, title1, _ := d.gateRetry(now, "retry:a", check)
	poke2, title2, _ := d.gateRetry(now, "retry:b", check)

	if poke1 || poke2 {
		t.Fatal("want no poke for either cell while offline")
	}
	notifications := 0
	for _, title := range []string{title1, title2} {
		if title != "" {
			notifications++
		}
	}
	if notifications != 1 {
		t.Fatalf("want exactly one offline notification across both calls, got %d (titles %q, %q)", notifications, title1, title2)
	}
	if f.calls != 1 {
		t.Fatalf("want the probe dialed once for the whole tick, got %d calls", f.calls)
	}
}

// TestDaemonStateClearOffline pins clearOffline's bookkeeping directly: the
// helper backing both gateRetry's reconnect path and tick's own tick-start
// reconnect check.
func TestDaemonStateClearOffline(t *testing.T) {
	d := newDaemon()
	now := time.Now()
	d.offline = true
	d.Notified["offline"] = now.Unix()
	d.Poked["retry:a"] = now.Unix()
	d.Poked["blocked:b"] = now.Unix() // non-retry key must survive the purge

	title, msg := d.clearOffline()

	if d.offline {
		t.Fatal("want offline cleared")
	}
	if _, ok := d.Notified["offline"]; ok {
		t.Fatal("want the offline Notified key cleared")
	}
	if _, ok := d.Poked["retry:a"]; ok {
		t.Fatal("want retry: backoff purged")
	}
	if _, ok := d.Poked["blocked:b"]; !ok {
		t.Fatal("want non-retry: Poked keys preserved")
	}
	if title == "" || msg == "" {
		t.Fatal("want a back-online notification")
	}
}

// TestTickStartReconnectClearsStaleOfflineFlag pins the bug this slice
// fixes: before the tick-start check, a stale d.offline could only clear
// inside gateRetry, which only runs when a retryable health reason exists
// THIS tick. If every stalled cell recovered some other way (a manual
// retry, the turn just finishing) while d.offline stayed true, the flag
// lingered until some later, unrelated stall — which then fired a spurious
// "back online" notification alongside its own. With zero jobs in play here,
// gateRetry never runs at all, so this exercises the tick-start check in
// isolation.
func TestTickStartReconnectClearsStaleOfflineFlag(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	old := probeOnline
	probeOnline = func() bool { return true }
	defer func() { probeOnline = old }()

	d := newDaemon()
	d.offline = true
	d.Notified["offline"] = time.Now().Unix()
	d.Poked["retry:a"] = time.Now().Unix()

	if _, err := d.tick(false); err != nil {
		t.Fatalf("tick: %v", err)
	}

	if d.offline {
		t.Fatal("want the stale offline flag cleared at tick start, even with no stalled cells this tick")
	}
	if _, ok := d.Notified["offline"]; ok {
		t.Fatal("want the offline Notified key cleared")
	}
	if _, ok := d.Poked["retry:a"]; ok {
		t.Fatal("want retry: backoff purged on reconnect")
	}
}

func TestReconcilePRs(t *testing.T) {
	pr := func(n, state string) JobPR { return JobPR{URL: "https://x/pull/" + n, State: state} }
	cases := []struct {
		name        string
		recorded    []JobPR
		fetched     []JobPR
		want        []JobPR
		wantMerged  []string
		wantChanged bool
	}{
		{
			name:        "first sight of an open PR appends",
			fetched:     []JobPR{pr("1", "open")},
			want:        []JobPR{pr("1", "open")},
			wantChanged: true,
		},
		{
			name:        "unchanged fetch is a no-op",
			recorded:    []JobPR{pr("1", "open")},
			fetched:     []JobPR{pr("1", "open")},
			want:        []JobPR{pr("1", "open")},
			wantChanged: false,
		},
		{
			name:        "open to merged transitions once",
			recorded:    []JobPR{pr("1", "open")},
			fetched:     []JobPR{pr("1", "merged")},
			want:        []JobPR{pr("1", "merged")},
			wantMerged:  []string{"https://x/pull/1"},
			wantChanged: true,
		},
		{
			name:        "first sight already merged still notifies",
			fetched:     []JobPR{pr("1", "merged")},
			want:        []JobPR{pr("1", "merged")},
			wantMerged:  []string{"https://x/pull/1"},
			wantChanged: true,
		},
		{
			name:        "recorded history survives a fetch that no longer lists it",
			recorded:    []JobPR{pr("1", "merged")},
			fetched:     []JobPR{pr("2", "open")},
			want:        []JobPR{pr("1", "merged"), pr("2", "open")},
			wantChanged: true,
		},
		{
			name:        "closed updates without a merge notification",
			recorded:    []JobPR{pr("1", "open")},
			fetched:     []JobPR{pr("1", "closed")},
			want:        []JobPR{pr("1", "closed")},
			wantChanged: true,
		},
		{
			name:        "already merged never re-notifies",
			recorded:    []JobPR{pr("1", "merged")},
			fetched:     []JobPR{pr("1", "merged")},
			want:        []JobPR{pr("1", "merged")},
			wantChanged: false,
		},
	}
	for _, c := range cases {
		got, merged, changed := reconcilePRs(c.recorded, c.fetched)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: prs %+v, want %+v", c.name, got, c.want)
		}
		if !reflect.DeepEqual(merged, c.wantMerged) {
			t.Errorf("%s: newlyMerged %v, want %v", c.name, merged, c.wantMerged)
		}
		if changed != c.wantChanged {
			t.Errorf("%s: changed %v, want %v", c.name, changed, c.wantChanged)
		}
	}
}

// repairFilter's whole safety story: a record is dropped only when NEITHER
// create-vouched NOR a family head backs it — everything else (vouched,
// family, or simply unknown) survives, and unknown additionally reports the
// pass incomplete so the caller retries instead of hardening a guess.
func TestRepairFilter(t *testing.T) {
	pr := func(n string) JobPR { return JobPR{URL: "https://x/pull/" + n, State: "open"} }
	inFamily := func(b string) bool { return b == "carl/slug" }

	t.Run("drops only both-negative records", func(t *testing.T) {
		headOf := func(url string) (string, bool) { return "carl/other-job", true }
		kept, dropped, complete := repairFilter([]JobPR{pr("1")}, map[string]bool{}, true, headOf, inFamily)
		if len(kept) != 0 || !reflect.DeepEqual(dropped, []string{"https://x/pull/1"}) || !complete {
			t.Fatalf("kept=%+v dropped=%v complete=%v", kept, dropped, complete)
		}
	})

	t.Run("create-vouched keeps even with a foreign head", func(t *testing.T) {
		vouched := map[string]bool{"https://x/pull/1": true}
		// headOf would say "drop" if consulted — vouched must short-circuit it.
		headOf := func(url string) (string, bool) { return "carl/other-job", true }
		kept, dropped, complete := repairFilter([]JobPR{pr("1")}, vouched, true, headOf, inFamily)
		if len(dropped) != 0 || !reflect.DeepEqual(kept, []JobPR{pr("1")}) || !complete {
			t.Fatalf("kept=%+v dropped=%v complete=%v", kept, dropped, complete)
		}
	})

	t.Run("family head keeps", func(t *testing.T) {
		headOf := func(url string) (string, bool) { return "carl/slug", true }
		kept, dropped, complete := repairFilter([]JobPR{pr("1")}, map[string]bool{}, true, headOf, inFamily)
		if len(dropped) != 0 || !reflect.DeepEqual(kept, []JobPR{pr("1")}) || !complete {
			t.Fatalf("kept=%+v dropped=%v complete=%v", kept, dropped, complete)
		}
	})

	t.Run("unknown head keeps and reports incomplete", func(t *testing.T) {
		headOf := func(url string) (string, bool) { return "", false }
		kept, dropped, complete := repairFilter([]JobPR{pr("1")}, map[string]bool{}, true, headOf, inFamily)
		if len(dropped) != 0 || !reflect.DeepEqual(kept, []JobPR{pr("1")}) || complete {
			t.Fatalf("kept=%+v dropped=%v complete=%v, want complete=false", kept, dropped, complete)
		}
	})

	t.Run("empty recorded is trivially complete with no drops", func(t *testing.T) {
		headOf := func(url string) (string, bool) { t.Fatal("headOf must not be called"); return "", false }
		kept, dropped, complete := repairFilter(nil, map[string]bool{}, true, headOf, inFamily)
		if kept != nil || dropped != nil || !complete {
			t.Fatalf("kept=%+v dropped=%v complete=%v", kept, dropped, complete)
		}
	})

	// vouchedOK=false is the tri-state fix: an unreadable transcript must
	// never be treated as "vouches for nothing". Every record survives
	// untouched, complete is false so the caller retries, and headOf is
	// never invoked — asserted with a counting closure — since nothing
	// could safely be dropped this pass anyway, so spending a gh call on
	// the head check would be pure waste.
	t.Run("vouchedOK=false keeps everything and never calls headOf", func(t *testing.T) {
		calls := 0
		headOf := func(url string) (string, bool) {
			calls++
			return "carl/other-job", true // would say "drop" if ever consulted
		}
		recorded := []JobPR{pr("1"), pr("2")}
		kept, dropped, complete := repairFilter(recorded, map[string]bool{}, false, headOf, inFamily)
		if !reflect.DeepEqual(kept, recorded) || dropped != nil || complete {
			t.Fatalf("kept=%+v dropped=%v complete=%v, want kept=%+v dropped=nil complete=false", kept, dropped, complete, recorded)
		}
		if calls != 0 {
			t.Fatalf("headOf called %d times, want 0", calls)
		}
	})
}

// branchFamily must match exactly the branches a job could actually have
// shipped from — every run through the job's current Run, nothing that
// merely shares the slug as a prefix.
//
// It matches TWO families since branches took the job id: the current
// carl/<slug>-<id> names, and the pre-cutover bare-slug ones that PRs opened
// before the change still sit on. Dropping the legacy half would make the
// PR-repair pass stop recognising those PRs as the job's own.
func TestBranchFamily(t *testing.T) {
	fam1 := branchFamily(&Job{Slug: "slug", ID: 7, Run: 1})
	if len(fam1) != 2 || !fam1["carl/slug-7"] || !fam1["carl/slug"] {
		t.Fatalf("run 1: got %v, want carl/slug-7 plus the legacy carl/slug", fam1)
	}

	fam3 := branchFamily(&Job{Slug: "slug", ID: 7, Run: 3})
	want := []string{
		"carl/slug-7", "carl/slug-7-r2", "carl/slug-7-r3",
		"carl/slug", "carl/slug-r2", "carl/slug-r3",
	}
	if len(fam3) != len(want) {
		t.Fatalf("run 3: got %v, want exactly %v", fam3, want)
	}
	for _, b := range want {
		if !fam3[b] {
			t.Errorf("run 3: missing %s in %v", b, fam3)
		}
	}

	reject := []string{
		"carl/slugx",        // no separator — not a run suffix
		"carl/slug-r",       // dangling -r, no digit
		"carl/slug-extra",   // unrelated suffix, not -rN
		"carl/slug2-r2",     // prefix-similar OTHER slug
		"carl/slug-r2extra", // -r2 prefix but not an exact match
	}
	for _, b := range reject {
		if fam3[b] {
			t.Errorf("run 3: unexpectedly matched %s", b)
		}
	}

	// a hand-typo'd run (e.g. `run: 2000000000`) must not make branchFamily
	// allocate/iterate billions of entries — the cap holds maxRun at 64,
	// giving base (r=1) + r2..r64 = 64 names per family, and since the family
	// covers both the id-form and the legacy bare-slug branches that is 128
	// entries. The whole call must still return quickly.
	start := time.Now()
	famHuge := branchFamily(&Job{Slug: "slug", ID: 7, Run: 2000000000})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("run 2000000000: took %v, want fast (cap should bound the loop)", elapsed)
	}
	if len(famHuge) != 128 {
		t.Fatalf("run 2000000000: got %d entries, want exactly 128 (64 per family, cap applied)", len(famHuge))
	}
}

// --- mirror mode (kraken serve --mirror, ServeOpts.Mirror) ---

// TestTickMirrorNeverStartsACell covers the mirror tick's core contract. A
// never-started job sits in the sandbox DB exactly like it would for the real
// scheduler, and d.tick(true) must leave it entirely alone: no StartCell
// call, so no cell dir/worktree/tmux session ever gets created, and the job's
// own state stays untouched.
//
// With the queue pass gone this is narrower than it was, and deliberately
// kept: the tick's remaining start path is the dream/graph one, which is
// behind the same !mirror gate, so a mirror tick that ever started anything
// would be a second real scheduler racing the live daemon — the thing this
// test exists to prevent. A draft is the shape that would have been swept up
// first.
func TestTickMirrorNeverStartsACell(t *testing.T) {
	st := useTestStore(t)
	sj := mustCreateJob(t, st, store.Job{
		Slug: "mirror-draft", Repo: "fake-repo",
	})

	d := newDaemon()
	if _, err := d.tick(true); err != nil {
		t.Fatalf("mirror tick: %v", err)
	}

	j := JobBySlug(LoadJobs(), sj.Slug)
	if j == nil {
		t.Fatal("job vanished after mirror tick")
	}
	if j.Started() {
		t.Fatalf("mirror tick started the job (state %q) — StartCell must never run under mirror", j.State)
	}
	if fresh, err := st.GetJobBySlug(sj.Slug); err != nil || fresh.State != "" {
		t.Fatalf("job state = %q, err %v — want it to remain a draft (empty state)", fresh.State, err)
	}
	if _, err := os.Stat(cellDir(j)); !os.IsNotExist(err) {
		t.Fatalf("cell dir %s exists after a mirror tick — no cell should ever be created", cellDir(j))
	}
}

// TestTickMirrorSkipsAutoArchive covers another mirror-only skip: a done job
// old enough for autoArchivePass to normally sweep must be left completely
// alone — mirror must never move real pluto/archive files on its own
// initiative.
func TestTickMirrorSkipsAutoArchive(t *testing.T) {
	st := useTestStore(t)
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	sj := mustCreateJob(t, st, store.Job{
		Slug: "mirror-old-done", Repo: "fake-repo", State: "done",
		// UpdatedAt set directly at creation (CreateJob only defaults it to
		// "now" when left zero) so autoArchivable's 24h-old gate is already
		// satisfied without a second write bumping it back to now.
		UpdatedAt: old,
	})

	d := newDaemon()
	if _, err := d.tick(true); err != nil {
		t.Fatalf("mirror tick: %v", err)
	}

	if _, err := st.GetJobBySlug(sj.Slug); err != nil {
		t.Fatalf("job %s: want it to still be active (never archived) after a mirror tick, got %v", sj.Slug, err)
	}
	archived, err := st.ListArchivedJobs()
	if err != nil {
		t.Fatal(err)
	}
	if len(archived) != 0 {
		t.Fatalf("want zero archived jobs after a mirror tick, got %d", len(archived))
	}
}

// watchDecision's whole safety story: a bump only fires on genuine news (a
// newer comment/review timestamp, or a failing check on a SHA not already
// bumped), first sight never floods zeus with a PR's prior comment history,
// a currently-failing check bumps even on first sight since it's actionable
// regardless of history, and a PR that isn't (still) open never bumps.
func TestWatchDecision(t *testing.T) {
	cases := []struct {
		name                                                string
		fetch                                               prWatchFetch
		lastCommentAt, bumpedSHA, conflictState             string
		wantBump                                            bool
		wantMessageContains                                 []string
		wantLastCommentAt, wantBumpedSHA, wantConflictState string
	}{
		{
			name:              "no change is a no-op",
			fetch:             prWatchFetch{State: "open", HeadSHA: "sha1", LastCommentAt: "2024-01-01T00:00:00Z"},
			lastCommentAt:     "2024-01-01T00:00:00Z",
			bumpedSHA:         "sha1",
			wantBump:          false,
			wantLastCommentAt: "2024-01-01T00:00:00Z",
			wantBumpedSHA:     "sha1",
		},
		{
			name:                "new comment after the watermark bumps",
			fetch:               prWatchFetch{State: "open", HeadSHA: "sha1", LastCommentAt: "2024-01-02T00:00:00Z"},
			lastCommentAt:       "2024-01-01T00:00:00Z",
			bumpedSHA:           "sha1",
			wantBump:            true,
			wantMessageContains: []string{"new comment or review", "--comments", "[kraken pr-watch — automated notice, not a reply from carl]"},
			wantLastCommentAt:   "2024-01-02T00:00:00Z",
			wantBumpedSHA:       "sha1",
		},
		{
			name:              "failing CI on the already-bumped SHA never re-bumps",
			fetch:             prWatchFetch{State: "open", HeadSHA: "sha1", FailingChecks: []string{"ci/build"}},
			lastCommentAt:     "2024-01-01T00:00:00Z",
			bumpedSHA:         "sha1",
			wantBump:          false,
			wantLastCommentAt: "2024-01-01T00:00:00Z",
			wantBumpedSHA:     "sha1",
		},
		{
			// empty-SHA guard: gh occasionally returns a headRefOid gh
			// itself didn't fully populate. Must never bump with a blank
			// SHA in the message, and must never advance BumpedSHA to "" —
			// doing so would make the NEXT pass look like first sight again.
			name:              "failing CI with an empty head SHA never bumps or seeds",
			fetch:             prWatchFetch{State: "open", HeadSHA: "", FailingChecks: []string{"ci/build"}},
			lastCommentAt:     "2024-01-01T00:00:00Z",
			bumpedSHA:         "sha1",
			wantBump:          false,
			wantLastCommentAt: "2024-01-01T00:00:00Z",
			wantBumpedSHA:     "sha1",
		},
		{
			// load-bearing: a PR seeded quiet on green CI (bumpedSHA set,
			// lastCommentAt never seeded because there was no comment
			// history yet) must still bump on carl's first review days
			// later — firstSight is false here (bumpedSHA != ""), so this is
			// NOT the first-sight seed path; it was correct only by
			// accident before this test existed.
			name:                "quiet PR seeded on green CI still bumps on a later first comment",
			fetch:               prWatchFetch{State: "open", HeadSHA: "sha1", LastCommentAt: "2024-02-01T00:00:00Z"},
			lastCommentAt:       "",
			bumpedSHA:           "sha1",
			wantBump:            true,
			wantMessageContains: []string{"new comment or review"},
			wantLastCommentAt:   "2024-02-01T00:00:00Z",
			wantBumpedSHA:       "sha1",
		},
		{
			name:                "failing CI on a new SHA bumps",
			fetch:               prWatchFetch{State: "open", HeadSHA: "sha2", FailingChecks: []string{"ci/build"}},
			lastCommentAt:       "2024-01-01T00:00:00Z",
			bumpedSHA:           "sha1",
			wantBump:            true,
			wantMessageContains: []string{"failing CI", "sha2", "ci/build", "[kraken pr-watch — automated notice, not a reply from carl]", "pristine origin/main"},
			wantLastCommentAt:   "2024-01-01T00:00:00Z",
			wantBumpedSHA:       "sha2",
		},
		{
			name: "first sight seeds the comment watermark silently but still bumps a currently-failing check",
			fetch: prWatchFetch{
				State: "open", HeadSHA: "sha1",
				LastCommentAt: "2023-06-01T00:00:00Z", // pre-existing history — must not itself bump
				FailingChecks: []string{"ci/build"},
			},
			lastCommentAt:       "",
			bumpedSHA:           "",
			wantBump:            true,
			wantMessageContains: []string{"failing CI"},
			wantLastCommentAt:   "2023-06-01T00:00:00Z", // seeded, not reported
			wantBumpedSHA:       "sha1",
		},
		{
			name:              "first sight with checks currently green seeds both watermarks without bumping",
			fetch:             prWatchFetch{State: "open", HeadSHA: "sha1", LastCommentAt: "2023-06-01T00:00:00Z"},
			lastCommentAt:     "",
			bumpedSHA:         "",
			wantBump:          false,
			wantLastCommentAt: "2023-06-01T00:00:00Z",
			wantBumpedSHA:     "sha1",
		},
		{
			name: "a new comment and a newly-failing SHA in the same pass combine into one message",
			fetch: prWatchFetch{
				State: "open", HeadSHA: "sha2",
				LastCommentAt: "2024-01-02T00:00:00Z",
				FailingChecks: []string{"ci/build", "ci/lint"},
			},
			lastCommentAt:       "2024-01-01T00:00:00Z",
			bumpedSHA:           "sha1",
			wantBump:            true,
			wantMessageContains: []string{"new comment or review", "failing CI", "ci/build, ci/lint"},
			wantLastCommentAt:   "2024-01-02T00:00:00Z",
			wantBumpedSHA:       "sha2",
		},
		{
			name: "closed since last recorded as open never bumps, watermarks untouched",
			fetch: prWatchFetch{
				State: "closed", HeadSHA: "sha2",
				LastCommentAt: "2024-01-02T00:00:00Z",
				FailingChecks: []string{"ci/build"},
			},
			lastCommentAt:     "2024-01-01T00:00:00Z",
			bumpedSHA:         "sha1",
			wantBump:          false,
			wantLastCommentAt: "2024-01-01T00:00:00Z",
			wantBumpedSHA:     "sha1",
		},
		{
			name: "merged since last recorded as open never bumps, watermarks untouched",
			fetch: prWatchFetch{
				State: "merged", HeadSHA: "sha2",
				LastCommentAt: "2024-01-02T00:00:00Z",
			},
			lastCommentAt:     "",
			bumpedSHA:         "",
			wantBump:          false,
			wantLastCommentAt: "",
			wantBumpedSHA:     "",
		},
		{
			// the load-bearing conflict case: the base moved under an
			// UNCHANGED head, so a SHA-keyed watermark would never fire —
			// the clean→conflicting transition must.
			name:                "fresh conflict on an unchanged head bumps with a goal-stated message",
			fetch:               prWatchFetch{State: "open", HeadSHA: "sha1", Mergeable: "conflicting", BaseRef: "main"},
			lastCommentAt:       "2024-01-01T00:00:00Z",
			bumpedSHA:           "sha1",
			conflictState:       "clean",
			wantBump:            true,
			wantMessageContains: []string{"merge conflicts", "get the branch mergeable", "merge origin/main into your branch", "needs no force-push", "[kraken pr-watch — automated notice, not a reply from carl]"},
			wantLastCommentAt:   "2024-01-01T00:00:00Z",
			wantBumpedSHA:       "sha1",
			wantConflictState:   "conflicting",
		},
		{
			// the base-less variant must carry the same provenance prefix —
			// it was the second of the two PR-watch notices shipped without
			// one, and both arrive on the pane's user channel.
			name:                "fresh conflict with no base ref still carries the provenance prefix",
			fetch:               prWatchFetch{State: "open", HeadSHA: "sha1", Mergeable: "conflicting"},
			lastCommentAt:       "2024-01-01T00:00:00Z",
			bumpedSHA:           "sha1",
			conflictState:       "clean",
			wantBump:            true,
			wantMessageContains: []string{"merge conflicts with its base branch", "[kraken pr-watch — automated notice, not a reply from carl]"},
			wantLastCommentAt:   "2024-01-01T00:00:00Z",
			wantBumpedSHA:       "sha1",
			wantConflictState:   "conflicting",
		},
		{
			name:              "standing conflict never re-bumps",
			fetch:             prWatchFetch{State: "open", HeadSHA: "sha1", Mergeable: "conflicting", BaseRef: "main"},
			lastCommentAt:     "2024-01-01T00:00:00Z",
			bumpedSHA:         "sha1",
			conflictState:     "conflicting",
			wantBump:          false,
			wantLastCommentAt: "2024-01-01T00:00:00Z",
			wantBumpedSHA:     "sha1",
			wantConflictState: "conflicting",
		},
		{
			// gh recomputes mergeability lazily after a push; UNKNOWN must
			// neither bump nor clear, or a standing conflict would re-bump on
			// the UNKNOWN→CONFLICTING recompute.
			name:              "unknown mergeability leaves the conflict watermark untouched",
			fetch:             prWatchFetch{State: "open", HeadSHA: "sha1", Mergeable: "unknown"},
			lastCommentAt:     "2024-01-01T00:00:00Z",
			bumpedSHA:         "sha1",
			conflictState:     "conflicting",
			wantBump:          false,
			wantLastCommentAt: "2024-01-01T00:00:00Z",
			wantBumpedSHA:     "sha1",
			wantConflictState: "conflicting",
		},
		{
			name:              "conflict resolved records clean silently, re-arming the bump",
			fetch:             prWatchFetch{State: "open", HeadSHA: "sha2", Mergeable: "mergeable"},
			lastCommentAt:     "2024-01-01T00:00:00Z",
			bumpedSHA:         "sha1",
			conflictState:     "conflicting",
			wantBump:          false,
			wantLastCommentAt: "2024-01-01T00:00:00Z",
			wantBumpedSHA:     "sha1",
			wantConflictState: "clean",
		},
		{
			// like a failing check, a conflict is actionable right now — it
			// bumps even on the tick kraken starts watching the PR, while the
			// comment history still seeds silently.
			name: "first sight conflicting bumps even while comment history seeds silently",
			fetch: prWatchFetch{
				State: "open", HeadSHA: "sha1", Mergeable: "conflicting", BaseRef: "main",
				LastCommentAt: "2023-06-01T00:00:00Z",
			},
			lastCommentAt:       "",
			bumpedSHA:           "",
			conflictState:       "",
			wantBump:            true,
			wantMessageContains: []string{"merge conflicts"},
			wantLastCommentAt:   "2023-06-01T00:00:00Z",
			wantBumpedSHA:       "sha1",
			wantConflictState:   "conflicting",
		},
		{
			name:              "first sight mergeable seeds clean without bumping",
			fetch:             prWatchFetch{State: "open", HeadSHA: "sha1", Mergeable: "mergeable"},
			lastCommentAt:     "",
			bumpedSHA:         "",
			conflictState:     "",
			wantBump:          false,
			wantLastCommentAt: "",
			wantBumpedSHA:     "sha1",
			wantConflictState: "clean",
		},
		{
			name: "a new comment and a fresh conflict in the same pass combine into one message",
			fetch: prWatchFetch{
				State: "open", HeadSHA: "sha1", Mergeable: "conflicting", BaseRef: "main",
				LastCommentAt: "2024-01-02T00:00:00Z",
			},
			lastCommentAt:       "2024-01-01T00:00:00Z",
			bumpedSHA:           "sha1",
			conflictState:       "clean",
			wantBump:            true,
			wantMessageContains: []string{"new comment or review", "merge conflicts"},
			wantLastCommentAt:   "2024-01-02T00:00:00Z",
			wantBumpedSHA:       "sha1",
			wantConflictState:   "conflicting",
		},
		{
			name:                "conflict with an unknown base still bumps with a generic message",
			fetch:               prWatchFetch{State: "open", HeadSHA: "sha1", Mergeable: "conflicting"},
			lastCommentAt:       "2024-01-01T00:00:00Z",
			bumpedSHA:           "sha1",
			conflictState:       "clean",
			wantBump:            true,
			wantMessageContains: []string{"merge conflicts", "its base branch"},
			wantLastCommentAt:   "2024-01-01T00:00:00Z",
			wantBumpedSHA:       "sha1",
			wantConflictState:   "conflicting",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bump, msg, newLastCommentAt, newBumpedSHA, newConflictState := watchDecision("https://x/pull/1", c.fetch, c.lastCommentAt, c.bumpedSHA, c.conflictState)
			if bump != c.wantBump {
				t.Errorf("bump = %v, want %v (msg %q)", bump, c.wantBump, msg)
			}
			if newLastCommentAt != c.wantLastCommentAt {
				t.Errorf("newLastCommentAt = %q, want %q", newLastCommentAt, c.wantLastCommentAt)
			}
			if newBumpedSHA != c.wantBumpedSHA {
				t.Errorf("newBumpedSHA = %q, want %q", newBumpedSHA, c.wantBumpedSHA)
			}
			if newConflictState != c.wantConflictState {
				t.Errorf("newConflictState = %q, want %q", newConflictState, c.wantConflictState)
			}
			for _, want := range c.wantMessageContains {
				if !strings.Contains(msg, want) {
					t.Errorf("message %q missing %q", msg, want)
				}
			}
			if !c.wantBump && msg != "" {
				t.Errorf("no-bump case produced a non-empty message %q", msg)
			}
		})
	}
}

// TestLatestActivityAt covers the self-bump-loop fix: zeus' own `gh pr
// comment` replies (ViewerDidAuthor) must never advance LastCommentAt, or
// the daemon would re-bump zeus every pass on its own reply forever
// ("address it" → reply → bump → ...). Reviews are filtered by the PR's
// author instead: GitHub allows COMMENT-type reviews on your own PR (inline
// thread replies land as exactly that), so zeus-as-PR-author must not
// advance the watermark either — while an empty author fails open so an
// unknown login can never silently swallow carl's review.
func TestLatestActivityAt(t *testing.T) {
	review := func(at, login string) prActivityReview {
		r := prActivityReview{SubmittedAt: at}
		r.Author.Login = login
		return r
	}
	cases := []struct {
		name     string
		comments []prActivityComment
		reviews  []prActivityReview
		prAuthor string
		want     string
	}{
		{
			name: "viewer-authored comment is excluded from the newest timestamp",
			comments: []prActivityComment{
				{CreatedAt: "2024-01-01T00:00:00Z", ViewerDidAuthor: false},
				{CreatedAt: "2024-01-05T00:00:00Z", ViewerDidAuthor: true}, // zeus' own reply — newest by time, must not win
			},
			want: "2024-01-01T00:00:00Z",
		},
		{
			name: "all comments viewer-authored falls back to reviews",
			comments: []prActivityComment{
				{CreatedAt: "2024-01-05T00:00:00Z", ViewerDidAuthor: true},
			},
			reviews: []prActivityReview{review("2024-01-02T00:00:00Z", "car1zhang")},
			want:    "2024-01-02T00:00:00Z",
		},
		{
			name: "non-viewer comment newer than any review wins",
			comments: []prActivityComment{
				{CreatedAt: "2024-01-09T00:00:00Z", ViewerDidAuthor: false},
			},
			reviews: []prActivityReview{review("2024-01-02T00:00:00Z", "car1zhang")},
			want:    "2024-01-09T00:00:00Z",
		},
		{
			name: "PR-author-authored review is excluded (zeus' inline reply)",
			reviews: []prActivityReview{
				review("2024-01-02T00:00:00Z", "car1zhang"),
				review("2024-01-06T00:00:00Z", "carl-probook"), // zeus' thread reply — newest, must not win
			},
			prAuthor: "carl-probook",
			want:     "2024-01-02T00:00:00Z",
		},
		{
			name:     "empty PR author fails open — review still counts",
			reviews:  []prActivityReview{review("2024-01-03T00:00:00Z", "carl-probook")},
			prAuthor: "",
			want:     "2024-01-03T00:00:00Z",
		},
		{
			name: "no comments or reviews yields empty",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := latestActivityAt(c.comments, c.reviews, c.prAuthor); got != c.want {
				t.Errorf("latestActivityAt = %q, want %q", got, c.want)
			}
		})
	}
}

// TestFailingConclusions pins which CheckConclusionState values count as an
// actionable CI failure (see the var's doc comment in prpoll.go): FAILURE,
// TIMED_OUT, STARTUP_FAILURE, ACTION_REQUIRED are failing; CANCELLED,
// NEUTRAL, SKIPPED are explicitly not — a check that was cancelled or
// skipped isn't "broken", and bumping zeus for one would just be noise.
func TestFailingConclusions(t *testing.T) {
	failing := []string{"FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED"}
	for _, c := range failing {
		if !failingConclusions[c] {
			t.Errorf("%s: want counted as failing, got not counted", c)
		}
	}
	notFailing := []string{"CANCELLED", "NEUTRAL", "SKIPPED", "SUCCESS", ""}
	for _, c := range notFailing {
		if failingConclusions[c] {
			t.Errorf("%s: want NOT counted as failing, got counted", c)
		}
	}
}

// --- upstream availability poll (kickUpdateAvail) ---

// availStub replaces the checkUpdateAvail seam — nothing here fetches, and
// nothing here shells out. It records every call, signals each one on fired
// (buffered, so the stub never blocks on a test that isn't listening yet),
// and stamps CheckedAt into the store exactly the way the real
// refreshUpdateAvail does, since that stamp IS the throttle kickUpdateAvail
// reads back.
type availStub struct {
	calls atomic.Int32
	fired chan struct{}
	block chan struct{} // when non-nil, the stub waits on it before returning
	out   UpdateAvail
}

func stubAvailCheck(t *testing.T, out UpdateAvail) *availStub {
	t.Helper()
	// A tick test that ran earlier may still have the REAL check in flight —
	// it fails instantly under a sandboxed $HOME, but "instantly" isn't
	// "synchronously", and ActiveStore is process-global, so its trailing
	// write would land in this test's brand-new store and move the CheckedAt
	// this test is about to reason over. Wait it out, then reset the row to
	// never-checked; a test that wants a different starting point seeds it
	// after this returns.
	waitAvailIdle(t)
	if err := writeUpdateAvail(UpdateAvail{}); err != nil {
		t.Fatal(err)
	}
	// The stored stamp's in-memory twin has to be reset with it, or the
	// backstop would (correctly) throttle every test after the first one in
	// this process.
	lastUpdateAvailCheck.Store(0)
	t.Cleanup(func() { lastUpdateAvailCheck.Store(0) })
	s := &availStub{fired: make(chan struct{}, 8), out: out}
	prev := checkUpdateAvail
	checkUpdateAvail = func() UpdateAvail {
		s.calls.Add(1)
		s.fired <- struct{}{}
		if s.block != nil {
			<-s.block
		}
		a := s.out
		a.CheckedAt = time.Now().UnixMilli()
		_ = writeUpdateAvail(a)
		return a
	}
	t.Cleanup(func() {
		waitAvailIdle(t)
		checkUpdateAvail = prev
	})
	return s
}

// waitFired blocks until the fire-and-forget goroutine has actually called
// the seam — the alternative, sleeping, is what makes this kind of test flaky.
func (s *availStub) waitFired(t *testing.T) {
	t.Helper()
	select {
	case <-s.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("availability check never fired")
	}
}

// waitAvailIdle waits out the single-flight flag so a test can assert on a
// finished goroutine (and so the next test starts from a clean one).
func waitAvailIdle(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		if !updateAvailRunning.Load() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("availability check goroutine never finished")
}

// stubNoUsagePass keeps tick()'s first-tick-eager usage poll off the network:
// it is unrelated to what these tests assert, and a real pass would fetch
// every account's usage.
func stubNoUsagePass(t *testing.T) {
	t.Helper()
	prev := runUsagePass
	runUsagePass = func() error { return nil }
	t.Cleanup(func() { runUsagePass = prev })
}

// A store that has never been checked reads CheckedAt 0 — always due — so the
// first kick after startup fires one.
func TestKickUpdateAvailFiresWhenNeverChecked(t *testing.T) {
	useTestStore(t)
	s := stubAvailCheck(t, UpdateAvail{Behind: 2})

	kickUpdateAvail(time.Now())
	s.waitFired(t)
	waitAvailIdle(t)

	if got := ReadUpdateAvail(); got.Behind != 2 || got.CheckedAt == 0 {
		t.Fatalf("stored avail = %+v, want the check's result with a fresh CheckedAt", got)
	}
}

// Inside the interval the kick is a no-op — the whole point of the throttle,
// since every check is a real git fetch.
func TestKickUpdateAvailSkipsInsideInterval(t *testing.T) {
	useTestStore(t)
	s := stubAvailCheck(t, UpdateAvail{Behind: 9})
	now := time.Now()
	if err := writeUpdateAvail(UpdateAvail{Behind: 2, CheckedAt: now.Add(-time.Minute).UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	kickUpdateAvail(now)

	if n := s.calls.Load(); n != 0 {
		t.Fatalf("check fired %d times one minute after the last one, want 0 (interval %s)", n, updateAvailInterval)
	}
}

func TestKickUpdateAvailFiresAfterInterval(t *testing.T) {
	useTestStore(t)
	s := stubAvailCheck(t, UpdateAvail{Behind: 9})
	now := time.Now()
	if err := writeUpdateAvail(UpdateAvail{Behind: 2, CheckedAt: now.Add(-updateAvailInterval - time.Second).UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	kickUpdateAvail(now)
	s.waitFired(t)
	waitAvailIdle(t)

	if got := ReadUpdateAvail(); got.Behind != 9 {
		t.Fatalf("stored avail = %+v, want the fresh check's Behind 9", got)
	}
}

// A store that never records anything — no store wired, a failing kv write —
// makes ReadUpdateAvail read zero (always due) forever, which without the
// in-memory backstop would turn the ten-minute poll into a `git fetch` on
// every 15s tick.
func TestKickUpdateAvailBackstopsAStoreThatNeverRecords(t *testing.T) {
	useTestStore(t)
	s := stubAvailCheck(t, UpdateAvail{Behind: 2})
	// Swap the seam again, this time for one that records NOTHING — the
	// shape of a store miss, seen from the throttle's side.
	prev := checkUpdateAvail
	checkUpdateAvail = func() UpdateAvail {
		s.calls.Add(1)
		s.fired <- struct{}{}
		return UpdateAvail{Behind: 2}
	}
	t.Cleanup(func() { checkUpdateAvail = prev })

	now := time.Now()
	kickUpdateAvail(now)
	s.waitFired(t)
	waitAvailIdle(t)
	kickUpdateAvail(now.Add(time.Minute))
	kickUpdateAvail(now.Add(2 * time.Minute))

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("check fired %d times with an unrecorded CheckedAt, want 1 — the in-memory stamp is the only throttle left", n)
	}
}

// Two due kicks with the first still in flight must produce ONE fetch: the
// stored CheckedAt hasn't moved yet, so the interval gate lets the second one
// through and updateAvailRunning is what has to stop it.
func TestKickUpdateAvailSingleFlightsConcurrentCalls(t *testing.T) {
	useTestStore(t)
	s := stubAvailCheck(t, UpdateAvail{Behind: 2})
	s.block = make(chan struct{})

	now := time.Now()
	kickUpdateAvail(now)
	s.waitFired(t) // the first check is now inside the stub, holding the flag
	kickUpdateAvail(now)
	close(s.block)
	waitAvailIdle(t)

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("check fired %d times with one already in flight, want exactly 1", n)
	}
}

// The tick's own wiring: a normal (non-mirror) tick fires the check.
func TestTickFiresUpdateAvailCheck(t *testing.T) {
	useTestStore(t)
	stubNoUsagePass(t)
	s := stubAvailCheck(t, UpdateAvail{Behind: 3})

	d := newDaemon()
	if _, err := d.tick(false); err != nil {
		t.Fatalf("tick: %v", err)
	}
	s.waitFired(t)
	waitAvailIdle(t)

	if got := ReadUpdateAvail(); got.Behind != 3 {
		t.Fatalf("stored avail = %+v, want the tick's check result", got)
	}
}

// A mirror tick must never run it: the check fetches in the REAL kraken
// checkout and writes shared state, both off-limits to a read-only scheduler
// variant.
func TestTickMirrorSkipsUpdateAvailCheck(t *testing.T) {
	useTestStore(t)
	s := stubAvailCheck(t, UpdateAvail{Behind: 3})

	d := newDaemon()
	if _, err := d.tick(true); err != nil {
		t.Fatalf("mirror tick: %v", err)
	}

	if n := s.calls.Load(); n != 0 {
		t.Fatalf("availability check fired %d times under --mirror, want 0", n)
	}
	if got := ReadUpdateAvail(); got != (UpdateAvail{}) {
		t.Fatalf("stored avail = %+v after a mirror tick, want it untouched", got)
	}
}

// A failed check is a debug log line and nothing else: never a tick error,
// never a board error.
func TestTickIgnoresFailedUpdateAvailCheck(t *testing.T) {
	useTestStore(t)
	stubNoUsagePass(t)
	s := stubAvailCheck(t, UpdateAvail{Error: "network is down"})

	d := newDaemon()
	cells, err := d.tick(false)
	if err != nil {
		t.Fatalf("tick returned %v, want nil — a failed availability check must never error the tick", err)
	}
	if cells != 0 {
		t.Fatalf("cells = %d, want 0", cells)
	}
	s.waitFired(t)
	waitAvailIdle(t)
}
