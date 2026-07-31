import CoreGraphics
import Foundation
import XCTest

@testable import Kraken

// Regression pins for defects found in the shipped round-1 physics slice and
// fixed in round 3 (logic r2 S1/S2, edge r2 I1-I5). These are the implementer's
// half of the split: each one reproduces a DEFECT that existed, not a
// requirement from the frozen contract — the contract's requirements belong to
// the two blind suites, and nothing here restates one.
//
// Every pin below FAILED against the round-1 implementation. That is the bar
// for a file like this: a regression test that would have passed before the fix
// is pinning nothing.
//
// Every file-scope identifier is `private` and prefixed `reg` / `Reg` — this
// target's namespace is shared with both blind authors' files.

// MARK: - fixtures

private func regNode(_ id: Int64, _ x: Double, _ y: Double, type: String = "component") -> GraphNode {
    GraphNode(id: id, name: "n\(id)", type: type, description: "", x: x, y: y, aliases: nil)
}

private func regEdge(_ id: Int64, _ src: Int64, _ dst: Int64, occurrences: Int64 = 1) -> GraphEdge {
    GraphEdge(
        id: id, src: src, dst: dst, predicate: "relates-to", fact: "f",
        strength: 1, occurrences: occurrences, validUntil: 0, expiredAt: 0, episode: 1
    )
}

private func regPayload(nodes: [GraphNode], edges: [GraphEdge]) -> GraphPayload {
    GraphPayload(
        nodes: nodes, edges: edges,
        stats: GraphStats(
            nodes: Int64(nodes.count), edges: Int64(edges.count),
            liveEdges: Int64(edges.count), episodes: 1, pending: 0, missingEmbeddings: 0
        )
    )
}

@MainActor
private func regVM(_ payload: GraphPayload) -> GraphViewModel {
    GraphViewModel(
        fetchGraph: { _ in payload },
        fetchNode: { _, id in GraphNodeDetail(node: regNode(id, 0, 0), edges: []) }
    )
}

private let regBaseURL = "http://mac.local:7777"

/// Two nodes joined by one edge, far enough apart to have somewhere to settle.
private func regPair() -> GraphPayload {
    regPayload(nodes: [regNode(1, -60, 0), regNode(2, 60, 0)], edges: [regEdge(10, 1, 2)])
}

@MainActor
private func regDrive(_ vm: GraphViewModel, _ frames: Int) {
    for _ in 0..<frames { vm.tickPhysics() }
}

@MainActor
private func regSettle(_ vm: GraphViewModel, cap: Int = 400) {
    var spent = 0
    while vm.physicsShouldTick && spent < cap {
        vm.tickPhysics()
        spent += 1
    }
}

// MARK: - S1: the glide was a function of the event rate, not the finger

final class RegressionGlideIsFrameRelativeTests: XCTestCase {
    /// Drags one node the SAME world distance at the SAME frame rate, varying
    /// only how many `drag` calls the gesture layer delivers per frame, and
    /// measures how far the node keeps going after the release.
    ///
    /// Round 1 recorded one CALL's displacement as the release velocity, so the
    /// finer the event stream the smaller the glide — the same flick that
    /// carried 34 units at one event per frame carried 2.5 at eight. A gesture
    /// system is free to coalesce or subdivide its callbacks; the feel must not
    /// depend on that choice.
    private func glideDistance(callsPerFrame: Int) -> Double {
        let engine = GraphPhysics(
            nodes: [(id: 1, x: 0, y: 0, radius: 5), (id: 2, x: 300, y: 0, radius: 5)],
            links: []
        )
        // 10 frames of travel at 24 world units per frame, delivered in
        // `callsPerFrame` equal steps.
        let perFrame = 24.0
        var x = 0.0
        for _ in 0..<10 {
            for _ in 0..<callsPerFrame {
                x += perFrame / Double(callsPerFrame)
                engine.drag(id: 1, to: CGPoint(x: x, y: 0))
            }
            engine.tick()
        }
        let released = engine.nodes().first { $0.id == 1 }!.x
        engine.release(id: 1)
        engine.tick()
        let after = engine.nodes().first { $0.id == 1 }!.x
        return Double(after - released)
    }

    func testGlideIsTheSameWhateverTheEventRate() {
        let one = glideDistance(callsPerFrame: 1)
        let eight = glideDistance(callsPerFrame: 8)
        XCTAssertGreaterThan(one, 1, "a flick must glide at all")
        // The two differ only in float accumulation order and in the forces
        // acting over the same ten frames, so they should agree closely. The
        // round-1 bug put them a factor of 13 apart.
        XCTAssertEqual(
            one, eight, accuracy: max(0.5, one * 0.1),
            "the glide must come from the finger's speed, not the callback rate"
        )
    }

