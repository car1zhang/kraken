package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// codexusage.go — the codex half of the daemon's account-usage poll.
//
// There are two account pools on this machine and they share nothing: the
// claude pool (~/.claude-pool, credentials in the macOS keychain, usage read
// from Anthropic's API) and the codex pool (~/.codex-pool, credentials in a
// slot's auth.json, usage read out of the slot's own rollout files). A job's
// binding names a slot in the pool its AGENT uses, so a slug is only
// meaningful next to the agent it was picked for — "alpha" in the claude pool
// and "alpha" in the codex pool are different accounts belonging to different
// people.
//
// That is the whole reason this is a second snapshot under a second key
// rather than more rows in the claude one. A merged snapshot would make the
// two pools' slugs collide in one namespace, and every reader — the binder's
// candidate list, the board's usage panel, the rescue's exclusion by email —
// would have to re-separate them correctly or silently mix an account from
// one pool into a decision about the other.
//
// The claude pass (usage.go) is untouched: nothing here reads, writes or
// merges usage_snapshot.

// CodexUsageSnapshotPath is the file the codex snapshot lands in when no
// store is wired — the sibling of UsageSnapshotPath, named so the two can
// never be mistaken for each other on disk. Pure cache, same as the claude
// one: deleting it costs one poll interval. Never contains a token — every
// row comes from core.FetchCodexUsage, which reads only the email and the
// plan out of a slot's credentials.
func CodexUsageSnapshotPath() string {
	return filepath.Join(util.Home(), ".kraken", "usage-codex.json")
}

// codexUsageSnapshotKVKey is the kv key the store-backed path uses. Distinct
// from usageSnapshotKVKey by construction, for the namespace reason in this
// file's header.
const codexUsageSnapshotKVKey = "usage_snapshot_codex"

// CodexUsagePass rebuilds the codex snapshot from scratch and persists it.
//
// No merge step, deliberately — the claude pass carries one (mergeUsage) so a
// single failed HTTP fetch does not blank a row that was fine a minute ago.
// A codex row has no network call behind it: it is a read of files kraken
// already has on disk, so a row that comes back empty means the slot really
// is empty (no auth.json, or no rollout has recorded a limit yet), and
// carrying a previous poll's numbers forward would claim usage for an account
// that no longer has credentials.
func CodexUsagePass() error {
	start := time.Now()
	snap := core.UsageSnapshot{
		FetchedAt: time.Now().UnixMilli(),
		Accounts:  core.FetchCodexUsage(),
	}
	if err := writeCodexUsageSnapshot(snap, CodexUsageSnapshotPath()); err != nil {
		return err
	}
	noCreds := 0
	for _, a := range snap.Accounts {
		if a.NoCreds {
			noCreds++
		}
	}
	dlog("codex usage pass: %d accounts, %d without creds, %s", len(snap.Accounts), noCreds, time.Since(start).Round(time.Millisecond))
	return nil
}

// writeCodexUsageSnapshot is writeUsageSnapshot's codex twin: the kv row when
// a store is wired, else an atomic 0600 file (AtomicWrite itself writes 0644,
// so the mode is tightened after the rename).
func writeCodexUsageSnapshot(snap core.UsageSnapshot, path string) error {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if st := ActiveStore(); st != nil {
		return st.SetKV(codexUsageSnapshotKVKey, string(data))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := util.AtomicWrite(path, string(data)); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// ReadCodexUsageSnapshot loads the codex snapshot, defensively: a missing key,
// a missing file or JSON that no longer parses all read as "no snapshot yet"
// rather than as an error. Callers (the codex binder, the codex rescue) treat
// that as "no candidates", which leaves a cell unbound — today's behaviour for
// every codex job, and never a failed spawn.
func ReadCodexUsageSnapshot() (core.UsageSnapshot, bool) {
	if st := ActiveStore(); st != nil {
		raw, err := st.GetKV(codexUsageSnapshotKVKey)
		if err != nil {
			return core.UsageSnapshot{}, false
		}
		var snap core.UsageSnapshot
		if json.Unmarshal([]byte(raw), &snap) != nil {
			return core.UsageSnapshot{}, false
		}
		return snap, true
	}
	return readUsageSnapshotFrom(CodexUsageSnapshotPath())
}

// codexUsagePassRunning single-flights the codex pass across ticks, its own
// atomic rather than usagePassRunning's: the two passes are independent, and
// sharing one flag would let a slow claude fetch suppress the codex poll (and
// the other way round) for no reason either of them can see.
var codexUsagePassRunning atomic.Bool

// runCodexUsagePass is the seam KickCodexUsagePass calls through, the sibling
// of runUsagePass (rotation.go's seam block). TestMain no-ops it for the whole
// package's test binary: the real pass reads the machine's ~/.codex-pool and
// writes ~/.kraken/usage-codex.json, and no test may touch either.
var runCodexUsagePass = CodexUsagePass

// KickCodexUsagePass best-effort triggers a codex usage pass in its own
// goroutine, the shape KickUsagePass uses. Fire-and-forget is what keeps the
// two passes independent: neither waits on the other, so one failing or
// hanging can never stop the other from running on this tick.
func KickCodexUsagePass() {
	// The seam is captured before the goroutine starts, for KickUsagePass'
	// reason: a test that swaps it back the moment this returns must not race
	// the goroutine's own read of it.
	if pass := runCodexUsagePass; codexUsagePassRunning.CompareAndSwap(false, true) {
		go func() {
			defer codexUsagePassRunning.Store(false)
			if err := pass(); err != nil {
				dlog("codex usage pass failed: %v", err)
			}
		}()
	}
}
