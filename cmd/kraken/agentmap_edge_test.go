package main

import (
	"testing"

	"kraken/internal/core"
)

// agentmap_edge_test.go — athena-edge's blind suite for cellMessageTarget
// (50-contract.md G6): every way the target is refused, and the trim on the
// sending cell's slug.

func TestAgentMapEdgeCellMessageTargetRefusals(t *testing.T) {
	zeus := &core.Peer{Name: "zeus-other-cell", Depth: 0}
	for _, tc := range []struct {
		name string
		self string
		p    *core.Peer
	}{
		{"empty self", "", zeus},
		{"blank self", "   \t\n", zeus},
		{"nil peer", "my-cell", nil},
		{"nested agent", "my-cell", &core.Peer{Name: "zeus-other-cell", Depth: 1}},
		{"deeper nested agent", "my-cell", &core.Peer{Name: "zeus-other-cell", Depth: 3}},
		{"teammate", "my-cell", &core.Peer{Name: "heph-1", Depth: 0}},
		{"side thread", "my-cell", &core.Peer{Name: "thread-other-cell-3", Depth: 0}},
		{"bare zeus", "my-cell", &core.Peer{Name: "zeus", Depth: 0}},
		{"zeus without dash", "my-cell", &core.Peer{Name: "zeusother", Depth: 0}},
		{"capitalised", "my-cell", &core.Peer{Name: "Zeus-other-cell", Depth: 0}},
		{"zeus- not at start", "my-cell", &core.Peer{Name: "x-zeus-other", Depth: 0}},
		{"unnamed", "my-cell", &core.Peer{Depth: 0}},
		{"shell pane", "my-cell", &core.Peer{Name: "", Role: "shell", Depth: 0}},
	} {
		if to, ok := cellMessageTarget(tc.self, tc.p); ok {
			t.Errorf("%s: (%q, true), want ok false", tc.name, to)
		}
	}
}

// A padded self slug is trimmed and still counts; `to` is the peer's name,
// verbatim.
func TestAgentMapEdgeCellMessageTargetTrimmedSelf(t *testing.T) {
	p := &core.Peer{Name: "zeus-other-cell", Depth: 0}
	to, ok := cellMessageTarget("  my-cell \n", p)
	if !ok || to != "zeus-other-cell" {
		t.Fatalf("got (%q, %v), want (zeus-other-cell, true)", to, ok)
	}
	// the shortest name that still starts with zeus- qualifies
	to, ok = cellMessageTarget("my-cell", &core.Peer{Name: "zeus-x", Depth: 0})
	if !ok || to != "zeus-x" {
		t.Fatalf("got (%q, %v), want (zeus-x, true)", to, ok)
	}
}
