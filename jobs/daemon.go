package jobs

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"kraken/core"
	"kraken/util"
)

// k daemon — the one long-lived kraken process (launchd-managed, installed by
// make install). It hosts the scheduler poll: queue startable jobs under the
// cell cap, derive each started job's working/blocked state from live pod
// activity, watch cells for unhealthy zeuses (auto-retrying with backoff),
// watch PRs for auto-reap, and write a heartbeat the viewer renders. It owns
// no state that can't be re-derived from files + discovery — the in-memory
// dedup maps only suppress duplicate notifications, and losing them on
// restart merely re-notifies once.

const (
	CellCap        = 15
	DaemonInterval = 15 * time.Second
	// PR tracking is throttled to every reapEvery-th tick (~1 min): gh
	// calls per open cell are the daemon's only rate-limited traffic.
	reapEvery = 4
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
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
	LastTick  int64  `json:"last_tick"`
	LastError string `json:"last_error,omitempty"`
	Cells     int    `json:"cells"`
	// Stale is this daemon process' own self-check (see selfcheck.go) — true
	// once make install has replaced the binary on disk out from under it.
	Stale bool `json:"stale,omitempty"`
}

func daemonStatusPath() string {
	return filepath.Join(util.Home(), ".kraken", "daemon.json")
}

func ReadDaemonStatus() (DaemonStatus, bool) {
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
	tickN       int
	// offline: in-memory only, like the dedup maps above — losing it on
	// restart just costs one duplicate "offline" notification. Set while
	// probeOnline fails so auto-retry pokes are suppressed instead of eaten
	// by the same 529/network outage they're meant to survive.
	offline bool
}

func newDaemon() *daemonState {
	return &daemonState{
		Notified:    map[string]int64{},
		Poked:       map[string]int64{},
		Repaired:    map[string]bool{},
		PRHeads:     map[string]string{},
		RepairTries: map[string]int{},
	}
}

// usagePassRunning single-flights UsagePass across ticks: it does network I/O
// up to ~10s and must never overlap itself or stretch the scheduler loop.
var usagePassRunning atomic.Bool

// RunDaemon runs the scheduler loop in the foreground: queue startable jobs,
// derive live state, watch for unhealthy cells, track PRs, write a heartbeat.
// Never returns.
func RunDaemon() {
	if st, ok := ReadDaemonStatus(); ok && st.PID != os.Getpid() && ProcessAlive(st.PID) {
		util.Die("daemon already running (pid %d)", st.PID)
	}
	if err := os.MkdirAll(filepath.Dir(daemonStatusPath()), 0o755); err != nil {
		util.Die("mkdir %s: %v", filepath.Dir(daemonStatusPath()), err)
	}
	util.InitSelfCheck()
	d := newDaemon()
	st := DaemonStatus{PID: os.Getpid(), StartedAt: time.Now().Unix()}
	dlogEvent("info", "daemon-start", "daemon up (pid %d, cap %d cells, tick %s)", st.PID, CellCap, DaemonInterval)
	// launchd stops the daemon with SIGTERM (make install's kickstart -k,
	// logout, unload); log the shutdown so the event log shows every daemon
	// lifecycle edge, not just the starts
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigc
		dlogEvent("info", "daemon-stop", "daemon stopping (pid %d, %v)", os.Getpid(), s)
		os.Exit(0)
	}()
	// TCC watchdog: reading ~/Documents from a launchd process blocks in
	// open() until macOS consent is granted — and every reinstall changes the
	// binary's code hash, revoking the previous grant. A silently-hung first
	// tick must get loud (notify doesn't touch protected paths, so it works
	// even while blocked).
	firstTick := make(chan struct{})
	go func() {
		select {
		case <-firstTick:
		case <-time.After(45 * time.Second):
			dlog("first tick still not done after 45s — likely blocked on a macOS Documents-access prompt (TCC re-consent after reinstall)")
			notify("kraken daemon blocked", "approve the Documents-access dialog (or grant Full Disk Access) — reinstalls reset it")
		}
	}()
	first := true
	for {
		cells, err := d.tick()
		if first {
			close(firstTick)
			first = false
		}
		st.LastTick, st.Cells, st.LastError, st.Stale = time.Now().Unix(), cells, "", util.IsSelfStale()
		if err != nil {
			st.LastError = err.Error()
			dlogEvent("error", "tick-error", "tick error: %v", err)
		}
		if data, err := json.Marshal(st); err == nil {
			_ = util.AtomicWrite(daemonStatusPath(), string(data))
		}
		time.Sleep(DaemonInterval)
	}
}

