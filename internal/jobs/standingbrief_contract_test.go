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

// standingbrief_contract_test.go — athena-logic's BLIND suite for slice A of
// the dream-2026-09-26 contract: the daemon respawns a standing agent whose
// staged brief has drifted. Written from 50-contract.md alone; every test pins
// ONE obligation and quotes the contract line it pins. Nothing here kills a
// real pane: standingRespawn is swapped for a recorder in every pass test.

const (
	briefContractID      = "kronos-fixture"
	briefContractSession = "kronos-fixture-session"
	briefContractName    = "kronos-fixture-agent"
	briefContractSID     = "11111111-2222-3333-4444-555555555555"
	briefContractCurrent = "you are kronos — current brief\n"
)

// briefContractNow is a fixed tick clock so every stamp is comparable.
var briefContractNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// briefContractCounts records how often the fixture agent's closures run, so a
// test can observe that a gate returned BEFORE a later gate was evaluated.
type briefContractCounts struct {
	brief    int
	recorded int
}

// briefContractAgent builds a fixture standingAgent whose staged brief lives
// at stagedPath and whose would-stage-now brief is `now`. Enabled, Find and
// Respawn fail the test if called: the pass never calls them.
func briefContractAgent(t *testing.T, stagedPath, now string, c *briefContractCounts) standingAgent {
	t.Helper()
	return standingAgent{
		ID:        briefContractID,
		Session:   briefContractSession,
		AgentName: briefContractName,
		Enabled: func() bool {
			t.Errorf("contract: the pass never calls a.Enabled")
			return true
		},
		RecordedSession: func(*store.Store) (string, bool) {
			c.recorded++
			return briefContractSID, true
		},
		Find: func([]*core.Peer) *core.Peer {
			t.Errorf("contract: the pass never calls a.Find")
			return nil
		},
		Respawn: func(*store.Store, map[string]bool) error {
			t.Errorf("contract: \"The pass never calls a.Respawn directly\"")
			return nil
		},
		BriefPath: func() string { return stagedPath },
		Brief: func() string {
			c.brief++
			return now
		},
	}
}

// briefContractStage writes the staged-brief fixture under t.TempDir().
func briefContractStage(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("stage brief: %v", err)
	}
	return p
}

type briefContractCall struct {
	id       string
	st       *store.Store
	live     map[string]bool
	stampSaw string
	stampErr error
}

// briefContractRecorder swaps standingRespawn for a recorder that also reads
// the stamp row at the moment it is called (to pin write-before-respawn).
func briefContractRecorder(t *testing.T, st *store.Store, ret error) *[]briefContractCall {
	t.Helper()
	calls := &[]briefContractCall{}
	prev := standingRespawn
	standingRespawn = func(a standingAgent, s *store.Store, live map[string]bool) error {
		c := briefContractCall{id: a.ID, st: s, live: live}
		if st != nil {
			c.stampSaw, c.stampErr = st.GetKV(standingBriefKey(a.ID))
		}
		*calls = append(*calls, c)
		return ret
	}
	t.Cleanup(func() { standingRespawn = prev })
	return calls
}

func briefContractLive() map[string]bool { return map[string]bool{briefContractSession: true} }

// briefContractPeers returns the agent's identifiable, idle peer.
func briefContractPeers() []*core.Peer {
	p := contractStandingPeer(briefContractSession, "%7", briefContractName, briefContractSID)
	p.Status = "idle"
	return []*core.Peer{p}
}

func briefContractStampWant() string { return strconv.FormatInt(briefContractNow.UnixMilli(), 10) }

// briefContractNoStamp asserts no stamp row exists under standingBriefKey.
func briefContractNoStamp(t *testing.T, st *store.Store, line string) {
	t.Helper()
	v, err := st.GetKV(standingBriefKey(briefContractID))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stamp row = %q (err %v), want absent — contract: %s", v, err, line)
	}
}

func briefContractNoRespawn(t *testing.T, calls *[]briefContractCall, line string) {
	t.Helper()
	if len(*calls) != 0 {
		t.Fatalf("standingRespawn called %d times, want 0 — contract: %s", len(*calls), line)
	}
}

