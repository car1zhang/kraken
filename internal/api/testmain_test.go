package api

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
	// The operator's ~/.kraken/env knobs reach a `go test` run inside a cell
	// through the daemon's environment (internal/jobs' TestMain has the
	// mechanism). Here the cost was the pr-attach fixtures: with
	// KRAKEN_REPOS_DIR pointing at the operator's home, `repo: monolith`
	// resolved to a REAL checkout and the same-repo check read its origin,
	// refusing the fixture PR with a 409. A test that wants a knob sets it
	// with t.Setenv.
	for _, knob := range []string{"KRAKEN_REPOS_DIR", "KRAKEN_BRANCH_PREFIX", "KRAKEN_OPERATOR"} {
		os.Unsetenv(knob)
	}
	code := m.Run()
	os.RemoveAll(dir) // deferred cleanup never runs past os.Exit
	os.Exit(code)
}
