import XCTest
import Foundation
import CoreGraphics
@testable import Kraken

// Boundaries, degenerate inputs, refusals and no-ops for the native graph
// screen's PHYSICS slice (artifacts/50-contract.md, round 3b). Authored blind,
// from that contract alone, before the implementation existed: no line here
// was written after reading ios/Kraken/GraphPhysics.swift, and the view model,
// the view and the model were read only at their committed (HEAD) state.
//
// Dimension split: a sibling author owns the stated invariants and happy paths
// (seed fidelity as a rule, the 3x follow ratio, the occurrences-9-settles-
// closer pair, the 3,000-node timing bound). What lives here is the second
// half of each rule — the id that names nothing, the release that unpins
// nobody, the pin held at 1e6, the empty engine, the `<= 3000` that includes
// 3000 and stops at 3001.
//
// Where the contract is SILENT this file asserts nothing, and the silence is
// listed in the author's report rather than resolved by guessing. Three known
// silences, none asserted below: a duplicate id at init; a non-finite SEED
// coordinate (obligation (f) is scoped to "any sequence of the above calls",
// not to poisoned input, and "Seeds EXACTLY at the given positions" points the
// other way); and a non-finite point handed to `drag(id:to:)`, where the same
// two sentences collide.
//
// Every file-scope identifier is `private` and prefixed `edgePhys` / class
// names `EdgePhysics` — this target's namespace is shared with a second
// author's file for this round AND with the round-1 `GraphEdgeTests.swift`,
// which already owns the bare `edge` prefix.

// MARK: - Fixtures & helpers

private typealias EdgePhysSeed = (id: Int64, x: CGFloat, y: CGFloat, radius: CGFloat)
private typealias EdgePhysLink = (src: Int64, dst: Int64, occurrences: Int64)

/// One seed node. The radius is a required column of the contract's init and
/// is given a plausible non-zero default: the contract fixes no relationship
/// between radius and any assertion here, so no test below varies it.
private func edgePhysSeed(_ id: Int64, _ x: CGFloat, _ y: CGFloat, radius: CGFloat = 8) -> EdgePhysSeed {
    (id: id, x: x, y: y, radius: radius)
}

private func edgePhysLink(_ src: Int64, _ dst: Int64, occurrences: Int64 = 1) -> EdgePhysLink {
    (src: src, dst: dst, occurrences: occurrences)
}

private func edgePhysMkEngine(_ nodes: [EdgePhysSeed], _ links: [EdgePhysLink] = []) -> GraphPhysics {
    GraphPhysics(nodes: nodes, links: links)
}

private func edgePhysTick(_ engine: GraphPhysics, _ frames: Int) {
    for _ in 0..<frames { engine.tick() }
}

/// Ticks until the simulation reports itself asleep, capped. Returns the
/// number of ticks spent, so a caller can assert the CAP the contract names
/// rather than a number this helper invented.
@discardableResult
private func edgePhysSettle(_ engine: GraphPhysics, max: Int = 200) -> Int {
    var spent = 0
    while spent < max {
        if !engine.isAwake { return spent }
        engine.tick()
        spent += 1
    }
    return spent
}

private func edgePhysPosition(_ engine: GraphPhysics, _ id: Int64) -> CGPoint? {
    guard let n = engine.nodes().first(where: { $0.id == id }) else { return nil }
    return CGPoint(x: n.x, y: n.y)
}

private func edgePhysPositions(_ engine: GraphPhysics) -> [Int64: CGPoint] {
    var out: [Int64: CGPoint] = [:]
    for n in engine.nodes() { out[n.id] = CGPoint(x: n.x, y: n.y) }
    return out
}

private func edgePhysIDs(_ engine: GraphPhysics) -> Set<Int64> {
    Set(engine.nodes().map(\.id))
}

/// Obligation (f), spelled once: "No position is ever NaN/infinite after any
/// sequence of the above calls." Every long-running test below ends here.
private func edgePhysAssertAllFinite(
    _ engine: GraphPhysics, _ message: String = "",
    file: StaticString = #filePath, line: UInt = #line
) {
    for n in engine.nodes() {
        XCTAssertTrue(n.x.isFinite, "node \(n.id) x = \(n.x) — \(message)", file: file, line: line)
        XCTAssertTrue(n.y.isFinite, "node \(n.id) y = \(n.y) — \(message)", file: file, line: line)
    }
}

/// "IS the pinned point exactly (it never lags or springs)". The accuracy is a
/// float round-trip's worth and no more — this is an equality assertion, not a
/// convergence one.
private func edgePhysAssertPinned(
    _ engine: GraphPhysics, _ id: Int64, _ point: CGPoint,
    _ message: String = "", file: StaticString = #filePath, line: UInt = #line
) {
    guard let p = edgePhysPosition(engine, id) else {
        return XCTFail("node \(id) missing — \(message)", file: file, line: line)
    }
    XCTAssertEqual(p.x, point.x, accuracy: 1e-9, "pinned x: \(message)", file: file, line: line)
    XCTAssertEqual(p.y, point.y, accuracy: 1e-9, "pinned y: \(message)", file: file, line: line)
}

private func edgePhysDistance(_ a: CGPoint, _ b: CGPoint) -> CGFloat {
    let dx = a.x - b.x, dy = a.y - b.y
    return (dx * dx + dy * dy).squareRoot()
}

/// A small, ordinary graph: three nodes, two links, no coincident positions.
/// The baseline every "and nothing else moved" test perturbs.
private func edgePhysTrio() -> GraphPhysics {
    edgePhysMkEngine(
        [edgePhysSeed(1, -40, 10), edgePhysSeed(5, 12, -6), edgePhysSeed(6, 30, 44)],
        [edgePhysLink(1, 5), edgePhysLink(5, 6)]
    )
}

// MARK: - Membership: what exists, and what is dropped

