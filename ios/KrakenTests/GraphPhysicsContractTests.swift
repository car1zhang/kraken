import XCTest
import Foundation
import CoreGraphics
@testable import Kraken

// GraphPhysicsContractTests — blind tests for the native iOS physics + drag
// slice, authored from artifacts/50-contract.md ALONE, before any of the code
// they exercise existed. Dimension: the STATED INVARIANTS AND HAPPY PATHS —
// seed fidelity, pin-exactly, the glide, linked-follow, occurrence-scaled rest
// distance, determinism, the isAwake lifecycle, the Barnes-Hut timing pin,
// graphPhysicsAllowed's table, and the view-model obligations' happy half.
// A sibling author owns boundaries and refusals (unknown ids, self-loops,
// non-finite inputs, empty inputs); nothing here duplicates that lane, and
// where the contract is silent this file asserts nothing.
//
// Three deliberate authoring choices, all about surviving a blind join:
//
//  1. Nothing is compared by a literal coordinate the contract did not fix.
//     Every force assertion is a RELATION the contract states in words — "more
//     than 3x", "strictly closer", "past the release point" — never a tuned
//     number, because the contract freezes the feel and not the constants
//     (it names physics.ts as "the reference implementation of the constants,
//     NOT of the API").
//  2. `GraphPhysicsNode` is not declared Equatable in the frozen signature, so
//     positions are read field by field through `contractPhysicsPositions`
//     rather than by comparing the structs. Likewise `nodes()` is promised
//     "every node given at init, stable ids" and NOT an order, so every
//     lookup is by id and no test indexes the array.
//  3. The view-model section of the contract states its obligations but names
//     no symbol for driving the engine from the view model (no tick-driver
//     method, no `isAwake` accessor, no gesture-decision function name — see
//     the report for the exact lines). Rather than invent one and hand the
//     join a file that does not compile, the view-model tests below pin the
//     half that IS reachable through the committed `GraphViewModel` surface:
//     position PRESERVATION across an unchanged reload, and a NEW node
//     entering at its server position across a changed one. The simulated-vs-
//     server divergence half is flagged in the author report as unfrozen.
//
// Every file-scope identifier here is `contractPhysics`-prefixed (types
// `ContractPhysics`-) because two other files share this test target's
// namespace.

// MARK: - Engine helpers

private func contractPhysicsEngine(
    nodes: [(id: Int64, x: CGFloat, y: CGFloat, radius: CGFloat)],
    links: [(src: Int64, dst: Int64, occurrences: Int64)] = []
) -> GraphPhysics {
    GraphPhysics(nodes: nodes, links: links)
}

/// Current positions keyed by id. Reads through `nodes()`, which is the only
/// position accessor the contract freezes, and never assumes an array order.
private func contractPhysicsPositions(_ engine: GraphPhysics) -> [Int64: CGPoint] {
    var out: [Int64: CGPoint] = [:]
    for n in engine.nodes() { out[n.id] = CGPoint(x: n.x, y: n.y) }
    return out
}

/// Ticks until the simulation reports itself asleep, at most `maxTicks` times.
/// Returns how many ticks it actually took — the caller asserts the CEILING
/// the contract states, so a settle that never arrives fails on `isAwake`
/// rather than hanging.
@discardableResult
private func contractPhysicsSettle(_ engine: GraphPhysics, maxTicks: Int) -> Int {
    var ticks = 0
    while ticks < maxTicks {
        if !engine.isAwake { return ticks }
        engine.tick()
        ticks += 1
    }
    return ticks
}

private func contractPhysicsTick(_ engine: GraphPhysics, _ count: Int) {
    for _ in 0..<count { engine.tick() }
}

private func contractPhysicsDistance(_ a: CGPoint, _ b: CGPoint) -> CGFloat {
    hypot(a.x - b.x, a.y - b.y)
}

/// Obligation (f), asserted alongside every happy path rather than as a test
/// of its own: no position is ever NaN or infinite.
private func contractPhysicsAssertFinite(
    _ engine: GraphPhysics, _ what: String,
    file: StaticString = #filePath, line: UInt = #line
) {
    for n in engine.nodes() {
        XCTAssertTrue(
            n.x.isFinite && n.y.isFinite,
            "\(what): node \(n.id) at (\(n.x), \(n.y)) is not finite",
            file: file, line: line
        )
    }
}