func briefContractOpenHit(t *testing.T, st *store.Store, handledAt int64) {
	t.Helper()
	h := LimitHit{Error: "rate_limit", At: briefContractNow.Add(-time.Minute).UnixMilli(), AgentID: briefContractID, HandledAt: handledAt}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(standingHitKey(briefContractID), string(b)); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// standingBriefKey / standingBriefBackoff
// ---------------------------------------------------------------------------

func TestContractStandingBriefKeyExactString(t *testing.T) {
	for _, id := range []string{"kronos", "iris", "x"} {
		if got, want := standingBriefKey(id), "standing_brief_respawn:"+id; got != want {
			t.Fatalf("standingBriefKey(%q) = %q, want %q — contract: Returns \"standing_brief_respawn:\" + agentID", id, got, want)
		}
	}
}

func TestContractStandingBriefKeyDisjointFromHitKey(t *testing.T) {
	for _, id := range []string{"kronos", "iris", "", "42"} {
		if standingBriefKey(id) == standingHitKey(id) {
			t.Fatalf("standingBriefKey(%q) == standingHitKey(%q) = %q — contract: Never equal to standingHitKey(agentID)", id, id, standingBriefKey(id))
		}
	}
}

func TestContractStandingBriefBackoffIsSixHours(t *testing.T) {
	if standingBriefBackoff != 6*time.Hour {
		t.Fatalf("standingBriefBackoff = %v — contract: const standingBriefBackoff = 6 * time.Hour", standingBriefBackoff)
	}
}

// ---------------------------------------------------------------------------
// standingBriefDrift
// ---------------------------------------------------------------------------

func TestContractStandingBriefDriftNilBriefPath(t *testing.T) {
	a := standingAgent{ID: briefContractID, Brief: func() string { return "x" }}
	drifted, reason := standingBriefDrift(a)
	if drifted || reason == "" {
		t.Fatalf("drift = (%v, %q) — contract: a.BriefPath == nil → (false, non-empty reason)", drifted, reason)
	}
}

func TestContractStandingBriefDriftNilBrief(t *testing.T) {
	p := briefContractStage(t, "x")
	a := standingAgent{ID: briefContractID, BriefPath: func() string { return p }}
	drifted, reason := standingBriefDrift(a)
	if drifted || reason == "" {
		t.Fatalf("drift = (%v, %q) — contract: a.Brief == nil → (false, non-empty reason)", drifted, reason)
	}
}

func TestContractStandingBriefDriftAbsentFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "never-staged.md")
	a := standingAgent{ID: briefContractID, BriefPath: func() string { return p }, Brief: func() string { return briefContractCurrent }}
	drifted, reason := standingBriefDrift(a)
	if drifted || reason == "" {
		t.Fatalf("drift = (%v, %q) — contract: Staged file absent (os.IsNotExist) → (false, non-empty reason)", drifted, reason)
	}
}

func TestContractStandingBriefDriftOtherReadError(t *testing.T) {
	// A directory at the path: reading it errors, and not with IsNotExist.
	p := t.TempDir()
	a := standingAgent{ID: briefContractID, BriefPath: func() string { return p }, Brief: func() string { return briefContractCurrent }}
	drifted, reason := standingBriefDrift(a)
	if drifted || reason == "" {
		t.Fatalf("drift = (%v, %q) — contract: Any other read error → (false, non-empty reason): an unreadable file is never drift", drifted, reason)
	}
}

func TestContractStandingBriefDriftEqualBytes(t *testing.T) {
	p := briefContractStage(t, briefContractCurrent)
	a := standingAgent{ID: briefContractID, BriefPath: func() string { return p }, Brief: func() string { return briefContractCurrent }}
	drifted, reason := standingBriefDrift(a)
	if drifted || reason != "" {
		t.Fatalf("drift = (%v, %q) — contract: File bytes equal to []byte(a.Brief()) → (false, \"\")", drifted, reason)
	}
}

