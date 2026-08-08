import Foundation
import XCTest

@testable import KrakenMac

// Blind CONTRACT-dimension tests for Kraken.app, authored against
// artifacts/50-contract.md alone — no byte of macos/Kraken/ was read while
// writing this file. Every assertion below is derived from an obligation
// column in that contract's S1-S6 tables, or from a reference the contract
// explicitly cites (web/_ui/src/styles/style.css for the theme primitives,
// internal/jobs/color.go for the job palette).
//
// The rule that shapes the file: where the contract is SILENT, there is no
// test. A tolerant probe around a gap would turn a missing decision into a
// green tick; the gaps are reported to the join instead. Every assertion here
// asserts exactly one answer.
//
// Naming: every package-level declaration is `contract`-prefixed and file-
// private, and every test class starts with `HudContract`, so a second blind
// author writing into this same module cannot collide with anything here.

// MARK: - shared colour arithmetic

/// A plain 0-255 triple, kept separate from the module's `RGB` so an
/// expectation is never computed by the same code it checks.
private struct ContractRGBValue: Equatable {
    let r: Int
    let g: Int
    let b: Int
}

/// `#rrggbb` → triple. Byte-exact; this is how the style.css / color.go hexes
/// quoted throughout this file enter the assertions.
private func contractHex(_ s: String) -> ContractRGBValue {
    var t = s
    if t.hasPrefix("#") { t.removeFirst() }
    let v = Int(t, radix: 16) ?? -1
    precondition(v >= 0, "contract test fixture carries a malformed hex: \(s)")
    return ContractRGBValue(r: (v >> 16) & 0xFF, g: (v >> 8) & 0xFF, b: v & 0xFF)
}

/// The contract's own definition of the mix: `channel = round(a·f + b·(1−f))`
/// on 0-255, plain sRGB, no gamma step.
private func contractMixValue(_ a: ContractRGBValue, _ pct: Double, over b: ContractRGBValue) -> ContractRGBValue {
    let f = pct / 100.0
    func ch(_ x: Int, _ y: Int) -> Int {
        Int((Double(x) * f + Double(y) * (1.0 - f)).rounded())
    }
    return ContractRGBValue(r: ch(a.r, b.r), g: ch(a.g, b.g), b: ch(a.b, b.b))
}

/// Colour equality at the contract's stated tolerance: each channel within ±1,
/// which absorbs rounding-mode differences and nothing else.
private func contractAssertColor(
    _ actual: RGB,
    _ expected: ContractRGBValue,
    _ what: String,
    file: StaticString = #filePath,
    line: UInt = #line
) {
    let ok = abs(actual.r - expected.r) <= 1
        && abs(actual.g - expected.g) <= 1
        && abs(actual.b - expected.b) <= 1
    XCTAssertTrue(
        ok,
        "\(what): got rgb(\(actual.r), \(actual.g), \(actual.b)) "
            + "want rgb(\(expected.r), \(expected.g), \(expected.b)) ±1/channel",
        file: file,
        line: line
    )
}

private func contractAssertColor(
    _ actual: RGB,
    hex: String,
    _ what: String,
    file: StaticString = #filePath,
    line: UInt = #line
) {
    contractAssertColor(actual, contractHex(hex), "\(what) (\(hex))", file: file, line: line)
}

// MARK: - the theme reference tables
//
// Primitives are read straight out of web/_ui/src/styles/style.css, which the
// contract names as the byte-exact source: the dark `:root` block, the
// `:root[data-theme="light"]` block (style.css:163-214), and the two accent
// override blocks (style.css:148-151 dark, :221-224 light).

private struct ContractPrimitives {
    let b0: String
    let b1: String
    let g1: String
    let g2: String
    let fg: String
    let ok: String
    let warn: String
    let polling: String
}

private let contractDarkPrimitives = ContractPrimitives(
    b0: "#0a090d",
    b1: "#0d0c12",
    g1: "#90909a",
    g2: "#ababb8",
    fg: "#f2edff",
    ok: "#7fd44a",
    warn: "#ffb000",
    polling: "#00e5ff"
)

private let contractLightPrimitives = ContractPrimitives(
    b0: "#f6f4fa",
    b1: "#ecebf3",
    g1: "#6b6a78",
    g2: "#4c4b58",
    fg: "#1a1724",
    ok: "#3e8f1e",
    warn: "#a36f00",
    polling: "#00799c"
)

private struct ContractAccentHexes {
    let ac: String
    let ac2: String
    let tint: String
}

/// style.css `:root` (violet, the default) + `:root[data-accent="…"]` :148-151.
private let contractDarkAccents: [HudAccent: ContractAccentHexes] = [
    .violet: ContractAccentHexes(ac: "#a86aff", ac2: "#ff4fd8", tint: "#c9b2ff"),
    .cyan: ContractAccentHexes(ac: "#00e5ff", ac2: "#ff2d95", tint: "#a8f4ff"),
    .amber: ContractAccentHexes(ac: "#ffb000", ac2: "#ff6a00", tint: "#ffe0a8"),
    .crimson: ContractAccentHexes(ac: "#ff3b5c", ac2: "#ff8a00", tint: "#ffb2c0"),
    .green: ContractAccentHexes(ac: "#2bff88", ac2: "#ffcf3f", tint: "#b2ffd4"),
]

/// style.css `:root[data-theme="light"]` (violet) + the (0,3,0) light accent
/// blocks at :221-224.
private let contractLightAccents: [HudAccent: ContractAccentHexes] = [
    .violet: ContractAccentHexes(ac: "#6d3fd4", ac2: "#c41d9e", tint: "#5a2eb8"),
    .cyan: ContractAccentHexes(ac: "#00799c", ac2: "#c41d6e", tint: "#005f7d"),
    .amber: ContractAccentHexes(ac: "#a36f00", ac2: "#bc4e00", tint: "#8a5e00"),
    .crimson: ContractAccentHexes(ac: "#c2183a", ac2: "#b05e00", tint: "#a3142f"),
    .green: ContractAccentHexes(ac: "#0e8a45", ac2: "#9c7a00", tint: "#0b7038"),
]

private func contractPrimitives(_ mode: ThemeMode) -> ContractPrimitives {
    mode == .dark ? contractDarkPrimitives : contractLightPrimitives
}

private func contractAccent(_ mode: ThemeMode, _ accent: HudAccent) -> ContractAccentHexes {
    let table = mode == .dark ? contractDarkAccents : contractLightAccents
    guard let hexes = table[accent] else {
        preconditionFailure("contract test fixture has no accent row for \(mode)/\(accent)")
    }
    return hexes
}

/// Which primitive a derivation recipe mixes the accent OVER.
private enum ContractRecipeBase {
    case b0
    case b1
    case g1
    case g2
}

/// The nine derived fields the contract freezes as `HudTheme` properties, each
/// with its style.css recipe percentage and base.
private struct ContractRecipe {
    let name: String
    let pct: Double
    let base: ContractRecipeBase
    let read: (HudTheme) -> RGB
}

private let contractRecipes: [ContractRecipe] = [
    ContractRecipe(name: "bgBoard", pct: 4, base: .b0, read: { $0.bgBoard }),
    ContractRecipe(name: "surface", pct: 7, base: .b1, read: { $0.surface }),
    ContractRecipe(name: "border", pct: 26, base: .b1, read: { $0.border }),
    ContractRecipe(name: "borderStrong", pct: 36, base: .b1, read: { $0.borderStrong }),
    ContractRecipe(name: "divider", pct: 16, base: .b1, read: { $0.divider }),
    ContractRecipe(name: "muted", pct: 28, base: .g1, read: { $0.muted }),
    ContractRecipe(name: "body", pct: 24, base: .g2, read: { $0.body }),
    ContractRecipe(name: "chipBg", pct: 14, base: .b1, read: { $0.chipBg }),
    ContractRecipe(name: "raised", pct: 6, base: .b1, read: { $0.raised }),
]

