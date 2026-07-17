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
// make install). It hosts the scheduler poll: queue ready jobs under the cell
// cap, watch PRs for auto-reap, watch zeus panes for stalls (poking hermes),
// watch escalation inboxes (notifying carl), and write a heartbeat the viewer
// renders. It owns no state that can't be re-derived from files + discovery —
// the in-memory dedup maps only suppress duplicate notifications, and losing
// them on restart merely re-notifies once.

const (
	cellCap        = 5
	daemonInterval = 15 * time.Second
	// PR polling is throttled to every reapEvery-th tick (~1 min): gh calls
	// per open cell are the daemon's only rate-limited traffic.
	reapEvery = 4
	// a zeus sitting in "waiting" this long with no open escalation smells
	// stalled; "question" pokes immediately.
	stallAfter  = 5 * time.Minute
	repokeAfter = 10 * time.Minute
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
// inbox itself is the durable signal.
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
	notified map[string]int64 // escalation path / err key → mtime notified
	poked    map[string]int64 // job slug + zeus status → unix time poked
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

	// unparseable jobs: the "inbox gets an error" contract — one notification
	// per file+mtime, the board carries the detail
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

	escs := loadEscalations(jobs)
	for _, e := range escs {
		if e.Status != "open" {
			continue
		}
		if mt := e.ModTime.Unix(); d.notified[e.File] != mt {
			notify("kraken ⚑ "+e.JobSlug, e.Title)
			d.notified[e.File] = mt
			dlog("escalation %s: %s", e.JobSlug, e.Title)
		}
	}
	for _, line := range syncStatesFromInbox(jobs, escs) {
		dlog("%s", line)
	}

	// stall watch: zeus panes in question (immediately) or long-waiting (with
	// nothing already escalated) get hermes poked to investigate
	openBySlug := map[string]bool{}
	for _, e := range escs {
		if e.Status == "open" {
			openBySlug[e.JobSlug] = true
		}
	}
	all, _ := orderAll(discoverAll())
	peerByName := map[string]*Peer{}
	for _, p := range all {
		if p.Name != "" {
			peerByName[p.Name] = p
		}
	}
	now := time.Now()
	cells := 0
	for _, j := range jobs {
		if !j.started() || j.State == "done" || !live[cellSession(j)] {
			continue
		}
		cells++
		// hermes' completion marker: the crew says the job is finished, so
		// park it in the review column for carl's final drag — jobs with no
		// PR (artifact-only, explore) have no other route out of "working"
		if _, err := os.Stat(cellDoneMarker(j)); err == nil &&
			(j.State == "executing" || j.State == "planning" || j.State == "blocked") {
			if err := writeJobState(j, "testing"); err == nil {
				notify("kraken ✓ "+j.Slug, "crew reports complete — review and drag to done")
				dlog("%s: done marker → review column", j.Slug)
			}
			continue
		}
		if openBySlug[j.Slug] || j.Err != "" {
			continue // already waiting on carl, or not safe to touch
		}
		zeus, hermes := cellAgents(j, all)
		if zeus == nil || hermes == nil {
			continue
		}
		// health first: an API-errored, half-dead, or silently-idle cell needs
		// carl's retry, not a hermes poke (hermes may be the broken half).
		// Idle-stall dedup: the reason embeds the rounded idle duration, so
		// key on the prefix or every tick re-notifies with a fresh number.
		if reason := cellHealth(j, all); reason != "" {
			key := "health:" + j.Slug + "|" + strings.SplitN(reason, " idle ", 2)[0]
			if d.notified[key] == 0 {
				notify("kraken ⚠ "+j.Slug, reason+" — auto-retrying; also on the board")
				d.notified[key] = now.Unix()
				dlog("unhealthy %s: %s", j.Slug, reason)
			}
			// auto-retry retryable stalls with backoff: API overload comes in
			// waves, and a manual retry during one just eats the next 529 —
			// keep nudging every repokeAfter until a turn survives
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
			continue
		}
		// question prompts still route through hermes: it can often answer
		// from plan context without waking carl
		if zeus.Status != "question" {
			continue
		}
		key := j.Slug + "|" + zeus.Status
		if last := d.poked[key]; last > 0 && now.Unix()-last < int64(repokeAfter/time.Second) {
			continue
		}
		msg := fmt.Sprintf("zeus looks blocked (status: %s) — investigate with k r %s and write an escalation if carl is needed", zeus.Status, zeusName(j))
		if err := sendMessage(hermes, msg); err != nil {
			dlog("poke hermes for %s failed: %v", j.Slug, err)
			continue
		}
		d.poked[key] = now.Unix()
		dlog("poked hermes for %s (zeus %s)", j.Slug, zeus.Status)
	}

	// PR watch (throttled): reap merged cells
	if d.tickN%reapEvery == 0 {
		for _, j := range jobs {
			if j.Err != "" || !j.started() || j.State == "done" {
				continue
			}
			if j.State == "pr-open" || j.State == "executing" || j.State == "testing" {
				msg, changed := reapOne(j, live)
				if changed {
					dlog("%s: %s", j.Slug, msg)
					if j.State == "merged" {
						notify("kraken ✓ "+j.Slug, "PR merged — review and drag to done")
					}
				}
			}
		}
	}

	// queue: start ready jobs under the cap. displayState "ready" is exactly
	// the auto-startable condition — gate-review (human release) never
	// auto-starts, matching the human-gate decision.
	var firstErr error
	for _, j := range jobs {
		if cells >= cellCap {
			break
		}
		if displayState(j, bySlug) != "ready" {
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
