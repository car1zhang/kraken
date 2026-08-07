import XCTest
import Foundation
@testable import Kraken

// RouterContractTests — the stated route outcomes and happy paths of the
// follow-up round's Slice E (nav restructure), written blind from
// artifacts/50-contract.md. Nothing here was derived by reading
// AppRouter.swift; every assertion quotes the frozen contract row it holds
// the router to, and the route strings are transcribed from that table
// rather than from the implementation.
//
// The contract's own testing note scopes this file: "the ONLY
// headless-assertable surface here is `AppRouter` — its `Tab` cases and every
// route's `selectedTab`/pending-flag outcome, including the two new flags,
// the deleted `pendingGraph`/`usage`/`log` symbols (asserting absence = the
// file must not compile references to them; assert the new behavior instead),
// and byte-for-byte preservation of the untouched routes." Tab-bar
// composition, drawers, truncation and carousel behavior are deliberately
// absent — those are the simulator gate, not this one.
//
// The failure mode this suite exists for is silent. A nav restructure that
// drops the usage tab and forgets to land `kraken://usage` on the board, a
// `kraken://log` that sets its flag but leaves the user on the board, or a
// new pending flag that a sibling route also flips — none of these crash,
// none fail a build, and every one of them turns a deep link that used to
// work into a deep link that quietly goes to the wrong screen.
//
// Two assertion styles appear below, on purpose:
//
//   * Frozen symbols (`Tab`, `selectedTab`, `pendingUsage`, `pendingLog`,
//     `handle(url:)`) are asserted directly by name. The contract guarantees
//     they exist with those spellings.
//
//   * The PRESERVED routes (`overview`, `dream`, `job/...`, `session/<pane>`)
//     carry state under property names the frozen contract never spells out —
//     it only says they "behave byte-for-byte as before". Naming those
//     properties here would be guessing at an implementation this file is
//     required to be blind to, so their obligations are asserted structurally
//     instead: a reflected snapshot of the router's stored state, compared
//     before/after a route. That expresses "this route changed exactly its
//     own state and nothing else" without ever spelling a symbol the contract
//     does not freeze. It is also a stronger assertion than a named one:
//     it fails on state a named test would not know to look at.

// MARK: - reflection helpers
//
// `AppRouter` is `@Observable`, whose macro rewrites each stored property
// into a computed property over an underscored stored one, so Mirror's child
// labels arrive as `_pendingUsage` and the registrar as `_$observationRegistrar`.
// Both spellings are normalized here so the snapshot keys read as the plain
// property names the contract uses.

/// Every stored property of the router as `name -> String(describing: value)`.
/// Values are stringified rather than compared as `Any` so that a snapshot is
/// `Equatable` and diffable without knowing any property's type.
@MainActor
private func routerContractSnapshot(_ router: AppRouter) -> [String: String] {
    var out: [String: String] = [:]
    for child in Mirror(reflecting: router).children {
        guard let rawLabel = child.label else { continue }
        var label = rawLabel
        if label.hasPrefix("_") { label.removeFirst() }
        // `_$observationRegistrar` (and any other macro-synthesized storage)
        // is bookkeeping, not router state.
        if label.hasPrefix("$") { continue }
        if label.lowercased().contains("observationregistrar") { continue }
        out[label] = String(describing: child.value)
    }
    return out
}

/// The names whose values differ between two snapshots of the SAME router
/// instance. Same-instance comparison is deliberate: per-instance identity
/// values (a UUID, a token) stay put, so every key returned here is something
/// the route under test actually wrote.
private func routerContractChangedKeys(
    _ before: [String: String],
    _ after: [String: String]
) -> Set<String> {
    var changed = Set<String>()
    for (key, value) in after where before[key] != value { changed.insert(key) }
    for (key, value) in before where after[key] != value { changed.insert(key) }
    return changed
}

