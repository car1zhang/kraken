package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitStub scripts updateRunGit off the joined args ("status --porcelain"),
// so a preflight case is expressed as the git output it would have seen. A
// value prefixed with "!" is returned as an error instead — an unstubbed
// command is always an error, so a test can never silently pass because the
// code asked git something the fixture didn't anticipate.
type gitStub map[string]string

func (g gitStub) run(_ context.Context, _ string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	out, ok := g[key]
	if !ok {
		return "", fmt.Errorf("unstubbed git %s", key)
	}
	if strings.HasPrefix(out, "!") {
		return "", errors.New(out[1:])
	}
	return out, nil
}

func stubGit(t *testing.T, fn func(ctx context.Context, dir string, args ...string) (string, error)) {
	t.Helper()
	prev := updateRunGit
	updateRunGit = fn
	t.Cleanup(func() { updateRunGit = prev })
}

func stubLookPath(t *testing.T, fn func(string) (string, error)) {
	t.Helper()
	prev := updateLookPath
	updateLookPath = fn
	t.Cleanup(func() { updateLookPath = prev })
}

func stubToolDirs(t *testing.T, dirs ...string) {
	t.Helper()
	prev := updateToolDirs
	updateToolDirs = dirs
	t.Cleanup(func() { updateToolDirs = prev })
}

// fakeKrakenRepo makes ResolveRepo("kraken") resolve inside the sandboxed
// $HOME — a .git entry is the whole of isGitRepo's test, so no real checkout
// (and no git) is involved.
func fakeKrakenRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(os.Getenv("HOME"), "Developer", "kraken")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repo
}

// --- phases + status file ---

func TestUpdateTerminal(t *testing.T) {
	terminal := []string{UpdateIdle, UpdateDone, UpdateFailed, UpdateUpToDate}
	inFlight := []string{UpdatePreflight, UpdateFetching, UpdateMerging, UpdateBuilding, UpdateInstalling, UpdateRestarting}
	for _, p := range terminal {
		if !UpdateTerminal(p) {
			t.Errorf("UpdateTerminal(%q) = false, want true", p)
		}
	}
	for _, p := range inFlight {
		if UpdateTerminal(p) {
			t.Errorf("UpdateTerminal(%q) = true, want false", p)
		}
	}
}

func TestUpdateStatusJSONKeys(t *testing.T) {
	data, err := json.Marshal(UpdateStatus{
		Phase: UpdateBuilding, StartedAt: 1, UpdatedAt: 2, PID: 3,
		FromSHA: "aaa", ToSHA: "bbb", Behind: 4, Error: "boom", ElapsedMS: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	// The wire keys are read by the web UI and the iOS-facing board payload;
	// a rename here is a breaking change, so pin every one of them.
	for _, k := range []string{"phase", "started_at", "updated_at", "pid", "from_sha", "to_sha", "behind", "error", "elapsed_ms"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("marshalled UpdateStatus missing key %q: %s", k, data)
		}
	}
	var back UpdateStatus
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Phase != UpdateBuilding || back.PID != 3 || back.ElapsedMS != 5 || back.Error != "boom" {
		t.Fatalf("round trip lost fields: %+v", back)
	}
}

func TestReadUpdateStatusMissingFileIsIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if st := ReadUpdateStatus(); st.Phase != UpdateIdle {
		t.Fatalf("no update.json: phase = %q, want %q", st.Phase, UpdateIdle)
	}
}

func TestReadUpdateStatusMalformedIsIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(UpdateStatusPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UpdateStatusPath(), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := ReadUpdateStatus(); st.Phase != UpdateIdle {
		t.Fatalf("malformed update.json: phase = %q, want %q (defensive read, never an error)", st.Phase, UpdateIdle)
	}
}

func TestWriteThenReadUpdateStatusRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	started := time.Now().UnixMilli() - 4_000
	// Our own pid keeps staleUpdate's liveness rule satisfied, so the read
	// exercises the write/parse path rather than the downgrade path.
	in := UpdateStatus{Phase: UpdateBuilding, StartedAt: started, PID: os.Getpid(), FromSHA: "abc1234", Behind: 2}
	if err := WriteUpdateStatus(in); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := ReadUpdateStatus()
	if got.Phase != UpdateBuilding || got.FromSHA != "abc1234" || got.Behind != 2 || got.PID != os.Getpid() {
		t.Fatalf("round trip: %+v", got)
	}
	if got.UpdatedAt < started {
		t.Fatalf("UpdatedAt = %d, want a fresh stamp (>= %d)", got.UpdatedAt, started)
	}
	if got.ElapsedMS < 4_000 {
		t.Fatalf("ElapsedMS = %d, want >= 4000 (derived from StartedAt)", got.ElapsedMS)
	}
}

func TestWriteUpdateStatusLeavesElapsedZeroWithoutStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WriteUpdateStatus(UpdateStatus{Phase: UpdateIdle}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadUpdateStatus(); got.ElapsedMS != 0 {
		t.Fatalf("ElapsedMS = %d with StartedAt 0, want 0", got.ElapsedMS)
	}
}

// --- staleness ---

func TestStaleUpdate(t *testing.T) {
	const now = 1_000_000_000

	cases := []struct {
		name      string
		st        UpdateStatus
		alive     bool
		wantPhase string
		wantErr   string
	}{
		{"idle unchanged", UpdateStatus{Phase: UpdateIdle}, false, UpdateIdle, ""},
		{"done unchanged", UpdateStatus{Phase: UpdateDone, PID: 424242}, false, UpdateDone, ""},
		{"failed unchanged", UpdateStatus{Phase: UpdateFailed, Error: "original", PID: 424242}, false, UpdateFailed, "original"},
		{"uptodate unchanged", UpdateStatus{Phase: UpdateUpToDate, PID: 424242}, false, UpdateUpToDate, ""},
		{
			"pid 0 inside grace stays in flight",
			UpdateStatus{Phase: UpdatePreflight, StartedAt: now - updateClaimGraceMS + 1},
			false, UpdatePreflight, "",
		},
		{
			"pid 0 at the grace boundary stays in flight",
			UpdateStatus{Phase: UpdatePreflight, StartedAt: now - updateClaimGraceMS},
			false, UpdatePreflight, "",
		},
		{
			"pid 0 past grace fails",
			UpdateStatus{Phase: UpdatePreflight, StartedAt: now - updateClaimGraceMS - 1},
			false, UpdateFailed, "updater never started",
		},
		{
			"live pid unchanged",
			UpdateStatus{Phase: UpdateBuilding, StartedAt: now - 600_000, PID: 4321},
			true, UpdateBuilding, "",
		},
		{
			"dead pid fails, naming the pid",
			UpdateStatus{Phase: UpdateBuilding, StartedAt: now - 600_000, PID: 4321},
			false, UpdateFailed, "updater exited without finishing (pid 4321)",
		},
		// The max-run deadline: liveness alone can't see a process wedged on a
		// codesign auth dialog, or a pid the OS recycled out from under us.
		{
			"live pid just inside the deadline stays in flight",
			UpdateStatus{Phase: UpdateInstalling, StartedAt: now - 3_600_000, UpdatedAt: now - updateMaxRun.Milliseconds(), PID: 4321},
			true, UpdateInstalling, "",
		},
		{
			"live pid past the deadline fails",
			UpdateStatus{Phase: UpdateInstalling, StartedAt: now - 3_600_000, UpdatedAt: now - updateMaxRun.Milliseconds() - 1, PID: 4321},
			true, UpdateFailed,
			fmt.Sprintf("updater stalled in %s for over %s (pid 4321) — see ~/.kraken/update.log", UpdateInstalling, updateMaxRun),
		},
		{
			// UpdatedAt, not StartedAt: a long run that is still writing
			// phases is healthy however long ago it began.
			"long run still reporting progress stays in flight",
			UpdateStatus{Phase: UpdateBuilding, StartedAt: now - 10*updateMaxRun.Milliseconds(), UpdatedAt: now - 1_000, PID: 4321},
			true, UpdateBuilding, "",
		},
		{
			// No stamp at all (a hand-written or pre-UpdatedAt record) leaves
			// nothing to judge by, so the deadline abstains rather than guesses.
			"live pid with no timestamps at all is left alone",
			UpdateStatus{Phase: UpdateBuilding, PID: 4321},
			true, UpdateBuilding, "",
		},
		{
			"terminal record older than the deadline is still left alone",
			UpdateStatus{Phase: UpdateDone, UpdatedAt: now - 10*updateMaxRun.Milliseconds(), PID: 4321},
			true, UpdateDone, "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staleUpdate(tc.st, tc.alive, now)
			if got.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q", got.Phase, tc.wantPhase)
			}
			if got.Error != tc.wantErr {
				t.Errorf("error = %q, want %q", got.Error, tc.wantErr)
			}
		})
	}
}

