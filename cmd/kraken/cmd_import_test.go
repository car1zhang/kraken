package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// cmd_import_test.go — wire-level coverage only: flag parsing, and the
// startup guard refusing to run while a daemon/serve is alive. jobs.ImportAll
// itself (every source it sweeps) is already covered by jobs/import_test.go.

func TestParseImportArgs(t *testing.T) {
	cases := []struct {
		name            string
		args            []string
		wantDry         bool
		wantForce       bool
		wantReimport    bool
		wantLiveSources bool
		wantErr         bool
	}{
		{"no args", nil, false, false, false, false, false},
		{"dry-run only", []string{"--dry-run"}, true, false, false, false, false},
		{"force only", []string{"--force"}, false, true, false, false, false},
		{"reimport only", []string{"--reimport"}, false, false, true, false, false},
		{"live-sources only", []string{"--live-sources"}, false, false, false, true, false},
		{"all four", []string{"--dry-run", "--force", "--reimport", "--live-sources"}, true, true, true, true, false},
		{"unknown flag", []string{"--bogus"}, false, false, false, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dry, force, reimport, liveSources, err := parseImportArgs(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error for %v, got nil", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %v: %v", tc.args, err)
			}
			if dry != tc.wantDry || force != tc.wantForce || reimport != tc.wantReimport || liveSources != tc.wantLiveSources {
				t.Fatalf("parseImportArgs(%v) = (%v, %v, %v, %v), want (%v, %v, %v, %v)",
					tc.args, dry, force, reimport, liveSources, tc.wantDry, tc.wantForce, tc.wantReimport, tc.wantLiveSources)
			}
		})
	}
}

// writeFakeDaemonStatus writes ~/.kraken/daemon.json directly (mirroring
// jobs.writeDaemonStatusFile, unexported and therefore unreachable from
// package main) so runImport's guard has something to read.
func writeFakeDaemonStatus(t *testing.T, home string, st jobs.DaemonStatus) {
	t.Helper()
	dir := filepath.Join(home, ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRunImportRefusesWhileServeAlive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// The parent pid (the `go test` runner) is guaranteed alive for the
	// duration of this test and, sharing our UID, is one this process may
	// legally signal — unlike pid 1 (launchd/init), which exists but isn't
	// ours to signal and would make syscall.Kill's EPERM read back as "not
	// alive". It's also never this test process' own pid, so it reads as a
	// genuinely different, live conflicting process.
	writeFakeDaemonStatus(t, home, jobs.DaemonStatus{PID: os.Getppid()})
	// E14: daemonGuardErr also cross-checks the pid is actually named
	// "kraken" (closing a pid-recycling false positive) — the `go test`
	// runner obviously isn't, so stub the check to simulate a genuine
	// kraken conflict without spawning a real kraken binary.
	oldCheck := jobs.ProcessIsKraken
	jobs.ProcessIsKraken = func(pid int) bool { return true }
	defer func() { jobs.ProcessIsKraken = oldCheck }()

	report, err := runImport(true, false, false, false)
	if err == nil {
		t.Fatal("want an error while a daemon/serve is running, got nil")
	}
	if !strings.Contains(err.Error(), "running") {
		t.Fatalf("error = %q, want it to mention the running daemon/serve", err.Error())
	}
	if !reflect.DeepEqual(report, jobs.ImportReport{}) {
		t.Fatalf("want a zero-value report on guard refusal, got %+v", report)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".kraken", "kraken.db")); statErr == nil {
		t.Fatal("guard refusal must never even open the store")
	}
}

func TestRunImportSucceedsWhenNoDaemonRunning(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_DB_PATH", filepath.Join(home, "kraken.db"))

	// no daemon.json at all: the common case (fresh machine / after a clean
	// shutdown) must not be mistaken for a conflict.
	if _, err := runImport(true, false, false, false); err != nil {
		t.Fatalf("dry-run with no live daemon: want nil, got %v", err)
	}
}

// TestRunImportDryRunLeavesRealDBPathUntouched covers E3: store.Open alone
// (independent of whether ImportAll ever writes a row) creates ~/.kraken and
// touches kraken.db via os.MkdirAll + the schema migration — so a dry run
// that opened store.DBPath() directly left kraken.db sitting on disk
// afterward even though nothing was ever inserted into it. A dry run must
// route through a throwaway temp-dir database instead, so DBPath() stays
// exactly as absent as it was before the dry run ran.
func TestRunImportDryRunLeavesRealDBPathUntouched(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, ".kraken", "kraken.db")

	if _, statErr := os.Stat(dbPath); statErr == nil {
		t.Fatal("kraken.db unexpectedly pre-exists")
	}
	report, err := runImport(true, false, false, false)
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if _, statErr := os.Stat(dbPath); statErr == nil {
		t.Fatal("dry-run created the real kraken.db — must open a throwaway temp DB instead")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".kraken")); statErr == nil {
		t.Fatal("dry-run created ~/.kraken at all — must not touch anything under the real HOME")
	}
	_ = report
}

