import XCTest
import Foundation
@testable import Kraken

// KronosContractTests — the stated obligations of round 5's Slice E (the iOS
// kronos tab), written BLIND from artifacts/50-contract.md's "Amendments
// (round 5 — iOS kronos tab)" table plus ios/API.md's documented
// `GET /api/kronos` wire shape. Nothing here was derived by reading
// Models.swift, AppRouter.swift, APIClient.swift or KronosTabView.swift;
// every assertion quotes the contract row it holds the implementation to,
// and every JSON fixture is transcribed from the API doc's key table rather
// than from a live response.
//
// Scope, per the round's blind-test note: the headless-assertable surface is
// the wire decode (`KronosStatus`/`KronosContext`), the three free functions
// the contract declares as exact mirrors of web/_ui/src/lib/kronos.ts
// (`kronosMeterState`, `kronosFillPct`, `kronosThresholds`), and the one new
// router row (`kraken://kronos`). Tab ORDER, the off/pending/live view
// states and the SessionDetailView hand-off are marked "review-checked, not
// blind-testable" in the contract itself and are deliberately absent.
// `APIClient.kronosStatus` is likewise absent by instruction: its existence
// is checked by the app compiling, and a test for it could only reach the
// network, which this suite may not do.
//
// The failure mode this suite exists for is a meter that lies. Every band
// boundary here is INCLUSIVE, the label carries exactly one decimal, and the
// percentage is passed through UNCLAMPED while only the FILL is clamped —
// four rules that no crash and no build failure will ever surface, and each
// of which, got wrong, makes the phone disagree with the board about how
// close kronos is to a compaction.

// MARK: - reflection helpers
//
// Mirrors RouterContractTests' pattern (its own helpers are file-private, so
// they cannot be reused across files and are replicated here under distinct
// names). `AppRouter` is `@Observable`, whose macro rewrites each stored
// property into a computed property over an underscored stored one, so
// Mirror's child labels arrive as `_selectedTab` and the registrar as
// `_$observationRegistrar`. Both spellings are normalized away so the
// snapshot keys read as the plain property names the contract uses.

