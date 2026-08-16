import XCTest
import Foundation
@testable import Kraken

// LibraryEdgeTests — the EDGE dimension of artifacts/50-contract.md's Slice C
// pure layer: `libraryItemMatching`'s nil arms and boundary urls, and what
// `WebLibraryItem`/`LibraryPayload` do with wire bodies that are not the happy
// one. (The item type was frozen as `LibraryItem` and AMENDED to
// `WebLibraryItem` at the join: SwiftUI re-exports
// `DeveloperToolsSupport.LibraryItem`, so the bare name is ambiguous in any file
// that imports both SwiftUI and the app under test.)
// Authored blind from the frozen contract, before the implementation existed, so
// every assertion traces to a sentence in that file rather than to code — where
// the contract is silent this file asserts nothing.
//
// The happy paths and the stated invariants belong to the other blind author
// (LibraryContractTests); what lives here is the second half of each rule: the
// empty list, the id that matches nothing, the pair that agrees on only one
// half, the key that is absent, and the Int64 that a Double cannot hold.
//
// Every file-scope identifier is `private` and prefixed `edge` — this test
// target's namespace is shared with the sibling blind file.

// MARK: - Fixtures

/// The wire body `GET /api/library` serves, in the contract's own key spelling
/// (`saved_at`, snake case) — written out rather than built from the model so a
/// decode failure names the bytes that failed.
private let edgeLibraryPayloadJSON = """
{"items":[{"id":7,"slug":"save-artifacts","name":"00-overview.md","saved_at":1770000000000,"url":"/api/library/artifact/7/00-overview.md"},{"id":6,"slug":"graph-view","name":"icons/v2.svg","saved_at":1769000000000,"url":"/api/library/artifact/6/icons/v2.svg"}]}
"""

private func edgeDecodeLibrary<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(json.utf8))
}

private func edgeItem(
    _ id: Int64,
    slug: String = "a-job",
    name: String = "00-overview.md",
    savedAt: Int64 = 1_770_000_000_000
) -> WebLibraryItem {
    WebLibraryItem(
        id: id,
        slug: slug,
        name: name,
        savedAt: savedAt,
        url: "/api/library/artifact/\(id)/\(name)"
    )
}

final class LibraryEdgeTests: XCTestCase {

    // MARK: - decode: the shapes that are not the happy one

