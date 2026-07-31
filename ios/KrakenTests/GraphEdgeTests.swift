import XCTest
import Foundation
import CoreGraphics
@testable import Kraken

// Edge cases, boundaries, degenerate inputs and error paths for the native
// graph screen's pure layer (artifacts/50-contract.md, slice 1). Authored from
// the frozen contract alone, before the implementation existed, so every
// assertion here traces to a sentence in that file rather than to code — where
// the contract is silent this file asserts nothing.
//
// The happy paths and stated invariants belong to the other blind author; what
// lives here is the second half of each rule: the ≤ that includes its
// boundary, the 0 that is not a coordinate, the NaN that is not a position,
// the fetch that throws, the tie that either side may win.
//
// Every file-scope identifier is `private` and prefixed `edge` / `Edge` — this
// test target's namespace is shared with a second author's file.

// MARK: - Fixtures & helpers

/// The real `GET /api/graph` body captured in the contract's Fixtures section,
/// verbatim. Reproduced rather than referenced so a decode failure names the
/// bytes that failed.
private let edgeGraphFixtureJSON = """
{"nodes":[{"id":1,"name":"carl","type":"person","description":"The operator of kraken: steers cells from the board, leaves per-artifact feedback, and accepts or rejects every job.","x":-103.4903506081596,"y":79.87158754979711},{"id":5,"name":"renderMD","type":"component","description":"The kraken board's markdown renderer, which turns artifact .md files into the HTML the web board displays.","x":-75.18157241820735,"y":21.115214853358946},{"id":6,"name":"never-hard-wrap rule","type":"convention","description":"One paragraph, bullet, or table row is ONE source line in markdown artifacts; mid-sentence hard wraps are forbidden.","x":-50.51906630702463,"y":8.879077510894259}],"edges":[{"id":4,"src":1,"dst":5,"predicate":"requested","fact":"carl asked that kraken's markdown renderer require a double line break to break a paragraph - a single line break in raw markdown must not break lines in the rendered output.","strength":9,"occurrences":1,"valid_until":0,"expired_at":0,"episode":3},{"id":5,"src":5,"dst":6,"predicate":"implements","fact":"renderMD folds consecutive prose lines into one paragraph and requires a blank line to start a new one (PR #94, shipped by the dream pass of 2026-07-28), measured over 16,810 real .md files with none rendering worse.","strength":8,"occurrences":1,"valid_until":0,"expired_at":0,"episode":3}],"stats":{"nodes":35,"edges":29,"live_edges":29,"episodes":678,"pending":653,"missing_embeddings":0}}
"""

/// The `GET /api/graph/node?id=5` body from the same section: the node object
/// with `aliases` present (and empty), plus the same edge objects.
private let edgeNodeDetailFixtureJSON = """
{"node":{"id":5,"name":"renderMD","type":"component","description":"The kraken board's markdown renderer, which turns artifact .md files into the HTML the web board displays.","x":-75.18157241820735,"y":21.115214853358946,"aliases":[]},"edges":[{"id":4,"src":1,"dst":5,"predicate":"requested","fact":"carl asked that kraken's markdown renderer require a double line break to break a paragraph - a single line break in raw markdown must not break lines in the rendered output.","strength":9,"occurrences":1,"valid_until":0,"expired_at":0,"episode":3},{"id":5,"src":5,"dst":6,"predicate":"implements","fact":"renderMD folds consecutive prose lines into one paragraph and requires a blank line to start a new one (PR #94, shipped by the dream pass of 2026-07-28), measured over 16,810 real .md files with none rendering worse.","strength":8,"occurrences":1,"valid_until":0,"expired_at":0,"episode":3}]}
"""

private func edgeDecode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(json.utf8))
}

/// x/y default to a non-zero, finite pair so a node is POSITIONED unless a test
/// asks otherwise — (0,0) is the contract's "unpositioned" sentinel and a
/// defaulted 0 would silently opt every helper node into the ring.
private func edgeMkNode(
    _ id: Int64,
    _ name: String = "n",
    type: String = "component",
    description: String = "",
    x: Double = 3,
    y: Double = 4,
    aliases: [String]? = nil
) -> GraphNode {
    GraphNode(id: id, name: name, type: type, description: description, x: x, y: y, aliases: aliases)
}

private func edgeMkEdge(
    _ id: Int64,
    _ src: Int64,
    _ dst: Int64,
    predicate: String = "relates-to",
    fact: String = "a fact sentence",
    strength: Int64 = 1,
    occurrences: Int64 = 1,
    validUntil: Int64 = 0,
    expiredAt: Int64 = 0,
    episode: Int64 = 1
) -> GraphEdge {
    GraphEdge(
        id: id, src: src, dst: dst, predicate: predicate, fact: fact,
        strength: strength, occurrences: occurrences,
        validUntil: validUntil, expiredAt: expiredAt, episode: episode
    )
}

private func edgeMkStats(nodes: Int64 = 0, edges: Int64 = 0) -> GraphStats {
    GraphStats(nodes: nodes, edges: edges, liveEdges: edges, episodes: 0, pending: 0, missingEmbeddings: 0)
}

private func edgeMkPayload(nodes: [GraphNode], edges: [GraphEdge]) -> GraphPayload {
    GraphPayload(nodes: nodes, edges: edges, stats: edgeMkStats(nodes: Int64(nodes.count), edges: Int64(edges.count)))
}

private func edgeAssertPoint(
    _ p: CGPoint, _ x: CGFloat, _ y: CGFloat, accuracy: CGFloat = 1e-9,
    _ message: String = "", file: StaticString = #filePath, line: UInt = #line
) {
    XCTAssertEqual(p.x, x, accuracy: accuracy, "x: \(message)", file: file, line: line)
    XCTAssertEqual(p.y, y, accuracy: accuracy, "y: \(message)", file: file, line: line)
}

private func edgeHypot(_ p: CGPoint) -> CGFloat { (p.x * p.x + p.y * p.y).squareRoot() }

/// The "no position at all" stand-in for a lookup that must not be nil. Written
/// out rather than spelled with a leading-dot `.nan` at each site: with both
/// `CGFloat.nan` and `Double.nan` visible, the leading-dot form is ambiguous.
private let edgeNaNPoint = CGPoint(x: CGFloat.nan, y: CGFloat.nan)

/// The one calendar every expectation in this file is built from: GREGORIAN in
/// the local timezone, the same pair the contract binds `graphShortDate` to.
/// Never `Calendar.current` — a device region on a non-Gregorian calendar
/// resolves "year 2026" in a different era, which would fail these tests
/// against correct code.
private let edgeGregorian = Calendar(identifier: .gregorian)

/// The expected `YYYY-MM-DD` for an instant. Never a hardcoded literal: this
/// machine's timezone is not UTC and a literal would pin the wrong day.
private func edgeLocalYMD(_ date: Date) -> String {
    let c = edgeGregorian.dateComponents([.year, .month, .day], from: date)
    return String(format: "%04d-%02d-%02d", c.year ?? 0, c.month ?? 0, c.day ?? 0)
}

/// A local wall-clock instant, so the resulting unix-millis value means the
/// named local date wherever this runs.
private func edgeLocalDate(_ y: Int, _ m: Int, _ d: Int, _ hour: Int = 12) -> Date {
    var c = DateComponents()
    c.year = y; c.month = m; c.day = d; c.hour = hour; c.minute = 0; c.second = 0
    return edgeGregorian.date(from: c) ?? Date(timeIntervalSince1970: 0)
}

private func edgeMillis(_ date: Date) -> Int64 { Int64((date.timeIntervalSince1970 * 1000).rounded()) }

// MARK: - Invalidation predicate boundaries

final class EdgeGraphInvalidationTests: XCTestCase {

    // "0 < validUntil ≤ now" — the boundary itself is INVALIDATED, and one
    // millisecond later it is not. This is the whole of the ≤ and the only
    // place the two readings of it differ.
    func testValidUntilExactlyNowIsInvalidated() {
        let now: Int64 = 1_769_040_000_000
        XCTAssertTrue(graphIsInvalidated(edgeMkEdge(1, 1, 2, validUntil: now), now: now))
        XCTAssertTrue(graphIsInvalidated(edgeMkEdge(1, 1, 2, validUntil: now - 1), now: now))
        XCTAssertFalse(graphIsInvalidated(edgeMkEdge(1, 1, 2, validUntil: now + 1), now: now))
    }

    func testBothAxesZeroIsLive() {
        XCTAssertFalse(graphIsInvalidated(edgeMkEdge(1, 1, 2), now: 1_769_040_000_000))
        // now = 0 too: a zero clock must not turn every unstamped fact stale.
        XCTAssertFalse(graphIsInvalidated(edgeMkEdge(1, 1, 2), now: 0))
    }

    // validUntil is a real timestamp only when > 0; with now == 0 a positive
    // validUntil is in the future, so the fact is live.
    func testFutureValidUntilAgainstAZeroClockIsLive() {
        XCTAssertFalse(graphIsInvalidated(edgeMkEdge(1, 1, 2, validUntil: 1), now: 0))
        XCTAssertTrue(graphIsInvalidated(edgeMkEdge(1, 1, 2, validUntil: 1), now: 1))
    }

