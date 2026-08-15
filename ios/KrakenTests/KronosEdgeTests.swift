import XCTest
@testable import Kraken

// The kronos tab's refusal surface — Slice E's boundaries, malformed shapes and
// out-of-range numbers.
//
// Everything here is derived from 50-contract.md's "Amendments (round 5 — iOS
// kronos tab)" table and the `GET /api/kronos` section of ios/API.md. The happy
// paths are KronosContractTests' subject; what is pinned below is the far side
// of each stated rule:
//
//   * `KronosStatus`'s five non-context keys are "always present per the API
//     doc, so they are non-optional — a payload missing one is a decode ERROR,
//     not a default". A decoder that defaulted a missing `enabled` to false
//     would render the whole feature as off on a server that had merely
//     dropped a key, which is the one reading the tab exists to distinguish.
//   * `context`'s absence is "the wire's only spelling of 'never reported'"
//     while a PRESENT zeroed object "would instead claim a report OF zero,
//     which is a freshly compacted window and the opposite reading". Those two
//     shapes are one byte apart on the wire and must never collapse into each
//     other.
//   * `kronosMeterState` bands by INCLUSIVE thresholds and passes `pct` through
//     UNCLAMPED, while `kronosFillPct` clamps into 0...100. Two functions over
//     one number that disagree on clamping by design: the label tells the truth,
//     the bar geometry stays inside its track.
//
// Naming: every helper here is `kronosEdge`-prefixed so it cannot collide with
// the concurrently-authored KronosContractTests at package scope.

// MARK: - Fixtures

private func kronosEdgeDecode<T: Decodable>(_ json: String, as: T.Type = T.self) throws -> T {
    try JSONDecoder().decode(T.self, from: Data(json.utf8))
}

/// The five always-present keys, as raw JSON fragments, in the order ios/API.md
/// documents them. Held as a list rather than a literal string so a test can
/// remove exactly one key and leave the other four byte-identical — a
/// hand-written "payload missing `pane`" fixture drifts from the complete one
/// and turns a refusal test into a comparison of two different payloads.
private let kronosEdgeBaseFields: [(key: String, json: String)] = [
    ("enabled", "true"),
    ("live", "true"),
    ("session_id", "\"9f2c1a44-0e77-4b6d-9a1e-2f9f0c3d5b71\""),
    ("pane", "\"%17\""),
    ("spawned_at", "1786778082690"),
]

private func kronosEdgeStatusJSON(
    dropping drop: String? = nil,
    overriding overrides: [String: String] = [:],
    context: String? = nil
) -> String {
    var parts: [String] = []
    for field in kronosEdgeBaseFields where field.key != drop {
        parts.append("\"\(field.key)\": \(overrides[field.key] ?? field.json)")
    }
    if let context {
        parts.append("\"context\": \(context)")
    }
    return "{" + parts.joined(separator: ", ") + "}"
}

final class KronosEdgeTests: XCTestCase {

    // MARK: - The control

    /// The complete documented payload decodes, so every refusal below is a
    /// refusal of the thing that was removed rather than of the fixture itself.
    /// A `KronosStatus` that could not decode ANY body would pass all five
    /// missing-key tests on its own.
    func testCompletePayloadDecodesSoTheRefusalsBelowMeanSomething() throws {
        let s: KronosStatus = try kronosEdgeDecode(kronosEdgeStatusJSON())
        XCTAssertTrue(s.enabled)
        XCTAssertTrue(s.live)
        XCTAssertEqual(s.pane, "%17")
        XCTAssertEqual(s.spawnedAt, 1_786_778_082_690)
    }

    // MARK: - Decode refusals: the five non-context keys

    /// Each of the five always-present keys, dropped one at a time from an
    /// otherwise-complete body. The contract's word is ERROR, not default, for
    /// every one of them — `enabled` and `pane` most sharply, because a
    /// defaulted `false`/`""` is a legal-looking state (feature off; no pane)
    /// that the view would render as fact.
    func testEveryMissingNonContextKeyIsADecodeError() {
        for field in kronosEdgeBaseFields {
            XCTAssertThrowsError(
                try kronosEdgeDecode(kronosEdgeStatusJSON(dropping: field.key), as: KronosStatus.self),
                "a payload missing \(field.key) must not decode"
            )
        }
    }

    /// The two the slice brief calls out by name, asserted individually so a
    /// failure names the key rather than the loop index.
    func testMissingEnabledIsADecodeError() {
        XCTAssertThrowsError(
            try kronosEdgeDecode(kronosEdgeStatusJSON(dropping: "enabled"), as: KronosStatus.self)
        )
    }

    func testMissingPaneIsADecodeError() {
        XCTAssertThrowsError(
            try kronosEdgeDecode(kronosEdgeStatusJSON(dropping: "pane"), as: KronosStatus.self)
        )
    }

    /// An empty body is the degenerate case of the above — all five missing at
    /// once. Worth its own line because `{}` is what a route answers when it has
    /// been stubbed out or has failed halfway, and it is the single most likely
    /// shape to be silently accepted by a model with defaults.
    func testEmptyObjectIsADecodeError() {
        XCTAssertThrowsError(try kronosEdgeDecode("{}", as: KronosStatus.self))
    }

