package core

import (
	"math"

	"kraken/internal/store"
)

// layout.go — ForceAtlas2 with a Barnes-Hut quadtree, computed server-side.
//
// Server-side layout is load-bearing rather than an optimisation (the spec's
// component 8): both clients must draw the SAME picture, because a graph is
// navigated by remembered shape, and a phone cannot run a force simulation
// over tens of thousands of nodes at all. Computing it once in Go and caching
// it per graph revision reduces both clients to viewport, culling and labels.
//
// Two properties are non-negotiable and both are pinned by tests in
// layout_test.go:
//
//   - DETERMINISM. Identical inputs give bit-identical outputs. There is no
//     math/rand anywhere in this file; a new node's starting position comes
//     from a hash of its own id, so the same node lands in the same place on
//     every machine and every run. Every loop iterates a SLICE, never a map,
//     for the same reason.
//   - INCREMENTALITY. A node that already has a cached position starts there
//     and is damped (see prevMobility), so adding one node to a settled graph
//     moves the existing map by a little rather than reshuffling it. A layout
//     that reshuffled on every ingest would destroy the remembered shape the
//     whole design is built around.
//
// Barnes-Hut is what makes the scale in the spec (50k nodes) reachable:
// all-pairs repulsion is O(n^2) per iteration — 2.5 billion pair
// computations at 50k, per iteration, times ~100 iterations — while the
// quadtree approximation is O(n log n).

const (
	// bhTheta is the Barnes-Hut opening angle. A cell whose width divided by
	// its distance is below bhTheta is treated as a single body at its centre
	// of mass. Higher is faster and coarser; the spec fixes 0.9, which is
	// the value ForceAtlas2's own implementations default to.
	bhTheta = 0.9

	// defaultLayoutIters is what iterations<=0 means.
	defaultLayoutIters = 100

	// kRepulsion and kAttraction are the force coefficients. FA2 with LinLog
	// OFF: repulsion between two bodies is k_r*m_i*m_j/d and attraction along
	// an edge is linear in d. Mass is degree+1, which is what makes hubs push
	// harder and gives FA2 its characteristic readable hub-and-spoke shape.
	kRepulsion  = 1.0
	kAttraction = 0.02

	// kGravity pulls every node toward the origin so disconnected components
	// — of which this graph will have many, since facts about unrelated
	// subsystems never touch — drift apart instead of flying apart. Weak
	// enough that it does not visibly compress a connected component.
	kGravity = 0.02

	// layoutMaxStep bounds one node's movement in one iteration, in layout units.
	// Without it a node that starts inside another node's cell takes a single
	// enormous jump and never recovers.
	layoutMaxStep = 10.0

	// prevMobility damps a node that arrived with a cached position. New
	// nodes move at full speed and find their place; the existing map holds
	// still around them. 0.05 rather than 0 because a graph whose topology
	// genuinely changed must still be able to relax, just slowly, over the
	// many layout passes a growing corpus will run.
	prevMobility = 0.05

	// bhMaxDepth caps quadtree subdivision. Two nodes at the same coordinates
	// would otherwise subdivide forever; past this depth a cell simply
	// aggregates its bodies' mass, which is the same approximation
	// Barnes-Hut already makes one level up.
	bhMaxDepth = 40

	// layoutSeedSpread scales the hash placement circle for nodes with no
	// cached position, in layout units per sqrt(node).
	layoutSeedSpread = 30.0
)

