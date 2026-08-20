import XCTest
import Foundation
@testable import Kraken

// MMContractTests — blind tests for the iOS mental-model slice (S5),
// authored from artifacts/50-contract.md ALONE, before any of the code they
// exercise existed. Dimension: the STATED invariants and happy paths of the
// S5 table. A second author (MMEdgeTests.swift) owns boundaries and
// refusals; where the contract is silent this file asserts nothing and the
// silence is listed in the author's report instead.
//
// Three authoring choices, all about surviving a blind join:
//
//  1. Every wire value is built by DECODING JSON, never by a memberwise
//     initializer. The contract freezes the JSON shape and the property
//     list; it does not freeze the presence of a synthesized memberwise init
//     (a hand-written `init(from:)` in the struct body silently removes
//     one). Decoding is the construction path the contract promises, and it
//     is the path the app actually takes. Same reasoning as
//     GraphContractTests.
//  2. Nothing here compares two whole `WebMMItem`/`WebMMAnchor` values. The
//     contract declares them `Decodable` (and `Identifiable` for the item)
//     and says nothing about `Equatable`, so identity is asserted through
//     `id` and fields through their own accessors.
//  3. `mmMatch` is called as `mmMatch(items:slug:name:)`. The contract as
//     first frozen named the SYMBOL and its inputs ("given items + (slug,
//     name)") but no argument labels; this file was authored against the
//     literal reading of that sentence and the silence was reported. The
//     contract's mmMatch row was AMENDED AT THE JOIN to freeze the fully
//     labelled spelling, and the ten call sites below carry it.
//
// Every file-scope identifier is `contractMM`-prefixed because the edge
// author shares this test target's namespace.

// MARK: - Fixtures (keys verbatim from 50-contract.md's S5 rows)

/// The MAXIMALLY full row: every key `webMMItem` can carry, including the
/// omitempty invalidation trio and a fully-populated anchor. Each field holds
/// a value distinguishable from every other field of its own type, so a
/// swapped CodingKey fails rather than passing by coincidence.
///
/// `check_error` riding alongside `invalid_*` is a WIRE-SHAPE fixture, not a
/// semantic claim about how a real row reaches that state — this file is
/// asserting the decoder, and the decoder must carry every key it is given.
private let contractMMFullItemJSON = """
{"id":42,
 "kind":"reading",
 "source":"code",
 "slug":"committed-artifacts-for-mental-model-349",
 "source_ref":"kraken/internal/jobs/mm.go:120-138",
 "content":"func MMVerifyPass(st *store.Store, now time.Time) {",
 "context":"the throttle stamp lands before any bail",
 "created_at":1755300000000,
 "episode_id":907,
 "invalidated_at":1755386400000,
 "invalid_reason":"content-changed",
 "invalid_detail":"text not found at head",
 "anchor":{"repo":"kraken",
           "path":"internal/jobs/mm.go",
           "orig_path":"internal/jobs/verify.go",
           "start_line":120,
           "end_line":138,
           "anchor_sha":"a1b2c3d4e5f60718293a4b5c6d7e8f9012345678",
           "anchor_kind":"origin-main",
           "text_sha":"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b",
           "checked_at":1755303600000,
           "checked_sha":"0badc0ffee0badc0ffee0badc0ffee0badc0ffee",
           "check_error":"fatal: could not read Username",
           "ambiguous":true,
           "invalid_sha":"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
           "invalid_pr":"https://github.com/car1zhang/kraken/pull/231"}}
"""

/// A LIVE row: the invalidation trio absent (omitempty), no anchor (a mark
/// that is not source=code carries none), `context` present-and-empty — the
/// shape the join fixed as always-present on the wire.
private let contractMMLiveItemJSON = """
{"id":7,
 "kind":"decision",
 "source":"question",
 "slug":"committed-artifacts-for-mental-model-349",
 "source_ref":"q3",
 "content":"Does any byte change invalidate an artifact mark?\\n\\u2192 yes",
 "context":"",
 "created_at":1755200000000,
 "episode_id":880}
"""

/// The `GET /api/mm` envelope over both rows above, live first.
private let contractMMPageJSON = """
{"items":[\(contractMMLiveItemJSON),\(contractMMFullItemJSON)]}
"""