    // expiredAt dominates: "expiredAt > 0 OR ...", so a still-valid window with
    // a system-time expiry is invalidated regardless of `now`.
    func testExpiredAtDominatesAFutureValidUntil() {
        let now: Int64 = 1_000
        XCTAssertTrue(graphIsInvalidated(edgeMkEdge(1, 1, 2, validUntil: now + 99_999, expiredAt: 5), now: now))
        XCTAssertTrue(graphIsInvalidated(edgeMkEdge(1, 1, 2, expiredAt: 1), now: 0))
    }

    // Neither axis is > 0, so neither clause fires. A negative stamp is not a
    // "yes" — it is a row that was never stamped.
    func testNegativeStampsAreNotInvalidations() {
        let now: Int64 = 1_769_040_000_000
        XCTAssertFalse(graphIsInvalidated(edgeMkEdge(1, 1, 2, validUntil: -5), now: now))
        XCTAssertFalse(graphIsInvalidated(edgeMkEdge(1, 1, 2, expiredAt: -5), now: now))
        XCTAssertFalse(graphIsInvalidated(edgeMkEdge(1, 1, 2, validUntil: -5, expiredAt: -5), now: now))
    }

    // MARK: graphInvalidatedAt

    func testInvalidatedAtPrefersExpiredAtWhenBothAreStamped() {
        XCTAssertEqual(graphInvalidatedAt(edgeMkEdge(1, 1, 2, validUntil: 200, expiredAt: 100)), 100)
    }

    func testInvalidatedAtFallsBackToValidUntilThenZero() {
        XCTAssertEqual(graphInvalidatedAt(edgeMkEdge(1, 1, 2, validUntil: 200)), 200)
        XCTAssertEqual(graphInvalidatedAt(edgeMkEdge(1, 1, 2)), 0)
    }

    // Neither axis is > 0, so the "else 0" arm takes it — a negative stamp is
    // never rendered as a date (graphShortDate would answer "" for it anyway,
    // and the two guards agree on purpose).
    func testInvalidatedAtIgnoresNegativeStamps() {
        XCTAssertEqual(graphInvalidatedAt(edgeMkEdge(1, 1, 2, validUntil: -1, expiredAt: -1)), 0)
        XCTAssertEqual(graphInvalidatedAt(edgeMkEdge(1, 1, 2, validUntil: 7, expiredAt: -1)), 7)
    }
}

// MARK: - graphShortDate

final class EdgeGraphShortDateTests: XCTestCase {

    // "ms ≤ 0 → empty string": 0 is the sentinel a live fact carries, so the
    // strike-through label for a live row must render as nothing at all rather
    // than as 1970.
    func testNonPositiveMillisecondsRenderEmpty() {
        XCTAssertEqual(graphShortDate(ms: 0), "")
        XCTAssertEqual(graphShortDate(ms: -1), "")
        XCTAssertEqual(graphShortDate(ms: -62_135_596_800_000), "")
        XCTAssertEqual(graphShortDate(ms: Int64.min), "")
    }

    // One millisecond past the sentinel is a real (if absurd) instant and gets
    // a real local date — whatever the local calendar says it is.
    func testOneMillisecondIsARealDate() {
        let expected = edgeLocalYMD(Date(timeIntervalSince1970: 0.001))
        XCTAssertEqual(graphShortDate(ms: 1), expected)
        XCTAssertEqual(graphShortDate(ms: 1).count, 10)
    }

    // Both fields zero-padded: a January 5th must not render as "2026-1-5".
    func testSingleDigitMonthAndDayAreZeroPadded() {
        let ms = edgeMillis(edgeLocalDate(2026, 1, 5, 9))
        XCTAssertEqual(graphShortDate(ms: ms), "2026-01-05")
    }

    // The local-midnight boundary, from both sides. Expected values come from
    // the shared Gregorian-local calendar, never from a UTC assumption: one
    // millisecond before a day starts is, by definition of startOfDay, the
    // previous local day.
    func testLocalMidnightBoundary() {
        let cal = edgeGregorian
        let start = cal.startOfDay(for: edgeLocalDate(2026, 3, 15))
        let startMS = edgeMillis(start)

        XCTAssertEqual(graphShortDate(ms: startMS), "2026-03-15")
        XCTAssertEqual(graphShortDate(ms: startMS + 1), "2026-03-15")
        XCTAssertEqual(graphShortDate(ms: startMS - 1), "2026-03-14")
        // The last millisecond of the day is still that day.
        XCTAssertEqual(graphShortDate(ms: startMS + 86_400_000 - 1), edgeLocalYMD(start.addingTimeInterval(86_399.999)))
    }

    // Sub-second remainders are dropped, not rounded into tomorrow.
    func testSubSecondRemaindersDoNotMoveTheDay() {
        let start = edgeGregorian.startOfDay(for: edgeLocalDate(2026, 12, 31))
        let ms = edgeMillis(start)
        XCTAssertEqual(graphShortDate(ms: ms + 999), "2026-12-31")
    }
}

// MARK: - Degrees & radius

final class EdgeGraphDegreeTests: XCTestCase {

    func testEmptyEdgesYieldNoDegrees() {
        XCTAssertTrue(graphDegrees(edges: []).isEmpty)
    }

    // "a self-loop (src == dst) counts 2 for that node" — the one place the
    // per-endpoint increment cannot be written as "+1 for each distinct id".
    func testSelfLoopCountsTwo() {
        let d = graphDegrees(edges: [edgeMkEdge(1, 7, 7)])
        XCTAssertEqual(d[7], 2)
        XCTAssertEqual(d.count, 1)
    }

    func testTwoSelfLoopsCountFour() {
        let d = graphDegrees(edges: [edgeMkEdge(1, 7, 7), edgeMkEdge(2, 7, 7)])
        XCTAssertEqual(d[7], 4)
    }

    // "Ids with no edges are absent" — the map is not seeded from the node
    // list, so an untouched id has no key rather than a key holding 0.
    func testUntouchedIdsHaveNoKeyAtAll() {
        let d = graphDegrees(edges: [edgeMkEdge(1, 1, 2)])
        XCTAssertNil(d[3])
        XCTAssertEqual(Set(d.keys), [1, 2])
    }

    // "regardless of validity": an expired edge still sizes its endpoints, so
    // flipping the invalidated toggle never resizes a node.
    func testInvalidatedAndExpiredEdgesStillCount() {
        let d = graphDegrees(edges: [
            edgeMkEdge(1, 1, 2, validUntil: 5),
            edgeMkEdge(2, 1, 2, expiredAt: 5),
            edgeMkEdge(3, 1, 2),
        ])
        XCTAssertEqual(d[1], 3)
        XCTAssertEqual(d[2], 3)
    }

    // Parallel edges are separate touches; a mixed self-loop adds 2 on top.
    func testParallelEdgesAndASelfLoopAccumulate() {
        let d = graphDegrees(edges: [
            edgeMkEdge(1, 1, 2), edgeMkEdge(2, 1, 2), edgeMkEdge(3, 2, 1), edgeMkEdge(4, 1, 1),
        ])
        XCTAssertEqual(d[1], 5)
        XCTAssertEqual(d[2], 3)
    }

    // MARK: graphNodeRadius

    func testZeroAndNegativeDegreeFloorAtFour() {
        XCTAssertEqual(graphNodeRadius(degree: 0), 4, accuracy: 1e-12)
        XCTAssertEqual(graphNodeRadius(degree: -1), 4, accuracy: 1e-12)
        XCTAssertEqual(graphNodeRadius(degree: Int.min), 4, accuracy: 1e-12)
    }

    // 4 + 2√36 = 16 exactly: the last degree BELOW the clamp and the first one
    // that reaches it land on the same number, which is what makes the clamp
    // invisible rather than a step.
    func testClampMeetsTheCurveAtDegreeThirtySix() {
        XCTAssertEqual(graphNodeRadius(degree: 35), 4 + 2 * CGFloat(35.0).squareRoot(), accuracy: 1e-9)
        XCTAssertLessThan(graphNodeRadius(degree: 35), 16)
        XCTAssertEqual(graphNodeRadius(degree: 36), 16, accuracy: 1e-9)
    }

    // A hub does not grow without bound.
    func testHugeDegreesClampAtSixteen() {
        XCTAssertEqual(graphNodeRadius(degree: 37), 16, accuracy: 1e-12)
        XCTAssertEqual(graphNodeRadius(degree: 10_000), 16, accuracy: 1e-12)
        XCTAssertEqual(graphNodeRadius(degree: Int.max), 16, accuracy: 1e-12)
    }

    func testRadiusIsMonotonicUpToTheClamp() {
        var previous = graphNodeRadius(degree: 0)
        for degree in 1...36 {
            let r = graphNodeRadius(degree: degree)
            XCTAssertGreaterThanOrEqual(r, previous, "degree \(degree)")
            XCTAssertLessThanOrEqual(r, 16, "degree \(degree)")
            previous = r
        }
    }
}

// MARK: - graphPositions / ring fallback

final class EdgeGraphPositionsTests: XCTestCase {

    func testEmptyNodesYieldNoPositions() {
        XCTAssertTrue(graphPositions(nodes: []).isEmpty)
    }

    // "A position for EVERY node" — including nodes the server never laid out.
    func testEveryNodeGetsAPositionEvenWhenNoneAreLaidOut() {
        let nodes = (1...5).map { edgeMkNode(Int64($0), x: 0, y: 0) }
        let p = graphPositions(nodes: nodes)
        XCTAssertEqual(p.count, 5)
        XCTAssertEqual(Set(p.keys), Set(nodes.map(\.id)))
    }