// MARK: - Seeding

final class ContractPhysicsSeedTests: XCTestCase {

    // "Seeds EXACTLY at the given positions — nodes() immediately after init
    // returns them unchanged (warm start; no random scatter anywhere, ever)."
    func testInitSeedsExactlyAtTheGivenPositions() {
        let seeds: [(id: Int64, x: CGFloat, y: CGFloat, radius: CGFloat)] = [
            (id: 1, x: -103.4903506081596, y: 79.87158754979711, radius: 8),
            (id: 5, x: -75.18157241820735, y: 21.115214853358946, radius: 12),
            (id: 6, x: 250.5, y: -0.25, radius: 4),
        ]
        let engine = contractPhysicsEngine(nodes: seeds)
        let got = contractPhysicsPositions(engine)

        XCTAssertEqual(Set(got.keys), [1, 5, 6], "nodes() returns every node given at init")
        for s in seeds {
            XCTAssertEqual(got[s.id]?.x, s.x, "node \(s.id) x is the seed, untouched")
            XCTAssertEqual(got[s.id]?.y, s.y, "node \(s.id) y is the seed, untouched")
        }
    }

    // "Membership is the caller's: only the given nodes exist in the
    // simulation." A link's endpoints do not conjure nodes.
    func testOnlyTheGivenNodesExist() {
        let engine = contractPhysicsEngine(
            nodes: [(id: 1, x: 0, y: 0, radius: 8), (id: 2, x: 40, y: 0, radius: 8)],
            links: [(src: 1, dst: 2, occurrences: 1)]
        )
        XCTAssertEqual(engine.nodes().count, 2)
        XCTAssertEqual(Set(engine.nodes().map(\.id)), [1, 2])
    }

    // Ids survive the simulation: the same set comes back after ticking, and
    // no node is dropped or renumbered.
    func testIdsAreStableAcrossTicks() {
        let engine = contractPhysicsEngine(
            nodes: [
                (id: 11, x: 10, y: 10, radius: 6),
                (id: 22, x: -30, y: 5, radius: 6),
                (id: 33, x: 4, y: -90, radius: 6),
            ],
            links: [(src: 11, dst: 22, occurrences: 3)]
        )
        contractPhysicsTick(engine, 50)
        XCTAssertEqual(Set(engine.nodes().map(\.id)), [11, 22, 33])
        contractPhysicsAssertFinite(engine, "after 50 ticks")
    }
}

// MARK: - isAwake lifecycle

final class ContractPhysicsWakeTests: XCTestCase {

    // "After init it is awake (settling the seed delta)."
    func testAFreshEngineIsAwake() {
        let engine = contractPhysicsEngine(
            nodes: [(id: 1, x: 0, y: 0, radius: 8), (id: 2, x: 300, y: 120, radius: 8)],
            links: [(src: 1, dst: 2, occurrences: 1)]
        )
        XCTAssertTrue(engine.isAwake, "a seeded engine has a delta to settle")
    }

    // "it MUST go false within 200 ticks with no interaction."
    func testSettlesWithinTwoHundredTicksFromInit() {
        let engine = contractPhysicsEngine(
            nodes: [
                (id: 1, x: 0, y: 0, radius: 8),
                (id: 2, x: 300, y: 120, radius: 8),
                (id: 3, x: -250, y: 60, radius: 8),
                (id: 4, x: 40, y: -400, radius: 8),
            ],
            links: [(src: 1, dst: 2, occurrences: 1), (src: 2, dst: 3, occurrences: 4)]
        )
        let ticks = contractPhysicsSettle(engine, maxTicks: 200)
        XCTAssertFalse(engine.isAwake, "still awake after \(ticks) ticks; the ceiling is 200")
        contractPhysicsAssertFinite(engine, "after settling from init")
    }

