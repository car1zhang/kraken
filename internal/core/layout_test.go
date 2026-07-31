package core

import (
	"math"
	"testing"

	"kraken/internal/store"
)

// ringGraph builds n nodes wired into a ring plus a hub every 10th node, which
// gives the layout something with real structure (a cycle and a few
// high-degree nodes) rather than a uniform mesh.
func ringGraph(n int) ([]store.GraphNode, []store.GraphEdge) {
	nodes := make([]store.GraphNode, n)
	for i := range nodes {
		nodes[i] = store.GraphNode{ID: int64(i + 1), Name: "n"}
	}
	var edges []store.GraphEdge
	id := int64(1)
	for i := 0; i < n; i++ {
		edges = append(edges, store.GraphEdge{ID: id, SrcID: int64(i + 1), DstID: int64((i+1)%n + 1)})
		id++
		if i%10 == 0 && i+5 < n {
			edges = append(edges, store.GraphEdge{ID: id, SrcID: int64(i + 1), DstID: int64(i + 6)})
			id++
		}
	}
	return nodes, edges
}

func layoutPosByID(p []store.LayoutPos) map[int64]store.LayoutPos {
	m := make(map[int64]store.LayoutPos, len(p))
	for _, q := range p {
		m[q.NodeID] = q
	}
	return m
}

func layoutExtent(p []store.LayoutPos) float64 {
	if len(p) == 0 {
		return 0
	}
	minX, maxX := p[0].X, p[0].X
	minY, maxY := p[0].Y, p[0].Y
	for _, q := range p {
		minX, maxX = math.Min(minX, q.X), math.Max(maxX, q.X)
		minY, maxY = math.Min(minY, q.Y), math.Max(maxY, q.Y)
	}
	return math.Max(maxX-minX, maxY-minY)
}

// TestComputeLayoutDeterministic is the property everything else rests on:
// both clients must draw the same picture, and a cached layout must be
// reproducible. No math/rand, and no map iteration in any force loop.
func TestComputeLayoutDeterministic(t *testing.T) {
	nodes, edges := ringGraph(60)
	a := ComputeLayout(nodes, edges, nil, 60)
	for i := 0; i < 3; i++ {
		b := ComputeLayout(nodes, edges, nil, 60)
		if len(a) != len(b) {
			t.Fatalf("run %d returned %d positions, want %d", i, len(b), len(a))
		}
		for j := range a {
			if a[j] != b[j] {
				t.Fatalf("run %d node %d: %+v vs %+v — layout must be bit-identical for fixed input", i, a[j].NodeID, b[j], a[j])
			}
		}
	}
}

// TestComputeLayoutSeedIsHashNotOrder: the same node id gets the same starting
// place regardless of where it sits in the input slice, so a graph re-read in
// a different order does not draw a different picture.
func TestComputeLayoutSeedIsHashNotOrder(t *testing.T) {
	nodes := []store.GraphNode{{ID: 7}, {ID: 9}, {ID: 3}}
	fwd := ComputeLayout(nodes, nil, nil, 0)
	rev := ComputeLayout([]store.GraphNode{{ID: 3}, {ID: 9}, {ID: 7}}, nil, nil, 0)
	f, r := layoutPosByID(fwd), layoutPosByID(rev)
	for id, p := range f {
		q := r[id]
		if math.Abs(p.X-q.X) > 1e-9 || math.Abs(p.Y-q.Y) > 1e-9 {
			t.Fatalf("node %d landed at (%v,%v) in one order and (%v,%v) in the other", id, p.X, p.Y, q.X, q.Y)
		}
	}
}

// TestComputeLayoutIncrementalBarelyMoves is the incrementality contract: a
// settled map plus one new node must still be the same map. A layout that
// reshuffled on every nightly ingest would destroy the remembered shape the
// viewer is navigated by.
func TestComputeLayoutIncrementalBarelyMoves(t *testing.T) {
	nodes, edges := ringGraph(60)
	settled := ComputeLayout(nodes, edges, nil, 120)
	span := layoutExtent(settled)
	if span <= 0 {
		t.Fatal("settled layout has no extent")
	}

	// The same settled map, translated far off the origin. Both cases must
	// hold, and the shifted one is the discriminating half: it is only
	// satisfiable by a run that actually starts from prev. A from-scratch run
	// of the same graph puts node 1 near the origin, some 7000 units from
	// where the shifted prev left it — two orders of magnitude past the
	// threshold below, which is what keeps this assertion from being
	// trivially true.
	shifted := make([]store.LayoutPos, len(settled))
	for i, p := range settled {
		shifted[i] = store.LayoutPos{NodeID: p.NodeID, X: p.X + 5000, Y: p.Y - 5000}
	}

	// One new node, attached to the ring.
	grown := append(append([]store.GraphNode{}, nodes...), store.GraphNode{ID: 999})
	grownEdges := append(append([]store.GraphEdge{}, edges...),
		store.GraphEdge{ID: 9999, SrcID: 999, DstID: 5})

	// Threshold is a fraction of the layout's own extent rather than an
	// absolute distance, so it stays meaningful if the force constants are
	// ever retuned. This fixture measures ~1.9% of extent in both cases; the
	// contract is "barely moves", not "does not move at all" — prevMobility
	// is deliberately non-zero so a genuinely changed topology can relax.
	limit := 0.03 * span

	for _, tc := range []struct {
		name string
		prev []store.LayoutPos
	}{{"in place", settled}, {"translated off-origin", shifted}} {
		next := ComputeLayout(grown, grownEdges, tc.prev, 120)
		before, after := layoutPosByID(tc.prev), layoutPosByID(next)

		var worst float64
		var worstID int64
		for id, b := range before {
			a, ok := after[id]
			if !ok {
				t.Fatalf("%s: node %d vanished from the incremental layout", tc.name, id)
			}
			if d := math.Hypot(a.X-b.X, a.Y-b.Y); d > worst {
				worst, worstID = d, id
			}
		}
		if worst > limit {
			t.Fatalf("%s: existing node %d moved %.2f on an incremental run (%.1f%% of the %.0f extent, limit %.2f)",
				tc.name, worstID, worst, 100*worst/span, span, limit)
		}
		if _, ok := after[999]; !ok {
			t.Fatalf("%s: the new node got no position", tc.name)
		}
	}
}

