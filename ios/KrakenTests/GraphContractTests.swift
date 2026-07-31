import XCTest
import Foundation
import CoreGraphics
import UIKit
@testable import Kraken

// GraphContractTests — blind tests for the native iOS knowledge-graph screen,
// authored from artifacts/50-contract.md ALONE, before any of the code they
// exercise existed. Dimension: the stated obligations and happy paths (the
// LOGIC of the contract). A second author owns the edge cases; where the
// contract is silent this file asserts nothing.
//
// Two deliberate authoring choices, both about surviving a blind join:
//
//  1. Every wire value is built by DECODING JSON rather than by a memberwise
//     initializer. The contract freezes the JSON shape and the property list;
//     it does not freeze the presence of a synthesized memberwise init (a
//     hand-written `init(from:)` inside the struct body would silently remove
//     one). Decoding is the construction path the contract actually promises.
//  2. `graphPositions`' ring is asserted by RADIUS, EVEN SPACING and ORDER
//     DEPENDENCE, never by literal coordinates: the contract fixes the spacing
//     (`2π·k/missing`) but neither the base angle nor the direction of travel,
//     so a literal (x, y) would be an assertion nobody froze.
//
// Every file-scope identifier here is `contract`-prefixed (types `Contract`-)
// because a second author shares this test target's namespace.

// MARK: - Fixtures (verbatim from 50-contract.md)

/// `GET /api/graph`, captured off the real server. Used verbatim.
private let contractGraphFixture = """
{"nodes":[{"id":1,"name":"carl","type":"person","description":"The operator of kraken: steers cells from the board, leaves per-artifact feedback, and accepts or rejects every job.","x":-103.4903506081596,"y":79.87158754979711},{"id":5,"name":"renderMD","type":"component","description":"The kraken board's markdown renderer, which turns artifact .md files into the HTML the web board displays.","x":-75.18157241820735,"y":21.115214853358946},{"id":6,"name":"never-hard-wrap rule","type":"convention","description":"One paragraph, bullet, or table row is ONE source line in markdown artifacts; mid-sentence hard wraps are forbidden.","x":-50.51906630702463,"y":8.879077510894259}],"edges":[{"id":4,"src":1,"dst":5,"predicate":"requested","fact":"carl asked that kraken's markdown renderer require a double line break to break a paragraph - a single line break in raw markdown must not break lines in the rendered output.","strength":9,"occurrences":1,"valid_until":0,"expired_at":0,"episode":3},{"id":5,"src":5,"dst":6,"predicate":"implements","fact":"renderMD folds consecutive prose lines into one paragraph and requires a blank line to start a new one (PR #94, shipped by the dream pass of 2026-07-28), measured over 16,810 real .md files with none rendering worse.","strength":8,"occurrences":1,"valid_until":0,"expired_at":0,"episode":3}],"stats":{"nodes":35,"edges":29,"live_edges":29,"episodes":678,"pending":653,"missing_embeddings":0}}
"""

/// `GET /api/graph/node?id=5` — the node object quoted verbatim by the
/// contract (note `aliases`, absent from the board payload above), over the
/// same two edge objects.
private let contractNodeDetailFixture = """
{"node":{"id":5,"name":"renderMD","type":"component","description":"The kraken board's markdown renderer, which turns artifact .md files into the HTML the web board displays.","x":-75.18157241820735,"y":21.115214853358946,"aliases":[]},"edges":[{"id":4,"src":1,"dst":5,"predicate":"requested","fact":"carl asked that kraken's markdown renderer require a double line break to break a paragraph - a single line break in raw markdown must not break lines in the rendered output.","strength":9,"occurrences":1,"valid_until":0,"expired_at":0,"episode":3},{"id":5,"src":5,"dst":6,"predicate":"implements","fact":"renderMD folds consecutive prose lines into one paragraph and requires a blank line to start a new one (PR #94, shipped by the dream pass of 2026-07-28), measured over 16,810 real .md files with none rendering worse.","strength":8,"occurrences":1,"valid_until":0,"expired_at":0,"episode":3}]}
"""

// MARK: - Builders

private func contractDecode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(json.utf8))
}

private func contractNodeJSON(id: Int64, name: String, type: String, description: String,
                              x: Double, y: Double, aliases: [String]?) -> String {
    var s = "{\"id\":\(id),\"name\":\"\(name)\",\"type\":\"\(type)\","
    s += "\"description\":\"\(description)\",\"x\":\(x),\"y\":\(y)"
    if let aliases {
        s += ",\"aliases\":[" + aliases.map { "\"\($0)\"" }.joined(separator: ",") + "]"
    }
    return s + "}"
}

/// Gregorian in the local timezone — the calendar pair the contract binds
/// graphShortDate to. Hoisted so no test can drift back to Calendar.current,
/// which resolves "year 2026" in a different era on non-Gregorian regions.
private let contractGregorian = Calendar(identifier: .gregorian)

private func contractMkNode(id: Int64, name: String = "node", type: String = "component",
                            description: String = "", x: Double = 1, y: Double = 1,
                            aliases: [String]? = nil) -> GraphNode {
    let json = contractNodeJSON(id: id, name: name, type: type, description: description,
                                x: x, y: y, aliases: aliases)
    // try! deliberately: a fixture that will not decode is a test-authoring
    // bug and should stop the run loudly.
    return try! contractDecode(json)
}

private func contractMkEdge(id: Int64, src: Int64, dst: Int64, predicate: String = "relates-to",
                            fact: String = "a fact", strength: Int64 = 1, occurrences: Int64 = 1,
                            validUntil: Int64 = 0, expiredAt: Int64 = 0,
                            episode: Int64 = 1) -> GraphEdge {
    var json = "{\"id\":\(id),\"src\":\(src),\"dst\":\(dst),\"predicate\":\"\(predicate)\","
    json += "\"fact\":\"\(fact)\",\"strength\":\(strength),\"occurrences\":\(occurrences),"
    json += "\"valid_until\":\(validUntil),\"expired_at\":\(expiredAt),\"episode\":\(episode)}"
    return try! contractDecode(json)
}

private func contractMkStats(nodes: Int64 = 0, edges: Int64 = 0, liveEdges: Int64 = 0,
                             episodes: Int64 = 0, pending: Int64 = 0,
                             missingEmbeddings: Int64 = 0) -> GraphStats {
    var json = "{\"nodes\":\(nodes),\"edges\":\(edges),\"live_edges\":\(liveEdges),"
    json += "\"episodes\":\(episodes),\"pending\":\(pending),\"missing_embeddings\":\(missingEmbeddings)}"
    return try! contractDecode(json)
}

/// Re-encodes the parts and decodes the container, so the container is built
/// the one way the contract guarantees exists.
private func contractMkPayload(nodes: [GraphNode], edges: [GraphEdge],
                               stats: GraphStats = contractMkStats()) -> GraphPayload {
    let enc = JSONEncoder()
    let n = String(decoding: try! enc.encode(nodes), as: UTF8.self)
    let e = String(decoding: try! enc.encode(edges), as: UTF8.self)
    let s = String(decoding: try! enc.encode(stats), as: UTF8.self)
    return try! contractDecode("{\"nodes\":\(n),\"edges\":\(e),\"stats\":\(s)}")
}

