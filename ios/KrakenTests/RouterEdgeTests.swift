import XCTest
@testable import Kraken

// AppRouter's refusal surface — the deep-link boundary.
//
// Everything here is derived from the frozen contract's "Follow-up round"
// AppRouter rows: the four-case `Tab`, the two new pending flags
// (`pendingUsage`, `pendingLog`), the graph route becoming a tab, and the
// blanket "every other route ... behaves byte-for-byte as before; unknown hosts
// still no-op".
//
// A router is a parser sitting on an attacker-shaped input: the URL arrives
// from outside the app (a tapped link, a shortcut, a pasted string), so the
// interesting behaviour is not the happy route but what it does with a scheme
// it doesn't own, a host it doesn't know, and a route whose payload component
// is missing or garbage. The contract's promise for all of those is the same
// one word — no-op — and a no-op has a precise meaning here: `selectedTab` is
// left exactly where the user had it, and no pending flag is raised. Raising a
// flag on a garbage URL is worse than ignoring it, because a pending flag is
// consumed later by a view that will present a drawer for a route nobody asked
// for.
//
// Naming: every helper in this file is `edge`-prefixed so it cannot collide
// with the concurrently-authored RouterContractTests at package scope.
//
// The class is `@MainActor` because `AppRouter` is UI-observable state; if it
// carries main-actor isolation this is required, and if it does not this is
// merely harmless.
@MainActor
final class RouterEdgeTests: XCTestCase {

    // MARK: - Helpers

    /// A router parked on a deliberately NON-default tab. Every no-op assertion
    /// below is only meaningful against a starting tab the route under test
    /// would never select on its own — starting on `.board` would make a
    /// "stayed on board" assertion indistinguishable from "routed to board".
    private func edgeRouter(startingAt tab: AppRouter.Tab) -> AppRouter {
        let router = AppRouter()
        router.selectedTab = tab
        return router
    }

    private func edgeURL(_ string: String, file: StaticString = #filePath, line: UInt = #line) -> URL {
        guard let url = URL(string: string) else {
            XCTFail("not a URL at all: \(string)", file: file, line: line)
            return URL(string: "kraken://unroutable-placeholder")!
        }
        return url
    }

    /// The full "nothing was raised" claim. Kept in one place so a no-op test
    /// asserts the WHOLE pending surface and not just the flag it happens to be
    /// thinking about — a router that ignored `kraken://nonsense` for the tab
    /// but still set `pendingJobSlug` would pass a narrower check.
    private func edgeAssertNothingPending(
        _ router: AppRouter,
        _ what: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) {
        XCTAssertFalse(router.pendingUsage, "\(what): pendingUsage", file: file, line: line)
        XCTAssertFalse(router.pendingLog, "\(what): pendingLog", file: file, line: line)
        XCTAssertNil(router.pendingBoardColumn, "\(what): pendingBoardColumn", file: file, line: line)
        XCTAssertNil(router.pendingJobSlug, "\(what): pendingJobSlug", file: file, line: line)
        XCTAssertNil(router.pendingSessionPane, "\(what): pendingSessionPane", file: file, line: line)
    }

    // MARK: - The shape of the tab set

    // "Exactly 4 cases, this order. `usage` and `log` cases are GONE." The
    // *order* is not reachable from a test — the contract types `Tab` as
    // `Hashable`, not `CaseIterable`, so there is no `allCases` to index and no
    // way to observe declaration order at runtime. What IS assertable is the
    // count and the distinctness: four cases that are four DIFFERENT values.
    // Distinctness is the part that actually matters to routing — two cases
    // that compared equal would make every tab assertion in this file vacuous.
    //
    // The deleted `usage`/`log` cases are asserted by absence: a reference to
    // `AppRouter.Tab.usage` would not compile, so it cannot appear here. This
    // test's job is only to pin what remains.
    func testEdgeTabIsExactlyFourDistinctCases() {
        let tabs: Set<AppRouter.Tab> = [.board, .sessions, .graph, .settings]
        XCTAssertEqual(tabs.count, 4, "four distinct Tab cases")
    }

