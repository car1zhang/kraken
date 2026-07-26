import XCTest
@testable import Kraken

// Decodes the self-update wire shapes (see ios/API.md's `webUpdate` table
// under "GET /api/board" plus the "POST /api/update" and "GET
// /api/update/status" sections, and internal/api/update.go) — the record
// with every field populated, POST /api/update's 202 body, and the
// older-server case where /api/board carries no `update` key at all.
final class UpdateDecodeTests: XCTestCase {

    func testDecodesFullUpdateRecord() throws {
        let json = """
        {
          "phase": "restarting",
          "from_sha": "805c986",
          "to_sha": "9fa23c1",
          "behind": 3,
          "error": "",
          "started_at": 1780000000000,
          "updated_at": 1780000042000,
          "build_sha": "bf91ca5",
          "checked_at": 1779999000000
        }
        """
        let u = try JSONDecoder().decode(WebUpdate.self, from: Data(json.utf8))

        XCTAssertEqual(u.phase, "restarting")
        XCTAssertEqual(u.fromSHA, "805c986")
        XCTAssertEqual(u.toSHA, "9fa23c1")
        XCTAssertEqual(u.behind, 3)
        XCTAssertEqual(u.error, "")
        XCTAssertEqual(u.startedAt, 1_780_000_000_000)
        XCTAssertEqual(u.updatedAt, 1_780_000_042_000)
        XCTAssertEqual(u.buildSHA, "bf91ca5")
        XCTAssertEqual(u.checkedAt, 1_779_999_000_000)
    }

    // No field is omitempty server-side, so a zeroed record still carries
    // every key — the "never run, never checked" shape a fresh Mac serves.
    func testDecodesZeroedUpdateRecord() throws {
        let json = """
        {"phase": "idle", "from_sha": "", "to_sha": "", "behind": 0, "error": "",
         "started_at": 0, "updated_at": 0, "build_sha": "dev", "checked_at": 0}
        """
        let u = try JSONDecoder().decode(WebUpdate.self, from: Data(json.utf8))
        XCTAssertEqual(u.phase, "idle")
        XCTAssertEqual(u.behind, 0)
        XCTAssertEqual(u.startedAt, 0)
        XCTAssertEqual(u.buildSHA, "dev")
    }

    func testDecodesBoardUpdateKey() throws {
        let json = """
        {
          "daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 2},
          "daemon_ok": true,
          "cell_cap": 4,
          "live_cells": 2,
          "jobs": [],
          "contract": 5,
          "stale": false,
          "update": {
            "phase": "done", "from_sha": "805c986", "to_sha": "9fa23c1", "behind": 0,
            "error": "", "started_at": 1780000000000, "updated_at": 1780000090000,
            "build_sha": "9fa23c1", "checked_at": 1779999000000
          }
        }
        """
        let board = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertEqual(board.update?.phase, "done")
        XCTAssertEqual(board.update?.toSHA, "9fa23c1")
        XCTAssertEqual(board.update?.behind, 0)
    }

    // A server older than the self-update button omits `update` entirely.
    // That must decode to nil, not throw — the whole board would be lost
    // over a key the control can perfectly well do without.
    func testBoardWithoutUpdateKeyDecodesToNil() throws {
        let json = """
        {
          "daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 0},
          "daemon_ok": true,
          "cell_cap": 4,
          "live_cells": 0,
          "jobs": [],
          "contract": 5,
          "stale": false
        }
        """
        let board = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertNil(board.update)
        XCTAssertEqual(board.contract, 5)
    }

    // The blast-radius guarantee (see WebUpdate's header in Models.swift):
    // `update` is decoded with decodeIfPresent, which only nils on an absent
    // or null KEY — an inner decode failure propagates and takes the whole
    // /api/board response with it. Every WebUpdate field is therefore
    // Optional, so the worst a drifted sub-key can cost is the ⟳ control,
    // never the board. Each case below threw `keyNotFound` /
    // `valueNotFound` / `typeMismatch` on the ENTIRE board before that.
    func testMalformedUpdateSubKeysStillYieldAUsableBoard() throws {
        let malformed = [
            // a key dropped entirely (a future `omitempty` on the Go struct)
            #""phase": "done", "from_sha": "805c986", "to_sha": "9fa23c1", "behind": 0, "error": "", "started_at": 1780000000000, "updated_at": 1780000090000, "build_sha": "9fa23c1""#,
            // an explicit null where a string is documented
            #""phase": "done", "from_sha": null, "to_sha": "9fa23c1", "behind": 0, "error": "", "started_at": 1780000000000, "updated_at": 1780000090000, "build_sha": "9fa23c1", "checked_at": 0"#,
            // the wrong scalar type
            #""phase": "done", "from_sha": "805c986", "to_sha": "9fa23c1", "behind": "3", "error": "", "started_at": 1780000000000, "updated_at": 1780000090000, "build_sha": "9fa23c1", "checked_at": 0"#,
        ]
        for (i, body) in malformed.enumerated() {
            let json = """
            {
              "daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 2},
              "daemon_ok": true,
              "cell_cap": 4,
              "live_cells": 2,
              "jobs": [],
              "contract": 5,
              "stale": false,
              "update": {\(body)}
            }
            """
            let board = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
            XCTAssertEqual(board.cellCap, 4, "case \(i): the board itself must survive")
            XCTAssertEqual(board.liveCells, 2, "case \(i)")
            XCTAssertNotNil(board.update, "case \(i): the update key still decodes")
        }
    }

    // POST /api/update's 202 body — the claim, not the finished run.
    func testDecodesUpdateStartResponse() throws {
        let json = """
        {"ok": true, "phase": "preflight", "from_sha": "805c986"}
        """
        let resp = try JSONDecoder().decode(UpdateStartResponse.self, from: Data(json.utf8))
        XCTAssertTrue(resp.ok)
        XCTAssertEqual(resp.phase, "preflight")
        XCTAssertEqual(resp.fromSHA, "805c986")
    }
}
