import XCTest
import Foundation
@testable import Kraken

// PortabilityContractTests — athena-logic's BLIND contract tests for the
// portability audit's iOS row (artifacts/50-contract.md, Slice S `iOS
// (ios/Kraken)`). Written from the contract text alone; nothing under
// ios/Kraken was opened by the author.
//
// Contract: "KrakenDefaults.serverURL == """ — a fresh install ships no
// server address (no literal tailnet IP baked in) and starts on the first-run
// "set your server" state instead of polling someone else's Mac.
final class PortabilityContractTests: XCTestCase {
    func testContractPortabilityDefaultServerURLIsEmpty() {
        XCTAssertEqual(KrakenDefaults.serverURL, "")
    }

    func testContractPortabilityDefaultServerURLCarriesNoAddress() {
        let url = KrakenDefaults.serverURL
        XCTAssertFalse(url.contains("100."), "default server URL carries a tailnet IP: \(url)")
        XCTAssertFalse(url.contains("http"), "default server URL carries a scheme: \(url)")
    }
}
