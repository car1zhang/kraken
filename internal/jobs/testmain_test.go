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
	// Same reasoning for the dev-env mirror's git probe: the real gitUntracked
	// shells out to git in a worktree, and no test fixture dir is one. A test
	// that asserts on the probe re-swaps it locally (swapGitUntracked).
	gitUntracked = func(string, []string) ([]string, error) { return nil, nil }
	code := m.Run()
	os.RemoveAll(dir) // deferred cleanup never runs past os.Exit
	os.Exit(code)
}