func TestReadUpdateStatusDowngradesDeadUpdater(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := updateProcessAlive
	updateProcessAlive = func(int) bool { return false }
	t.Cleanup(func() { updateProcessAlive = prev })

	if err := WriteUpdateStatus(UpdateStatus{Phase: UpdateBuilding, StartedAt: time.Now().UnixMilli(), PID: 4321}); err != nil {
		t.Fatal(err)
	}
	got := ReadUpdateStatus()
	if got.Phase != UpdateFailed || !strings.Contains(got.Error, "4321") {
		t.Fatalf("dead updater reads as %+v, want failed naming pid 4321", got)
	}
}

// The wedged-updater path end to end: a record whose pid is genuinely alive
// (this test process') but hasn't moved in longer than updateMaxRun must read
// as failed, so the UPDATE button unblocks instead of 409ing forever.
func TestReadUpdateStatusFailsAStalledUpdater(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stalled := time.Now().Add(-updateMaxRun - time.Minute).UnixMilli()
	// Written straight to the file: WriteUpdateStatus would stamp UpdatedAt
	// with now, which is exactly the field under test.
	if err := os.MkdirAll(filepath.Dir(UpdateStatusPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(UpdateStatus{Phase: UpdateInstalling, StartedAt: stalled, UpdatedAt: stalled, PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UpdateStatusPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadUpdateStatus()
	if !UpdateTerminal(got.Phase) || got.Phase != UpdateFailed {
		t.Fatalf("stalled updater reads as %+v, want a terminal %q so the button unblocks", got, UpdateFailed)
	}
	if !strings.Contains(got.Error, "stalled") {
		t.Fatalf("error = %q, want it to say the run stalled", got.Error)
	}
}

// --- lock ---

func TestAcquireUpdateLockSecondAcquireFailsWhileHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	release, ok, err := AcquireUpdateLock()
	if !ok || err != nil {
		t.Fatalf("first acquire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	defer release()

	_, ok2, err2 := AcquireUpdateLock()
	if ok2 || err2 != nil {
		t.Fatalf("second acquire while held: ok=%v err=%v, want ok=false err=nil (contention, not an error)", ok2, err2)
	}
}

func TestAcquireUpdateLockSucceedsAfterRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	release, ok, err := AcquireUpdateLock()
	if !ok || err != nil {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	release()

	release2, ok2, err2 := AcquireUpdateLock()
	if !ok2 || err2 != nil {
		t.Fatalf("acquire after release: ok=%v err=%v, want ok=true err=nil", ok2, err2)
	}
	release2()
}

// The readback the contention message depends on: whoever lost the flock has
// to be able to name the process still holding it.
func TestUpdateLockHolderPIDNamesTheHolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	release, ok, err := AcquireUpdateLock()
	if !ok || err != nil {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	defer release()

	if got := UpdateLockHolderPID(); got != os.Getpid() {
		t.Fatalf("UpdateLockHolderPID() = %d, want this process' %d", got, os.Getpid())
	}
}

// Every way the lock file can fail to name a pid reads as 0 — "nothing to
// offer" — because this only decorates a message that is already an error.
func TestUpdateLockHolderPIDDegradesToZero(t *testing.T) {
	cases := []struct {
		name     string
		contents string // "" = write no file at all
		write    bool
	}{
		{"no lock file", "", false},
		{"empty file", "", true},
		{"garbage", "who knows", true},
		{"unparseable pid", "pid=nonsense at=123", true},
		{"zero pid", "pid=0 at=123", true},
		{"negative pid", "pid=-1 at=123", true},
		{"truncated write", "pid=", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if tc.write {
				if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(updateLockPath(), []byte(tc.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := UpdateLockHolderPID(); got != 0 {
				t.Fatalf("UpdateLockHolderPID() = %d, want 0", got)
			}
		})
	}
}

func TestAcquireUpdateLockCreateFailureReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// ~/.kraken as a plain FILE makes MkdirAll fail — the "couldn't even
	// create the lock" outcome, kept distinct from "held elsewhere".
	if err := os.WriteFile(filepath.Join(home, ".kraken"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := AcquireUpdateLock()
	if ok || err == nil {
		t.Fatalf("acquire with ~/.kraken as a plain file: ok=%v err=%v, want ok=false err!=nil", ok, err)
	}
}

// --- preflight ---

func TestPreflightUpdate(t *testing.T) {
	cleanGit := gitStub{
		"rev-parse --abbrev-ref HEAD": "main",
		"status --porcelain":          "",
		"rev-parse --short HEAD":      "abc1234",
	}
	allTools := func(name string) (string, error) { return "/stub/" + name, nil }

	cases := []struct {
		name    string
		git     gitStub
		look    func(string) (string, error)
		wantErr string // "" = expect success
	}{
		{"clean main", cleanGit, allTools, ""},
		{
			"wrong branch",
			gitStub{"rev-parse --abbrev-ref HEAD": "carl/update-button"},
			allTools,
			"REPO is on carl/update-button, not main",
		},
		{
			"dirty tree",
			gitStub{"rev-parse --abbrev-ref HEAD": "main", "status --porcelain": " M internal/jobs/update.go"},
			allTools,
			"REPO has uncommitted changes — commit or clean it first",
		},
		{
			"no go",
			cleanGit,
			func(string) (string, error) { return "", exec.ErrNotFound },
			"go not found — checked ",
		},
		{
			"no make",
			cleanGit,
			func(name string) (string, error) {
				if name == "go" {
					return "/stub/go", nil
				}
				return "", exec.ErrNotFound
			},
			"make not found — checked ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			repo := fakeKrakenRepo(t)
			stubGit(t, tc.git.run)
			stubLookPath(t, tc.look)
			// An empty candidate dir makes the toolchain misses
			// deterministic — the real /usr/local/go/bin exists on this
			// machine and would otherwise satisfy the lookup.
			stubToolDirs(t, filepath.Join(t.TempDir(), "empty"))

			pf, err := PreflightUpdate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("preflight: %v, want success", err)
				}
				if pf.Repo != repo || pf.Branch != "main" || pf.HeadSHA != "abc1234" || pf.GoPath != "/stub/go" || pf.MakePath != "/stub/make" {
					t.Fatalf("preflight = %+v, want every field populated (repo %s)", pf, repo)
				}
				return
			}
			want := strings.ReplaceAll(tc.wantErr, "REPO", repo)
			if err == nil {
				t.Fatalf("preflight succeeded, want error %q", want)
			}
			if !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("preflight error = %q, want prefix %q", err.Error(), want)
			}
		})
	}
}

func TestPreflightUpdateNoRepoPropagatesResolverError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), "Developer"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubGit(t, gitStub{}.run)

	if _, err := PreflightUpdate(); err == nil || !strings.Contains(err.Error(), "kraken") {
		t.Fatalf("preflight with no kraken checkout: err = %v, want the resolver's own error", err)
	}
}

// --- toolchain resolution ---

func TestLookToolchainFoundOnPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stubLookPath(t, func(name string) (string, error) { return "/somewhere/" + name, nil })

	got, err := lookToolchain("go")
	if err != nil || got != "/somewhere/go" {
		t.Fatalf("lookToolchain = %q, %v; want /somewhere/go, nil", got, err)
	}
}

func TestLookToolchainFoundInCandidateDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	tool := filepath.Join(dir, "make")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stubLookPath(t, func(string) (string, error) { return "", exec.ErrNotFound })
	stubToolDirs(t, filepath.Join(t.TempDir(), "empty"), dir)

	got, err := lookToolchain("make")
	if err != nil || got != tool {
		t.Fatalf("lookToolchain = %q, %v; want %s, nil", got, err, tool)
	}
}

