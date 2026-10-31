package jobs

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// kraken serve — the one long-lived kraken process (launchd-managed,
// installed by make install). It hosts the scheduler poll: derive each
// started job's working/blocked state from live pod activity, watch cells for
// unhealthy zeuses (auto-retrying with backoff), watch PRs, and write a
// heartbeat the viewer renders.
//
// It STARTS nothing on its own initiative — submitting a job starts
// its cell in that same HTTP request (jobs.SubmitJob), so there is no queue
// pass and no cell cap; the tick's only start attempt is the one it makes for
// a dream job it just created itself, and that attempt is
// single-shot. It owns no state that can't be re-derived from files +
// discovery — the in-memory dedup maps only suppress duplicate
// notifications, and losing them on restart merely re-notifies once.

const (
	DaemonInterval = 15 * time.Second
	// usage polling is throttled to every usageEvery-th tick (~5 min): each
	// account fetch is a real network call, and usage windows don't move
	// fast enough to justify polling every 15s tick.
	usageEvery = 20
	// backoff between auto-retry pokes of the same stalled cell, and between
	// re-notifications of the same standing health complaint.
	repokeAfter = 10 * time.Minute
	// sessionCreateGrace: a cell session younger than this counts active
	// regardless of the ROLL-CALL — claude is still launching before any
	// session file or pane signal exists yet.
	sessionCreateGrace = 2 * time.Minute
	// autoArchiveAfter: a done job left untouched this long gets swept into
	// the archive on its own, so the board doesn't accumulate accepted work
	// forever — carl can still unarchive it any time.
	autoArchiveAfter = 24 * time.Hour
)

type DaemonStatus struct {
	PID       int   `json:"pid"`
	StartedAt int64 `json:"started_at"`
	LastTick  int64 `json:"last_tick"`
	// LastError is VESTIGIAL and is kept only for wire compatibility, the
	// same call webBoard.CellCap gets (api/board.go's legacyCellCap) and for
	// the same reason: `last_error` is on the wire, and both clients model it.
	//
	// It has no writer left. tick() returns a nil error on every path — the
	// queue pass was the only thing that ever produced one, and the one start
	// the tick still makes (a dream job it created itself) reports its
	// failure per job, to the event log and a notification, rather than
	// through this one global string that no client renders per job.
	//
	// Kept rather than removed because omitting a key is a breaking change
	// and this one buys nothing: it is `omitempty`, so an empty value is
	// already absent from every payload, and a client reading it sees exactly
	// what it saw on a healthy tick before. It goes when the clients drop it.
	// Do not add new writers — a per-job surface is the right home for a
	// per-job failure.
	LastError string `json:"last_error,omitempty"`
	Cells     int    `json:"cells"`
	// Stale is this daemon process' own self-check (see selfcheck.go) — true
	// once make install has replaced the binary on disk out from under it.
	Stale bool `json:"stale,omitempty"`
}

func daemonStatusPath() string {
	return filepath.Join(util.Home(), ".kraken", "daemon.json")
}

// inProcessDaemonStatus holds kraken serve's own heartbeat when a store is
// wired: RunServeTicks stamps it every tick instead of writing
// daemon.json, since serve's tick loop and the process reading
// ReadDaemonStatus (its own web API) are the same process — no file
// round-trip needed. UseStore clears it (see store.go) so a not-yet-ticked
// serve process reads as "no status" rather than a leftover value.
var inProcessDaemonStatus atomic.Pointer[DaemonStatus]

// ReadDaemonStatus returns the daemon's last-known heartbeat: the
// in-process value (set by RunServeTicks) when a store is wired in THIS
// process, else ~/.kraken/daemon.json — the file lets a separate process
// (the viewer, `k v`, which never wires a store itself) read serve's
// heartbeat too, since RunServeTicks writes both.
func ReadDaemonStatus() (DaemonStatus, bool) {
	if ActiveStore() != nil {
		st := inProcessDaemonStatus.Load()
		if st == nil {
			return DaemonStatus{}, false
		}
		return *st, true
	}
	return readDaemonStatusFile()
}

// readDaemonStatusFile reads ~/.kraken/daemon.json directly, ignoring
// whether a store is wired in THIS process. ReadDaemonStatus must keep
// branching on ActiveStore for its normal callers (the web API, the
// viewer), which want serve's own in-process status once wired — but
// GuardServeStart's single-instance startup guard (daemonGuardErr) needs
// the file unconditionally: by the time kraken serve's guard runs,
// jobs.UseStore has already wired ActiveStore in this process, so
// ReadDaemonStatus would otherwise report this process' own (still-nil,
// not-yet-ticked) in-process status instead of a DIFFERENT already-running
// serve process' file heartbeat.
func readDaemonStatusFile() (DaemonStatus, bool) {
	var st DaemonStatus
	data, err := os.ReadFile(daemonStatusPath())
	if err != nil || json.Unmarshal(data, &st) != nil {
		return st, false
	}
	return st, true
}

func ProcessAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// ProcessIsKraken cross-checks (E14) that pid — already confirmed alive by
// ProcessAlive — is actually a kraken process, via one `ps` exec (same
// shell-out style core/discover.go already uses for its own process
// enumeration). PIDs recycle: a daemon.json left behind by a process that
// died can, after enough time and enough other processes starting/exiting,
// end up naming a pid the OS has since handed to something completely
// unrelated — without this check, daemonGuardErr would read that unrelated
// process as "the daemon is alive" and refuse to start forever, with no
// remedy short of a human noticing and deleting the file by hand. Fails
// open (true) on any ps error: the guard's whole purpose is refusing to
// double-run a real daemon, and a ps hiccup must never manufacture a FALSE
// refusal by assuming the pid ISN'T kraken.
//
// Exported as a package-level var (not a plain func), same stubbing
// pattern this file already uses for probeOnline: it's exported (capital
// P) specifically so tests in OTHER packages (cmd_import_test.go, package
// main) that need to simulate "a live, genuinely-kraken conflicting
// process" — which they can't do for real without spawning an actual
// kraken binary just to get ps to report the right comm — can swap it too.
var ProcessIsKraken = func(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return true
	}
	return strings.Contains(strings.ToLower(string(out)), "kraken")
}

// daemonGuardErr is kraken serve's single-instance startup guard
// (GuardServeStart): refuses when daemon.json names a
// DIFFERENT, still-alive, ACTUALLY-kraken pid (processIsKraken, E14 —
// closes the pid-recycling false-positive above). Always reads the file
// directly (readDaemonStatusFile), not ReadDaemonStatus, so it works the
// same whether or not this process has wired a store yet. The error names
// the stale-state remedy (rm ~/.kraken/daemon.json) so a genuinely stuck
// guard — the recycled-pid case processIsKraken doesn't catch because the
// unrelated process' name happens to also contain "kraken", or any other
// edge this heuristic misses — always has an obvious way out without
// reading source.
func daemonGuardErr() error {
	st, ok := readDaemonStatusFile()
	if !ok || st.PID == os.Getpid() || !ProcessAlive(st.PID) || !ProcessIsKraken(st.PID) {
		return nil
	}
	return daemonConflictErr(st.PID)
}

// daemonConflictErr formats daemonGuardErr's refusal — pulled out as its
// own pure function (E14) so the error text's stale-state remedy (rm
// ~/.kraken/daemon.json) is testable without needing a second live,
// actually-named-kraken pid to trigger the real refusal branch.
func daemonConflictErr(pid int) error {
	return fmt.Errorf("daemon already running (pid %d) — if this is stale (daemon.json outlived the process, or its pid was recycled), remove ~/.kraken/daemon.json and retry", pid)
}

// GuardServeStart is kraken serve's own startup guard: called synchronously
// by cmd_serve BEFORE starting the tick loop or binding the web server, so a
// second `kraken serve` already running makes this one refuse cleanly and
// exit non-zero instead of silently double-running the scheduler underneath
// a web server that came up anyway.
func GuardServeStart() error {
	return daemonGuardErr()
}

// writeDaemonStatusFile persists st to ~/.kraken/daemon.json, creating the
// parent dir if needed. RunServeTicks writes it every tick (so a second
// `kraken serve`'s own startup guard trips while this one is running the
// scheduler, and so a separate process like the viewer can read it — see
// ReadDaemonStatus) in addition to the in-process status it already keeps.
// Best-effort: a failed write is silently skipped, matching
// every other heartbeat/status write in this package.
func writeDaemonStatusFile(st DaemonStatus) {
	if err := os.MkdirAll(filepath.Dir(daemonStatusPath()), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(st); err == nil {
		_ = util.AtomicWrite(daemonStatusPath(), string(data))
	}
}

// serveStop, closed by StopServeTicks, tells RunServeTicks' between-tick
// sleep to return immediately instead of riding out the rest of the current
// DaemonInterval — used by kraken serve's shutdown handler so a SIGTERM
// doesn't have to wait up to DaemonInterval before the process can exit.
var serveStop = make(chan struct{})
var serveStopOnce sync.Once

// StopServeTicks signals RunServeTicks to return after its current tick
// instead of sleeping again. Idempotent — safe to call more than once (a
// repeated signal, a test) without panicking on a double close.
func StopServeTicks() {
	serveStopOnce.Do(func() { close(serveStop) })
}

// notify fires a macOS notification; failures are logged, never fatal — the
// board itself is the durable signal.
func notify(title, msg string) {
	script := fmt.Sprintf("display notification %q with title %q", core.TruncateOneLine(msg, 120), title)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		dlogErr("osascript notify failed: %v", err)
	}
}

// dlogEvent prints one daemon log line (launchd captures stdout into
// daemon.log as before) AND appends it to the unified event log — the
// single source of truth the board's log viewer reads. level/event feed the
// viewer's badges; the plain dlog/dlogErr wrappers cover the long tail of
// call sites that don't need a dedicated event id.
func dlogEvent(level, event, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	fmt.Printf("%s %s\n", time.Now().Format("15:04:05"), msg)
	util.LogEvent("daemon", level, event, msg)
}

func dlog(format string, a ...any)    { dlogEvent("info", "daemon", format, a...) }
func dlogErr(format string, a ...any) { dlogEvent("error", "daemon-error", format, a...) }

