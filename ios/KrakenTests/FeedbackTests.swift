import XCTest
@testable import Kraken

// The pure pieces behind the feedback surface: the artifact-URL parse a
// create is addressed with, the optimistic overlay a frozen job snapshot is
// rendered through, and WebFeedback's own decode — which nothing pinned
// before this, unlike webUpdate/webShipIOS/usage.
final class FeedbackTests: XCTestCase {

    // MARK: - artifactParts

    func testFlatNameInCellHome() {
        XCTAssertEqual(
            artifactParts(from: "/api/artifact/helper-catalog/cell/00-overview.md"),
            ArtifactRef(slug: "helper-catalog", home: "cell", name: "00-overview.md")
        )
    }

    // The trap this whole function exists for: artifactTree rewrites
    // WebArtifact.name to a relative leaf ("v2.png") while preserving the
    // url, so only the url still knows the full server-side name. Everything
    // after the home segment is the name, slashes included.
    func testNestedNameKeepsItsSlashes() {
        XCTAssertEqual(
            artifactParts(from: "/api/artifact/icons/cell/icon-variations/v2.png"),
            ArtifactRef(slug: "icons", home: "cell", name: "icon-variations/v2.png")
        )
    }

    func testDeeplyNestedName() {
        XCTAssertEqual(
            artifactParts(from: "/api/artifact/j/pluto/a/b/c/d.svg"),
            ArtifactRef(slug: "j", home: "pluto", name: "a/b/c/d.svg")
        )
    }

    func testEachHome() {
        for home in ["cell", "pluto", "archive"] {
            XCTAssertEqual(
                artifactParts(from: "/api/artifact/j/\(home)/x.md")?.home, home,
                "home \(home) should parse"
            )
        }
    }

    func testMalformedURLsReturnNil() {
        // Not an artifact route at all.
        XCTAssertNil(artifactParts(from: "/api/upload/j/img-001.png"))
        // No name segment.
        XCTAssertNil(artifactParts(from: "/api/artifact/j/cell"))
        // Trailing slash: home present, name empty.
        XCTAssertNil(artifactParts(from: "/api/artifact/j/cell/"))
        // No home.
        XCTAssertNil(artifactParts(from: "/api/artifact/j"))
        XCTAssertNil(artifactParts(from: "/api/artifact/"))
        XCTAssertNil(artifactParts(from: ""))
        // Relative — the app always joins baseURL itself, so a path without
        // the leading slash is a caller bug, not a variant to tolerate.
        XCTAssertNil(artifactParts(from: "api/artifact/j/cell/x.md"))
    }

    // MARK: - rune cap

    func testRuneCountIsScalarsNotCharacters() {
        // "👍🏽" is ONE Character (grapheme cluster) but TWO Unicode scalars —
        // and it is scalars (Go runes) the server's 8000 cap counts. A
        // Character-based counter would under-report by half here and let a
        // note through that the server then 400s.
        XCTAssertEqual("👍🏽".count, 1)
        XCTAssertEqual(feedbackRuneCount("👍🏽"), 2)
    }

    func testRuneCountTrimsFirst() {
        // The server caps AFTER trim, so the counter has to as well or it
        // reports "over" for a note that would actually be accepted.
        XCTAssertEqual(feedbackRuneCount("   abc \n"), 3)
        XCTAssertEqual(feedbackRuneCount("   \n "), 0)
    }

    // MARK: - optimistic overlay

    private func row(_ id: Int, _ note: String, addressed: String? = nil) -> WebFeedback {
        WebFeedback(id: id, artifact: "a.md", note: note, at: 1_700_000_000_000,
                    addressed: addressed, addressedAt: addressed == nil ? nil : "2026-07-26T00:00:00Z")
    }

    func testEmptyOverlayIsIdentity() {
        let server = [row(1, "one"), row(2, "two")]
        let merged = FeedbackOverlay().apply(to: server)
        XCTAssertEqual(merged.map(\.id), [1, 2])
        XCTAssertEqual(merged.map(\.note), ["one", "two"])
    }

    func testEditReplacesNoteAndKeepsEverythingElse() {
        var overlay = FeedbackOverlay()
        overlay.edits[2] = "rewritten"
        let merged = overlay.apply(to: [row(1, "one"), row(2, "two", addressed: "did it")])
        XCTAssertEqual(merged.map(\.note), ["one", "rewritten"])
        // An addressed row stays addressed after an edit — the app never
        // clears zeus' account, only the note text.
        XCTAssertEqual(merged[1].addressed, "did it")
        XCTAssertTrue(merged[1].isAddressed)
    }

