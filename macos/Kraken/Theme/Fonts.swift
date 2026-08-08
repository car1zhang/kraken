import AppKit
import SwiftUI

// Fonts.swift — the two type families.
//
// style.css :132-140 sets out the whole rule: `--display` is Chakra Petch and
// is worn by headings, labels, tabs and buttons ONLY; everything else is
// IBM Plex Mono. `--sans` keeps its name (the sheet reads it all over) but
// resolves to the mono stack, because in this design there is no prose voice
// distinct from the data voice.
//
//   --display: "Chakra Petch", "Avenir Next", sans-serif;
//   --mono:    "IBM Plex Mono", "SF Mono", "Menlo", monospace;
//
// Both fallback chains are reproduced below. They matter more here than on
// the web: the TTFs are bundled in the app (macos/Kraken/Fonts/, declared
// through `ATSApplicationFontsPath`, never the iOS-only `UIAppFonts`), and if
// that declaration is ever wrong the fonts do not load and `Font.custom`
// silently substitutes the SYSTEM font — a failure that looks like a design
// regression rather than like a build problem. Resolving through
// `NSFont(name:)` and stepping down the same chain the sheet declares keeps
// the failure looking like the web board's own fallback.
//
// The PostScript names below were read from the bundled files themselves this
// session, via CTFontManagerCreateFontDescriptorsFromURL:
//   ChakraPetch-SemiBold, ChakraPetch-Bold,
//   IBMPlexMono-Regular, IBMPlexMono-Medium, IBMPlexMono-SemiBold
// (family names "Chakra Petch" and "IBM Plex Mono"). `Font.custom` wants the
// PostScript name, not the family name — passing the family gets the regular
// face regardless of the weight asked for.
//
// TRACKING: CSS `letter-spacing` is written in em and SwiftUI's `.tracking`
// takes POINTS, so every call site multiplies. `HudTracking` carries the
// values the sheet actually uses so a caller writes `.hudDisplay(size: 10,
// tracking: HudTracking.tab)` rather than a bare 0.12 whose unit is anyone's
// guess.

/// The display face's two weights — the only two bundled, matching the two
/// `@fontsource/chakra-petch` imports at style.css :9-10.
enum HudDisplayWeight {
    /// CSS 600 — headings, labels, tabs, buttons.
    case semibold
    /// CSS 700 — the wordmark.
    case bold
}

/// The mono face's three weights, matching style.css :11-13.
enum HudMonoWeight {
    /// CSS 400 — body text, the default.
    case regular
    /// CSS 500
    case medium
    /// CSS 600
    case semibold
}

enum HudFont {
    // PostScript names, verified against the bundled TTFs.
    static let displaySemiBold = "ChakraPetch-SemiBold"
    static let displayBold = "ChakraPetch-Bold"
    static let monoRegular = "IBMPlexMono-Regular"
    static let monoMedium = "IBMPlexMono-Medium"
    static let monoSemiBold = "IBMPlexMono-SemiBold"

    /// `--display`'s second entry (:138). PostScript names for the two
    /// weights the chain needs; Avenir Next ships with macOS.
    static let displayFallbacks = ["AvenirNext-DemiBold", "AvenirNext-Bold"]
    /// `--mono`'s second and third entries (:139). SF Mono is present on
    /// every modern macOS but is not user-installed, so Menlo backs it up.
    static let monoFallbacks = ["SFMono-Regular", "Menlo-Regular"]

    /// Whether a PostScript name resolves to a real face on this machine.
    static func exists(_ postScriptName: String) -> Bool {
        NSFont(name: postScriptName, size: 12) != nil
    }

    /// The first name in a chain that actually resolves, or nil when none do
    /// — at which point the caller hands the job to `Font.system`, which is
    /// the `sans-serif`/`monospace` generic at the end of each CSS chain.
    static func firstAvailable(_ names: [String]) -> String? {
        names.first(where: exists)
    }