/// Every stored property of the router as `name -> String(describing: value)`.
/// Values are stringified rather than compared as `Any` so a snapshot is
/// `Equatable` and diffable without this file knowing any property's type —
/// which is what lets the "and changes NOTHING else" half of the kronos route
/// row be asserted without naming a single symbol the contract does not
/// freeze.
@MainActor
private func kronosContractRouterSnapshot(_ router: AppRouter) -> [String: String] {
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

/// The names whose values differ between two snapshots of the SAME router
/// instance. Same-instance comparison is deliberate: per-instance identity
/// values stay put, so every key returned here is something the route under
/// test actually wrote.
private func kronosContractChangedKeys(
    _ before: [String: String],
    _ after: [String: String]
) -> Set<String> {
    var changed = Set<String>()
    for (key, value) in after where before[key] != value { changed.insert(key) }
    for (key, value) in before where after[key] != value { changed.insert(key) }
    return changed
}

/// Snapshot keys that look like route-pending state, matched
/// case-insensitively so every pending flag is caught without this file
/// naming ones the contract does not freeze.
private func kronosContractPendingKeys(_ snapshot: [String: String]) -> [String] {
    snapshot.keys.filter { $0.lowercased().contains("pending") }.sorted()
}

/// A value that reads as "no pending state" whether the flag is a `Bool` or an
/// `Optional` payload.
private func kronosContractIsUnset(_ described: String) -> Bool {
    described == "false" || described == "nil" || described == "Optional(nil)"
}

@MainActor
final class KronosContractTests: XCTestCase {

    // MARK: - fixtures

    /// The full documented payload: all five always-present keys plus a
    /// present `context`. Values are distinct and non-default so a field
    /// silently wired to the wrong key, or defaulted, cannot pass.
    private static let kronosContractFullJSON = """
    {
      "enabled": true,
      "live": true,
      "session_id": "b7c1e2f4-0a5d-4c11-9f3e-2ad6c8e10001",
      "pane": "%17",
      "spawned_at": 1780000123456,
      "context": {"pct": 62.34, "window": 200000, "at": 1780000200000}
    }
    """

    /// The same payload with the `context` KEY omitted entirely — the wire's
    /// only spelling of "kronos has never reported its context".
    private static let kronosContractNoContextJSON = """
    {
      "enabled": false,
      "live": false,
      "session_id": "",
      "pane": "",
      "spawned_at": 0
    }
    """

    private func kronosContractDecode(_ json: String) throws -> KronosStatus {
        try JSONDecoder().decode(KronosStatus.self, from: Data(json.utf8))
    }

    // MARK: - KronosStatus decoding
    //
    // Contract row `KronosStatus`: "`enabled: Bool`, `live: Bool`,
    // `sessionID: String` (key `session_id`), `pane: String`,
    // `spawnedAt: Int64` (key `spawned_at`), `context: KronosContext?`
    // (key `context`) — Decodes `/api/kronos` exactly as ios/API.md documents
    // it."

    /// Every one of the six documented keys lands on its own property, with
    /// the two snake_case keys reaching their camelCase properties through
    /// CodingKeys — the pair that a synthesized-only decode would drop.
    func testFullPayloadDecodesEveryDocumentedKey() throws {
        let status = try kronosContractDecode(Self.kronosContractFullJSON)

        XCTAssertTrue(status.enabled, "`enabled` must decode from the wire's enabled key")
        XCTAssertTrue(status.live, "`live` must decode from the wire's live key")
        XCTAssertEqual(
            status.sessionID,
            "b7c1e2f4-0a5d-4c11-9f3e-2ad6c8e10001",
            "`sessionID` must map to the wire key `session_id`"
        )
        XCTAssertEqual(status.pane, "%17", "`pane` carries the tmux pane id verbatim, percent included")
        XCTAssertEqual(
            status.spawnedAt,
            1_780_000_123_456,
            "`spawnedAt` must map to the wire key `spawned_at` and hold unix MILLIS without truncation"
        )
    }

    /// A present `context` object decodes its three numbers, each onto the
    /// field of the same name — contract row `KronosContext`: "`pct: Double`,
    /// `window: Int64`, `at: Int64` — field names match the wire keys
    /// verbatim (no CodingKeys needed)."
    func testFullPayloadDecodesTheThreeContextNumbers() throws {
        let status = try kronosContractDecode(Self.kronosContractFullJSON)
        let context = try XCTUnwrap(status.context, "a payload carrying a context key must decode a context")

        XCTAssertEqual(context.pct, 62.34, accuracy: 1e-9, "`pct` is a fractional percentage, not an integer")
        XCTAssertEqual(context.window, 200_000, "`window` is the context window size in tokens")
        XCTAssertEqual(context.at, 1_780_000_200_000, "`at` is unix millis and must survive as Int64")
    }

    /// Contract row: "An ABSENT `context` key decodes to nil (the wire's only
    /// spelling of 'never reported')." The rest of the payload must still
    /// decode — this is the shape a machine with `~/.kraken/kronos.env`
    /// absent serves, and a throw here would cost the whole tab its status.
    func testPayloadWithoutContextKeyDecodesWithNilContext() throws {
        let status = try kronosContractDecode(Self.kronosContractNoContextJSON)

        XCTAssertNil(status.context, "an absent context key means never reported, not a zeroed report")
        XCTAssertFalse(status.enabled)
        XCTAssertFalse(status.live)
        XCTAssertEqual(status.sessionID, "", "`session_id` is empty until kronos has ever been spawned")
        XCTAssertEqual(status.pane, "", "`pane` is empty when live is false")
        XCTAssertEqual(status.spawnedAt, 0, "`spawned_at` is 0 when never spawned")
    }

    /// Contract row: "The five non-context keys are always present per the API
    /// doc, so they are non-optional — a payload missing one is a decode
    /// ERROR, not a default." Asserted per key, and by PRESENCE of a throw
    /// rather than by its text.
    func testPayloadMissingAnyRequiredKeyIsADecodeError() {
        let required = ["enabled", "live", "session_id", "pane", "spawned_at"]
        for key in required {
            let json = Self.kronosContractNoContextJSON
                .split(separator: "\n")
                .filter { !$0.contains("\"\(key)\"") }
                .joined(separator: "\n")
                // dropping a middle line leaves a trailing comma before `}`
                .replacingOccurrences(of: ",\n}", with: "\n}")

            XCTAssertThrowsError(
                try kronosContractDecode(json),
                "a payload missing `\(key)` must fail to decode rather than default it"
            )
        }
    }

    // MARK: - kronosMeterState
    //
    // Contract row `kronosMeterState`: "MIRRORS web/_ui/src/lib/kronos.ts
    // `kronosMeterState` exactly: nil or non-finite → `(pct: nil, band: .ok,
    // label: "—")`; else band by INCLUSIVE thresholds (>=90 warn90, >=75
    // warn75, >=50 warn50, else ok), label = one decimal + "%" (e.g.
    // `"62.3%"`, `String(format: "%.1f%%", pct)`), pct passed through
    // UNCLAMPED."

    /// nil in gives the whole "never reported" triple out: no percentage, the
    /// calm band, and the em-dash placeholder. The em dash is U+2014, spelled
    /// as an escape here so a copy through an editor that helpfully swaps it
    /// for a hyphen cannot pass.
    func testMeterStateForNilIsTheNeverReportedTriple() {
        let state = kronosMeterState(nil)

        XCTAssertNil(state.pct, "a never-reported context has no percentage to render")
        XCTAssertEqual(state.band, .ok, "never reported must read as calm, not as a warning")
        XCTAssertEqual(state.label, "\u{2014}", "the placeholder label is an em dash")
    }

    /// The four bands over representative interior values — one clearly
    /// inside each range, so this test fails on a band mapping that is wrong
    /// everywhere rather than only at a boundary.
    func testMeterStateBandsRepresentativeValues() {
        XCTAssertEqual(kronosMeterState(0).band, .ok, "an empty window is ok")
        XCTAssertEqual(kronosMeterState(12.5).band, .ok)
        XCTAssertEqual(kronosMeterState(62.34).band, .warn50)
        XCTAssertEqual(kronosMeterState(80).band, .warn75)
        XCTAssertEqual(kronosMeterState(95.5).band, .warn90)
    }

    /// The thresholds are INCLUSIVE: a value sitting exactly on a tick belongs
    /// to the band the tick opens, not to the one below it. This is the half
    /// of the rule that a `>` instead of `>=` gets wrong at exactly three
    /// inputs in the whole domain.
    func testMeterStateThresholdsAreInclusiveAtEachTick() {
        XCTAssertEqual(kronosMeterState(50).band, .warn50, "exactly 50 is already warn50")
        XCTAssertEqual(kronosMeterState(75).band, .warn75, "exactly 75 is already warn75")
        XCTAssertEqual(kronosMeterState(90).band, .warn90, "exactly 90 is already warn90")
    }

    /// The other side of each tick stays in the band below — the assertion
    /// that keeps "inclusive" from being satisfied by a rule that simply
    /// rounds everything up.
    func testMeterStateJustBelowEachThresholdStaysInTheLowerBand() {
        XCTAssertEqual(kronosMeterState(49.9).band, .ok, "49.9 has not reached the first tick")
        XCTAssertEqual(kronosMeterState(74.9).band, .warn50, "74.9 is still the first warning band")
        XCTAssertEqual(kronosMeterState(89.9).band, .warn75, "89.9 is still the second warning band")
    }

    /// The label is one decimal place followed by a literal percent sign —
    /// the contract's own worked example plus the two shapes that a naive
    /// integer or full-precision format would render differently.
    func testMeterStateLabelIsOneDecimalPlusPercent() {
        XCTAssertEqual(kronosMeterState(62.34).label, "62.3%", "the contract's worked example")
        XCTAssertEqual(kronosMeterState(50).label, "50.0%", "a whole number still carries its decimal")
        XCTAssertEqual(kronosMeterState(0).label, "0.0%")
        XCTAssertEqual(kronosMeterState(100).label, "100.0%")
    }

    /// The percentage is passed through UNCLAMPED — a report above 100 stays
    /// above 100 in both the number and the label, and lands in the top band.
    /// Clamping belongs to the fill alone; clamping here would silently
    /// rewrite what kronos actually said about its own window.
    func testMeterStatePassesPercentageThroughUnclamped() throws {
        let over = kronosMeterState(150)
        XCTAssertEqual(try XCTUnwrap(over.pct), 150, accuracy: 1e-9, "150 must survive as 150")
        XCTAssertEqual(over.band, .warn90, "anything at or above 90 is the top band, 150 included")
        XCTAssertEqual(over.label, "150.0%", "the label reports what was measured, not what fits")
    }

    /// The pct that comes back is the pct that went in, for ordinary
    /// in-range inputs too — the passthrough is the rule, not a special case
    /// reserved for out-of-range values.
    func testMeterStateCarriesTheInputPercentage() throws {
        XCTAssertEqual(try XCTUnwrap(kronosMeterState(62.34).pct), 62.34, accuracy: 1e-9)
        XCTAssertEqual(try XCTUnwrap(kronosMeterState(0).pct), 0, accuracy: 1e-9)
        XCTAssertEqual(try XCTUnwrap(kronosMeterState(90).pct), 90, accuracy: 1e-9)
    }

    /// Same clause of the same row: a non-finite reading is treated exactly as
    /// a missing one. There is no percentage to render for an infinity or a
    /// NaN, and a band derived from one would be arbitrary.
    func testMeterStateForNonFiniteInputIsTheNeverReportedTriple() {
        for value in [Double.nan, .infinity, -.infinity] {
            let state = kronosMeterState(value)
            XCTAssertNil(state.pct, "a non-finite reading has no percentage; got \(String(describing: state.pct))")
            XCTAssertEqual(state.band, .ok, "a non-finite reading must not raise a warning band")
            XCTAssertEqual(state.label, "\u{2014}", "a non-finite reading renders the placeholder label")
        }
    }

    /// Contract row `KronosMeterState`: the struct is `Equatable`. Pinned by
    /// comparing two whole results rather than by constructing one, so this
    /// assertion holds whatever initializer the implementation offers.
    func testMeterStateIsEquatableAsAWholeValue() {
        XCTAssertEqual(kronosMeterState(62.34), kronosMeterState(62.34), "the same input yields an equal state")
        XCTAssertEqual(kronosMeterState(nil), kronosMeterState(nil), "two never-reported states are equal")
        XCTAssertNotEqual(kronosMeterState(62.34), kronosMeterState(80), "different bands are different states")
        XCTAssertNotEqual(kronosMeterState(nil), kronosMeterState(0), "never reported is not the same as zero")
    }

    // MARK: - KronosBand
    //
    // Contract row `KronosBand`: "`enum KronosBand: String, Equatable { case
    // ok, warn50, warn75, warn90 }` — Same four bands as web `KronosBand`,
    // same names."

    /// The four cases exist, are distinct, and carry the web names as their
    /// raw values — the raw string IS the shared vocabulary, so a renamed
    /// case would silently part the two clients' band language.
    func testBandHasTheFourWebNamesAsRawValues() {
        XCTAssertEqual(KronosBand.ok.rawValue, "ok")
        XCTAssertEqual(KronosBand.warn50.rawValue, "warn50")
        XCTAssertEqual(KronosBand.warn75.rawValue, "warn75")
        XCTAssertEqual(KronosBand.warn90.rawValue, "warn90")

        let all: [KronosBand] = [.ok, .warn50, .warn75, .warn90]
        XCTAssertEqual(Set(all.map(\.rawValue)).count, 4, "the four band names must be distinct")
    }

    /// `RawRepresentable` round-trips both ways, which is what makes the raw
    /// value a usable wire/CSS token rather than a display accident.
    func testBandRoundTripsThroughItsRawValue() {
        for band in [KronosBand.ok, .warn50, .warn75, .warn90] {
            XCTAssertEqual(KronosBand(rawValue: band.rawValue), band)
        }
    }

    // MARK: - kronosFillPct
    //
    // Contract row `kronosFillPct`: "MIRRORS web `kronosFillPct`: nil/non-finite
    // → 0; else clamped into 0...100."

    /// Nothing reported draws an empty bar rather than a full or an
    /// indeterminate one.
    func testFillPctForNilIsZero() {
        XCTAssertEqual(kronosFillPct(nil), 0, accuracy: 1e-9)
    }

    /// An in-range percentage is the fill, unchanged — the clamp must not
    /// round, snap or rescale the ordinary case.
    func testFillPctPassesInRangeValuesThrough() {
        XCTAssertEqual(kronosFillPct(0), 0, accuracy: 1e-9)
        XCTAssertEqual(kronosFillPct(62.34), 62.34, accuracy: 1e-9)
        XCTAssertEqual(kronosFillPct(100), 100, accuracy: 1e-9)
    }

    /// Both ends clamp. This is the one place the contract asks for clamping,
    /// and it exists because a bar geometry driven by 150 or by -10 draws
    /// outside its own track.
    func testFillPctClampsAtBothEnds() {
        XCTAssertEqual(kronosFillPct(150), 100, accuracy: 1e-9, "an over-100 reading fills the bar, no more")
        XCTAssertEqual(kronosFillPct(100.0001), 100, accuracy: 1e-9)
        XCTAssertEqual(kronosFillPct(-10), 0, accuracy: 1e-9, "a negative reading empties the bar, no less")
        XCTAssertEqual(kronosFillPct(-0.0001), 0, accuracy: 1e-9)
    }

    /// Same clause: a non-finite reading is an empty bar, not a full one —
    /// which is what a naive `min(max(x, 0), 100)` returns for NaN in some
    /// orderings, and what an infinity returns in all of them.
    func testFillPctForNonFiniteInputIsZero() {
        for value in [Double.nan, .infinity, -.infinity] {
            XCTAssertEqual(kronosFillPct(value), 0, accuracy: 1e-9, "non-finite \(value) must fill nothing")
        }
    }

    /// The fill and the meter state agree on which inputs are renderable: the
    /// three inputs that yield no percentage are exactly the three that yield
    /// no fill. Stated as a relationship because the two functions are
    /// mirrored from one web module and are only useful together.
    func testFillPctAndMeterStateAgreeOnUnrenderableInputs() {
        let unrenderable: [Double?] = [nil, Double.nan, Double.infinity, -Double.infinity]
        for value in unrenderable {
            XCTAssertNil(kronosMeterState(value).pct, "\(String(describing: value)) has no percentage")
            XCTAssertEqual(kronosFillPct(value), 0, accuracy: 1e-9, "\(String(describing: value)) has no fill")
        }
    }

    // MARK: - kronosThresholds
    //
    // Contract row `kronosThresholds`: "`let kronosThresholds: [Double] =
    // [50, 75, 90]` — Same three ticks as web `KRONOS_THRESHOLDS`, same
    // order."

    /// The three ticks, in the contract's order. Order is asserted by
    /// comparing the array whole rather than as a set: the meter draws these
    /// left to right, and a sorted-differently array puts the ticks in the
    /// wrong places without changing the values.
    func testThresholdsAreTheThreeTicksInOrder() {
        XCTAssertEqual(kronosThresholds, [50, 75, 90], "same three ticks as web KRONOS_THRESHOLDS, same order")
    }

    /// The ticks are the same numbers the band rule uses — a tick drawn where
    /// no band changes is decoration, and a band change with no tick is
    /// invisible. Derived from `kronosMeterState` so the two cannot drift
    /// apart without this failing.
    func testEachThresholdIsWhereItsBandBegins() {
        let expected: [Double: KronosBand] = [50: .warn50, 75: .warn75, 90: .warn90]
        for tick in kronosThresholds {
            XCTAssertEqual(
                kronosMeterState(tick).band,
                expected[tick],
                "tick \(tick) must be exactly where its band begins"
            )
            XCTAssertNotEqual(
                kronosMeterState(tick).band,
                kronosMeterState(tick - 0.1).band,
                "tick \(tick) must mark a real band change"
            )
        }
    }

    // MARK: - the router row
    //
    // Contract row `AppRouter.Tab.kronos`: "new case in the `Tab` enum —
    // `kraken://kronos` → `selectedTab = .kronos` and changes NOTHING else
    // (no pending flags touched) — RouterContractTests' existing 'changes
    // only the selected tab' rule. Every existing deep link keeps its
    // behavior byte-identical."

    private func kronosContractRouter() -> AppRouter { AppRouter() }

    private func kronosContractURL(
        _ string: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) throws -> URL {
        try XCTUnwrap(URL(string: string), "route string is not a URL: \(string)", file: file, line: line)
    }

    /// Handle one route on a fresh router and hand back the router plus the
    /// before/after snapshots — RouterContractTests' helper shape, replicated
    /// because that file's version is file-private.
    private func kronosContractHandle(
        _ string: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) throws -> (router: AppRouter, before: [String: String], after: [String: String]) {
        let router = kronosContractRouter()
        let before = kronosContractRouterSnapshot(router)
        router.handle(url: try kronosContractURL(string, file: file, line: line))
        return (router, before, kronosContractRouterSnapshot(router))
    }

    /// The new case exists and is a value of its own — distinct from every tab
    /// that was already there, so `kraken://kronos` cannot be satisfied by
    /// aliasing an existing tab.
    ///
    /// Deliberately NOT an exhaustive switch: "exactly N cases" is pinned once,
    /// in RouterContractTests, and the tab ORDER this round sets is marked
    /// review-checked rather than blind-testable.
    func testTabHasADistinctKronosCase() {
        let tabs: [AppRouter.Tab] = [.board, .kronos, .sessions, .graph, .settings]
        XCTAssertEqual(Set(tabs).count, 5, "kronos must be its own Tab case, not an alias of an existing one")
        XCTAssertNotEqual(AppRouter.Tab.kronos, .board)
        XCTAssertNotEqual(AppRouter.Tab.kronos, .sessions)
    }

    /// `kraken://kronos` selects the kronos tab.
    func testKronosRouteSelectsKronosTab() throws {
        let (router, _, _) = try kronosContractHandle("kraken://kronos")

        XCTAssertEqual(router.selectedTab, .kronos, "kraken://kronos must select the Kronos tab")
    }

    /// The structural half of the same row: selecting the tab is the WHOLE
    /// outcome. Asserted over the router's reflected state so a route that
    /// quietly flips a sibling's pending flag on its way to the tab is caught
    /// without this file naming any flag the contract does not freeze.
    func testKronosRouteChangesOnlyTheSelectedTab() throws {
        let (_, before, after) = try kronosContractHandle("kraken://kronos")

        let changed = kronosContractChangedKeys(before, after)
        XCTAssertTrue(
            changed.contains("selectedTab"),
            "kraken://kronos must actually select a tab, changed: \(changed.sorted())"
        )
        XCTAssertTrue(
            changed.subtracting(["selectedTab"]).isEmpty,
            "kraken://kronos must change only the selected tab, changed: \(changed.sorted())"
        )
    }

    /// Named-symbol form of the same "no pending flags touched" clause: every
    /// pending-looking key on the router is still unset after the route.
    /// Redundant with the structural test by design — it survives an
    /// implementation Mirror reports differently, and it fails with a message
    /// naming the offending flag.
    func testKronosRouteLeavesEveryPendingFlagUnset() throws {
        let (_, _, after) = try kronosContractHandle("kraken://kronos")

        let pending = kronosContractPendingKeys(after)
        XCTAssertFalse(
            after.isEmpty,
            "AppRouter exposed no reflectable stored state; this assertion would be inert"
        )
        for key in pending {
            XCTAssertTrue(
                kronosContractIsUnset(after[key] ?? ""),
                "kraken://kronos left \(key) = \(after[key] ?? "?"); it must touch no pending flag"
            )
        }
    }

    /// The route is not a no-op host: an unknown host leaves the router
    /// untouched, so `kronos` must be distinguishable from `kronoss`. This is
    /// the regression an implementation that adds the CASE but forgets the
    /// route produces — the tab exists, the deep link silently does nothing.
    func testKronosRouteIsRecognizedUnlikeANeighbouringUnknownHost() throws {
        let known = try kronosContractHandle("kraken://kronos")
        XCTAssertNotEqual(known.before, known.after, "kraken://kronos must not be an unknown host")

        let unknown = try kronosContractHandle("kraken://kronoss")
        XCTAssertEqual(unknown.before, unknown.after, "kraken://kronoss is an unknown host and must no-op")
    }

    /// "Every existing deep link keeps its behavior byte-identical" — the
    /// spot-check that the new case did not capture its neighbours. Each of
    /// these lands on its own tab, and none of them lands on kronos.
    func testExistingTabRoutesStillSelectTheirOwnTabs() throws {
        let expected: [(String, AppRouter.Tab)] = [
            ("kraken://board", .board),
            ("kraken://sessions", .sessions),
            ("kraken://graph", .graph),
            ("kraken://settings", .settings),
        ]
        for (route, tab) in expected {
            let (router, _, _) = try kronosContractHandle(route)
            XCTAssertEqual(router.selectedTab, tab, "\(route) must still select its own tab")
            XCTAssertNotEqual(router.selectedTab, .kronos, "\(route) must not be captured by the new kronos tab")
        }
    }

    /// Routing to kronos and away again leaves the second route's outcome, not
    /// a union of both — a tab a later deep link cannot leave is the same bug
    /// as one it cannot reach.
    func testRoutingAwayFromKronosLeavesTheKronosTab() throws {
        let router = kronosContractRouter()

        router.handle(url: try kronosContractURL("kraken://kronos"))
        XCTAssertEqual(router.selectedTab, .kronos)

        router.handle(url: try kronosContractURL("kraken://board"))
        XCTAssertEqual(router.selectedTab, .board, "a later route must be able to leave the kronos tab")

        router.handle(url: try kronosContractURL("kraken://kronos"))
        XCTAssertEqual(router.selectedTab, .kronos, "a second kraken://kronos must select it again")
    }
}
