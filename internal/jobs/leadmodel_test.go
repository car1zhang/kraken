package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/core"
)

// TestLeadModel pins the model id the agent map colours a lead by: a codex
// lead's own model, a claude lead's spawn stamp over its configured model,
// and the Fable fallback tier only for the pid the fallback record names.
func TestLeadModel(t *testing.T) {
	useTestStore(t)

	if got := LeadModel(&Job{ID: 21, Slug: "cx", Agent: "codex"}, 0); got != core.CodexZeusModel {
		t.Errorf("codex lead with no model: = %q, want %q", got, core.CodexZeusModel)
	}
	if got := LeadModel(&Job{ID: 22, Slug: "cx-pin", Agent: "codex", Model: "gpt-x"}, 0); got != "gpt-x" {
		t.Errorf("codex lead with a model: field = %q, want gpt-x", got)
	}

	j := &Job{ID: 23, Slug: "lead"}
	if got := LeadModel(j, 0); got != core.ZeusModel {
		t.Errorf("unstamped claude lead = %q, want the configured %q", got, core.ZeusModel)
	}
	if got := LeadModel(&Job{ID: 24, Slug: "pinned", Model: "claude-sonnet-5-5"}, 0); got != "claude-sonnet-5-5" {
		t.Errorf("unstamped pinned lead = %q, want its model: field", got)
	}

	// spawned on the fallback tier: the stamp wins over the configured model
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cellDir(j), "zeus-model"), []byte(fableFallbackModel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LeadModel(j, 0); got != fableFallbackModel {
		t.Errorf("stamped lead = %q, want the stamp %q", got, fableFallbackModel)
	}

	// spawned on Fable, then switched mid-run: only the switched pid reads
	// as the fallback tier
	k := &Job{ID: 25, Slug: "switched"}
	recordLastFallback(limitHitKey(k.ID), fallbackRecord{At: 1, PID: 4242})
	if got := LeadModel(k, 4242); got != fableFallbackModel {
		t.Errorf("switched process = %q, want %q", got, fableFallbackModel)
	}
	if got := LeadModel(k, 4243); got != core.ZeusModel {
		t.Errorf("a respawned process (new pid) = %q, want %q", got, core.ZeusModel)
	}
	if got := LeadModel(k, 0); got != core.ZeusModel {
		t.Errorf("no live pid = %q, want %q (the record's pid is never 0-matched)", got, core.ZeusModel)
	}
	// both: a cell spawned on Fable (stamped) whose process was then switched
	// — the switch is what the process runs on now
	if err := os.MkdirAll(cellDir(k), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cellDir(k), "zeus-model"), []byte(core.ZeusModel+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LeadModel(k, 4242); got != fableFallbackModel {
		t.Errorf("stamped and switched = %q, want the fallback %q over the stamp", got, fableFallbackModel)
	}
	// a switch that expired with no turn confirmed reads as the spawn model
	recordLastFallback(limitHitKey(k.ID), fallbackRecord{At: 1, PID: 4242, ExpiredAt: 2})
	if got := LeadModel(k, 4242); got != core.ZeusModel {
		t.Errorf("expired switch = %q, want the stamp %q", got, core.ZeusModel)
	}
	// a job with no fallback record and a live pid
	if got := LeadModel(j, 4242); got != fableFallbackModel {
		t.Errorf("stamped lead with a pid and no record = %q, want the stamp", got)
	}
}
