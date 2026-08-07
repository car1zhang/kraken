import SwiftUI
import UIKit

// KTheme is kraken's iOS 26 "tinted glass" design system: one user-selected
// HUE drives the background field, the glass tint and the accent, while
// MEANING colors — the six state hues, the eight job colors, the six skill
// hues — stay fixed so a re-tint never changes what a color says.
//
// The previous incarnation of this file ported kraken web's "socrat
// cyber-dark" stylesheet (sharp corners, hand-ported hex surfaces, UIKit
// appearance proxies). That port is gone: surfaces are now translucent
// material/glass over a tinted gradient field, corners are continuous, and
// chrome is whatever iOS 26 paints natively — the absence of a global
// appearance-configuring entry point here is deliberate and load-bearing (an
// opaque scroll-edge nav-bar appearance hides large titles on iOS 26; do not
// reintroduce one, per-view or globally).
//
// Every token below is still a dynamic light/dark UIColor plus the SwiftUI
// Color derived from it, so the whole app re-resolves on a scheme flip with
// no call-site changes. The tint-derived ones additionally read the stored
// tint at resolve time, so a tint change plus a view invalidation re-tints
// everything.

extension Color {
    init(hex: UInt32) {
        self.init(
            red: Double((hex >> 16) & 0xFF) / 255,
            green: Double((hex >> 8) & 0xFF) / 255,
            blue: Double(hex & 0xFF) / 255
        )
    }
}

extension UIColor {
    convenience init(hex: UInt32) {
        self.init(
            red: CGFloat((hex >> 16) & 0xFF) / 255,
            green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255,
            alpha: 1
        )
    }
}

// MARK: - the tint

/// The eight curated theme tints. Violet is kraken's own and the default;
/// graphite is the neutral escape hatch (same hue machinery, saturation
/// scaled almost to zero) so "no color" is still one code path rather than a
/// special case.
///
/// The order of the cases IS the order the Settings swatch row renders in,
/// and the rawValues are what land in UserDefaults under
/// `KTheme.themeTintKey` — both are contract.
enum ThemeTint: String, CaseIterable {
    case violet, blue, cyan, green, amber, pink, red, graphite

    /// nil, "", and anything the app does not recognise (an older build
    /// reading a newer build's tint, a hand-edited defaults plist) all fall
    /// back to violet rather than trapping.
    static func resolve(_ raw: String?) -> ThemeTint {
        guard let raw, let tint = ThemeTint(rawValue: raw) else { return .violet }
        return tint
    }

    /// The hue that drives everything, in degrees on the colour wheel.
    /// Graphite carries a hue too (a cool blue-grey) — it just spends almost
    /// none of it, see `saturationScale`.
    var hueDegrees: Double {
        switch self {
        case .violet: return 262
        case .blue: return 215
        case .cyan: return 190
        case .green: return 145
        case .amber: return 40
        case .pink: return 330
        case .red: return 5
        case .graphite: return 220
        }
    }

    /// Multiplied into every saturation this tint produces. 1 for the seven
    /// chromatic tints; 0.10 for graphite, which is what makes it read as
    /// neutral while still resolving to a well-defined hue.
    var saturationScale: Double {
        self == .graphite ? 0.10 : 1.0
    }

    /// The LIGHT-appearance accent brightness, per tint.
    ///
    /// Per-tint rather than one constant because brightness is not what the
    /// eye reads — luminance is, and at a fixed HSB brightness the yellow-
    /// green half of the wheel is far brighter than the blue-violet half. A
    /// single 0.82 (the violet value, which is the one the mockups were
    /// judged on) left graphite at 1.4:1, green at 1.7:1, cyan at 2.1:1 and
    /// amber at 2.2:1 against the light field's brightest stop — unreadable.
    ///
    /// Each value below is the brightness at which that hue clears WCAG
    /// 4.5:1 against THAT STOP — `hue, sat 0.06 × saturationScale, bri 0.99`,
    /// the first entry of `GlassTheme.backgroundStops` — and not against
    /// plain white. The distinction is not academic: the stop carries the
    /// same hue as the accent, so it is dimmer than white by exactly the
    /// amount that matters, and solving against white understates the
    /// required drop. That is what put blue/pink/red at 4.24:1 in the first
    /// pass; re-solved here they are 0.75/0.76/0.76.
    ///
    /// The margins are thin by construction (4.5–4.7:1 for six of the eight)
    /// because every 0.01 of brightness bought back is chroma kept. If you
    /// change a hue in `hueDegrees`, re-solve its brightness here — do not
    /// assume a neighbour's value transfers.
    ///
    /// The DARK curve needs no equivalent: it paints onto a near-black field,
    /// where every hue at brightness 1.0 clears easily.
    var lightBrightness: Double {
        switch self {
        case .violet: return 0.82
        case .blue: return 0.75
        case .cyan: return 0.55
        case .green: return 0.51
        case .amber: return 0.56
        case .pink: return 0.76
        case .red: return 0.76
        case .graphite: return 0.47
        }
    }

    /// Sentence-case name for the Settings row's accessibility label.
    var displayName: String {
        rawValue.prefix(1).uppercased() + rawValue.dropFirst()
    }

