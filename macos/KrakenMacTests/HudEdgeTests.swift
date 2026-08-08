import XCTest
import Foundation
@testable import KrakenMac

// HudEdgeTests — the EDGE dimension of the Kraken.app blind test round.
//
// Authored against `artifacts/50-contract.md` alone, with no sight of
// `macos/Kraken/**`. The subject here is not the happy path: it is the
// boundaries, the refusals and the error paths — the answers a caller gets
// when it asks for something the contract says is not there.
//
// Three rules shaped what is and is not asserted below.
//
//  1. A CONTRACT SILENCE IS NOT A TEST. Where the contract does not state or
//     imply an edge's answer, that edge is recorded in the round's report as
//     unpinnable and NOTHING is asserted here. A tolerant probe that accepts
//     two shapes would convert the gap into a green tick, which is the one
//     outcome worse than a red one.
//  2. STRICT, SINGLE-VALUED ASSERTIONS. Every expectation below is one value
//     the contract names or one value its arithmetic forces.
//  3. `edge`-PREFIXED FILE SCOPE. This module's declaration space is shared
//     with a concurrently-authored contract suite; a collision fails the whole
//     test binary rather than one case, so every file-scope declaration here
//     is both `private` and `edge`-prefixed.
//
// Timing obligations use an injected debounce and XCTestExpectation. Nothing
// sleeps, nothing touches the network, nothing reads `~/`.

// MARK: - File-scope helpers

/// Channel-wise color comparison at the contract's stated ±1 tolerance
/// ("Color equality tolerance" in the ground rules). Used only for values the
/// contract defines as lerp RESULTS; palette hexes are compared exactly, since
/// those are byte pins against `internal/jobs/color.go` rather than arithmetic.
private func edgeAssertRGB(
    _ got: RGB?,
    _ want: RGB,
    _ what: String,
    file: StaticString = #filePath,
    line: UInt = #line
) {
    guard let got else {
        XCTFail("\(what): expected a color, got nil", file: file, line: line)
        return
    }
    XCTAssertLessThanOrEqual(abs(got.r - want.r), 1, "\(what) red \(got.r) vs \(want.r)", file: file, line: line)
    XCTAssertLessThanOrEqual(abs(got.g - want.g), 1, "\(what) green \(got.g) vs \(want.g)", file: file, line: line)
    XCTAssertLessThanOrEqual(abs(got.b - want.b), 1, "\(what) blue \(got.b) vs \(want.b)", file: file, line: line)
}

/// The ten-entry transition table, transcribed from the contract's S3 row and
/// nothing else. `nil` is the answer for every pair it does not name — which
/// is the property the 6×6 sweep below actually measures.
private func edgeExpectedMove(_ from: BoardColumn, _ to: BoardColumn) -> MoveAction? {
    if from == to {
        // "X→X (same column, any column EXCEPT archived) reorder".
        return from == .archived ? nil : .reorder
    }
    switch (from, to) {
    case (.draft, .queued):   return .post(endpoint: "/api/queue")
    case (.queued, .draft):   return .post(endpoint: "/api/dequeue")
    case (.queued, .working): return .post(endpoint: "/api/start")
    case (.working, .blocked): return .post(endpoint: "/api/stop")
    case (.blocked, .done):   return .post(endpoint: "/api/done")
    case (.blocked, .working): return .steer(mode: .unblock, requireText: false)
    case (.done, .blocked):   return .post(endpoint: "/api/reactivate")
    case (.done, .working):   return .steer(mode: .reactivate, requireText: true)
    case (.done, .archived):  return .post(endpoint: "/api/archive")
    default:                  return nil
    }
}

/// A `WebTokens` built the only way a blind author may build one: by decoding
/// a JSON literal through the module's own model. The wire keys are the ones
/// `ios/API.md` and `Models.swift`'s `CodingKeys` name.
private func edgeTokens(
    input: Int64 = 0,
    output: Int64 = 0,
    cacheWrite: Int64 = 0,
    cacheRead: Int64 = 0,
    sessions: Int64 = 0
) throws -> WebTokens {
    let json = """
    {"in":\(input),"out":\(output),"cache_write":\(cacheWrite),\
    "cache_read":\(cacheRead),"sessions":\(sessions),"updated_at":0}
    """
    return try JSONDecoder().decode(WebTokens.self, from: Data(json.utf8))
}

/// A transition rendered as a comparable string. `JobTransition` is Equatable
/// but the contract says nothing about the ORDER the array comes back in, so
/// every assertion here compares unordered contents — asserting an order the
/// contract does not name would be inventing a requirement.
private func edgeKey(_ t: JobTransition) -> String {
    switch t {
    case .blocked(let s):  return "blocked:\(s)"
    case .started(let s):  return "started:\(s)"
    case .erroring(let s): return "erroring:\(s)"
    }
}

private func edgeKeys(_ ts: [JobTransition]) -> Set<String> {
    Set(ts.map(edgeKey))
}

/// A thread-safe recorder for `ComposerDraft`'s save closure. The contract does
/// not name the queue the closure runs on, so the recorder assumes none.
private final class edgeSaveRecorder: @unchecked Sendable {
    private let lock = NSLock()
    private var saves: [String] = []
    private var callback: (() -> Void)?

    /// Every access goes through the lock, including the callback SLOT: the
    /// test thread installs it while the draft's timer thread may be reading
    /// it, and an unguarded property would be a data race in the harness rather
    /// than in the subject.
    func onSave(_ cb: (() -> Void)?) {
        lock.lock()
        callback = cb
        lock.unlock()
    }

    func record(_ text: String) {
        lock.lock()
        saves.append(text)
        let cb = callback
        lock.unlock()
        cb?()
    }

    var all: [String] {
        lock.lock()
        defer { lock.unlock() }
        return saves
    }

    var count: Int { all.count }
}

/// A ZWJ emoji sequence: ONE `Character`, SEVEN unicode scalars. The gap
/// between those two numbers is the whole of the "cut surrogate-safely" clause
/// — a budget applied to scalars rather than to graphemes cuts this in half
/// and leaves a dangling joiner behind, which is what the title tests below
/// look for.
private let edgeFamily = "👨‍👩‍👧‍👦"
private let edgeZWJ: Unicode.Scalar = "\u{200D}"

// MARK: - S2, theme engine: refusals and boundaries

final class HudEdgeThemeTests: XCTestCase {

    // MARK: JobPalette — the nil that makes the fallback edge fire

    /// The eight names are the WHOLE domain. Everything else — including the
    /// empty string, which is how kraken spells "this job has no colour" —
    /// answers nil, and nil is what the card's 40%-accent fallback edge keys
    /// off. A palette that quietly returned a default colour for an unknown
    /// name would erase that fallback with nothing failing.
    func testJobPaletteRefusesEveryNameOffTheEightInBothModes() {
        let offList = [
            "",                 // the unset colour: the contract names this one explicitly
            " ",
            " red",             // leading whitespace is not one of the eight names
            "red ",
            "reds",
            "blu",
            "magenta",
            "default",          // /color's CLEAR verb, deliberately absent from the palette
            "violet", "cyan2", "grey", "white", "black",
            "#DC2626",          // a hex is not a name
            "job-red",
        ]
        for mode in [ThemeMode.dark, ThemeMode.light] {
            for name in offList {
                XCTAssertNil(
                    JobPalette.color(name, mode),
                    "\(mode) palette must refuse \(String(reflecting: name))"
                )
            }
        }
    }

    /// Case is part of the name. The contract's domain is the eight LOWERCASE
    /// names `internal/jobs/color.go` carries, and its refusal clause is "any
    /// other string"; `"Red"` is another string. This matters beyond pedantry:
    /// the same eight strings are what kraken types into `/color`, and a
    /// client that accepted `"Red"` would render a colour for a value the
    /// server would reject.
    func testJobPaletteIsCaseSensitive() {
        for name in ["Red", "RED", "Blue", "GREEN", "Cyan", "PURPLE"] {
            XCTAssertNil(JobPalette.color(name, .dark), "dark must refuse \(name)")
            XCTAssertNil(JobPalette.color(name, .light), "light must refuse \(name)")
        }
    }

    /// The positive half of the domain, stated exactly: the eight names resolve
    /// to `internal/jobs/color.go`'s hexes byte-for-byte in both modes. Exact
    /// equality rather than the ±1 lerp tolerance — these are pinned literals,
    /// not arithmetic.
    func testJobPaletteResolvesTheEightExactlyInBothModes() {
        let dark: [(String, String)] = [
            ("red", "#DC2626"), ("blue", "#6A9BCC"), ("green", "#16A34A"), ("yellow", "#CA8A04"),
            ("purple", "#827DBD"), ("orange", "#D97757"), ("pink", "#C46686"), ("cyan", "#0891B2"),
        ]
        let light: [(String, String)] = [
            ("red", "#CA2323"), ("blue", "#496B8D"), ("green", "#107937"), ("yellow", "#8B5F03"),
            ("purple", "#676395"), ("orange", "#9C563F"), ("pink", "#9B516A"), ("cyan", "#06718B"),
        ]
        for (name, hex) in dark {
            XCTAssertEqual(JobPalette.color(name, .dark), RGB(hex: hex), "dark \(name)")
        }
        for (name, hex) in light {
            XCTAssertEqual(JobPalette.color(name, .light), RGB(hex: hex), "light \(name)")
        }
    }

    /// A consequence of the two obligations above rather than a rule of its
    /// own: the palette is pinned in UPPERCASE hex (`color.go`) and the theme
    /// primitives in LOWERCASE (`style.css`), so one parser has to read both.
    func testRGBHexParsingIsCaseInsensitive() {
        XCTAssertEqual(RGB(hex: "#DC2626"), RGB(hex: "#dc2626"))
        XCTAssertEqual(RGB(hex: "#A86AFF"), RGB(hex: "#a86aff"))
        XCTAssertEqual(RGB(hex: "#0891B2"), RGB(hex: "#0891b2"))
    }

    /// The two ends of the channel range, so a parser that dropped or clamped a
    /// nibble is caught without relying on any single palette entry.
    func testRGBHexParsesTheChannelExtremes() {
        let black = RGB(hex: "#000000")
        XCTAssertEqual(black.r, 0); XCTAssertEqual(black.g, 0); XCTAssertEqual(black.b, 0)
        let white = RGB(hex: "#ffffff")
        XCTAssertEqual(white.r, 255); XCTAssertEqual(white.g, 255); XCTAssertEqual(white.b, 255)
        let mixed = RGB(hex: "#01ff80")
        XCTAssertEqual(mixed.r, 1); XCTAssertEqual(mixed.g, 255); XCTAssertEqual(mixed.b, 128)
    }

    // MARK: mix — the two ends of the ramp

    /// `mix` at 0% and 100% is the definition's own boundary: with
    /// `channel = round(a·f + b·(1−f))`, `f = 0` is exactly `b` and `f = 1` is
    /// exactly `a`. A ramp with its arguments swapped passes every midpoint
    /// test that only checks "somewhere between" and fails precisely here.
    func testMixAtZeroAndHundredPercentIsExactlyOneEndpoint() {
        let a = RGB(hex: "#a86aff")
        let b = RGB(hex: "#0d0c12")
        edgeAssertRGB(HudTheme.mix(a, 0, over: b), b, "0% is the OVER color")
        edgeAssertRGB(HudTheme.mix(a, 100, over: b), a, "100% is the MIXED-IN color")
    }

