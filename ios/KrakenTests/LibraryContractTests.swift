import XCTest
import Foundation
@testable import Kraken

// LibraryContractTests — athena-logic's BLIND contract tests for Slice C's two
// pure seams: the `WebLibraryItem`/`LibraryPayload` wire models and the free
// function `libraryItemMatching(url:in:)`.
//
// Authored from artifacts/50-contract.md ALONE, before any of the code they
// exercise existed. Nothing under ios/Kraken/ was read by any route.
//
// Dimension: CONTRACT — the stated obligations and happy paths. Boundary shapes
// (malformed urls, absent keys, duplicate rows) are athena-edge's ground in
// LibraryEdgeTests.swift; where the contract is silent this file asserts
// nothing.
//
// Two deliberate authoring choices, both about surviving a blind join (the
// GraphContractTests idiom):
//
//  1. Every value is built by DECODING JSON rather than by a memberwise
//     initializer. The contract freezes the JSON shape and the property list;
//     it does not freeze the presence of a synthesized memberwise init (a
//     hand-written `init(from:)` in the struct body would silently remove one).
//     Decoding is the construction path the contract actually promises.
//  2. `Identifiable` is probed through a generic constrained to it, so the
//     assertion is the CONFORMANCE the contract names rather than the mere
//     existence of a property called `id`.
//
// Every file-scope identifier here is `contractLibrary`-prefixed; the test
// class is `LibraryContractTests` as the contract names it.

// MARK: - Fixtures

/// `GET /api/library`, in the frozen wire shape: five keys per item, `saved_at`
/// snake-cased, `url` carrying a nested name with its slashes intact.
private let contractLibraryPayloadFixture = """
{"items":[{"id":9,"slug":"kraken-graph","name":"icons/deep/diagram.svg","saved_at":1755300000000,"url":"/api/library/artifact/9/icons/deep/diagram.svg"},{"id":7,"slug":"kraken-graph","name":"00-overview.md","saved_at":1755200000000,"url":"/api/library/artifact/7/00-overview.md"},{"id":4,"slug":"monolith-yelp","name":"00-overview.md","saved_at":1755100000000,"url":"/api/library/artifact/4/00-overview.md"}]}
"""

private func contractLibraryDecode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(json.utf8))
}

private func contractLibraryItemJSON(id: Int64, slug: String, name: String,
                                     savedAt: Int64, url: String) -> String {
    "{\"id\":\(id),\"slug\":\"\(slug)\",\"name\":\"\(name)\",\"saved_at\":\(savedAt),\"url\":\"\(url)\"}"
}

/// One item, always via the decoder. `try!` deliberately: a fixture that will
/// not decode is a test-authoring bug and should stop the run loudly.
private func contractLibraryItem(id: Int64, slug: String, name: String,
                                 savedAt: Int64 = 1_755_000_000_000) -> WebLibraryItem {
    let json = contractLibraryItemJSON(id: id, slug: slug, name: name, savedAt: savedAt,
                                       url: "/api/library/artifact/\(id)/\(name)")
    return try! contractLibraryDecode(json)
}

/// The three-item library every matching test searches. Two items deliberately
/// share the name `00-overview.md` under different slugs, so a match that
/// leaned on the name alone could not tell them apart.
private let contractLibraryItems: [WebLibraryItem] = [
    contractLibraryItem(id: 9, slug: "kraken-graph", name: "icons/deep/diagram.svg"),
    contractLibraryItem(id: 7, slug: "kraken-graph", name: "00-overview.md"),
    contractLibraryItem(id: 4, slug: "monolith-yelp", name: "00-overview.md"),
]

/// Reads `id` through the `Identifiable` conformance rather than off the
/// concrete type, so this is an assertion about the protocol the contract names.
private func contractLibraryIdentity<T: Identifiable>(_ value: T) -> T.ID { value.id }

final class LibraryContractTests: XCTestCase {

    // MARK: - WebLibraryItem / LibraryPayload