    // A fresh router has raised nothing. Both new flags are contractually
    // `= false` at rest ("var pendingUsage = false", "var pendingLog = false"),
    // and the pre-existing pending payloads are empty until a route fills them.
    // Without this, every "no flag was raised" test below could be passing on a
    // router that had simply never been able to raise one.
    func testEdgeFreshRouterHasNothingPending() {
        edgeAssertNothingPending(AppRouter(), "fresh router")
    }

    // MARK: - The control: known routes DO move the router

    // The no-op tests are refusals, and a refusal is only evidence if the thing
    // refusing is capable of saying yes. `handle(url:)` that did nothing at all
    // would pass every other boundary test in this file. This pins the four
    // host→tab routes the follow-up contract states outright:
    //   kraken://board    → .board
    //   kraken://sessions → .sessions
    //   kraken://graph    → .graph   (pendingGraph is DELETED; graph is a tab)
    //   kraken://settings → .settings
    // Each is routed from a different starting tab so none can pass by accident.
    func testEdgeKnownHostsRouteSoTheRefusalsBelowMeanSomething() {
        let cases: [(String, AppRouter.Tab, AppRouter.Tab)] = [
            ("kraken://board", .settings, .board),
            ("kraken://sessions", .board, .sessions),
            ("kraken://graph", .board, .graph),
            ("kraken://settings", .board, .settings),
        ]
        for (string, start, expected) in cases {
            let router = edgeRouter(startingAt: start)
            router.handle(url: edgeURL(string))
            XCTAssertEqual(router.selectedTab, expected, "\(string) from \(start)")
        }
    }

    // MARK: - Scheme boundary

    // A URL this app does not own is not a route. The contract's route table is
    // written entirely in `kraken://` terms, so an `https` URL matches no row —
    // it falls to "unknown ... still no-op". This is the boundary that keeps a
    // web link named after one of our hosts (https://board, and the nastier
    // https://kraken/usage, where "kraken" is the HOST rather than the scheme)
    // from steering the app.
    func testEdgeNonKrakenSchemesAreRefused() {
        for string in [
            "https://board",
            "https://usage",
            "https://kraken/usage",
            "http://log",
            "krakenx://board",
            "file:///board",
        ] {
            let router = edgeRouter(startingAt: .sessions)
            router.handle(url: edgeURL(string))
            XCTAssertEqual(router.selectedTab, .sessions, "\(string) must not steer the tab")
            edgeAssertNothingPending(router, string)
        }
    }

    // Scheme comparison is case-insensitive: the scheme is lowercased before it
    // is compared, so `KRAKEN://board` is the same route as `kraken://board`.
    // The follow-up contract does not restate scheme handling, which is exactly
    // the point — board is in the "every other route ... unchanged" row, and
    // "unchanged" carries the old file's case-folding along with everything
    // else. This matters in the field because a scheme typed by a human, or
    // round-tripped through a system that upper-cases, still has to land.
    //
    // NOTE (reported as an inference, not a contract quotation): the frozen text
    // in 50-contract.md nowhere spells out "the scheme is lowercased". This test
    // encodes the round-trip claim handed down with the surface assignment. If
    // it fails, the disagreement is about the contract's silence, not about the
    // implementation being sloppy.
    func testEdgeUppercaseSchemeStillRoutes() {
        let router = edgeRouter(startingAt: .sessions)
        router.handle(url: edgeURL("KRAKEN://board"))
        XCTAssertEqual(router.selectedTab, .board, "KRAKEN://board is the board route")
    }

