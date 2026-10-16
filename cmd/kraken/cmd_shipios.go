package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// cmd_shipios.go — `kraken ship-ios`, the verb that ships the phone app to
// TestFlight. It is a deliberate sibling of cmd_update.go: same shape (parse
// → exit-free core → package-var seams), same run-record discipline, and the
// same relationship to the board, whose SHIP iOS button does no work itself
// and only spawns this binary with `ship-ios --detached`.
//
// The verb does NOT reimplement the pipeline. ios/ship-testflight.sh is the
// one implementation of "drive xcodegen, xcodebuild, and altool in order",
// exactly as `kraken update` shells out to `make install`; what this file adds
// on top is the part a shell script cannot do — a run record a phone can
// poll. It learns where the run has got to by scanning the script's stdout for
// the `KRAKEN-PHASE:` / `KRAKEN-BUILD:` seam the script documents as an
// interface, never by inferring phases from elapsed time: the phases here
// differ in duration by two orders of magnitude (xcodegen is a second, an
// upload can be twenty minutes), so a timing heuristic would be wrong almost
// always, and wrong in the direction that makes the UI claim progress that
// hasn't happened.
//
// # Detachment
//
// `--detached` here means ONLY "the API already opened a run record for me,
// inherit it" — it does not fork anything. Why the spawner (internal/api) sets
// Setsid is a separate question from the updater's, reaching the same answer
// by a different route, and it is worth being explicit because copying
// update's comment would get the conclusion right for the wrong reason:
//
//   - update needs Setsid because it executes its own executioner: its last
//     step is `launchctl kickstart -k`, which kills the launchd job — and
//     therefore every process in that job's group, including itself — before
//     it can record that it finished.
//   - ship-ios restarts nothing, so that argument does not apply. Nor does
//     the one that sounds like it should: "the parent returns before the
//     child finishes" is NOT a reason for Setsid. A POSIX child is not killed
//     when its parent exits, it is reparented, so a 20-minute upload outlives
//     the HTTP request that started it with no help at all. What actually
//     matters for survival is that the child not inherit a PIPE the parent
//     holds — its write end would close under the child, discarding the only
//     forensic record of a failed archive — hence the log FILE, here and in
//     the spawner.
//
// What DOES argue for Setsid here is a hazard the ship inherits rather than
// causes: without a new session the child sits in the launchd job's process
// group, so `launchctl kickstart -k` fired by something ELSE — an UPDATE
// click, a hand-typed `make install` — would take a live ship down
// mid-upload. spawnShipIOS therefore DOES set Setsid, and a ship outlives an
// unrelated restart; the full reasoning lives on it, in one place, and nothing
// in this file depends on the answer.

type shipIOSOpts struct {
	detached bool
	dryRun   bool
	help     bool
}

// parseShipIOSArgs recognizes only the documented flags; anything else is a
// usage error rather than a silently-ignored typo. That matters more here than
// for update: a mistyped --dry-run that fell through would run a REAL archive
// and upload, and the build number an upload consumes is gone forever.
//
// Note what is absent on purpose: --allow-provisioning-updates. The script
// takes it, and the first ever run needs it, but it lets Xcode create
// certificates and profiles on carl's real Apple developer account. Automation
// must never be able to do that, so the only way to pass it is to run
// ios/ship-testflight.sh by hand.
func parseShipIOSArgs(args []string) (shipIOSOpts, error) {
	var o shipIOSOpts
	for _, a := range args {
		switch a {
		case "--detached":
			o.detached = true
		case "--dry-run":
			o.dryRun = true
		case "--help", "-h":
			o.help = true
		default:
			return shipIOSOpts{}, fmt.Errorf("unknown argument: %s", a)
		}
	}
	return o, nil
}