    /// The scalar types are not coerced. Go writes `enabled` as a JSON bool and
    /// `spawned_at` as a JSON integer; a string-wrapped bool, a string-wrapped
    /// number, or a fractional value where an Int64 is documented are all wire
    /// drift, and drift that decodes is drift nobody notices.
    func testWrongScalarTypesAreDecodeErrors() {
        let bad: [String: String] = [
            "enabled": "\"true\"",
            "live": "1",
            "session_id": "42",
            "pane": "17",
            "spawned_at": "\"1786778082690\"",
        ]
        for (key, value) in bad {
            XCTAssertThrowsError(
                try kronosEdgeDecode(kronosEdgeStatusJSON(overriding: [key: value]), as: KronosStatus.self),
                "\(key) as \(value) must not decode"
            )
        }
        XCTAssertThrowsError(
            try kronosEdgeDecode(kronosEdgeStatusJSON(overriding: ["spawned_at": "1.5"]), as: KronosStatus.self),
            "a fractional spawned_at is not an Int64"
        )
    }

    /// camelCase on the wire is NOT the contract: the keys are `session_id` and
    /// `spawned_at`, mapped by CodingKeys rather than by a decoder-wide key
    /// strategy. A body carrying `sessionId`/`spawnedAt` is therefore a body
    /// missing two required keys, and has to fail rather than read "" and 0.
    /// (Same claim GraphEdgeTests makes for `valid_until`/`expired_at`.)
    func testCamelCasedWireKeysDoNotDecode() {
        let json = """
        {"enabled": true, "live": true, "sessionId": "abc", "pane": "%17", "spawnedAt": 1786778082690}
        """
        XCTAssertThrowsError(try kronosEdgeDecode(json, as: KronosStatus.self))
    }

    // MARK: - Legal zero-ish values are NOT refusals

    /// The never-spawned shape ios/API.md documents outright: `session_id` and
    /// `pane` empty, `spawned_at` 0, `live` false. Empty and zero are VALUES
    /// here, not absences — this is the pair to the missing-key tests above, and
    /// without it "missing throws" could be satisfied by a model that threw on
    /// the honest fresh-Mac body too.
    func testNeverSpawnedShapeDecodesAsEmptyRatherThanThrowing() throws {
        let json = kronosEdgeStatusJSON(overriding: [
            "enabled": "true",
            "live": "false",
            "session_id": "\"\"",
            "pane": "\"\"",
            "spawned_at": "0",
        ])
        let s: KronosStatus = try kronosEdgeDecode(json)
        XCTAssertTrue(s.enabled)
        XCTAssertFalse(s.live)
        XCTAssertEqual(s.sessionID, "")
        XCTAssertEqual(s.pane, "")
        XCTAssertEqual(s.spawnedAt, 0)
        XCTAssertNil(s.context, "never spawned means never reported: no context key")
    }

    /// A server newer than the app adds keys additively (the round-5 note is
    /// explicit that this slice makes no wire change and `apiContract` stays 6,
    /// which is exactly the regime in which unknown keys appear). An unknown key
    /// must be ignored, not fatal — refusing it would make every future additive
    /// server change break the tab.
    func testUnknownKeysAreIgnored() throws {
        let json = kronosEdgeStatusJSON(overriding: [:], context: nil)
            .replacingOccurrences(of: "}", with: ", \"future_key\": {\"nested\": [1,2,3]}}")
        let s: KronosStatus = try kronosEdgeDecode(json)
        XCTAssertEqual(s.pane, "%17")
    }

    // MARK: - context: absent vs. zeroed

