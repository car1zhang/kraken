import SwiftUI

// Glow.swift — the centred halos.
//
// style.css :85-91 declares five glow tokens, all of them a `box-shadow` with
// no offset ("centred") and a colour that is a status hue at partial alpha:
//
//   --glow       0 0 8px  ac  40%   (:87)  focus/hover on ordinary surfaces
//   --glow-hot   0 0 8px  ac2 40%   (:88)
//   --glow-ok    0 0 8px  ok  45%   (:89)
//   --glow-warn  0 0 8px  warn 45%  (:90)
//   --text-glow  0 0 9px  ac  55%   (:91)  the wordmark's trailing glyph etc.
//
// and the light block (:170-172) flattens ALL FIVE to `none` — the kit's rule
// is "no glow in light mode, on anything". `HudTheme`'s four glow optionals
// carry that rule; this file turns a non-nil one into something a view can
// apply, and turns a nil one into a no-op that still type-checks, so a call
// site never writes the `if theme.mode == .dark` test itself.
//
// ONE conversion is not byte-parity and is worth stating: a CSS box-shadow's
// third length is a BLUR DIAMETER, while SwiftUI's `.shadow(radius:)` is
// closer to the Gaussian sigma — the conventional mapping, and the one used
// here, is `radius = cssBlur / 2`. The concessions ledger already books this
// as "compositing differs from CSS by a hair at high zoom".

/// One resolved shadow: everything `.shadow(color:radius:x:y:)` needs.
struct HudGlow: Equatable {
    let color: Color
    /// Already converted from the stylesheet's blur length (see the note
    /// above) — pass it to `.shadow(radius:)` unchanged.
    let radius: CGFloat
    let x: CGFloat
    let y: CGFloat

    init(_ rgb: RGB, alpha: Double, cssBlur: CGFloat, x: CGFloat = 0, y: CGFloat = 0) {
        self.color = rgb.color(alpha)
        self.radius = cssBlur / 2
        self.x = x
        self.y = y
    }
}

extension HudTheme {
    /// `--glow` (:87). nil in light mode, and every caller is expected to
    /// pass the nil straight to `.hudGlow(_:)`.
    var glowStyle: HudGlow? { glow.map { HudGlow($0, alpha: 0.40, cssBlur: 8) } }
    /// `--glow-hot` (:88)
    var glowHotStyle: HudGlow? { glowHot.map { HudGlow($0, alpha: 0.40, cssBlur: 8) } }
    /// `--glow-ok` (:89)
    var glowOKStyle: HudGlow? { glowOK.map { HudGlow($0, alpha: 0.45, cssBlur: 8) } }
    /// `--glow-warn` (:90)
    var glowWarnStyle: HudGlow? { glowWarn.map { HudGlow($0, alpha: 0.45, cssBlur: 8) } }
    /// `--text-glow` (:91) — a slightly wider, stronger halo than `--glow`,
    /// because it sits behind GLYPHS rather than behind a filled box.
    var textGlowStyle: HudGlow? { textGlow.map { HudGlow($0, alpha: 0.55, cssBlur: 9) } }

    /// `.btn.primary:hover` / `.btn-big:hover` (:1735, :1757) — the one glow
    /// the sheet writes inline instead of through a token, wider and stronger
    /// than `--glow` because the button underneath it is already accent-filled.
    var primaryHoverGlow: HudGlow? { glow.map { HudGlow($0, alpha: 0.55, cssBlur: 12) } }

    /// The two-layer neon pair the skill palette wears
    /// (`--glow-skill-*` :113-124 dark, :194-205 light). Dark is a wide 30%
    /// wash under a tight 70% core; light collapses to a single soft drop with
    /// a 1px downward offset — the one place light mode keeps a shadow at all,
    /// and it is a drop shadow rather than a halo, which is why it is not one
    /// of the five tokens the light block flattens.
    func skillGlow(_ skill: String) -> [HudGlow] {
        guard let c = SkillPalette.color(skill, mode) else { return [] }
        switch mode {
        case .dark:
            return [
                HudGlow(c, alpha: 0.30, cssBlur: 12),
                HudGlow(c, alpha: 0.70, cssBlur: 2),
            ]
        case .light:
            return [HudGlow(c, alpha: 0.25, cssBlur: 6, y: 1)]
        }
    }

    /// `--text-glow-skill-*` (:114-124) — `0 0 1px <c>, 0 0 9px <c>`, both at
    /// full alpha, which is what makes an inline `/do` read as neon rather
    /// than as coloured text. Empty in light mode: `--text-glow: none` above
    /// governs the whole family, and the light block restates each one as
    /// `none` (:195-205).
    func skillTextGlow(_ skill: String) -> [HudGlow] {
        guard mode == .dark, let c = SkillPalette.color(skill, mode) else { return [] }
        return [
            HudGlow(c, alpha: 1.0, cssBlur: 1),
            HudGlow(c, alpha: 1.0, cssBlur: 9),
        ]
    }
}

extension View {
    /// Applies a glow, or nothing at all when it is nil. The optional is the
    /// point: light mode resolves every halo to nil in `HudTheme`, so a view
    /// that writes `.hudGlow(theme.glowStyle)` is correct in both modes with
    /// no branch of its own.
    @ViewBuilder
    func hudGlow(_ glow: HudGlow?) -> some View {
        if let glow {
            shadow(color: glow.color, radius: glow.radius, x: glow.x, y: glow.y)
        } else {
            self
        }
    }

    /// Stacks a multi-layer glow (the skill pairs). This is not quite what
    /// CSS does and the difference is worth knowing: a comma list of
    /// box-shadows draws each layer from the SAME box, whereas each SwiftUI
    /// `.shadow` blurs whatever it is applied to — including the previous
    /// shadow. For these two-layer neon pairs (a wide wash plus a tight core
    /// of the same hue) the compounding reads as the same soft bloom, which
    /// is why it is accepted rather than worked around with a ZStack of
    /// duplicated content.
    func hudGlow(_ glows: [HudGlow]) -> some View {
        glows.reduce(AnyView(self)) { view, glow in
            AnyView(view.shadow(color: glow.color, radius: glow.radius, x: glow.x, y: glow.y))
        }
    }
}