// probeOnline reports whether the Claude API endpoint is reachable — the
// dependency that actually matters for a stalled cell. ~2s cap so a dead
// network can't stall the tick. Note this is TCP reachability, not API
// health: a 529-storm outage still probes "online" (the socket connects
// fine), so those pokes ride the normal repokeAfter backoff instead of being
// gated here — intended, since gating on 529s would need a real API call
// every tick just to find out the endpoint is still overloaded.
var probeOnline = func() bool {
	conn, err := net.DialTimeout("tcp", "api.anthropic.com:443", 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// daemonState holds the scheduler's cross-tick dedup state. newDaemon constructs
// one; tick runs a single scheduler pass.
type daemonState struct {
	Notified map[string]int64 // dedup key (err/repo file mtime, or "blocked:"/"health:" slug) → notified marker
	Poked    map[string]int64 // "retry:" + slug → unix time last auto-retried
	// Repaired marks a job's PR-repair pass as fully done for this process
	// (slug → done) — see repairPRs. In-memory only, like Notified/Poked
	// above, but re-running the pass across a daemon restart is safe BY
	// DESIGN: the repair criterion never drops a create-vouched or
	// family-branch record, so repeating it finds nothing new to drop.
	Repaired map[string]bool
	// PRHeads memoizes viewPRHead's successful URL → headRefName lookups.
	// Heads are immutable once a PR exists, so this is a positive cache with
	// no invalidation, consulted before every gh pr view call and shared
	// across jobs and passes — without it, a permanently-unresolvable URL
	// (e.g. its repo got deleted) costs one gh subprocess per record per PR
	// pass forever, and a URL recorded against two jobs is billed twice.
	PRHeads map[string]string
	// RepairTries counts consecutive incomplete repair passes per slug (slug
	// → count). After 3, repairPRs gives up: it sets Repaired[slug]=true
	// WITHOUT dropping anything, so giving up is always safe — it just stops
	// spending gh calls on a job whose evidence never resolves.
	RepairTries map[string]int
	// ArtifactSyncState tracks each started job's own last-derived board
	// state (slug → "working"/"blocked"/...), independent of the job's own
	// State field, purely to detect the working→blocked edge that triggers
	// syncArtifactsNow. In-process only, like the dedup maps above: losing
	// it on restart just means the next real transition (not a spurious one
	// — there's no false trigger from a blank map) does the mirror instead.
	ArtifactSyncState map[string]string
	// BlockedSince is the tick on which a WORKING job's roll call first
	// read blocked (slug → tickN), the one-tick memory holdBlocked needs:
	// the working→blocked write, its notification and its artifact sync
	// wait for a second consecutive blocked reading, because the roll call
	// reads a zeus mid-turn as idle for exactly one tick a dozen-plus times
	// a night (measured across three nights) and each unconfirmed flip cost
	// a "blocked — see the board" notification before the next tick read
	// working again. Reset to zero on any non-blocked reading, confirmed
	// only by the immediately following tick, and pruned with
	// ArtifactSyncState. In-process only, like the maps above: losing it on
	// a restart costs one more tick of hold, never a missed transition.
	BlockedSince map[string]int
	// TokenScan throttles tokensPass over jobs with no live cell (slug →
	// unix millis of the last sum). In-process only, like the dedup maps
	// above: losing it on a restart costs one extra sum per job, which is a
	// stat per transcript file and no writes when nothing moved.
	TokenScan map[string]int64
	// PRScan throttles the PR tracker over jobs with no live cell (slug →
	// unix millis of the last poll), TokenScan's exact shape: in-process
	// only, and losing it on a restart costs one extra gh round per idle
	// job. Slug-keyed for TokenScan's reason (a per-tick throttle clock over
	// the active board, whose slugs are unique), with the same freed-slug
	// hazard closed by prunePRScan.
	PRScan map[string]int64
	// nodes is the fleet pass' per-process memory (nodes.go): failure counts,
	// the last announced envelope, and its single-flight flag.
	nodes NodesState
	// Jev is the dialog answerer's memory (jev.go): how often each dialog
	// on screen was typed into and which escalations were already logged.
	// Created on first use. In-process only, like the maps above: losing
	// it on a restart costs at most one more keypress or one duplicate log
	// line per dialog on screen.
	Jev   *jevState
	tickN int
	// offline: in-memory only, like the dedup maps above — losing it on
	// restart just costs one duplicate "offline" notification. Set while
	// probeOnline fails so auto-retry pokes are suppressed instead of eaten
	// by the same 529/network outage they're meant to survive.
	offline bool
}

func newDaemon() *daemonState {
	return &daemonState{
		Notified:          map[string]int64{},
		Poked:             map[string]int64{},
		Repaired:          map[string]bool{},
		PRHeads:           map[string]string{},
		RepairTries:       map[string]int{},
		ArtifactSyncState: map[string]string{},
		BlockedSince:      map[string]int{},
		TokenScan:         map[string]int64{},
		PRScan:            map[string]int64{},
	}
}

// pruneArtifactSyncState drops ArtifactSyncState entries for slugs that left
// the "started and not done" live set — finished (done), archived, or
// vanished from the board entirely. A stale "working" entry would otherwise
// survive keyed on a slug that later gets reused by an unrelated new job
// (archiving frees the slug — see archive.go), spuriously firing that new
// job's first working→blocked transition as if it were a continuation of
// the old job's. Pure over (d, jobs), so it's unit-testable without a live
// cell, tmux, or a real repo to resolve.
func (d *daemonState) pruneArtifactSyncState(jobs []*Job) {
	stillTracked := map[string]bool{}
	for _, j := range jobs {
		if j.Started() && j.State != "done" {
			stillTracked[j.Slug] = true
		}
	}
	for slug := range d.ArtifactSyncState {
		if !stillTracked[slug] {
			delete(d.ArtifactSyncState, slug)
		}
	}
	// BlockedSince is slug-keyed for the same reason and pruned on the same
	// rule: holdBlocked's adjacency check already makes a stale entry
	// harmless, so this is housekeeping rather than correctness.
	for slug := range d.BlockedSince {
		if !stillTracked[slug] {
			delete(d.BlockedSince, slug)
		}
	}
}

// usagePassRunning single-flights UsagePass across ticks: it does network I/O
// up to ~10s and must never overlap itself or stretch the scheduler loop.
var usagePassRunning atomic.Bool

// updateAvailRunning single-flights the upstream availability check across
// ticks, for exactly the reason usagePassRunning exists above:
// refreshUpdateAvail runs a real `git fetch`, so it must never overlap itself
// (two fetches racing in the same checkout) nor stretch the scheduler loop
// behind however long an unreachable origin takes to give up. In-memory
// process state, so like usagePassRunning it collapses same-process races
// only — which is all there is here, since only serve's tick calls it.
var updateAvailRunning atomic.Bool

// lastUpdateAvailCheck is the in-memory backstop for the throttle below: the
// millis at which this process last STARTED a check, regardless of whether
// the result ever reached the store. Necessary because ReadUpdateAvail
// degrades every failure — no store wired, a failed kv read, a write that
// silently didn't land — to the zero value, and a zero CheckedAt is always
// due. Without this a broken store would quietly turn a ten-minute poll into
// a real `git fetch` on every 15s tick. Deliberately the SECOND gate, not the
// first: it is lost on restart, while the stored stamp survives one, so the
// store stays the source of truth and this only catches what the store can't
// tell us.
var lastUpdateAvailCheck atomic.Int64

// checkUpdateAvail is the seam kickUpdateAvail calls through, so tests can
// drive the tick's cadence without a subprocess or a network.
var checkUpdateAvail = refreshUpdateAvail

// kickUpdateAvail refreshes the "N commits behind origin/main" count the
// update button renders, at most once per updateAvailInterval, in a
// fire-and-forget goroutine behind updateAvailRunning.
//
// "When was it last checked?" is UpdateAvail.CheckedAt in the store rather
// than a package-level timestamp alone: refreshUpdateAvail stamps it on every
// outcome, success and failure alike, so there is one source of truth — the
// same value the board renders, meaning "checked 3m ago" in the UI and "not
// due yet" here can never disagree — and it survives a restart, so
// reinstalling the daemon a few times in a row can't turn a 10-minute cadence
// into a fetch per start. A store that has never been checked reads CheckedAt
// 0, which is always due, so the first tick after startup fires one.
func kickUpdateAvail(now time.Time) {
	ms := now.UnixMilli()
	if ms-ReadUpdateAvail().CheckedAt < updateAvailInterval.Milliseconds() {
		return
	}
	if last := lastUpdateAvailCheck.Load(); last != 0 && ms-last < updateAvailInterval.Milliseconds() {
		return
	}
	// The seam is captured into a local before the goroutine starts, same
	// reason KickUsagePass captures runUsagePass: a test that swaps it back
	// the moment this returns must not race the goroutine's own read of it.
	if check := checkUpdateAvail; updateAvailRunning.CompareAndSwap(false, true) {
		lastUpdateAvailCheck.Store(ms)
		go func() {
			defer updateAvailRunning.Store(false)
			if a := check(); a.Error != "" {
				// Debug level and otherwise ignored: an unreachable origin is
				// a normal state for a laptop, the check already recorded the
				// error itself, and a stale decoration on a button is not
				// worth erroring a tick over.
				dlog("update availability check failed: %s", a.Error)
			}
		}()
	}
}

// ServeOpts configures RunServeTicks' per-tick behavior. The zero value is
// the normal kraken serve scheduler; Mirror selects the local
// mirror-testing mode (`kraken serve --mirror`, cmd_serve.go): a read-only
// scheduler variant for testing feature parity against real data without
// ever touching real tmux/git/gh, popping a real OS notification, or
// conflicting with the live daemon.json heartbeat a genuine `kraken serve`
// process depends on.
type ServeOpts struct {
	Mirror bool
}

// RunServeTicks runs the scheduler tick loop for kraken serve, as a
// goroutine alongside web.Serve — no SIGTERM/SIGINT handling here (cmd_serve
// registers its own, since web.Serve's own signal handling already covers
// HTTP shutdown and this goroutine additionally needs StopServeTicks/the
// store closed in the right order). It performs the single-instance startup
// guard (daemonGuardErr — dies loudly, as a backstop in case it's ever
// reached without cmd_serve's own GuardServeStart pre-check having already
// refused, matching this package's util.Die-on-fatal-startup-error
// convention) and writes the daemon.json heartbeat on top of (not instead
// of) the in-process status inProcessDaemonStatus/ReadDaemonStatus serves —
// the file lets a second `kraken serve` refuse to start while this one is
// running, and lets a separate process (the viewer) read the heartbeat too.
// It returns only once StopServeTicks is called (serve's shutdown path).
// UseStore must already have been called — every accessor tick() touches
// (LoadJobs, WriteJobFields, ...) is only store-backed once wired.
//
// opts.Mirror skips both of those: no daemonGuardErr check (mirror is
// designed to run ALONGSIDE a live serve process against a separate sandbox
// DB and port, never to contend with it for the single-instance slot) and
// no daemon.json write, ever — a mirror process must never be mistaken for
// the real serve process by another process' own guard check. The
// in-process heartbeat (inProcessDaemonStatus/ReadDaemonStatus) still
// updates normally, so the mirror process' own board header shows a live
// status.
func RunServeTicks(opts ServeOpts) {
	if !opts.Mirror {
		if err := daemonGuardErr(); err != nil {
			util.Die("%v", err)
		}
		if err := os.MkdirAll(filepath.Dir(daemonStatusPath()), 0o755); err != nil {
			util.Die("mkdir %s: %v", filepath.Dir(daemonStatusPath()), err)
		}
	}
	d := newDaemon()
	st := DaemonStatus{PID: os.Getpid(), StartedAt: time.Now().Unix()}
	cur := st
	inProcessDaemonStatus.Store(&cur)
	if !opts.Mirror {
		// L7: daemon.json is written BEFORE the first tick runs, not only
		// afterward — d.tick() is not guaranteed fast (live-cell roll call,
		// throttled PR/usage polling, ...), and a second `kraken serve`
		// starting in that window would read no daemon.json yet and (wrongly)
		// conclude no serve is running, racing this process to write the same
		// kraken.db. Publishing the heartbeat file first closes that window.
		writeDaemonStatusFile(st)
	}
	dlogEvent("info", "daemon-start", "serve ticks up (pid %d, tick %s, mirror %v)", st.PID, DaemonInterval, opts.Mirror)
	for {
		cells, err := d.tick(opts.Mirror)
		next := st
		next.LastTick, next.Cells, next.LastError, next.Stale = time.Now().Unix(), cells, "", util.IsSelfStale()
		if err != nil {
			next.LastError = err.Error()
			dlogEvent("error", "tick-error", "tick error: %v", err)
		}
		st = next
		// L5: store a FRESH copy each tick, not &st — st is one variable
		// reused across every loop iteration, so a reader that Loads the
		// pointer once and holds onto it (ReadDaemonStatus's caller) was
		// racing this goroutine's next st = next assignment, which
		// mutates the exact struct the reader's pointer already points
		// at. A distinct local per iteration means each Store publishes an
		// immutable snapshot that's never written to again.
		cur := next
		inProcessDaemonStatus.Store(&cur)
		if !opts.Mirror {
			writeDaemonStatusFile(st)
		}
		select {
		case <-serveStop:
			return
		case <-time.After(DaemonInterval):
		}
	}
}

// Tick is one scheduler pass. Each concern is independent: an error in one
// (reported via return for the status file) never blocks the others.
//
// mirror (spec: kraken serve --mirror, RunServeTicks' ServeOpts.Mirror) is
// the local mirror-testing mode's read-only-scheduler contract: state
// derivation (the ROLL-CALL, deriveState, WriteJobState) and the
// working→blocked artifact sync keep running — the whole point is testing
// those against real data — but every real-world side effect this pass can
// otherwise trigger is skipped: no StartCell/queueing, no teardown
// (ResumeTeardown), no PR tracking/polling/scanning/repair, no usage
// polling, no auto-archive, and no macOS notify() popups or auto-retry
// pokes (RetryCell). dlog/dlogEvent keep firing either way — they're
// process-local logging (stdout + the unified event log, itself mirrored
// into the sandbox DB when one is wired), not a real-world side effect.
func (d *daemonState) tick(mirror bool) (int, error) {
	d.tickN++
	jobs := LoadJobs()
	live := LiveSessions()
	bySlug := map[string]*Job{}
	for _, j := range jobs {
		bySlug[j.Slug] = j
	}

	// unparseable jobs: one notification per job+mtime, the board carries
	// the detail. Keyed on j.Slug, not j.File: a store-backed job's File is
	// always empty (jobFromStoreRow leaves it zero), which would collide
	// every unparseable store job onto the same dedup key — j.Slug is the
	// one identity both file- and store-backed jobs actually have.
	for _, j := range jobs {
		if j.Err == "" {
			continue
		}
		key := "err:" + j.Slug
		if mt := j.ModTime.Unix(); d.Notified[key] != mt {
			if !mirror {
				notify("kraken: job error", j.Slug+": "+j.Err)
			}
			d.Notified[key] = mt
		}
	}

	all, _ := core.OrderAll(core.DiscoverAll())
	times := liveSessionTimes()
	now := time.Now()
	cells := 0
	// side-thread panes, ONE unscoped query for the whole tick rather than a
	// ListThreads per job: the roll call below (cellActive) must not count a
	// thread as the job working — carl branches one precisely to ask about a
	// block, and a voting thread would hide it. Fails open (nil set = today's
	// behavior) so a store hiccup degrades to a possibly-optimistic state
	// rather than a tick that dies.
	//
	// Read once, ahead of the per-job ReconcileThreads passes below, so a row
	// closed during THIS tick still abstains for the rest of it: one tick of
	// lag, self-correcting on the next, and the alternative (re-querying per
	// job) costs a query per job to buy nothing.
	var threadPanes map[string]bool
	if st := ActiveStore(); st != nil {
		var err error
		if threadPanes, err = st.LiveThreadPanes(); err != nil {
			dlogErr("live thread panes: %v", err)
			threadPanes = nil
		}
	}
	// connectivity probe: at most one dial per tick, invoked lazily from
	// inside gateRetry — only when a retryable poke is actually due or the
	// daemon is already flagged offline. A tick with no stalled cells never
	// touches the network.
	var onlineProbed, online bool
	checkOnline := func() bool {
		if !onlineProbed {
			online = probeOnline()
			onlineProbed = true
		}
		return online
	}
	// stale-offline-flag guard: gateRetry only runs when a retryable health
	// reason exists this tick, so a d.offline left true by a prior outage
	// would otherwise linger forever once every stalled cell has recovered
	// some other way (carl retried by hand, the turn just finished) — a much
	// later, unrelated stall would then fire a spurious "back online"
	// notification alongside its own. Checking here, unconditionally, at
	// every tick's start clears it promptly instead.
	if d.offline && checkOnline() {
		title, msg := d.clearOffline()
		if !mirror {
			notify(title, msg)
		}
	}
	for _, j := range jobs {
		if !j.Started() || j.State == "done" {
			continue
		}
		// FindCellSession, not live[CellSession(j)]: a renamed cell whose tmux
		// convergence has not landed yet is still running under an earlier
		// name, and reading it as not-live would flip a working job to
		// blocked, fire the dead-cell alert, and hand the roll call a session
		// no pane is in.
		sess, isLive := FindCellSession(j, live)
		if isLive {
			cells++
			// self-heal: found under a FORMER name, so the rename's own
			// convergence failed (or this tick beat it). Re-running it is
			// idempotent and is what stops the cell sitting under the old name
			// indefinitely — every later resolver would otherwise keep paying
			// the former-name probe for a session tmux could simply rename.
			// mirror never touches a pane belonging to the real deployment.
			if !mirror && sess != CellSession(j) {
				for _, note := range convergeCellTmux(j, sess, markerSlugForSession(j, sess)) {
					dlogErr("%s: cell rename convergence: %s", j.Slug, note)
				}
			}
			// eager session stamp: zeus' session id lands in the job file
			// within a tick of the cell coming up (and re-lands if it ever
			// changes), so even a job whose cell later dies uncleanly stays
			// reactivatable
			if err := stampSession(j, all); err != nil {
				dlogErr("session stamp %s failed: %v", j.Slug, err)
			}
			delete(d.Notified, "gone:"+j.Slug)
		} else {
			// interrupted teardown: a surviving teardown: marker means a done/
			// kill died between its first destructive step and its final state
			// write (e.g. the teardown process lived inside the session it
			// killed). The marker's provenance is carl's explicit action, so
			// finishing it — worktree discard included — is sanctioned.
			if j.Teardown != "" {
				// mirror never finishes a real teardown (worktree removal,
				// tmux kill-session) — a surviving teardown: marker is left
				// exactly as found, for the real daemon/serve to resume.
				if mirror {
					continue
				}
				// Read the marker BEFORE the resume: ResumeTeardown's final
				// state write clears j.Teardown on this same struct, so the
				// log line below printed an empty "()" on every one of its
				// firings and could not say WHICH marker it finished — the
				// one word that tells a genuine interrupted teardown from a
				// resume racing the board's own Done.
				marker := j.Teardown
				if err := ResumeTeardown(j); err != nil {
					d.noteTeardownFailure(j, err)
				} else {
					d.clearTeardownFailure(j)
					dlog("%s: finished interrupted teardown (%s)", j.Slug, marker)
				}
				continue
			}
			// dead-cell watch: a started job whose tmux session vanished
			// (killed by hand, tmux server restart) would otherwise sit on
			// its file state forever, looking healthy in the state column —
			// the other half of the demo-job incident. Detection only:
			// recovery stays carl's explicit reactivate/done/kill — reactivate
			// is the one that preserves the worktree's uncommitted work.
			//
			// Absent from the snapshot is not evidence of death: one tick's
			// discovery failing (a tmux call erroring or returning partial
			// output) reads every live cell as gone at once, and this alert
			// has fired for five live cells inside 1.4 seconds off exactly
			// that — an alert carl then acted on with a teardown that
			// destroyed a day's uncommitted work. Same stance the thread
			// reconcile takes: deciding a cell is GONE demands positive
			// evidence, so re-probe tmux directly and flag only when the
			// probe agrees. A cell that is genuinely gone costs one probe per
			// tick until the flag is set; a snapshot artifact costs one probe
			// and self-corrects next tick.
			// the re-probe covers EVERY name the cell may be running under,
			// for the reason FindCellSession exists: a renamed cell that has
			// not converged is alive under an older session name, and alerting
			// on it would be the same false "cell is gone" carl has previously
			// acted on with a teardown that destroyed uncommitted work.
			key := "gone:" + j.Slug
			if d.Notified[key] == 0 && !anyCellSessionAlive(j) {
				if !mirror {
					notify("kraken ⚠ "+j.Slug, "cell session is gone — reactivate to resume; done/kill discard the worktree")
				}
				dlogEvent("error", "cell-gone", "%s: cell %s is gone (was %s)", j.Slug, CellSession(j), j.State)
				d.Notified[key] = now.Unix()
			}
		}

		// close out any side thread whose window died since the last tick,
		// off THIS tick's snapshot — no extra tmux/ps call, one query per
		// started job. mirror never writes.
		//
		// Deliberately OUTSIDE the isLive branch above, covering dead cells
		// too: a live row's pane goes into the roll call's skip set globally
		// (LiveThreadPanes is unscoped), so a row still claiming the pane of a
		// vanished cell would silence whichever job's agent tmux next hands
		// that recycled pane id to — pinning a genuinely working job at
		// "blocked". A dead session has no peers, so every one of its threads
		// reconciles closed here on the first tick after it goes.
		//
		// `live` rides along as the cross-check: this is the one consumer of
		// the tick's snapshot that WRITES off it, so an empty snapshot from a
		// failed tmux call must not read as "every thread is gone".
		if !mirror {
			if err := ReconcileThreads(j, all, live); err != nil {
				dlogErr("thread reconcile %s failed: %v", j.Slug, err)
			}
		}

		// health first, independent of state derivation below: an API-errored
		// or half-dead cell needs carl's retry regardless of what state it
		// reads as. Idle-stall dedup: the reason embeds the rounded idle
		// duration, so key on the prefix or every tick re-notifies with a
		// fresh number.
		// mirror: the whole health/auto-retry pass is skipped — RetryCell is a
		// real poke into a live tmux pane, and the notify() calls here are
		// exactly the "no notifications/pokes" mirror is meant never to fire.
		if isLive && !mirror {
			if reason := CellHealth(j, all); reason != "" {
				key := "health:" + j.Slug + "|" + strings.SplitN(reason, " idle ", 2)[0]
				if d.Notified[key] == 0 {
					suffix := " — on the board"
					if RetryableHealth(reason) {
						// accurate even while offline: it WILL auto-retry, once
						// connectivity returns — gateRetry just gates the poke.
						suffix = " — will auto-retry; also on the board"
					}
					notify("kraken ⚠ "+j.Slug, reason+suffix)
					d.Notified[key] = now.Unix()
					dlogEvent("error", "cell-unhealthy", "unhealthy %s: %s", j.Slug, reason)
				}
				// transcript arm of the usage-limit rescue. The hook
				// (StopFailure → kraken limit-hook) is the fast trigger, but
				// it is installed into a cell's settings at SPAWN, so every
				// cell already running when this ships has no hook at all and
				// would otherwise never be rescued. The stall classification
				// is the second, hook-independent way to learn the same fact:
				// zeus' transcript ends in a synthetic api-error that reads as
				// a usage limit. Recorded as "rate_limit" so the rescue's own
				// gate 3 accepts it — the two triggers must describe one event
				// in one vocabulary or the fold below can't join them.
				//
				// Two things bound the churn. RecordLimitHit folds a repeat
				// onto the still-unresolved hit (resolution, not time, is the
				// event boundary — a windowed fold made the standing stall's
				// re-deliveries reset the rescue's attempt counter forever),
				// and this key stops a STANDING complaint (a stalled cell
				// keeps the same stall at the tail of its transcript
				// indefinitely) from re-recording — and, once the hit
				// resolves, from opening a fresh one — every tick.
				//
				// The key EXPIRES rather than being set once, and that is the
				// difference between a bound and a mute: a weekly-limit banner
				// is byte-identical every time that window exhausts, so a
				// never-expiring key would record Monday's limit on account A
				// and then silently decline Wednesday's on account B forever —
				// silently, because the "health:" key above suppresses the
				// notification for the same reason. Reusing the stamp the map
				// already stores costs nothing and re-arms after the same
				// interval the rotate backoff uses. Keyed on the job's
				// immutable ID rather than its slug, matching the record it
				// guards: archiving frees a slug, so a new job reusing an
				// archived generation's name would otherwise inherit its
				// suppression.
				if IsUsageLimitReason(reason) {
					lkey := limitHitTriggerKey(j.ID, reason)
					if limitHitTriggerDue(d.Notified[lkey], now) {
						if _, err := RecordLimitHit(j, "rate_limit", reason, j.Session, "", now); err != nil {
							dlogErr("%s: record usage-limit hit from transcript failed: %v", j.Slug, err)
						} else {
							d.Notified[lkey] = now.Unix()
							dlogEvent("info", "limit-hit", "%s: usage limit seen in transcript: %s", j.Slug, reason)
						}
					}
				}
				// auto-retry retryable stalls with backoff: API overload comes
				// in waves, and a manual retry during one just eats the next
				// 529 — keep nudging every repokeAfter until a turn survives.
				// gateRetry additionally gates the poke behind connectivity —
				// an offline machine gets one notification and zero poke spam
				// instead of burning every backoff window on a dead network.
				if RetryableHealth(reason) {
					rkey := "retry:" + j.Slug
					poke, title, msg := d.gateRetry(now, rkey, checkOnline)
					if title != "" {
						notify(title, msg)
					}
					if poke {
						if err := RetryCell(j); err != nil {
							dlogErr("auto-retry %s failed: %v", j.Slug, err)
						} else {
							d.Poked[rkey] = now.Unix()
							dlog("auto-retried %s (%s)", j.Slug, reason)
						}
					}
				}
			}
		}

		// state derivation: working ⇄ blocked, driven off a live ROLL-CALL of
		// the cell's pane agents and team members (ResolveRepo stays here
		// only for its error-notification side effect — state derivation
		// itself no longer needs the worktree).
		if _, err := ResolveRepo(j.Repo); err != nil {
			// surfaces on the board as repo?; notify once per job version.
			// Keyed on j.Slug, not j.File — see the "err:" dedup above.
			key := "repo:" + j.Slug
			if mt := j.ModTime.Unix(); d.Notified[key] != mt {
				if !mirror {
					notify("kraken: job error", j.Slug+": "+err.Error())
				}
				d.Notified[key] = mt
			}
			continue
		}
		active := false
		if isLive {
			// a session just created is at least as fresh as its own
			// creation — covers pre-first-transcript startup before any pane
			// or team-config signal exists yet
			if created, ok := times[sess]; ok && now.Sub(created) < sessionCreateGrace {
				active = true
			} else {
				zeus := CellZeus(j, all)
				// prefer the LIVE discovered zeus peer's session id over the
				// job file's stamped one: right after a daemon restart (or a
				// reactivation onto a fresh run), j.Session can lag a tick
				// behind the session claude is actually running under, which
				// would make teamMembersForLead look up the wrong team config
				// and silently see no members. A guessed id is never a safe
				// lookup key (see SessionIDGuessed), so still fall back to
				// j.Session when the discovered id is guessed or absent.
				sessionID := j.Session
				if zeus != nil && zeus.SessionID != "" && !zeus.SessionIDGuessed {
					sessionID = zeus.SessionID
				}
				// the session the cell was actually FOUND under: a
				// not-yet-converged renamed cell has every one of its panes in
				// the old session, so passing the current name would empty the
				// roll call and read a working job as blocked.
				active = cellActive(sess, all, core.TeamMembersForLead(sessionID), core.PaneShowsQuestion, threadPanes)
			}
		}
		st := deriveState(j, isLive, active)
		// A working job's first blocked reading is held for one tick (see
		// holdBlocked): everything below — the artifact sync, the state
		// write, the notification — keys off the CONFIRMED state, so a
		// one-tick misread of a mid-turn zeus moves nothing.
		st, d.BlockedSince[j.Slug] = holdBlocked(j.State, st, isLive, d.BlockedSince[j.Slug], d.tickN)
		// E11: on the very first tick after this process starts (fresh
		// daemonState, so ArtifactSyncState is blank), seed the map from
		// THIS tick's own derived state AND run one unconditional sync for
		// every started, not-done job — regardless of whether st is
		// "working" or "blocked". Without this, a job that was ALREADY
		// blocked before a `kraken serve` restart (its working→blocked
		// EDGE happened under a PREVIOUS process, or never happened at all
		// if it's been sitting blocked since before the daemon first ever
		// ran) would never get its artifacts captured: the transition
		// check right below only fires on an OBSERVED "working"→"blocked"
		// edge, and a blank ArtifactSyncState reads as "" != "working", so
		// it silently misses (not spuriously fires — just misses) exactly
		// the case a restart-while-blocked leaves behind. This one-shot
		// startup sync is unconditional on live/blocked/working precisely
		// because it isn't trying to detect a transition; it's a "capture
		// whatever this process finds on boot" pass, same idea as
		// teardownCell's own "last chance" sync but at startup instead of
		// teardown.
		if d.tickN == 1 && j.Started() && j.State != "done" {
			syncArtifactsNow(j, "serve-startup")
		}
		// block-event artifact sync (spec Q11): a working→blocked transition
		// in the ROLL-CALL's own derived state — tracked independently of
		// j.State's file/store write below, which may already agree with st
		// (a web/TUI action can write "working" directly between ticks) —
		// gets one store mirror of the cell's artifacts dir. No-op when no
		// store is wired.
		if d.ArtifactSyncState[j.Slug] == "working" && st == "blocked" {
			syncArtifactsNow(j, "working→blocked")
		}
		d.ArtifactSyncState[j.Slug] = st
		// the dedup key must clear whenever the DERIVED state reads as
		// working, not only when the daemon itself performs the write below:
		// web/TUI actions (answering a question, steering, /send) write
		// "working" directly, so gating the delete on "a write happened here"
		// left it permanently stuck after the first such action — every later
		// block on the same job would then never notify again.
		if st == "working" {
			delete(d.Notified, "blocked:"+j.Slug)
		}
		if (st == "working" || st == "blocked") && st != j.State {
			prev := j.State
			if err := WriteJobState(j, st); err != nil {
				dlogErr("write state %s for %s failed: %v", st, j.Slug, err)
				continue
			}
			dlogEvent("info", "state-change", "%s: %s → %s", j.Slug, prev, st)
			if st == "blocked" {
				key := "blocked:" + j.Slug
				if d.Notified[key] == 0 {
					if !mirror {
						notify("kraken ⏸ "+j.Slug, blockedNotifyBody(jobQuestionRows(j)))
					}
					d.Notified[key] = now.Unix()
				}
			}
		}
	}

	d.pruneArtifactSyncState(jobs)

	// PR tracker (live cells every tick, 15s; idle jobs paced — see
	// prIdleInterval beside prTrackable): record every PR shipped from a cell
	// branch into the job's prs: list. A merge is a signal — one notification and
	// a board chip — NEVER a teardown trigger; teardown lives only behind
	// carl's explicit accept via board Done. Tracking continues
	// through done — a merge after carl accepts still deserves recording —
	// and stops only when the job is archived (autoArchivePass below drops
	// it from LoadJobs' namespace entirely).
	// mirror: nothing in this block runs. Every pass here either shells out
	// to gh and/or writes prs: history that only matters for a real cell's
	// lifecycle, or TYPES INTO A LIVE PANE (the PR watcher's bumps, the
	// colour reconcile's /color) — and a mirror board is read-only by
	// policy, most of all toward panes belonging to the real deployment.
	if !mirror {
		idlePR := 0
		for _, j := range jobs {
			if !prTrackable(j) {
				continue
			}
			// Idle jobs are paced — see prIdleInterval beside prTrackable
			// for why gh traffic is worth bounding and a live cell's is not.
			if !live[CellSession(j)] {
				if !prScanDue(d.PRScan[j.Slug], now.UnixMilli()) {
					continue
				}
				if idlePR >= prIdleBatch {
					continue
				}
				idlePR++
			}
			// Stamped before trackPRs can bail on a bad repo — this job has
			// had its turn whatever comes of it (tokensPass' lesson: stamping
			// only on success leaves a bailing job due again every tick,
			// permanently consuming an idle slot while logging nothing).
			d.PRScan[j.Slug] = now.UnixMilli()
			d.trackPRs(j, all, live)
			d.scanTranscriptPRs(j, all)
			d.repairPRs(j, all)
		}
		d.prunePRScan(jobs)
		// PR watcher (every tick, 15s — its own pass after the tracker
		// above so every job's j.PRs is fully reconciled — state transitions,
		// transcript attaches, repair drops — before watchPRs reads it; its
		// own live-cell gate is what keeps that cheap): bump
		// zeus' pane when an open PR of an active job gets a new comment/
		// review, a newly-failing CI check, or a fresh merge conflict.
		for _, j := range jobs {
			d.watchPRs(j, all, live)
		}
		// colour reconcile (its own pass after the PR watcher, same
		// per-tick cadence): type /color into zeus' pane whenever the job's
		// chosen colour differs from what was last delivered to this cell.
		for _, j := range jobs {
			d.syncColor(j, all, live)
		}
		// auto-handoff (its own pass right after the colour reconcile, same
		// per-tick cadence): type /handoff into a live cell's zeus once its
		// statusline-reported context fill reaches autoHandoffThreshold
		// (cellctx.go). It types into a live pane, which is why it sits in
		// this block.
		for _, j := range jobs {
			autoHandoffPass(ActiveStore(), j, all, live, now)
		}
		// dialog answerer (its own pass, same per-tick cadence): when a live
		// cell pane shows a Claude Code or codex dialog, ask Jev which option
		// keeps the job running and type it, or log an escalation (jev.go).
		// It types into live panes, which is why it sits in this block.
		if d.Jev == nil {
			d.Jev = newJevState()
		}
		for _, j := range jobs {
			d.Jev.jevPass(j, all, live, threadPanes, now, func(l, e, m string) { dlogEvent(l, e, "%s", m) })
		}
		// stack probe (its own pass, same per-tick cadence): dial each live
		// cell's loopback stack links and stamp up/down on change
		// (stackprobe.go). It writes rows a mirror board must not, which is
		// why it sits in this block; it types into no pane.
		for _, j := range jobs {
			stackProbePass(ActiveStore(), j, live, now)
		}
		// usage-limit rescue (its own pass, same per-tick cadence): a cell
		// whose claudes hit their account's usage limit is REBOUND to a
		// healthier pool slot — one job row, nothing machine-wide — and then,
		// a settle window later, its zeus is KILLED IN PLACE and respawned on
		// its own session, since a running claude neither re-reads a changed
		// credential nor drops the limit it has latched onto locally. It sits
		// in this block for one of this block's two reasons rather than both:
		// it destroys and recreates a live pane's process. (Since Q2 it makes
		// no machine-wide credential change at all — the main login is
		// reachable only from `k account` and the account API.) A --mirror
		// daemon still RECORDS hits (the API handler is not mirror-gated) —
		// it just never acts on one.
		//
		// Its own sweep rather than a line in the colour loop above, for the
		// same reason every pass in this block has one: each is independently
		// orderable and independently skippable. The extra loop costs one kv
		// read per job (limitRescue's first line is ReadLimitHit, and it
		// returns immediately unless that job has an unresolved hit).
		for _, j := range jobs {
			d.limitRescue(j, all, live)
		}
		// the same two passes for the STANDING agent — iris, the long-lived
		// claude that belongs to no job (standingrescue.go). They sit in this
		// block for its two reasons exactly: a rotate makes a machine-wide
		// credential change, and a resume destroys and recreates a live
		// pane's process. They consume the snapshots this tick already holds
		// rather than sweeping again, like every pass above, and each row is
		// gated on its own feature switch — a machine that never configured
		// iris pays one Slack config read.
		//
		// Health first, then rescue, so a limit the health pass records this
		// tick is acted on this tick rather than one interval later. Both are
		// no-ops without a store: every record they read and write is a kv row.
		if st := ActiveStore(); st != nil {
			for _, a := range standingAgents() {
				if a.Enabled == nil || !a.Enabled() {
					continue
				}
				// A FRESH clock per agent, not the tick-start `now` the rest of
				// this function carries: these run after the whole per-job loop
				// and the PR tracker's gh shell-outs, so the tick clock can be
				// tens of seconds stale by here — and the rescue compares its
				// `now` against second-granularity stamps (the settle window,
				// the adoption's swapAt >= hit.At) where that staleness decides
				// outcomes. The cell pass has the same need and takes its own
				// reading inside limitRescue; the standing signature makes the
				// clock a parameter, so the freshness is the call site's job.
				standingNow := time.Now()
				d.standingHealthPass(a, st, all, live, standingNow)
				d.standingLimitRescue(a, st, all, live, standingNow)
			}
		}
	}

	// token pass (every tick, self-paced internally — see tokensPass): keep
	// each job's job_tokens row current from its own transcripts, so the
	// board and the nightly dream pass can read a number instead of summing
	// tens of megabytes on demand. Its own block rather than a line in the
	// one above because it shares none of that block's reasons for existing:
	// it types into no pane, shells out to nothing, and its only writes are
	// two store tables. Skipped under mirror all the same — mirror's tick
	// runs state derivation and the block-event artifact sync, nothing else.
	if !mirror {
		d.tokensPass(jobs, live, now)
	}

	// mental-model verify pass (mm.go, self-paced internally at ten minutes):
	// check every live item against what its substrate says today — artifact
	// rows inline, code anchors in a single-flighted goroutine that fetches.
	//
	// Skipped under mirror for the sharpest form of the usual two reasons: it
	// FETCHES INTO REAL CHECKOUTS under ~/Developer — the ones the live
	// deployment's cells branch from — and it writes rows and fires
	// notifications on every death.
	if !mirror {
		MMVerifyPass(ActiveStore(), now)
		// the mental model rendered into rules files (mmrender.go, self-paced
		// at 60s): it WRITES into real checkouts under ~/Developer, which is
		// the same reason as the verify pass for never running under mirror
		MMRenderPass(ActiveStore(), now)
	}

	// Slack intake (slack.go, iris.go): keep iris alive while Slack is
	// configured, poll carl's DMs/mentions into the inbox, and bump iris'
	// pane when rows are waiting. Each takes a snapshot THIS tick already
	// holds rather than running its own — EnsureIris the `live` session map,
	// deliverSlack the discovered `all` peers — since a discovery sweep costs
	// a `ps` plus a `tmux list-panes` and one per tick is already the budget.
	//
	// Skipped under mirror for all three of the usual reasons at once: real
	// network I/O against Slack, real store writes, and typing into a pane
	// that belongs to the live deployment.
	if !mirror {
		if err := EnsureIris(ActiveStore(), live); err != nil {
			// Deduped like tokensfail: a spawn that fails because tmux or
			// claude is unavailable will fail identically four times a minute.
			if d.Notified["irisfail"] == 0 {
				dlogErr("iris: %v", err)
				d.Notified["irisfail"] = now.Unix()
			}
		} else {
			delete(d.Notified, "irisfail")
		}
		slackPass(ActiveStore(), all, now)
		deliverSlack(ActiveStore(), all)
	}

	// linear pass (self-paced internally — see LinearPass): poll Linear for
	// issues newly assigned to carl, create a draft job per qualifying one,
	// and keep the ones that already have a job in step. A free function
	// owning its own throttle and single-flight, so this stays one line; it
	// reaches for ActiveStore() itself, since its first gate is the config
	// rather than the store — and skipped under mirror for the sharpest reasons on the
	// list at once: it creates real job rows, downloads real files, and
	// TYPES INTO A LIVE PANE.
	if !mirror {
		LinearPass(now)
	}

	// the fleet (nodes.go): discover sibling krakens on the tailnet, announce
	// this one, poll their card lists. Skipped under mirror because it writes
	// the registry and, by announcing, tells every sibling that this process
	// is a node — a sandbox serve must never present itself as the machine's.
	if !mirror {
		d.nodesPass(now)
	}

	// usage poll (throttled, first-tick eager so UIs have data right after
	// daemon start): KickUsagePass's own usagePassRunning atomic
	// single-flights this into its own goroutine (network I/O up to ~10s,
	// must not stretch this loop) against the account API's own post-switch
	// kick running in this same process. Skipped under mirror — tick() there runs
	// ONLY state derivation + block-event artifact sync, nothing else.
	// Both pools poll on the same ticks, each in its own goroutine behind its
	// own single-flight flag, so neither can delay or suppress the other: the
	// claude pass is up to ~10s of network I/O and the codex pass is a disk
	// read, and a board that showed one pool's numbers going stale whenever
	// the other's fetch was slow would be reporting the wrong thing.
	if !mirror && (d.tickN == 1 || d.tickN%usageEvery == 0) {
		KickUsagePass()
		KickCodexUsagePass()
	}

	// upstream availability poll: refreshes the update button's "N behind"
	// badge. Throttled by the stored CheckedAt rather than a tick count (see
	// kickUpdateAvail) and single-flighted into its own goroutine, since it
	// shells out to `git fetch`. Skipped under mirror for both of the usual
	// reasons at once — it would fetch in the REAL kraken checkout, and it
	// writes shared state a read-only scheduler variant has no business
	// touching.
	if !mirror {
		kickUpdateAvail(now)
	}

	// auto-archive: done jobs left untouched for a day sweep themselves into
	// the archive, so the board doesn't accumulate accepted work forever.
	// !live is the same safety net ArchiveJob itself re-checks — a job
	// stamped done but whose cell somehow lingers is left alone here.
	// Skipped entirely under mirror: ArchiveJob writes the store, deletes
	// uploads dirs and deregisters worktrees, none of which a mirror may do
	// on its own initiative.
	if !mirror {
		archivedSlugs := autoArchivePass(jobs, live, now)
		switch n := len(archivedSlugs); {
		case n == 0:
		case n <= 3:
			for _, slug := range archivedSlugs {
				notify("kraken 📦 "+slug, "auto-archived (done > 24h)")
			}
		default:
			notify("kraken 📦", fmt.Sprintf("auto-archived %d done jobs (> 24h)", n))
		}
	}

	// dream loop: at most one corroboration pass per local calendar day (see
	// kickDream — the first wall-clock cadence in this binary). Gated on
	// !mirror because creating a real job row — and, below, STARTING its cell
	// — is exactly the class of side effect a read-only scheduler variant must
	// never have.
	//
	// The mind runs (mindRunPass, mindrun.go) follow under the same gate: the
	// daily run is queued and the oldest queued run started, never under
	// --mirror. A mind run is a headless child, not a job, so it starts no cell.
	//
	// The dream job is the ONLY start the daemon makes. Every other job on the
	// board starts from carl's own submit, synchronously inside that request;
	// a dream job has no human in the loop by construction, so the
	// creator starts what it created, here, on the same tick it created it. The
	// attempt is single-shot on purpose: a failure notifies loudly and leaves
	// a DRAFT on the board, which carl can submit by hand, rather than a
	// background retry loop re-attempting a broken start every 15s forever
	// with nothing on screen (which is precisely what the queue pass did).
	if !mirror {
		if created := kickDream(now); created != nil && d.startCreatedJob(created) {
			cells++
		}
		d.mindRunPass(ActiveStore(), now)
	}
	return cells, nil
}

// startCreatedJob submits a job the tick just created for itself (the dream
// job) and reports whether the cell came up. Success logs and notifies
// exactly as the old queue pass did; failure logs, notifies, and returns
// false — the job stays draft, and nothing retries it.
//
// It goes through SubmitJob rather than calling StartCell directly so the
// creators get the same validation carl's own submit gets: a dream job whose
// spec came out empty, or whose repo no longer resolves, is refused here with
// a reason instead of spawning a cell with an empty prompt.
//
// A failure deliberately does NOT become the tick's returned error. That
// return feeds DaemonStatus.LastError — one global string for the whole tick,
// which no client renders per job — and the reason is already on the two
// surfaces that carry it per job: the event log and a notification. The old
// queue pass drew the same line for the same failure, letting a ResolveRepo
// miss notify and `continue` without touching firstErr.
func (d *daemonState) startCreatedJob(j *Job) bool {
	if err := SubmitJob(j); err != nil {
		dlogEvent("error", "start-failed", "start %s failed: %v", j.Slug, err)
		notify("kraken ⚠ "+j.Slug, "created but could not start: "+err.Error())
		return false
	}
	dlogEvent("info", "cell-start", "started %s (cell %s)", j.Slug, CellSession(j))
	notify("kraken ▶ "+j.Slug, "cell started")
	return true
}

// gateRetry decides whether a due auto-retry poke for rkey ("retry:"+slug)
// is allowed to proceed, gating it behind reachability of the endpoint the
// retry actually depends on: a stall caused by an outage can't be nudged
// away, and hammering RetryCell every repokeAfter during one just burns
// pokes for nothing. checkOnline is expected to memoize its dial for the
// tick, so calling this for several stalled cells in one pass costs at most
// one probe. Mutates d.offline/d.Notified/d.Poked; never touches tmux or
// osascript itself — the caller fires the returned notification (title
// non-empty) and, on poke==true, performs the actual RetryCell + Poked
// stamp so gateRetry stays pure enough to unit test without a live cell.
func (d *daemonState) gateRetry(now time.Time, rkey string, checkOnline func() bool) (poke bool, notifyTitle, notifyMsg string) {
	pokeDue := now.Unix()-d.Poked[rkey] >= int64(repokeAfter/time.Second)
	if !pokeDue && !d.offline {
		return false, "", ""
	}
	if !checkOnline() {
		d.offline = true
		// only log the suppression when a poke was actually due — the
		// not-due-but-offline path above still consults the probe (to notice
		// a reconnect promptly), and logging that every tick would just be
		// "still offline" noise with nothing suppressed.
		if pokeDue {
			dlog("suppressed retry poke (%s) — offline", rkey)
		}
		if d.Notified["offline"] == 0 {
			d.Notified["offline"] = now.Unix()
			return false, "kraken ⚠ offline", "offline — pausing auto-retries until connectivity returns"
		}
		return false, "", ""
	}
	if d.offline {
		title, msg := d.clearOffline()
		return pokeDue, title, msg
	}
	return pokeDue, "", ""
}

// teardownFailPrefix keys the resume-teardown failure dedup in d.Notified:
// "teardownfail:<job id>:<hash of the error text>". A teardown that keeps
// failing on resume (an artifact the serve user cannot read, say) would
// otherwise log one identical error row every 15s tick, forever.
func teardownFailPrefix(j *Job) string {
	return "teardownfail:" + strconv.FormatInt(j.ID, 10) + ":"
}

// noteTeardownFailure logs a failed resume of an interrupted teardown once
// per job and error text — a CHANGED cause logs again — and sends one
// notification the first time the job's teardown fails on resume, since
// the board shows nothing but a working card with no zeus behind it.
func (d *daemonState) noteTeardownFailure(j *Job, err error) {
	prefix := teardownFailPrefix(j)
	h := fnv.New64a()
	_, _ = h.Write([]byte(err.Error()))
	key := prefix + strconv.FormatUint(h.Sum64(), 16)
	if d.Notified[key] != 0 {
		return
	}
	first := true
	for k := range d.Notified {
		if strings.HasPrefix(k, prefix) {
			first = false
			delete(d.Notified, k)
		}
	}
	d.Notified[key] = time.Now().Unix()
	dlogErr("%s: finish interrupted teardown failed: %v", j.Slug, err)
	if first {
		notifyFn("kraken ⚠ "+j.Slug, "teardown failed and is being retried: "+err.Error())
	}
}

// clearTeardownFailure drops the job's resume-teardown failure dedup, so a
// later failure is logged and notified afresh.
func (d *daemonState) clearTeardownFailure(j *Job) {
	prefix := teardownFailPrefix(j)
	for k := range d.Notified {
		if strings.HasPrefix(k, prefix) {
			delete(d.Notified, k)
		}
	}
}

// clearOffline resets the offline dedup bookkeeping (the flag itself, the
// "offline" Notified marker, and every backed-off "retry:" Poked key) and
// returns the one back-online notification to fire. Shared by gateRetry's
// reconnect path and tick's own tick-start check below — a stalled cell can
// recover by some path OTHER than gateRetry (carl retries by hand, the turn
// just finishes) while d.offline stays stuck true, and only checking at
// tick-start (not only inside gateRetry, which only runs when a retryable
// health reason exists this tick) catches that case before a much-later
// stall would otherwise fire a spurious "back online" notification.
func (d *daemonState) clearOffline() (title, msg string) {
	d.offline = false
	delete(d.Notified, "offline")
	for k := range d.Poked {
		if strings.HasPrefix(k, "retry:") {
			delete(d.Poked, k)
		}
	}
	dlog("connectivity restored — resuming auto-retries")
	return "kraken ✓ online", "back online — resuming auto-retries"
}

// autoArchivePass sweeps jobs eligible under autoArchivable into the
// archive, returning the slugs it moved. No dedup map is needed: archiving
// moves the file out of LoadJobs' namespace, so an archived job can't
// reappear here on a later tick to re-trigger.
func autoArchivePass(jobs []*Job, live map[string]bool, now time.Time) []string {
	var archived []string
	for _, j := range jobs {
		if !autoArchivable(j, now) || live[CellSession(j)] {
			continue
		}
		name, err := ArchiveJob(j, live)
		if err != nil {
			dlogErr("auto-archive %s failed: %v", j.Slug, err)
			continue
		}
		dlogEvent("info", "auto-archive", "%s: auto-archived as %s (done > 24h)", j.Slug, name)
		archived = append(archived, j.Slug)
	}
	return archived
}

// prTrackable reports whether the PR tracker should poll a job: any
// parse-clean job that could have shipped PRs (Q14 removed the build/info
// job-type split — a purely explanatory job simply never opens one, so there
// is nothing left to gate on here). Done jobs stay tracked (a merge after
// carl accepts still deserves recording) — the tracker only stops when the
// job is archived, which removes the file from LoadJobs' namespace entirely.
// A never-started job is skipped only while it has no recorded PRs: kill
// returns a job to draft with its prs: history intact, and those records
// should keep refreshing.
func prTrackable(j *Job) bool {
	return j.Err == "" && (j.Started() || len(j.PRs) > 0)
}

// prIdleInterval/prIdleBatch pace the PR tracker over trackable jobs with NO
// live cell — done jobs awaiting archive, drafts killed back with PR history.
// Tracking a job costs at least one gh subprocess per pass, gh's PR queries
// bill against GitHub's hourly GraphQL point quota, and at the 15s tick a
// couple dozen trackable jobs saturate that quota by themselves — starving
// the PR watcher, the updater, and every other gh consumer on the machine,
// with each poll answering exactly what it answered 15 seconds earlier. A
// job with a live cell stays on the every-tick cadence (a merge should reach
// its zeus within seconds); an idle job's PRs move rarely and can wait
// prIdleInterval, at most prIdleBatch of them per tick — the same pacing
// shape, and the same constants, as tokensPass' idle throttle, for the same
// reason.
const (
	prIdleInterval = 10 * time.Minute
	prIdleBatch    = 8
)

// prScanDue reports whether an idle job's last PR poll (unix millis, zero =
// never polled) is old enough for another. Pure, so the pacing decision is
// table-testable without a tick loop.
func prScanDue(last, nowMS int64) bool {
	return last == 0 || nowMS-last >= prIdleInterval.Milliseconds()
}

// prunePRScan drops PRScan stamps for slugs no longer on the board —
// archiving frees a slug, and a stale stamp would make an unrelated later
// job of the same name wait out an interval it never earned. Pure over
// (d, jobs), same shape and reason as TokenScan's prune in tokensPass.
func (d *daemonState) prunePRScan(jobs []*Job) {
	present := map[string]bool{}
	for _, j := range jobs {
		present[j.Slug] = true
	}
	for slug := range d.PRScan {
		if !present[slug] {
			delete(d.PRScan, slug)
		}
	}
}

// trackPRs reconciles one job's recorded prs: list against what gh
// reports for the cell branch. Job-file writes, plus ONE tmux write — the
// merge bump into a live cell's zeus pane (mergeBumpMessage below); never
// git, never state. Every outcome worth noting gets a dlog line
// unconditionally (the old reap loop logged only on state change, which is
// how a failing teardown stayed invisible). all and live are the tick's
// discovery snapshot and live-session set, the same pair watchPRs takes.
func (d *daemonState) trackPRs(j *Job, all []*core.Peer, live map[string]bool) {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		// the state-derivation pass only covers live jobs — for done or
		// killed-back-to-draft jobs this is the sole surface for a bad repo
		if key := "repofail:" + j.Slug; d.Notified[key] == 0 {
			dlogErr("%s: resolve repo failed — leaving prs alone: %v", j.Slug, err)
			d.Notified[key] = time.Now().Unix()
		}
		return
	}
	delete(d.Notified, "repofail:"+j.Slug)
	dir := ExistingWorktree(j, repo)
	if dir == "" {
		dir = repo // worktree gone: still ask in the repo so history converges
	}
	fetched, detail, ok := listBranchPRs(dir, CellBranch(j))
	if !ok {
		// log the outage once per stretch, not every 15s tick — and say WHY
		// (rate limit, auth, network): the call itself swallows the error,
		// so this line is the outage's only diagnostic anywhere
		if key := "ghfail:" + j.Slug; d.Notified[key] == 0 {
			dlogErr("%s: gh pr list failed — leaving prs alone: %s", j.Slug, detail)
			d.Notified[key] = time.Now().Unix()
		}
		return
	}
	delete(d.Notified, "ghfail:"+j.Slug)
	// backstop: a recorded PR the branch-list query didn't return (recycled
	// branch, hand-opened against a different head) gets asked about
	// directly. A single-URL failure just skips that URL — it's not the
	// "gh is down" signal listBranchPRs' ok=false already handled above.
	for _, url := range needsPoll(j.PRs, fetched) {
		if state, ok := viewPRState(dir, url); ok {
			fetched = append(fetched, JobPR{URL: url, State: state})
		}
	}
	updated, newlyMerged, changed := reconcilePRs(j.PRs, fetched)
	if !changed {
		return
	}
	if err := writeJobPRs(j, updated); err != nil {
		dlogErr("%s: prs write failed: %v", j.Slug, err)
		return
	}
	for _, pr := range updated {
		dlog("%s: pr %s %s", j.Slug, pr.State, pr.URL)
	}
	if len(newlyMerged) > 0 {
		// The mental model's verify pass skips a repo whose origin/main head
		// has not moved since it last looked, and a merge is the one moment
		// kraken KNOWS it has. Marking the repo makes the next pass re-verify
		// it without waiting for the watermark comparison to notice.
		//
		// The key is the CHECKOUT's basename, not `j.Repo`. An anchor's repo
		// is the basename of its `git remote get-url origin` (falling back to
		// the worktree's), while a job's `repo:` field is matched by
		// ResolveRepo as a case-insensitive SUBSTRING of a directory name — so
		// `repo: mono` legitimately resolves to `~/Developer/monolith` whose
		// anchors are all stored as `monolith`, and marking the field would
		// name a repo no anchor group ever looks up. `repo` here is that
		// resolved directory, taken above.
		//
		// An accelerant, never the trigger of record: this only ever sees PRs
		// opened from kraken's own cell branches, so a push carl made from
		// anywhere else is still caught by the ordinary head comparison. That
		// is why the mark is not the mechanism — losing it costs latency, and
		// the pass would still be correct if this line were deleted.
		MarkMMRepoDirty(filepath.Base(repo))
	}
	var zeus *core.Peer
	zeusLookedUp := false
	for _, url := range newlyMerged {
		dlogEvent("info", "pr-merged", "%s: merged %s — cell not torn down", j.Slug, url)
		msg := "PR merged — accept from the board when the job is finished"
		if j.State == "done" {
			msg = "PR merged — job already accepted; recorded"
		}
		notify("kraken ✓ "+j.Slug, msg)
		// zeus' pane. Carl hears about the merge above; the
		// one party that did not was the agent still working the branch,
		// which has no other way to learn its PR closed — it keeps pushing
		// to the dead branch, re-running its checks and reporting it as
		// awaiting review. Best-effort like the notification beside it: the
		// merge is already on disk, there is no per-PR watermark to retry
		// from, and a cell with no live zeus (torn down, or the job idle)
		// has nobody to tell. The one send that fails deterministically is
		// a zeus blocked on an open question or permission prompt — the
		// pane core.SendMessage refuses to type into — so that cell learns
		// of the merge only from its own gh reads. A pr-watch bump hits the
		// same refusal but retries off its per-PR watermark once the prompt
		// closes; this path carries no such watermark, so the merge bump is
		// simply lost.
		if !mergeBumpWanted(j, live) {
			continue
		}
		if !zeusLookedUp {
			zeus = CellZeus(j, all)
			zeusLookedUp = true
		}
		if zeus == nil {
			continue
		}
		if _, err := core.SendToPeer(zeus, mergeBumpMessage(url)); err != nil {
			if key := "mergebumpfail:" + j.Slug; d.Notified[key] == 0 {
				dlogErr("%s: merge-bump send failed for %s: %v", j.Slug, url, err)
				d.Notified[key] = time.Now().Unix()
			}
			continue
		}
		delete(d.Notified, "mergebumpfail:"+j.Slug)
		dlogEvent("info", "pr-merge-bump", "%s: told zeus %s merged", j.Slug, url)
	}
}

// mergeBumpWanted gates the merge bump on the two facts that make a pane
// worth typing into: the job is in a state that HAS a working zeus
// (working or blocked — a draft has no cell, a done job's zeus has stopped
// and would only be woken to read old news) and its cell session is live
// this tick. Pure, so the gate is table-testable without a tick loop; the
// peer lookup and the send stay in trackPRs.
func mergeBumpWanted(j *Job, live map[string]bool) bool {
	if j.State != "working" && j.State != "blocked" {
		return false
	}
	return live[CellSession(j)]
}

// mergeBumpMessage renders the pane notice for one merged PR. It opens with
// prWatchPrefix for the reason that constant exists — a machine notice must
// never read as carl — and then says the three things a lead working a
// merged branch gets wrong without it: the branch is finished, so no push,
// no re-run and no "awaiting checks"; a follow-up starts from a recycled
// branch; and the job may or may not be done, which the lead decides. The
// closing clauses settle the gate question, because cells and PRs are
// many-to-one: the operator often merges the PR a manual-test gate names
// instead of replying to it, and a notice that only said "the gate still
// holds" would leave the gate's test services, stack chips and load estimate
// standing on a finished job, where a test stack's processes outlive the
// worktree they serve from. So a merge of the PR the gate names PASSES that
// gate and the lead shuts its stack down and stops, even when the job has
// more work (the operator's next prompt starts it); a gate for a LATER PR,
// or a question round, still holds, and the lead must not read "continue"
// as that gate opening. The gate clause comes before the continue-or-stop
// sentence so a lead at the gate reads one instruction, not two.
func mergeBumpMessage(url string) string {
	return prWatchPrefix() + " " + url + " has MERGED into its base branch. That branch is finished: do not push to it, re-run its checks, or report it as awaiting checks or review. Any further change starts on a fresh branch recycled off origin/main (the recycle step in do/SKILL.md's cell section). If your open manual-test gate names THIS PR, the merge PASSES that gate: shut down every test service you started for it, run `kraken stack clear <slug>` and `kraken load clear <slug>`, and stop even if the job has more work — the operator's next prompt starts it. Otherwise, if this PR completed the job's work, say so in your closing message and stop; if not, continue and open the follow-up from the recycled branch. A gate naming another PR, or a question round, still holds — this notice does not answer those."
}

// prWatchPrefix opens EVERY message the PR watcher types into a pane. It is
// one symbol on purpose: the four bump variants below each inline it into
// their format string, and a reword that reached three of four would leave
// one machine notice reading as the operator again — the hazard the prefix
// exists to close. The tests pin the wire text per-variant, independently of
// this function, so a drift there still fails. The name is Operator(), and
// every variant concatenates the prefix OUTSIDE its format string, so a name
// carrying a '%' is printed literally rather than read as a verb.
func prWatchPrefix() string {
	return "[kraken pr-watch — automated notice, not a reply from " + Operator() + "]"
}

// watchDecision is watchPRs' pure core: given one open PR's freshly fetched
// state and its currently recorded watermarks, decides whether to bump zeus'
// pane and what the new watermarks should be.
//
// A PR whose fetched state isn't (still) open never bumps and its watermarks
// pass through unchanged — trackPRs' own pass reconciles the recorded state;
// this just declines to act on stale-open data.
//
// First sight (both watermarks still empty) seeds LastCommentAt to the
// newest existing comment/review timestamp WITHOUT bumping — a PR's whole
// prior comment history must not flood zeus on the tick kraken starts
// watching it. A currently-failing check DOES bump even on first sight,
// since it's actionable right now regardless of history; BumpedSHA is seeded
// silently (no bump) on first sight only when checks are currently green.
//
// After first sight, a newer comment/review timestamp bumps, and a head SHA
// that differs from BumpedSHA while checks are failing bumps (once per SHA —
// an unchanged SHA never re-bumps for the same standing failure). All kinds
// of news in the same pass fold into one message. bump is false, and every
// watermark passes through unchanged, when there is nothing new to report.
//
// Merge conflicts are watermarked by transition, not by SHA: the common
// conflict arrives when the BASE moves under an unchanged head, so a
// SHA-keyed watermark would never fire for it. conflictState remembers the
// last decisive mergeability seen ("clean"/"conflicting", "" never seen);
// gh's UNKNOWN (mergeability still being computed after a push) leaves it
// untouched — never a bump, never a clear, so a standing conflict doesn't
// re-bump on an UNKNOWN→CONFLICTING recompute. A conflict bumps even on
// first sight (like a failing check, it's actionable right now); a clean
// fetch silently records "clean", re-arming the conflict bump.
func watchDecision(url string, f prWatchFetch, lastCommentAt, bumpedSHA, conflictState string) (bump bool, message string, newLastCommentAt, newBumpedSHA, newConflictState string) {
	newLastCommentAt, newBumpedSHA, newConflictState = lastCommentAt, bumpedSHA, conflictState
	if f.State != "open" {
		return false, "", newLastCommentAt, newBumpedSHA, newConflictState
	}
	firstSight := lastCommentAt == "" && bumpedSHA == ""

	var parts []string
	if f.LastCommentAt != "" {
		switch {
		case firstSight:
			newLastCommentAt = f.LastCommentAt // seed silently — no history flood
		case f.LastCommentAt > lastCommentAt:
			// The suggested command matters: `gh pr view --comments` shows
			// ISSUE comments only, and the comment that triggered this bump
			// is routinely an INLINE review comment it cannot show — two
			// cells have chased "left a comment" through the wrong command,
			// one finding only a bot notice where carl's review sat inline.
			// The PR number is substituted from the url ({owner}/{repo} gh
			// expands itself; a bare N it does not).
			parts = append(parts, prWatchPrefix()+" "+fmt.Sprintf(
				"%s got a new comment or review — read it with `gh pr view %s --comments`, and if nothing is there check the INLINE review comments (`gh api repos/{owner}/{repo}/pulls/%s/comments`, which `--comments` does not show); address it (reply or fix), and continue",
				url, url, url[strings.LastIndex(url, "/")+1:]))
			newLastCommentAt = f.LastCommentAt
		}
	}

	switch {
	// f.HeadSHA != "" guards both branches: an empty headRefOid (a gh
	// response gh itself didn't fully populate) must never bump with a
	// blank SHA in the message, and must never seed/advance BumpedSHA to ""
	// — doing so would make the NEXT pass's fetch look like first sight
	// again (bumpedSHA=="" is part of firstSight's definition).
	case len(f.FailingChecks) > 0 && f.HeadSHA != "" && f.HeadSHA != bumpedSHA:
		// The bracketed prefix and the inherited-red clause are both
		// load-bearing. This message arrives on the same user channel as
		// carl's own words, on a timer, with no knowledge of what the pane
		// last said — a bump landing minutes after zeus asked carl a yes/no
		// question has been read as carl's "yes" and bought a six-figure
		// detour of discarded cross-team work. And the bare imperative
		// "fix it" left no branch for a red inherited from origin/main: two
		// sibling cells each adopted the same cross-team breakage as their
		// own work the same evening, duplicating a fix hours apart.
		parts = append(parts, prWatchPrefix()+" "+fmt.Sprintf(
			"%s has failing CI on %s (at least %s) — run `gh pr checks %s` to see the full current rollup and fix it. If the failure reproduces on pristine origin/main it is inherited, not yours: note that on the PR and report it in your pane instead of adopting the cross-team fix",
			url, f.HeadSHA, strings.Join(f.FailingChecks, ", "), url))
		newBumpedSHA = f.HeadSHA
	case firstSight && f.HeadSHA != "":
		newBumpedSHA = f.HeadSHA // seed silently — checks are green right now
	}

	switch {
	case f.Mergeable == "conflicting" && conflictState != "conflicting":
		// Same provenance prefix as the comment and CI bumps above — the
		// conflict bump was the one PR-watch notice without it, arriving on
		// the same user channel with the same read-as-carl hazard the
		// comment block above the CI bump spells out, and a cell has
		// visibly hedged on whether this message was carl or a machine.
		//
		// The message states the GOAL (get the branch mergeable) rather
		// than prescribing rebase + --force-with-lease: on a branch that
		// already carries pushed merge commits and an open PR, a history
		// rewrite is the wrong move, and a lead correctly refused the old
		// prescription three times in one afternoon (merging instead, no
		// force needed, every time) — a canned bump the recipient must
		// overrule on every occurrence is a hazard, not advice.
		if f.BaseRef != "" {
			parts = append(parts, prWatchPrefix()+" "+fmt.Sprintf(
				"%s has merge conflicts with %s — get the branch mergeable: git fetch origin, then merge origin/%s into your branch (a merge commit is fine and needs no force-push; only rebase if the branch has no pushed history worth keeping), resolve the conflicts, verify the build still passes, push, and continue",
				url, f.BaseRef, f.BaseRef))
		} else {
			parts = append(parts, prWatchPrefix()+" "+fmt.Sprintf(
				"%s has merge conflicts with its base branch — get the branch mergeable: fetch, merge the base branch in (a merge commit is fine and needs no force-push; only rebase if the branch has no pushed history worth keeping), resolve the conflicts, verify the build still passes, push, and continue",
				url))
		}
		newConflictState = "conflicting"
	case f.Mergeable == "mergeable":
		newConflictState = "clean" // silent — re-arms the conflict bump
	}
	// any other Mergeable value (unknown, "") leaves conflictState untouched

	return len(parts) > 0, strings.Join(parts, "; "), newLastCommentAt, newBumpedSHA, newConflictState
}

// watchPRs polls each open, recorded PR of an active (working/blocked) job
// for new comments/reviews, failing CI, and merge conflicts, bumping zeus'
// pane once per new comment-or-review timestamp, once per newly-failing head
// SHA, and once per fresh clean→conflicting mergeability transition
// (combined into a single message when several land in the same pass).
// Watermark writes only happen after a successful SendToPeer — a failed send
// leaves the watermark untouched so the next pass retries the same bump,
// matching trackPRs' "stored state only advances on a confirmed write"
// discipline. Skipped entirely (no gh calls) for jobs that aren't
// prTrackable (same Err eligibility as the tracker's own gate: an
// unparseable job's PR list is whatever a strict parse salvaged, not the
// job's record), jobs with no live cell, no working/blocked state, or no recorded open PR —
// with the tracker running every 15s tick, this gate is what bounds gh
// traffic to jobs that could plausibly need a bump right now.
func (d *daemonState) watchPRs(j *Job, all []*core.Peer, live map[string]bool) {
	if !prTrackable(j) {
		return
	}
	if j.State != "working" && j.State != "blocked" {
		return
	}
	if !live[CellSession(j)] {
		return
	}
	var open []JobPR
	for _, pr := range j.PRs {
		if pr.State == "open" {
			open = append(open, pr)
		}
	}
	if len(open) == 0 {
		return
	}
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return // trackPRs already surfaces a bad repo for this job this pass
	}
	dir := ExistingWorktree(j, repo)
	if dir == "" {
		dir = repo // worktree gone: still ask in the repo, same fallback as trackPRs
	}
	var zeus *core.Peer
	zeusLookedUp := false
	for _, pr := range open {
		fetched, detail, ok := viewPRActivity(dir, pr.URL)
		if !ok {
			// log the outage once per stretch, not every 15s tick — same
			// dedup shape as trackPRs' ghfail key, and the same "say WHY"
			// rule: this line is the fetch failure's only diagnostic
			if key := "prwatchfail:" + j.Slug; d.Notified[key] == 0 {
				dlogErr("%s: pr-watch fetch failed for %s — leaving watermarks alone: %s", j.Slug, pr.URL, detail)
				d.Notified[key] = time.Now().Unix()
			}
			continue // gh failure: leave this PR's watermarks alone, retry next pass
		}
		delete(d.Notified, "prwatchfail:"+j.Slug)
		bump, msg, newLastCommentAt, newBumpedSHA, newConflictState := watchDecision(pr.URL, fetched, pr.LastCommentAt, pr.BumpedSHA, pr.ConflictState)
		if newLastCommentAt == pr.LastCommentAt && newBumpedSHA == pr.BumpedSHA && newConflictState == pr.ConflictState {
			continue // nothing changed, not even a first-sight seed
		}
		if !bump {
			// seed-only: persist immediately, no cell message involved
			if err := updatePRWatermark(j, pr.URL, newLastCommentAt, newBumpedSHA, newConflictState); err != nil {
				dlogErr("%s: pr-watch seed write failed: %v", j.Slug, err)
			}
			continue
		}
		if !zeusLookedUp {
			zeus = CellZeus(j, all)
			zeusLookedUp = true
		}
		if zeus == nil {
			continue // no live zeus peer to bump — retry next pass
		}
		if _, err := core.SendToPeer(zeus, msg); err != nil {
			// dedup like the gh-fetch failure above: a question-blocked cell
			// would otherwise write ~4 identical error lines per minute
			// overnight while carl is asleep
			if key := "bumpfail:" + j.Slug; d.Notified[key] == 0 {
				dlogErr("%s: pr-bump send failed: %v", j.Slug, err)
				d.Notified[key] = time.Now().Unix()
			}
			continue // watermark stays put on disk — retried next pass
		}
		delete(d.Notified, "bumpfail:"+j.Slug)
		if err := updatePRWatermark(j, pr.URL, newLastCommentAt, newBumpedSHA, newConflictState); err != nil {
			dlogErr("%s: pr-watch watermark write failed: %v", j.Slug, err)
			continue
		}
		dlogEvent("info", "pr-bump", "%s: bumped zeus for %s: %s", j.Slug, pr.URL, msg)
	}
}