    // The piece a value map alone can't express, and the reason `hidden` is a
    // set: a delete removes the row, and every string is a legal note.
    func testDeleteRemovesTheRow() {
        var overlay = FeedbackOverlay()
        overlay.hidden.insert(1)
        let merged = overlay.apply(to: [row(1, "one"), row(2, "two")])
        XCTAssertEqual(merged.map(\.id), [2])
    }

    func testDeleteWinsOverAnEditOfTheSameRow() {
        var overlay = FeedbackOverlay()
        overlay.edits[1] = "rewritten"
        overlay.hidden.insert(1)
        XCTAssertEqual(overlay.apply(to: [row(1, "one")]).count, 0)
    }

    func testCreatedRowsAppendAndAreOptimistic() {
        var overlay = FeedbackOverlay()
        let id = overlay.nextOptimisticID()
        XCTAssertLessThan(id, 0, "optimistic ids must be negative so they can't collide with a rowid")
        overlay.created.append(row(id, "fresh"))
        let merged = overlay.apply(to: [row(1, "one")])
        XCTAssertEqual(merged.map(\.note), ["one", "fresh"])
        XCTAssertFalse(merged[0].isOptimistic)
        XCTAssertTrue(merged[1].isOptimistic)
    }

    func testOptimisticIDsAreDistinct() {
        var overlay = FeedbackOverlay()
        let a = overlay.nextOptimisticID()
        let b = overlay.nextOptimisticID()
        XCTAssertNotEqual(a, b)
    }

    func testDeletingAnOptimisticRowHidesIt() {
        var overlay = FeedbackOverlay()
        let id = overlay.nextOptimisticID()
        overlay.created.append(row(id, "fresh"))
        overlay.hidden.insert(id)
        XCTAssertEqual(overlay.apply(to: []).count, 0)
    }

    // MARK: - WebFeedback decode

    private func decode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
        try JSONDecoder().decode(T.self, from: Data(json.utf8))
    }

    func testWebFeedbackDecodesOpenRow() throws {
        let f: WebFeedback = try decode("""
        {"id": 41, "artifact": "12-catalog-flow.html", "note": "edges run through boxes", "at": 1769472000000}
        """)
        XCTAssertEqual(f.id, 41)
        XCTAssertEqual(f.artifact, "12-catalog-flow.html")
        XCTAssertEqual(f.note, "edges run through boxes")
        XCTAssertEqual(f.at, 1_769_472_000_000)
        // addressed/addressed_at are omitempty on the wire — absent means the
        // note is still open, which is the whole signal.
        XCTAssertNil(f.addressed)
        XCTAssertNil(f.addressedAt)
        XCTAssertFalse(f.isAddressed)
    }

    func testWebFeedbackDecodesAddressedRow() throws {
        let f: WebFeedback = try decode("""
        {"id": 42, "artifact": "00-overview.md", "note": "too long", "at": 1769472000000,
         "addressed": "cut the table, 6 lines now", "addressed_at": "2026-07-26T04:00:00Z"}
        """)
        XCTAssertTrue(f.isAddressed)
        XCTAssertEqual(f.addressed, "cut the table, 6 lines now")
        XCTAssertEqual(f.addressedAt, "2026-07-26T04:00:00Z")
    }

    // An empty `addressed` is not the same shape as an absent one, but it
    // must read the same way: still open.
    func testWebFeedbackEmptyAddressedIsStillOpen() throws {
        let f: WebFeedback = try decode("""
        {"id": 43, "artifact": "a.md", "note": "n", "at": 0, "addressed": ""}
        """)
        XCTAssertFalse(f.isAddressed)
    }

    func testWebJobDecodesFeedbackArray() throws {
        // The board's per-job key — including for an ARCHIVED job, which is
        // the case the detail view must not gate away (doc_questions is never
        // set for one, feedback always is).
        let job: WebJob = try decode("""
        {"slug": "old-2", "repo": "kraken", "state": "archived", "live": false, "mtime": 0,
         "archived": true, "display_slug": "old",
         "feedback": [{"id": 7, "artifact": "a.md", "note": "still true", "at": 1}]}
        """)
        XCTAssertEqual(job.feedback?.count, 1)
        XCTAssertEqual(job.feedback?.first?.note, "still true")
        XCTAssertNil(job.docQuestions)
        XCTAssertEqual(job.title, "old")
    }

    func testWebJobWithoutFeedbackKeyDecodes() throws {
        let job: WebJob = try decode("""
        {"slug": "j", "repo": "kraken", "state": "draft", "live": false, "mtime": 0}
        """)
        XCTAssertNil(job.feedback)
    }
}