final class EdgePhysicsMembershipTests: XCTestCase {

    // "A link naming an id absent from `nodes` is DROPPED silently, never a
    // crash (the caller's filter and the payload can disagree for one frame)."
    // Both endpoints absent, one endpoint absent, and both directions.
    func testLinksNamingAbsentIdsAreDroppedWithoutCrashing() {
        let engine = edgePhysMkEngine(
            [edgePhysSeed(1, -40, 10), edgePhysSeed(2, 20, 5)],
            [
                edgePhysLink(1, 99),      // dst absent
                edgePhysLink(99, 1),      // src absent
                edgePhysLink(98, 99),     // both absent
                edgePhysLink(1, 2),       // the one real link
            ]
        )
        XCTAssertEqual(edgePhysIDs(engine), [1, 2], "membership is the caller's: only the given nodes exist")
        edgePhysTick(engine, 200)
        XCTAssertEqual(edgePhysIDs(engine), [1, 2], "a dropped link must not conjure its missing endpoint")
        edgePhysAssertAllFinite(engine, "after 200 ticks over three dangling links")
    }

    // "Dropped" is stronger than "does not crash": a link to nobody must exert
    // no force at all, so an engine carrying it simulates identically to one
    // that never had it. Determinism ("two engines built from identical
    // inputs, ticked identically ... yield identical positions") is what makes
    // this comparison legal — the dropped link makes the two inputs identical.
    func testADroppedLinkChangesNothingAboutTheSimulation() {
        let clean = edgePhysMkEngine([edgePhysSeed(1, -40, 10), edgePhysSeed(2, 20, 5)])
        let dangling = edgePhysMkEngine(
            [edgePhysSeed(1, -40, 10), edgePhysSeed(2, 20, 5)],
            [edgePhysLink(1, 99, occurrences: 9), edgePhysLink(77, 2)]
        )
        edgePhysTick(clean, 120)
        edgePhysTick(dangling, 120)
        for id in [Int64(1), Int64(2)] {
            guard let a = edgePhysPosition(clean, id), let b = edgePhysPosition(dangling, id) else {
                return XCTFail("node \(id) missing from one of the two engines")
            }
            XCTAssertEqual(a.x, b.x, accuracy: 1e-9, "node \(id) x diverged — the dangling link pulled")
            XCTAssertEqual(a.y, b.y, accuracy: 1e-9, "node \(id) y diverged — the dangling link pulled")
        }
    }

    // "Seeds EXACTLY at the given positions — `nodes()` immediately after init
    // returns them unchanged". The interesting seeds are the ones a helper
    // might normalize away: the origin (which GraphModel's ring treats as
    // "unpositioned" — that rule belongs to graphPositions, NOT here), a
    // negative pair, and a very large finite pair.
    func testSeedIsExactForZeroNegativeAndLargeCoordinates() {
        let engine = edgePhysMkEngine([
            edgePhysSeed(1, 0, 0),
            edgePhysSeed(2, -103.4903506081596, 79.87158754979711),
            edgePhysSeed(3, 1_000_000, -1_000_000),
            edgePhysSeed(4, 1e-9, -1e-9),
        ])
        edgePhysAssertPinned(engine, 1, CGPoint(x: 0, y: 0), "the origin is a coordinate, not a sentinel")
        edgePhysAssertPinned(engine, 2, CGPoint(x: -103.4903506081596, y: 79.87158754979711))
        edgePhysAssertPinned(engine, 3, CGPoint(x: 1_000_000, y: -1_000_000))
        edgePhysAssertPinned(engine, 4, CGPoint(x: 1e-9, y: -1e-9))
        XCTAssertEqual(engine.nodes().count, 4)
    }

    // The empty engine: every method must survive it. `nodes()` is empty,
    // ticking is a no-op that neither crashes nor invents a node, and the
    // 200-tick settle bound holds vacuously rather than hanging awake forever.
    func testEmptyEngineSurvivesEveryMethod() {
        let engine = edgePhysMkEngine([], [])
        XCTAssertTrue(engine.nodes().isEmpty)
        engine.drag(id: 1, to: CGPoint(x: 5, y: 5))
        engine.release(id: 1)
        edgePhysTick(engine, 200)
        XCTAssertTrue(engine.nodes().isEmpty, "an empty engine must not materialize a node")
        XCTAssertFalse(engine.isAwake, "nothing can move, so nothing may keep the loop alive")
    }

    // Links with no nodes at all — the extreme of the drop rule.
    func testLinksWithNoNodesAtAllAreAllDropped() {
        let engine = edgePhysMkEngine([], [edgePhysLink(1, 2), edgePhysLink(2, 2, occurrences: 4)])
        XCTAssertTrue(engine.nodes().isEmpty)
        edgePhysTick(engine, 60)
        XCTAssertTrue(engine.nodes().isEmpty)
    }

    // Obligation (e): "A self-loop (src == dst) neither crashes nor produces
    // non-finite positions." Both alone and beside real links, and at an
    // occurrences value the rest-distance rule (c) reads as "very short".
    func testSelfLoopIsFiniteAloneAndInCompany() {
        let solo = edgePhysMkEngine([edgePhysSeed(1, 7, -3)], [edgePhysLink(1, 1, occurrences: 9)])
        edgePhysTick(solo, 200)
        edgePhysAssertAllFinite(solo, "lone self-loop")

        let mixed = edgePhysMkEngine(
            [edgePhysSeed(1, -40, 10), edgePhysSeed(5, 12, -6), edgePhysSeed(6, 30, 44)],
            [edgePhysLink(1, 1), edgePhysLink(1, 5), edgePhysLink(6, 6, occurrences: 12), edgePhysLink(5, 6)]
        )
        edgePhysTick(mixed, 200)
        edgePhysAssertAllFinite(mixed, "self-loops beside real links")
        XCTAssertEqual(edgePhysIDs(mixed), [1, 5, 6])
    }