// syncColor is the colour reconcile's per-job step, a sibling of watchPRs
// above and reduced to the same shape: every gate, the send, and the
// watermark live in syncCellColor (color.go), and all this owns is the
// logging. The success line reports the arg syncCellColor actually TYPED
// rather than re-deriving the ""→"default" rule, so the log can never
// disagree with the pane.
func (d *daemonState) syncColor(j *Job, all []*core.Peer, live map[string]bool) {
	sent, arg, err := syncCellColor(j, all, live)
	if err != nil {
		// dedup like watchPRs' bumpfail key: a question-blocked cell would
		// otherwise write ~4 identical lines a minute overnight while carl
		// is asleep
		if key := "colorfail:" + j.Slug; d.Notified[key] == 0 {
			dlogErr("%s: color sync failed: %v", j.Slug, err)
			d.Notified[key] = time.Now().Unix()
		}
		return
	}
	delete(d.Notified, "colorfail:"+j.Slug)
	if sent {
		dlogEvent("info", "color-sync", "%s: /color %s → zeus", j.Slug, arg)
	}
}

// scanTranscriptPRs is the transcript-scanning backstop: it looks for GitHub
// PR URLs zeus' own transcript mentions, in the job's own repo ONLY, and
// attaches any not yet recorded. Unlike trackPRs (which asks gh, so an
// unreachable API or bad repo just means "leave prs alone, retry next
// pass"), and unlike pr-hook (which fails open with an empty prefix when the
// origin can't be read, since the command it saw really did run in the
// cell's worktree), this has no such anchor — a transcript can quote PR URLs
// from anywhere — so an unresolved repo prefix means scan nothing (carl's Q7
// call). Job-file writes only, same as trackPRs; every attach gets one dlog
// line, and — matching trackPRs' repofail dedup — failures degrade silently.
func (d *daemonState) scanTranscriptPRs(j *Job, all []*core.Peer) {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return // trackPRs already surfaces a bad repo for this job this pass
	}
	prefix, ok := repoOriginPrefix(repo)
	if !ok || prefix == "" {
		return // no fail-open here: an unresolved prefix means scan nothing
	}
	transcript := zeusTranscriptPath(j, all)
	if transcript == "" {
		return // no live/stamped session — not an error, just nothing to scan
	}
	for _, url := range transcriptPRURLs(transcript, prefix) {
		attached, err := appendJobPR(j, JobPR{URL: url, State: "open"})
		switch {
		case err != nil:
			dlog("%s: transcript attach %s failed: %v", j.Slug, url, err)
		case attached:
			dlog("%s: attached from transcript %s", j.Slug, url)
		}
	}
}