    /// Mixing a color with itself is the identity at every fraction — the one
    /// invariant that holds no matter which way the lerp is oriented, so it
    /// catches an arithmetic slip (a stray divide, a 255/256 confusion) that
    /// the endpoint test above cannot.
    func testMixOfAColorWithItselfIsThatColorAtEveryFraction() {
        let c = RGB(hex: "#7fd44a")
        for pct in [0.0, 1, 13, 26, 50, 74, 99, 100] {
            edgeAssertRGB(HudTheme.mix(c, pct, over: c), c, "mix(c, \(pct), over: c)")
        }
    }

    // MARK: glow — "no glow in light mode, on anything"

    /// The flattening is unconditional across accents. Stated as "ALL nil in
    /// light", so a single accent's glow surviving is a contract violation even
    /// if the other four are flattened — hence the sweep rather than one case.
    func testLightModeFlattensEveryGlowForEveryAccent() {
        for accent in HudAccent.allCases {
            let t = HudTheme.resolve(.light, accent)
            XCTAssertNil(t.glow, "light/\(accent.rawValue) glow")
            XCTAssertNil(t.glowHot, "light/\(accent.rawValue) glowHot")
            XCTAssertNil(t.glowOK, "light/\(accent.rawValue) glowOK")
            XCTAssertNil(t.glowWarn, "light/\(accent.rawValue) glowWarn")
        }
    }

    /// The other side of the same claim: dark is where glow LIVES, on every
    /// accent. Without this, an engine that returned nil unconditionally would
    /// pass the flattening test above.
    func testDarkModeCarriesEveryGlowForEveryAccent() {
        for accent in HudAccent.allCases {
            let t = HudTheme.resolve(.dark, accent)
            XCTAssertNotNil(t.glow, "dark/\(accent.rawValue) glow")
            XCTAssertNotNil(t.glowHot, "dark/\(accent.rawValue) glowHot")
            XCTAssertNotNil(t.glowOK, "dark/\(accent.rawValue) glowOK")
            XCTAssertNotNil(t.glowWarn, "dark/\(accent.rawValue) glowWarn")
        }
    }

    // MARK: derivation actually re-derives

    /// Every one of the nine derived fields takes `--ac` as a recipe input, and
    /// `--ac` is one of the three primitives an accent overrides — so changing
    /// the accent must move ALL nine. A field hardcoded to its violet value (or
    /// resolved once and cached across accents) is invisible to a test that
    /// only checks dark+violet, and this is the cheapest way to see it.
    func testEveryDerivedFieldMovesWhenTheAccentChanges() {
        let violet = HudTheme.resolve(.dark, .violet)
        let cyan = HudTheme.resolve(.dark, .cyan)
        XCTAssertNotEqual(violet.bgBoard, cyan.bgBoard, "bgBoard")
        XCTAssertNotEqual(violet.surface, cyan.surface, "surface")
        XCTAssertNotEqual(violet.border, cyan.border, "border")
        XCTAssertNotEqual(violet.borderStrong, cyan.borderStrong, "borderStrong")
        XCTAssertNotEqual(violet.divider, cyan.divider, "divider")
        XCTAssertNotEqual(violet.muted, cyan.muted, "muted")
        XCTAssertNotEqual(violet.body, cyan.body, "body")
        XCTAssertNotEqual(violet.chipBg, cyan.chipBg, "chipBg")
        XCTAssertNotEqual(violet.raised, cyan.raised, "raised")
    }

    /// The same claim across the mode axis: light overrides every primitive the
    /// recipes read, so no derived field may survive the switch unchanged.
    func testEveryDerivedFieldMovesWhenTheModeChanges() {
        let dark = HudTheme.resolve(.dark, .violet)
        let light = HudTheme.resolve(.light, .violet)
        XCTAssertNotEqual(dark.bgBoard, light.bgBoard, "bgBoard")
        XCTAssertNotEqual(dark.surface, light.surface, "surface")
        XCTAssertNotEqual(dark.border, light.border, "border")
        XCTAssertNotEqual(dark.borderStrong, light.borderStrong, "borderStrong")
        XCTAssertNotEqual(dark.divider, light.divider, "divider")
        XCTAssertNotEqual(dark.muted, light.muted, "muted")
        XCTAssertNotEqual(dark.body, light.body, "body")
        XCTAssertNotEqual(dark.chipBg, light.chipBg, "chipBg")
        XCTAssertNotEqual(dark.raised, light.raised, "raised")
    }

    /// The enum domains are closed. `allCases` is what a settings picker
    /// iterates, so an extra case is a control that renders a theme nothing
    /// pins, and the accent ORDER is contract text ("exactly these five, in
    /// this order").
    func testThemeEnumDomainsAreExactlyTheContractsAndInOrder() {
        XCTAssertEqual(ThemeMode.allCases.map(\.rawValue), ["dark", "light"])
        XCTAssertEqual(
            HudAccent.allCases.map(\.rawValue),
            ["violet", "cyan", "amber", "crimson", "green"]
        )
    }
}

// MARK: - S3, board logic: the no-op is the product

final class HudEdgeBoardTests: XCTestCase {

    // MARK: MoveTable — the full 6×6 matrix

    /// The whole point of a transition TABLE is what it refuses, and the only
    /// way to measure a refusal set is to sweep the entire domain: all 36
    /// ordered pairs, each compared against the contract's ten entries.
    ///
    /// Ten ENTRIES is fourteen PAIRS — the nine named cross-column moves plus
    /// the reorder rule, which covers five same-column pairs (archived
    /// excluded). The count assertion is carried separately below so that a
    /// table which happens to satisfy every named pair while ALSO answering
    /// something extra cannot pass.
    func testMoveTableSweepsAllThirtySixPairs() {
        var nonNil = 0
        for from in BoardColumn.allCases {
            for to in BoardColumn.allCases {
                let want = edgeExpectedMove(from, to)
                let got = MoveTable.action(from: from, to: to)
                XCTAssertEqual(got, want, "\(from.rawValue) → \(to.rawValue)")
                if got != nil { nonNil += 1 }
            }
        }
        XCTAssertEqual(
            nonNil, 14,
            "ten table entries = 9 named cross-column pairs + 5 same-column reorders"
        )
        XCTAssertEqual(BoardColumn.allCases.count, 6, "the sweep must cover all six columns")
    }

    /// Archived is a DRAWER, not a column: nothing is ever dragged out of it
    /// (unarchive is a button), so every pair with an archived SOURCE is nil —
    /// including `archived → archived`, which the reorder rule explicitly
    /// carves out. A same-column shortcut written as `if from == to { .reorder }`
    /// with no archived guard passes every other test in this file.
    func testArchivedIsNeverADragSource() {
        for to in BoardColumn.allCases {
            XCTAssertNil(
                MoveTable.action(from: .archived, to: to),
                "archived → \(to.rawValue) must be a no-op"
            )
        }
    }

    /// And archived is a target for exactly one source. The four refusals here
    /// each destroy real work if they ever resolve — archiving a WORKING job
    /// tears down a live cell.
    func testOnlyDoneMayBeArchived() {
        for from in BoardColumn.allCases where from != .done {
            XCTAssertNil(
                MoveTable.action(from: from, to: .archived),
                "\(from.rawValue) → archived must be a no-op"
            )
        }
        XCTAssertEqual(MoveTable.action(from: .done, to: .archived), .post(endpoint: "/api/archive"))
    }

    /// The two steer moves must NOT be posts. This is the distinction the web's
    /// own table comment calls the highest-consequence one in the UI: a board
    /// that posted `blocked → working` would assert a state change with no
    /// message behind it.
    func testTheTwoSteerMovesNeverCarryAnEndpoint() {
        XCTAssertEqual(
            MoveTable.action(from: .blocked, to: .working),
            .steer(mode: .unblock, requireText: false)
        )
        XCTAssertEqual(
            MoveTable.action(from: .done, to: .working),
            .steer(mode: .reactivate, requireText: true)
        )
        // requireText is what separates them, so pin the negative too.
        XCTAssertNotEqual(
            MoveTable.action(from: .blocked, to: .working),
            .steer(mode: .unblock, requireText: true)
        )
        XCTAssertNotEqual(
            MoveTable.action(from: .done, to: .working),
            .steer(mode: .reactivate, requireText: false)
        )
    }

    /// The display order is contract text, and the drag layer iterates it.
    func testBoardColumnOrderIsTheDisplayOrder() {
        XCTAssertEqual(
            BoardColumn.allCases.map(\.rawValue),
            ["draft", "queued", "working", "blocked", "done", "archived"]
        )
    }

    // MARK: Bucketing — nil health vs empty-string health

    /// `health` arrives off the wire as a string, and "no health problem" is
    /// spelled BOTH ways depending on the server's mood: the key absent (nil)
    /// or present-and-empty. Only a NON-EMPTY health relocates a job, so a
    /// truthiness test written as `health != nil` files every healthy job in
    /// the payload under BLOCKED — a whole-board failure that no happy-path
    /// fixture with a populated health string would surface.
    func testEmptyStringHealthIsNotHealthAtAll() {
        for state in ["draft", "queued", "working", "blocked", "done"] {
            let withNil = Bucketing.bucket(state: state, health: nil, archived: false)
            let withEmpty = Bucketing.bucket(state: state, health: "", archived: false)
            XCTAssertEqual(withEmpty, withNil, "empty health must behave as no health (\(state))")
        }
        XCTAssertEqual(Bucketing.bucket(state: "working", health: "", archived: false), .working)
        XCTAssertEqual(Bucketing.bucket(state: "queued", health: "", archived: false), .queued)
    }

    /// A non-empty health relocates ANY live state — the daemon is not going to
    /// fix an unhealthy cell, so it needs to read as blocked wherever it sits.
    func testNonEmptyHealthRelocatesEveryLiveState() {
        for state in ["draft", "queued", "working", "blocked", "done", "nonsense"] {
            XCTAssertEqual(
                Bucketing.bucket(state: state, health: "dead cell", archived: false),
                .blocked,
                "health must relocate \(state)"
            )
        }
        // A single space is non-empty. The contract's word is "non-empty", not
        // "non-blank", and this is the boundary between those two readings.
        XCTAssertEqual(Bucketing.bucket(state: "working", health: " ", archived: false), .blocked)
    }

    /// "Archived wins over everything" — over health, and over a state that
    /// would otherwise pick a column. The archive drawer is where a job goes
    /// when its slug has been freed; a health string on it is stale by
    /// construction.
    func testArchivedOutranksHealthAndState() {
        for state in ["draft", "queued", "working", "blocked", "done", "nonsense", ""] {
            for health in [nil, "", "dead cell"] as [String?] {
                XCTAssertEqual(
                    Bucketing.bucket(state: state, health: health, archived: true),
                    .archived,
                    "archived must win for state=\(state) health=\(String(describing: health))"
                )
            }
        }
    }

    /// An unknown state is a REFUSAL to guess, not a crash and not a hole: it
    /// lands in draft. The empty string is the case that actually happens — a
    /// job file with no `state:` line at all.
    func testUnknownStateFallsBackToDraft() {
        for state in ["", "  ", "DRAFT", "Working", "queued ", "reaped", "unknown", "🙂"] {
            XCTAssertEqual(
                Bucketing.bucket(state: state, health: nil, archived: false),
                .draft,
                "state \(String(reflecting: state)) must fall back to draft"
            )
        }
    }