const shipIOSUsageText = `usage: k ship-ios [--dry-run] [--detached]
  build the iOS app and upload it to TestFlight by running
  ios/ship-testflight.sh in kraken's own checkout (` + "`kraken`" + ` under the repos dir): xcodegen ->
  xcodebuild archive -> export -> xcrun altool --upload-app. The same sequence
  the board's SHIP iOS button fires (it just spawns this verb detached).

  Needs ASC_KEY_ID and ASC_ISSUER_ID in the environment plus the matching
  ~/.appstoreconnect/private_keys/AuthKey_<key id>.p8, and refuses without
  them. 'done' means UPLOADED, not installed: TestFlight still has to process
  the build (usually a few minutes) before the phone is offered it.

  --dry-run    preflight and print the plan; builds nothing, uploads nothing,
               and leaves the board's ship record exactly as it found it
  --detached   spawned by the board's SHIP iOS button: inherit the run record
               the API already opened instead of starting a new one

  The very first run needs an Apple Distribution certificate and an App Store
  profile that do not exist yet, and creating them MUTATES the Apple developer
  account — so that is deliberately not reachable from this verb or the board.
  Run 'ios/ship-testflight.sh --allow-provisioning-updates' by hand, once.
  Full setup: ios/TESTFLIGHT.md`

func cmdShipIOS(args []string) {
	opts, err := parseShipIOSArgs(args)
	if err != nil {
		util.Die("%v\n%s", err, shipIOSUsageText)
	}
	if opts.help {
		fmt.Println(shipIOSUsageText)
		return
	}
	if err := runShipIOS(opts, os.Stdout); err != nil {
		// runShipIOS has already recorded phase=failed where that was the right
		// thing to do; Die's job here is only the message + exit code.
		util.Die("%v", err)
	}
}