/// The minimal DIRTY anchor the contract names by hand: "repo/path/lines/kind
/// /text_sha only". Every omitempty anchor key is absent.
private let contractMMDirtyAnchorItemJSON = """
{"id":9,
 "kind":"reading",
 "source":"code",
 "slug":"a-job",
 "source_ref":"monolith/core/lib/util.py:4-4",
 "content":"def slugify(s):",
 "context":"",
 "created_at":1755100000000,
 "episode_id":871,
 "anchor":{"repo":"monolith",
           "path":"core/lib/util.py",
           "start_line":4,
           "end_line":4,
           "anchor_kind":"dirty",
           "text_sha":"1a2b3c4d5e6f70819a2b3c4d5e6f70819a2b3c4d"}}
"""

// MARK: - Builders

private func contractMMDecode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(json.utf8))
}

/// One `webMMItem` object with the keys the S3 wire row lists. `invalidatedAt
/// == nil` omits the whole invalidation trio, exactly as `omitempty` does.
private func contractMMItemJSON(id: Int64,
                                kind: String = "reading",
                                source: String = "artifact",
                                slug: String,
                                sourceRef: String,
                                content: String = "content",
                                context: String = "",
                                createdAt: Int64 = 1_755_000_000_000,
                                episodeID: Int64 = 1,
                                invalidatedAt: Int64? = nil,
                                invalidReason: String = "removed") -> String {
    var s = "{\"id\":\(id),\"kind\":\"\(kind)\",\"source\":\"\(source)\","
    s += "\"slug\":\"\(slug)\",\"source_ref\":\"\(sourceRef)\","
    s += "\"content\":\"\(content)\",\"context\":\"\(context)\","
    s += "\"created_at\":\(createdAt),\"episode_id\":\(episodeID)"
    if let invalidatedAt {
        s += ",\"invalidated_at\":\(invalidatedAt),\"invalid_reason\":\"\(invalidReason)\""
    }
    return s + "}"
}

/// try! deliberately: a fixture that will not decode is an authoring bug and
/// should stop the run loudly rather than read as a passing nil arm.
private func contractMMItem(id: Int64,
                            kind: String = "reading",
                            source: String = "artifact",
                            slug: String,
                            sourceRef: String,
                            content: String = "content",
                            context: String = "",
                            createdAt: Int64 = 1_755_000_000_000,
                            episodeID: Int64 = 1,
                            invalidatedAt: Int64? = nil) -> WebMMItem {
    try! contractMMDecode(contractMMItemJSON(id: id, kind: kind, source: source, slug: slug,
                                             sourceRef: sourceRef, content: content,
                                             context: context, createdAt: createdAt,
                                             episodeID: episodeID,
                                             invalidatedAt: invalidatedAt))
}

final class MMContractTests: XCTestCase {

    // MARK: - WebMMItem: the full wire row

    /// Obligation (S5 `WebMMItem` row + S3's `webMMItem` key list):
    /// "`source_ref`/`created_at`/`episode_id`/`invalidated_at`/
    /// `invalid_reason`/`invalid_detail` snake_case-mapped".
    ///
    /// The whole point of the row is that a key the server spells one way
    /// reaches the property the view reads. Every value below is distinct
    /// within its type, so a CodingKey that maps `invalid_reason` onto
    /// `invalidDetail` (or `created_at` onto `episode_id`) fails here rather
    /// than in a screenshot three weeks later.
    func testContractMMItemDecodesTheFullWireRow() throws {
        let it: WebMMItem = try contractMMDecode(contractMMFullItemJSON)
        XCTAssertEqual(it.id, 42)
        XCTAssertEqual(it.kind, "reading")
        XCTAssertEqual(it.source, "code")
        XCTAssertEqual(it.slug, "committed-artifacts-for-mental-model-349")
        XCTAssertEqual(it.sourceRef, "kraken/internal/jobs/mm.go:120-138")
        XCTAssertEqual(it.content, "func MMVerifyPass(st *store.Store, now time.Time) {")
        XCTAssertEqual(it.context, "the throttle stamp lands before any bail")
        XCTAssertEqual(it.createdAt, 1_755_300_000_000)
        XCTAssertEqual(it.episodeId, 907)
        XCTAssertEqual(it.invalidatedAt, 1_755_386_400_000)
        XCTAssertEqual(it.invalidReason, "content-changed")
        XCTAssertEqual(it.invalidDetail, "text not found at head")
        XCTAssertNotNil(it.anchor)
    }