    // Obligation (d), at its boundary: one node, no links, nothing to push
    // against. It must stay where it was seeded rather than drift off under an
    // anchoring force computed against a centroid of one — which is itself.
    func testLoneNodeStaysAtItsSeed() {
        let engine = edgePhysMkEngine([edgePhysSeed(42, -18.5, 6.25)])
        edgePhysTick(engine, 200)
        edgePhysAssertAllFinite(engine, "lone node")
        guard let p = edgePhysPosition(engine, 42) else { return XCTFail("node 42 missing") }
        XCTAssertEqual(edgePhysDistance(p, CGPoint(x: -18.5, y: 6.25)), 0, accuracy: 0.5,
                       "a single unlinked node settles at its seed")
    }

    // Two nodes seeded at the SAME point: the classic divide-by-zero site in
    // any repulsion kernel. The contract says nothing about where they end up;
    // it says (f), so that is all this asserts.
    func testCoincidentSeedsDoNotProduceNonFinitePositions() {
        let engine = edgePhysMkEngine(
            [edgePhysSeed(1, 0, 0), edgePhysSeed(2, 0, 0), edgePhysSeed(3, 0, 0)],
            [edgePhysLink(1, 2), edgePhysLink(2, 3)]
        )
        edgePhysTick(engine, 200)
        edgePhysAssertAllFinite(engine, "three nodes seeded on one point")
    }
}

// MARK: - isAwake: the settle floor and the pin

final class EdgePhysicsAwakeTests: XCTestCase {

    // "it MUST go false within 200 ticks with no interaction" — asserted at
    // the boundary the contract names, for the shapes most likely to miss it:
    // empty, one node, and a linked trio.
    func testEverySeedShapeSettlesWithin200Ticks() {
        let empty = edgePhysMkEngine([])
        edgePhysTick(empty, 200)
        XCTAssertFalse(empty.isAwake, "empty")

        let solo = edgePhysMkEngine([edgePhysSeed(1, 3, 4)])
        edgePhysTick(solo, 200)
        XCTAssertFalse(solo.isAwake, "one node")

        let trio = edgePhysTrio()
        edgePhysTick(trio, 200)
        XCTAssertFalse(trio.isAwake, "three nodes, two links")
    }

    // "While a drag pin is held it stays true." Well past both budgets — the
    // 200-tick settle and the 90-tick release — so a pin cannot be a wake that
    // merely decays.
    func testAHeldPinKeepsTheSimulationAwakeIndefinitely() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        engine.drag(id: 5, to: CGPoint(x: 400, y: -250))
        edgePhysTick(engine, 600)
        XCTAssertTrue(engine.isAwake, "a held pin must outlast every settle budget")
        edgePhysAssertAllFinite(engine, "600 ticks under a held pin")
    }

    // "After `release` following a drag it MUST go false within 90 ticks (the
    // <= ~1.5 s budget at 60 fps)." Measured from a FAR drag, which is the
    // slowest case: the glide has the most energy to bleed off.
    func testAReleasedDragSettlesWithin90Ticks() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        engine.drag(id: 1, to: CGPoint(x: 900, y: 900))
        edgePhysTick(engine, 30)
        engine.release(id: 1)
        edgePhysTick(engine, 90)
        XCTAssertFalse(engine.isAwake, "release must settle inside the 90-tick budget")
        edgePhysAssertAllFinite(engine, "after a released far drag")
    }

    // A second release of the same node is not a second wake: the budget is
    // counted from the release that followed the drag, and an idempotent
    // release is the only reading under which that budget is enforceable.
    func testASecondReleaseDoesNotRestartTheBudget() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        engine.drag(id: 1, to: CGPoint(x: 200, y: -140))
        edgePhysTick(engine, 10)
        engine.release(id: 1)
        engine.release(id: 1)
        engine.release(id: 1)
        edgePhysTick(engine, 90)
        XCTAssertFalse(engine.isAwake, "release is idempotent — it is not a wake")
        edgePhysAssertAllFinite(engine, "after three releases of one node")
    }

    // A release with no drag anywhere in the engine's history, on a settled
    // simulation. Nothing to unpin, nothing to glide.
    func testReleaseWithoutAnyDragIsSafeAndDoesNotMoveAnything() {
        let engine = edgePhysTrio()
        edgePhysTick(engine, 200)
        let before = edgePhysPositions(engine)
        engine.release(id: 1)
        engine.release(id: 5)
        engine.release(id: 6)
        edgePhysTick(engine, 1)
        for (id, p) in edgePhysPositions(engine) {
            guard let was = before[id] else { return XCTFail("node \(id) appeared from nowhere") }
            XCTAssertEqual(edgePhysDistance(p, was), 0, accuracy: 1e-6,
                           "node \(id) moved on a release that unpinned nothing")
        }
    }
}

// MARK: - drag / release: unknown ids and other no-ops

final class EdgePhysicsUnknownIDTests: XCTestCase {

    // "Unknown id: complete no-op — no wake, no crash." All three halves:
    // it does not crash, it does not wake, and it does not move a node.
    func testDraggingAnUnknownIDIsACompleteNoOp() {
        let engine = edgePhysTrio()
        edgePhysTick(engine, 200)
        XCTAssertFalse(engine.isAwake, "precondition: settled")
        let before = edgePhysPositions(engine)

        engine.drag(id: 99, to: CGPoint(x: 5_000, y: -5_000))
        engine.drag(id: -1, to: CGPoint(x: 1, y: 1))
        engine.drag(id: Int64.max, to: .zero)
        engine.drag(id: Int64.min, to: .zero)

        XCTAssertFalse(engine.isAwake, "an unknown id must not wake the simulation")
        XCTAssertEqual(edgePhysIDs(engine), [1, 5, 6], "an unknown id must not become a node")
        edgePhysTick(engine, 1)
        for (id, p) in edgePhysPositions(engine) {
            guard let was = before[id] else { return XCTFail("node \(id) appeared from nowhere") }
            XCTAssertEqual(edgePhysDistance(p, was), 0, accuracy: 1e-6,
                           "node \(id) moved for a drag naming nobody")
        }
    }

