package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kraken/internal/util"
)

// The update pipeline's phases, in the order a real run walks them. These
// strings cross the wire (the board's `update` key, GET /api/update/status)
// and are matched by the web UI, so they are part of the API contract — new
// phases are additive, renames are not.
const (
	UpdateIdle       = "idle"
	UpdatePreflight  = "preflight"
	UpdateFetching   = "fetching"
	UpdateMerging    = "merging"
	UpdateBuilding   = "building"
	UpdateInstalling = "installing"
	UpdateRestarting = "restarting"
	// UpdateMacApp is the `make install-mac` step (cmd_update.go carries the
	// ordering rationale). The SPACE is deliberate, chosen knowing renames are
	// contract breaks: both clients render the token as the button label
	// (uppercased on web — "MAC APP… 12s"; raw on iOS) and nothing anywhere
	// keys on the string, so the human-facing form wins over `uptodate`-style
	// compression.
	UpdateMacApp   = "mac app"
	UpdateDone     = "done"
	UpdateFailed   = "failed"
	UpdateUpToDate = "uptodate"
)

// UpdateTerminal reports whether phase means "no run is in flight". It is the
// single definition of in-flight-ness: the API's 409 guard, the UI's polling
// stop condition, and staleUpdate's first rule all key off it, so a phase
// added to one and not the others can't drift.
func UpdateTerminal(phase string) bool {
	switch phase {
	case UpdateIdle, UpdateDone, UpdateFailed, UpdateUpToDate:
		return true
	}
	return false
}

// UpdateStatus is the one in-flight record of a self-update, at
// ~/.kraken/update.json. It is a FILE rather than a store row precisely
// because its writer outlives the store: the detached updater keeps writing
// phases straight through `launchctl kickstart -k`, which kills the very
// `kraken serve` process that owns kraken.db. A file has no such dependency
// on the process being replaced.
type UpdateStatus struct {
	Phase     string `json:"phase"`
	StartedAt int64  `json:"started_at"` // unix millis; 0 = never run
	UpdatedAt int64  `json:"updated_at"`
	PID       int    `json:"pid"` // 0 while the API handler owns it, pre-spawn
	FromSHA   string `json:"from_sha"`
	ToSHA     string `json:"to_sha"`
	Behind    int    `json:"behind"`
	Error     string `json:"error"`
	ElapsedMS int64  `json:"elapsed_ms"`
	// Restarted records that THIS run replaced the binary and kickstarted the
	// daemon. It exists because the mac-app phase can fail after that point,
	// and a `failed` record alone would leave two readers guessing wrong: the
	// SPA's mandatory post-restart reload (which must fire on failed-with-
	// restart exactly as on done, or every open board keeps running the old
	// bundle against the new server), and the API's behind-count suppression.
	Restarted bool `json:"restarted"`
	// MacAppSHA is the commit the mac app was last successfully installed at,
	// CARRIED FORWARD across runs (the API's claim-write and the updater both
	// preserve it). It is what lets an up-to-date runtime catch up a missed
	// app build — a mac-phase failure leaves HEAD == origin/main, so without
	// this every later click would answer "already up to date" while
	// /Applications/Kraken.app stayed a build behind, with no board-side
	// remedy.
	MacAppSHA string `json:"mac_app_sha"`
}

// UpdateStatusPath is the run record the detached updater writes at every
// phase transition and the API/board read back.
func UpdateStatusPath() string {
	return filepath.Join(util.Home(), ".kraken", "update.json")
}

// UpdateLogPath collects the detached updater's stdout+stderr — the only
// place `make install`'s own output survives, since nobody is attached to
// the child's pipes.
func UpdateLogPath() string {
	return filepath.Join(util.Home(), ".kraken", "update.log")
}

// updateLockPath is the cross-process single-flight guard held for a whole
// update run (see AcquireUpdateLock). Unexported: nothing outside this
// package names the path, they take the lock through AcquireUpdateLock.
func updateLockPath() string {
	return filepath.Join(util.Home(), ".kraken", "update.lock")
}

