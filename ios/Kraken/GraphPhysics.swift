import CoreGraphics
import Foundation

// The graph screen's force simulation — the Swift twin of
// web/_ui/src/components/graph/physics.ts, and a deliberate re-implementation
// of d3-force's integrator rather than a fresh design: the two clients draw
// the same graph, so the phone's feel has to come out of the same constants
// (link distance 70/√occurrences with a floor of 30, charge -60, anchor 0.02
// toward the seed set's own centroid, velocity decay 0.4) applied in the same
// order. Where the two differ it is because iOS has no d3 to lean on — with
// one exception, `alphaDecay`, where the two clients were given different
// settle budgets and so landed a hair apart; it is disclosed at the constant.
//
// No SwiftUI, no UIKit, no timer. The engine is a pure state machine over
// positions and the CALLER drives it: GraphViewModel owns the display link and
// decides when a frame happens, which is what lets every obligation below be
// asserted from a unit test with no view and no run loop standing up — the
// same split GraphModel.swift draws one step further along.
//
// Three properties carry the Obsidian feel the spec asked for, and each is a
// decision rather than an accident:
//   - WARM START. The engine seeds EXACTLY at the positions handed to it (the
//     server's own `kg_layout`), never at a random scatter, so the map opens
//     in its remembered shape and physics only settles the delta.
//   - PINNED DRAG. A dragged node is fixed AT the pointer (d3's fx/fy), not
//     pushed toward it, so it can never lag the finger; its neighbours follow
//     through their links, which is the part that reads as life.
//   - WARM, NOT HOT. At rest alpha falls below a settle floor, `isAwake` goes
//     false, and the caller stops scheduling frames — an idle graph costs
//     nothing. Interaction bumps alpha back up.
//
// Everything here is deterministic. d3 jiggles coincident points with
// `Math.random()`; that would make two engines built from identical inputs
// diverge, so `graphJiggle` below is a pure function of the indices involved.

/// Above this many VISIBLE nodes physics is never constructed and the canvas
/// falls back to the static server layout.
///
/// 3,000 is the web module's cap, kept identical on purpose: the two clients
/// should not disagree about whether a given graph is simulatable. The number
/// itself came from round-1 research putting comfortable 60 fps headroom for
/// an O(n log n) tick at 3,000 nodes / ~6,000 edges.
let graphPhysicsNodeCap = 3000

/// The one on/off decision. Reduce-motion wins outright; otherwise the cap
/// decides.
///
/// There is deliberately no user toggle on the phone — the web header has a
/// row to hang one on and this screen's chip row does not, so the accessibility
/// setting and the cap are the whole gate.
func graphPhysicsAllowed(visibleNodes: Int, reduceMotion: Bool) -> Bool {
    if reduceMotion { return false }
    return visibleNodes <= graphPhysicsNodeCap
}

/// One node's position as the engine reports it. WORLD coordinates — the same
/// space the server layout is in and the same space `GraphCamera` transforms
/// from, so a simulated position drops straight into `GraphRender.positions`.
struct GraphPhysicsNode: Equatable {
    let id: Int64
    var x: CGFloat
    var y: CGFloat
}

/// One link as the simulation sees it: the pair, plus the corroboration count
/// that sets its rest length. A named type rather than the engine's own tuple
/// because the view model compares two of these lists to decide whether the
/// simulation's MEMBERSHIP changed, and tuples are not `Equatable`.
struct GraphPhysicsLink: Equatable {
    let src: Int64
    let dst: Int64
    let occurrences: Int64
}

/// What a simulation was built for: the visible nodes and the visible edges.
///
/// Its whole job is to answer "did anything the engine is made of change?"
/// with one `==`. That question has to be asked of the MEMBERSHIP rather than
/// of the payload, because the graph screen re-polls every 30 s and almost
/// every poll returns a byte-identical graph — keying a rebuild on the payload
/// would restart a settled simulation twice a minute forever.
struct GraphPhysicsMembership: Equatable {
    let nodes: [Int64]
    let links: [GraphPhysicsLink]
}

/// A deterministic stand-in for d3's random jiggle.
///
/// Two coincident points give a zero separation to divide by, and every force
/// here divides by one. d3 breaks the tie with `Math.random()`; this engine
/// owes its caller determinism ("two engines built from identical inputs …
/// yield identical positions"), so the tie is broken from the indices instead.
/// The magnitude matches d3's 1e-6 scale — big enough to divide by, far too
/// small to see.
@inline(__always)
private func graphJiggle(_ seed: Int) -> Double {
    let m = (seed &* 2_654_435_761) & 0x3FF
    let magnitude = 1e-6 + Double(m) * 1e-9
    return seed & 1 == 0 ? magnitude : -magnitude
}