// Tick is one scheduler pass. Each concern is independent: an error in one
// (reported via return for the status file) never blocks the others.
func (d *daemonState) tick() (int, error) {
	d.tickN++
	jobs := LoadJobs()
	live := LiveSessions()
	bySlug := map[string]*Job{}
	for _, j := range jobs {
		bySlug[j.Slug] = j
	}

	// unparseable jobs: one notification per file+mtime, the board carries
	// the detail
	for _, j := range jobs {
		if j.Err == "" {
			continue
		}
		key := "err:" + j.File
		if mt := j.ModTime.Unix(); d.Notified[key] != mt {
			notify("kraken: job error", j.Slug+": "+j.Err)
			d.Notified[key] = mt
		}
	}

	all, _ := core.OrderAll(core.DiscoverAll())
	times := liveSessionTimes()
	now := time.Now()
	cells := 0
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
		notify(title, msg)
	}
	for _, j := range jobs {
		if !j.Started() || j.State == "done" {
			continue
		}
		isLive := live[CellSession(j)]
		if isLive {
			cells++
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
				if err := ResumeTeardown(j); err != nil {
					dlogErr("%s: finish interrupted teardown failed: %v", j.Slug, err)
				} else {
					dlog("%s: finished interrupted teardown (%s)", j.Slug, j.Teardown)
				}
				continue
			}
			// dead-cell watch: a started job whose tmux session vanished
			// (killed by hand, tmux server restart) would otherwise sit on
			// its file state forever, looking healthy in the state column —
			// the other half of the demo-job incident. Detection only:
			// recovery stays carl's explicit reactivate/done/kill — reactivate
			// is the one that preserves the worktree's uncommitted work.
			key := "gone:" + j.Slug
			if d.Notified[key] == 0 {
				notify("kraken ⚠ "+j.Slug, "cell session is gone — reactivate to resume; done/kill discard the worktree")
				dlogEvent("error", "cell-gone", "%s: cell %s is gone (was %s)", j.Slug, CellSession(j), j.State)
				d.Notified[key] = now.Unix()
			}
		}

		// health first, independent of state derivation below: an API-errored
		// or half-dead cell needs carl's retry regardless of what state it
		// reads as. Idle-stall dedup: the reason embeds the rounded idle
		// duration, so key on the prefix or every tick re-notifies with a
		// fresh number.
		if isLive {
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
			// surfaces on the board as repo?; notify once per file version
			key := "repo:" + j.File
			if mt := j.ModTime.Unix(); d.Notified[key] != mt {
				notify("kraken: job error", j.Slug+": "+err.Error())
				d.Notified[key] = mt
			}
			continue
		}
		active := false
		if isLive {
			// a session just created is at least as fresh as its own
			// creation — covers pre-first-transcript startup before any pane
			// or team-config signal exists yet
			if created, ok := times[CellSession(j)]; ok && now.Sub(created) < sessionCreateGrace {
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
				active = cellActive(CellSession(j), all, core.TeamMembersForLead(sessionID), core.PaneShowsQuestion)
			}
		}
		st := deriveState(j, isLive, active)
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
					notify("kraken ⏸ "+j.Slug, "blocked — see the board")
					d.Notified[key] = now.Unix()
				}
			}
		}
	}

	// PR tracker (throttled): record every PR shipped from a cell branch
	// into the job's prs: list. A merge is a signal — one notification and
	// a board chip — NEVER a teardown trigger; teardown lives only behind
	// carl's explicit accept (k done / board Done). Tracking continues
	// through done — a merge after carl accepts still deserves recording —
	// and stops only when the job is archived (autoArchivePass below drops
	// it from LoadJobs' namespace entirely).
	if d.tickN%reapEvery == 0 {
		for _, j := range jobs {
			if !prTrackable(j) {
				continue
			}
			d.trackPRs(j)
			d.scanTranscriptPRs(j, all)
			d.repairPRs(j, all)
		}
		// PR watcher (same throttled cadence, its own pass after the tracker
		// above so every job's j.PRs is fully reconciled — state transitions,
		// transcript attaches, repair drops — before watchPRs reads it): bump
		// zeus' pane when an open PR of an active job gets a new comment/
		// review or a newly-failing CI check.
		for _, j := range jobs {
			d.watchPRs(j, all, live)
		}
	}

	// usage poll (throttled, first-tick eager so UIs have data right after
	// daemon start): single-flighted into its own goroutine since it's
	// network I/O up to ~10s and must not stretch this loop.
	if d.tickN == 1 || d.tickN%usageEvery == 0 {
		if usagePassRunning.CompareAndSwap(false, true) {
			go func() {
				defer usagePassRunning.Store(false)
				if err := UsagePass(); err != nil {
					dlog("usage pass failed: %v", err)
				}
			}()
		}
	}

	// auto-archive: done jobs left untouched for a day sweep themselves into
	// the archive, so the board doesn't accumulate accepted work forever.
	// !live is the same safety net ArchiveJob itself re-checks — a job
	// stamped done but whose cell somehow lingers is left alone here.
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

	// queue: start startable jobs under the cap. StartCell writes "working"
	// itself once the cell is up.
	var firstErr error
	for _, j := range jobs {
		if cells >= CellCap {
			break
		}
		if !startable(j, bySlug) {
			continue
		}
		repo, err := ResolveRepo(j.Repo)
		if err != nil {
			// surfaces on the board as repo?; notify once per file version
			key := "repo:" + j.File
			if mt := j.ModTime.Unix(); d.Notified[key] != mt {
				notify("kraken: job error", j.Slug+": "+err.Error())
				d.Notified[key] = mt
			}
			continue
		}
		if err := StartCell(j, repo); err != nil {
			dlogEvent("error", "start-failed", "start %s failed: %v", j.Slug, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		cells++
		dlogEvent("info", "cell-start", "started %s (cell %s, %d/%d cells)", j.Slug, CellSession(j), cells, CellCap)
		notify("kraken ▶ "+j.Slug, "cell started")
	}
	return cells, firstErr
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
// parse-clean build job that could have shipped PRs. Done jobs stay
// tracked (a merge after carl accepts still deserves recording) — the
// tracker only stops when the job is archived, which removes the file
// from LoadJobs' namespace entirely. A never-started job is skipped
// only while it has no recorded PRs: kill returns a job to draft with
// its prs: history intact, and those records should keep refreshing.
func prTrackable(j *Job) bool {
	return j.Err == "" && j.Type == "build" && (j.Started() || len(j.PRs) > 0)
}

// trackPRs reconciles one build job's recorded prs: list against what gh
// reports for the cell branch. Job-file writes only — never tmux, git, or
// state. Every outcome worth noting gets a dlog line unconditionally (the
// old reap loop logged only on state change, which is how a failing
// teardown stayed invisible).
func (d *daemonState) trackPRs(j *Job) {
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
	fetched, ok := listBranchPRs(dir, CellBranch(j))
	if !ok {
		// log the outage once per stretch, not every minute
		if key := "ghfail:" + j.Slug; d.Notified[key] == 0 {
			dlogErr("%s: gh pr list failed — leaving prs alone", j.Slug)
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
	for _, url := range newlyMerged {
		dlogEvent("info", "pr-merged", "%s: merged %s — cell left alone", j.Slug, url)
		msg := "PR merged — accept from the board when the job is finished"
		if j.State == "done" {
			msg = "PR merged — job already accepted; recorded"
		}
		notify("kraken ✓ "+j.Slug, msg)
	}
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
// an unchanged SHA never re-bumps for the same standing failure). Both kinds
// of news in the same pass fold into one message. bump is false, and both
// watermarks pass through unchanged, when there is nothing new to report.
func watchDecision(url string, f prWatchFetch, lastCommentAt, bumpedSHA string) (bump bool, message string, newLastCommentAt, newBumpedSHA string) {
	newLastCommentAt, newBumpedSHA = lastCommentAt, bumpedSHA
	if f.State != "open" {
		return false, "", newLastCommentAt, newBumpedSHA
	}
	firstSight := lastCommentAt == "" && bumpedSHA == ""

	var parts []string
	if f.LastCommentAt != "" {
		switch {
		case firstSight:
			newLastCommentAt = f.LastCommentAt // seed silently — no history flood
		case f.LastCommentAt > lastCommentAt:
			parts = append(parts, fmt.Sprintf(
				"%s got a new comment or review — read it with `gh pr view %s --comments`, address it (reply or fix), and continue",
				url, url))
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
		parts = append(parts, fmt.Sprintf(
			"%s has failing CI on %s (at least %s) — run `gh pr checks %s` to see the full current rollup and fix it",
			url, f.HeadSHA, strings.Join(f.FailingChecks, ", "), url))
		newBumpedSHA = f.HeadSHA
	case firstSight && f.HeadSHA != "":
		newBumpedSHA = f.HeadSHA // seed silently — checks are green right now
	}

	return len(parts) > 0, strings.Join(parts, "; "), newLastCommentAt, newBumpedSHA
}

// watchPRs polls each open, recorded PR of an active (working/blocked) job
// for new comments/reviews and failing CI, bumping zeus' pane once per new
// comment-or-review timestamp and once per newly-failing head SHA (combined
// into a single message when both land in the same pass). Job-file
// watermark writes only happen after a successful SendToPeer — a failed send
// leaves the watermark untouched so the next pass retries the same bump,
// matching trackPRs' "state on disk only advances on a confirmed write"
// discipline. Skipped entirely (no gh calls) for jobs that aren't
// prTrackable (same Err/Type eligibility as the tracker's own gate): an
// unparseable job must never have its prs: block rewritten, since
// updatePRWatermark's patchPRs pass rewrites the WHOLE block from a fresh
// re-parse and would silently and permanently drop any record a hand-edit
// typo currently makes unparseable, clearing the error in the process), jobs
// with no live cell, no working/blocked state, or no recorded open PR — the
// throttled reapEvery cadence already bounds gh traffic; this bounds it
// further to jobs that could plausibly need a bump right now.
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
		fetched, ok := viewPRActivity(dir, pr.URL)
		if !ok {
			// log the outage once per stretch, not every minute — same
			// dedup shape as trackPRs' ghfail key
			if key := "prwatchfail:" + j.Slug; d.Notified[key] == 0 {
				dlogErr("%s: pr-watch fetch failed for %s — leaving watermarks alone", j.Slug, pr.URL)
				d.Notified[key] = time.Now().Unix()
			}
			continue // gh failure: leave this PR's watermarks alone, retry next pass
		}
		delete(d.Notified, "prwatchfail:"+j.Slug)
		bump, msg, newLastCommentAt, newBumpedSHA := watchDecision(pr.URL, fetched, pr.LastCommentAt, pr.BumpedSHA)
		if newLastCommentAt == pr.LastCommentAt && newBumpedSHA == pr.BumpedSHA {
			continue // nothing changed, not even a first-sight seed
		}
		if !bump {
			// seed-only: persist immediately, no cell message involved
			if err := updatePRWatermark(j, pr.URL, newLastCommentAt, newBumpedSHA); err != nil {
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
			// would otherwise write ~1 identical error line per minute
			// overnight while carl is asleep
			if key := "bumpfail:" + j.Slug; d.Notified[key] == 0 {
				dlogErr("%s: pr-bump send failed: %v", j.Slug, err)
				d.Notified[key] = time.Now().Unix()
			}
			continue // watermark stays put on disk — retried next pass
		}
		delete(d.Notified, "bumpfail:"+j.Slug)
		if err := updatePRWatermark(j, pr.URL, newLastCommentAt, newBumpedSHA); err != nil {
			dlogErr("%s: pr-watch watermark write failed: %v", j.Slug, err)
			continue
		}
		dlogEvent("info", "pr-bump", "%s: bumped zeus for %s: %s", j.Slug, pr.URL, msg)
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
