package jobs

// revived_edge_test.go — athena-edge's BLIND tests for slice A of
// dream-2026-10-30 (the limit rescue leaves a zeus that already took a real
// turn): boundaries, error paths and refusals of LimitHit.LastDeliveryAt,
// hitEvidenceAt, revivedSinceHit, zeusRanSince, transcriptTurnSince,
// rescueLeaveRevived and limitHitReactivated. Authored from 50-contract.md
// alone. Every package-level identifier is prefixed edgeRev.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
)

// edgeRevNow is an invented anchor.
var edgeRevNow = time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC)

// edgeRevAssistant is one real assistant record at t on model.
func edgeRevAssistant(t time.Time, model string) string {
	return `{"type":"assistant","timestamp":"` + t.UTC().Format(time.RFC3339Nano) + `","message":{"model":"` + model + `"}}`
}

// edgeRevWrite writes lines (each newline-terminated) into a fresh file.
func edgeRevWrite(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// edgeRevPeerWithTranscript sandboxes HOME and places a claude transcript for
// an invented session where core.ReadTranscriptPath finds it.
func edgeRevPeerWithTranscript(t *testing.T, status string, lines ...string) *core.Peer {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	sid := "e0e0e0e0-1111-4222-8333-444444444444"
	dir := filepath.Join(home, ".claude", "projects", "-tmp-edge-rev-cell")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return &core.Peer{SessionID: sid, Status: status, Agent: "claude", Name: "zeus-edge-rev", CWD: "/tmp/edge/rev/cell"}
}

// ---------------------------------------------------------------------------
// LimitHit.LastDeliveryAt — wire key and fold stamping
// ---------------------------------------------------------------------------

func TestEdgeRevivedLastDeliveryAtWire(t *testing.T) {
	var old LimitHit
	if err := json.Unmarshal([]byte(`{"error":"rate_limit","at":1700000000}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.LastDeliveryAt != 0 {
		t.Errorf("record without the key: LastDeliveryAt = %d, want 0", old.LastDeliveryAt)
	}

	raw, err := json.Marshal(LimitHit{Error: "rate_limit", At: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "last_delivery_at") {
		t.Errorf("zero LastDeliveryAt must be omitted (omitempty): %s", raw)
	}

	raw, err = json.Marshal(LimitHit{Error: "rate_limit", At: 1700000000, LastDeliveryAt: 1700000123})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if v, ok := keys["last_delivery_at"]; !ok || v != float64(1700000123) {
		t.Errorf("last_delivery_at = %v (present %v), want 1700000123", v, ok)
	}
}

func TestEdgeRevivedMergeStampsLastDelivery(t *testing.T) {
	// fresh record from an empty current one
	fresh := mergeLimitHitDelivery(LimitHit{}, "rate_limit", "You've hit your session limit · resets 3am (UTC)", "sess-a", "", edgeRevNow)
	if fresh.LastDeliveryAt != edgeRevNow.Unix() {
		t.Errorf("fresh: LastDeliveryAt = %d, want %d", fresh.LastDeliveryAt, edgeRevNow.Unix())
	}

	// a fold onto an unresolved same-error record restamps to the fold's now
	later := edgeRevNow.Add(7 * time.Minute)
	folded := mergeLimitHitDelivery(fresh, "rate_limit", "429", "sess-a", "agent-x", later)
	if folded.LastDeliveryAt != later.Unix() {
		t.Errorf("fold: LastDeliveryAt = %d, want %d", folded.LastDeliveryAt, later.Unix())
	}
	if folded.At != fresh.At {
		t.Errorf("fold moved At %d → %d; only LastDeliveryAt tracks the latest delivery", fresh.At, folded.At)
	}

	// a second fold restamps again
	latest := later.Add(90 * time.Second)
	again := mergeLimitHitDelivery(folded, "rate_limit", "429", "sess-a", "", latest)
	if again.LastDeliveryAt != latest.Unix() {
		t.Errorf("second fold: LastDeliveryAt = %d, want %d", again.LastDeliveryAt, latest.Unix())
	}
}

// ---------------------------------------------------------------------------
// hitEvidenceAt
// ---------------------------------------------------------------------------

func TestEdgeRevivedHitEvidenceAt(t *testing.T) {
	cases := []struct {
		name string
		hit  LimitHit
		want int64
	}{
		{"both zero", LimitHit{}, 0},
		{"At only", LimitHit{At: 100}, 100},
		{"LastDeliveryAt only", LimitHit{LastDeliveryAt: 250}, 250},
		{"LastDeliveryAt later", LimitHit{At: 100, LastDeliveryAt: 250}, 250},
		{"LastDeliveryAt earlier", LimitHit{At: 300, LastDeliveryAt: 250}, 300},
		{"equal", LimitHit{At: 400, LastDeliveryAt: 400}, 400},
		{"swap and reset do not count", LimitHit{At: 100, LastDeliveryAt: 150, SwappedAt: 900, ResetAt: 990, HandledAt: 999, LastAttempt: 950}, 150},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hitEvidenceAt(c.hit); got != c.want {
				t.Errorf("hitEvidenceAt(%+v) = %d, want %d", c.hit, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// transcriptTurnSince
// ---------------------------------------------------------------------------

func TestEdgeRevivedTranscriptTurnSinceBoundaries(t *testing.T) {
	since := edgeRevNow.Unix()
	after := edgeRevNow.Add(2 * time.Second)
	before := edgeRevNow.Add(-2 * time.Second)
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"empty file", nil, false},
		{"only non-JSON", []string{"not json at all", "{broken"}, false},
		{"real turn after since", []string{edgeRevAssistant(after, "claude-edge-7")}, true},
		{"real turn exactly at since is not strictly after", []string{edgeRevAssistant(edgeRevNow, "claude-edge-7")}, false},
		{"real turn one second after since", []string{edgeRevAssistant(edgeRevNow.Add(time.Second), "claude-edge-7")}, true},
		{"real turn only before since", []string{edgeRevAssistant(before, "claude-edge-7")}, false},
		{"synthetic after since", []string{edgeRevAssistant(after, "<synthetic>")}, false},
		{"empty model after since", []string{edgeRevAssistant(after, "")}, false},
		{"no message.model key", []string{`{"type":"assistant","timestamp":"` + after.Format(time.RFC3339Nano) + `","message":{}}`}, false},
		{"no message at all", []string{`{"type":"assistant","timestamp":"` + after.Format(time.RFC3339Nano) + `"}`}, false},
		{"assistant without timestamp", []string{`{"type":"assistant","message":{"model":"claude-edge-7"}}`}, false},
		{"assistant with unparseable timestamp", []string{`{"type":"assistant","timestamp":"yesterday-ish","message":{"model":"claude-edge-7"}}`}, false},
		{"user record after since", []string{`{"type":"user","timestamp":"` + after.Format(time.RFC3339Nano) + `","message":{"model":"claude-edge-7"}}`}, false},
		{"system record after since", []string{`{"type":"system","timestamp":"` + after.Format(time.RFC3339Nano) + `","message":{"model":"claude-edge-7"}}`}, false},
		{"real before, synthetic after", []string{edgeRevAssistant(before, "claude-edge-7"), edgeRevAssistant(after, "<synthetic>")}, false},
		{"synthetic then real, both after", []string{edgeRevAssistant(after, "<synthetic>"), edgeRevAssistant(after.Add(time.Second), "claude-edge-7")}, true},
		{"real after among junk", []string{"garbage", edgeRevAssistant(before, "claude-edge-7"), "{", edgeRevAssistant(after, "claude-edge-7"), `{"type":"user"}`}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := edgeRevWrite(t, c.lines...)
			if got := transcriptTurnSince(p, since); got != c.want {
				t.Errorf("transcriptTurnSince = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEdgeRevivedTranscriptTurnSinceMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", "absent.jsonl")
	if transcriptTurnSince(missing, edgeRevNow.Unix()) {
		t.Error("a missing file must read false")
	}
	if transcriptTurnSince("", edgeRevNow.Unix()) {
		t.Error("an empty path must read false")
	}
}

// A real turn whose last byte is not newline-terminated (a transcript caught
// mid-write after its final complete line) still counts when complete JSON.
func TestEdgeRevivedTranscriptTurnSinceNoTrailingNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	body := edgeRevAssistant(edgeRevNow.Add(5*time.Second), "claude-edge-7")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if !transcriptTurnSince(p, edgeRevNow.Unix()) {
		t.Error("a single complete record without trailing newline must still count")
	}
}

func TestEdgeRevivedTranscriptTurnTailIsAtLeastOneMiB(t *testing.T) {
	if int64(transcriptTurnTail) < 1<<20 {
		t.Errorf("transcriptTurnTail = %d, want ≥ 1 MiB", int64(transcriptTurnTail))
	}
}

// Only the file's last transcriptTurnTail bytes are read: a real turn placed
// before that window is invisible, while one at the end of an oversize file
// is found.
func TestEdgeRevivedTranscriptTurnSinceTailWindow(t *testing.T) {
	since := edgeRevNow.Unix()
	filler := `{"type":"user","timestamp":"` + edgeRevNow.Add(-time.Hour).Format(time.RFC3339Nano) + `","message":{"content":"` + strings.Repeat("f", 900) + `"}}`
	n := int(int64(transcriptTurnTail)/int64(len(filler)+1)) + 64

	t.Run("real turn outside the tail", func(t *testing.T) {
		lines := []string{edgeRevAssistant(edgeRevNow.Add(time.Minute), "claude-edge-7")}
		for i := 0; i < n; i++ {
			lines = append(lines, filler)
		}
		p := edgeRevWrite(t, lines...)
		if transcriptTurnSince(p, since) {
			t.Error("a real turn before the last transcriptTurnTail bytes must not be seen")
		}
	})
	t.Run("real turn at the end of an oversize file", func(t *testing.T) {
		var lines []string
		for i := 0; i < n; i++ {
			lines = append(lines, filler)
		}
		lines = append(lines, edgeRevAssistant(edgeRevNow.Add(time.Minute), "claude-edge-7"))
		p := edgeRevWrite(t, lines...)
		if !transcriptTurnSince(p, since) {
			t.Error("a real turn at the end of an oversize file must be seen")
		}
	})
}

// ---------------------------------------------------------------------------
// zeusRanSince
// ---------------------------------------------------------------------------

func TestEdgeRevivedZeusRanSinceRefusals(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	cases := []struct {
		name  string
		p     *core.Peer
		since int64
		want  bool
	}{
		{"nil peer", nil, edgeRevNow.Unix(), false},
		{"nil peer, since 0", nil, 0, false},
		{"since 0 refuses even a running peer", &core.Peer{Status: "running", SessionID: "s-1", Agent: "claude"}, 0, false},
		// amended at the join (review round 1): status is not evidence for claude
		{"running answers false without a transcript", &core.Peer{Status: "running", SessionID: "s-no-file", Agent: "claude"}, edgeRevNow.Unix(), false},
		{"waiting with no session id", &core.Peer{Status: "waiting", Agent: "claude"}, edgeRevNow.Unix(), false},
		{"idle with no transcript on disk", &core.Peer{Status: "idle", SessionID: "s-absent", Agent: "claude"}, edgeRevNow.Unix(), false},
		{"shell pane", &core.Peer{Status: "idle", Role: "shell", SessionID: "s-shell"}, edgeRevNow.Unix(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := zeusRanSince(c.p, c.since); got != c.want {
				t.Errorf("zeusRanSince = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEdgeRevivedZeusRanSinceReadsTranscript(t *testing.T) {
	since := edgeRevNow.Unix()
	t.Run("turn after since", func(t *testing.T) {
		p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(edgeRevNow.Add(time.Minute), "claude-edge-7"))
		if !zeusRanSince(p, since) {
			t.Error("a waiting zeus whose transcript holds a real turn after since must answer true")
		}
	})
	t.Run("turn only before since", func(t *testing.T) {
		p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(edgeRevNow.Add(-time.Minute), "claude-edge-7"))
		if zeusRanSince(p, since) {
			t.Error("a turn before since must answer false")
		}
	})
	t.Run("synthetic after since", func(t *testing.T) {
		p := edgeRevPeerWithTranscript(t, "blocked", edgeRevAssistant(edgeRevNow.Add(time.Minute), "<synthetic>"))
		if zeusRanSince(p, since) {
			t.Error("a <synthetic> record is not a turn")
		}
	})
	t.Run("guessed session id is unreadable", func(t *testing.T) {
		p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(edgeRevNow.Add(time.Minute), "claude-edge-7"))
		p.SessionIDGuessed = true
		if zeusRanSince(p, since) {
			t.Error("ReadTranscriptPath refuses a guessed session id, so the answer must be false")
		}
	})
}

// ---------------------------------------------------------------------------
// revivedSinceHit
// ---------------------------------------------------------------------------

func TestEdgeRevivedSinceHitGuards(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hit := LimitHit{Error: "rate_limit", At: edgeRevNow.Unix()}
	cases := []struct {
		name  string
		hit   LimitHit
		agent string
		zeus  *core.Peer
		want  bool
	}{
		{"nil zeus", hit, "claude", nil, false},
		{"nil zeus, codex", hit, "codex", nil, false},
		// amended at the join (review round 1): only codex is judged by status
		{"running claude without a transcript", hit, "claude", &core.Peer{Status: "running"}, false},
		{"running codex", hit, "codex", &core.Peer{Status: "running", Agent: "codex"}, true},
		{"running with empty agent, no transcript", hit, "", &core.Peer{Status: "running"}, false},
		{"running claude with an empty hit (evidence 0)", LimitHit{}, "claude", &core.Peer{Status: "running"}, false},
		{"waiting codex never reads a transcript", hit, "codex", &core.Peer{Status: "waiting", Agent: "codex", RolloutPath: "/nonexistent"}, false},
		{"idle claude with an empty hit (evidence 0)", LimitHit{}, "claude", &core.Peer{Status: "idle", SessionID: "s-1"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := revivedSinceHit(c.hit, c.agent, c.zeus); got != c.want {
				t.Errorf("revivedSinceHit = %v, want %v", got, c.want)
			}
		})
	}
}

// A waiting codex zeus is false even when a claude-shaped transcript with a
// real later turn is reachable for its session id.
func TestEdgeRevivedSinceHitCodexWaitingIgnoresTranscript(t *testing.T) {
	p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(edgeRevNow.Add(time.Hour), "claude-edge-7"))
	if revivedSinceHit(LimitHit{Error: "rate_limit", At: edgeRevNow.Unix()}, "codex", p) {
		t.Error("agent codex and not running must answer false")
	}
}

// The evidence moment is max(At, LastDeliveryAt): a turn between the first
// delivery and the latest one is not a revival.
func TestEdgeRevivedSinceHitUsesLatestDelivery(t *testing.T) {
	at := edgeRevNow
	last := edgeRevNow.Add(10 * time.Minute)
	hit := LimitHit{Error: "rate_limit", At: at.Unix(), LastDeliveryAt: last.Unix(), Deliveries: 3}

	t.Run("turn between At and LastDeliveryAt", func(t *testing.T) {
		p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(at.Add(5*time.Minute), "claude-edge-7"))
		if revivedSinceHit(hit, "claude", p) {
			t.Error("a turn before the latest delivery is not evidence of a revival")
		}
	})
	t.Run("turn exactly at LastDeliveryAt", func(t *testing.T) {
		p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(last, "claude-edge-7"))
		if revivedSinceHit(hit, "claude", p) {
			t.Error("strictly after: a turn at the evidence second is not after it")
		}
	})
	t.Run("turn after LastDeliveryAt", func(t *testing.T) {
		p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(last.Add(time.Minute), "claude-edge-7"))
		if !revivedSinceHit(hit, "claude", p) {
			t.Error("a real turn after the latest delivery is a revival")
		}
	})
	t.Run("LastDeliveryAt older than At", func(t *testing.T) {
		odd := LimitHit{Error: "rate_limit", At: last.Unix(), LastDeliveryAt: at.Unix()}
		p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(at.Add(5*time.Minute), "claude-edge-7"))
		if revivedSinceHit(odd, "claude", p) {
			t.Error("the evidence is the LARGER of the two; a turn before At is no revival")
		}
	})
}

// The guard ignores SwappedAt and ResetAt: rebind arm, reset arm and park
// arm alike.
func TestEdgeRevivedSinceHitIgnoresSwapAndReset(t *testing.T) {
	turn := edgeRevNow.Add(time.Minute)
	for _, c := range []struct {
		name string
		hit  LimitHit
	}{
		{"swapped after the turn", LimitHit{Error: "rate_limit", At: edgeRevNow.Unix(), SwappedAt: turn.Add(time.Hour).Unix(), SwappedTo: "slot-b@example.invalid"}},
		{"reset in the future", LimitHit{Error: "rate_limit", At: edgeRevNow.Unix(), ResetAt: turn.Add(3 * time.Hour).Unix()}},
		{"parked with attempts spent", LimitHit{Error: "rate_limit", At: edgeRevNow.Unix(), Attempts: 3, LastAttempt: turn.Add(time.Hour).Unix()}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := edgeRevPeerWithTranscript(t, "waiting", edgeRevAssistant(turn, "claude-edge-7"))
			if !revivedSinceHit(c.hit, "claude", p) {
				t.Error("a real turn after the hit evidence is a revival regardless of SwappedAt/ResetAt/attempts")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// rescueLeaveRevived
// ---------------------------------------------------------------------------

func TestEdgeRevivedLeaveRevivedApply(t *testing.T) {
	if string(rescueLeaveRevived) != "leave-revived" {
		t.Errorf("rescueLeaveRevived = %q, want %q", string(rescueLeaveRevived), "leave-revived")
	}
	now := edgeRevNow.Add(20 * time.Minute)
	hit := LimitHit{
		Error: "rate_limit", Details: "You've hit your session limit · resets 11am (UTC)",
		At: edgeRevNow.Unix(), LastDeliveryAt: edgeRevNow.Add(5 * time.Minute).Unix(),
		SessionID: "sess-z", Deliveries: 6, Attempts: 2, LastAttempt: edgeRevNow.Add(4 * time.Minute).Unix(),
		SwappedAt: edgeRevNow.Add(3 * time.Minute).Unix(), SwappedTo: "slot-c@example.invalid",
	}
	got := limitRescueApply(hit, rescueLeaveRevived, "", "", now)
	if got.HandledAt != now.Unix() {
		t.Errorf("HandledAt = %d, want %d", got.HandledAt, now.Unix())
	}
	if got.Outcome != "resumed-by-hand" {
		t.Errorf("Outcome = %q, want resumed-by-hand", got.Outcome)
	}
	if got.Attempts != hit.Attempts || got.LastAttempt != hit.LastAttempt || got.SwappedAt != hit.SwappedAt ||
		got.SwappedTo != hit.SwappedTo || got.Deliveries != hit.Deliveries || got.At != hit.At {
		t.Errorf("leave-revived moved a field it must leave: got %+v from %+v", got, hit)
	}

	// a first-attempt hit with nothing spent: still no attempt recorded
	bare := LimitHit{Error: "rate_limit", At: edgeRevNow.Unix(), Deliveries: 1}
	got = limitRescueApply(bare, rescueLeaveRevived, "", "", now)
	if got.Attempts != 0 || got.LastAttempt != 0 || got.SwappedAt != 0 || got.SwappedTo != "" {
		t.Errorf("leave-revived on a bare hit recorded rescue state: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// limitHitReactivated
// ---------------------------------------------------------------------------

func TestEdgeRevivedLimitHitReactivated(t *testing.T) {
	now := edgeRevNow.Add(time.Hour)
	full := LimitHit{
		Error: "rate_limit", Details: "You've hit your weekly limit · resets 6am (UTC)",
		At: edgeRevNow.Unix(), LastDeliveryAt: edgeRevNow.Add(time.Minute).Unix(),
		SessionID: "sess-r", AgentID: "agent-r", Deliveries: 2, Attempts: 3,
		LastAttempt: edgeRevNow.Add(2 * time.Minute).Unix(), ResetAt: edgeRevNow.Add(5 * time.Hour).Unix(),
	}
	cases := []struct {
		name   string
		hit    LimitHit
		wantOK bool
	}{
		{"empty hit", LimitHit{}, false},
		{"At zero with other fields set", LimitHit{Error: "rate_limit", Deliveries: 4, Attempts: 1}, false},
		{"At zero but handled", LimitHit{HandledAt: 55, Outcome: "rotated"}, false},
		{"already resolved", LimitHit{Error: "rate_limit", At: edgeRevNow.Unix(), HandledAt: edgeRevNow.Add(time.Minute).Unix(), Outcome: "parked-no-target"}, false},
		{"resolved by hand already", LimitHit{Error: "rate_limit", At: edgeRevNow.Unix(), HandledAt: 1, Outcome: "resumed-by-hand"}, false},
		{"unresolved, fully populated", full, true},
		{"unresolved, minimal", LimitHit{At: 1}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := limitHitReactivated(c.hit, now)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			want := c.hit
			if c.wantOK {
				want.HandledAt = now.Unix()
				want.Outcome = "reactivated"
			}
			if got != want {
				t.Errorf("limitHitReactivated = %+v, want %+v", got, want)
			}
		})
	}
}