    // "While a drag pin is held it stays true." Held well past both settle
    // ceilings, so a pin cannot be outlived by the alpha floor.
    func testAHeldPinKeepsTheSimulationAwakeIndefinitely() {
        let engine = contractPhysicsEngine(
            nodes: [(id: 1, x: 0, y: 0, radius: 8), (id: 2, x: 60, y: 0, radius: 8)],
            links: [(src: 1, dst: 2, occurrences: 1)]
        )
        engine.drag(id: 1, to: CGPoint(x: 400, y: 25))
        contractPhysicsTick(engine, 400)
        XCTAssertTrue(engine.isAwake, "a held pin is a reason to keep ticking")
        contractPhysicsAssertFinite(engine, "under a long held pin")
    }

    // "Wakes the simulation." — drag on a settled engine.
    func testDragWakesASettledSimulation() {
        let engine = contractPhysicsEngine(
            nodes: [(id: 1, x: 0, y: 0, radius: 8), (id: 2, x: 60, y: 0, radius: 8)],
            links: [(src: 1, dst: 2, occurrences: 1)]
        )
        contractPhysicsSettle(engine, maxTicks: 200)
        XCTAssertFalse(engine.isAwake, "precondition: settled")

        engine.drag(id: 1, to: CGPoint(x: 220, y: -40))
        XCTAssertTrue(engine.isAwake, "drag wakes the simulation")
    }

    // "After `release` following a drag it MUST go false within 90 ticks
    // (the <= ~1.5 s budget at 60 fps)."
    func testSettlesWithinNinetyTicksAfterRelease() {
        let engine = contractPhysicsEngine(
            nodes: [
                (id: 1, x: 0, y: 0, radius: 8),
                (id: 2, x: 60, y: 0, radius: 8),
                (id: 3, x: -60, y: 30, radius: 8),
            ],
            links: [(src: 1, dst: 2, occurrences: 1), (src: 1, dst: 3, occurrences: 2)]
        )
        contractPhysicsSettle(engine, maxTicks: 200)

        // A real drag: a moving pin, several frames of it.
        for step in 1...20 {
            engine.drag(id: 1, to: CGPoint(x: CGFloat(step) * 30, y: 0))
            engine.tick()
        }
        engine.release(id: 1)

        let ticks = contractPhysicsSettle(engine, maxTicks: 90)
        XCTAssertFalse(engine.isAwake, "still awake \(ticks) ticks after release; the ceiling is 90")
        contractPhysicsAssertFinite(engine, "after settling from a release")
    }
}

// MARK: - Drag and the glide

final class ContractPhysicsDragTests: XCTestCase {

    // "Pins the node AT the given world point: after tick(), that node's
    // position IS the pinned point exactly (it never lags or springs)."
    func testAPinnedNodeIsAtThePinnedPointExactly() {
        let engine = contractPhysicsEngine(
            nodes: [
                (id: 1, x: 0, y: 0, radius: 8),
                (id: 2, x: 60, y: 0, radius: 8),
                (id: 3, x: -30, y: 45, radius: 8),
            ],
            links: [(src: 1, dst: 2, occurrences: 5), (src: 1, dst: 3, occurrences: 1)]
        )
        let pin = CGPoint(x: 512.5, y: -333.25)
        engine.drag(id: 1, to: pin)
        engine.tick()

        let p = contractPhysicsPositions(engine)[1]
        XCTAssertEqual(p?.x, pin.x, "a pin does not lag on x")
        XCTAssertEqual(p?.y, pin.y, "a pin does not lag on y")
    }

    // The same, held across many frames and moved between them: every tick
    // leaves the node exactly where the finger last put it, so link tension
    // from a heavily-linked neighbourhood can never pull it off the finger.
    func testAMovingPinStaysExactOnEveryFrame() {
        let engine = contractPhysicsEngine(
            nodes: [
                (id: 1, x: 0, y: 0, radius: 8),
                (id: 2, x: 60, y: 0, radius: 8),
                (id: 3, x: -60, y: 0, radius: 8),
                (id: 4, x: 0, y: 60, radius: 8),
            ],
            links: [
                (src: 1, dst: 2, occurrences: 9),
                (src: 1, dst: 3, occurrences: 9),
                (src: 1, dst: 4, occurrences: 9),
            ]
        )
        for step in 1...30 {
            let pin = CGPoint(x: CGFloat(step) * 17.5, y: CGFloat(step) * -3.25)
            engine.drag(id: 1, to: pin)
            engine.tick()
            let p = contractPhysicsPositions(engine)[1]
            XCTAssertEqual(p?.x, pin.x, "frame \(step): pin x")
            XCTAssertEqual(p?.y, pin.y, "frame \(step): pin y")
        }
        contractPhysicsAssertFinite(engine, "under a moving pin")
    }