/// Snapshot keys that look like route-pending state, matched case-insensitively
/// so `pendingUsage`, `pendingOverview` and any sibling are all caught without
/// this file naming the ones the contract does not freeze.
private func routerContractPendingKeys(_ snapshot: [String: String]) -> [String] {
    snapshot.keys.filter { $0.lowercased().contains("pending") }.sorted()
}

/// A value that reads as "no pending state" regardless of whether the flag is
/// a `Bool` or an `Optional` payload — the contract's two new flags are
/// `Bool`s, the preserved routes' state may be either.
private func routerContractIsUnset(_ described: String) -> Bool {
    described == "false" || described == "nil" || described == "Optional(nil)"
}

@MainActor
final class RouterContractTests: XCTestCase {

    // MARK: - helpers

    private func routerContractRouter() -> AppRouter { AppRouter() }

    private func routerContractURL(
        _ string: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) throws -> URL {
        try XCTUnwrap(URL(string: string), "route string is not a URL: \(string)", file: file, line: line)
    }

    /// Handle one route on a fresh router and hand back the router plus the
    /// before/after snapshots, so a test can assert both the named outcome and
    /// the structural "and nothing else" half in one breath.
    private func routerContractHandle(
        _ string: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) throws -> (router: AppRouter, before: [String: String], after: [String: String]) {
        let router = routerContractRouter()
        let before = routerContractSnapshot(router)
        router.handle(url: try routerContractURL(string, file: file, line: line))
        return (router, before, routerContractSnapshot(router))
    }

    /// The board columns this suite exercises `board/<col>` with. The frozen
    /// contract names the route shape (`board/<col>`) but not the column
    /// vocabulary; these are the `BoardColumn` cases already relied on by the
    /// committed `BoardDragStateTests`, so they are real columns rather than
    /// invented ones. A route naming a column that does not exist would be an
    /// unknown route, which the contract says must no-op — so the assertions
    /// below (board tab, no new pending flags) hold either way.
    private static let routerContractColumns = ["draft", "queued", "working", "blocked", "solved"]

    /// Per-job action suffixes spot-checked under `job/<slug>/...`. Same
    /// reasoning: the contract freezes `job/...` as preserved without
    /// enumerating the suffix vocabulary, and `artifact/<name>` is the one the
    /// round's brief calls out explicitly.
    private static let routerContractJobSuffixes = [
        "",
        "/steer",
        "/feedback",
        "/answer",
        "/artifact/plan.md",
    ]

    // MARK: - Tab shape
    //
    // Contract row `AppRouter.Tab`: "`enum Tab: Hashable { case board,
    // sessions, graph, settings }` — Exactly 4 cases, this order. `usage` and
    // `log` cases are GONE (their screens become drawers)."

    /// The four cases exist, are distinct, and are the ONLY cases.
    ///
    /// "Exactly 4" is enforced at COMPILE time by the exhaustive switch below:
    /// it has no `default`, so re-adding `case usage` or `case log` — or
    /// inventing any fifth case — stops this file from compiling. That is the
    /// contract's stated way to assert a deleted symbol's absence: "the file
    /// must not compile references to them."
    func testTabHasExactlyTheFourContractCases() {
        let cases: [AppRouter.Tab] = [.board, .sessions, .graph, .settings]
        XCTAssertEqual(Set(cases).count, 4, "the four Tab cases must be distinct values")

        for tab in cases {
            switch tab {
            case .board, .sessions, .graph, .settings:
                break   // no `default:` — exhaustiveness IS the "exactly 4" assertion
            }
        }
    }

    /// `Tab` is `Hashable` — the contract's declared conformance, and what a
    /// `TabView(selection:)` binding needs.
    func testTabIsHashable() {
        var seen = Set<AppRouter.Tab>()
        seen.insert(.board)
        seen.insert(.board)
        seen.insert(.settings)
        XCTAssertEqual(seen.count, 2)
    }

    // MARK: - fresh router