/// The simulation. One instance per membership: the caller rebuilds it when
/// the set of visible nodes or edges changes and otherwise leaves it alone.
///
/// Not `Sendable` and not internally synchronised — it is plain mutable state,
/// and main-actor confinement is the caller's job (GraphViewModel is
/// `@MainActor`, and the display link that drives it fires on the main thread).
final class GraphPhysics {
    // MARK: - tuning
    //
    // Every constant below is a FEEL decision rather than a correctness one,
    // and each is the web module's value:
    //   - charge is mild: Obsidian's swaying, not a starburst.
    //   - anchoring is weak and POSITIONAL, toward the seed set's own
    //     centroid. Never a d3-style `forceCenter`, which translates the whole
    //     system every tick to hold the centroid fixed — that made dragging
    //     one node shove every unlinked node too, and centring on (0,0) would
    //     drift a map whose server-layout centroid is elsewhere.
    //   - link rest distance shrinks as occurrences grow, so corroborated
    //     facts sit closer and the map's geometry carries evidence weight; the
    //     floor keeps a heavily-corroborated pair from overlapping.

    private static let chargeStrength: Double = -60
    private static let anchorStrength: Double = 0.02
    private static let linkBaseDistance: Double = 70
    private static let linkMinDistance: Double = 30
    /// Breathing room added to each node's radius before two of them collide,
    /// so labels do not pile on top of each other at rest. The web module adds
    /// the same 2 at its collide force; it lives here rather than in the
    /// caller's radius so `radius:` at the init still means the node's radius.
    private static let collidePadding: Double = 2

    /// d3's `velocityDecay` expressed as its multiplier (d3 stores 1 - 0.4).
    private static let velocityDecay: Double = 0.6
    /// How fast alpha walks toward its target each tick.
    ///
    /// Tuned against the two settle deadlines this engine owes rather than
    /// copied from d3 (whose 0.0228 is aimed at a 300-tick cold start): from
    /// the drag level of 0.3 this reaches the settle floor in ~67 ticks —
    /// about 1.1 s at 60 fps — and from the seed level of 0.1 in ~49.
    ///
    /// This is the ONE number that is not its web twin's: `physics.ts` uses
    /// 0.066 (~60 ticks release→sleep, ~44 to settle the seed). Both were
    /// solved for a budget and the budgets differ — the web spec asked for
    /// "≤ ~1 s after release", the frozen contract for this slice asks for
    /// false-within-90-ticks, i.e. ~1.5 s. Neither is a drift, and the gap is
    /// six ticks of settle nobody can see; it is written down here rather than
    /// quietly reconciled because the two files claim to share their tuning.
    private static let alphaDecay: Double = 0.06
    /// Below this, the simulation would no longer visibly move.
    private static let alphaSettleFloor: Double = 0.005
    /// Where alpha starts: the map arrives near equilibrium already, so a
    /// small alpha settles the delta without convulsing the whole graph on
    /// first open.
    private static let seedAlpha: Double = 0.1
    /// The neighbourhood-wakes level, held while a drag pin is down.
    private static let dragAlpha: Double = 0.3

    /// Barnes-Hut's opening angle, squared. d3's default 0.9 — a cell is
    /// approximated by its centre of mass once it is further away than about
    /// 1.11x its own width.
    private static let theta2: Double = 0.81
    /// d3's `distanceMin` squared: the closest two bodies are allowed to be
    /// for charge purposes, which is what stops a near-coincident pair from
    /// launching each other off the canvas.
    private static let distanceMin2: Double = 1
    /// Depth cap on the quadtree. Coincident points cannot be separated by
    /// subdivision at all, so without this the build recurses forever.
    private static let maxDepth = 24

    // MARK: - state

    private var ids: [Int64]
    private var indexByID: [Int64: Int]
    private var px: [Double]
    private var py: [Double]
    private var vx: [Double]
    private var vy: [Double]
    /// The positions handed in at init. Kept for one purpose: a coordinate
    /// that somehow came out non-finite is reset to its seed rather than left
    /// to poison every later frame — see `integrate`.
    private var seedX: [Double]
    private var seedY: [Double]
    private var rad: [Double]