// runShipIOS is cmdShipIOS' exit-free core: it returns an error instead of
// calling util.Die, and writes every phase the script announces to
// ~/.kraken/ship-ios.json on the way.
//
// out receives the human progress lines and a verbatim copy of the script's
// own output. In --detached mode stdout IS ~/.kraken/ship-ios.log (the API
// opens it and points the child there, exactly as api/update.go does for the
// updater), which is why the tee below is skipped in that mode — opening the
// same file a second time would double every line of a fifteen-minute build.
func runShipIOS(opts shipIOSOpts, out io.Writer) error {
	st := jobs.ShipStatus{StartedAt: time.Now().UnixMilli(), PID: os.Getpid()}
	if opts.detached {
		// The API wrote phase=preflight the instant the button was clicked —
		// that write is its mutex — and only then spawned us. Inherit its
		// StartedAt so ElapsedMS measures the run the user actually watched.
		if prior := jobs.ReadShipStatus(); prior.StartedAt != 0 {
			st.StartedAt = prior.StartedAt
		}
	}

	// setPhase is the only writer of the run record. It no-ops under
	// --dry-run: a dry run reports what a ship would do and must leave the
	// board's state exactly as it found it, including a failed or done record
	// from a previous real run.
	setPhase := func(phase string) {
		st.Phase = phase
		if opts.dryRun {
			return
		}
		if err := shipWriteStatus(st); err != nil {
			fmt.Fprintf(out, "warning: could not write ship status: %v\n", err)
		}
	}
	// fail records a failure that happened while we owned the run, then
	// returns the error verbatim — the same sentence lands in ship-ios.json,
	// the event log, and the process' stderr.
	fail := func(err error) error {
		st.Error = err.Error()
		setPhase(jobs.ShipFailed)
		util.LogEvent("ship-ios", "error", "ship-error", err.Error())
		return err
	}

	util.LogEvent("ship-ios", "info", "ship-start", "kraken ship-ios starting")

	pf, err := shipPreflight()
	if err != nil {
		// Preflight runs BEFORE the lock, so another shipper may legitimately
		// own the run record right now. Only claim it when nothing is in
		// flight (or when we are the detached child, in which case the
		// non-terminal record on disk is the one the API opened for us) —
		// otherwise a `kraken ship-ios` typed without credentials would stomp
		// a healthy concurrent upload's phase with our own refusal.
		if opts.detached || jobs.ShipTerminal(jobs.ReadShipStatus().Phase) {
			return fail(err)
		}
		util.LogEvent("ship-ios", "error", "ship-error", err.Error())
		return err
	}

	// Held for the WHOLE run, including --dry-run (which mirrors `k update
	// --dry-run`): what is being serialized is two archives fighting over one
	// DerivedData and, worse, two altool uploads racing to claim the same
	// build number — of which App Store Connect accepts exactly one, forever.
	// flock, so a killed shipper's lock dies with it.
	release, ok, err := shipAcquireLock()
	if err != nil {
		return fail(fmt.Errorf("acquire ship lock: %w", err))
	}
	if !ok {
		// The holder's pid is the entire remedy for the case this message most
		// often appears in: a run wedged past shipMaxRun keeps its flock while
		// the board, reading the status file, has already downgraded it to
		// failed and re-enabled the button, so every click from then on lands
		// here. Without the pid we would answer a user who was just told which
		// process is stalled by overwriting that record with a sentence naming
		// nobody, and do it again on every retry.
		contention := errors.New("ship already in flight")
		if pid := jobs.ShipLockHolderPID(); pid > 0 {
			contention = fmt.Errorf("ship already in flight (pid %d)", pid)
		}
		if opts.detached {
			// Detached, the record on disk is the one the API opened FOR US —
			// phase=preflight, pid 0 — and if we say nothing, nothing else ever
			// advances it: the board spins for staleShip's 60s grace and then
			// reports "ship never started", which is false and discards the
			// accurate reason.
			return fail(contention)
		}
		// Interactive, nothing is ours to record: the run record belongs to
		// whoever holds the lock, and stomping it would replace a healthy
		// upload's phase with our refusal.
		return contention
	}
	defer release()

	setPhase(jobs.ShipPreflight)

	// The tee. Interactive runs get their own copy of the log because a build
	// this long is worth keeping even when a human watched it scroll past;
	// detached runs already have one (see the doc comment). A log that cannot
	// be opened is a warning, never a reason to refuse a ship.
	sink := out
	if !opts.detached && !opts.dryRun {
		if lf, err := jobs.OpenShipLog("kraken ship-ios"); err != nil {
			fmt.Fprintf(out, "warning: could not open %s: %v\n", jobs.ShipLogPath(), err)
		} else {
			defer lf.Close()
			sink = io.MultiWriter(out, lf)
		}
	}

	scan := newShipScanner(sink, func(p shipProgress) {
		// Runs on os/exec's stdout/stderr copier goroutines, serialized by the
		// scanner's own mutex; the main goroutine is parked inside shipExec
		// until Run has joined both copiers, so st is only ever touched by one
		// goroutine at a time.
		if p.build > 0 && p.build != st.Build {
			st.Build = p.build
			util.LogEvent("ship-ios", "info", "ship-build", "build number "+strconv.Itoa(p.build))
		}
		// Terminal phases are decided by the process' EXIT STATUS, never by a
		// line of its stdout: a script that prints `done` and then dies in its
		// exit trap would otherwise be recorded as a successful upload, and
		// `done` on this button is a claim that a build reached Apple.
		if p.phase != "" && !jobs.ShipTerminal(p.phase) {
			setPhase(p.phase)
			util.LogEvent("ship-ios", "info", "ship-phase", p.phase)
		}
	})

	var scriptArgs []string
	if opts.dryRun {
		scriptArgs = append(scriptArgs, "--dry-run")
	}
	// util.ToolchainEnv prepends the resolved xcodegen's directory
	// to PATH. Same gap it closes for the updater's bare `go`: this process may
	// have been forked by launchd, whose PATH does not reach the homebrew
	// prefix xcodegen lives in, and the script invokes it by bare name.
	// jobs.ASCEnviron() adds the App Store Connect ids from ~/.kraken/asc.env
	// for any the current process is missing. A ship started from the board is
	// forked by the launchd daemon, which cannot be given these through
	// launchctl at all (see jobs.ASCEnvPath) — without this the button refuses
	// preflight forever while the identical terminal run works.
	shipEnv := append(util.ToolchainEnv(os.Environ(), pf.XcodegenPath), jobs.ASCEnviron()...)
	if err := shipExec(pf.Script, pf.Repo, shipEnv, scriptArgs, scan); err != nil {
		scan.flush()
		// Name the phase it died in. "ship failed" alone sends someone into a
		// fifteen-minute log with no idea which of four Apple tools to look
		// for; `exporting` says "no distribution profile" before they open it.
		//
		// And carry the script's own last refusal when it made one. shipExec
		// can only report an exit status, but the two likeliest failures — a
		// dirty tree, a missing .p8 — are ones the script diagnoses in a full
		// sentence on its way out. Without this they were readable only by
		// opening a file on the Mac, which defeats the point of a button that
		// ships from a phone.
		detail := ""
		if refusal := scan.refusal(); refusal != "" {
			detail = " — " + refusal
		}
		return fail(fmt.Errorf("ship failed in %s: %w%s (see %s)", st.Phase, err, detail, jobs.ShipLogPath()))
	}
	scan.flush()

	if opts.dryRun {
		// The script printed its own plan; adding a second summary here would
		// just be kraken repeating it in different words.
		return nil
	}

	setPhase(jobs.ShipDone)
	util.LogEvent("ship-ios", "info", "ship-done", fmt.Sprintf("uploaded build %d to App Store Connect", st.Build))
	fmt.Fprintf(out, "uploaded build %d to App Store Connect — TestFlight still has to process it before the phone is offered the update\n", st.Build)
	return nil
}