func TestContractStandingBriefDriftDifferentBytes(t *testing.T) {
	p := briefContractStage(t, "you are kronos — stale brief\n")
	a := standingAgent{ID: briefContractID, BriefPath: func() string { return p }, Brief: func() string { return briefContractCurrent }}
	drifted, reason := standingBriefDrift(a)
	if !drifted || reason == "" {
		t.Fatalf("drift = (%v, %q) — contract: Otherwise → (true, non-empty reason)", drifted, reason)
	}
}

func TestContractStandingBriefDriftTrailingNewlineIsDrift(t *testing.T) {
	p := briefContractStage(t, "brief")
	a := standingAgent{ID: briefContractID, BriefPath: func() string { return p }, Brief: func() string { return "brief\n" }}
	drifted, reason := standingBriefDrift(a)
	if !drifted || reason == "" {
		t.Fatalf("drift = (%v, %q) — contract: Comparison is byte-exact: a one-byte difference is drift, a trailing newline included", drifted, reason)
	}
}

func TestContractStandingBriefDriftIsPureRead(t *testing.T) {
	const staged = "stale\n"
	p := briefContractStage(t, staged)
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	a := standingAgent{ID: briefContractID, BriefPath: func() string { return p }, Brief: func() string { return briefContractCurrent }}
	standingBriefDrift(a)
	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("staged brief gone after drift check: %v — contract: Pure read, no side effects", err)
	}
	after, _ := os.Stat(p)
	if string(got) != staged || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("staged brief rewritten by drift check (now %q) — contract: Pure read, no side effects", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("drift check left %d entries beside the brief — contract: Pure read, no side effects", len(entries)-1)
	}
}

// ---------------------------------------------------------------------------
// (*daemonState).standingBriefPass — the gates, in order
// ---------------------------------------------------------------------------

func TestContractStandingBriefPassNilStoreGate(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, nil, nil)
	c := &briefContractCounts{}
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, c)
	(&daemonState{}).standingBriefPass(a, nil, briefContractPeers(), briefContractLive(), briefContractNow)
	briefContractNoRespawn(t, calls, "gate (1) st == nil returns with NO write and NO respawn")
	briefContractNoStamp(t, st, "gate (1) st == nil returns with NO write")
	if c.brief != 0 || c.recorded != 0 {
		t.Fatalf("Brief called %d, RecordedSession %d times after gate (1) — contract: gates, in this order, each returning", c.brief, c.recorded)
	}
}

func TestContractStandingBriefPassNotLiveGate(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	c := &briefContractCounts{}
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, c)
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), map[string]bool{"some-other-session": true}, briefContractNow)
	briefContractNoRespawn(t, calls, "gate (2) !live[a.Session] returns with NO respawn")
	briefContractNoStamp(t, st, "gate (2) !live[a.Session] returns with NO write")
}

func TestContractStandingBriefPassNotLiveGateBeforeDrift(t *testing.T) {
	st := useTestStore(t)
	briefContractRecorder(t, st, nil)
	c := &briefContractCounts{}
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, c)
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), map[string]bool{}, briefContractNow)
	if c.brief != 0 || c.recorded != 0 {
		t.Fatalf("Brief called %d, RecordedSession %d times after gate (2) — contract: gates, in this order, each returning", c.brief, c.recorded)
	}
}

func TestContractStandingBriefPassOpenLimitHitGate(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	briefContractOpenHit(t, st, 0)
	c := &briefContractCounts{}
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, c)
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	briefContractNoRespawn(t, calls, "gate (3) an OPEN limit hit under standingHitKey(a.ID) (the rescue owns the pane)")
	briefContractNoStamp(t, st, "gate (3) an OPEN limit hit returns with NO write")
	if c.brief != 0 || c.recorded != 0 {
		t.Fatalf("Brief called %d, RecordedSession %d times after gate (3) — contract: gates, in this order, each returning", c.brief, c.recorded)
	}
}

func TestContractStandingBriefPassResolvedLimitHitDoesNotGate(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	briefContractOpenHit(t, st, briefContractNow.Add(-30*time.Second).UnixMilli())
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times with only a RESOLVED hit, want 1 — contract: a hit is OPEN when At != 0 && HandledAt == 0; only an OPEN one gates", len(*calls))
	}
}

