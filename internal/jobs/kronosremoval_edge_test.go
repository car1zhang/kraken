package jobs

import (
	"strings"
	"testing"
)

// kronosremoval_edge_test.go — the EDGE dimension of slice G of
// artifacts/50-contract.md (kronos removal, iris kept), as it lands in
// package jobs: the standing-agent table's cardinality boundaries (exactly
// one row — not zero, not two) and IsStandingAgentID's refusals for the
// removed id, the empty id and a case variant of the surviving one.
//
// Written blind from the contract; every package-level identifier carries
// the `edge` prefix so this file coexists with athena-logic's sibling.

// edgeKronosRemovalRefusedIDs are ids the closed list must NOT contain: the
// removed agent in two spellings, the empty id, and case variants of the
// surviving one (the contract pins the check as case-sensitive, unchanged).
var edgeKronosRemovalRefusedIDs = []string{"kronos", "KRONOS", "Kronos", "", "Iris", "IRIS"}

// The table must have exactly ONE row. Zero would mean iris' rescue was
// deleted along with kronos; two would mean kronos' row survived (or a
// placeholder replaced it). Both are silent at runtime — the rescue pass
// iterates the table and simply does more or less than it should.
func TestKronosRemovalEdgeStandingAgentsHasExactlyOneRow(t *testing.T) {
	got := standingAgents()
	if len(got) != 1 {
		ids := make([]string, 0, len(got))
		for _, a := range got {
			ids = append(ids, a.ID)
		}
		t.Fatalf("standingAgents() has %d rows %v, want exactly 1 (iris)", len(got), ids)
	}
	a := got[0]
	if a.ID != "iris" {
		t.Fatalf("the one standing agent is %q, want iris", a.ID)
	}
	for field, v := range map[string]string{"ID": a.ID, "Session": a.Session, "AgentName": a.AgentName} {
		if strings.Contains(strings.ToLower(v), "kronos") {
			t.Errorf("iris descriptor field %s = %q still names kronos", field, v)
		}
	}
}

func TestKronosRemovalEdgeStandingAgentIDsHasNoKronos(t *testing.T) {
	ids := standingAgentIDs()
	if len(ids) != 1 {
		t.Fatalf("standingAgentIDs() = %v, want exactly one id", ids)
	}
	for _, id := range ids {
		if strings.EqualFold(id, "kronos") {
			t.Fatalf("standingAgentIDs() = %v still carries kronos", ids)
		}
		if id == "" {
			t.Fatalf("standingAgentIDs() = %v carries an empty id", ids)
		}
	}
}

// The table and the id list must agree at the boundary too: the table's one
// row id is the list's one id, so a refusal by one is a refusal by the other.
func TestKronosRemovalEdgeTableAndIDListAgree(t *testing.T) {
	agents := standingAgents()
	ids := standingAgentIDs()
	if len(agents) != len(ids) {
		t.Fatalf("standingAgents() has %d rows but standingAgentIDs() has %d ids (%v)", len(agents), len(ids), ids)
	}
	for i := range agents {
		if agents[i].ID != ids[i] {
			t.Errorf("row %d: descriptor id %q != id list entry %q", i, agents[i].ID, ids[i])
		}
	}
}

func TestKronosRemovalEdgeIsStandingAgentIDRefusals(t *testing.T) {
	for _, id := range edgeKronosRemovalRefusedIDs {
		if IsStandingAgentID(id) {
			t.Errorf("IsStandingAgentID(%q) = true, want false", id)
		}
	}
	if !IsStandingAgentID("iris") {
		t.Errorf(`IsStandingAgentID("iris") = false, want true — refusals must not take the survivor with them`)
	}
}

// The hit key for the removed agent is no longer a key any standing row
// owns: no descriptor's hit key may equal "limit_hit:kronos".
func TestKronosRemovalEdgeNoRowOwnsKronosHitKey(t *testing.T) {
	for _, a := range standingAgents() {
		if standingHitKey(a.ID) == "limit_hit:kronos" {
			t.Fatalf("standing row %q owns the removed key limit_hit:kronos", a.ID)
		}
	}
	if got := standingHitKey("iris"); got != "limit_hit:iris" {
		t.Fatalf(`standingHitKey("iris") = %q, want "limit_hit:iris"`, got)
	}
}