// ComputeLayout runs ForceAtlas2 over the given graph and returns one position
// per node, in the order nodes were given.
//
// prev seeds the simulation: any node with a cached position starts there and
// moves at prevMobility, and any node without one is placed deterministically
// from a hash of its id. Positions in prev for nodes that are no longer in the
// graph are ignored. iterations<=0 means defaultLayoutIters.
//
// Rev is 0 on every returned position. The revision a layout was computed AT
// is the caller's fact, not this function's — the layout pass stamps
// GraphStats.MaxEdgeID onto each row before store.SetLayout, because that is
// the number the cache is keyed on and this function never reads the store.
//
// Edges whose endpoints are not in nodes are skipped rather than erroring: the
// caller's snapshot of nodes and edges is taken by two separate queries, so an
// edge inserted between them is expected, not a failure.
func ComputeLayout(nodes []store.GraphNode, edges []store.GraphEdge, prev []store.LayoutPos, iterations int) []store.LayoutPos {
	n := len(nodes)
	if n == 0 {
		return nil
	}
	if iterations <= 0 {
		iterations = defaultLayoutIters
	}

	idx := make(map[int64]int, n)
	for i, nd := range nodes {
		idx[nd.ID] = i
	}
	prevPos := make(map[int64]store.LayoutPos, len(prev))
	for _, p := range prev {
		prevPos[p.NodeID] = p
	}

	xs := make([]float64, n)
	ys := make([]float64, n)
	mobility := make([]float64, n)

	// Where the existing map IS, so a new node seeds inside it rather than at
	// the origin. This matters more than it looks: a cached layout that has
	// drifted off-centre would otherwise get every new node dropped thousands
	// of units away, and the edge attraction pulling it home would drag the
	// settled map with it — exactly the reshuffle prevMobility exists to
	// prevent.
	var sumX, sumY float64
	var have int
	minX, maxX := math.Inf(1), math.Inf(-1)
	minY, maxY := math.Inf(1), math.Inf(-1)
	for _, nd := range nodes {
		p, ok := prevPos[nd.ID]
		if !ok {
			continue
		}
		have++
		sumX += p.X
		sumY += p.Y
		minX, maxX = math.Min(minX, p.X), math.Max(maxX, p.X)
		minY, maxY = math.Min(minY, p.Y), math.Max(maxY, p.Y)
	}
	var centreX, centreY float64
	spread := layoutSeedSpread * math.Sqrt(float64(n))
	if have > 0 {
		centreX, centreY = sumX/float64(have), sumY/float64(have)
		if r := math.Max(maxX-minX, maxY-minY) / 2; r > 0 {
			spread = r
		}
	}

	for i, nd := range nodes {
		if p, ok := prevPos[nd.ID]; ok {
			xs[i], ys[i] = p.X, p.Y
			mobility[i] = prevMobility
			continue
		}
		dx, dy := seedPos(nd.ID, spread)
		xs[i], ys[i] = centreX+dx, centreY+dy
		mobility[i] = 1
	}

	// Mass is degree+1, counting only edges whose endpoints are both present.
	// Self-edges contribute nothing: they exert no attraction and inflating
	// their endpoint's mass would push a node away from the graph for a fact
	// that is about the node alone.
	type link struct{ a, b int }
	links := make([]link, 0, len(edges))
	mass := make([]float64, n)
	for i := range mass {
		mass[i] = 1
	}
	for _, e := range edges {
		a, okA := idx[e.SrcID]
		b, okB := idx[e.DstID]
		if !okA || !okB || a == b {
			continue
		}
		links = append(links, link{a, b})
		mass[a]++
		mass[b]++
	}

	fx := make([]float64, n)
	fy := make([]float64, n)
	var qt quadTree
	for it := 0; it < iterations; it++ {
		for i := range fx {
			fx[i], fy[i] = 0, 0
		}

		// Repulsion, approximated by the quadtree.
		qt.build(xs, ys, mass)
		for i := 0; i < n; i++ {
			dx, dy := qt.repulsion(i, xs[i], ys[i], mass[i])
			fx[i] += dx
			fy[i] += dy
		}

		// Attraction along edges: linear in distance (LinLog off).
		for _, l := range links {
			dx := xs[l.b] - xs[l.a]
			dy := ys[l.b] - ys[l.a]
			fx[l.a] += kAttraction * dx
			fy[l.a] += kAttraction * dy
			fx[l.b] -= kAttraction * dx
			fy[l.b] -= kAttraction * dy
		}

		// Gravity toward the origin, proportional to mass.
		for i := 0; i < n; i++ {
			d := math.Hypot(xs[i], ys[i])
			if d == 0 {
				continue
			}
			fx[i] -= kGravity * mass[i] * xs[i] / d
			fy[i] -= kGravity * mass[i] * ys[i] / d
		}

		// Cool linearly to zero so the run converges rather than oscillating
		// around an equilibrium forever.
		step := layoutMaxStep * (1 - float64(it)/float64(iterations))
		for i := 0; i < n; i++ {
			d := math.Hypot(fx[i], fy[i])
			if d == 0 {
				continue
			}
			scale := math.Min(step, d) / d * mobility[i]
			xs[i] += fx[i] * scale
			ys[i] += fy[i] * scale
		}
	}

	out := make([]store.LayoutPos, n)
	for i, nd := range nodes {
		out[i] = store.LayoutPos{NodeID: nd.ID, X: xs[i], Y: ys[i]}
	}
	return out
}