    private var pinned: [Bool]
    private var fxAt: [Double]
    private var fyAt: [Double]
    /// How far the pin has been dragged SINCE THE LAST TICK, and how far it
    /// travelled during the frame before that.
    ///
    /// Two pairs rather than one because a velocity is displacement per FRAME
    /// and a drag arrives as an unbounded number of events per frame. Recording
    /// one call's displacement made the glide a function of the event rate
    /// rather than of the finger: at the same finger speed, eight `drag` calls
    /// per frame recorded one eighth of the motion of one call per frame, so a
    /// fast flick on a busy frame glided a fraction of the distance of the same
    /// flick on a quiet one. The step accumulates every call in a frame; the
    /// tick rotates it into the frame slot and clears it. `release` prefers the
    /// step — the part of the frame the finger actually did before letting go —
    /// and falls back to the last completed frame when the release lands on a
    /// tick boundary with nothing accumulated yet.
    private var pinStepX: [Double]
    private var pinStepY: [Double]
    private var pinFrameX: [Double]
    private var pinFrameY: [Double]
    /// The indices currently pinned. A LIST rather than a count because both
    /// the tick's rotation above and `isAwake` need to walk exactly the pins,
    /// and scanning `pinned` per frame would be an O(nodes) pass at the cap to
    /// find the one or two entries that are ever set.
    private var pinnedList: [Int] = []

    private var linkS: [Int]
    private var linkT: [Int]
    private var linkDistance: [Double]
    private var linkStrength: [Double]
    private var linkBias: [Double]

    private let anchorX: Double
    private let anchorY: Double
    private let maxRadius: Double

    private var alpha = GraphPhysics.seedAlpha
    private var alphaTarget: Double = 0

    // Scratch, allocated once and reused every tick — a per-frame allocation
    // of any of these would dominate the tick on a graph of any size.
    private var cells: [Cell] = []
    private var order: [Int32] = []
    private var scratch: [Int32] = []
    /// The charge traversal's visit stack. Sized on first use and reused
    /// after — see `applyCharge`.
    private var stack: [Int32] = []
    private var qx: [Double] = []
    private var qy: [Double] = []
    private var nodeCell: [Int] = []
    private var bucketStart: [Int] = []
    private var bucketCursor: [Int] = []
    private var buckets: [Int32] = []

    /// One quadtree cell, flat in an array rather than a reference graph: this
    /// is rebuilt every tick, and a tree of class instances would pay ARC on
    /// every node of it, every frame.
    private struct Cell {
        var cx: Double = 0
        var cy: Double = 0
        /// Summed charge strength beneath this cell (negative, since charge
        /// repels).
        var mass: Double = 0
        /// Width of this cell's square — Barnes-Hut's `w`.
        var size: Double = 0
        var c0: Int32 = -1
        var c1: Int32 = -1
        var c2: Int32 = -1
        var c3: Int32 = -1
        /// Leaves only: the range of `order` holding this cell's node indices.
        var start: Int32 = -1
        var count: Int32 = 0
    }

    // MARK: - init

    /// Seeds the simulation EXACTLY at the given positions.
    ///
    /// A link naming an id absent from `nodes` is dropped silently: the
    /// caller's node filter and its edge list can legitimately disagree for
    /// one frame (a poll landing mid-gesture), and a crash there would take
    /// the screen down over a transient.
    init(
        nodes: [(id: Int64, x: CGFloat, y: CGFloat, radius: CGFloat)],
        links: [(src: Int64, dst: Int64, occurrences: Int64)]
    ) {
        let n = nodes.count
        ids = nodes.map(\.id)
        // A non-finite seed coordinate is taken as 0. "Seeds exactly at the
        // given positions" is about the warm start and cannot be honoured for
        // a coordinate that is not a position: NaN in means NaN out of every
        // later frame, and there is no arithmetic that recovers from it.
        px = nodes.map { $0.x.isFinite ? Double($0.x) : 0 }
        py = nodes.map { $0.y.isFinite ? Double($0.y) : 0 }
        seedX = px
        seedY = py
        rad = nodes.map { Double($0.radius) }
        vx = [Double](repeating: 0, count: n)
        vy = [Double](repeating: 0, count: n)
        pinned = [Bool](repeating: false, count: n)
        fxAt = [Double](repeating: 0, count: n)
        fyAt = [Double](repeating: 0, count: n)
        pinStepX = [Double](repeating: 0, count: n)
        pinStepY = [Double](repeating: 0, count: n)
        pinFrameX = [Double](repeating: 0, count: n)
        pinFrameY = [Double](repeating: 0, count: n)

        // First id wins on a duplicate. Nothing upstream produces one (node
        // ids are a primary key), so this is only about the lookup being
        // total rather than about a case that happens.
        var index: [Int64: Int] = [:]
        index.reserveCapacity(n)
        for i in 0..<n where index[ids[i]] == nil { index[ids[i]] = i }
        indexByID = index

        // The anchor is the SEED SET's centroid, sampled once. Per-node
        // springs toward where the map already was keep disconnected
        // components from wandering off-canvas without translating anything.
        if n > 0 {
            var sx = 0.0, sy = 0.0
            for i in 0..<n { sx += px[i]; sy += py[i] }
            anchorX = sx / Double(n)
            anchorY = sy / Double(n)
        } else {
            anchorX = 0
            anchorY = 0
        }

        var s: [Int] = []
        var t: [Int] = []
        var occ: [Int64] = []
        s.reserveCapacity(links.count)
        t.reserveCapacity(links.count)
        occ.reserveCapacity(links.count)
        for l in links {
            guard let a = index[l.src], let b = index[l.dst] else { continue }
            s.append(a)
            t.append(b)
            occ.append(l.occurrences)
        }
        linkS = s
        linkT = t

        // d3's link strength and bias, both derived from endpoint degree: a
        // link into a hub pulls the hub less than it pulls the leaf, which is
        // what keeps a high-degree node from being yanked around by every one
        // of its edges. A self-loop counts twice, which is also what makes it
        // cancel exactly rather than explode — see `applyLinks`.
        var degree = [Int](repeating: 0, count: n)
        for k in 0..<s.count {
            degree[s[k]] += 1
            degree[t[k]] += 1
        }
        linkDistance = occ.map { o in
            max(
                GraphPhysics.linkMinDistance,
                GraphPhysics.linkBaseDistance / Double(max(1, o)).squareRoot()
            )
        }
        linkStrength = (0..<s.count).map { 1 / Double(max(1, min(degree[s[$0]], degree[t[$0]]))) }
        linkBias = (0..<s.count).map {
            let ds = Double(degree[s[$0]])
            let dt = Double(degree[t[$0]])
            return ds + dt > 0 ? ds / (ds + dt) : 0.5
        }

        maxRadius = (rad.max() ?? 0) + GraphPhysics.collidePadding

        order = [Int32](repeating: 0, count: n)
        scratch = [Int32](repeating: 0, count: n)
        qx = [Double](repeating: 0, count: n)
        qy = [Double](repeating: 0, count: n)
        nodeCell = [Int](repeating: 0, count: n)
        buckets = [Int32](repeating: 0, count: n)
        cells.reserveCapacity(max(16, 2 * n))
    }

