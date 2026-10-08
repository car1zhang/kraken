import XCTest
import Foundation
@testable import Kraken

// KronosRemovalContractTests — athena-logic's BLIND logic suite for slice I
// (iOS) of the kronos removal, written from artifacts/50-contract.md alone.
// AppRouter.swift was not read; the reflection-snapshot idiom is borrowed
// from the committed RouterContractTests (a test file), re-declared here
// under this file's own prefix because those helpers are file-private.
//
// Obligations held:
//   * `AppRouter.Tab` has exactly board, sessions, graph, settings — the
//     exhaustive switch below has no `default`, so a surviving `.kronos`
//     case stops this file compiling.
//   * `kraken://kronos` is now an unknown route: from `.sessions` it leaves
//     `selectedTab == .sessions` and sets no pending flag.
//   * `kraken://board|sessions|graph` still select their tabs.
// Deleted symbols (`KronosTabView`, `KronosStatus`, `KronosContext`,
// `APIClient.kronosStatus`) are proven by compilation, not named here.

@MainActor
private func kronosRemovalContractSnapshot(_ router: AppRouter) -> [String: String] {
    var out: [String: String] = [:]
    for child in Mirror(reflecting: router).children {
        guard let rawLabel = child.label else { continue }
        var label = rawLabel
        if label.hasPrefix("_") { label.removeFirst() }
        if label.hasPrefix("$") { continue }
        if label.lowercased().contains("observationregistrar") { continue }
        out[label] = String(describing: child.value)
    }
    return out
}

private func kronosRemovalContractPending(_ snapshot: [String: String]) -> [String: String] {
    snapshot.filter { $0.key.lowercased().contains("pending") }
}

@MainActor
final class KronosRemovalContractTests: XCTestCase {

    private func kronosRemovalURL(_ s: String) throws -> URL {
        try XCTUnwrap(URL(string: s), "not a URL: \(s)")
    }

    /// Contract: `enum Tab: Hashable { case board, sessions, graph, settings }`
    /// — exactly those four. Exhaustiveness (no `default:`) is the assertion.
    func testKronosRemovalTabHasExactlyFourCases() {
        let cases: [AppRouter.Tab] = [.board, .sessions, .graph, .settings]
        XCTAssertEqual(Set(cases).count, 4)
        for tab in cases {
            switch tab {
            case .board, .sessions, .graph, .settings:
                break
            }
        }
    }

    /// Contract: after `selectedTab = .sessions`, `handle(url: kraken://kronos)`
    /// leaves `selectedTab == .sessions` and sets no pending flag.
    func testKronosRemovalKronosURLIsANoOp() throws {
        let router = AppRouter()
        router.selectedTab = .sessions
        let before = kronosRemovalContractSnapshot(router)

        router.handle(url: try kronosRemovalURL("kraken://kronos"))

        XCTAssertEqual(router.selectedTab, .sessions, "kraken://kronos must not move the tab any more")
        let after = kronosRemovalContractSnapshot(router)
        XCTAssertEqual(
            kronosRemovalContractPending(after),
            kronosRemovalContractPending(before),
            "kraken://kronos set a pending flag"
        )
    }

    /// Contract: `kraken://board`, `kraken://sessions`, `kraken://graph` still
    /// select their tabs.
    func testKronosRemovalSurvivingTabRoutesSelect() throws {
        let cases: [(String, AppRouter.Tab, AppRouter.Tab)] = [
            ("kraken://board", .board, .sessions),
            ("kraken://sessions", .sessions, .board),
            ("kraken://graph", .graph, .board),
        ]
        for (route, want, start) in cases {
            let router = AppRouter()
            router.selectedTab = start
            router.handle(url: try kronosRemovalURL(route))
            XCTAssertEqual(router.selectedTab, want, "\(route) selected \(router.selectedTab), want \(want)")
        }
    }
}