private func contractExpectedDerived(
    _ recipe: ContractRecipe,
    _ mode: ThemeMode,
    _ accent: HudAccent
) -> ContractRGBValue {
    let prims = contractPrimitives(mode)
    let baseHex: String
    switch recipe.base {
    case .b0: baseHex = prims.b0
    case .b1: baseHex = prims.b1
    case .g1: baseHex = prims.g1
    case .g2: baseHex = prims.g2
    }
    return contractMixValue(
        contractHex(contractAccent(mode, accent).ac),
        recipe.pct,
        over: contractHex(baseHex)
    )
}

// MARK: - S2: theme engine

final class HudContractThemeTests: XCTestCase {

    func testThemeModeHasExactlyTwoCases() {
        XCTAssertEqual(ThemeMode.allCases.map(\.rawValue), ["dark", "light"])
    }

    func testHudAccentHasExactlyFiveCasesInOrder() {
        XCTAssertEqual(
            HudAccent.allCases.map(\.rawValue),
            ["violet", "cyan", "amber", "crimson", "green"]
        )
    }

    func testRGBInitAcceptsHashRRGGBB() {
        let c = RGB(hex: "#a86aff")
        XCTAssertEqual(c.r, 168)
        XCTAssertEqual(c.g, 106)
        XCTAssertEqual(c.b, 255)

        let black = RGB(hex: "#000000")
        XCTAssertEqual(ContractRGBValue(r: black.r, g: black.g, b: black.b),
                       ContractRGBValue(r: 0, g: 0, b: 0))
        let white = RGB(hex: "#ffffff")
        XCTAssertEqual(ContractRGBValue(r: white.r, g: white.g, b: white.b),
                       ContractRGBValue(r: 255, g: 255, b: 255))
    }

    /// The contract's own worked example: `mix(ac, 26, over: b1)` with the
    /// violet-dark primitives is `#352450`.
    func testMixIsPlainSRGBLerpAndHitsTheWorkedExample() {
        let ac = RGB(hex: "#a86aff")
        let b1 = RGB(hex: "#0d0c12")
        contractAssertColor(HudTheme.mix(ac, 26, over: b1), hex: "#352450", "mix(ac, 26, over: b1)")

        // the two endpoints of the lerp
        contractAssertColor(HudTheme.mix(ac, 100, over: b1), hex: "#a86aff", "mix at 100%")
        contractAssertColor(HudTheme.mix(ac, 0, over: b1), hex: "#0d0c12", "mix at 0%")

        // a midpoint that is not either primitive
        contractAssertColor(
            HudTheme.mix(RGB(hex: "#000000"), 50, over: RGB(hex: "#ffffff")),
            ContractRGBValue(r: 128, g: 128, b: 128),
            "mix(black, 50, over: white)"
        )
    }

    func testDarkVioletPrimitivesArePinned() {
        let t = HudTheme.resolve(.dark, .violet)
        contractAssertColor(t.ac, hex: "#a86aff", "dark/violet ac")
        contractAssertColor(t.ac2, hex: "#ff4fd8", "dark/violet ac2")
        contractAssertColor(t.tint, hex: "#c9b2ff", "dark/violet tint")
        contractAssertColor(t.b0, hex: "#0a090d", "dark b0")
        contractAssertColor(t.b1, hex: "#0d0c12", "dark b1")
        contractAssertColor(t.fg, hex: "#f2edff", "dark fg")
        contractAssertColor(t.ok, hex: "#7fd44a", "dark ok")
        contractAssertColor(t.warn, hex: "#ffb000", "dark warn")
        contractAssertColor(t.polling, hex: "#00e5ff", "dark polling")
    }

    func testLightVioletPrimitivesArePinned() {
        let t = HudTheme.resolve(.light, .violet)
        contractAssertColor(t.ac, hex: "#6d3fd4", "light/violet ac")
        contractAssertColor(t.ac2, hex: "#c41d9e", "light/violet ac2")
        contractAssertColor(t.tint, hex: "#5a2eb8", "light/violet tint")
        contractAssertColor(t.b0, hex: "#f6f4fa", "light b0")
        contractAssertColor(t.b1, hex: "#ecebf3", "light b1")
        contractAssertColor(t.fg, hex: "#1a1724", "light fg")
        contractAssertColor(t.ok, hex: "#3e8f1e", "light ok")
        contractAssertColor(t.warn, hex: "#a36f00", "light warn")
        contractAssertColor(t.polling, hex: "#00799c", "light polling")
    }

    /// "the other four accents override ONLY `ac`/`ac2`/`tint`" — in both modes.
    func testAccentsOverrideOnlyTheThreeHuePrimitives() {
        for mode in ThemeMode.allCases {
            let prims = contractPrimitives(mode)
            for accent in HudAccent.allCases {
                let t = HudTheme.resolve(mode, accent)
                let want = contractAccent(mode, accent)
                let tag = "\(mode.rawValue)/\(accent.rawValue)"
                contractAssertColor(t.ac, hex: want.ac, "\(tag) ac")
                contractAssertColor(t.ac2, hex: want.ac2, "\(tag) ac2")
                contractAssertColor(t.tint, hex: want.tint, "\(tag) tint")

                contractAssertColor(t.b0, hex: prims.b0, "\(tag) b0 must not follow the accent")
                contractAssertColor(t.b1, hex: prims.b1, "\(tag) b1 must not follow the accent")
                contractAssertColor(t.fg, hex: prims.fg, "\(tag) fg must not follow the accent")
                contractAssertColor(t.ok, hex: prims.ok, "\(tag) ok must not follow the accent")
                contractAssertColor(t.warn, hex: prims.warn, "\(tag) warn must not follow the accent")
                contractAssertColor(t.polling, hex: prims.polling, "\(tag) polling must not follow the accent")
            }
        }
    }

    /// The nine hexes the contract spells out by hand for dark+violet.
    func testDarkVioletDerivedFieldsArePinnedHexes() {
        let t = HudTheme.resolve(.dark, .violet)
        contractAssertColor(t.bgBoard, hex: "#100d17", "dark/violet bgBoard")
        contractAssertColor(t.surface, hex: "#181323", "dark/violet surface")
        contractAssertColor(t.border, hex: "#352450", "dark/violet border")
        contractAssertColor(t.borderStrong, hex: "#452e67", "dark/violet borderStrong")
        contractAssertColor(t.divider, hex: "#261b38", "dark/violet divider")
        contractAssertColor(t.muted, hex: "#9785b6", "dark/violet muted")
        contractAssertColor(t.body, hex: "#aa9bc9", "dark/violet body")
        contractAssertColor(t.chipBg, hex: "#231933", "dark/violet chipBg")
        contractAssertColor(t.raised, hex: "#161220", "dark/violet raised")
    }

    /// Every derived field, in every mode × accent, equals the style.css recipe
    /// arithmetic — 2 × 5 × 9 = 90 assertions.
    func testEveryDerivedFieldFollowsItsRecipeInEveryModeAndAccent() {
        for mode in ThemeMode.allCases {
            for accent in HudAccent.allCases {
                let t = HudTheme.resolve(mode, accent)
                for recipe in contractRecipes {
                    contractAssertColor(
                        recipe.read(t),
                        contractExpectedDerived(recipe, mode, accent),
                        "\(mode.rawValue)/\(accent.rawValue) \(recipe.name) "
                            + "= mix(ac \(Int(recipe.pct))%, base)"
                    )
                }
            }
        }
    }

