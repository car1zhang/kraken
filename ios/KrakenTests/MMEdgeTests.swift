import XCTest
import Foundation
@testable import Kraken

// MMEdgeTests — the EDGE dimension of artifacts/50-contract.md's slice S5 (the
// iOS mental-model parity slice, AMENDED AT GATE R2). Authored blind from the
// frozen contract alone, before the implementation existed, so every assertion
// below traces to a sentence in that file rather than to code — where the
// contract is silent this file either asserts nothing or asserts the strictest
// defensible reading and says so in a comment.
//
// The happy paths and the stated invariants belong to the other blind author
// (MMContractTests); what lives here is the second half of each rule: the
// MINIMAL wire object with every optional absent, the key that is present as
// `null`, the `invalidated_at` that flips `live`, the empty item list, the
// source that is not `artifact`, the dead row that must not win over the live
// one, and the Int64 a Double cannot hold.
//
// Two silences in the S5 rows are load-bearing enough to name here, since both
// shape what this file could assert at all:
//
// 1. `mmMatch`'s SWIFT SIGNATURE is not frozen. The contract fixes the NAME
//    (`mmMatch`), the inputs ("given items + (slug, name)"), the result
//    ("nil on no match"), and the rule (live + source=="artifact", slug plus
//    leading-slash-trimmed name, `home` deliberately not compared) — but not
//    the argument labels, and Swift has no way to call a function whose labels
//    are unknown. The strictest reading of "given items + (slug, name)" is a
//    fully-labelled free function in that order, `mmMatch(items:slug:name:)`,
//    and that is what this file assumes. It is called through ONE private
//    shim, `edgeMMMatch`, so if the implementer spelled the labels differently
//    the whole disagreement is a single line rather than twenty.
//
// 2. `home` is not a parameter of the iOS matcher at all — the input is
//    already `(slug, name)`. "home deliberately not compared" is therefore
//    structurally true here rather than testable, and nothing below asserts
//    it; the LibraryEdgeTests precedent that does assert it is testing a
//    matcher whose input is a URL.
//
// Every file-scope identifier is `private` and prefixed `edgeMM` — this test
// target's namespace is shared with the sibling blind file.

// MARK: - Fixtures & helpers

private func edgeMMDecode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(json.utf8))
}

/// A Swift string as a JSON string literal, escaping done by Foundation rather
/// than by hand: these fixtures carry slashes, quotes and newlines, and an
/// escaping bug in a test helper reads exactly like a decode bug in the model.
private func edgeMMJSON(_ s: String) -> String {
    guard
        let data = try? JSONSerialization.data(withJSONObject: [s], options: []),
        var out = String(data: data, encoding: .utf8),
        out.count >= 2
    else { return "\"\"" }
    out.removeFirst()  // the wrapping [
    out.removeLast()   // the wrapping ]
    return out
}

/// One `anchor` object, defaulting to the MINIMAL DIRTY shape: `repo`, `path`,
/// the two line numbers, `anchor_kind` and `text_sha`, and nothing else.
///
/// That is not an invented shape — it is what the wire actually serves for a
/// dirty anchor. The S1 row makes `AnchorKind == "dirty"` ⇔ `AnchorSHA == ""`,
/// and the `webMMItem` paragraph puts `omitempty` on every empty anchor field,
/// so an anchor taken off a still-uncommitted buffer omits `orig_path`,
/// `anchor_sha`, `checked_at`, `checked_sha`, `check_error`, `ambiguous`,
/// `invalid_sha` and `invalid_pr` all at once.
private func edgeMMAnchorJSON(
    repo: String = "kraken",
    path: String = "internal/store/mm.go",
    origPath: String? = nil,
    startLine: Int = 10,
    endLine: Int = 12,
    anchorSHA: String? = nil,
    anchorKind: String = "dirty",
    textSHA: String = "3f1a9c",
    checkedAt: Int64? = nil,
    checkedSHA: String? = nil,
    checkError: String? = nil,
    ambiguous: Bool? = nil,
    invalidSHA: String? = nil,
    invalidPR: String? = nil,
    extraKeys: String = ""
) -> String {
    var parts: [String] = [
        "\"repo\":\(edgeMMJSON(repo))",
        "\"path\":\(edgeMMJSON(path))",
        "\"start_line\":\(startLine)",
        "\"end_line\":\(endLine)",
        "\"anchor_kind\":\(edgeMMJSON(anchorKind))",
        "\"text_sha\":\(edgeMMJSON(textSHA))",
    ]
    if let origPath { parts.append("\"orig_path\":\(edgeMMJSON(origPath))") }
    if let anchorSHA { parts.append("\"anchor_sha\":\(edgeMMJSON(anchorSHA))") }
    if let checkedAt { parts.append("\"checked_at\":\(checkedAt)") }
    if let checkedSHA { parts.append("\"checked_sha\":\(edgeMMJSON(checkedSHA))") }
    if let checkError { parts.append("\"check_error\":\(edgeMMJSON(checkError))") }
    if let ambiguous { parts.append("\"ambiguous\":\(ambiguous)") }
    if let invalidSHA { parts.append("\"invalid_sha\":\(edgeMMJSON(invalidSHA))") }
    if let invalidPR { parts.append("\"invalid_pr\":\(edgeMMJSON(invalidPR))") }
    if !extraKeys.isEmpty { parts.append(extraKeys) }
    return "{" + parts.joined(separator: ",") + "}"
}

