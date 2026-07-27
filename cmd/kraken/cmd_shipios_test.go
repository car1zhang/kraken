package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// cmd_shipios_test.go — the ship sequence driven through cmd_shipios.go's
// seams: no Xcode, no checkout, no upload, no real ~/.kraken. Every test
// sandboxes HOME. jobs/shipios_test.go covers the pieces underneath (the
// status file, the flock, staleness, preflight's refusals); what is tested
// here is the ORDER things happen in, the stdout seam that decides what the
// board renders, and the branches that decide whether anything is written at
// all.

// --- harness ---

// shipHarness swaps every seam in cmd_shipios.go and restores them after the
// test. The status writer delegates to the real jobs.WriteShipStatus (HOME is
// sandboxed, so it lands in a temp dir) while recording the phase sequence,
// which the file itself can't show — it only ever holds the last one.
type shipHarness struct {
	home string

	phases   []string
	statuses []jobs.ShipStatus

	preflight  jobs.ShipPreflightInfo
	preflightE error

	lockOK     bool
	lockErr    error
	lockTaken  int
	lockFreed  int
	holderPID  int
	scriptOut  string // what the stubbed script prints
	scriptErr  error  // what the stubbed script exits with
	execCalls  int
	execScript string
	execDir    string
	execArgs   []string
	execEnv    []string

	events []string // "src level event msg", from util.SetEventSink
}

func newShipHarness(t *testing.T) *shipHarness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	h := &shipHarness{
		home:   home,
		lockOK: true,
		preflight: jobs.ShipPreflightInfo{
			Repo:         filepath.Join(home, "Developer", "kraken"),
			Script:       filepath.Join(home, "Developer", "kraken", jobs.ShipScriptRel),
			XcodegenPath: "/opt/homebrew/bin/xcodegen",
		},
	}

	prevPreflight, prevLock, prevWrite, prevExec := shipPreflight, shipAcquireLock, shipWriteStatus, shipExec
	shipPreflight = func() (jobs.ShipPreflightInfo, error) { return h.preflight, h.preflightE }
	shipAcquireLock = func() (func(), bool, error) {
		h.lockTaken++
		if h.lockErr != nil || !h.lockOK {
			return nil, h.lockOK, h.lockErr
		}
		return func() { h.lockFreed++ }, true, nil
	}
	shipWriteStatus = func(st jobs.ShipStatus) error {
		h.phases = append(h.phases, st.Phase)
		h.statuses = append(h.statuses, st)
		return jobs.WriteShipStatus(st)
	}
	shipExec = func(script, dir string, env, args []string, out io.Writer) error {
		h.execCalls++
		h.execScript, h.execDir, h.execEnv = script, dir, env
		h.execArgs = args
		// Written through the real scanner, so the phase mapping, the tee, and
		// the status writes are all exercised by the path production takes.
		_, _ = io.WriteString(out, h.scriptOut)
		return h.scriptErr
	}
	util.SetEventSink(func(src, level, event, msg string) {
		h.events = append(h.events, strings.Join([]string{src, level, event, msg}, " "))
	})
	t.Cleanup(func() {
		shipPreflight, shipAcquireLock, shipWriteStatus, shipExec = prevPreflight, prevLock, prevWrite, prevExec
		util.SetEventSink(nil)
	})
	return h
}

func (h *shipHarness) run(t *testing.T, opts shipIOSOpts) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runShipIOS(opts, &out)
	return out.String(), err
}

func (h *shipHarness) eventText() string { return strings.Join(h.events, "\n") }

// A realistic successful run, markers interleaved with the noise Xcode
// actually produces.
const shipScriptSuccess = `KRAKEN-PHASE: preflight
KRAKEN-BUILD: 412
KRAKEN-PHASE: generating
Loaded project ios/project.yml
Created project at ios/Kraken.xcodeproj
KRAKEN-PHASE: archiving
Build settings from command line:
    CURRENT_PROJECT_VERSION = 412
** ARCHIVE SUCCEEDED **
KRAKEN-PHASE: exporting
Exported Kraken to: /tmp/kraken-testflight.XXXX/export
KRAKEN-PHASE: uploading
UPLOAD SUCCEEDED with no errors.
KRAKEN-PHASE: done
uploaded build 412 to App Store Connect.
`

