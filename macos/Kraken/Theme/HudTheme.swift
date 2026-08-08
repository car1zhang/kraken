import SwiftUI

// HudTheme.swift — the Industrial HUD theme engine, ported from
// `web/_ui/src/styles/style.css`.
//
// The web board declares a handful of PRIMITIVES on `:root` (`--ac`, `--b0`,
// `--b1`, …) and then declares every surface, border and glow as a
// `color-mix(in srgb, …)` DERIVATION of them, on the same element. Overriding
// only the primitives — for a light theme, or for one of the four alternate
// accents — makes all thirteen derivations recompute against the new palette.
// That cascade has no equivalent in SwiftUI, so it becomes an explicit
// recomputation: `HudTheme.resolve(mode, accent)` picks the primitives and
// computes the derived fields once, and views read the result out of the
// environment. The pixels are identical; only the mechanism differs.
//
// Every hex below is byte-copied from style.css:
//   - dark violet primitives      :24-34
//   - dark accent overrides       :148-151
//   - light primitives            :163-169
//   - light accent overrides      :221-224
//   - the derivation recipes      :37-60
//
// `color-mix(in srgb, A p%, B)` is a plain per-channel lerp on 0-255 sRGB
// values, which is what `mix` implements. No gamma, no premultiplication:
// the CSS `in srgb` keyword means exactly this.

// MARK: - primitives

/// The two board themes. Dark is the default everywhere — style.css keeps the
/// dark palette on bare `:root` and overrides it under
/// `[data-theme="light"]` precisely so that any failure mode lands on dark
/// rather than on a half-resolved mix.
enum ThemeMode: String, CaseIterable {
    case dark, light
}

/// The five accent hues. Violet is kraken's own and the default; the other
/// four override ONLY `--ac`/`--ac2`/`--tint` and let every derivation
/// recompute. The case order is the order the accent control cycles through,
/// matching style.css's declaration order.
enum HudAccent: String, CaseIterable {
    case violet, cyan, amber, crimson, green
}

/// One opaque sRGB colour as three 0-255 channels — the same shape CSS hexes
/// and `color-mix` arithmetic are written in, so a mix result can be compared
/// against the stylesheet by eye and by test without a float round-trip.
struct RGB: Equatable {
    let r: Int
    let g: Int
    let b: Int

    init(r: Int, g: Int, b: Int) {
        self.r = r
        self.g = g
        self.b = b
    }

    /// Parses `"#rrggbb"` (case-insensitive; a missing `#` is tolerated).
    /// Anything else resolves to black rather than trapping — every colour in
    /// this app is a compile-time literal, so a malformed one is a typo to be
    /// seen on screen, never a reason to kill the app at launch. This is the
    /// same defensive posture the Go side takes with data it does not own.
    init(hex: String) {
        var s = hex.trimmingCharacters(in: .whitespaces)
        if s.hasPrefix("#") { s.removeFirst() }
        guard s.count == 6, let v = UInt32(s, radix: 16) else {
            self.init(r: 0, g: 0, b: 0)
            return
        }
        self.init(r: Int((v >> 16) & 0xFF), g: Int((v >> 8) & 0xFF), b: Int(v & 0xFF))
    }

    /// `"#rrggbb"`, lowercase — the round trip of `init(hex:)`.
    var hex: String { String(format: "#%02x%02x%02x", r, g, b) }

    /// The SwiftUI colour, in sRGB explicitly: the mixes above are sRGB
    /// arithmetic, so resolving them through any other working space would
    /// undo the parity this file exists to hold.
    var color: Color { Color(.sRGB, red: Double(r) / 255, green: Double(g) / 255, blue: Double(b) / 255) }

    /// The same colour at a chosen alpha — how the four `…, transparent`
    /// mixes (gridline, graph edge, accent edge, chip border) and the scrim
    /// are expressed, since `color-mix(X p%, transparent)` is just X at p%
    /// alpha.
    func color(_ opacity: Double) -> Color {
        Color(.sRGB, red: Double(r) / 255, green: Double(g) / 255, blue: Double(b) / 255, opacity: opacity)
    }
}

// MARK: - the theme

/// One fully resolved palette: the primitives for a (mode, accent) pair plus
/// every derivation computed off them. Immutable and `Equatable`, so putting
/// it in the environment costs SwiftUI one cheap comparison per invalidation
/// rather than a re-derivation per read.
struct HudTheme: Equatable {
    let mode: ThemeMode
    let accent: HudAccent