    /// The other half of the same rule: a finger that stops moving and is then
    /// released does NOT fling. Round 1 kept the last non-zero displacement
    /// forever, so a fast drag, a long motionless hold, and a release threw the
    /// node as if it had never stopped.
    func testAMotionlessReleaseDoesNotFling() {
        let engine = GraphPhysics(nodes: [(id: 1, x: 0, y: 0, radius: 5)], links: [])
        for i in 1...10 {
            engine.drag(id: 1, to: CGPoint(x: CGFloat(i) * 30, y: 0))
            engine.tick()
        }
        // Held still: a real gesture emits no callback while the finger does
        // not move, so this is silence, not repeated drags.
        for _ in 0..<20 { engine.tick() }
        let held = engine.nodes()[0].x
        engine.release(id: 1)
        engine.tick()
        let after = engine.nodes()[0].x

        // The claim is about DIRECTION, not stillness. A released node is back
        // under the forces and this one has an anchor pulling it toward the
        // seed centroid it started at, so it legitimately drifts about a unit
        // BACKWARD (measured: -1.02). What must never happen is the node
        // continuing along the drag: round 1 handed the release a velocity of
        // 30 and it carried +18 on the first frame.
        XCTAssertLessThan(
            after, held + 1,
            "a stationary release must not carry the velocity of a drag that ended long ago"
        )
        XCTAssertGreaterThan(
            after, held - 5,
            "and it must not be flung backward either — this is a drift, not a launch"
        )
    }
}

// MARK: - S2 / I2: a cancelled gesture left the simulation hot forever

final class RegressionAbandonedPinTests: XCTestCase {
    /// SwiftUI does not guarantee `onEnded` on a cancelled gesture, so the
    /// view model can be left holding a pin nobody will ever release. Round 1
    /// then ran the display link at 60 fps for the life of the screen.
    @MainActor
    func testAPinNobodyReleasesIsEventuallyDropped() async {
        let vm = regVM(regPair())
        await vm.load(baseURL: regBaseURL)
        regSettle(vm)
        XCTAssertFalse(vm.physicsShouldTick)

        vm.dragNode(id: 1, to: CGPoint(x: 400, y: 400))
        XCTAssertTrue(vm.physicsShouldTick, "a live pin is per-frame work")

        // No release, ever — the gesture died. The loop must still reach a
        // stop on its own.
        regSettle(vm, cap: 600)
        XCTAssertFalse(
            vm.physicsShouldTick,
            "an abandoned pin must not hold the simulation awake forever"
        )
    }

    /// A pin must not survive the screen. Round 1 released nothing on the way
    /// out, so returning to the screen restarted a hot loop around a finger
    /// that had been gone for minutes.
    @MainActor
    func testLeavingTheScreenDropsEveryPin() async {
        let vm = regVM(regPair())
        await vm.load(baseURL: regBaseURL)
        regSettle(vm)

        vm.dragNode(id: 1, to: CGPoint(x: 400, y: 400))
        vm.stopDisplayLoop()
        // Whatever alpha is still bleeding off, the PIN is what would have
        // made this permanent: drive to a stop and require one.
        regSettle(vm, cap: 600)
        XCTAssertFalse(
            vm.physicsShouldTick,
            "a pin must not outlive the screen that took it"
        )
    }
}

// MARK: - I3: a rebuild mid-drag dropped the pin

final class RegressionPinSurvivesRebuildTests: XCTestCase {
    /// A poll delivering one new node rebuilds the engine. Round 1 built the
    /// new one with no knowledge of the pin, so the held node was handed back
    /// to the forces and walked out from under a stationary finger.
    @MainActor
    func testAMembershipChangeMidDragKeepsTheNodeUnderTheFinger() async {
        let first = regPair()
        let second = regPayload(
            nodes: [regNode(1, -60, 0), regNode(2, 60, 0), regNode(3, 0, 200)],
            edges: [regEdge(10, 1, 2)]
        )
        var current = first
        let vm = GraphViewModel(
            fetchGraph: { _ in current },
            fetchNode: { _, id in GraphNodeDetail(node: regNode(id, 0, 0), edges: []) }
        )
        await vm.load(baseURL: regBaseURL)
        regSettle(vm)

        let grabbed = CGPoint(x: 400, y: 400)
        vm.dragNode(id: 1, to: grabbed)
        regDrive(vm, 5)
        XCTAssertEqual(vm.render.positions[1], grabbed, "the pin holds before the poll")

        // The poll lands mid-drag and changes membership.
        current = second
        await vm.load(baseURL: regBaseURL)
        regDrive(vm, 20)

        let held = vm.render.positions[1]
        XCTAssertNotNil(held)
        XCTAssertEqual(
            held, grabbed,
            "a rebuild must not hand a pinned node back to the forces"
        )
    }
}