/// One `webMMItem` object. Defaults are the LIVE artifact-mark row: the shape
/// `mmMatch` is meant to find.
///
/// `content` defaults to `sourceRef` because that is what the server stores for
/// `source == "artifact"` ("source_ref = name with leading `/` trimmed,
/// content = source_ref"). Keeping the fixture faithful is deliberate: with the
/// two fields equal on every artifact row, no test here can — or should — pin
/// WHICH of them the matcher compares, and a divergent fixture would invent a
/// wire shape that cannot occur.
private func edgeMMItemJSON(
    id: Int64 = 1,
    kind: String = "reading",
    source: String = "artifact",
    slug: String = "a-job",
    sourceRef: String = "00-overview.md",
    content: String? = nil,
    context: String? = "",
    createdAt: Int64 = 1_770_000_000_000,
    episodeID: Int64 = 9,
    invalidatedAt: Int64? = nil,
    invalidReason: String? = nil,
    invalidDetail: String? = nil,
    anchor: String? = nil,
    extraKeys: String = ""
) -> String {
    var parts: [String] = [
        "\"id\":\(id)",
        "\"kind\":\(edgeMMJSON(kind))",
        "\"source\":\(edgeMMJSON(source))",
        "\"slug\":\(edgeMMJSON(slug))",
        "\"source_ref\":\(edgeMMJSON(sourceRef))",
        "\"content\":\(edgeMMJSON(content ?? sourceRef))",
        "\"created_at\":\(createdAt)",
        "\"episode_id\":\(episodeID)",
    ]
    if let context { parts.append("\"context\":\(edgeMMJSON(context))") }
    if let invalidatedAt { parts.append("\"invalidated_at\":\(invalidatedAt)") }
    if let invalidReason { parts.append("\"invalid_reason\":\(edgeMMJSON(invalidReason))") }
    if let invalidDetail { parts.append("\"invalid_detail\":\(edgeMMJSON(invalidDetail))") }
    if let anchor { parts.append("\"anchor\":\(anchor)") }
    if !extraKeys.isEmpty { parts.append(extraKeys) }
    return "{" + parts.joined(separator: ",") + "}"
}

private func edgeMMItem(_ json: String) throws -> WebMMItem {
    try edgeMMDecode(json, as: WebMMItem.self)
}

private func edgeMMItems(_ jsons: [String]) throws -> [WebMMItem] {
    try edgeMMDecode("[" + jsons.joined(separator: ",") + "]", as: [WebMMItem].self)
}

/// The ONE call site of `mmMatch` in this file — see silence (1) in the header.
/// If the implementer chose different argument labels, this shim is the entire
/// fix; every test below calls it positionally.
private func edgeMMMatch(_ items: [WebMMItem], _ slug: String, _ name: String) -> WebMMItem? {
    mmMatch(items: items, slug: slug, name: name)
}

// MARK: - WebMMAnchor: the minimal object and the absent optionals

final class EdgeMMAnchorDecodeTests: XCTestCase {

    /// Obligation: "a minimal dirty-anchor payload (repo/path/lines/kind/
    /// text_sha only) decodes".
    ///
    /// This is the shape every `k mm mark` of an uncommitted buffer produces,
    /// so a model that required any of the omitted keys would fail to decode
    /// the FIRST anchor a new user ever creates — and, because `anchor` hangs
    /// off the item, would take that whole item down with it.
    func testEdgeMMAnchorMinimalDirtyPayloadDecodes() throws {
        let anchor: WebMMAnchor = try edgeMMDecode(edgeMMAnchorJSON())
        XCTAssertEqual(anchor.repo, "kraken")
        XCTAssertEqual(anchor.path, "internal/store/mm.go")
        XCTAssertEqual(anchor.startLine, 10)
        XCTAssertEqual(anchor.endLine, 12)
        XCTAssertEqual(anchor.anchorKind, "dirty")
        XCTAssertEqual(anchor.textSHA, "3f1a9c")
        XCTAssertNil(anchor.origPath)
        XCTAssertNil(anchor.anchorSHA)
        XCTAssertNil(anchor.checkedAt)
        XCTAssertNil(anchor.checkedSHA)
        XCTAssertNil(anchor.checkError)
        XCTAssertNil(anchor.ambiguous)
        XCTAssertNil(anchor.invalidSHA)
        XCTAssertNil(anchor.invalidPR)
    }

    /// `ambiguous` is typed `Bool?`, not `Bool`, and the wire omits it when
    /// false. Absent must therefore read as nil rather than as a decoded
    /// `false`: the two are the same thing to a renderer, but a non-Optional
    /// `Bool` would THROW on the omission and lose the anchor entirely.
    func testEdgeMMAnchorAmbiguousDistinguishesAbsentFromBothBooleans() throws {
        let absent: WebMMAnchor = try edgeMMDecode(edgeMMAnchorJSON())
        XCTAssertNil(absent.ambiguous)
        let yes: WebMMAnchor = try edgeMMDecode(edgeMMAnchorJSON(ambiguous: true))
        XCTAssertEqual(yes.ambiguous, true)
        // The server omits `false` (omitempty), but an explicit false must not
        // be mistaken for absence either.
        let no: WebMMAnchor = try edgeMMDecode(edgeMMAnchorJSON(ambiguous: false))
        XCTAssertEqual(no.ambiguous, false)
    }