    /// The five live states each map to their own column, so the fallback above
    /// is a fallback rather than the whole function.
    func testEachLiveStateMapsToItsOwnColumn() {
        XCTAssertEqual(Bucketing.bucket(state: "draft", health: nil, archived: false), .draft)
        XCTAssertEqual(Bucketing.bucket(state: "queued", health: nil, archived: false), .queued)
        XCTAssertEqual(Bucketing.bucket(state: "working", health: nil, archived: false), .working)
        XCTAssertEqual(Bucketing.bucket(state: "blocked", health: nil, archived: false), .blocked)
        XCTAssertEqual(Bucketing.bucket(state: "done", health: nil, archived: false), .done)
    }

    // MARK: ReorderPrediction — nil is the fallback signal

    /// A dragged slug that is not in the column is the "a job appeared, vanished
    /// or moved column between the last poll and the drop" case, and the answer
    /// is nil: the caller must refetch rather than guess. Returning the
    /// unchanged array instead would look like a successful prediction and
    /// silently commit a stale order.
    func testPredictRefusesADraggedSlugItCannotFind() {
        XCTAssertNil(ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "z", dropIndex: 0))
        XCTAssertNil(ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "", dropIndex: 1))
        XCTAssertNil(ReorderPrediction.predict(slugs: [], dragged: "a", dropIndex: 0))
        XCTAssertNil(ReorderPrediction.predict(slugs: ["a"], dragged: "A", dropIndex: 0))
    }

    /// A drop index past the end clamps to last rather than trapping or
    /// dropping the card. A drop below the last card in a column produces
    /// exactly this: the pointer is past every card's midpoint.
    func testPredictClampsADropIndexPastTheEnd() {
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "a", dropIndex: 3),
            ["b", "c", "a"]
        )
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "a", dropIndex: 99),
            ["b", "c", "a"]
        )
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "b", dropIndex: 1_000_000),
            ["a", "c", "b"]
        )
    }

    /// The two in-range ends, so the clamp above is a clamp rather than a
    /// blanket move-to-last.
    func testPredictMovesToBothInRangeEnds() {
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "c", dropIndex: 0),
            ["c", "a", "b"]
        )
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "a", dropIndex: 2),
            ["b", "c", "a"]
        )
    }

    /// A one-card column is the degenerate case a modulo-based "wrap" or an
    /// index-1 arithmetic slip falls over on, and it is extremely common — most
    /// columns on a real board hold one or two cards.
    func testPredictHandlesASingleElementColumn() {
        XCTAssertEqual(ReorderPrediction.predict(slugs: ["only"], dragged: "only", dropIndex: 0), ["only"])
        XCTAssertEqual(ReorderPrediction.predict(slugs: ["only"], dragged: "only", dropIndex: 7), ["only"])
    }

    /// Dropping a card back where it started is a no-move, and the prediction
    /// must say so by returning the same order rather than nil — nil is the
    /// refusal signal and would send the caller into an unnecessary refetch.
    func testPredictOfAnUnchangedIndexIsTheSameOrder() {
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "b", dropIndex: 1),
            ["a", "b", "c"]
        )
    }

    /// "The result always contains exactly the input slugs" — a permutation, not
    /// a rewrite. A splice implemented as insert-before-remove duplicates the
    /// dragged slug and drops another; sorting both sides is what catches it
    /// independently of where the card landed.
    func testPredictIsAlwaysAPermutationOfItsInput() {
        let slugs = ["alpha", "bravo", "charlie", "delta", "echo"]
        for dragged in slugs {
            for dropIndex in 0..<(slugs.count + 3) {
                guard let out = ReorderPrediction.predict(
                    slugs: slugs, dragged: dragged, dropIndex: dropIndex
                ) else {
                    XCTFail("predict refused a legal move: \(dragged) → \(dropIndex)")
                    continue
                }
                XCTAssertEqual(out.count, slugs.count, "\(dragged) → \(dropIndex) length")
                XCTAssertEqual(out.sorted(), slugs.sorted(), "\(dragged) → \(dropIndex) membership")
            }
        }
    }

    // MARK: ReorderOverride — tolerate exactly once

    /// An agreeing payload releases immediately. This is the ordinary path and
    /// the one that must not be starved by the tolerance below.
    func testOverrideReleasesOnAnAgreeingPayload() {
        var o = ReorderOverride(column: .queued, slugs: ["a", "b", "c"])
        XCTAssertFalse(o.reconcile(serverOrder: ["a", "b", "c"]), "agreement releases the override")
    }

    /// The tolerance is exactly one payload deep. A board poll already in
    /// flight when the reorder POSTed comes back with the OLD order, which is
    /// why the first disagreement is not evidence of failure; the second one
    /// is, and holding past it would pin a stale local order on screen forever.
    func testOverrideToleratesOneDisagreementAndReleasesOnTheSecond() {
        var o = ReorderOverride(column: .working, slugs: ["a", "b", "c"])
        XCTAssertTrue(o.reconcile(serverOrder: ["c", "b", "a"]), "first disagreement is tolerated")
        XCTAssertFalse(o.reconcile(serverOrder: ["c", "b", "a"]), "second consecutive disagreement releases")
    }

    /// The tolerated payload is a WAIT, not a countdown that has to run out: an
    /// agreeing payload arriving second still releases. An implementation that
    /// released only ever on the second call would pass the test above and fail
    /// here.
    func testOverrideReleasesWhenTheSecondPayloadAgrees() {
        var o = ReorderOverride(column: .blocked, slugs: ["a", "b", "c"])
        XCTAssertTrue(o.reconcile(serverOrder: ["b", "a", "c"]), "first disagreement is tolerated")
        XCTAssertFalse(o.reconcile(serverOrder: ["a", "b", "c"]), "agreement releases")
    }

    /// Agreement is ORDER equality, not membership. The whole subject of the
    /// override is a permutation, so a set-comparison would call every possible
    /// payload agreeing and release the override on the first poll — the exact
    /// flash the optimistic path exists to prevent.
    func testOverrideTreatsAReorderedPayloadAsDisagreement() {
        var o = ReorderOverride(column: .draft, slugs: ["a", "b", "c"])
        XCTAssertTrue(o.reconcile(serverOrder: ["a", "c", "b"]), "same members, wrong order = disagreement")
    }

    /// Membership changes are disagreements too — a job left or joined the
    /// column, so the held order is describing a board that no longer exists.
    func testOverrideTreatsMembershipChangesAsDisagreement() {
        var shorter = ReorderOverride(column: .draft, slugs: ["a", "b", "c"])
        XCTAssertTrue(shorter.reconcile(serverOrder: ["a", "b"]), "a job left the column")

        var longer = ReorderOverride(column: .draft, slugs: ["a", "b", "c"])
        XCTAssertTrue(longer.reconcile(serverOrder: ["a", "b", "c", "d"]), "a job joined the column")
    }

    /// The degenerate column: an override over nothing agrees with an empty
    /// payload and releases at once. Reached by dragging the only card in a
    /// column out of it mid-poll.
    func testOverrideOverAnEmptyColumnAgreesWithAnEmptyPayload() {
        var o = ReorderOverride(column: .done, slugs: [])
        XCTAssertFalse(o.reconcile(serverOrder: []), "empty agrees with empty")
    }

    // MARK: ChipRules — absent is not zero

    /// The load badge has a column gate the drilldown does not. Sweeping the
    /// full 6 × 2 domain is what pins "only": exactly one of the twelve
    /// combinations may be true.
    func testLoadChipIsVisibleInExactlyOneOfTwelveCombinations() {
        var visible: [String] = []
        for column in BoardColumn.allCases {
            for hasLoad in [true, false] {
                if ChipRules.loadChipVisible(hasLoad: hasLoad, column: column) {
                    visible.append("\(column.rawValue)/\(hasLoad)")
                }
            }
        }
        XCTAssertEqual(visible, ["blocked/true"])
    }

    /// `tokens` absent is a server that never reported spend; `tokens` present
    /// and all-zero with zero sessions is a job measured to have spent nothing.
    /// Those are different facts and the chip distinguishes them — an
    /// `if tokens?.total ?? 0 > 0` collapse renders both as nothing and is
    /// invisible until someone asks why a cheap job shows no panel.
    func testTokensChipRefusesNilAndTheAllZeroRow() throws {
        XCTAssertFalse(ChipRules.tokensVisible(tokens: nil), "absent ≠ zero")
        XCTAssertFalse(
            ChipRules.tokensVisible(tokens: try edgeTokens()),
            "all four counters zero AND zero sessions renders nothing"
        )
    }

    /// Sessions alone carry the row: files were summed, the spend was genuinely
    /// zero, and that measured zero is worth showing.
    func testTokensChipShowsAMeasuredZero() throws {
        XCTAssertTrue(ChipRules.tokensVisible(tokens: try edgeTokens(sessions: 1)))
    }

    /// Each of the four counters alone is enough — a row summed from a
    /// transcript whose `sessions` never landed still describes real spend.
    func testAnyNonZeroCounterAloneShowsTheTokensChip() throws {
        XCTAssertTrue(ChipRules.tokensVisible(tokens: try edgeTokens(input: 1)), "input alone")
        XCTAssertTrue(ChipRules.tokensVisible(tokens: try edgeTokens(output: 1)), "output alone")
        XCTAssertTrue(ChipRules.tokensVisible(tokens: try edgeTokens(cacheWrite: 1)), "cacheWrite alone")
        XCTAssertTrue(ChipRules.tokensVisible(tokens: try edgeTokens(cacheRead: 1)), "cacheRead alone")
    }

    /// The status token exists for two columns and is nil for the other four.
    /// Sweeping the domain is the only way to pin "every other column nil".
    func testStatusTokenIsNilForEveryColumnButQueuedAndDone() {
        for column in BoardColumn.allCases {
            switch column {
            case .queued: XCTAssertEqual(ChipRules.statusToken(column: column), "◌ queued")
            case .done:   XCTAssertEqual(ChipRules.statusToken(column: column), "✓ done")
            default:      XCTAssertNil(ChipRules.statusToken(column: column), column.rawValue)
            }
        }
    }

    // MARK: JobKey — the archived namespace

    /// A slug is unique only WITHIN a namespace, so the archived entry needs its
    /// own key or it wins a last-writer-wins race against a live job of the same
    /// name and renders that job unsteerable. The two calls below are exactly
    /// the collision.
    func testJobKeySeparatesTheArchivedAndLiveNamespaces() {
        XCTAssertEqual(JobKey.key(slug: "foo", archived: false), "foo")
        XCTAssertEqual(JobKey.key(slug: "foo", archived: true), "archive:foo")
        XCTAssertNotEqual(
            JobKey.key(slug: "foo", archived: true),
            JobKey.key(slug: "foo", archived: false)
        )
    }

    /// The prefix is applied by the archived FLAG and never inferred from the
    /// slug's text — a job legitimately named `archive:whatever` is still a live
    /// job, and a live key is never double-prefixed.
    func testJobKeyPrefixesOnTheFlagOnly() {
        XCTAssertEqual(JobKey.key(slug: "archive:foo", archived: false), "archive:foo")
        XCTAssertEqual(JobKey.key(slug: "archive:foo", archived: true), "archive:archive:foo")
        XCTAssertEqual(JobKey.key(slug: "", archived: false), "")
        XCTAssertEqual(JobKey.key(slug: "", archived: true), "archive:")
    }
}

// MARK: - S4, overlays / composer: the stack and the debounce

final class HudEdgeOverlayTests: XCTestCase {