    /// Contract: `pendingUsage` and `pendingLog` are declared `= false`, and a
    /// router nobody has routed through must be carrying no route state at all
    /// — a drawer that presents itself on launch is the exact bug a
    /// default-true flag causes.
    func testFreshRouterHasNoPendingState() {
        let router = routerContractRouter()

        XCTAssertFalse(router.pendingUsage, "a fresh AppRouter must not be asking for the Usage drawer")
        XCTAssertFalse(router.pendingLog, "a fresh AppRouter must not be asking for the Log drawer")

        let snapshot = routerContractSnapshot(router)
        XCTAssertFalse(
            snapshot.isEmpty,
            "AppRouter exposed no reflectable stored state; the structural assertions in this file are inert"
        )
        for key in routerContractPendingKeys(snapshot) {
            XCTAssertTrue(
                routerContractIsUnset(snapshot[key] ?? ""),
                "fresh router has \(key) = \(snapshot[key] ?? "?"); every pending flag must start false/nil"
            )
        }
    }

    /// Contract row for `graph`: "`pendingGraph` is DELETED (graph is a tab
    /// now, not a Board sheet)."
    ///
    /// Asserted structurally rather than by name — a named reference would not
    /// compile once the symbol is gone, which is the point, but then this file
    /// could never SAY anything about it. Reflection lets the absence be a
    /// runtime assertion instead.
    func testFreshRouterHasNoPendingGraphState() {
        let snapshot = routerContractSnapshot(routerContractRouter())
        let graphish = snapshot.keys.filter { $0.lowercased() == "pendinggraph" }
        XCTAssertTrue(
            graphish.isEmpty,
            "pendingGraph must be DELETED — graph is a tab now, not a Board sheet (found \(graphish))"
        )
    }

    // MARK: - the three changed routes

    /// Contract row: "`kraken://graph` → `selectedTab = .graph`".
    func testGraphRouteSelectsGraphTab() throws {
        let (router, before, after) = try routerContractHandle("kraken://graph")

        XCTAssertEqual(router.selectedTab, .graph, "kraken://graph must select the Graph TAB")
        XCTAssertFalse(router.pendingUsage)
        XCTAssertFalse(router.pendingLog)

        // Graph is a tab, not a sheet: selecting it is the whole outcome.
        let changed = routerContractChangedKeys(before, after)
        XCTAssertTrue(
            changed.subtracting(["selectedTab"]).isEmpty,
            "kraken://graph must change only the selected tab, changed: \(changed.sorted())"
        )
    }

    /// Contract row `AppRouter.pendingUsage`: "Replaces the usage tab:
    /// `kraken://usage` → `selectedTab = .board` AND `pendingUsage = true`."
    func testUsageRouteSelectsBoardAndSetsPendingUsage() throws {
        let (router, _, _) = try routerContractHandle("kraken://usage")

        XCTAssertEqual(router.selectedTab, .board, "the usage tab is gone; kraken://usage lands on the Board")
        XCTAssertTrue(router.pendingUsage, "kraken://usage must ask the Board to present the Usage drawer")
        XCTAssertFalse(router.pendingLog, "kraken://usage must not also ask Settings for the Log drawer")
    }

    /// Same row, structural half: "a handled route sets ONLY its own pending
    /// state" — usage must not flip `pendingOverview` or any other sibling
    /// flag on its way to the board.
    func testUsageRouteSetsOnlyItsOwnPendingState() throws {
        let (_, before, after) = try routerContractHandle("kraken://usage")

        let changed = routerContractChangedKeys(before, after)
        XCTAssertTrue(
            changed.contains("pendingUsage"),
            "kraken://usage must actually write pendingUsage, changed: \(changed.sorted())"
        )
        XCTAssertTrue(
            changed.subtracting(["selectedTab", "pendingUsage"]).isEmpty,
            "kraken://usage may change only selectedTab + pendingUsage, changed: \(changed.sorted())"
        )
    }

