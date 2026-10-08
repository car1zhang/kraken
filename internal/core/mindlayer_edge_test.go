package core

import "testing"

// mindlayer_edge_test.go — the EDGE dimension of 50-contract.md slice B's
// `core.CallerSessionID`. Written blind: callersession.go was not read. The
// contract's test obligation is the false case only: "Under `HOME` isolated
// with no session files it returns `false`" — true even when the test binary
// itself runs under a claude process, since that ancestor's session file would
// live under the REAL ~/.claude/sessions, not the isolated HOME.

func TestMindLayerEdgeCallerSessionIDFalseUnderIsolatedHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	t.Setenv("KRAKEN_THREAD_SESSION", "")
	t.Setenv("KRAKEN_THREAD_ROLE", "")
	for i := 0; i < 2; i++ { // and stays false on a repeat call
		sid, ok := CallerSessionID()
		if ok {
			t.Fatalf("call %d: CallerSessionID() = %q, true; want false with no session files", i, sid)
		}
		if sid != "" {
			t.Errorf("call %d: CallerSessionID() returned session %q alongside false, want \"\"", i, sid)
		}
	}
}

// KRAKEN_THREAD_SESSION is the CLI's first choice, not CallerSessionID's:
// setting it must not make the ancestor walk report a session.
func TestMindLayerEdgeCallerSessionIDIgnoresThreadEnv(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	t.Setenv("KRAKEN_THREAD_SESSION", "sess-from-env")
	t.Setenv("KRAKEN_THREAD_ROLE", "mind")
	if sid, ok := CallerSessionID(); ok || sid == "sess-from-env" {
		t.Fatalf("CallerSessionID() = %q, %v; want \"\", false (it walks processes, not env)", sid, ok)
	}
}