func TestLookToolchainAbsentNamesEveryCandidate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	stubLookPath(t, func(string) (string, error) { return "", exec.ErrNotFound })
	// Deliberately NOT stubbing updateToolDirs: the real default list is
	// what the message must enumerate, and no dir on any machine holds a
	// binary by this name.
	_, err := lookToolchain("kraken-no-such-tool")
	if err == nil {
		t.Fatal("lookToolchain for a nonexistent tool succeeded")
	}
	for _, dir := range append(append([]string{}, updateToolDirs...), filepath.Join(home, "go", "bin")) {
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("error %q does not name candidate dir %s", err.Error(), dir)
		}
	}
}

// --- availability ---

func TestReadUpdateAvailNoStoreIsZero(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := ReadUpdateAvail(); got != (UpdateAvail{}) {
		t.Fatalf("ReadUpdateAvail with no store = %+v, want zero value", got)
	}
	if err := writeUpdateAvail(UpdateAvail{Behind: 3}); err != nil {
		t.Fatalf("writeUpdateAvail with no store: %v, want nil (no-op, never an error)", err)
	}
}

func TestUpdateAvailStoreRoundTrip(t *testing.T) {
	useTestStore(t)
	want := UpdateAvail{Behind: 7, LocalSHA: "aaa1111", RemoteSHA: "bbb2222", CheckedAt: 1234}
	if err := writeUpdateAvail(want); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := ReadUpdateAvail(); got != want {
		t.Fatalf("ReadUpdateAvail = %+v, want %+v", got, want)
	}
}