    /// Swift's synthesised Decodable treats a missing key and an explicit
    /// `null` identically for an Optional property. The wire uses omitempty
    /// rather than null today, so this pins the tolerance rather than the
    /// current server: an anchor field that ever starts arriving as `null`
    /// must not take the row down.
    func testEdgeMMAnchorExplicitNullsDecodeAsNil() throws {
        let json = """
        {"repo":"kraken","path":"a.go","start_line":1,"end_line":1,"anchor_kind":"dirty","text_sha":"aa",\
        "orig_path":null,"anchor_sha":null,"checked_at":null,"checked_sha":null,"check_error":null,\
        "ambiguous":null,"invalid_sha":null,"invalid_pr":null}
        """
        let anchor: WebMMAnchor = try edgeMMDecode(json, as: WebMMAnchor.self)
        XCTAssertNil(anchor.origPath)
        XCTAssertNil(anchor.anchorSHA)
        XCTAssertNil(anchor.checkedAt)
        XCTAssertNil(anchor.ambiguous)
        XCTAssertNil(anchor.invalidPR)
    }

    /// Obligation: "unknown keys ignored (Decodable default)". The wire rule
    /// for this whole change is additive-only (`apiContract` stays 6), so an
    /// installed build must keep decoding when the server grows a key.
    func testEdgeMMAnchorUnknownKeysAreIgnored() throws {
        let anchor: WebMMAnchor = try edgeMMDecode(
            edgeMMAnchorJSON(extraKeys: "\"lang\":\"go\",\"future_field\":{\"nested\":[1,2]}")
        )
        XCTAssertEqual(anchor.anchorKind, "dirty")
        XCTAssertEqual(anchor.textSHA, "3f1a9c")
    }

    /// Obligation: "CodingKeys map the wire's snake_case exactly (`orig_path`,
    /// `anchor_sha`, `text_sha`, …)". A missed mapping on a NON-Optional
    /// (`anchor_kind`, `text_sha`) throws and is loud; a missed mapping on an
    /// Optional is silent and permanent — every anchor would render as if the
    /// field were empty. So each snake_case key is read back by value.
    func testEdgeMMAnchorReadsEverySnakeCaseKey() throws {
        let json = edgeMMAnchorJSON(
            origPath: "internal/store/old.go",
            anchorSHA: "deadbeef",
            anchorKind: "origin-main",
            textSHA: "cafe01",
            checkedAt: 1_770_000_111_000,
            checkedSHA: "feed77",
            checkError: "fatal: not a git repository",
            invalidSHA: "b0b0b0",
            invalidPR: "https://github.com/car1zhang/kraken/pull/231"
        )
        let anchor: WebMMAnchor = try edgeMMDecode(json)
        XCTAssertEqual(anchor.origPath, "internal/store/old.go")
        XCTAssertEqual(anchor.anchorSHA, "deadbeef")
        XCTAssertEqual(anchor.checkedAt, 1_770_000_111_000)
        XCTAssertEqual(anchor.checkedSHA, "feed77")
        XCTAssertEqual(anchor.checkError, "fatal: not a git repository")
        XCTAssertEqual(anchor.invalidSHA, "b0b0b0")
        XCTAssertEqual(anchor.invalidPR, "https://github.com/car1zhang/kraken/pull/231")
    }

    /// The line pair is `Int`, which on every device this ships to is 64-bit.
    /// A single-line mark (`start == end`, the `k mm mark` default when the
    /// selection is empty) and a file far past Int32 both have to survive: an
    /// `Int32` typo would decode most anchors and throw on one.
    func testEdgeMMAnchorLineNumberBoundaries() throws {
        let single: WebMMAnchor = try edgeMMDecode(edgeMMAnchorJSON(startLine: 1, endLine: 1))
        XCTAssertEqual(single.startLine, 1)
        XCTAssertEqual(single.endLine, 1)
        let huge: WebMMAnchor = try edgeMMDecode(
            edgeMMAnchorJSON(startLine: 2_147_483_648, endLine: 2_147_483_649)
        )
        XCTAssertEqual(huge.startLine, 2_147_483_648)
        XCTAssertEqual(huge.endLine, 2_147_483_649)
    }

    /// `repo`, `path`, `anchor_kind` and `text_sha` are non-Optional in the
    /// frozen struct, and the S1 validators make all four non-empty on every
    /// stored anchor — so an object missing one is a drifted server, not a
    /// dirty anchor. It must FAIL rather than decode with a defaulted "": an
    /// anchor silently defaulted to the empty repo renders a code reading
    /// pointing at nothing.
    func testEdgeMMAnchorMissingRequiredKeyFailsToDecode() {
        let bodies = [
            #"{"path":"a.go","start_line":1,"end_line":1,"anchor_kind":"dirty","text_sha":"aa"}"#,
            #"{"repo":"kraken","start_line":1,"end_line":1,"anchor_kind":"dirty","text_sha":"aa"}"#,
            #"{"repo":"kraken","path":"a.go","end_line":1,"anchor_kind":"dirty","text_sha":"aa"}"#,
            #"{"repo":"kraken","path":"a.go","start_line":1,"anchor_kind":"dirty","text_sha":"aa"}"#,
            #"{"repo":"kraken","path":"a.go","start_line":1,"end_line":1,"text_sha":"aa"}"#,
            #"{"repo":"kraken","path":"a.go","start_line":1,"end_line":1,"anchor_kind":"dirty"}"#,
            // camelCase where the wire is snake_case: the mapping, not the key.
            #"{"repo":"kraken","path":"a.go","startLine":1,"endLine":1,"anchor_kind":"dirty","text_sha":"aa"}"#,
        ]
        for body in bodies {
            XCTAssertThrowsError(
                try edgeMMDecode(body, as: WebMMAnchor.self),
                "anchor should not decode: \(body)"
            )
        }
    }
}