    // The five chains, each resolved ONCE. A `static let` is lazy and
    // thread-safe, which is the whole reason the resolution lives here rather
    // than inside the `Font` factories below: those are called from every
    // view body on the board, and a font-manager lookup per label per frame
    // at ~70 cards is a cost worth paying exactly once. The answer cannot go
    // stale either — the app's own faces are registered from its bundle at
    // launch and nothing unregisters them.
    static let resolvedDisplaySemiBold = firstAvailable([displaySemiBold] + displayFallbacks)
    static let resolvedDisplayBold = firstAvailable([displayBold] + displayFallbacks)
    static let resolvedMonoRegular = firstAvailable([monoRegular] + monoFallbacks)
    static let resolvedMonoMedium = firstAvailable([monoMedium] + monoFallbacks)
    static let resolvedMonoSemiBold = firstAvailable([monoSemiBold] + monoFallbacks)

    static func resolvedDisplay(_ weight: HudDisplayWeight) -> String? {
        switch weight {
        case .semibold: return resolvedDisplaySemiBold
        case .bold: return resolvedDisplayBold
        }
    }

    static func resolvedMono(_ weight: HudMonoWeight) -> String? {
        switch weight {
        case .regular: return resolvedMonoRegular
        case .medium: return resolvedMonoMedium
        case .semibold: return resolvedMonoSemiBold
        }
    }
}

extension Font {
    /// Chakra Petch at `size`, stepping down `--display`'s chain if it is not
    /// loaded. `size` is a fixed point size, not a text style: the HUD's type
    /// scale is absolute (9/10/11/12/16 px in the sheet) and a Dynamic-Type
    /// relative size would break the bar's `nowrap` layout on the first
    /// accessibility bump.
    static func hudDisplay(_ size: CGFloat, weight: HudDisplayWeight = .semibold) -> Font {
        if let name = HudFont.resolvedDisplay(weight) {
            return .custom(name, fixedSize: size)
        }
        return .system(size: size, weight: weight == .bold ? .bold : .semibold)
    }

    /// IBM Plex Mono at `size`, stepping down `--mono`'s chain.
    static func hudMono(_ size: CGFloat, weight: HudMonoWeight = .regular) -> Font {
        if let name = HudFont.resolvedMono(weight) {
            return .custom(name, fixedSize: size)
        }
        let systemWeight: Font.Weight
        switch weight {
        case .regular: systemWeight = .regular
        case .medium: systemWeight = .medium
        case .semibold: systemWeight = .semibold
        }
        return .system(size: size, weight: systemWeight, design: .monospaced)
    }
}

/// The em-valued letter-spacings the sheet uses, named by the role that wears
/// them. Multiply by the point size to get SwiftUI tracking — which is what
/// the view modifiers below do, so a caller passes the em value.
enum HudTracking {
    /// `.mark`, the wordmark — 0.24em (:445)
    static let wordmark: CGFloat = 0.24
    /// `.viewtab` — 0.12em (:506)
    static let tab: CGFloat = 0.12
    /// `.theme-btn` / `.upd` — 0.08em (:531, :559)
    static let control: CGFloat = 0.08
    /// `.chrome .stat` / `.colorsel-btn` — 0.06em (:472, :2087)
    static let stat: CGFloat = 0.06
    /// the clock, `.stat.build` — 0.04em (:497, :480)
    static let tight: CGFloat = 0.04
    /// no tracking
    static let none: CGFloat = 0

    /// em → points. The one conversion; nothing else should multiply a
    /// tracking value by a size.
    static func points(_ em: CGFloat, size: CGFloat) -> CGFloat { em * size }
}

extension View {
    /// Chakra Petch plus its tracking, in one modifier — the two are never
    /// applied separately in this design, and splitting them is how a heading
    /// ends up correctly-faced and untracked.
    func hudDisplay(size: CGFloat, weight: HudDisplayWeight = .semibold, tracking: CGFloat = HudTracking.none) -> some View {
        font(.hudDisplay(size, weight: weight))
            .tracking(HudTracking.points(tracking, size: size))
    }

    /// IBM Plex Mono plus its tracking.
    func hudMono(size: CGFloat, weight: HudMonoWeight = .regular, tracking: CGFloat = HudTracking.none) -> some View {
        font(.hudMono(size, weight: weight))
            .tracking(HudTracking.points(tracking, size: size))
    }
}
