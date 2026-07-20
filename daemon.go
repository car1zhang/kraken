package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
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
	cellCap        = 5
	daemonInterval = 15 * time.Second
	// PR polling is throttled to every reapEvery-th tick (~1 min): gh calls
	// per open cell are the daemon's only rate-limited traffic.
	reapEvery = 4
	// backoff between auto-retry pokes of the same stalled cell, and between
	// re-notifications of the same standing health complaint.
	repokeAfter = 10 * time.Minute
	// sessionCreateGrace: a cell session younger than this counts active
	// regardless of the ROLL-CALL — claude is still launching before any
	// session file or pane signal exists yet.
	sessionCreateGrace = 2 * time.Minute
)

type daemonStatus struct {
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
	LastTick  int64  `json:"last_tick"`
	LastError string `json:"last_error,omitempty"`
	Cells     int    `json:"cells"`
}

func daemonStatusPath() string {
	return filepath.Join(home(), ".kraken", "daemon.json")
}

func readDaemonStatus() (daemonStatus, bool) {
	var st daemonStatus
	data, err := os.ReadFile(daemonStatusPath())
	if err != nil || json.Unmarshal(data, &st) != nil {
		return st, false
	}
	return st, true
}

func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// notify fires a macOS notification; failures are logged, never fatal — the
// board itself is the durable signal.
func notify(title, msg string) {
	script := fmt.Sprintf("display notification %q with title %q", truncateOneLine(msg, 120), title)
	if err := exec.Command("osascript", "-e", script).Run(); err != nil {
		dlog("osascript notify failed: %v", err)
	}
}

func dlog(format string, a ...any) {
	fmt.Printf("%s "+format+"\n", append([]any{time.Now().Format("15:04:05")}, a...)...)
}

type daemon struct {
	notified map[string]int64 // dedup key (err/repo file mtime, or "blocked:"/"health:" slug) → notified marker
	poked    map[string]int64 // "retry:" + slug → unix time last auto-retried
	tickN    int
}

func cmdDaemon(args []string) {
	if len(args) != 0 {
		die("usage: k daemon")
	}
	if st, ok := readDaemonStatus(); ok && st.PID != os.Getpid() && processAlive(st.PID) {
		die("daemon already running (pid %d)", st.PID)
	}
	if err := os.MkdirAll(filepath.Dir(daemonStatusPath()), 0o755); err != nil {
		die("mkdir %s: %v", filepath.Dir(daemonStatusPath()), err)
	}
	d := &daemon{notified: map[string]int64{}, poked: map[string]int64{}}
	st := daemonStatus{PID: os.Getpid(), StartedAt: time.Now().Unix()}
	dlog("daemon up (pid %d, cap %d cells, tick %s)", st.PID, cellCap, daemonInterval)
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
		st.LastTick, st.Cells, st.LastError = time.Now().Unix(), cells, ""
		if err != nil {
			st.LastError = err.Error()
			dlog("tick error: %v", err)
		}
		if data, err := json.Marshal(st); err == nil {
			_ = atomicWrite(daemonStatusPath(), string(data))
		}
		time.Sleep(daemonInterval)
	}
}