    // "radius 1 when nothing is positioned", and the single missing node takes
    // angle 2π·0/1 = 0.
    func testSingleUnpositionedNodeSitsAtRadiusOneAngleZero() {
        let p = graphPositions(nodes: [edgeMkNode(1, x: 0, y: 0)])
        XCTAssertEqual(p.count, 1)
        edgeAssertPoint(p[1] ?? edgeNaNPoint, 1, 0, accuracy: 1e-12)
    }

    // Every ring member sits at exactly the ring radius, and no two share a
    // spot — the part of "spaced evenly by 2π·k/missing" that holds under any
    // choice of where angle 0 points.
    func testAllUnpositionedRingIsUnitRadiusAndDistinct() {
        let nodes = (1...6).map { edgeMkNode(Int64($0), x: 0, y: 0) }
        let p = graphPositions(nodes: nodes)
        for id in nodes.map(\.id) {
            let point = p[id] ?? edgeNaNPoint
            XCTAssertEqual(edgeHypot(point), 1, accuracy: 1e-9, "id \(id) off the unit ring")
        }
        for a in nodes.map(\.id) {
            for b in nodes.map(\.id) where b > a {
                let pa = p[a] ?? .zero, pb = p[b] ?? .zero
                XCTAssertGreaterThan(edgeHypot(CGPoint(x: pa.x - pb.x, y: pa.y - pb.y)), 1e-6, "ids \(a)/\(b) collide")
            }
        }
    }

    // Two missing nodes are half a turn apart, which is true of any polar
    // convention for 2π·k/2.
    func testTwoUnpositionedNodesAreAntipodal() {
        let p = graphPositions(nodes: [edgeMkNode(1, x: 0, y: 0), edgeMkNode(2, x: 0, y: 0)])
        let a = p[1] ?? .zero, b = p[2] ?? .zero
        edgeAssertPoint(CGPoint(x: a.x + b.x, y: a.y + b.y), 0, 0, accuracy: 1e-9)
    }

    // "x == 0 && y == 0 exactly" — a coordinate that is merely tiny is a real
    // layout result and must survive untouched, however close to the origin.
    func testNearZeroCoordinatesArePositioned() {
        let nodes = [edgeMkNode(1, x: 0, y: 1e-12), edgeMkNode(2, x: -1e-300, y: 0)]
        let p = graphPositions(nodes: nodes)
        edgeAssertPoint(p[1] ?? .zero, 0, 1e-12, accuracy: 0)
        edgeAssertPoint(p[2] ?? .zero, -1e-300, 0, accuracy: 0)
    }

    // "x or y is non-finite" — either axis alone disqualifies the pair, and a
    // non-finite node contributes nothing to the positioned extent (the ring
    // below is sized by the (3,4) node's distance of 5, not by infinity).
    func testNonFiniteCoordinatesAreUnpositionedAndDoNotSizeTheRing() {
        let nodes = [
            edgeMkNode(1, x: 3, y: 4),
            edgeMkNode(2, x: .nan, y: 1),
            edgeMkNode(3, x: 1, y: .infinity),
            edgeMkNode(4, x: -.infinity, y: -.infinity),
        ]
        let p = graphPositions(nodes: nodes)
        edgeAssertPoint(p[1] ?? .zero, 3, 4, accuracy: 0)
        for id: Int64 in [2, 3, 4] {
            let point = p[id] ?? edgeNaNPoint
            XCTAssertTrue(point.x.isFinite && point.y.isFinite, "id \(id) kept a non-finite coordinate")
            XCTAssertEqual(edgeHypot(point), 1.15 * 5, accuracy: 1e-9, "id \(id) off the 1.15×5 ring")
        }
    }

    // "1.15 × max positioned distance from origin", so the ring is strictly
    // outside the positioned extent — and the positioned node keeps its exact
    // server coordinates.
    func testRingSitsOutsideThePositionedExtent() {
        let nodes = [edgeMkNode(1, x: 3, y: 4), edgeMkNode(2, x: -6, y: 8), edgeMkNode(3, x: 0, y: 0)]
        let p = graphPositions(nodes: nodes)
        edgeAssertPoint(p[1] ?? .zero, 3, 4, accuracy: 0)
        edgeAssertPoint(p[2] ?? .zero, -6, 8, accuracy: 0)
        let ring = edgeHypot(p[3] ?? .zero)
        XCTAssertEqual(ring, 1.15 * 10, accuracy: 1e-9)
        XCTAssertGreaterThan(ring, 10)
    }

    // The extent is a distance from the ORIGIN, so a graph laid out entirely in
    // negative space rings just as far out.
    func testExtentUsesDistanceFromOriginNotBounds() {
        let nodes = [edgeMkNode(1, x: -3, y: -4), edgeMkNode(2, x: 0, y: 0)]
        XCTAssertEqual(edgeHypot(graphPositions(nodes: nodes)[2] ?? .zero), 1.15 * 5, accuracy: 1e-9)
    }

    // "deterministic: same input, same output" — the board polls every 30s and
    // a ring that reshuffles on each poll is a screen that will not sit still.
    func testRepeatedCallsAgreeExactly() {
        let nodes = (1...7).map { edgeMkNode(Int64($0), x: $0 == 1 ? 12 : 0, y: 0) }
        let first = graphPositions(nodes: nodes)
        let second = graphPositions(nodes: nodes)
        XCTAssertEqual(first.count, second.count)
        for (id, point) in first {
            edgeAssertPoint(second[id] ?? edgeNaNPoint, point.x, point.y, accuracy: 0, "id \(id)")
        }
    }

    // "in the array order the server sent": the k-th MISSING node takes angle
    // 2π·k/missing, so swapping two unpositioned nodes swaps their slots — the
    // ring is not keyed on id.
    func testRingSlotsFollowArrayOrderRatherThanID() {
        let a = edgeMkNode(1, x: 0, y: 0)
        let b = edgeMkNode(2, x: 0, y: 0)
        let forward = graphPositions(nodes: [a, b])
        let reversed = graphPositions(nodes: [b, a])
        edgeAssertPoint(reversed[2] ?? .zero, (forward[1] ?? .zero).x, (forward[1] ?? .zero).y, accuracy: 1e-12)
        edgeAssertPoint(reversed[1] ?? .zero, (forward[2] ?? .zero).x, (forward[2] ?? .zero).y, accuracy: 1e-12)
    }

    // A positioned node between two missing ones must not consume a ring slot:
    // k counts MISSING nodes, so the two missing ones are still antipodal.
    func testPositionedNodesDoNotConsumeRingSlots() {
        let nodes = [edgeMkNode(1, x: 0, y: 0), edgeMkNode(2, x: 5, y: 0), edgeMkNode(3, x: 0, y: 0)]
        let p = graphPositions(nodes: nodes)
        let a = p[1] ?? .zero, b = p[3] ?? .zero
        edgeAssertPoint(CGPoint(x: a.x + b.x, y: a.y + b.y), 0, 0, accuracy: 1e-9)
    }

    // Nothing missing: the ring code never runs and every coordinate is the
    // server's, bit for bit (the fixture's real doubles, not round numbers).
    func testFullyPositionedGraphIsPassedThroughUnchanged() {
        let nodes = [edgeMkNode(1, x: -103.4903506081596, y: 79.87158754979711),
                     edgeMkNode(2, x: -75.18157241820735, y: 21.115214853358946)]
        let p = graphPositions(nodes: nodes)
        edgeAssertPoint(p[1] ?? .zero, -103.4903506081596, 79.87158754979711, accuracy: 0)
        edgeAssertPoint(p[2] ?? .zero, -75.18157241820735, 21.115214853358946, accuracy: 0)
    }
}

// MARK: - Types, filters, visibility

final class EdgeGraphFilterTests: XCTestCase {

    func testTypeListOfNoNodesIsEmpty() {
        XCTAssertEqual(graphTypeList(nodes: []), [])
    }

    // "Distinct types in first-seen array order" — first-seen, so a repeat does
    // not move an earlier type down the chip row.
    func testTypeListKeepsFirstSightingPosition() {
        let nodes = [
            edgeMkNode(1, type: "person"), edgeMkNode(2, type: "component"),
            edgeMkNode(3, type: "person"), edgeMkNode(4, type: "convention"),
            edgeMkNode(5, type: "component"),
        ]
        XCTAssertEqual(graphTypeList(nodes: nodes), ["person", "component", "convention"])
    }

    // An empty type string is a value like any other — it is distinct, so it
    // gets one chip rather than being folded away or dropped.
    func testEmptyStringIsItsOwnType() {
        let nodes = [edgeMkNode(1, type: ""), edgeMkNode(2, type: "person"), edgeMkNode(3, type: "")]
        XCTAssertEqual(graphTypeList(nodes: nodes), ["", "person"])
    }

    // Type matching is exact string matching, so case is significant on both
    // the list and the hidden set.
    func testTypeMatchingIsCaseSensitive() {
        let nodes = [edgeMkNode(1, type: "Person"), edgeMkNode(2, type: "person")]
        XCTAssertEqual(graphTypeList(nodes: nodes), ["Person", "person"])
        XCTAssertEqual(graphHiddenNodeIDs(nodes: nodes, hiddenTypes: ["person"]), [2])
    }