// MARK: - WebMMItem: absence shapes and the live computation

final class EdgeMMItemDecodeTests: XCTestCase {

    /// The ordinary live row: no `anchor` (only `source == "code"` items carry
    /// one) and no `invalid_*` keys at all, since all four are omitempty and
    /// absence is the wire's ONLY spelling of "live".
    func testEdgeMMItemWithNoAnchorAndNoInvalidKeysIsLive() throws {
        let item = try edgeMMItem(edgeMMItemJSON())
        XCTAssertNil(item.anchor)
        XCTAssertNil(item.invalidatedAt)
        XCTAssertNil(item.invalidReason)
        XCTAssertNil(item.invalidDetail)
        XCTAssertTrue(item.live)
    }

    /// An explicit `null` anchor is the same nil as an absent one — nothing on
    /// the wire sends it today, and a row that failed to decode over it would
    /// vanish from the pane rather than render without its anchor.
    func testEdgeMMItemExplicitNullAnchorAndInvalidKeysDecodeAsLive() throws {
        let json = """
        {"id":3,"kind":"note","source":"manual","slug":"","source_ref":"","content":"a thought",\
        "context":"","created_at":1770000000000,"episode_id":4,"anchor":null,"invalidated_at":null,\
        "invalid_reason":null,"invalid_detail":null}
        """
        let item = try edgeMMItem(json)
        XCTAssertNil(item.anchor)
        XCTAssertNil(item.invalidatedAt)
        XCTAssertTrue(item.live)
    }

    /// Obligation: "`live` computed as `invalidatedAt == nil`". The presence of
    /// the key is the whole of the flip, and this is the assertion that a
    /// stamped row is not still being offered to future agents as something
    /// carl knows.
    func testEdgeMMItemInvalidatedAtPresentFlipsLiveToFalse() throws {
        let item = try edgeMMItem(
            edgeMMItemJSON(invalidatedAt: 1_770_000_500_000, invalidReason: "content-changed")
        )
        XCTAssertEqual(item.invalidatedAt, 1_770_000_500_000)
        XCTAssertEqual(item.invalidReason, "content-changed")
        XCTAssertFalse(item.live)
    }

    /// The strict spelling of the same rule, at its sharpest edge. The wire
    /// never sends `"invalidated_at":0` — the key is omitempty, so a zero is
    /// omitted — but the frozen computation is `invalidatedAt == nil` and not
    /// `(invalidatedAt ?? 0) == 0`, and the two differ on exactly this body.
    /// Asserted as written because the contract wrote it: a computation that
    /// re-derives liveness from the VALUE has a second opinion about what
    /// "live" means, which is the drift the frozen spelling exists to prevent.
    func testEdgeMMItemPresentZeroInvalidatedAtIsNotLive() throws {
        let item = try edgeMMItem(edgeMMItemJSON(invalidatedAt: 0))
        XCTAssertNotNil(item.invalidatedAt)
        XCTAssertFalse(item.live)
    }

    /// Obligation: "context decodes "" and absent alike (wire sends "" —
    /// always-present per the join)".
    ///
    /// "Alike" is asserted as the OBSERVABLE it names: neither shape throws,
    /// and both read as no-context to anything that renders them. Whether the
    /// implementer keeps "" as "" or normalises it to nil is a silence — the
    /// contract types the property `String?` and states only that the two
    /// decode alike — so this asserts the property both readings share rather
    /// than picking one and calling it frozen.
    func testEdgeMMItemContextAbsentAndEmptyDecodeAlike() throws {
        let absent = try edgeMMItem(edgeMMItemJSON(context: nil))
        let empty = try edgeMMItem(edgeMMItemJSON(context: ""))
        XCTAssertTrue((absent.context ?? "").isEmpty)
        XCTAssertTrue((empty.context ?? "").isEmpty)
        // And a real context survives verbatim — the half of the pair that
        // proves the above is emptiness rather than the field being dropped.
        let real = try edgeMMItem(edgeMMItemJSON(context: "why this matters"))
        XCTAssertEqual(real.context, "why this matters")
    }

    /// The full death record, which is the only shape where the `invalid_*`
    /// trio and the anchor's own invalid meta are all present at once. The
    /// pane renders reason + frozen content off exactly this row, so each part
    /// is read back by value.
    func testEdgeMMItemInvalidatedCodeRowCarriesReasonAndAnchorMeta() throws {
        let anchor = edgeMMAnchorJSON(
            anchorSHA: "1111aa",
            anchorKind: "origin-main",
            checkedAt: 1_770_000_400_000,
            checkedSHA: "2222bb",
            ambiguous: true,
            invalidSHA: "3333cc",
            invalidPR: "https://github.com/car1zhang/kraken/pull/99"
        )
        let item = try edgeMMItem(edgeMMItemJSON(
            id: 42,
            source: "code",
            sourceRef: "kraken/internal/store/mm.go:10-12",
            content: "func (s *Store) AddMMItem(",
            invalidatedAt: 1_770_000_600_000,
            invalidReason: "file-deleted",
            invalidDetail: "path gone at 3333cc",
            anchor: anchor
        ))
        XCTAssertFalse(item.live)
        XCTAssertEqual(item.invalidReason, "file-deleted")
        XCTAssertEqual(item.invalidDetail, "path gone at 3333cc")
        XCTAssertEqual(item.anchor?.invalidSHA, "3333cc")
        XCTAssertEqual(item.anchor?.invalidPR, "https://github.com/car1zhang/kraken/pull/99")
        XCTAssertEqual(item.anchor?.ambiguous, true)
        // The frozen text is kept for the knew-vs-now view — an invalidation
        // must not blank it.
        XCTAssertEqual(item.content, "func (s *Store) AddMMItem(")
    }