    /// Contract row `AppRouter.pendingLog`: "`kraken://log` (NEW route) →
    /// `selectedTab = .settings` AND `pendingLog = true`."
    func testLogRouteSelectsSettingsAndSetsPendingLog() throws {
        let (router, _, _) = try routerContractHandle("kraken://log")

        XCTAssertEqual(router.selectedTab, .settings, "the log tab is gone; kraken://log lands on Settings")
        XCTAssertTrue(router.pendingLog, "kraken://log must ask Settings to present the Log drawer")
        XCTAssertFalse(router.pendingUsage, "kraken://log must not also ask the Board for the Usage drawer")
    }

    /// Same row, structural half.
    func testLogRouteSetsOnlyItsOwnPendingState() throws {
        let (_, before, after) = try routerContractHandle("kraken://log")

        let changed = routerContractChangedKeys(before, after)
        XCTAssertTrue(
            changed.contains("pendingLog"),
            "kraken://log must actually write pendingLog, changed: \(changed.sorted())"
        )
        XCTAssertTrue(
            changed.subtracting(["selectedTab", "pendingLog"]).isEmpty,
            "kraken://log may change only selectedTab + pendingLog, changed: \(changed.sorted())"
        )
    }

    /// The two new flags are independent of each other in both directions —
    /// the pairwise form of "ONLY its own pending state", stated with named
    /// symbols so it survives any reflection-hostile implementation.
    func testTheTwoDrawerFlagsAreIndependent() throws {
        let usage = try routerContractHandle("kraken://usage").router
        XCTAssertTrue(usage.pendingUsage)
        XCTAssertFalse(usage.pendingLog)

        let log = try routerContractHandle("kraken://log").router
        XCTAssertTrue(log.pendingLog)
        XCTAssertFalse(log.pendingUsage)
    }

    // MARK: - preserved routes
    //
    // Contract row "every other route": "`board`/`board/<col>`/`overview`/
    // `dream`/`job/...`/`sessions`/`session/<pane>`/`settings` behave
    // byte-for-byte as before; unknown hosts still no-op."

    /// `kraken://board` selects the board and asks for no drawer.
    func testBoardRouteSelectsBoardTab() throws {
        let (router, before, after) = try routerContractHandle("kraken://board")

        XCTAssertEqual(router.selectedTab, .board)
        XCTAssertFalse(router.pendingUsage, "the bare board route must not open the Usage drawer")
        XCTAssertFalse(router.pendingLog)

        let changed = routerContractChangedKeys(before, after)
        XCTAssertTrue(
            changed.subtracting(["selectedTab"]).isEmpty,
            "kraken://board is a tab selection only, changed: \(changed.sorted())"
        )
    }

    /// `kraken://board/<col>` for every column still lands on the board tab and
    /// still opens no drawer.
    func testBoardColumnRoutesSelectBoardTab() throws {
        for column in Self.routerContractColumns {
            let (router, _, _) = try routerContractHandle("kraken://board/\(column)")
            XCTAssertEqual(router.selectedTab, .board, "kraken://board/\(column) must select the Board tab")
            XCTAssertFalse(router.pendingUsage, "kraken://board/\(column) must not open the Usage drawer")
            XCTAssertFalse(router.pendingLog, "kraken://board/\(column) must not open the Log drawer")
        }
    }

    /// The column half of `board/<col>` is still honored — two different
    /// columns must not leave the router in identical states, which is what a
    /// restructure that quietly dropped the column path segment would do.
    func testBoardColumnRouteHonorsTheColumn() throws {
        let working = try routerContractHandle("kraken://board/working").after
        let blocked = try routerContractHandle("kraken://board/blocked").after

        XCTAssertNotEqual(
            working,
            blocked,
            "board/working and board/blocked must not produce identical router state — the column is being dropped"
        )
    }