    // "The node keeps its drag-imparted velocity (the glide): on the tick
    // after a moving release the node's position continues PAST the release
    // point, it does not freeze there."
    func testReleaseGlidesPastTheReleasePoint() {
        let engine = contractPhysicsEngine(
            nodes: [
                (id: 1, x: 0, y: 0, radius: 8),
                (id: 2, x: 40, y: 0, radius: 8),
            ],
            links: [(src: 1, dst: 2, occurrences: 1)]
        )
        // Twelve frames of steady travel in +x, so the release carries a real
        // velocity rather than a single displacement.
        var last = CGPoint.zero
        for step in 1...12 {
            last = CGPoint(x: CGFloat(step) * 40, y: 0)
            engine.drag(id: 1, to: last)
            engine.tick()
        }
        engine.release(id: 1)
        engine.tick()

        guard let after = contractPhysicsPositions(engine)[1] else {
            return XCTFail("node 1 vanished from the simulation")
        }
        XCTAssertGreaterThan(
            after.x, last.x,
            "a moving release glides on: expected x past \(last.x), got \(after.x)"
        )
        contractPhysicsAssertFinite(engine, "after a glide")
    }

    // The negative half of the same obligation, stated by the contract as
    // "it does not freeze there": the node is not still sitting on the pin.
    func testAReleasedNodeDoesNotFreezeOnThePin() {
        let engine = contractPhysicsEngine(
            nodes: [(id: 1, x: 0, y: 0, radius: 8), (id: 2, x: 40, y: 0, radius: 8)],
            links: [(src: 1, dst: 2, occurrences: 1)]
        )
        var last = CGPoint.zero
        for step in 1...12 {
            last = CGPoint(x: CGFloat(step) * 40, y: 0)
            engine.drag(id: 1, to: last)
            engine.tick()
        }
        engine.release(id: 1)
        engine.tick()

        let after = contractPhysicsPositions(engine)[1] ?? .zero
        XCTAssertGreaterThan(
            contractPhysicsDistance(after, last), 0,
            "the node froze exactly on the release point"
        )
    }
}

// MARK: - The force model

final class ContractPhysicsForceTests: XCTestCase {

    // (a) "A dragged node's LINKED neighbours are pulled toward it; over 60
    // ticks of a held far drag a linked node moves MORE than 3x an unlinked
    // one." — and (b)'s observable: dragging one node must not translate the
    // whole system, which is exactly what bounds the unlinked node here.
    //
    // The two witnesses are seeded SYMMETRICALLY about the dragged node, so
    // the only asymmetry between them is the link.
    func testALinkedNeighbourFollowsMoreThanTripleAnUnlinkedOne() {
        let linkedSeed = CGPoint(x: 120, y: 0)
        let unlinkedSeed = CGPoint(x: -120, y: 0)
        let engine = contractPhysicsEngine(
            nodes: [
                (id: 1, x: 0, y: 0, radius: 8),
                (id: 2, x: linkedSeed.x, y: linkedSeed.y, radius: 8),
                (id: 3, x: unlinkedSeed.x, y: unlinkedSeed.y, radius: 8),
            ],
            links: [(src: 1, dst: 2, occurrences: 1)]
        )
        engine.drag(id: 1, to: CGPoint(x: 900, y: 0))
        contractPhysicsTick(engine, 60)

        let after = contractPhysicsPositions(engine)
        let linkedMoved = contractPhysicsDistance(after[2] ?? .zero, linkedSeed)
        let unlinkedMoved = contractPhysicsDistance(after[3] ?? .zero, unlinkedSeed)

        XCTAssertGreaterThan(linkedMoved, 0, "the linked neighbour is pulled toward the drag")
        XCTAssertGreaterThan(
            linkedMoved, 3 * unlinkedMoved,
            "linked moved \(linkedMoved), unlinked moved \(unlinkedMoved); the bound is 3x"
        )
        contractPhysicsAssertFinite(engine, "under a far held drag")
    }