    /// This tint's accent, for the Settings swatch row.
    var swatch: Color {
        GlassTheme(tint: self).accent
    }
}

/// The tint-derived token set. A value type: build one from a `ThemeTint`
/// and read tokens off it. Injected at the app root through
/// `EnvironmentValues.theme`, so any view can ask for the CURRENT theme
/// rather than reaching for a global.
struct GlassTheme {
    let tint: ThemeTint

    init(tint: ThemeTint) {
        self.tint = tint
    }

    /// Hue in the 0...1 space UIColor wants.
    var hue: Double { tint.hueDegrees / 360 }
    var saturationScale: Double { tint.saturationScale }

    /// One tint-derived dynamic UIColor. Saturations are pre-scale — the
    /// tint's `saturationScale` is applied here, once, so no caller has to
    /// remember graphite exists.
    func shadeUI(
        darkSaturation: Double, darkBrightness: Double,
        lightSaturation: Double, lightBrightness: Double,
        alpha: Double = 1
    ) -> UIColor {
        let hue = self.hue
        let scale = saturationScale
        return UIColor { trait in
            trait.userInterfaceStyle == .light
                ? UIColor(hue: hue, saturation: lightSaturation * scale, brightness: lightBrightness, alpha: alpha)
                : UIColor(hue: hue, saturation: darkSaturation * scale, brightness: darkBrightness, alpha: alpha)
        }
    }

    func shade(
        darkSaturation: Double, darkBrightness: Double,
        lightSaturation: Double, lightBrightness: Double,
        alpha: Double = 1
    ) -> Color {
        Color(uiColor: shadeUI(
            darkSaturation: darkSaturation, darkBrightness: darkBrightness,
            lightSaturation: lightSaturation, lightBrightness: lightBrightness,
            alpha: alpha
        ))
    }

    /// The interactive/identity colour. Dark keeps the mockups' proven curve
    /// (a light, lightly-saturated wash that survives being laid over glass);
    /// light pushes saturation and drops brightness to `tint.lightBrightness`
    /// — a PER-TINT value, because one constant cannot clear contrast for
    /// both violet and green on a near-white field. See `lightBrightness`.
    var accentUI: UIColor {
        shadeUI(darkSaturation: 0.45, darkBrightness: 1.0, lightSaturation: 0.72, lightBrightness: tint.lightBrightness)
    }
    var accent: Color { Color(uiColor: accentUI) }

    /// The accent as a FILL: same hue and curve, low alpha. Alpha rather
    /// than a separate saturation/brightness pair so the soft variant can
    /// never drift off the accent's hue — and so it inherits the per-tint
    /// light brightness for free.
    var accentSoftUI: UIColor {
        shadeUI(darkSaturation: 0.45, darkBrightness: 1.0, lightSaturation: 0.72, lightBrightness: tint.lightBrightness, alpha: 0.18)
    }
    var accentSoft: Color { Color(uiColor: accentSoftUI) }

    /// What a glass surface is tinted with. Deliberately weaker than
    /// `accentSoft` — glass already carries the backdrop's colour.
    var glassTint: Color { accent.opacity(0.10) }

    /// The three gradient stops of the page field, top-leading to
    /// bottom-trailing. Dark: near-black stops of the tint hue. Light: pale
    /// stops of the same hue.
    var backgroundStops: [Color] {
        [
            shade(darkSaturation: 0.55, darkBrightness: 0.075, lightSaturation: 0.06, lightBrightness: 0.99),
            shade(darkSaturation: 0.62, darkBrightness: 0.170, lightSaturation: 0.12, lightBrightness: 0.94),
            shade(darkSaturation: 0.55, darkBrightness: 0.075, lightSaturation: 0.08, lightBrightness: 0.97),
        ]
    }

    /// The opaque base of the field — what a non-translucent surface (a
    /// WKWebView, a plain `.background`) has to sit on to match.
    var groundUI: UIColor {
        shadeUI(darkSaturation: 0.55, darkBrightness: 0.075, lightSaturation: 0.06, lightBrightness: 0.99)
    }
}

struct ThemeEnvironmentKey: EnvironmentKey {
    static let defaultValue = GlassTheme(tint: .violet)
}

extension EnvironmentValues {
    /// The current tint-derived theme. Injected once at the app root from
    /// the persisted tint; the root also re-identifies on a tint change so
    /// the whole tree re-resolves.
    var theme: GlassTheme {
        get { self[ThemeEnvironmentKey.self] }
        set { self[ThemeEnvironmentKey.self] = newValue }
    }
}

// MARK: - palette

enum KTheme {
    /// The one UserDefaults key the persisted tint lives under. Settings
    /// writes a `ThemeTint` rawValue through it; app start and every
    /// tint-derived token below read it back through `ThemeTint.resolve`.
    static let themeTintKey = "themeTint"

    /// The tint as currently persisted. Read at colour-RESOLVE time (inside
    /// the dynamic providers below), not once at launch, so a tint change
    /// plus a view invalidation is enough to re-tint the app.
    static var storedTint: ThemeTint {
        ThemeTint.resolve(UserDefaults.standard.string(forKey: themeTintKey))
    }