    /// "changing accent … changes every derived field consistently" — under
    /// cyan every recipe input moved, so no derived field may still read its
    /// dark+violet value.
    func testChangingAccentMovesEveryDerivedField() {
        let violet = HudTheme.resolve(.dark, .violet)
        let cyan = HudTheme.resolve(.dark, .cyan)
        for recipe in contractRecipes {
            let a = recipe.read(violet)
            let b = recipe.read(cyan)
            XCTAssertNotEqual(
                ContractRGBValue(r: a.r, g: a.g, b: a.b),
                ContractRGBValue(r: b.r, g: b.g, b: b.b),
                "\(recipe.name) is unchanged between dark/violet and dark/cyan"
            )
        }
    }

    /// The same obligation for the other axis: dark → light moves every base
    /// AND the accent, so every derived field must move.
    func testChangingModeMovesEveryDerivedField() {
        let dark = HudTheme.resolve(.dark, .violet)
        let light = HudTheme.resolve(.light, .violet)
        for recipe in contractRecipes {
            let a = recipe.read(dark)
            let b = recipe.read(light)
            XCTAssertNotEqual(
                ContractRGBValue(r: a.r, g: a.g, b: a.b),
                ContractRGBValue(r: b.r, g: b.g, b: b.b),
                "\(recipe.name) is unchanged between dark/violet and light/violet"
            )
        }
    }

    /// "no glow in light mode, on anything" — and dark keeps all four.
    func testGlowsAreNonNilInDarkAndAllNilInLight() {
        for accent in HudAccent.allCases {
            let dark = HudTheme.resolve(.dark, accent)
            XCTAssertNotNil(dark.glow, "dark/\(accent.rawValue) glow")
            XCTAssertNotNil(dark.glowHot, "dark/\(accent.rawValue) glowHot")
            XCTAssertNotNil(dark.glowOK, "dark/\(accent.rawValue) glowOK")
            XCTAssertNotNil(dark.glowWarn, "dark/\(accent.rawValue) glowWarn")

            let light = HudTheme.resolve(.light, accent)
            XCTAssertNil(light.glow, "light/\(accent.rawValue) glow")
            XCTAssertNil(light.glowHot, "light/\(accent.rawValue) glowHot")
            XCTAssertNil(light.glowOK, "light/\(accent.rawValue) glowOK")
            XCTAssertNil(light.glowWarn, "light/\(accent.rawValue) glowWarn")
        }
    }
}

// MARK: - S2: job palette
//
// The sixteen hexes are internal/jobs/color.go's `Palette` — `Hex` for dark,
// `HexLight` for light — in /color's own order.

private let contractJobPaletteDark: [(String, String)] = [
    ("red", "#DC2626"),
    ("blue", "#6A9BCC"),
    ("green", "#16A34A"),
    ("yellow", "#CA8A04"),
    ("purple", "#827DBD"),
    ("orange", "#D97757"),
    ("pink", "#C46686"),
    ("cyan", "#0891B2"),
]

private let contractJobPaletteLight: [(String, String)] = [
    ("red", "#CA2323"),
    ("blue", "#496B8D"),
    ("green", "#107937"),
    ("yellow", "#8B5F03"),
    ("purple", "#676395"),
    ("orange", "#9C563F"),
    ("pink", "#9B516A"),
    ("cyan", "#06718B"),
]

final class HudContractJobPaletteTests: XCTestCase {

    func testDarkPaletteMatchesColorGo() {
        for (name, hex) in contractJobPaletteDark {
            guard let got = JobPalette.color(name, .dark) else {
                return XCTFail("JobPalette.color(\"\(name)\", .dark) returned nil")
            }
            contractAssertColor(got, hex: hex, "job palette dark \(name)")
        }
    }

    func testLightPaletteMatchesColorGo() {
        for (name, hex) in contractJobPaletteLight {
            guard let got = JobPalette.color(name, .light) else {
                return XCTFail("JobPalette.color(\"\(name)\", .light) returned nil")
            }
            contractAssertColor(got, hex: hex, "job palette light \(name)")
        }
    }

    /// nil off-list is what makes the card's 40%-accent fallback edge fire, so
    /// it is a behaviour rather than a defensive default. `""` is the unset
    /// job colour and must be nil too; `"default"` is /color's clear verb, not
    /// a colour; case is significant.
    func testOffListNamesAreNil() {
        for mode in ThemeMode.allCases {
            for name in ["", "default", "magenta", "Red", "RED", " red", "red ", "grey"] {
                XCTAssertNil(
                    JobPalette.color(name, mode),
                    "JobPalette.color(\"\(name)\", .\(mode.rawValue)) must be nil"
                )
            }
        }
    }
}

// MARK: - S3: board logic

final class HudContractBoardTests: XCTestCase {

    func testBoardColumnDisplayOrder() {
        XCTAssertEqual(
            BoardColumn.allCases.map(\.rawValue),
            ["draft", "queued", "working", "blocked", "done", "archived"]
        )
    }

    func testBucketingMapsEachStateToItsColumn() {
        XCTAssertEqual(Bucketing.bucket(state: "draft", health: nil, archived: false), .draft)
        XCTAssertEqual(Bucketing.bucket(state: "queued", health: nil, archived: false), .queued)
        XCTAssertEqual(Bucketing.bucket(state: "working", health: nil, archived: false), .working)
        XCTAssertEqual(Bucketing.bucket(state: "blocked", health: nil, archived: false), .blocked)
        XCTAssertEqual(Bucketing.bucket(state: "done", health: nil, archived: false), .done)
    }

    func testBucketingUnknownStateFallsToDraft() {
        XCTAssertEqual(Bucketing.bucket(state: "bogus", health: nil, archived: false), .draft)
        XCTAssertEqual(Bucketing.bucket(state: "", health: nil, archived: false), .draft)
    }

    /// "a non-nil non-empty `health` puts any live state in `.blocked`" — an
    /// unhealthy cell needs a retry and the daemon is not going to fix it.
    func testBucketingHealthRelocatesAnyLiveStateToBlocked() {
        XCTAssertEqual(Bucketing.bucket(state: "working", health: "dead cell", archived: false), .blocked)
        XCTAssertEqual(Bucketing.bucket(state: "queued", health: "dead cell", archived: false), .blocked)
        XCTAssertEqual(Bucketing.bucket(state: "draft", health: "dead cell", archived: false), .blocked)
        XCTAssertEqual(Bucketing.bucket(state: "done", health: "dead cell", archived: false), .blocked)
    }

    /// Empty health is not health — it is the wire's absence of one.
    func testBucketingEmptyHealthIsNotHealth() {
        XCTAssertEqual(Bucketing.bucket(state: "working", health: "", archived: false), .working)
        XCTAssertEqual(Bucketing.bucket(state: "queued", health: "", archived: false), .queued)
    }

    func testBucketingArchivedWinsOverEverything() {
        XCTAssertEqual(Bucketing.bucket(state: "working", health: "dead cell", archived: true), .archived)
        XCTAssertEqual(Bucketing.bucket(state: "draft", health: nil, archived: true), .archived)
        XCTAssertEqual(Bucketing.bucket(state: "bogus", health: nil, archived: true), .archived)
    }

    /// The cross-column half of the ten-entry table.
    private var contractCrossMoves: [String: MoveAction] {
        [
            "draft>queued": .post(endpoint: "/api/queue"),
            "queued>draft": .post(endpoint: "/api/dequeue"),
            "queued>working": .post(endpoint: "/api/start"),
            "working>blocked": .post(endpoint: "/api/stop"),
            "blocked>done": .post(endpoint: "/api/done"),
            "blocked>working": .steer(mode: .unblock, requireText: false),
            "done>blocked": .post(endpoint: "/api/reactivate"),
            "done>working": .steer(mode: .reactivate, requireText: true),
            "done>archived": .post(endpoint: "/api/archive"),
        ]
    }