    // (c) "Link rest distance shrinks as occurrences grow: two nodes joined at
    // occurrences 9 settle strictly closer than an identical pair at
    // occurrences 1."
    func testHigherOccurrencesSettleStrictlyCloser() {
        func settledDistance(occurrences: Int64) -> CGFloat {
            let engine = contractPhysicsEngine(
                nodes: [
                    (id: 1, x: -150, y: 0, radius: 8),
                    (id: 2, x: 150, y: 0, radius: 8),
                ],
                links: [(src: 1, dst: 2, occurrences: occurrences)]
            )
            // Past the 200-tick settle ceiling, so both pairs are measured at
            // rest rather than mid-flight.
            contractPhysicsTick(engine, 300)
            let p = contractPhysicsPositions(engine)
            return contractPhysicsDistance(p[1] ?? .zero, p[2] ?? .zero)
        }

        let loose = settledDistance(occurrences: 1)
        let tight = settledDistance(occurrences: 9)
        XCTAssertTrue(loose.isFinite && tight.isFinite, "both pairs settled to finite positions")
        XCTAssertLessThan(
            tight, loose,
            "occurrences 9 settled at \(tight), occurrences 1 at \(loose); 9 must be closer"
        )
    }

    // (d) "A single node with no links settles at its seed." Nothing pulls it,
    // so it must not drift — a warm start that wanders is the bug this pins.
    func testASingleUnlinkedNodeSettlesAtItsSeed() {
        let seed = CGPoint(x: -412.75, y: 88.5)
        let engine = contractPhysicsEngine(nodes: [(id: 7, x: seed.x, y: seed.y, radius: 8)])
        contractPhysicsSettle(engine, maxTicks: 200)
        XCTAssertFalse(engine.isAwake, "a lone node has nothing to settle")

        let p = contractPhysicsPositions(engine)[7]
        XCTAssertEqual(p?.x ?? .nan, seed.x, accuracy: 1e-6, "it drifted on x")
        XCTAssertEqual(p?.y ?? .nan, seed.y, accuracy: 1e-6, "it drifted on y")
    }

    // (b) again, from the other side: a whole disconnected component far from
    // the drag must not be translated along with it. Same statement as the
    // unlinked bound above, on a component rather than one node.
    func testDraggingOneNodeDoesNotTranslateADisconnectedComponent() {
        let farA = CGPoint(x: -800, y: 400)
        let farB = CGPoint(x: -800, y: 460)
        let engine = contractPhysicsEngine(
            nodes: [
                (id: 1, x: 0, y: 0, radius: 8),
                (id: 2, x: 60, y: 0, radius: 8),
                (id: 3, x: farA.x, y: farA.y, radius: 8),
                (id: 4, x: farB.x, y: farB.y, radius: 8),
            ],
            links: [(src: 1, dst: 2, occurrences: 1), (src: 3, dst: 4, occurrences: 1)]
        )
        engine.drag(id: 1, to: CGPoint(x: 5000, y: 0))
        contractPhysicsTick(engine, 60)

        let after = contractPhysicsPositions(engine)
        let movedA = contractPhysicsDistance(after[3] ?? .zero, farA)
        let movedB = contractPhysicsDistance(after[4] ?? .zero, farB)
        let dragged = contractPhysicsDistance(after[1] ?? .zero, .zero)
        XCTAssertGreaterThan(dragged, 0, "precondition: the dragged node actually moved")
        XCTAssertLessThan(movedA, dragged / 3, "the far component followed the drag")
        XCTAssertLessThan(movedB, dragged / 3, "the far component followed the drag")
    }
}

// MARK: - Determinism

final class ContractPhysicsDeterminismTests: XCTestCase {