// --- arg parsing ---

func TestParseShipIOSArgs(t *testing.T) {
	cases := []struct {
		args    []string
		want    shipIOSOpts
		wantErr bool
	}{
		{args: nil, want: shipIOSOpts{}},
		{args: []string{"--dry-run"}, want: shipIOSOpts{dryRun: true}},
		{args: []string{"--detached"}, want: shipIOSOpts{detached: true}},
		{args: []string{"--detached", "--dry-run"}, want: shipIOSOpts{detached: true, dryRun: true}},
		{args: []string{"--help"}, want: shipIOSOpts{help: true}},
		{args: []string{"-h"}, want: shipIOSOpts{help: true}},
		// A mistyped flag must never fall through to a real archive+upload:
		// the build number an upload consumes is gone forever.
		{args: []string{"--dryrun"}, wantErr: true},
		{args: []string{"--dry-run=true"}, wantErr: true},
		{args: []string{"--force"}, wantErr: true},
		{args: []string{"--test"}, wantErr: true},
		// Explicitly rejected: it would let automation create certificates on
		// the real Apple developer account.
		{args: []string{"--allow-provisioning-updates"}, wantErr: true},
		{args: []string{"ios"}, wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseShipIOSArgs(tc.args)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseShipIOSArgs(%v) = %+v, want an error", tc.args, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseShipIOSArgs(%v): %v", tc.args, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseShipIOSArgs(%v) = %+v, want %+v", tc.args, got, tc.want)
		}
	}
}

// The usage text is the only place the credential requirement and the
// "uploaded, not installed" caveat are stated to someone at a terminal.
func TestShipIOSUsageTextCoversTheEssentials(t *testing.T) {
	for _, want := range []string{"--dry-run", "--detached", "ASC_KEY_ID", "ASC_ISSUER_ID", "ios/TESTFLIGHT.md", "not installed"} {
		if !strings.Contains(shipIOSUsageText, want) {
			t.Errorf("usage text does not mention %q", want)
		}
	}
	// --allow-provisioning-updates may be mentioned, but only as something to
	// run the SCRIPT with by hand — never as a flag of this verb, which
	// rejects it (see TestParseShipIOSArgs).
	if i := strings.Index(shipIOSUsageText, "--allow-provisioning-updates"); i >= 0 {
		if !strings.Contains(shipIOSUsageText[:i], "ios/ship-testflight.sh ") {
			t.Error("usage text offers --allow-provisioning-updates without attributing it to the script")
		}
	}
}

// --- the stdout seam ---

func TestParseShipLine(t *testing.T) {
	cases := []struct {
		line string
		want shipProgress
	}{
		{"KRAKEN-PHASE: archiving", shipProgress{phase: jobs.ShipArchiving}},
		{"  KRAKEN-PHASE: uploading  ", shipProgress{phase: jobs.ShipUploading}},
		{"KRAKEN-PHASE:done", shipProgress{phase: jobs.ShipDone}},
		{"KRAKEN-BUILD: 412", shipProgress{build: 412}},
		{"KRAKEN-BUILD:   7", shipProgress{build: 7}},

		// Malformed markers are ordinary lines, never an error: a script that
		// grows a phase this binary predates must not fail the ship over it.
		{"KRAKEN-PHASE:", shipProgress{}},
		{"KRAKEN-PHASE:   ", shipProgress{}},
		{"KRAKEN-PHASE: notarizing", shipProgress{}},
		{"KRAKEN-PHASE: ARCHIVING", shipProgress{}},
		{"KRAKEN-BUILD: ", shipProgress{}},
		{"KRAKEN-BUILD: abc", shipProgress{}},
		{"KRAKEN-BUILD: 0", shipProgress{}},
		{"KRAKEN-BUILD: -3", shipProgress{}},
		{"KRAKEN-BUILD: 4.1", shipProgress{}},

		// Ordinary build output, including lines that merely quote a marker
		// rather than starting with one.
		{"", shipProgress{}},
		{"** ARCHIVE SUCCEEDED **", shipProgress{}},
		{"echo KRAKEN-PHASE: uploading", shipProgress{}},
		{`note: printing "KRAKEN-BUILD: 999"`, shipProgress{}},
	}
	for _, tc := range cases {
		if got := parseShipLine(tc.line); got != tc.want {
			t.Errorf("parseShipLine(%q) = %+v, want %+v", tc.line, got, tc.want)
		}
	}
}