    // "Unknown id: no-op that does NOT end another node's pin and does not
    // kill an unrelated wake." Both clauses, in one sequence.
    func testReleasingAnUnknownIDNeitherUnpinsNorSleeps() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        let pin = CGPoint(x: 320, y: -180)
        engine.drag(id: 5, to: pin)
        edgePhysTick(engine, 5)

        engine.release(id: 99)
        engine.release(id: Int64.min)
        edgePhysTick(engine, 5)

        XCTAssertTrue(engine.isAwake, "an unrelated release must not kill the pin's wake")
        edgePhysAssertPinned(engine, 5, pin, "the real pin survived a release naming nobody")
    }

    // The same shape one step in: a KNOWN id that is not the pinned one. The
    // contract's "does NOT end another node's pin" is written about unknown
    // ids, so this asserts only the part both readings share — node 5 is still
    // pinned — and says nothing about wake state for a known-but-unpinned id.
    func testReleasingADifferentKnownNodeDoesNotEndTheHeldPin() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        let pin = CGPoint(x: -260, y: 90)
        engine.drag(id: 5, to: pin)
        edgePhysTick(engine, 3)
        engine.release(id: 1)
        engine.release(id: 6)
        edgePhysTick(engine, 3)
        edgePhysAssertPinned(engine, 5, pin, "node 5's pin outlives its neighbours' releases")
    }

    // An unknown id on an engine that has no nodes at all — the two edge
    // cases stacked. Nothing to no-op against.
    //
    // The settle comes FIRST, and that ordering is the assertion's whole
    // meaning: "After init it is awake (settling the seed delta); it MUST go
    // false within 200 ticks with no interaction." An engine still inside that
    // window is contractually awake whatever the drag did, so only a settled
    // engine can testify that the unknown id failed to wake it — the same
    // shape `testDraggingAnUnknownIDIsACompleteNoOp` uses.
    func testUnknownIDsAgainstAnEmptyEngine() {
        let engine = edgePhysMkEngine([])
        edgePhysSettle(engine)
        XCTAssertFalse(engine.isAwake, "precondition: settled within the 200-tick bound")

        engine.drag(id: 7, to: CGPoint(x: 12, y: 12))
        XCTAssertFalse(engine.isAwake, "a drag naming nobody, in an engine holding nobody")
        edgePhysTick(engine, 10)
        XCTAssertFalse(engine.isAwake, "and it stays asleep across the ticks that follow")

        engine.release(id: 7)
        edgePhysTick(engine, 10)
        XCTAssertFalse(engine.isAwake, "a release naming nobody is no wake either")
        XCTAssertTrue(engine.nodes().isEmpty)
    }
}

// MARK: - drag: the pin, the extremes, the glide

final class EdgePhysicsDragTests: XCTestCase {

