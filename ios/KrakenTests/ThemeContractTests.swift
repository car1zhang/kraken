import XCTest
import Foundation
import UIKit
import SwiftUI
@testable import Kraken

// ThemeContractTests — the stated invariants and happy paths of Slice A,
// written blind from artifacts/50-contract.md. Nothing here was derived by
// reading Theme.swift; every assertion below quotes a row of the frozen
// contract table in its comment, and the numbers are transcribed from that
// table rather than from the implementation. A test that read the tables it
// checks would only prove those tables equal themselves.
//
// The failure mode this suite exists for is silent. A tint whose hue drifted
// ten degrees, an accent that stopped following the stored tint, a font
// helper that kept its old fixed-point arithmetic under a Dynamic Type name —
// none of these crash, none fail a build, and all of them quietly break the
// one promise the redesign makes (one hue derives the whole surface, and type
// scales with the user's setting).

// MARK: - resolution helpers
//
// The contract's own note for test authors: "assert color obligations by
// resolving `UIColor(<Color>)` with `UITraitCollection(userInterfaceStyle:
// .dark)` / `.light` and reading `getHue`." These are that, plus the circular
// hue distance a naive subtraction gets wrong near red.

/// Accepts either a SwiftUI `Color` or a `UIColor` so the assertions below
/// bind to the CONTRACT (a color token, resolved per trait) and not to which
/// of the two representations a given member happens to be declared as.
private func contractUI(_ color: Color) -> UIColor { UIColor(color) }
private func contractUI(_ color: UIColor) -> UIColor { color }

private struct ContractHSB {
    let hue: CGFloat
    let saturation: CGFloat
    let brightness: CGFloat
}

private func contractHSB(_ color: UIColor, _ style: UIUserInterfaceStyle) -> ContractHSB {
    let resolved = color.resolvedColor(with: UITraitCollection(userInterfaceStyle: style))
    var h: CGFloat = 0, s: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
    resolved.getHue(&h, saturation: &s, brightness: &b, alpha: &a)
    return ContractHSB(hue: h, saturation: s, brightness: b)
}

private struct ContractRGB: Equatable {
    let r: CGFloat
    let g: CGFloat
    let b: CGFloat
}

private func contractRGB(_ color: UIColor, _ style: UIUserInterfaceStyle) -> ContractRGB {
    let resolved = color.resolvedColor(with: UITraitCollection(userInterfaceStyle: style))
    var r: CGFloat = 0, g: CGFloat = 0, b: CGFloat = 0, a: CGFloat = 0
    resolved.getRed(&r, green: &g, blue: &b, alpha: &a)
    return ContractRGB(r: r, g: g, b: b)
}

private func contractHexRGB(_ hex: UInt32) -> ContractRGB {
    ContractRGB(r: CGFloat((hex >> 16) & 0xFF) / 255,
                g: CGFloat((hex >> 8) & 0xFF) / 255,
                b: CGFloat(hex & 0xFF) / 255)
}

/// Hue lives on a circle, so 0.999 and 0.001 are two thousandths apart, not
/// 0.998. Red's contract hue is 5° — a hair below the seam — so a plain
/// subtraction would report a near-maximal error for a correct color.
private func contractHueDelta(_ a: CGFloat, _ b: CGFloat) -> CGFloat {
    let d = abs(a - b).truncatingRemainder(dividingBy: 1)
    return min(d, 1 - d)
}

final class ThemeContractTests: XCTestCase {

    // MARK: - contract constants, transcribed from 50-contract.md

    /// § Slice A, `ThemeTint`: "Exactly 8 cases, in this order, with these
    /// rawValues: violet, blue, cyan, green, amber, pink, red, graphite."
    private static let contractRawValues = [
        "violet", "blue", "cyan", "green", "amber", "pink", "red", "graphite",
    ]

    /// § Slice A, `ThemeTint.hueDegrees`: "violet 262 · blue 215 · cyan 190 ·
    /// green 145 · amber 40 · pink 330 · red 5 · graphite 220."
    private static let contractHueDegrees: [String: Double] = [
        "violet": 262, "blue": 215, "cyan": 190, "green": 145,
        "amber": 40, "pink": 330, "red": 5, "graphite": 220,
    ]