    func testHiddenIDsOfDegenerateInputsAreEmpty() {
        XCTAssertTrue(graphHiddenNodeIDs(nodes: [], hiddenTypes: ["person"]).isEmpty)
        XCTAssertTrue(graphHiddenNodeIDs(nodes: [edgeMkNode(1, type: "person")], hiddenTypes: []).isEmpty)
        XCTAssertTrue(graphHiddenNodeIDs(nodes: [edgeMkNode(1, type: "person")], hiddenTypes: ["tool"]).isEmpty)
    }

    func testHiddenIDsCoverEveryNodeOfEveryHiddenType() {
        let nodes = [
            edgeMkNode(1, type: "person"), edgeMkNode(2, type: "person"),
            edgeMkNode(3, type: "tool"), edgeMkNode(4, type: "component"),
        ]
        XCTAssertEqual(graphHiddenNodeIDs(nodes: nodes, hiddenTypes: ["person", "tool"]), [1, 2, 3])
    }

    // MARK: graphEdgeVisible

    // "False when either endpoint is hidden" beats every other clause: a shown
    // invalidated edge dangling off a hidden node would draw to nowhere.
    func testHiddenEndpointWinsOverEveryOtherClause() {
        let live = edgeMkEdge(1, 1, 2)
        XCTAssertFalse(graphEdgeVisible(live, hiddenNodeIDs: [1], includeInvalidated: true, now: 100))
        XCTAssertFalse(graphEdgeVisible(live, hiddenNodeIDs: [2], includeInvalidated: true, now: 100))
        XCTAssertFalse(graphEdgeVisible(live, hiddenNodeIDs: [1, 2], includeInvalidated: true, now: 100))

        let dead = edgeMkEdge(2, 1, 2, expiredAt: 5)
        XCTAssertFalse(graphEdgeVisible(dead, hiddenNodeIDs: [2], includeInvalidated: true, now: 100))
    }

    func testSelfLoopOnAHiddenNodeIsHidden() {
        XCTAssertFalse(graphEdgeVisible(edgeMkEdge(1, 7, 7), hiddenNodeIDs: [7], includeInvalidated: true, now: 1))
        XCTAssertTrue(graphEdgeVisible(edgeMkEdge(1, 7, 7), hiddenNodeIDs: [8], includeInvalidated: false, now: 1))
    }

    // The toggle is the SECOND clause and it moves exactly one edge: the same
    // edge flips with it while the live one never does.
    func testInvalidatedToggleGovernsOnlyInvalidatedEdges() {
        let dead = edgeMkEdge(1, 1, 2, validUntil: 50)
        XCTAssertFalse(graphEdgeVisible(dead, hiddenNodeIDs: [], includeInvalidated: false, now: 100))
        XCTAssertTrue(graphEdgeVisible(dead, hiddenNodeIDs: [], includeInvalidated: true, now: 100))

        let live = edgeMkEdge(2, 1, 2)
        XCTAssertTrue(graphEdgeVisible(live, hiddenNodeIDs: [], includeInvalidated: false, now: 100))
        XCTAssertTrue(graphEdgeVisible(live, hiddenNodeIDs: [], includeInvalidated: true, now: 100))
    }

    // The visibility rule reads `now` through the same ≤ boundary the predicate
    // does — one millisecond decides whether an edge is drawn.
    func testVisibilityMovesAtTheValidUntilBoundary() {
        let e = edgeMkEdge(1, 1, 2, validUntil: 1_000)
        XCTAssertFalse(graphEdgeVisible(e, hiddenNodeIDs: [], includeInvalidated: false, now: 1_000))
        XCTAssertTrue(graphEdgeVisible(e, hiddenNodeIDs: [], includeInvalidated: false, now: 999))
    }

    // A hidden set holding ids this edge does not touch changes nothing.
    func testUnrelatedHiddenIDsDoNotHideAnEdge() {
        XCTAssertTrue(graphEdgeVisible(edgeMkEdge(1, 1, 2), hiddenNodeIDs: [3, 4, 5], includeInvalidated: false, now: 1))
    }
}

// MARK: - Fact sorting & text

final class EdgeGraphFactTests: XCTestCase {

    func testSortingNothingYieldsNothing() {
        XCTAssertTrue(graphSortFacts([], now: 1_000).isEmpty)
    }

    // "Live facts before invalidated" is the OUTER key: a strength-1 live fact
    // outranks a strength-10 dead one.
    func testLiveOutranksInvalidatedRegardlessOfStrength() {
        let sorted = graphSortFacts([
            edgeMkEdge(1, 1, 2, strength: 10, expiredAt: 5),
            edgeMkEdge(2, 1, 3, strength: 1),
        ], now: 1_000)
        XCTAssertEqual(sorted.map(\.id), [2, 1])
    }

    // "ties keep input order (stable)" — within one group and one strength the
    // server's order survives, so a re-sort never reshuffles the drawer.
    func testEqualStrengthKeepsInputOrder() {
        let sorted = graphSortFacts([
            edgeMkEdge(31, 1, 2, strength: 5), edgeMkEdge(17, 1, 3, strength: 5), edgeMkEdge(99, 1, 4, strength: 5),
        ], now: 1_000)
        XCTAssertEqual(sorted.map(\.id), [31, 17, 99])
    }

    // Stability holds inside the invalidated group too, and the two groups do
    // not interleave.
    func testStabilityHoldsWithinTheInvalidatedGroup() {
        let sorted = graphSortFacts([
            edgeMkEdge(1, 1, 2, strength: 3, expiredAt: 9),
            edgeMkEdge(2, 1, 3, strength: 3, validUntil: 500),
            edgeMkEdge(3, 1, 4, strength: 3),
            edgeMkEdge(4, 1, 5, strength: 3),
        ], now: 1_000)
        XCTAssertEqual(sorted.map(\.id), [3, 4, 1, 2])
    }

    // Sorting is a permutation: nothing is filtered out on the way through.
    func testSortIsAPermutationOfItsInput() {
        var input: [GraphEdge] = []
        for i in 1...8 {
            let expired: Int64 = i % 2 == 0 ? 5 : 0
            let strength = Int64(i % 3)
            input.append(edgeMkEdge(Int64(i), 1, Int64(i + 100), strength: strength, expiredAt: expired))
        }
        let sorted = graphSortFacts(input, now: 1_000)
        XCTAssertEqual(sorted.count, input.count)
        XCTAssertEqual(Set(sorted.map(\.id)), Set(input.map(\.id)))
    }

    // All-invalidated is still strength-descending; the group key just never
    // changes.
    func testAllInvalidatedStillSortsByStrengthDescending() {
        let sorted = graphSortFacts([
            edgeMkEdge(1, 1, 2, strength: 2, expiredAt: 1),
            edgeMkEdge(2, 1, 3, strength: 9, expiredAt: 1),
            edgeMkEdge(3, 1, 4, strength: 5, expiredAt: 1),
        ], now: 1_000)
        XCTAssertEqual(sorted.map(\.id), [2, 3, 1])
    }

    // The group boundary is the same ≤ the predicate uses: at now == validUntil
    // the fact drops below every live one.
    func testGroupingUsesTheSameNowBoundary() {
        let facts = [edgeMkEdge(1, 1, 2, strength: 9, validUntil: 1_000), edgeMkEdge(2, 1, 3, strength: 1)]
        XCTAssertEqual(graphSortFacts(facts, now: 1_000).map(\.id), [2, 1])
        XCTAssertEqual(graphSortFacts(facts, now: 999).map(\.id), [1, 2])
    }

    // Negative strengths cannot come off the wire but must not invert the
    // comparator if a hand-edited row carries one.
    func testNegativeStrengthSortsLast() {
        let sorted = graphSortFacts([
            edgeMkEdge(1, 1, 2, strength: -5), edgeMkEdge(2, 1, 3, strength: 0),
        ], now: 1)
        XCTAssertEqual(sorted.map(\.id), [2, 1])
    }

    // MARK: graphEndpointName

    // "the name, or #<id> when absent or empty" — both arms of the fallback,
    // including the empty map.
    func testEndpointNameFallsBackForAbsentAndEmpty() {
        XCTAssertEqual(graphEndpointName(id: 42, names: [:]), "#42")
        XCTAssertEqual(graphEndpointName(id: 42, names: [1: "carl"]), "#42")
        XCTAssertEqual(graphEndpointName(id: 7, names: [7: ""]), "#7")
        XCTAssertEqual(graphEndpointName(id: 7, names: [7: "renderMD"]), "renderMD")
    }

    func testEndpointNameOfANegativeOrZeroIDStillFormats() {
        XCTAssertEqual(graphEndpointName(id: 0, names: [:]), "#0")
        XCTAssertEqual(graphEndpointName(id: -3, names: [:]), "#-3")
    }

    // MARK: graphFactText

    func testNonBlankFactIsUsedAsIs() {
        let e = edgeMkEdge(1, 1, 5, predicate: "requested", fact: "carl asked for a double line break")
        XCTAssertEqual(graphFactText(e, names: [5: "renderMD"]), "carl asked for a double line break")
    }

    // "when blank, <predicate> <dst endpoint name>" — the fallback resolves the
    // DESTINATION, and it resolves it through the same #<id> rule.
    func testBlankFactFallsBackToPredicateAndDestination() {
        let e = edgeMkEdge(1, 1, 5, predicate: "implements", fact: "")
        XCTAssertEqual(graphFactText(e, names: [5: "renderMD"]), "implements renderMD")
        XCTAssertEqual(graphFactText(e, names: [1: "carl"]), "implements #5")
        XCTAssertEqual(graphFactText(e, names: [5: ""]), "implements #5")
    }

