package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
// the given model id — the one field the drift pass reads.
func modelDriftTranscript(t *testing.T, sessionID, model string) {
	t.Helper()
	dir := filepath.Join(util.Home(), ".claude", "projects", "-tmp-kraken-standing-fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var lines []byte
	for _, m := range []string{"claude-fable-5", model} {
		rec := map[string]any{
			"type": "assistant",
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

func TestStandingAgentsPinTheirModels(t *testing.T) {
	want := map[string]string{"kronos": core.KronosModel, "iris": core.IrisModel}
	for _, a := range standingAgents() {
		if a.Model != want[a.ID] {
			t.Errorf("%s: Model = %q, want %q — the drift pass has nothing to hold the transcript against", a.ID, a.Model, want[a.ID])
		}
	}
}

// The newest assistant record decides: a session whose tail is on another
// tier is drift, one on the pinned tier is not, and a non-claude stamp
// (`<synthetic>`, a limit banner's record) is a refusal to judge rather than
// a drift to a model called "<synthetic>".
func TestStandingModelDriftReadsTheRecordedTail(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, _ := contractStandingRescueWorld(t, rig, "kronos-sess-drift")

	modelDriftTranscript(t, "kronos-sess-drift", "claude-opus-4-8")
	got, drifted := standingModelDrift(a, all, "kronos-sess-drift")
	if !drifted || got != "claude-opus-4-8" {
		t.Fatalf("opus tail: drifted=%v got=%q, want true/claude-opus-4-8", drifted, got)
	}

	modelDriftTranscript(t, "kronos-sess-drift", core.KronosModel)
	if got, drifted := standingModelDrift(a, all, "kronos-sess-drift"); drifted {
		t.Fatalf("pinned tail read as drift (got %q)", got)
	}

	modelDriftTranscript(t, "kronos-sess-drift", "<synthetic>")
	if got, drifted := standingModelDrift(a, all, "kronos-sess-drift"); drifted || got != "<synthetic>" {
		t.Fatalf("synthetic tail: drifted=%v got=%q, want false/<synthetic>", drifted, got)
	}
}

// Every quiet arm is a refusal to judge: no recorded id, no transcript for
// it, no peer, a nameless peer (a claude still starting), no pinned model.
func TestStandingModelDriftQuietArms(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, _ := contractStandingRescueWorld(t, rig, "kronos-sess-quiet")
	modelDriftTranscript(t, "kronos-sess-quiet", "claude-opus-4-8")

	if _, drifted := standingModelDrift(a, all, ""); drifted {
		t.Error("empty recorded id judged drift")
	}
	if _, drifted := standingModelDrift(a, all, "kronos-sess-missing"); drifted {
		t.Error("unresolvable transcript judged drift")
	}
	if _, drifted := standingModelDrift(a, nil, "kronos-sess-quiet"); drifted {
		t.Error("no peer judged drift")
	}
	nameless := []*core.Peer{contractStandingPeer("kronos", "%1", "", "")}
	if _, drifted := standingModelDrift(a, nameless, "kronos-sess-quiet"); drifted {
		t.Error("nameless (still-starting) peer judged drift")
	}
	unpinned := a
	unpinned.Model = ""
	if _, drifted := standingModelDrift(unpinned, all, "kronos-sess-quiet"); drifted {
		t.Error("agent with no pinned model judged drift")
	}
}

// One drifted tick respawns exactly once, stamps the backoff BEFORE the
// respawn seam is reached, and the next tick inside the window respawns
// nothing; a tick past the window respawns again.
func TestStandingModelPassRespawnsOnceAndHonoursTheBackoff(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, live := contractStandingRescueWorld(t, rig, "kronos-sess-pass")
	modelDriftTranscript(t, "kronos-sess-pass", "claude-opus-4-8")
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
	a, all, live := contractStandingRescueWorld(t, rig, "kronos-sess-fine")
	modelDriftTranscript(t, "kronos-sess-fine", core.KronosModel)

	newDaemon().standingModelPass(a, rig.st, all, live, time.Unix(1_700_000_000, 0))
	if len(rig.Respawns) != 0 {
		t.Fatalf("respawned without drift: %+v", rig.Respawns)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Fatalf("stamped without drift: %d", got)
	}
}

// An unresolved usage-limit hit owns the pane: the drift pass defers to it
// without stamping, and acts once the hit is resolved.
func TestStandingModelPassDefersToAnOpenLimitHit(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, all, live := contractStandingRescueWorld(t, rig, "kronos-sess-hit")
	modelDriftTranscript(t, "kronos-sess-hit", "claude-opus-4-8")
	t0 := time.Unix(1_700_000_000, 0)
	key := standingHitKey("kronos")
	contractStandingPutHit(t, rig.st, key, LimitHit{Error: "rate_limit", Details: "429", At: t0.Add(-time.Minute).Unix(), Deliveries: 1})

	d := newDaemon()
	d.standingModelPass(a, rig.st, all, live, t0)
	if len(rig.Respawns) != 0 {
		t.Fatalf("respawned over an open limit hit: %+v", rig.Respawns)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Fatalf("stamped while deferring: %d", got)
	}

	contractStandingPutHit(t, rig.st, key, LimitHit{Error: "rate_limit", Details: "429", At: t0.Add(-time.Minute).Unix(), Deliveries: 1, HandledAt: t0.Unix()})
	d.standingModelPass(a, rig.st, all, live, t0.Add(time.Minute))
	if len(rig.Respawns) != 1 {
		t.Fatalf("did not respawn once the hit resolved: %+v", rig.Respawns)
	}
}

// The identity gate is the rescue's: a pane that is not the agent by name or
// recorded session id is never killed on the positional fallback.
func TestStandingModelPassRefusesWithoutIdentity(t *testing.T) {
	rig := contractStandingRigNew(t)
	a, _, live := contractStandingRescueWorld(t, rig, "kronos-sess-ident")
	modelDriftTranscript(t, "kronos-sess-ident", "claude-opus-4-8")
	// Named, so the drift check runs — but under a name that is not kronos'
	// and a session id that is not the recorded one.
	stranger := []*core.Peer{contractStandingPeer("kronos", "%1", "someone-else", "other-sess")}

	newDaemon().standingModelPass(a, rig.st, stranger, live, time.Unix(1_700_000_000, 0))
	if len(rig.Respawns) != 0 {
		t.Fatalf("killed a pane that is not kronos by identity: %+v", rig.Respawns)
	}
	if got := readStandingModelStamp(rig.st, "kronos"); got != 0 {
		t.Fatalf("stamped a refused round: %d", got)
	}
}

// A respawn that fails keeps its stamp, so the retry is a backoff window
// away rather than a tick away.
func TestStandingModelPassKeepsTheStampOnRespawnFailure(t *testing.T) {
	rig := contractStandingRigNew(t)
	rig.RespawnErr = os.ErrPermission
	a, all, live := contractStandingRescueWorld(t, rig, "kronos-sess-fail")
	modelDriftTranscript(t, "kronos-sess-fail", "claude-opus-4-8")
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
	a, all, live := contractStandingRescueWorld(t, rig, "kronos-sess-off")
	modelDriftTranscript(t, "kronos-sess-off", "claude-opus-4-8")
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

func TestTranscriptModelIDReadsTheFullID(t *testing.T) {
	useTestStore(t)
	modelDriftTranscript(t, "model-id-sess", "claude-opus-4-8")
	if got := core.TranscriptModelID(core.TranscriptPathForSession("model-id-sess")); got != "claude-opus-4-8" {
		t.Fatalf("TranscriptModelID = %q, want the full id claude-opus-4-8", got)
	}
	if got := core.TranscriptModelID(""); got != "" {
		t.Fatalf("empty path: %q", got)
	}
}
