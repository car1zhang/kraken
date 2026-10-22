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
	// The operator's own ~/.kraken/env knobs reach every cell through the
	// daemon's environment (main applies the file to its process env, and a
	// cell pane inherits it), so a `go test` run inside a cell sees them
	// too. The package's fixtures build their repos under a temp dir and
	// never expect a knob to be set: with KRAKEN_REPOS_DIR pointing at the
	// operator's home every SubmitJob fixture refused with "no repo under
	// <home>", and the submit-serial tests, which wait inside the start seam
	// that refusal never reaches, hung the whole package until the test
	// binary's deadline. Unset all three so no operator knob the fixtures
	// depend on leaks in; a test that wants one sets it with t.Setenv.
	// internal/api's TestMain does the same for the same reason.
	for _, knob := range []string{"KRAKEN_REPOS_DIR", "KRAKEN_BRANCH_PREFIX", "KRAKEN_OPERATOR"} {
		os.Unsetenv(knob)
	}
	// Real bindAccount reads the usage snapshot and shells out to the
	// keychain — no-op it for the whole package's test binary so any test
	// that calls StartCell or ReactivateJob (now or in the future) never
	// touches either. A test that specifically wants to assert on binding
	// re-swaps this locally.
	bindGate = func(*Job, string) {}
	// Same reasoning for the codex usage pass, which the daemon tick kicks:
	// the real pass walks the machine's ~/.codex-pool and writes
	// ~/.kraken/usage-codex.json, and no test may touch either. A test that
	// asserts on the kick re-swaps this locally.
	runCodexUsagePass = func() error { return nil }
	// Same reasoning for the dev-env mirror's git probe: the real gitUntracked
	// shells out to git in a worktree, and no test fixture dir is one. A test
	// that asserts on the probe re-swaps it locally (swapGitUntracked).
	gitUntracked = func(string, []string) ([]string, error) { return nil, nil }
	code := m.Run()
	os.RemoveAll(dir) // deferred cleanup never runs past os.Exit
	os.Exit(code)
}