private func contractMkDetail(node: GraphNode, edges: [GraphEdge]) -> GraphNodeDetail {
    let enc = JSONEncoder()
    let n = String(decoding: try! enc.encode(node), as: UTF8.self)
    let e = String(decoding: try! enc.encode(edges), as: UTF8.self)
    return try! contractDecode("{\"node\":\(n),\"edges\":\(e)}")
}

private func contractCamera(scale: CGFloat, offset: CGSize = .zero) -> GraphCamera {
    GraphCamera(scale: scale, offset: offset)
}

// MARK: - Geometry helpers

private func contractHypot(_ p: CGPoint) -> CGFloat { hypot(p.x, p.y) }

private func contractAngle(_ p: CGPoint) -> Double { atan2(Double(p.y), Double(p.x)) }

/// Smallest absolute angle between two directions, so an assertion about
/// SPACING never accidentally asserts a direction of travel.
private func contractCircularGap(_ a: Double, _ b: Double) -> Double {
    let twoPi = 2 * Double.pi
    var d = (a - b).truncatingRemainder(dividingBy: twoPi)
    if d < 0 { d += twoPi }
    return min(d, twoPi - d)
}

// MARK: - Wire models

final class ContractGraphDecodeTests: XCTestCase {

    func testBoardPayloadFixtureDecodes() throws {
        let p: GraphPayload = try contractDecode(contractGraphFixture)

        XCTAssertEqual(p.nodes.map(\.id), [1, 5, 6])
        XCTAssertEqual(p.nodes.map(\.name), ["carl", "renderMD", "never-hard-wrap rule"])
        XCTAssertEqual(p.nodes.map(\.type), ["person", "component", "convention"])
        XCTAssertEqual(p.nodes[1].description,
                       "The kraken board's markdown renderer, which turns artifact .md files into the HTML the web board displays.")
        XCTAssertEqual(p.nodes[0].x, -103.4903506081596, accuracy: 1e-12)
        XCTAssertEqual(p.nodes[0].y, 79.87158754979711, accuracy: 1e-12)

        // The board route carries no `aliases` key at all — it decodes to nil,
        // not to an empty array.
        for n in p.nodes { XCTAssertNil(n.aliases, "node \(n.id) aliases") }
    }

    func testBoardPayloadEdgesDecode() throws {
        let p: GraphPayload = try contractDecode(contractGraphFixture)
        XCTAssertEqual(p.edges.count, 2)

        let first = p.edges[0]
        XCTAssertEqual(first.id, 4)
        XCTAssertEqual(first.src, 1)
        XCTAssertEqual(first.dst, 5)
        XCTAssertEqual(first.predicate, "requested")
        XCTAssertTrue(first.fact.hasPrefix("carl asked that kraken's markdown renderer"))
        XCTAssertEqual(first.strength, 9)
        XCTAssertEqual(first.occurrences, 1)
        XCTAssertEqual(first.episode, 3)

        // `valid_until`/`expired_at` → validUntil/expiredAt, and 0 is 0.
        XCTAssertEqual(first.validUntil, 0)
        XCTAssertEqual(first.expiredAt, 0)
        XCTAssertEqual(p.edges[1].predicate, "implements")
        XCTAssertEqual(p.edges[1].src, 5)
        XCTAssertEqual(p.edges[1].dst, 6)
        XCTAssertEqual(p.edges[1].strength, 8)
    }

    func testStatsDecodeTheirSnakeCaseKeys() throws {
        let p: GraphPayload = try contractDecode(contractGraphFixture)
        XCTAssertEqual(p.stats.nodes, 35)
        XCTAssertEqual(p.stats.edges, 29)
        XCTAssertEqual(p.stats.liveEdges, 29)
        XCTAssertEqual(p.stats.episodes, 678)
        XCTAssertEqual(p.stats.pending, 653)
        XCTAssertEqual(p.stats.missingEmbeddings, 0)
    }

    func testNodeDetailFixtureDecodesWithAliases() throws {
        let d: GraphNodeDetail = try contractDecode(contractNodeDetailFixture)
        XCTAssertEqual(d.node.id, 5)
        XCTAssertEqual(d.node.name, "renderMD")
        XCTAssertEqual(d.node.type, "component")
        XCTAssertEqual(d.node.aliases, [])       // present and empty, not nil
        XCTAssertEqual(d.edges.map(\.id), [4, 5])
    }

    // A stamped edge round-trips both time axes, and the wire keys stay
    // snake_case on the way back out.
    func testTimeAxesSurviveARoundTrip() throws {
        let e = contractMkEdge(id: 7, src: 1, dst: 2, validUntil: 1_769_040_000_000,
                               expiredAt: 1_769_050_000_000)
        let data = try JSONEncoder().encode(e)
        let text = String(decoding: data, as: UTF8.self)
        XCTAssertTrue(text.contains("valid_until"), text)
        XCTAssertTrue(text.contains("expired_at"), text)
        XCTAssertEqual(try JSONDecoder().decode(GraphEdge.self, from: data), e)
    }

    // Server-side additions must never break an installed build.
    func testUnknownFutureKeysDoNotFailTheDecode() throws {
        let json = """
        {"nodes":[{"id":1,"name":"carl","type":"person","description":"d","x":1,"y":2,"cluster":"a"}],
         "edges":[{"id":4,"src":1,"dst":1,"predicate":"p","fact":"f","strength":3,"occurrences":2,
                   "valid_until":0,"expired_at":0,"episode":3,"confidence":0.5}],
         "stats":{"nodes":1,"edges":1,"live_edges":1,"episodes":2,"pending":0,
                  "missing_embeddings":0,"orphans":4},
         "generated_at":123}
        """
        let p: GraphPayload = try contractDecode(json)
        XCTAssertEqual(p.nodes.count, 1)
        XCTAssertEqual(p.edges.count, 1)
        XCTAssertEqual(p.stats.liveEdges, 1)
    }

    // Equatable is in the frozen signature; two decodes of one body agree.
    func testDecodingIsValueStable() throws {
        let a: GraphPayload = try contractDecode(contractGraphFixture)
        let b: GraphPayload = try contractDecode(contractGraphFixture)
        XCTAssertEqual(a, b)
    }
}

// MARK: - Invalidation

final class ContractGraphInvalidationTests: XCTestCase {

    private let now: Int64 = 1_769_040_000_000

    // The contract's truth table, row by row: invalidated iff
    // `expiredAt > 0` OR `0 < validUntil <= now`.
    func testInvalidationTruthTable() {
        let live = contractMkEdge(id: 1, src: 1, dst: 2)
        XCTAssertFalse(graphIsInvalidated(live, now: now), "both axes 0 is live")

        let expired = contractMkEdge(id: 2, src: 1, dst: 2, expiredAt: 5)
        XCTAssertTrue(graphIsInvalidated(expired, now: now), "expiredAt > 0")

        let lapsed = contractMkEdge(id: 3, src: 1, dst: 2, validUntil: now - 1)
        XCTAssertTrue(graphIsInvalidated(lapsed, now: now), "validUntil in the past")

        let lapsingNow = contractMkEdge(id: 4, src: 1, dst: 2, validUntil: now)
        XCTAssertTrue(graphIsInvalidated(lapsingNow, now: now), "validUntil == now is <= now")

        let future = contractMkEdge(id: 5, src: 1, dst: 2, validUntil: now + 1)
        XCTAssertFalse(graphIsInvalidated(future, now: now), "a future validUntil is a live fact")

        let expiredButFutureValid = contractMkEdge(id: 6, src: 1, dst: 2,
                                                   validUntil: now + 10_000, expiredAt: 9)
        XCTAssertTrue(graphIsInvalidated(expiredButFutureValid, now: now),
                      "expiredAt alone invalidates regardless of validUntil")
    }

