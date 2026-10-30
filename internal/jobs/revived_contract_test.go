package jobs

// revived_contract_test.go — athena-logic's BLIND contract tests for slice A
// of dream-2026-10-30: the limit rescue leaves a zeus that already took a
// real turn since the hit (LimitHit.LastDeliveryAt, hitEvidenceAt,
// revivedSinceHit, zeusRanSince, transcriptTurnSince, rescueLeaveRevived,
// limitHitReactivated). Authored from artifacts/50-contract.md alone.
//
// LOGIC dimension: stated invariants and happy paths. Every package-level
// identifier is prefixed `contractRevived`.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
)

var contractRevivedNow = time.Date(2026, 3, 14, 9, 30, 0, 0, time.UTC)

const contractRevivedSID = "5d0c7a11-0000-4000-8000-00000000a001"

// contractRevivedAssistant is one assistant transcript line at ts with model.
func contractRevivedAssistant(ts time.Time, model string) string {
	return `{"type":"assistant","timestamp":"` + ts.UTC().Format(time.RFC3339Nano) + `","message":{"model":"` + model + `"}}`
}

// contractRevivedWrite writes lines as a JSONL file under dir and returns its path.
func contractRevivedWrite(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := strings.Join(lines, "\n")
	if len(lines) > 0 {
		body += "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// contractRevivedPeerWithTranscript sandboxes HOME, files a transcript for
// contractRevivedSID under ~/.claude/projects/<dir>/, and returns a waiting
// claude peer whose transcript resolves to it.
func contractRevivedPeerWithTranscript(t *testing.T, lines ...string) *core.Peer {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, ".claude", "projects", "-Users-alice-cells-demo-7-wt-kraken")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	contractRevivedWrite(t, proj, contractRevivedSID+".jsonl", lines...)
	return &core.Peer{SessionID: contractRevivedSID, Status: "waiting", Agent: "claude", Role: "zeus"}
}

// --- LimitHit.LastDeliveryAt ---

func TestContractRevivedLastDeliveryAtJSON(t *testing.T) {
	raw, err := json.Marshal(LimitHit{Error: "rate_limit", At: 1_800_000_000, LastDeliveryAt: 1_800_000_420})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["last_delivery_at"]; !ok || v != float64(1_800_000_420) {
		t.Errorf("last_delivery_at = %v (present %v), want 1800000420 in %s", v, ok, raw)
	}

	zero, err := json.Marshal(LimitHit{Error: "rate_limit", At: 1_800_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(zero), "last_delivery_at") {
		t.Errorf("a zero LastDeliveryAt is serialized (omitempty): %s", zero)
	}

	var old LimitHit
	if err := json.Unmarshal([]byte(`{"error":"rate_limit","at":1800000000}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.LastDeliveryAt != 0 {
		t.Errorf("a record without the key decoded LastDeliveryAt = %d, want 0", old.LastDeliveryAt)
	}
}

func TestContractRevivedMergeStampsLastDelivery(t *testing.T) {
	t.Run("fresh record", func(t *testing.T) {
		got := mergeLimitHitDelivery(LimitHit{}, "rate_limit", "You've hit your session limit · resets 2pm (America/New_York)", "sess-fresh", "", contractRevivedNow)
		if got.LastDeliveryAt != contractRevivedNow.Unix() {
			t.Errorf("fresh LastDeliveryAt = %d, want %d", got.LastDeliveryAt, contractRevivedNow.Unix())
		}
	})
	t.Run("fold onto unresolved same-error record", func(t *testing.T) {
		first := contractRevivedNow.Add(-20 * time.Minute)
		cur := LimitHit{Error: "rate_limit", Details: "You've hit your session limit", At: first.Unix(), LastDeliveryAt: first.Unix(), Deliveries: 2, SessionID: "sess-zeus"}
		got := mergeLimitHitDelivery(cur, "rate_limit", "You've hit your session limit", "sess-zeus", "", contractRevivedNow)
		if got.LastDeliveryAt != contractRevivedNow.Unix() {
			t.Errorf("folded LastDeliveryAt = %d, want %d", got.LastDeliveryAt, contractRevivedNow.Unix())
		}
	})
}

// --- hitEvidenceAt ---

func TestContractRevivedHitEvidenceAt(t *testing.T) {
	cases := []struct {
		name string
		hit  LimitHit
		want int64
	}{
		{"both zero", LimitHit{}, 0},
		{"only At", LimitHit{At: 1_800_000_100}, 1_800_000_100},
		{"only LastDeliveryAt", LimitHit{LastDeliveryAt: 1_800_000_200}, 1_800_000_200},
		{"LastDeliveryAt later", LimitHit{At: 1_800_000_100, LastDeliveryAt: 1_800_000_900}, 1_800_000_900},
		{"At later", LimitHit{At: 1_800_000_900, LastDeliveryAt: 1_800_000_100}, 1_800_000_900},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hitEvidenceAt(c.hit); got != c.want {
				t.Errorf("hitEvidenceAt(%+v) = %d, want %d", c.hit, got, c.want)
			}
		})
	}
}

// --- transcriptTurnSince ---

func TestContractRevivedTranscriptTurnSince(t *testing.T) {
	since := contractRevivedNow
	after := since.Add(3 * time.Minute)
	before := since.Add(-3 * time.Minute)
	cases := []struct {
		name  string
		lines []string
		want  bool
	}{
		{"real assistant turn after since", []string{contractRevivedAssistant(after, "claude-opus-5")}, true},
		{"real assistant turn with another invented model", []string{contractRevivedAssistant(after, "claude-test-model-9")}, true},
		{"only turns before since", []string{contractRevivedAssistant(before, "claude-opus-5")}, false},
		{"synthetic assistant record after since", []string{contractRevivedAssistant(after, "<synthetic>")}, false},
		{"empty model after since", []string{contractRevivedAssistant(after, "")}, false},
		{"user record after since", []string{`{"type":"user","timestamp":"` + after.Format(time.RFC3339Nano) + `","message":{"model":"claude-opus-5"}}`}, false},
		{"assistant without timestamp", []string{`{"type":"assistant","message":{"model":"claude-opus-5"}}`}, false},
		{"non-JSON line mentioning assistant", []string{`not json "type":"assistant" ` + after.Format(time.RFC3339Nano)}, false},
		{"real turn after synthetic and junk lines", []string{
			contractRevivedAssistant(before, "claude-opus-5"),
			"garbage line",
			contractRevivedAssistant(after, "<synthetic>"),
			contractRevivedAssistant(after.Add(time.Second), "claude-opus-5"),
		}, true},
		{"empty file", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := contractRevivedWrite(t, t.TempDir(), "t.jsonl", c.lines...)
			if got := transcriptTurnSince(p, since.Unix()); got != c.want {
				t.Errorf("transcriptTurnSince = %v, want %v", got, c.want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if transcriptTurnSince(filepath.Join(t.TempDir(), "absent.jsonl"), since.Unix()) {
			t.Error("a missing file answered true")
		}
	})
}

func TestContractRevivedTranscriptTailIsAtLeastOneMiB(t *testing.T) {
	if transcriptTurnTail < 1<<20 {
		t.Errorf("transcriptTurnTail = %d, want >= 1 MiB", transcriptTurnTail)
	}
}

// --- zeusRanSince ---

func TestContractRevivedZeusRanSince(t *testing.T) {
	since := contractRevivedNow.Unix()
	t.Run("nil peer", func(t *testing.T) {
		if zeusRanSince(nil, since) {
			t.Error("nil peer answered true")
		}
	})
	t.Run("since zero", func(t *testing.T) {
		if zeusRanSince(&core.Peer{Status: "running", Agent: "claude"}, 0) {
			t.Error("since == 0 answered true")
		}
	})
	// Amended at the join (review round 1, contract defect): the pane's
	// status is not evidence of a turn for a claude pane — a running peer
	// with no transcript to read answers false.
	t.Run("running peer without a transcript", func(t *testing.T) {
		if zeusRanSince(&core.Peer{Status: "running", Agent: "claude"}, since) {
			t.Error("a running peer with no transcript answered true")
		}
	})
	t.Run("waiting peer, unreadable transcript", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		if zeusRanSince(&core.Peer{Status: "waiting", Agent: "claude"}, since) {
			t.Error("a peer with no resolvable transcript answered true")
		}
	})
	t.Run("waiting peer, real turn after since", func(t *testing.T) {
		p := contractRevivedPeerWithTranscript(t, contractRevivedAssistant(contractRevivedNow.Add(5*time.Minute), "claude-opus-5"))
		if !zeusRanSince(p, since) {
			t.Error("a real turn after the hit answered false")
		}
	})
	t.Run("waiting peer, only turns before since", func(t *testing.T) {
		p := contractRevivedPeerWithTranscript(t, contractRevivedAssistant(contractRevivedNow.Add(-5*time.Minute), "claude-opus-5"))
		if zeusRanSince(p, since) {
			t.Error("turns only before the hit answered true")
		}
	})
	t.Run("waiting peer, only a synthetic record after since", func(t *testing.T) {
		p := contractRevivedPeerWithTranscript(t, contractRevivedAssistant(contractRevivedNow.Add(5*time.Minute), "<synthetic>"))
		if zeusRanSince(p, since) {
			t.Error("a synthetic (limit banner) record answered true")
		}
	})
}

// --- revivedSinceHit ---

func TestContractRevivedSinceHit(t *testing.T) {
	hit := LimitHit{Error: "rate_limit", At: contractRevivedNow.Unix(), LastDeliveryAt: contractRevivedNow.Add(2 * time.Minute).Unix()}

	t.Run("nil zeus", func(t *testing.T) {
		if revivedSinceHit(hit, "claude", nil) {
			t.Error("nil zeus answered true")
		}
	})
	// Amended at the join (review round 1, contract defect): a running
	// codex zeus is revived; a running claude zeus is judged by its
	// transcript alone, so with none to read it answers false.
	t.Run("running codex", func(t *testing.T) {
		if !revivedSinceHit(hit, "codex", &core.Peer{Status: "running", Agent: "codex"}) {
			t.Error("a running codex zeus answered false")
		}
	})
	t.Run("running claude without a transcript", func(t *testing.T) {
		if revivedSinceHit(hit, "claude", &core.Peer{Status: "running", Agent: "claude"}) {
			t.Error("a running claude zeus with no transcript answered true")
		}
	})
	t.Run("waiting codex", func(t *testing.T) {
		if revivedSinceHit(hit, "codex", &core.Peer{Status: "waiting", Agent: "codex"}) {
			t.Error("a waiting codex zeus answered true")
		}
	})
	t.Run("waiting claude, real turn after the latest delivery", func(t *testing.T) {
		p := contractRevivedPeerWithTranscript(t, contractRevivedAssistant(contractRevivedNow.Add(4*time.Minute), "claude-opus-5"))
		if !revivedSinceHit(hit, "claude", p) {
			t.Error("a real turn after the latest delivery answered false")
		}
	})
	t.Run("waiting claude, turn after At but before the latest delivery", func(t *testing.T) {
		// evidence is max(At, LastDeliveryAt): a turn between them predates
		// the last limit delivery and is not a revival
		p := contractRevivedPeerWithTranscript(t, contractRevivedAssistant(contractRevivedNow.Add(1*time.Minute), "claude-opus-5"))
		if revivedSinceHit(hit, "claude", p) {
			t.Error("a turn older than the latest delivery answered true")
		}
	})
	t.Run("independent of SwappedAt and ResetAt", func(t *testing.T) {
		p := contractRevivedPeerWithTranscript(t, contractRevivedAssistant(contractRevivedNow.Add(4*time.Minute), "claude-opus-5"))
		arms := map[string]LimitHit{
			"plain": hit,
			"rebind": func() LimitHit {
				h := hit
				h.SwappedAt = contractRevivedNow.Add(time.Minute).Unix()
				h.SwappedTo = "slot-b"
				return h
			}(),
			"reset": func() LimitHit { h := hit; h.ResetAt = contractRevivedNow.Add(time.Hour).Unix(); return h }(),
			"park": func() LimitHit {
				h := hit
				h.Attempts = 3
				h.Outcome = "parked-until-reset"
				h.ResetAt = contractRevivedNow.Add(time.Hour).Unix()
				return h
			}(),
		}
		for name, h := range arms {
			if !revivedSinceHit(h, "claude", p) {
				t.Errorf("%s arm: revived zeus answered false", name)
			}
		}
	})
}

// --- rescueLeaveRevived ---

func TestContractRevivedLeaveRevivedApply(t *testing.T) {
	if string(rescueLeaveRevived) != "leave-revived" {
		t.Fatalf("rescueLeaveRevived = %q, want %q", rescueLeaveRevived, "leave-revived")
	}
	base := LimitHit{
		Error:          "rate_limit",
		Details:        "You've hit your session limit · resets 2pm (America/New_York)",
		At:             contractRevivedNow.Add(-40 * time.Minute).Unix(),
		LastDeliveryAt: contractRevivedNow.Add(-30 * time.Minute).Unix(),
		SessionID:      "sess-zeus",
		Deliveries:     5,
		Attempts:       1,
		LastAttempt:    contractRevivedNow.Add(-25 * time.Minute).Unix(),
		SwappedAt:      contractRevivedNow.Add(-25 * time.Minute).Unix(),
		SwappedTo:      "slot-b",
	}
	got := limitRescueApply(base, rescueLeaveRevived, "", "", contractRevivedNow)
	if got.HandledAt != contractRevivedNow.Unix() {
		t.Errorf("HandledAt = %d, want %d", got.HandledAt, contractRevivedNow.Unix())
	}
	if got.Outcome != "resumed-by-hand" {
		t.Errorf("Outcome = %q, want %q", got.Outcome, "resumed-by-hand")
	}
	if got.Attempts != base.Attempts || got.LastAttempt != base.LastAttempt || got.SwappedAt != base.SwappedAt ||
		got.SwappedTo != base.SwappedTo || got.Deliveries != base.Deliveries || got.At != base.At {
		t.Errorf("leave-revived changed a preserved field:\n got  %+v\n base %+v", got, base)
	}
}

// --- limitHitReactivated ---

func TestContractRevivedLimitHitReactivated(t *testing.T) {
	open := LimitHit{
		Error:       "rate_limit",
		Details:     "You've hit your weekly limit · resets 6am (America/New_York)",
		At:          contractRevivedNow.Add(-2 * time.Hour).Unix(),
		SessionID:   "sess-zeus",
		Deliveries:  3,
		Attempts:    3,
		LastAttempt: contractRevivedNow.Add(-time.Hour).Unix(),
		Outcome:     "parked-until-reset",
		ResetAt:     contractRevivedNow.Add(5 * time.Hour).Unix(),
	}
	t.Run("unresolved hit resolves", func(t *testing.T) {
		got, ok := limitHitReactivated(open, contractRevivedNow)
		if !ok {
			t.Fatal("unresolved hit answered false")
		}
		want := open
		want.HandledAt = contractRevivedNow.Unix()
		want.Outcome = "reactivated"
		if got != want {
			t.Errorf("got  %+v\nwant %+v", got, want)
		}
	})
	t.Run("empty hit unchanged", func(t *testing.T) {
		got, ok := limitHitReactivated(LimitHit{}, contractRevivedNow)
		if ok || got != (LimitHit{}) {
			t.Errorf("empty hit = %+v, %v; want unchanged, false", got, ok)
		}
	})
	t.Run("resolved hit unchanged", func(t *testing.T) {
		done := open
		done.HandledAt = contractRevivedNow.Add(-10 * time.Minute).Unix()
		done.Outcome = "rotated"
		got, ok := limitHitReactivated(done, contractRevivedNow)
		if ok || got != done {
			t.Errorf("resolved hit = %+v, %v; want unchanged, false", got, ok)
		}
	})
}
