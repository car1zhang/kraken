package main

import (
	"testing"

	"kraken/internal/core"
)

// agentmap_contract_test.go — athena-logic's blind tests for G6's pure half:
// cellMessageTarget decides whether a `kraken s` was a cell-to-cell send.

// G6: ok only for a trimmed non-empty self slug and a depth-0 peer named
// zeus-…; `to` is then the peer's name.
func TestAgentMapContractCellMessageTarget(t *testing.T) {
	zeus := &core.Peer{Name: "zeus-other-cell", Depth: 0}
	if to, ok := cellMessageTarget("my-cell", zeus); !ok || to != "zeus-other-cell" {
		t.Fatalf("cellMessageTarget(my-cell, zeus-other-cell) = (%q, %v), want (zeus-other-cell, true)", to, ok)
	}
	if to, ok := cellMessageTarget("  my-cell  ", zeus); !ok || to != "zeus-other-cell" {
		t.Fatalf("padded self slug = (%q, %v), want (zeus-other-cell, true)", to, ok)
	}

	cases := []struct {
		name string
		self string
		peer *core.Peer
	}{
		{"empty self", "", zeus},
		{"blank self", "   ", zeus},
		{"nil peer", "my-cell", nil},
		{"nested zeus", "my-cell", &core.Peer{Name: "zeus-other-cell", Depth: 1}},
		{"teammate", "my-cell", &core.Peer{Name: "heph-1", Depth: 0}},
		{"thread", "my-cell", &core.Peer{Name: "thread-other-cell-t1", Depth: 0}},
		{"unnamed", "my-cell", &core.Peer{Depth: 0}},
	}
	for _, c := range cases {
		if _, ok := cellMessageTarget(c.self, c.peer); ok {
			t.Errorf("%s: ok = true, want false", c.name)
		}
	}
}