    // "after `tick()`, that node's position IS the pinned point exactly (it
    // never lags or springs)" — held across many ticks and across a MOVING
    // pin, which is what a real finger does.
    func testThePinIsExactOnEveryTickAndFollowsAMovingFinger() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        let path = [
            CGPoint(x: 100, y: 0), CGPoint(x: 140, y: 30), CGPoint(x: 220, y: 95),
            CGPoint(x: 221, y: 95), CGPoint(x: -400, y: -400),
        ]
        for p in path {
            engine.drag(id: 1, to: p)
            engine.tick()
            edgePhysAssertPinned(engine, 1, p, "pin must not lag toward \(p)")
        }
        // And it stays exact while the finger is still.
        edgePhysTick(engine, 40)
        edgePhysAssertPinned(engine, 1, CGPoint(x: -400, y: -400), "a stationary pin does not spring back")
    }

    // A zero-delta drag — a finger that pressed and did not move — is still a
    // drag: "Wakes the simulation" carries no exception for it.
    func testDraggingANodeToItsOwnCurrentPositionStillWakesAndPins() {
        let engine = edgePhysTrio()
        edgePhysTick(engine, 200)
        XCTAssertFalse(engine.isAwake, "precondition: settled")
        guard let here = edgePhysPosition(engine, 6) else { return XCTFail("node 6 missing") }
        engine.drag(id: 6, to: here)
        XCTAssertTrue(engine.isAwake, "a drag wakes the simulation even with no displacement")
        engine.tick()
        edgePhysAssertPinned(engine, 6, here)
    }

    // Extreme (but finite) world coordinates, held and then released. The
    // repulsion kernel and the anchoring both see a distance no real layout
    // produces; (f) still holds, and the 90-tick release budget still applies.
    func testAnExtremeDragKeepsEveryPositionFiniteAndStillSettles() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        engine.drag(id: 1, to: CGPoint(x: 1e7, y: -1e7))
        edgePhysTick(engine, 60)
        edgePhysAssertAllFinite(engine, "under a 1e7 pin")
        edgePhysAssertPinned(engine, 1, CGPoint(x: 1e7, y: -1e7), "the pin is exact at any magnitude")
        engine.release(id: 1)
        edgePhysTick(engine, 90)
        edgePhysAssertAllFinite(engine, "after releasing a 1e7 pin")
        XCTAssertFalse(engine.isAwake, "even an extreme release owes the 90-tick budget")
    }

    // The last `drag` call names the node the contract's sentence is about; a
    // second pin elsewhere cannot make the first one lag. What happens to the
    // EARLIER pin is contract-silent, so nothing here asserts it.
    func testASecondPinDoesNotBreakTheContractOfTheOneJustSet() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        engine.drag(id: 1, to: CGPoint(x: 200, y: 0))
        engine.drag(id: 5, to: CGPoint(x: -200, y: 0))
        engine.tick()
        edgePhysAssertPinned(engine, 5, CGPoint(x: -200, y: 0))
        XCTAssertTrue(engine.isAwake)
        edgePhysAssertAllFinite(engine, "two pins held at once")
    }

    // "on the tick after a moving release the node's position continues PAST
    // the release point, it does not freeze there." This asserts the half the
    // contract states negatively — the node does not stop dead — and leaves
    // the exact direction of travel to the sibling's happy-path file.
    func testAMovingReleaseDoesNotFreezeTheNodeWhereItLetGo() {
        let engine = edgePhysTrio()
        edgePhysSettle(engine)
        // Move the pin every tick, so there is a real drag-imparted velocity
        // to keep. A single drag call followed by ticks is a STATIONARY pin.
        var p = CGPoint(x: 0, y: 0)
        for _ in 0..<12 {
            p = CGPoint(x: p.x + 20, y: p.y + 12)
            engine.drag(id: 1, to: p)
            engine.tick()
        }
        engine.release(id: 1)
        engine.tick()
        guard let after = edgePhysPosition(engine, 1) else { return XCTFail("node 1 missing") }
        XCTAssertGreaterThan(edgePhysDistance(after, p), 0,
                             "a moving release glides; it does not freeze at the release point")
        XCTAssertTrue(after.x.isFinite && after.y.isFinite)
    }

    // Release taken mid-flight — while the simulation is still settling its
    // seed delta rather than from a quiet start — is the same rule under a
    // different alpha. Only (f) and the budget are asserted.
    func testReleaseMidFlightIsFiniteAndStillSettles() {
        let engine = edgePhysTrio()
        edgePhysTick(engine, 3) // still awake: the seed delta has not settled
        engine.drag(id: 6, to: CGPoint(x: -500, y: 260))
        engine.tick()
        engine.release(id: 6)
        edgePhysAssertAllFinite(engine, "immediately after a mid-flight release")
        edgePhysTick(engine, 200)
        edgePhysAssertAllFinite(engine, "200 ticks after a mid-flight release")
        XCTAssertFalse(engine.isAwake)
    }

    // A long, adversarial mixed sequence: every method, in every order the
    // gesture layer can produce, including pins on nodes that are also link
    // endpoints and self-loops. One assertion — (f) — and it covers the
    // sentence "after any sequence of the above calls".
    func testALongMixedCallSequenceNeverProducesANonFinitePosition() {
        let engine = edgePhysMkEngine(
            [
                edgePhysSeed(1, 0, 0), edgePhysSeed(2, -3, 4), edgePhysSeed(3, 1e5, 1e5),
                edgePhysSeed(4, -1e5, 0), edgePhysSeed(5, 0.001, -0.001),
            ],
            [
                edgePhysLink(1, 2, occurrences: 9), edgePhysLink(2, 3), edgePhysLink(3, 3),
                edgePhysLink(4, 1, occurrences: 1), edgePhysLink(5, 5, occurrences: 3),
                edgePhysLink(1, 6), // dangling
            ]
        )
        var flip = 0
        for step in 0..<400 {
            switch step % 7 {
            case 0: engine.drag(id: Int64(1 + flip % 5), to: CGPoint(x: CGFloat(step) * 37, y: -CGFloat(step) * 11))
            case 3: engine.release(id: Int64(1 + flip % 5))
            case 5: engine.drag(id: 99, to: CGPoint(x: 1e9, y: 1e9)); engine.release(id: 99)
            default: break
            }
            engine.tick()
            flip += step % 3
        }
        edgePhysAssertAllFinite(engine, "400 mixed drag/release/tick steps")
        XCTAssertEqual(edgePhysIDs(engine), [1, 2, 3, 4, 5])
    }

    // Occurrences outside the range rule (c) describes. The contract fixes the
    // ORDER of two settled distances for 1 vs 9; it fixes nothing for 0, for a
    // negative, or for a saturating value — so only (f) is asserted, which is
    // exactly where a `log(0)` or an overflow would surface.
    func testDegenerateOccurrenceCountsStayFinite() {
        for occ in [Int64(0), -1, -1_000_000, 1_000_000, Int64.max, Int64.min] {
            let engine = edgePhysMkEngine(
                [edgePhysSeed(1, -30, 0), edgePhysSeed(2, 30, 0)],
                [edgePhysLink(1, 2, occurrences: occ)]
            )
            edgePhysTick(engine, 200)
            edgePhysAssertAllFinite(engine, "occurrences = \(occ)")
        }
    }

    // Many parallel links between one pair — the payload can carry several
    // facts joining the same two entities. Semantics are contract-silent;
    // finiteness is not.
    func testManyParallelLinksBetweenOnePairStayFinite() {
        let links = (0..<64).map { _ in edgePhysLink(1, 2, occurrences: 9) }
        let engine = edgePhysMkEngine([edgePhysSeed(1, -30, 0), edgePhysSeed(2, 30, 0)], links)
        edgePhysTick(engine, 200)
        edgePhysAssertAllFinite(engine, "64 parallel links")
    }
}

// MARK: - graphPhysicsAllowed

final class EdgePhysicsAllowedTests: XCTestCase {