    /// § Slice A, `ThemeTint.saturationScale`: "1.0 for every case except
    /// graphite, which is 0.10."
    private static let contractSaturationScale: [String: Double] = [
        "violet": 1.0, "blue": 1.0, "cyan": 1.0, "green": 1.0,
        "amber": 1.0, "pink": 1.0, "red": 1.0, "graphite": 0.10,
    ]

    /// The seven tints whose accent carries enough chroma for `getHue` to
    /// recover the hue reliably. Graphite (saturation 0.045 in dark, 0.072 in
    /// light) is asserted separately — see `testGraphiteAccentStillCarries…`.
    private static let contractChromaticTints = [
        "violet", "blue", "cyan", "green", "amber", "pink", "red",
    ]

    /// § Slice A, byte-stable `jobRed…jobCyan` lines, cross-pinned by
    /// `web/jobpalette_test.go` against `internal/jobs.Palette` — the eight
    /// names and their light/dark hexes, in the Go palette's order.
    private static let contractJobPalette: [(name: String, dark: UInt32, light: UInt32)] = [
        ("red", 0xDC2626, 0xCA2323),
        ("blue", 0x6A9BCC, 0x496B8D),
        ("green", 0x16A34A, 0x107937),
        ("yellow", 0xCA8A04, 0x8B5F03),
        ("purple", 0x827DBD, 0x676395),
        ("orange", 0xD97757, 0x9C563F),
        ("pink", 0xC46686, 0x9B516A),
        ("cyan", 0x0891B2, 0x06718B),
    ]

    private static let contractTintKey = "themeTint"

    // MARK: - isolation
    //
    // 50-contract.md: "Tests must not read or assume anything about
    // UserDefaults state left by other tests — each test that touches the tint
    // key sets it explicitly first and removes it after." Removing in both
    // hooks means a test that fails mid-way still cannot poison a later one,
    // and cannot inherit a value an earlier failure left behind.

    override func setUp() {
        super.setUp()
        UserDefaults.standard.removeObject(forKey: Self.contractTintKey)
    }

    override func tearDown() {
        UserDefaults.standard.removeObject(forKey: Self.contractTintKey)
        super.tearDown()
    }

    private func contractStoreTint(_ raw: String) {
        UserDefaults.standard.set(raw, forKey: Self.contractTintKey)
    }

    // MARK: - ThemeTint: the case list

    // The count and the ORDER are both requirements, not one requirement. The
    // Settings swatch row renders "the 8 curated swatches in ThemeTint order",
    // so a reordering silently rearranges a shipped picker; a ninth case
    // silently adds a swatch nobody chose.
    func testThemeTintIsExactlyTheEightContractCasesInOrder() {
        XCTAssertEqual(ThemeTint.allCases.count, 8)
        XCTAssertEqual(ThemeTint.allCases.map(\.rawValue), Self.contractRawValues)
    }

    // MARK: - ThemeTint.resolve

    // "a valid rawValue → its case." Every case, not a sample: resolve is the
    // only door between the persisted string and the enum, so a single case it
    // failed to recognize would degrade to violet forever with no other symptom
    // than the user's choice not sticking.
    func testResolveReturnsTheCaseForEveryValidRawValue() {
        for tint in ThemeTint.allCases {
            XCTAssertEqual(ThemeTint.resolve(tint.rawValue), tint, "rawValue \(tint.rawValue)")
        }
    }

    // "nil, empty, and any unrecognized string → .violet ... Never traps."
    // Violet is the default in three separate places (env default, no stored
    // value, garbage stored value); this is the one that has to survive a
    // hand-edited or downgraded defaults plist.
    func testResolveDegradesToVioletForNilEmptyAndUnrecognized() {
        XCTAssertEqual(ThemeTint.resolve(nil), .violet)
        XCTAssertEqual(ThemeTint.resolve(""), .violet)
        XCTAssertEqual(ThemeTint.resolve("Violet"), .violet)
        XCTAssertEqual(ThemeTint.resolve("teal"), .violet)
        XCTAssertEqual(ThemeTint.resolve("graphit"), .violet)
    }

    // MARK: - ThemeTint tables

