import XCTest
import Foundation
@testable import Kraken

// KronosRemovalEdgeTests — the EDGE dimension of slice I of
// artifacts/50-contract.md (kronos removal). Written blind from the
// contract: a stale `kraken://kronos` deep link (in several spellings) must
// leave the router exactly where it was — no tab change, no pending flag —
// and `Tab` must have exactly the four contract cases, proven at compile
// time by an exhaustive switch with no `default`.
//
// "Sets no pending flag" is asserted structurally: a reflected snapshot of
// the router's stored state before and after the route must be identical,
// which catches a leftover flag under any name.

@MainActor
private func kronosRemovalEdgeSnapshot(_ router: AppRouter) -> [String: String] {
    var out: [String: String] = [:]
    for child in Mirror(reflecting: router).children {
        guard var label = child.label else { continue }
        if label.hasPrefix("_") { label.removeFirst() }
        if label.hasPrefix("$") { continue }
        if label.lowercased().contains("observationregistrar") { continue }
        out[label] = String(describing: child.value)
    }
    return out
}

@MainActor
final class KronosRemovalEdgeTests: XCTestCase {

    /// Compile-level: an exhaustive switch over exactly the four contract
    /// cases with no `default`. A surviving `.kronos` makes this
    /// non-exhaustive (build error); a removed survivor makes it reference a
    /// case that does not exist (build error).
    private func kronosRemovalEdgeName(_ tab: AppRouter.Tab) -> String {
        switch tab {
        case .board: return "board"
        case .sessions: return "sessions"
        case .graph: return "graph"
        case .settings: return "settings"
        }
    }

    func testKronosRemovalTabHasExactlyFourCases() {
        let all: [AppRouter.Tab] = [.board, .sessions, .graph, .settings]
        XCTAssertEqual(all.map(kronosRemovalEdgeName), ["board", "sessions", "graph", "settings"])
        XCTAssertEqual(Set(all).count, 4)
    }

    private func assertKronosRemovalRouteIsInert(_ raw: String, file: StaticString = #filePath, line: UInt = #line) {
        let router = AppRouter()
        router.selectedTab = .sessions
        let before = kronosRemovalEdgeSnapshot(router)
        guard let url = URL(string: raw) else {
            XCTFail("could not build URL \(raw)", file: file, line: line)
            return
        }
        router.handle(url: url)
        XCTAssertEqual(router.selectedTab, .sessions, "\(raw) moved the selected tab", file: file, line: line)
        XCTAssertEqual(kronosRemovalEdgeSnapshot(router), before,
                       "\(raw) changed router state (a pending flag or other leftover)", file: file, line: line)
    }

    func testKronosRemovalKronosURLLeavesTabAndState() {
        assertKronosRemovalRouteIsInert("kraken://kronos")
    }

    func testKronosRemovalUpperCaseHostLeavesTabAndState() {
        assertKronosRemovalRouteIsInert("kraken://KRONOS")
    }

    func testKronosRemovalKronosURLVariantsLeaveTabAndState() {
        for raw in ["kraken://Kronos", "kraken://kronos/", "kraken://kronos/inbox", "kraken://kronos?x=1"] {
            assertKronosRemovalRouteIsInert(raw)
        }
    }

    /// From a different starting tab too — an inert route must not fall
    /// through to "select the board" as a default.
    func testKronosRemovalKronosURLFromGraphTabStaysOnGraph() {
        let router = AppRouter()
        router.selectedTab = .graph
        router.handle(url: URL(string: "kraken://kronos")!)
        XCTAssertEqual(router.selectedTab, .graph)
    }

    /// The surviving tab routes still select their tabs after a stale
    /// kronos link was handled first.
    func testKronosRemovalSurvivingRoutesStillWorkAfterStaleLink() {
        let router = AppRouter()
        router.selectedTab = .sessions
        router.handle(url: URL(string: "kraken://kronos")!)
        for (raw, tab) in [("kraken://board", AppRouter.Tab.board),
                           ("kraken://sessions", .sessions),
                           ("kraken://graph", .graph)] {
            router.handle(url: URL(string: raw)!)
            XCTAssertEqual(router.selectedTab, tab, "\(raw) did not select \(tab)")
        }
    }
}
