package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kraken/internal/util"
)

// shipios.go — the state layer behind `kraken ship-ios`: build the iOS app
// and upload it to TestFlight. It is deliberately a SIBLING of update.go, not
// a new subsystem — same status file / flock / staleness shapes, same
// defensive-read convention — because the two are the same problem: a slow,
// unattended subprocess whose progress a phone-sized button has to render.
// Where the two differ, the difference is commented.
//
// The work itself is not here and not in Go at all: ios/ship-testflight.sh is
// the one implementation of the sequence (xcodegen → archive → export →
// altool), exactly as `kraken update` shells out to `make install` rather
// than reimplementing it. This file owns only the run record.

// The ship pipeline's phases, in the order a real run walks them. These
// strings come off the script's stdout seam (`KRAKEN-PHASE: <phase>`), cross
// the wire on the board's `ship_ios` key, and are matched by the web UI and
// the iOS app — so they are three interfaces at once. New phases are
// additive; renames are not, and a rename must change the script in the same
// commit.
const (
	ShipIdle       = "idle"
	ShipPreflight  = "preflight"
	ShipGenerating = "generating"
	ShipArchiving  = "archiving"
	ShipExporting  = "exporting"
	ShipUploading  = "uploading"
	ShipDone       = "done"
	ShipFailed     = "failed"
)

// ShipTerminal reports whether phase means "no ship is in flight". Single
// definition of in-flight-ness, for the same reason UpdateTerminal is one:
// the API's 409 guard, the UI's polling stop condition, and staleShip's first
// rule all key off it, so a phase added to one and not the others can't drift.
func ShipTerminal(phase string) bool {
	switch phase {
	case ShipIdle, ShipDone, ShipFailed:
		return true
	}
	return false
}

// ShipKnownPhase reports whether phase is one this package defines. The
// stdout scanner in cmd/kraken/cmd_shipios.go uses it as its whitelist: a
// `KRAKEN-PHASE:` line naming anything else is a script/binary version skew
// (or a line of build output that happens to start with the prefix), and
// writing it into the status file would put a phase on the wire that no UI
// knows how to render.
func ShipKnownPhase(phase string) bool {
	switch phase {
	case ShipIdle, ShipPreflight, ShipGenerating, ShipArchiving, ShipExporting, ShipUploading, ShipDone, ShipFailed:
		return true
	}
	return false
}