    // "`false` when `reduceMotion`; else `visibleNodes <= 3000`." The `<=`
    // lives entirely in these three calls: 3000 is allowed, 3001 is not.
    func testTheCapIncludesThreeThousandAndStopsImmediatelyAfter() {
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 2999, reduceMotion: false))
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 3000, reduceMotion: false))
        XCTAssertFalse(graphPhysicsAllowed(visibleNodes: 3001, reduceMotion: false))
    }

    // reduceMotion dominates the whole domain — the cap never rescues it.
    func testReduceMotionRefusesEveryCount() {
        for n in [Int.min, -1, 0, 1, 2999, 3000, 3001, 10_000, Int.max] {
            XCTAssertFalse(graphPhysicsAllowed(visibleNodes: n, reduceMotion: true),
                           "reduce-motion must refuse \(n)")
        }
    }

    // The full grid's other row, including the counts that are not real: an
    // empty screen (0) and a negative, both of which satisfy `<= 3000` and so
    // are ALLOWED — a nonsense count must not be a second, unwritten refusal.
    func testTheAllowedRowIncludesEmptyAndNonsenseCounts() {
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 0, reduceMotion: false))
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 1, reduceMotion: false))
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: -1, reduceMotion: false))
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: Int.min, reduceMotion: false))
        XCTAssertFalse(graphPhysicsAllowed(visibleNodes: 10_000, reduceMotion: false))
        XCTAssertFalse(graphPhysicsAllowed(visibleNodes: Int.max, reduceMotion: false))
    }

    // Pure: same inputs, same answer, no state anywhere behind it.
    func testTheGateIsPure() {
        for _ in 0..<3 {
            XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 3000, reduceMotion: false))
            XCTAssertFalse(graphPhysicsAllowed(visibleNodes: 3000, reduceMotion: true))
        }
    }
}

// ============================================================================
// MARK: - ASSUMED NAMES — the view-model slice
// ============================================================================
//
// Everything below this line tests slice 2 ("view-model + gesture
// integration"), whose obligations the contract states in PROSE and for which
// it freezes NO symbol. It names the observations — "expose the decision as a
// testable function taking (start point, positions, camera) -> `.node(id)` or
// `.camera`", "the driver (exposed as a testable 'should the loop continue'
// predicate...)" — without naming one symbol that carries them.
//
// So the five names below are ASSUMPTIONS, not contract text, and they are
// funnelled through the shims in this one region precisely so that a rename is
// a five-line edit rather than a rewrite. They are reported to the lead as a
// contract silence; if the implementation spells them differently, the fix is
// mechanical and belongs here, not in the assertions.
//
//   1. GraphViewModel.tickPhysics()                 — advance one frame
//   2. GraphViewModel.physicsShouldTick   -> Bool   — "should the loop continue"
//   3. GraphViewModel.dragNode(id:to:)              — world-point drag
//   4. GraphViewModel.releaseNode(id:)              — end that drag
//   5. GraphViewModel.setPhysicsEnabled(_:)         — the reduce-motion / cap gate
//   6. graphDragTarget(start:positions:camera:) -> GraphDragTarget{.node(Int64),.camera}
//
// The ASSERTIONS in this region are contract text; only the spelling is a
// guess.

private let edgePhysBaseURL = "http://mac.local:7777"

@MainActor private func edgePhysDrive(_ vm: GraphViewModel, _ frames: Int) {
    for _ in 0..<frames { vm.tickPhysics() }
}

@MainActor private func edgePhysShouldTick(_ vm: GraphViewModel) -> Bool {
    vm.physicsShouldTick
}

@MainActor private func edgePhysVMDrag(_ vm: GraphViewModel, _ id: Int64, _ to: CGPoint) {
    vm.dragNode(id: id, to: to)
}

@MainActor private func edgePhysVMRelease(_ vm: GraphViewModel, _ id: Int64) {
    vm.releaseNode(id: id)
}

@MainActor private func edgePhysSetEnabled(_ vm: GraphViewModel, _ enabled: Bool) {
    vm.setPhysicsEnabled(enabled)
}

/// Drives the loop the way the display link would: until the driver says stop,
/// capped. Returns the frames spent so a caller asserts a stated bound.
@discardableResult
@MainActor private func edgePhysDriveUntilStopped(_ vm: GraphViewModel, max: Int = 400) -> Int {
    var spent = 0
    while spent < max {
        if !edgePhysShouldTick(vm) { return spent }
        vm.tickPhysics()
        spent += 1
    }
    return spent
}

private struct EdgePhysStubError: Error {}

private func edgePhysNode(_ id: Int64, _ type: String, x: Double, y: Double) -> GraphNode {
    GraphNode(id: id, name: "n\(id)", type: type, description: "", x: x, y: y, aliases: nil)
}

private func edgePhysEdge(_ id: Int64, _ src: Int64, _ dst: Int64) -> GraphEdge {
    GraphEdge(id: id, src: src, dst: dst, predicate: "relates-to", fact: "a fact",
              strength: 1, occurrences: 1, validUntil: 0, expiredAt: 0, episode: 1)
}

private func edgePhysPayload(nodes: [GraphNode], edges: [GraphEdge]) -> GraphPayload {
    GraphPayload(
        nodes: nodes, edges: edges,
        stats: GraphStats(nodes: Int64(nodes.count), edges: Int64(edges.count),
                          liveEdges: Int64(edges.count), episodes: 0, pending: 0,
                          missingEmbeddings: 0)
    )
}

@MainActor private func edgePhysMkVM(_ payload: GraphPayload?) -> GraphViewModel {
    GraphViewModel(
        fetchGraph: { _ in
            guard let payload else { throw EdgePhysStubError() }
            return payload
        },
        fetchNode: { _, _ in throw EdgePhysStubError() }
    )
}

/// The board payload every view-model test below loads: three types, so a chip
/// toggle can hide exactly one node.
private func edgePhysBoard() -> GraphPayload {
    edgePhysPayload(
        nodes: [
            edgePhysNode(1, "person", x: -40, y: 10),
            edgePhysNode(5, "component", x: 12, y: -6),
            edgePhysNode(6, "convention", x: 30, y: 44),
        ],
        edges: [edgePhysEdge(4, 1, 5), edgePhysEdge(5, 5, 6)]
    )
}

@MainActor
final class EdgePhysicsViewModelBoundaryTests: XCTestCase {

