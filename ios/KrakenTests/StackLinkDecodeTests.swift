import XCTest
@testable import Kraken

// Pins the `stack` wire shape (ios/API.md's job-level `stack` row and its
// `jobStackLink` shape; internal/api/jobstack.go's webStackLink): a job WITH
// the additive key decodes every field, a job WITHOUT it decodes with `stack`
// nil (absence is the only no-links spelling), and an item carrying a status
// word this build does not know still decodes — the field is a plain String
// on purpose, so a future probe verdict costs nothing.
//
// Nothing renders these yet (Q3 of job 622 is open: a localhost link means
// nothing on the phone until it is rewritten to the owner's tailnet host), so
// the decode is the whole client-side contract for now.
final class StackLinkDecodeTests: XCTestCase {

    private func board(jobs: String) -> String {
        """
        {
          "daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 1},
          "daemon_ok": true,
          "cell_cap": 4,
          "live_cells": 1,
          "jobs": \(jobs),
          "contract": 7,
          "stale": false
        }
        """
    }

    private func job(extra: String = "") -> String {
        """
        [{"slug": "kraken-localhost-ui", "repo": "kraken", "state": "blocked", "live": true,
          "mtime": 1780000000000\(extra)}]
        """
    }

    func testDecodesStackLinks() throws {
        let extra = #", "stack": [{"url": "http://127.0.0.1:7878", "label": "board", "status": "up", "at": 1780000001000}, {"url": "https://abc.ngrok.app", "label": "", "status": "unknown", "at": 1780000002000}]"#
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job(extra: extra)).utf8))
        let stack = try XCTUnwrap(b.jobs.first?.stack)
        XCTAssertEqual(stack.count, 2)
        XCTAssertEqual(stack[0].url, "http://127.0.0.1:7878")
        XCTAssertEqual(stack[0].label, "board")
        XCTAssertEqual(stack[0].status, "up")
        XCTAssertEqual(stack[0].at, 1780000001000)
        XCTAssertEqual(stack[1].label, "", "an empty label is a real answer, not a missing key")
        XCTAssertEqual(stack[1].status, "unknown")
    }

    func testAbsentStackIsNil() throws {
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job()).utf8))
        XCTAssertNil(b.jobs.first?.stack, "absent `stack` is nil, never an empty list")
    }

    func testUnknownStatusWordStillDecodes() throws {
        let extra = #", "stack": [{"url": "http://localhost:5173", "label": "ui", "status": "flapping", "at": 1}]"#
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board(jobs: job(extra: extra)).utf8))
        XCTAssertEqual(b.jobs.first?.stack?.first?.status, "flapping")
    }
}