    /// The distinction the API doc calls the wire's "only spelling of 'never
    /// reported'", asserted as a PAIR because either half alone is satisfiable
    /// by the wrong model: absent → nil, and a present all-zero object → a
    /// non-nil report whose pct is 0. A decoder that folded `{"pct":0,...}` into
    /// nil would erase a freshly-compacted window — the reading opposite to
    /// "never reported" — and one that defaulted an absent key to a zeroed
    /// object would invent a report that was never made.
    func testAbsentContextAndZeroedContextAreDifferentAnswers() throws {
        let absent: KronosStatus = try kronosEdgeDecode(kronosEdgeStatusJSON())
        XCTAssertNil(absent.context, "an absent context key is 'never reported'")

        let zeroed: KronosStatus = try kronosEdgeDecode(
            kronosEdgeStatusJSON(context: #"{"pct": 0, "window": 0, "at": 0}"#)
        )
        XCTAssertNotNil(zeroed.context, "a present zeroed context IS a report")
        XCTAssertEqual(zeroed.context?.pct, 0)
        XCTAssertEqual(zeroed.context?.window, 0)
        XCTAssertEqual(zeroed.context?.at, 0)
    }

    /// An explicit `"context": null` is a shape the contract does not name — the
    /// documented spellings are "omitted" and "present object", and Go's
    /// `omitempty` on a pointer emits neither `null` nor the key. Rather than
    /// guess a direction, this asserts the DISJUNCTION the contract does
    /// license: null is either an absence (nil) or a decode error, and never a
    /// zeroed KronosContext — because a synthesized zero would claim a report of
    /// 0%, the exact confusion the absent-vs-zero rule exists to prevent.
    func testNullContextIsNeverAZeroedReport() {
        let json = kronosEdgeStatusJSON(context: "null")
        guard let s = try? kronosEdgeDecode(json, as: KronosStatus.self) else {
            return  // refused outright — also acceptable, and not a zeroed report
        }
        XCTAssertNil(s.context, "an explicit null must not become a report of zero")
    }

    /// A context object missing one of its three numbers is a malformed report,
    /// not a partial one: `pct`, `window` and `at` are all non-optional on
    /// `KronosContext`, so the failure propagates rather than defaulting the
    /// missing number to 0 — a 0 `window` would render as a context meter over a
    /// window that does not exist.
    func testPartialContextObjectIsADecodeError() {
        for body in [
            #"{"window": 200000, "at": 1786778082690}"#,
            #"{"pct": 6.25, "at": 1786778082690}"#,
            #"{"pct": 6.25, "window": 200000}"#,
            #"{}"#,
        ] {
            XCTAssertThrowsError(
                try kronosEdgeDecode(kronosEdgeStatusJSON(context: body), as: KronosStatus.self),
                "context \(body) must not decode"
            )
        }
    }

    /// A fractional pct survives as a Double. The hazard is a model typing `pct`
    /// as Int: 6.25 would then either fail to decode or truncate to 6, and the
    /// meter's whole resolution below one percent — the range in which a freshly
    /// compacted window sits — would be gone.
    func testFractionalPctSurvivesAsDouble() throws {
        let s: KronosStatus = try kronosEdgeDecode(
            kronosEdgeStatusJSON(context: #"{"pct": 6.25, "window": 200000, "at": 1786778082690}"#)
        )
        XCTAssertEqual(s.context?.pct ?? .nan, 6.25, accuracy: 1e-12)
    }

    /// The other direction: Go marshals `float64(62)` as the JSON token `62`,
    /// with no decimal point anywhere in the body. A whole-numbered pct is the
    /// COMMON case on the wire, not an exotic one, and it has to land on the
    /// Double as 62.0.
    func testWholeNumberedPctDecodesAsADouble() throws {
        let s: KronosStatus = try kronosEdgeDecode(
            kronosEdgeStatusJSON(context: #"{"pct": 62, "window": 200000, "at": 1786778082690}"#)
        )
        XCTAssertEqual(s.context?.pct ?? .nan, 62, accuracy: 1e-12)
    }

    /// Unix-millis timestamps and token windows at Int64 scale. 1786778082690
    /// overflows Int32 by three orders of magnitude, so this is the assertion
    /// that catches `Int32`/`Int`-on-a-32-bit-platform typing and any lossy trip
    /// through a Double (2^53 is comfortably above these, but a model that
    /// decoded to Double and rounded back would show here at the extremes below).
    func testInt64ScaleTimestampsAndWindowsDoNotOverflow() throws {
        let s: KronosStatus = try kronosEdgeDecode(
            kronosEdgeStatusJSON(
                overriding: ["spawned_at": "1786778082690"],
                context: #"{"pct": 12.5, "window": 1000000, "at": 1786778082690}"#
            )
        )
        XCTAssertEqual(s.spawnedAt, 1_786_778_082_690)
        XCTAssertEqual(s.context?.at, 1_786_778_082_690)
        XCTAssertEqual(s.context?.window, 1_000_000)

        let extreme: KronosStatus = try kronosEdgeDecode(
            kronosEdgeStatusJSON(
                overriding: ["spawned_at": "9223372036854775807"],
                context: #"{"pct": 1, "window": 9223372036854775807, "at": 9223372036854775807}"#
            )
        )
        XCTAssertEqual(extreme.spawnedAt, Int64.max, "Int64.max is representable, not an overflow")
        XCTAssertEqual(extreme.context?.at, Int64.max)
        XCTAssertEqual(extreme.context?.window, Int64.max)
    }

    // MARK: - kronosMeterState: band boundaries

    /// Assert the whole returned triple field by field. Kept in one helper so a
    /// boundary test cannot accidentally check the band while leaving the label
    /// — the two are the same claim rendered twice, and a band that moved
    /// without its label is a bar whose colour disagrees with its number.
    private func kronosEdgeAssertMeter(
        _ pct: Double?,
        pct expectedPct: Double?,
        band expectedBand: KronosBand,
        label expectedLabel: String,
        _ what: String,
        file: StaticString = #filePath,
        line: UInt = #line
    ) {
        let state = kronosMeterState(pct)
        if let expectedPct {
            XCTAssertEqual(state.pct ?? .nan, expectedPct, accuracy: 1e-9, "\(what): pct", file: file, line: line)
        } else {
            XCTAssertNil(state.pct, "\(what): pct must be nil", file: file, line: line)
        }
        XCTAssertEqual(state.band, expectedBand, "\(what): band", file: file, line: line)
        XCTAssertEqual(state.label, expectedLabel, "\(what): label", file: file, line: line)
    }

    /// The three thresholds are INCLUSIVE: the value AT 50/75/90 is already in
    /// the higher band, and the largest representable value below it is still in
    /// the lower one. `nextDown` is the sharpest form of "just below" a Double
    /// admits — a hand-written 49.9 leaves every `> 50` vs `>= 50` implementation
    /// passing.
    func testBandsAreInclusiveAtEachThreshold() {
        XCTAssertEqual(kronosMeterState(50.0.nextDown).band, .ok, "just below 50 is still ok")
        XCTAssertEqual(kronosMeterState(50).band, .warn50, "50 is inclusive")
        XCTAssertEqual(kronosMeterState(75.0.nextDown).band, .warn50, "just below 75 is still warn50")
        XCTAssertEqual(kronosMeterState(75).band, .warn75, "75 is inclusive")
        XCTAssertEqual(kronosMeterState(90.0.nextDown).band, .warn75, "just below 90 is still warn75")
        XCTAssertEqual(kronosMeterState(90).band, .warn90, "90 is inclusive")
    }

    /// The same sweep in the coarser terms the slice brief names (49.999… /
    /// 74.9 / 89.9), plus the interior of each band. This is what fails loudly
    /// if a threshold moves by a whole point rather than by one ulp.
    func testBandSweepAcrossEveryInterval() {
        let cases: [(Double, KronosBand)] = [
            (0, .ok),
            (1, .ok),
            (49.9, .ok),
            (49.999_999, .ok),
            (50, .warn50),
            (50.000_001, .warn50),
            (62.3, .warn50),
            (74.9, .warn50),
            (75, .warn75),
            (89.9, .warn75),
            (90, .warn90),
            (99.9, .warn90),
        ]
        for (pct, band) in cases {
            XCTAssertEqual(kronosMeterState(pct).band, band, "band at \(pct)")
        }
    }

    /// nil and the three non-finite doubles all take the same exit: no pct, the
    /// `ok` band, and the em-dash label. `.nan` is the one that must be tested
    /// rather than reasoned about — every comparison against NaN is false, so a
    /// `>= 90` ladder with no `isFinite` guard falls through to `ok` by accident
    /// and looks correct, while `pct` and the label quietly carry a NaN into the
    /// UI as "nan%" and a bar of NaN width.
    func testNilAndNonFiniteAllCollapseToTheUnknownState() {
        kronosEdgeAssertMeter(nil, pct: nil, band: .ok, label: "\u{2014}", "nil")
        kronosEdgeAssertMeter(Double.nan, pct: nil, band: .ok, label: "\u{2014}", "NaN")
        kronosEdgeAssertMeter(Double.signalingNaN, pct: nil, band: .ok, label: "\u{2014}", "signaling NaN")
        kronosEdgeAssertMeter(Double.infinity, pct: nil, band: .ok, label: "\u{2014}", "+inf")
        kronosEdgeAssertMeter(-Double.infinity, pct: nil, band: .ok, label: "\u{2014}", "-inf")
    }

    /// The unknown label is the EM DASH (U+2014), the same character the
    /// contract quotes for the "never reported" meter. Asserted by codepoint
    /// because an en dash, a hyphen and a figure dash are all indistinguishable
    /// at a glance in a diff, and the round-5 row quotes this one exactly.
    func testUnknownLabelIsAnEmDash() {
        let label = kronosMeterState(nil).label
        XCTAssertEqual(label, "\u{2014}")
        XCTAssertEqual(label.unicodeScalars.count, 1, "one scalar, not a multi-character fallback")
        XCTAssertEqual(label.unicodeScalars.first?.value, 0x2014, "U+2014 EM DASH")
    }

    /// A negative pct — which the wire can carry, since the report is whatever
    /// the statusline hook computed and nothing clamps it server-side. The
    /// contract says pct passes through UNCLAMPED and bands by `>=` ladders
    /// only, so a negative is the `ok` band with its sign intact in the label.
    /// Clamping here would report -5% as 0% and hide a broken reporter.
    func testNegativePctIsOkBandUnclampedAndSigned() {
        kronosEdgeAssertMeter(-5, pct: -5, band: .ok, label: "-5.0%", "-5")
        kronosEdgeAssertMeter(-0.5, pct: -0.5, band: .ok, label: "-0.5%", "-0.5")
        XCTAssertEqual(kronosMeterState(-1_000).band, .ok, "far negative is still ok")
        XCTAssertEqual(kronosMeterState(-1_000).pct ?? .nan, -1_000, accuracy: 1e-9, "still unclamped")
    }

    /// 100 and beyond. A window that is over-full is exactly the state the
    /// warning ladder exists to surface, so >100 must stay in `warn90` and keep
    /// its real number: clamping to 100 would make "110% full" and "exactly
    /// full" render identically at the moment they differ most.
    func testAtAndAboveOneHundredStayWarn90WithAnUnclampedPct() {
        kronosEdgeAssertMeter(100, pct: 100, band: .warn90, label: "100.0%", "100")
        kronosEdgeAssertMeter(100.4, pct: 100.4, band: .warn90, label: "100.4%", "100.4")
        kronosEdgeAssertMeter(150, pct: 150, band: .warn90, label: "150.0%", "150")

        let huge = kronosMeterState(1_000_000)
        XCTAssertEqual(huge.band, .warn90, "a huge pct is still the top band")
        XCTAssertEqual(huge.pct ?? .nan, 1_000_000, accuracy: 1e-6, "and still unclamped")
    }

    /// The label carries exactly one decimal place at every magnitude, including
    /// on whole numbers and on zero. `"50%"` and `"50.00%"` are both wrong: the
    /// contract fixes the format as `String(format: "%.1f%%", pct)` so the two
    /// meters agree on precision and suffix (exact rounding at a half-tie can
    /// still differ — sanctioned by 50-contract.md's round-5 review addendum:
    /// `toFixed` rounds half away from zero, `%.1f` half to even).
    func testLabelIsAlwaysExactlyOneDecimalPlace() {
        XCTAssertEqual(kronosMeterState(0).label, "0.0%")
        XCTAssertEqual(kronosMeterState(50).label, "50.0%")
        XCTAssertEqual(kronosMeterState(62.3).label, "62.3%")
        XCTAssertEqual(kronosMeterState(7).label, "7.0%")
    }

    /// Zero is a real report, not an unknown. It is the one finite value most
    /// likely to be conflated with "no report" by a nil-coalescing shortcut, and
    /// the wire has already drawn that line one layer up (absent key vs. zeroed
    /// object) — the meter must not undo it.
    func testZeroPctIsAReportAndNotTheUnknownState() {
        kronosEdgeAssertMeter(0, pct: 0, band: .ok, label: "0.0%", "0")
        XCTAssertNotEqual(kronosMeterState(0).label, "\u{2014}", "0% is not '—'")
    }

    /// `-0.0` is a Double distinct from `0.0` in bit pattern but equal under
    /// `==`. It stays a report (it is finite), lands in `ok`, and must not be
    /// mistaken for the unknown state. The label is asserted as one of the two
    /// spellings `%.1f` may produce for a negative zero, since the contract
    /// fixes the format string but says nothing about the sign of zero.
    func testNegativeZeroIsStillAFiniteReport() {
        let state = kronosMeterState(-0.0)
        XCTAssertNotNil(state.pct, "-0.0 is finite, so it is a report")
        XCTAssertEqual(state.band, .ok)
        XCTAssertTrue(state.label == "0.0%" || state.label == "-0.0%", "got \(state.label)")
    }

    // MARK: - KronosBand and kronosThresholds

    /// The four band names, spelled as the raw String values the enum is
    /// declared over. These names are the shared vocabulary with web's
    /// `KronosBand`, so they are part of the mirror rather than an internal
    /// detail — a Swift-side rename would compile fine and silently end the
    /// parity.
    func testBandRawValuesAreTheWebNames() {
        XCTAssertEqual(KronosBand.ok.rawValue, "ok")
        XCTAssertEqual(KronosBand.warn50.rawValue, "warn50")
        XCTAssertEqual(KronosBand.warn75.rawValue, "warn75")
        XCTAssertEqual(KronosBand.warn90.rawValue, "warn90")
        XCTAssertEqual(Set([KronosBand.ok, .warn50, .warn75, .warn90]).count, 4, "four distinct bands")
    }

    /// The tick list is the three thresholds in ascending order — the order the
    /// meter draws them in, so a reversed array puts every tick on the wrong
    /// side of the bar.
    func testThresholdsAreTheThreeTicksInOrder() {
        XCTAssertEqual(kronosThresholds, [50, 75, 90])
    }

    /// The ticks and the band ladder are two encodings of one set of numbers,
    /// and this is the assertion that keeps them from drifting apart: at every
    /// declared tick the band must already have changed from the band one ulp
    /// below it. A tick moved to 80 while the ladder still steps at 75 would
    /// draw a mark where no colour changes.
    func testEveryDeclaredTickIsAnActualBandBoundary() {
        for t in kronosThresholds {
            let at = kronosMeterState(t).band
            let below = kronosMeterState(t.nextDown).band
            XCTAssertNotEqual(at, below, "the band must change AT \(t)")
            XCTAssertNotEqual(at, .ok, "\(t) is a warning tick, so the band at it is not ok")
        }
    }

    // MARK: - kronosFillPct: the clamp

    /// Everything below the track's floor pins to 0. This is the geometry half
    /// of the split: `kronosMeterState` keeps -5 as -5 for the label, while the
    /// fill must not ask a layout for a negative width.
    func testFillClampsEveryNegativeToZero() {
        for pct in [-0.000_001, -0.5, -5, -100, -Double.greatestFiniteMagnitude] as [Double] {
            XCTAssertEqual(kronosFillPct(pct), 0, accuracy: 1e-12, "fill for \(pct)")
        }
    }

    /// And everything above the ceiling pins to 100 — an over-full window draws
    /// a full bar, never a bar overflowing its track.
    func testFillClampsEverythingAboveOneHundred() {
        for pct in [100.000_001, 100.5, 150, 1_000_000, Double.greatestFiniteMagnitude] as [Double] {
            XCTAssertEqual(kronosFillPct(pct), 100, accuracy: 1e-12, "fill for \(pct)")
        }
    }

    /// The two endpoints are INSIDE the range and pass through untouched — the
    /// pair that makes the two tests above a clamp rather than a blanket
    /// saturation. 0 in particular must stay 0 by pass-through, not because
    /// everything unknown becomes 0.
    func testFillPassesTheEndpointsAndTheInteriorThrough() {
        XCTAssertEqual(kronosFillPct(0), 0, accuracy: 1e-12)
        XCTAssertEqual(kronosFillPct(100), 100, accuracy: 1e-12)
        XCTAssertEqual(kronosFillPct(0.25), 0.25, accuracy: 1e-12)
        XCTAssertEqual(kronosFillPct(62.3), 62.3, accuracy: 1e-12)
        XCTAssertEqual(kronosFillPct(99.999), 99.999, accuracy: 1e-12)
    }

    /// nil and the non-finites all yield 0 — an empty track, which is the only
    /// honest width for a number that does not exist. NaN is the load-bearing
    /// one again: `min(max(nan, 0), 100)` propagates NaN through both clamps on
    /// most spellings, and a NaN width is a layout crash rather than a wrong bar.
    func testFillIsZeroForNilAndEveryNonFinite() {
        XCTAssertEqual(kronosFillPct(nil), 0, accuracy: 1e-12, "nil")
        XCTAssertEqual(kronosFillPct(Double.nan), 0, accuracy: 1e-12, "NaN")
        XCTAssertEqual(kronosFillPct(Double.signalingNaN), 0, accuracy: 1e-12, "signaling NaN")
        XCTAssertEqual(kronosFillPct(Double.infinity), 0, accuracy: 1e-12, "+inf")
        XCTAssertEqual(kronosFillPct(-Double.infinity), 0, accuracy: 1e-12, "-inf")
    }

    /// The whole output range is inside the track, for every input a Double can
    /// hold — the invariant the fill exists to provide, stated once over a sweep
    /// that includes the values the individual tests above do not name.
    func testFillNeverLeavesZeroThroughOneHundred() {
        let everyShape: [Double] = [
            Double.nan, Double.infinity, -Double.infinity,
            -Double.greatestFiniteMagnitude, -1e300, -1, -0.0, 0, 0.5,
            49.999_999, 50, 75, 89.999_999, 90, 99.999_999, 100, 100.000_001, 1e300,
            Double.greatestFiniteMagnitude,
        ]
        let inputs: [Double?] = [nil] + everyShape.map { Optional($0) }
        for pct in inputs {
            let fill = kronosFillPct(pct)
            XCTAssertTrue(fill.isFinite, "fill for \(String(describing: pct)) is not finite: \(fill)")
            XCTAssertTrue(fill >= 0 && fill <= 100, "fill for \(String(describing: pct)) escaped the track: \(fill)")
        }
    }

    /// The two functions disagree on clamping BY DESIGN, and this pins the
    /// disagreement rather than either half of it. At 150 the label must read
    /// 150.0% while the bar sits at 100; at -5 the label reads -5.0% while the
    /// bar sits at 0. A future "simplification" that routed both through one
    /// clamped number would pass most of the tests above and quietly make the
    /// meter lie at exactly the two moments it matters.
    func testTheLabelIsUnclampedWhileTheFillIsClamped() {
        let over = kronosMeterState(150)
        XCTAssertEqual(over.pct ?? .nan, 150, accuracy: 1e-9, "the state keeps 150")
        XCTAssertEqual(over.label, "150.0%")
        XCTAssertEqual(kronosFillPct(150), 100, accuracy: 1e-12, "the fill clamps 150")

        let under = kronosMeterState(-5)
        XCTAssertEqual(under.pct ?? .nan, -5, accuracy: 1e-9, "the state keeps -5")
        XCTAssertEqual(under.label, "-5.0%")
        XCTAssertEqual(kronosFillPct(-5), 0, accuracy: 1e-12, "the fill clamps -5")
    }

    /// The decoded wire number reaches both functions unaltered — the seam
    /// between the two halves of this slice. A 6.25% report (a freshly compacted
    /// window, the exact state the sentinel exists to make visible) must arrive
    /// at the meter as 6.25 and not as 6, and land in `ok` rather than tripping
    /// a warning band.
    func testADecodedFractionalReportFlowsThroughBothFunctions() throws {
        let s: KronosStatus = try kronosEdgeDecode(
            kronosEdgeStatusJSON(context: #"{"pct": 6.25, "window": 200000, "at": 1786778082690}"#)
        )
        let state = kronosMeterState(s.context?.pct)
        XCTAssertEqual(state.band, .ok)
        XCTAssertEqual(state.pct ?? .nan, 6.25, accuracy: 1e-12)
        XCTAssertEqual(kronosFillPct(s.context?.pct), 6.25, accuracy: 1e-12)
    }

    /// A status with NO context feeds nil straight into both functions — the
    /// "never reported" path end to end, and the reason both take an Optional at
    /// all. The view has nothing to unwrap and must not have to.
    func testAStatusWithoutContextFeedsNilIntoBothFunctions() throws {
        let s: KronosStatus = try kronosEdgeDecode(kronosEdgeStatusJSON())
        XCTAssertNil(s.context)
        kronosEdgeAssertMeter(s.context?.pct, pct: nil, band: .ok, label: "\u{2014}", "no context")
        XCTAssertEqual(kronosFillPct(s.context?.pct), 0, accuracy: 1e-12)
    }
}

// MARK: - Router

// Split into its own class because `AppRouter` is UI-observable state and the
// existing RouterEdgeTests marks its class `@MainActor`; the decode and pure
// -function tests above carry no such requirement and should not inherit one.
@MainActor
final class KronosRouterEdgeTests: XCTestCase {

    /// A router parked on a deliberately non-`kronos`, non-default tab. Same
    /// reasoning as RouterEdgeTests' helper: a no-op assertion is only
    /// meaningful from a starting tab the route under test would never select.
    private func kronosEdgeRouter(startingAt tab: AppRouter.Tab) -> AppRouter {
        let router = AppRouter()
        router.selectedTab = tab
        return router
    }

    private func kronosEdgeURL(_ string: String, file: StaticString = #filePath, line: UInt = #line) -> URL {
        guard let url = URL(string: string) else {
            XCTFail("not a URL at all: \(string)", file: file, line: line)
            return URL(string: "kraken://unroutable-placeholder")!
        }
        return url
    }

    /// The whole pending surface, asserted together — the same claim
    /// RouterEdgeTests makes, restated here so the kronos route is measured
    /// against every flag rather than the one a test happens to be thinking of.
    private func kronosEdgeAssertNothingPending(
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

    /// `kronos` is a fifth, distinct `Tab` case. Distinctness is what makes
    /// every tab assertion below non-vacuous — a `kronos` that compared equal to
    /// an existing case would satisfy "routed to kronos" while showing another
    /// screen.
    func testKronosIsAFifthDistinctTabCase() {
        let tabs: Set<AppRouter.Tab> = [.board, .kronos, .sessions, .graph, .settings]
        XCTAssertEqual(tabs.count, 5, "five distinct Tab cases")
        XCTAssertNotEqual(AppRouter.Tab.kronos, .board)
        XCTAssertNotEqual(AppRouter.Tab.kronos, .sessions)
    }

    /// `kraken://kronos` selects the tab "and changes NOTHING else (no pending
    /// flags touched)". Routed from `.settings` so "landed on kronos" cannot be
    /// read as "was already there" or "was reset to the default".
    func testKronosRouteSelectsTheTabAndRaisesNothing() {
        let router = kronosEdgeRouter(startingAt: .settings)
        router.handle(url: kronosEdgeURL("kraken://kronos"))
        XCTAssertEqual(router.selectedTab, .kronos, "kraken://kronos is the kronos tab")
        kronosEdgeAssertNothingPending(router, "kraken://kronos")
    }

    /// The stronger half of "changes nothing else": untouched means it does not
    /// CLEAR a pending request either. The flags are consumed by other screens
    /// that mount independently, so a request raised before the user tapped a
    /// kronos link has to survive the navigation — a route that reset them would
    /// silently cancel a drawer nobody has shown yet.
    func testKronosRouteLeavesAlreadyPendingFlagsUntouched() {
        let router = kronosEdgeRouter(startingAt: .sessions)
        router.handle(url: kronosEdgeURL("kraken://usage"))
        router.handle(url: kronosEdgeURL("kraken://log"))
        XCTAssertTrue(router.pendingUsage, "precondition: usage pending")
        XCTAssertTrue(router.pendingLog, "precondition: log pending")

        router.handle(url: kronosEdgeURL("kraken://kronos"))
        XCTAssertEqual(router.selectedTab, .kronos, "the kronos route still owns the tab")
        XCTAssertTrue(router.pendingUsage, "kronos must not clear pendingUsage")
        XCTAssertTrue(router.pendingLog, "kronos must not clear pendingLog")
    }

    /// Repeating the route is idempotent and still raises nothing. A tab route
    /// carries no request to consume, so the second arrival must be
    /// indistinguishable from the first.
    func testKronosRouteIsIdempotent() {
        let router = kronosEdgeRouter(startingAt: .board)
        for attempt in 1...3 {
            router.handle(url: kronosEdgeURL("kraken://kronos"))
            XCTAssertEqual(router.selectedTab, .kronos, "route #\(attempt)")
            kronosEdgeAssertNothingPending(router, "kraken://kronos #\(attempt)")
        }
    }

    /// The new case must not become a prefix match. `kronoss`, `kron` and
    /// `kronos-tab` are one keystroke from the live route and are exactly what a
    /// `hasPrefix` implementation would swallow; each has to stay an unknown
    /// host, which the contract's untouched blanket rule makes a total no-op.
    func testNearMissHostsAroundKronosAreTotalNoOps() {
        for string in [
            "kraken://kronoss",
            "kraken://kron",
            "kraken://kronos-tab",
            "kraken://kronosx",
            "kraken://orch",
        ] {
            let router = kronosEdgeRouter(startingAt: .settings)
            router.handle(url: kronosEdgeURL(string))
            XCTAssertEqual(router.selectedTab, .settings, "\(string) must not steer the tab")
            kronosEdgeAssertNothingPending(router, string)
        }
    }

    /// The scheme boundary holds for the new host too: a web link named after it
    /// is not a route into the app.
    func testNonKrakenSchemesDoNotReachTheKronosRoute() {
        for string in ["https://kronos", "https://kraken/kronos", "krakenx://kronos", "file:///kronos"] {
            let router = kronosEdgeRouter(startingAt: .sessions)
            router.handle(url: kronosEdgeURL(string))
            XCTAssertEqual(router.selectedTab, .sessions, "\(string) must not steer the tab")
            kronosEdgeAssertNothingPending(router, string)
        }
    }

    /// No fallthrough: "Every existing deep link keeps its behavior
    /// byte-identical." The four host→tab routes are re-asserted here from
    /// starting tabs that include `.kronos`, because a new case inserted into
    /// the middle of a switch is exactly how an existing route starts landing
    /// one row over, and a router parked on kronos is the state in which that
    /// would first be seen.
    func testExistingTabRoutesAreUnchangedAfterTheNewCaseExists() {
        let cases: [(String, AppRouter.Tab, AppRouter.Tab)] = [
            ("kraken://board", .kronos, .board),
            ("kraken://sessions", .kronos, .sessions),
            ("kraken://graph", .kronos, .graph),
            ("kraken://settings", .kronos, .settings),
        ]
        for (string, start, expected) in cases {
            let router = kronosEdgeRouter(startingAt: start)
            router.handle(url: kronosEdgeURL(string))
            XCTAssertEqual(router.selectedTab, expected, "\(string) from \(start)")
        }
    }

    /// `kraken://graph` specifically, since it is the route most like the new
    /// one — a bare host that selects a tab and raises no request. It must still
    /// select `.graph` and still raise nothing, from a `.kronos` start.
    func testGraphRouteStillSelectsGraphAndRaisesNothing() {
        let router = kronosEdgeRouter(startingAt: .kronos)
        router.handle(url: kronosEdgeURL("kraken://graph"))
        XCTAssertEqual(router.selectedTab, .graph, "graph is still a tab of its own")
        kronosEdgeAssertNothingPending(router, "kraken://graph")
    }

    /// The two drawer routes still reach their flags and their tabs from a
    /// `.kronos` start. These are the routes that raise state, so they are where
    /// a mis-ordered switch would show up as a raised flag on the wrong tab —
    /// the half-raised shape RouterEdgeTests calls out as the real hazard.
    func testDrawerRoutesStillRaiseTheirOwnFlagFromTheKronosTab() {
        let usage = kronosEdgeRouter(startingAt: .kronos)
        usage.handle(url: kronosEdgeURL("kraken://usage"))
        XCTAssertTrue(usage.pendingUsage, "usage route still raises pendingUsage")
        XCTAssertEqual(usage.selectedTab, .board, "usage route still owes the board tab")
        XCTAssertFalse(usage.pendingLog, "and still does not touch pendingLog")

        let log = kronosEdgeRouter(startingAt: .kronos)
        log.handle(url: kronosEdgeURL("kraken://log"))
        XCTAssertTrue(log.pendingLog, "log route still raises pendingLog")
        XCTAssertEqual(log.selectedTab, .settings, "log route still owes the settings tab")
        XCTAssertFalse(log.pendingUsage, "and still does not touch pendingUsage")
    }

    /// An unknown host arriving while the router sits on `.kronos` must not move
    /// it. A no-op is not a reset, and the new tab is not a special case of one.
    func testUnknownHostDoesNotKnockTheRouterOffTheKronosTab() {
        let router = kronosEdgeRouter(startingAt: .kronos)
        router.handle(url: kronosEdgeURL("kraken://nonsense"))
        XCTAssertEqual(router.selectedTab, .kronos, "an unknown host must not move the tab")
        kronosEdgeAssertNothingPending(router, "kraken://nonsense from kronos")
    }

    /// Scheme case-folding, inherited rather than restated: the existing router
    /// lowercases the scheme before comparing (RouterEdgeTests pins
    /// `KRAKEN://board`), and the new case is a row in the same table, so
    /// `KRAKEN://kronos` is the same route.
    ///
    /// Reported as an inference: the round-5 text does not restate scheme
    /// handling. If this fails, the disagreement is about the contract's
    /// silence, not about the case being wired wrong.
    func testUppercaseSchemeStillReachesKronos() {
        let router = kronosEdgeRouter(startingAt: .settings)
        router.handle(url: kronosEdgeURL("KRAKEN://kronos"))
        XCTAssertEqual(router.selectedTab, .kronos, "KRAKEN://kronos is the kronos route")
    }

    /// A trailing path component on a route that takes no payload. The contract
    /// gives `kronos` no path grammar at all, so this asserts only the
    /// disjunction it licenses — either the host still routes (the tab is
    /// kronos) or the whole URL is unrecognized (the tab did not move) — plus
    /// the claim that holds either way: no OTHER route's state may be raised by
    /// it. A `kronos/<something>` that set a board column or a job slug would be
    /// a positional-parsing bug leaking across routes.
    func testKronosWithATrailingComponentLeaksIntoNoOtherRoute() {
        for string in ["kraken://kronos/", "kraken://kronos/anything", "kraken://kronos//"] {
            let router = kronosEdgeRouter(startingAt: .settings)
            router.handle(url: kronosEdgeURL(string))
            XCTAssertTrue(
                router.selectedTab == .kronos || router.selectedTab == .settings,
                "\(string): either the kronos tab or an untouched one, never a third tab"
            )
            kronosEdgeAssertNothingPending(router, string)
        }
    }
}