// updateClaimGraceMS is how long a status with PID 0 is believed. The API
// handler writes phase=preflight BEFORE the child process exists — that write
// IS the mutex a second request trips over — so for a short window a
// perfectly healthy run legitimately names no pid. Without this grace every
// update would read as "failed" the instant it began. 60s is far longer than
// the fork+exec it covers and far shorter than a human's patience for a
// button that appears wedged.
const updateClaimGraceMS = 60_000

// updateMaxRun caps how long a non-terminal record is believed no matter how
// alive its pid looks. Liveness alone is not enough for two reasons that both
// end in a permanently dead UPDATE button (every later POST /api/update 409s
// "update already in flight", with no remedy short of a human deleting
// ~/.kraken/update.json):
//
//   - A subprocess can HANG rather than exit. `make install` runs codesign
//     against a keychain key; if that key's ACL isn't already "always allow"
//     for codesign, macOS raises an authorization dialog — invisible from a
//     detached, terminal-less updater — and the run waits on it forever, pid
//     perfectly alive, phase pinned at installing.
//   - PIDs recycle. ProcessAlive is a bare kill(pid,0) with no identity
//     check, so a SIGKILLed updater whose pid the OS later hands to something
//     unrelated reads as live indefinitely. daemon.go's ProcessIsKraken
//     exists because this repo already shipped that exact bug once.
//
// A real run is a few minutes — ~40s to the restart, then the mac-app
// rebuild's xcodebuild on top; 15 minutes is far past any honest build (even
// with --test) and far short of a human's patience for a wedged button.
const updateMaxRun = 15 * time.Minute

// ReadUpdateStatus loads the run record defensively — a missing, unreadable,
// or malformed file all read as idle, never an error (the house convention
// for externally-owned/optional state) — and then applies staleUpdate, so a
// crashed or killed updater surfaces as failed instead of pinning the UI in
// "BUILDING…" forever. The downgrade is read-side only and deliberately not
// written back: the dead process is by definition not coming back to correct
// the file, and a reader that persisted its own inference would race the
// updater in the one case where it guessed wrong.
func ReadUpdateStatus() UpdateStatus {
	data, err := os.ReadFile(UpdateStatusPath())
	if err != nil {
		return UpdateStatus{Phase: UpdateIdle}
	}
	var st UpdateStatus
	if json.Unmarshal(data, &st) != nil {
		return UpdateStatus{Phase: UpdateIdle}
	}
	if st.Phase == "" {
		st.Phase = UpdateIdle
	}
	return staleUpdate(st, updateProcessAlive(st.PID), time.Now().UnixMilli())
}

// staleUpdate downgrades a non-terminal status whose owning process is gone.
// Split out as a pure function of (status, liveness, clock) so the whole
// decision table is testable without a real pid or a real clock.
func staleUpdate(st UpdateStatus, alive bool, now int64) UpdateStatus {
	if UpdateTerminal(st.Phase) {
		return st
	}
	if st.PID == 0 {
		// Pre-spawn: the API handler wrote this, the child hasn't claimed
		// it yet. Live inside the grace window, dead past it — a spawn that
		// failed so hard the handler couldn't even reset the status leaves
		// exactly this shape behind.
		//
		// StartedAt <= now closes the other end of that window. Without it a
		// FUTURE stamp — a clock that jumped backwards, a hand-edited
		// update.json — satisfies `now - StartedAt <= grace` forever, and
		// because this branch returns, neither the liveness rule nor
		// updateMaxRun below ever gets to see the record: every later POST
		// /api/update 409s and the only remedy is deleting the file. Both
		// clients already bound their own freshness check with abs(); this is
		// the server doing the same.
		if now-st.StartedAt <= updateClaimGraceMS && st.StartedAt <= now {
			return st
		}
		st.Phase = UpdateFailed
		st.Error = "updater never started"
		return st
	}
	if !alive {
		st.Phase = UpdateFailed
		st.Error = fmt.Sprintf("updater exited without finishing (pid %d)", st.PID)
		return st
	}
	// Alive, but for too long. This is the last rule rather than the first so
	// the two cases above keep their more precise sentences; what it adds is
	// the case neither of them can see — a process that never exits (a
	// codesign auth dialog nobody can click) or a pid that isn't really ours
	// any more (recycled). Keyed off UpdatedAt, which every phase transition
	// refreshes, so a slow-but-progressing run is never cut off; StartedAt is
	// only the fallback for a record old enough to predate that stamp, and
	// with neither we have no clock to judge by and leave it alone.
	stamp := st.UpdatedAt
	if stamp == 0 {
		stamp = st.StartedAt
	}
	if stamp != 0 && now-stamp > updateMaxRun.Milliseconds() {
		st.Error = fmt.Sprintf("updater stalled in %s for over %s (pid %d) — see ~/.kraken/update.log", st.Phase, updateMaxRun, st.PID)
		st.Phase = UpdateFailed
		return st
	}
	return st
}