    // MARK: - the caller's surface

    /// Current positions, one per node given at init, in that order.
    func nodes() -> [GraphPhysicsNode] {
        (0..<ids.count).map {
            GraphPhysicsNode(id: ids[$0], x: CGFloat(px[$0]), y: CGFloat(py[$0]))
        }
    }

    /// True while the simulation would visibly move: alpha above its settle
    /// floor, or a drag pin held. The caller's cue to schedule another frame —
    /// when this is false an idle screen schedules no per-frame work at all.
    var isAwake: Bool {
        alpha > GraphPhysics.alphaSettleFloor || !pinnedList.isEmpty
    }

    /// Pins `id` AT the given world point and wakes the simulation.
    ///
    /// The position is written immediately as well as pinned, so a drag that
    /// releases before the next frame still reports where the finger left the
    /// node. An unknown id is a complete no-op — no wake, no pin, no crash:
    /// the gesture can outlive the node it grabbed when a poll lands mid-drag.
    func drag(id: Int64, to point: CGPoint) {
        guard let i = indexByID[id], point.x.isFinite, point.y.isFinite else { return }
        let x = Double(point.x)
        let y = Double(point.y)
        if pinned[i] {
            // ACCUMULATED, never assigned: this is one event's share of the
            // frame's motion, and the frame may carry any number of them. It
            // cannot be read off `vx`, which the integrator zeroes on every
            // tick a node is pinned.
            pinStepX[i] += x - fxAt[i]
            pinStepY[i] += y - fyAt[i]
        } else {
            pinned[i] = true
            pinnedList.append(i)
            pinStepX[i] = 0
            pinStepY[i] = 0
            pinFrameX[i] = 0
            pinFrameY[i] = 0
        }
        fxAt[i] = x
        fyAt[i] = y
        px[i] = x
        py[i] = y
        vx[i] = 0
        vy[i] = 0
        alphaTarget = GraphPhysics.dragAlpha
        if alpha < GraphPhysics.dragAlpha { alpha = GraphPhysics.dragAlpha }
    }

