package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shipios_test.go — the run record behind `kraken ship-ios`. Nothing here
// shells out: the only subprocess the feature has lives in
// ios/ship-testflight.sh and is driven through cmd/kraken's seam, so this file
// is purely the file/lock/staleness layer plus preflight's refusals.

// writeShipEnv sets both credentials for a test that needs preflight to get
// past them. The values are obvious nonsense on purpose — every test that
// asserts something about what we log or persist checks these strings are
// absent from it.
const (
	testKeyID    = "TESTKEYID000"
	testIssuerID = "test-issuer-0000-0000"
)

func setShipCreds(t *testing.T) {
	t.Helper()
	t.Setenv("ASC_KEY_ID", testKeyID)
	t.Setenv("ASC_ISSUER_ID", testIssuerID)
}

// onMainGit is the git fixture a ship preflight sees on a healthy checkout.
// PreflightShipIOS asks exactly one question of git (which branch), and
// gitStub errors on anything unstubbed, so a preflight that grows a second git
// call fails loudly here rather than quietly shelling out.
func stubShipGit(t *testing.T, branch string) {
	t.Helper()
	stubGit(t, gitStub{"rev-parse --abbrev-ref HEAD": branch}.run)
}

// fakeShipScript drops an executable stand-in for ios/ship-testflight.sh into
// the fake kraken checkout. It is never executed by these tests — preflight
// only stats it.
func fakeShipScript(t *testing.T, repo string) string {
	t.Helper()
	path := filepath.Join(repo, ShipScriptRel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/bash\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- phases + status file ---

func TestShipTerminal(t *testing.T) {
	terminal := []string{ShipIdle, ShipDone, ShipFailed}
	inFlight := []string{ShipPreflight, ShipGenerating, ShipArchiving, ShipExporting, ShipUploading}
	for _, p := range terminal {
		if !ShipTerminal(p) {
			t.Errorf("ShipTerminal(%q) = false, want true", p)
		}
	}
	for _, p := range inFlight {
		if ShipTerminal(p) {
			t.Errorf("ShipTerminal(%q) = true, want false", p)
		}
	}
}

// ShipKnownPhase is the stdout scanner's whitelist, so it has to accept every
// phase the script can print and reject anything else — including the empty
// string, which is what a bare "KRAKEN-PHASE:" line yields.
func TestShipKnownPhase(t *testing.T) {
	for _, p := range []string{ShipIdle, ShipPreflight, ShipGenerating, ShipArchiving, ShipExporting, ShipUploading, ShipDone, ShipFailed} {
		if !ShipKnownPhase(p) {
			t.Errorf("ShipKnownPhase(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"", " ", "PREFLIGHT", "notarizing", "building", "uploaded"} {
		if ShipKnownPhase(p) {
			t.Errorf("ShipKnownPhase(%q) = true, want false", p)
		}
	}
}

func TestShipStatusJSONKeys(t *testing.T) {
	data, err := json.Marshal(ShipStatus{
		Phase: ShipArchiving, StartedAt: 1, UpdatedAt: 2, PID: 3, Build: 4, Error: "boom", ElapsedMS: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	// Read by the web board and the iOS app; a rename is a breaking change.
	want := []string{"phase", "started_at", "updated_at", "pid", "build", "error", "elapsed_ms"}
	for _, k := range want {
		if _, ok := raw[k]; !ok {
			t.Errorf("marshalled ShipStatus missing key %q: %s", k, data)
		}
	}
	if len(raw) != len(want) {
		t.Errorf("ShipStatus has %d wire keys, want exactly %d (%v): %s", len(raw), len(want), want, data)
	}
	var back ShipStatus
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Phase != ShipArchiving || back.PID != 3 || back.Build != 4 || back.ElapsedMS != 5 || back.Error != "boom" {
		t.Fatalf("round trip lost fields: %+v", back)
	}
}

func TestReadShipStatusMissingFileIsIdle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if st := ReadShipStatus(); st.Phase != ShipIdle {
		t.Fatalf("no ship-ios.json: phase = %q, want %q", st.Phase, ShipIdle)
	}
}

func TestReadShipStatusMalformedIsIdle(t *testing.T) {
	for _, contents := range []string{"", "{", "not json at all", `{"phase":`, `[]`, `{"phase":""}`} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ShipStatusPath(), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if st := ReadShipStatus(); st.Phase != ShipIdle {
			t.Fatalf("ship-ios.json = %q: phase = %q, want %q", contents, st.Phase, ShipIdle)
		}
	}
}

func TestWriteThenReadShipStatusRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	start := time.Now().UnixMilli() - 1000
	// Our own pid keeps staleShip's liveness rule satisfied, so the read back
	// is the record as written rather than a downgrade.
	if err := WriteShipStatus(ShipStatus{Phase: ShipUploading, StartedAt: start, PID: os.Getpid(), Build: 412}); err != nil {
		t.Fatal(err)
	}
	got := ReadShipStatus()
	if got.Phase != ShipUploading || got.Build != 412 || got.PID != os.Getpid() {
		t.Fatalf("round trip: %+v", got)
	}
	if got.UpdatedAt == 0 || got.ElapsedMS < 1000 {
		t.Fatalf("WriteShipStatus should stamp UpdatedAt and derive ElapsedMS: %+v", got)
	}
}

// A record that has never run carries no elapsed time — deriving one from a
// zero StartedAt would report the milliseconds since 1970.
func TestWriteShipStatusNoElapsedWithoutStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := WriteShipStatus(ShipStatus{Phase: ShipIdle}); err != nil {
		t.Fatal(err)
	}
	if got := ReadShipStatus(); got.ElapsedMS != 0 {
		t.Fatalf("ElapsedMS = %d with StartedAt 0, want 0", got.ElapsedMS)
	}
}

// --- staleness: the three rules, as a table ---

func TestStaleShip(t *testing.T) {
	now := time.Now().UnixMilli()
	cases := []struct {
		name      string
		st        ShipStatus
		alive     bool
		wantPhase string
		wantErr   string // substring, "" = expect no error text
	}{
		{
			name:      "terminal phases are never touched",
			st:        ShipStatus{Phase: ShipDone, PID: 999999, StartedAt: now - 10*shipMaxRun.Milliseconds()},
			alive:     false,
			wantPhase: ShipDone,
		},
		{
			name:      "idle with a dead pid stays idle",
			st:        ShipStatus{Phase: ShipIdle},
			alive:     false,
			wantPhase: ShipIdle,
		},
		{
			name:      "pid 0 inside the claim grace is believed",
			st:        ShipStatus{Phase: ShipPreflight, StartedAt: now - shipClaimGraceMS/2},
			alive:     false,
			wantPhase: ShipPreflight,
		},
		{
			name:      "pid 0 past the claim grace never started",
			st:        ShipStatus{Phase: ShipPreflight, StartedAt: now - shipClaimGraceMS - 1},
			alive:     false,
			wantPhase: ShipFailed,
			wantErr:   "never started",
		},
		{
			// The window is bounded on BOTH sides. A future StartedAt satisfies
			// `now - StartedAt <= grace` forever, and since the PID-0 branch
			// returns, nothing downstream could ever rescue the button: every
			// POST would 409 until someone deleted the file by hand.
			name:      "pid 0 with a future StartedAt is not believed",
			st:        ShipStatus{Phase: ShipPreflight, StartedAt: now + time.Hour.Milliseconds()},
			alive:     false,
			wantPhase: ShipFailed,
			wantErr:   "never started",
		},
		{
			name:      "dead pid fails and names it",
			st:        ShipStatus{Phase: ShipArchiving, PID: 4321, StartedAt: now - 1000, UpdatedAt: now - 1000},
			alive:     false,
			wantPhase: ShipFailed,
			wantErr:   "4321",
		},
		{
			name:      "alive and progressing is left alone",
			st:        ShipStatus{Phase: ShipUploading, PID: 4321, StartedAt: now - 10*time.Minute.Milliseconds(), UpdatedAt: now - 1000},
			alive:     true,
			wantPhase: ShipUploading,
		},
		{
			// The deadline rule. A long upload must NOT trip it — this is why
			// shipMaxRun is 45m and not updateMaxRun's 15m.
			name:      "alive and 20 minutes into an upload is still fine",
			st:        ShipStatus{Phase: ShipUploading, PID: 4321, StartedAt: now - 25*time.Minute.Milliseconds(), UpdatedAt: now - 20*time.Minute.Milliseconds()},
			alive:     true,
			wantPhase: ShipUploading,
		},
		{
			name:      "alive but past the deadline is stalled",
			st:        ShipStatus{Phase: ShipUploading, PID: 4321, StartedAt: now - 2*shipMaxRun.Milliseconds(), UpdatedAt: now - shipMaxRun.Milliseconds() - 1000},
			alive:     true,
			wantPhase: ShipFailed,
			wantErr:   "stalled",
		},
		{
			// UpdatedAt is what the deadline reads; StartedAt only fills in for
			// a record old enough to predate that stamp.
			name:      "deadline falls back to StartedAt when UpdatedAt is missing",
			st:        ShipStatus{Phase: ShipExporting, PID: 4321, StartedAt: now - shipMaxRun.Milliseconds() - 1000},
			alive:     true,
			wantPhase: ShipFailed,
			wantErr:   "stalled",
		},
		{
			name:      "no clock at all is left alone",
			st:        ShipStatus{Phase: ShipExporting, PID: 4321},
			alive:     true,
			wantPhase: ShipExporting,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := staleShip(tc.st, tc.alive, now)
			if got.Phase != tc.wantPhase {
				t.Errorf("phase = %q, want %q (err %q)", got.Phase, tc.wantPhase, got.Error)
			}
			if tc.wantErr == "" && got.Error != "" {
				t.Errorf("error = %q, want none", got.Error)
			}
			if tc.wantErr != "" && !strings.Contains(got.Error, tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", got.Error, tc.wantErr)
			}
		})
	}
}

func TestReadShipStatusDowngradesDeadShipper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := shipProcessAlive
	shipProcessAlive = func(int) bool { return false }
	t.Cleanup(func() { shipProcessAlive = prev })

	if err := WriteShipStatus(ShipStatus{Phase: ShipArchiving, StartedAt: time.Now().UnixMilli(), PID: 4321}); err != nil {
		t.Fatal(err)
	}
	got := ReadShipStatus()
	if got.Phase != ShipFailed || !strings.Contains(got.Error, "4321") {
		t.Fatalf("dead shipper reads as %+v, want failed naming pid 4321", got)
	}
}