    // Host case-folding is the one obligation on this surface I could NOT
    // derive from the contract text: the follow-up section says the untouched
    // routes behave "byte-for-byte as before" without ever stating what "before"
    // did with an upper-cased host, and the implementation file is off-limits to
    // a blind author. Rather than guess a direction and manufacture either a
    // false pass or a false failure, this asserts the DISJUNCTION the contract
    // does license: whichever way host matching folds, `KRAKEN://USAGE` is
    // either the usage route (board tab + pendingUsage) or an unknown host (a
    // total no-op) — and never a third thing, e.g. landing on some other tab, or
    // raising pendingUsage while leaving the tab where it was, or raising a flag
    // belonging to a different route.
    //
    // The half-raised state is the real hazard: `pendingUsage` true with the tab
    // left on `.sessions` means BoardView never mounts to consume the flag, so
    // the drawer fires on whatever unrelated navigation next brings Board up.
    func testEdgeUppercaseHostIsEitherTheUsageRouteOrANoOpAndNeverHalfOfEach() {
        let router = edgeRouter(startingAt: .sessions)
        router.handle(url: edgeURL("KRAKEN://USAGE"))

        if router.pendingUsage {
            XCTAssertEqual(
                router.selectedTab, .board,
                "KRAKEN://USAGE raised pendingUsage, so it took the usage route and owes the board tab"
            )
            XCTAssertFalse(router.pendingLog, "usage route must not raise pendingLog")
            XCTAssertNil(router.pendingBoardColumn, "usage route must not raise pendingBoardColumn")
            XCTAssertNil(router.pendingJobSlug, "usage route must not raise pendingJobSlug")
            XCTAssertNil(router.pendingSessionPane, "usage route must not raise pendingSessionPane")
        } else {
            XCTAssertEqual(
                router.selectedTab, .sessions,
                "KRAKEN://USAGE was not taken as the usage route, so it is an unknown host and must no-op"
            )
            edgeAssertNothingPending(router, "KRAKEN://USAGE as unknown host")
        }
    }

    // MARK: - Unknown hosts

    // "unknown hosts still no-op" — the follow-up round's one explicitly
    // restated refusal. A no-op is asserted from a NON-default starting tab
    // (`.settings`) so "the tab did not move" cannot be confused with "the tab
    // was reset to its default". The near-misses matter more than the nonsense:
    // `boards`, `usages`, `logs`, `graphs` are one keystroke off a live route
    // and are exactly what a prefix-match bug would swallow, and `usage`/`log`
    // themselves must NOT be resurrected as tabs by their own routes reaching a
    // deleted case.
    func testEdgeUnknownHostsAreTotalNoOps() {
        for string in [
            "kraken://nonsense",
            "kraken://boards",
            "kraken://usages",
            "kraken://logs",
            "kraken://graphs",
            "kraken://sessionss",
            "kraken://Board%20",
            "kraken://",
            "kraken:///board",
        ] {
            let router = edgeRouter(startingAt: .settings)
            router.handle(url: edgeURL(string))
            XCTAssertEqual(router.selectedTab, .settings, "\(string) must not steer the tab")
            edgeAssertNothingPending(router, string)
        }
    }

    // MARK: - Payload boundaries on the preserved routes

    // `board/<col>` where `<col>` is not one of the six columns. The host half
    // of the URL is still a legitimate route, so the tab obligation holds — the
    // user asked for the board and gets the board. What must NOT survive is the
    // column: `pendingBoardColumn` is consumed by BoardView to scroll the
    // carousel to a named page, and a value that names no page is a scroll
    // target that cannot be honoured. This mirrors the theme contract's own
    // refusal idiom for unrecognized names ("unrecognized job color name → nil"):
    // the lookup answers nil rather than inventing a fallback.
    func testEdgeGarbageBoardColumnLandsOnBoardWithNoPendingColumn() {
        for string in [
            "kraken://board/not-a-column",
            "kraken://board/%20",
            "kraken://board/BLOCKED%20%2F%20done",
            "kraken://board/12345",
        ] {
            let router = edgeRouter(startingAt: .settings)
            router.handle(url: edgeURL(string))
            XCTAssertEqual(router.selectedTab, .board, "\(string) still asked for the board")
            XCTAssertNil(router.pendingBoardColumn, "\(string) names no real column")
            XCTAssertFalse(router.pendingUsage, "\(string) must not raise pendingUsage")
            XCTAssertFalse(router.pendingLog, "\(string) must not raise pendingLog")
        }
    }

