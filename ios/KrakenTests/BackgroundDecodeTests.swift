import XCTest
@testable import Kraken

// Pins the `background` wire shape (see ios/API.md's `background` row under
// "GET /api/board" and internal/api/background.go) — a board WITH the
// additive key, a board WITHOUT it, and the drifted-value degrade the
// hand-rolled WebBackground.init(from:) promises. The absent half is the
// point, exactly as in GoalsDecodeTests: the key is additive AND omitted
// whenever no background is set, so absence is both "older server" and "no
// background", and a decode that threw on it would cost the entire board.
final class BackgroundDecodeTests: XCTestCase {

    private func board(extraKeys: String = "") -> String {
        """
        {
          "daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 2},
          "daemon_ok": true,
          "cell_cap": 4,
          "live_cells": 2,
          "jobs": [],
          "contract": 6,
          "stale": false\(extraKeys)
        }
        """
    }

    func testDecodesBoardWithBackground() throws {
        let json = board(extraKeys: #", "background": {"updated_at": 1786945521465}"#)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertEqual(b.background?.updatedAt, 1_786_945_521_465)
    }

    func testDecodesBoardWithoutBackground() throws {
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(board().utf8))
        XCTAssertNil(b.background)
    }

    func testNullBackgroundDecodesToNil() throws {
        let json = board(extraKeys: #", "background": null"#)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertNil(b.background)
    }

    // A drifted value — a string where an object should be — must degrade to
    // a zeroed stamp rather than throw out of decodeIfPresent and cost the
    // whole board. This is the claim WebBackground's comment makes; pin it.
    func testDriftedBackgroundDegradesToZeroStamp() throws {
        let json = board(extraKeys: #", "background": "not-an-object""#)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertEqual(b.background?.updatedAt, 0)
    }

    // Same degrade one level in: an object whose stamp is the wrong type.
    func testDriftedStampDegradesToZero() throws {
        let json = board(extraKeys: #", "background": {"updated_at": "soon"}"#)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertEqual(b.background?.updatedAt, 0)
    }
}
