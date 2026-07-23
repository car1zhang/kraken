package jobs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kraken/store"
	"kraken/util"
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

// TestDaemonGuardErr covers the single-instance startup guard shared by
// RunDaemon and kraken serve's GuardServeStart: refuses only when
// daemon.json names a DIFFERENT, currently-alive pid.
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

// TestGuardFileModeStart covers E2: `kraken daemon`/`k web` must refuse to
// start file-mode against a machine that has already migrated onto the
// sqlite store (kraken.db exists on disk), unless --force-file-mode. Uses a
// temp HOME (not KRAKEN_DB_PATH) so this exercises store.DBPath()'s real
// default resolution, matching how a genuine developer machine would look.
func TestGuardFileModeStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, ".kraken", "kraken.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}

	// no db file yet: file mode is fine
	if err := GuardFileModeStart(false); err != nil {
		t.Fatalf("no kraken.db: want nil, got %v", err)
	}

	// db file now exists (e.g. `kraken import` ran on this machine, or
	// kraken serve created it) but this process never wired a store
	if err := os.WriteFile(dbPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := GuardFileModeStart(false)
	if err == nil {
		t.Fatal("kraken.db exists, no store wired: want a refusal, got nil")
	}
	if !strings.Contains(err.Error(), "kraken serve") {
		t.Fatalf("error = %q, want it to name kraken serve", err.Error())
	}

	// the force override bypasses the refusal
	if err := GuardFileModeStart(true); err != nil {
		t.Fatalf("forced: want nil, got %v", err)
	}

	// a process that DID wire a store (ActiveStore() != nil) is exempt too,
	// db-exists or not
	useTestStore(t)
	if err := GuardFileModeStart(false); err != nil {
		t.Fatalf("store wired: want nil, got %v", err)
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
		Slug: "already-blocked", Type: "info", Repo: "fake-repo",
		Ready: true, State: "blocked",
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
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	stale := writeArchiveFixture(t, PlutoDir(), "stale.job.md", "job: stale\nrepo: x\nready: true\nstate: done\n")
	old := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	writeArchiveFixture(t, PlutoDir(), "fresh.job.md", "job: fresh\nrepo: x\nready: true\nstate: done\n")

	jobs := LoadJobs()
	archived := autoArchivePass(jobs, map[string]bool{}, time.Now())

	if len(archived) != 1 || archived[0] != "stale" {
		t.Fatalf("archived: got %v, want [stale]", archived)
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "stale.job.md")); err != nil {
		t.Fatal("stale job didn't land in archive/")
	}
	if _, err := os.Stat(filepath.Join(PlutoDir(), "fresh.job.md")); err != nil {
		t.Fatal("fresh done job was archived too early")
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
			name: "working build job",
			job:  &Job{Type: "build", State: "working"},
			want: true,
		},
		{
			name: "blocked build job",
			job:  &Job{Type: "build", State: "blocked"},
			want: true,
		},
		{
			name: "done build job stays tracked",
			job:  &Job{Type: "build", State: "done"},
			want: true,
		},
		{
			name: "never-started draft with no PR history",
			job:  &Job{Type: "build", State: ""},
			want: false,
		},
		{
			name: "killed back to draft keeps its PR history tracked",
			job:  &Job{Type: "build", State: "", PRs: []JobPR{{URL: "https://x/pull/1", State: "merged"}}},
			want: true,
		},
		{
			name: "info job never tracked",
			job:  &Job{Type: "info", State: "working"},
			want: false,
		},
		{
			name: "done info job with PRs still gated by type",
			job:  &Job{Type: "info", State: "done", PRs: []JobPR{{URL: "https://x/pull/9", State: "open"}}},
			want: false,
		},
		{
			name: "re-queued killed job keeps tracking",
			job:  &Job{Type: "build", State: "queued", PRs: []JobPR{{URL: "https://x/pull/2", State: "open"}}},
			want: true,
		},
		{
			name: "unparseable job never tracked",
			job:  &Job{Type: "build", State: "working", Err: "duplicate slug"},
			want: false,
		},
	}
	for _, c := range cases {
		if got := prTrackable(c.job); got != c.want {
			t.Errorf("%s: prTrackable = %v, want %v", c.name, got, c.want)
		}
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
func TestBranchFamily(t *testing.T) {
	fam1 := branchFamily(&Job{Slug: "slug", Run: 1})
	if len(fam1) != 1 || !fam1["carl/slug"] {
		t.Fatalf("run 1: got %v, want just carl/slug", fam1)
	}

	fam3 := branchFamily(&Job{Slug: "slug", Run: 3})
	want := []string{"carl/slug", "carl/slug-r2", "carl/slug-r3"}
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
	// giving exactly base (r=1) + r2..r64 = 64 entries, and the whole call
	// must return quickly.
	start := time.Now()
	famHuge := branchFamily(&Job{Slug: "slug", Run: 2000000000})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("run 2000000000: took %v, want fast (cap should bound the loop)", elapsed)
	}
	if len(famHuge) != 64 {
		t.Fatalf("run 2000000000: got %d entries, want exactly 64 (cap applied)", len(famHuge))
	}
}

// --- mirror mode (kraken serve --mirror, ServeOpts.Mirror) ---

// TestTickMirrorNeverStartsACell covers the mirror tick's core contract: a
// startable (ready, never-started) job sits in the sandbox DB exactly like
// it would for the real scheduler, but d.tick(true) must never queue it —
// no StartCell call, so no cell dir/worktree/tmux session ever gets
// created, and the job's own state stays untouched (still not started).
func TestTickMirrorNeverStartsACell(t *testing.T) {
	st := useTestStore(t)
	sj := mustCreateJob(t, st, store.Job{
		Slug: "mirror-queued", Type: "info", Repo: "fake-repo", Ready: true,
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
		t.Fatalf("job state = %q, err %v — want it to remain queued (empty state)", fresh.State, err)
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
		Slug: "mirror-old-done", Type: "info", Repo: "fake-repo", State: "done",
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