    /// The whole 6×6 space, swept: the nine cross moves, the five same-column
    /// reorders, and nil everywhere else — archived as a source, and
    /// archived→archived, included.
    func testMoveTableIsExhaustivelyPinned() {
        var nonNil = 0
        for from in BoardColumn.allCases {
            for to in BoardColumn.allCases {
                let key = "\(from.rawValue)>\(to.rawValue)"
                let got = MoveTable.action(from: from, to: to)
                if from == to && from != .archived {
                    XCTAssertEqual(got, .reorder, key)
                } else if let want = contractCrossMoves[key] {
                    XCTAssertEqual(got, want, key)
                } else {
                    XCTAssertNil(got, "\(key) must be a no-op")
                }
                if got != nil { nonNil += 1 }
            }
        }
        // nine cross-column moves + five same-column reorders
        XCTAssertEqual(nonNil, 14)
    }

    func testMoveTableRefusesEveryArchivedSource() {
        for to in BoardColumn.allCases {
            XCTAssertNil(
                MoveTable.action(from: .archived, to: to),
                "archived>\(to.rawValue) must be a no-op"
            )
        }
    }

    /// Only `done` may reach the archive drawer.
    func testOnlyDoneMayBeArchived() {
        for from in BoardColumn.allCases where from != .done {
            XCTAssertNil(MoveTable.action(from: from, to: .archived), "\(from.rawValue)>archived")
        }
        XCTAssertEqual(MoveTable.action(from: .done, to: .archived), .post(endpoint: "/api/archive"))
    }

    func testReorderPredictionMovesTheDraggedSlug() {
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "a", dropIndex: 2),
            ["b", "c", "a"]
        )
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "c", dropIndex: 0),
            ["c", "a", "b"]
        )
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "b", dropIndex: 1),
            ["a", "b", "c"]
        )
    }

    func testReorderPredictionIsNilForAnUnknownSlug() {
        XCTAssertNil(ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "z", dropIndex: 0))
        XCTAssertNil(ReorderPrediction.predict(slugs: [], dragged: "a", dropIndex: 0))
    }

    func testReorderPredictionClampsPastTheEnd() {
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "a", dropIndex: 99),
            ["b", "c", "a"]
        )
        XCTAssertEqual(
            ReorderPrediction.predict(slugs: ["a", "b", "c"], dragged: "a", dropIndex: 3),
            ["b", "c", "a"]
        )
    }

    /// "the result always contains exactly the input slugs" — nothing is lost,
    /// duplicated, or invented by the simulation.
    func testReorderPredictionPreservesTheSlugSet() {
        let slugs = ["alpha", "beta", "gamma", "delta"]
        for dragged in slugs {
            for drop in 0..<slugs.count {
                guard let out = ReorderPrediction.predict(slugs: slugs, dragged: dragged, dropIndex: drop) else {
                    return XCTFail("predict(\(dragged), \(drop)) returned nil for a member slug")
                }
                XCTAssertEqual(out.sorted(), slugs.sorted(), "predict(\(dragged), \(drop))")
                XCTAssertEqual(out.count, slugs.count)
                XCTAssertEqual(out.firstIndex(of: dragged), drop, "predict(\(dragged), \(drop)) landing index")
            }
        }
    }

    /// The settle protocol: agree → release, first disagreement tolerated,
    /// second consecutive disagreement releases.
    func testReorderOverrideReleasesOnAnAgreeingPayload() {
        var o = ReorderOverride(column: .working, slugs: ["a", "b", "c"])
        XCTAssertFalse(o.reconcile(serverOrder: ["a", "b", "c"]))
    }

    func testReorderOverrideToleratesExactlyOneDisagreement() {
        var o = ReorderOverride(column: .working, slugs: ["a", "b", "c"])
        XCTAssertTrue(o.reconcile(serverOrder: ["b", "a", "c"]), "first disagreement is tolerated")
        XCTAssertFalse(o.reconcile(serverOrder: ["b", "a", "c"]), "second consecutive disagreement releases")
    }

    func testReorderOverrideAgreementAfterOneDisagreementReleases() {
        var o = ReorderOverride(column: .working, slugs: ["a", "b", "c"])
        XCTAssertTrue(o.reconcile(serverOrder: ["c", "b", "a"]))
        XCTAssertFalse(o.reconcile(serverOrder: ["a", "b", "c"]))
    }

    func testLoadChipIsBlockedOnly() {
        for column in BoardColumn.allCases {
            XCTAssertEqual(
                ChipRules.loadChipVisible(hasLoad: true, column: column),
                column == .blocked,
                "hasLoad=true, column=\(column.rawValue)"
            )
            XCTAssertFalse(
                ChipRules.loadChipVisible(hasLoad: false, column: column),
                "hasLoad=false, column=\(column.rawValue)"
            )
        }
    }

    func testStatusToken() {
        XCTAssertEqual(ChipRules.statusToken(column: .queued), "◌ queued")
        XCTAssertEqual(ChipRules.statusToken(column: .done), "✓ done")
        for column in BoardColumn.allCases where column != .queued && column != .done {
            XCTAssertNil(ChipRules.statusToken(column: column), column.rawValue)
        }
    }

    /// absent ≠ zero: nil hides the chip, and so does an all-zero row with no
    /// sessions summed; anything else shows it.
    func testTokensVisibility() throws {
        XCTAssertFalse(ChipRules.tokensVisible(tokens: nil))

        let allZero = try contractDecodeTokens(
            #"{"in":0,"out":0,"cache_write":0,"cache_read":0,"sessions":0,"updated_at":0}"#
        )
        XCTAssertFalse(ChipRules.tokensVisible(tokens: allZero))

        let spent = try contractDecodeTokens(
            #"{"in":120,"out":40,"cache_write":0,"cache_read":0,"sessions":0,"updated_at":0}"#
        )
        XCTAssertTrue(ChipRules.tokensVisible(tokens: spent))

        let sessionsOnly = try contractDecodeTokens(
            #"{"in":0,"out":0,"cache_write":0,"cache_read":0,"sessions":3,"updated_at":0}"#
        )
        XCTAssertTrue(ChipRules.tokensVisible(tokens: sessionsOnly))

        let cacheOnly = try contractDecodeTokens(
            #"{"in":0,"out":0,"cache_write":0,"cache_read":900,"sessions":0,"updated_at":0}"#
        )
        XCTAssertTrue(ChipRules.tokensVisible(tokens: cacheOnly))
    }

    func testJobKey() {
        XCTAssertEqual(JobKey.key(slug: "foo", archived: false), "foo")
        XCTAssertEqual(JobKey.key(slug: "foo", archived: true), "archive:foo")
        // an archived job renamed by the numbering still keys off its own slug
        XCTAssertEqual(JobKey.key(slug: "foo-2", archived: true), "archive:foo-2")
        XCTAssertNotEqual(
            JobKey.key(slug: "foo", archived: true),
            JobKey.key(slug: "foo", archived: false)
        )
    }
}

/// Wire fixtures are decoded through the module's own model, never built from
/// the implementation's internals.
private func contractDecodeTokens(_ json: String) throws -> WebTokens {
    try JSONDecoder().decode(WebTokens.self, from: Data(json.utf8))
}

// MARK: - S4: overlays, keyboard, composer

final class HudContractOverlayTests: XCTestCase {