    // "Deterministic: two engines built from identical inputs, ticked
    // identically with identical drag calls, yield identical positions."
    // Exact equality, per the word "identical" — a seeded scatter or a
    // wall-clock dt would fail here and nowhere else.
    func testTwoEnginesRunIdenticallyYieldIdenticalPositions() {
        func run() -> [Int64: CGPoint] {
            let engine = contractPhysicsEngine(
                nodes: [
                    (id: 1, x: 0, y: 0, radius: 8),
                    (id: 2, x: 130, y: -40, radius: 10),
                    (id: 3, x: -75, y: 210, radius: 6),
                    (id: 4, x: 300, y: 300, radius: 14),
                ],
                links: [
                    (src: 1, dst: 2, occurrences: 3),
                    (src: 2, dst: 3, occurrences: 1),
                    (src: 1, dst: 4, occurrences: 7),
                ]
            )
            contractPhysicsTick(engine, 25)
            for step in 1...15 {
                engine.drag(id: 2, to: CGPoint(x: CGFloat(step) * 22, y: CGFloat(step) * -6))
                engine.tick()
            }
            engine.release(id: 2)
            contractPhysicsTick(engine, 40)
            return contractPhysicsPositions(engine)
        }

        let a = run()
        let b = run()
        XCTAssertEqual(Set(a.keys), Set(b.keys))
        for (id, pa) in a {
            guard let pb = b[id] else { return XCTFail("node \(id) missing from the second run") }
            XCTAssertEqual(pa.x, pb.x, "node \(id) x diverged: \(pa.x) vs \(pb.x)")
            XCTAssertEqual(pa.y, pb.y, "node \(id) y diverged: \(pa.y) vs \(pb.y)")
        }
    }

    // Determinism is a claim about the SEQUENCE, not only about the endpoint:
    // the same two runs agree frame by frame, so an implementation that
    // converges to the same rest from different paths does not pass by luck.
    func testDeterminismHoldsFrameByFrame() {
        func engine() -> GraphPhysics {
            contractPhysicsEngine(
                nodes: [
                    (id: 1, x: 12, y: -8, radius: 8),
                    (id: 2, x: -90, y: 33, radius: 8),
                    (id: 3, x: 45, y: 190, radius: 8),
                ],
                links: [(src: 1, dst: 2, occurrences: 2), (src: 2, dst: 3, occurrences: 5)]
            )
        }
        let a = engine()
        let b = engine()
        for frame in 1...40 {
            a.tick()
            b.tick()
            let pa = contractPhysicsPositions(a)
            let pb = contractPhysicsPositions(b)
            for (id, p) in pa {
                XCTAssertEqual(p.x, pb[id]?.x, "frame \(frame), node \(id) x")
                XCTAssertEqual(p.y, pb[id]?.y, "frame \(frame), node \(id) y")
            }
            XCTAssertEqual(a.isAwake, b.isAwake, "frame \(frame): isAwake diverged")
        }
    }
}

// MARK: - Charge scalability

final class ContractPhysicsScaleTests: XCTestCase {

    // "Repulsion uses a Barnes-Hut approximation (quadtree), NOT all-pairs:
    // 3,000 nodes x 100 ticks completes in under 10 s in a unit test on the
    // build machine."
    //
    // The layout is a deterministic lattice rather than a scatter: the
    // contract forbids randomness in the engine, and a random FIXTURE would
    // make this test's own runtime vary run to run.
    func testThreeThousandNodesTickOneHundredTimesUnderTenSeconds() {
        var nodes: [(id: Int64, x: CGFloat, y: CGFloat, radius: CGFloat)] = []
        nodes.reserveCapacity(3000)
        for i in 0..<3000 {
            nodes.append((
                id: Int64(i + 1),
                x: CGFloat((i % 60) * 25) - 750,
                y: CGFloat((i / 60) * 25) - 625,
                radius: 6
            ))
        }
        var links: [(src: Int64, dst: Int64, occurrences: Int64)] = []
        links.reserveCapacity(2999)
        for i in 1..<3000 {
            links.append((src: Int64(i), dst: Int64(i + 1), occurrences: Int64(i % 9 + 1)))
        }

        let engine = contractPhysicsEngine(nodes: nodes, links: links)
        let started = Date()
        contractPhysicsTick(engine, 100)
        let elapsed = Date().timeIntervalSince(started)

        XCTAssertLessThan(
            elapsed, 10,
            "3000 nodes x 100 ticks took \(elapsed)s; all-pairs repulsion is the usual cause"
        )
        XCTAssertEqual(engine.nodes().count, 3000)
        contractPhysicsAssertFinite(engine, "after 100 ticks at 3000 nodes")
    }
}