    /// Same additive-wire rule as the anchor's: the server may grow keys
    /// without an `apiContract` bump, and an installed build must survive it.
    func testEdgeMMItemUnknownKeysAreIgnored() throws {
        let item = try edgeMMItem(edgeMMItemJSON(
            extraKeys: "\"snapshot_ext\":\".md\",\"job_id\":313,\"future\":{\"a\":[1]}"
        ))
        XCTAssertEqual(item.id, 1)
        XCTAssertTrue(item.live)
    }

    /// `id`, `created_at`, `episode_id` and `invalidated_at` are all `Int64`,
    /// and all four are values a Double cannot hold exactly. An id routed
    /// through a Double and back is off by one, which for `POST /api/mm/remove`
    /// is a different row invalidated — one-way and terminal.
    func testEdgeMMItemInt64FieldsKeepFullPrecision() throws {
        let item = try edgeMMItem(edgeMMItemJSON(
            id: 9_007_199_254_740_993,
            createdAt: 9_007_199_254_740_993,
            episodeID: 9_007_199_254_740_993,
            invalidatedAt: 9_007_199_254_740_993
        ))
        XCTAssertEqual(item.id, 9_007_199_254_740_993)
        XCTAssertEqual(item.createdAt, 9_007_199_254_740_993)
        XCTAssertEqual(item.episodeId, 9_007_199_254_740_993)
        XCTAssertEqual(item.invalidatedAt, 9_007_199_254_740_993)
        XCTAssertFalse(item.live)
    }

    /// The extreme of the same field, plus the anchor's own `checked_at`: the
    /// upper bound of Int64 must decode rather than overflow.
    func testEdgeMMItemInt64MaxDecodes() throws {
        let item = try edgeMMItem(edgeMMItemJSON(
            id: Int64.max,
            createdAt: Int64.max,
            episodeID: Int64.max,
            anchor: edgeMMAnchorJSON(checkedAt: Int64.max)
        ))
        XCTAssertEqual(item.id, Int64.max)
        XCTAssertEqual(item.createdAt, Int64.max)
        XCTAssertEqual(item.episodeId, Int64.max)
        XCTAssertEqual(item.anchor?.checkedAt, Int64.max)
    }

    /// A note taken through `POST /api/mm/note` has `source == "manual"` and no
    /// job behind it, so the server has no slug or source_ref to put on the
    /// row. Both fields are non-Optional `String`, so they arrive as "" — and
    /// the pane must render that row rather than fail on it. ("pane renders
    /// items whose jobs no longer exist" is the same requirement from the
    /// other direction.)
    func testEdgeMMItemEmptySlugAndSourceRefDecode() throws {
        let item = try edgeMMItem(edgeMMItemJSON(
            id: 8, kind: "note", source: "manual", slug: "", sourceRef: "", content: "a standalone thought"
        ))
        XCTAssertEqual(item.slug, "")
        XCTAssertEqual(item.sourceRef, "")
        XCTAssertEqual(item.content, "a standalone thought")
        XCTAssertTrue(item.live)
    }

    /// Multi-line content with markdown structure — the `artifact-element`
    /// row a marked table produces, which the pane renders through
    /// `MarkdownText`. The pipes, newlines and backslashes have to survive the
    /// decode byte for byte or the table renders as prose.
    func testEdgeMMItemMultilineMarkdownContentSurvivesVerbatim() throws {
        let table = "| a | b |\n|---|---|\n| 1 | 2 |\n\n- item \"one\"\n- item \\ two"
        let item = try edgeMMItem(edgeMMItemJSON(
            id: 11, source: "artifact-element", sourceRef: "50-contract.md", content: table
        ))
        XCTAssertEqual(item.content, table)
    }

    /// Every non-omitempty key is a non-Optional `let`, so a body missing one
    /// is a decode FAILURE rather than a row with a defaulted value. A
    /// defaulted `id` is a ✕ that drops something else; a defaulted `source`
    /// is a row `mmMatch` would then compare against "".
    func testEdgeMMItemMissingRequiredKeyFailsToDecode() {
        let full = edgeMMItemJSON()
        for key in ["id", "kind", "source", "slug", "source_ref", "content", "created_at", "episode_id"] {
            let body = edgeMMDropKey(full, key)
            XCTAssertNotEqual(body, full, "fixture should contain \(key)")
            XCTAssertThrowsError(
                try edgeMMDecode(body, as: WebMMItem.self),
                "item should not decode without \(key)"
            )
        }
        // camelCase where the wire is snake_case: the mapping, not the key.
        XCTAssertThrowsError(try edgeMMDecode(
            #"{"id":1,"kind":"reading","source":"artifact","slug":"a","sourceRef":"x.md","content":"x.md","created_at":1,"episode_id":1}"#,
            as: WebMMItem.self
        ))
        XCTAssertThrowsError(try edgeMMDecode(
            #"{"id":1,"kind":"reading","source":"artifact","slug":"a","source_ref":"x.md","content":"x.md","createdAt":1,"episode_id":1}"#,
            as: WebMMItem.self
        ))
        XCTAssertThrowsError(try edgeMMDecode(
            #"{"id":1,"kind":"reading","source":"artifact","slug":"a","source_ref":"x.md","content":"x.md","created_at":1,"episodeId":1}"#,
            as: WebMMItem.self
        ))
    }

