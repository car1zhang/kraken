// Protocol parity: this file is part of the UPDATE protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// update.go — the board's UPDATE button: POST /api/update kicks off a
// self-update (fetch → merge → build → install → restart), GET
// /api/update/status reports where that run has got to. The work itself
// runs in a DETACHED `kraken update --detached` child, never in this
// process, for one reason: the last step of an update is `launchctl
// kickstart -k`, which kills the very `kraken serve` that served the
// request. A handler goroutine would be executing its own executioner.

// preflightUpdate is jobs.PreflightUpdate, swapped out in tests so handler
// tests never shell out to git — the same seam pattern account.go uses for
// swapMainTo.
var preflightUpdate = jobs.PreflightUpdate

// readUpdateAvail is jobs.ReadUpdateAvail behind a seam, so a test can seed
// an availability row without wiring a store: the row's writer is
// jobs-internal (the daemon is the only one), by design.
var readUpdateAvail = jobs.ReadUpdateAvail

// openUpdateLog opens ~/.kraken/update.log for one run and stamps the banner
// that separates it from the previous one. Split out of spawnUpdater so the
// append-not-truncate rule below is testable without forking anything.
func openUpdateLog(args []string) (*os.File, error) {
	logPath := jobs.UpdateLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	// O_APPEND with a per-run banner, NOT O_TRUNC. The truncate would happen
	// in the PARENT before Start, so two spawns landing close together would
	// each blow away a log the other child is already writing — and since
	// every child holds its own open file description with its own offset,
	// what survives is interleaved garbage with holes in it. Appending costs
	// only that a reader has to find the last banner; truncating costs the
	// one forensic record of why a build failed.
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(lf, "\n=== kraken %s — %s ===\n", strings.Join(args, " "), time.Now().Format("2006-01-02 15:04:05"))
	return lf, nil
}

