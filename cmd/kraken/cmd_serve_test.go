package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/web"
)

// TestServeSetupOccupiedPortRefusesCleanly covers E5: net.Listen must be
// the very first thing kraken serve attempts, before the store is opened,
// the single-instance guard is checked, or the scheduler tick / nightly
// backup goroutines start. An occupied port must fail right there —
// leaving no daemon.json (RunServeTicks' first tick writes it, and
// RunServeTicks must never have started), no store wired onto the jobs
// package, and a non-nil, clearly-worded error.
func TestServeSetupOccupiedPortRefusesCleanly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_DB_PATH", filepath.Join(home, "kraken.db"))
	t.Cleanup(func() { jobs.UseStore(nil) })

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	_, portStr, err := net.SplitHostPort(occupied.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	ln, st, _, err := serveSetup(web.Options{Port: portStr, DisableSignalHandler: true}, false)
	if err == nil {
		if ln != nil {
			ln.Close()
		}
		if st != nil {
			st.Close()
		}
		t.Fatal("want an error binding an already-occupied port, got nil")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Fatalf("error = %q, want it to mention the listen failure", err.Error())
	}
	if ln != nil || st != nil {
		t.Fatalf("want nil listener and store on failure, got ln=%v st=%v", ln, st)
	}
	if jobs.ActiveStore() != nil {
		t.Fatal("store must not be wired onto the jobs package when the bind itself failed")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".kraken", "daemon.json")); statErr == nil {
		t.Fatal("daemon.json written despite the bind failing — the tick loop must never have started")
	}
	if _, statErr := os.Stat(filepath.Join(home, "kraken.db")); statErr == nil {
		t.Fatal("kraken.db created despite the bind failing — the store must never have been opened")
	}
}

// TestServeSetupSucceedsOnFreePort is the happy-path twin of
// TestServeSetupOccupiedPortRefusesCleanly: a free port binds, the store
// opens and wires onto the jobs package, and the guard passes cleanly.
func TestServeSetupSucceedsOnFreePort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Cleanup(func() { jobs.UseStore(nil) })

	ln, st, addr, err := serveSetup(web.Options{Port: "0", DisableSignalHandler: true}, false)
	if err != nil {
		t.Fatalf("serveSetup: %v", err)
	}
	defer ln.Close()
	defer st.Close()
	if ln == nil || st == nil {
		t.Fatalf("want a non-nil listener and store, got ln=%v st=%v", ln, st)
	}
	if jobs.ActiveStore() != st {
		t.Fatal("store not wired onto the jobs package")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".kraken", "kraken.db")); statErr != nil {
		t.Fatalf("kraken.db not created: %v", statErr)
	}
	// E7: the returned addr must be the listener's REAL bound address —
	// Port "0" means the OS picked one, so it can't just echo back "0".
	if addr != ln.Addr().String() {
		t.Fatalf("addr = %q, want the listener's actual address %q", addr, ln.Addr().String())
	}
	if strings.HasSuffix(addr, ":0") {
		t.Fatalf("addr = %q, want a real resolved port, not the :0 wildcard", addr)
	}
}

// --- --mirror (local mirror testing) ---

// TestServeSetupMirrorRefusesDefaultPort covers --mirror's port-squat guard:
// a mirror invocation that (deliberately, or by forgetting -p/-addr)
// resolves to the real deployment's default port must refuse before
// net.Listen, store.Open, or the guard ever run — it must never even
// attempt to bind where the real `kraken serve` listens.
func TestServeSetupMirrorRefusesDefaultPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Cleanup(func() { jobs.UseStore(nil) })

	ln, st, _, err := serveSetup(web.Options{Port: defaultServePort, DisableSignalHandler: true}, true)
	if err == nil {
		if ln != nil {
			ln.Close()
		}
		if st != nil {
			st.Close()
		}
		t.Fatal("want an error for --mirror on the default port, got nil")
	}
	if !strings.Contains(err.Error(), defaultServePort) {
		t.Fatalf("error = %q, want it to mention the squatted port %s", err.Error(), defaultServePort)
	}
	if ln != nil || st != nil {
		t.Fatalf("want nil listener and store on refusal, got ln=%v st=%v", ln, st)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".kraken", "kraken.db")); statErr == nil {
		t.Fatal("mirror's port-squat refusal must never open the store")
	}
}

// TestServeSetupMirrorAllowsDifferentPortEvenWithLiveDaemon covers --mirror's
// other half: once the port genuinely differs from the default, mirror
// skips GuardServeStart entirely — a live daemon.json naming an alive,
// actually-kraken pid (the exact condition that refuses a normal `kraken
// serve`, per TestServeSetupSucceedsOnFreePort's non-mirror sibling
// scenario) must not block a mirror process from coming up alongside it.
func TestServeSetupMirrorAllowsDifferentPortEvenWithLiveDaemon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_DB_PATH", filepath.Join(home, "sandbox", "kraken.db"))
	t.Cleanup(func() { jobs.UseStore(nil) })

	dir := filepath.Join(home, ".kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(jobs.DaemonStatus{PID: os.Getppid()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "daemon.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	oldCheck := jobs.ProcessIsKraken
	jobs.ProcessIsKraken = func(pid int) bool { return true }
	defer func() { jobs.ProcessIsKraken = oldCheck }()

	ln, st, addr, err := serveSetup(web.Options{Port: "0", DisableSignalHandler: true}, true)
	if err != nil {
		t.Fatalf("mirror serveSetup on a free (non-default) port despite a live daemon: %v", err)
	}
	defer ln.Close()
	defer st.Close()
	if addr == "" {
		t.Fatal("want a real bound address")
	}
	// the sandbox db (KRAKEN_DB_PATH above) opened normally — serveSetup
	// wasn't fooled by the pre-existing daemon.json into refusing.
	if _, statErr := os.Stat(filepath.Join(home, "sandbox", "kraken.db")); statErr != nil {
		t.Fatalf("sandbox kraken.db not created: %v", statErr)
	}
}

