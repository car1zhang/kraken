package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"kraken/core"
	"kraken/util"
)

// UsageSnapshotPath is the daemon-owned usage snapshot: one row per Claude
// account (main + every ~/.claude-pool slot), written by the daemon's usage
// pass and served read-only by /api/usage. Pure cache — safe to delete, the
// next pass rebuilds it from scratch. Never contains tokens.
func UsageSnapshotPath() string {
	return filepath.Join(util.Home(), ".kraken", "usage.json")
}

// usageSnapshotKVKey is the kv-table key the store-backed path stashes the
// snapshot under (jobs/store.go's UpdateJobFields-adjacent kv usage,
// mirroring the daemon's "serve_started_at" key) — a singleton row, same
// shape as the file it replaces.
const usageSnapshotKVKey = "usage_snapshot"

// ReadUsageSnapshot loads the snapshot, defensively: a missing or corrupt
// value reads as "no snapshot yet", never an error. Store-backed (kv key
// usageSnapshotKVKey) when a store is wired, else the file.
func ReadUsageSnapshot() (core.UsageSnapshot, bool) {
	if st := ActiveStore(); st != nil {
		raw, err := st.GetKV(usageSnapshotKVKey)
		if err != nil {
			return core.UsageSnapshot{}, false
		}
		var snap core.UsageSnapshot
		if json.Unmarshal([]byte(raw), &snap) != nil {
			return core.UsageSnapshot{}, false
		}
		return snap, true
	}
	return readUsageSnapshotFrom(UsageSnapshotPath())
}

func readUsageSnapshotFrom(path string) (core.UsageSnapshot, bool) {
	var snap core.UsageSnapshot
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &snap) != nil {
		return core.UsageSnapshot{}, false
	}
	return snap, true
}

// mergeUsage folds a fresh fetch onto the previous snapshot. A fresh row that
// failed (nil Session and Weekly — no creds, reauth, or a fetch error) keeps
// showing the account's last-known windows, marked Stale, rather than
// blanking the row out for one bad tick; a fresh row that succeeded always
// wins outright. Rows only in prev (a pool slot that's gone) are dropped —
// fresh's shape is authoritative for which accounts exist. Current is
// stamped here, the single place it's set, off Slug alone.
func mergeUsage(prev core.UsageSnapshot, fresh []core.AccountUsage, now int64) core.UsageSnapshot {
	prevBySlug := make(map[string]core.AccountUsage, len(prev.Accounts))
	for _, a := range prev.Accounts {
		prevBySlug[a.Slug] = a
	}

	accounts := make([]core.AccountUsage, len(fresh))
	for i, row := range fresh {
		if row.Session == nil && row.Weekly == nil {
			if p, ok := prevBySlug[row.Slug]; ok && (p.Session != nil || p.Weekly != nil) {
				row.Session, row.Weekly, row.Models, row.FetchedAt = p.Session, p.Weekly, p.Models, p.FetchedAt
				row.Stale = true
				if row.Email == "" {
					row.Email = p.Email
				}
				if row.DisplayName == "" {
					row.DisplayName = p.DisplayName
				}
				if row.Tier == "" {
					row.Tier = p.Tier
				}
			}
		}
		// a success row's own Stale marking is preserved: core marks the
		// main-account statusline-cache fallback stale even though it
		// carries windows, and that must survive the merge
		row.Current = row.Slug == "main"
		accounts[i] = row
	}
	return core.UsageSnapshot{FetchedAt: now, Accounts: accounts}
}

// writeUsageSnapshot marshals snap and persists it: store-backed (kv key
// usageSnapshotKVKey) when a store is wired — path is unused in that case,
// callers still pass UsageSnapshotPath() unconditionally — else atomically
// to path at 0600 (AtomicWrite itself writes 0644, so perms are tightened
// after the rename).
func writeUsageSnapshot(snap core.UsageSnapshot, path string) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if st := ActiveStore(); st != nil {
		return st.SetKV(usageSnapshotKVKey, string(data))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := util.AtomicWrite(path, string(data)); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// UsagePass fetches usage for every account and writes the merged snapshot.
// Per-account failures are data (carried as Stale rows), not an error here —
// only a write failure is.
func UsagePass() error {
	start := time.Now()
	fresh := core.FetchAllUsage()
	prev, _ := ReadUsageSnapshot()
	snap := mergeUsage(prev, fresh, time.Now().UnixMilli())
	if err := writeUsageSnapshot(snap, UsageSnapshotPath()); err != nil {
		return err
	}
	errCount := 0
	for _, a := range snap.Accounts {
		if a.Error != "" || a.Reauth || a.NoCreds {
			errCount++
		}
	}
	dlog("usage pass: %d accounts, %d errors, %s", len(snap.Accounts), errCount, time.Since(start).Round(time.Millisecond))
	return nil
}
