package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// standingbrief_edge_test.go — athena-edge's BLIND tests for slice A of
// dream-2026-09-26's 50-contract.md (the daemon respawns a standing agent
// whose staged brief has drifted). Written from the contract text alone:
// standingbrief.go and the daemon.go diff were never opened. Dimension:
// EDGE CASES and boundaries — the nil/absent/unreadable inputs of
// standingBriefDrift, and the boundary of every gate of standingBriefPass.
//
// Every package-level identifier here is prefixed `edgeSB` so it cannot
// collide with the shared jobs test namespace.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

const (
	edgeSBAgentID   = "edge-sb-agent"
	edgeSBSession   = "edge-sb-session"
	edgeSBAgentName = "edge-sb-name"
	edgeSBRecorded  = "edge-sb-recorded-session-id"
)

type edgeSBRig struct {
	st         *store.Store
	respawns   int
	respawnErr error
}

// edgeSBRigNew isolates HOME (useTestStore) and swaps standingRespawn for a
// recorder, restoring it on cleanup. Nothing here can reach tmux.
func edgeSBRigNew(t *testing.T) *edgeSBRig {
	t.Helper()
	rig := &edgeSBRig{st: useTestStore(t)}
	t.Setenv("PATH", t.TempDir())
	orig := standingRespawn
	t.Cleanup(func() { standingRespawn = orig })
	standingRespawn = func(a standingAgent, _ *store.Store, _ map[string]bool) error {
		rig.respawns++
		return rig.respawnErr
	}
	return rig
}

// edgeSBStage writes content to a fresh staged-brief path under t.TempDir()
// and returns the path.
func edgeSBStage(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("stage brief: %v", err)
	}
	return p
}

// edgeSBAgent builds the fixture agent over a staged path and a current brief.
func edgeSBAgent(briefPath, brief string) standingAgent {
	return standingAgent{
		ID:        edgeSBAgentID,
		Session:   edgeSBSession,
		AgentName: edgeSBAgentName,
		RecordedSession: func(*store.Store) (string, bool) {
			return edgeSBRecorded, true
		},
		BriefPath: func() string { return briefPath },
		Brief:     func() string { return brief },
	}
}

// edgeSBDriftedAgent is an agent whose staged brief differs from its current.
func edgeSBDriftedAgent(t *testing.T) standingAgent {
	t.Helper()
	return edgeSBAgent(edgeSBStage(t, "old brief\n"), "new brief\n")
}

func edgeSBLive() map[string]bool { return map[string]bool{edgeSBSession: true} }

// edgeSBIdlePeers is a snapshot holding the agent, by name, idle.
func edgeSBIdlePeers() []*core.Peer {
	p := contractStandingPeer(edgeSBSession, "%1", edgeSBAgentName, edgeSBRecorded)
	p.Status = "idle"
	return []*core.Peer{p}
}

// edgeSBNow is millisecond-aligned so a stamp round-trips exactly.
func edgeSBNow() time.Time { return time.UnixMilli(time.Now().UnixMilli()) }

func edgeSBStamp(t *testing.T, st *store.Store) (string, bool) {
	t.Helper()
	v, err := st.GetKV(standingBriefKey(edgeSBAgentID))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", false
		}
		t.Fatalf("GetKV: %v", err)
	}
	return v, true
}

func edgeSBSetStamp(t *testing.T, st *store.Store, v string) {
	t.Helper()
	if err := st.SetKV(standingBriefKey(edgeSBAgentID), v); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
}

func edgeSBSetHit(t *testing.T, st *store.Store, h LimitHit) {
	t.Helper()
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := st.SetKV(standingHitKey(edgeSBAgentID), string(b)); err != nil {
		t.Fatalf("SetKV hit: %v", err)
	}
}

// edgeSBAssertRespawned asserts one respawn and a stamp equal to now.
func edgeSBAssertRespawned(t *testing.T, rig *edgeSBRig, now time.Time, pin string) {
	t.Helper()
	if rig.respawns != 1 {
		t.Fatalf("respawns = %d, want 1 — contract: %s", rig.respawns, pin)
	}
	got, ok := edgeSBStamp(t, rig.st)
	want := strconv.FormatInt(now.UnixMilli(), 10)
	if !ok || got != want {
		t.Fatalf("stamp = %q (present %v), want %q — contract: \"the kv row at standingBriefKey(a.ID) is set to now.UnixMilli() as a decimal string\"", got, ok, want)
	}
}

// edgeSBAssertGated asserts no respawn and a stamp equal to before (absent
// when before is "").
func edgeSBAssertGated(t *testing.T, rig *edgeSBRig, before, pin string) {
	t.Helper()
	if rig.respawns != 0 {
		t.Fatalf("respawns = %d, want 0 — contract: %s", rig.respawns, pin)
	}
	got, ok := edgeSBStamp(t, rig.st)
	if before == "" {
		if ok {
			t.Fatalf("stamp written (%q), want none — contract: %s (\"each returning with NO write and NO respawn\")", got, pin)
		}
		return
	}
	if !ok || got != before {
		t.Fatalf("stamp = %q (present %v), want unchanged %q — contract: %s (\"NO write\")", got, ok, before, pin)
	}
}

