import XCTest
@testable import Kraken

// Decodes the TestFlight-ship wire shapes (see ios/API.md's `webShipIOS`
// table under "GET /api/board" plus the "POST /api/ship-ios" and "GET
// /api/ship-ios/status" sections, and internal/api/shipios.go) — the record
// with every field populated, the 202 start body, and the older-server case
// where /api/board carries no `ship_ios` key at all. Mirrors
// UpdateDecodeTests.swift, which is the point: the two controls are siblings
// and their models must fail in the same (contained) ways.
final class ShipIOSDecodeTests: XCTestCase {

    func testDecodesFullShipRecord() throws {
        let json = """
        {
          "phase": "uploading",
          "build": 137,
          "error": "",
          "started_at": 1780000000000,
          "updated_at": 1780000420000
        }
        """
        let s = try JSONDecoder().decode(WebShipIOS.self, from: Data(json.utf8))

        XCTAssertEqual(s.phase, "uploading")
        XCTAssertEqual(s.build, 137)
        XCTAssertEqual(s.error, "")
        XCTAssertEqual(s.startedAt, 1_780_000_000_000)
        XCTAssertEqual(s.updatedAt, 1_780_000_420_000)
    }

    // No field is omitempty server-side, so a zeroed record still carries every
    // key — the "never shipped" shape a fresh Mac serves.
    func testDecodesZeroedShipRecord() throws {
        let json = """
        {"phase": "idle", "build": 0, "error": "", "started_at": 0, "updated_at": 0}
        """
        let s = try JSONDecoder().decode(WebShipIOS.self, from: Data(json.utf8))
        XCTAssertEqual(s.phase, "idle")
        XCTAssertEqual(s.build, 0)
        XCTAssertEqual(s.startedAt, 0)
        XCTAssertEqual(s.error, "")
    }

    func testDecodesBoardShipKey() throws {
        let json = """
        {
          "daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 2},
          "daemon_ok": true,
          "cell_cap": 4,
          "live_cells": 2,
          "jobs": [],
          "contract": 5,
          "stale": false,
          "ship_ios": {
            "phase": "done", "build": 137, "error": "",
            "started_at": 1780000000000, "updated_at": 1780000600000
          }
        }
        """
        let board = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertEqual(board.shipIOS?.phase, "done")
        XCTAssertEqual(board.shipIOS?.build, 137)
    }

    // A server older than the ship button omits `ship_ios` entirely. That must
    // decode to nil, not throw — the whole board would be lost over a key the
    // control can perfectly well do without. `ship_ios` was added additively,
    // so `contract` stays 5 and cannot be used to tell the two servers apart.
    func testBoardWithoutShipKeyDecodesToNil() throws {
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
        XCTAssertNil(board.shipIOS)
        XCTAssertEqual(board.contract, 5)
    }

    // The blast-radius guarantee (see WebShipIOS's header in Models.swift):
    // `ship_ios` is decoded with decodeIfPresent, which only nils on an absent
    // or null KEY — an inner decode failure would propagate and take the whole
    // /api/board response with it. Every field is therefore Optional and
    // hand-decoded with `try?`, so the worst a drifted sub-key can cost is the
    // ⇧ control, never the board.
    func testMalformedShipSubKeysStillYieldAUsableBoard() throws {
        let malformed = [
            // a key dropped entirely (a future `omitempty` on the Go struct)
            #""phase": "done", "build": 137, "error": "", "started_at": 1780000000000"#,
            // an explicit null where a string is documented
            #""phase": "done", "build": 137, "error": null, "started_at": 1780000000000, "updated_at": 1780000600000"#,
            // the wrong scalar type
            #""phase": "done", "build": "137", "error": "", "started_at": 1780000000000, "updated_at": 1780000600000"#,
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
              "ship_ios": {\(body)}
            }
            """
            let board = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
            XCTAssertEqual(board.cellCap, 4, "case \(i): the board itself must survive")
            XCTAssertEqual(board.liveCells, 2, "case \(i)")
            XCTAssertNotNil(board.shipIOS, "case \(i): the ship key still decodes")
        }
    }

    // POST /api/ship-ios' 202 body — the claim, not the finished run, and a
    // long way from an installed build.
    func testDecodesShipStartResponse() throws {
        let json = """
        {"ok": true, "phase": "preflight"}
        """
        let resp = try JSONDecoder().decode(ShipIOSStartResponse.self, from: Data(json.utf8))
        XCTAssertTrue(resp.ok)
        XCTAssertEqual(resp.phase, "preflight")
    }
}