    /// `kraken://overview` is preserved: still the board tab, still its own
    /// state, and — the spot-check the round asks for — still not the usage or
    /// log drawer.
    func testOverviewRouteIsPreserved() throws {
        let (router, before, after) = try routerContractHandle("kraken://overview")

        XCTAssertEqual(router.selectedTab, .board, "the overview strip lives on the Board")
        XCTAssertFalse(router.pendingUsage, "kraken://overview must not open the Usage drawer")
        XCTAssertFalse(router.pendingLog, "kraken://overview must not open the Log drawer")

        let changed = routerContractChangedKeys(before, after)
        XCTAssertFalse(
            changed.isEmpty,
            "kraken://overview must record something — a route that writes nothing cannot present anything"
        )
        XCTAssertTrue(
            changed.isDisjoint(with: ["pendingUsage", "pendingLog"]),
            "kraken://overview may not touch the two drawer flags, changed: \(changed.sorted())"
        )
    }

    /// `kraken://dream` is preserved with the same shape.
    func testDreamRouteIsPreserved() throws {
        let (router, before, after) = try routerContractHandle("kraken://dream")

        XCTAssertEqual(router.selectedTab, .board, "dream is presented from the Board")
        XCTAssertFalse(router.pendingUsage)
        XCTAssertFalse(router.pendingLog)

        let changed = routerContractChangedKeys(before, after)
        XCTAssertFalse(changed.isEmpty, "kraken://dream must record something to present")
        XCTAssertTrue(
            changed.isDisjoint(with: ["pendingUsage", "pendingLog"]),
            "kraken://dream may not touch the two drawer flags, changed: \(changed.sorted())"
        )
    }

    /// `kraken://job/<slug>` is preserved: board tab, records the job, no drawer.
    func testJobRouteIsPreserved() throws {
        let (router, before, after) = try routerContractHandle("kraken://job/ios-dragging-fix")

        XCTAssertEqual(router.selectedTab, .board, "job detail is presented from the Board")
        XCTAssertFalse(router.pendingUsage)
        XCTAssertFalse(router.pendingLog)

        let changed = routerContractChangedKeys(before, after)
        XCTAssertFalse(changed.isEmpty, "kraken://job/<slug> must record the pending job")
        XCTAssertTrue(
            changed.isDisjoint(with: ["pendingUsage", "pendingLog"]),
            "kraken://job/<slug> may not touch the two drawer flags, changed: \(changed.sorted())"
        )
    }

    /// The slug half is still honored — two jobs must not collapse to the same
    /// router state.
    func testJobRouteHonorsTheSlug() throws {
        let first = try routerContractHandle("kraken://job/ios-dragging-fix").after
        let second = try routerContractHandle("kraken://job/btw-on-jobs").after

        XCTAssertNotEqual(
            first,
            second,
            "two different job slugs must not produce identical router state"
        )
    }

    /// Every action suffix under `job/<slug>` — including `artifact/<name>` —
    /// still resolves to the board tab and still opens neither drawer.
    func testJobActionSuffixRoutesArePreserved() throws {
        for suffix in Self.routerContractJobSuffixes {
            let route = "kraken://job/ios-dragging-fix\(suffix)"
            let (router, before, after) = try routerContractHandle(route)

            XCTAssertEqual(router.selectedTab, .board, "\(route) must select the Board tab")
            XCTAssertFalse(router.pendingUsage, "\(route) must not open the Usage drawer")
            XCTAssertFalse(router.pendingLog, "\(route) must not open the Log drawer")

            let changed = routerContractChangedKeys(before, after)
            XCTAssertFalse(changed.isEmpty, "\(route) must record the pending job")
        }
    }

    /// An action suffix is not silently discarded: `job/<slug>` and
    /// `job/<slug>/artifact/<name>` must leave distinguishable state, or the
    /// artifact deep link opens the plain job detail instead.
    func testJobArtifactSuffixIsNotDiscarded() throws {
        let plain = try routerContractHandle("kraken://job/ios-dragging-fix").after
        let artifact = try routerContractHandle("kraken://job/ios-dragging-fix/artifact/plan.md").after

        XCTAssertNotEqual(
            plain,
            artifact,
            "job/<slug> and job/<slug>/artifact/<name> must not produce identical router state"
        )
    }