// ---------------------------------------------------------------------------
// standingBriefDrift
// ---------------------------------------------------------------------------

func TestEdgeStandingBriefDriftNilBriefPath(t *testing.T) {
	a := edgeSBAgent("", "brief")
	a.BriefPath = nil
	drifted, reason := standingBriefDrift(a)
	if drifted || reason == "" {
		t.Fatalf("got (%v, %q) — contract: \"a.BriefPath == nil or a.Brief == nil → (false, non-empty reason)\"", drifted, reason)
	}
}

func TestEdgeStandingBriefDriftNilBrief(t *testing.T) {
	a := edgeSBAgent(edgeSBStage(t, "brief"), "")
	a.Brief = nil
	drifted, reason := standingBriefDrift(a)
	if drifted || reason == "" {
		t.Fatalf("got (%v, %q) — contract: \"a.BriefPath == nil or a.Brief == nil → (false, non-empty reason)\"", drifted, reason)
	}
}

func TestEdgeStandingBriefDriftAbsentFile(t *testing.T) {
	a := edgeSBAgent(filepath.Join(t.TempDir(), "never-staged.md"), "brief")
	drifted, reason := standingBriefDrift(a)
	if drifted || reason == "" {
		t.Fatalf("got (%v, %q) — contract: \"Staged file absent (os.IsNotExist) → (false, non-empty reason)\"", drifted, reason)
	}
}

func TestEdgeStandingBriefDriftDirectoryInPlaceOfFile(t *testing.T) {
	dir := t.TempDir() // a directory: reading it errors, and not IsNotExist
	a := edgeSBAgent(dir, "brief")
	drifted, reason := standingBriefDrift(a)
	if drifted || reason == "" {
		t.Fatalf("got (%v, %q) — contract: \"Any other read error → (false, non-empty reason): an unreadable file is never drift\"", drifted, reason)
	}
}

func TestEdgeStandingBriefDriftEmptyStagedFile(t *testing.T) {
	a := edgeSBAgent(edgeSBStage(t, ""), "a non-empty brief")
	drifted, reason := standingBriefDrift(a)
	if !drifted || reason == "" {
		t.Fatalf("got (%v, %q) — contract: bytes differ \"Otherwise → (true, non-empty reason)\"", drifted, reason)
	}
}

func TestEdgeStandingBriefDriftByteEqual(t *testing.T) {
	const brief = "# brief\n\nsame bytes\n"
	a := edgeSBAgent(edgeSBStage(t, brief), brief)
	drifted, reason := standingBriefDrift(a)
	if drifted || reason != "" {
		t.Fatalf("got (%v, %q) — contract: \"File bytes equal to []byte(a.Brief()) → (false, \\\"\\\")\"", drifted, reason)
	}
}

func TestEdgeStandingBriefDriftTrailingNewline(t *testing.T) {
	a := edgeSBAgent(edgeSBStage(t, "brief\n"), "brief")
	drifted, reason := standingBriefDrift(a)
	if !drifted || reason == "" {
		t.Fatalf("got (%v, %q) — contract: \"a one-byte difference is drift, a trailing newline included\"", drifted, reason)
	}
}

func TestEdgeStandingBriefDriftMultiByteDifference(t *testing.T) {
	// "é" (U+00E9, 2 bytes) vs "è" (U+00E8): same rune count, same length,
	// one differing byte.
	a := edgeSBAgent(edgeSBStage(t, "brief — café\n"), "brief — cafè\n")
	drifted, reason := standingBriefDrift(a)
	if !drifted || reason == "" {
		t.Fatalf("got (%v, %q) — contract: \"Comparison is byte-exact: a one-byte difference is drift\"", drifted, reason)
	}
}

// ---------------------------------------------------------------------------
// standingBriefPass — backoff boundary (gate 4)
// ---------------------------------------------------------------------------

func TestEdgeStandingBriefBackoffExactlyElapsedProceeds(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	edgeSBSetStamp(t, rig.st, strconv.FormatInt(now.Add(-standingBriefBackoff).UnixMilli(), 10))
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), edgeSBLive(), now)
	edgeSBAssertRespawned(t, rig, now, "gate 4 is \"now.Sub(that) < standingBriefBackoff\"; exactly standingBriefBackoff old is not within it")
}

func TestEdgeStandingBriefBackoffOneMilliYoungerGated(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	before := strconv.FormatInt(now.Add(-standingBriefBackoff+time.Millisecond).UnixMilli(), 10)
	edgeSBSetStamp(t, rig.st, before)
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), edgeSBLive(), now)
	edgeSBAssertGated(t, rig, before, "gate (4) \"now.Sub(that) < standingBriefBackoff\"")
}