    static var currentTheme: GlassTheme {
        GlassTheme(tint: storedTint)
    }

    /// Builds a dynamic UIColor that resolves to `light` or `dark` hex per
    /// the current trait collection's userInterfaceStyle.
    static func dyn(light: UInt32, dark: UInt32) -> UIColor {
        UIColor { $0.userInterfaceStyle == .light ? UIColor(hex: light) : UIColor(hex: dark) }
    }

    /// Builds a dynamic UIColor from the STORED tint, re-derived on every
    /// resolution.
    static func tinted(_ token: @escaping (GlassTheme) -> UIColor) -> UIColor {
        UIColor { trait in token(currentTheme).resolvedColor(with: trait) }
    }

    // --- the field ---
    // `ground` is the opaque base of the tinted gradient field: what a
    // surface that cannot be translucent (ArtifactView's WKWebView) must sit
    // on. Screens that CAN be translucent use ThemeBackgroundView instead,
    // which paints the full three-stop gradient.
    static let groundUI = tinted { $0.groundUI }
    static let ground = Color(uiColor: groundUI)

    // --- surfaces ---
    // No longer opaque panels: a card/row surface is a translucent white
    // wash that lets the field's colour through, which is what makes the
    // glass idiom read as depth rather than as flat boxes.
    static let surfaceUI = UIColor { $0.userInterfaceStyle == .light ? UIColor(white: 1, alpha: 0.72) : UIColor(white: 1, alpha: 0.08) }
    static let surface = Color(uiColor: surfaceUI)
    static let raisedUI = UIColor { $0.userInterfaceStyle == .light ? UIColor(white: 1, alpha: 0.92) : UIColor(white: 1, alpha: 0.14) }
    static let raised = Color(uiColor: raisedUI)
    // The card/field hairline — a translucent edge, not a drawn 1px rule.
    static let hairlineUI = UIColor { $0.userInterfaceStyle == .light ? UIColor(white: 0, alpha: 0.08) : UIColor(white: 1, alpha: 0.12) }
    static let hairline = Color(uiColor: hairlineUI)
    // hairline2 stays an OPAQUE ink: besides borders it is the fill for the
    // graph canvas's structural node types (file/job) and for real dividers,
    // where a translucent value would render as nothing.
    static let hairline2UI = dyn(light: 0xB9C6D6, dark: 0x2A3A50)
    static let hairline2 = Color(uiColor: hairline2UI)

    // --- ink ---
    // The system label colours rather than hand-ported hexes: on glass the
    // system pair is what stays legible as the backdrop shifts.
    static let textUI = UIColor.label
    static let text = Color(uiColor: textUI)
    static let text2UI = UIColor.secondaryLabel
    static let text2 = Color(uiColor: text2UI)

    // --- accent (tint-derived; see GlassTheme) ---
    static let accentUI = tinted { $0.accentUI }
    static let accent = Color(uiColor: accentUI)
    static let accentInkUI = dyn(light: 0xFFFFFF, dark: 0x0A0E14) // on-accent text
    static let accentInk = Color(uiColor: accentInkUI)
    static let accentSoftUI = tinted { $0.accentSoftUI }
    static let accentSoft = Color(uiColor: accentSoftUI)

    // --- meaning colours: NOT tint-derived, by design ---
    // `queued` is the QUEUED state's hue, and it carries the literals the
    // accent used to have before the accent became tint-derived — so under
    // the default violet tint nothing moved, and under every other tint
    // queued stays violet. It is a token of its own rather than an alias of
    // `accent` because a tint-following queued collapses into a neighbouring
    // state: the pink tint lands 3° from `counter` (blocked), green 3° from
    // `solved` (working), amber 2° from `revealed` and cyan on top of
    // `polling`. A user picking a theme colour must not be able to make two
    // states indistinguishable.
    static let queuedUI = dyn(light: 0x6B3FE0, dark: 0x9E7BFF)
    static let queued = Color(uiColor: queuedUI)
    static let counterUI = dyn(light: 0xC0105F, dark: 0xFF4D9D)
    static let counter = Color(uiColor: counterUI)
    static let counterSoftUI = dyn(light: 0xFBDFEC, dark: 0x3D1428)
    static let counterSoft = Color(uiColor: counterSoftUI)
    static let solvedUI = dyn(light: 0x178A44, dark: 0x4ADE80)
    static let solved = Color(uiColor: solvedUI)
    static let solvedSoftUI = dyn(light: 0xDCF3E4, dark: 0x0F2E1D)
    static let solvedSoft = Color(uiColor: solvedSoftUI)
    static let revealedUI = dyn(light: 0x8A5106, dark: 0xF5B84D)
    static let revealed = Color(uiColor: revealedUI)
    static let revealedSoftUI = dyn(light: 0xF7ECD4, dark: 0x33260E)
    static let revealedSoft = Color(uiColor: revealedSoftUI)
    // "polling" status (a claude mid poll-loop on a pending ScheduleWakeup)
    // — cyan, mirroring util/ansi.go's StatusColor("polling") = Cyan on the CLI side.
    static let pollingUI = dyn(light: 0x0E7490, dark: 0x22D3EE)
    static let polling = Color(uiColor: pollingUI)
    static let pollingSoftUI = dyn(light: 0xD9F2F7, dark: 0x0E2E33)
    static let pollingSoft = Color(uiColor: pollingSoftUI)