// The wedged-shipper path end to end: a record whose pid is genuinely alive
// (this test process') but hasn't moved in longer than shipMaxRun must read as
// failed, so the SHIP button unblocks instead of 409ing forever.
func TestReadShipStatusFailsAStalledShipper(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stalled := time.Now().Add(-shipMaxRun - time.Minute).UnixMilli()
	// Written straight to the file: WriteShipStatus would stamp UpdatedAt with
	// now, which is exactly the field under test.
	if err := os.MkdirAll(filepath.Dir(ShipStatusPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(ShipStatus{Phase: ShipUploading, StartedAt: stalled, UpdatedAt: stalled, PID: os.Getpid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ShipStatusPath(), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadShipStatus()
	if got.Phase != ShipFailed || !ShipTerminal(got.Phase) {
		t.Fatalf("stalled shipper reads as %+v, want a terminal %q so the button unblocks", got, ShipFailed)
	}
	if !strings.Contains(got.Error, "stalled") {
		t.Fatalf("error = %q, want it to say the run stalled", got.Error)
	}
}

// --- lock ---

func TestAcquireShipLockSecondAcquireFailsWhileHeld(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	release, ok, err := AcquireShipLock()
	if !ok || err != nil {
		t.Fatalf("first acquire: ok=%v err=%v, want ok=true err=nil", ok, err)
	}
	defer release()

	_, ok2, err2 := AcquireShipLock()
	if ok2 || err2 != nil {
		t.Fatalf("second acquire while held: ok=%v err=%v, want ok=false err=nil (contention, not an error)", ok2, err2)
	}
}

func TestAcquireShipLockSucceedsAfterRelease(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	release, ok, err := AcquireShipLock()
	if !ok || err != nil {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}
	release()

	release2, ok2, err2 := AcquireShipLock()
	if !ok2 || err2 != nil {
		t.Fatalf("acquire after release: ok=%v err=%v, want ok=true err=nil", ok2, err2)
	}
	release2()
}

// The ship lock and the update lock are different files: a 20-minute upload
// must not block a 40-second self-update, and vice versa.
func TestShipAndUpdateLocksAreIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	shipRelease, ok, err := AcquireShipLock()
	if !ok || err != nil {
		t.Fatalf("ship acquire: ok=%v err=%v", ok, err)
	}
	defer shipRelease()

	updRelease, ok, err := AcquireUpdateLock()
	if !ok || err != nil {
		t.Fatalf("update acquire while ship lock held: ok=%v err=%v, want it to succeed", ok, err)
	}
	updRelease()
}

func TestShipLockHolderPIDNamesTheHolder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	release, ok, err := AcquireShipLock()
	if !ok || err != nil {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}
	defer release()

	if got := ShipLockHolderPID(); got != os.Getpid() {
		t.Fatalf("ShipLockHolderPID() = %d, want this process' %d", got, os.Getpid())
	}
}

// Every way the lock file can fail to name a pid reads as 0 — "nothing to
// offer" — because this only decorates a message that is already an error.
func TestShipLockHolderPIDDegradesToZero(t *testing.T) {
	cases := []struct {
		name     string
		contents string
		write    bool
	}{
		{"no lock file", "", false},
		{"empty file", "", true},
		{"garbage", "who knows", true},
		{"unparseable pid", "pid=nonsense at=123", true},
		{"zero pid", "pid=0 at=123", true},
		{"negative pid", "pid=-1 at=123", true},
		{"truncated write", "pid=", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			if tc.write {
				if err := os.MkdirAll(filepath.Join(home, ".kraken"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(shipLockPath(), []byte(tc.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if got := ShipLockHolderPID(); got != 0 {
				t.Fatalf("ShipLockHolderPID() = %d, want 0", got)
			}
		})
	}
}

func TestAcquireShipLockCreateFailureReturnsError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// ~/.kraken as a plain FILE makes MkdirAll fail — the "couldn't even
	// create the lock" outcome, kept distinct from "held elsewhere".
	if err := os.WriteFile(filepath.Join(home, ".kraken"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, ok, err := AcquireShipLock()
	if ok || err == nil {
		t.Fatalf("acquire with ~/.kraken as a plain file: ok=%v err=%v, want ok=false err!=nil", ok, err)
	}
}

// --- paths ---

func TestShipPathsAreDistinctFromUpdates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	paths := map[string]string{
		"status": ShipStatusPath(),
		"log":    ShipLogPath(),
		"lock":   shipLockPath(),
	}
	seen := map[string]string{}
	for name, p := range paths {
		if other, dup := seen[p]; dup {
			t.Fatalf("%s and %s share the path %s", name, other, p)
		}
		seen[p] = name
		if !strings.HasSuffix(filepath.Dir(p), ".kraken") {
			t.Errorf("%s path %s is not under ~/.kraken", name, p)
		}
	}
	for _, p := range []string{UpdateStatusPath(), UpdateLogPath(), updateLockPath()} {
		if _, clash := seen[p]; clash {
			t.Fatalf("ship path collides with update's %s", p)
		}
	}
}

// --- the ship log ---

// The log is opened by the PARENT before the child starts, so a truncate lets
// two spawns landing close together each blow away a log the other child is
// already writing. It costs more here than for an update — a ship's failure
// output can't be reproduced without re-running ten minutes of archiving.
func TestOpenShipLogAppendsInsteadOfTruncating(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := OpenShipLog("kraken ship-ios")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteString("archive output from the first run\n"); err != nil {
		t.Fatal(err)
	}
	second, err := OpenShipLog("kraken ship-ios --detached")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.WriteString("archive output from the second run\n"); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second.Close()

	data, err := os.ReadFile(ShipLogPath())
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, want := range []string{
		"archive output from the first run",
		"archive output from the second run",
		"=== kraken ship-ios —",            // the banner names the entry point
		"=== kraken ship-ios --detached —", // …and tells the two apart
	} {
		if !strings.Contains(body, want) {
			t.Errorf("log %q missing %q", body, want)
		}
	}
	if n := strings.Count(body, "=== kraken "); n != 2 {
		t.Errorf("found %d run banners, want 2 — each run must be findable in the log", n)
	}
}

// --- preflight ---

func TestPreflightShipIOSHappyPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setShipCreds(t)
	repo := fakeKrakenRepo(t)
	script := fakeShipScript(t, repo)
	stubShipGit(t, "main")
	stubLookPath(t, func(name string) (string, error) {
		if name == "xcodegen" {
			return "/opt/homebrew/bin/xcodegen", nil
		}
		return "", errors.New("not found")
	})

	pf, err := PreflightShipIOS()
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if pf.Repo != repo || pf.Branch != "main" || pf.Script != script || pf.XcodegenPath != "/opt/homebrew/bin/xcodegen" {
		t.Fatalf("preflight resolved %+v, want repo %s on main, script %s", pf, repo, script)
	}
}

func TestPreflightShipIOSRefusals(t *testing.T) {
	cases := []struct {
		name    string
		setup   func(t *testing.T, repo string)
		creds   bool
		want    []string // substrings the message must carry
		notWant []string
	}{
		{
			name:  "no credentials at all",
			creds: false,
			setup: func(t *testing.T, repo string) { fakeShipScript(t, repo) },
			want:  []string{"ASC_KEY_ID", "ASC_ISSUER_ID", "ios/TESTFLIGHT.md", "launchctl"},
		},
		{
			name:  "no script on this checkout",
			creds: true,
			setup: func(t *testing.T, repo string) {},
			want:  []string{ShipScriptRel, "not found"},
		},
		{
			name:  "script is not executable",
			creds: true,
			setup: func(t *testing.T, repo string) {
				path := filepath.Join(repo, ShipScriptRel)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("#!/bin/bash\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: []string{"not executable", "chmod"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			// Explicitly cleared, not merely unset in the parent: the machine
			// running the suite may legitimately export both.
			t.Setenv("ASC_KEY_ID", "")
			t.Setenv("ASC_ISSUER_ID", "")
			if tc.creds {
				setShipCreds(t)
			}
			repo := fakeKrakenRepo(t)
			tc.setup(t, repo)
			stubShipGit(t, "main")
			stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })

			_, err := PreflightShipIOS()
			if err == nil {
				t.Fatalf("preflight succeeded, want a refusal")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q missing %q", err, want)
				}
			}
		})
	}
}

// The branch check, and the reason it is not decoration: the build number is
// `git rev-list --count HEAD`, so shipping off main uploads a count main may
// already own — and App Store Connect burns a (version, build) pair forever,
// so the collision cannot be undone. A detached HEAD is in the table because
// it is the shape that reads as "no branch at all" and used to sail straight
// through.
func TestPreflightShipIOSRefusesOffMain(t *testing.T) {
	for _, branch := range []string{"carl/update-button", "HEAD", "main-backup", ""} {
		t.Run("branch "+branch, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			setShipCreds(t)
			repo := fakeKrakenRepo(t)
			fakeShipScript(t, repo)
			stubShipGit(t, branch)
			stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })

			_, err := PreflightShipIOS()
			if err == nil {
				t.Fatalf("preflight on %q succeeded — a ship off main can burn a build number permanently", branch)
			}
			// Same sentence PreflightUpdate produces, so the two refusals read
			// alike wherever they surface.
			want := repo + " is on " + branch + ", not main"
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("refusal = %q, want it to contain %q", err, want)
			}
		})
	}
}