    // `expiredAt` when stamped, else `validUntil`, else 0.
    func testInvalidatedAtPrefersExpiredAt() {
        XCTAssertEqual(graphInvalidatedAt(contractMkEdge(id: 1, src: 1, dst: 2,
                                                         validUntil: 300, expiredAt: 700)), 700)
        XCTAssertEqual(graphInvalidatedAt(contractMkEdge(id: 2, src: 1, dst: 2,
                                                         validUntil: 300)), 300)
        XCTAssertEqual(graphInvalidatedAt(contractMkEdge(id: 3, src: 1, dst: 2)), 0)
    }

    // A local-timezone YYYY-MM-DD, zero-padded. The expectation is built from
    // contractGregorian at noon — Gregorian in the local timezone, the same
    // pair the implementation is bound to — so no timezone rolls it into a
    // neighbouring day, and a device region on a non-Gregorian calendar cannot
    // resolve "year 2026" in a different era than the code under test does.
    func testShortDateIsAZeroPaddedLocalDate() throws {
        let comps = DateComponents(year: 2026, month: 3, day: 7, hour: 12)
        let date = try XCTUnwrap(contractGregorian.date(from: comps))
        let ms = Int64((date.timeIntervalSince1970 * 1000).rounded())
        XCTAssertEqual(graphShortDate(ms: ms), "2026-03-07")
    }

    func testShortDateShapeHoldsAcrossTheYear() throws {
        let re = try NSRegularExpression(pattern: "^[0-9]{4}-[0-9]{2}-[0-9]{2}$")
        for month in 1...12 {
            let comps = DateComponents(year: 2026, month: month, day: 21, hour: 12)
            let date = try XCTUnwrap(contractGregorian.date(from: comps))
            let s = graphShortDate(ms: Int64((date.timeIntervalSince1970 * 1000).rounded()))
            let range = NSRange(s.startIndex..., in: s)
            XCTAssertNotNil(re.firstMatch(in: s, range: range), "month \(month) rendered \(s)")
        }
    }

    // An unstamped edge has no date to show, and the empty string is how the
    // drawer is told there is nothing to render.
    func testShortDateOfAnUnstampedTimeIsEmpty() {
        XCTAssertEqual(graphShortDate(ms: 0), "")
    }
}

// MARK: - Degree, radius, positions

final class ContractGraphLayoutTests: XCTestCase {

    func testDegreeCountsEveryTouchingEdge() {
        let edges = [
            contractMkEdge(id: 1, src: 1, dst: 2),
            contractMkEdge(id: 2, src: 2, dst: 3),
            contractMkEdge(id: 3, src: 2, dst: 3),
        ]
        let d = graphDegrees(edges: edges)
        XCTAssertEqual(d[1], 1)
        XCTAssertEqual(d[2], 3)
        XCTAssertEqual(d[3], 2)
    }

    // A self-loop touches its node twice.
    func testSelfLoopCountsTwo() {
        let d = graphDegrees(edges: [contractMkEdge(id: 1, src: 4, dst: 4)])
        XCTAssertEqual(d[4], 2)
    }

    // Validity is a RENDER filter; the size of a node is not filtered by it.
    func testDegreeIgnoresInvalidation() {
        let edges = [
            contractMkEdge(id: 1, src: 1, dst: 2, expiredAt: 99),
            contractMkEdge(id: 2, src: 1, dst: 3, validUntil: 5),
        ]
        XCTAssertEqual(graphDegrees(edges: edges)[1], 2)
    }

    func testIdsWithNoEdgesAreAbsentFromTheDegreeMap() {
        let d = graphDegrees(edges: [contractMkEdge(id: 1, src: 1, dst: 2)])
        XCTAssertNil(d[9])
        XCTAssertEqual(d.count, 2)
        XCTAssertTrue(graphDegrees(edges: []).isEmpty)
    }

    // min(16, 4 + 2·√degree), floored at 4.
    func testNodeRadiusFormula() {
        XCTAssertEqual(graphNodeRadius(degree: 0), 4, accuracy: 1e-9)
        XCTAssertEqual(graphNodeRadius(degree: 1), 6, accuracy: 1e-9)
        XCTAssertEqual(graphNodeRadius(degree: 2), 4 + 2 * CGFloat(2.0.squareRoot()), accuracy: 1e-9)
        XCTAssertEqual(graphNodeRadius(degree: 4), 8, accuracy: 1e-9)
        XCTAssertEqual(graphNodeRadius(degree: 9), 10, accuracy: 1e-9)
        XCTAssertEqual(graphNodeRadius(degree: 36), 16, accuracy: 1e-9)   // exactly at the cap
        XCTAssertEqual(graphNodeRadius(degree: 400), 16, accuracy: 1e-9)  // a hub clamps
    }

    func testNodeRadiusFloorsAtFour() {
        XCTAssertEqual(graphNodeRadius(degree: 0), 4, accuracy: 1e-9)
        XCTAssertEqual(graphNodeRadius(degree: -3), 4, accuracy: 1e-9)
    }

    // Positioned nodes keep the server's coordinates to the bit.
    func testPositionedNodesAreUntouched() {
        let nodes = [
            contractMkNode(id: 1, x: -103.4903506081596, y: 79.87158754979711),
            contractMkNode(id: 5, x: -75.18157241820735, y: 21.115214853358946),
            contractMkNode(id: 6, x: 0, y: 8.879077510894259),   // one zero axis is POSITIONED
        ]
        let p = graphPositions(nodes: nodes)
        XCTAssertEqual(p.count, 3)
        XCTAssertEqual(p[1], CGPoint(x: -103.4903506081596, y: 79.87158754979711))
        XCTAssertEqual(p[5], CGPoint(x: -75.18157241820735, y: 21.115214853358946))
        XCTAssertEqual(p[6], CGPoint(x: 0, y: 8.879077510894259))
    }

    func testEveryNodeGetsAPosition() {
        let nodes = [
            contractMkNode(id: 1, x: 3, y: 4),
            contractMkNode(id: 2, x: 0, y: 0),
            contractMkNode(id: 3, x: 0, y: 0),
        ]
        let p = graphPositions(nodes: nodes)
        XCTAssertEqual(Set(p.keys), [1, 2, 3])
    }

    // Nothing positioned → a unit ring.
    func testAllUnpositionedRingsAtRadiusOne() {
        let nodes = (1...5).map { contractMkNode(id: Int64($0), x: 0, y: 0) }
        let p = graphPositions(nodes: nodes)
        XCTAssertEqual(p.count, 5)
        for (id, point) in p {
            XCTAssertEqual(contractHypot(point), 1, accuracy: 1e-9, "node \(id)")
        }
    }