    func testOverlayKindRawValuesAreTheEscPriorities() {
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

    func testEmptyStackHasNoTopAndSwallowsNothing() {
        let s = OverlayStack()
        XCTAssertNil(s.top)
        XCTAssertNil(s.handleEscape())
        XCTAssertNil(s.top)
    }

    func testTopIsTheMaxOpenKind() {
        let s = OverlayStack()
        s.open(.editor)
        XCTAssertEqual(s.top, .editor)
        s.open(.usage)
        XCTAssertEqual(s.top, .editor, "usage(10) is below editor(20)")
        s.open(.artifact)
        XCTAssertEqual(s.top, .artifact)
    }

    /// Esc closes exactly the topmost, one press at a time, all the way down.
    func testEscapeClosesExactlyTheTopmost() {
        let s = OverlayStack()
        s.open(.usage)
        s.open(.editor)
        s.open(.steer)
        s.open(.thread)
        s.open(.artifact)

        XCTAssertEqual(s.handleEscape(), .artifact)
        XCTAssertEqual(s.top, .thread)
        XCTAssertEqual(s.handleEscape(), .thread)
        XCTAssertEqual(s.top, .steer)
        XCTAssertEqual(s.handleEscape(), .steer)
        XCTAssertEqual(s.top, .editor)
        XCTAssertEqual(s.handleEscape(), .editor)
        XCTAssertEqual(s.top, .usage)
        XCTAssertEqual(s.handleEscape(), .usage)
        XCTAssertNil(s.top)
        XCTAssertNil(s.handleEscape())
    }

    /// "opening artifact from thread REPLACES nothing implicitly — each kind
    /// tracks open/closed independently".
    func testKindsTrackIndependently() {
        let s = OverlayStack()
        s.open(.thread)
        s.open(.artifact)
        s.close(.thread)
        XCTAssertEqual(s.top, .artifact)
        XCTAssertEqual(s.handleEscape(), .artifact)
        XCTAssertNil(s.top, "thread was closed directly and must not reappear")
    }

    func testClosingAKindThatIsNotOpenIsInert() {
        let s = OverlayStack()
        s.open(.steer)
        s.close(.artifact)
        XCTAssertEqual(s.top, .steer)
        s.close(.steer)
        XCTAssertNil(s.top)
        s.close(.steer)
        XCTAssertNil(s.top)
    }

    func testOpeningTwiceIsIdempotent() {
        let s = OverlayStack()
        s.open(.steer)
        s.open(.steer)
        XCTAssertEqual(s.handleEscape(), .steer)
        XCTAssertNil(s.top, "a second open must not need a second Esc")
    }
}

final class HudContractCmdHighlighterTests: XCTestCase {

    private func contractSkills(_ text: String) -> [String] {
        CmdHighlighter.ranges(in: text).map(\.skill)
    }

    private func contractSlices(_ text: String) -> [String] {
        CmdHighlighter.ranges(in: text).map { String(text[$0.range]) }
    }

    func testFindsAllSixTokens() {
        let text = "/info /aim /do /pr /dream /graph"
        XCTAssertEqual(contractSkills(text), ["info", "aim", "do", "pr", "dream", "graph"])
        XCTAssertEqual(contractSlices(text), ["/info", "/aim", "/do", "/pr", "/dream", "/graph"])
    }

    func testStartOfTextIsABoundary() {
        XCTAssertEqual(contractSkills("/do this"), ["do"])
        XCTAssertEqual(contractSlices("/do this"), ["/do"])
    }

    /// The contract's own example.
    func testMatchesAfterWhitespaceMidSentence() {
        let text = "ledger accepted — /do"
        XCTAssertEqual(contractSkills(text), ["do"])
        XCTAssertEqual(contractSlices(text), ["/do"])
    }

    func testTrailingPunctuationEndsTheToken() {
        XCTAssertEqual(contractSkills("run /aim."), ["aim"])
        XCTAssertEqual(contractSlices("run /aim."), ["/aim"])
        XCTAssertEqual(contractSkills("run /pr, then wait"), ["pr"])
    }

    /// "`/door` matches nothing" — the trailing boundary is real.
    func testTrailingWordCharactersKillTheMatch() {
        XCTAssertEqual(contractSkills("/door"), [])
        XCTAssertEqual(contractSkills("/prod"), [])
        XCTAssertEqual(contractSkills("/informational"), [])
        XCTAssertEqual(contractSkills("/graphs"), [])
    }

    /// The leading boundary is real too: a mid-word slash run is not a command.
    func testMidWordSlashRunsAreNotCommands() {
        XCTAssertEqual(contractSkills("web/static/do"), [])
        XCTAssertEqual(contractSkills("a/pr"), [])
    }

    /// `/dream` must not be read as `/do` plus tail, nor mis-skilled.
    func testLongerTokensWinOverTheirShorterNeighbours() {
        XCTAssertEqual(contractSkills("/dream"), ["dream"])
        XCTAssertEqual(contractSlices("/dream"), ["/dream"])
    }

    func testEmptyAndPlainTextFindNothing() {
        XCTAssertEqual(contractSkills(""), [])
        XCTAssertEqual(contractSkills("no commands here at all"), [])
        XCTAssertEqual(contractSkills("/nope"), [])
    }

    func testEveryOccurrenceIsFoundNotJustTheFirst() {
        let text = "/do one thing then /do another"
        XCTAssertEqual(contractSkills(text), ["do", "do"])
    }
}

/// Thread-safe recorder for the debounce callbacks, which may land off the
/// calling thread.
private final class ContractSaveLog: @unchecked Sendable {
    private let lock = NSLock()
    private var items: [String] = []

    func add(_ s: String) {
        lock.lock()
        items.append(s)
        lock.unlock()
    }

    var all: [String] {
        lock.lock()
        defer { lock.unlock() }
        return items
    }
}

private extension XCTestCase {
    /// Burn a bounded interval without a wall-clock sleep, so a "nothing fires"
    /// obligation can be asserted after the debounce window has passed.
    func contractSettle(_ seconds: TimeInterval = 0.4) {
        let idle = XCTestExpectation(description: "contract settle")
        idle.isInverted = true
        wait(for: [idle], timeout: seconds)
    }
}

final class HudContractComposerDraftTests: XCTestCase {

    private let contractDebounce: TimeInterval = 0.05

    func testTypedSchedulesTheSaveAfterTheDebounce() {
        let log = ContractSaveLog()
        let fired = expectation(description: "save fired")
        let d = ComposerDraft(debounce: contractDebounce) { text in
            log.add(text)
            fired.fulfill()
        }
        d.typed("hello")
        XCTAssertEqual(log.all, [], "the save must not fire synchronously")
        wait(for: [fired], timeout: 2.0)
        XCTAssertEqual(log.all, ["hello"])
    }

    func testASecondTypedSupersedesThePendingSave() {
        let log = ContractSaveLog()
        let fired = expectation(description: "save fired")
        fired.assertForOverFulfill = true
        let d = ComposerDraft(debounce: contractDebounce) { text in
            log.add(text)
            fired.fulfill()
        }
        d.typed("h")
        d.typed("he")
        d.typed("hel")
        wait(for: [fired], timeout: 2.0)
        contractSettle()
        XCTAssertEqual(log.all, ["hel"], "only the latest text may be saved, exactly once")
    }

    /// "send cancels BEFORE and AFTER the post" — each half on its own.
    func testWillSendCancelsThePendingSave() {
        let log = ContractSaveLog()
        let never = expectation(description: "no save")
        never.isInverted = true
        let d = ComposerDraft(debounce: contractDebounce) { text in
            log.add(text)
            never.fulfill()
        }
        d.typed("sent text")
        d.willSend()
        wait(for: [never], timeout: 0.4)
        XCTAssertEqual(log.all, [])
    }

    func testDidSendCancelsThePendingSave() {
        let log = ContractSaveLog()
        let never = expectation(description: "no save")
        never.isInverted = true
        let d = ComposerDraft(debounce: contractDebounce) { text in
            log.add(text)
            never.fulfill()
        }
        // a keystroke landing DURING the in-flight send is what didSend exists
        // to catch — the first cancel never saw it
        d.willSend()
        d.typed("typed mid-send")
        d.didSend()
        wait(for: [never], timeout: 0.4)
        XCTAssertEqual(log.all, [])
    }