// The scanner must be a faithful tee — the log is what anyone debugging a
// failed upload reads, so not one byte may be dropped or reordered — while
// still finding markers split arbitrarily across Write calls, which is exactly
// what a pipe delivers.
func TestShipScannerTeesAndSplits(t *testing.T) {
	var sink bytes.Buffer
	var got []shipProgress
	s := newShipScanner(&sink, func(p shipProgress) { got = append(got, p) })

	// One marker deliberately torn across two writes.
	for _, chunk := range []string{"noise\nKRAKEN-PH", "ASE: archiving\nmore noise\nKRAKEN-BUILD: 412\n"} {
		if n, err := s.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	s.flush()

	if sink.String() != "noise\nKRAKEN-PHASE: archiving\nmore noise\nKRAKEN-BUILD: 412\n" {
		t.Fatalf("tee is not byte-faithful: %q", sink.String())
	}
	want := []shipProgress{{phase: jobs.ShipArchiving}, {build: 412}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("progress = %+v, want %+v", got, want)
	}
}

// A last line with no trailing newline — a script killed mid-print — is still
// reported, but only by flush, after the process has exited.
func TestShipScannerFlushReportsPartialLine(t *testing.T) {
	var got []shipProgress
	s := newShipScanner(io.Discard, func(p shipProgress) { got = append(got, p) })
	if _, err := s.Write([]byte("KRAKEN-PHASE: exporting")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("reported %+v before the line was terminated", got)
	}
	s.flush()
	if len(got) != 1 || got[0].phase != jobs.ShipExporting {
		t.Fatalf("after flush: %+v, want one exporting", got)
	}
	s.flush() // idempotent: nothing left to report
	if len(got) != 1 {
		t.Fatalf("second flush re-reported: %+v", got)
	}
}

// Windows-style line endings would otherwise make every marker unrecognizable
// (the phase would come back as "archiving\r").
func TestShipScannerHandlesCarriageReturns(t *testing.T) {
	var got []shipProgress
	s := newShipScanner(io.Discard, func(p shipProgress) { got = append(got, p) })
	if _, err := s.Write([]byte("KRAKEN-PHASE: archiving\r\n")); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].phase != jobs.ShipArchiving {
		t.Fatalf("progress = %+v, want one archiving", got)
	}
}

// A stream that never produces a newline must not grow the buffer for the
// hours an upload can last — but the bytes still reach the log.
func TestShipScannerCapsThePartialLine(t *testing.T) {
	var sink bytes.Buffer
	s := newShipScanner(&sink, func(shipProgress) { t.Error("no marker should be found") })
	huge := strings.Repeat("x", shipScannerMaxLine+1024)
	if _, err := s.Write([]byte(huge)); err != nil {
		t.Fatal(err)
	}
	if len(s.buf) != 0 {
		t.Fatalf("buffer held %d bytes past the cap, want it dropped", len(s.buf))
	}
	if sink.Len() != len(huge) {
		t.Fatalf("tee wrote %d bytes, want all %d", sink.Len(), len(huge))
	}
}

// A write error from the underlying log must be reported verbatim: os/exec
// treats a short write as a failure of the command, and swallowing it would
// turn a full disk into a silently truncated forensic record.
func TestShipScannerPropagatesWriteErrors(t *testing.T) {
	boom := errors.New("disk full")
	s := newShipScanner(errWriter{err: boom}, nil)
	if _, err := s.Write([]byte("anything\n")); !errors.Is(err, boom) {
		t.Fatalf("Write err = %v, want %v", err, boom)
	}
}

type errWriter struct{ err error }

func (e errWriter) Write(p []byte) (int, error) { return 0, e.err }

// --- the run ---