    /// The raw values ARE the Esc priorities, so the ordering the escape
    /// handler depends on is a property of the enum rather than of a sort
    /// written beside it.
    func testOverlayPriorityOrderIsTheRawValueOrder() {
        XCTAssertEqual(OverlayKind.usage.rawValue, 10)
        XCTAssertEqual(OverlayKind.editor.rawValue, 20)
        XCTAssertEqual(OverlayKind.steer.rawValue, 30)
        XCTAssertEqual(OverlayKind.thread.rawValue, 40)
        XCTAssertEqual(OverlayKind.artifact.rawValue, 50)
        XCTAssertTrue(OverlayKind.usage < OverlayKind.editor)
        XCTAssertTrue(OverlayKind.editor < OverlayKind.steer)
        XCTAssertTrue(OverlayKind.steer < OverlayKind.thread)
        XCTAssertTrue(OverlayKind.thread < OverlayKind.artifact)
    }

    /// Escape with nothing open is the case that must do NOTHING. A stack that
    /// popped an empty collection, or returned a default kind, would either trap
    /// or close a layer that was never open — and the user's Esc was meant for
    /// the window, not the stack.
    func testEscapeOnAnEmptyStackClosesNothingAndReturnsNil() {
        let s = OverlayStack()
        XCTAssertNil(s.top, "an untouched stack is empty")
        XCTAssertNil(s.handleEscape())
        XCTAssertNil(s.top, "still empty after an escape that found nothing")
        XCTAssertNil(s.handleEscape(), "and repeatedly so")
    }

    /// Each kind tracks OPEN/CLOSED, which is a boolean and not a counter:
    /// opening twice and closing once leaves the overlay closed. A depth
    /// counter here would strand a layer on screen with no key that closes it,
    /// because the second `open` is what a double-click or a re-entrant route
    /// produces.
    func testOpeningTheSameKindTwiceIsNotADepthCounter() {
        let s = OverlayStack()
        s.open(.steer)
        s.open(.steer)
        XCTAssertEqual(s.top, .steer)
        s.close(.steer)
        XCTAssertNil(s.top, "one close must undo a double open")
    }

    /// Closing something that was never open is a no-op — reached whenever a
    /// view's teardown fires for an overlay the user already dismissed.
    func testClosingAnUnopenedKindIsANoOp() {
        let s = OverlayStack()
        s.close(.artifact)
        XCTAssertNil(s.top)

        s.open(.usage)
        s.close(.thread)
        XCTAssertEqual(s.top, .usage, "closing an unrelated kind must not disturb the open one")
    }

    /// Escape unwinds strictly by priority, one layer per press, and the sixth
    /// press finds nothing. The interesting part is that the opens below are in
    /// the WRONG order relative to priority — `top` is the max open, not the
    /// most recently opened, so a stack implemented as a LIFO array passes a
    /// happy-path test and fails here.
    func testEscapeUnwindsByPriorityRegardlessOfOpenOrder() {
        let s = OverlayStack()
        s.open(.artifact)
        s.open(.usage)
        s.open(.thread)
        s.open(.editor)
        s.open(.steer)

        XCTAssertEqual(s.top, .artifact, "top is the highest priority, not the newest")
        XCTAssertEqual(s.handleEscape(), .artifact)
        XCTAssertEqual(s.handleEscape(), .thread)
        XCTAssertEqual(s.handleEscape(), .steer)
        XCTAssertEqual(s.handleEscape(), .editor)
        XCTAssertEqual(s.handleEscape(), .usage)
        XCTAssertNil(s.handleEscape(), "the stack is empty again")
        XCTAssertNil(s.top)
    }

    /// A layer closed by its own button is gone from the escape order: the next
    /// Esc must reach PAST it rather than returning a kind nothing is showing.
    func testEscapeSkipsALayerThatWasClosedDirectly() {
        let s = OverlayStack()
        s.open(.editor)
        s.open(.artifact)
        s.close(.artifact)
        XCTAssertEqual(s.top, .editor)
        XCTAssertEqual(s.handleEscape(), .editor)
        XCTAssertNil(s.handleEscape())
    }

    /// Reopening after a full unwind starts from a clean state — the stack is
    /// reusable, which it must be, since one window hosts every overlay for the
    /// lifetime of the app.
    func testTheStackIsReusableAfterBeingEmptied() {
        let s = OverlayStack()
        s.open(.usage)
        XCTAssertEqual(s.handleEscape(), .usage)
        s.open(.thread)
        XCTAssertEqual(s.top, .thread)
        XCTAssertEqual(s.handleEscape(), .thread)
        XCTAssertNil(s.top)
    }

    // MARK: CmdHighlighter — boundaries in both directions

    /// Every match, as (matched text, skill), left to right. The extraction
    /// through `text[range]` is itself an assertion: a range built against a
    /// different string, or off by one, cannot survive it.
    private func edgeMatches(_ text: String) -> [String] {
        CmdHighlighter.ranges(in: text)
            .sorted { $0.range.lowerBound < $1.range.lowerBound }
            .map { "\(String(text[$0.range]))|\($0.skill)" }
    }

    /// The six tokens, each alone, each recognised, each reporting its skill
    /// WITHOUT the slash. This is the baseline the refusals below are measured
    /// against.
    func testTheSixTokensAreRecognisedAloneWithSlashlessSkillNames() {
        XCTAssertEqual(edgeMatches("/info"), ["/info|info"])
        XCTAssertEqual(edgeMatches("/aim"), ["/aim|aim"])
        XCTAssertEqual(edgeMatches("/do"), ["/do|do"])
        XCTAssertEqual(edgeMatches("/pr"), ["/pr|pr"])
        XCTAssertEqual(edgeMatches("/dream"), ["/dream|dream"])
        XCTAssertEqual(edgeMatches("/graph"), ["/graph|graph"])
    }

    /// `/door` is the contract's own named refusal, and it is the whole reason
    /// the trailing boundary exists: a prefix scan highlights the `/do` inside
    /// it and tells the user a skill will run when none will. The siblings
    /// below are the same shape for the other five tokens.
    func testATokenFollowedByMoreWordCharactersMatchesNothing() {
        XCTAssertEqual(edgeMatches("/door"), [], "the contract's named case")
        XCTAssertEqual(edgeMatches("/dog"), [])
        XCTAssertEqual(edgeMatches("/does"), [])
        XCTAssertEqual(edgeMatches("/prompt"), [])
        XCTAssertEqual(edgeMatches("/prs"), [])
        XCTAssertEqual(edgeMatches("/information"), [])
        XCTAssertEqual(edgeMatches("/aims"), [])
        XCTAssertEqual(edgeMatches("/dreaming"), [])
        XCTAssertEqual(edgeMatches("/graphql"), [])
    }

    /// The leading boundary is start-of-text or WHITESPACE. A slash reached
    /// mid-word — inside a path, a URL, a fraction — is not a command, and paths
    /// are exactly what a steer message is full of.
    func testATokenNeedsWhitespaceOrStartOfTextBeforeIt() {
        XCTAssertEqual(edgeMatches("x/do"), [])
        XCTAssertEqual(edgeMatches("cmd/kraken/do"), [])
        XCTAssertEqual(edgeMatches("~/Developer/pr"), [])
        XCTAssertEqual(edgeMatches("a/graph b"), [])
        XCTAssertEqual(edgeMatches("(/do)"), [], "punctuation before is not whitespace")
        XCTAssertEqual(edgeMatches("—/do"), [])
    }

    /// End-of-text and whitespace-after both close a token, and so does
    /// punctuation — the contract lists all three. The em-dash case is quoted
    /// from the contract itself.
    func testATokenClosesAtEndOfTextWhitespaceOrPunctuation() {
        XCTAssertEqual(edgeMatches("run /do"), ["/do|do"], "end of text")
        XCTAssertEqual(edgeMatches("run /do now"), ["/do|do"], "whitespace after")
        XCTAssertEqual(edgeMatches("ledger accepted — /do"), ["/do|do"], "the contract's own example")
        XCTAssertEqual(edgeMatches("/do."), ["/do|do"], "period")
        XCTAssertEqual(edgeMatches("/do,"), ["/do|do"], "comma")
        XCTAssertEqual(edgeMatches("/do!"), ["/do|do"])
        XCTAssertEqual(edgeMatches("/do?"), ["/do|do"])
        XCTAssertEqual(edgeMatches("/do:"), ["/do|do"])
        XCTAssertEqual(edgeMatches("(/do"), [], "…but the LEADING boundary still has to hold")
    }

    /// A newline is whitespace on both sides. Steer messages are multi-line by
    /// default — the composer submits on ⌘⏎, so plain Return makes lines.
    func testNewlinesAreBoundariesOnBothSides() {
        XCTAssertEqual(edgeMatches("first line\n/do"), ["/do|do"])
        XCTAssertEqual(edgeMatches("/do\nsecond line"), ["/do|do"])
        XCTAssertEqual(edgeMatches("a\n/pr\nb"), ["/pr|pr"])
    }

    /// Every occurrence, not just the first: the scan continues past a match.
    /// A repeated token is the case a `firstIndex(of:)` implementation gets
    /// wrong while looking completely correct on a one-command message.
    func testEveryOccurrenceIsFoundIncludingRepeats() {
        XCTAssertEqual(edgeMatches("/do and then /do"), ["/do|do", "/do|do"])
        XCTAssertEqual(
            edgeMatches("/info then /aim then /do then /pr then /dream then /graph"),
            ["/info|info", "/aim|aim", "/do|do", "/pr|pr", "/dream|dream", "/graph|graph"]
        )
        XCTAssertEqual(edgeMatches("/do /do /do"), ["/do|do", "/do|do", "/do|do"])
    }

    /// The empty and no-token cases return an empty array — not nil, not a
    /// zero-width range at index 0.
    func testTextWithNoTokensYieldsNoRanges() {
        XCTAssertEqual(edgeMatches(""), [])
        XCTAssertEqual(edgeMatches("   "), [])
        XCTAssertEqual(edgeMatches("no commands here at all"), [])
        XCTAssertEqual(edgeMatches("/"), [])
        XCTAssertEqual(edgeMatches("//do"), [])
        XCTAssertEqual(edgeMatches("do"), [], "a token without its slash is prose")
    }

    /// `/dream` is not `/do` plus letters and `/pr` is not a prefix of
    /// anything — the two tokens that share a first character must each report
    /// their OWN skill, or the longest-match rule has been dropped.
    func testOverlappingTokensReportTheirOwnSkill() {
        XCTAssertEqual(edgeMatches("/dream"), ["/dream|dream"])
        XCTAssertEqual(edgeMatches("/do /dream"), ["/do|do", "/dream|dream"])
        XCTAssertEqual(edgeMatches("/dream /do"), ["/dream|dream", "/do|do"])
    }

    /// A token surrounded by text that itself contains multibyte characters:
    /// the returned range must index the ORIGINAL string correctly. A range
    /// computed in UTF-8 or UTF-16 offsets and handed back as `String.Index`
    /// slides off the token here and nowhere else.
    func testRangesIndexTheOriginalStringAcrossMultibyteText() {
        XCTAssertEqual(edgeMatches("✅ 完了 — /pr"), ["/pr|pr"])
        XCTAssertEqual(edgeMatches("👨‍👩‍👧‍👦 /graph 🎉"), ["/graph|graph"])
    }

    // MARK: DraftSeed / QuestionAnswer

    /// The server draft is a SEED, not an overwrite: anything already typed
    /// wins. Getting this backwards silently eats the user's half-written
    /// message the moment a poll lands.
    func testDraftSeedYieldsToAnythingAlreadyTyped() {
        XCTAssertEqual(DraftSeed.accept(serverDraft: "from server", typedSoFar: ""), "from server")
        XCTAssertEqual(DraftSeed.accept(serverDraft: "from server", typedSoFar: "mine"), "mine")
        XCTAssertEqual(DraftSeed.accept(serverDraft: "", typedSoFar: "mine"), "mine")
        XCTAssertEqual(DraftSeed.accept(serverDraft: "", typedSoFar: ""), "")
    }

