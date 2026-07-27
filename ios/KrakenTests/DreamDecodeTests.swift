import XCTest
@testable import Kraken

// The dream loop's two read shapes. Both endpoints are additive (apiContract
// stays 5), so nothing here pins a contract number — it pins the field names
// and optionality, which is what a drift would actually break.
final class DreamDecodeTests: XCTestCase {

    private func decode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
        try JSONDecoder().decode(T.self, from: Data(json.utf8))
    }

    // MARK: - GET /api/dream

    func testDreamReportsDecode() throws {
        let resp: DreamReportsResponse = try decode("""
        {"reports": [
          {"date": "2026-07-28", "slug": "dream-2026-07-28", "body": "# night\\n\\n- shipped one"},
          {"date": "2026-07-27", "slug": "dream-2026-07-27", "body": "nothing shipped"}
        ]}
        """)
        XCTAssertEqual(resp.reports.count, 2)
        // Newest first is the server's ordering, mirrored verbatim.
        XCTAssertEqual(resp.reports.map(\.date), ["2026-07-28", "2026-07-27"])
        XCTAssertEqual(resp.reports[0].id, "dream-2026-07-28")
        XCTAssertTrue(resp.reports[0].body.hasPrefix("# night"))
    }

    func testEmptyDreamReportsDecode() throws {
        let resp: DreamReportsResponse = try decode(#"{"reports": []}"#)
        XCTAssertTrue(resp.reports.isEmpty)
    }

    // MARK: - GET /api/watchlist

    func testWatchlistDecodesUnjudgedRow() throws {
        let resp: WatchlistResponse = try decode("""
        {"watchlist": [{
          "id": 3, "observation": "hermes re-reads the whole contract",
          "class": "repetition", "state": "watching", "sightings": 3,
          "first_seen": 1769000000000, "last_seen": 1769472000000,
          "updated_at": 1769472000000
        }]}
        """)
        let w = try XCTUnwrap(resp.watchlist.first)
        XCTAssertEqual(w.id, 3)
        // `class` is a Swift keyword, so the property is watchClass with an
        // explicit CodingKey — a rename here would silently drop the column.
        XCTAssertEqual(w.watchClass, "repetition")
        XCTAssertEqual(w.state, "watching")
        XCTAssertEqual(w.sightings, 3)
        // carl-owned columns are omitempty: an unjudged row genuinely has none.
        XCTAssertNil(w.verdict)
        XCTAssertNil(w.comment)
        XCTAssertNil(w.commentedAt)
        XCTAssertFalse(w.isJudged)
        XCTAssertNil(w.evidence)
        XCTAssertNil(w.suggestedFix)
        XCTAssertNil(w.prURL)
    }

    func testWatchlistDecodesJudgedRow() throws {
        let resp: WatchlistResponse = try decode("""
        {"watchlist": [{
          "id": 9, "observation": "overviews run long", "class": "feedback",
          "state": "watching", "sightings": 1,
          "first_seen": 1, "last_seen": 2, "updated_at": 3,
          "evidence": "{\\"note\\":43}", "suggested_fix": "cap at 5 lines",
          "pr_url": "https://github.com/car1zhang/kraken/pull/74",
          "verdict": "fix", "comment": "read the Shape Kit section only",
          "commented_at": "2026-07-28T09:00:00Z"
        }]}
        """)
        let w = try XCTUnwrap(resp.watchlist.first)
        XCTAssertTrue(w.isJudged)
        XCTAssertEqual(w.verdict, "fix")
        XCTAssertEqual(w.comment, "read the Shape Kit section only")
        XCTAssertEqual(w.commentedAt, "2026-07-28T09:00:00Z")
        XCTAssertEqual(w.suggestedFix, "cap at 5 lines")
        XCTAssertEqual(w.prURL, "https://github.com/car1zhang/kraken/pull/74")
        XCTAssertEqual(w.evidence, #"{"note":43}"#)
    }

    func testEmptyWatchlistDecodes() throws {
        let resp: WatchlistResponse = try decode(#"{"watchlist": []}"#)
        XCTAssertTrue(resp.watchlist.isEmpty)
    }

    // MARK: - POST /api/watchlist

    // The verdict body is deliberately tiny: the server 400s a request that
    // so much as NAMES an agent-owned key, so this encoding must never grow
    // one. Encoding the DTO and reading the key set back is the check.
    func testWatchVerdictRequestEncodesOnlyItsThreeKeys() throws {
        let data = try JSONEncoder().encode(
            WatchVerdictRequest(id: 3, verdict: "fix", comment: "do it this way")
        )
        let obj = try XCTUnwrap(
            JSONSerialization.jsonObject(with: data) as? [String: Any]
        )
        XCTAssertEqual(Set(obj.keys), ["id", "verdict", "comment"])
        XCTAssertEqual(obj["verdict"] as? String, "fix")
    }

    func testWatchVerdictRequestCanClear() throws {
        let data = try JSONEncoder().encode(WatchVerdictRequest(id: 3, verdict: "", comment: ""))
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        // "" is a LEGAL verdict (the documented clear), so the key must ride
        // the wire empty rather than being omitted.
        XCTAssertEqual(obj["verdict"] as? String, "")
        XCTAssertEqual(obj["comment"] as? String, "")
    }
}
