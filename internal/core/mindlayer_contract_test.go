package core

import (
	"os"
	"testing"
)

// mindlayer_contract_test.go — athena-logic's BLIND contract test for
// artifacts/50-contract.md (PR 2), slice B's `core.CallerSessionID`. Written
// from the frozen contract alone; internal/core/callersession.go was never
// read. Every package-level identifier here is prefixed `contractML`.

// "Under `HOME` isolated with no session files it returns `false` (the test
// obligation)."
func TestMindLayerCallerSessionIDFalseUnderIsolatedHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"KRAKEN_DB_PATH", "KRAKEN_THREAD_SESSION", "KRAKEN_THREAD_ROLE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if sid, ok := CallerSessionID(); ok {
		t.Errorf("CallerSessionID() = %q, true under an isolated HOME; want false", sid)
	}
}