    /// The payload decodes, in served order, with every field mapped — and
    /// `saved_at` landing on `savedAt`, which is the one key whose Swift name
    /// differs from the wire's.
    func testLibraryPayloadDecodesEveryFieldInServedOrder() throws {
        let payload: LibraryPayload = try contractLibraryDecode(contractLibraryPayloadFixture)
        XCTAssertEqual(payload.items.count, 3)
        XCTAssertEqual(payload.items.map(\.id), [9, 7, 4],
                       "the client renders the served order; decoding must not reorder")

        let first = payload.items[0]
        XCTAssertEqual(first.id, 9)
        XCTAssertEqual(first.slug, "kraken-graph")
        XCTAssertEqual(first.name, "icons/deep/diagram.svg",
                       "a nested name keeps its slashes")
        XCTAssertEqual(first.savedAt, 1_755_300_000_000,
                       "saved_at maps onto savedAt, in unix millis")
        XCTAssertEqual(first.url, "/api/library/artifact/9/icons/deep/diagram.svg")
    }

    /// An empty library decodes to an empty array, not to a failure — the wire
    /// promises `items` is always an array.
    func testLibraryPayloadDecodesAnEmptyItemsArray() throws {
        let payload: LibraryPayload = try contractLibraryDecode("{\"items\":[]}")
        XCTAssertTrue(payload.items.isEmpty)
    }

    /// `Codable`, not bare `Decodable`: the item survives an encode/decode round
    /// trip, and the encoded form spells the wire key `saved_at` rather than
    /// Swift's `savedAt`.
    func testLibraryItemRoundTripsThroughCodableKeepingTheWireKey() throws {
        let original = contractLibraryItem(id: 7, slug: "kraken-graph",
                                           name: "00-overview.md", savedAt: 1_755_200_000_000)
        let data = try JSONEncoder().encode(original)
        let encoded = String(decoding: data, as: UTF8.self)
        XCTAssertTrue(encoded.contains("\"saved_at\""),
                      "encoded item must carry the wire key saved_at: \(encoded)")
        XCTAssertFalse(encoded.contains("\"savedAt\""),
                       "encoded item must not leak the Swift property name: \(encoded)")

        let round: WebLibraryItem = try JSONDecoder().decode(WebLibraryItem.self, from: data)
        XCTAssertEqual(round, original, "WebLibraryItem is Equatable and must round-trip unchanged")
    }

    /// The whole payload round-trips too — `LibraryPayload` is `Codable` and
    /// `Equatable` for exactly this.
    func testLibraryPayloadRoundTrips() throws {
        let original: LibraryPayload = try contractLibraryDecode(contractLibraryPayloadFixture)
        let data = try JSONEncoder().encode(original)
        let round: LibraryPayload = try JSONDecoder().decode(LibraryPayload.self, from: data)
        XCTAssertEqual(round, original)
    }

    /// Two items differing in exactly one field are not equal — otherwise the
    /// `Equatable` conformance above would be asserting nothing.
    func testLibraryItemEqualityIsFieldwise() {
        let base = contractLibraryItem(id: 7, slug: "kraken-graph", name: "00-overview.md")
        XCTAssertEqual(base, contractLibraryItem(id: 7, slug: "kraken-graph", name: "00-overview.md"))
        XCTAssertNotEqual(base, contractLibraryItem(id: 8, slug: "kraken-graph", name: "00-overview.md"))
        XCTAssertNotEqual(base, contractLibraryItem(id: 7, slug: "other-job", name: "00-overview.md"))
        XCTAssertNotEqual(base, contractLibraryItem(id: 7, slug: "kraken-graph", name: "90-build-report.md"))
        XCTAssertNotEqual(base, contractLibraryItem(id: 7, slug: "kraken-graph",
                                                    name: "00-overview.md", savedAt: 1))
    }

    /// `Identifiable`, satisfied by the `id` field: a SwiftUI `List`/`ForEach`
    /// over the served rows needs it, and two rows sharing a name must still be
    /// distinct identities.
    func testLibraryItemIsIdentifiableByItsID() {
        let a = contractLibraryItem(id: 7, slug: "kraken-graph", name: "00-overview.md")
        let b = contractLibraryItem(id: 4, slug: "monolith-yelp", name: "00-overview.md")
        XCTAssertEqual(contractLibraryIdentity(a), 7)
        XCTAssertEqual(contractLibraryIdentity(b), 4)
        XCTAssertNotEqual(contractLibraryIdentity(a), contractLibraryIdentity(b))
    }