    /// A single typed character is enough to win — the boundary is emptiness,
    /// not length.
    func testDraftSeedYieldsToASingleCharacter() {
        XCTAssertEqual(DraftSeed.accept(serverDraft: "from server", typedSoFar: "x"), "x")
        XCTAssertEqual(DraftSeed.accept(serverDraft: "from server", typedSoFar: "\n"), "\n")
    }

    /// The payload is the 1-BASED index as a string, which is what the server's
    /// answer route resolves against the question's options. An off-by-one here
    /// answers a different question than the one the user tapped.
    func testQuestionPayloadIsTheOneBasedIndex() {
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 0), "1")
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 1), "2")
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 4), "5")
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 9), "10")
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 99), "100")
    }
}

// MARK: - S4, composer draft: the cancel/flush lifecycle

final class HudEdgeComposerDraftTests: XCTestCase {

    /// Short enough that a positive wait is quick, long enough that a spurious
    /// early fire is not mistaken for a debounce elapsing.
    private let edgeShortDebounce: TimeInterval = 0.05
    /// Comfortably longer than the whole test, so a save observed under it can
    /// ONLY have been an immediate flush.
    private let edgeLongDebounce: TimeInterval = 30

    /// Waits out a debounce and asserts nothing was saved.
    private func edgeAssertNoSave(
        _ rec: edgeSaveRecorder,
        _ what: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) {
        let never = XCTestExpectation(description: what)
        never.isInverted = true
        rec.onSave { never.fulfill() }
        wait(for: [never], timeout: edgeShortDebounce * 6)
        XCTAssertEqual(rec.count, 0, what, file: file, line: line)
    }

    /// The ordinary path, and the "superseding" clause with it: two keystrokes
    /// inside one debounce window are ONE save carrying the later text. A
    /// scheduler that stacked timers would post the intermediate text after the
    /// final one and leave the server draft a keystroke behind.
    func testTypingCoalescesIntoOneSaveOfTheLatestText() {
        let rec = edgeSaveRecorder()
        let fired = expectation(description: "save fires")
        rec.onSave { fired.fulfill() }
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })

        d.typed("a")
        d.typed("ab")
        d.typed("abc")

        wait(for: [fired], timeout: 2)
        XCTAssertEqual(rec.all, ["abc"], "one save, carrying the last text typed")
    }

    /// `typed("")` is a real edit — the user selected all and deleted — and the
    /// contract states the scheduling unconditionally. Suppressing it leaves a
    /// stale draft on the server that the next composer open seeds back in,
    /// resurrecting text the user deliberately cleared.
    func testTypingAnEmptyStringStillSchedulesASave() {
        let rec = edgeSaveRecorder()
        let fired = expectation(description: "empty save fires")
        rec.onSave { fired.fulfill() }
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })

        d.typed("")

        wait(for: [fired], timeout: 2)
        XCTAssertEqual(rec.all, [""])
    }

    /// Send cancels BEFORE the post. The pending save carries the text that is
    /// about to be sent, and a draft write landing after the send re-creates
    /// the message the user just dispatched.
    func testWillSendCancelsThePendingSave() {
        let rec = edgeSaveRecorder()
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })
        d.typed("about to send")
        d.willSend()
        edgeAssertNoSave(rec, "willSend must cancel the pending save")
    }

    /// And AFTER the post — the second half of the same clause, covering a
    /// keystroke that lands while the request is in flight.
    func testDidSendCancelsThePendingSave() {
        let rec = edgeSaveRecorder()
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })
        d.typed("in flight")
        d.didSend()
        edgeAssertNoSave(rec, "didSend must cancel the pending save")
    }

    /// The real send sequence, twice over: nothing is ever saved. Double-send is
    /// reachable by ⌘⏎ on a key-repeat, and the second pair of cancels must be
    /// harmless rather than resurrecting a timer.
    func testDoubleSendSavesNothing() {
        let rec = edgeSaveRecorder()
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })
        d.typed("message")
        d.willSend()
        d.didSend()
        d.willSend()
        d.didSend()
        edgeAssertNoSave(rec, "a double send must still save nothing")
    }

    /// The job finishing cancels the pending draft: the composer is going away
    /// and the draft would describe a job nobody can steer.
    func testJobDoneCancelsThePendingSave() {
        let rec = edgeSaveRecorder()
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })
        d.typed("half a thought")
        d.jobDone()
        edgeAssertNoSave(rec, "jobDone must cancel the pending save")
    }

    /// "After a cancel, no save fires without a new `typed`" — the strongest
    /// form of the cancel clause, and the one that catches a timer merely
    /// RESCHEDULED rather than invalidated.
    func testNoSaveFiresAfterACancelWithoutFurtherTyping() {
        let rec = edgeSaveRecorder()
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })
        d.typed("one")
        d.willSend()
        d.didSend()
        d.jobDone()
        edgeAssertNoSave(rec, "a chain of cancels leaves nothing armed")
    }

    /// …and a NEW keystroke re-arms it. Without this, "cancel" could be
    /// implemented as "shut the draft store down forever" and every later edit
    /// in the session would be lost.
    func testTypingAfterACancelSchedulesAgain() {
        let rec = edgeSaveRecorder()
        let fired = expectation(description: "re-armed save fires")
        rec.onSave { fired.fulfill() }
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })

        d.typed("first")
        d.willSend()
        d.didSend()
        d.typed("second")

        wait(for: [fired], timeout: 2)
        XCTAssertEqual(rec.all, ["second"], "only the post-cancel text is saved")
    }

    /// Kill FLUSHES rather than cancels — killing a cell does not clear its
    /// server draft, so the half-typed text has to land. The 30-second debounce
    /// is the measurement: a save observed inside two seconds cannot have come
    /// from the timer.
    func testJobKilledFlushesThePendingSaveImmediately() {
        let rec = edgeSaveRecorder()
        let fired = expectation(description: "flush fires immediately")
        rec.onSave { fired.fulfill() }
        let d = ComposerDraft(debounce: edgeLongDebounce, save: { rec.record($0) })

        d.typed("survives the kill")
        d.jobKilled()

        wait(for: [fired], timeout: 2)
        XCTAssertEqual(rec.all, ["survives the kill"])
    }

    /// A flush consumes the pending save. Two kills — a retry, a double-click —
    /// must not post the draft twice.
    func testASecondKillAfterAFlushSavesNothingMore() {
        let rec = edgeSaveRecorder()
        let fired = expectation(description: "first flush")
        rec.onSave { fired.fulfill() }
        let d = ComposerDraft(debounce: edgeLongDebounce, save: { rec.record($0) })

        d.typed("once")
        d.jobKilled()
        wait(for: [fired], timeout: 2)

        // Arm the refusal BEFORE the second kill, so a second flush is caught
        // by the expectation as well as by the recorded list.
        let never = XCTestExpectation(description: "no second flush")
        never.isInverted = true
        rec.onSave { never.fulfill() }
        d.jobKilled()
        wait(for: [never], timeout: 0.3)
        XCTAssertEqual(rec.all, ["once"], "the flush consumed the pending save")
    }

    /// Kill with nothing pending saves nothing. "Fires any PENDING save" — with
    /// none pending there is nothing to fire, and a flush that invented one
    /// would post an empty draft over the server's stored text.
    func testKillWithNothingPendingSavesNothing() {
        let rec = edgeSaveRecorder()
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })
        d.jobKilled()
        edgeAssertNoSave(rec, "kill with nothing pending must save nothing")
    }

    /// Kill after a cancel is the same case one step further along: the cancel
    /// already emptied the pending slot, so the kill has nothing to flush.
    func testKillAfterACancelSavesNothing() {
        let rec = edgeSaveRecorder()
        let d = ComposerDraft(debounce: edgeShortDebounce, save: { rec.record($0) })
        d.typed("sent already")
        d.willSend()
        d.didSend()
        d.jobKilled()
        edgeAssertNoSave(rec, "a cancelled save must not be resurrected by a kill")
    }
}

// MARK: - S5, artifacts and threads

final class HudEdgeArtifactTests: XCTestCase {

    // MARK: ArtifactKind

    /// Extension matching is case-insensitive: the server serves whatever the
    /// agent named the file, and `DIAGRAM.PNG` is a real filename.
    func testArtifactKindIsCaseInsensitive() {
        for ext in ["png", "PNG", "PnG", "jpg", "JPG", "jpeg", "JPEG", "gif", "GIF", "webp", "WEBP"] {
            XCTAssertEqual(ArtifactKind.kind(forExtension: ext), .image, ext)
        }
        for ext in ["html", "HTML", "Html", "svg", "SVG", "pdf", "PDF"] {
            XCTAssertEqual(ArtifactKind.kind(forExtension: ext), .web, ext)
        }
    }

    /// Text is the FALLBACK, not a third list — everything unrecognised renders
    /// through the markdown path rather than refusing. The empty extension is
    /// the one that actually occurs (an artifact named `LICENSE`, a job log).
    func testEveryUnrecognisedExtensionFallsBackToText() {
        for ext in ["", "md", "MD", "txt", "log", "yaml", "json", "go", "swift", "exe", "p", "pn", "pngg", "htm"] {
            XCTAssertEqual(
                ArtifactKind.kind(forExtension: ext), .text,
                "\(String(reflecting: ext)) must fall back to text"
            )
        }
    }

    // MARK: ArtifactSequence

    /// The index is found by URL EQUALITY, so a URL that is not in the list has
    /// no index — nil, rather than 0. Returning 0 would silently page the user
    /// to the first artifact when a live cell replaced the one they were
    /// reading.
    func testIndexRefusesAURLThatIsNotInTheList() {
        XCTAssertNil(ArtifactSequence.index(of: "/z", in: ["/a", "/b"]))
        XCTAssertNil(ArtifactSequence.index(of: "/a", in: []))
        XCTAssertNil(ArtifactSequence.index(of: "", in: ["/a"]))
        XCTAssertNil(ArtifactSequence.index(of: "/A", in: ["/a"]), "URL equality is exact")
        XCTAssertNil(ArtifactSequence.index(of: "/a ", in: ["/a"]))
    }

    /// Both ends of the list, so the lookup is a real search rather than a
    /// constant.
    func testIndexFindsBothEnds() {
        let urls = ["/a", "/b", "/c"]
        XCTAssertEqual(ArtifactSequence.index(of: "/a", in: urls), 0)
        XCTAssertEqual(ArtifactSequence.index(of: "/c", in: urls), 2)
        XCTAssertEqual(ArtifactSequence.index(of: "/b", in: urls), 1)
    }

    /// The pager CLAMPS and NEVER wraps. Wrapping is the plausible-looking
    /// alternative and it is wrong in a specific way: paging left off the first
    /// artifact would jump to the last, which reads as the overlay having
    /// closed and reopened somewhere else.
    func testStepClampsAtBothEndsAndNeverWraps() {
        XCTAssertEqual(ArtifactSequence.step(from: 0, by: -1, count: 3), 0, "left off the start")
        XCTAssertEqual(ArtifactSequence.step(from: 0, by: -99, count: 3), 0)
        XCTAssertEqual(ArtifactSequence.step(from: 2, by: 1, count: 3), 2, "right off the end")
        XCTAssertEqual(ArtifactSequence.step(from: 2, by: 99, count: 3), 2)
        XCTAssertEqual(ArtifactSequence.step(from: 1, by: -1, count: 3), 0)
        XCTAssertEqual(ArtifactSequence.step(from: 1, by: 1, count: 3), 2)
        XCTAssertEqual(ArtifactSequence.step(from: 1, by: 0, count: 3), 1)
    }