    // Per-skill colors: /info /aim /do /pr each get their own hue so a spec
    // full of pipeline commands reads as a colored pipeline, both in the
    // iOS composer inline highlight (HighlightingTextEditor) and in
    // CmdChipsRow. aim-red is a true red, kept distinct from the pink
    // `counter` above; do-yellow is yellower than the amber `revealed` above
    // so "do" doesn't read as "question pending"; pr-green mirrors `solved`
    // via its own token so the two can diverge later; info-blue is a new
    // hue (the palette otherwise has no true blue — `polling` is cyan).
    // These are MEANING colors: they do not follow the theme tint.
    static let skillInfoUI = dyn(light: 0x1A6FD4, dark: 0x5EA8FF)
    static let skillInfo = Color(uiColor: skillInfoUI)
    static let skillInfoSoftUI = dyn(light: 0xDCEAFB, dark: 0x102A45)
    static let skillInfoSoft = Color(uiColor: skillInfoSoftUI)
    static let skillAimUI = dyn(light: 0xC42B2B, dark: 0xFF6B6B)
    static let skillAim = Color(uiColor: skillAimUI)
    static let skillAimSoftUI = dyn(light: 0xFBE0E0, dark: 0x3A1414)
    static let skillAimSoft = Color(uiColor: skillAimSoftUI)
    static let skillDoUI = dyn(light: 0x8A7206, dark: 0xF5D34D)
    static let skillDo = Color(uiColor: skillDoUI)
    static let skillDoSoftUI = dyn(light: 0xF7F0D4, dark: 0x332B0E)
    static let skillDoSoft = Color(uiColor: skillDoSoftUI)
    static let skillPrUI = dyn(light: 0x178A44, dark: 0x4ADE80)
    static let skillPr = Color(uiColor: skillPrUI)
    static let skillPrSoftUI = dyn(light: 0xDCF3E4, dark: 0x0F2E1D)
    static let skillPrSoft = Color(uiColor: skillPrSoftUI)
    // /dream is the fifth skill hue, and violet is the pick because the four
    // above are blue/red/yellow/green — the five chips sit side by side in
    // CmdChipsRow, so separation from all four at once is what matters. Its
    // nearest neighbour is the generic `accent` purple, which never appears
    // IN the chip row, so that collision is cosmetic rather than confusing.
    // Doubles as the DREAM surface's own accent (DreamView, the feedback
    // section header, the artifact-viewer feedback chip).
    static let skillDreamUI = dyn(light: 0x7E22CE, dark: 0xC084FC)
    static let skillDream = Color(uiColor: skillDreamUI)
    static let skillDreamSoftUI = dyn(light: 0xF3E8FF, dark: 0x2A1440)
    static let skillDreamSoft = Color(uiColor: skillDreamSoftUI)
    // /graph is the sixth skill hue, and it doubles as the GRAPH screen's own
    // accent — the selection ring on the canvas. Both hexes are `--skill-graph`
    // in web/_ui/src/styles/style.css (dark from `:root`, light from the
    // `[data-theme="light"]` block), and web/graphpalette_contract_test.go
    // PARSES the line below to hold the two files in step, exactly as
    // TestJobPaletteMatchesSwift does for the eight job colors above. Keep it
    // as one literal `dyn(light: 0x…, dark: 0x…)` line: a hex assembled from
    // constants would read as a changed palette to that check.
    static let skillGraphUI = dyn(light: 0x456F0B, dark: 0x87DF49)
    static let skillGraph = Color(uiColor: skillGraphUI)

    // Per-job colors: the eight names Claude Code's own `/color` command
    // accepts, in `/color`'s order. The DARK values are Claude Code's exact
    // RGB, so a swatch here is the same pixel color as the prompt bar of the
    // pane that job's cell opens; light is the darkened sibling of each,
    // chosen to clear WCAG AA 4.5:1 against a light card and white.
    // The same sixteen hexes are spelled out in web/static/style.css's two
    // --job-* blocks and in internal/jobs/color.go's `Palette`, which is the
    // source of truth; TestJobPaletteMatchesCSS and TestJobPaletteMatchesSwift
    // (web/jobpalette_test.go) are what hold all three in step — the second
    // reads THIS file — so an edit here without the other two fails the
    // build rather than drifting silently, which is exactly how
    // --skill-info and --skill-do drifted before any such test existed. Keep
    // each entry as one literal `dyn(light: 0x…, dark: 0x…)` line: a hex
    // assembled from constants would read as a changed palette to that check.
    // These are MEANING colors: they do not follow the theme tint.
    static let jobRedUI = dyn(light: 0xCA2323, dark: 0xDC2626)
    static let jobRed = Color(uiColor: jobRedUI)
    static let jobBlueUI = dyn(light: 0x496B8D, dark: 0x6A9BCC)
    static let jobBlue = Color(uiColor: jobBlueUI)
    static let jobGreenUI = dyn(light: 0x107937, dark: 0x16A34A)
    static let jobGreen = Color(uiColor: jobGreenUI)
    static let jobYellowUI = dyn(light: 0x8B5F03, dark: 0xCA8A04)
    static let jobYellow = Color(uiColor: jobYellowUI)
    static let jobPurpleUI = dyn(light: 0x676395, dark: 0x827DBD)
    static let jobPurple = Color(uiColor: jobPurpleUI)
    static let jobOrangeUI = dyn(light: 0x9C563F, dark: 0xD97757)
    static let jobOrange = Color(uiColor: jobOrangeUI)
    static let jobPinkUI = dyn(light: 0x9B516A, dark: 0xC46686)
    static let jobPink = Color(uiColor: jobPinkUI)
    static let jobCyanUI = dyn(light: 0x06718B, dark: 0x0891B2)
    static let jobCyan = Color(uiColor: jobCyanUI)