    // "Filtered-out nodes (hidden types) ... are ABSENT from the simulation —
    // a hidden element exerts no force. Restoring a filter re-inserts at last
    // simulated position."
    //
    // Both halves, in one sequence: while hidden the node cannot move (absent
    // from the simulation), and on restore it is back exactly where it was —
    // NOT at the server's seed, which is what a naive re-seed would give.
    func testAHiddenNodeIsFrozenAndReturnsWhereItLeft() async {
        let vm = edgePhysMkVM(edgePhysBoard())
        await vm.load(baseURL: edgePhysBaseURL)

        // Put node 6 somewhere the server payload plainly did not.
        edgePhysVMDrag(vm, 6, CGPoint(x: 500, y: -300))
        edgePhysDrive(vm, 5)
        edgePhysVMRelease(vm, 6)
        edgePhysDriveUntilStopped(vm)

        guard let parked = vm.render.positions[6] else { return XCTFail("node 6 has no position") }
        XCTAssertGreaterThan(edgePhysDistance(parked, CGPoint(x: 30, y: 44)), 1,
                             "precondition: the simulated position has left the server seed")

        vm.hiddenTypes = ["convention"]
        edgePhysDrive(vm, 120)
        guard let whileHidden = vm.render.positions[6] else {
            return XCTFail("a hidden node still has a last-known position")
        }
        XCTAssertEqual(edgePhysDistance(whileHidden, parked), 0, accuracy: 1e-6,
                       "a hidden node exerts no force and receives none — it must not move")

        vm.hiddenTypes = []
        guard let restored = vm.render.positions[6] else { return XCTFail("node 6 vanished on restore") }
        XCTAssertEqual(edgePhysDistance(restored, parked), 0, accuracy: 1e-6,
                       "restoring a filter re-inserts at the LAST SIMULATED position, not the seed")
    }

    // "With physics OFF (`graphPhysicsAllowed` false — reduce-motion or
    // over-cap), a node-drag still moves THAT ONE node (position override, no
    // simulation, no cascade)." Three assertions: the node moves to the point,
    // nothing else moves, and nothing wakes.
    func testPhysicsOffDragMovesExactlyOneNodeAndWakesNothing() async {
        let vm = edgePhysMkVM(edgePhysBoard())
        edgePhysSetEnabled(vm, false)
        await vm.load(baseURL: edgePhysBaseURL)

        let before = vm.render.positions
        XCTAssertFalse(edgePhysShouldTick(vm), "physics off: the loop never starts")

        let target = CGPoint(x: 777, y: -333)
        edgePhysVMDrag(vm, 5, target)

        guard let moved = vm.render.positions[5] else { return XCTFail("node 5 has no position") }
        XCTAssertEqual(edgePhysDistance(moved, target), 0, accuracy: 1e-6,
                       "with physics off the drag IS the position — an override, not a force")
        for id in [Int64(1), Int64(6)] {
            guard let now = vm.render.positions[id], let was = before[id] else {
                return XCTFail("node \(id) has no position")
            }
            XCTAssertEqual(edgePhysDistance(now, was), 0, accuracy: 1e-6,
                           "node \(id) moved: a physics-off drag must not cascade")
        }
        XCTAssertFalse(edgePhysShouldTick(vm), "a physics-off drag wakes nothing")

        edgePhysVMRelease(vm, 5)
        XCTAssertFalse(edgePhysShouldTick(vm), "and the release wakes nothing either")
        guard let afterRelease = vm.render.positions[5] else { return XCTFail("node 5 lost its position") }
        XCTAssertEqual(edgePhysDistance(afterRelease, target), 0, accuracy: 1e-6,
                       "with no simulation there is no glide to carry it away")
    }

    // "The tick driver stops when `isAwake` is false and no drag is held — an
    // idle screen schedules no per-frame work."
    func testTheDriverStopsAfterSettleAndRestartsOnlyForADrag() async {
        let vm = edgePhysMkVM(edgePhysBoard())
        await vm.load(baseURL: edgePhysBaseURL)

        let spent = edgePhysDriveUntilStopped(vm)
        XCTAssertLessThan(spent, 400, "the driver must actually stop, not run to the cap")
        XCTAssertFalse(edgePhysShouldTick(vm), "an idle screen schedules no per-frame work")

        edgePhysVMDrag(vm, 1, CGPoint(x: 260, y: 120))
        XCTAssertTrue(edgePhysShouldTick(vm), "a held drag is per-frame work")
        edgePhysDrive(vm, 30)
        XCTAssertTrue(edgePhysShouldTick(vm), "and it stays that way while the finger is down")

        edgePhysVMRelease(vm, 1)
        let after = edgePhysDriveUntilStopped(vm)
        XCTAssertLessThan(after, 400, "the driver stops again after the release settles")
        XCTAssertFalse(edgePhysShouldTick(vm))
    }

    // The empty screen: no nodes at all. Every method must survive a payload
    // with nothing in it, and the driver must STOP — the obligation is
    // "stops when `isAwake` is false and no drag is held", and a driver still
    // running inside the engine's own 200-tick settle window is honouring that
    // sentence rather than breaking it. So the loop is driven to its stop
    // (bounded) and the assertion is that it stopped, not that it never ran.
    func testAnEmptyPayloadSchedulesNoWorkAndSurvivesEveryCall() async {
        let vm = edgePhysMkVM(edgePhysPayload(nodes: [], edges: []))
        await vm.load(baseURL: edgePhysBaseURL)
        XCTAssertTrue(vm.render.positions.isEmpty)

        edgePhysVMDrag(vm, 1, CGPoint(x: 10, y: 10))
        edgePhysVMRelease(vm, 1)
        let spent = edgePhysDriveUntilStopped(vm)
        XCTAssertLessThan(spent, 400, "an empty screen must reach a stop, not run to the cap")
        XCTAssertTrue(vm.render.positions.isEmpty, "an empty payload must not gain a node")
        XCTAssertFalse(edgePhysShouldTick(vm), "nothing to move, nothing to schedule")
    }

