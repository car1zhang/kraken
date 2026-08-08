import XCTest
@testable import KrakenMac

/// Exists only so the KrakenMacTests target has a source file and compiles.
/// The real suites are blind-authored; nothing here asserts anything about the
/// app beyond the one thing 50-contract.md makes a ground rule — that
/// `@testable import KrakenMac` resolves and reaches `internal` symbols.
final class TargetSmokeTests: XCTestCase {
    func testModuleIsImportable() {
        XCTAssertEqual(ServerConfig.fallbackURL.absoluteString, "http://127.0.0.1:7777")
    }
}