    // Some positioned → the ring sits at 1.15 × the furthest positioned node,
    // which is strictly outside the positioned extent.
    func testRingSitsOutsideThePositionedExtent() {
        let nodes = [
            contractMkNode(id: 1, x: 3, y: 4),      // distance 5 — the extent
            contractMkNode(id: 2, x: 1, y: 0),      // distance 1
            contractMkNode(id: 3, x: 0, y: 0),      // unpositioned
            contractMkNode(id: 4, x: 0, y: 0),      // unpositioned
        ]
        let p = graphPositions(nodes: nodes)
        XCTAssertEqual(p[1], CGPoint(x: 3, y: 4))
        XCTAssertEqual(p[2], CGPoint(x: 1, y: 0))
        for id in [Int64(3), Int64(4)] {
            let point = p[id]!
            XCTAssertEqual(contractHypot(point), 5 * 1.15, accuracy: 1e-9, "node \(id) ring radius")
            XCTAssertGreaterThan(contractHypot(point), 5, "node \(id) must sit outside the extent")
        }
    }

    // Spacing is `2π·k/missing`. Asserted as an absolute angular gap so the
    // direction of travel — which the contract does not fix — stays free.
    func testRingIsEvenlySpaced() {
        let nodes = (1...4).map { contractMkNode(id: Int64($0), x: 0, y: 0) }
        let p = graphPositions(nodes: nodes)
        let base = contractAngle(p[1]!)
        let quarter = Double.pi / 2
        XCTAssertEqual(contractCircularGap(contractAngle(p[2]!), base), quarter, accuracy: 1e-9)
        XCTAssertEqual(contractCircularGap(contractAngle(p[3]!), base), Double.pi, accuracy: 1e-9)
        XCTAssertEqual(contractCircularGap(contractAngle(p[4]!), base), quarter, accuracy: 1e-9)
        // Consecutive ring neighbours are one quarter turn apart.
        XCTAssertEqual(contractCircularGap(contractAngle(p[3]!), contractAngle(p[2]!)),
                       quarter, accuracy: 1e-9)
    }

    // Same input, same output — the whole point of a deterministic fallback.
    func testRingIsDeterministic() {
        let nodes = (1...6).map { contractMkNode(id: Int64($0), x: 0, y: 0) }
        XCTAssertEqual(graphPositions(nodes: nodes), graphPositions(nodes: nodes))
    }

    // The angle is keyed to the node's INDEX among the missing, in the array
    // order the server sent — so swapping two unpositioned nodes swaps their
    // slots rather than leaving them where they were.
    func testRingSlotFollowsServerOrder() {
        let a = contractMkNode(id: 10, x: 0, y: 0)
        let b = contractMkNode(id: 11, x: 0, y: 0)
        let c = contractMkNode(id: 12, x: 0, y: 0)
        let straight = graphPositions(nodes: [a, b, c])
        let swapped = graphPositions(nodes: [a, c, b])
        XCTAssertEqual(straight[11], swapped[12], "slot 1 belongs to whoever the server sent second")
        XCTAssertEqual(straight[12], swapped[11], "slot 2 belongs to whoever the server sent third")
        XCTAssertEqual(straight[10], swapped[10], "slot 0 did not move")
    }
}

// MARK: - Types, filters

final class ContractGraphFilterTests: XCTestCase {

    private let now: Int64 = 1_769_040_000_000

    private func sample() -> [GraphNode] {
        [
            contractMkNode(id: 1, type: "person"),
            contractMkNode(id: 2, type: "component"),
            contractMkNode(id: 3, type: "person"),
            contractMkNode(id: 4, type: "convention"),
            contractMkNode(id: 5, type: "component"),
        ]
    }

    func testTypeListIsDistinctInFirstSeenOrder() {
        XCTAssertEqual(graphTypeList(nodes: sample()), ["person", "component", "convention"])
        XCTAssertEqual(graphTypeList(nodes: []), [])
    }

    // A type with no entry in the colour map is still a type and still lists.
    func testTypeListKeepsUnmappedTypes() {
        let nodes = [contractMkNode(id: 1, type: "component"),
                     contractMkNode(id: 2, type: "brand-new-type")]
        XCTAssertEqual(graphTypeList(nodes: nodes), ["component", "brand-new-type"])
    }

    func testHiddenNodeIDsAreTheNodesOfHiddenTypes() {
        XCTAssertEqual(graphHiddenNodeIDs(nodes: sample(), hiddenTypes: ["person"]), [1, 3])
        XCTAssertEqual(graphHiddenNodeIDs(nodes: sample(), hiddenTypes: ["person", "convention"]),
                       [1, 3, 4])
        XCTAssertEqual(graphHiddenNodeIDs(nodes: sample(), hiddenTypes: []), [])
        XCTAssertEqual(graphHiddenNodeIDs(nodes: sample(), hiddenTypes: ["nothing-of-that-type"]), [])
    }

    func testEdgeVisibilityTruthTable() {
        let live = contractMkEdge(id: 1, src: 1, dst: 2)
        let dead = contractMkEdge(id: 2, src: 1, dst: 2, expiredAt: 10)

        XCTAssertTrue(graphEdgeVisible(live, hiddenNodeIDs: [], includeInvalidated: false, now: now))
        XCTAssertFalse(graphEdgeVisible(dead, hiddenNodeIDs: [], includeInvalidated: false, now: now))
        XCTAssertTrue(graphEdgeVisible(dead, hiddenNodeIDs: [], includeInvalidated: true, now: now))

        // Either endpoint hidden wins over everything else.
        XCTAssertFalse(graphEdgeVisible(live, hiddenNodeIDs: [1], includeInvalidated: true, now: now))
        XCTAssertFalse(graphEdgeVisible(live, hiddenNodeIDs: [2], includeInvalidated: true, now: now))
        XCTAssertFalse(graphEdgeVisible(dead, hiddenNodeIDs: [2], includeInvalidated: true, now: now))
    }

    // Hiding a type hides its nodes AND every edge touching them; the
    // invalidated toggle acts on edges alone and never on nodes.
    func testFiltersCompose() {
        let nodes = sample()
        let edges = [
            contractMkEdge(id: 10, src: 1, dst: 2),                    // touches a person
            contractMkEdge(id: 11, src: 2, dst: 5),                    // component ↔ component
            contractMkEdge(id: 12, src: 2, dst: 4, expiredAt: 77),     // invalidated
            contractMkEdge(id: 13, src: 3, dst: 4),                    // touches a person
        ]
        let hidden = graphHiddenNodeIDs(nodes: nodes, hiddenTypes: ["person"])
        XCTAssertEqual(hidden, [1, 3])

        let visibleLive = edges.filter {
            graphEdgeVisible($0, hiddenNodeIDs: hidden, includeInvalidated: false, now: now)
        }
        XCTAssertEqual(visibleLive.map(\.id), [11])

        let visibleAll = edges.filter {
            graphEdgeVisible($0, hiddenNodeIDs: hidden, includeInvalidated: true, now: now)
        }
        XCTAssertEqual(visibleAll.map(\.id), [11, 12])

        // The toggle moved only edges; the node set is the type filter's alone.
        XCTAssertEqual(graphHiddenNodeIDs(nodes: nodes, hiddenTypes: ["person"]), hidden)
    }
}