    func testHueDegreesMatchesTheContractTable() {
        for tint in ThemeTint.allCases {
            let want = Self.contractHueDegrees[tint.rawValue]
            XCTAssertNotNil(want, "contract table is missing \(tint.rawValue)")
            XCTAssertEqual(tint.hueDegrees, want ?? -1, accuracy: 0.0001, tint.rawValue)
        }
    }

    // Graphite is the only tint that is a near-greyscale, and it is the whole
    // reason `saturationScale` exists as a separate knob from `hueDegrees` —
    // it still carries a hue (220) so the field leans cool, but at a tenth of
    // the chroma. Fold the scale into the hue and graphite becomes blue.
    func testSaturationScaleIsOneEverywhereExceptGraphite() {
        for tint in ThemeTint.allCases {
            let want = Self.contractSaturationScale[tint.rawValue]
            XCTAssertNotNil(want, "contract table is missing \(tint.rawValue)")
            XCTAssertEqual(tint.saturationScale, want ?? -1, accuracy: 0.0001, tint.rawValue)
        }
        XCTAssertEqual(ThemeTint.graphite.saturationScale, 0.10, accuracy: 0.0001)
    }

    // MARK: - GlassTheme.accent
    //
    // "Resolved against a DARK trait collection: hue = hueDegrees/360 ±0.02,
    // saturation = 0.45 × saturationScale ±0.05, brightness 1.0 ±0.05. Against
    // LIGHT: same hue ±0.02, saturation 0.72 × saturationScale ±0.05,
    // brightness = ThemeTint.lightBrightness ±0.05" — per-tint since the
    // 2026-08-07 r2/r3 amendments (one 0.82 constant failed WCAG contrast for
    // half the tints on the light field; the frozen per-tint table lives in
    // the contract's GlassTheme.accent row and is pinned by
    // testLightBrightnessMatchesTheContractTable below).
    //
    // Hue and the sat/brightness pair are separate tests per trait: when an
    // accent is wrong, WHICH of the three numbers moved is the entire diagnosis
    // (a hue drift is a table typo, a saturation drift is a scale applied at the
    // wrong step, a brightness drift is the light/dark branch swapped).

    func testAccentHueFollowsTheTintHueInDark() {
        for raw in Self.contractChromaticTints {
            let tint = ThemeTint.resolve(raw)
            let hsb = contractHSB(contractUI(GlassTheme(tint: tint).accent), .dark)
            let want = CGFloat(tint.hueDegrees / 360)
            XCTAssertLessThanOrEqual(contractHueDelta(hsb.hue, want), 0.02,
                                     "\(raw) dark accent hue \(hsb.hue), want \(want)")
        }
    }

    func testAccentHueFollowsTheTintHueInLight() {
        for raw in Self.contractChromaticTints {
            let tint = ThemeTint.resolve(raw)
            let hsb = contractHSB(contractUI(GlassTheme(tint: tint).accent), .light)
            let want = CGFloat(tint.hueDegrees / 360)
            XCTAssertLessThanOrEqual(contractHueDelta(hsb.hue, want), 0.02,
                                     "\(raw) light accent hue \(hsb.hue), want \(want)")
        }
    }

    func testAccentSaturationAndBrightnessInDark() {
        for tint in ThemeTint.allCases {
            let hsb = contractHSB(contractUI(GlassTheme(tint: tint).accent), .dark)
            XCTAssertEqual(hsb.saturation, CGFloat(0.45 * tint.saturationScale),
                           accuracy: 0.05, "\(tint.rawValue) dark saturation")
            XCTAssertEqual(hsb.brightness, 1.0,
                           accuracy: 0.05, "\(tint.rawValue) dark brightness")
        }
    }

    func testAccentSaturationAndBrightnessInLight() {
        for tint in ThemeTint.allCases {
            let hsb = contractHSB(contractUI(GlassTheme(tint: tint).accent), .light)
            XCTAssertEqual(hsb.saturation, CGFloat(0.72 * tint.saturationScale),
                           accuracy: 0.05, "\(tint.rawValue) light saturation")
            XCTAssertEqual(hsb.brightness, CGFloat(tint.lightBrightness),
                           accuracy: 0.05, "\(tint.rawValue) light brightness")
        }
    }

