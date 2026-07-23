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
	// Real RotationGate shells out to the keychain/network — no-op it for
	// the whole package's test binary so any test that calls StartCell or
	// ReactivateJob (now or in the future) never touches either. A test
	// that specifically wants to assert on gate invocation re-swaps this
	// locally.
	rotationGate = func(string) {}
	code := m.Run()
	os.RemoveAll(dir) // deferred cleanup never runs past os.Exit
	os.Exit(code)
}