// --- the stdout seam ---

// shipProgress is what one line of the script's output carried, if anything.
// Both fields zero means "an ordinary line of build output", which is the
// overwhelming majority of them.
type shipProgress struct {
	phase string
	build int
}

const (
	shipPhasePrefix = "KRAKEN-PHASE:"
	shipBuildPrefix = "KRAKEN-BUILD:"
	// shipDiePrefix is what ios/ship-testflight.sh's die() stamps on the first
	// line of every refusal it makes on its own — a dirty tree, a missing .p8,
	// an export that produced no .ipa. Those are the best sentences the feature
	// has and, until the scanner retained them, the only place they existed was
	// a file on the Mac: a phone-side failure read "ship failed in preflight:
	// ship-testflight.sh : exit status 1", which for a feature whose whole
	// point is shipping from anywhere is the one thing it must not say.
	shipDiePrefix = "ship-testflight: "
)

// parseShipLine reads one line of the script's stdout. Pure, and strict in
// both directions: a line must START with the marker — xcodebuild echoes its
// own command line, so a line that merely quotes one must not be mistaken for
// progress — and the payload must be a phase this binary knows or a positive
// integer.
//
// Everything unrecognized is silently an ordinary line rather than an error.
// The alternative is a ship that fails because the script gained a phase the
// installed binary predates, which is precisely backwards: the seam is
// decoration on a build that is otherwise going fine.
func parseShipLine(line string) shipProgress {
	line = strings.TrimSpace(line)
	if rest, ok := strings.CutPrefix(line, shipPhasePrefix); ok {
		phase := strings.TrimSpace(rest)
		if jobs.ShipKnownPhase(phase) {
			return shipProgress{phase: phase}
		}
		return shipProgress{}
	}
	if rest, ok := strings.CutPrefix(line, shipBuildPrefix); ok {
		n, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || n <= 0 {
			return shipProgress{}
		}
		return shipProgress{build: n}
	}
	return shipProgress{}
}

// shipScanner is the io.Writer the script's stdout and stderr are pointed at.
// It passes every byte through to the underlying writer unchanged — the log
// must be a faithful copy of what the tools printed, markers included, since
// that is what anyone debugging a failed upload will read — while splitting
// the stream into lines and reporting any progress marker it finds.
//
// A Writer rather than a bufio.Scanner over a pipe because it makes the exec
// seam trivial in both directions: production points cmd.Stdout here, and a
// test's stub simply writes synthetic script output into the same Writer, so
// the scanner, the phase mapping, and the status writes are all exercised by
// the same path the real thing takes.
type shipScanner struct {
	mu   sync.Mutex // stdout and stderr are two copier goroutines, one Writer
	w    io.Writer
	buf  []byte
	emit func(shipProgress)
	// die is the last `ship-testflight: …` line seen, without its prefix. Kept
	// here rather than emitted because it is not progress and has no phase to
	// report — it is the sentence the exit status alone cannot carry, read once
	// at the end by whoever builds the error (see refusal).
	die string
}

// shipDieMax caps how much of a refusal is carried into the error. die()'s
// messages are one line plus, sometimes, a heredoc of setup instructions; the
// first line is the diagnosis and the rest belongs in the log, which already
// has all of it verbatim. The cap is on the retained line itself so a stray
// megabyte-long stderr line starting with the prefix can't end up in a status
// file the board serves to the phone.
const shipDieMax = 300

// refusal returns the script's own last complaint, or "" if it never made one.
// Read after the command has exited and both copier goroutines are joined, so
// the lock is a formality — but this type is written to be safe under two
// writers and there is no reason for the reader to be the exception.
func (s *shipScanner) refusal() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.die
}