    /// `anchor` is Optional, but a PRESENT anchor that is malformed is not the
    /// same as an absent one: Swift throws for the whole item. That is the
    /// wanted behaviour — a code reading rendered without the anchor it is
    /// pinned to would claim a range it never verified.
    func testEdgeMMItemMalformedPresentAnchorFailsTheWholeItem() {
        let body = edgeMMItemJSON(anchor: #"{"repo":"kraken"}"#)
        XCTAssertThrowsError(try edgeMMDecode(body, as: WebMMItem.self))
        let notAnObject = edgeMMItemJSON(anchor: #""dirty""#)
        XCTAssertThrowsError(try edgeMMDecode(notAnObject, as: WebMMItem.self))
    }
}

/// Removes one top-level `"key":value` pair from a flat fixture object. Only
/// ever fed the fixtures above, whose values are numbers or escaped strings
/// with no nested objects before the key being dropped — a general JSON editor
/// is not needed and would be one more thing to get wrong.
private func edgeMMDropKey(_ object: String, _ key: String) -> String {
    guard object.hasPrefix("{"), object.hasSuffix("}") else { return object }
    let body = object.dropFirst().dropLast()
    var depth = 0
    var inString = false
    var escaped = false
    var parts: [String] = []
    var current = ""
    for ch in body {
        if inString {
            current.append(ch)
            if escaped { escaped = false } else if ch == "\\" { escaped = true } else if ch == "\"" { inString = false }
            continue
        }
        switch ch {
        case "\"": inString = true; current.append(ch)
        case "{", "[": depth += 1; current.append(ch)
        case "}", "]": depth -= 1; current.append(ch)
        case "," where depth == 0: parts.append(current); current = ""
        default: current.append(ch)
        }
    }
    if !current.isEmpty { parts.append(current) }
    let kept = parts.filter { !$0.hasPrefix("\"\(key)\":") }
    return "{" + kept.joined(separator: ",") + "}"
}

// MARK: - MMPage: the envelope

final class EdgeMMPageDecodeTests: XCTestCase {

    /// `GET /api/mm` guarantees "array never null", and the empty array is an
    /// ordinary state — every install before carl's first mark. The pane's
    /// empty state is rendered from it, so a throw here would show a fetch
    /// error for a working server.
    func testEdgeMMPageEmptyItemsDecodesToAnEmptyArray() throws {
        let page: MMPage = try edgeMMDecode(#"{"items":[]}"#)
        XCTAssertTrue(page.items.isEmpty)
    }

    /// A mixed page: live and invalidated rows, one of each source shape the
    /// wire can carry, in served order. Nothing here re-sorts (the LibraryView
    /// rule) — the assertion is that the served ORDER survives the decode, so
    /// "newest first" stays the server's single opinion.
    func testEdgeMMPageKeepsServedOrderAcrossMixedRows() throws {
        let page: MMPage = try edgeMMDecode("""
        {"items":[\(edgeMMItemJSON(id: 30, source: "code", sourceRef: "kraken/a.go:1-2", anchor: edgeMMAnchorJSON())),\
        \(edgeMMItemJSON(id: 20, invalidatedAt: 1_770_000_900_000, invalidReason: "content-changed")),\
        \(edgeMMItemJSON(id: 10, kind: "note", source: "manual", slug: "", sourceRef: "", content: "n"))]}
        """, as: MMPage.self)
        XCTAssertEqual(page.items.map(\.id), [30, 20, 10])
        XCTAssertEqual(page.items.map(\.live), [true, false, true])
        XCTAssertNotNil(page.items[0].anchor)
        XCTAssertNil(page.items[1].anchor)
    }

    /// `items` is a non-Optional `let`, so a body that has lost it — a drifted
    /// or older server — is an ERROR the screen reports, never an empty list.
    /// A defaulted `[]` would render "nothing marked", which is a lie told in
    /// exactly the state carl would most want to notice.
    func testEdgeMMPageMissingOrNullItemsFailsToDecode() {
        for body in [#"{}"#, #"{"items":null}"#, #"{"item":[]}"#, #"{"items":{}}"#] {
            XCTAssertThrowsError(
                try edgeMMDecode(body, as: MMPage.self),
                "page should not decode: \(body)"
            )
        }
    }

    /// The envelope is additive too: `GET /api/mm` may grow a sibling key
    /// (a count, a cursor) without a contract bump.
    func testEdgeMMPageUnknownEnvelopeKeysAreIgnored() throws {
        let page: MMPage = try edgeMMDecode("""
        {"items":[\(edgeMMItemJSON())],"total":1,"live":1}
        """, as: MMPage.self)
        XCTAssertEqual(page.items.count, 1)
    }

    /// One malformed row is not a degraded page: `[WebMMItem]` is all-or-
    /// nothing, so the whole fetch fails and the screen says so, rather than
    /// silently showing a mental model with a hole in it.
    func testEdgeMMPageOneUndecodableRowFailsTheWholePage() {
        let body = "{\"items\":[\(edgeMMItemJSON()),{\"id\":2}]}"
        XCTAssertThrowsError(try edgeMMDecode(body, as: MMPage.self))
    }
}

// MARK: - mmMatch: the nil arms

final class EdgeMMMatchTests: XCTestCase {

    /// The empty mental model: the state every install is in until carl's first
    /// ✓, and the one the artifact screen's toggle is decided against on every
    /// artifact opened before then.
    func testEdgeMMMatchEmptyItemListNeverMatches() {
        XCTAssertNil(edgeMMMatch([], "a-job", "00-overview.md"))
        XCTAssertNil(edgeMMMatch([], "", ""))
    }

    /// Obligation: "an invalidated item never matches".
    ///
    /// This is the load-bearing negative of the whole toggle. `MMVerifyPass`
    /// kills an artifact mark on ANY byte change (Q3), and the artifact carl is
    /// looking at right now is the CHANGED one — a matcher that still found the
    /// dead row would render ✓ pressed and offer `dropMM` on a row that is
    /// already terminal, telling carl he still knows something the server has
    /// recorded that he does not.
    func testEdgeMMMatchInvalidatedItemNeverMatches() throws {
        let items = try edgeMMItems([
            edgeMMItemJSON(id: 1, invalidatedAt: 1_770_000_500_000, invalidReason: "content-changed"),
        ])
        XCTAssertNil(edgeMMMatch(items, "a-job", "00-overview.md"))
        // …and with a leading slash on the query too, so the trim cannot smuggle
        // it back in by taking a different branch.
        XCTAssertNil(edgeMMMatch(items, "a-job", "/00-overview.md"))
    }

    /// Every invalidated: the ordinary state of a long-lived job whose
    /// artifacts have been rewritten a few times. The pane still lists those
    /// rows; the toggle must still read as unmarked.
    func testEdgeMMMatchAllRowsInvalidatedReturnsNil() throws {
        let items = try edgeMMItems([
            edgeMMItemJSON(id: 1, invalidatedAt: 100, invalidReason: "content-changed"),
            edgeMMItemJSON(id: 2, invalidatedAt: 200, invalidReason: "superseded"),
            edgeMMItemJSON(id: 3, invalidatedAt: 300, invalidReason: "removed"),
        ])
        XCTAssertNil(edgeMMMatch(items, "a-job", "00-overview.md"))
    }

    /// Obligation: the LIVE `source == "artifact"` item. The other five sources
    /// can carry the same slug and, for `artifact-element`, literally the same
    /// `source_ref` — an element mark IS a mark on a span of this very
    /// artifact. Matching one would make selecting a paragraph light up the
    /// whole-artifact ✓, and then a press would drop the element mark.
    func testEdgeMMMatchOtherSourcesNeverMatch() throws {
        for source in ["artifact-element", "code", "question", "agent-output", "manual"] {
            let items = try edgeMMItems([edgeMMItemJSON(id: 1, source: source)])
            XCTAssertNil(
                edgeMMMatch(items, "a-job", "00-overview.md"),
                "source \(source) should not match the whole-artifact toggle"
            )
        }
    }

    /// Both halves of the pair come from the same screen, so a one-sided
    /// comparison would light up every artifact of a job that has any mark at
    /// all — or the same filename across every job on the board.
    func testEdgeMMMatchHalfAPairIsNotAMatch() throws {
        let items = try edgeMMItems([edgeMMItemJSON(id: 1, slug: "a-job", sourceRef: "00-overview.md")])
        XCTAssertNil(edgeMMMatch(items, "a-job", "90-build-report.md"))
        XCTAssertNil(edgeMMMatch(items, "other-job", "00-overview.md"))
        XCTAssertEqual(edgeMMMatch(items, "a-job", "00-overview.md")?.id, 1)
    }

    /// "exact" is exact. Slugs and artifact names are both case-sensitive
    /// server-side, so a case-folded match would claim a mark that `dropMM`
    /// then addresses correctly — dropping a row for an artifact carl never
    /// marked.
    func testEdgeMMMatchIsCaseSensitive() throws {
        let items = try edgeMMItems([edgeMMItemJSON(id: 1)])
        XCTAssertNil(edgeMMMatch(items, "A-Job", "00-overview.md"))
        XCTAssertNil(edgeMMMatch(items, "a-job", "00-OVERVIEW.md"))
    }

    /// A note row carries `slug == ""` and `source_ref == ""` (source
    /// `manual`), so an empty query pair is not a degenerate input the matcher
    /// can shrug at — it is a pair that would MATCH such a row if the source
    /// gate were missing. Nothing on the artifact screen ever asks with empty
    /// strings, and nothing must come back if it does.
    func testEdgeMMMatchEmptyQueryStringsNeverMatch() throws {
        let items = try edgeMMItems([
            edgeMMItemJSON(id: 1, kind: "note", source: "manual", slug: "", sourceRef: "", content: "n"),
            edgeMMItemJSON(id: 2),
        ])
        XCTAssertNil(edgeMMMatch(items, "", ""))
        XCTAssertNil(edgeMMMatch(items, "a-job", ""))
        XCTAssertNil(edgeMMMatch(items, "", "00-overview.md"))
        XCTAssertNil(edgeMMMatch(items, " ", " "))
    }
}

// MARK: - mmMatch: the trim, the slashes, and the live-over-dead pick

final class EdgeMMMatchNameTests: XCTestCase {

    /// Obligation: "leading-slash-trimmed name". The server already stores
    /// `source_ref` trimmed ("source_ref = name with leading `/` trimmed"), so
    /// the trim is the QUERY's — and it is not cosmetic: the app's own artifact
    /// paths carry the slash, and an untrimmed compare would report every
    /// artifact as unmarked forever.
    func testEdgeMMMatchTrimsALeadingSlashOnTheQueryName() throws {
        let items = try edgeMMItems([edgeMMItemJSON(id: 1, sourceRef: "00-overview.md")])
        XCTAssertEqual(edgeMMMatch(items, "a-job", "/00-overview.md")?.id, 1)
        XCTAssertEqual(edgeMMMatch(items, "a-job", "00-overview.md")?.id, 1)
    }

    /// Only the LEADING slash goes. An artifact name is a relative path and its
    /// internal slashes are part of its identity — `icons/v2.svg` and `v2.svg`
    /// are two different artifacts, and a matcher that took the leaf would
    /// light up the wrong one.
    ///
    /// (What a name with SEVERAL leading slashes should do is a silence: the
    /// server never stores one, no screen ever asks with one, and "leading
    /// slash trimmed" reads as one trim in every prior spelling of the rule.
    /// Nothing here asserts it.)
    func testEdgeMMMatchKeepsInternalSlashes() throws {
        let items = try edgeMMItems([edgeMMItemJSON(id: 5, sourceRef: "icon-variations/v2.svg")])
        XCTAssertEqual(edgeMMMatch(items, "a-job", "icon-variations/v2.svg")?.id, 5)
        XCTAssertEqual(edgeMMMatch(items, "a-job", "/icon-variations/v2.svg")?.id, 5)
        XCTAssertNil(edgeMMMatch(items, "a-job", "v2.svg"))
        XCTAssertNil(edgeMMMatch(items, "a-job", "icon-variations"))
    }

    /// The trim is the leading slash and NOTHING else. A trailing slash, a
    /// trailing space or a wrapping trim would each make the matcher accept a
    /// name the server would then refuse to address.
    func testEdgeMMMatchTrimsNothingButTheLeadingSlash() throws {
        let items = try edgeMMItems([edgeMMItemJSON(id: 1, sourceRef: "00-overview.md")])
        XCTAssertNil(edgeMMMatch(items, "a-job", "00-overview.md/"))
        XCTAssertNil(edgeMMMatch(items, "a-job", "00-overview.md "))
        XCTAssertNil(edgeMMMatch(items, "a-job", " 00-overview.md"))
        XCTAssertNil(edgeMMMatch(items, "a-job", "./00-overview.md"))
    }

    /// The state a re-mark leaves behind: `POST /api/mm/artifact` on changed
    /// bytes INSERTS the replacement first and supersedes the old row after, so
    /// a page holds a dead row and a live row for one identity — and the served
    /// order is newest first, which puts them either way round depending on the
    /// clock and the id tiebreak.
    ///
    /// Both orders are asserted because "an invalidated item never matches" is
    /// a filter, not a preference: a matcher that took the FIRST row with a
    /// matching pair and only then checked liveness would pass one order and
    /// fail the other, which is the failure that hides behind a fixture.
    func testEdgeMMMatchPrefersTheLiveRowInEitherOrder() throws {
        let dead = edgeMMItemJSON(id: 1, invalidatedAt: 1_770_000_500_000, invalidReason: "superseded")
        let live = edgeMMItemJSON(id: 2, createdAt: 1_770_000_600_000)
        XCTAssertEqual(try edgeMMMatch(edgeMMItems([dead, live]), "a-job", "00-overview.md")?.id, 2)
        XCTAssertEqual(try edgeMMMatch(edgeMMItems([live, dead]), "a-job", "00-overview.md")?.id, 2)
    }

    /// The same pick with the dead rows piled up around the live one, and with
    /// unrelated live rows of other jobs and other sources in the list — the
    /// realistic page, where the row wanted is neither first nor last.
    func testEdgeMMMatchFindsTheLiveRowInACrowdedPage() throws {
        let items = try edgeMMItems([
            edgeMMItemJSON(id: 9, slug: "other-job", sourceRef: "00-overview.md"),
            edgeMMItemJSON(id: 8, invalidatedAt: 300, invalidReason: "content-changed"),
            edgeMMItemJSON(id: 7, source: "artifact-element", sourceRef: "00-overview.md"),
            edgeMMItemJSON(id: 6),
            edgeMMItemJSON(id: 5, invalidatedAt: 100, invalidReason: "superseded"),
            edgeMMItemJSON(id: 4, kind: "note", source: "manual", slug: "", sourceRef: "", content: "n"),
        ])
        XCTAssertEqual(edgeMMMatch(items, "a-job", "00-overview.md")?.id, 6)
        XCTAssertEqual(edgeMMMatch(items, "other-job", "00-overview.md")?.id, 9)
    }

    /// A `source == "artifact"` row whose id is at the Int64 ceiling is still
    /// the row that comes back — the match is on the pair, and the identity the
    /// caller then hands to `dropMM` must survive the round trip intact.
    func testEdgeMMMatchReturnsTheRowIdentityUnrounded() throws {
        let items = try edgeMMItems([edgeMMItemJSON(id: 9_007_199_254_740_993)])
        XCTAssertEqual(edgeMMMatch(items, "a-job", "00-overview.md")?.id, 9_007_199_254_740_993)
    }
}