    // The per-tint light brightness table, frozen by the contract's
    // GlassTheme.accent row (amendments 2026-08-07 r2/r3): each value is the
    // brightness at which that hue's light accent clears WCAG 4.5:1 against
    // the light field's brightest stop. Asserted as literals so a re-derived
    // or "simplified back to one constant" table fails here rather than in a
    // manual contrast pass.
    func testLightBrightnessMatchesTheContractTable() {
        let want: [ThemeTint: Double] = [
            .violet: 0.82, .blue: 0.75, .cyan: 0.55, .green: 0.51,
            .amber: 0.56, .pink: 0.76, .red: 0.76, .graphite: 0.47,
        ]
        for tint in ThemeTint.allCases {
            XCTAssertEqual(tint.lightBrightness, want[tint]!, accuracy: 0.0001,
                           "\(tint.rawValue) lightBrightness")
        }
    }

    // Graphite's hue is held to the same ±0.02 as everything else, but it is
    // the one measurement in this file with real numerical exposure: at
    // saturation 0.045 the three channels sit within ~0.05 of each other, so
    // any 8-bit quantization on the way through the color space eats a
    // meaningful fraction of the tolerance. Isolated so that if this ever goes
    // red it is obvious the answer is "measure graphite differently", not
    // "the seven chromatic tints drifted".
    func testGraphiteAccentStillCarriesTheCoolHue() {
        let want = CGFloat(ThemeTint.graphite.hueDegrees / 360)
        for style in [UIUserInterfaceStyle.dark, .light] {
            let hsb = contractHSB(contractUI(GlassTheme(tint: .graphite).accent), style)
            XCTAssertLessThanOrEqual(contractHueDelta(hsb.hue, want), 0.02,
                                     "graphite accent hue \(hsb.hue) in \(style.rawValue)")
        }
    }

    // MARK: - GlassTheme.accentSoft

    // "The accent at reduced opacity or a derived soft fill — must carry the
    // same hue ±0.02 (both traits)." Deliberately asserted against the tint's
    // hue AND against the accent's own measured hue: the first catches a soft
    // fill built from the wrong tint, the second catches one that drifted off
    // its own accent while both stayed inside the table's tolerance.
    func testAccentSoftCarriesTheAccentHueInBothTraits() {
        for raw in Self.contractChromaticTints {
            let tint = ThemeTint.resolve(raw)
            let theme = GlassTheme(tint: tint)
            let want = CGFloat(tint.hueDegrees / 360)
            for style in [UIUserInterfaceStyle.dark, .light] {
                let soft = contractHSB(contractUI(theme.accentSoft), style)
                let accent = contractHSB(contractUI(theme.accent), style)
                XCTAssertLessThanOrEqual(contractHueDelta(soft.hue, want), 0.02,
                                         "\(raw) accentSoft hue vs table, style \(style.rawValue)")
                XCTAssertLessThanOrEqual(contractHueDelta(soft.hue, accent.hue), 0.02,
                                         "\(raw) accentSoft hue vs accent, style \(style.rawValue)")
            }
        }
    }

    // MARK: - persistence key

    // "The one UserDefaults key for the persisted tint." The literal matters
    // beyond taste: the Settings picker writes through it and app start reads
    // through it, so a renamed key does not fail — it silently forgets every
    // existing user's choice on the update that renames it.
    func testThemeTintKeyIsTheContractLiteral() {
        XCTAssertEqual(KTheme.themeTintKey, Self.contractTintKey)
    }

    // MARK: - KTheme.accent follows the stored tint

    // "Re-derived from the CURRENTLY STORED tint (dynamic resolution — a
    // UIColor provider may read UserDefaults). After a tint change plus view
    // invalidation, KTheme.accent resolves to the new tint's hue."
    // `resolvedColor(with:)` IS the invalidation moment for a dynamic provider,
    // so a stored change followed by a resolve is exactly the contract's claim.
    func testKThemeAccentResolvesToTheStoredTintsHue() {
        for raw in ["green", "amber", "pink"] {
            contractStoreTint(raw)
            let want = CGFloat(ThemeTint.resolve(raw).hueDegrees / 360)
            for style in [UIUserInterfaceStyle.dark, .light] {
                let hsb = contractHSB(contractUI(KTheme.accent), style)
                XCTAssertLessThanOrEqual(contractHueDelta(hsb.hue, want), 0.02,
                                         "stored \(raw), style \(style.rawValue): hue \(hsb.hue)")
            }
        }
    }