// repairPRs is a cleanup of PR records the historical raw-text transcript
// scan mis-attached to the wrong job: it used to scan zeus' whole transcript
// for anything that looked like a PR URL under the repo, so a PR another job
// (or a human) opened against an unrelated branch could land in this job's
// prs: list. That scan is now gone in favor of gh-pr-create-tool-call
// attachment only, but the pollution it already wrote survives on disk until
// this pass removes it.
//
// Criterion: a recorded PR is dropped only when it is BOTH not create-vouched
// (the transcript's own gh-pr-create evidence, via transcriptPRURLs — now
// tool-call-only) AND its head branch is confirmed to be outside the job's
// own branchFamily. The vouch side is TRI-STATE, not "empty means no
// creates": an empty createVouched set is trustworthy evidence of "zero
// creates" ONLY when the transcript was actually read. If the repo can't be
// resolved, the origin prefix can't be resolved, there's no transcript path,
// or the transcript file fails to stat, that's an absent vouch, not a
// negative one — the pass keeps every record for this job untouched, marks
// itself incomplete, and skips every gh call (nothing could be safely
// dropped anyway). Concretely: job 2nd-kraken-status-investigation's
// pull/27 is a genuine zeus create from a non-family branch, vouched for
// only by its transcript — if that transcript ever went missing, treating
// the resulting empty set as "no creates" would silently delete a
// legitimate record with no recovery path.
//
// Residual risk: the vouch reads exactly one transcript — the live zeus
// peer's, or else the job's stamped session — so a legitimate non-family
// create from an EARLIER session of a reactivated/re-stamped cell would be
// unvouched by that read and, being non-family, dropped anyway. Accepted:
// verified absent from current data.
//
// Repeating the whole pass after a daemon restart can only ever find nothing
// left to drop, never re-drop a legitimate record — create-vouched and
// family-branch records are never candidates, and now, with the tri-state
// vouch rule, missing evidence never drops anything either. d.Repaired only
// exists to skip redundant gh calls within one process's lifetime; d.offline
// and RepairTries additionally bound how much gh traffic an unresolvable job
// can cost.
func (d *daemonState) repairPRs(j *Job, all []*core.Peer) {
	if d.Repaired[j.Slug] {
		return
	}
	if d.offline {
		return // outage amplification guard: gh calls would just fail/hang during an outage
	}
	if len(j.PRs) == 0 {
		d.Repaired[j.Slug] = true
		return
	}

	repo, resolveErr := ResolveRepo(j.Repo)
	var dir string
	if resolveErr == nil {
		dir = ExistingWorktree(j, repo)
		if dir == "" {
			dir = repo // worktree gone: still ask in the repo, same fallback as trackPRs
		}
	}

	// create-vouched set: tri-state (see doc comment above). vouchedOK is
	// true only once the transcript was actually stat'able — repo resolves,
	// origin prefix resolves, a transcript path exists, and the file itself
	// is readable. Anything short of that leaves createVouched meaningless,
	// so it must not be treated as "vouches for nothing".
	createVouched := map[string]bool{}
	vouchedOK := false
	if resolveErr == nil {
		if prefix, ok := repoOriginPrefix(repo); ok && prefix != "" {
			if transcript := zeusTranscriptPath(j, all); transcript != "" {
				if _, statErr := os.Stat(transcript); statErr == nil {
					vouchedOK = true
					for _, url := range transcriptPRURLs(transcript, prefix) {
						createVouched[url] = true
					}
					// A PR of another repo that the spec names was attached on
					// purpose (api/prattach.go's carve-out); the transcript scan
					// above is narrowed to the job's own repo and can never
					// vouch for it, so it would be dropped here on the first
					// pass after every restart. The own-repo exclusion needs the
					// NORMALISED prefix (githubPrefix: userinfo stripped), not the
					// raw one the transcript scan uses — a `user@github.com/o/r`
					// origin would otherwise never match its own PRs and the
					// exclusion would silently admit them.
					own, _ := githubPrefix(prefix)
					for _, url := range specNamedPRs(j, own) {
						createVouched[url] = true
					}
				}
			}
		}
	}
	if !vouchedOK {
		d.repairGiveUpOrRetry(j, false)
		return
	}

	// d.PRHeads memoizes headOf's successful lookups across jobs and passes
	// — see the PRHeads field doc. The drop log line below reads straight
	// from it instead of a second gh call.
	headOf := func(url string) (string, bool) {
		if dir == "" {
			return "", false
		}
		if head, ok := d.PRHeads[url]; ok {
			return head, true
		}
		head, ok := viewPRHead(dir, url)
		if ok {
			d.PRHeads[url] = head
		}
		return head, ok
	}

	family := branchFamily(j)
	inFamily := func(branch string) bool { return family[branch] }
	_, dropped, complete := repairFilter(j.PRs, createVouched, vouchedOK, headOf, inFamily)
	if len(dropped) == 0 {
		d.repairGiveUpOrRetry(j, complete)
		return
	}

	dropSet := make(map[string]bool, len(dropped))
	for _, url := range dropped {
		dropSet[url] = true
	}
	removed, err := removeJobPRs(j, dropSet)
	if err != nil {
		if key := "repairfail:" + j.Slug; d.Notified[key] == 0 {
			dlogErr("%s: pr-repair write failed: %v", j.Slug, err)
			d.Notified[key] = time.Now().Unix()
		}
		return // leave d.Repaired unset — retry the write next PR pass
	}
	delete(d.Notified, "repairfail:"+j.Slug)
	if len(removed) > 0 {
		for _, url := range removed {
			dlog("%s: repair dropped %s (head %s)", j.Slug, url, d.PRHeads[url])
		}
		dlogEvent("info", "pr-repair", "%s: repair dropped %d foreign PR record(s)", j.Slug, len(removed))
	}
	d.repairGiveUpOrRetry(j, complete)
}