func TestEdgeStandingBriefUnparseableStampReadsAsNoAttempt(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	edgeSBSetStamp(t, rig.st, "not-a-number")
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), edgeSBLive(), now)
	edgeSBAssertRespawned(t, rig, now, "\"an absent or unparseable row reads as no prior attempt\"")
}

func TestEdgeStandingBriefEmptyStampReadsAsNoAttempt(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	edgeSBSetStamp(t, rig.st, "")
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), edgeSBLive(), now)
	edgeSBAssertRespawned(t, rig, now, "\"an absent or unparseable row reads as no prior attempt\"")
}

// ---------------------------------------------------------------------------
// standingBriefPass — limit hit (gate 3)
// ---------------------------------------------------------------------------

func TestEdgeStandingBriefResolvedLimitHitDoesNotGate(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	edgeSBSetHit(t, rig.st, LimitHit{Error: "rate_limit", At: now.Add(-time.Hour).UnixMilli(), HandledAt: now.Add(-30 * time.Minute).UnixMilli()})
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), edgeSBLive(), now)
	edgeSBAssertRespawned(t, rig, now, "gate (3) is an OPEN hit only — \"a hit is OPEN when At != 0 && HandledAt == 0\"")
}

func TestEdgeStandingBriefOpenLimitHitGates(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	edgeSBSetHit(t, rig.st, LimitHit{Error: "rate_limit", At: now.Add(-time.Minute).UnixMilli()})
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), edgeSBLive(), now)
	edgeSBAssertGated(t, rig, "", "gate (3) \"an OPEN limit hit under standingHitKey(a.ID) (the rescue owns the pane)\"")
}

// ---------------------------------------------------------------------------
// standingBriefPass — live map (gate 2)
// ---------------------------------------------------------------------------

func TestEdgeStandingBriefLiveMapMissingSession(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), map[string]bool{"some-other-session": true}, now)
	edgeSBAssertGated(t, rig, "", "gate (2) \"!live[a.Session]\"")
}

func TestEdgeStandingBriefLiveMapHoldsFalse(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), map[string]bool{edgeSBSession: false}, now)
	edgeSBAssertGated(t, rig, "", "gate (2) \"!live[a.Session]\"")
}

// ---------------------------------------------------------------------------
// standingBriefPass — kill target (gate 6) and status (gate 7)
// ---------------------------------------------------------------------------

func TestEdgeStandingBriefPeerMatchedBySessionIDOnly(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	p := contractStandingPeer(edgeSBSession, "%1", "", edgeSBRecorded)
	p.Status = "idle"
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, []*core.Peer{p}, edgeSBLive(), now)
	edgeSBAssertRespawned(t, rig, now, "gate (6) uses standingKillTarget, which accepts \"SessionID == recorded\" where recorded is a.RecordedSession(st)'s id")
}

func TestEdgeStandingBriefPeerAtDepthOneDoesNotCount(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	p := contractStandingPeer(edgeSBSession, "%1", edgeSBAgentName, edgeSBRecorded)
	p.Depth = 1
	p.Status = "idle"
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, []*core.Peer{p}, edgeSBLive(), now)
	edgeSBAssertGated(t, rig, "", "gate (6) \"standingKillTarget(a, all, recorded) returns a nil peer ... (a deferral: no stamp is written)\"")
}

func TestEdgeStandingBriefRunningPeerDefers(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	peers := edgeSBIdlePeers()
	peers[0].Status = "running"
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, peers, edgeSBLive(), now)
	edgeSBAssertGated(t, rig, "", "gate (7) \"that peer's Status == \\\"running\\\" (mid-turn: same deferral, no stamp)\"")
}

func TestEdgeStandingBriefWaitingPeerProceeds(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	peers := edgeSBIdlePeers()
	peers[0].Status = "waiting"
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, peers, edgeSBLive(), now)
	edgeSBAssertRespawned(t, rig, now, "gate (7) defers only on Status == \"running\"")
}

func TestEdgeStandingBriefIdlePeerProceeds(t *testing.T) {
	rig := edgeSBRigNew(t)
	now := edgeSBNow()
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), edgeSBLive(), now)
	edgeSBAssertRespawned(t, rig, now, "gate (7) defers only on Status == \"running\"")
}

// ---------------------------------------------------------------------------
// standingBriefPass — respawn error
// ---------------------------------------------------------------------------

func TestEdgeStandingBriefRespawnErrorKeepsStamp(t *testing.T) {
	rig := edgeSBRigNew(t)
	rig.respawnErr = errors.New("edge: respawn failed")
	now := edgeSBNow()
	(&daemonState{}).standingBriefPass(edgeSBDriftedAgent(t), rig.st, edgeSBIdlePeers(), edgeSBLive(), now)
	edgeSBAssertRespawned(t, rig, now, "\"standingRespawn(a, st, live) is called exactly once. Its error is logged, never returned, and the stamp stays either way\"")
}