    func testJobDoneCancelsThePendingSave() {
        let log = ContractSaveLog()
        let never = expectation(description: "no save")
        never.isInverted = true
        let d = ComposerDraft(debounce: contractDebounce) { text in
            log.add(text)
            never.fulfill()
        }
        d.typed("half typed")
        d.jobDone()
        wait(for: [never], timeout: 0.4)
        XCTAssertEqual(log.all, [])
    }

    /// "kill does not clear the server draft" — the pending save FLUSHES, and
    /// it flushes immediately rather than on the timer it was waiting for.
    func testJobKilledFlushesThePendingSaveImmediately() {
        let log = ContractSaveLog()
        let d = ComposerDraft(debounce: contractDebounce) { log.add($0) }
        d.typed("half typed")
        d.jobKilled()
        XCTAssertEqual(log.all, ["half typed"], "kill must flush synchronously")
        contractSettle()
        XCTAssertEqual(log.all, ["half typed"], "the flushed save must not fire a second time")
    }

    func testJobKilledWithNothingPendingSavesNothing() {
        let log = ContractSaveLog()
        let d = ComposerDraft(debounce: contractDebounce) { log.add($0) }
        d.jobKilled()
        XCTAssertEqual(log.all, [])
        contractSettle()
        XCTAssertEqual(log.all, [])
    }

    /// "after a cancel, no save fires without a new `typed`".
    func testACancelledDraftStaysCancelledUntilTheNextTyped() {
        let log = ContractSaveLog()
        let fired = expectation(description: "the post-cancel save")
        let d = ComposerDraft(debounce: contractDebounce) { text in
            log.add(text)
            fired.fulfill()
        }
        d.typed("dropped")
        d.willSend()
        contractSettle()
        XCTAssertEqual(log.all, [], "the cancelled save must not resurrect on its own")

        d.typed("kept")
        wait(for: [fired], timeout: 2.0)
        XCTAssertEqual(log.all, ["kept"])
    }
}

final class HudContractComposerHelpersTests: XCTestCase {

    /// Options are posted as their 1-BASED index, as a string.
    func testQuestionAnswerPayloadIsTheOneBasedIndex() {
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 0), "1")
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 1), "2")
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 4), "5")
        XCTAssertEqual(QuestionAnswer.payload(optionIndex: 9), "10")
    }

    /// "anything already typed wins" — a reopened composer only lands the
    /// server draft into an empty box.
    func testDraftSeedOnlyLandsIntoAnEmptyComposer() {
        XCTAssertEqual(DraftSeed.accept(serverDraft: "from server", typedSoFar: ""), "from server")
        XCTAssertEqual(DraftSeed.accept(serverDraft: "from server", typedSoFar: "mine"), "mine")
        XCTAssertEqual(DraftSeed.accept(serverDraft: "", typedSoFar: ""), "")
        XCTAssertEqual(DraftSeed.accept(serverDraft: "", typedSoFar: "mine"), "mine")
        // a single space is something typed, not an empty box
        XCTAssertEqual(DraftSeed.accept(serverDraft: "from server", typedSoFar: " "), " ")
    }
}

// MARK: - S5: artifact viewer + threads

final class HudContractArtifactTests: XCTestCase {

    func testKindByExtension() {
        for ext in ["png", "jpg", "jpeg", "gif", "webp"] {
            XCTAssertEqual(ArtifactKind.kind(forExtension: ext), .image, ext)
        }
        for ext in ["html", "svg", "pdf"] {
            XCTAssertEqual(ArtifactKind.kind(forExtension: ext), .web, ext)
        }
        for ext in ["md", "txt", "log", "", "json", "yaml", "go", "swift"] {
            XCTAssertEqual(ArtifactKind.kind(forExtension: ext), .text, "\"\(ext)\"")
        }
    }

    func testKindIsCaseInsensitive() {
        XCTAssertEqual(ArtifactKind.kind(forExtension: "PNG"), .image)
        XCTAssertEqual(ArtifactKind.kind(forExtension: "Jpeg"), .image)
        XCTAssertEqual(ArtifactKind.kind(forExtension: "HTML"), .web)
        XCTAssertEqual(ArtifactKind.kind(forExtension: "Svg"), .web)
        XCTAssertEqual(ArtifactKind.kind(forExtension: "PDF"), .web)
        XCTAssertEqual(ArtifactKind.kind(forExtension: "MD"), .text)
    }

    /// "index is found by URL equality, never carried position".
    func testSequenceIndexIsFoundByURL() {
        let urls = ["/api/artifact/j/cell/a.md", "/api/artifact/j/cell/b.md", "/api/artifact/j/cell/c.md"]
        XCTAssertEqual(ArtifactSequence.index(of: urls[0], in: urls), 0)
        XCTAssertEqual(ArtifactSequence.index(of: urls[2], in: urls), 2)
        XCTAssertNil(ArtifactSequence.index(of: "/api/artifact/j/cell/z.md", in: urls))
        XCTAssertNil(ArtifactSequence.index(of: urls[0], in: []))
    }

    /// "`step` clamps to `0...count-1` and NEVER wraps".
    func testStepClampsAndNeverWraps() {
        XCTAssertEqual(ArtifactSequence.step(from: 0, by: 1, count: 3), 1)
        XCTAssertEqual(ArtifactSequence.step(from: 1, by: 1, count: 3), 2)
        XCTAssertEqual(ArtifactSequence.step(from: 2, by: 1, count: 3), 2, "must not wrap to 0")
        XCTAssertEqual(ArtifactSequence.step(from: 0, by: -1, count: 3), 0, "must not wrap to the end")
        XCTAssertEqual(ArtifactSequence.step(from: 2, by: -1, count: 3), 1)
        XCTAssertEqual(ArtifactSequence.step(from: 0, by: 5, count: 3), 2)
        XCTAssertEqual(ArtifactSequence.step(from: 2, by: -9, count: 3), 0)
        XCTAssertEqual(ArtifactSequence.step(from: 0, by: 0, count: 1), 0)
    }

    func testDraftStashRoundTripsPerURL() {
        let stash = ArtifactDraftStash()
        stash.bank(url: "u1", mode: "feedback", text: "half typed")
        stash.bank(url: "u2", mode: "ask", text: "why two passes?")

        let one = stash.restore(url: "u1")
        XCTAssertEqual(one?.mode, "feedback")
        XCTAssertEqual(one?.text, "half typed")

        let two = stash.restore(url: "u2")
        XCTAssertEqual(two?.mode, "ask")
        XCTAssertEqual(two?.text, "why two passes?")
    }

    func testDraftStashRestoreOfAnUnbankedURLIsNil() {
        let stash = ArtifactDraftStash()
        XCTAssertNil(stash.restore(url: "never-banked"))
        stash.bank(url: "u1", mode: "feedback", text: "x")
        XCTAssertNil(stash.restore(url: "u2"))
    }

    func testDraftStashClearDropsIt() {
        let stash = ArtifactDraftStash()
        stash.bank(url: "u1", mode: "feedback", text: "x")
        stash.clear(url: "u1")
        XCTAssertNil(stash.restore(url: "u1"))
    }

    /// "banking empty text clears instead of storing" — otherwise a cleared
    /// composer would resurrect as an empty banked draft.
    func testBankingEmptyTextClears() {
        let stash = ArtifactDraftStash()
        stash.bank(url: "u1", mode: "feedback", text: "x")
        stash.bank(url: "u1", mode: "feedback", text: "")
        XCTAssertNil(stash.restore(url: "u1"))
    }

    func testAskThreadTitleNamesTheArtifactAndTheQuestion() {
        XCTAssertEqual(
            AskThread.title(artifactName: "10-flow.md", question: "why two passes?"),
            "10-flow.md — why two passes?"
        )
    }

    func testAskThreadTitleTakesTheBasename() {
        XCTAssertEqual(
            AskThread.title(artifactName: "docs/diagrams/13-tree.html", question: "what feeds this?"),
            "13-tree.html — what feeds this?"
        )
    }