func TestContractStandingBriefPassBackoffGate(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	prior := strconv.FormatInt(briefContractNow.Add(-standingBriefBackoff+time.Minute).UnixMilli(), 10)
	if err := st.SetKV(standingBriefKey(briefContractID), prior); err != nil {
		t.Fatal(err)
	}
	c := &briefContractCounts{}
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, c)
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	briefContractNoRespawn(t, calls, "gate (4) now.Sub(that) < standingBriefBackoff returns with NO respawn")
	if got, _ := st.GetKV(standingBriefKey(briefContractID)); got != prior {
		t.Fatalf("stamp = %q, want untouched %q — contract: gate (4) returns with NO write", got, prior)
	}
	if c.brief != 0 || c.recorded != 0 {
		t.Fatalf("Brief called %d, RecordedSession %d times after gate (4) — contract: gates, in this order, each returning", c.brief, c.recorded)
	}
}

func TestContractStandingBriefPassBackoffElapsedProceeds(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	prior := strconv.FormatInt(briefContractNow.Add(-standingBriefBackoff-time.Minute).UnixMilli(), 10)
	if err := st.SetKV(standingBriefKey(briefContractID), prior); err != nil {
		t.Fatal(err)
	}
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times with the last attempt older than the backoff, want 1 — contract: gate (4) holds only while now.Sub(that) < standingBriefBackoff", len(*calls))
	}
	if got, _ := st.GetKV(standingBriefKey(briefContractID)); got != briefContractStampWant() {
		t.Fatalf("stamp = %q, want %q — contract: the kv row at standingBriefKey(a.ID) is set to now.UnixMilli() as a decimal string", got, briefContractStampWant())
	}
}

func TestContractStandingBriefPassUnparseableStampReadsAsNoPriorAttempt(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	if err := st.SetKV(standingBriefKey(briefContractID), "not-a-number"); err != nil {
		t.Fatal(err)
	}
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times over an unparseable stamp, want 1 — contract: an absent or unparseable row reads as no prior attempt", len(*calls))
	}
}

func TestContractStandingBriefPassNoDriftGate(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	c := &briefContractCounts{}
	a := briefContractAgent(t, briefContractStage(t, briefContractCurrent), briefContractCurrent, c)
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	briefContractNoRespawn(t, calls, "gate (5) standingBriefDrift(a) reports no drift")
	briefContractNoStamp(t, st, "gate (5) no drift returns with NO write")
	if c.recorded != 0 {
		t.Fatalf("RecordedSession called %d times after gate (5) — contract: gates, in this order, each returning", c.recorded)
	}
}

func TestContractStandingBriefPassAbsentStagedBriefIsNoDrift(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	a := briefContractAgent(t, filepath.Join(t.TempDir(), "absent.md"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	briefContractNoRespawn(t, calls, "Staged file absent → (false, …): nothing to compare, the next spawn stages one")
	briefContractNoStamp(t, st, "gate (5) no drift returns with NO write")
}

func TestContractStandingBriefPassNoKillTargetDefers(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	// A depth-0 claude in the session, but neither the agent's name nor its
	// recorded session id: standingKillTarget refuses it.
	stranger := contractStandingPeer(briefContractSession, "%7", "someone-else", "99999999-0000-0000-0000-000000000000")
	stranger.Status = "idle"
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, []*core.Peer{stranger}, briefContractLive(), briefContractNow)
	briefContractNoRespawn(t, calls, "gate (6) standingKillTarget returns a nil peer")
	briefContractNoStamp(t, st, "gate (6) a deferral: no stamp is written, so the next tick re-checks")
}

func TestContractStandingBriefPassNoPeerAtAllDefers(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, nil, briefContractLive(), briefContractNow)
	briefContractNoRespawn(t, calls, "gate (6) standingKillTarget returns a nil peer")
	briefContractNoStamp(t, st, "gate (6) a deferral: no stamp is written")
}