    /// A knowledge-graph node's fill, by entity type.
    ///
    /// The nine mappings are the port of web graphModel.ts's `TYPE_COLOR_VARS`
    /// — the same palette TOKEN per type, which is the claim this makes: a
    /// `component` takes `--polling` on both surfaces. Whether the two resolve
    /// to the same pixel is a property of each token, and seven of the nine do
    /// in both appearances; `--skill-do` (tool) and `--skill-info`
    /// (external-system) are drifted in LIGHT only — style.css has #7B6505 /
    /// #1866C4 where skillDoUI / skillInfoUI above have 0x8A7206 / 0x1A6FD4.
    /// That drift predates this map, is not the graph's to fix, and no test
    /// pins those two (TestJobPaletteMatchesSwift covers the eight job colors
    /// only); it is written down here so the next reader does not take the
    /// resemblance for a guarantee.
    ///
    /// `convention` takes `queuedUI`, NOT `accentUI`, and that is the one
    /// place this map deliberately reads differently from its source: web's
    /// `TYPE_COLOR_VARS` names `--accent`, but web's `--accent` is a FIXED
    /// violet while ours became tint-derived. Pointing it at `queuedUI` —
    /// which carries the pre-tint accent literals — is what keeps the two
    /// boards the same pixel. Following the tint here would also collide the
    /// type with its neighbours: a cyan tint would make `convention`
    /// indistinguishable from `component` (`polling`), a pink one from
    /// `person` (`counter`), a green one from `decision` (`solved`). A node
    /// type is a MEANING, and meanings never re-tint.
    ///
    /// All NINE seed types of the `/graph` skill's vocabulary are named: a
    /// seed type falling through to the invented-type grey made three
    /// documented types indistinguishable from unrecognised ones, which is a
    /// review finding the web side already carries.
    ///
    /// UIColor rather than Color because the canvas resolves it against a
    /// trait collection before drawing; the SwiftUI wrapper is one
    /// `Color(uiColor:)` at the call site.
    static func graphTypeColorUI(_ type: String) -> UIColor {
        switch type {
        case "convention": return queuedUI
        case "component": return pollingUI
        case "failure-mode": return revealedUI
        case "decision": return solvedUI
        case "person": return counterUI
        case "tool": return skillDoUI
        case "external-system": return skillInfoUI
        // file and job are structural context rather than knowledge, so they
        // deliberately share the hairline grey instead of taking hues of their
        // own — they should recede behind the concepts they carry.
        case "file", "job": return hairline2UI
        // Anything the extraction agent invented that the map does not name.
        default: return text2UI
        }
    }

    // Global chrome is NOT configured here any more. iOS 26 paints the
    // floating tab bar, the glass nav bar and the large title itself, and the
    // opaque scroll-edge nav-bar appearance this file used to install is
    // exactly what hid large titles on scrollable roots (the
    // Sessions/Settings "no title" bug). Tint flows through SwiftUI
    // `.tint(KTheme.accent)` at the app root instead. Do not reintroduce a
    // UIKit appearance-proxy call — not globally, and not per-view.
}

// MARK: - typography

/// The frozen Dynamic Type bucket table. Both font helpers below and
/// `UIFont.voice` map a legacy point size onto a text STYLE, so every call
/// site in the app scales with the user's Dynamic Type setting without any
/// of them being re-sorted by hand. The old central +1.5/+2pt `bumped()`
/// arithmetic is gone — bucketing supersedes it.
enum KTypeScale {
    static func textStyle(for size: CGFloat) -> Font.TextStyle {
        switch size {
        case 17...: return .body
        case 15..<17: return .callout
        case 13..<15: return .footnote
        case 11..<13: return .caption
        default: return .caption2
        }
    }

    /// The UIKit twin of `textStyle(for:)` — same buckets, UIKit's names
    /// (`.caption1` is SwiftUI's `.caption`).
    static func uiTextStyle(for size: CGFloat) -> UIFont.TextStyle {
        switch size {
        case 17...: return .body
        case 15..<17: return .callout
        case 13..<15: return .footnote
        case 11..<13: return .caption1
        default: return .caption2
        }
    }
}

extension Font {
    /// The data accent: SF Mono / Menlo equivalent (system monospaced design).
    /// Slugs, states, meta lines, column titles, section labels.
    static func data(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(KTypeScale.textStyle(for: size), design: .monospaced, weight: weight)
    }