    // --- primitives (style.css :24-34 dark / :163-169 light) ---
    /// `--ac`: the one hue every derivation below is mixed from.
    let ac: RGB
    /// `--ac2`: the counter hue — the LED, the `hot` stat, danger usage.
    let ac2: RGB
    /// `--tint`: the light accent used for emphasis text.
    let tint: RGB
    /// `--b0`: the deepest ground (the window behind the board).
    let b0: RGB
    /// `--b1`: the panel ground every surface/border recipe mixes over.
    let b1: RGB
    /// `--b2`: the chrome bar's translucent ground; `b2Alpha` carries the
    /// `rgba()` alpha the CSS declares alongside it.
    let b2: RGB
    let b2Alpha: Double
    /// `--g1`/`--g2`: the two neutral greys `muted` and `body` mix over.
    let g1: RGB
    let g2: RGB
    /// `--fg`: the brightest text.
    let fg: RGB
    /// `--ok`/`--warn`: fixed status hues; they do NOT follow the accent.
    let ok: RGB
    let warn: RGB
    /// `--polling`: the "mid poll-loop" status hue. A primitive rather than a
    /// derivation for the same reason as ok/warn — it must not follow the
    /// accent, or the status stops meaning one thing.
    let polling: RGB
    /// `--scan`: the scanline rake's ink, declared as `rgba()` in CSS, so it
    /// arrives here as a colour plus a separate alpha.
    let scan: RGB
    let scanAlpha: Double

    // --- derivations (style.css :37-60), recomputed from the above ---
    /// `--bg-board` = ac 4% over b0
    let bgBoard: RGB
    /// `--surface` = ac 7% over b1
    let surface: RGB
    /// `--border` = ac 26% over b1
    let border: RGB
    /// `--border-strong` = ac 36% over b1
    let borderStrong: RGB
    /// `--divider` = ac 16% over b1
    let divider: RGB
    /// `--muted` = ac 28% over g1
    let muted: RGB
    /// `--body` = ac 24% over g2
    let body: RGB
    /// `--chip-bg` = ac 14% over b1
    let chipBg: RGB
    /// `--raised` = ac 6% over b1 — the sub-panel fill, one step brighter
    /// than `surface`.
    let raised: RGB

    /// `color-mix(in srgb, a p%, b)` — the plain per-channel sRGB lerp, and
    /// the single piece of arithmetic the whole engine rests on. `pct` is
    /// 0-100 (a percentage, as written in the stylesheet), and the result is
    /// clamped so an out-of-range percentage cannot produce an invalid
    /// channel.
    static func mix(_ a: RGB, _ pct: Double, over b: RGB) -> RGB {
        let f = pct / 100
        func channel(_ x: Int, _ y: Int) -> Int {
            let v = (Double(x) * f + Double(y) * (1 - f)).rounded()
            return min(255, max(0, Int(v)))
        }
        return RGB(r: channel(a.r, b.r), g: channel(a.g, b.g), b: channel(a.b, b.b))
    }

    /// The one constructor: pick primitives for the pair, then run all
    /// thirteen recipes. Nothing else may build a `HudTheme`, which is what
    /// guarantees no derived field can ever be out of step with the
    /// primitives it claims to come from.
    static func resolve(_ mode: ThemeMode, _ accent: HudAccent) -> HudTheme {
        HudTheme(mode: mode, accent: accent)
    }

    init(mode: ThemeMode, accent: HudAccent) {
        self.mode = mode
        self.accent = accent

        let hue = HudTheme.hue(mode, accent)
        ac = hue.ac
        ac2 = hue.ac2
        tint = hue.tint

        switch mode {
        case .dark:
            // style.css :25-34
            b0 = RGB(hex: "#0a090d")
            b1 = RGB(hex: "#0d0c12")
            b2 = RGB(hex: "#0a090d")
            b2Alpha = 0.92
            g1 = RGB(hex: "#90909a")
            g2 = RGB(hex: "#ababb8")
            fg = RGB(hex: "#f2edff")
            ok = RGB(hex: "#7fd44a")
            warn = RGB(hex: "#ffb000")
            polling = RGB(hex: "#00e5ff")
            scan = RGB(hex: "#000000")
            scanAlpha = 0.18
        case .light:
            // style.css :165-169
            b0 = RGB(hex: "#f6f4fa")
            b1 = RGB(hex: "#ecebf3")
            b2 = RGB(hex: "#faf9fd")
            b2Alpha = 0.92
            g1 = RGB(hex: "#6b6a78")
            g2 = RGB(hex: "#4c4b58")
            fg = RGB(hex: "#1a1724")
            ok = RGB(hex: "#3e8f1e")
            warn = RGB(hex: "#a36f00")
            polling = RGB(hex: "#00799c")
            scan = RGB(hex: "#141028")
            scanAlpha = 0.04
        }

        // the derivation recipes, verbatim from style.css :37-60
        bgBoard = HudTheme.mix(ac, 4, over: b0)
        surface = HudTheme.mix(ac, 7, over: b1)
        border = HudTheme.mix(ac, 26, over: b1)
        borderStrong = HudTheme.mix(ac, 36, over: b1)
        divider = HudTheme.mix(ac, 16, over: b1)
        muted = HudTheme.mix(ac, 28, over: g1)
        body = HudTheme.mix(ac, 24, over: g2)
        chipBg = HudTheme.mix(ac, 14, over: b1)
        raised = HudTheme.mix(ac, 6, over: b1)
    }