// TestComputeLayoutFreshRunIgnoresStalePrev: positions for nodes no longer in
// the graph are dropped rather than carried into the output.
func TestComputeLayoutFreshRunIgnoresStalePrev(t *testing.T) {
	nodes := []store.GraphNode{{ID: 1}, {ID: 2}}
	prev := []store.LayoutPos{{NodeID: 1, X: 5, Y: 5}, {NodeID: 42, X: 1000, Y: 1000}}
	got := ComputeLayout(nodes, nil, prev, 10)
	if len(got) != 2 {
		t.Fatalf("got %d positions, want one per node", len(got))
	}
	for _, p := range got {
		if p.NodeID == 42 {
			t.Fatal("a stale prev row produced a position for a node that is gone")
		}
	}
}

// TestComputeLayoutRevIsZero: Rev is the caller's stamp. ComputeLayout never
// reads the store, so it cannot know the revision it was computed at.
func TestComputeLayoutRevIsZero(t *testing.T) {
	nodes, edges := ringGraph(20)
	prev := []store.LayoutPos{{NodeID: 1, X: 1, Y: 1, Rev: 77}}
	for _, p := range ComputeLayout(nodes, edges, prev, 10) {
		if p.Rev != 0 {
			t.Fatalf("node %d came back with Rev=%d, want 0 — the layout pass stamps it", p.NodeID, p.Rev)
		}
	}
}

func TestComputeLayoutEmpty(t *testing.T) {
	if got := ComputeLayout(nil, nil, nil, 0); got != nil {
		t.Fatalf("empty graph = %v, want nil", got)
	}
}

// TestComputeLayoutSpreadsAndStaysFinite: the simulation must not collapse
// every node onto the origin, blow up to infinity, or produce a NaN from a
// divide-by-zero between two coincident nodes.
func TestComputeLayoutSpreadsAndStaysFinite(t *testing.T) {
	nodes, edges := ringGraph(80)
	got := ComputeLayout(nodes, edges, nil, 100)
	distinct := map[[2]float64]bool{}
	for _, p := range got {
		if math.IsNaN(p.X) || math.IsNaN(p.Y) || math.IsInf(p.X, 0) || math.IsInf(p.Y, 0) {
			t.Fatalf("node %d at (%v,%v)", p.NodeID, p.X, p.Y)
		}
		distinct[[2]float64{p.X, p.Y}] = true
	}
	if len(distinct) != len(got) {
		t.Fatalf("%d nodes occupy only %d distinct positions", len(got), len(distinct))
	}
	if layoutExtent(got) < 1 {
		t.Fatalf("layout collapsed: extent %v", layoutExtent(got))
	}
}

// TestComputeLayoutCoincidentNodes: every node handed the SAME prior position
// is the degenerate case Barnes-Hut subdivision cannot represent — it is what
// bhMaxDepth and the deterministic nudge in repulsion exist for. It must
// terminate and separate them rather than divide by zero.
func TestComputeLayoutCoincidentNodes(t *testing.T) {
	var nodes []store.GraphNode
	var prev []store.LayoutPos
	for i := 1; i <= 30; i++ {
		nodes = append(nodes, store.GraphNode{ID: int64(i)})
		prev = append(prev, store.LayoutPos{NodeID: int64(i), X: 3, Y: 4})
	}
	got := ComputeLayout(nodes, nil, prev, 40)
	for _, p := range got {
		if math.IsNaN(p.X) || math.IsNaN(p.Y) || math.IsInf(p.X, 0) || math.IsInf(p.Y, 0) {
			t.Fatalf("node %d at (%v,%v) from a fully coincident start", p.NodeID, p.X, p.Y)
		}
	}
}

// TestComputeLayoutScales exercises the Barnes-Hut path at a size all-pairs
// repulsion could not reach: 20k nodes is 400 million pairs per iteration
// quadratically, and this test does five iterations of it. It asserts only
// that the answer is finite and complete — its real job is to fail by
// timeout if the O(n log n) approximation is ever replaced by a nested loop.
func TestComputeLayoutScales(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	nodes, edges := ringGraph(20000)
	got := ComputeLayout(nodes, edges, nil, 5)
	if len(got) != len(nodes) {
		t.Fatalf("got %d positions for %d nodes", len(got), len(nodes))
	}
	for _, p := range got {
		if math.IsNaN(p.X) || math.IsNaN(p.Y) {
			t.Fatalf("node %d is NaN at scale", p.NodeID)
		}
	}
}