func TestRunShipIOSHappyPath(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = shipScriptSuccess

	out, err := h.run(t, shipIOSOpts{})
	if err != nil {
		t.Fatalf("runShipIOS: %v", err)
	}

	wantPhases := []string{
		jobs.ShipPreflight, // ours, before the script starts
		jobs.ShipPreflight, // the script's own announcement
		jobs.ShipGenerating, jobs.ShipArchiving, jobs.ShipExporting, jobs.ShipUploading,
		jobs.ShipDone,
	}
	if strings.Join(h.phases, ",") != strings.Join(wantPhases, ",") {
		t.Fatalf("phases = %v, want %v", h.phases, wantPhases)
	}
	if h.execCalls != 1 || len(h.execArgs) != 0 {
		t.Fatalf("script ran %d time(s) with args %v, want once with none", h.execCalls, h.execArgs)
	}
	if h.execScript != h.preflight.Script || h.execDir != h.preflight.Repo {
		t.Fatalf("ran %s in %s, want %s in %s", h.execScript, h.execDir, h.preflight.Script, h.preflight.Repo)
	}
	if h.lockTaken != 1 || h.lockFreed != 1 {
		t.Fatalf("lock taken %d, freed %d, want 1/1", h.lockTaken, h.lockFreed)
	}

	final := jobs.ReadShipStatus()
	if final.Phase != jobs.ShipDone || final.Build != 412 {
		t.Fatalf("final status = %+v, want done at build 412", final)
	}
	// The build number reaches the record as soon as the script announces it,
	// not only at the end: a failed upload has to say which number it burned.
	if h.statuses[2].Build != 412 {
		t.Fatalf("build number missing from the generating record: %+v", h.statuses[2])
	}
	if !strings.Contains(out, "** ARCHIVE SUCCEEDED **") {
		t.Errorf("script output did not reach the caller's writer:\n%s", out)
	}
	if !strings.Contains(out, "412") || !strings.Contains(out, "TestFlight") {
		t.Errorf("closing line should name the build and say it is not installed yet:\n%s", out)
	}
}

