package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
)

// cmd_update_test.go — the whole update sequence driven through cmd_update.go's
// seams: no subprocess, no repo, no launchd, no real ~/.kraken. Every test
// sandboxes HOME, so the only status file any of this touches is a temp one.
// jobs/update_test.go already covers the pieces underneath (preflight's
// refusals, the flock, staleness); what is tested here is the ORDER things
// happen in and the branches that decide whether anything gets built at all.

// --- fakes ---

// fakeGit answers `git -C <dir> <args...>` from a table keyed on the joined
// args, recording every call so a test can assert both what ran and — just as
// importantly — what didn't (see TestRunUpdateNeverPullsOrStashes).
type fakeGit struct {
	calls   []string
	replies map[string]string
	errs    map[string]error
	// errsOnce fires for the FIRST call of a key and then clears itself — the
	// shape of the gh-account rescue, where a fetch fails, the account is
	// switched, and the identical command then succeeds.
	errsOnce map[string]error
}

func (g *fakeGit) run(dir string, args ...string) (string, error) {
	key := strings.Join(args, " ")
	g.calls = append(g.calls, key)
	if err, ok := g.errsOnce[key]; ok {
		delete(g.errsOnce, key)
		return "", err
	}
	if err, ok := g.errs[key]; ok {
		return "", err
	}
	return g.replies[key], nil
}

func (g *fakeGit) count(key string) int {
	n := 0
	for _, c := range g.calls {
		if c == key {
			n++
		}
	}
	return n
}

func (g *fakeGit) ran(key string) bool {
	for _, c := range g.calls {
		if c == key {
			return true
		}
	}
	return false
}

// fakeExec stands in for `make build`, `make install`, and `go test ./...`.
// It keeps the env of the last call so a test can assert the build tools were
// handed a PATH that leads to go (see TestRunUpdateBuildsWithGoOnPath).
type fakeExec struct {
	calls   []string
	lastEnv []string
	errs    map[string]error
}

func (e *fakeExec) run(dir string, env []string, name string, args ...string) error {
	key := name + " " + strings.Join(args, " ")
	e.calls = append(e.calls, key)
	e.lastEnv = env
	return e.errs[key]
}