    /// The voice: system sans. Prose — spec text, job detail bodies,
    /// transcript text blocks.
    static func voice(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(KTypeScale.textStyle(for: size), design: .default, weight: weight)
    }
}

extension UIFont {
    /// UIKit twin of Font.voice, for UIKit-hosted text (HighlightingTextEditor's
    /// UITextView) that can't consume a SwiftUI Font. Scales the caller's
    /// literal `size` through the SAME bucket's UIFontMetrics — unlike
    /// Font.voice, which returns the bucket style's own size, so the two
    /// agree only where `size` equals that style's own default. Both still
    /// track Dynamic Type through the same style; the sole call site passes 13.
    static func voice(_ size: CGFloat, weight: UIFont.Weight = .regular) -> UIFont {
        UIFontMetrics(forTextStyle: KTypeScale.uiTextStyle(for: size))
            .scaledFont(for: .systemFont(ofSize: size, weight: weight))
    }
}

// MARK: - state → color

/// The five kanban columns — draft/queued/working/blocked/done — mirroring
/// the server's five job states 1:1 (review was folded into blocked), plus
/// ARCHIVED as a sixth pager page (the web board shows archived jobs in a
/// collapsible drawer instead; on the phone an extra page is cheaper).
enum BoardColumn: String, CaseIterable, Identifiable, Hashable {
    case draft, queued, working, blocked, done, archived

    var id: String { rawValue }

    var title: String {
        switch self {
        case .draft: return "DRAFT"
        case .queued: return "QUEUED"
        case .working: return "WORKING"
        case .blocked: return "BLOCKED"
        case .done: return "DONE"
        case .archived: return "ARCHIVED"
        }
    }

    /// Truncated for the always-visible overview strip, where six columns
    /// have to fit in one row.
    var shortTitle: String {
        switch self {
        case .draft: return "DRAFT"
        case .queued: return "QUEUE"
        case .working: return "WORK"
        case .blocked: return "BLOCK"
        case .done: return "DONE"
        case .archived: return "ARCH"
        }
    }
}

enum BoardTheme {
    // Exact 1:1 mapping to the server's job states — no aliases, no
    // prefix matching. See BoardColumn's doc comment.
    private static let columnStates: [BoardColumn: [String]] = [
        .draft: ["draft"],
        .queued: ["queued"],
        .working: ["working"],
        .blocked: ["blocked"],
        .done: ["done"],
        .archived: ["archived"],
    ]

    /// Which column a job belongs on. Mirrors app.js's render(): a job with
    /// a non-empty `health` always renders under BLOCKED regardless of its
    /// file state — the daemon isn't going to fix it, only a retry will.
    static func column(for job: WebJob) -> BoardColumn {
        if let health = job.health, !health.isEmpty {
            return .blocked
        }
        for (column, states) in columnStates where states.contains(job.state) {
            return column
        }
        return .draft
    }

    /// State → color. working=active (green), blocked=attention (red),
    /// queued=neutral (violet), draft/done/archived=muted.
    ///
    /// NO state colour follows the theme tint — see `KTheme.queued`, which is
    /// a fixed token carrying the pre-tint accent literals precisely so this
    /// switch cannot re-tint. Every entry here is a MEANING hue.
    static func color(forState state: String) -> Color {
        switch state {
        case "working":
            return KTheme.solved
        case "blocked":
            return KTheme.counter
        case "queued":
            return KTheme.queued
        case "draft", "done", "archived":
            return KTheme.text2
        default:
            return KTheme.text2
        }
    }

    /// The eight legal job-color names, in `/color`'s own order — the order
    /// the form's picker strip renders them in.
    static let jobColorNames = ["red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"]

    /// A job's `color` field → its swatch color, or nil for "no color".
    /// nil/empty is the unset case (the card renders exactly as it always
    /// has); an UNRECOGNIZED name — a newer server growing a ninth color —
    /// degrades to nil too rather than crashing or guessing.
    static func jobColor(_ name: String?) -> Color? {
        switch name {
        case "red": return KTheme.jobRed
        case "blue": return KTheme.jobBlue
        case "green": return KTheme.jobGreen
        case "yellow": return KTheme.jobYellow
        case "purple": return KTheme.jobPurple
        case "orange": return KTheme.jobOrange
        case "pink": return KTheme.jobPink
        case "cyan": return KTheme.jobCyan
        default: return nil
        }
    }