    // No stored value is the state every user is in on first launch and the
    // state `resolve(nil)` exists for; violet is the documented default.
    func testKThemeAccentDefaultsToVioletWithNothingStored() {
        UserDefaults.standard.removeObject(forKey: Self.contractTintKey)
        let want = CGFloat(ThemeTint.violet.hueDegrees / 360)
        for style in [UIUserInterfaceStyle.dark, .light] {
            let hsb = contractHSB(contractUI(KTheme.accent), style)
            XCTAssertLessThanOrEqual(contractHueDelta(hsb.hue, want), 0.02,
                                     "unset tint, style \(style.rawValue): hue \(hsb.hue)")
        }
    }

    // MARK: - environment default

    // "custom EnvironmentKey, default GlassTheme(tint: .violet)". The default
    // is what any view rendered outside the injected root sees — previews,
    // sheets presented from UIKit hosting, and anything the `.id(tint)`
    // re-injection misses.
    @MainActor
    func testEnvironmentThemeDefaultsToVioletGlass() {
        XCTAssertEqual(EnvironmentValues().theme.tint, .violet)
    }

    // MARK: - Font.data / Font.voice bucket tables
    //
    // "Returns a MONOSPACED font built from a Dynamic Type text style by this
    // bucket table on `size`: ≥17 → .body · 15..<17 → .callout · 13..<15 →
    // .footnote · 11..<13 → .caption · <11 → .caption2. i.e.
    // `Font.system(<style>, design: .monospaced, weight: weight)` — equality
    // with that expression is the test. The old bumped() arithmetic is gone."
    //
    // Sizes below are the happy side of every boundary: the first size in each
    // bucket and the last size before the next one starts. `Font.system(…)` is
    // written out per row rather than looked up, because the frozen expression
    // is the requirement.

    func testFontDataBucketsSizesToTheContractTextStyles() {
        let cases: [(CGFloat, Font)] = [
            (34, .system(.body, design: .monospaced, weight: .regular)),
            (17, .system(.body, design: .monospaced, weight: .regular)),
            (16.9, .system(.callout, design: .monospaced, weight: .regular)),
            (15, .system(.callout, design: .monospaced, weight: .regular)),
            (14.9, .system(.footnote, design: .monospaced, weight: .regular)),
            (13, .system(.footnote, design: .monospaced, weight: .regular)),
            (12.9, .system(.caption, design: .monospaced, weight: .regular)),
            (11, .system(.caption, design: .monospaced, weight: .regular)),
            (10.9, .system(.caption2, design: .monospaced, weight: .regular)),
            (8, .system(.caption2, design: .monospaced, weight: .regular)),
        ]
        for (size, want) in cases {
            XCTAssertEqual(Font.data(size), want, "Font.data(\(size))")
        }
    }

    func testFontVoiceBucketsSizesToTheContractTextStyles() {
        let cases: [(CGFloat, Font)] = [
            (34, .system(.body, design: .default, weight: .regular)),
            (17, .system(.body, design: .default, weight: .regular)),
            (16.9, .system(.callout, design: .default, weight: .regular)),
            (15, .system(.callout, design: .default, weight: .regular)),
            (14.9, .system(.footnote, design: .default, weight: .regular)),
            (13, .system(.footnote, design: .default, weight: .regular)),
            (12.9, .system(.caption, design: .default, weight: .regular)),
            (11, .system(.caption, design: .default, weight: .regular)),
            (10.9, .system(.caption2, design: .default, weight: .regular)),
            (8, .system(.caption2, design: .default, weight: .regular)),
        ]
        for (size, want) in cases {
            XCTAssertEqual(Font.voice(size), want, "Font.voice(\(size))")
        }
    }