    // Whitespace-only is blank: a fact of spaces would otherwise render as an
    // empty row in the drawer with no hint of what the edge means.
    func testWhitespaceOnlyFactIsBlank() {
        XCTAssertEqual(graphFactText(edgeMkEdge(1, 1, 5, predicate: "knows", fact: "   "), names: [5: "carl"]), "knows carl")
        XCTAssertEqual(graphFactText(edgeMkEdge(1, 1, 5, predicate: "knows", fact: "\n"), names: [5: "carl"]), "knows carl")
    }

    // "trimmed" — with no predicate the leading space goes with it, so the row
    // never starts with whitespace.
    func testBlankPredicateAndBlankFactTrimToTheEndpointAlone() {
        XCTAssertEqual(graphFactText(edgeMkEdge(1, 1, 5, predicate: "", fact: ""), names: [5: "renderMD"]), "renderMD")
        XCTAssertEqual(graphFactText(edgeMkEdge(1, 1, 5, predicate: "", fact: ""), names: [:]), "#5")
        XCTAssertEqual(graphFactText(edgeMkEdge(1, 1, 5, predicate: "  ", fact: ""), names: [:]), "#5")
    }

    // A self-loop's destination is itself, so the fallback names that node.
    func testSelfLoopFallbackNamesItsOwnNode() {
        XCTAssertEqual(graphFactText(edgeMkEdge(1, 7, 7, predicate: "supersedes", fact: ""), names: [7: "kraken"]), "supersedes kraken")
    }
}

// MARK: - Camera & hit test

final class EdgeGraphCameraTests: XCTestCase {

    // The transform, spelled out: a value check the round-trip tests cannot
    // give (an inverse agrees with a wrong forward transform).
    func testWorldToScreenIsTheStatedAffineMap() {
        let cam = GraphCamera(scale: 2, offset: CGSize(width: 10, height: -5))
        edgeAssertPoint(cam.worldToScreen(CGPoint(x: 3, y: 4)), 16, 3, accuracy: 1e-12)
        edgeAssertPoint(cam.worldToScreen(.zero), 10, -5, accuracy: 1e-12)
    }

    // "round-trip within 1e-6 for scale ≥ 0.01", at both ends of the stated
    // clamp and at the tolerance's own floor.
    func testRoundTripAtExtremeScales() {
        let points = [CGPoint(x: 0, y: 0), CGPoint(x: -1234.5, y: 987.25), CGPoint(x: 9_999.5, y: -8_888.25)]
        for scale: CGFloat in [0.01, 0.05, 1, 7.5, 20] {
            let cam = GraphCamera(scale: scale, offset: CGSize(width: 137.5, height: -42.25))
            for p in points {
                edgeAssertPoint(cam.screenToWorld(cam.worldToScreen(p)), p.x, p.y, accuracy: 1e-6,
                                "world round-trip at scale \(scale)")
                edgeAssertPoint(cam.worldToScreen(cam.screenToWorld(p)), p.x, p.y, accuracy: 1e-6,
                                "screen round-trip at scale \(scale)")
            }
        }
    }

    func testPanAccumulatesAndLeavesScaleAlone() {
        var cam = GraphCamera(scale: 3, offset: CGSize(width: 10, height: 10))
        cam.pan(by: CGSize(width: 5, height: -7))
        XCTAssertEqual(cam.offset.width, 15, accuracy: 1e-12)
        XCTAssertEqual(cam.offset.height, 3, accuracy: 1e-12)
        cam.pan(by: CGSize(width: -20, height: 0))
        XCTAssertEqual(cam.offset.width, -5, accuracy: 1e-12)
        XCTAssertEqual(cam.offset.height, 3, accuracy: 1e-12)
        XCTAssertEqual(cam.scale, 3, accuracy: 1e-12)
    }

    // A zero pan is a no-op: a drag that ends where it began must not nudge.
    func testZeroPanChangesNothing() {
        let before = GraphCamera(scale: 1.5, offset: CGSize(width: 8, height: -3))
        var cam = before
        cam.pan(by: .zero)
        XCTAssertEqual(cam, before)
    }

    // A pinch of exactly 1 is the identity — the value a two-finger gesture
    // reports on its first frame, every frame.
    func testPinchByOneIsTheIdentity() {
        let anchor = CGPoint(x: 211, y: -87)
        var cam = GraphCamera(scale: 1.75, offset: CGSize(width: 33, height: -19))
        let probe = CGPoint(x: -404, y: 606)
        let mappedBefore = cam.worldToScreen(probe)
        cam.pinch(by: 1, anchor: anchor)
        XCTAssertEqual(cam.scale, 1.75, accuracy: 1e-12)
        XCTAssertEqual(cam.offset.width, 33, accuracy: 1e-9)
        XCTAssertEqual(cam.offset.height, -19, accuracy: 1e-9)
        edgeAssertPoint(cam.worldToScreen(probe), mappedBefore.x, mappedBefore.y, accuracy: 1e-9)
    }

    // "keeps the anchor's world point at the same screen point" — the property
    // that makes a pinch feel attached to the fingers, checked at an anchor far
    // from the origin and in both directions.
    func testPinchKeepsItsAnchorFixed() {
        for factor: CGFloat in [0.25, 0.5, 2, 4] {
            var cam = GraphCamera(scale: 1.25, offset: CGSize(width: 30, height: -12))
            let anchor = CGPoint(x: 137.5, y: 42.25)
            let world = cam.screenToWorld(anchor)
            cam.pinch(by: factor, anchor: anchor)
            XCTAssertEqual(cam.scale, 1.25 * factor, accuracy: 1e-9, "scale after ×\(factor)")
            edgeAssertPoint(cam.worldToScreen(world), anchor.x, anchor.y, accuracy: 1e-6, "anchor after ×\(factor)")
        }
    }

    // An anchor at the screen origin is the case where the offset must scale
    // with the zoom rather than stay put.
    func testPinchAtScreenOriginScalesTheOffset() {
        var cam = GraphCamera(scale: 2, offset: CGSize(width: 40, height: -60))
        let world = cam.screenToWorld(.zero)
        cam.pinch(by: 3, anchor: .zero)
        XCTAssertEqual(cam.scale, 6, accuracy: 1e-9)
        edgeAssertPoint(cam.worldToScreen(world), 0, 0, accuracy: 1e-6)
    }

    // Two successive pinches about one anchor compose into their product.
    func testSuccessivePinchesCompose() {
        var cam = GraphCamera(scale: 1, offset: .zero)
        let anchor = CGPoint(x: 90, y: 160)
        let world = cam.screenToWorld(anchor)
        cam.pinch(by: 2, anchor: anchor)
        cam.pinch(by: 2.5, anchor: anchor)
        XCTAssertEqual(cam.scale, 5, accuracy: 1e-9)
        edgeAssertPoint(cam.worldToScreen(world), anchor.x, anchor.y, accuracy: 1e-6)
    }

    // MARK: fitting

    // "a degenerate rect (zero width and height) yields scale 1 centered on its
    // center" — the shape a one-node graph produces, where the fitting scale is
    // otherwise a division by zero.
    func testFittingADegenerateRectYieldsScaleOneCentered() {
        let viewport = CGSize(width: 100, height: 200)
        let cam = GraphCamera.fitting(worldRect: CGRect(x: 5, y: -3, width: 0, height: 0), viewport: viewport)
        XCTAssertEqual(cam.scale, 1, accuracy: 1e-12)
        edgeAssertPoint(cam.worldToScreen(CGPoint(x: 5, y: -3)), 50, 100, accuracy: 1e-9)
    }

    // Half-degenerate: one axis has extent, the other has none. The obligations
    // that still bind are the center mapping, the clamp, and finiteness.
    func testFittingAZeroWidthRectStaysFiniteAndCentered() {
        let viewport = CGSize(width: 320, height: 480)
        for rect in [CGRect(x: 10, y: -50, width: 0, height: 100), CGRect(x: 10, y: -50, width: 100, height: 0)] {
            let cam = GraphCamera.fitting(worldRect: rect, viewport: viewport)
            XCTAssertTrue(cam.scale.isFinite, "scale not finite for \(rect)")
            XCTAssertGreaterThanOrEqual(cam.scale, 0.05, "clamp floor for \(rect)")
            XCTAssertLessThanOrEqual(cam.scale, 20, "clamp ceiling for \(rect)")
            let center = CGPoint(x: rect.midX, y: rect.midY)
            edgeAssertPoint(cam.worldToScreen(center), viewport.width / 2, viewport.height / 2, accuracy: 1e-6,
                            "center for \(rect)")
        }
    }