// MARK: - Fact ordering and text

final class ContractGraphFactTests: XCTestCase {

    private let now: Int64 = 1_769_040_000_000

    func testLiveFactsSortBeforeInvalidatedOnes() {
        let strongDead = contractMkEdge(id: 1, src: 1, dst: 2, strength: 10, expiredAt: 5)
        let weakLive = contractMkEdge(id: 2, src: 1, dst: 3, strength: 1)
        XCTAssertEqual(graphSortFacts([strongDead, weakLive], now: now).map(\.id), [2, 1])
    }

    func testStrengthDescendsWithinEachGroup() {
        let edges = [
            contractMkEdge(id: 1, src: 1, dst: 2, strength: 3),
            contractMkEdge(id: 2, src: 1, dst: 3, strength: 9),
            contractMkEdge(id: 3, src: 1, dst: 4, strength: 2, expiredAt: 4),
            contractMkEdge(id: 4, src: 1, dst: 5, strength: 8, expiredAt: 4),
            contractMkEdge(id: 5, src: 1, dst: 6, strength: 6),
        ]
        XCTAssertEqual(graphSortFacts(edges, now: now).map(\.id), [2, 5, 1, 4, 3])
    }

    // Ties keep the input order — a stable sort, so the list does not reshuffle
    // itself between two identical polls.
    func testTiesKeepInputOrder() {
        let a = contractMkEdge(id: 10, src: 1, dst: 2, strength: 5)
        let b = contractMkEdge(id: 11, src: 1, dst: 3, strength: 5)
        let c = contractMkEdge(id: 12, src: 1, dst: 4, strength: 5)
        XCTAssertEqual(graphSortFacts([a, b, c], now: now).map(\.id), [10, 11, 12])
        XCTAssertEqual(graphSortFacts([c, a, b], now: now).map(\.id), [12, 10, 11])
    }

    func testSortingDropsNothing() {
        // Spelled out rather than mapped over a range: the one-liner version of
        // this blew the type checker's budget (`unable to type-check this
        // expression in reasonable time`), which is a fact about inference
        // through six defaulted Int64 parameters, not about the contract.
        var edges: [GraphEdge] = []
        for i in 1...6 {
            let n = Int64(i)
            let expired: Int64 = i % 2 == 0 ? 9 : 0
            edges.append(contractMkEdge(id: n, src: 1, dst: n + 100,
                                        strength: n % 3, expiredAt: expired))
        }
        let ids: [Int64] = edges.map { $0.id }
        let sorted: [GraphEdge] = graphSortFacts(edges, now: now)
        XCTAssertEqual(Set(sorted.map { $0.id }), Set(ids))
        XCTAssertEqual(sorted.count, 6)
        XCTAssertEqual(graphSortFacts([GraphEdge](), now: now).count, 0)
    }

    func testEndpointNameResolvesOrFallsBackToTheID() {
        let names: [Int64: String] = [5: "renderMD", 6: ""]
        XCTAssertEqual(graphEndpointName(id: 5, names: names), "renderMD")
        XCTAssertEqual(graphEndpointName(id: 6, names: names), "#6", "an empty name is not a name")
        XCTAssertEqual(graphEndpointName(id: 99, names: names), "#99")
        XCTAssertEqual(graphEndpointName(id: 1, names: [:]), "#1")
    }

    func testFactTextPrefersTheSentence() {
        let e = contractMkEdge(id: 1, src: 5, dst: 6, predicate: "implements",
                               fact: "renderMD folds consecutive prose lines into one paragraph.")
        XCTAssertEqual(graphFactText(e, names: [6: "never-hard-wrap rule"]),
                       "renderMD folds consecutive prose lines into one paragraph.")
    }

    // A blank sentence degrades to predicate + the destination's name, and the
    // fallback name is the same one the endpoint row shows.
    func testBlankFactDegradesToPredicateAndEndpoint() {
        let e = contractMkEdge(id: 1, src: 5, dst: 6, predicate: "implements", fact: "")
        XCTAssertEqual(graphFactText(e, names: [6: "never-hard-wrap rule"]),
                       "implements never-hard-wrap rule")
        XCTAssertEqual(graphFactText(e, names: [:]), "implements #6")
    }

    // "trimmed" is a property of the joined string, so a missing predicate
    // leaves the endpoint name alone rather than a leading space.
    func testBlankFactAndBlankPredicateTrimToTheEndpointName() {
        let e = contractMkEdge(id: 1, src: 5, dst: 6, predicate: "", fact: "")
        XCTAssertEqual(graphFactText(e, names: [6: "renderMD"]), "renderMD")
    }
}

// MARK: - Camera

final class ContractGraphCameraTests: XCTestCase {

    func testWorldToScreenIsScaleThenOffset() {
        let cam = contractCamera(scale: 2, offset: CGSize(width: 10, height: -5))
        XCTAssertEqual(cam.worldToScreen(CGPoint(x: 3, y: 4)), CGPoint(x: 16, y: 3))
        XCTAssertEqual(cam.worldToScreen(.zero), CGPoint(x: 10, y: -5))
    }

    func testScreenToWorldIsTheExactInverse() {
        let cams = [
            contractCamera(scale: 0.01, offset: CGSize(width: 3, height: 9)),
            contractCamera(scale: 0.05, offset: CGSize(width: -40, height: 12)),
            contractCamera(scale: 1, offset: .zero),
            contractCamera(scale: 3.7, offset: CGSize(width: 120.5, height: -7.25)),
            contractCamera(scale: 20, offset: CGSize(width: -1000, height: 640)),
        ]
        let points = [CGPoint.zero, CGPoint(x: 12.5, y: -3.75),
                      CGPoint(x: -1000, y: 640), CGPoint(x: 333.125, y: 999.5)]
        for cam in cams {
            for p in points {
                let back = cam.screenToWorld(cam.worldToScreen(p))
                XCTAssertEqual(back.x, p.x, accuracy: 1e-6, "scale \(cam.scale) x")
                XCTAssertEqual(back.y, p.y, accuracy: 1e-6, "scale \(cam.scale) y")
                let there = cam.worldToScreen(cam.screenToWorld(p))
                XCTAssertEqual(there.x, p.x, accuracy: 1e-6, "scale \(cam.scale) screen x")
                XCTAssertEqual(there.y, p.y, accuracy: 1e-6, "scale \(cam.scale) screen y")
            }
        }
    }

    func testPanAddsToTheOffsetAndLeavesTheScaleAlone() {
        var cam = contractCamera(scale: 2.5, offset: CGSize(width: 10, height: 10))
        cam.pan(by: CGSize(width: 4, height: -6))
        XCTAssertEqual(cam.offset.width, 14, accuracy: 1e-9)
        XCTAssertEqual(cam.offset.height, 4, accuracy: 1e-9)
        XCTAssertEqual(cam.scale, 2.5, accuracy: 1e-9)

        cam.pan(by: CGSize(width: -14, height: -4))
        XCTAssertEqual(cam.offset.width, 0, accuracy: 1e-9)
        XCTAssertEqual(cam.offset.height, 0, accuracy: 1e-9)
    }