    // `kraken://board` with a bare trailing slash and no column. Same claim as
    // above, split out because an empty path component is the case a naive
    // `pathComponents` split is most likely to hand downstream as an empty
    // string rather than dropping — and an empty-string pending column is a
    // scroll target for a column named "".
    func testEdgeBoardWithEmptyColumnComponentRaisesNoPendingColumn() {
        for string in ["kraken://board/", "kraken://board//"] {
            let router = edgeRouter(startingAt: .settings)
            router.handle(url: edgeURL(string))
            XCTAssertEqual(router.selectedTab, .board, "\(string) still asked for the board")
            XCTAssertNil(router.pendingBoardColumn, "\(string) carries no column")
        }
    }

    // `kraken://job` with no slug at all, and with an empty slug component.
    // `pendingJobSlug` is a fetch key — a view consumes it by asking the daemon
    // for that job — so an empty or absent slug must stay nil rather than
    // becoming `""`. A `""` slug is the shape that produces a request for a job
    // that cannot exist and a detail sheet with nothing in it.
    func testEdgeJobWithoutASlugRaisesNoPendingJobSlug() {
        for string in ["kraken://job", "kraken://job/", "kraken://job//"] {
            let router = edgeRouter(startingAt: .settings)
            router.handle(url: edgeURL(string))
            XCTAssertNil(router.pendingJobSlug, "\(string) carries no slug")
            XCTAssertFalse(router.pendingUsage, "\(string) must not raise pendingUsage")
            XCTAssertFalse(router.pendingLog, "\(string) must not raise pendingLog")
        }
    }

    // `job/<slug>/artifact` with the artifact NAME missing. The deepest route on
    // this surface, and the one with the most components to mis-index: a router
    // that reads the name positionally can hand the literal "artifact" — or an
    // empty string — downstream as the artifact to open.
    //
    // UNDER-SPECIFIED, and asserted only as far as the contract reaches: the
    // frozen text names no symbol for the artifact action (it is folded into the
    // "job/... unchanged" row), so a blind author has nothing to read the
    // outcome from. What is assertable is that a nameless artifact URL does not
    // leak into a DIFFERENT route's state — it may not raise the usage or log
    // drawers, a board column, or a sessions pane. The artifact-action symbol
    // itself is reported as untestable from the contract alone.
    func testEdgeArtifactRouteWithoutANameLeaksIntoNoOtherRoute() {
        for string in [
            "kraken://job/some-slug/artifact",
            "kraken://job/some-slug/artifact/",
            "kraken://job//artifact/name.md",
        ] {
            let router = edgeRouter(startingAt: .settings)
            router.handle(url: edgeURL(string))
            XCTAssertFalse(router.pendingUsage, "\(string) must not raise pendingUsage")
            XCTAssertFalse(router.pendingLog, "\(string) must not raise pendingLog")
            XCTAssertNil(router.pendingBoardColumn, "\(string) must not raise pendingBoardColumn")
            XCTAssertNil(router.pendingSessionPane, "\(string) must not raise pendingSessionPane")
        }
    }

    // `kraken://session` with the pane missing. `session/<pane>` is a preserved
    // route, so the host alone still means "the sessions screen"; only the pane
    // selection is absent. `pendingSessionPane` staying nil is what leaves the
    // pager on whichever page it was already showing, instead of jumping to a
    // page named by an empty string.
    func testEdgeSessionWithoutAPaneLandsOnSessionsWithNoPendingPane() {
        for string in ["kraken://session", "kraken://session/", "kraken://session//"] {
            let router = edgeRouter(startingAt: .board)
            router.handle(url: edgeURL(string))
            XCTAssertEqual(router.selectedTab, .sessions, "\(string) still asked for sessions")
            XCTAssertNil(router.pendingSessionPane, "\(string) names no pane")
            XCTAssertFalse(router.pendingUsage, "\(string) must not raise pendingUsage")
            XCTAssertFalse(router.pendingLog, "\(string) must not raise pendingLog")
        }
    }

    // MARK: - Flag lifetime