// git failing is not "not on main": it has to propagate, or a broken checkout
// would be reported as a branch problem nobody can find.
func TestPreflightShipIOSPropagatesGitFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setShipCreds(t)
	fakeShipScript(t, fakeKrakenRepo(t))
	stubGit(t, gitStub{"rev-parse --abbrev-ref HEAD": "!not a git repository"}.run)
	stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })

	_, err := PreflightShipIOS()
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("err = %v, want git's own failure", err)
	}
}

// --- ship vs. update: one checkout, two long runs ---

// A self-update fast-forwards the very checkout a ship archives from, so each
// preflight refuses while the other is in flight. Both directions, because a
// guard in only one of them leaves the same torn .ipa reachable by starting
// the two in the other order.
func TestPreflightsRefuseEachOtherWhileInFlight(t *testing.T) {
	t.Run("ship refuses during an update", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		setShipCreds(t)
		fakeShipScript(t, fakeKrakenRepo(t))
		stubShipGit(t, "main")
		stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })
		// A live updater: our own pid, so staleUpdate leaves it alone.
		if err := WriteUpdateStatus(UpdateStatus{Phase: UpdateMerging, StartedAt: time.Now().UnixMilli(), PID: os.Getpid()}); err != nil {
			t.Fatal(err)
		}

		_, err := PreflightShipIOS()
		if err == nil {
			t.Fatal("ship preflight succeeded mid-update — a merge under xcodebuild produces a torn .ipa")
		}
		for _, want := range []string{"self-update", UpdateMerging} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q missing %q", err, want)
			}
		}
	})

	t.Run("update refuses during a ship", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		fakeKrakenRepo(t)
		stubGit(t, gitStub{
			"rev-parse --abbrev-ref HEAD": "main",
			"status --porcelain":          "",
			"rev-parse --short HEAD":      "abc1234",
		}.run)
		stubLookPath(t, func(name string) (string, error) { return "/stub/" + name, nil })
		if err := WriteShipStatus(ShipStatus{Phase: ShipArchiving, StartedAt: time.Now().UnixMilli(), PID: os.Getpid()}); err != nil {
			t.Fatal(err)
		}

		_, err := PreflightUpdate()
		if err == nil {
			t.Fatal("update preflight succeeded mid-ship — the merge would rewrite the tree being archived")
		}
		for _, want := range []string{"TestFlight ship", ShipArchiving} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q missing %q", err, want)
			}
		}
	})
}