// WriteUpdateStatus persists st, stamping UpdatedAt and deriving ElapsedMS so
// no caller has to remember to. AtomicWrite (not a plain WriteFile) because
// the board polls this file every second from another process and must never
// catch a half-written record.
func WriteUpdateStatus(st UpdateStatus) error {
	now := time.Now().UnixMilli()
	st.UpdatedAt = now
	if st.StartedAt != 0 {
		st.ElapsedMS = now - st.StartedAt
	}
	path := UpdateStatusPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return util.AtomicWrite(path, string(data))
}

// AcquireUpdateLock takes the cross-process single-flight guard at
// ~/.kraken/update.lock, mirroring acquireRotationLock (rotation.go) exactly:
// a kernel flock, so a holder that dies mid-run has its lock released by the
// kernel rather than leaving a stale file behind. Three outcomes: acquired
// (release non-nil, ok=true, err=nil); held elsewhere (nil, false, nil — an
// update is already in flight, which is contention, NOT an error); or the
// lock file couldn't be opened at all (nil, false, err), kept distinct so a
// filesystem problem never gets reported as a phantom concurrent update.
//
// The status file's non-terminal phase is the mutex the API checks; this lock
// is the one the updater process itself holds for the whole run, so a `kraken
// update` typed in a terminal and a board click can't both build at once.
func AcquireUpdateLock() (release func(), ok bool, err error) {
	path := updateLockPath()
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return nil, false, mkErr
	}
	f, openErr := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if openErr != nil {
		return nil, false, openErr
	}
	if flockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); flockErr != nil {
		_ = f.Close()
		return nil, false, nil // held elsewhere — not an error
	}
	_ = f.Truncate(0)
	_, _ = f.WriteString(fmt.Sprintf("pid=%d at=%d", os.Getpid(), time.Now().UnixMilli()))
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}

// UpdateLockHolderPID reads back the pid AcquireUpdateLock stamped into the
// lock file, or 0 when there is nothing trustworthy to report.
//
// It exists for exactly one caller — the updater that just LOST the flock
// (cmd/kraken/cmd_update.go) — and it is what makes that refusal actionable.
// The case it serves is the one updateMaxRun was written for: a run wedged in
// `installing` behind an invisible codesign auth dialog. staleUpdate names
// that holder's pid in the record, the board unblocks the button on it, and
// then the retry's own contention message would overwrite that record with a
// bare "update already in flight" — throwing away the single fact (which
// process to kill) the user needed, and doing it again on every retry.
//
// Call it only on the contention path. The release func unlocks without
// truncating, so a lock file that nobody holds still names whoever held it
// last; the pid is meaningful precisely because flock just told us someone is
// holding it. Defensive to the bone — missing file, truncated write, garbage
// contents, a nonsense pid all read as 0, meaning "no pid to offer", never an
// error: a failure to decorate a message must not become a second failure.
func UpdateLockHolderPID() int {
	data, err := os.ReadFile(updateLockPath())
	if err != nil {
		return 0
	}
	for _, field := range strings.Fields(string(data)) {
		rest, ok := strings.CutPrefix(field, "pid=")
		if !ok {
			continue
		}
		pid, convErr := strconv.Atoi(rest)
		if convErr != nil || pid <= 0 {
			return 0
		}
		return pid
	}
	return 0
}