func (e *fakeExec) ranAll(keys ...string) bool {
	for _, k := range keys {
		found := false
		for _, c := range e.calls {
			if c == k {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// updateHarness swaps every seam in cmd_update.go and restores them after the
// test. The status writer delegates to the real jobs.WriteUpdateStatus (HOME
// is sandboxed, so that lands in a temp dir) while recording the phase
// sequence, which the file itself can't show — it only ever holds the last one.
type updateHarness struct {
	git        *fakeGit
	exec       *fakeExec
	phases     []string
	lockTaken  int
	lockOK     bool
	lockErr    error
	preflight  jobs.UpdatePreflightInfo
	preflightE error
	// The gh-account rescue seam. It refuses by default, which is what
	// jobs.RecoverGHAccount does for every failure that isn't credential-
	// shaped — so a test that doesn't mention gh sees the plain behaviour.
	ghAccount  string
	ghErr      error
	ghFailures []string // the git error text handed to each rescue attempt
}

func newUpdateHarness(t *testing.T) *updateHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	h := &updateHarness{
		git:    &fakeGit{replies: map[string]string{}, errs: map[string]error{}, errsOnce: map[string]error{}},
		exec:   &fakeExec{errs: map[string]error{}},
		lockOK: true,
		ghErr:  errors.New("git failure does not look like a gh account mismatch"),
		preflight: jobs.UpdatePreflightInfo{
			Repo: filepath.Join(home, "Developer", "kraken"), Branch: "main",
			HeadSHA: "aaa1111", GoPath: "/usr/local/go/bin/go", MakePath: "/usr/bin/make",
		},
	}
	// The default remote is 3 commits ahead and a clean fast-forward; each
	// test overrides only the answer it cares about.
	h.git.replies["rev-list --count HEAD..origin/main"] = "3"
	h.git.replies["rev-parse --short origin/main"] = "bbb2222"

	origGit, origExec := updateGit, updateExec
	origPre, origLock, origWrite, origSleep := updatePreflight, updateAcquireLock, updateWriteStatus, updateSleep
	origGH := updateGHRecover
	updateGit = h.git.run
	updateExec = h.exec.run
	updateGHRecover = func(_, failure string) (string, error) {
		h.ghFailures = append(h.ghFailures, failure)
		return h.ghAccount, h.ghErr
	}
	updatePreflight = func() (jobs.UpdatePreflightInfo, error) { return h.preflight, h.preflightE }
	updateAcquireLock = func() (func(), bool, error) {
		h.lockTaken++
		return func() {}, h.lockOK, h.lockErr
	}
	updateWriteStatus = func(st jobs.UpdateStatus) error {
		h.phases = append(h.phases, st.Phase)
		return jobs.WriteUpdateStatus(st)
	}
	updateSleep = func(time.Duration) {} // never actually wait out the restart settle
	t.Cleanup(func() {
		updateGit, updateExec = origGit, origExec
		updatePreflight, updateAcquireLock, updateWriteStatus, updateSleep = origPre, origLock, origWrite, origSleep
		updateGHRecover = origGH
	})
	return h
}

func (h *updateHarness) statusFileExists() bool {
	_, err := os.Stat(jobs.UpdateStatusPath())
	return err == nil
}

// --- flags ---

func TestParseUpdateArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    updateOpts
		wantErr bool
	}{
		{"no args", nil, updateOpts{}, false},
		{"detached", []string{"--detached"}, updateOpts{detached: true}, false},
		{"force", []string{"--force"}, updateOpts{force: true}, false},
		{"dry run", []string{"--dry-run"}, updateOpts{dryRun: true}, false},
		{"test", []string{"--test"}, updateOpts{test: true}, false},
		{"help long", []string{"--help"}, updateOpts{help: true}, false},
		{"help short", []string{"-h"}, updateOpts{help: true}, false},
		{"all", []string{"--detached", "--force", "--dry-run", "--test"},
			updateOpts{detached: true, force: true, dryRun: true, test: true}, false},
		{"unknown flag", []string{"--bogus"}, updateOpts{}, true},
		{"bare word", []string{"now"}, updateOpts{}, true},
		{"good then bad", []string{"--force", "-x"}, updateOpts{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUpdateArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseUpdateArgs(%v) = %+v, want error", tc.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseUpdateArgs(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Fatalf("parseUpdateArgs(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

func TestUpdateUsageTextMentionsEveryFlag(t *testing.T) {
	for _, flag := range []string{"--force", "--dry-run", "--test", "--detached"} {
		if !strings.Contains(updateUsageText, flag) {
			t.Fatalf("usage text does not document %s:\n%s", flag, updateUsageText)
		}
	}
}

// --- the branches that decide whether anything is built ---

func TestRunUpdateUpToDateBuildsNothing(t *testing.T) {
	h := newUpdateHarness(t)
	h.git.replies["rev-list --count HEAD..origin/main"] = "0"

	var buf strings.Builder
	if err := runUpdate(updateOpts{}, &buf); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	if len(h.exec.calls) != 0 {
		t.Fatalf("up-to-date run shelled out to %v, want nothing built or installed", h.exec.calls)
	}
	if h.git.ran("merge --ff-only origin/main") {
		t.Fatal("up-to-date run merged")
	}
	if got := jobs.ReadUpdateStatus().Phase; got != jobs.UpdateUpToDate {
		t.Fatalf("phase = %q, want %q", got, jobs.UpdateUpToDate)
	}
	if !strings.Contains(buf.String(), "already up to date") {
		t.Fatalf("output %q does not say it was already up to date", buf.String())
	}
}

func TestRunUpdateForceInstallsWhenUpToDate(t *testing.T) {
	h := newUpdateHarness(t)
	h.git.replies["rev-list --count HEAD..origin/main"] = "0"

	if err := runUpdate(updateOpts{force: true}, io.Discard); err != nil {
		t.Fatalf("runUpdate --force: %v", err)
	}
	if !h.exec.ranAll("/usr/bin/make build", "/usr/bin/make install") {
		t.Fatalf("--force ran %v, want a build and an install", h.exec.calls)
	}
	// Nothing to fast-forward: --force means "reinstall anyway", not
	// "merge something that isn't there".
	if h.git.ran("merge --ff-only origin/main") {
		t.Fatalf("--force merged with 0 commits behind: %v", h.git.calls)
	}
	if got := jobs.ReadUpdateStatus().Phase; got != jobs.UpdateDone {
		t.Fatalf("phase = %q, want %q", got, jobs.UpdateDone)
	}
}

func TestRunUpdateDivergedRefusesByHand(t *testing.T) {
	h := newUpdateHarness(t)
	// Exit 1 is git's real answer for "HEAD is not an ancestor" — the ONLY
	// status that means divergence.
	h.git.errs["merge-base --is-ancestor HEAD origin/main"] = &gitExitError{code: 1, msg: "git merge-base --is-ancestor HEAD origin/main: exit status 1: "}

	err := runUpdate(updateOpts{}, io.Discard)
	if err == nil {
		t.Fatal("diverged tree updated anyway")
	}
	const want = "local main has diverged from origin/main — resolve by hand"
	if err.Error() != want {
		t.Fatalf("error = %q, want exactly %q", err.Error(), want)
	}
	if h.git.ran("merge --ff-only origin/main") || len(h.exec.calls) != 0 {
		t.Fatalf("diverged tree merged or built: git=%v exec=%v", h.git.calls, h.exec.calls)
	}
	st := jobs.ReadUpdateStatus()
	if st.Phase != jobs.UpdateFailed || st.Error != want {
		t.Fatalf("status = %+v, want phase %q carrying the refusal", st, jobs.UpdateFailed)
	}
}

// A git that could not ANSWER the ancestry question — exit 128 for a corrupt
// object or an unreadable ref, or no exit status at all — must not be
// reported as divergence: "resolve by hand" sends the user off to reconcile
// branches that were never out of line.
func TestRunUpdateAncestryFailureIsNotReportedAsDivergence(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"git failed hard (128)", &gitExitError{code: 128, msg: "git merge-base: exit status 128: fatal: bad object HEAD"}},
		{"git never ran", errors.New("exec: \"git\": executable file not found in $PATH")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newUpdateHarness(t)
			h.git.errs["merge-base --is-ancestor HEAD origin/main"] = tc.err

			err := runUpdate(updateOpts{}, io.Discard)
			if err == nil {
				t.Fatal("an unanswerable ancestry check still built")
			}
			if strings.Contains(err.Error(), "diverged") {
				t.Fatalf("error = %q, want git's own failure, not a divergence claim", err)
			}
			if !strings.Contains(err.Error(), tc.err.Error()) {
				t.Fatalf("error = %q, want it to carry git's own text %q", err, tc.err)
			}
			if h.git.ran("merge --ff-only origin/main") || len(h.exec.calls) != 0 {
				t.Fatalf("merged or built anyway: git=%v exec=%v", h.git.calls, h.exec.calls)
			}
		})
	}
}

// --- the toolchain PATH the build tools run under ---

// The measured bug this closes: `make build` calls a BARE `go`, and the PATH
// launchd hands `kraken serve` (and so the updater it forks) has no
// /usr/local/go/bin on it, so a board-click update died at the build while
// the same command in a terminal worked.
func TestRunUpdatePutsGoOnThePathOfEveryBuildTool(t *testing.T) {
	h := newUpdateHarness(t)
	t.Setenv("PATH", "/usr/bin:/bin") // launchd-shaped: no go anywhere on it

	if err := runUpdate(updateOpts{test: true}, io.Discard); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	var path string
	for _, kv := range h.exec.lastEnv {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	if !strings.HasPrefix(path, "/usr/local/go/bin:") {
		t.Fatalf("build tools ran with PATH=%q, want the resolved go's dir prepended", path)
	}
	if !strings.Contains(path, "/usr/bin") {
		t.Fatalf("PATH=%q dropped the inherited entries — make/git/codesign still have to be findable", path)
	}
}

// The PATH-prepending itself now lives in internal/util (PrependToolDir /
// ToolchainEnv) and is tested there — shared with the cell spawn, which needs
// the identical fix for the identical launchd reason. What stays here is the
// assertion above, that runUpdate actually hands the build tools that
// environment.

// --- refusals that happen before anything is owned ---

func TestRunUpdatePreflightRefusalAbortsBeforeTheLock(t *testing.T) {
	h := newUpdateHarness(t)
	h.preflightE = errors.New("/repo has uncommitted changes — commit or clean it first")

	err := runUpdate(updateOpts{}, io.Discard)
	if err == nil || err.Error() != h.preflightE.Error() {
		t.Fatalf("error = %v, want the preflight message verbatim", err)
	}
	if h.lockTaken != 0 {
		t.Fatalf("lock acquired %d time(s) despite a preflight refusal", h.lockTaken)
	}
	if len(h.git.calls) != 0 || len(h.exec.calls) != 0 {
		t.Fatalf("preflight refusal still ran git=%v exec=%v", h.git.calls, h.exec.calls)
	}
	// Nothing was in flight, so recording the refusal is ours to do.
	if st := jobs.ReadUpdateStatus(); st.Phase != jobs.UpdateFailed {
		t.Fatalf("phase = %q, want %q", st.Phase, jobs.UpdateFailed)
	}
}

func TestRunUpdatePreflightRefusalLeavesAForeignRunAlone(t *testing.T) {
	h := newUpdateHarness(t)
	h.preflightE = errors.New("/repo is on carl/x, not main")
	// Somebody else's healthy run, mid-build. Our own pid stands in for its
	// owner so jobs.ReadUpdateStatus' staleness check reads it as alive.
	if err := jobs.WriteUpdateStatus(jobs.UpdateStatus{Phase: jobs.UpdateBuilding, PID: os.Getpid(), StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	h.phases = nil

	if err := runUpdate(updateOpts{}, io.Discard); err == nil {
		t.Fatal("want the preflight refusal")
	}
	if got := jobs.ReadUpdateStatus().Phase; got != jobs.UpdateBuilding {
		t.Fatalf("phase = %q, want the in-flight run's %q left untouched", got, jobs.UpdateBuilding)
	}
	if len(h.phases) != 0 {
		t.Fatalf("wrote %v over a concurrent run's record", h.phases)
	}
}

// The pid readback: with a real lock file on disk naming its holder, the
// refusal has to carry it. This is the message a user sees when a run wedged
// past updateMaxRun still owns the flock — the board has already told them
// which pid is stalled, and the only useful thing a retry can do is agree.
func TestRunUpdateContentionNamesTheLockHolder(t *testing.T) {
	h := newUpdateHarness(t)
	h.lockOK = false
	writeUpdateLockFile(t, "pid=4321 at=1700000000000")

	err := runUpdate(updateOpts{}, io.Discard)
	if err == nil || err.Error() != "update already in flight (pid 4321)" {
		t.Fatalf("error = %v, want the holder's pid in the message", err)
	}
}

// Detached, the same message has to reach the record too — that record is the
// only thing the board reads, and the pid in it is the whole remedy.
func TestRunUpdateDetachedContentionRecordsTheLockHolder(t *testing.T) {
	h := newUpdateHarness(t)
	h.lockOK = false
	writeUpdateLockFile(t, "pid=4321 at=1700000000000")
	if err := jobs.WriteUpdateStatus(jobs.UpdateStatus{Phase: jobs.UpdatePreflight, StartedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	h.phases = nil

	if err := runUpdate(updateOpts{detached: true}, io.Discard); err == nil {
		t.Fatal("want the contention refusal")
	}
	st := jobs.ReadUpdateStatus()
	if st.Error != "update already in flight (pid 4321)" {
		t.Fatalf("status error = %q, want the holder's pid", st.Error)
	}
	// Set before the lock is attempted, so even a run that got no further
	// than contention records the head it would have built from.
	if st.FromSHA != h.preflight.HeadSHA {
		t.Fatalf("from_sha = %q, want the preflighted head %q", st.FromSHA, h.preflight.HeadSHA)
	}
}

// An unreadable or garbled lock file costs the message its parenthetical and
// nothing else: decorating a refusal must never become a second failure.
func TestRunUpdateContentionWithoutAReadablePID(t *testing.T) {
	cases := map[string]string{
		"no lock file":  "",
		"garbage":       "who knows",
		"unparseable":   "pid=nonsense at=1",
		"truncated pid": "pid=",
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			h := newUpdateHarness(t)
			h.lockOK = false
			if contents != "" {
				writeUpdateLockFile(t, contents)
			}

			err := runUpdate(updateOpts{}, io.Discard)
			if err == nil || err.Error() != "update already in flight" {
				t.Fatalf("error = %v, want the bare refusal with no pid", err)
			}
		})
	}
}

func TestRunUpdateRefusesWhileAnotherUpdateHoldsTheLock(t *testing.T) {
	h := newUpdateHarness(t)
	h.lockOK = false

	err := runUpdate(updateOpts{}, io.Discard)
	if err == nil || err.Error() != "update already in flight" {
		t.Fatalf("error = %v, want %q", err, "update already in flight")
	}
	if len(h.git.calls) != 0 || len(h.exec.calls) != 0 {
		t.Fatalf("ran work while another update held the lock: git=%v exec=%v", h.git.calls, h.exec.calls)
	}
	if len(h.phases) != 0 {
		t.Fatalf("wrote %v to the lock holder's run record", h.phases)
	}
}

// The detached half of the case above. The API already wrote
// {phase: preflight, pid: 0} as its mutex and spawned us; if we then lose the
// flock and say nothing, NOTHING advances that record — the board spins for
// staleUpdate's 60s grace and then reports "updater never started", which is
// both false and strictly less useful than the real reason.
func TestRunUpdateDetachedRecordsLostLockContention(t *testing.T) {
	h := newUpdateHarness(t)
	h.lockOK = false
	// What the API writes the instant the button is clicked.
	if err := jobs.WriteUpdateStatus(jobs.UpdateStatus{Phase: jobs.UpdatePreflight, StartedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	h.phases = nil

	err := runUpdate(updateOpts{detached: true}, io.Discard)
	if err == nil || err.Error() != "update already in flight" {
		t.Fatalf("error = %v, want %q", err, "update already in flight")
	}
	st := jobs.ReadUpdateStatus()
	if st.Phase != jobs.UpdateFailed {
		t.Fatalf("phase = %q, want %q — the record the API opened for us is ours to close", st.Phase, jobs.UpdateFailed)
	}
	if st.Error != "update already in flight" {
		t.Fatalf("status error = %q, want the real reason, not a timed-out spinner", st.Error)
	}
	if len(h.git.calls) != 0 || len(h.exec.calls) != 0 {
		t.Fatalf("ran work while another update held the lock: git=%v exec=%v", h.git.calls, h.exec.calls)
	}
}

// --- dry run ---

func TestRunUpdateDryRunMutatesNothing(t *testing.T) {
	h := newUpdateHarness(t)

	var buf strings.Builder
	if err := runUpdate(updateOpts{dryRun: true}, &buf); err != nil {
		t.Fatalf("runUpdate --dry-run: %v", err)
	}
	if len(h.exec.calls) != 0 {
		t.Fatalf("dry run shelled out to %v", h.exec.calls)
	}
	if h.git.ran("merge --ff-only origin/main") {
		t.Fatalf("dry run merged: %v", h.git.calls)
	}
	if len(h.phases) != 0 || h.statusFileExists() {
		t.Fatalf("dry run wrote the run record (%v, file=%v)", h.phases, h.statusFileExists())
	}
	for _, want := range []string{"aaa1111", "bbb2222", "behind: 3"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("dry-run output %q missing %q", buf.String(), want)
		}
	}
}

// --- the happy path, end to end ---

func TestRunUpdatePhaseOrder(t *testing.T) {
	h := newUpdateHarness(t)

	if err := runUpdate(updateOpts{}, io.Discard); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	want := []string{
		jobs.UpdateFetching, jobs.UpdateMerging, jobs.UpdateBuilding,
		jobs.UpdateInstalling, jobs.UpdateRestarting, jobs.UpdateDone,
	}
	if strings.Join(h.phases, ",") != strings.Join(want, ",") {
		t.Fatalf("phases = %v, want %v", h.phases, want)
	}
	st := jobs.ReadUpdateStatus()
	if st.Phase != jobs.UpdateDone || st.FromSHA != "aaa1111" || st.ToSHA != "bbb2222" || st.Behind != 3 {
		t.Fatalf("final status = %+v, want done aaa1111→bbb2222 behind 3", st)
	}
	if st.Error != "" {
		t.Fatalf("successful run recorded error %q", st.Error)
	}
	// A healthy fetch must never reach the gh rescue — it is only ever
	// consulted about a failure, and consulting it costs a `gh auth status`
	// on every single update.
	if len(h.ghFailures) != 0 {
		t.Fatalf("healthy run consulted the gh rescue: %v", h.ghFailures)
	}
}

func TestRunUpdateTestFlagRunsTestsBetweenBuildAndInstall(t *testing.T) {
	h := newUpdateHarness(t)

	if err := runUpdate(updateOpts{test: true}, io.Discard); err != nil {
		t.Fatalf("runUpdate --test: %v", err)
	}
	want := []string{"/usr/bin/make build", "/usr/local/go/bin/go test ./...", "/usr/bin/make install"}
	if strings.Join(h.exec.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("exec order = %v, want %v", h.exec.calls, want)
	}
}

func TestRunUpdateFailedTestsInstallNothing(t *testing.T) {
	h := newUpdateHarness(t)
	h.exec.errs["/usr/local/go/bin/go test ./..."] = errors.New("exit status 1")

	err := runUpdate(updateOpts{test: true}, io.Discard)
	if err == nil {
		t.Fatal("red suite still installed")
	}
	for _, c := range h.exec.calls {
		if strings.HasSuffix(c, "install") {
			t.Fatalf("installed after a failed suite: %v", h.exec.calls)
		}
	}
	st := jobs.ReadUpdateStatus()
	if st.Phase != jobs.UpdateFailed || !strings.Contains(st.Error, "nothing was installed") {
		t.Fatalf("status = %+v, want failed saying nothing was installed", st)
	}
}

func TestRunUpdateMidRunFailureRecordsFailedPhase(t *testing.T) {
	h := newUpdateHarness(t)
	h.exec.errs["/usr/bin/make install"] = errors.New("exit status 2")

	err := runUpdate(updateOpts{}, io.Discard)
	if err == nil {
		t.Fatal("want the install failure")
	}
	if got := h.phases[len(h.phases)-1]; got != jobs.UpdateFailed {
		t.Fatalf("last phase = %q, want %q (phases: %v)", got, jobs.UpdateFailed, h.phases)
	}
	st := jobs.ReadUpdateStatus()
	if st.Phase != jobs.UpdateFailed || !strings.Contains(st.Error, "make install failed") {
		t.Fatalf("status = %+v, want failed carrying the install error", st)
	}
	// It failed inside `make install`, so the restart never happened and the
	// run must not claim otherwise.
	for _, p := range h.phases {
		if p == jobs.UpdateDone || p == jobs.UpdateRestarting {
			t.Fatalf("failed run passed through %q: %v", p, h.phases)
		}
	}
}

// --- the gh-account rescue (internal/jobs/ghauth.go owns the decision; what
// is tested here is what the update sequence does with its answer) ---

const fetchKey = "fetch origin main"

func TestRunUpdateSwitchesGHAccountAndRetriesTheFetch(t *testing.T) {
	h := newUpdateHarness(t)
	h.git.errsOnce[fetchKey] = errors.New("git fetch origin main: exit status 128: remote: Repository not found.")
	h.ghAccount, h.ghErr = "car1zhang", nil

	var buf strings.Builder
	if err := runUpdate(updateOpts{}, &buf); err != nil {
		t.Fatalf("runUpdate after a rescued fetch: %v", err)
	}
	if n := h.git.count(fetchKey); n != 2 {
		t.Fatalf("fetched %d time(s), want the failure plus one retry: %v", n, h.git.calls)
	}
	// The rescue has to see git's own text — that message is the entire
	// evidence it decides on.
	if len(h.ghFailures) != 1 || !strings.Contains(h.ghFailures[0], "Repository not found") {
		t.Fatalf("rescue saw %v, want the fetch's own error once", h.ghFailures)
	}
	if !strings.Contains(buf.String(), "switched gh to car1zhang") {
		t.Fatalf("output never mentions the switch:\n%s", buf.String())
	}
	// And the run carried on as normal — a rescued fetch is not a failure.
	if got := h.phases[len(h.phases)-1]; got != jobs.UpdateDone {
		t.Fatalf("last phase = %q, want %q (phases: %v)", got, jobs.UpdateDone, h.phases)
	}
}

func TestRunUpdateFetchFailureWithoutARescueReportsGitsError(t *testing.T) {
	h := newUpdateHarness(t)
	h.git.errs[fetchKey] = errors.New("git fetch origin main: exit status 128: Could not resolve host: github.com")
	// h.ghErr is the default refusal: this failure is not credential-shaped.

	err := runUpdate(updateOpts{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "Could not resolve host") {
		t.Fatalf("err = %v, want git's own failure, not the rescue's explanation of why it did not apply", err)
	}
	if n := h.git.count(fetchKey); n != 1 {
		t.Fatalf("fetched %d time(s), want no retry when nothing was switched: %v", n, h.git.calls)
	}
	st := jobs.ReadUpdateStatus()
	if st.Phase != jobs.UpdateFailed || !strings.Contains(st.Error, "Could not resolve host") {
		t.Fatalf("status = %+v, want failed carrying git's error", st)
	}
}

func TestRunUpdateDryRunNeverSwitchesGHAccounts(t *testing.T) {
	h := newUpdateHarness(t)
	h.git.errs[fetchKey] = errors.New("git fetch origin main: exit status 128: remote: Repository not found.")
	// The rescue would succeed if it were consulted — the point is that a dry
	// run does not consult it. The active gh account is the most global state
	// this command can reach, and --dry-run promises to change nothing.
	h.ghAccount, h.ghErr = "car1zhang", nil

	var buf strings.Builder
	err := runUpdate(updateOpts{dryRun: true}, &buf)
	if err == nil || !strings.Contains(err.Error(), "Repository not found") {
		t.Fatalf("err = %v, want the fetch failure", err)
	}
	if len(h.ghFailures) != 0 {
		t.Fatalf("dry run attempted a gh rescue: %v", h.ghFailures)
	}
	if n := h.git.count(fetchKey); n != 1 {
		t.Fatalf("fetched %d time(s), want no retry under --dry-run: %v", n, h.git.calls)
	}
	if !strings.Contains(buf.String(), "nothing was changed") {
		t.Fatalf("dry run never says it changed nothing:\n%s", buf.String())
	}
	if h.statusFileExists() {
		t.Fatal("dry run wrote a run record")
	}
}

func TestRunUpdateRetriedFetchFailureNamesTheAccount(t *testing.T) {
	h := newUpdateHarness(t)
	h.git.errs[fetchKey] = errors.New("git fetch origin main: exit status 128: remote: Repository not found.")
	h.ghAccount, h.ghErr = "car1zhang", nil

	err := runUpdate(updateOpts{}, io.Discard)
	if err == nil {
		t.Fatal("want the second fetch's failure")
	}
	// Which account the retry ran as is the one fact that makes a second
	// "Repository not found" actionable rather than baffling.
	if !strings.Contains(err.Error(), "retried as gh account car1zhang") {
		t.Fatalf("err = %v, want it to name the account the retry used", err)
	}
	if n := h.git.count(fetchKey); n != 2 {
		t.Fatalf("fetched %d time(s), want exactly one retry: %v", n, h.git.calls)
	}
	if got := h.phases[len(h.phases)-1]; got != jobs.UpdateFailed {
		t.Fatalf("last phase = %q, want %q (phases: %v)", got, jobs.UpdateFailed, h.phases)
	}
}

// --- the git rules that exist because of past incidents ---

func TestRunUpdateNeverPullsOrStashes(t *testing.T) {
	h := newUpdateHarness(t)
	if err := runUpdate(updateOpts{}, io.Discard); err != nil {
		t.Fatalf("runUpdate: %v", err)
	}
	// `pull` can write a merge commit where --ff-only would refuse; `stash`
	// has twice swept unrelated uncommitted work in this user's checkouts;
	// reset --hard / checkout -f can orphan commits. None of them belong in
	// an updater that runs with nobody watching.
	banned := []string{"pull", "stash", "reset", "checkout", "clean", "push"}
	for _, call := range h.git.calls {
		verb := strings.Fields(call)[0]
		for _, b := range banned {
			if verb == b {
				t.Fatalf("update ran `git %s` — forbidden (all calls: %v)", call, h.git.calls)
			}
		}
	}
	if !h.git.ran("merge --ff-only origin/main") {
		t.Fatalf("no fast-forward merge in %v", h.git.calls)
	}
}

func TestRunUpdateDetachedInheritsTheAPIsStartTime(t *testing.T) {
	newUpdateHarness(t)
	// What the API writes the instant the button is clicked, before the
	// child exists.
	const clicked = 1_700_000_000_000
	if err := jobs.WriteUpdateStatus(jobs.UpdateStatus{Phase: jobs.UpdatePreflight, StartedAt: clicked}); err != nil {
		t.Fatal(err)
	}
	if err := runUpdate(updateOpts{detached: true}, io.Discard); err != nil {
		t.Fatalf("runUpdate --detached: %v", err)
	}
	st := jobs.ReadUpdateStatus()
	if st.StartedAt != clicked {
		t.Fatalf("StartedAt = %d, want the API's %d so elapsed covers the whole click", st.StartedAt, clicked)
	}
	if st.PID != os.Getpid() {
		t.Fatalf("PID = %d, want the updater's own %d", st.PID, os.Getpid())
	}
}

// writeUpdateLockFile plants the lock file jobs.AcquireUpdateLock would have
// written, in the harness' sandboxed HOME. The harness stubs the acquire seam
// (no real flock is taken), so the file has to be planted by hand for the
// contention path to have a holder to read back.
func writeUpdateLockFile(t *testing.T, contents string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update.lock"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
