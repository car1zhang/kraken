package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"kraken/internal/core"
)

// The reset arm's kill is owed only to a pane still latched on the limit. A
// transcript holding a real assistant record after the reset means the pane
// already ran, and the record must be a real one: the banner a latched pane
// writes on a keystroke is an assistant record too, stamped "<synthetic>".
func TestTranscriptTurnSinceAcceptsOnlyRealTurnsAfterTheCut(t *testing.T) {
	cut := time.Date(2026, 10, 8, 20, 30, 0, 0, time.UTC)
	rec := func(kind, model string, at time.Time) string {
		return `{"type":"` + kind + `","timestamp":"` + at.Format(time.RFC3339Nano) + `","message":{"model":"` + model + `","role":"assistant"}}` + "\n"
	}
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "s.jsonl")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := cut.Add(-10 * time.Minute)
	after := cut.Add(3 * time.Minute)
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"real turn after the cut", rec("assistant", "claude-fable-5-1", before) + rec("assistant", "claude-fable-5-1", after), true},
		{"only a banner after the cut", rec("assistant", "claude-fable-5-1", before) + rec("assistant", "<synthetic>", after), false},
		{"real turn before the cut only", rec("assistant", "claude-fable-5-1", before) + rec("user", "", after), false},
		{"user record after the cut is not a turn", rec("user", "", after), false},
		{"empty file", "", false},
		{"garbage line then a real turn", "not json\n" + rec("assistant", "claude-opus-5-5", after), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := transcriptTurnSince(write(t, c.body), cut.Unix()); got != c.want {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
	if transcriptTurnSince(filepath.Join(t.TempDir(), "missing.jsonl"), cut.Unix()) {
		t.Fatal("a missing transcript must read as no turn")
	}
}

// The tail bound must not hide a real turn at the end of a long file (the
// partial first line a tail read starts inside is user filler here, so this
// pins the bound, not the skip).
func TestTranscriptTurnSinceReadsOnlyTheTail(t *testing.T) {
	cut := time.Date(2026, 10, 8, 20, 30, 0, 0, time.UTC)
	after := cut.Add(time.Minute).Format(time.RFC3339Nano)
	filler := `{"type":"user","timestamp":"` + cut.Add(-time.Hour).Format(time.RFC3339Nano) + `","message":{"role":"user","content":"` + string(make([]byte, 4000)) + `"}}` + "\n"
	var body string
	for len(body) < transcriptTurnTail+100000 {
		body += filler
	}
	body += `{"type":"assistant","timestamp":"` + after + `","message":{"model":"claude-fable-5-1"}}` + "\n"
	p := filepath.Join(t.TempDir(), "long.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if !transcriptTurnSince(p, cut.Unix()) {
		t.Fatal("the real turn at the end of a long file must be found")
	}
}

// resetArmLeavesRevived is the pass's one call on the reset arm, so these are
// the wiring obligations: the rebind arm (SwappedAt set) never consults the
// pane, a running pane is left alone whatever its agent, a codex zeus never
// takes the file half, and a claude zeus whose transcript under
// ~/.claude/projects holds a real post-reset record is left alone while one
// holding only the banner is not.
func TestResetArmLeavesRevivedWiring(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cut := time.Date(2026, 10, 8, 20, 30, 0, 0, time.UTC)
	rec := func(model string, at time.Time) string {
		return `{"type":"assistant","timestamp":"` + at.Format(time.RFC3339Nano) + `","message":{"model":"` + model + `","role":"assistant"}}` + "\n"
	}
	dir := filepath.Join(home, ".claude", "projects", "-tmp-cell-wt-demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(sid, body string) *core.Peer {
		if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return &core.Peer{SessionID: sid, Status: "idle", Agent: "claude"}
	}
	revived := write("11111111-aaaa-bbbb-cccc-000000000001", rec("claude-fable-5-1", cut.Add(-time.Hour))+rec("<synthetic>", cut.Add(-50*time.Minute))+rec("claude-fable-5-1", cut.Add(4*time.Minute)))
	latched := write("11111111-aaaa-bbbb-cccc-000000000002", rec("claude-fable-5-1", cut.Add(-time.Hour))+rec("<synthetic>", cut.Add(-50*time.Minute))+rec("<synthetic>", cut.Add(5*time.Minute)))
	reset := LimitHit{Error: "rate_limit", Attempts: 3, ResetAt: cut.Unix(), Outcome: "parked-until-reset"}
	if !resetArmLeavesRevived(reset, "claude", revived) {
		t.Fatal("a claude zeus with a real post-reset record must be left alone")
	}
	if resetArmLeavesRevived(reset, "claude", latched) {
		t.Fatal("a zeus whose only post-reset records are banners is still latched and must be resumed")
	}
	if resetArmLeavesRevived(reset, "claude", nil) {
		t.Fatal("no zeus pane: the no-agents path, never a leave")
	}
	rebound := reset
	rebound.SwappedAt = cut.Add(-time.Minute).Unix()
	if resetArmLeavesRevived(rebound, "claude", &core.Peer{Status: "running", Agent: "claude"}) {
		t.Fatal("the rebind arm never consults the pane: a running zeus on the old account is still resumed")
	}
	if !resetArmLeavesRevived(reset, "codex", &core.Peer{Status: "running", Agent: "codex"}) {
		t.Fatal("a running codex zeus is left alone: the status half holds for every agent")
	}
	codexIdle := write("11111111-aaaa-bbbb-cccc-000000000003", rec("gpt-6-astra", cut.Add(4*time.Minute)))
	codexIdle.Agent = "codex"
	if resetArmLeavesRevived(reset, "codex", codexIdle) {
		t.Fatal("an idle codex zeus never takes the file half: a rollout carries no assistant records, so this fixture must not be read as one")
	}
}

func TestZeusRanSinceTrustsARunningPaneAndNothingElseWithoutATranscript(t *testing.T) {
	if zeusRanSince(nil, 1) {
		t.Fatal("nil peer")
	}
	if zeusRanSince(&core.Peer{Status: "running"}, 0) {
		t.Fatal("a zero cut means no reset is known — never a turn since")
	}
	if !zeusRanSince(&core.Peer{Status: "running"}, 1) {
		t.Fatal("a running pane has run")
	}
	if zeusRanSince(&core.Peer{Status: "question"}, 1) {
		t.Fatal("a pane parked on a dialog has not run")
	}
	if zeusRanSince(&core.Peer{Status: "idle", Role: "shell"}, 1) {
		t.Fatal("an idle pane with no transcript has no evidence of a turn")
	}
}

// rescueLeaveRevived resolves the hit as resumed by hand, touches no counter
// and stamps no swap — nothing was attempted.
func TestContractLimitApplyLeaveRevivedResolvesWithoutACounter(t *testing.T) {
	now := time.Unix(1_791_490_000, 0)
	hit := LimitHit{At: now.Add(-25 * time.Minute).Unix(), Error: "rate_limit", Attempts: 3, ResetAt: now.Add(-11 * time.Minute).Unix(), Outcome: "parked-until-reset"}
	got := limitRescueApply(hit, rescueLeaveRevived, "", "", now)
	if got.HandledAt != now.Unix() || got.Outcome != "resumed-by-hand" {
		t.Fatalf("resolved wrong: %+v", got)
	}
	if got.Attempts != 3 || got.LastAttempt != hit.LastAttempt || got.SwappedAt != 0 {
		t.Fatalf("counters or swap touched: %+v", got)
	}
}