// repairGiveUpOrRetry records the outcome of one repair pass for j: complete
// marks the job fully repaired (no further gh calls, ever, for this
// process). Otherwise it counts the incomplete attempt and, once RepairTries
// hits the cap, gives up anyway WITHOUT dropping anything — a job whose
// evidence never resolves (permanently unreachable repo, permanently gone
// transcript) would otherwise cost one gh round-trip attempt every PR-
// tracker tick forever. Giving up is always safe because it never drops a
// record; it only stops spending gh calls on one that can't be verified.
func (d *daemonState) repairGiveUpOrRetry(j *Job, complete bool) {
	if complete {
		d.Repaired[j.Slug] = true
		delete(d.RepairTries, j.Slug)
		return
	}
	d.RepairTries[j.Slug]++
	if d.RepairTries[j.Slug] >= 3 {
		d.Repaired[j.Slug] = true
		dlogEvent("info", "pr-repair", "%s: giving up after 3 tries — records kept", j.Slug)
	}
}

// repairFilter is repairPRs' pure decision: which recorded PRs survive.
// vouchedOK gates the whole thing — when false, the transcript-based
// evidence is meaningless (couldn't be read), so EVERY record is kept
// untouched, headOf is never called, and complete is unconditionally false
// so the caller retries. When vouchedOK is true, a record is dropped only
// when BOTH createVouched doesn't cover it AND headOf resolves to a branch
// outside family — an unknown headOf (ok=false) always keeps the record and
// flips complete to false, so the caller knows to retry rather than treat
// this job as fully repaired. Table-tested without gh via fake headOf/family
// closures.
func repairFilter(recorded []JobPR, createVouched map[string]bool, vouchedOK bool, headOf func(url string) (string, bool), family func(branch string) bool) (kept []JobPR, dropped []string, complete bool) {
	if !vouchedOK {
		return recorded, nil, false
	}
	complete = true
	for _, r := range recorded {
		if createVouched[r.URL] {
			kept = append(kept, r)
			continue
		}
		head, ok := headOf(r.URL)
		if !ok {
			kept = append(kept, r)
			complete = false
			continue
		}
		if family(head) {
			kept = append(kept, r)
			continue
		}
		dropped = append(dropped, r.URL)
	}
	return kept, dropped, complete
}

// reconcilePRs merges freshly fetched PR states into the recorded list.
// Pure, so it's table-testable. Recorded entries never disappear (the list
// is job history — gh may stop returning PRs from recycled -rN branches);
// unseen fetched entries append in fetch order. newlyMerged carries the
// URLs whose recorded state just became merged — including first-sight
// merges (a PR that merged before kraken ever saw it still deserves its one
// notification). Dedup across daemon restarts is the recorded state itself.
func reconcilePRs(recorded, fetched []JobPR) (out []JobPR, newlyMerged []string, changed bool) {
	out = append(out, recorded...)
	index := map[string]int{}
	for i, p := range out {
		index[p.URL] = i
	}
	for _, f := range fetched {
		i, seen := index[f.URL]
		if !seen {
			out = append(out, f)
			index[f.URL] = len(out) - 1
			changed = true
			if f.State == "merged" {
				newlyMerged = append(newlyMerged, f.URL)
			}
			continue
		}
		if out[i].State == f.State {
			continue
		}
		if f.State == "merged" && out[i].State != "merged" {
			newlyMerged = append(newlyMerged, f.URL)
		}
		out[i].State = f.State
		changed = true
	}
	return out, newlyMerged, changed
}