    // A drag naming a node the payload does not carry — the view model's own
    // version of the engine's unknown-id rule, reachable whenever a hit-test
    // result outlives the poll that produced it.
    func testAVMDragNamingAnAbsentNodeMovesNothing() async {
        let vm = edgePhysMkVM(edgePhysBoard())
        await vm.load(baseURL: edgePhysBaseURL)
        edgePhysDriveUntilStopped(vm)
        let before = vm.render.positions

        edgePhysVMDrag(vm, 404, CGPoint(x: 900, y: 900))
        edgePhysVMRelease(vm, 404)

        XCTAssertNil(vm.render.positions[404], "an unknown id must not become a node")
        for (id, was) in before {
            guard let now = vm.render.positions[id] else { return XCTFail("node \(id) vanished") }
            XCTAssertEqual(edgePhysDistance(now, was), 0, accuracy: 1e-6,
                           "node \(id) moved for a drag naming nobody")
        }
    }
}

@MainActor
final class EdgePhysicsDragTargetTests: XCTestCase {

    // "A drag gesture that begins ON a node (within the existing
    // `graphTapReach` hit-test at the gesture's start location) moves that
    // node; one that begins on empty canvas pans the camera exactly as today."
    //
    // Authored blind against HEAD, where `graphTapReach` was `private` in
    // GraphView.swift and unnameable from a test; the implementation slice
    // then moved it to GraphModel.swift as `internal` precisely to make this
    // fork testable. The literal stays as the blind file's own record of the
    // committed constant (lead-corrected explanation, post-arbitration —
    // assertions untouched); `graphTapReach` is what production passes.
    private let reach: CGFloat = 24

    func testAnEmptyCanvasPansTheCamera() {
        let camera = GraphCamera(scale: 1, offset: .zero)
        XCTAssertEqual(graphDragTarget(start: .zero, positions: [:], camera: camera), .camera,
                       "no nodes at all: there is nothing to grab")
        XCTAssertEqual(
            graphDragTarget(start: CGPoint(x: 5, y: 5), positions: [1: CGPoint(x: 900, y: 900)], camera: camera),
            .camera,
            "a node far outside reach is empty canvas"
        )
    }

    func testTheReachBoundaryIsInclusive() {
        let camera = GraphCamera(scale: 1, offset: .zero)
        let positions: [Int64: CGPoint] = [7: .zero]
        XCTAssertEqual(graphDragTarget(start: .zero, positions: positions, camera: camera), .node(7),
                       "dead centre")
        XCTAssertEqual(
            graphDragTarget(start: CGPoint(x: reach, y: 0), positions: positions, camera: camera),
            .node(7), "exactly at reach is still ON the node"
        )
        XCTAssertEqual(
            graphDragTarget(start: CGPoint(x: reach + 0.5, y: 0), positions: positions, camera: camera),
            .camera, "half a point past reach is canvas"
        )
    }

    // Reach is a SCREEN distance, so the camera decides how much world it
    // covers. This is the whole reason the function takes a camera.
    //
    // `start` is a gesture location, i.e. a SCREEN point — `graphHitTest`, the
    // "existing `graphTapReach` hit-test" the contract defines this by, takes a
    // `screenPoint` and measures "Nearest-in-SCREEN-space within maxDistance".
    // So the node must sit somewhere OTHER than the world origin, or scale
    // moves its screen position by zero and the fixture discriminates nothing.
    // Node at world (100, 0), pressed at screen (110, 0) throughout:
    //   scale 1.0 → node draws at screen (100, 0) → 10pt away → on the node
    //   scale 0.1 → node draws at screen  (10, 0) → 100pt away → empty canvas
    func testReachIsMeasuredInScreenSpaceNotWorldSpace() {
        let positions: [Int64: CGPoint] = [7: CGPoint(x: 100, y: 0)]
        let start = CGPoint(x: 110, y: 0)
        XCTAssertEqual(
            graphDragTarget(start: start, positions: positions,
                            camera: GraphCamera(scale: 1, offset: .zero)),
            .node(7), "10pt away on screen at scale 1 is inside reach"
        )
        XCTAssertEqual(
            graphDragTarget(start: start, positions: positions,
                            camera: GraphCamera(scale: 0.1, offset: .zero)),
            .camera, "the same press is 100pt from the node once it is zoomed out"
        )
    }

    // The camera's offset is part of the transform, so a pan must move what a
    // press lands on. Without this, a panned canvas grabs the wrong node.
    func testThePanOffsetMovesWhatAPressLandsOn() {
        let positions: [Int64: CGPoint] = [7: .zero]
        let panned = GraphCamera(scale: 1, offset: CGSize(width: 300, height: -100))
        XCTAssertEqual(graphDragTarget(start: .zero, positions: positions, camera: panned), .camera,
                       "the node is no longer under the origin")
        XCTAssertEqual(
            graphDragTarget(start: CGPoint(x: 300, y: -100), positions: positions, camera: panned),
            .node(7), "it is under the offset instead"
        )
    }

    // A degenerate camera: scale 0 has no inverse, which GraphCamera already
    // guards in `screenToWorld`. The decision must still answer — a broken
    // camera is a pan, never a crash and never a node grabbed at random.
    func testAZeroScaleCameraDoesNotCrash() {
        let broken = GraphCamera(scale: 0, offset: .zero)
        let target = graphDragTarget(start: CGPoint(x: 10, y: 10),
                                     positions: [1: CGPoint(x: 5, y: 5), 2: CGPoint(x: -5, y: -5)],
                                     camera: broken)
        // Both nodes collapse onto the offset at scale 0, so WHICH one wins is
        // not something the contract fixes; that it answers at all is.
        XCTAssertTrue(target == .camera || target == .node(1) || target == .node(2),
                      "a degenerate camera must still yield one of the two decisions")
    }
}
