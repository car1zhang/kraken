package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// shipios.go — the board's SHIP iOS button: POST /api/ship-ios kicks off a
// TestFlight run (xcodegen → archive → export → upload to App Store
// Connect), GET /api/ship-ios/status reports where it has got to. A
// deliberate sibling of update.go, step for step, with one structural
// difference worth stating up front: an update ends by restarting the very
// `kraken serve` that answered the request, and a ship does not. Nothing
// here replaces the server, the binary, or the daemon — the Mac stays up
// from preflight to upload — so a status fetch that fails during a ship is
// a genuine error rather than the expected middle of a successful run, and
// none of update.go's restart-gap machinery is mirrored on either client.
// The one thing both clients DO carry is a stretched failure tolerance while
// an UPDATE is in flight: that run's `launchctl kickstart -k` really does
// take serve down for a couple of seconds, and spawnShipIOS's Setsid exists
// so a ship survives it — so the poller must not paint the one outage the
// run is engineered to live through as a failure (see the web board's
// shipFailCeiling and BoardViewModel.shipFailureCeilingNow). The refusal
// direction is handled a layer down, in the two preflights: neither run may
// start while the other is live, since they share one git checkout.
//
// The other honest point the whole surface is worded around: `done` means
// the .ipa reached App Store Connect. TestFlight still has to process it
// (minutes), and someone still has to install it from the TestFlight app.
// "Shipped" would read as "the phone is updated", which it is not.

// preflightShipIOS is jobs.PreflightShipIOS behind a seam, so handler tests
// never shell out to git/xcodebuild — the same pattern preflightUpdate uses.
var preflightShipIOS = jobs.PreflightShipIOS

// spawnShipIOS launches the detached ship run and returns its pid. A package
// var so tests can assert "exactly one spawn was requested" without ever
// forking a process, let alone starting a real archive.
//
// Two deliberate differences from spawnUpdater:
//
//   - Setsid, but for a different reason than the updater's. The updater
//     needs a fresh session because its OWN last step (`launchctl kickstart
//     -k`) kills the launchd job's process group, itself included. A ship
//     restarts nothing, so it has no such executioner of its own — but it
//     can still be caught by someone else's: a concurrent `k update`, or a
//     hand-typed `make install`, kickstarts the job and would take a live
//     ship down with the rest of serve's group. That is the wrong outcome.
//     Nothing about a ship depends on which kraken binary is installed — the
//     child is already running the old inode's code, and the actual work is
//     xcodebuild and altool, which have never heard of kraken. Losing a
//     ten-to-twenty-minute upload because an unrelated update happened to
//     overlap it destroys real work to no end, so the ship gets its own
//     session and outlives the restart. The costs are asymmetric: an
//     orphaned ship is bounded by shipMaxRun and reports itself through the
//     same status file either way.
//   - No flags. The script's --allow-provisioning-updates is reachable only
//     from a terminal on purpose: it MUTATES carl's Apple developer account
//     (creating certs/profiles against his real team), which is not
//     something a button on a board should be able to do by accident.
//
// stdout and stderr go to a FILE rather than a pipe for the same reason they
// do in spawnUpdater — the parent's pipe ends die with the parent, and a run
// this long outliving the process that started it is normal.
var spawnShipIOS = func() (int, error) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "kraken"
	}
	args := []string{"ship-ios", "--detached"}

	// One opener, shared with the CLI verb's own tee (jobs.OpenShipLog), so the
	// two entry points stamp one banner format into one file; the label is the
	// only thing that differs, and it is the thing worth telling apart.
	lf, err := jobs.OpenShipLog("kraken " + strings.Join(args, " "))
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
	// Reap in the background so a serve that outlives the run — the normal
	// case here, unlike an update — doesn't accumulate a zombie.
	go func() { _ = cmd.Wait() }()
	return cmd.Process.Pid, nil
}