    // The weight argument is passed through, not consumed by the bucketing.
    // Checked at two sizes so a helper that hardcoded .regular for one branch
    // cannot hide behind the other.
    func testFontDataPassesTheRequestedWeightThrough() {
        XCTAssertEqual(Font.data(17, weight: .semibold),
                       .system(.body, design: .monospaced, weight: .semibold))
        XCTAssertEqual(Font.data(12, weight: .bold),
                       .system(.caption, design: .monospaced, weight: .bold))
    }

    func testFontVoicePassesTheRequestedWeightThrough() {
        XCTAssertEqual(Font.voice(17, weight: .semibold),
                       .system(.body, design: .default, weight: .semibold))
        XCTAssertEqual(Font.voice(12, weight: .bold),
                       .system(.caption, design: .default, weight: .bold))
    }

    // Q2 in force: "Mono is data-only — the existing .data/.voice split IS that
    // decision." The split only means anything if the two helpers still differ
    // in design at the same size.
    func testDataAndVoiceStayDistinctDesignsAtTheSameSize() {
        XCTAssertNotEqual(Font.data(17), Font.voice(17))
        XCTAssertNotEqual(Font.data(11), Font.voice(11))
    }

    // MARK: - BoardTheme.color(forState:)
    //
    // "Behaviour byte-for-byte preserved: ... working→solved, blocked→counter,
    // draft/done/archived→text2 ... NO state hue follows the tint" — amendment
    // 2026-08-07 r2: meaning colors never re-tint, without exception. The
    // queued arm of that row went with the queued state itself; the tokens
    // asserted below are the ones that survive it.
    //
    // Compared as resolved RGB in both traits rather than by `==` on the token:
    // the obligation is that the two render identically, and a re-derived
    // dynamic color can be a different value with the same rendering.

    private func contractAssertSameColor(_ got: UIColor, _ want: UIColor,
                                         _ label: String,
                                         file: StaticString = #filePath, line: UInt = #line) {
        for style in [UIUserInterfaceStyle.dark, .light] {
            let g = contractRGB(got, style), w = contractRGB(want, style)
            XCTAssertEqual(g.r, w.r, accuracy: 0.004, "\(label) red, style \(style.rawValue)",
                           file: file, line: line)
            XCTAssertEqual(g.g, w.g, accuracy: 0.004, "\(label) green, style \(style.rawValue)",
                           file: file, line: line)
            XCTAssertEqual(g.b, w.b, accuracy: 0.004, "\(label) blue, style \(style.rawValue)",
                           file: file, line: line)
        }
    }

    func testStateColorsKeepTheirPreservedKThemeMapping() {
        contractAssertSameColor(contractUI(BoardTheme.color(forState: "working")),
                                contractUI(KTheme.solved), "working→solved")
        contractAssertSameColor(contractUI(BoardTheme.color(forState: "blocked")),
                                contractUI(KTheme.counter), "blocked→counter")
        for state in ["draft", "done", "archived"] {
            contractAssertSameColor(contractUI(BoardTheme.color(forState: state)),
                                    contractUI(KTheme.text2), "\(state)→text2")
        }
    }

    // The r2 amendment's whole point: a state hue must NOT move with the
    // stored tint. It was written for queued, which was the one state hue
    // ever derived from the accent (under a pink tint it sat 3° from
    // blocked's counter hue); queued is retired, so the claim is carried by
    // the two hues that remain. Asserted under the most adversarial tints
    // rather than the default.
    func testStateColorsIgnoreTheStoredTint() {
        for tint in ["pink", "amber"] {
            contractStoreTint(tint)
            contractAssertSameColor(contractUI(BoardTheme.color(forState: "working")),
                                    contractUI(KTheme.solved), "working under \(tint) tint")
            contractAssertSameColor(contractUI(BoardTheme.color(forState: "blocked")),
                                    contractUI(KTheme.counter), "blocked under \(tint) tint")
        }
    }

