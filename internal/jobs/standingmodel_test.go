package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// standingmodel_test.go — the standing agents' model-drift pass. Every
// store-backed case runs inside contractStandingRigNew's isolated world (HOME
// sandboxed, every effectful seam recorded, PATH emptied), so nothing here
// touches a real tmux server, keychain or notification centre; the respawn
// seam is the whole observable surface of the pass.

// modelDriftTranscript writes a transcript for sessionID where
// core.TranscriptPathForSession looks, whose newest assistant record carries
// the given model id at the given clock — the two fields the drift pass
// reads. An earlier fable-5 record rides along so "newest wins" is what the
// fixture actually exercises.
func modelDriftTranscript(t *testing.T, sessionID, model string, at time.Time) {
	t.Helper()
	dir := filepath.Join(util.Home(), ".claude", "projects", "-tmp-kraken-standing-fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []byte
	for i, m := range []string{"claude-fable-5", model} {
		rec := map[string]any{
			"type":      "assistant",
			"timestamp": at.Add(time.Duration(i-1) * time.Minute).UTC().Format(time.RFC3339),
			"message": map[string]any{
				"model":   m,
				"content": []any{map[string]any{"type": "text", "text": "ok"}},
			},
		}
		line, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, append(line, '\n')...)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, lines, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := core.TranscriptPathForSession(sessionID); got != path {
		t.Fatalf("fixture transcript for %q resolves to %q, want %q", sessionID, got, path)
	}
}

// driftWorld is contractStandingRescueWorld plus a transcript whose tail
// post-dates the recorded spawn stamp — the ordinary "the agent has been
// running on this model for a while" world.
func driftWorld(t *testing.T, rig *contractStandingRig, sid, model string) (standingAgent, []*core.Peer, map[string]bool) {
	t.Helper()
	a, all, live := contractStandingRescueWorld(t, rig, sid)
	modelDriftTranscript(t, sid, model, time.Now().Add(time.Hour))
	return a, all, live
}

func TestStandingAgentsPinTheirModels(t *testing.T) {
	want := map[string]string{"kronos": core.KronosModel, "iris": core.IrisModel}
	for _, a := range standingAgents() {
		w, ok := want[a.ID]
		if !ok {
			t.Fatalf("unexpected standing agent %q — add its pinned model here", a.ID)
		}
		if a.Model != w {
			t.Errorf("%s: Model = %q, want %q — the drift pass has nothing to hold the transcript against", a.ID, a.Model, w)
		}
		if a.SpawnStamp == nil {
			t.Errorf("%s: SpawnStamp is nil — the drift pass cannot tell a stale tail from evidence", a.ID)
		}
	}
	if len(standingAgents()) != len(want) {
		t.Fatalf("standingAgents() has %d rows, this test knows %d", len(standingAgents()), len(want))
	}
}

// The newest assistant record decides: a tail on another tier is drift, one
// on the pinned tier is not, and a non-claude stamp (`<synthetic>`, a limit
// banner's record) is a refusal to judge rather than a drift to a model
// called "<synthetic>".
func TestStandingModelDriftReadsTheRecordedTail(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, _, _ := contractStandingRescueWorld(t, rig, "kronos-sess-drift")
	fresh := time.Now().Add(time.Hour)

	modelDriftTranscript(t, "kronos-sess-drift", "claude-opus-4-8", fresh)
	got, drifted := standingModelDrift(a, "kronos-sess-drift", 0)
	if !drifted || got != "claude-opus-4-8" {
		t.Fatalf("opus tail: drifted=%v got=%q, want true/claude-opus-4-8", drifted, got)
	}

	modelDriftTranscript(t, "kronos-sess-drift", core.KronosModel, fresh)
	if got, drifted := standingModelDrift(a, "kronos-sess-drift", 0); drifted {
		t.Fatalf("pinned tail read as drift (got %q)", got)
	}

	modelDriftTranscript(t, "kronos-sess-drift", "<synthetic>", fresh)
	if got, drifted := standingModelDrift(a, "kronos-sess-drift", 0); drifted || got != "<synthetic>" {
		t.Fatalf("synthetic tail: drifted=%v got=%q, want false/<synthetic>", drifted, got)
	}
}

// A tail record OLDER than the agent's spawn stamp is the PREVIOUS process'
// evidence — a `-r` resume appends to the same transcript — so it is never
// judged: a freshly resumed, correctly-pinned agent must not be killed on
// the tier it was resumed to fix.
func TestStandingModelDriftRefusesAStaleTail(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, _, _ := contractStandingRescueWorld(t, rig, "kronos-sess-stale")
	tail := time.Now()
	modelDriftTranscript(t, "kronos-sess-stale", "claude-opus-4-8", tail)

	if _, drifted := standingModelDrift(a, "kronos-sess-stale", tail.Add(30*time.Minute).UnixMilli()); drifted {
		t.Fatal("a tail predating the spawn stamp was judged drift")
	}
	if _, drifted := standingModelDrift(a, "kronos-sess-stale", tail.Add(-30*time.Minute).UnixMilli()); !drifted {
		t.Fatal("a tail postdating the spawn stamp was not judged")
	}
	// No spawn stamp recorded at all: the tail is the only evidence there is.
	if _, drifted := standingModelDrift(a, "kronos-sess-stale", 0); !drifted {
		t.Fatal("zero spawn stamp should not veto the judgment")
	}
}

// Every quiet arm is a refusal to judge: no recorded id, no transcript for
// it, no pinned model, no parseable record clock.
func TestStandingModelDriftQuietArms(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, _, _ := contractStandingRescueWorld(t, rig, "kronos-sess-quiet")
	modelDriftTranscript(t, "kronos-sess-quiet", "claude-opus-4-8", time.Now().Add(time.Hour))

	if _, drifted := standingModelDrift(a, "", 0); drifted {
		t.Error("empty recorded id judged drift")
	}
	if _, drifted := standingModelDrift(a, "kronos-sess-missing", 0); drifted {
		t.Error("unresolvable transcript judged drift")
	}
	unpinned := a
	unpinned.Model = ""
	if _, drifted := standingModelDrift(unpinned, "kronos-sess-quiet", 0); drifted {
		t.Error("agent with no pinned model judged drift")
	}
	// A record with no timestamp has no clock to hold against the spawn
	// stamp, so it is not judged even with no stamp recorded.
	dir := filepath.Join(util.Home(), ".claude", "projects", "-tmp-kraken-standing-fixture")
	rec := `{"type":"assistant","message":{"model":"claude-opus-4-8","content":[{"type":"text","text":"ok"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "kronos-sess-noclock.jsonl"), []byte(rec), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, drifted := standingModelDrift(a, "kronos-sess-noclock", 0); drifted {
		t.Error("clockless record judged drift")
	}
}

// One drifted tick respawns exactly once, stamps the backoff BEFORE the
// respawn seam is reached, and the next tick inside the window respawns
// nothing; a tick past the window respawns again.
func TestStandingModelPassRespawnsOnceAndHonoursTheBackoff(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, live := driftWorld(t, rig, "kronos-sess-pass", "claude-opus-4-8")
	t0 := time.Unix(1_700_000_000, 0)

	d := newDaemon()
	d.standingModelPass(a, rig.st, all, live, t0)
	if len(rig.Respawns) != 1 || rig.Respawns[0].agentID != "kronos" || rig.Respawns[0].session != "kronos" {
		t.Fatalf("tick 1 respawns = %+v, want exactly one for kronos", rig.Respawns)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != t0.Unix() {
		t.Fatalf("backoff stamp = %d, want %d", got, t0.Unix())
	}

	d.standingModelPass(a, rig.st, all, live, t0.Add(time.Minute))
	d.standingModelPass(a, rig.st, all, live, t0.Add(standingModelDriftBackoff-time.Second))
	if len(rig.Respawns) != 1 {
		t.Fatalf("respawned inside the backoff window: %+v", rig.Respawns)
	}

	d.standingModelPass(a, rig.st, all, live, t0.Add(standingModelDriftBackoff+time.Second))
	if len(rig.Respawns) != 2 {
		t.Fatalf("no respawn past the backoff window: %+v", rig.Respawns)
	}
}

// A pinned-tier transcript costs nothing: no stamp, no respawn.
func TestStandingModelPassIsQuietWithoutDrift(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, live := driftWorld(t, rig, "kronos-sess-fine", core.KronosModel)

	newDaemon().standingModelPass(a, rig.st, all, live, time.Unix(1_700_000_000, 0))
	if len(rig.Respawns) != 0 {
		t.Fatalf("respawned without drift: %+v", rig.Respawns)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Fatalf("stamped without drift: %d", got)
	}
}

// An unresolved RATE-LIMIT hit owns the pane: the drift pass defers to it
// without stamping, and acts once the hit is resolved. A hit of any OTHER
// error type never defers — nothing ever resolves one (the rescue skips it
// unresolved by design), so an unscoped deferral would disable this pass
// silently forever on one stray delivery.
func TestStandingModelPassDefersOnlyToARateLimitHit(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, live := driftWorld(t, rig, "kronos-sess-hit", "claude-opus-4-8")
	t0 := time.Unix(1_700_000_000, 0)
	key := standingHitKey("kronos")
	contractStandingPutHit(t, rig.st, key, LimitHit{Error: "rate_limit", Details: "429", At: t0.Add(-time.Minute).Unix(), Deliveries: 1})

	d := newDaemon()
	d.standingModelPass(a, rig.st, all, live, t0)
	if len(rig.Respawns) != 0 {
		t.Fatalf("respawned over an open rate-limit hit: %+v", rig.Respawns)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Fatalf("stamped while deferring: %d", got)
	}

	// A non-rate_limit hit — which the rescue will never resolve — does not
	// own the pane.
	contractStandingPutHit(t, rig.st, key, LimitHit{Error: "authentication_failed", Details: "401", At: t0.Add(-time.Minute).Unix(), Deliveries: 1})
	d.standingModelPass(a, rig.st, all, live, t0.Add(time.Minute))
	if len(rig.Respawns) != 1 {
		t.Fatalf("did not act over a non-rate_limit hit: %+v", rig.Respawns)
	}

	// And a RESOLVED rate-limit hit releases the pane.
	if err := rig.st.SetKV(standingModelDriftKey("kronos"), "0"); err != nil {
		t.Fatal(err)
	}
	contractStandingPutHit(t, rig.st, key, LimitHit{Error: "rate_limit", Details: "429", At: t0.Add(-time.Minute).Unix(), Deliveries: 1, HandledAt: t0.Unix()})
	d.standingModelPass(a, rig.st, all, live, t0.Add(2*time.Minute))
	if len(rig.Respawns) != 2 {
		t.Fatalf("did not respawn once the hit resolved: %+v", rig.Respawns)
	}
}

// The identity gate is standingKillTarget's, and with the drift judgment
// peer-free it is REACHABLE: a session whose only pane is not the agent by
// name or by the recorded session id (carl's own claude in that session, a
// stranger) is deferred, never killed — no stamp, no respawn.
func TestStandingModelPassRefusesWithoutIdentity(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, _, live := contractStandingRescueWorld(t, rig, "kronos-sess-ident")
	modelDriftTranscript(t, "kronos-sess-ident", "claude-opus-4-8", time.Now().Add(time.Hour))
	stranger := []*core.Peer{contractStandingPeer("kronos", "%1", "someone-else", "other-sess")}

	newDaemon().standingModelPass(a, rig.st, stranger, live, time.Unix(1_700_000_000, 0))
	if len(rig.Respawns) != 0 {
		t.Fatalf("killed a pane that is not kronos by identity: %+v", rig.Respawns)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Fatalf("stamped a refused round: %d", got)
	}

	// A still-booting claude — nameless, no session file, so neither
	// signature — is the same refusal.
	booting := []*core.Peer{contractStandingPeer("kronos", "%1", "", "")}
	newDaemon().standingModelPass(a, rig.st, booting, live, time.Unix(1_700_000_000, 0))
	if len(rig.Respawns) != 0 {
		t.Fatalf("killed a still-booting nameless pane: %+v", rig.Respawns)
	}
}

// A peer mid-turn is not killed: the pass waits for an idle tick rather than
// losing whatever the turn was doing. (The rescue kills mid-anything because
// its target is latched and cannot progress; a drifted agent is working.)
func TestStandingModelPassWaitsForAnIdlePeer(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, live := driftWorld(t, rig, "kronos-sess-busy", "claude-opus-4-8")
	all[0].Status = "running"
	t0 := time.Unix(1_700_000_000, 0)

	d := newDaemon()
	d.standingModelPass(a, rig.st, all, live, t0)
	if len(rig.Respawns) != 0 {
		t.Fatalf("killed a peer mid-turn: %+v", rig.Respawns)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Fatalf("stamped a deferred round: %d", got)
	}
	all[0].Status = "waiting"
	d.standingModelPass(a, rig.st, all, live, t0.Add(time.Minute))
	if len(rig.Respawns) != 1 {
		t.Fatalf("did not respawn once the peer went idle: %+v", rig.Respawns)
	}
}

// A respawn that fails keeps its stamp, so the retry is a backoff window
// away rather than a tick away.
func TestStandingModelPassKeepsTheStampOnRespawnFailure(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.RespawnErr = os.ErrPermission
	a, all, live := driftWorld(t, rig, "kronos-sess-fail", "claude-opus-4-8")
	t0 := time.Unix(1_700_000_000, 0)

	d := newDaemon()
	d.standingModelPass(a, rig.st, all, live, t0)
	d.standingModelPass(a, rig.st, all, live, t0.Add(time.Minute))
	if len(rig.Respawns) != 1 {
		t.Fatalf("respawns = %d after a failure inside the window, want 1", len(rig.Respawns))
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != t0.Unix() {
		t.Fatalf("stamp = %d, want %d", got, t0.Unix())
	}
}

// Disabled, or not live: nothing is read and nothing is stamped.
func TestStandingModelPassSkipsWhenDisabledOrNotLive(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, live := driftWorld(t, rig, "kronos-sess-off", "claude-opus-4-8")
	d := newDaemon()

	d.standingModelPass(a, rig.st, all, map[string]bool{}, time.Unix(1_700_000_000, 0))
	off := a
	off.Enabled = func() bool { return false }
	d.standingModelPass(off, rig.st, all, live, time.Unix(1_700_000_000, 0))
	if len(rig.Respawns) != 0 {
		t.Fatalf("respawned while disabled or not live: %+v", rig.Respawns)
	}
}

// The stamp survives a malformed row as zero rather than as an error.
func TestReadStandingModelStampDegradesToZero(t *testing.T) {
	rig := contractStandingRigNew(t)
	if got := readStandingModelStamp(nil, "kronos"); got != 0 {
		t.Errorf("nil store: %d", got)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Errorf("missing row: %d", got)
	}
	if err := rig.st.SetKV(standingModelDriftKey("kronos"), "not-a-number"); err != nil {
		t.Fatal(err)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Errorf("malformed row: %d", got)
	}
	if err := rig.st.SetKV(standingModelDriftKey("kronos"), strconv.FormatInt(42, 10)); err != nil {
		t.Fatal(err)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 42 {
		t.Errorf("round trip: %d, want 42", got)
	}
}

// The tick wires the pass, and BEFORE the health pass — the ordering the
// daemon comment calls load-bearing. Pinned at the source level because
// every behavioural test drives the pass directly, so without this the whole
// feature can be unwired and the suite stays green (measured by a reviewer:
// deleting the call left ./internal/jobs ok).
func TestDaemonTickWiresTheModelDriftPass(t *testing.T) {
	src, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	model := strings.Index(string(src), "d.standingModelPass(a,")
	health := strings.Index(string(src), "d.standingHealthPass(a,")
	if model < 0 {
		t.Fatal("daemon.go does not call d.standingModelPass — the feature is unwired")
	}
	if health < 0 {
		t.Fatal("daemon.go does not call d.standingHealthPass — this pin's landmark moved; update it")
	}
	if model > health {
		t.Fatal("d.standingModelPass runs after d.standingHealthPass — the drift pass must run first so at most one kill path acts on one tick's snapshot")
	}
}