    func testPinchMultipliesTheScale() {
        var cam = contractCamera(scale: 2, offset: CGSize(width: 30, height: -12))
        cam.pinch(by: 1.5, anchor: CGPoint(x: 100, y: 200))
        XCTAssertEqual(cam.scale, 3, accuracy: 1e-9)
    }

    // The invariant the gesture exists for: the world point under the
    // fingertip does not move while the fingers do.
    func testPinchKeepsItsAnchorWorldPointUnderTheAnchor() {
        let anchors = [CGPoint(x: 0, y: 0), CGPoint(x: 180, y: 400), CGPoint(x: -25.5, y: 63.25)]
        let factors: [CGFloat] = [0.5, 1, 1.25, 4]
        for anchor in anchors {
            for factor in factors {
                var cam = contractCamera(scale: 1.6, offset: CGSize(width: -44, height: 21))
                let worldUnderAnchor = cam.screenToWorld(anchor)
                cam.pinch(by: factor, anchor: anchor)
                let after = cam.worldToScreen(worldUnderAnchor)
                XCTAssertEqual(after.x, anchor.x, accuracy: 1e-6, "anchor \(anchor) × \(factor)")
                XCTAssertEqual(after.y, anchor.y, accuracy: 1e-6, "anchor \(anchor) × \(factor)")
            }
        }
    }

    // A pinch of exactly 1 is the identity — the anchor invariant plus a
    // scale multiplied by 1 leaves nothing to move.
    func testPinchByOneMovesNothing() {
        var cam = contractCamera(scale: 1.6, offset: CGSize(width: -44, height: 21))
        cam.pinch(by: 1, anchor: CGPoint(x: 90, y: 90))
        XCTAssertEqual(cam.scale, 1.6, accuracy: 1e-9)
        XCTAssertEqual(cam.offset.width, -44, accuracy: 1e-6)
        XCTAssertEqual(cam.offset.height, 21, accuracy: 1e-6)
    }

    func testFittingCentersTheRectInTheViewport() {
        let rect = CGRect(x: -100, y: -50, width: 200, height: 100)
        let viewport = CGSize(width: 400, height: 400)
        let cam = GraphCamera.fitting(worldRect: rect, viewport: viewport)
        let center = cam.worldToScreen(CGPoint(x: rect.midX, y: rect.midY))
        XCTAssertEqual(center.x, viewport.width / 2, accuracy: 1e-6)
        XCTAssertEqual(center.y, viewport.height / 2, accuracy: 1e-6)
    }

    func testFittingKeepsEveryCornerInsideTheViewport() {
        let rects = [
            CGRect(x: -100, y: -50, width: 200, height: 100),   // wider than tall
            CGRect(x: 40, y: 40, width: 30, height: 900),       // taller than wide
            CGRect(x: -7.5, y: -7.5, width: 15, height: 15),    // square, off-origin
        ]
        let viewport = CGSize(width: 390, height: 700)
        for rect in rects {
            let cam = GraphCamera.fitting(worldRect: rect, viewport: viewport)
            let corners = [CGPoint(x: rect.minX, y: rect.minY), CGPoint(x: rect.maxX, y: rect.minY),
                           CGPoint(x: rect.minX, y: rect.maxY), CGPoint(x: rect.maxX, y: rect.maxY)]
            for c in corners {
                let s = cam.worldToScreen(c)
                XCTAssertGreaterThanOrEqual(s.x, -1e-6, "\(rect) corner \(c) x")
                XCTAssertLessThanOrEqual(s.x, viewport.width + 1e-6, "\(rect) corner \(c) x")
                XCTAssertGreaterThanOrEqual(s.y, -1e-6, "\(rect) corner \(c) y")
                XCTAssertLessThanOrEqual(s.y, viewport.height + 1e-6, "\(rect) corner \(c) y")
            }
        }
    }

    // A single node has no extent to frame, so the answer is "don't zoom,
    // just look at it".
    func testFittingADegenerateRectIsUnitScaleOnItsCenter() {
        let rect = CGRect(x: 7, y: -3, width: 0, height: 0)
        let viewport = CGSize(width: 300, height: 200)
        let cam = GraphCamera.fitting(worldRect: rect, viewport: viewport)
        XCTAssertEqual(cam.scale, 1, accuracy: 1e-9)
        let center = cam.worldToScreen(CGPoint(x: 7, y: -3))
        XCTAssertEqual(center.x, 150, accuracy: 1e-6)
        XCTAssertEqual(center.y, 100, accuracy: 1e-6)
    }

    func testFittingClampsTheScaleAndStillCenters() {
        let viewport = CGSize(width: 390, height: 700)

        let huge = CGRect(x: -5e8, y: -5e8, width: 1e9, height: 1e9)
        let out = GraphCamera.fitting(worldRect: huge, viewport: viewport)
        XCTAssertGreaterThanOrEqual(out.scale, 0.05)
        XCTAssertLessThanOrEqual(out.scale, 20)
        let hugeCenter = out.worldToScreen(CGPoint(x: huge.midX, y: huge.midY))
        XCTAssertEqual(hugeCenter.x, viewport.width / 2, accuracy: 1e-3)
        XCTAssertEqual(hugeCenter.y, viewport.height / 2, accuracy: 1e-3)

        let tiny = CGRect(x: 0, y: 0, width: 1e-6, height: 1e-6)
        let inn = GraphCamera.fitting(worldRect: tiny, viewport: viewport)
        XCTAssertGreaterThanOrEqual(inn.scale, 0.05)
        XCTAssertLessThanOrEqual(inn.scale, 20)
    }

    func testCameraIsAValue() {
        XCTAssertEqual(contractCamera(scale: 2, offset: CGSize(width: 1, height: 2)),
                       contractCamera(scale: 2, offset: CGSize(width: 1, height: 2)))
        XCTAssertNotEqual(contractCamera(scale: 2, offset: .zero),
                          contractCamera(scale: 3, offset: .zero))
    }
}

// MARK: - Hit test

final class ContractGraphHitTestTests: XCTestCase {

    private let positions: [Int64: CGPoint] = [1: CGPoint(x: 0, y: 0), 2: CGPoint(x: 100, y: 0)]

    func testPicksTheNearestNodeWithinReach() {
        let cam = contractCamera(scale: 1)
        XCTAssertEqual(graphHitTest(screenPoint: CGPoint(x: 2, y: 0), positions: positions,
                                    camera: cam, maxDistance: 24), 1)
        XCTAssertEqual(graphHitTest(screenPoint: CGPoint(x: 88, y: 3), positions: positions,
                                    camera: cam, maxDistance: 24), 2)
    }

    func testATapBeyondReachSelectsNothing() {
        let cam = contractCamera(scale: 1)
        XCTAssertNil(graphHitTest(screenPoint: CGPoint(x: 50, y: 0), positions: positions,
                                  camera: cam, maxDistance: 24))
        XCTAssertNil(graphHitTest(screenPoint: CGPoint(x: 0, y: 400), positions: positions,
                                  camera: cam, maxDistance: 24))
    }