    func testAskThreadTitleTakesTheQuestionsFirstLineOnly() {
        XCTAssertEqual(
            AskThread.title(artifactName: "a.md", question: "why?\nand also how?"),
            "a.md — why?"
        )
    }

    func testAskThreadTitleCapsAt80() {
        let t = AskThread.title(artifactName: "a.md", question: String(repeating: "x", count: 200))
        XCTAssertLessThanOrEqual(t.count, 80)
    }

    /// The artifact half is BUDGETED at 40 so a long section-doc name cannot
    /// eat the whole cap and truncate away the distinguishing question.
    func testAskThreadTitleKeepsTheQuestionUnderALongArtifactName() {
        let long = "50-the-very-long-section-document-that-explains-the-token-rule.md"
        let t = AskThread.title(artifactName: long, question: "why two passes?")
        XCTAssertLessThanOrEqual(t.count, 80)
        XCTAssertTrue(t.contains("why two passes?"), "the question was truncated away: \(t)")
    }

    /// Cut surrogate-safely: no replacement glyph, no half emoji.
    func testAskThreadTitleNeverSplitsAGrapheme() {
        for base in ["a.md", "ab.md", "abc.md", "abcd.md", "abcde.md"] {
            let t = AskThread.title(artifactName: base, question: String(repeating: "🎉", count: 60))
            XCTAssertLessThanOrEqual(t.count, 80, base)
            XCTAssertFalse(t.unicodeScalars.contains("\u{FFFD}"), "\(base) produced a replacement glyph")
            for scalar in t.unicodeScalars {
                XCTAssertFalse(
                    (0xD800...0xDFFF).contains(Int(scalar.value)),
                    "\(base) produced a lone surrogate"
                )
            }
        }
    }

    func testAskThreadSeedNamesTheArtifactCarriesTheQuestionAndForbidsEditing() {
        let seed = AskThread.seed(artifactName: "10-flow.md", question: "why two passes?")
        XCTAssertTrue(seed.contains("10-flow.md"), "seed does not name the artifact: \(seed)")
        XCTAssertTrue(seed.contains("why two passes?"), "seed does not carry the question: \(seed)")
        XCTAssertTrue(
            seed.lowercased().contains("edit"),
            "seed does not instruct the thread not to edit the artifact: \(seed)"
        )
    }
}

// MARK: - S6: screens, chrome, notifications

final class HudContractNotificationTests: XCTestCase {

    private func contractSnap(_ slug: String, _ column: BoardColumn, erroring: Bool = false) -> JobSnapshot {
        JobSnapshot(slug: slug, column: column, erroring: erroring)
    }

    func testEnteringBlockedFires() {
        let out = NotificationDiff.transitions(
            prevBlocked: [],
            prevWorking: [],
            prevErroring: [],
            board: [contractSnap("a", .blocked)]
        )
        XCTAssertEqual(out, [.blocked("a")])
    }

    func testEnteringWorkingFires() {
        let out = NotificationDiff.transitions(
            prevBlocked: [],
            prevWorking: [],
            prevErroring: [],
            board: [contractSnap("b", .working)]
        )
        XCTAssertEqual(out, [.started("b")])
    }

    func testEnteringErroringFires() {
        let out = NotificationDiff.transitions(
            prevBlocked: [],
            prevWorking: [],
            prevErroring: [],
            board: [contractSnap("c", .draft, erroring: true)]
        )
        XCTAssertEqual(out, [.erroring("c")])
    }

    /// "one transition per entry" — a job entering two sets at once fires both.
    func testAJobEnteringTwoSetsFiresTwice() {
        let out = NotificationDiff.transitions(
            prevBlocked: [],
            prevWorking: [],
            prevErroring: [],
            board: [contractSnap("a", .blocked, erroring: true)]
        )
        XCTAssertEqual(out.count, 2, "got \(out)")
        XCTAssertTrue(out.contains(.blocked("a")), "got \(out)")
        XCTAssertTrue(out.contains(.erroring("a")), "got \(out)")
    }

    func testSteadyStateFiresNothing() {
        XCTAssertEqual(
            NotificationDiff.transitions(
                prevBlocked: ["a"],
                prevWorking: ["b"],
                prevErroring: ["c"],
                board: [
                    contractSnap("a", .blocked),
                    contractSnap("b", .working),
                    contractSnap("c", .draft, erroring: true),
                ]
            ),
            []
        )
    }

    func testUnchangedSetsGiveAnEmptyArray() {
        XCTAssertEqual(
            NotificationDiff.transitions(prevBlocked: [], prevWorking: [], prevErroring: [], board: []),
            []
        )
        XCTAssertEqual(
            NotificationDiff.transitions(
                prevBlocked: ["gone"],
                prevWorking: ["also-gone"],
                prevErroring: [],
                board: []
            ),
            [],
            "a job leaving the board fires nothing"
        )
    }

    /// "a job leaving and re-entering across two calls fires again".
    func testLeavingAndReEnteringFiresAgain() {
        let first = NotificationDiff.transitions(
            prevBlocked: ["a"],
            prevWorking: [],
            prevErroring: [],
            board: [contractSnap("a", .working)]
        )
        XCTAssertEqual(first, [.started("a")])

        let second = NotificationDiff.transitions(
            prevBlocked: [],
            prevWorking: ["a"],
            prevErroring: [],
            board: [contractSnap("a", .blocked)]
        )
        XCTAssertEqual(second, [.blocked("a")], "re-entering blocked must fire a second time")
    }

    /// Only the NEWLY entering jobs fire, never the ones that were already in.
    func testOnlyTheNewEntrantsFire() {
        let out = NotificationDiff.transitions(
            prevBlocked: ["old"],
            prevWorking: [],
            prevErroring: [],
            board: [contractSnap("old", .blocked), contractSnap("new", .blocked)]
        )
        XCTAssertEqual(out, [.blocked("new")])
    }

    /// The persistence shape has to survive a relaunch unchanged.
    func testNotifiedSetsCodableRoundTripIsIdentity() throws {
        let sets = NotifiedSets(
            blocked: ["a", "b"],
            working: ["c"],
            erroring: []
        )
        let data = try JSONEncoder().encode(sets)
        let back = try JSONDecoder().decode(NotifiedSets.self, from: data)
        XCTAssertEqual(back, sets)

        let empty = NotifiedSets(blocked: [], working: [], erroring: [])
        let backEmpty = try JSONDecoder().decode(
            NotifiedSets.self,
            from: try JSONEncoder().encode(empty)
        )
        XCTAssertEqual(backEmpty, empty)
    }
}

final class HudContractChromeTests: XCTestCase {

    func testUsageBandThresholds() {
        XCTAssertEqual(UsageBand.band(0), .ok)
        XCTAssertEqual(UsageBand.band(49), .ok)
        XCTAssertEqual(UsageBand.band(50), .warn)
        XCTAssertEqual(UsageBand.band(79), .warn)
        XCTAssertEqual(UsageBand.band(80), .danger)
        XCTAssertEqual(UsageBand.band(100), .danger)
        XCTAssertEqual(UsageBand.band(1000), .danger)
    }

    func testFlagCountIsThePlainSum() {
        XCTAssertEqual(FlagCount.count(pendingQuestions: 0, unhealthyCells: 0, openP0DocQuestions: 0), 0)
        XCTAssertEqual(FlagCount.count(pendingQuestions: 2, unhealthyCells: 3, openP0DocQuestions: 4), 9)
        XCTAssertEqual(FlagCount.count(pendingQuestions: 1, unhealthyCells: 0, openP0DocQuestions: 0), 1)
        XCTAssertEqual(FlagCount.count(pendingQuestions: 0, unhealthyCells: 0, openP0DocQuestions: 7), 7)
    }