    // The live states are the board's whole read-at-a-glance promise
    // (52-testing-plan.md: the live states "map to distinguishable hues in
    // both appearances" — it named three while queued was one of them). An
    // equal pair would satisfy the mapping test above only if two KTheme
    // tokens had collapsed onto one color.
    func testWorkingAndBlockedStayDistinctColors() {
        for style in [UIUserInterfaceStyle.dark, .light] {
            let working = contractRGB(contractUI(BoardTheme.color(forState: "working")), style)
            let blocked = contractRGB(contractUI(BoardTheme.color(forState: "blocked")), style)
            XCTAssertNotEqual(working, blocked, "working vs blocked, style \(style.rawValue)")
        }
    }

    // MARK: - BoardTheme.jobColor

    // Every name the server can persist (`internal/jobs.Palette`, the list
    // `/color` validates against) must resolve to something. Asserted apart
    // from the hex check below so "the eight valid names still work" survives
    // as a claim even if the palette's literals were ever legitimately renegotiated.
    func testEveryPaletteNameIsAValidJobColor() {
        for entry in Self.contractJobPalette {
            XCTAssertNotNil(BoardTheme.jobColor(entry.name), "jobColor(\(entry.name))")
        }
    }

    // The eight `jobRed…jobCyan` lines are BYTE-STABLE and cross-pinned to
    // `internal/jobs.Palette` by web/jobpalette_test.go. That Go test reads the
    // source line; this one reads the RUNTIME value through the public lookup,
    // so between them a swapped light/dark order, or a name wired to the wrong
    // literal, is caught — neither of which the source-parsing test can see.
    func testJobColorResolvesEveryPaletteNameToItsFrozenHex() {
        for entry in Self.contractJobPalette {
            guard let color = BoardTheme.jobColor(entry.name) else {
                XCTFail("jobColor(\(entry.name)) is nil")
                continue
            }
            let ui = contractUI(color)
            let dark = contractRGB(ui, .dark), wantDark = contractHexRGB(entry.dark)
            XCTAssertEqual(dark.r, wantDark.r, accuracy: 0.004, "\(entry.name) dark red")
            XCTAssertEqual(dark.g, wantDark.g, accuracy: 0.004, "\(entry.name) dark green")
            XCTAssertEqual(dark.b, wantDark.b, accuracy: 0.004, "\(entry.name) dark blue")

            let light = contractRGB(ui, .light), wantLight = contractHexRGB(entry.light)
            XCTAssertEqual(light.r, wantLight.r, accuracy: 0.004, "\(entry.name) light red")
            XCTAssertEqual(light.g, wantLight.g, accuracy: 0.004, "\(entry.name) light green")
            XCTAssertEqual(light.b, wantLight.b, accuracy: 0.004, "\(entry.name) light blue")
        }
    }

    // "Job colors are NOT derived from the tint." Same lookup under two very
    // different stored tints must give the same bytes, or the board swatch
    // stops being the same pixel color as the tmux prompt bar of the pane that
    // job opens — the entire point of the shared palette.
    func testJobColorsAreIndependentOfTheStoredTint() {
        contractStoreTint("violet")
        let underViolet = Self.contractJobPalette.map { entry -> [ContractRGB] in
            let ui = contractUI(BoardTheme.jobColor(entry.name) ?? .clear)
            return [contractRGB(ui, .dark), contractRGB(ui, .light)]
        }
        contractStoreTint("amber")
        let underAmber = Self.contractJobPalette.map { entry -> [ContractRGB] in
            let ui = contractUI(BoardTheme.jobColor(entry.name) ?? .clear)
            return [contractRGB(ui, .dark), contractRGB(ui, .light)]
        }
        for (i, entry) in Self.contractJobPalette.enumerated() {
            XCTAssertEqual(underViolet[i], underAmber[i], "\(entry.name) moved with the tint")
        }
    }

    // MARK: - SessionTheme.color(forStatus:role:)

    // "role shell→text2." A shell pane has no agent and no agent status, so it
    // is deliberately the muted token whatever status rides along with it —
    // the rule is on the role, not on the pair.
    func testShellRoleAlwaysUsesTheMutedTextColor() {
        contractStoreTint("violet")
        for status in ["running", "waiting", "in-process", ""] {
            contractAssertSameColor(contractUI(SessionTheme.color(forStatus: status, role: "shell")),
                                    contractUI(KTheme.text2),
                                    "shell/\(status.isEmpty ? "<empty>" : status)→text2")
        }
    }
}