    func testReachIsInclusiveOfItsOwnBoundary() {
        let one: [Int64: CGPoint] = [7: CGPoint(x: 24, y: 0)]
        let cam = contractCamera(scale: 1)
        XCTAssertEqual(graphHitTest(screenPoint: .zero, positions: one, camera: cam,
                                    maxDistance: 24), 7)
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: [7: CGPoint(x: 24.5, y: 0)],
                                  camera: cam, maxDistance: 24))
    }

    // Distance is measured in SCREEN space, so the same tap and the same world
    // positions answer differently at two zoom levels.
    func testDistanceIsMeasuredAfterTheCameraTransform() {
        let zoomedOut = contractCamera(scale: 0.1)
        XCTAssertEqual(graphHitTest(screenPoint: CGPoint(x: 9, y: 0), positions: positions,
                                    camera: zoomedOut, maxDistance: 24), 2)
        let unity = contractCamera(scale: 1)
        XCTAssertEqual(graphHitTest(screenPoint: CGPoint(x: 9, y: 0), positions: positions,
                                    camera: unity, maxDistance: 24), 1)
    }

    func testOffsetIsHonoured() {
        let cam = contractCamera(scale: 1, offset: CGSize(width: 50, height: 50))
        XCTAssertEqual(graphHitTest(screenPoint: CGPoint(x: 50, y: 50), positions: positions,
                                    camera: cam, maxDistance: 24), 1)
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: positions, camera: cam,
                                  maxDistance: 24))
    }

    func testAnEmptyGraphHasNothingToHit() {
        XCTAssertNil(graphHitTest(screenPoint: .zero, positions: [:],
                                  camera: contractCamera(scale: 1), maxDistance: 24))
    }
}

// MARK: - Theme

private struct ContractRGB: Equatable {
    let r: CGFloat
    let g: CGFloat
    let b: CGFloat
}

private func contractRGB(_ color: UIColor, _ style: UIUserInterfaceStyle) -> ContractRGB {
    let resolved = color.resolvedColor(with: UITraitCollection(userInterfaceStyle: style))
    var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
    resolved.getRed(&r, green: &g, blue: &b, alpha: &a)
    return ContractRGB(r: r, g: g, b: b)
}

private func contractHexRGB(_ hex: UInt32) -> ContractRGB {
    ContractRGB(r: CGFloat((hex >> 16) & 0xFF) / 255,
                g: CGFloat((hex >> 8) & 0xFF) / 255,
                b: CGFloat(hex & 0xFF) / 255)
}

final class ContractGraphThemeTests: XCTestCase {

    // The two hexes the contract freezes, resolved through the trait
    // collection rather than read off the source line — the Go parity test
    // (web/graphpalette_contract_test.go) reads the source line, so between
    // them a drift in either the literal or the light/dark ORDER is caught.
    func testSkillGraphResolvesToTheFrozenHexes() {
        let dark = contractRGB(KTheme.skillGraphUI, .dark)
        let wantDark = contractHexRGB(0x87DF49)
        XCTAssertEqual(dark.r, wantDark.r, accuracy: 0.004)
        XCTAssertEqual(dark.g, wantDark.g, accuracy: 0.004)
        XCTAssertEqual(dark.b, wantDark.b, accuracy: 0.004)

        let light = contractRGB(KTheme.skillGraphUI, .light)
        let wantLight = contractHexRGB(0x456F0B)
        XCTAssertEqual(light.r, wantLight.r, accuracy: 0.004)
        XCTAssertEqual(light.g, wantLight.g, accuracy: 0.004)
        XCTAssertEqual(light.b, wantLight.b, accuracy: 0.004)
    }

    // The two entries the contract sends to ONE constant must stay together;
    // this is the only claim about the map that names no other KTheme colour.
    func testFileAndJobShareOneColour() {
        XCTAssertEqual(KTheme.graphTypeColorUI("file"), KTheme.graphTypeColorUI("job"))
    }

    // "anything else" is one fallback, not a per-string colour.
    func testEveryUnmappedTypeLandsOnOneFallback() {
        let fallback = KTheme.graphTypeColorUI("no-such-type")
        XCTAssertEqual(KTheme.graphTypeColorUI("also-not-a-type"), fallback)
        XCTAssertEqual(KTheme.graphTypeColorUI(""), fallback)
    }
}

// MARK: - View model

private struct ContractNodeCall: Equatable {
    let baseURL: String
    let id: Int64
}

private struct ContractStubError: Error {}

/// What the stubbed fetches were actually asked for. Locked because the
/// closures may run off the main actor.
private final class ContractCallLog: @unchecked Sendable {
    private let lock = NSLock()
    private var graph: [String] = []
    private var node: [ContractNodeCall] = []

    func recordGraph(_ baseURL: String) {
        lock.lock(); defer { lock.unlock() }
        graph.append(baseURL)
    }

    func recordNode(_ call: ContractNodeCall) {
        lock.lock(); defer { lock.unlock() }
        node.append(call)
    }

    var graphCalls: [String] {
        lock.lock(); defer { lock.unlock() }
        return graph
    }

    var nodeCalls: [ContractNodeCall] {
        lock.lock(); defer { lock.unlock() }
        return node
    }
}

@MainActor
private func contractWait(timeout: TimeInterval = 2, until ready: () -> Bool) async {
    let deadline = Date().addingTimeInterval(timeout)
    while !ready() {
        if Date() > deadline { return }
        await Task.yield()
        try? await Task.sleep(nanoseconds: 500_000)
    }
}

@MainActor
final class ContractGraphViewModelTests: XCTestCase {

    private let now: Int64 = 1_769_040_000_000

    private func board() -> GraphPayload {
        contractMkPayload(
            nodes: [
                contractMkNode(id: 1, name: "carl", type: "person", x: 3, y: 4),
                contractMkNode(id: 5, name: "renderMD", type: "component", x: -7, y: 2),
                contractMkNode(id: 6, name: "never-hard-wrap rule", type: "convention", x: 1, y: 1),
            ],
            edges: [
                contractMkEdge(id: 4, src: 1, dst: 5, predicate: "requested", strength: 9),
                contractMkEdge(id: 5, src: 5, dst: 6, predicate: "implements", strength: 8),
                contractMkEdge(id: 6, src: 1, dst: 6, predicate: "wrote", strength: 2),
            ],
            stats: contractMkStats(nodes: 3, edges: 3, liveEdges: 3, episodes: 9, pending: 2)
        )
    }

    private func detail() -> GraphNodeDetail {
        contractMkDetail(
            node: contractMkNode(id: 5, name: "renderMD", type: "component",
                                 x: -7, y: 2, aliases: ["render-md"]),
            edges: [
                contractMkEdge(id: 4, src: 1, dst: 5, predicate: "requested", strength: 9),
                contractMkEdge(id: 5, src: 5, dst: 6, predicate: "implements", strength: 8),
                contractMkEdge(id: 9, src: 5, dst: 1, predicate: "superseded", strength: 10,
                               expiredAt: 5),
            ]
        )
    }

    private func makeVM(log: ContractCallLog = ContractCallLog(),
                        graph: GraphPayload? = nil,
                        graphFails: Bool = false,
                        node: GraphNodeDetail? = nil,
                        nodeFails: Bool = false) -> GraphViewModel {
        let payload = graph
        let nodeDetail = node
        return GraphViewModel(
            fetchGraph: { baseURL in
                log.recordGraph(baseURL)
                if graphFails || payload == nil { throw ContractStubError() }
                return payload!
            },
            fetchNode: { baseURL, id in
                log.recordNode(ContractNodeCall(baseURL: baseURL, id: id))
                if nodeFails || nodeDetail == nil { throw ContractStubError() }
                return nodeDetail!
            }
        )
    }