// shipScannerMaxLine caps the partial-line buffer. xcodebuild can emit a very
// long single line (a full compiler invocation), and a stream that somehow
// never produces a newline must not grow the buffer without bound for the
// hours an upload might last. Past the cap the pending fragment is dropped:
// the bytes have already been written through to the log, so nothing is lost
// except the chance to find a marker in a line far longer than any marker.
const shipScannerMaxLine = 64 << 10

func newShipScanner(w io.Writer, emit func(shipProgress)) *shipScanner {
	return &shipScanner{w: w, emit: emit}
}

func (s *shipScanner) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Write through FIRST and report its error verbatim: os/exec treats a
	// short write as a failure of the command, and a log that fell behind must
	// not also lose its byte count to the scanning below.
	n, err := s.w.Write(p)
	s.buf = append(s.buf, p[:n]...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		s.report(string(s.buf[:i]))
		s.buf = s.buf[i+1:]
	}
	if len(s.buf) > shipScannerMaxLine {
		s.buf = nil
	}
	return n, err
}

// flush reports a trailing line that never got its newline — a script killed
// mid-print, or one whose last line lacks the terminator. Called once, after
// the command has exited and both copier goroutines have been joined.
func (s *shipScanner) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.buf) == 0 {
		return
	}
	s.report(string(s.buf))
	s.buf = nil
}

// report is called with s.mu held, which is what serializes the emit callback
// against the second copier goroutine.
func (s *shipScanner) report(line string) {
	line = strings.TrimSuffix(line, "\r")
	// The script's own refusals come first: they are not progress, and the LAST
	// one wins because a `set -e` script's final complaint is the one that
	// stopped it.
	if rest, ok := strings.CutPrefix(strings.TrimSpace(line), shipDiePrefix); ok {
		if msg := strings.TrimSpace(rest); msg != "" {
			// Runes, not bytes: these messages carry → and ' and would
			// otherwise be cut mid-codepoint into an invalid UTF-8 string that
			// then has to survive a JSON round trip through the status file.
			if r := []rune(msg); len(r) > shipDieMax {
				msg = string(r[:shipDieMax]) + "…"
			}
			s.die = msg
		}
		return
	}
	p := parseShipLine(line)
	if p == (shipProgress{}) || s.emit == nil {
		return
	}
	s.emit(p)
}

// --- swappable seams: the subprocess, the lock, the preflight, and the status
// writer, so the tests drive the whole sequence without shelling out to Xcode,
// touching a real checkout, or uploading anything. Same package-var pattern
// cmd_update.go uses. ---
var (
	// shipExec runs the ship script in dir under env, streaming its combined
	// output into out. script is always an absolute path resolved by
	// jobs.PreflightShipIOS — never a bare name — because this process may have
	// been forked by launchd, whose PATH is not a login shell's.
	//
	// Stdout and stderr share one writer so the log reads in the order things
	// actually happened; xcodebuild puts warnings on stderr and progress on
	// stdout, and two separate captures would interleave wrongly or not at all.
	//
	// The environment is inherited whole (util.ToolchainEnv is os.Environ() plus a
	// PATH edit) because that is how ASC_KEY_ID and ASC_ISSUER_ID reach the
	// script. altool never names the .p8 on a command line (it finds the key by
	// convention), so the upload keeps the key FILE out of the process table. It
	// does pass the two ids, as --apiKey/--apiIssuer, and always has.
	// The export is where the key PATH reaches an argv too: xcodebuild has no
	// convention lookup and is given -authenticationKey* flags.
	// They still never reach this LOG — `xcodebuild archive` echoes its own argv
	// and `-exportArchive` does not, which is why ship-testflight.sh passes them
	// to the export alone.
	shipExec = func(script, dir string, env, args []string, out io.Writer) error {
		cmd := exec.Command(script, args...)
		cmd.Dir = dir
		cmd.Env = env
		cmd.Stdout = out
		cmd.Stderr = out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s %s: %v", filepath.Base(script), strings.Join(args, " "), err)
		}
		return nil
	}

	shipPreflight   = jobs.PreflightShipIOS
	shipAcquireLock = jobs.AcquireShipLock
	shipWriteStatus = jobs.WriteShipStatus
)
