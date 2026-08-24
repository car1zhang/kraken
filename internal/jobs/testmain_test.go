package jobs

import (
	"os"
	"testing"
)

// TestMain quarantines the unified event log for the whole package: daemon
// helpers (autoArchivePass, dlog*) log as a side effect, and without this a
// test fixture's entries would land in the real ~/.kraken/logs.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kraken-test-logs")
	if err != nil {
		panic(err)
	}
	os.Setenv("KRAKEN_LOGS_DIR", dir)
	// Real bindAccount reads the usage snapshot and shells out to the
	// keychain — no-op it for the whole package's test binary so any test
	// that calls StartCell or ReactivateJob (now or in the future) never
	// touches either. A test that specifically wants to assert on binding
	// re-swaps this locally.
	bindGate = func(*Job, string) {}
	// Same for the usage pass. The daemon's first-tick-eager KickUsagePass
	// and the standing-agent rescue's post-swap kick (rescueSwap) each START
	// A GOROUTINE running runUsagePass, and a test that reaches either
	// without stubNoUsagePass lets that goroutine outlive the test:
	// t.Setenv("HOME") and the test store are torn down at cleanup, so a
	// pass finishing late — after a real network fetch of at least the main
	// account's usage — either resolves ActiveStore() to nil and util.Home()
	// to the REAL home and writes the real ~/.kraken/usage.json from inside
	// `go test ./...`, or, when a LATER test has already opened its own
	// store, writes a usage_snapshot kv row into that test's database
	// instead (writeUsageSnapshot prefers the store), poisoning a run rather
	// than a home dir. Hermetic by default for this test binary;
	// stubNoUsagePass and the rescue/rotation tests still re-swap their own
	// stubs locally (redundant now, kept) and restore this one.
	runUsagePass = func() error { return nil }
	// Same reasoning for the dev-env mirror's git probe: the real gitUntracked
	// shells out to git in a worktree, and no test fixture dir is one. A test
	// that asserts on the probe re-swaps it locally (swapGitUntracked).
	gitUntracked = func(string, []string) ([]string, error) { return nil, nil }
	code := m.Run()
	os.RemoveAll(dir) // deferred cleanup never runs past os.Exit
	os.Exit(code)
}