    /// "count alone freezes a sliding fixed-size window" — the last entry is
    /// half of the comparison.
    func testLogPagingChanged() {
        XCTAssertFalse(LogPaging.changed(prevCount: 3, prevLast: "a", nextCount: 3, nextLast: "a"))
        XCTAssertTrue(LogPaging.changed(prevCount: 3, prevLast: "a", nextCount: 4, nextLast: "a"))
        XCTAssertTrue(LogPaging.changed(prevCount: 3, prevLast: "a", nextCount: 3, nextLast: "b"),
                      "a sliding fixed-size window changes its last entry, not its count")
        XCTAssertFalse(LogPaging.changed(prevCount: 0, prevLast: nil, nextCount: 0, nextLast: nil))
        XCTAssertTrue(LogPaging.changed(prevCount: 0, prevLast: nil, nextCount: 1, nextLast: "a"))
        XCTAssertTrue(LogPaging.changed(prevCount: 3, prevLast: "a", nextCount: 3, nextLast: nil))
        XCTAssertTrue(LogPaging.changed(prevCount: 3, prevLast: nil, nextCount: 3, nextLast: "a"))
    }
}

final class HudContractDreamTests: XCTestCase {

    private let contractReport = """
    # Dream report 2026-08-06

    ## Shipped
    - hardened the token scan
    - fixed the palette pin

    ## Judgment calls
    None

    ### Watching
    - the reorder settle protocol

    ## Prompting feedback
    - one note
    - two note
    - three note
    """

    func testSectionCountCountsBulletsUnderTheHeading() {
        XCTAssertEqual(DreamText.sectionCount(markdown: contractReport, heading: "Shipped"), 2)
        XCTAssertEqual(DreamText.sectionCount(markdown: contractReport, heading: "Prompting feedback"), 3)
    }

    /// "any level" — the report's heading LEVEL is not fixed by the skill.
    func testSectionCountIsHeadingLevelAgnostic() {
        XCTAssertEqual(DreamText.sectionCount(markdown: contractReport, heading: "Watching"), 1)
    }

    /// A section that spells out "None" carries no bullets.
    func testSectionCountOfAQuietSectionIsZero() {
        XCTAssertEqual(DreamText.sectionCount(markdown: contractReport, heading: "Judgment calls"), 0)
    }

    func testSectionCountOfAnAbsentHeadingIsZero() {
        XCTAssertEqual(DreamText.sectionCount(markdown: contractReport, heading: "Nowhere"), 0)
        XCTAssertEqual(DreamText.sectionCount(markdown: "", heading: "Shipped"), 0)
    }

    /// Stops at the next heading of any level, and takes the FIRST matching
    /// heading rather than the last.
    func testSectionCountStopsAtTheNextHeadingAndTakesTheFirstMatch() {
        let md = """
        ## Shipped
        - one
        - two
        ### Detail
        - not counted
        ## Shipped
        - also not counted
        """
        XCTAssertEqual(DreamText.sectionCount(markdown: md, heading: "Shipped"), 2)
    }
}

final class HudContractWatchSortTests: XCTestCase {

    func testOpenRowsSortAboveFineRowsThenLastSeenDescThenIdDesc() {
        let rows = [
            WatchSortRow(id: 1, verdict: nil, lastSeen: Date(timeIntervalSince1970: 100)),
            WatchSortRow(id: 2, verdict: "fix", lastSeen: Date(timeIntervalSince1970: 300)),
            WatchSortRow(id: 3, verdict: "fine", lastSeen: Date(timeIntervalSince1970: 500)),
            WatchSortRow(id: 4, verdict: nil, lastSeen: Date(timeIntervalSince1970: 300)),
        ]
        XCTAssertEqual(WatchSort.sort(rows).map(\.id), [4, 2, 1, 3])
    }

    /// `fix` is still OPEN — it is work the loop has not finished with.
    func testFixCountsAsOpen() {
        let rows = [
            WatchSortRow(id: 1, verdict: "fine", lastSeen: Date(timeIntervalSince1970: 900)),
            WatchSortRow(id: 2, verdict: "fix", lastSeen: Date(timeIntervalSince1970: 100)),
        ]
        XCTAssertEqual(WatchSort.sort(rows).map(\.id), [2, 1])
    }

    func testIdBreaksALastSeenTieDescending() {
        let t = Date(timeIntervalSince1970: 42)
        let rows = [
            WatchSortRow(id: 7, verdict: nil, lastSeen: t),
            WatchSortRow(id: 9, verdict: nil, lastSeen: t),
            WatchSortRow(id: 8, verdict: nil, lastSeen: t),
        ]
        XCTAssertEqual(WatchSort.sort(rows).map(\.id), [9, 8, 7])
    }

    func testSortOfAnEmptyListIsEmpty() {
        XCTAssertEqual(WatchSort.sort([]).map(\.id), [])
    }
}

// MARK: - S1: shell + routing

final class HudContractRoutingTests: XCTestCase {

    /// The SAME-MACHINE default, deliberately not the iOS tailnet one.
    private let contractDefaultBase = "http://127.0.0.1:7777"

    func testBaseURLFallsBackWhenThereIsNoUsableStoredValue() {
        XCTAssertEqual(ServerConfig.baseURL(stored: nil).absoluteString, contractDefaultBase)
        XCTAssertEqual(ServerConfig.baseURL(stored: "").absoluteString, contractDefaultBase)
        XCTAssertEqual(ServerConfig.baseURL(stored: "   ").absoluteString, contractDefaultBase)
        XCTAssertEqual(ServerConfig.baseURL(stored: "not a url").absoluteString, contractDefaultBase)
        XCTAssertEqual(ServerConfig.baseURL(stored: "127.0.0.1:7777").absoluteString, contractDefaultBase,
                       "a scheme-less value is malformed for this purpose")
    }

    func testAValidStoredURLWins() {
        XCTAssertEqual(
            ServerConfig.baseURL(stored: "http://10.0.0.4:8080").absoluteString,
            "http://10.0.0.4:8080"
        )
        XCTAssertEqual(
            ServerConfig.baseURL(stored: "https://kraken.example.com").absoluteString,
            "https://kraken.example.com"
        )
    }

    private func contractParse(_ s: String) -> MacRoute? {
        guard let url = URL(string: s) else {
            XCTFail("fixture is not a URL: \(s)")
            return nil
        }
        return MacRoute.parse(url)
    }

    func testBoardRoutes() {
        XCTAssertEqual(contractParse("kraken://board"), .board(column: nil))
        XCTAssertEqual(contractParse("kraken://board/blocked"), .board(column: .blocked))
        XCTAssertEqual(contractParse("kraken://board/draft"), .board(column: .draft))
        XCTAssertEqual(contractParse("kraken://board/queued"), .board(column: .queued))
        XCTAssertEqual(contractParse("kraken://board/working"), .board(column: .working))
        XCTAssertEqual(contractParse("kraken://board/done"), .board(column: .done))
        XCTAssertEqual(contractParse("kraken://board/nonsense"), .board(column: nil),
                       "an invalid column degrades to the whole board, never to nil")
    }

    func testJobRoute() {
        XCTAssertEqual(contractParse("kraken://job/macos-native-kraken"), .job(slug: "macos-native-kraken"))
        XCTAssertEqual(contractParse("kraken://job/foo-2"), .job(slug: "foo-2"))
    }

    func testStandaloneScreenRoutes() {
        XCTAssertEqual(contractParse("kraken://graph"), .graph)
        XCTAssertEqual(contractParse("kraken://log"), .log)
        XCTAssertEqual(contractParse("kraken://dream"), .dream)
        XCTAssertEqual(contractParse("kraken://settings"), .settings)
    }

    func testUnknownHostsAreNil() {
        XCTAssertNil(contractParse("kraken://nope"))
        XCTAssertNil(contractParse("kraken://usage"))
        XCTAssertNil(contractParse("https://example.com/board"))
    }
}