// tick is one scheduler pass. Each concern is independent: an error in one
// (reported via return for the status file) never blocks the others.
func (d *daemon) tick() (int, error) {
	d.tickN++
	jobs := loadJobs()
	live := liveSessions()
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
		if mt := j.ModTime.Unix(); d.notified[key] != mt {
			notify("kraken: job error", j.Slug+": "+j.Err)
			d.notified[key] = mt
		}
	}

	all, _ := orderAll(discoverAll())
	times := liveSessionTimes()
	now := time.Now()
	cells := 0
	for _, j := range jobs {
		if !j.started() || j.State == "done" {
			continue
		}
		isLive := live[cellSession(j)]
		if isLive {
			cells++
			// eager session stamp: zeus' session id lands in the job file
			// within a tick of the cell coming up (and re-lands if it ever
			// changes), so even a job whose cell later dies uncleanly stays
			// reactivatable
			if err := stampSession(j, all); err != nil {
				dlog("session stamp %s failed: %v", j.Slug, err)
			}
		}

		// health first, independent of state derivation below: an API-errored
		// or half-dead cell needs carl's retry regardless of what state it
		// reads as. Idle-stall dedup: the reason embeds the rounded idle
		// duration, so key on the prefix or every tick re-notifies with a
		// fresh number.
		if isLive {
			if reason := cellHealth(j, all); reason != "" {
				key := "health:" + j.Slug + "|" + strings.SplitN(reason, " idle ", 2)[0]
				if d.notified[key] == 0 {
					notify("kraken ⚠ "+j.Slug, reason+" — auto-retrying; also on the board")
					d.notified[key] = now.Unix()
					dlog("unhealthy %s: %s", j.Slug, reason)
				}
				// auto-retry retryable stalls with backoff: API overload comes
				// in waves, and a manual retry during one just eats the next
				// 529 — keep nudging every repokeAfter until a turn survives
				if !strings.Contains(reason, "is gone") {
					rkey := "retry:" + j.Slug
					if now.Unix()-d.poked[rkey] >= int64(repokeAfter/time.Second) {
						if err := retryCell(j); err != nil {
							dlog("auto-retry %s failed: %v", j.Slug, err)
						} else {
							d.poked[rkey] = now.Unix()
							dlog("auto-retried %s (%s)", j.Slug, reason)
						}
					}
				}
			}
		}

		// state derivation: working ⇄ blocked, driven off a live ROLL-CALL of
		// the cell's pane agents and team members (resolveRepo stays here
		// only for its error-notification side effect — state derivation
		// itself no longer needs the worktree).
		if _, err := resolveRepo(j.Repo); err != nil {
			// surfaces on the board as repo?; notify once per file version
			key := "repo:" + j.File
			if mt := j.ModTime.Unix(); d.notified[key] != mt {
				notify("kraken: job error", j.Slug+": "+err.Error())
				d.notified[key] = mt
			}
			continue
		}
		active := false
		if isLive {
			// a session just created is at least as fresh as its own
			// creation — covers pre-first-transcript startup before any pane
			// or team-config signal exists yet
			if created, ok := times[cellSession(j)]; ok && now.Sub(created) < sessionCreateGrace {
				active = true
			} else {
				zeus := cellZeus(j, all)
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
				active = cellActive(cellSession(j), all, teamMembersForLead(sessionID), paneShowsQuestion)
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
			delete(d.notified, "blocked:"+j.Slug)
		}
		if (st == "working" || st == "blocked") && st != j.State {
			prev := j.State
			if err := writeJobState(j, st); err != nil {
				dlog("write state %s for %s failed: %v", st, j.Slug, err)
				continue
			}
			dlog("%s: %s → %s", j.Slug, prev, st)
			if st == "blocked" {
				key := "blocked:" + j.Slug
				if d.notified[key] == 0 {
					notify("kraken ⏸ "+j.Slug, "blocked — see the board")
					d.notified[key] = now.Unix()
				}
			}
		}
	}

	// PR watch (throttled): reap merged build cells
	if d.tickN%reapEvery == 0 {
		for _, j := range jobs {
			if j.Err != "" || j.Type != "build" || !j.started() || j.State == "done" {
				continue
			}
			msg, changed := reapOne(j, live)
			if changed {
				dlog("%s: %s", j.Slug, msg)
				if j.State == "done" {
					notify("kraken ✓ "+j.Slug, "PR merged — done")
				}
			}
		}
	}

	// queue: start startable jobs under the cap. startCell writes "working"
	// itself once the cell is up.
	var firstErr error
	for _, j := range jobs {
		if cells >= cellCap {
			break
		}
		if !startable(j, bySlug) {
			continue
		}
		repo, err := resolveRepo(j.Repo)
		if err != nil {
			// surfaces on the board as repo?; notify once per file version
			key := "repo:" + j.File
			if mt := j.ModTime.Unix(); d.notified[key] != mt {
				notify("kraken: job error", j.Slug+": "+err.Error())
				d.notified[key] = mt
			}
			continue
		}
		if err := startCell(j, repo); err != nil {
			dlog("start %s failed: %v", j.Slug, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		cells++
		dlog("started %s (cell %s, %d/%d cells)", j.Slug, cellSession(j), cells, cellCap)
		notify("kraken ▶ "+j.Slug, "cell started")
	}
	return cells, firstErr
}