    // "maps worldRect's center to the viewport center and all four corners
    // inside the viewport", on a rect whose aspect differs from the viewport's
    // in each direction (the axis that does NOT bind is the one a wrong
    // min/max overflows).
    func testFittingFramesEveryCornerForBothAspects() {
        let viewport = CGSize(width: 300, height: 600)
        let rects = [
            CGRect(x: -400, y: -10, width: 800, height: 20),
            CGRect(x: -10, y: -400, width: 20, height: 800),
            CGRect(x: 100, y: 100, width: 50, height: 50),
        ]
        for rect in rects {
            let cam = GraphCamera.fitting(worldRect: rect, viewport: viewport)
            edgeAssertPoint(cam.worldToScreen(CGPoint(x: rect.midX, y: rect.midY)),
                            viewport.width / 2, viewport.height / 2, accuracy: 1e-6, "center for \(rect)")
            for corner in edgeCorners(rect) {
                let s = cam.worldToScreen(corner)
                XCTAssertGreaterThanOrEqual(s.x, -1e-6, "corner \(corner) left of viewport for \(rect)")
                XCTAssertLessThanOrEqual(s.x, viewport.width + 1e-6, "corner \(corner) right of viewport for \(rect)")
                XCTAssertGreaterThanOrEqual(s.y, -1e-6, "corner \(corner) above viewport for \(rect)")
                XCTAssertLessThanOrEqual(s.y, viewport.height + 1e-6, "corner \(corner) below viewport for \(rect)")
            }
        }
    }

    // "scale is clamped to [0.05, 20]" — both ends, reached from rects whose
    // honest fitting scale is far outside them. The center mapping survives the
    // clamp; the corner-framing obligation cannot and is not asserted here.
    func testFittingClampsAtBothEnds() {
        let viewport = CGSize(width: 100, height: 100)

        let huge = GraphCamera.fitting(worldRect: CGRect(x: -5_000_000, y: -5_000_000, width: 10_000_000, height: 10_000_000),
                                       viewport: viewport)
        XCTAssertEqual(huge.scale, 0.05, accuracy: 1e-12)
        edgeAssertPoint(huge.worldToScreen(.zero), 50, 50, accuracy: 1e-6)

        let tiny = GraphCamera.fitting(worldRect: CGRect(x: 1, y: 1, width: 0.0001, height: 0.0001), viewport: viewport)
        XCTAssertEqual(tiny.scale, 20, accuracy: 1e-12)
        edgeAssertPoint(tiny.worldToScreen(CGPoint(x: 1.00005, y: 1.00005)), 50, 50, accuracy: 1e-6)
    }

    // A viewport of zero is what the first layout pass hands over. The clamp is
    // stated unconditionally, so the answer must still be a usable camera
    // rather than a NaN that poisons every later transform.
    func testFittingAZeroViewportStillYieldsAClampedScale() {
        let cam = GraphCamera.fitting(worldRect: CGRect(x: 0, y: 0, width: 100, height: 100), viewport: .zero)
        XCTAssertTrue(cam.scale.isFinite)
        XCTAssertGreaterThanOrEqual(cam.scale, 0.05)
        XCTAssertLessThanOrEqual(cam.scale, 20)
        XCTAssertTrue(cam.offset.width.isFinite && cam.offset.height.isFinite)
    }

    private func edgeCorners(_ r: CGRect) -> [CGPoint] {
        [CGPoint(x: r.minX, y: r.minY), CGPoint(x: r.maxX, y: r.minY),
         CGPoint(x: r.minX, y: r.maxY), CGPoint(x: r.maxX, y: r.maxY)]
    }
}

final class EdgeGraphHitTestTests: XCTestCase {

    private let identity = GraphCamera(scale: 1, offset: .zero)

    // "nil otherwise (including empty positions)" — an empty graph is tapped
    // every time the screen is opened before the first payload lands.
    func testEmptyPositionsMissEverything() {
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: [:], camera: identity, maxDistance: 24))
    }

    // "only if that distance ≤ maxDistance": the boundary hits, and the first
    // representable step past it does not. 24 is exact in binary and √576 is
    // exactly 24, so there is no float slop to excuse either side.
    func testExactlyMaxDistanceHitsAndJustPastMisses() {
        let positions: [Int64: CGPoint] = [1: CGPoint(x: 24, y: 0)]
        XCTAssertEqual(graphHitTest(screenPoint: .zero, positions: positions, camera: identity, maxDistance: 24), 1)
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: positions, camera: identity, maxDistance: 23.999))
        XCTAssertNil(graphHitTest(screenPoint: CGPoint(x: -0.001, y: 0), positions: positions, camera: identity, maxDistance: 24))
    }

    // A maxDistance of 0 admits only a dead-center tap.
    func testZeroMaxDistanceAdmitsOnlyAnExactHit() {
        let positions: [Int64: CGPoint] = [1: CGPoint(x: 10, y: 10)]
        XCTAssertEqual(graphHitTest(screenPoint: CGPoint(x: 10, y: 10), positions: positions, camera: identity, maxDistance: 0), 1)
        XCTAssertNil(graphHitTest(screenPoint: CGPoint(x: 10, y: 10.5), positions: positions, camera: identity, maxDistance: 0))
    }

    // A negative reach can never be satisfied — distance is never negative.
    func testNegativeMaxDistanceNeverHits() {
        let positions: [Int64: CGPoint] = [1: .zero]
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: positions, camera: identity, maxDistance: -1))
    }

    // "in SCREEN space": zoomed out, a node 100 world units away is 10pt away
    // and inside the 24pt reach; zoomed in, a node 5 world units away is 50pt
    // away and outside it. A world-space comparison gets both backwards.
    func testDistanceIsMeasuredAfterTheCameraTransform() {
        let far: [Int64: CGPoint] = [1: CGPoint(x: 100, y: 0)]
        XCTAssertEqual(graphHitTest(screenPoint: .zero, positions: far,
                                    camera: GraphCamera(scale: 0.1, offset: .zero), maxDistance: 24), 1)

        let near: [Int64: CGPoint] = [1: CGPoint(x: 5, y: 0)]
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: near,
                                  camera: GraphCamera(scale: 10, offset: .zero), maxDistance: 24))
    }

    // The offset counts too: a panned canvas moves the touch targets with it.
    func testOffsetMovesTheTargets() {
        let positions: [Int64: CGPoint] = [1: CGPoint(x: 0, y: 0)]
        let panned = GraphCamera(scale: 1, offset: CGSize(width: 200, height: 300))
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: positions, camera: panned, maxDistance: 24))
        XCTAssertEqual(graphHitTest(screenPoint: CGPoint(x: 200, y: 300), positions: positions, camera: panned, maxDistance: 24), 1)
    }

    // Nearest wins, and the loser being closer to the tap in only ONE axis is
    // not enough.
    func testNearestOfSeveralCandidatesWins() {
        let positions: [Int64: CGPoint] = [
            1: CGPoint(x: 20, y: 0),
            2: CGPoint(x: 0, y: 5),
            3: CGPoint(x: 12, y: 12),
        ]
        XCTAssertEqual(graphHitTest(screenPoint: .zero, positions: positions, camera: identity, maxDistance: 24), 2)
    }

    // "Exact ties may return either contender" — the assertion is that a tie
    // resolves to ONE of them rather than to nil, which is all the contract
    // promises (dictionary iteration order is not stable across runs).
    func testAnExactTieResolvesToSomeContender() {
        let positions: [Int64: CGPoint] = [1: CGPoint(x: 10, y: 0), 2: CGPoint(x: -10, y: 0)]
        let hit = graphHitTest(screenPoint: .zero, positions: positions, camera: identity, maxDistance: 24)
        XCTAssertNotNil(hit)
        XCTAssertTrue(hit == 1 || hit == 2, "tie resolved to \(String(describing: hit))")
    }

    // Everything out of reach is a miss even when the graph is crowded — this
    // is what makes "tap empty canvas deselects" reachable at all.
    func testATapFarFromEveryNodeMisses() {
        let positions: [Int64: CGPoint] = [1: CGPoint(x: 100, y: 100), 2: CGPoint(x: -100, y: -100), 3: CGPoint(x: 0, y: 60)]
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: positions, camera: identity, maxDistance: 24))
    }

    // A single node exactly under the finger, with the reach wide open.
    func testExactCenterHitsAtAnyReach() {
        let positions: [Int64: CGPoint] = [9: CGPoint(x: -7.5, y: 3.25)]
        let cam = GraphCamera(scale: 2, offset: CGSize(width: 11, height: -4))
        let screen = cam.worldToScreen(CGPoint(x: -7.5, y: 3.25))
        XCTAssertEqual(graphHitTest(screenPoint: screen, positions: positions, camera: cam, maxDistance: 24), 9)
    }
}

// MARK: - Wire decoding

final class EdgeGraphDecodeTests: XCTestCase {

    // The captured board body, with the two absences that matter: no `aliases`
    // key on any node, and both time axes present and 0.
    func testCapturedGraphBodyDecodes() throws {
        let payload: GraphPayload = try edgeDecode(edgeGraphFixtureJSON)
        XCTAssertEqual(payload.nodes.count, 3)
        XCTAssertEqual(payload.edges.count, 2)
        XCTAssertNil(payload.nodes[0].aliases)
        XCTAssertEqual(payload.nodes[0].id, 1)
        XCTAssertEqual(payload.nodes[0].name, "carl")
        XCTAssertEqual(payload.nodes[0].type, "person")
        XCTAssertEqual(payload.nodes[0].x, -103.4903506081596, accuracy: 1e-12)
        XCTAssertEqual(payload.nodes[0].y, 79.87158754979711, accuracy: 1e-12)
        XCTAssertEqual(payload.edges[0].validUntil, 0)
        XCTAssertEqual(payload.edges[0].expiredAt, 0)
        XCTAssertEqual(payload.edges[0].episode, 3)
        XCTAssertEqual(payload.stats.liveEdges, 29)
        XCTAssertEqual(payload.stats.missingEmbeddings, 0)
        XCTAssertEqual(payload.stats.pending, 653)
        // 0 on both axes is the live sentinel, decoded rather than inferred.
        XCTAssertFalse(graphIsInvalidated(payload.edges[0], now: 1_769_040_000_000))
    }