// Phase order is the whole point of the stdout seam, so assert the events
// carry it too — that log is how a failed ship is reconstructed after the fact.
func TestRunShipIOSLogsAPhaseEvent(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = shipScriptSuccess
	if _, err := h.run(t, shipIOSOpts{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ship-ios info ship-start", "ship-ios info ship-phase archiving", "ship-ios info ship-build", "ship-ios info ship-done"} {
		if !strings.Contains(h.eventText(), want) {
			t.Errorf("event log missing %q:\n%s", want, h.eventText())
		}
	}
}

// A script that prints nothing recognizable still produces a coherent record:
// our own preflight, then done off the exit status.
func TestRunShipIOSWithNoPhaseLines(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = "just some output\nno markers here at all\n"

	if _, err := h.run(t, shipIOSOpts{}); err != nil {
		t.Fatalf("runShipIOS: %v", err)
	}
	if strings.Join(h.phases, ",") != jobs.ShipPreflight+","+jobs.ShipDone {
		t.Fatalf("phases = %v, want preflight,done", h.phases)
	}
	if got := jobs.ReadShipStatus(); got.Build != 0 {
		t.Fatalf("build = %d with no KRAKEN-BUILD line, want 0", got.Build)
	}
}

// The terminal state comes from the EXIT STATUS, never from a line of stdout:
// a script that prints `done` and then dies in its exit trap must not be
// recorded as a successful upload.
func TestRunShipIOSDoneLineDoesNotOutrankAFailedExit(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = shipScriptSuccess
	h.scriptErr = errors.New("exit status 1")

	_, err := h.run(t, shipIOSOpts{})
	if err == nil {
		t.Fatal("runShipIOS returned nil for a script that failed")
	}
	if got := jobs.ReadShipStatus(); got.Phase != jobs.ShipFailed {
		t.Fatalf("phase = %q, want %q", got.Phase, jobs.ShipFailed)
	}
}

// A failure has to name the phase it died in — "ship failed" alone sends
// someone into a fifteen-minute log with no idea which tool to look for.
func TestRunShipIOSFailureNamesThePhase(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = "KRAKEN-PHASE: preflight\nKRAKEN-BUILD: 412\nKRAKEN-PHASE: generating\nKRAKEN-PHASE: archiving\nKRAKEN-PHASE: exporting\nerror: exportArchive No profiles for 'dev.carl.Kraken' were found\n"
	h.scriptErr = errors.New("exit status 70")

	_, err := h.run(t, shipIOSOpts{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{jobs.ShipExporting, "exit status 70", "ship-ios.log"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	final := jobs.ReadShipStatus()
	if final.Phase != jobs.ShipFailed || !strings.Contains(final.Error, jobs.ShipExporting) {
		t.Fatalf("final status = %+v, want failed naming the exporting phase", final)
	}
	if final.Build != 412 {
		t.Errorf("failed record lost the build number: %+v", final)
	}
	if h.lockFreed != 1 {
		t.Errorf("lock freed %d times on the failure path, want 1", h.lockFreed)
	}
}

// The script's own refusals are the best sentences the feature has, and the
// exit status carries none of them. Without this, the two likeliest failures —
// a dirty tree and a missing .p8 — reached the phone as "exit status 1" and
// were diagnosable only by opening a file on the Mac.
func TestRunShipIOSCarriesTheScriptsRefusal(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = "KRAKEN-PHASE: preflight\nship-testflight: working tree is dirty — commit or stash first, so the build number matches what ships\n"
	h.scriptErr = errors.New("exit status 1")

	_, err := h.run(t, shipIOSOpts{})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{jobs.ShipPreflight, "exit status 1", "working tree is dirty", "ship-ios.log"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	// And it has to reach the record, which is what the board and the phone
	// actually render — the error return only ever reaches a terminal.
	if final := jobs.ReadShipStatus(); !strings.Contains(final.Error, "working tree is dirty") {
		t.Fatalf("recorded error = %q, want the script's own refusal", final.Error)
	}
}

// The LAST refusal wins (a `set -e` script's final complaint is what stopped
// it), an ordinary line that merely mentions the script's name is not one, and
// the retained text is capped so a runaway stderr line can't ride into a status
// file the board serves to the phone.
func TestShipScannerRetainsTheLastRefusalOnly(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want string
	}{
		{"none", "KRAKEN-PHASE: archiving\n** ARCHIVE SUCCEEDED **\n", ""},
		{
			"last one wins",
			"ship-testflight: xcodegen not found — brew install xcodegen\nship-testflight: export produced no .ipa in /tmp/x\n",
			"export produced no .ipa in /tmp/x",
		},
		{
			// xcodebuild echoes command lines; a mention is not a refusal.
			"a mention is not a refusal",
			"running ios/ship-testflight: --dry-run\n",
			"",
		},
		{"bare prefix carries nothing", "ship-testflight: \n", ""},
		{"unterminated final line still counts", "ship-testflight: no App Store Connect private key", "no App Store Connect private key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newShipScanner(io.Discard, nil)
			if _, err := io.WriteString(s, tc.out); err != nil {
				t.Fatal(err)
			}
			s.flush()
			if got := s.refusal(); got != tc.want {
				t.Fatalf("refusal() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("capped", func(t *testing.T) {
		s := newShipScanner(io.Discard, nil)
		// Multi-byte on purpose: a byte-wise cut here would produce invalid
		// UTF-8 that then has to survive a JSON round trip through the record.
		if _, err := io.WriteString(s, "ship-testflight: "+strings.Repeat("→", shipDieMax*2)+"\n"); err != nil {
			t.Fatal(err)
		}
		got := s.refusal()
		if n := len([]rune(got)); n != shipDieMax+1 { // +1 for the ellipsis
			t.Fatalf("refusal is %d runes, want %d plus an ellipsis", n, shipDieMax)
		}
		if !utf8.ValidString(got) {
			t.Fatalf("refusal is not valid UTF-8: %q", got)
		}
	})
}

// --- dry run ---

// A dry run must mutate nothing kraken owns: no status file, no log, and a
// previous run's record left exactly as it was.
func TestRunShipIOSDryRunWritesNothing(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = "KRAKEN-PHASE: preflight\nKRAKEN-BUILD: 412\nDRY RUN — preflight passed\nKRAKEN-PHASE: done\n"

	// A record from an earlier real run, which the dry run must not touch.
	prior := jobs.ShipStatus{Phase: jobs.ShipFailed, StartedAt: 1000, Build: 411, Error: "previous failure"}
	if err := jobs.WriteShipStatus(prior); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(jobs.ShipStatusPath())
	if err != nil {
		t.Fatal(err)
	}

	out, err := h.run(t, shipIOSOpts{dryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(h.execArgs) != 1 || h.execArgs[0] != "--dry-run" {
		t.Fatalf("script args = %v, want [--dry-run]", h.execArgs)
	}
	if len(h.phases) != 0 {
		t.Fatalf("dry run wrote phases %v, want none", h.phases)
	}
	after, err := os.ReadFile(jobs.ShipStatusPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("dry run rewrote ship-ios.json:\n before %s\n after  %s", before, after)
	}
	if _, err := os.Stat(jobs.ShipLogPath()); !os.IsNotExist(err) {
		t.Fatalf("dry run created %s (stat err %v), want it untouched", jobs.ShipLogPath(), err)
	}
	if strings.Contains(out, "uploaded build") {
		t.Errorf("dry run claimed an upload:\n%s", out)
	}
	if !strings.Contains(out, "DRY RUN") {
		t.Errorf("script's own plan did not reach the caller:\n%s", out)
	}
}

// --- the log ---

func TestRunShipIOSTeesToTheLog(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = shipScriptSuccess

	if _, err := h.run(t, shipIOSOpts{}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jobs.ShipLogPath())
	if err != nil {
		t.Fatalf("read ship-ios.log: %v", err)
	}
	if !strings.Contains(string(data), "** ARCHIVE SUCCEEDED **") {
		t.Fatalf("log missing the script's output:\n%s", data)
	}
	if !strings.Contains(string(data), "=== kraken ship-ios") {
		t.Fatalf("log missing its per-run banner:\n%s", data)
	}

	// A second run appends rather than truncating: the previous run's failure
	// is exactly what someone comes looking for.
	if _, err := h.run(t, shipIOSOpts{}); err != nil {
		t.Fatal(err)
	}
	data2, err := os.ReadFile(jobs.ShipLogPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data2), "=== kraken ship-ios") != 2 {
		t.Fatalf("second run did not append:\n%s", data2)
	}
}

// Detached, stdout IS the log — the spawner opened it — so opening it again
// here would double every line of a fifteen-minute build.
func TestRunShipIOSDetachedDoesNotOpenTheLogItself(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = shipScriptSuccess

	if _, err := h.run(t, shipIOSOpts{detached: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(jobs.ShipLogPath()); !os.IsNotExist(err) {
		t.Fatalf("detached run opened %s itself (stat err %v)", jobs.ShipLogPath(), err)
	}
}

// --- detached / contention ---

// The API stamps StartedAt when the button is clicked; the child inherits it
// so ElapsedMS measures the run the user actually watched, not the part after
// fork+exec.
func TestRunShipIOSDetachedInheritsStartedAt(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = shipScriptSuccess
	opened := time.Now().UnixMilli() - 30_000
	if err := jobs.WriteShipStatus(jobs.ShipStatus{Phase: jobs.ShipPreflight, StartedAt: opened}); err != nil {
		t.Fatal(err)
	}

	if _, err := h.run(t, shipIOSOpts{detached: true}); err != nil {
		t.Fatal(err)
	}
	final := jobs.ReadShipStatus()
	if final.StartedAt != opened {
		t.Fatalf("StartedAt = %d, want the API's %d", final.StartedAt, opened)
	}
	if final.ElapsedMS < 30_000 {
		t.Fatalf("ElapsedMS = %d, want it to include the pre-spawn wait", final.ElapsedMS)
	}
}

// Interactive contention must not stomp the record: it belongs to whoever
// holds the lock, and overwriting it would replace a healthy upload's phase
// with our refusal.
func TestRunShipIOSContentionInteractiveLeavesTheRecord(t *testing.T) {
	h := newShipHarness(t)
	h.lockOK = false
	live := jobs.ShipStatus{Phase: jobs.ShipUploading, StartedAt: time.Now().UnixMilli(), PID: os.Getpid(), Build: 412}
	if err := jobs.WriteShipStatus(live); err != nil {
		t.Fatal(err)
	}

	_, err := h.run(t, shipIOSOpts{})
	if err == nil || !strings.Contains(err.Error(), "already in flight") {
		t.Fatalf("err = %v, want a contention refusal", err)
	}
	if h.execCalls != 0 {
		t.Fatal("contention still ran the script")
	}
	if got := jobs.ReadShipStatus(); got.Phase != jobs.ShipUploading || got.Build != 412 {
		t.Fatalf("contention stomped the holder's record: %+v", got)
	}
}

// Detached, the record on disk is the one the API opened FOR US, so if we say
// nothing it sits at preflight until staleShip's grace expires and then lies
// about why ("ship never started").
func TestRunShipIOSContentionDetachedRecordsTheReason(t *testing.T) {
	h := newShipHarness(t)
	h.lockOK = false
	if err := jobs.WriteShipStatus(jobs.ShipStatus{Phase: jobs.ShipPreflight, StartedAt: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}

	_, err := h.run(t, shipIOSOpts{detached: true})
	if err == nil {
		t.Fatal("expected a contention refusal")
	}
	got := jobs.ReadShipStatus()
	if got.Phase != jobs.ShipFailed || !strings.Contains(got.Error, "already in flight") {
		t.Fatalf("record = %+v, want failed with the contention reason", got)
	}
}

func TestRunShipIOSLockErrorIsNotContention(t *testing.T) {
	h := newShipHarness(t)
	h.lockErr = errors.New("permission denied")

	_, err := h.run(t, shipIOSOpts{})
	if err == nil || !strings.Contains(err.Error(), "acquire ship lock") {
		t.Fatalf("err = %v, want a distinct lock-acquisition failure", err)
	}
	if got := jobs.ReadShipStatus(); got.Phase != jobs.ShipFailed {
		t.Fatalf("phase = %q, want %q", got.Phase, jobs.ShipFailed)
	}
}

// --- preflight ---

func TestRunShipIOSPreflightRefusalDoesNotRunTheScript(t *testing.T) {
	h := newShipHarness(t)
	h.preflightE = errors.New("App Store Connect credentials missing: ASC_KEY_ID not set")

	_, err := h.run(t, shipIOSOpts{})
	if err == nil || !strings.Contains(err.Error(), "ASC_KEY_ID") {
		t.Fatalf("err = %v, want preflight's own sentence", err)
	}
	if h.execCalls != 0 || h.lockTaken != 0 {
		t.Fatalf("refused run still ran the script (%d) or took the lock (%d)", h.execCalls, h.lockTaken)
	}
	if got := jobs.ReadShipStatus(); got.Phase != jobs.ShipFailed {
		t.Fatalf("phase = %q, want %q (nothing was in flight, so the record is ours to claim)", got.Phase, jobs.ShipFailed)
	}
}

// ...but only when nothing is in flight: a `k ship-ios` typed without
// credentials must not stomp a running ship's phase with its own refusal.
func TestRunShipIOSPreflightRefusalSparesAnInFlightRun(t *testing.T) {
	h := newShipHarness(t)
	h.preflightE = errors.New("credentials missing")
	live := jobs.ShipStatus{Phase: jobs.ShipArchiving, StartedAt: time.Now().UnixMilli(), PID: os.Getpid(), Build: 412}
	if err := jobs.WriteShipStatus(live); err != nil {
		t.Fatal(err)
	}

	if _, err := h.run(t, shipIOSOpts{}); err == nil {
		t.Fatal("expected a refusal")
	}
	if got := jobs.ReadShipStatus(); got.Phase != jobs.ShipArchiving || got.Build != 412 {
		t.Fatalf("refusal stomped the in-flight record: %+v", got)
	}
}

// The launchd-PATH gap, closed the same way the updater closes it for `go`:
// the script invokes a bare `xcodegen`, and launchd's PATH does not reach the
// homebrew prefix it lives in.
func TestRunShipIOSPutsXcodegenOnPath(t *testing.T) {
	h := newShipHarness(t)
	h.scriptOut = shipScriptSuccess
	if _, err := h.run(t, shipIOSOpts{}); err != nil {
		t.Fatal(err)
	}
	var path string
	for _, kv := range h.execEnv {
		if rest, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = rest
		}
	}
	if !strings.HasPrefix(path, "/opt/homebrew/bin:") {
		t.Fatalf("script PATH = %q, want the resolved xcodegen's dir first", path)
	}
}

// --- credentials never leak ---

// The credentials must reach the script's environment and NOTHING else: not
// the status file the board serves to the phone, not the event log, not
// stdout, not the run log on disk.
func TestRunShipIOSNeverLeaksCredentials(t *testing.T) {
	const keyID, issuerID = "LEAKKEYID999", "leak-issuer-9999"
	t.Setenv("ASC_KEY_ID", keyID)
	t.Setenv("ASC_ISSUER_ID", issuerID)

	h := newShipHarness(t)
	h.scriptOut = shipScriptSuccess
	h.scriptErr = errors.New("exit status 1") // the failure path, which is chattier

	out, err := h.run(t, shipIOSOpts{})
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	status, readErr := os.ReadFile(jobs.ShipStatusPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	log, readErr := os.ReadFile(jobs.ShipLogPath())
	if readErr != nil {
		t.Fatal(readErr)
	}
	haystacks := map[string]string{
		"stdout":        out,
		"error":         err.Error(),
		"ship-ios.json": string(status),
		"ship-ios.log":  string(log),
		"event log":     h.eventText(),
	}
	for where, hay := range haystacks {
		for _, secret := range []string{keyID, issuerID} {
			if strings.Contains(hay, secret) {
				t.Errorf("%s leaks a credential: %q", where, hay)
			}
		}
	}

	// ...while the script's own environment still carries both, since that is
	// the only way altool can authenticate.
	env := strings.Join(h.execEnv, "\n")
	if !strings.Contains(env, "ASC_KEY_ID="+keyID) || !strings.Contains(env, "ASC_ISSUER_ID="+issuerID) {
		t.Error("the script's environment lost the credentials it needs")
	}
}

// TestShipScriptKeepsCredentialOffArchive pins WHERE the App Store Connect
// credential may appear in ios/ship-testflight.sh, which is the only thing
// keeping it out of ~/.kraken/ship-ios.log.
//
// `xcodebuild archive` prints its own full argv as its first line of output and
// `-exportArchive` does not (measured over this machine's log: 12 archive
// echoes, 0 export echoes). shipScanner tees that output verbatim into a
// world-readable, append-only, never-rotated log, so the same flags that are
// harmless on the export write the key id — which is embedded in the .p8's
// filename — permanently into the log when they ride on the archive.
//
// The first draft of the fix passed them to both commands, which is how easily
// this goes wrong; a later edit that "symmetrizes" the two calls would restore
// the leak with every other test still green, and nothing would report it.
func TestShipScriptKeepsCredentialOffArchive(t *testing.T) {
	// A checkout without ios/ skips; anything else is a hard failure. A test whose
	// whole value is failing loudly must not go quiet because it could not read
	// its own subject — same split as TestJobPaletteMatchesSwift.
	src, err := os.ReadFile("../../ios/ship-testflight.sh")
	if os.IsNotExist(err) {
		t.Skip("no ios/ship-testflight.sh in this checkout")
	}
	if err != nil {
		t.Fatalf("read ios/ship-testflight.sh: %v", err)
	}
	archive := xcodebuildCommand(t, string(src), "xcodebuild archive")
	export := xcodebuildCommand(t, string(src), "xcodebuild -exportArchive")

	for _, banned := range []string{"ASC_AUTH", "-authenticationKey"} {
		if strings.Contains(archive, banned) {
			t.Errorf("`xcodebuild archive` carries %s — it echoes its argv into "+
				"~/.kraken/ship-ios.log, so the credential must stay on the export:\n%s",
				banned, archive)
		}
	}
	// And the export must still HAVE it, or the export cannot authenticate and
	// the whole pipeline is back to `exportArchive No Accounts`. Either spelling
	// counts: expanding the flags inline instead of via the array preserves the
	// invariant exactly, and the archive ban above keeps the leak closed either way.
	if !strings.Contains(export, "ASC_AUTH") && !strings.Contains(export, "-authenticationKey") {
		t.Errorf("`xcodebuild -exportArchive` lost its App Store Connect credential:\n%s", export)
	}
}

// xcodebuildCommand returns the one shell command in src that starts with
// prefix: that line plus every continuation line, stopping at the first line
// that is not continued. Comments above the command are excluded on purpose —
// they discuss the flags without passing them.
func xcodebuildCommand(t *testing.T, src, prefix string) string {
	t.Helper()
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), prefix) {
			continue
		}
		cmd := []string{line}
		for j := i; strings.HasSuffix(strings.TrimSpace(lines[j]), `\`); {
			j++
			if j >= len(lines) {
				break
			}
			cmd = append(cmd, lines[j])
		}
		return strings.Join(cmd, "\n")
	}
	t.Fatalf("ios/ship-testflight.sh has no %q command", prefix)
	return ""
}