// spawnUpdater launches the detached updater and returns its pid. A package
// var so tests can assert "exactly one spawn was requested" without ever
// forking a process.
//
// Three details here are load-bearing, in ascending order of importance:
//
//   - os.Executable with a "kraken" fallback (cell.go's convention) so the
//     update re-execs the binary that is actually running, not whatever
//     `kraken` resolves to on the daemon's PATH.
//   - stdout AND stderr go to a FILE, not a pipe: the parent's pipe ends
//     close the moment the parent dies, and the parent is guaranteed to die
//     mid-run — a pipe would silently discard `make install`'s output,
//     which is the only forensic record when an update fails.
//   - Setsid. Without a new session the child stays in the launchd job's
//     process group, so `launchctl kickstart -k dev.carl.kraken` — which
//     the child itself triggers as its final step — kills the child before
//     it can record that it finished. This is the single line the whole
//     feature stands on.
var spawnUpdater = func(force, test bool) (int, error) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "kraken"
	}
	args := []string{"update", "--detached"}
	if force {
		args = append(args, "--force")
	}
	if test {
		args = append(args, "--test")
	}

	lf, err := openUpdateLog(args)
	if err != nil {
		return 0, err
	}
	defer lf.Close() // the child dup'd the fd at Start; this is only our copy

	cmd := exec.Command(exe, args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// Setsid detaches the session, not the parent-child link, so the child
	// is still ours to reap. Reap it in the background — on the normal path
	// this process is killed by the child's own restart long before the Wait
	// returns, and on the abnormal path (a run that fails before the
	// restart) it keeps serve from accumulating a zombie.
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// webUpdate is the update state both the board's `update` key and GET
// /api/update/status serve. Assembled in exactly one place (currentUpdate)
// so the two can never drift into disagreeing about the same run.
type webUpdate struct {
	Phase     string `json:"phase"`
	FromSHA   string `json:"from_sha"`
	ToSHA     string `json:"to_sha"`
	Behind    int    `json:"behind"`
	Error     string `json:"error"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
	BuildSHA  string `json:"build_sha"`  // util.Build()
	CheckedAt int64  `json:"checked_at"` // from UpdateAvail
	// Restarted mirrors the run record's flag: this run replaced the binary
	// and kickstarted the daemon. The SPA's mandatory post-update reload keys
	// on it for the one shape `done` alone cannot express — a run that
	// restarted and THEN failed (the mac-app phase), where skipping the
	// reload would leave the open page's old bundle running against the new
	// server indefinitely.
	Restarted bool `json:"restarted"`
}

// currentUpdate merges the two independently-written sources behind the
// button: the run record (~/.kraken/update.json, written by the detached
// updater) and the availability check (a kv row, written by the daemon).
// Behind and CheckedAt come from availability — they describe "is there
// anything to update to", which is true whether or not a run has ever
// happened — while Error is the RUN's error only: an availability check
// that couldn't reach the network is not something to put in front of the
// user, and the check deliberately swallows it for that reason.
func currentUpdate() webUpdate {
	st := jobs.ReadUpdateStatus()
	av := readUpdateAvail()

	// A run that finished AFTER the last availability check supersedes it:
	// the check only re-runs every ten minutes, so without this the board
	// would keep advertising "3 behind" for up to ten minutes against a
	// binary that is now current — on the very screen the user just watched
	// update. The correction lives here rather than in the updater because
	// the updater is detached with no store handle, so it has nothing to
	// write the corrected row with. Both terminal success phases count:
	// `done` took the commits, `uptodate` proved there were none.
	behind := av.Behind
	// `failed && Restarted` joined the suppression when the mac-app phase
	// arrived: that failure happens AFTER the merge landed, so the pre-run
	// count is exactly as stale as it is after `done` — advertising it would
	// paint "N BEHIND" against a binary that is current.
	terminalAfterMerge := st.Phase == jobs.UpdateDone || st.Phase == jobs.UpdateUpToDate ||
		(st.Phase == jobs.UpdateFailed && st.Restarted)
	if terminalAfterMerge && st.UpdatedAt > av.CheckedAt {
		behind = 0
	}
	return webUpdate{
		Phase:     st.Phase,
		FromSHA:   st.FromSHA,
		ToSHA:     st.ToSHA,
		Behind:    behind,
		Error:     st.Error,
		StartedAt: st.StartedAt,
		UpdatedAt: st.UpdatedAt,
		BuildSHA:  util.Build(),
		CheckedAt: av.CheckedAt,
		Restarted: st.Restarted,
	}
}

type updateReq struct {
	Force bool `json:"force"`
	Test  bool `json:"test"`
}

type updateResp struct {
	OK      bool   `json:"ok"`
	Phase   string `json:"phase"`
	FromSHA string `json:"from_sha"`
}

// updateHandler starts a self-update. The ordering below is the whole
// design: the status write at step 5 happens BEFORE the spawn, and THAT
// WRITE IS THE MUTEX — a second request arriving in the window between the
// write and the child's first breath reads a non-terminal phase at step 3
// and 409s. (This is also why jobs.staleUpdate grants a 60s grace to a
// status with PID 0: the record legitimately exists before the process it
// describes does.)
//
// Deliberately NOT the flock: taking jobs.AcquireUpdateLock here would mean
// the parent releasing it before the child could acquire it, leaving
// exactly the race window the lock was meant to close. The child takes the
// real flock and holds it for the whole run.
func updateHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	// An empty body is a valid "just update" (the web button posts nothing),
	// so decode leniently: malformed JSON is an error, EOF is not.
	var req updateReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}

	if st := jobs.ReadUpdateStatus(); !jobs.UpdateTerminal(st.Phase) {
		started := time.UnixMilli(st.StartedAt).Format("15:04:05")
		msg := "update already in flight (started " + started + ")"
		util.LogEvent("web", "error", "update", "update refused: "+msg+" — phase "+st.Phase)
		webJSON(w, http.StatusConflict, map[string]string{"error": msg})
		return
	}

	// Preflight's error text IS the refusal the user reads — it is shared
	// with the CLI verb precisely so a board click and a terminal run refuse
	// in the same words. 409 rather than 400: nothing about the request is
	// malformed, the machine is just in a state that can't be updated from.
	pf, err := preflightUpdate()
	if err != nil {
		util.LogEvent("web", "error", "update", "update refused: "+err.Error())
		webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	now := time.Now().UnixMilli()
	// MacAppSHA is cross-run state — the last commit the mac app was
	// installed at — and this claim-write replaces the whole record, so it is
	// carried forward explicitly or the updater's catch-up logic would see a
	// blank history on every board-started run.
	if err := jobs.WriteUpdateStatus(jobs.UpdateStatus{Phase: jobs.UpdatePreflight, StartedAt: now, FromSHA: pf.HeadSHA, MacAppSHA: jobs.ReadUpdateStatus().MacAppSHA}); err != nil {
		util.LogEvent("web", "error", "update", "update status write: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	pid, err := spawnUpdater(req.Force, req.Test)
	if err != nil {
		// The claim above is now a lie — nothing is coming to advance it. Put
		// the failure in the record itself rather than leaving the status
		// pinned at "preflight" for staleUpdate's 60s grace to time out, which
		// would show the user a spinner for a run that never began.
		_ = jobs.WriteUpdateStatus(jobs.UpdateStatus{Phase: jobs.UpdateFailed, StartedAt: now, FromSHA: pf.HeadSHA, Error: err.Error()})
		util.LogEvent("web", "error", "update", "update spawn: "+err.Error())
		webBoardCache.invalidate()
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	util.LogEvent("web", "info", "update", "update started from "+pf.HeadSHA+" (pid "+strconv.Itoa(pid)+")")
	webBoardCache.invalidate()
	webJSON(w, http.StatusAccepted, updateResp{OK: true, Phase: jobs.UpdatePreflight, FromSHA: pf.HeadSHA})
}

// updateStatusHandler serves the same shape the board's `update` key
// carries, for the UI's in-flight poller — which polls this narrow endpoint
// rather than the whole board so a 1s poll during a build doesn't re-run
// discovery every tick.
func updateStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	webJSON(w, http.StatusOK, currentUpdate())
}