    /// Unpins `id`, handing it the velocity the drag imparted — the glide. A
    /// node released mid-movement carries on past the release point and
    /// decelerates into its new equilibrium rather than stopping dead under
    /// the finger.
    ///
    /// An id that is not currently pinned returns before touching anything,
    /// which is what keeps it from ending ANOTHER node's pin or dropping a
    /// wake that belongs to a live gesture.
    func release(id: Int64) {
        guard let i = indexByID[id], pinned[i] else { return }
        pinned[i] = false
        pinnedList.removeAll { $0 == i }
        // The part of THIS frame the finger already did, or — when the release
        // lands on a tick boundary with nothing accumulated since — the last
        // frame it completed. A finger that has been motionless for a frame has
        // neither, which is the honest answer: a stationary release does not
        // fling.
        let mid = pinStepX[i] != 0 || pinStepY[i] != 0
        vx[i] = mid ? pinStepX[i] : pinFrameX[i]
        vy[i] = mid ? pinStepY[i] : pinFrameY[i]
        pinStepX[i] = 0
        pinStepY[i] = 0
        pinFrameX[i] = 0
        pinFrameY[i] = 0
        if pinnedList.isEmpty { alphaTarget = 0 }
    }

    /// Advances one frame.
    ///
    /// The order is d3's and is load-bearing: link and charge write
    /// VELOCITIES, collide corrects against PREDICTED positions (x + vx), and
    /// only then does the integrator apply the velocities to the positions. A
    /// force that moved positions directly would be applied at a different
    /// effective strength depending on where in the list it sat.
    func tick() {
        // Close the previous frame's drag accounting FIRST, before anything
        // this frame can add to it: everything `drag` accumulated since the
        // last tick is exactly one frame's worth of finger travel, and that is
        // the velocity a release during the coming frame falls back on.
        for i in pinnedList {
            pinFrameX[i] = pinStepX[i]
            pinFrameY[i] = pinStepY[i]
            pinStepX[i] = 0
            pinStepY[i] = 0
        }
        alpha += (alphaTarget - alpha) * GraphPhysics.alphaDecay
        applyLinks(alpha: alpha)
        applyCharge(alpha: alpha)
        applyAnchor(alpha: alpha)
        applyCollide()
        integrate()
    }

    // MARK: - forces

    private func applyLinks(alpha: Double) {
        for k in 0..<linkS.count {
            let s = linkS[k]
            let t = linkT[k]
            var dx = (px[t] + vx[t]) - (px[s] + vx[s])
            var dy = (py[t] + vy[t]) - (py[s] + vy[s])
            if dx == 0 { dx = graphJiggle(k) }
            if dy == 0 { dy = graphJiggle(k &+ 13) }
            let distance = (dx * dx + dy * dy).squareRoot()
            let f = (distance - linkDistance[k]) / distance * alpha * linkStrength[k]
            let ix = dx * f
            let iy = dy * f
            let b = linkBias[k]
            // A SELF-LOOP lands both halves on the same node with b = 0.5, so
            // they cancel exactly and the pair contributes nothing — which is
            // why a self-loop neither crashes nor produces a non-finite
            // position, despite its separation being the jiggle alone.
            vx[t] -= ix * b
            vy[t] -= iy * b
            vx[s] += ix * (1 - b)
            vy[s] += iy * (1 - b)
        }
    }

    private func applyAnchor(alpha: Double) {
        let k = GraphPhysics.anchorStrength * alpha
        for i in 0..<ids.count {
            vx[i] += (anchorX - px[i]) * k
            vy[i] += (anchorY - py[i]) * k
        }
    }