    /// An ROI band → how the badge paints. Each band maps to the same palette
    /// tokens web's `.ld-chip` rules name (`--counter`/`--counter-soft`,
    /// `--revealed`/`--revealed-soft`, `--text-2`, `--hairline-2`), which this
    /// enum's entries are the ports of, so the two boards grade one number the
    /// same way with no new hex here to keep in step with that stylesheet.
    /// Every token is already a dynamic light/dark pair, so both appearances
    /// follow for free.
    ///
    /// Hue is deliberately not the only channel — red/green is the common
    /// colour-blind failure and two of these four sit either side of it. So the
    /// box treatment separates them as well: the `hot` FILL is what tells it
    /// from `warm` without relying on pink-vs-amber, and the `cold` DASH is
    /// what tells it from `even`, which shares its colour.
    ///
    ///   hot   counter text on a counter-soft fill — the palette's "a human is
    ///         needed here" colour, which is what a high ratio says
    ///   warm  amber text and border, no fill
    ///   even  muted text, plain hairline border — the resting chip look
    ///   cold  the same muted pair, dashed and dimmed, so it reads with no
    ///         colour perception at all
    static func style(forLoadBand band: LoadBand) -> LoadBandStyle {
        switch band {
        case .hot:
            return LoadBandStyle(text: KTheme.counter, border: KTheme.counterSoft, fill: KTheme.counterSoft, dashed: false, opacity: 1)
        case .warm:
            return LoadBandStyle(text: KTheme.revealed, border: KTheme.revealedSoft, fill: nil, dashed: false, opacity: 1)
        case .even:
            return LoadBandStyle(text: KTheme.text2, border: KTheme.hairline2, fill: nil, dashed: false, opacity: 1)
        case .cold:
            return LoadBandStyle(text: KTheme.text2, border: KTheme.hairline2, fill: nil, dashed: true, opacity: 0.75)
        }
    }
}

/// How one ROI band paints. A value rather than four parallel switches so a
/// band can only ever be described in one place — see
/// `BoardTheme.style(forLoadBand:)`, which is the only thing that builds one.
struct LoadBandStyle {
    let text: Color
    let border: Color
    /// nil = no fill, the card surface shows through.
    let fill: Color?
    /// `cold`'s non-colour channel: the only dashed border on the card.
    let dashed: Bool
    /// `cold`'s second dimming, matching web's `.ld-chip.cold { opacity: .75 }`.
    /// 1 everywhere else.
    let opacity: Double
}

enum SessionTheme {
    /// running=solved, waiting=text2, question=revealed, blocked/error=counter,
    /// polling=cyan (mid poll-loop, idle between ScheduleWakeup ticks by
    /// design), shell=text2 — the same semantic mapping ls.go uses.
    static func color(forStatus status: String, role: String) -> Color {
        if role == "shell" {
            return KTheme.text2
        }
        switch status {
        case "running":
            return KTheme.solved
        case "waiting":
            return KTheme.text2
        case "question":
            return KTheme.revealed
        case "polling":
            return KTheme.polling
        case "blocked", "error":
            return KTheme.counter
        default:
            return KTheme.text2
        }
    }
}

// MARK: - reusable view pieces

/// The themed page field: the tint's three-stop gradient, edge to edge. Every
/// restyled screen's root background.
struct ThemeBackgroundView: View {
    @Environment(\.theme) var theme

    init() {}

    var body: some View {
        LinearGradient(
            colors: theme.backgroundStops,
            startPoint: .topLeading,
            endPoint: .bottomTrailing
        )
        .ignoresSafeArea()
    }
}

/// One glass card. The surface every restyled section/row/card is built on:
/// real Liquid Glass tinted with the environment theme's accent at low
/// opacity, a continuous 24pt corner, and a hairline edge.
struct ThemedCard<Content: View>: View {
    @Environment(\.theme) var theme
    var content: Content

    init(@ViewBuilder content: () -> Content) {
        self.content = content()
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: 24, style: .continuous)
        content
            .glassEffect(.regular.tint(theme.glassTint), in: shape)
            .overlay(shape.stroke(KTheme.hairline, lineWidth: 1))
    }
}

/// A job/session state as a dot + word in a tinted capsule. The dot GLOWS —
/// that shadow is part of this theme, not decoration to be trimmed. Colors
/// come from `BoardTheme.color(forState:)` so the capsule can never disagree
/// with the rest of the board about what a state means.
struct StateCapsule: View {
    let state: String

    init(state: String) {
        self.state = state
    }

    var body: some View {
        let color = BoardTheme.color(forState: state)
        HStack(spacing: 5) {
            Circle()
                .fill(color)
                .frame(width: 6, height: 6)
                .shadow(color: color.opacity(0.8), radius: 3)
            Text(state)
                .font(.data(11, weight: .medium))
                .foregroundStyle(color)
        }
        .padding(.horizontal, 9)
        .padding(.vertical, 4)
        .background(color.opacity(0.12), in: Capsule())
    }
}

/// An SF Symbol plus an optional count in a soft capsule — the card metadata
/// unit (worktrees, questions, artifacts, feedback, PRs). A nil tint renders
/// muted, which is the "present but not interesting" reading.
struct KChip: View {
    let icon: String
    let text: String?
    let tint: Color?

    init(icon: String, text: String? = nil, tint: Color? = nil) {
        self.icon = icon
        self.text = text
        self.tint = tint
    }

    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: icon)
                .font(.system(size: 10, weight: .semibold))
            if let text {
                Text(text).font(.data(11, weight: .medium))
            }
        }
        .foregroundStyle(tint ?? KTheme.text2)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .background((tint ?? KTheme.text2).opacity(0.12), in: Capsule())
    }
}

