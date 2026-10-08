package jobs

import (
	"reflect"
	"testing"

	"kraken/internal/core"
)

// kronosremoval_contract_test.go — athena-logic's BLIND contract tests for
// the kronos removal (iris kept), artifacts/50-contract.md slice G. Written
// from the contract text alone; no implementation file in this package was
// read. Dimension: LOGIC — the standing-agent table shrinks to exactly iris,
// iris' descriptor is intact, the model constants keep their values. The
// deleted symbols are proven by compilation (nothing here may name them);
// refusals and boundaries are athena-edge's.

// contractKronosRemovalIris is the one standing agent id that survives.
const contractKronosRemovalIris = "iris"

// Contract: `standingAgentIDs()` returns exactly `[]string{"iris"}`.
func TestKronosRemovalContractStandingIDsAreIrisOnly(t *testing.T) {
	got := standingAgentIDs()
	want := []string{contractKronosRemovalIris}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("standingAgentIDs() = %#v, want %#v", got, want)
	}
}

// Contract: `standingAgents()` returns exactly one descriptor — ID, Session,
// AgentName all "iris", Model == core.IrisModel, and the four func fields
// non-nil; its ids equal standingAgentIDs() in order.
func TestKronosRemovalContractStandingTableIsIrisOnly(t *testing.T) {
	agents := standingAgents()
	if len(agents) != 1 {
		ids := make([]string, 0, len(agents))
		for _, a := range agents {
			ids = append(ids, a.ID)
		}
		t.Fatalf("standingAgents() has %d descriptors (%v), want exactly 1 (iris)", len(agents), ids)
	}
	a := agents[0]
	if a.ID != "iris" {
		t.Errorf("ID = %q, want iris", a.ID)
	}
	if a.Session != "iris" {
		t.Errorf("Session = %q, want iris", a.Session)
	}
	if a.AgentName != "iris" {
		t.Errorf("AgentName = %q, want iris", a.AgentName)
	}
	if a.Model != core.IrisModel {
		t.Errorf("Model = %q, want core.IrisModel (%q)", a.Model, core.IrisModel)
	}
	if a.Enabled == nil {
		t.Error("Enabled is nil — the daemon could never decide iris is on")
	}
	if a.RecordedSession == nil {
		t.Error("RecordedSession is nil — the transcript arm could never classify an iris stall")
	}
	if a.Find == nil {
		t.Error("Find is nil — the standing pass could never locate iris' pane")
	}
	if a.Respawn == nil {
		t.Error("Respawn is nil — the standing rescue could never resume iris")
	}

	ids := make([]string, 0, len(agents))
	for _, d := range agents {
		ids = append(ids, d.ID)
	}
	if !reflect.DeepEqual(ids, standingAgentIDs()) {
		t.Fatalf("descriptor ids %v differ from standingAgentIDs() %v (order is contract)", ids, standingAgentIDs())
	}
}

// Contract: `standingHitKey("iris") == "limit_hit:iris"` (unchanged).
func TestKronosRemovalContractIrisHitKeyUnchanged(t *testing.T) {
	if got := standingHitKey("iris"); got != "limit_hit:iris" {
		t.Fatalf("standingHitKey(iris) = %q, want limit_hit:iris", got)
	}
}

// Contract: IsStandingAgentID("iris") is true, "kronos" false, and the check
// stays case-sensitive ("Iris" false).
func TestKronosRemovalContractIsStandingAgentID(t *testing.T) {
	if !IsStandingAgentID("iris") {
		t.Error(`IsStandingAgentID("iris") = false, want true`)
	}
	if IsStandingAgentID("kronos") {
		t.Error(`IsStandingAgentID("kronos") = true, want false — kronos is no longer a standing agent`)
	}
	if IsStandingAgentID("Iris") {
		t.Error(`IsStandingAgentID("Iris") = true, want false (case-sensitive, unchanged)`)
	}
	// Every id the table iterates must be one the route admits.
	for _, id := range standingAgentIDs() {
		if !IsStandingAgentID(id) {
			t.Errorf("standingAgentIDs() carries %q but IsStandingAgentID refuses it", id)
		}
	}
}

// Contract: the four surviving model constants keep their values, and the
// zeus/default split still holds. (core.KronosModel no longer existing is
// proven by compilation, not here.)
func TestKronosRemovalContractModelConstantsUnchanged(t *testing.T) {
	for _, c := range []struct{ name, got, want string }{
		{"IrisModel", core.IrisModel, "claude-fable-5-1"},
		{"ZeusModel", core.ZeusModel, "claude-fable-5-1"},
		{"DefaultModel", core.DefaultModel, "claude-opus-5"},
		{"FableFallbackModel", core.FableFallbackModel, "claude-opus-5-5"},
	} {
		if c.got != c.want {
			t.Errorf("core.%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if core.DefaultModel == core.ZeusModel {
		t.Error("core.DefaultModel == core.ZeusModel — the lead/crew tier split collapsed")
	}
}