    /// Repulsion, Barnes-Hut. The quadtree is what keeps this O(n log n): the
    /// all-pairs form is 9,000,000 interactions per tick at the 3,000-node cap
    /// and would miss 60 fps by two orders of magnitude.
    ///
    /// The buffer-pointer nest is not decoration. This is the hottest loop in
    /// the binary's UI layer and the one place a bounds check per array access
    /// shows up in wall clock: the whole tick loop went from 4.90 s to 3.34 s
    /// with no other change, and only the traversal was touched.
    ///
    /// Those two figures came from a standalone `swiftc -Onone` harness at
    /// 3,000 nodes and 6,000 edges over 100 ticks — deliberately HEAVIER than
    /// the unit test that guards this, which builds a 2,999-link path graph
    /// over the same 3,000 nodes. So the test's own margin against its 10 s
    /// ceiling is at least the one measured here, never less. Unoptimized in
    /// both cases, which is the configuration a test target actually runs in
    /// and the only one worth quoting.
    ///
    /// Nothing inside reads a velocity, so writing them through a mutable
    /// buffer while the positions are borrowed read-only is sound.
    private func applyCharge(alpha: Double) {
        buildTree()
        guard !cells.isEmpty else { return }
        let n = ids.count
        // The visit stack, sized for the worst case and never grown inside the
        // loop: each pop can push four children, so it is bounded by the cells
        // visited rather than by the tree's depth. `cap` below is a hard stop
        // rather than a resize — a traversal that somehow exceeded it would
        // lose repulsion for one node, which no reader can see, while a
        // reallocation mid-loop would invalidate the pointer it runs on.
        if stack.count < 4 * n + 64 { stack = [Int32](repeating: 0, count: 4 * n + 64) }
        cells.withUnsafeBufferPointer { cb in
        order.withUnsafeBufferPointer { ob in
        px.withUnsafeBufferPointer { xb in
        py.withUnsafeBufferPointer { yb in
        stack.withUnsafeMutableBufferPointer { sb in
        vx.withUnsafeMutableBufferPointer { vxb in
        vy.withUnsafeMutableBufferPointer { vyb in
            let cap = sb.count
            for i in 0..<n {
                let xi = xb[i]
                let yi = yb[i]
                var accX = 0.0
                var accY = 0.0
                sb[0] = 0
                var top = 1
                while top > 0 {
                    top -= 1
                    let cell = cb[Int(sb[top])]
                    if cell.mass == 0 { continue }
                    var dx = cell.cx - xi
                    var dy = cell.cy - yi
                    var l = dx * dx + dy * dy
                    let w = cell.size
                    if w * w < GraphPhysics.theta2 * l {
                        if dx == 0 { dx = graphJiggle(i &+ Int(cell.start)); l += dx * dx }
                        if dy == 0 { dy = graphJiggle(i &+ Int(cell.start) &+ 7); l += dy * dy }
                        if l < GraphPhysics.distanceMin2 {
                            l = (GraphPhysics.distanceMin2 * l).squareRoot()
                        }
                        let f = cell.mass * alpha / l
                        accX += dx * f
                        accY += dy * f
                        continue
                    }
                    if cell.count > 0 {
                        let lo = Int(cell.start)
                        let hi = lo + Int(cell.count)
                        for k in lo..<hi {
                            let j = Int(ob[k])
                            if j == i { continue }
                            var jx = xb[j] - xi
                            var jy = yb[j] - yi
                            var jl = jx * jx + jy * jy
                            if jx == 0 { jx = graphJiggle(i &+ j); jl += jx * jx }
                            if jy == 0 { jy = graphJiggle(i &+ j &+ 7); jl += jy * jy }
                            if jl < GraphPhysics.distanceMin2 {
                                jl = (GraphPhysics.distanceMin2 * jl).squareRoot()
                            }
                            let f = GraphPhysics.chargeStrength * alpha / jl
                            accX += jx * f
                            accY += jy * f
                        }
                        continue
                    }
                    if cell.c0 >= 0, top < cap { sb[top] = cell.c0; top += 1 }
                    if cell.c1 >= 0, top < cap { sb[top] = cell.c1; top += 1 }
                    if cell.c2 >= 0, top < cap { sb[top] = cell.c2; top += 1 }
                    if cell.c3 >= 0, top < cap { sb[top] = cell.c3; top += 1 }
                }
                vxb[i] += accX
                vyb[i] += accY
            }
        }}}}}}}
    }

    // MARK: - the quadtree

    private func buildTree() {
        cells.removeAll(keepingCapacity: true)
        let n = ids.count
        guard n > 0 else { return }
        var minX = px[0], maxX = px[0], minY = py[0], maxY = py[0]
        for i in 1..<n {
            if px[i] < minX { minX = px[i] }
            if px[i] > maxX { maxX = px[i] }
            if py[i] < minY { minY = py[i] }
            if py[i] > maxY { maxY = py[i] }
        }
        var size = max(maxX - minX, maxY - minY)
        // A degenerate or non-finite extent still has to produce a usable
        // square: every point then lands in one leaf, which is correct, just
        // not selective.
        if !(size > 0) || !size.isFinite { size = 1 }
        // Grown a hair so the point ON the far edge is still inside the root.
        size *= 1.000_000_1
        for i in 0..<n { order[i] = Int32(i) }
        _ = subdivide(lo: 0, hi: n, x0: minX, y0: minY, size: size, depth: 0)
    }

