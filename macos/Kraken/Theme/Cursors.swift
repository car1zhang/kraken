import AppKit
import SwiftUI

// Cursors.swift — the crosshair cursor family.
//
// The board never shows the OS arrow on an interactive surface. style.css
// :317-412 declares the whole family as inline SVG data URIs, in TWO arts,
// each in the board's opposite (near-white on dark, near-black on light):
//
//   rest      — a full crosshair: 2px arms running from 3px of centre out to
//               the edge, plus a small filled dot (r 1.3) at the middle.
//               Worn by `body`, i.e. everything not listed below.
//   act-able  — an open reticle: the arms RETRACT to 6px of centre and a
//               1.8px-stroke ring (r 3.5) floats in the gap they leave. Worn
//               by every clickable, every drag handle AND every text-entry
//               surface — "you can act here" is one statement.
//
// The sheet's `text`/`grab`/`grabbing` rules carry the SAME act-able SVG and
// differ only in the trailing CSS KEYWORD, which is the fallback for a
// browser that refuses the data URI. `PointerStyle.image` has no fallback
// slot, so those three cases collapse onto one art here — deliberately, and
// with no pixel consequence.
//
// The art is 24×24 with the hotspot dead centre at (12, 12), and it is drawn
// in code rather than shipped as an asset: two arts × two themes is four
// images that must stay in step with a stylesheet, and four asset files would
// drift silently. `PointerStyle.image(_:hotSpot:)` is macOS 15.0 exactly,
// which is this app's deployment target.

/// The four roles the sheet distinguishes. `.text` and `.grab` render the
/// same art as `.actable`; they stay separate cases so a future art split
/// needs no call-site change.
enum HudCursor: Hashable {
    case rest
    case actable
    case text
    case grab
}

enum HudCursorArt {
    /// 24 points square, hotspot dead centre — style.css :334
    /// ("24px art, hotspot 12 12").
    static let size = CGSize(width: 24, height: 24)
    /// The hotspot as the stylesheet writes it: pixels from the art's
    /// top-left.
    static let hotSpotPixels = CGPoint(x: 12, y: 12)
    /// The same point in the fractional space `PointerStyle.image` actually
    /// takes — it wants a `UnitPoint`, not pixels. Derived rather than
    /// written as `.center` so the two forms cannot disagree if the art is
    /// ever re-cut.
    static var hotSpot: UnitPoint {
        UnitPoint(x: hotSpotPixels.x / size.width, y: hotSpotPixels.y / size.height)
    }

    /// The two ink colours, taken verbatim from the cursor rules rather than
    /// from `HudTheme.fg`. They happen to equal `--fg` in both modes today,
    /// but the sheet hardcodes them inside the data URIs (a CSS variable
    /// cannot be interpolated into one), so this file copies what the sheet
    /// says, not what it would say if it could.
    static let darkInk = RGB(hex: "#f2edff")   // style.css :336
    static let lightInk = RGB(hex: "#1a1724")  // style.css :353

    // Main-actor isolated rather than lock-guarded: every reader is a SwiftUI
    // view body, and AppKit drawing belongs on the main thread anyway.
    @MainActor private static var cache: [Key: NSImage] = [:]

    private struct Key: Hashable {
        let kind: HudCursor
        let mode: ThemeMode
    }

    /// The rendered art, memoised: a cursor is re-applied on every hover
    /// change across ~70 cards, and re-rasterising four small images on each
    /// of those would be pure waste.
    @MainActor static func nsImage(_ kind: HudCursor, _ mode: ThemeMode) -> NSImage {
        let key = Key(kind: kind, mode: mode)
        if let hit = cache[key] { return hit }
        let made = render(kind, mode)
        cache[key] = made
        return made
    }

    @MainActor static func image(_ kind: HudCursor, _ mode: ThemeMode) -> Image {
        Image(nsImage: nsImage(kind, mode))
    }

    @MainActor private static func render(_ kind: HudCursor, _ mode: ThemeMode) -> NSImage {
        let ink = (mode == .dark ? darkInk : lightInk).color
        let stroke = NSColor(ink)
        // flipped: true so the coordinates below read exactly like the SVG's
        // (y grows downward). Both arts are symmetric about the horizontal
        // centreline, so the flip changes no pixel — it changes only whether
        // the numbers here can be compared to the stylesheet by eye.
        let image = NSImage(size: size, flipped: true) { _ in
            stroke.setStroke()
            stroke.setFill()

            let arms = NSBezierPath()
            arms.lineWidth = 2
            arms.lineCapStyle = .round
            // rest reaches to 3px of centre (:336); act-able retracts to 6 (:350)
            let gap: CGFloat = (kind == .rest) ? 3 : 6
            arms.move(to: CGPoint(x: 12, y: 3))
            arms.line(to: CGPoint(x: 12, y: 12 - gap))
            arms.move(to: CGPoint(x: 12, y: 12 + gap))
            arms.line(to: CGPoint(x: 12, y: 21))
            arms.move(to: CGPoint(x: 3, y: 12))
            arms.line(to: CGPoint(x: 12 - gap, y: 12))
            arms.move(to: CGPoint(x: 12 + gap, y: 12))
            arms.line(to: CGPoint(x: 21, y: 12))
            arms.stroke()

            if kind == .rest {
                // <circle cx="12" cy="12" r="1.3" fill=…>
                let r: CGFloat = 1.3
                NSBezierPath(ovalIn: CGRect(x: 12 - r, y: 12 - r, width: r * 2, height: r * 2)).fill()
            } else {
                // <circle cx="12" cy="12" r="3.5" fill="none" stroke-width="1.8">
                let r: CGFloat = 3.5
                let ring = NSBezierPath(ovalIn: CGRect(x: 12 - r, y: 12 - r, width: r * 2, height: r * 2))
                ring.lineWidth = 1.8
                ring.stroke()
            }
            return true
        }
        image.isTemplate = false
        return image
    }
}

/// Applies the theme-correct cursor art. Reads the mode from the environment
/// rather than taking it as a parameter, so a theme flip repaints every
/// cursor in the window with no call-site touching it.
struct HudCursorModifier: ViewModifier {
    let kind: HudCursor
    @Environment(\.hudTheme) private var theme

    func body(content: Content) -> some View {
        content.pointerStyle(.image(HudCursorArt.image(kind, theme.mode), hotSpot: HudCursorArt.hotSpot))
    }
}

extension View {
    /// The one way a view declares its cursor. New clickables get this
    /// modifier, never an `NSCursor` push — style.css :338-344 records what
    /// happens otherwise: eight surfaces silently regained the OS hand
    /// because a later local declaration won the cascade.
    func hudCursor(_ kind: HudCursor) -> some View {
        modifier(HudCursorModifier(kind: kind))
    }
}