    /// A one-artifact job: every key press stays put. This is the common case
    /// for an explanation job that has shipped only `00-overview.md`.
    func testStepOnASingleArtifactAlwaysStaysAtZero() {
        for delta in [-99, -1, 0, 1, 99] {
            XCTAssertEqual(ArtifactSequence.step(from: 0, by: delta, count: 1), 0, "delta \(delta)")
        }
    }

    /// A starting index already outside the list clamps into it. Reached when
    /// the artifact list SHRINKS under an open pager — a cell deleting a file
    /// between two polls.
    func testStepClampsAStartingIndexThatIsAlreadyOutOfRange() {
        XCTAssertEqual(ArtifactSequence.step(from: 9, by: 0, count: 3), 2)
        XCTAssertEqual(ArtifactSequence.step(from: 9, by: 1, count: 3), 2)
        XCTAssertEqual(ArtifactSequence.step(from: -4, by: 0, count: 3), 0)
        XCTAssertEqual(ArtifactSequence.step(from: -4, by: -1, count: 3), 0)
    }

    // MARK: ArtifactDraftStash

    /// An un-banked URL restores nil — the pager must not hand the user another
    /// artifact's half-written feedback.
    func testRestoringAnUnbankedURLReturnsNil() {
        let s = ArtifactDraftStash()
        XCTAssertNil(s.restore(url: "/artifact/a"))
        s.bank(url: "/artifact/a", mode: "note", text: "hello")
        XCTAssertNil(s.restore(url: "/artifact/b"), "banking one URL must not populate another")
        XCTAssertNil(s.restore(url: ""), "and the empty URL is just another key")
    }

    /// Text AND mode round-trip together: the mode is which composer the text
    /// was typed into (feedback vs ask), and restoring text into the wrong one
    /// posts it to the wrong route.
    func testBankedTextAndModeRoundTripPerURL() {
        let s = ArtifactDraftStash()
        s.bank(url: "/a", mode: "feedback", text: "on a")
        s.bank(url: "/b", mode: "ask", text: "on b")

        let a = s.restore(url: "/a")
        XCTAssertEqual(a?.mode, "feedback")
        XCTAssertEqual(a?.text, "on a")

        let b = s.restore(url: "/b")
        XCTAssertEqual(b?.mode, "ask")
        XCTAssertEqual(b?.text, "on b")
    }

    /// Restore is a READ, not a pop: paging away and back twice must still find
    /// the text. A destructive restore loses the draft on the second visit,
    /// which is exactly when the user expects it to still be there.
    func testRestoreDoesNotConsumeTheBankedDraft() {
        let s = ArtifactDraftStash()
        s.bank(url: "/a", mode: "feedback", text: "kept")
        XCTAssertEqual(s.restore(url: "/a")?.text, "kept")
        XCTAssertEqual(s.restore(url: "/a")?.text, "kept", "restore must not consume")
        XCTAssertEqual(s.restore(url: "/a")?.mode, "feedback")
    }

    /// Banking EMPTY text clears rather than stores. The distinction is
    /// observable: a stored empty draft restores as a non-nil pair, which is
    /// how a composer decides to reopen itself — so a user who cleared the box
    /// would find it reopening on every page turn.
    func testBankingEmptyTextClearsInsteadOfStoring() {
        let s = ArtifactDraftStash()
        s.bank(url: "/a", mode: "feedback", text: "typed something")
        XCTAssertNotNil(s.restore(url: "/a"))

        s.bank(url: "/a", mode: "feedback", text: "")
        XCTAssertNil(s.restore(url: "/a"), "empty text clears the entry")
    }

    /// The clear is unconditional on mode: banking empty text under a DIFFERENT
    /// mode still clears, since the clause is about the text.
    func testBankingEmptyTextClearsRegardlessOfMode() {
        let s = ArtifactDraftStash()
        s.bank(url: "/a", mode: "feedback", text: "typed")
        s.bank(url: "/a", mode: "ask", text: "")
        XCTAssertNil(s.restore(url: "/a"))
    }

    /// Banking empty text on an already-absent URL is a no-op, not an insert.
    func testBankingEmptyTextOnAnUnbankedURLStoresNothing() {
        let s = ArtifactDraftStash()
        s.bank(url: "/fresh", mode: "feedback", text: "")
        XCTAssertNil(s.restore(url: "/fresh"))
    }

    /// `clear` drops the entry, and clearing something absent is harmless.
    func testClearDropsOnlyTheNamedURL() {
        let s = ArtifactDraftStash()
        s.bank(url: "/a", mode: "feedback", text: "on a")
        s.bank(url: "/b", mode: "feedback", text: "on b")

        s.clear(url: "/a")
        XCTAssertNil(s.restore(url: "/a"))
        XCTAssertEqual(s.restore(url: "/b")?.text, "on b", "clear must be scoped to its URL")

        s.clear(url: "/never-banked")
        s.clear(url: "/a")
        XCTAssertNil(s.restore(url: "/a"), "a redundant clear is harmless")
    }

    /// Re-banking after a clear works — the entry is removed, not tombstoned.
    func testAURLCanBeRebankedAfterAClear() {
        let s = ArtifactDraftStash()
        s.bank(url: "/a", mode: "feedback", text: "first")
        s.clear(url: "/a")
        s.bank(url: "/a", mode: "ask", text: "second")
        XCTAssertEqual(s.restore(url: "/a")?.text, "second")
        XCTAssertEqual(s.restore(url: "/a")?.mode, "ask")
    }

    // MARK: AskThread.title — two budgets and a grapheme

    /// The 40-char name budget, exactly at its edge and one past it. A name at
    /// 40 survives whole; the 41st character of a longer one must not appear
    /// anywhere in the result, whatever truncation marker the implementation
    /// chooses.
    func testTitleBudgetsTheArtifactNameAtFortyCharacters() {
        let exactly40 = String(repeating: "n", count: 40)
        let title40 = AskThread.title(artifactName: exactly40, question: "q")
        XCTAssertTrue(title40.contains(exactly40), "a 40-char name fits its budget whole")

        let long = String(repeating: "n", count: 60)
        let titleLong = AskThread.title(artifactName: long, question: "q")
        XCTAssertFalse(
            titleLong.contains(String(repeating: "n", count: 41)),
            "no more than 40 characters of the name may survive"
        )
    }

    /// A 200-character artifact name — a real possibility for an agent-authored
    /// diagram card — must not blow either budget.
    func testAVeryLongArtifactNameBlowsNeitherBudget() {
        let name = String(repeating: "n", count: 200)
        let title = AskThread.title(artifactName: name, question: "why is this here?")
        XCTAssertLessThanOrEqual(title.count, 80, "the whole result is capped at 80")
        XCTAssertFalse(title.contains(String(repeating: "n", count: 41)), "the name budget still holds")
    }

    /// Only the question's FIRST line is used. A steer-style question is
    /// routinely a paragraph, and a thread title with a newline in it is a
    /// broken row rather than a long one.
    func testTitleTakesOnlyTheFirstLineOfTheQuestion() {
        let title = AskThread.title(
            artifactName: "00-overview.md",
            question: "first line\nSECONDLINE\nthird"
        )
        XCTAssertFalse(title.contains("SECONDLINE"), "later lines must not appear")
        XCTAssertFalse(title.contains("\n"), "and no newline may survive into the title")
        XCTAssertTrue(title.contains("first line"))
    }

    /// A `\r\n` question is the same case arriving from a pasted Windows-authored
    /// note: the first line still ends at the break.
    func testTitleTreatsACarriageReturnLineBreakAsALineBreak() {
        let title = AskThread.title(artifactName: "00-overview.md", question: "first\r\nSECONDLINE")
        XCTAssertFalse(title.contains("SECONDLINE"))
    }

    /// An empty question leaves the artifact name — the title must still name
    /// what it is about rather than collapsing to a separator.
    func testAnEmptyQuestionStillNamesTheArtifact() {
        let title = AskThread.title(artifactName: "50-contract.md", question: "")
        XCTAssertTrue(title.contains("50-contract.md"), "the name survives an empty question")
        XCTAssertLessThanOrEqual(title.count, 80)
    }

    /// The 80-char cap is never exceeded, whatever the inputs.
    func testTitleNeverExceedsEightyCharacters() {
        let cases: [(String, String)] = [
            ("", ""),
            ("a.md", "short"),
            (String(repeating: "n", count: 40), String(repeating: "q", count: 200)),
            (String(repeating: "n", count: 300), String(repeating: "q", count: 300)),
            ("00-overview.md", String(repeating: "why ", count: 60)),
        ]
        for (name, question) in cases {
            let title = AskThread.title(artifactName: name, question: question)
            XCTAssertLessThanOrEqual(
                title.count, 80,
                "name=\(name.count) question=\(question.count) produced \(title.count) chars"
            )
        }
    }

    /// A ZWJ emoji sitting inside the 40-char NAME budget must survive whole.
    /// The name below is 39 Characters but 45 unicode SCALARS, so a budget
    /// applied to scalars cuts the family in half — the exact failure the
    /// "cut surrogate-safely" clause names, and one that looks like a
    /// well-formed string in a debugger.
    func testTheNameBudgetCountsGraphemesNotScalars() {
        XCTAssertEqual(edgeFamily.count, 1, "fixture sanity: one grapheme")
        XCTAssertEqual(edgeFamily.unicodeScalars.count, 7, "fixture sanity: seven scalars")

        let name = String(repeating: "a", count: 38) + edgeFamily   // 39 graphemes, 45 scalars
        XCTAssertEqual(name.count, 39)
        let title = AskThread.title(artifactName: name, question: "q")
        XCTAssertTrue(title.contains(edgeFamily), "the emoji is inside the 40-char budget and must survive whole")
    }

    /// The same grapheme, this time exactly at the 80-char OVERALL cap:
    /// `"a.md"` (4) + `" — "` (3) + 72 question characters + the family = 80.
    /// It fits, so it must arrive intact.
    func testAGraphemeSittingExactlyAtTheEightyCharCapSurvives() {
        let question = String(repeating: "b", count: 72) + edgeFamily
        let title = AskThread.title(artifactName: "a.md", question: question)
        XCTAssertLessThanOrEqual(title.count, 80)
        XCTAssertTrue(title.contains(edgeFamily), "a grapheme that fits the cap must not be cut")
    }

    /// And one character past it: the family is now beyond the cap and must be
    /// dropped WHOLE. A surviving zero-width joiner is positive evidence that a
    /// multi-scalar grapheme was cut through the middle — nothing else in these
    /// inputs contains one.
    func testAGraphemeStraddlingTheEightyCharCapIsDroppedWhole() {
        let question = String(repeating: "b", count: 73) + edgeFamily
        let title = AskThread.title(artifactName: "a.md", question: question)
        XCTAssertLessThanOrEqual(title.count, 80)
        XCTAssertFalse(
            title.unicodeScalars.contains(edgeZWJ),
            "a dangling ZWJ means the emoji was split rather than dropped"
        )
    }

    // MARK: AskThread.seed