    /// `kraken://sessions` is preserved and now points at the second of four tabs.
    func testSessionsRouteSelectsSessionsTab() throws {
        let (router, before, after) = try routerContractHandle("kraken://sessions")

        XCTAssertEqual(router.selectedTab, .sessions)
        XCTAssertFalse(router.pendingUsage)
        XCTAssertFalse(router.pendingLog)

        let changed = routerContractChangedKeys(before, after)
        XCTAssertTrue(
            changed.subtracting(["selectedTab"]).isEmpty,
            "kraken://sessions is a tab selection only, changed: \(changed.sorted())"
        )
    }

    /// `kraken://session/<pane>` is preserved: sessions tab, pane recorded.
    /// The pane travels BARE in the route (`session/3`); the leading `%` of the
    /// tmux pane id is implicit, which is what keeps the scheme URL-safe.
    func testSessionPaneRouteSelectsSessionsTabAndRecordsThePane() throws {
        let (router, before, after) = try routerContractHandle("kraken://session/3")

        XCTAssertEqual(router.selectedTab, .sessions)
        XCTAssertFalse(router.pendingUsage)
        XCTAssertFalse(router.pendingLog)

        let changed = routerContractChangedKeys(before, after)
        XCTAssertFalse(changed.isEmpty, "kraken://session/<pane> must record the pane it should open")
    }