    /// Builds one cell over `order[lo..<hi]`, partitioning that range in place
    /// into its four quadrants. Returns the cell's index.
    private func subdivide(
        lo: Int, hi: Int, x0: Double, y0: Double, size: Double, depth: Int
    ) -> Int32 {
        let idx = Int32(cells.count)
        cells.append(Cell())
        cells[Int(idx)].size = size
        let count = hi - lo

        if count <= 1 || depth >= GraphPhysics.maxDepth {
            // A depth-capped leaf can hold several points; its centre of mass
            // is their mean, which is exact for the coincident case that
            // produces one and a fair approximation otherwise.
            var sx = 0.0
            var sy = 0.0
            for k in lo..<hi {
                let i = Int(order[k])
                sx += px[i]
                sy += py[i]
            }
            cells[Int(idx)].cx = sx / Double(count)
            cells[Int(idx)].cy = sy / Double(count)
            cells[Int(idx)].mass = Double(count) * GraphPhysics.chargeStrength
            cells[Int(idx)].start = Int32(lo)
            cells[Int(idx)].count = Int32(count)
            return idx
        }

        let half = size / 2
        let mx = x0 + half
        let my = y0 + half
        var n0 = 0, n1 = 0, n2 = 0, n3 = 0
        for k in lo..<hi {
            let i = Int(order[k])
            switch (px[i] >= mx ? 1 : 0) | (py[i] >= my ? 2 : 0) {
            case 0: n0 += 1
            case 1: n1 += 1
            case 2: n2 += 1
            default: n3 += 1
            }
        }
        let s0 = lo, s1 = s0 + n0, s2 = s1 + n1, s3 = s2 + n2
        var o0 = s0, o1 = s1, o2 = s2, o3 = s3
        for k in lo..<hi {
            let i = order[k]
            switch (px[Int(i)] >= mx ? 1 : 0) | (py[Int(i)] >= my ? 2 : 0) {
            case 0: scratch[o0] = i; o0 += 1
            case 1: scratch[o1] = i; o1 += 1
            case 2: scratch[o2] = i; o2 += 1
            default: scratch[o3] = i; o3 += 1
            }
        }
        for k in lo..<hi { order[k] = scratch[k] }

        var kids: (Int32, Int32, Int32, Int32) = (-1, -1, -1, -1)
        if n0 > 0 {
            kids.0 = subdivide(lo: s0, hi: s1, x0: x0, y0: y0, size: half, depth: depth + 1)
        }
        if n1 > 0 {
            kids.1 = subdivide(lo: s1, hi: s2, x0: mx, y0: y0, size: half, depth: depth + 1)
        }
        if n2 > 0 {
            kids.2 = subdivide(lo: s2, hi: s3, x0: x0, y0: my, size: half, depth: depth + 1)
        }
        if n3 > 0 {
            kids.3 = subdivide(lo: s3, hi: hi, x0: mx, y0: my, size: half, depth: depth + 1)
        }

        // Accumulated through a local rather than by iterating an array
        // literal of the four child indices: this runs once per internal cell
        // per tick, and that literal would be a heap allocation on that path.
        var mass = 0.0
        var weight = 0.0
        var ax = 0.0
        var ay = 0.0
        func absorb(_ child: Int32) {
            guard child >= 0 else { return }
            let c = cells[Int(child)]
            let w = abs(c.mass)
            mass += c.mass
            weight += w
            ax += w * c.cx
            ay += w * c.cy
        }
        absorb(kids.0)
        absorb(kids.1)
        absorb(kids.2)
        absorb(kids.3)
        cells[Int(idx)].mass = mass
        cells[Int(idx)].cx = weight > 0 ? ax / weight : mx
        cells[Int(idx)].cy = weight > 0 ? ay / weight : my
        cells[Int(idx)].c0 = kids.0
        cells[Int(idx)].c1 = kids.1
        cells[Int(idx)].c2 = kids.2
        cells[Int(idx)].c3 = kids.3
        return idx
    }

    // MARK: - collision