// MARK: - I1: a duplicate node id made the drag invisible

final class RegressionDuplicateNodeIDTests: XCTestCase {
    /// A duplicate id is a server bug, but it must not break the client: round
    /// 1 seeded the engine with two rows under one id, pinned the first, and
    /// then let the second — free, unpinned — win the position write-back, so
    /// the dragged node did not appear to move at all.
    @MainActor
    func testADragIsVisibleEvenWhenThePayloadRepeatsAnID() async {
        let vm = regVM(regPayload(
            nodes: [regNode(1, -60, 0), regNode(1, -60, 0), regNode(2, 60, 0)],
            edges: [regEdge(10, 1, 2)]
        ))
        await vm.load(baseURL: regBaseURL)
        regSettle(vm)

        let grabbed = CGPoint(x: 500, y: 250)
        vm.dragNode(id: 1, to: grabbed)
        regDrive(vm, 10)

        XCTAssertEqual(
            vm.render.positions[1], grabbed,
            "the duplicate must not shadow the pinned row"
        )
    }
}

// MARK: - I4: membership equality depended on the server's row order

final class RegressionMembershipOrderTests: XCTestCase {
    /// The rebuild is keyed on membership equality, and round 1 compared the
    /// lists positionally. That held only because `internal/store/graph.go`
    /// happens to `ORDER BY` — an undeclared dependency three layers away,
    /// whose failure is silent: the simulation goes back to restarting on
    /// every 30 s poll and simply never settles.
    @MainActor
    func testAReorderedPayloadIsNotAMembershipChange() async {
        let ordered = regPayload(
            nodes: [regNode(1, -60, 0), regNode(2, 60, 0), regNode(3, 0, 90)],
            edges: [regEdge(10, 1, 2), regEdge(11, 2, 3)]
        )
        let shuffled = regPayload(
            nodes: [regNode(3, 0, 90), regNode(1, -60, 0), regNode(2, 60, 0)],
            edges: [regEdge(11, 2, 3), regEdge(10, 1, 2)]
        )
        var current = ordered
        let vm = GraphViewModel(
            fetchGraph: { _ in current },
            fetchNode: { _, id in GraphNodeDetail(node: regNode(id, 0, 0), edges: []) }
        )
        await vm.load(baseURL: regBaseURL)
        regSettle(vm)
        XCTAssertFalse(vm.physicsShouldTick, "settled before the reordered poll")

        current = shuffled
        await vm.load(baseURL: regBaseURL)
        XCTAssertFalse(
            vm.physicsShouldTick,
            "the same graph in a different order is the same simulation"
        )
    }
}

// MARK: - I5: reduce-motion arrived one beat too late

final class RegressionReduceMotionAtConstructionTests: XCTestCase {
    /// The gate has to govern the FIRST payload, because the first payload is
    /// what builds the simulation. Round 1 defaulted the flag to false and
    /// waited for the view's `onAppear`, so a reader who had asked the system
    /// for no motion got one settle animation on entry, every time.
    ///
    /// The production path reads `UIAccessibility` in `init()`; this pins the
    /// property that makes that reachable — a model constructed with the flag
    /// already set must never build an engine.
    @MainActor
    func testAModelBornWithReduceMotionNeverAnimatesItsFirstPayload() async {
        let payload = regPair()
        let vm = GraphViewModel(
            fetchGraph: { _ in payload },
            fetchNode: { _, id in GraphNodeDetail(node: regNode(id, 0, 0), edges: []) },
            reduceMotion: true
        )
        await vm.load(baseURL: regBaseURL)

        XCTAssertNil(vm.physics, "reduce-motion must be in force before the first frame")
        XCTAssertFalse(vm.physicsShouldTick)
        XCTAssertEqual(
            vm.render.positions[1], CGPoint(x: -60, y: 0),
            "and the canvas must show the server layout untouched"
        )
    }
}