    /// The seed is what the forked thread wakes up holding, so it has to carry
    /// both halves of the context: which artifact, and what was asked.
    func testSeedCarriesTheArtifactNameAndTheQuestion() {
        let seed = AskThread.seed(
            artifactName: "50-contract.md",
            question: "why is the reorder tolerance exactly one?"
        )
        XCTAssertTrue(seed.contains("50-contract.md"), "the seed must name the artifact")
        XCTAssertTrue(
            seed.contains("why is the reorder tolerance exactly one?"),
            "the seed must contain the question"
        )
    }

    /// A multi-line question reaches the seed WHOLE — unlike the title, which is
    /// a one-line label. The seed is the thread's prompt, and truncating it
    /// there would silently drop the part of the question that mattered.
    func testSeedIsNotTruncatedLikeTheTitle() {
        let question = String(repeating: "why does this happen? ", count: 20)
        let seed = AskThread.seed(artifactName: "00-overview.md", question: question)
        XCTAssertTrue(seed.contains(question), "the seed carries the whole question")
        XCTAssertGreaterThan(seed.count, 80, "the 80-char title cap is not the seed's cap")
    }
}

// MARK: - S6, notifications, bands, paging, watch sort

final class HudEdgeNotificationTests: XCTestCase {

    private func edgeSnap(_ slug: String, _ column: BoardColumn, erroring: Bool = false) -> JobSnapshot {
        JobSnapshot(slug: slug, column: column, erroring: erroring)
    }

    /// Nothing in, nothing out — and specifically an empty ARRAY. The engine
    /// runs on every board poll, so the no-news case is the overwhelmingly
    /// common one.
    func testEmptyBoardAndEmptySetsFireNothing() {
        XCTAssertTrue(
            NotificationDiff.transitions(
                prevBlocked: [], prevWorking: [], prevErroring: [], board: []
            ).isEmpty
        )
    }

    /// Steady state fires nothing: the same job in the same column across two
    /// polls is not news. This is the clause that keeps a 3-second poll from
    /// notifying every 3 seconds.
    func testSteadyStateFiresNothing() {
        let board = [
            edgeSnap("a", .blocked),
            edgeSnap("b", .working),
            edgeSnap("c", .working, erroring: true),
        ]
        let out = NotificationDiff.transitions(
            prevBlocked: ["a"], prevWorking: ["b", "c"], prevErroring: ["c"], board: board
        )
        XCTAssertTrue(out.isEmpty, "unchanged sets → empty array, got \(edgeKeys(out))")
    }

    /// Only ENTRIES fire. A job in the previous set that is no longer on the
    /// board at all — archived, deleted, or simply gone — is a departure, and a
    /// departure is not a notification.
    func testAJobLeavingTheBoardFiresNothing() {
        let out = NotificationDiff.transitions(
            prevBlocked: ["gone"], prevWorking: [], prevErroring: [], board: []
        )
        XCTAssertTrue(out.isEmpty, "a departure is not a transition, got \(edgeKeys(out))")
    }

    /// Columns that are not blocked or working produce nothing at all, so a job
    /// settling into done or sitting in draft is silent.
    func testQuietColumnsFireNothing() {
        let board = [
            edgeSnap("a", .draft),
            edgeSnap("b", .queued),
            edgeSnap("c", .done),
        ]
        let out = NotificationDiff.transitions(
            prevBlocked: [], prevWorking: [], prevErroring: [], board: board
        )
        XCTAssertTrue(out.isEmpty, "draft/queued/done are not notifiable entries, got \(edgeKeys(out))")
    }

    /// The three transition kinds, all newly entering in one payload. Order is
    /// deliberately not asserted — the contract does not name one.
    func testAllThreeTransitionsCanFireInOnePayload() {
        let board = [
            edgeSnap("newly-blocked", .blocked),
            edgeSnap("newly-working", .working),
            edgeSnap("newly-erroring", .working, erroring: true),
        ]
        let out = NotificationDiff.transitions(
            prevBlocked: [], prevWorking: ["newly-erroring"], prevErroring: [], board: board
        )
        XCTAssertEqual(
            edgeKeys(out),
            ["blocked:newly-blocked", "started:newly-working", "erroring:newly-erroring"]
        )
        XCTAssertEqual(out.count, 3, "one transition per entry, no duplicates")
    }

    /// A job entering TWO sets at once fires twice — "one transition per entry"
    /// counts entries into sets, not jobs. A cell that starts and immediately
    /// errors produces exactly this.
    func testAJobEnteringTwoSetsAtOnceFiresTwice() {
        let out = NotificationDiff.transitions(
            prevBlocked: [], prevWorking: [], prevErroring: [],
            board: [edgeSnap("both", .working, erroring: true)]
        )
        XCTAssertEqual(edgeKeys(out), ["started:both", "erroring:both"])
        XCTAssertEqual(out.count, 2)
    }

    /// The re-entry case, driven across three polls with the caller-maintained
    /// sets the contract's signature implies. A job that was blocked, got
    /// unblocked, and blocked AGAIN must notify the second time — this is the
    /// notification that actually matters, and an engine that only ever adds to
    /// its sets never sends it.
    func testAJobLeavingAndReenteringFiresAgain() {
        // Poll 1: nothing known, `a` is blocked.
        let first = NotificationDiff.transitions(
            prevBlocked: [], prevWorking: [], prevErroring: [],
            board: [edgeSnap("a", .blocked)]
        )
        XCTAssertEqual(edgeKeys(first), ["blocked:a"])

        // Poll 2: `a` was unblocked and is running. It LEFT the blocked set.
        let second = NotificationDiff.transitions(
            prevBlocked: ["a"], prevWorking: [], prevErroring: [],
            board: [edgeSnap("a", .working)]
        )
        XCTAssertEqual(edgeKeys(second), ["started:a"])

        // Poll 3: `a` is blocked again — and the sets the caller now holds no
        // longer contain it under blocked.
        let third = NotificationDiff.transitions(
            prevBlocked: [], prevWorking: ["a"], prevErroring: [],
            board: [edgeSnap("a", .blocked)]
        )
        XCTAssertEqual(edgeKeys(third), ["blocked:a"], "a re-entry is news again")
    }

    /// An erroring flag CLEARING and returning fires again on the return, for
    /// the same reason.
    func testErroringClearingAndReturningFiresAgain() {
        let cleared = NotificationDiff.transitions(
            prevBlocked: [], prevWorking: ["a"], prevErroring: ["a"],
            board: [edgeSnap("a", .working, erroring: false)]
        )
        XCTAssertTrue(cleared.isEmpty, "clearing an error is not a notification")

        let returned = NotificationDiff.transitions(
            prevBlocked: [], prevWorking: ["a"], prevErroring: [],
            board: [edgeSnap("a", .working, erroring: true)]
        )
        XCTAssertEqual(edgeKeys(returned), ["erroring:a"])
    }

    /// Stale members of the previous sets — jobs that moved elsewhere on the
    /// board — must not fire anything on their own account.
    func testJobsMovingOutOfASetFireNothingForTheMove() {
        let out = NotificationDiff.transitions(
            prevBlocked: ["a", "b", "c"], prevWorking: [], prevErroring: [],
            board: [edgeSnap("a", .done), edgeSnap("b", .queued), edgeSnap("c", .draft)]
        )
        XCTAssertTrue(out.isEmpty, "leaving blocked for a quiet column is silent, got \(edgeKeys(out))")
    }

    // MARK: NotifiedSets

    /// The persistence shape round-trips as identity — including when it is
    /// EMPTY, which is what a first launch writes. An encoder that dropped empty
    /// collections and a decoder that required them would fail exactly here and
    /// nowhere else, and the symptom would be a first-launch crash or a set that
    /// silently resets every relaunch (re-notifying every blocked job).
    func testNotifiedSetsRoundTripsWhenEmpty() throws {
        let empty = NotifiedSets(blocked: [], working: [], erroring: [])
        let data = try JSONEncoder().encode(empty)
        let back = try JSONDecoder().decode(NotifiedSets.self, from: data)
        XCTAssertEqual(back, empty)
        XCTAssertTrue(back.blocked.isEmpty)
        XCTAssertTrue(back.working.isEmpty)
        XCTAssertTrue(back.erroring.isEmpty)
    }

    /// The populated round trip, with the three sets deliberately DIFFERENT so a
    /// coding-key swap (blocked decoded into working) cannot pass.
    func testNotifiedSetsRoundTripsWithDistinctSets() throws {
        let sets = NotifiedSets(
            blocked: ["b1", "b2"],
            working: ["w1"],
            erroring: ["e1", "e2", "e3"]
        )
        let back = try JSONDecoder().decode(
            NotifiedSets.self, from: try JSONEncoder().encode(sets)
        )
        XCTAssertEqual(back, sets)
        XCTAssertEqual(back.blocked, ["b1", "b2"])
        XCTAssertEqual(back.working, ["w1"])
        XCTAssertEqual(back.erroring, ["e1", "e2", "e3"])
    }

    /// A partially-empty shape — the ordinary steady state on a quiet board.
    func testNotifiedSetsRoundTripsWithSomeSetsEmpty() throws {
        let sets = NotifiedSets(blocked: ["only"], working: [], erroring: [])
        let back = try JSONDecoder().decode(
            NotifiedSets.self, from: try JSONEncoder().encode(sets)
        )
        XCTAssertEqual(back, sets)
    }

    // MARK: UsageBand — the two boundaries

    /// The band boundaries, each probed on both sides. `<50` ok, `50...79`
    /// warn, `>=80` danger: 49/50 and 79/80 are where an off-by-one lives, and
    /// this is a meter that decides whether the user sees a warning colour
    /// before an account runs out mid-job.
    func testUsageBandsAtTheirExactBoundaries() {
        XCTAssertEqual(UsageBand.band(49), .ok)
        XCTAssertEqual(UsageBand.band(50), .warn)
        XCTAssertEqual(UsageBand.band(79), .warn)
        XCTAssertEqual(UsageBand.band(80), .danger)
    }

    /// The interiors and the far ends, including values outside 0...100 — a
    /// percentage computed from a pool that reports more usage than its quota
    /// is not hypothetical.
    func testUsageBandsAcrossTheWholeIntegerRange() {
        XCTAssertEqual(UsageBand.band(0), .ok)
        XCTAssertEqual(UsageBand.band(1), .ok)
        XCTAssertEqual(UsageBand.band(-1), .ok, "below 50 is ok, negatives included")
        XCTAssertEqual(UsageBand.band(-100), .ok)
        XCTAssertEqual(UsageBand.band(51), .warn)
        XCTAssertEqual(UsageBand.band(99), .danger)
        XCTAssertEqual(UsageBand.band(100), .danger)
        XCTAssertEqual(UsageBand.band(1000), .danger)
    }

    // MARK: FlagCount

    /// The plain sum, including the all-zero case that must read as no flag at
    /// all rather than as a badge showing 0.
    func testFlagCountIsThePlainSum() {
        XCTAssertEqual(FlagCount.count(pendingQuestions: 0, unhealthyCells: 0, openP0DocQuestions: 0), 0)
        XCTAssertEqual(FlagCount.count(pendingQuestions: 1, unhealthyCells: 0, openP0DocQuestions: 0), 1)
        XCTAssertEqual(FlagCount.count(pendingQuestions: 0, unhealthyCells: 1, openP0DocQuestions: 0), 1)
        XCTAssertEqual(FlagCount.count(pendingQuestions: 0, unhealthyCells: 0, openP0DocQuestions: 1), 1)
        XCTAssertEqual(FlagCount.count(pendingQuestions: 2, unhealthyCells: 3, openP0DocQuestions: 4), 9)
    }

