package jobs

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kraken/internal/core"
)

// usage_test.go — the merge pass's pure core: a fresh fetch overwrites on
// success, carries forward last-known windows (marked stale) on failure, and
// never resurrects a slug that fresh no longer reports.

func window(pct float64) *core.UsageWindow { return &core.UsageWindow{Percent: pct} }

func TestMergeUsageSuccessOverwrites(t *testing.T) {
	prev := core.UsageSnapshot{Accounts: []core.AccountUsage{
		{Slug: "main", Session: window(10), Weekly: window(20), FetchedAt: 100, Stale: true},
	}}
	fresh := []core.AccountUsage{
		{Slug: "main", Session: window(50), Weekly: window(60), FetchedAt: 200},
	}
	got := mergeUsage(prev, fresh, 999)

	want := core.UsageSnapshot{FetchedAt: 999, Accounts: []core.AccountUsage{
		{Slug: "main", Session: window(50), Weekly: window(60), FetchedAt: 200, Stale: false, Current: true},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeUsage success = %+v, want %+v", got, want)
	}
}

func TestMergeUsageSuccessPreservesOwnStale(t *testing.T) {
	// core marks the main-account statusline-cache fallback Stale even though
	// the row carries windows — the merge must not clear it
	fresh := []core.AccountUsage{
		{Slug: "main", Session: window(50), Weekly: window(60), FetchedAt: 200, Stale: true},
	}
	got := mergeUsage(core.UsageSnapshot{}, fresh, 999)
	if !got.Accounts[0].Stale {
		t.Fatalf("mergeUsage cleared a success row's own Stale marking: %+v", got.Accounts[0])
	}
}

func TestMergeUsageFailureCarriesForwardPrev(t *testing.T) {
	prev := core.UsageSnapshot{Accounts: []core.AccountUsage{
		{Slug: "slot-a", Email: "old@x.com", DisplayName: "Old", Tier: "pro",
			Session: window(10), Weekly: window(20), FetchedAt: 100},
	}}
	fresh := []core.AccountUsage{
		{Slug: "slot-a", Error: "usage fetch: status 529"},
	}
	got := mergeUsage(prev, fresh, 999)

	want := core.UsageSnapshot{FetchedAt: 999, Accounts: []core.AccountUsage{
		{Slug: "slot-a", Email: "old@x.com", DisplayName: "Old", Tier: "pro",
			Session: window(10), Weekly: window(20), FetchedAt: 100,
			Stale: true, Error: "usage fetch: status 529", Current: false},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeUsage failure carry-forward = %+v, want %+v", got, want)
	}
}

func TestMergeUsageFailurePrefersFreshIdentity(t *testing.T) {
	prev := core.UsageSnapshot{Accounts: []core.AccountUsage{
		{Slug: "slot-a", Email: "old@x.com", DisplayName: "Old", Tier: "pro", Session: window(10)},
	}}
	fresh := []core.AccountUsage{
		{Slug: "slot-a", Email: "new@x.com", Reauth: true, Error: "reauth required"},
	}
	got := mergeUsage(prev, fresh, 999)

	if got.Accounts[0].Email != "new@x.com" {
		t.Errorf("Email = %q, want fresh identity to win", got.Accounts[0].Email)
	}
	if got.Accounts[0].DisplayName != "Old" {
		t.Errorf("DisplayName = %q, want prev's carried forward (fresh had none)", got.Accounts[0].DisplayName)
	}
	if !got.Accounts[0].Stale || !got.Accounts[0].Reauth || got.Accounts[0].Error != "reauth required" {
		t.Errorf("failure flags not preserved: %+v", got.Accounts[0])
	}
	if got.Accounts[0].Session == nil || got.Accounts[0].Session.Percent != 10 {
		t.Errorf("Session not carried forward: %+v", got.Accounts[0].Session)
	}
}

func TestMergeUsageFailureNoPrevKeepsAsIs(t *testing.T) {
	prev := core.UsageSnapshot{}
	fresh := []core.AccountUsage{
		{Slug: "slot-b", NoCreds: true},
	}
	got := mergeUsage(prev, fresh, 999)

	want := core.UsageSnapshot{FetchedAt: 999, Accounts: []core.AccountUsage{
		{Slug: "slot-b", NoCreds: true, Current: false},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeUsage failure no-prev = %+v, want %+v", got, want)
	}
}

func TestMergeUsageDropsSlugAbsentFromFresh(t *testing.T) {
	prev := core.UsageSnapshot{Accounts: []core.AccountUsage{
		{Slug: "main", Session: window(10)},
		{Slug: "slot-gone", Session: window(20)},
	}}
	fresh := []core.AccountUsage{
		{Slug: "main", Session: window(30)},
	}
	got := mergeUsage(prev, fresh, 999)

	if len(got.Accounts) != 1 || got.Accounts[0].Slug != "main" {
		t.Fatalf("mergeUsage should drop slugs absent from fresh: %+v", got.Accounts)
	}
}

func TestMergeUsageStampsCurrentOnMainOnly(t *testing.T) {
	fresh := []core.AccountUsage{
		{Slug: "main", Session: window(1)},
		{Slug: "slot-a", Session: window(2)},
	}
	got := mergeUsage(core.UsageSnapshot{}, fresh, 999)

	if !got.Accounts[0].Current {
		t.Error("main should be stamped Current")
	}
	if got.Accounts[1].Current {
		t.Error("non-main slug should not be stamped Current")
	}
}

func TestReadUsageSnapshotFromMissingOrGarbage(t *testing.T) {
	dir := t.TempDir()
	if _, ok := readUsageSnapshotFrom(filepath.Join(dir, "missing.json")); ok {
		t.Error("missing file should read as ok=false")
	}

	garbage := filepath.Join(dir, "garbage.json")
	if err := os.WriteFile(garbage, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readUsageSnapshotFrom(garbage); ok {
		t.Error("garbage file should read as ok=false")
	}
}

func TestReadUsageSnapshotFromValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")
	want := core.UsageSnapshot{FetchedAt: 123, Accounts: []core.AccountUsage{{Slug: "main"}}}
	if err := writeUsageSnapshot(want, path); err != nil {
		t.Fatal(err)
	}
	got, ok := readUsageSnapshotFrom(path)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("readUsageSnapshotFrom = %+v, %v, want %+v, true", got, ok, want)
	}
}

func TestWriteUsageSnapshotAtomicAndPerms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "usage.json")
	snap := core.UsageSnapshot{FetchedAt: 1, Accounts: []core.AccountUsage{{Slug: "main"}}}

	if err := writeUsageSnapshot(snap, path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perms = %o, want 0600", perm)
	}
	if entries, _ := os.ReadDir(dir + "/nested"); len(entries) != 1 {
		t.Errorf("expected only the final file, no leftover temp file: %v", entries)
	}

	got, ok := readUsageSnapshotFrom(path)
	if !ok || !reflect.DeepEqual(got, snap) {
		t.Fatalf("round-trip = %+v, %v, want %+v, true", got, ok, snap)
	}
}