// ShipStatus is the one in-flight record of a TestFlight ship, at
// ~/.kraken/ship-ios.json.
//
// A file rather than a store row for a weaker reason than UpdateStatus' (this
// run does not restart `kraken serve`, so the store would survive it), but a
// sufficient one: the shipper runs detached, with no store handle and no
// listener to POST to, and it must be able to record `failed` even when serve
// has been restarted or killed underneath it. Matching update.go's shape also
// means the board renders both buttons off the same code.
type ShipStatus struct {
	Phase     string `json:"phase"`
	StartedAt int64  `json:"started_at"` // unix millis; 0 = never run
	UpdatedAt int64  `json:"updated_at"`
	PID       int    `json:"pid"` // 0 while the API handler owns it, pre-spawn
	// Build is the CFBundleVersion the script resolved (git rev-list --count
	// HEAD), 0 until its `KRAKEN-BUILD:` line has been seen. Worth carrying on
	// the wire because App Store Connect burns a build number permanently:
	// when an upload fails, the number it failed on is the first thing anyone
	// needs to know.
	Build     int    `json:"build"`
	Error     string `json:"error"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// ShipStatusPath is the run record the shipper writes at every phase
// transition and the API/board read back.
func ShipStatusPath() string {
	return filepath.Join(util.Home(), ".kraken", "ship-ios.json")
}

// ShipLogPath collects the shipper's stdout+stderr — the only place
// xcodebuild's and altool's own output survives a detached run, and the only
// way to tell "the export found no distribution profile" apart from "altool
// rejected the build number" after the fact.
func ShipLogPath() string {
	return filepath.Join(util.Home(), ".kraken", "ship-ios.log")
}

// OpenShipLog opens ~/.kraken/ship-ios.log for one run and stamps a banner
// naming the entry point that started it, so the two spawn paths — the CLI
// verb's own tee and the API handler's redirect of the detached child — write
// ONE banner format into one file. They used to have a copy each, which is how
// the same log ended up with two grammars for "a run starts here" and a reader
// looking for the last one had to know both.
//
// O_APPEND rather than O_TRUNC, for the reason api/update.go's openUpdateLog
// documents: the truncate would happen in the parent before the child starts,
// so two runs landing close together each blow away a log the other is still
// writing and what survives is interleaved garbage with holes in it. It matters
// more here than for an update — an archive+upload's failure output
// (xcodebuild's signing errors, altool's rejections) is genuinely
// unreproducible without re-running ten minutes of work. Appending costs only
// that a reader has to find the last banner.
func OpenShipLog(label string) (*os.File, error) {
	path := ShipLogPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lf, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(lf, "\n=== %s — %s ===\n", label, time.Now().Format("2006-01-02 15:04:05"))
	return lf, nil
}

// shipLockPath is the cross-process single-flight guard held for a whole ship
// run (see AcquireShipLock). Unexported for the same reason updateLockPath is:
// nothing outside this package names the path, callers take the lock through
// the accessor.
func shipLockPath() string {
	return filepath.Join(util.Home(), ".kraken", "ship-ios.lock")
}

// shipClaimGraceMS is how long a status with PID 0 is believed — identical in
// value and in reasoning to updateClaimGraceMS: the API handler writes
// phase=preflight BEFORE the child exists (that write IS the mutex), so for a
// short window a healthy run legitimately names no pid.
const shipClaimGraceMS = 60_000

// shipMaxRun caps how long a non-terminal ship record is believed no matter
// how alive its pid looks, closing the same two holes updateMaxRun does (a
// subprocess that HANGS rather than exits; a recycled pid that reads as live
// forever) — either of which otherwise wedges the SHIP button permanently,
// since every later POST /api/ship-ios 409s on the non-terminal record.
//
// The VALUE is three times updateMaxRun's 15 minutes, and that is the whole
// reason this is a separate constant: a Go build is ~40s, while this run is
// four Apple tools in series. Measured/expected on this machine: xcodegen is
// seconds, a clean Release archive of a Swift app is several minutes (Xcode
// re-compiles from scratch — the archive action does not reuse the debug
// build's derived data), export re-signs the whole bundle, and then altool
// uploads tens of megabytes to Apple and waits on server-side validation
// before it returns. The upload is the unbounded one: it is a single HTTP
// transfer to Apple's servers over whatever uplink the Mac has, and a hotel
// or tethered connection can genuinely put it in the tens of minutes. 45
// minutes is far past any honest ship even on a bad link, and still far short
// of a human's patience for a button that appears wedged.
const shipMaxRun = 45 * time.Minute

// ReadShipStatus loads the run record defensively — missing, unreadable, or
// malformed all read as idle, never an error (the house convention for
// optional state) — and then applies staleShip, so a killed shipper surfaces
// as failed instead of pinning the UI in "ARCHIVING…" forever. The downgrade
// is read-side only and deliberately not written back, for the same reason
// ReadUpdateStatus doesn't: the dead process is not coming back to correct the
// file, and a reader that persisted its own inference would race the shipper
// in the one case where it guessed wrong.
func ReadShipStatus() ShipStatus {
	data, err := os.ReadFile(ShipStatusPath())
	if err != nil {
		return ShipStatus{Phase: ShipIdle}
	}
	var st ShipStatus
	if json.Unmarshal(data, &st) != nil {
		return ShipStatus{Phase: ShipIdle}
	}
	if st.Phase == "" {
		st.Phase = ShipIdle
	}
	return staleShip(st, shipProcessAlive(st.PID), time.Now().UnixMilli())
}

// staleShip downgrades a non-terminal status whose owning process is gone.
// Pure function of (status, liveness, clock) — like staleUpdate — so the whole
// decision table is testable without a real pid or a real clock.
func staleShip(st ShipStatus, alive bool, now int64) ShipStatus {
	if ShipTerminal(st.Phase) {
		return st
	}
	if st.PID == 0 {
		// Pre-spawn: the API handler wrote this and the child hasn't claimed
		// it yet. Live inside the grace window, dead past it.
		//
		// StartedAt <= now is the other half of that window, and it is not
		// pedantry: `now - StartedAt <= grace` is trivially true for ANY stamp
		// in the future, so a clock that jumped backwards (or a hand-edited
		// ship-ios.json) would make this record live forever — and because the
		// PID-0 branch returns here, neither the liveness rule nor shipMaxRun
		// below can ever reach it to rescue the button. Every later POST would
		// 409 and the only remedy would be deleting the file by hand. Both
		// clients already bound their own freshness check with abs(); the
		// server has no excuse not to.
		if now-st.StartedAt <= shipClaimGraceMS && st.StartedAt <= now {
			return st
		}
		st.Phase = ShipFailed
		st.Error = "ship never started"
		return st
	}
	if !alive {
		st.Phase = ShipFailed
		st.Error = fmt.Sprintf("ship exited without finishing (pid %d) — see ~/.kraken/ship-ios.log", st.PID)
		return st
	}
	// Alive, but for too long. Last rule rather than first so the two cases
	// above keep their more precise sentences. Keyed off UpdatedAt, which
	// every phase transition refreshes, so a slow-but-progressing run is never
	// cut off — which matters more here than for update: `uploading` can
	// legitimately be the last transition for a very long time, so the clock
	// this rule reads is the one that says "we got as far as the upload", not
	// "the upload is still moving". That is why shipMaxRun is generous.
	stamp := st.UpdatedAt
	if stamp == 0 {
		stamp = st.StartedAt
	}
	if stamp != 0 && now-stamp > shipMaxRun.Milliseconds() {
		st.Error = fmt.Sprintf("ship stalled in %s for over %s (pid %d) — see ~/.kraken/ship-ios.log", st.Phase, shipMaxRun, st.PID)
		st.Phase = ShipFailed
		return st
	}
	return st
}

// WriteShipStatus persists st, stamping UpdatedAt and deriving ElapsedMS so no
// caller has to remember to. AtomicWrite because the board polls this file
// every second from another process and must never catch a half-written record.
//
// Note what is NOT in ShipStatus and must never be added to it: ASC_KEY_ID and
// ASC_ISSUER_ID. This file is world-readable (0644, via AtomicWrite) and is
// served, field for field, to the web board and the phone.
func WriteShipStatus(st ShipStatus) error {
	now := time.Now().UnixMilli()
	st.UpdatedAt = now
	if st.StartedAt != 0 {
		st.ElapsedMS = now - st.StartedAt
	}
	path := ShipStatusPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return util.AtomicWrite(path, string(data))
}

// AcquireShipLock takes the cross-process single-flight guard at
// ~/.kraken/ship-ios.lock, mirroring AcquireUpdateLock exactly: a kernel
// flock, so a holder that dies mid-run has its lock released by the kernel
// rather than leaving a stale file behind. Three outcomes: acquired (release
// non-nil, ok=true, err=nil); held elsewhere (nil, false, nil — contention,
// NOT an error); or the lock file couldn't be opened (nil, false, err), kept
// distinct so a filesystem problem is never reported as a phantom concurrent
// ship.
//
// A SEPARATE lock from update's, not a shared one, because the two guard
// different things: this one serializes ship against ship — two concurrent
// archives would fight over the same DerivedData and, worse, could both reach
// altool with the same build number, of which App Store Connect accepts
// exactly one, forever.
//
// Ship against UPDATE is a real conflict too, and it is deliberately NOT
// handled here: the two runs are not disjoint (they drive different tools, but
// over the SAME ~/Developer/kraken checkout, so an update's `git merge
// --ff-only` landing mid-archive produces an .ipa torn across two commits and
// uploads it under the pre-merge build number). Cross-locking here would be
// the wrong instrument — a flock held by a detached child can only make the
// other run BLOCK, silently, for as long as an upload takes. The refusal
// belongs where the user can read it, so each preflight checks the other's run
// record instead (PreflightShipIOS, PreflightUpdate) and says no in a sentence.
func AcquireShipLock() (release func(), ok bool, err error) {
	path := shipLockPath()
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

// ShipLockHolderPID reads back the pid AcquireShipLock stamped into the lock
// file, or 0 when there is nothing trustworthy to report. Same single caller
// and same purpose as UpdateLockHolderPID: it is what makes the contention
// refusal actionable when a run has wedged past shipMaxRun — the board has
// already downgraded that run to failed and re-enabled the button, so every
// retry lands on the contention path, and without the pid each one would
// overwrite the one fact (which process to kill) the user needed.
//
// Defensive to the bone — missing file, truncated write, garbage contents, a
// nonsense pid all read as 0, meaning "no pid to offer", never an error: a
// failure to decorate a message must not become a second failure.
func ShipLockHolderPID() int {
	data, err := os.ReadFile(shipLockPath())
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

// ShipScriptRel is where the pipeline lives inside the kraken checkout,
// relative to its root. One constant so the two places that must agree on it —
// the path PreflightShipIOS resolves and hands on as pf.Script, and the
// refusals that name the file when it is missing or not executable — cannot
// drift. Everything downstream (the CLI verb, the API handler) runs pf.Script
// and never rebuilds the path itself.
const ShipScriptRel = "ios/ship-testflight.sh"

// ShipPreflightInfo is what PreflightShipIOS resolved on the way to saying
// yes: the checkout it would ship from, the branch that checkout is on, the
// script it would run, and the absolute xcodegen the script needs on PATH (see
// PreflightShipIOS for why that last one is here and not left to the script).
type ShipPreflightInfo struct {
	Repo         string // ~/Developer/kraken
	Branch       string // must be "main"
	Script       string // <Repo>/ios/ship-testflight.sh
	XcodegenPath string
}

// PreflightShipIOS runs every cheap local check before anything is spawned.
// Shared by the CLI verb and the API handler on purpose: the refusal a board
// click shows and the refusal a terminal prints are then the same sentence,
// produced by the same code. Its error text IS the user-facing message.
//
// What is deliberately NOT checked here: the git tree being clean, xcodebuild
// existing, the .p8 key file being present. The script checks all three in its
// own preflight phase and its messages are good; duplicating them in Go would
// give two sentences for one condition that drift apart on the first edit.
// What IS here is what the script cannot see from where it stands — the state
// of the OTHER long-running job that shares this checkout, and the environment
// `kraken serve` would hand it — plus the two checks (credentials, branch) the
// script also makes for its own sake as a hand-run tool; see each for why the
// duplication is right rather than drift.
func PreflightShipIOS() (ShipPreflightInfo, error) {
	var pf ShipPreflightInfo

	// Credentials first: the cheapest check, the most common refusal, and the
	// only one that can be true of a perfectly healthy checkout. The script
	// checks this too, and SHOULD — run by hand it is the only thing standing
	// between a typo'd export and fifteen minutes of archiving. What makes the
	// copy here worth its keep is not the check but the sentence: the script
	// can only describe the environment it was handed, while this side knows
	// the environment a board click hands it is `kraken serve`'s launchd one,
	// which is exactly where these are most likely to be missing. Naming that
	// saves the "but it works in my terminal" round trip.
	if missing := missingASCEnv(); len(missing) > 0 {
		return pf, fmt.Errorf("App Store Connect credentials missing: %s not set. "+
			"Both are shown at App Store Connect → Users and Access → Integrations → App Store Connect API; "+
			"the matching AuthKey_<key id>.p8 belongs in ~/.appstoreconnect/private_keys/. "+
			"A ship started from the board inherits `kraken serve`'s launchd environment, not a login shell's, "+
			"so set them there too (launchctl setenv) for the button to work. Setup: ios/TESTFLIGHT.md",
			strings.Join(missing, " and "))
	}

	// Always the canonical checkout, never the caller's cwd — even when that
	// cwd is a kraken worktree: what ships to real testers must be what is on
	// main.
	repo, err := ResolveRepo("kraken")
	if err != nil {
		return pf, err
	}
	pf.Repo = repo

	// A self-update is the one other thing that touches this checkout, and it
	// touches it destructively: `git merge --ff-only` moves HEAD and rewrites
	// the working tree under whatever is reading it. Five minutes into an
	// `xcodebuild archive` that produces an .ipa assembled from two different
	// commits — and it is uploaded under the build number resolved BEFORE the
	// merge, which App Store Connect then burns forever. `xcodegen generate`
	// racing the same merge over the tracked pbxproj is the same hazard in
	// miniature. Refuse rather than serialize: the update is 40 seconds away
	// from being over, and a message beats a button that silently hangs.
	if u := ReadUpdateStatus(); !UpdateTerminal(u.Phase) {
		return pf, fmt.Errorf("a kraken self-update is in flight (%s) — it rewrites %s, which is the checkout this ship would archive; wait for it to finish", u.Phase, repo)
	}

	// Branch, for the same reason PreflightUpdate checks it, plus one this
	// sibling owns alone: the build number is `git rev-list --count HEAD`, and
	// off main that count is not unique. A feature branch behind main has a
	// count main has ALREADY uploaded (as of writing, main is at 220 and a live
	// worktree here at 217), and App Store Connect accepts a given
	// (version, build) exactly once, forever — so the collision cannot be
	// undone, only worked around by bumping the marketing version. A detached
	// HEAD reads as "HEAD" here and is refused by the same comparison, which is
	// the point: it is the shape most likely to produce a stale count.
	branch, err := updateRunGit(context.Background(), repo, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return pf, err
	}
	pf.Branch = branch
	if branch != "main" {
		return pf, fmt.Errorf("%s is on %s, not main", repo, branch)
	}

	pf.Script = filepath.Join(repo, ShipScriptRel)
	fi, err := os.Stat(pf.Script)
	if err != nil {
		return pf, fmt.Errorf("%s not found in %s — the ship pipeline is not on this checkout's HEAD", ShipScriptRel, repo)
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return pf, fmt.Errorf("%s is not executable — chmod +x it", pf.Script)
	}

	// xcodegen resolved to an absolute path here, for exactly the reason
	// documented on toolchainEnv (cmd/kraken/cmd_update.go): a ship started
	// from the board is forked by a launchd job, and launchd's PATH is
	// /usr/bin:/bin:/usr/sbin:/sbin — which does not include the homebrew
	// prefix xcodegen installs into. The script would then die in its own
	// preflight with "xcodegen not found — brew install xcodegen" against a
	// machine that has it, and the failure would be invisible from a terminal,
	// where the same command works. xcodebuild and xcrun need no such help:
	// they live in /usr/bin, which every PATH has.
	if pf.XcodegenPath, err = lookToolchain("xcodegen"); err != nil {
		return pf, fmt.Errorf("%w (needed to regenerate the Xcode project before archiving)", err)
	}
	return pf, nil
}

// ascEnvKeys are the two App Store Connect credential variables, named in one
// place. Their VALUES must never be read into a log line, an event, or the
// status file — only their names, and only to say one is missing.
var ascEnvKeys = []string{"ASC_KEY_ID", "ASC_ISSUER_ID"}

// ASCEnvPath is the credential file a board-triggered ship reads its App Store
// Connect ids from: ~/.kraken/asc.env, KEY=VALUE per line.
//
// It exists because there is no working way to hand these to the daemon
// through launchd. `launchctl setenv` is the obvious answer and it does not
// work: measured on this machine, the variables land in the gui/ domain
// (launchctl getenv returns them) and never reach the job, because the plist
// declares its own EnvironmentVariables dict. Neither `kickstart -k` nor a
// full bootout+bootstrap changes that. And even if it did, setenv is lost on
// reboot, which would break the button in a way that leaves terminal runs
// working — the worst shape a bug can have.
//
// A file solves both: it survives reboots and it is read by the process that
// actually needs it. The .p8 itself is NOT here; altool still finds that by
// its own convention under ~/.appstoreconnect/private_keys/.
func ASCEnvPath() string { return filepath.Join(util.Home(), ".kraken", "asc.env") }

// loadASCEnv parses ASCEnvPath into a map. Defensive by the house rule: a
// missing, unreadable, or malformed file yields an empty map and never an
// error — a ship with no credentials must refuse with the actionable preflight
// message below, not with a parse error nobody can act on.
//
// Only ascEnvKeys are honored. This file is a credential channel, not a
// general environment: letting it set PATH or DYLD_* would turn "someone can
// write one file in ~/.kraken" into arbitrary code execution in every ship.
func loadASCEnv() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(ASCEnvPath())
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`))
		for _, allowed := range ascEnvKeys {
			if k == allowed && v != "" {
				out[k] = v
			}
		}
	}
	return out
}

// ASCEnviron returns the KEY=VALUE pairs a ship child needs that the current
// process does not already carry, so the caller can append them to the child's
// environment. The process environment wins: an explicit `export` in a
// terminal must override the file, or there is no way to test a new key
// without editing the file first.
func ASCEnviron() []string {
	file := loadASCEnv()
	var out []string
	for _, k := range ascEnvKeys {
		if strings.TrimSpace(os.Getenv(k)) == "" {
			if v, ok := file[k]; ok {
				out = append(out, k+"="+v)
			}
		}
	}
	return out
}

// missingASCEnv returns the names of the credential variables that are unset
// or blank in BOTH the environment and ASCEnvPath. Whitespace counts as unset:
// an `export ASC_KEY_ID=` typo produces a set-but-empty variable that would
// otherwise sail through preflight and fail minutes later at altool.
func missingASCEnv() []string {
	file := loadASCEnv()
	var missing []string
	for _, k := range ascEnvKeys {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			continue
		}
		if strings.TrimSpace(file[k]) != "" {
			continue
		}
		missing = append(missing, k)
	}
	return missing
}

// --- swappable seams, mirroring update.go's, so the tests drive every branch
// above without a real pid. Nothing in the test suite may shell out. ---
var (
	shipProcessAlive = ProcessAlive
)