// webShipIOS is the ship state both the board's `ship_ios` key and GET
// /api/ship-ios/status serve. Assembled in exactly one place
// (currentShipIOS) so the two can never drift into disagreeing about the
// same run — the same rule webUpdate/currentUpdate follow.
type webShipIOS struct {
	Phase string `json:"phase"`
	// Build is the git-count CFBundleVersion this run is uploading; 0 until
	// the script announces it (KRAKEN-BUILD on stdout). It is the only
	// identifier that ties what the board says to what shows up in App Store
	// Connect, which is why the clients render it in the done label.
	Build     int    `json:"build"`
	Error     string `json:"error"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// currentShipIOS reads the one source behind the button: the run record at
// ~/.kraken/ship-ios.json, written by the detached ship child. There is no
// second source to merge — the update button's `behind`/`checked_at` come
// from a daemon availability check, and there is no equivalent question to
// ask here (nothing polls App Store Connect for "is the phone's build
// stale"). ReadShipStatus already downgrades a dead or hung run to failed,
// so a crashed ship can't pin the button.
func currentShipIOS() webShipIOS {
	st := jobs.ReadShipStatus()
	return webShipIOS{
		Phase:     st.Phase,
		Build:     st.Build,
		Error:     st.Error,
		StartedAt: st.StartedAt,
		UpdatedAt: st.UpdatedAt,
	}
}

// shipIOSReq is deliberately empty: every knob the script takes is either
// environment (ASC_KEY_ID/ASC_ISSUER_ID, never on the wire) or too dangerous
// to expose to a button (see spawnShipIOS). It exists only so a malformed
// body is a 400 rather than being silently ignored.
type shipIOSReq struct{}

type shipIOSResp struct {
	OK    bool   `json:"ok"`
	Phase string `json:"phase"`
}

// shipIOSHandler starts a TestFlight ship. The ordering mirrors
// updateHandler exactly, and for the same reason: the status write at step 4
// happens BEFORE the spawn, and THAT WRITE IS THE MUTEX — a second request
// arriving in the window between the write and the child's first breath
// reads a non-terminal phase at step 2 and 409s. (This is also why
// jobs.ReadShipStatus grants a grace period to a status with PID 0: the
// record legitimately exists before the process it describes does.)
//
// Deliberately NOT the flock: taking the ship lock here would mean the
// parent releasing it before the child could acquire it, leaving exactly the
// race window the lock was meant to close. The child takes the real flock
// and holds it for the whole run.
func shipIOSHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	// An empty body is the whole request (the web button posts "{}", the app
	// posts nothing meaningful), so decode leniently: malformed JSON is an
	// error, EOF is not.
	var req shipIOSReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}

	if st := jobs.ReadShipStatus(); !jobs.ShipTerminal(st.Phase) {
		started := time.UnixMilli(st.StartedAt).Format("15:04:05")
		msg := "ship already in flight (started " + started + ")"
		util.LogEvent("web", "error", "ship-ios", "ship refused: "+msg+" — phase "+st.Phase)
		webJSON(w, http.StatusConflict, map[string]string{"error": msg})
		return
	}

	// Preflight's error text IS the refusal the user reads — it is shared
	// with the CLI verb precisely so a board click and a terminal run refuse
	// in the same words ("ASC_KEY_ID is not set", "xcodegen not found", a
	// dirty tree). 409 rather than 400: nothing about the request is
	// malformed, the machine is just in a state that can't ship from.
	// The resolved ShipPreflightInfo is deliberately dropped: unlike the
	// update button, none of it belongs on the wire. The repo/script paths
	// are the same on every run, and the build number — the one fact worth
	// reporting — isn't knowable until the script announces it mid-run.
	if _, err := preflightShipIOS(); err != nil {
		util.LogEvent("web", "error", "ship-ios", "ship refused: "+err.Error())
		webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	now := time.Now().UnixMilli()
	if err := jobs.WriteShipStatus(jobs.ShipStatus{Phase: jobs.ShipPreflight, StartedAt: now}); err != nil {
		util.LogEvent("web", "error", "ship-ios", "ship status write: "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	pid, err := spawnShipIOS()
	if err != nil {
		// The claim above is now a lie — nothing is coming to advance it. Put
		// the failure in the record itself rather than leaving the status
		// pinned at "preflight" for the claim grace to time out, which would
		// show the user a spinner for a run that never began.
		_ = jobs.WriteShipStatus(jobs.ShipStatus{Phase: jobs.ShipFailed, StartedAt: now, Error: err.Error()})
		util.LogEvent("web", "error", "ship-ios", "ship spawn: "+err.Error())
		webBoardCache.invalidate()
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	util.LogEvent("web", "info", "ship-ios", "ship-ios started (pid "+strconv.Itoa(pid)+")")
	webBoardCache.invalidate()
	webJSON(w, http.StatusAccepted, shipIOSResp{OK: true, Phase: jobs.ShipPreflight})
}

// shipIOSStatusHandler serves the same shape the board's `ship_ios` key
// carries, for the UI's in-flight poller — which polls this narrow endpoint
// rather than the whole board so a 1s poll during a ten-minute archive
// doesn't re-run discovery every tick.
func shipIOSStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	webJSON(w, http.StatusOK, currentShipIOS())
}