    /// The percent-prefix rule, pinned as PRESERVED behavior.
    ///
    /// Contract row "every other route": `session/<pane>` "behave[s]
    /// byte-for-byte as before". The before-behavior is an unconditional
    /// `"%" + pane`: the route carries the pane BARE (`session/3`) and the
    /// leading `%` of a tmux pane id is IMPLICIT, which is what keeps the URL
    /// scheme percent-escape-free. So:
    ///
    ///   * `session/3` → pane `%3` — the documented form.
    ///   * `session/%3` (written `%253` in a URL) → pane `%%3` — outside the
    ///     documented scheme, and the double prefix is the byte-for-byte
    ///     consequence of the unconditional rule.
    ///
    /// An earlier revision of this file asserted the opposite (that the two
    /// forms normalize to one pane). That was a requirement nobody set:
    /// normalizing would CHANGE preserved behavior, which this round forbids.
    /// The assertion is inverted rather than deleted because pinning the real
    /// rule is the stronger test — it fails if someone "helpfully" adds a
    /// hasPrefix check and silently moves the scheme.
    ///
    /// Asserted over snapshot VALUES rather than by property name: the pane's
    /// storage is not part of the frozen surface, so this file does not spell
    /// it, and a rename stays a refactor instead of a false failure.
    func testSessionPaneRoutePercentIsImplicitAndUnconditional() throws {
        let bare = try routerContractHandle("kraken://session/3")
        XCTAssertEqual(bare.router.selectedTab, .sessions)
        XCTAssertTrue(
            bare.after.values.contains(#"Optional("%3")"#),
            "session/3 must resolve to pane %3 — the leading percent is implicit; state: \(bare.after)"
        )

        let encoded = try routerContractHandle("kraken://session/%253")
        XCTAssertEqual(encoded.router.selectedTab, .sessions)
        XCTAssertTrue(
            encoded.after.values.contains(#"Optional("%%3")"#),
            "the percent prefix is unconditional, so session/%3 preserves as pane %%3; state: \(encoded.after)"
        )

        XCTAssertNotEqual(
            bare.after,
            encoded.after,
            "the two forms are NOT interchangeable — normalizing them would change preserved behavior"
        )
    }

    /// Two different panes stay distinguishable.
    func testSessionPaneRouteHonorsThePane() throws {
        let third = try routerContractHandle("kraken://session/3").after
        let fourth = try routerContractHandle("kraken://session/4").after

        XCTAssertNotEqual(third, fourth, "two different panes must not produce identical router state")
    }

    /// `kraken://settings` is preserved and — now that the Log drawer lives
    /// there — must NOT present it uninvited. Only `kraken://log` does that.
    func testSettingsRouteSelectsSettingsTabWithoutTheLogDrawer() throws {
        let (router, before, after) = try routerContractHandle("kraken://settings")

        XCTAssertEqual(router.selectedTab, .settings)
        XCTAssertFalse(router.pendingLog, "plain kraken://settings must not present the Log drawer")
        XCTAssertFalse(router.pendingUsage)

        let changed = routerContractChangedKeys(before, after)
        XCTAssertTrue(
            changed.subtracting(["selectedTab"]).isEmpty,
            "kraken://settings is a tab selection only, changed: \(changed.sorted())"
        )
    }

    // MARK: - unknown hosts

    /// Contract: "unknown hosts still no-op." Asserted over the whole
    /// reflected state, so a route that quietly writes a flag on its way to
    /// doing nothing is caught too.
    func testUnknownHostsNoOp() throws {
        for route in ["kraken://usages", "kraken://logs", "kraken://nope", "kraken://"] {
            guard let url = URL(string: route) else { continue }
            let router = routerContractRouter()
            let before = routerContractSnapshot(router)
            router.handle(url: url)
            XCTAssertEqual(routerContractSnapshot(router), before, "\(route) must be a no-op")
        }
    }

    /// The two retired tab hosts are gone as CASES but their routes still have
    /// to work — this is the regression that a "delete the tab, delete the
    /// route" reading of the contract produces.
    func testRetiredTabRoutesStillRoute() throws {
        let usage = try routerContractHandle("kraken://usage").router
        XCTAssertEqual(usage.selectedTab, .board)
        XCTAssertTrue(usage.pendingUsage, "kraken://usage must not become an unknown host")

        let log = try routerContractHandle("kraken://log").router
        XCTAssertEqual(log.selectedTab, .settings)
        XCTAssertTrue(log.pendingLog, "kraken://log must not become an unknown host")
    }

    // MARK: - sequences

    /// Routing twice leaves the second route's outcome, not a union of both —
    /// the board must not still be holding a stale Usage-drawer request after
    /// the user follows a job link.
    func testLaterRouteDoesNotInheritTheEarlierRoutesDrawer() throws {
        let router = routerContractRouter()
        router.handle(url: try routerContractURL("kraken://usage"))
        XCTAssertTrue(router.pendingUsage)

        router.handle(url: try routerContractURL("kraken://graph"))
        XCTAssertEqual(router.selectedTab, .graph)

        router.handle(url: try routerContractURL("kraken://log"))
        XCTAssertEqual(router.selectedTab, .settings)
        XCTAssertTrue(router.pendingLog)
    }

    /// A drawer flag is consumable: the view that presents the drawer clears
    /// it, and a second identical route can raise it again. (The clear is the
    /// view's job — this asserts only that the flag is writable state, which
    /// "BoardView consumes+clears it" requires.)
    func testDrawerFlagsAreConsumableAndReRaisable() throws {
        let router = routerContractRouter()

        router.handle(url: try routerContractURL("kraken://usage"))
        XCTAssertTrue(router.pendingUsage)
        router.pendingUsage = false                      // BoardView consuming the request
        XCTAssertFalse(router.pendingUsage)
        router.handle(url: try routerContractURL("kraken://usage"))
        XCTAssertTrue(router.pendingUsage, "a second kraken://usage must raise the drawer again")

        router.handle(url: try routerContractURL("kraken://log"))
        XCTAssertTrue(router.pendingLog)
        router.pendingLog = false                        // SettingsView consuming the request
        XCTAssertFalse(router.pendingLog)
        router.handle(url: try routerContractURL("kraken://log"))
        XCTAssertTrue(router.pendingLog, "a second kraken://log must raise the drawer again")
    }
}