    /// The hue triple for a (mode, accent) pair. Violet is the base and the
    /// other four are complete replacements of the same three primitives —
    /// exactly what style.css's `[data-accent]` blocks are. An exhaustive
    /// switch rather than a dictionary lookup, so adding a case to `HudAccent`
    /// fails the build here instead of silently falling back to violet.
    static func hue(_ mode: ThemeMode, _ accent: HudAccent) -> HueTriple {
        switch (mode, accent) {
        // style.css :24 (violet base) and :148-151 (the four overrides)
        case (.dark, .violet): return HueTriple(ac: "#a86aff", ac2: "#ff4fd8", tint: "#c9b2ff")
        case (.dark, .cyan): return HueTriple(ac: "#00e5ff", ac2: "#ff2d95", tint: "#a8f4ff")
        case (.dark, .amber): return HueTriple(ac: "#ffb000", ac2: "#ff6a00", tint: "#ffe0a8")
        case (.dark, .crimson): return HueTriple(ac: "#ff3b5c", ac2: "#ff8a00", tint: "#ffb2c0")
        case (.dark, .green): return HueTriple(ac: "#2bff88", ac2: "#ffcf3f", tint: "#b2ffd4")
        // style.css :164 (violet base) and :221-224 (the four overrides)
        case (.light, .violet): return HueTriple(ac: "#6d3fd4", ac2: "#c41d9e", tint: "#5a2eb8")
        case (.light, .cyan): return HueTriple(ac: "#00799c", ac2: "#c41d6e", tint: "#005f7d")
        case (.light, .amber): return HueTriple(ac: "#a36f00", ac2: "#bc4e00", tint: "#8a5e00")
        case (.light, .crimson): return HueTriple(ac: "#c2183a", ac2: "#b05e00", tint: "#a3142f")
        case (.light, .green): return HueTriple(ac: "#0e8a45", ac2: "#9c7a00", tint: "#0b7038")
        }
    }

    struct HueTriple {
        let ac: RGB
        let ac2: RGB
        let tint: RGB
        init(ac: String, ac2: String, tint: String) {
            self.ac = RGB(hex: ac)
            self.ac2 = RGB(hex: ac2)
            self.tint = RGB(hex: tint)
        }
    }
}

// MARK: - glows

extension HudTheme {
    /// `--glow` (style.css :87) — the soft accent halo. The kit's rule, stated
    /// at :170, is "no glow in light mode, on anything", and the light block
    /// implements it by flattening all five glow tokens to `none`; here that
    /// is one `nil` per token, so a caller cannot accidentally paint a light
    /// halo by reading a colour it should never have had.
    var glow: RGB? { mode == .dark ? ac : nil }
    /// `--glow-hot` (:88)
    var glowHot: RGB? { mode == .dark ? ac2 : nil }
    /// `--glow-ok` (:89)
    var glowOK: RGB? { mode == .dark ? ok : nil }
    /// `--glow-warn` (:90)
    var glowWarn: RGB? { mode == .dark ? warn : nil }
    /// `--text-glow` (:91)
    var textGlow: RGB? { mode == .dark ? ac : nil }
}

// MARK: - the four `…, transparent` derivations

