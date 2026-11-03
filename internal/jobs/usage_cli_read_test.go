package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// usage_cli_read_test.go — a process with no store wired (every CLI command,
// `kraken account` among them) must see the snapshot the daemon keeps in
// kraken.db, not only the legacy ~/.kraken/usage.json. Reading the file alone
// printed numbers six days old (2026-11-03) while the daemon's were current.

// cliReadFixture writes a daemon-side snapshot into a fresh kraken.db and a
// file-side snapshot into $HOME/.kraken/usage.json (0 skips either one), then
// unwires the store so the reader behaves the way a CLI process does.
func cliReadFixture(t *testing.T, dbAt, fileAt int64) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, ".kraken", "kraken.db")
	t.Setenv("KRAKEN_DB_PATH", dbPath)

	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if dbAt != 0 {
		raw, _ := json.Marshal(core.UsageSnapshot{FetchedAt: dbAt, Accounts: []core.AccountUsage{{Slug: "from-db"}}})
		if err := st.SetKV(usageSnapshotKVKey, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	UseStore(nil)
	t.Cleanup(func() { UseStore(nil) })

	if fileAt != 0 {
		snap := core.UsageSnapshot{FetchedAt: fileAt, Accounts: []core.AccountUsage{{Slug: "from-file"}}}
		if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
			t.Fatal(err)
		}
	}
}

func readSlug(t *testing.T) string {
	t.Helper()
	snap, ok := ReadUsageSnapshot()
	if !ok || len(snap.Accounts) == 0 {
		t.Fatalf("ReadUsageSnapshot = %+v, %v, want a snapshot", snap, ok)
	}
	return snap.Accounts[0].Slug
}

func TestCLIReadPrefersDaemonSnapshotOverOlderFile(t *testing.T) {
	cliReadFixture(t, 2000, 1000)
	if got := readSlug(t); got != "from-db" {
		t.Fatalf("read %q, want the daemon's newer kraken.db snapshot", got)
	}
}

func TestCLIReadKeepsNewerFile(t *testing.T) {
	cliReadFixture(t, 1000, 2000)
	if got := readSlug(t); got != "from-file" {
		t.Fatalf("read %q, want the newer usage.json snapshot", got)
	}
}

func TestCLIReadFallsBackToFileWhenDBHasNoRow(t *testing.T) {
	cliReadFixture(t, 0, 1000)
	if got := readSlug(t); got != "from-file" {
		t.Fatalf("read %q, want usage.json when kraken.db has no snapshot row", got)
	}
}

func TestCLIReadFallsBackToFileWhenNoDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "absent", "kraken.db")
	t.Setenv("KRAKEN_DB_PATH", dbPath)
	UseStore(nil)
	snap := core.UsageSnapshot{FetchedAt: 1000, Accounts: []core.AccountUsage{{Slug: "from-file"}}}
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}
	if got := readSlug(t); got != "from-file" {
		t.Fatalf("read %q, want usage.json when there is no kraken.db", got)
	}
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatal("the read created kraken.db; a CLI read must never create or migrate the database")
	}
}