    // The node route's body differs from the board's in exactly one way, and
    // the empty array must not collapse into the absent case.
    func testCapturedNodeDetailDecodesWithEmptyAliases() throws {
        let detail: GraphNodeDetail = try edgeDecode(edgeNodeDetailFixtureJSON)
        XCTAssertEqual(detail.node.id, 5)
        XCTAssertNotNil(detail.node.aliases)
        XCTAssertEqual(detail.node.aliases?.count, 0)
        XCTAssertEqual(detail.edges.count, 2)
        XCTAssertEqual(detail.edges.map(\.id), [4, 5])
    }

    // Absent, null and empty are three different wire shapes and only the first
    // two are nil.
    func testAliasesAbsentNullAndPopulated() throws {
        let base = #"{"id":5,"name":"renderMD","type":"component","description":"d","x":1,"y":2"#
        XCTAssertNil(try edgeDecode(base + "}", as: GraphNode.self).aliases)
        XCTAssertNil(try edgeDecode(base + #","aliases":null}"#, as: GraphNode.self).aliases)
        XCTAssertEqual(try edgeDecode(base + #","aliases":[]}"#, as: GraphNode.self).aliases, [])
        XCTAssertEqual(try edgeDecode(base + #","aliases":["md renderer","renderer"]}"#, as: GraphNode.self).aliases,
                       ["md renderer", "renderer"])
    }

    // "Unknown future JSON keys never fail the decode" — the server is allowed
    // to grow additively without the phone in the room.
    func testUnknownKeysAtEveryLevelAreIgnored() throws {
        let json = """
        {"nodes":[{"id":1,"name":"carl","type":"person","description":"d","x":1.5,"y":-2.5,"community":7,"pinned":true}],
         "edges":[{"id":4,"src":1,"dst":5,"predicate":"requested","fact":"f","strength":9,"occurrences":1,
                   "valid_until":0,"expired_at":0,"episode":3,"embedding":[0.1,0.2],"source":"transcript"}],
         "stats":{"nodes":35,"edges":29,"live_edges":29,"episodes":678,"pending":653,"missing_embeddings":0,"orphans":2},
         "layout":{"algorithm":"forceatlas2"},"generated_at":1769040000000}
        """
        let payload: GraphPayload = try edgeDecode(json)
        XCTAssertEqual(payload.nodes.count, 1)
        XCTAssertEqual(payload.edges[0].strength, 9)
        XCTAssertEqual(payload.stats.episodes, 678)
    }

    func testUnknownKeysInTheNodeDetailBodyAreIgnored() throws {
        let json = """
        {"node":{"id":5,"name":"renderMD","type":"component","description":"d","x":0,"y":0,"aliases":[],"degree":12},
         "edges":[],"neighbors":[1,2,3]}
        """
        let detail: GraphNodeDetail = try edgeDecode(json)
        XCTAssertEqual(detail.node.id, 5)
        XCTAssertTrue(detail.edges.isEmpty)
    }

    // Empty collections are legal bodies: a fresh database serves exactly this.
    func testEmptyPayloadDecodes() throws {
        let payload: GraphPayload = try edgeDecode("""
        {"nodes":[],"edges":[],"stats":{"nodes":0,"edges":0,"live_edges":0,"episodes":0,"pending":0,"missing_embeddings":0}}
        """)
        XCTAssertTrue(payload.nodes.isEmpty)
        XCTAssertTrue(payload.edges.isEmpty)
        XCTAssertEqual(payload.stats.nodes, 0)
    }

    // The snake-cased keys carry real values, not just zeros, and land on the
    // camel-cased properties.
    func testSnakeCasedTimeAxesCarryTheirValues() throws {
        let e: GraphEdge = try edgeDecode("""
        {"id":88,"src":1,"dst":2,"predicate":"p","fact":"f","strength":3,"occurrences":4,
         "valid_until":1769040000000,"expired_at":1769126400000,"episode":9}
        """)
        XCTAssertEqual(e.validUntil, 1_769_040_000_000)
        XCTAssertEqual(e.expiredAt, 1_769_126_400_000)
        XCTAssertEqual(e.occurrences, 4)
    }

    // camelCase on the wire is NOT the contract: the decoder uses no key
    // strategy, so `validUntil` is an unknown key and the required `valid_until`
    // is missing — that has to fail rather than silently read 0.
    func testCamelCasedTimeKeysDoNotDecode() {
        let json = """
        {"id":88,"src":1,"dst":2,"predicate":"p","fact":"f","strength":3,"occurrences":4,
         "validUntil":5,"expiredAt":6,"episode":9}
        """
        XCTAssertThrowsError(try edgeDecode(json, as: GraphEdge.self))
    }

    // Non-optional properties are non-optional: a body missing one is an error
    // the caller surfaces, never a default-constructed row.
    func testMissingRequiredKeysThrow() {
        XCTAssertThrowsError(try edgeDecode(#"{"id":1,"name":"carl","x":1,"y":2,"description":"d"}"#, as: GraphNode.self))
        XCTAssertThrowsError(try edgeDecode(#"{"id":1,"src":2,"dst":3,"predicate":"p","fact":"f"}"#, as: GraphEdge.self))
        XCTAssertThrowsError(try edgeDecode(#"{"nodes":0,"edges":0}"#, as: GraphStats.self))
    }

    // A node the layout pass has not reached yet arrives as an honest 0/0 pair
    // — the value graphPositions treats as "put me on the ring".
    func testUnlaidOutNodeDecodesAsExactZeroes() throws {
        let n: GraphNode = try edgeDecode(#"{"id":3,"name":"n","type":"tool","description":"","x":0,"y":0}"#)
        XCTAssertEqual(n.x, 0)
        XCTAssertEqual(n.y, 0)
        XCTAssertEqual(edgeHypot(graphPositions(nodes: [n])[3] ?? .zero), 1, accuracy: 1e-9)
    }

    // Equatable is part of the frozen signature and the drawer's diffing leans
    // on it; two decodes of one body must compare equal.
    func testDecodedValuesAreEquatable() throws {
        let a: GraphPayload = try edgeDecode(edgeGraphFixtureJSON)
        let b: GraphPayload = try edgeDecode(edgeGraphFixtureJSON)
        XCTAssertEqual(a, b)
        XCTAssertNotEqual(a.nodes[0], a.nodes[1])
    }
}

// MARK: - GraphViewModel error paths

private struct EdgeStubFailure: Error, Equatable { let label: String }

/// One recorded detail fetch. A named struct rather than a tuple — tuples for
/// structured data are against the house convention, and `.url`/`.id` read
/// better than `.0`/`.1` at the assertion site.
private struct EdgeNodeCall: Equatable {
    let url: String
    let id: Int64
}

/// A reference box the stub closures write into — the closures are non-isolated
/// so the recorder cannot be MainActor-bound.
private final class EdgeStubCalls {
    var graphBaseURLs: [String] = []
    var nodeCalls: [EdgeNodeCall] = []
    /// Flipped between loads to make the SECOND fetch fail. A box rather than a
    /// captured local `var` so the escaping stub never mutates a stack slot.
    var failGraph = false
    var graphCount: Int { graphBaseURLs.count }
    var nodeCount: Int { nodeCalls.count }
}

/// Lets a spawned detail/load task reach its assignment. The stubs never touch
/// the network, so a handful of yields is enough; the count is generous
/// because the cost of an extra yield is nothing and the cost of a flake is a
/// test nobody trusts.
private func edgeSettle(_ rounds: Int = 40) async {
    for _ in 0..<rounds { await Task.yield() }
}

@MainActor
final class EdgeGraphViewModelTests: XCTestCase {

    private func edgeMakeVM(
        calls: EdgeStubCalls,
        graph: @escaping (String) async throws -> GraphPayload,
        node: @escaping (String, Int64) async throws -> GraphNodeDetail = { _, _ in throw EdgeStubFailure(label: "no node stub") }
    ) -> GraphViewModel {
        GraphViewModel(
            fetchGraph: { url in
                calls.graphBaseURLs.append(url)
                return try await graph(url)
            },
            fetchNode: { url, id in
                calls.nodeCalls.append(EdgeNodeCall(url: url, id: id))
                return try await node(url, id)
            }
        )
    }

    private var edgeSamplePayload: GraphPayload {
        edgeMkPayload(
            nodes: [edgeMkNode(1, "carl", type: "person"), edgeMkNode(2, "renderMD"), edgeMkNode(3, "lonely")],
            edges: [edgeMkEdge(10, 1, 2, strength: 5), edgeMkEdge(11, 2, 1, strength: 9), edgeMkEdge(12, 2, 2)]
        )
    }

    // MARK: failure paths

    // "A failed load sets errorText and PRESERVES the previous payload" — the
    // screen keeps its picture when the Mac goes to sleep mid-poll.
    func testFailedLoadKeepsTheStalePayloadAndSurfacesAnError() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in
            if calls.failGraph { throw EdgeStubFailure(label: "offline") }
            return payload
        })

        await vm.load(baseURL: "http://mac:7777")
        XCTAssertEqual(vm.payload, payload)
        XCTAssertNil(vm.errorText)

        calls.failGraph = true
        await vm.load(baseURL: "http://mac:7777")
        XCTAssertEqual(vm.payload, payload, "a failed poll dropped the picture")
        XCTAssertNotNil(vm.errorText)
        XCTAssertFalse(vm.errorText?.isEmpty ?? true, "an empty error line reads as no error at all")
        XCTAssertEqual(calls.graphCount, 2)
    }

    // The first load failing leaves nothing to fall back to — payload stays nil
    // rather than becoming an empty graph, which the screen words differently.
    func testFirstLoadFailureLeavesNoPayload() async {
        let calls = EdgeStubCalls()
        let vm = edgeMakeVM(calls: calls, graph: { _ in throw EdgeStubFailure(label: "boom") })
        await vm.load(baseURL: "http://mac:7777")
        XCTAssertNil(vm.payload)
        XCTAssertNotNil(vm.errorText)
    }

    // "A successful load replaces payload and CLEARS errorText" — the recovery
    // half of the rule above, which is the half a sticky error line breaks.
    func testRecoveryClearsTheErrorLine() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        calls.failGraph = true
        let vm = edgeMakeVM(calls: calls, graph: { _ in
            if calls.failGraph { throw EdgeStubFailure(label: "offline") }
            return payload
        })

        await vm.load(baseURL: "http://mac:7777")
        XCTAssertNotNil(vm.errorText)
        calls.failGraph = false
        await vm.load(baseURL: "http://mac:7777")
        XCTAssertNil(vm.errorText)
        XCTAssertEqual(vm.payload, payload)
    }

    // "A detail fetch failure leaves errorText untouched" — the drawer degrades
    // to payload facts instead of painting the whole screen as broken.
    func testDetailFailureDoesNotTouchTheErrorLine() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, _ in throw EdgeStubFailure(label: "node route down") })

        await vm.load(baseURL: "http://mac:7777")
        let before = vm.errorText
        vm.select(1)
        await edgeSettle()

        XCTAssertEqual(vm.errorText, before)
        XCTAssertNil(vm.errorText)
        XCTAssertEqual(vm.selectedID, 1, "a failed detail must not drop the selection")
        XCTAssertNil(vm.detail)
        // The degraded mode: payload facts still answer.
        XCTAssertEqual(vm.selectedNode?.id, 1)
        XCTAssertEqual(vm.selectedFacts.map(\.id), [11, 10])
        XCTAssertEqual(calls.nodeCount, 1)
    }

    // MARK: selection edges

    // "selection before any load sets selectedID and fetches nothing — no
    // crash": tapping through a deep link before the first payload lands.
    func testSelectBeforeAnyLoadFetchesNothing() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload })

        vm.select(7)
        await edgeSettle()

        XCTAssertEqual(vm.selectedID, 7)
        XCTAssertNil(vm.detail)
        XCTAssertNil(vm.selectedNode)
        XCTAssertTrue(vm.selectedFacts.isEmpty)
        XCTAssertEqual(calls.nodeCount, 0, "a detail fetch fired with no base URL to fetch from")
        XCTAssertNil(vm.errorText)
    }

    // "select(nil) clears selectedID and detail" — the empty-canvas tap.
    func testSelectNilClearsEverythingSelectionShaped() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, id in GraphNodeDetail(node: edgeMkNode(id, "detail"), edges: [edgeMkEdge(50, id, 3)]) })