// The cross-check keys off ShipTerminal/UpdateTerminal, not merely "a record
// exists": a finished run must not block the other button forever.
func TestPreflightsAllowEachOtherWhenTerminal(t *testing.T) {
	t.Run("ship after a finished update", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		setShipCreds(t)
		fakeShipScript(t, fakeKrakenRepo(t))
		stubShipGit(t, "main")
		stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })
		if err := WriteUpdateStatus(UpdateStatus{Phase: UpdateDone, StartedAt: time.Now().UnixMilli(), PID: os.Getpid()}); err != nil {
			t.Fatal(err)
		}

		if _, err := PreflightShipIOS(); err != nil {
			t.Fatalf("ship preflight after a done update: %v, want success", err)
		}
	})

	t.Run("update after a finished ship", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		fakeKrakenRepo(t)
		stubGit(t, gitStub{
			"rev-parse --abbrev-ref HEAD": "main",
			"status --porcelain":          "",
			"rev-parse --short HEAD":      "abc1234",
		}.run)
		stubLookPath(t, func(name string) (string, error) { return "/stub/" + name, nil })
		if err := WriteShipStatus(ShipStatus{Phase: ShipFailed, StartedAt: time.Now().UnixMilli(), PID: os.Getpid()}); err != nil {
			t.Fatal(err)
		}

		if _, err := PreflightUpdate(); err != nil {
			t.Fatalf("update preflight after a failed ship: %v, want success", err)
		}
	})
}

