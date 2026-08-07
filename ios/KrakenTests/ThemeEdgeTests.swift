import XCTest
import SwiftUI
@testable import Kraken

// The theme system's boundaries and refusals.
//
// Written blind against `artifacts/50-contract.md` — every assertion below
// names the obligation it derives from, and nothing here was read off an
// implementation. The dimension is edges: the exact sizes where one font
// bucket becomes the next, the strings `ThemeTint.resolve` must refuse, the
// two tints that sit at the extremes of the hue/saturation obligations
// (graphite, which is nearly desaturated, and red, whose hue is 5° and
// therefore lives on the wraparound seam of `getHue`), and the two inputs to
// `BoardTheme` that must degrade rather than trap.
//
// Two shapes recur and are worth naming once:
//
//   * A refusal test whose expected value IS the default is degenerate on its
//     own — `resolve("indigo") == .violet` also passes for a `resolve` that
//     returns `.violet` unconditionally. `testResolveRoundTripsEveryDeclaredRawValue`
//     is the inside-the-boundary pair that makes the refusals mean something,
//     and the wrong-case cases deliberately use "Blue"/"Cyan"/"GRAPHITE"
//     rather than "Violet" so that a case-insensitive `resolve` fails loudly
//     instead of coincidentally landing on the right answer.
//
//   * Colour obligations are asserted by resolving `UIColor(<Color>)` against
//     an explicit trait collection and reading HSB, per the contract's note
//     for test authors (50-contract.md line 38). Hue is compared as a
//     CIRCULAR distance: red's 5°/360 = 0.0139 is within 0.02 of the seam, so
//     an implementation that lands a hair below zero reports ~0.999 from
//     `getHue` and a plain subtraction would call that a 0.99 miss.

// MARK: - helpers (all `edge`-prefixed; this file declares nothing else)

private struct EdgeHSB {
    let hue: Double
    let saturation: Double
    let brightness: Double
    let alpha: Double
}

/// HSB of `color` as the system resolves it in one appearance.
private func edgeHSB(_ color: Color, _ style: UIUserInterfaceStyle) -> EdgeHSB {
    let resolved = UIColor(color).resolvedColor(with: UITraitCollection(userInterfaceStyle: style))
    var h: CGFloat = 0, s: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
    resolved.getHue(&h, saturation: &s, brightness: &b, alpha: &a)
    return EdgeHSB(hue: Double(h), saturation: Double(s), brightness: Double(b), alpha: Double(a))
}

/// Distance between two hues on the unit CIRCLE, so 0.999 and 0.001 are 0.002
/// apart rather than 0.998. Required for red (5°) — see the header note.
private func edgeHueDistance(_ a: Double, _ b: Double) -> Double {
    let raw = abs(a - b).truncatingRemainder(dividingBy: 1)
    return min(raw, 1 - raw)
}

private func edgeAssertHue(
    _ color: Color,
    _ style: UIUserInterfaceStyle,
    degrees: Double,
    _ what: String,
    accuracy: Double = 0.02,
    file: StaticString = #filePath,
    line: UInt = #line
) {
    let hsb = edgeHSB(color, style)
    let want = degrees / 360
    let distance = edgeHueDistance(hsb.hue, want)
    XCTAssertLessThanOrEqual(
        distance, accuracy,
        "\(what): hue \(hsb.hue) is \(distance) from \(want) (\(degrees)°) on the hue circle",
        file: file, line: line
    )
}

/// A `WebJob` built by DECODING wire JSON, the same way `JobSearchTests` builds
/// its fixture: the payload carries a handful of the ~30 stored properties and
/// this exercises the decode path the app actually runs. `health` is the
/// optional wire string the contract's BLOCKED rule keys off.
private func edgeJob(slug: String, state: String, health: String?) throws -> WebJob {
    var obj: [String: Any] = [
        "slug": slug,
        "repo": "kraken",
        "state": state,
        "live": false,
        "mtime": 0,
    ]
    if let health { obj["health"] = health }
    let data = try JSONSerialization.data(withJSONObject: obj)
    return try JSONDecoder().decode(WebJob.self, from: data)
}

final class ThemeEdgeTests: XCTestCase {