/// One card's chrome: a translucent rounded surface, a hairline edge, and a
/// leading CAPSULE that is the job's COLOR channel and nothing else.
///
/// The bar is the job's color and NOTHING else — no state, and no flagged
/// override. An uncolored job is grey whether it is draft, queued, working,
/// blocked or done, so the bar answers "which job is this", never "how is it
/// going" or "does this need me". State stays fully readable from the card's
/// column and its state capsule, which use BoardTheme.color(forState:); a
/// pending question still shows its own flag icon in the card's title row and
/// its own square on the column header, so dropping the card-wide pink glow
/// cost no signal.
///
/// The parameter is `barColor` rather than `jobColor` or `stateColor`
/// because two different callers fill it: a board card passes the job's
/// colour, while JobDetailView's DetailCard passes a state colour, which is
/// meaningful there — you are already looking at exactly one job. nil means
/// grey either way.
struct CardBackground: ViewModifier {
    var barColor: Color?
    /// false = the same surface with no leading bar at all — for cards where
    /// the bar would answer a question nobody is asking (DetailCard: you are
    /// already looking at one job whose state sits a section above).
    var bar: Bool = true

    var resolvedBar: Color { barColor ?? KTheme.text2 }

    func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: 18, style: .continuous)
        content
            .background(KTheme.surface, in: shape)
            .background(.ultraThinMaterial, in: shape)
            .overlay(shape.stroke(KTheme.hairline, lineWidth: 1))
            .overlay(alignment: .leading) {
                if bar {
                    Capsule()
                        .fill(resolvedBar)
                        .frame(width: 3)
                        .padding(.vertical, 10)
                        .padding(.leading, 3)
                }
            }
    }
}

extension View {
    func cardBackground(barColor: Color? = nil, bar: Bool = true) -> some View {
        modifier(CardBackground(barColor: barColor, bar: bar))
    }
}

/// Uppercase, letterspaced, dim mono — column headers, section labels.
struct KLabelStyle: ViewModifier {
    var size: CGFloat = 10

    func body(content: Content) -> some View {
        content
            .font(.data(size, weight: .semibold))
            .kerning(1.6)
            .foregroundStyle(KTheme.text2)
            .textCase(.uppercase)
    }
}

extension View {
    func kLabelStyle(size: CGFloat = 10) -> some View {
        modifier(KLabelStyle(size: size))
    }
}

/// A Form/List section with a kraken mono header on a translucent row
/// surface, instead of the system's grouped-gray uppercase header. Use in
/// place of `Section("Title") { ... }` throughout.
@ViewBuilder
func kSection<Content: View>(_ title: String? = nil, @ViewBuilder content: () -> Content) -> some View {
    if let title {
        Section {
            content()
        } header: {
            Text(title).kLabelStyle()
        }
        .listRowBackground(KTheme.surface)
        .listRowSeparatorTint(KTheme.hairline)
    } else {
        Section {
            content()
        }
        .listRowBackground(KTheme.surface)
        .listRowSeparatorTint(KTheme.hairline)
    }
}

/// Drops the system list background so the themed gradient field shows
/// through and the inset-grouped rows read as frosted cards on it. Apply to
/// every Form/List root.
struct KListStyle: ViewModifier {
    func body(content: Content) -> some View {
        content
            .scrollContentBackground(.hidden)
            .background { ThemeBackgroundView() }
    }
}

extension View {
    func kListStyle() -> some View {
        modifier(KListStyle())
    }
}

/// TextField/TextEditor chrome: a raised translucent fill, continuous 12pt
/// corner, hairline edge that goes accent on focus.
struct KFieldStyle: ViewModifier {
    var focused: Bool = false
    var minHeight: CGFloat?

    func body(content: Content) -> some View {
        let shape = RoundedRectangle(cornerRadius: 12, style: .continuous)
        return content
            .font(.voice(13))
            .foregroundStyle(KTheme.text)
            .padding(10)
            .frame(minHeight: minHeight, alignment: .topLeading)
            .background(KTheme.raised, in: shape)
            .overlay(shape.stroke(focused ? KTheme.accent : KTheme.hairline, lineWidth: focused ? 1.5 : 1))
    }
}

extension View {
    func kField(focused: Bool = false, minHeight: CGFloat? = nil) -> some View {
        modifier(KFieldStyle(focused: focused, minHeight: minHeight))
    }
}

// Buttons stay native — on iOS 26 that IS the glass idiom: .bordered /
// .borderedProminent / .glass / .glassProminent with SF Symbol icons, tinted
// with .tint(KTheme.___). See each view for which tint/icon/role a given
// action uses.

// Icon-only sheet/drawer dismiss control, used in every sheet's cancellation
// toolbar slot. A plain accent xmark: iOS 26 already renders toolbar buttons
// as glass capsules, so adding a button style here would double the surface.
// Toolbar buttons already carry a >=44pt tap area, and the explicit tint
// keeps it on the theme colour rather than leaning on a nav-bar appearance
// tint (there is none any more, by design).
struct SheetCloseButton: View {
    var label: String = "Close"
    let action: () -> Void
    var body: some View {
        Button(action: action) {
            Label(label, systemImage: "xmark")
        }
        .labelStyle(.iconOnly)
        .tint(KTheme.accent)
        .accessibilityLabel(label)
    }
}
