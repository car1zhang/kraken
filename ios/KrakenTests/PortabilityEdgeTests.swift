import XCTest
@testable import Kraken

// PortabilityEdgeTests — the EDGE dimension of artifacts/50-contract.md
// (kraken-portability-audit) Slice S, iOS half: `KrakenDefaults.serverURL == ""`.
// The edge is that "empty" means EMPTY — not a blank-looking placeholder, not a
// loopback address, not any host at all — since the first-run guard keys on the
// empty string and anything else would be polled as a server. Written blind;
// the first-run view, the Settings placeholder and the BoardView/KrakenApp/
// DraftStore guards name no symbol a unit test can reach (see the report).
final class PortabilityEdgeTests: XCTestCase {

    func testEdgeDefaultServerURLIsExactlyEmpty() {
        XCTAssertEqual(KrakenDefaults.serverURL, "")
        XCTAssertTrue(KrakenDefaults.serverURL.isEmpty)
        XCTAssertEqual(KrakenDefaults.serverURL.utf8.count, 0, "no invisible whitespace or BOM in the default")
    }

    func testEdgeDefaultServerURLNamesNoHost() {
        let url = KrakenDefaults.serverURL
        for fragment in ["http", "://", "127.0.0.1", "localhost", "100.", ".ts.net", ":7777"] {
            XCTAssertFalse(url.contains(fragment), "default server URL carries \(fragment)")
        }
        XCTAssertNil(URL(string: url)?.host, "the default must not parse to any host")
    }
}