    // The contract's isolation note (50-contract.md line 38): a test that
    // touches the tint key sets it explicitly and removes it after. Only
    // `testAPersistedGarbageTintReadsBackAsViolet` writes it, but the teardown
    // is unconditional so a future test here cannot leak the key by omission.
    override func tearDown() {
        UserDefaults.standard.removeObject(forKey: KTheme.themeTintKey)
        super.tearDown()
    }

    // MARK: - ThemeTint.resolve — the refusal surface
    //
    // Contract line 16: "nil, empty, and any unrecognized string → `.violet`;
    // a valid rawValue → its case. Never traps."

    func testResolveRefusesNilAndTheEmptyString() {
        XCTAssertEqual(ThemeTint.resolve(nil), .violet, "nil")
        XCTAssertEqual(ThemeTint.resolve(""), .violet, "the empty string")
    }

    func testResolveRefusesGarbage() {
        for raw in ["chartreuse", "  ", "\n", "0", "-1", "violet violet",
                    "🟣", String(repeating: "v", count: 4096)] {
            XCTAssertEqual(ThemeTint.resolve(raw), .violet, "garbage \(raw.debugDescription)")
        }
    }

    // Whitespace-padded and punctuated forms of a REAL rawValue: the rawValue
    // is matched exactly, not trimmed or normalised, so each of these is an
    // unrecognized string and lands on violet.
    func testResolveDoesNotTrimOrNormaliseARealRawValue() {
        for raw in [" blue", "blue ", " blue ", "blue\n", "blue;", "bl ue"] {
            XCTAssertEqual(ThemeTint.resolve(raw), .violet, "padded \(raw.debugDescription)")
        }
    }

    // Wrong CASE. Deliberately not "Violet": these three would pass a
    // case-insensitive `resolve` if the expected value were the same as the
    // input's case-folded case, so they are the cases that actually detect a
    // lenient match. "Violet" is included last only because the brief names it,
    // and it is degenerate on its own.
    func testResolveIsCaseSensitive() {
        XCTAssertEqual(ThemeTint.resolve("Blue"), .violet, "\"Blue\" is not a rawValue")
        XCTAssertEqual(ThemeTint.resolve("Cyan"), .violet, "\"Cyan\" is not a rawValue")
        XCTAssertEqual(ThemeTint.resolve("GRAPHITE"), .violet, "\"GRAPHITE\" is not a rawValue")
        XCTAssertEqual(ThemeTint.resolve("Violet"), .violet, "\"Violet\" is not a rawValue")
    }

    // A plausible-but-unlisted swatch name. The curated set is closed (Q7:
    // "curated tint swatches, never a free ColorPicker"), so a colour that
    // sounds like it belongs is still a miss.
    func testResolveRefusesAPlausibleButUnlistedTintName() {
        for raw in ["indigo", "purple", "teal", "orange", "gray", "grey", "magenta"] {
            XCTAssertEqual(ThemeTint.resolve(raw), .violet, "unlisted \(raw)")
        }
    }

    // The inside of the boundary. Without this, every refusal above is
    // satisfied by a `resolve` that ignores its argument.
    func testResolveRoundTripsEveryDeclaredRawValue() {
        for tint in ThemeTint.allCases {
            XCTAssertEqual(ThemeTint.resolve(tint.rawValue), tint, "round-trip \(tint.rawValue)")
        }
        XCTAssertEqual(ThemeTint.allCases.count, 8, "contract line 15: exactly 8 cases")
    }

    // Contract line 22 + the Settings-picker section (line 54): the picker
    // writes rawValues through `themeTintKey` and app start reads them back
    // through `resolve`, so "an unrecognized persisted value renders as
    // violet". This is the only test here that touches UserDefaults; it sets
    // the key explicitly and `tearDown` removes it.
    func testAPersistedGarbageTintReadsBackAsViolet() {
        XCTAssertEqual(KTheme.themeTintKey, "themeTint", "contract line 22 freezes the key name")

        UserDefaults.standard.set("chartreuse", forKey: KTheme.themeTintKey)
        XCTAssertEqual(
            ThemeTint.resolve(UserDefaults.standard.string(forKey: KTheme.themeTintKey)),
            .violet,
            "a hand-edited or stale persisted value degrades to violet"
        )

        // A key that was never written is the fresh-install case, and it is the
        // nil branch of `resolve` rather than a separate default.
        UserDefaults.standard.removeObject(forKey: KTheme.themeTintKey)
        XCTAssertEqual(
            ThemeTint.resolve(UserDefaults.standard.string(forKey: KTheme.themeTintKey)),
            .violet,
            "no persisted tint at all"
        )
    }