    // MARK: - libraryItemMatching

    /// A library url matches by its id segment.
    func testLibraryItemMatchingMatchesALibraryURLByID() {
        XCTAssertEqual(
            libraryItemMatching(url: "/api/library/artifact/7/00-overview.md", in: contractLibraryItems),
            contractLibraryItems[1])
        XCTAssertEqual(
            libraryItemMatching(url: "/api/library/artifact/9/icons/deep/diagram.svg", in: contractLibraryItems),
            contractLibraryItems[0])
        XCTAssertEqual(
            libraryItemMatching(url: "/api/library/artifact/4/00-overview.md", in: contractLibraryItems),
            contractLibraryItems[2])
    }

    /// The id is the WHOLE lookup for a library url — the server's own rule is
    /// that the trailing name segment is display/extension only.
    func testLibraryItemMatchingIgnoresALibraryURLsNameSegment() {
        XCTAssertEqual(
            libraryItemMatching(url: "/api/library/artifact/7/something-else.html", in: contractLibraryItems),
            contractLibraryItems[1])
    }

    func testLibraryItemMatchingReturnsNilForAnUnknownLibraryID() {
        XCTAssertNil(libraryItemMatching(url: "/api/library/artifact/404/00-overview.md",
                                         in: contractLibraryItems))
    }

    /// An `/api/artifact/` url matches by the exact `(slug, name)` pair — the
    /// two-name fixture is what makes this more than a name lookup.
    func testLibraryItemMatchingMatchesAnArtifactURLBySlugAndName() {
        XCTAssertEqual(
            libraryItemMatching(url: "/api/artifact/kraken-graph/cell/00-overview.md",
                                in: contractLibraryItems),
            contractLibraryItems[1])
        XCTAssertEqual(
            libraryItemMatching(url: "/api/artifact/monolith-yelp/pluto/00-overview.md",
                                in: contractLibraryItems),
            contractLibraryItems[2])
    }

    /// The home segment is not part of the pair: one save must read as saved
    /// while the job is live (`cell`), once it is done (`pluto`) and after it is
    /// archived (`archive`).
    func testLibraryItemMatchingIgnoresTheHomeSegment() {
        for home in ["cell", "pluto", "archive"] {
            XCTAssertEqual(
                libraryItemMatching(url: "/api/artifact/kraken-graph/\(home)/00-overview.md",
                                    in: contractLibraryItems),
                contractLibraryItems[1],
                "home \(home) must not change the match")
        }
    }

    /// A nested artifact name keeps its slashes on the way through.
    func testLibraryItemMatchingMatchesANestedArtifactName() {
        XCTAssertEqual(
            libraryItemMatching(url: "/api/artifact/kraken-graph/cell/icons/deep/diagram.svg",
                                in: contractLibraryItems),
            contractLibraryItems[0])
    }

    /// Half a pair is not a match — and neither is a pair assembled across two
    /// different rows.
    func testLibraryItemMatchingRequiresBothHalvesOfThePair() {
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/kraken-graph/cell/90-build-report.md",
                                         in: contractLibraryItems),
                     "right slug, wrong name")
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/some-other-job/cell/00-overview.md",
                                         in: contractLibraryItems),
                     "right name, wrong slug")
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/monolith-yelp/cell/icons/deep/diagram.svg",
                                         in: contractLibraryItems),
                     "slug from one row, name from another")
    }

    /// A url in neither family is nil.
    func testLibraryItemMatchingReturnsNilForOtherURLs() {
        XCTAssertNil(libraryItemMatching(url: "/api/upload/kraken-graph/img-001.png",
                                         in: contractLibraryItems))
        XCTAssertNil(libraryItemMatching(url: "/api/board", in: contractLibraryItems))
        XCTAssertNil(libraryItemMatching(url: "", in: contractLibraryItems))
    }

    /// An empty library matches nothing, in either family.
    func testLibraryItemMatchingReturnsNilAgainstAnEmptyLibrary() {
        XCTAssertNil(libraryItemMatching(url: "/api/library/artifact/7/00-overview.md", in: []))
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/kraken-graph/cell/00-overview.md", in: []))
    }
}