extension HudTheme {
    /// `--gridline` = ac 6% over transparent (:44) — the blueprint grid's ink.
    var gridline: Color { ac.color(0.06) }
    /// `--graph-edge` = ac 32% over transparent (:54).
    var graphEdge: Color { ac.color(0.32) }
    /// `--accent-edge` = ac 40% over transparent (:55) — also the card's
    /// left-bar fallback when a job has no colour.
    var accentEdge: Color { ac.color(0.40) }
    /// `--chip-border` = ac 25% over transparent (:57).
    var chipBorder: Color { ac.color(0.25) }
}

// MARK: - SwiftUI colour accessors

extension HudTheme {
    var accentColor: Color { ac.color }
    var counterColor: Color { ac2.color }
    var tintColor: Color { tint.color }
    var groundColor: Color { bgBoard.color }
    var surfaceColor: Color { surface.color }
    var raisedColor: Color { raised.color }
    var borderColor: Color { border.color }
    var borderStrongColor: Color { borderStrong.color }
    var dividerColor: Color { divider.color }
    var mutedColor: Color { muted.color }
    var bodyColor: Color { body.color }
    var chipBgColor: Color { chipBg.color }
    var textColor: Color { fg.color }
    var okColor: Color { ok.color }
    var warnColor: Color { warn.color }
    var pollingColor: Color { polling.color }
    var scanColor: Color { scan.color(scanAlpha) }
    /// `--b2`, the chrome bar's translucent ground, with its declared alpha.
    var chromeGround: Color { b2.color(b2Alpha) }
    /// text ON an accent fill — the board colour, per the kit's active tab
    /// (`--accent-ink`, :71).
    var accentInk: Color { bgBoard.color }

    // the -soft family (:72-79): a status hue laid thinly over the panel
    // ground, used as chip/banner fills.
    var accentSoft: Color { HudTheme.mix(ac, 22, over: b1).color }
    var counterSoft: Color { HudTheme.mix(ac2, 14, over: b1).color }
    var okSoft: Color { HudTheme.mix(ok, 13, over: b1).color }
    var warnSoft: Color { HudTheme.mix(warn, 13, over: b1).color }
    var pollingSoft: Color { HudTheme.mix(polling, 14, over: b1).color }

    /// `--scrim` (:81) — the flat 72% modal backdrop. Deliberately flat: the
    /// web board carries no `backdrop-filter`, so `.ultraThinMaterial` here
    /// would be a departure rather than a translation.
    var scrim: Color { b0.color(0.72) }
    /// `--drop-tint` / `--drop-tint-hover` (:82-83) — the drag-target washes.
    var dropTint: Color { ac.color(0.05) }
    var dropTintHover: Color { ac.color(0.11) }
    /// `--zebra` (:84) — the alternating table row wash.
    var zebra: Color { ac.color(0.04) }
    /// The chrome bar's own fill: `color-mix(in srgb, var(--ac) 8%, var(--b2))`
    /// (:428). Mixing over a translucent ground is not expressible as one
    /// opaque RGB, so the mix runs on the colour and the alpha rides along.
    var chromeFill: Color { HudTheme.mix(ac, 8, over: b2).color(b2Alpha) }
}

// MARK: - environment

struct HudThemeKey: EnvironmentKey {
    /// Dark violet, the same default `:root` carries — every failure mode
    /// lands on the dark board rather than on an unreadable mixed state.
    static let defaultValue = HudTheme.resolve(.dark, .violet)
}

/// The `▦ FX` toggle (style.css :313-315, `data-scanlines="off"`). A separate
/// key from the theme because it is a user preference rather than a palette:
/// flipping it must not re-derive thirteen colours.
struct HudScanlinesKey: EnvironmentKey {
    static let defaultValue = true
}

extension EnvironmentValues {
    var hudTheme: HudTheme {
        get { self[HudThemeKey.self] }
        set { self[HudThemeKey.self] = newValue }
    }

    var hudScanlines: Bool {
        get { self[HudScanlinesKey.self] }
        set { self[HudScanlinesKey.self] = newValue }
    }
}

extension View {
    /// Publishes a resolved theme to the subtree. Also sets `colorScheme`, so
    /// the handful of system-drawn things the app cannot repaint (text
    /// insertion points, focus rings, menu chrome) agree with the board —
    /// style.css does the same with its `color-scheme` declarations
    /// (:142, :213).
    func hudTheme(_ theme: HudTheme) -> some View {
        environment(\.hudTheme, theme)
            .environment(\.colorScheme, theme.mode == .dark ? .dark : .light)
    }

    func hudScanlines(_ on: Bool) -> some View {
        environment(\.hudScanlines, on)
    }
}