func TestBackupNowWritesAndRotates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateJob(&store.Job{Slug: "seed"}); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	base := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	for i := range backupsKeep + 3 {
		now := base.Add(time.Duration(i) * 24 * time.Hour)
		if err := backupNow(st, dir, now); err != nil {
			t.Fatalf("backup %d: %v", i, err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != backupsKeep {
		t.Fatalf("want %d backups kept, got %d: %v", backupsKeep, len(entries), entries)
	}

	// the newest backupsKeep must be the ones that survive — not an
	// arbitrary keep-first-N by directory order.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	wantOldestSurvivor := "kraken-" + base.Add(3*24*time.Hour).Format("20060102-150405") + ".db"
	found := false
	for _, n := range names {
		if n == wantOldestSurvivor {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected oldest surviving backup %s among %v", wantOldestSurvivor, names)
	}

	// every surviving backup must itself be a valid, openable database
	// (VACUUM INTO's output, not a truncated/partial file) carrying the
	// seeded row.
	for _, n := range names {
		copySt, err := store.Open(filepath.Join(dir, n))
		if err != nil {
			t.Fatalf("open backup %s: %v", n, err)
		}
		j, err := copySt.GetJobBySlug("seed")
		copySt.Close()
		if err != nil || j.Slug != "seed" {
			t.Fatalf("backup %s missing seeded job: %v", n, err)
		}
	}
}

func TestRotateBackupsMissingDirIsNotAnError(t *testing.T) {
	if err := rotateBackups(filepath.Join(t.TempDir(), "does-not-exist"), 7); err != nil {
		t.Fatalf("want nil for a missing dir, got %v", err)
	}
}

func TestBackupsDirEnvOverride(t *testing.T) {
	want := t.TempDir()
	t.Setenv("KRAKEN_BACKUPS_DIR", want)
	if got := backupsDir(); got != want {
		t.Fatalf("backupsDir() = %q, want %q", got, want)
	}
}

// TestLatestBackupTimeSeedsFromNewestFile covers seeding
// runNightlyBackups' `last` from disk on restart means a backup taken
// shortly before a restart reads as "not due yet" rather than blank/zero.
func TestLatestBackupTimeSeedsFromNewestFile(t *testing.T) {
	dir := t.TempDir()
	older := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	newer := older.Add(24 * time.Hour)
	for _, ts := range []time.Time{older, newer} {
		p := filepath.Join(dir, "kraken-"+ts.Format("20060102-150405")+".db")
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	got := latestBackupTime(dir)
	if !got.Equal(newer) {
		t.Fatalf("latestBackupTime = %v, want %v", got, newer)
	}
}

func TestLatestBackupTimeMissingDirIsZero(t *testing.T) {
	got := latestBackupTime(filepath.Join(t.TempDir(), "does-not-exist"))
	if !got.IsZero() {
		t.Fatalf("want zero time for a missing dir, got %v", got)
	}
}

// TestRunNightlyBackupsRestartNoImmediateBackup covers a backup that
// already happened recently (as latestBackupTime would report
// on a restart) must read as not-yet-due, the same "now.Sub(last) >= 24h"
// gate runNightlyBackups' loop itself uses.
func TestRunNightlyBackupsRestartNoImmediateBackup(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	recent := now.Add(-time.Hour)
	p := filepath.Join(dir, "kraken-"+recent.Format("20060102-150405")+".db")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, recent, recent); err != nil {
		t.Fatal(err)
	}

	last := latestBackupTime(dir)
	if now.Sub(last) >= 24*time.Hour {
		t.Fatalf("want the recent on-disk backup to read as not-yet-due, got age %v", now.Sub(last))
	}
}

// TestBackupNowSameSecondCollisionSkipsInsteadOfErroring covers
// VACUUM INTO refusing to overwrite an existing destination, but a
// same-wall-clock-second collision must skip quietly rather than surface as
// a hard error.
func TestBackupNowSameSecondCollisionSkipsInsteadOfErroring(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	if err := backupNow(st, dir, now); err != nil {
		t.Fatalf("first backup: %v", err)
	}
	if err := backupNow(st, dir, now); err != nil {
		t.Fatalf("same-second collision: want nil (skip), got %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 backup file after the collision, got %v", entries)
	}
}

// TestBackupNowLeavesNoTmpFileAndRotateIgnoresIt covers the
// VACUUM INTO destination is written to a ".tmp" name and renamed into
// place only on success, and rotateBackups must never trip over a leftover
// ".tmp" (it simply fails the ".db" suffix check).
func TestBackupNowLeavesNoTmpFileAndRotateIgnoresIt(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	if err := backupNow(st, dir, now); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover .tmp file after a successful backup: %s", e.Name())
		}
	}
	// a stray .tmp dropped in by hand (simulating an interrupted attempt at
	// a DIFFERENT timestamp) must not be counted by rotateBackups' listing.
	stray := filepath.Join(dir, "kraken-99999999-999999.db.tmp")
	if err := os.WriteFile(stray, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rotateBackups(dir, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatal("rotateBackups(keep=0) removed a .tmp file it should have ignored entirely")
	}
}
