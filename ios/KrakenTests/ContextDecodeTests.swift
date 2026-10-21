import XCTest
@testable import Kraken

// The `context` key on a board card (ios/API.md's `webJob` table,
// internal/api/board.go's webJobContext): decode-only this pass, and decoded
// TOLERANTLY — a malformed object costs the card its (unrendered) meter, never
// the board its decode. Lead-authored (direct job 630, no blind authorship).

final class ContextDecodeTests: XCTestCase {

    private func job(_ contextJSON: String?) throws -> WebJob {
        var fields = [
            "\"slug\":\"ctx\"", "\"repo\":\"kraken\"", "\"state\":\"working\"", "\"live\":true", "\"mtime\":1",
        ]
        if let contextJSON { fields.append("\"context\":\(contextJSON)") }
        let json = "{" + fields.joined(separator: ",") + "}"
        return try JSONDecoder().decode(WebJob.self, from: Data(json.utf8))
    }

    // This feature's real worked example: job 630's report on 2026-10-21, with
    // a fired handoff attached.
    func testWellFormedObjectDecodesEveryField() throws {
        let j = try job("""
        {"pct":25,"window":1000000,"used":242390,"at":1792599370254,"handoff_pct":75,
         "handoff":{"fires":1,"last_at":1792590000000,"exhausted":false}}
        """)
        let c = try XCTUnwrap(j.context)
        XCTAssertEqual(c.pct, 25)
        XCTAssertEqual(c.window, 1_000_000)
        XCTAssertEqual(c.used, 242_390)
        XCTAssertEqual(c.at, 1_792_599_370_254)
        XCTAssertEqual(c.handoffPct, 75)
        let h = try XCTUnwrap(c.handoff)
        XCTAssertEqual(h.fires, 1)
        XCTAssertEqual(h.lastAt, 1_792_590_000_000)
        XCTAssertFalse(h.exhausted)
    }

    // `handoff` is the one omitempty key inside: a fresh session has none.
    // `pct` may carry decimals (the statusline reference's example is 23.5).
    func testHandoffAbsentAndDecimalPct() throws {
        let j = try job(#"{"pct":23.5,"window":200000,"used":47000,"at":1,"handoff_pct":75}"#)
        let c = try XCTUnwrap(j.context)
        XCTAssertEqual(c.pct, 23.5)
        XCTAssertNil(c.handoff)
    }

    // Absent key: nil, not an error — the wire's spelling of "unreported".
    func testAbsentKeyIsNil() throws {
        XCTAssertNil(try job(nil).context)
    }

    // A malformed object (a retyped `pct`) degrades to nil and leaves every
    // sibling field intact — the same tolerance `account` has, because
    // `WebBoard.jobs` is a strictly decoded array.
    func testMalformedObjectDegradesToNil() throws {
        let j = try job(#"{"pct":"lots","window":1000000}"#)
        XCTAssertNil(j.context)
        XCTAssertEqual(j.slug, "ctx")
        XCTAssertTrue(j.live)
        let s = try job(#""x""#)
        XCTAssertNil(s.context)
    }

    // The whole board still decodes with a malformed context on one job.
    func testBoardSurvivesOneMalformedContext() throws {
        let json = """
        {"daemon":{"pid":4242,"started_at":1780000000000,"last_tick":1780000004000,"cells":2},"daemon_ok":true,"cell_cap":15,"live_cells":0,
         "jobs":[
           {"slug":"a","repo":"kraken","state":"working","live":true,"mtime":1,"context":{"pct":"bad"}},
           {"slug":"b","repo":"kraken","state":"working","live":true,"mtime":1,"context":{"pct":40,"window":1000000,"used":400000,"at":2,"handoff_pct":75}}
         ],"contract":7}
        """
        let board = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertEqual(board.jobs.count, 2)
        XCTAssertNil(board.jobs[0].context)
        XCTAssertEqual(board.jobs[1].context?.pct, 40)
    }
}