func TestContractStandingBriefPassKillTargetByRecordedSession(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	// Nameless (still starting) but carrying the RECORDED session id.
	p := contractStandingPeer(briefContractSession, "%7", "", briefContractSID)
	p.Status = "idle"
	c := &briefContractCounts{}
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, c)
	(&daemonState{}).standingBriefPass(a, st, []*core.Peer{p}, briefContractLive(), briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times for a peer matching the recorded session, want 1 — contract: standingKillTarget(a, all, recorded) where recorded is a.RecordedSession(st)'s id", len(*calls))
	}
	if c.recorded == 0 {
		t.Fatalf("RecordedSession never consulted — contract: recorded is a.RecordedSession(st)'s id")
	}
}

func TestContractStandingBriefPassRunningPeerDefers(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	peers := briefContractPeers()
	peers[0].Status = "running"
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, peers, briefContractLive(), briefContractNow)
	briefContractNoRespawn(t, calls, "gate (7) that peer's Status == \"running\" (mid-turn)")
	briefContractNoStamp(t, st, "gate (7) mid-turn: same deferral, no stamp")
}

// ---------------------------------------------------------------------------
// past every gate
// ---------------------------------------------------------------------------

func TestContractStandingBriefPassRespawnsExactlyOnce(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times, want 1 — contract: Then standingRespawn(a, st, live) is called exactly once", len(*calls))
	}
}

func TestContractStandingBriefPassRespawnArguments(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	live := briefContractLive()
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), live, briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times, want 1", len(*calls))
	}
	c := (*calls)[0]
	if c.id != briefContractID || c.st != st || c.live == nil || !c.live[briefContractSession] {
		t.Fatalf("standingRespawn got (id %q, st %p, live %v), want (%q, %p, the live map) — contract: standingRespawn(a, st, live)", c.id, c.st, c.live, briefContractID, st)
	}
}

func TestContractStandingBriefPassStampValue(t *testing.T) {
	st := useTestStore(t)
	briefContractRecorder(t, st, nil)
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	got, err := st.GetKV(standingBriefKey(briefContractID))
	if err != nil || got != briefContractStampWant() {
		t.Fatalf("stamp = %q (err %v), want %q — contract: the kv row at standingBriefKey(a.ID) is set to now.UnixMilli() as a decimal string", got, err, briefContractStampWant())
	}
}

func TestContractStandingBriefPassStampWrittenBeforeRespawn(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times, want 1", len(*calls))
	}
	if c := (*calls)[0]; c.stampErr != nil || c.stampSaw != briefContractStampWant() {
		t.Fatalf("at respawn time the stamp read %q (err %v), want %q — contract: set … BEFORE anything is killed", c.stampSaw, c.stampErr, briefContractStampWant())
	}
}

func TestContractStandingBriefPassStampHoldsWhenRespawnErrors(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, errors.New("respawn failed"))
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times, want 1", len(*calls))
	}
	if got, _ := st.GetKV(standingBriefKey(briefContractID)); got != briefContractStampWant() {
		t.Fatalf("stamp = %q after a failed respawn, want %q — contract: Its error is logged, never returned, and the stamp stays either way", got, briefContractStampWant())
	}
}

func TestContractStandingBriefPassSecondTickWithinBackoffDoesNotRespawn(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, errors.New("respawn failed"))
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow)
	(&daemonState{}).standingBriefPass(a, st, briefContractPeers(), briefContractLive(), briefContractNow.Add(time.Minute))
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times across two ticks a minute apart, want 1 — contract: standingBriefBackoff is the minimum interval between two brief-drift respawn ATTEMPTS", len(*calls))
	}
}

func TestContractStandingBriefPassIdleNonRunningStatusProceeds(t *testing.T) {
	st := useTestStore(t)
	calls := briefContractRecorder(t, st, nil)
	peers := briefContractPeers()
	peers[0].Status = "blocked"
	a := briefContractAgent(t, briefContractStage(t, "stale\n"), briefContractCurrent, &briefContractCounts{})
	(&daemonState{}).standingBriefPass(a, st, peers, briefContractLive(), briefContractNow)
	if len(*calls) != 1 {
		t.Fatalf("standingRespawn called %d times for a non-running peer, want 1 — contract: gate (7) defers only on Status == \"running\"", len(*calls))
	}
}