// seedPos places a node with no cached position on a disc of radius spread,
// deterministically from its id. Two independent hash draws (angle, and a
// sqrt-corrected radius so the disc fills evenly rather than clumping at the
// centre) rather than one, so ids that are numerically adjacent — which every
// id in a freshly ingested batch is — do not land on a line.
func seedPos(id int64, spread float64) (float64, float64) {
	h1 := splitmix64(uint64(id))
	h2 := splitmix64(h1)
	angle := 2 * math.Pi * unitFloat(h1)
	radius := spread * math.Sqrt(unitFloat(h2))
	return radius * math.Cos(angle), radius * math.Sin(angle)
}

// unitFloat maps a 64-bit hash into [0,1) using its top 53 bits — the exact
// number float64 can represent without rounding two draws onto one value.
func unitFloat(h uint64) float64 {
	return float64(h>>11) / float64(uint64(1)<<53)
}

// splitmix64 is the standard finalizer: a fast, well-distributed integer hash
// with no state and no seed, which is what makes it usable as the source of
// determinism here. Sequential inputs produce uncorrelated outputs, which is
// the property seedPos needs.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// qtCell is one quadtree cell. Children are indices into quadTree.cells, -1
// for absent; a slice-of-structs rather than a pointer tree so a rebuild per
// iteration reuses one allocation.
type qtCell struct {
	cx, cy, half float64 // centre and half-width of the square region
	mass         float64
	sumX, sumY   float64 // mass-weighted position sums; centre of mass is sum/mass
	child        [4]int32
	body         int32 // the single body this leaf holds, -1 when internal or aggregated
	count        int32
}

type quadTree struct {
	cells []qtCell
	xs    []float64
	ys    []float64
	ms    []float64
	stack []int32
}

// build rebuilds the tree over the given positions. The root square is the
// bounding box of every point, squared off and padded so no point sits exactly
// on a boundary.
func (t *quadTree) build(xs, ys, ms []float64) {
	t.xs, t.ys, t.ms = xs, ys, ms
	t.cells = t.cells[:0]
	if len(xs) == 0 {
		return
	}
	minX, maxX := xs[0], xs[0]
	minY, maxY := ys[0], ys[0]
	for i := range xs {
		if xs[i] < minX {
			minX = xs[i]
		}
		if xs[i] > maxX {
			maxX = xs[i]
		}
		if ys[i] < minY {
			minY = ys[i]
		}
		if ys[i] > maxY {
			maxY = ys[i]
		}
	}
	half := math.Max(maxX-minX, maxY-minY)/2 + 1
	t.cells = append(t.cells, newQTCell((minX+maxX)/2, (minY+maxY)/2, half))
	for i := range xs {
		t.insert(0, int32(i), 0)
	}
}

func newQTCell(cx, cy, half float64) qtCell {
	return qtCell{cx: cx, cy: cy, half: half, child: [4]int32{-1, -1, -1, -1}, body: -1}
}