    func testDefaultsAreAnUnfilteredLiveOnlyEmptyScreen() {
        let vm = makeVM(graph: board())
        XCTAssertNil(vm.payload)
        XCTAssertNil(vm.errorText)
        XCTAssertNil(vm.selectedID)
        XCTAssertNil(vm.detail)
        XCTAssertEqual(vm.hiddenTypes, [])
        XCTAssertFalse(vm.includeInvalidated)
        XCTAssertNil(vm.selectedNode)
        XCTAssertEqual(vm.selectedFacts.count, 0)
    }

    func testASuccessfulLoadLandsThePayload() async {
        let log = ContractCallLog()
        let vm = makeVM(log: log, graph: board())
        await vm.load(baseURL: "http://mac.local:7777")
        XCTAssertEqual(vm.payload, board())
        XCTAssertNil(vm.errorText)
        XCTAssertEqual(log.graphCalls, ["http://mac.local:7777"])
    }

    // The screen keeps its picture when the Mac goes away — an error line over
    // a stale graph, never a blank canvas.
    func testAFailedLoadPreservesThePreviousPayload() async {
        let log = ContractCallLog()
        let good = board()
        var fail = false
        let vm = GraphViewModel(
            fetchGraph: { baseURL in
                log.recordGraph(baseURL)
                if fail { throw ContractStubError() }
                return good
            },
            fetchNode: { _, _ in throw ContractStubError() }
        )
        await vm.load(baseURL: "http://mac.local:7777")
        XCTAssertEqual(vm.payload, good)

        fail = true
        await vm.load(baseURL: "http://mac.local:7777")
        XCTAssertEqual(vm.payload, good, "a failed poll must not erase the graph")
        XCTAssertNotNil(vm.errorText)
        XCTAssertFalse(vm.errorText?.isEmpty ?? true)

        fail = false
        await vm.load(baseURL: "http://mac.local:7777")
        XCTAssertNil(vm.errorText, "a recovery clears the error line")
        XCTAssertEqual(vm.payload, good)
    }

    func testSelectingANodeFetchesItsDetailFromTheLastLoadedBaseURL() async {
        let log = ContractCallLog()
        let vm = makeVM(log: log, graph: board(), node: detail())
        await vm.load(baseURL: "http://mac.local:7777")

        vm.select(5)
        XCTAssertEqual(vm.selectedID, 5)
        await contractWait { vm.detail != nil }
        XCTAssertEqual(log.nodeCalls, [ContractNodeCall(baseURL: "http://mac.local:7777", id: 5)])
        XCTAssertEqual(vm.detail?.node.aliases, ["render-md"])
    }

    func testSelectNilClearsTheSelectionAndTheDetail() async {
        let vm = makeVM(graph: board(), node: detail())
        await vm.load(baseURL: "http://mac.local:7777")
        vm.select(5)
        await contractWait { vm.detail != nil }

        vm.select(nil)
        XCTAssertNil(vm.selectedID)
        XCTAssertNil(vm.detail)
        XCTAssertNil(vm.selectedNode)
        XCTAssertEqual(vm.selectedFacts.count, 0)
    }

    // Nothing has been loaded, so there is no base URL to fetch against. The
    // selection still takes; the fetch simply does not happen.
    func testSelectingBeforeAnyLoadFetchesNothing() async {
        let log = ContractCallLog()
        let vm = makeVM(log: log, graph: board(), node: detail())
        vm.select(5)
        XCTAssertEqual(vm.selectedID, 5)
        await contractWait(timeout: 0.3) { !log.nodeCalls.isEmpty }
        XCTAssertEqual(log.nodeCalls, [])
        XCTAssertNil(vm.detail)
        XCTAssertNil(vm.selectedNode, "no payload either, so there is nothing to fall back to")
    }

    // The drawer opens on the board payload's own copy of the node and its
    // facts, and swaps to the detail when it lands. `select` is synchronous,
    // so the state right after it returns IS the first frame the drawer draws.
    func testTheDrawerFallsBackToThePayloadUntilTheDetailLands() async {
        let vm = makeVM(graph: board(), node: detail())
        await vm.load(baseURL: "http://mac.local:7777")

        vm.select(5)
        XCTAssertNil(vm.detail, "select clears any previous detail before fetching")
        XCTAssertEqual(vm.selectedNode?.id, 5)
        XCTAssertNil(vm.selectedNode?.aliases, "the board payload carries no aliases")
        XCTAssertEqual(vm.selectedFacts.map(\.id), [4, 5],
                       "payload edges touching 5, strength descending")

        await contractWait { vm.detail != nil }
        XCTAssertEqual(vm.selectedNode?.aliases, ["render-md"])
        XCTAssertEqual(vm.selectedFacts.map(\.id), [4, 5, 9],
                       "detail edges: live by strength descending, then the invalidated one")
    }

    // A detail fetch that fails is a degraded drawer, not an error banner —
    // the payload fallback is the whole reason it can be silent.
    func testAFailedDetailFetchLeavesTheErrorLineAlone() async {
        let vm = makeVM(graph: board(), node: detail(), nodeFails: true)
        await vm.load(baseURL: "http://mac.local:7777")
        XCTAssertNil(vm.errorText)

        vm.select(5)
        await contractWait(timeout: 0.5) { vm.detail != nil }
        XCTAssertNil(vm.detail)
        XCTAssertNil(vm.errorText, "a detail failure never touches the screen's error line")
        XCTAssertEqual(vm.selectedNode?.id, 5, "still the payload's node")
        XCTAssertEqual(vm.selectedFacts.map(\.id), [4, 5])
    }

    // `selectedFacts` is every edge TOUCHING the node, either end.
    func testSelectedFactsTakeBothEndpoints() async {
        let vm = makeVM(graph: board(), node: detail(), nodeFails: true)
        await vm.load(baseURL: "http://mac.local:7777")

        vm.select(6)
        XCTAssertEqual(Set(vm.selectedFacts.map(\.id)), [5, 6])
        XCTAssertEqual(vm.selectedFacts.map(\.id), [5, 6], "strength 8 before strength 2")

        vm.select(1)
        XCTAssertEqual(vm.selectedFacts.map(\.id), [4, 6])
    }

    func testSelectingANodeThatIsNotInThePayloadResolvesToNothing() async {
        let vm = makeVM(graph: board(), node: detail(), nodeFails: true)
        await vm.load(baseURL: "http://mac.local:7777")
        vm.select(4242)
        XCTAssertEqual(vm.selectedID, 4242)
        XCTAssertNil(vm.selectedNode)
        XCTAssertEqual(vm.selectedFacts.count, 0)
    }

    func testFilterStateIsWritableFromTheUI() async {
        let vm = makeVM(graph: board())
        await vm.load(baseURL: "http://mac.local:7777")
        vm.hiddenTypes.insert("person")
        vm.includeInvalidated = true
        XCTAssertEqual(vm.hiddenTypes, ["person"])
        XCTAssertTrue(vm.includeInvalidated)

        // Filters are render state; they never mutate the payload.
        XCTAssertEqual(vm.payload, board())
    }
}