    // MARK: LogPaging

    /// Count alone is not enough: the log view is a sliding fixed-size window,
    /// so a busy daemon writes N new lines and drops N old ones while the count
    /// never moves. A `prevCount != nextCount` implementation freezes the view
    /// permanently at exactly the moment the log is most interesting.
    func testLogChangeDetectionCatchesASlidingWindow() {
        XCTAssertTrue(
            LogPaging.changed(prevCount: 200, prevLast: "old", nextCount: 200, nextLast: "new"),
            "same count, different last entry = changed"
        )
        XCTAssertTrue(
            LogPaging.changed(prevCount: 199, prevLast: "same", nextCount: 200, nextLast: "same"),
            "different count, same last entry = changed"
        )
        XCTAssertFalse(
            LogPaging.changed(prevCount: 200, prevLast: "same", nextCount: 200, nextLast: "same"),
            "both unchanged = unchanged"
        )
    }

    /// The nil-last cases: an empty log, and the first line arriving into one.
    func testLogChangeDetectionHandlesAnAbsentLastEntry() {
        XCTAssertFalse(
            LogPaging.changed(prevCount: 0, prevLast: nil, nextCount: 0, nextLast: nil),
            "empty and still empty"
        )
        XCTAssertTrue(
            LogPaging.changed(prevCount: 0, prevLast: nil, nextCount: 1, nextLast: "first"),
            "the first line is a change"
        )
        XCTAssertTrue(
            LogPaging.changed(prevCount: 1, prevLast: "gone", nextCount: 1, nextLast: nil),
            "losing the last entry is a change"
        )
    }

    // MARK: DreamText

    /// An absent heading counts zero rather than falling through to the whole
    /// document — the dream report's sections are optional, and a missing one
    /// must read as "nothing here", not as "everything".
    func testAnAbsentHeadingCountsZero() {
        let md = """
        # Report

        ## Prompting

        - one
        - two
        """
        XCTAssertEqual(DreamText.sectionCount(markdown: md, heading: "Watchlist"), 0)
        XCTAssertEqual(DreamText.sectionCount(markdown: "", heading: "Prompting"), 0)
        XCTAssertEqual(DreamText.sectionCount(markdown: md, heading: ""), 0)
    }

    /// The count stops at the NEXT heading of any level, so a following section's
    /// bullets are not swept in.
    func testTheCountStopsAtTheNextHeadingOfAnyLevel() {
        let md = """
        ## Prompting

        - one
        - two

        ### Something else

        - not counted
        - nor this
        - nor this either
        """
        XCTAssertEqual(DreamText.sectionCount(markdown: md, heading: "Prompting"), 2)
    }

    /// The FIRST matching heading owns the count — a later repeat of the same
    /// name does not add to it.
    func testOnlyTheFirstMatchingHeadingIsCounted() {
        let md = """
        ## Prompting

        - one

        ## Elsewhere

        - ignored

        ## Prompting

        - not counted
        - nor this
        """
        XCTAssertEqual(DreamText.sectionCount(markdown: md, heading: "Prompting"), 1)
    }

    /// A heading with no bullets under it is zero, not one — an empty section is
    /// a real outcome on a quiet night.
    func testAHeadingWithNoBulletsCountsZero() {
        let md = """
        ## Prompting

        Nothing to report tonight.

        ## Next
        """
        XCTAssertEqual(DreamText.sectionCount(markdown: md, heading: "Prompting"), 0)
    }

    // MARK: WatchSort

    /// The degenerate inputs first: an empty list sorts to empty, a single row
    /// to itself.
    func testWatchSortHandlesEmptyAndSingletonInputs() {
        XCTAssertTrue(WatchSort.sort([]).isEmpty)
        let one = WatchSortRow(id: 1, verdict: nil, lastSeen: Date(timeIntervalSince1970: 0))
        XCTAssertEqual(WatchSort.sort([one]), [one])
    }

    /// `fine` rows sink below every open row regardless of how recently they
    /// were seen — the group is the primary key, and a `fine` row seen a second
    /// ago must not outrank an open one seen last week.
    func testFineRowsSinkBelowOpenRowsWhateverTheirRecency() {
        let freshFine = WatchSortRow(id: 1, verdict: "fine", lastSeen: Date(timeIntervalSince1970: 9_000))
        let staleOpen = WatchSortRow(id: 2, verdict: nil, lastSeen: Date(timeIntervalSince1970: 10))
        let staleFix = WatchSortRow(id: 3, verdict: "fix", lastSeen: Date(timeIntervalSince1970: 5))

        let out = WatchSort.sort([freshFine, staleOpen, staleFix])
        XCTAssertEqual(out.map(\.id), [2, 3, 1], "nil and \"fix\" are both open and both outrank \"fine\"")
    }

    /// Within a group: `lastSeen` DESC, then `id` DESC as the tie-break. The tie
    /// is not hypothetical — two observations swept from the same episode carry
    /// the same timestamp, and without a total order the list reshuffles itself
    /// on every poll.
    func testWatchSortBreaksTimestampTiesByIdDescending() {
        let t = Date(timeIntervalSince1970: 1_000)
        let rows = [
            WatchSortRow(id: 1, verdict: nil, lastSeen: t),
            WatchSortRow(id: 3, verdict: "fix", lastSeen: t),
            WatchSortRow(id: 2, verdict: nil, lastSeen: t),
        ]
        XCTAssertEqual(WatchSort.sort(rows).map(\.id), [3, 2, 1])
    }

    /// The recency ordering itself, and that it applies INSIDE the `fine` group
    /// too rather than only to open rows.
    func testWatchSortOrdersEachGroupByRecency() {
        let rows = [
            WatchSortRow(id: 10, verdict: nil, lastSeen: Date(timeIntervalSince1970: 100)),
            WatchSortRow(id: 11, verdict: nil, lastSeen: Date(timeIntervalSince1970: 300)),
            WatchSortRow(id: 12, verdict: "fine", lastSeen: Date(timeIntervalSince1970: 100)),
            WatchSortRow(id: 13, verdict: "fine", lastSeen: Date(timeIntervalSince1970: 300)),
        ]
        XCTAssertEqual(WatchSort.sort(rows).map(\.id), [11, 10, 13, 12])
    }
}

// MARK: - S1, shell + routing: the fallbacks

final class HudEdgeRoutingTests: XCTestCase {

    /// The same-machine default, asserted on the URL's PARTS rather than its
    /// string form: the parts are what a request is built from, and pinning
    /// them is immune to a trailing slash the contract does not legislate.
    /// `127.0.0.1` specifically — the iOS client's tailnet default landing here
    /// would be a working app on one machine and a broken one everywhere else.
    private func edgeAssertLocalDefault(
        _ url: URL,
        _ what: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) {
        XCTAssertEqual(url.scheme, "http", "\(what): scheme", file: file, line: line)
        XCTAssertEqual(url.host, "127.0.0.1", "\(what): host", file: file, line: line)
        XCTAssertEqual(url.port, 7777, "\(what): port", file: file, line: line)
    }

    /// The three absences the contract names: nil (never configured), empty
    /// (a cleared settings field), and a string that is not a URL at all.
    func testBaseURLFallsBackForNilEmptyAndUnparseableValues() {
        edgeAssertLocalDefault(ServerConfig.baseURL(stored: nil), "nil")
        edgeAssertLocalDefault(ServerConfig.baseURL(stored: ""), "empty string")
        edgeAssertLocalDefault(ServerConfig.baseURL(stored: "not a url at all"), "spaces")
        edgeAssertLocalDefault(ServerConfig.baseURL(stored: " "), "a single space")
        edgeAssertLocalDefault(ServerConfig.baseURL(stored: "   "), "whitespace only")
    }

    /// A valid stored URL wins outright — otherwise the settings field is
    /// decorative and a user pointing the app at another host is silently
    /// ignored.
    func testAValidStoredURLWins() {
        let u = ServerConfig.baseURL(stored: "http://192.168.1.50:9999")
        XCTAssertEqual(u.scheme, "http")
        XCTAssertEqual(u.host, "192.168.1.50")
        XCTAssertEqual(u.port, 9999)

        let https = ServerConfig.baseURL(stored: "https://kraken.example.com")
        XCTAssertEqual(https.scheme, "https")
        XCTAssertEqual(https.host, "kraken.example.com")
    }

    /// A stored value identical to the default is not a special case.
    func testTheDefaultMayAlsoBeStoredExplicitly() {
        edgeAssertLocalDefault(ServerConfig.baseURL(stored: "http://127.0.0.1:7777"), "explicit default")
    }

    // MARK: MacRoute

    private func edgeURL(_ s: String, file: StaticString = #filePath, line: UInt = #line) -> URL {
        guard let u = URL(string: s) else {
            XCTFail("fixture is not a URL: \(s)", file: file, line: line)
            return URL(string: "kraken://placeholder")!
        }
        return u
    }

    /// A deep link is an input from OUTSIDE the app — a tapped link, a pasted
    /// string, a shortcut. An unknown host is refused rather than routed
    /// somewhere plausible, because routing a garbage URL to the board is
    /// indistinguishable from the user having asked for the board.
    func testUnknownHostsAreRefused() {
        for s in [
            "kraken://nonsense",
            "kraken://jobs",          // plural: close, and still not a route
            "kraken://board2",
            "kraken://usage",
            "kraken://",
        ] {
            XCTAssertNil(MacRoute.parse(edgeURL(s)), "\(s) must not route")
        }
    }

    /// The five bare routes.
    func testTheBareRoutesMapToTheirCases() {
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://board")), .board(column: nil))
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://graph")), .graph)
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://log")), .log)
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://dream")), .dream)
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://settings")), .settings)
    }

    /// A column path selects a column; an INVALID one degrades to the whole
    /// board rather than to nil. That asymmetry with the unknown-host rule is
    /// the contract's own: the user did ask for the board, and only the column
    /// was unreadable.
    func testAValidColumnSelectsItAndAnInvalidOneDegradesToTheWholeBoard() {
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://board/blocked")), .board(column: .blocked))
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://board/draft")), .board(column: .draft))
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://board/queued")), .board(column: .queued))
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://board/working")), .board(column: .working))
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://board/done")), .board(column: .done))

        for bad in ["kraken://board/nonsense", "kraken://board/reaped", "kraken://board/1"] {
            XCTAssertEqual(
                MacRoute.parse(edgeURL(bad)), .board(column: nil),
                "\(bad) must degrade to the whole board, not to nil"
            )
        }
    }

    /// A job route carries its slug through verbatim, including the characters
    /// a real slug contains.
    func testJobRoutesCarryTheirSlug() {
        XCTAssertEqual(MacRoute.parse(edgeURL("kraken://job/foo")), .job(slug: "foo"))
        XCTAssertEqual(
            MacRoute.parse(edgeURL("kraken://job/macos-native-kraken-237")),
            .job(slug: "macos-native-kraken-237")
        )
    }

    /// The route cases are distinguishable — a `board(nil)` is not a `graph`,
    /// and `board(.blocked)` is not `board(nil)`. Equatable on an enum with
    /// associated values is synthesized, but the associated value is what the
    /// screen actually reads.
    func testRouteCasesAreDistinguishedByTheirPayload() {
        XCTAssertNotEqual(MacRoute.board(column: nil), MacRoute.board(column: .blocked))
        XCTAssertNotEqual(MacRoute.job(slug: "a"), MacRoute.job(slug: "b"))
        XCTAssertNotEqual(MacRoute.graph, MacRoute.log)
    }
}