// Only half the pair set is the likeliest real mistake (a typo'd export), and
// the message has to say WHICH half.
func TestPreflightShipIOSNamesTheMissingHalf(t *testing.T) {
	for _, missing := range ascEnvKeys {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			setShipCreds(t)
			t.Setenv(missing, "")
			fakeShipScript(t, fakeKrakenRepo(t))
			stubShipGit(t, "main")
			stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })

			_, err := PreflightShipIOS()
			if err == nil {
				t.Fatalf("preflight succeeded with %s unset", missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Fatalf("refusal %q does not name the missing %s", err, missing)
			}
			for _, other := range ascEnvKeys {
				if other != missing && strings.Contains(err.Error(), other+" not set") {
					t.Fatalf("refusal %q blames %s, which IS set", err, other)
				}
			}
		})
	}
}

// A whitespace-only value is a typo'd export, not a credential. Catching it
// here saves fifteen minutes of archiving before altool rejects it.
func TestPreflightShipIOSTreatsBlankCredsAsMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ASC_KEY_ID", "   ")
	t.Setenv("ASC_ISSUER_ID", testIssuerID)
	fakeShipScript(t, fakeKrakenRepo(t))
	stubShipGit(t, "main")
	stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })

	if _, err := PreflightShipIOS(); err == nil || !strings.Contains(err.Error(), "ASC_KEY_ID") {
		t.Fatalf("blank ASC_KEY_ID: err = %v, want a refusal naming it", err)
	}
}