        await vm.load(baseURL: "http://mac:7777")
        vm.select(1)
        await edgeSettle()
        XCTAssertNotNil(vm.detail)

        vm.select(nil)
        XCTAssertNil(vm.selectedID)
        XCTAssertNil(vm.detail)
        XCTAssertNil(vm.selectedNode)
        XCTAssertTrue(vm.selectedFacts.isEmpty)
    }

    // select(nil) with nothing selected is a no-op rather than a fetch or a
    // crash — every empty-canvas tap after the first one takes this path.
    func testSelectNilWithNothingSelectedIsANoOp() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload })

        await vm.load(baseURL: "http://mac:7777")
        vm.select(nil)
        await edgeSettle()
        XCTAssertNil(vm.selectedID)
        XCTAssertEqual(calls.nodeCount, 0)
    }

    // "select(id) ... clears any previous detail": checked with no await in
    // between, so the spawned fetch provably has not run yet — the exact window
    // in which a stale drawer would show the WRONG node's facts.
    func testSelectingASecondNodeClearsTheFirstDetailImmediately() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, id in GraphNodeDetail(node: edgeMkNode(id, "detail"), edges: []) })

        await vm.load(baseURL: "http://mac:7777")
        vm.select(1)
        await edgeSettle()
        XCTAssertEqual(vm.detail?.node.id, 1)

        vm.select(2)
        XCTAssertNil(vm.detail, "the previous node's detail survived the selection change")
        XCTAssertEqual(vm.selectedID, 2)
        // The payload fallback is what fills the gap until the fetch lands.
        XCTAssertEqual(vm.selectedNode?.id, 2)
        XCTAssertEqual(Set(vm.selectedFacts.map(\.id)), [10, 11, 12])
    }

    // "selectedNode is detail.node when detail.node.id == selectedID, ELSE the
    // payload node" — a detail describing some other node is not this node's
    // detail, however it got there.
    func testMismatchedDetailFallsBackToThePayload() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, _ in GraphNodeDetail(node: edgeMkNode(99, "someone else"),
                                                            edges: [edgeMkEdge(77, 99, 1, strength: 100)]) })

        await vm.load(baseURL: "http://mac:7777")
        vm.select(1)
        await edgeSettle()

        XCTAssertEqual(vm.selectedNode?.id, 1)
        XCTAssertEqual(vm.selectedNode?.name, "carl")
        XCTAssertEqual(vm.selectedFacts.map(\.id), [11, 10], "a mismatched detail's edges reached the drawer")
    }

    // A selection that no payload node answers: the drawer has an id and
    // nothing to draw, and must say so with nil rather than crash.
    func testSelectingAnUnknownIDYieldsNoNodeAndNoFacts() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, _ in throw EdgeStubFailure(label: "404") })

        await vm.load(baseURL: "http://mac:7777")
        vm.select(4_242)
        await edgeSettle()

        XCTAssertEqual(vm.selectedID, 4_242)
        XCTAssertNil(vm.selectedNode)
        XCTAssertTrue(vm.selectedFacts.isEmpty)
        XCTAssertNil(vm.errorText)
    }

    // A node with no edges at all: an empty fact list is a legitimate answer,
    // not an absence of data.
    func testSelectedNodeWithNoTouchingEdgesHasNoFacts() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, _ in throw EdgeStubFailure(label: "offline") })

        await vm.load(baseURL: "http://mac:7777")
        vm.select(3)
        await edgeSettle()
        XCTAssertEqual(vm.selectedNode?.id, 3)
        XCTAssertTrue(vm.selectedFacts.isEmpty)
    }

    // A self-loop touches its node once in the fallback list, not twice — the
    // drawer would otherwise print the same sentence twice.
    func testSelfLoopAppearsOnceInTheFallbackFacts() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, _ in throw EdgeStubFailure(label: "offline") })

        await vm.load(baseURL: "http://mac:7777")
        vm.select(2)
        await edgeSettle()
        XCTAssertEqual(vm.selectedFacts.filter { $0.id == 12 }.count, 1)
        XCTAssertEqual(Set(vm.selectedFacts.map(\.id)), [10, 11, 12])
    }

    // Nothing selected is the drawer-closed state: no facts, whatever the
    // payload holds.
    func testNoSelectionMeansNoFacts() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload })
        await vm.load(baseURL: "http://mac:7777")
        XCTAssertNil(vm.selectedID)
        XCTAssertNil(vm.selectedNode)
        XCTAssertTrue(vm.selectedFacts.isEmpty)
    }

    // "fetches detail using the base URL of the LAST load" — after the server
    // address changes in settings, the detail route must not still be asking
    // the old host.
    func testDetailFetchUsesTheMostRecentBaseURL() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, id in GraphNodeDetail(node: edgeMkNode(id), edges: []) })

        await vm.load(baseURL: "http://old:7777")
        await vm.load(baseURL: "http://new:7777")
        vm.select(1)
        await edgeSettle()

        XCTAssertEqual(calls.nodeCount, 1)
        XCTAssertEqual(calls.nodeCalls.last?.url, "http://new:7777")
        XCTAssertEqual(calls.nodeCalls.last?.id, 1)
    }

    // The detail's own edges win once it lands and matches, sorted by the same
    // rule the payload fallback uses — including the live-before-invalidated
    // split, which the fallback list here does not exercise.
    func testMatchingDetailEdgesReplaceTheFallbackAndStaySorted() async {
        let calls = EdgeStubCalls()
        let payload = edgeSamplePayload
        let vm = edgeMakeVM(calls: calls, graph: { _ in payload },
                            node: { _, id in
                                GraphNodeDetail(node: edgeMkNode(id, "carl"), edges: [
                                    edgeMkEdge(60, id, 2, strength: 10, expiredAt: 5),
                                    edgeMkEdge(61, id, 3, strength: 1),
                                    edgeMkEdge(62, id, 2, strength: 4),
                                ])
                            })

        await vm.load(baseURL: "http://mac:7777")
        vm.select(1)
        await edgeSettle()

        XCTAssertEqual(vm.detail?.node.id, 1)
        XCTAssertEqual(vm.selectedFacts.map(\.id), [62, 61, 60])
    }
}