    /// Keeps node circles from overlapping at rest, so labels do not pile up.
    ///
    /// Deliberately NOT scaled by alpha, exactly as d3's own collide is not:
    /// separation is a hard constraint rather than a settling force. The
    /// broad phase is a uniform grid rather than a second quadtree — every
    /// radius on this canvas is within a factor of four of every other
    /// (`graphNodeRadius` clamps to 4…16), which is the case a grid handles
    /// as well as a tree and with far less per-tick work.
    private func applyCollide() {
        let n = ids.count
        guard n > 1, maxRadius > 0 else { return }
        for i in 0..<n {
            qx[i] = px[i] + vx[i]
            qy[i] = py[i] + vy[i]
        }
        var minX = qx[0], maxX = qx[0], minY = qy[0], maxY = qy[0]
        for i in 1..<n {
            if qx[i] < minX { minX = qx[i] }
            if qx[i] > maxX { maxX = qx[i] }
            if qy[i] < minY { minY = qy[i] }
            if qy[i] > maxY { maxY = qy[i] }
        }
        let spanX = maxX - minX
        let spanY = maxY - minY
        guard spanX.isFinite, spanY.isFinite, spanX >= 0, spanY >= 0 else { return }
        var cellSize = 2 * maxRadius
        // The ratio guard is not slack: `Int(_: Double)` TRAPS above Int.max,
        // and a graph that has been flung apart can hand this a span of any
        // magnitude. A grid that absurd would buy nothing anyway.
        guard spanX / cellSize < 1e6, spanY / cellSize < 1e6 else { return }
        var gw = Int(spanX / cellSize) + 1
        var gh = Int(spanY / cellSize) + 1
        // Hold the grid to O(n) cells: zeroing the bucket table is per-CELL
        // work, so a sparse graph over a huge extent would otherwise spend the
        // whole tick clearing empty cells.
        let cellCap = 4 * n + 64
        if gw * gh > cellCap {
            let scale = (Double(gw) * Double(gh) / Double(cellCap)).squareRoot()
            cellSize *= max(scale, 1)
            gw = Int(spanX / cellSize) + 1
            gh = Int(spanY / cellSize) + 1
        }
        let cellsCount = gw * gh
        guard cellsCount > 0, cellsCount <= 8 * cellCap else { return }

        if bucketStart.count < cellsCount + 1 {
            bucketStart = [Int](repeating: 0, count: cellsCount + 1)
            bucketCursor = [Int](repeating: 0, count: cellsCount + 1)
        } else {
            for c in 0...cellsCount { bucketStart[c] = 0 }
        }
        for i in 0..<n {
            let gx = min(gw - 1, max(0, Int((qx[i] - minX) / cellSize)))
            let gy = min(gh - 1, max(0, Int((qy[i] - minY) / cellSize)))
            let c = gy * gw + gx
            nodeCell[i] = c
            bucketStart[c + 1] += 1
        }
        for c in 1...cellsCount { bucketStart[c] += bucketStart[c - 1] }
        for c in 0...cellsCount { bucketCursor[c] = bucketStart[c] }
        for i in 0..<n {
            let c = nodeCell[i]
            buckets[bucketCursor[c]] = Int32(i)
            bucketCursor[c] += 1
        }

        for i in 0..<n {
            let ri = rad[i] + GraphPhysics.collidePadding
            let ri2 = ri * ri
            let gx = nodeCell[i] % gw
            let gy = nodeCell[i] / gw
            let y0 = max(0, gy - 1), y1 = min(gh - 1, gy + 1)
            let x0 = max(0, gx - 1), x1 = min(gw - 1, gx + 1)
            for oy in y0...y1 {
                for ox in x0...x1 {
                    let c = oy * gw + ox
                    for b in bucketStart[c]..<bucketStart[c + 1] {
                        // Each unordered pair is visited once, from its lower
                        // index — the same rule d3 uses, and the reason the
                        // impulse below can be applied to both nodes at once.
                        let j = Int(buckets[b])
                        if j <= i { continue }
                        let rj = rad[j] + GraphPhysics.collidePadding
                        let r = ri + rj
                        var dx = (px[i] + vx[i]) - (px[j] + vx[j])
                        var dy = (py[i] + vy[i]) - (py[j] + vy[j])
                        var l = dx * dx + dy * dy
                        guard l < r * r else { continue }
                        if dx == 0 { dx = graphJiggle(i &+ j); l += dx * dx }
                        if dy == 0 { dy = graphJiggle(i &+ j &+ 3); l += dy * dy }
                        let d = l.squareRoot()
                        guard d > 0 else { continue }
                        let f = (r - d) / d
                        dx *= f
                        dy *= f
                        // The bigger circle moves less: the overlap is split
                        // by squared radius, so a hub is not shoved aside by
                        // every leaf that drifts into it.
                        let rj2 = rj * rj
                        let share = rj2 / (ri2 + rj2)
                        vx[i] += dx * share
                        vy[i] += dy * share
                        vx[j] -= dx * (1 - share)
                        vy[j] -= dy * (1 - share)
                    }
                }
            }
        }
    }

    // MARK: - integration

    private func integrate() {
        for i in 0..<ids.count {
            if pinned[i] {
                // A pinned node IS its pin, every frame, with no velocity of
                // its own — which is what makes a drag track the finger
                // exactly rather than spring toward it.
                px[i] = fxAt[i]
                py[i] = fyAt[i]
                vx[i] = 0
                vy[i] = 0
            } else {
                vx[i] *= GraphPhysics.velocityDecay
                vy[i] *= GraphPhysics.velocityDecay
                px[i] += vx[i]
                py[i] += vy[i]
            }
            // The engine's own backstop on "no position is ever NaN or
            // infinite". Every force above is written to avoid producing one,
            // so reaching this is a bug rather than a mode — but a single
            // non-finite coordinate propagates to every node through the
            // charge tree on the very next tick and blanks the canvas with
            // nothing logged, so it is caught here rather than trusted away.
            if !px[i].isFinite || !py[i].isFinite {
                px[i] = seedX[i]
                py[i] = seedY[i]
                vx[i] = 0
                vy[i] = 0
            }
            if !vx[i].isFinite { vx[i] = 0 }
            if !vy[i].isFinite { vy[i] = 0 }
        }
    }
}