// TestRunImportRefusesSecondImportWithoutReimport covers E4: once a
// non-dry-run import has completed (the imported_at kv marker is set),
// running import AGAIN must refuse even with --force — kraken serve may
// have accepted real writes on top of that DB since, and Force alone only
// covers "this fresh DB has stray rows", not "a full import already ran
// here." --reimport is the stronger, explicit override.
func TestRunImportRefusesSecondImportWithoutReimport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, ".kraken", "kraken.db")
	t.Setenv("KRAKEN_DB_PATH", dbPath)
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())

	// first import: no jobs table rows yet, so it succeeds without Force.
	if _, err := runImport(false, false, false, false); err != nil {
		t.Fatalf("first import: %v", err)
	}

	// a second import, even with Force, must be refused — the imported_at
	// marker is now set.
	report, err := runImport(false, true, false, false)
	if err == nil {
		t.Fatal("want a refusal for a second import without --reimport, got nil")
	}
	if !strings.Contains(err.Error(), "Reimport") {
		t.Fatalf("error = %q, want it to mention Reimport", err.Error())
	}
	if !reflect.DeepEqual(report, jobs.ImportReport{}) {
		t.Fatalf("want a zero-value report on the reimport refusal, got %+v", report)
	}

	// --reimport is the explicit override that lets it proceed.
	if _, err := runImport(false, true, true, false); err != nil {
		t.Fatalf("second import with --reimport: %v", err)
	}
}

func TestRunImportOwnPidIsNeverAConflict(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_DB_PATH", filepath.Join(home, "kraken.db"))

	writeFakeDaemonStatus(t, home, jobs.DaemonStatus{PID: os.Getpid()})

	if _, err := runImport(true, false, false, false); err != nil {
		t.Fatalf("status naming our own pid: want nil, got %v", err)
	}
}

// --- --live-sources (local mirror testing) ---

// TestRunImportLiveSourcesRefusedAtDefaultDBPath covers --live-sources'
// precondition: it may only skip the daemon-alive guard when KRAKEN_DB_PATH
// resolves somewhere other than the real deployment's default
// ~/.kraken/kraken.db. With no override at all (the default path), it must
// refuse — clearly, and before touching any source or opening any store —
// rather than silently import against the SAME database a live
// daemon/serve might be writing.
func TestRunImportLiveSourcesRefusedAtDefaultDBPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// no KRAKEN_DB_PATH override: store.DBPath() resolves to the real
	// default under this (fake, but unambiguous) HOME.

	report, err := runImport(true, false, false, true)
	if err == nil {
		t.Fatal("want an error for --live-sources at the default db path, got nil")
	}
	if !strings.Contains(err.Error(), "sandbox KRAKEN_DB_PATH") {
		t.Fatalf("error = %q, want it to mention the sandbox KRAKEN_DB_PATH requirement", err.Error())
	}
	if !reflect.DeepEqual(report, jobs.ImportReport{}) {
		t.Fatalf("want a zero-value report on the refusal, got %+v", report)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".kraken", "kraken.db")); statErr == nil {
		t.Fatal("--live-sources refusal at the default path must never even open the store")
	}
}

// TestRunImportLiveSourcesRefusedEvenWithExplicitOverrideMatchingDefault
// covers the exact-match edge of isSandboxDBPath: an explicit KRAKEN_DB_PATH
// that just happens to name the same path defaultDBPath() would compute is
// still "no real override" and must still refuse.
func TestRunImportLiveSourcesRefusedEvenWithExplicitOverrideMatchingDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_DB_PATH", filepath.Join(home, ".kraken", "kraken.db"))

	if _, err := runImport(true, false, false, true); err == nil {
		t.Fatal("want an error when KRAKEN_DB_PATH matches the real default, got nil")
	}
}

// TestRunImportLiveSourcesAllowedWithSandboxDBEvenWhileDaemonAlive covers
// --live-sources' whole point: once KRAKEN_DB_PATH genuinely points
// somewhere else, the daemon-alive guard is skipped entirely — a live
// daemon.json naming an alive, actually-kraken pid (the exact condition
// TestRunImportRefusesWhileServeAlive proves refuses a plain `kraken
// import`) must NOT block a sandboxed --live-sources import.
func TestRunImportLiveSourcesAllowedWithSandboxDBEvenWhileDaemonAlive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_DB_PATH", filepath.Join(home, "sandbox", "kraken.db"))
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())

	writeFakeDaemonStatus(t, home, jobs.DaemonStatus{PID: os.Getppid()})
	oldCheck := jobs.ProcessIsKraken
	jobs.ProcessIsKraken = func(pid int) bool { return true }
	defer func() { jobs.ProcessIsKraken = oldCheck }()

	if _, err := runImport(true, false, false, true); err != nil {
		t.Fatalf("--live-sources with a sandbox db path: want nil despite a live daemon.json, got %v", err)
	}
}