// MARK: - The gate

final class ContractPhysicsAllowedTests: XCTestCase {

    // "false when reduceMotion; else visibleNodes <= 3000."
    func testReduceMotionRefusesAtEveryCount() {
        for count in [0, 1, 42, 2999, 3000, 3001, 100_000] {
            XCTAssertFalse(
                graphPhysicsAllowed(visibleNodes: count, reduceMotion: true),
                "reduce-motion must win at \(count) nodes"
            )
        }
    }

    func testTheCapIsThreeThousandInclusive() {
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 0, reduceMotion: false))
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 1, reduceMotion: false))
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 2999, reduceMotion: false))
        XCTAssertTrue(graphPhysicsAllowed(visibleNodes: 3000, reduceMotion: false),
                      "3000 is <= 3000 — the cap is inclusive")
        XCTAssertFalse(graphPhysicsAllowed(visibleNodes: 3001, reduceMotion: false))
        XCTAssertFalse(graphPhysicsAllowed(visibleNodes: 100_000, reduceMotion: false))
    }
}

// MARK: - View-model fixtures

private struct ContractPhysicsStubError: Error {}

private func contractPhysicsDecode<T: Decodable>(_ json: String) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(json.utf8))
}

/// Built by DECODING, for the reason the sibling file states: the contract
/// freezes the wire shape, not the presence of a synthesized memberwise init.
private func contractPhysicsMkNode(
    id: Int64, name: String = "n", type: String = "component", x: Double, y: Double
) -> GraphNode {
    let json = """
    {"id":\(id),"name":"\(name)","type":"\(type)","description":"","x":\(x),"y":\(y)}
    """
    return try! contractPhysicsDecode(json)
}

private func contractPhysicsMkEdge(
    id: Int64, src: Int64, dst: Int64, occurrences: Int64 = 1
) -> GraphEdge {
    let json = """
    {"id":\(id),"src":\(src),"dst":\(dst),"predicate":"relates-to","fact":"f",
     "strength":5,"occurrences":\(occurrences),"valid_until":0,"expired_at":0,"episode":1}
    """
    return try! contractPhysicsDecode(json)
}

private func contractPhysicsMkPayload(nodes: [GraphNode], edges: [GraphEdge]) -> GraphPayload {
    let enc = JSONEncoder()
    let n = String(decoding: try! enc.encode(nodes), as: UTF8.self)
    let e = String(decoding: try! enc.encode(edges), as: UTF8.self)
    let s = """
    {"nodes":\(nodes.count),"edges":\(edges.count),"live_edges":\(edges.count),
     "episodes":1,"pending":0,"missing_embeddings":0}
    """
    return try! contractPhysicsDecode("{\"nodes\":\(n),\"edges\":\(e),\"stats\":\(s)}")
}

// MARK: - View-model obligations (the reachable half)

@MainActor
final class ContractPhysicsViewModelTests: XCTestCase {

    /// A view model whose graph read returns whatever `payload` holds at call
    /// time, so a test can swap the payload between two polls.
    private func makeVM(_ payload: @escaping () -> GraphPayload) -> GraphViewModel {
        GraphViewModel(
            fetchGraph: { _ in payload() },
            fetchNode: { _, _ in throw ContractPhysicsStubError() }
        )
    }

    private func base() -> GraphPayload {
        contractPhysicsMkPayload(
            nodes: [
                contractPhysicsMkNode(id: 1, name: "carl", type: "person", x: -103.5, y: 79.875),
                contractPhysicsMkNode(id: 5, name: "renderMD", x: -75.25, y: 21.125),
                contractPhysicsMkNode(id: 6, name: "rule", type: "convention", x: 250.5, y: -0.25),
            ],
            edges: [
                contractPhysicsMkEdge(id: 4, src: 1, dst: 5),
                contractPhysicsMkEdge(id: 5, src: 5, dst: 6, occurrences: 4),
            ]
        )
    }