    // MARK: - Font.data / Font.voice — the bucket boundaries
    //
    // Contract lines 25-26: "≥17 → .body · 15..<17 → .callout · 13..<15 →
    // .footnote · 11..<13 → .caption · <11 → .caption2", built as
    // `Font.system(<style>, design: .monospaced|.default, weight: weight)` —
    // "equality with that expression is the test".
    //
    // Every case below is a boundary or one representable step off it. 16.999
    // and 14.999 and 12.999 and 10.999 are the "just under the cut" side of
    // each half-open range; 17, 15, 13, 11 are the closed side. A table built
    // with `>` instead of `>=` (or with the ranges shifted by one) fails at
    // exactly these points and nowhere else.

    private static let edgeBuckets: [(CGFloat, Font.TextStyle, String)] = [
        (17, .body, "17 is the closed low end of ≥17"),
        (17.001, .body, "just above 17"),
        (24, .body, "well above 17"),
        (99, .body, "absurdly large still buckets, never traps"),
        (16.999, .callout, "just under 17 falls to callout"),
        (16, .callout, "inside 15..<17"),
        (15, .callout, "15 is the closed low end of 15..<17"),
        (14.999, .footnote, "just under 15 falls to footnote"),
        (14, .footnote, "inside 13..<15"),
        (13, .footnote, "13 is the closed low end of 13..<15"),
        (12.999, .caption, "just under 13 falls to caption"),
        (12, .caption, "inside 11..<13"),
        (11, .caption, "11 is the closed low end of 11..<13"),
        (10.999, .caption2, "just under 11 falls to caption2"),
        (10, .caption2, "inside <11"),
        (8, .caption2, "the smallest size any call site uses"),
        (0, .caption2, "degenerate zero still buckets, never traps"),
    ]

    // `weight: .regular` is written out rather than left to the default: the
    // contract's frozen expression names a weight, and `Font.system(_:design:)`
    // with the weight argument OMITTED is a different value from the same call
    // with `.regular` passed. Spelling it out keeps this an assertion about the
    // bucket table and not about SwiftUI's default-argument identity.
    func testDataBucketsAtEveryBoundary() {
        for (size, style, why) in Self.edgeBuckets {
            XCTAssertEqual(
                Font.data(size),
                Font.system(style, design: .monospaced, weight: .regular),
                "Font.data(\(size)) — \(why)"
            )
        }
    }

    func testVoiceBucketsAtEveryBoundaryWithTheDefaultDesign() {
        for (size, style, why) in Self.edgeBuckets {
            XCTAssertEqual(
                Font.voice(size),
                Font.system(style, design: .default, weight: .regular),
                "Font.voice(\(size)) — \(why)"
            )
        }
    }

    // The two helpers must not collapse into one another: mono is data-only
    // (Q2, contract line 9), so the same size through `.data` and `.voice`
    // yields different fonts.
    func testDataAndVoiceNeverAgreeAtAnyBoundary() {
        for (size, _, why) in Self.edgeBuckets {
            XCTAssertNotEqual(
                Font.data(size), Font.voice(size),
                "mono vs default design at \(size) — \(why)"
            )
        }
    }

    // Weight is passed through untouched at every bucket, including the
    // boundary sizes — a bucketer that rebuilt the font without threading
    // `weight` would still pass the default-weight tests above.
    func testWeightIsPassedThroughAtEveryBoundary() {
        let weights: [Font.Weight] = [.ultraLight, .light, .regular, .medium,
                                      .semibold, .bold, .heavy, .black]
        for (size, style, _) in Self.edgeBuckets {
            for weight in weights {
                XCTAssertEqual(
                    Font.data(size, weight: weight),
                    Font.system(style, design: .monospaced, weight: weight),
                    "Font.data(\(size), weight: \(weight))"
                )
                XCTAssertEqual(
                    Font.voice(size, weight: weight),
                    Font.system(style, design: .default, weight: weight),
                    "Font.voice(\(size), weight: \(weight))"
                )
            }
        }
    }

