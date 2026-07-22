import XCTest
@testable import Kraken

// Decodes GET /api/usage's wire shape (see ios/API.md's "GET /api/usage"
// section and core/usagepool.go) — one account exercising every field, one
// minimal account exercising the omitempty-is-Optional absence rule.
final class UsageDecodeTests: XCTestCase {

    func testDecodesFullResponse() throws {
        let json = """
        {
          "available": true,
          "fetched_at": 1780000000000,
          "age_seconds": 42,
          "daemon_ok": true,
          "accounts": [
            {
              "slug": "main",
              "email": "carl@example.com",
              "display_name": "Carl",
              "tier": "default_claude_max_20x",
              "current": true,
              "session": {"percent": 61.5, "resets_at": 1780003600000},
              "weekly": {"percent": 22, "resets_at": 1780500000000},
              "models": [
                {"name": "Fable", "percent": 5, "resets_at": 1780500000000}
              ],
              "fetched_at": 1779999900000,
              "stale": false,
              "error": "",
              "reauth": false,
              "no_creds": false,
              "cooldown_until": 1780001000000
            },
            {
              "slug": "pool-2",
              "no_creds": true
            }
          ]
        }
        """
        let data = Data(json.utf8)
        let resp = try JSONDecoder().decode(UsageResponse.self, from: data)

        XCTAssertTrue(resp.available)
        XCTAssertEqual(resp.fetchedAt, 1_780_000_000_000)
        XCTAssertEqual(resp.ageSeconds, 42)
        XCTAssertTrue(resp.daemonOK)
        XCTAssertEqual(resp.accounts.count, 2)

        let main = resp.accounts[0]
        XCTAssertEqual(main.slug, "main")
        XCTAssertEqual(main.email, "carl@example.com")
        XCTAssertEqual(main.displayName, "Carl")
        XCTAssertEqual(main.tier, "default_claude_max_20x")
        XCTAssertEqual(main.current, true)
        XCTAssertEqual(main.session?.percent, 61.5)
        XCTAssertEqual(main.session?.resetsAt, 1_780_003_600_000)
        XCTAssertEqual(main.weekly?.percent, 22)
        XCTAssertEqual(main.models?.count, 1)
        XCTAssertEqual(main.models?.first?.name, "Fable")
        XCTAssertEqual(main.models?.first?.percent, 5)
        XCTAssertEqual(main.fetchedAt, 1_779_999_900_000)
        XCTAssertEqual(main.stale, false)
        XCTAssertEqual(main.error, "")
        XCTAssertEqual(main.reauth, false)
        XCTAssertEqual(main.noCreds, false)
        XCTAssertEqual(main.cooldownUntil, 1_780_001_000_000)

        let pool = resp.accounts[1]
        XCTAssertEqual(pool.slug, "pool-2")
        XCTAssertNil(pool.email)
        XCTAssertNil(pool.displayName)
        XCTAssertNil(pool.tier)
        XCTAssertNil(pool.current)
        XCTAssertNil(pool.session)
        XCTAssertNil(pool.weekly)
        XCTAssertNil(pool.models)
        XCTAssertNil(pool.fetchedAt)
        XCTAssertNil(pool.stale)
        XCTAssertNil(pool.error)
        XCTAssertNil(pool.reauth)
        XCTAssertEqual(pool.noCreds, true)
        XCTAssertNil(pool.cooldownUntil)
    }

    func testDecodesUnavailableSnapshot() throws {
        let json = """
        {"available": false, "fetched_at": 0, "age_seconds": 0, "daemon_ok": false, "accounts": []}
        """
        let resp = try JSONDecoder().decode(UsageResponse.self, from: Data(json.utf8))
        XCTAssertFalse(resp.available)
        XCTAssertFalse(resp.daemonOK)
        XCTAssertTrue(resp.accounts.isEmpty)
    }
}