// UpdatePreflightInfo is what PreflightUpdate resolved on the way to saying
// yes: the repo it would build, the head it would build from, and the
// absolute toolchain paths the updater must use (see lookToolchain for why
// resolving them here rather than trusting PATH is the point). The Info
// suffix is not decoration — the bare name is taken by the phase constant
// above, which the API handler stamps into a status record.
type UpdatePreflightInfo struct {
	Repo     string // ~/Developer/kraken
	Branch   string // must be "main"
	HeadSHA  string // short
	GoPath   string
	MakePath string
	// NpmPath is resolved for the same reason GoPath is: `make build` now
	// depends on a web-assets target that shells out to a BARE `npm` to build
	// the React board into web/_ui/dist, and web/embed.go cannot embed an
	// empty directory — so a missing npm does not degrade the update, it
	// fails it at the build step. Resolving it here turns that into a refusal
	// before anything mutates, and hands cmd_update.go an absolute path to
	// prepend rather than a trust in launchd's PATH.
	NpmPath string
	// XcodegenPath is resolved for the mac-app phase (`make install-mac`
	// calls a bare `xcodegen`, which launchd's PATH does not reach — the same
	// story as go and npm). Unlike them it is resolved NON-fatally: an
	// unresolvable xcodegen must not refuse the runtime update, so the field
	// is empty in that case and the mac-app phase is what fails, loudly,
	// after the runtime is already updated and restarted.
	XcodegenPath string
}

// PreflightUpdate runs every cheap local check before anything mutates. It is
// shared by the CLI verb and the API handler on purpose: the refusal a board
// click shows and the refusal a terminal prints are then the same sentence,
// produced by the same code. Its error text IS the user-facing message.
func PreflightUpdate() (UpdatePreflightInfo, error) {
	var pf UpdatePreflightInfo
	repo, err := ResolveRepo("kraken")
	if err != nil {
		return pf, err
	}
	pf.Repo = repo

	// A TestFlight ship is the one other long-running job that drives this
	// checkout, and the merge below would pull the ground out from under it:
	// `git merge --ff-only` rewrites the working tree while `xcodebuild
	// archive` is reading it, yielding an .ipa torn across two commits that
	// then uploads under the pre-merge build number — which App Store Connect
	// burns permanently. The ship's own preflight refuses the mirror-image case
	// (PreflightShipIOS). Both are refusals rather than a shared flock: a lock
	// would make a 40-second update block silently behind a twenty-minute
	// upload, and a button that hangs teaches nothing.
	if s := ReadShipStatus(); !ShipTerminal(s.Phase) {
		return pf, fmt.Errorf("a TestFlight ship is in flight (%s) — it is archiving from %s, which this update would fast-forward under it; wait for it to finish", s.Phase, repo)
	}

	branch, err := updateRunGit(context.Background(), repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return pf, err
	}
	pf.Branch = branch
	if branch != "main" {
		return pf, fmt.Errorf("%s is on %s, not main", repo, branch)
	}

	// Dirty tree is a hard refusal, never a stash: a stash here would sweep
	// whatever else is uncommitted in that checkout into a pile nobody asked
	// for, and the update has no way to know what it just hid.
	dirty, err := updateRunGit(context.Background(), repo, "status", "--porcelain")
	if err != nil {
		return pf, err
	}
	if strings.TrimSpace(dirty) != "" {
		return pf, fmt.Errorf("%s has uncommitted changes — commit or clean it first", repo)
	}

	head, err := updateRunGit(context.Background(), repo, "rev-parse", "--short", "HEAD")
	if err != nil {
		return pf, err
	}
	pf.HeadSHA = head

	if pf.GoPath, err = lookToolchain("go"); err != nil {
		return pf, err
	}
	if pf.MakePath, err = lookToolchain("make"); err != nil {
		return pf, err
	}
	// npm joined this list when the board became a React app built by vite:
	// `make build` runs `npm ci && npm run build` before `go build`, so npm is
	// now as load-bearing as go is. toolDirs already lists /opt/homebrew/bin,
	// where node installs on this machine, so the fallback resolves it even
	// when launchd hands the daemon a PATH that does not.
	if pf.NpmPath, err = lookToolchain("npm"); err != nil {
		return pf, err
	}
	// Deliberately not an error path — see the field's comment.
	pf.XcodegenPath, _ = lookToolchain("xcodegen")
	return pf, nil
}