    // Weight must actually reach the built font — the vacuity guard for the
    // passthrough test above. A helper that dropped `weight` on the floor
    // would satisfy every default-weight assertion in this file.
    func testWeightChangesTheResultAtEveryBoundary() {
        for (size, _, why) in Self.edgeBuckets {
            XCTAssertNotEqual(Font.data(size, weight: .regular),
                              Font.data(size, weight: .bold), "data \(size) — \(why)")
            XCTAssertNotEqual(Font.voice(size, weight: .regular),
                              Font.voice(size, weight: .bold), "voice \(size) — \(why)")
        }
    }

    // MARK: - BoardTheme — degrade, never trap
    //
    // Contract line 28: "unrecognized job color name → nil".

    func testJobColorRefusesAnUnrecognizedNinthName() {
        // The palette is eight names (contract line 29), and it is Claude
        // Code's `/color` vocabulary — `internal/jobs/color.go` and the
        // `--job-*` blocks in `web/_ui/src/styles/style.css` spell it out as
        // red · blue · green · yellow · purple · orange · pink · cyan. A ninth
        // — whatever it is called — has no colour, and the strip must render
        // nothing rather than guess.
        for name in ["chartreuse", "indigo", "teal", "magenta", "gray", "grey"] {
            XCTAssertNil(BoardTheme.jobColor(name), "unrecognized job colour \(name)")
        }
    }

    // The sharpest edge on this function: the JOB palette and the THEME TINT
    // palette are two different eight-name vocabularies that overlap on five
    // names (red, blue, green, pink, cyan) and diverge on the other three —
    // tints are violet/amber/graphite where job colours are purple/yellow/orange.
    // A `jobColor` that resolved through `ThemeTint` would answer for the three
    // below instead of refusing.
    func testJobColorDoesNotAnswerForTintOnlyNames() {
        for name in ["violet", "amber", "graphite"] {
            XCTAssertNil(BoardTheme.jobColor(name),
                         "\(name) is a ThemeTint rawValue (contract line 15), not a job colour")
        }
    }

    func testJobColorRefusesTheEmptyAndAbsentName() {
        // `color` is `omitempty` on the wire, so "absent" is the common case
        // and "" is what a cleared colour serialises to.
        XCTAssertNil(BoardTheme.jobColor(nil), "no colour set on the job")
        XCTAssertNil(BoardTheme.jobColor(""), "the empty colour name")
        XCTAssertNil(BoardTheme.jobColor(" "), "whitespace is not a colour name")
    }

    // Case sensitivity at the palette boundary: the eight names are the wire
    // vocabulary shared with the CLI and web board (50-components.md line 50),
    // so a capitalised variant is an unrecognized name, not a lenient hit.
    func testJobColorIsCaseSensitive() {
        for name in ["Red", "CYAN", "Blue"] {
            XCTAssertNil(BoardTheme.jobColor(name), "wrong-case job colour \(name)")
        }
    }

    // Contract line 28: "non-empty `health` → BLOCKED". The state says
    // working; the health line overrides it. This is the rule the web board
    // implements as `byCol[j.health ? "blocked" : columnFor(j.state)]`.
    func testNonEmptyHealthSendsAWorkingJobToBlocked() throws {
        for health in ["needs a decision", "x", "0", " "] {
            let job = try edgeJob(slug: "edge-health", state: "working", health: health)
            XCTAssertEqual(
                BoardTheme.column(for: job), .blocked,
                "health \(health.debugDescription) on a working job"
            )
        }
    }

    // …and the boundary from the other side. "" is FALSY health, not health:
    // a job whose health line was cleared goes back to its state's column.
    // Asserted against the nil-health job rather than a named case, so this
    // pins the RULE (empty behaves exactly like absent) without assuming which
    // column `working` maps to.
    func testEmptyHealthIsNotBlockedByHealth() throws {
        let empty = try edgeJob(slug: "edge-empty", state: "working", health: "")
        let absent = try edgeJob(slug: "edge-absent", state: "working", health: nil)

        XCTAssertNotEqual(BoardTheme.column(for: empty), .blocked,
                          "an empty health string must not force BLOCKED")
        XCTAssertEqual(BoardTheme.column(for: empty), BoardTheme.column(for: absent),
                       "empty health behaves exactly like absent health")
        XCTAssertEqual(BoardTheme.column(for: absent), .working,
                       "with no health, the state decides the column")
    }