    /// Obligation: `LibraryPayload` is `{ let items: [WebLibraryItem] }`, and the
    /// wire's `items` is "always an array, never null".
    ///
    /// The empty array is the state before the first save and after the last
    /// remove, and it is the body the screen renders its empty state from — a
    /// decode that threw here would show a fetch error for an ordinary state.
    func testEmptyItemsDecodesToAnEmptyArray() throws {
        let payload: LibraryPayload = try edgeDecodeLibrary(#"{"items":[]}"#)
        XCTAssertEqual(payload.items, [])
    }

    /// Codable's own tolerance, which the wire contract depends on: the server
    /// may add keys (it is additive by rule — `apiContract` stays 6), and an
    /// installed build must keep decoding when it does. A model that rejected
    /// unknown keys would turn every additive server change into a dead app.
    func testUnknownKeysAreIgnored() throws {
        let json = """
        {"items":[{"id":7,"slug":"save-artifacts","name":"00-overview.md","saved_at":1770000000000,"url":"/api/library/artifact/7/00-overview.md","size":4096,"job_id":313}],"total":1}
        """
        let payload: LibraryPayload = try edgeDecodeLibrary(json)
        XCTAssertEqual(payload.items.count, 1)
        XCTAssertEqual(payload.items.first?.id, 7)
        XCTAssertEqual(payload.items.first?.savedAt, 1_770_000_000_000)
    }

    /// Obligation: `savedAt` ↔ `saved_at`. The wire key is snake case and the
    /// Swift property is not, so the mapping is explicit — and it is the one
    /// field where a silently-missed mapping would not be caught by anything
    /// else: every row would render with the same epoch date rather than fail.
    func testSavedAtReadsTheSnakeCaseWireKey() throws {
        let payload: LibraryPayload = try edgeDecodeLibrary(edgeLibraryPayloadJSON)
        XCTAssertEqual(payload.items.map(\.savedAt), [1_770_000_000_000, 1_769_000_000_000])
    }

    /// Every field is a non-optional `let`, so a body missing one is a decode
    /// FAILURE rather than a row with a defaulted value. A silently defaulted
    /// `id` would be a row whose remove POST addresses something else.
    func testAMissingRequiredKeyFailsToDecode() {
        let bodies = [
            #"{"items":[{"slug":"j","name":"x.md","saved_at":1,"url":"/u"}]}"#,          // no id
            #"{"items":[{"id":1,"name":"x.md","saved_at":1,"url":"/u"}]}"#,              // no slug
            #"{"items":[{"id":1,"slug":"j","saved_at":1,"url":"/u"}]}"#,                 // no name
            #"{"items":[{"id":1,"slug":"j","name":"x.md","url":"/u"}]}"#,                // no saved_at
            #"{"items":[{"id":1,"slug":"j","name":"x.md","saved_at":1}]}"#,              // no url
            #"{"items":[{"id":1,"slug":"j","name":"x.md","savedAt":1,"url":"/u"}]}"#,    // camel-case saved_at
        ]
        for body in bodies {
            XCTAssertThrowsError(
                try edgeDecodeLibrary(body, as: LibraryPayload.self),
                "body should not decode: \(body)"
            )
        }
    }

    /// `id` and `savedAt` are `Int64`, and both are values a `Double` cannot
    /// hold exactly: unix millis passed 2^53 in 2255, but a row id does not have
    /// to — and a JSON number decoded through Double and back is silently off by
    /// one, which for an id is a different row.
    func testInt64FieldsKeepFullPrecision() throws {
        let json = """
        {"items":[{"id":9007199254740993,"slug":"j","name":"x.md","saved_at":9007199254740993,"url":"/u"}]}
        """
        let payload: LibraryPayload = try edgeDecodeLibrary(json)
        XCTAssertEqual(payload.items.first?.id, 9_007_199_254_740_993)
        XCTAssertEqual(payload.items.first?.savedAt, 9_007_199_254_740_993)
    }

    /// Obligation: "`Codable` not bare `Decodable` (test round-trip idiom)". The
    /// round trip is the assertion the choice exists for, and `Equatable` is
    /// what makes it checkable in one line.
    func testCodableRoundTripsThroughEncodeAndDecode() throws {
        let original: LibraryPayload = try edgeDecodeLibrary(edgeLibraryPayloadJSON)
        let data = try JSONEncoder().encode(original)
        let again = try JSONDecoder().decode(LibraryPayload.self, from: data)
        XCTAssertEqual(again, original)
    }

    /// Obligation: "`var id` requirement satisfied by the `id` field"
    /// (`Identifiable`). A list built on a synthesised or index-based identity
    /// re-renders every row on every poll; one built on the row id does not.
    func testIdentifiableIDIsTheRowID() throws {
        let payload: LibraryPayload = try edgeDecodeLibrary(edgeLibraryPayloadJSON)
        XCTAssertEqual(payload.items.map(\.id), [7, 6])
    }

    // MARK: - libraryItemMatching: the nil arms

    /// Obligation: "Same matching rule as web `libraryMatch`: library urls by
    /// id, `/api/artifact/` urls by `(slug, name)` via the existing
    /// `artifactParts(from:)`, else nil."
    ///
    /// The toggle asks this on every pager page, so a url shape it does not
    /// understand must read as "not saved" rather than match something.
    func testUnknownURLFamiliesReturnNil() {
        let items = [edgeItem(1), edgeItem(2, slug: "b-job", name: "10-architecture.html")]
        for url in [
            "",
            "/",
            "/api/board",
            "/api/upload/a-job/img-001.png",
            "/api/feedback/snapshot?id=1",
            "/api/artifacts/a-job/pluto/00-overview.md",
            "/api/libraries/artifact/1/00-overview.md",
            // Relative: the app always joins baseURL itself, so a path without
            // the leading slash is a caller bug (FeedbackTests pins the same
            // rule for artifactParts).
            "api/library/artifact/1/00-overview.md",
            "api/artifact/a-job/pluto/00-overview.md",
        ] {
            XCTAssertNil(libraryItemMatching(url: url, in: items), "url \(url) should not match")
        }
    }

    /// The empty library: the state every install is in until the first save,
    /// and the one the toggle renders against on every artifact opened before
    /// then.
    func testEmptyItemListNeverMatches() {
        XCTAssertNil(libraryItemMatching(url: "/api/library/artifact/1/00-overview.md", in: []))
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/a-job/pluto/00-overview.md", in: []))
    }

    /// A well-formed library url whose id names no row — the ordinary state
    /// right after a swipe-to-delete, and of any url held while a poll is in
    /// flight.
    func testLibraryURLWithAnUnheldIDReturnsNil() {
        let items = [edgeItem(7), edgeItem(8)]
        XCTAssertNil(libraryItemMatching(url: "/api/library/artifact/9/00-overview.md", in: items))
        XCTAssertNil(libraryItemMatching(url: "/api/library/artifact/70/00-overview.md", in: items))
    }

    /// A non-numeric id can match no item, since every id is an Int64. It must
    /// read as nil rather than as a parse the matcher then compares loosely.
    func testMalformedLibraryIDSegmentReturnsNil() {
        let items = [edgeItem(7)]
        for url in [
            "/api/library/artifact/abc/00-overview.md",
            "/api/library/artifact/7x/00-overview.md",
            "/api/library/artifact//00-overview.md",
            "/api/library/artifact/",
            "/api/library/artifact",
        ] {
            XCTAssertNil(libraryItemMatching(url: url, in: items), "url \(url) should not match")
        }
    }

    // MARK: - libraryItemMatching: the matches

    /// Library urls match by id and by id ALONE — the trailing segment is
    /// display/extension only on the server, so a matcher that compared it too
    /// would fail exactly when a stored name and an opened url drift apart.
    func testLibraryURLMatchesByIDRegardlessOfTheNameSegment() {
        let items = [edgeItem(7), edgeItem(8, slug: "b-job", name: "10-architecture.html")]
        XCTAssertEqual(libraryItemMatching(url: "/api/library/artifact/7/00-overview.md", in: items)?.id, 7)
        XCTAssertEqual(libraryItemMatching(url: "/api/library/artifact/7/renamed.md", in: items)?.id, 7)
        XCTAssertEqual(libraryItemMatching(url: "/api/library/artifact/8/10-architecture.html", in: items)?.id, 8)
    }

    /// A nested stored name arrives as several path segments after the id; only
    /// the first one is the id.
    func testLibraryURLWithANestedNameMatchesByID() {
        let items = [edgeItem(12, slug: "icons", name: "icon-variations/v2.svg")]
        XCTAssertEqual(
            libraryItemMatching(url: "/api/library/artifact/12/icon-variations/v2.svg", in: items)?.id,
            12
        )
    }

    /// Artifact urls match the `(slug, name)` PAIR. This is the url the pager is
    /// usually showing, so it is the arm the bookmark glyph is actually decided
    /// by.
    func testArtifactURLMatchesTheSlugNamePair() {
        let items = [
            edgeItem(1, slug: "a-job", name: "00-overview.md"),
            edgeItem(2, slug: "b-job", name: "00-overview.md"),
            edgeItem(3, slug: "a-job", name: "10-architecture.html"),
        ]
        XCTAssertEqual(libraryItemMatching(url: "/api/artifact/a-job/pluto/00-overview.md", in: items)?.id, 1)
        XCTAssertEqual(libraryItemMatching(url: "/api/artifact/b-job/pluto/00-overview.md", in: items)?.id, 2)
        XCTAssertEqual(libraryItemMatching(url: "/api/artifact/a-job/pluto/10-architecture.html", in: items)?.id, 3)
    }

    /// The home segment is deliberately not part of the pair: an artifact's home
    /// moves with its job's lifecycle (cell → pluto → archive) while the saved
    /// row does not, so a matcher including it would un-star every item the
    /// moment its job was torn down.
    func testArtifactURLMatchesAcrossEveryHome() {
        let items = [edgeItem(1, slug: "a-job", name: "00-overview.md")]
        for home in ["cell", "pluto", "archive"] {
            XCTAssertEqual(
                libraryItemMatching(url: "/api/artifact/a-job/\(home)/00-overview.md", in: items)?.id, 1,
                "home \(home) should match"
            )
        }
    }

    /// Everything after the home segment is the name, slashes included — the
    /// rule `artifactParts(from:)` already follows (FeedbackTests pins it), and
    /// the leaf alone is a different artifact rather than another spelling.
    func testArtifactURLKeepsANestedNamesSlashes() {
        let items = [edgeItem(5, slug: "icons", name: "icon-variations/v2.png")]
        XCTAssertEqual(
            libraryItemMatching(url: "/api/artifact/icons/cell/icon-variations/v2.png", in: items)?.id, 5
        )
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/icons/cell/v2.png", in: items))
    }

    /// Half a pair is not a match: both halves come from the same url, so a
    /// one-sided comparison would bookmark every artifact of a job that has any
    /// saved item at all.
    func testArtifactURLWithOnlyOneHalfAgreeingReturnsNil() {
        let items = [edgeItem(1, slug: "a-job", name: "00-overview.md")]
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/a-job/pluto/90-build-report.md", in: items))
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/other-job/pluto/00-overview.md", in: items))
    }

    /// "exact" is exact. Slugs and artifact names are both case-sensitive
    /// server-side, so a case-folded match would claim a saved item that the
    /// remove POST could then not address.
    func testArtifactURLPairIsCaseSensitive() {
        let items = [edgeItem(1, slug: "a-job", name: "00-overview.md")]
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/A-Job/pluto/00-overview.md", in: items))
        XCTAssertNil(libraryItemMatching(url: "/api/artifact/a-job/pluto/00-OVERVIEW.md", in: items))
    }

    /// The malformed artifact urls `artifactParts(from:)` already returns nil
    /// for: no name segment, an empty name segment, no home at all.
    func testMalformedArtifactURLsReturnNil() {
        let items = [edgeItem(1, slug: "a-job", name: "00-overview.md")]
        for url in [
            "/api/artifact/a-job/pluto",
            "/api/artifact/a-job/pluto/",
            "/api/artifact/a-job",
            "/api/artifact/",
            "/api/artifact",
        ] {
            XCTAssertNil(libraryItemMatching(url: url, in: items), "url \(url) should not match")
        }
    }
}