// toolDirs are the fallbacks lookToolchain searches after PATH, in order. The
// first entry is the whole reason this function exists: go lives in
// /usr/local/go/bin on this machine and the launchd plist's PATH does not
// include it, so a build kicked off by the daemon failed with "go: command
// not found" while the identical command worked in a terminal.
//
// Named for the tool, not the caller: lookToolchain outgrew update.go —
// PreflightShipIOS resolves xcodegen through it for exactly the same
// launchd-PATH reason — and `updateToolDirs` read as though a ship borrowing
// it were reaching into another feature's state.
var toolDirs = []string{
	"/usr/local/go/bin",
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/usr/bin",
}

// lookToolchain resolves a build tool to an absolute path: PATH first, then
// the known install locations above plus ~/go/bin. The error names every
// place it looked, since "make not found" with no list is unactionable.
// Shared by both preflights and by ghauth.go — go/make for the updater,
// xcodegen for the shipper, gh for the account switch — which is why it and
// its two seams are named for the job rather than for update.go.
func lookToolchain(name string) (string, error) {
	if p, err := toolLookPath(name); err == nil {
		return p, nil
	}
	dirs := append(append([]string{}, toolDirs...), filepath.Join(util.Home(), "go", "bin"))
	for _, dir := range dirs {
		cand := filepath.Join(dir, name)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", fmt.Errorf("%s not found — checked %s", name, strings.Join(dirs, ", "))
}

// updateAvailKVKey is the kv row the background availability check lands in.
// Availability is deliberately NOT stored beside the run status: two
// different writers must never share one file. Run progress is written by the
// detached updater, which outlives `kraken serve`; availability is written by
// the daemon, which is alive exactly when it writes and already holds the
// store's single writer handle.
const updateAvailKVKey = "update_avail"

// updateAvailInterval is how often the daemon re-checks for upstream commits.
// Slow on purpose: it does network I/O, and nothing downstream of it is
// urgent — the number only decorates a button. Unexported along with the
// check itself: the only caller is the daemon, one file over.
const updateAvailInterval = 10 * time.Minute

// updateAvailTimeout bounds every git call one availability check makes,
// fetch included. Generous — a cold fetch over a bad connection is allowed to
// be slow, and cutting a check short only costs a 10-minute tick — but finite,
// which is the whole point (see refreshUpdateAvail). Longer than prpoll's
// 10s because that one runs synchronously inside tick() while this one runs
// off to the side.
const updateAvailTimeout = 60 * time.Second

// UpdateAvail is the daemon's answer to "is there anything to update to?".
type UpdateAvail struct {
	Behind    int    `json:"behind"`
	LocalSHA  string `json:"local_sha"`
	RemoteSHA string `json:"remote_sha"`
	CheckedAt int64  `json:"checked_at"` // unix millis
	Error     string `json:"error"`
}

// ReadUpdateAvail loads the last availability check, defensively: no store
// wired, key never set, or unparseable JSON all read as the zero value. A
// board that shows no "behind" count is correct-looking; an error here would
// not be.
func ReadUpdateAvail() UpdateAvail {
	st := ActiveStore()
	if st == nil {
		return UpdateAvail{}
	}
	raw, err := st.GetKV(updateAvailKVKey)
	if err != nil {
		return UpdateAvail{}
	}
	var a UpdateAvail
	if json.Unmarshal([]byte(raw), &a) != nil {
		return UpdateAvail{}
	}
	return a
}

// writeUpdateAvail persists the check. No store wired (the CLI, a test) is a
// no-op rather than an error — availability is a cache, and losing a write of
// it costs one 10-minute tick. Note the asymmetry with ReadUpdateAvail, which
// IS exported: the API reads this row to decorate the board, but the daemon
// is the only writer of it, and it lives in this package.
func writeUpdateAvail(a UpdateAvail) error {
	st := ActiveStore()
	if st == nil {
		return nil
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return st.SetKV(updateAvailKVKey, string(data))
}

// refreshUpdateAvail fetches origin/main and counts how far behind local main
// is, then records and returns the result. Every failure path — no repo, no
// network, unparseable count — produces a result carrying Error rather than an
// error return: a failed availability check must never be fatal, never surface
// as an alert, and never leave a caller deciding what to do about it.
//
// The daemon calls it through its own checkUpdateAvail seam (daemon.go), which
// is why the name differs: one is the implementation, the other the swappable
// var tests point elsewhere.
func refreshUpdateAvail() UpdateAvail {
	now := time.Now().UnixMilli()
	// A failure keeps the last known-good Behind/LocalSHA/RemoteSHA and
	// updates only CheckedAt and Error. This check is the sole writer of the
	// count and re-runs only every updateAvailInterval, so zeroing it on one
	// transient miss (laptop asleep, no wifi, a GitHub blip) would blank the
	// board's "2 behind" badge for a full ten minutes and then bring it back
	// — a badge that flaps reads as a bug, while a count that was true ten
	// minutes ago is stale at worst. Error carries the fact that it wasn't
	// re-confirmed, for anyone who cares to look.
	fail := func(msg string) UpdateAvail {
		a := ReadUpdateAvail()
		a.CheckedAt, a.Error = now, msg
		_ = writeUpdateAvail(a)
		return a
	}
	repo, err := ResolveRepo("kraken")
	if err != nil {
		return fail(err.Error())
	}
	// Bounded, unlike the updater's own git calls: this whole function runs
	// in a fire-and-forget goroutine whose `defer updateAvailRunning.Store(
	// false)` is the ONLY thing that ever re-arms the check (daemon.go's
	// kickUpdateAvail), so a git that never returns doesn't just lose one
	// tick — it silently retires the availability check for the daemon's
	// entire lifetime, freezing the "N BEHIND" badge in a state
	// indistinguishable from "up to date", with no log line to say so. The
	// fetch is the call that can do it: a socket left dead by a laptop
	// sleeping mid-transfer can take macOS' TCP keepalive hours to notice.
	// Same reasoning, and the same shape, as viewPRActivity in prpoll.go.
	ctx, cancel := context.WithTimeout(context.Background(), updateAvailTimeout)
	defer cancel()
	if _, err := updateRunGit(ctx, repo, "fetch", "origin", "main"); err != nil {
		return fail(err.Error())
	}
	out, err := updateRunGit(ctx, repo, "rev-list", "--count", "HEAD..origin/main")
	if err != nil {
		return fail(err.Error())
	}
	behind, convErr := strconv.Atoi(strings.TrimSpace(out))
	if convErr != nil {
		return fail(fmt.Sprintf("unexpected rev-list output %q", strings.TrimSpace(out)))
	}
	// The two shas are decoration on top of the count that actually drives
	// the UI, so a failure to read either leaves that field blank instead of
	// discarding a perfectly good answer.
	local, _ := updateRunGit(ctx, repo, "rev-parse", "--short", "HEAD")
	remote, _ := updateRunGit(ctx, repo, "rev-parse", "--short", "origin/main")
	a := UpdateAvail{Behind: behind, LocalSHA: local, RemoteSHA: remote, CheckedAt: now}
	_ = writeUpdateAvail(a)
	return a
}

// --- swappable seams, the same pattern rotation.go uses for its gate, so
// every branch above is testable without a subprocess, a network, or a real
// pid. Nothing in the test suite may shell out. ---
var (
	// updateRunGit runs one git command in dir under ctx. The context is in
	// the signature rather than inside the body because only one caller needs
	// it and the difference matters: PreflightUpdate's git calls are all local
	// and instant (rev-parse, status) and pass Background, while
	// refreshUpdateAvail's include a network fetch it must be able to give up
	// on (see updateAvailTimeout).
	updateRunGit = func(ctx context.Context, dir string, args ...string) (string, error) {
		full := append([]string{"-C", dir}, args...)
		cmd := exec.CommandContext(ctx, "git", full...)
		// GIT_TERMINAL_PROMPT=0 on every call, not just the fetch: nothing in
		// this package ever runs git with a human attached — the daemon is a
		// launchd job and the updater is detached — so a git that decides to
		// ask for a username has already failed, and the only question is
		// whether it fails in milliseconds or sits on the prompt until a
		// timeout (or, unbounded, forever). Failing fast also puts git's own
		// "could not read Username" text into the error, which beats a bare
		// "context deadline exceeded".
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}
	// toolLookPath is lookToolchain's PATH seam — shared with PreflightShipIOS,
	// hence the neutral name (see toolDirs).
	toolLookPath       = exec.LookPath
	updateProcessAlive = ProcessAlive
)