    // A job that is already blocked by STATE stays blocked whether or not it
    // carries a health line — the two paths to BLOCKED must not cancel.
    func testABlockedStateStaysBlockedWithOrWithoutHealth() throws {
        for health in [nil, "", "waiting on carl"] {
            let job = try edgeJob(slug: "edge-blocked", state: "blocked", health: health)
            XCTAssertEqual(BoardTheme.column(for: job), .blocked,
                           "state blocked, health \(String(describing: health))")
        }
    }

    // An unrecognized state is reachable (the wire string is whatever the
    // daemon wrote) and must land on ONE fallback rather than a per-string
    // colour — the same claim `GraphContractTests` makes about
    // `KTheme.graphTypeColorUI`. Derived from line 28's byte-for-byte
    // preservation of the four-way state→colour table, whose `default` arm is
    // what these two strings share.
    func testEveryUnrecognizedStateLandsOnOneColour() {
        let fallback = BoardTheme.color(forState: "no-such-state")
        XCTAssertEqual(BoardTheme.color(forState: "also-not-a-state"), fallback)
        XCTAssertEqual(BoardTheme.color(forState: ""), fallback)
    }

    // MARK: - SessionTheme — role beats status
    //
    // Contract line 28: "role shell→text2". The role is checked BEFORE the
    // status, so no status value — including ones that map to a loud colour on
    // their own — can tint a shell row.

    func testShellRoleOverridesEveryStatus() {
        let statuses = ["running", "working", "blocked", "queued", "done",
                        "failed", "error", "archived", "draft", "", "no-such-status"]
        for status in statuses {
            XCTAssertEqual(
                SessionTheme.color(forStatus: status, role: "shell"),
                KTheme.text2,
                "role shell with status \(status.debugDescription)"
            )
        }
    }

    // The vacuity guard for the test above: if EVERY (status, role) pair
    // resolved to text2, "shell beats every status" would be true for free.
    // Swept broadly rather than pinned to one role, because the contract
    // freezes only the shell arm — all this needs to establish is that the
    // function is not constant.
    func testSessionColourIsNotConstantOutsideTheShellRole() {
        let roles = ["agent", "main", "worker", "session", ""]
        let statuses = ["running", "working", "blocked", "queued", "done", "failed"]
        var differs = false
        for role in roles {
            for status in statuses where SessionTheme.color(forStatus: status, role: role) != KTheme.text2 {
                differs = true
            }
        }
        XCTAssertTrue(differs,
                      "some non-shell row must carry a status colour, else the shell override is vacuous")
    }

    // MARK: - GlassTheme.accent — the two extreme tints
    //
    // Contract line 20: resolved DARK → hue = hueDegrees/360 ±0.02,
    // saturation = 0.45 × saturationScale ±0.05, brightness 1.0 ±0.05.
    // Resolved LIGHT → same hue ±0.02, saturation 0.72 × saturationScale
    // ±0.05, brightness = ThemeTint.lightBrightness ±0.05 (per-tint since
    // the 2026-08-07 r2/r3 amendments; the table itself is pinned in
    // ThemeContractTests).

    // Graphite is the saturation extreme: `saturationScale` is 0.10 (line 18),
    // so its accent is near-grey — 0.45×0.10 = 0.045 dark, 0.72×0.10 = 0.072
    // light. Hue is deliberately NOT asserted: `getHue` on a near-grey colour
    // reports an arbitrary angle, so a hue assertion here would be testing
    // floating-point noise rather than the contract.
    func testGraphiteAccentIsNearlyDesaturatedInBothAppearances() {
        let accent = GlassTheme(tint: .graphite).accent

        let dark = edgeHSB(accent, .dark)
        XCTAssertLessThanOrEqual(dark.saturation, 0.10,
                                 "graphite dark saturation (0.45 × 0.10 ± 0.05) was \(dark.saturation)")
        XCTAssertEqual(dark.brightness, 1.0, accuracy: 0.05, "graphite dark brightness")

        let light = edgeHSB(accent, .light)
        XCTAssertLessThanOrEqual(light.saturation, 0.13,
                                 "graphite light saturation (0.72 × 0.10 ± 0.05) was \(light.saturation)")
        XCTAssertEqual(light.brightness, CGFloat(ThemeTint.graphite.lightBrightness),
                       accuracy: 0.05, "graphite light brightness")
    }