    // "A poll delivering an unchanged payload must not restart the simulation
    // or move any node." The reachable half: every rendered position survives
    // the reload byte for byte — a re-seed from the server payload, a scatter,
    // or a rebuilt engine would all show up here.
    func testAnUnchangedReloadPreservesEveryPosition() async {
        let vm = makeVM { self.base() }
        await vm.load(baseURL: "http://mac.local:7777")
        let first = vm.render.positions
        XCTAssertEqual(Set(first.keys), [1, 5, 6])

        await vm.load(baseURL: "http://mac.local:7777")
        let second = vm.render.positions
        XCTAssertEqual(Set(second.keys), Set(first.keys), "the node set is unchanged")
        for (id, p) in first {
            XCTAssertEqual(second[id]?.x, p.x, "node \(id) x moved on an unchanged poll")
            XCTAssertEqual(second[id]?.y, p.y, "node \(id) y moved on an unchanged poll")
        }
    }

    // "A poll delivering a changed payload keeps existing nodes at their
    // current simulated positions and enters NEW nodes at their server
    // positions." Two payloads differing by one added node, as the contract's
    // "Where observable" column asks for.
    func testAChangedPayloadKeepsExistingNodesAndEntersTheNewOneAtItsServerPosition() async {
        var payload = base()
        let vm = makeVM { payload }
        await vm.load(baseURL: "http://mac.local:7777")
        let before = vm.render.positions

        let newX = 640.5
        let newY = -128.25
        payload = contractPhysicsMkPayload(
            nodes: [
                contractPhysicsMkNode(id: 1, name: "carl", type: "person", x: -103.5, y: 79.875),
                contractPhysicsMkNode(id: 5, name: "renderMD", x: -75.25, y: 21.125),
                contractPhysicsMkNode(id: 6, name: "rule", type: "convention", x: 250.5, y: -0.25),
                contractPhysicsMkNode(id: 9, name: "newcomer", x: newX, y: newY),
            ],
            edges: [
                contractPhysicsMkEdge(id: 4, src: 1, dst: 5),
                contractPhysicsMkEdge(id: 5, src: 5, dst: 6, occurrences: 4),
                contractPhysicsMkEdge(id: 7, src: 6, dst: 9, occurrences: 2),
            ]
        )
        await vm.load(baseURL: "http://mac.local:7777")
        let after = vm.render.positions

        XCTAssertEqual(Set(after.keys), [1, 5, 6, 9], "the newcomer joined and nobody was dropped")
        for id in [Int64(1), 5, 6] {
            XCTAssertEqual(after[id]?.x, before[id]?.x, "existing node \(id) x was reset")
            XCTAssertEqual(after[id]?.y, before[id]?.y, "existing node \(id) y was reset")
        }
        XCTAssertEqual(after[9]?.x, CGFloat(newX), "a new node enters at its server x")
        XCTAssertEqual(after[9]?.y, CGFloat(newY), "a new node enters at its server y")
    }

    // A node LEAVING the payload takes its position with it and disturbs no
    // survivor — the same obligation read in the other direction, and the
    // shape a `/graph` revision job actually delivers.
    func testARetiredNodeLeavesTheSurvivorsWhereTheyWere() async {
        var payload = base()
        let vm = makeVM { payload }
        await vm.load(baseURL: "http://mac.local:7777")
        let before = vm.render.positions

        payload = contractPhysicsMkPayload(
            nodes: [
                contractPhysicsMkNode(id: 1, name: "carl", type: "person", x: -103.5, y: 79.875),
                contractPhysicsMkNode(id: 5, name: "renderMD", x: -75.25, y: 21.125),
            ],
            edges: [contractPhysicsMkEdge(id: 4, src: 1, dst: 5)]
        )
        await vm.load(baseURL: "http://mac.local:7777")
        let after = vm.render.positions

        XCTAssertEqual(Set(after.keys), [1, 5])
        for id in [Int64(1), 5] {
            XCTAssertEqual(after[id]?.x, before[id]?.x, "survivor \(id) x moved")
            XCTAssertEqual(after[id]?.y, before[id]?.y, "survivor \(id) y moved")
        }
    }
}
