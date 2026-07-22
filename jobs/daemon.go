package jobs

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		dlog("osascript notify failed: %v", err)
	}
}

func dlog(format string, a ...any) {
	fmt.Printf("%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
}

// daemonState holds the scheduler's cross-tick dedup state. newDaemon constructs
// one; tick runs a single scheduler pass.
type daemonState struct {
	Notified map[string]int64 // dedup key (err/repo file mtime, or "blocked:"/"health:" slug) → notified marker
	Poked    map[string]int64 // "retry:" + slug → unix time last auto-retried
	tickN    int
}

func newDaemon() *daemonState {
	return &daemonState{Notified: map[string]int64{}, Poked: map[string]int64{}}
}

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
	dlog("daemon up (pid %d, cap %d cells, tick %s)", st.PID, CellCap, DaemonInterval)
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
			dlog("tick error: %v", err)
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
				dlog("session stamp %s failed: %v", j.Slug, err)
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
					dlog("%s: finish interrupted teardown failed: %v", j.Slug, err)
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
				dlog("%s: cell %s is gone (was %s)", j.Slug, CellSession(j), j.State)
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
					notify("kraken ⚠ "+j.Slug, reason+" — auto-retrying; also on the board")
					d.Notified[key] = now.Unix()
					dlog("unhealthy %s: %s", j.Slug, reason)
				}
				// auto-retry retryable stalls with backoff: API overload comes
				// in waves, and a manual retry during one just eats the next
				// 529 — keep nudging every repokeAfter until a turn survives
				if !strings.Contains(reason, "is gone") {
					rkey := "retry:" + j.Slug
					if now.Unix()-d.Poked[rkey] >= int64(repokeAfter/time.Second) {
						if err := RetryCell(j); err != nil {
							dlog("auto-retry %s failed: %v", j.Slug, err)
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
				dlog("write state %s for %s failed: %v", st, j.Slug, err)
				continue
			}
			dlog("%s: %s → %s", j.Slug, prev, st)
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
	// carl's explicit accept (k done / board Done).
	if d.tickN%reapEvery == 0 {
		for _, j := range jobs {
			if j.Err != "" || j.Type != "build" || !j.Started() || j.State == "done" {
				continue
			}
			d.trackPRs(j)
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
			dlog("start %s failed: %v", j.Slug, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		cells++
		dlog("started %s (cell %s, %d/%d cells)", j.Slug, CellSession(j), cells, CellCap)
		notify("kraken ▶ "+j.Slug, "cell started")
	}
	return cells, firstErr
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
			dlog("auto-archive %s failed: %v", j.Slug, err)
			continue
		}
		dlog("%s: auto-archived as %s (done > 24h)", j.Slug, name)
		archived = append(archived, j.Slug)
	}
	return archived
}

// trackPRs reconciles one build job's recorded prs: list against what gh
// reports for the cell branch. Job-file writes only — never tmux, git, or
// state. Every outcome worth noting gets a dlog line unconditionally (the
// old reap loop logged only on state change, which is how a failing
// teardown stayed invisible).
func (d *daemonState) trackPRs(j *Job) {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return // surfaced already by the state-derivation pass this tick
	}
	dir := ExistingWorktree(j, repo)
	if dir == "" {
		dir = repo // worktree gone: still ask in the repo so history converges
	}
	fetched, ok := listBranchPRs(dir, CellBranch(j))
	if !ok {
		// log the outage once per stretch, not every minute
		if key := "ghfail:" + j.Slug; d.Notified[key] == 0 {
			dlog("%s: gh pr list failed — leaving prs alone", j.Slug)
			d.Notified[key] = time.Now().Unix()
		}
		return
	}
	delete(d.Notified, "ghfail:"+j.Slug)
	updated, newlyMerged, changed := reconcilePRs(j.PRs, fetched)
	if !changed {
		return
	}
	if err := writeJobPRs(j, updated); err != nil {
		dlog("%s: prs write failed: %v", j.Slug, err)
		return
	}
	for _, pr := range updated {
		dlog("%s: pr %s %s", j.Slug, pr.State, pr.URL)
	}
	for _, url := range newlyMerged {
		dlog("%s: merged %s — cell left alone", j.Slug, url)
		notify("kraken ✓ "+j.Slug, "PR merged — accept from the board when the job is finished")
	}
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