// insert adds body i to the subtree rooted at cell. Iterative rather than
// recursive so a degenerate distribution cannot overflow the goroutine stack,
// and so the cells slice may reallocate freely between steps (indices survive
// a reallocation, pointers do not).
func (t *quadTree) insert(cell int32, i int32, depth int) {
	x, y, m := t.xs[i], t.ys[i], t.ms[i]
	for {
		c := &t.cells[cell]
		if c.count == 0 {
			c.count, c.body = 1, i
			c.mass, c.sumX, c.sumY = m, x*m, y*m
			return
		}
		// Past the depth cap a cell simply aggregates. Coincident nodes would
		// otherwise subdivide forever.
		if depth >= bhMaxDepth {
			c.count++
			c.body = -1
			c.mass += m
			c.sumX += x * m
			c.sumY += y * m
			return
		}
		if c.body >= 0 {
			// A leaf becoming internal: push its own body down one level
			// first. Its mass is already accounted for in this cell.
			old := c.body
			c.body = -1
			t.pushDown(cell, old)
			c = &t.cells[cell] // pushDown may have grown the slice
		}
		c.count++
		c.mass += m
		c.sumX += x * m
		c.sumY += y * m
		cell = t.childFor(cell, x, y)
		depth++
	}
}

// pushDown places an existing body into the correct child of an internal cell
// without touching that cell's own aggregates.
func (t *quadTree) pushDown(cell int32, body int32) {
	child := t.childFor(cell, t.xs[body], t.ys[body])
	c := &t.cells[child]
	c.count, c.body = 1, body
	c.mass = t.ms[body]
	c.sumX = t.xs[body] * t.ms[body]
	c.sumY = t.ys[body] * t.ms[body]
}

// childFor returns the index of cell's child quadrant containing (x,y),
// creating it if absent.
func (t *quadTree) childFor(cell int32, x, y float64) int32 {
	c := t.cells[cell]
	q := 0
	if x >= c.cx {
		q |= 1
	}
	if y >= c.cy {
		q |= 2
	}
	if c.child[q] >= 0 {
		return c.child[q]
	}
	h := c.half / 2
	cx, cy := c.cx-h, c.cy-h
	if q&1 != 0 {
		cx = c.cx + h
	}
	if q&2 != 0 {
		cy = c.cy + h
	}
	t.cells = append(t.cells, newQTCell(cx, cy, h))
	id := int32(len(t.cells) - 1)
	t.cells[cell].child[q] = id
	return id
}

// repulsion accumulates the Barnes-Hut approximated repulsive force on body i.
// A cell is used whole when it is a leaf or when its width over its distance
// falls below bhTheta; otherwise its children are opened.
//
// The FA2 repulsion magnitude is k_r*m_i*m_j/d, so the displacement
// contribution along each axis is that magnitude times dx/d — i.e. dx/d^2.
func (t *quadTree) repulsion(i int, x, y, m float64) (float64, float64) {
	if len(t.cells) == 0 {
		return 0, 0
	}
	var fx, fy float64
	t.stack = append(t.stack[:0], 0)
	for len(t.stack) > 0 {
		ci := t.stack[len(t.stack)-1]
		t.stack = t.stack[:len(t.stack)-1]
		c := t.cells[ci]
		if c.count == 0 || c.mass == 0 {
			continue
		}
		if c.body == int32(i) && c.count == 1 {
			continue // self
		}
		comX, comY := c.sumX/c.mass, c.sumY/c.mass
		dx, dy := x-comX, y-comY
		d2 := dx*dx + dy*dy
		if d2 == 0 {
			// Exactly coincident — including a body sitting on its own
			// aggregate cell's centre of mass. Nudge deterministically off
			// the id so the pair separates instead of dividing by zero.
			dx = 1e-3 * (unitFloat(splitmix64(uint64(i))) - 0.5)
			dy = 1e-3 * (unitFloat(splitmix64(uint64(ci))) - 0.5)
			d2 = dx*dx + dy*dy
			if d2 == 0 {
				continue
			}
		}
		if c.body >= 0 || 2*c.half/math.Sqrt(d2) < bhTheta {
			f := kRepulsion * m * c.mass / d2
			fx += dx * f
			fy += dy * f
			continue
		}
		for _, ch := range c.child {
			if ch >= 0 {
				t.stack = append(t.stack, ch)
			}
		}
	}
	return fx, fy
}