    /// Obligation (S5 `WebMMAnchor` row): "CodingKeys map the wire's
    /// snake_case exactly (`orig_path`, `anchor_sha`, `text_sha`, …)".
    ///
    /// Read through the item, because that is the only way the anchor ever
    /// arrives — it is a nested object on the list row, never its own
    /// response.
    func testContractMMAnchorDecodesEveryWireKey() throws {
        let it: WebMMItem = try contractMMDecode(contractMMFullItemJSON)
        let a = try XCTUnwrap(it.anchor)
        XCTAssertEqual(a.repo, "kraken")
        XCTAssertEqual(a.path, "internal/jobs/mm.go")
        XCTAssertEqual(a.origPath, "internal/jobs/verify.go")
        XCTAssertEqual(a.startLine, 120)
        XCTAssertEqual(a.endLine, 138)
        XCTAssertEqual(a.anchorSHA, "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678")
        XCTAssertEqual(a.anchorKind, "origin-main")
        XCTAssertEqual(a.textSHA, "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b")
        XCTAssertEqual(a.checkedAt, 1_755_303_600_000)
        XCTAssertEqual(a.checkedSHA, "0badc0ffee0badc0ffee0badc0ffee0badc0ffee")
        XCTAssertEqual(a.checkError, "fatal: could not read Username")
        XCTAssertEqual(a.ambiguous, true)
        XCTAssertEqual(a.invalidSHA, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
        XCTAssertEqual(a.invalidPR, "https://github.com/car1zhang/kraken/pull/231")
    }

    /// Obligation (S5 `WebMMAnchor` row): "a minimal dirty-anchor payload
    /// (repo/path/lines/kind/text_sha only) decodes".
    ///
    /// This is the shape a mark taken in a dirty worktree actually produces —
    /// S3's code route sets `anchor_sha` empty for the dirty rung, and S1
    /// refuses the two together, so `anchor_kind == "dirty"` with no
    /// `anchor_sha` is not a degenerate case but the normal one. Every
    /// omitempty key being Optional is what lets it through.
    func testContractMMAnchorDecodesTheMinimalDirtyPayload() throws {
        let it: WebMMItem = try contractMMDecode(contractMMDirtyAnchorItemJSON)
        let a = try XCTUnwrap(it.anchor)
        XCTAssertEqual(a.repo, "monolith")
        XCTAssertEqual(a.path, "core/lib/util.py")
        XCTAssertEqual(a.startLine, 4)
        XCTAssertEqual(a.endLine, 4)
        XCTAssertEqual(a.anchorKind, "dirty")
        XCTAssertEqual(a.textSHA, "1a2b3c4d5e6f70819a2b3c4d5e6f70819a2b3c4d")
        XCTAssertNil(a.origPath)
        XCTAssertNil(a.anchorSHA)
        XCTAssertNil(a.checkedAt)
        XCTAssertNil(a.checkedSHA)
        XCTAssertNil(a.checkError)
        XCTAssertNil(a.invalidSHA)
        XCTAssertNil(a.invalidPR)
    }

    /// Obligation (S5 `WebMMItem` row): "`live` computed as `invalidatedAt ==
    /// nil`".
    ///
    /// Both arms, because `live` is the single predicate every surface in the
    /// slice branches on — the artifact toggle's pressed state, the pane's
    /// live/invalidated filter, whether ✕ drop is enabled, and `mmMatch`'s
    /// own filter.
    func testContractMMItemLiveTracksTheInvalidationStamp() throws {
        let live: WebMMItem = try contractMMDecode(contractMMLiveItemJSON)
        XCTAssertTrue(live.live)
        XCTAssertNil(live.invalidatedAt)
        XCTAssertNil(live.invalidReason)
        XCTAssertNil(live.invalidDetail)

        let dead: WebMMItem = try contractMMDecode(contractMMFullItemJSON)
        XCTAssertFalse(dead.live)
    }

    /// Obligation (S5 `WebMMItem` row): "`context` decodes "" and absent
    /// alike (wire sends "" — always-present per the join)".
    ///
    /// The strict answer asserted here is the one the sentence states: BOTH
    /// spellings decode without throwing, and neither costs the row any of
    /// its other fields. Whether "" normalizes to nil or stays `Optional("")`
    /// is a silence — the contract does not say, and no S5 surface is
    /// specified to distinguish them — so nothing here asserts it.
    func testContractMMItemDecodesPresentEmptyAndAbsentContextAlike() throws {
        let present: WebMMItem = try contractMMDecode(contractMMLiveItemJSON)
        XCTAssertEqual(present.id, 7)
        XCTAssertTrue((present.context ?? "").isEmpty)

        let absent: WebMMItem = try contractMMDecode("""
        {"id":7,"kind":"decision","source":"question","slug":"a-job","source_ref":"q3",
         "content":"c","created_at":1755200000000,"episode_id":880}
        """)
        XCTAssertEqual(absent.id, 7)
        XCTAssertTrue((absent.context ?? "").isEmpty)
    }

    /// Obligation (S5 `WebMMAnchor` row): "unknown keys ignored (Decodable
    /// default)".
    ///
    /// The slice ships with `apiContract` held at 6 precisely because every
    /// mm wire change is additive; an installed build that throws on a key
    /// the server grew would turn the next additive key into a breaking one.
    /// Asserted on both structs at once, since a nested throw fails the whole
    /// decode.
    func testContractMMDecodeIgnoresUnknownWireKeys() throws {
        let it: WebMMItem = try contractMMDecode("""
        {"id":11,"kind":"note","source":"manual","slug":"","source_ref":"",
         "content":"a note","context":"","created_at":1755000000000,"episode_id":5,
         "future_key":"whatever","another":{"nested":[1,2,3]},
         "anchor":{"repo":"kraken","path":"a.go","start_line":1,"end_line":1,
                   "anchor_kind":"head","text_sha":"aa","tomorrows_key":true}}
        """)
        XCTAssertEqual(it.id, 11)
        XCTAssertEqual(it.kind, "note")
        XCTAssertEqual(it.source, "manual")
        XCTAssertEqual(it.content, "a note")
        XCTAssertEqual(try XCTUnwrap(it.anchor).anchorKind, "head")
    }

    /// Obligation (S5 `WebMMItem` row): `Identifiable`.
    ///
    /// A SwiftUI list keyed on anything but the row id re-renders every row
    /// on every 30s poll; keyed on the id it does not. The `LibraryPayload`
    /// precedent pins the same thing the same way.
    func testContractMMItemIdentifiableIDIsTheRowID() throws {
        let page: MMPage = try contractMMDecode(contractMMPageJSON)
        XCTAssertEqual(page.items.map(\.id), [7, 42])
    }

    // MARK: - MMPage: the envelope

    /// Obligation (S5 `MMPage` row): "the GET /api/mm envelope",
    /// `{ let items: [WebMMItem] }`; and S3's `GET /api/mm` row: "all items
    /// newest first".
    ///
    /// Order is asserted as PRESERVED rather than re-derived: the server
    /// sorts (`created_at DESC, id DESC` per S1's `ListMMItems`) and the
    /// client's job is to not resort it. The fixture's two rows are already
    /// in server order, so an implementation that sorted by id ascending —
    /// the plausible mistake — would fail.
    func testContractMMPageDecodesItemsInServedOrder() throws {
        let page: MMPage = try contractMMDecode(contractMMPageJSON)
        XCTAssertEqual(page.items.count, 2)
        XCTAssertEqual(page.items[0].id, 7)
        XCTAssertEqual(page.items[0].kind, "decision")
        XCTAssertEqual(page.items[0].createdAt, 1_755_200_000_000)
        XCTAssertEqual(page.items[1].id, 42)
        XCTAssertEqual(page.items[1].kind, "reading")
        XCTAssertEqual(page.items[1].createdAt, 1_755_300_000_000)
    }

    // MARK: - mmMatch: the positive semantics

    /// Obligation (S5 `mmMatch` row): "given items + (slug, name), the LIVE
    /// `source == "artifact"` item matching slug + leading-slash-trimmed
    /// name".
    ///
    /// The happy path: the artifact screen asks "have I read this?" and gets
    /// back the row that says so. The fixture deliberately holds three other
    /// artifact items — a different slug, a different name, and both — so a
    /// matcher that compares only one half passes nothing by accident.
    func testContractMMMatchFindsTheLiveArtifactItemBySlugAndName() {
        let items = [
            contractMMItem(id: 1, slug: "b-job", sourceRef: "00-overview.md"),
            contractMMItem(id: 2, slug: "a-job", sourceRef: "10-architecture.md"),
            contractMMItem(id: 3, slug: "a-job", sourceRef: "00-overview.md"),
            contractMMItem(id: 4, slug: "b-job", sourceRef: "10-architecture.md"),
        ]
        XCTAssertEqual(mmMatch(items: items, slug: "a-job", name: "00-overview.md")?.id, 3)
        XCTAssertEqual(mmMatch(items: items, slug: "b-job", name: "10-architecture.md")?.id, 4)
    }

    /// Obligation (S5 `mmMatch` row): "leading-slash-trimmed name".
    ///
    /// The name reaches the matcher from a pager url whose path segment
    /// carries the slash and from a mark call that does not; both spellings
    /// name one artifact, and S3's `/api/mm/artifact` route stores
    /// `source_ref = name with leading / trimmed`, so the STORED side never
    /// carries one. The trim therefore belongs on the query.
    func testContractMMMatchTrimsALeadingSlashFromTheName() {
        let items = [contractMMItem(id: 3, slug: "a-job", sourceRef: "00-overview.md")]
        XCTAssertEqual(mmMatch(items: items, slug: "a-job", name: "/00-overview.md")?.id, 3)
        XCTAssertEqual(mmMatch(items: items, slug: "a-job", name: "00-overview.md")?.id, 3)
    }

    /// Obligation (S5 `mmMatch` row): "`home` deliberately not compared".
    ///
    /// On iOS the signature carries no `home` at all, so the assertable half
    /// is that the NAME is compared WHOLE and never split on `/` — a matcher
    /// that stripped a leading path segment to "ignore the home" would match
    /// a nested artifact by its basename, which is the failure the web
    /// sibling's own tests guard. The same artifact opened under `pluto` and
    /// under `cell` reaches this function as the identical (slug, name) pair
    /// and must find the identical row.
    func testContractMMMatchComparesTheWholeNestedName() {
        let items = [
            contractMMItem(id: 5, slug: "a-job", sourceRef: "icons/deep/diagram.svg"),
        ]
        XCTAssertEqual(mmMatch(items: items, slug: "a-job", name: "icons/deep/diagram.svg")?.id, 5)
        XCTAssertEqual(mmMatch(items: items, slug: "a-job", name: "/icons/deep/diagram.svg")?.id, 5)
    }

    /// Obligation (S5 `mmMatch` row): "an invalidated item never matches",
    /// and the cross-slice rule that invalidation is one-way and terminal.
    ///
    /// The live/dead pair share slug and name, which is the real state after
    /// carl unmarks an artifact and marks it again: the superseded row stays
    /// readable forever beside the new one. The toggle must read the LIVE
    /// row, and a matcher that returned the first hit in list order would
    /// return the dead one here.
    func testContractMMMatchTakesTheLiveRowOverAnInvalidatedTwin() {
        let items = [
            contractMMItem(id: 6, slug: "a-job", sourceRef: "00-overview.md",
                           invalidatedAt: 1_755_386_400_000),
            contractMMItem(id: 7, slug: "a-job", sourceRef: "00-overview.md"),
        ]
        XCTAssertEqual(mmMatch(items: items, slug: "a-job", name: "00-overview.md")?.id, 7)
    }

    /// Obligation (S5 `mmMatch` row): "the LIVE `source == "artifact"` item"
    /// — an invalidated row alone is no match at all.
    ///
    /// This is the state right after ✕ drop or after `MMVerifyPass` kills the
    /// mark on a byte change (Q3): the row is still in the poll's payload, and
    /// the button must go back to unpressed rather than stay lit off a dead
    /// row.
    func testContractMMMatchIgnoresAnInvalidatedRowWithNoLiveTwin() {
        let items = [
            contractMMItem(id: 8, slug: "a-job", sourceRef: "00-overview.md",
                           invalidatedAt: 1_755_386_400_000),
        ]
        XCTAssertNil(mmMatch(items: items, slug: "a-job", name: "00-overview.md"))
    }

    /// Obligation (S5 `mmMatch` row): `source == "artifact"`.
    ///
    /// The four other sources all coexist in one payload, and two of them —
    /// `artifact-element` and `question` — carry the SAME slug the artifact
    /// mark does, with an `artifact-element` row's `source_ref` being the
    /// trimmed artifact name verbatim (S3's element route). So a matcher that
    /// checked only slug + source_ref would light the whole-artifact toggle
    /// because carl once marked a table inside it, which is a different claim
    /// about what he has read.
    func testContractMMMatchIgnoresEverySourceButArtifact() {
        let name = "00-overview.md"
        for source in ["artifact-element", "question", "code", "agent-output", "manual"] {
            let items = [contractMMItem(id: 9, source: source, slug: "a-job", sourceRef: name)]
            XCTAssertNil(mmMatch(items: items, slug: "a-job", name: name),
                         "source \(source) must not match the whole-artifact toggle")
        }
        let items = [contractMMItem(id: 10, source: "artifact", slug: "a-job", sourceRef: name)]
        XCTAssertEqual(mmMatch(items: items, slug: "a-job", name: name)?.id, 10)
    }
}