    // `kraken://usage` twice. The flag is a request, not a toggle: the contract
    // gives the CLEARING job to BoardView ("consumes+clears it"), so a second
    // arrival before the consumer has run must leave the request standing. A
    // router that flipped the boolean would turn a double-tapped link into a
    // usage drawer that never opens — the second route silently cancelling the
    // first.
    func testEdgeUsageRouteIsIdempotent() {
        let router = edgeRouter(startingAt: .sessions)
        for attempt in 1...3 {
            router.handle(url: edgeURL("kraken://usage"))
            XCTAssertTrue(router.pendingUsage, "pendingUsage after route #\(attempt)")
            XCTAssertEqual(router.selectedTab, .board, "usage route owes the board tab, route #\(attempt)")
        }
        XCTAssertFalse(router.pendingLog, "the usage route never touches pendingLog")
    }

    // `kraken://log` twice, the same claim on the other new flag — it lands on
    // settings, not board, so this also pins that the two flags are not two
    // names for one piece of state.
    func testEdgeLogRouteIsIdempotent() {
        let router = edgeRouter(startingAt: .board)
        for attempt in 1...3 {
            router.handle(url: edgeURL("kraken://log"))
            XCTAssertTrue(router.pendingLog, "pendingLog after route #\(attempt)")
            XCTAssertEqual(router.selectedTab, .settings, "log route owes the settings tab, route #\(attempt)")
        }
        XCTAssertFalse(router.pendingUsage, "the log route never touches pendingUsage")
    }

    // Two drawer routes in sequence. Neither flag may clear the other — they are
    // consumed by two different screens (BoardView and SettingsView) that mount
    // independently, so an unconsumed request has to survive an unrelated route
    // arriving in between. The tab, by contrast, is single-valued: it settles on
    // whatever the LAST route asked for. Run in both orders so neither outcome
    // can be an artifact of ordering.
    func testEdgeUsageAndLogFlagsAreMutuallyIsolatedAndTheLastRouteOwnsTheTab() {
        let logThenUsage = edgeRouter(startingAt: .graph)
        logThenUsage.handle(url: edgeURL("kraken://log"))
        logThenUsage.handle(url: edgeURL("kraken://usage"))
        XCTAssertTrue(logThenUsage.pendingLog, "log→usage: the log request survives")
        XCTAssertTrue(logThenUsage.pendingUsage, "log→usage: the usage request is raised")
        XCTAssertEqual(logThenUsage.selectedTab, .board, "log→usage: usage routed last, so board")

        let usageThenLog = edgeRouter(startingAt: .graph)
        usageThenLog.handle(url: edgeURL("kraken://usage"))
        usageThenLog.handle(url: edgeURL("kraken://log"))
        XCTAssertTrue(usageThenLog.pendingUsage, "usage→log: the usage request survives")
        XCTAssertTrue(usageThenLog.pendingLog, "usage→log: the log request is raised")
        XCTAssertEqual(usageThenLog.selectedTab, .settings, "usage→log: log routed last, so settings")
    }

    // An unknown host arriving between a drawer route and its consumer must not
    // sweep the pending request away. The no-op tests above prove an unknown
    // host raises nothing; this proves the stronger claim that it also DESTROYS
    // nothing — a no-op is not a reset.
    func testEdgeUnknownHostDoesNotClearAnAlreadyPendingRequest() {
        let router = edgeRouter(startingAt: .sessions)
        router.handle(url: edgeURL("kraken://usage"))
        XCTAssertTrue(router.pendingUsage, "precondition: usage is pending")

        router.handle(url: edgeURL("kraken://nonsense"))
        XCTAssertTrue(router.pendingUsage, "an unknown host must not clear the pending usage request")
        XCTAssertEqual(router.selectedTab, .board, "an unknown host must not move the tab either")
    }

    // The graph route is a TAB now, not a Board sheet: it must select `.graph`
    // and, unlike the two drawer routes, raise no pending request at all. The
    // deleted `pendingGraph` is asserted by absence — naming it here would not
    // compile — so what is left to pin is that its behaviour did not migrate
    // onto one of the surviving flags.
    func testEdgeGraphRouteRaisesNoPendingRequest() {
        let router = edgeRouter(startingAt: .settings)
        router.handle(url: edgeURL("kraken://graph"))
        XCTAssertEqual(router.selectedTab, .graph, "graph is a tab")
        edgeAssertNothingPending(router, "kraken://graph")
    }
}