func TestReadUpdateAvailMalformedIsZero(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV(updateAvailKVKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	if got := ReadUpdateAvail(); got != (UpdateAvail{}) {
		t.Fatalf("malformed kv value = %+v, want zero value", got)
	}
}

func TestRefreshUpdateAvailCountsBehind(t *testing.T) {
	useTestStore(t)
	fakeKrakenRepo(t)
	stubGit(t, gitStub{
		"fetch origin main":                  "",
		"rev-list --count HEAD..origin/main": "3",
		"rev-parse --short HEAD":             "aaa1111",
		"rev-parse --short origin/main":      "bbb2222",
	}.run)

	got := refreshUpdateAvail()
	if got.Behind != 3 || got.LocalSHA != "aaa1111" || got.RemoteSHA != "bbb2222" || got.Error != "" {
		t.Fatalf("refreshUpdateAvail = %+v, want behind 3 with both shas and no error", got)
	}
	if got.CheckedAt == 0 {
		t.Fatal("CheckedAt not stamped")
	}
	if stored := ReadUpdateAvail(); stored != got {
		t.Fatalf("stored %+v != returned %+v", stored, got)
	}
}

func TestRefreshUpdateAvailFailuresAreNeverFatal(t *testing.T) {
	cases := []struct {
		name string
		repo bool
		git  gitStub
	}{
		{"no repo", false, gitStub{}},
		{"fetch fails", true, gitStub{"fetch origin main": "!network is down"}},
		{"rev-list fails", true, gitStub{"fetch origin main": "", "rev-list --count HEAD..origin/main": "!bad revision"}},
		{"unparseable count", true, gitStub{"fetch origin main": "", "rev-list --count HEAD..origin/main": "not a number"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useTestStore(t)
			if tc.repo {
				fakeKrakenRepo(t)
			}
			stubGit(t, tc.git.run)

			got := refreshUpdateAvail()
			if got.Behind != 0 || got.Error == "" {
				t.Fatalf("refreshUpdateAvail = %+v, want zero Behind carrying an Error", got)
			}
		})
	}
}

// A failed check must keep the last known-good count instead of zeroing it:
// the check re-runs only every updateAvailInterval, so one blip offline would
// otherwise blank the board's "2 behind" badge for ten full minutes and then
// bring it back — a stale-but-true count beats a flapping badge.
func TestRefreshUpdateAvailFailurePreservesLastGoodCount(t *testing.T) {
	useTestStore(t)
	fakeKrakenRepo(t)
	seed := UpdateAvail{Behind: 2, LocalSHA: "aaa1111", RemoteSHA: "bbb2222", CheckedAt: 1234}
	if err := writeUpdateAvail(seed); err != nil {
		t.Fatal(err)
	}
	stubGit(t, gitStub{"fetch origin main": "!network is down"}.run)

	got := refreshUpdateAvail()
	if got.Behind != seed.Behind || got.LocalSHA != seed.LocalSHA || got.RemoteSHA != seed.RemoteSHA {
		t.Fatalf("refreshUpdateAvail after a failed fetch = %+v, want the previous count/shas preserved (%+v)", got, seed)
	}
	if !strings.Contains(got.Error, "network is down") {
		t.Fatalf("error = %q, want it to carry the fetch failure", got.Error)
	}
	if got.CheckedAt == seed.CheckedAt {
		t.Fatalf("CheckedAt = %d, want it refreshed even on a failure (else the check retries every tick)", got.CheckedAt)
	}
	if stored := ReadUpdateAvail(); stored != got {
		t.Fatalf("stored %+v != returned %+v", stored, got)
	}
}

// The mirror image of the case above: a check that succeeds again overwrites
// everything and clears the error, so a recovered network doesn't leave a
// permanent complaint attached to a perfectly fresh count.
func TestRefreshUpdateAvailSuccessClearsPriorError(t *testing.T) {
	useTestStore(t)
	fakeKrakenRepo(t)
	if err := writeUpdateAvail(UpdateAvail{Behind: 2, LocalSHA: "old1111", Error: "network is down"}); err != nil {
		t.Fatal(err)
	}
	stubGit(t, gitStub{
		"fetch origin main":                  "",
		"rev-list --count HEAD..origin/main": "5",
		"rev-parse --short HEAD":             "new1111",
		"rev-parse --short origin/main":      "new2222",
	}.run)

	got := refreshUpdateAvail()
	if got.Behind != 5 || got.LocalSHA != "new1111" || got.RemoteSHA != "new2222" || got.Error != "" {
		t.Fatalf("refreshUpdateAvail = %+v, want behind 5 with fresh shas and a cleared error", got)
	}
}

// The availability check's git calls must all carry a deadline: this runs in
// kickUpdateAvail's fire-and-forget goroutine, whose return is the only thing
// that ever re-arms the check, so one fetch that never comes back retires the
// "N BEHIND" badge for the daemon's whole life.
func TestRefreshUpdateAvailBoundsEveryGitCall(t *testing.T) {
	useTestStore(t)
	fakeKrakenRepo(t)
	replies := gitStub{
		"fetch origin main":                  "",
		"rev-list --count HEAD..origin/main": "3",
		"rev-parse --short HEAD":             "aaa1111",
		"rev-parse --short origin/main":      "bbb2222",
	}
	type seen struct {
		args     string
		deadline time.Time
		ok       bool
	}
	var calls []seen
	stubGit(t, func(ctx context.Context, dir string, args ...string) (string, error) {
		d, ok := ctx.Deadline()
		calls = append(calls, seen{strings.Join(args, " "), d, ok})
		return replies.run(ctx, dir, args...)
	})

	start := time.Now()
	refreshUpdateAvail()

	if len(calls) != len(replies) {
		t.Fatalf("saw %d git calls, want %d", len(calls), len(replies))
	}
	for _, c := range calls {
		if !c.ok {
			t.Fatalf("git %s ran with an unbounded context", c.args)
		}
		if budget := c.deadline.Sub(start); budget <= 0 || budget > updateAvailTimeout+time.Second {
			t.Fatalf("git %s deadline is %s out, want ~%s", c.args, budget, updateAvailTimeout)
		}
	}
}

// The other half of the seam's contract: preflight's git is all local and
// instant, and bounding it would only invent a way for a good update to be
// refused.
func TestPreflightUpdateGitIsUnbounded(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fakeKrakenRepo(t)
	stubLookPath(t, func(name string) (string, error) { return "/usr/bin/" + name, nil })
	replies := gitStub{
		"rev-parse --abbrev-ref HEAD": "main",
		"status --porcelain":          "",
		"rev-parse --short HEAD":      "aaa1111",
	}
	stubGit(t, func(ctx context.Context, dir string, args ...string) (string, error) {
		if _, ok := ctx.Deadline(); ok {
			t.Errorf("git %s ran under a deadline", strings.Join(args, " "))
		}
		return replies.run(ctx, dir, args...)
	})

	if _, err := PreflightUpdate(); err != nil {
		t.Fatalf("PreflightUpdate: %v", err)
	}
}