// xcodegen resolution is part of preflight for the launchd-PATH reason, so a
// machine without it must refuse here rather than deep inside the script.
func TestPreflightShipIOSRefusesWithoutXcodegen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setShipCreds(t)
	fakeShipScript(t, fakeKrakenRepo(t))
	stubShipGit(t, "main")
	stubLookPath(t, func(string) (string, error) { return "", errors.New("not in PATH") })
	stubToolDirs(t, filepath.Join(t.TempDir(), "nowhere"))

	_, err := PreflightShipIOS()
	if err == nil || !strings.Contains(err.Error(), "xcodegen") {
		t.Fatalf("preflight without xcodegen: err = %v, want a refusal naming xcodegen", err)
	}
}

// --- credentials never leak ---

// The one rule that is not about correctness but about not writing a secret to
// a world-readable file the board serves to the phone: neither credential may
// appear in the status file, in any preflight refusal, or in the resolved
// preflight info.
func TestShipCredentialsNeverLeak(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	setShipCreds(t)
	repo := fakeKrakenRepo(t)
	fakeShipScript(t, repo)
	stubShipGit(t, "main")
	stubLookPath(t, func(string) (string, error) { return "/opt/homebrew/bin/xcodegen", nil })

	pf, err := PreflightShipIOS()
	if err != nil {
		t.Fatal(err)
	}
	haystacks := map[string]string{
		"preflight repo":   pf.Repo,
		"preflight script": pf.Script,
		"preflight tool":   pf.XcodegenPath,
	}

	// A full, populated status record — every field a real run would set.
	if err := WriteShipStatus(ShipStatus{
		Phase: ShipUploading, StartedAt: time.Now().UnixMilli(), PID: os.Getpid(),
		Build: 412, Error: "upload rejected",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(ShipStatusPath())
	if err != nil {
		t.Fatal(err)
	}
	haystacks["ship-ios.json"] = string(data)

	// And a refusal, which is the message most likely to be tempted into
	// echoing what it found.
	t.Setenv("ASC_ISSUER_ID", "")
	if _, refusal := PreflightShipIOS(); refusal != nil {
		haystacks["preflight refusal"] = refusal.Error()
	} else {
		t.Fatal("expected a refusal with ASC_ISSUER_ID cleared")
	}

	for where, hay := range haystacks {
		for _, secret := range []string{testKeyID, testIssuerID} {
			if strings.Contains(hay, secret) {
				t.Errorf("%s leaks a credential value: %q", where, hay)
			}
		}
	}
}