    // …and graphite must still be a *tint*, not a hard-coded grey shortcut:
    // it is the only case whose `saturationScale` is not 1.0 (line 18), and
    // every other case must clear the desaturation bar graphite sits under.
    func testGraphiteIsTheOnlyDesaturatedTint() {
        XCTAssertEqual(ThemeTint.graphite.saturationScale, 0.10, accuracy: 0.001)
        for tint in ThemeTint.allCases where tint != .graphite {
            XCTAssertEqual(tint.saturationScale, 1.0, accuracy: 0.001, "\(tint.rawValue) scale")
            let dark = edgeHSB(GlassTheme(tint: tint).accent, .dark)
            XCTAssertGreaterThan(dark.saturation, 0.10,
                                 "\(tint.rawValue) must be more saturated than graphite")
        }
    }

    // Red is the hue extreme: 5° is 0.0139 of the circle, closer to the
    // 0.0/1.0 seam than the ±0.02 tolerance itself. An implementation that
    // lands a hair below 0 reports ~0.999 from `getHue`, which is 0.014 away
    // on the circle and 0.985 away by subtraction — so the comparison MUST be
    // circular. `edgeHueDistance` is what makes this test honest.
    func testRedAccentSitsAtFiveDegreesAcrossTheHueSeam() {
        let accent = GlassTheme(tint: .red).accent
        XCTAssertEqual(ThemeTint.red.hueDegrees, 5, accuracy: 0.001, "contract line 17")

        edgeAssertHue(accent, .dark, degrees: 5, "red accent, dark")
        edgeAssertHue(accent, .light, degrees: 5, "red accent, light")

        let dark = edgeHSB(accent, .dark)
        XCTAssertEqual(dark.saturation, 0.45, accuracy: 0.05, "red dark saturation")
        XCTAssertEqual(dark.brightness, 1.0, accuracy: 0.05, "red dark brightness")

        let light = edgeHSB(accent, .light)
        XCTAssertEqual(light.saturation, 0.72, accuracy: 0.05, "red light saturation")
        XCTAssertEqual(light.brightness, CGFloat(ThemeTint.red.lightBrightness),
                       accuracy: 0.05, "red light brightness")
    }

    // Contract line 21: `accentSoft` is the accent at reduced opacity or a
    // derived soft fill, and "must carry the same hue ±0.02 (both traits)".
    // Red is where that is hardest to keep — a soft fill built by blending
    // toward white or black drifts across the seam.
    func testRedAccentSoftKeepsTheSameHueInBothAppearances() {
        let soft = GlassTheme(tint: .red).accentSoft
        edgeAssertHue(soft, .dark, degrees: 5, "red accentSoft, dark")
        edgeAssertHue(soft, .light, degrees: 5, "red accentSoft, light")
    }

    // The same hue-preservation claim at the OTHER extreme of the table —
    // pink, 330°, the highest declared hue, where a soft fill that overshoots
    // wraps past 360 into red. Graphite is excluded for the near-grey reason
    // given above.
    func testAccentSoftKeepsItsHueForEveryChromaticTint() {
        for tint in ThemeTint.allCases where tint != .graphite {
            let theme = GlassTheme(tint: tint)
            edgeAssertHue(theme.accentSoft, .dark, degrees: tint.hueDegrees,
                          "\(tint.rawValue) accentSoft, dark")
            edgeAssertHue(theme.accentSoft, .light, degrees: tint.hueDegrees,
                          "\(tint.rawValue) accentSoft, light")
        }
    }

    // `GlassTheme` is a value type carrying the tint (contract line 19): two
    // themes built from the same tint are interchangeable, and the accent is
    // a pure function of the tint rather than of any ambient stored state.
    // This is the refusal that keeps a stale UserDefaults read out of the
    // explicitly-constructed path.
    func testAnExplicitlyConstructedThemeIgnoresThePersistedTint() {
        UserDefaults.standard.set("graphite", forKey: KTheme.themeTintKey)
        let red = GlassTheme(tint: .red)
        edgeAssertHue(red.accent, .dark, degrees: 5,
                      "GlassTheme(tint: .red) while graphite is persisted")

        UserDefaults.standard.set("nonsense", forKey: KTheme.themeTintKey)
        edgeAssertHue(GlassTheme(tint: .red).accent, .dark, degrees: 5,
                      "GlassTheme(tint: .red) while garbage is persisted")
    }
}
