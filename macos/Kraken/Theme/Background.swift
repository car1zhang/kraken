import SwiftUI

// Background.swift — the three static window layers.
//
// style.css :258-315 calls them out as exactly that: "Three static layers,
// all pointer-events:none: the grid, the top edge strip, and the scanline
// rake." All three used to move and none does any more — the grid drifted one
// cell over 6s and the rake had a sweeping bar, and the perf round retired
// both, because even a compositor-only mover forces a frame at every vsync
// forever, which with an external monitor attached is system-wide
// WindowServer load. They are STATIC by decision, so nothing here is a timer
// and nothing here reads `SteppedTimer`.
//
// The z-order is the load-bearing part and it is why these are window layers
// rather than per-view decorations:
//
//   grid       CSS z-index -1  — behind every scrap of content
//   edge strip CSS z-index 10  — over the chrome bar (5), under every overlay
//   rake       CSS z-index 60  — over EVERYTHING, modals included, because
//                                scanlines are the CRT glass and glass sits
//                                in front of whatever the screen shows (:302-308)
//
// The rake sitting above the overlays is exactly why `54-scope.md` makes
// overlays in-window ZStack layers rather than NSWindow sheets: a sheet is a
// separate window and no in-window layer can be drawn over it.

/// The 48pt blueprint grid — two crossed 1pt lines per cell at 6% accent
/// (`--gridline`), style.css :282-288.
///
/// A `Canvas` rather than a tiled `Image`: the ink follows the accent, so a
/// tiled bitmap would have to be re-rasterised on every theme or accent
/// change anyway, and the canvas redraws only on resize.
struct BlueprintGrid: View {
    @Environment(\.hudTheme) private var theme

    /// `background-size: 48px 48px`
    var cell: CGFloat = 48

    var body: some View {
        Canvas(opaque: false, rendersAsynchronously: false) { context, size in
            let ink = theme.gridline
            // 1 POINT, not 1/displayScale: a CSS pixel on a retina browser is
            // two device pixels, and so is one SwiftUI point. A true hairline
            // would draw the grid at half the weight the sheet asks for.
            let line: CGFloat = 1
            // The CSS gradient puts its coloured pixel at the START of each
            // cell, so the lines land at 0, 48, 96, … in both axes.
            var x: CGFloat = 0
            while x < size.width {
                context.fill(Path(CGRect(x: x, y: 0, width: line, height: size.height)), with: .color(ink))
                x += cell
            }
            var y: CGFloat = 0
            while y < size.height {
                context.fill(Path(CGRect(x: 0, y: y, width: size.width, height: line)), with: .color(ink))
                y += cell
            }
        }
        .allowsHitTesting(false)
    }
}

/// The 2pt accent gradient strip along the window's top edge, style.css
/// :293-297: `linear-gradient(90deg, var(--ac2), var(--ac) 30%, transparent 60%)`.
///
/// It reads as the top edge of the WINDOW rather than of the header, which is
/// why it lives here with the other window layers and not in the chrome bar.
struct HudTopEdge: View {
    @Environment(\.hudTheme) private var theme

    var body: some View {
        LinearGradient(
            stops: [
                .init(color: theme.ac2.color, location: 0),
                .init(color: theme.ac.color, location: 0.30),
                .init(color: theme.ac.color(0), location: 0.60),
                .init(color: theme.ac.color(0), location: 1),
            ],
            startPoint: .leading,
            endPoint: .trailing
        )
        .frame(height: 2)
        .allowsHitTesting(false)
    }
}

/// The scanline rake — a 1pt line of `--scan` every 4pt, style.css :309-312:
/// `repeating-linear-gradient(0deg, var(--scan) 0 1px, transparent 1px 4px)`.
///
/// Draw this as the LAST layer of the window's ZStack, above the board, the
/// overlays and the toast alike. `allowsHitTesting(false)` is what makes that
/// safe, and it is not optional — without it the rake swallows every click in
/// the app.
///
/// Honours `hudScanlines` (the `▦ FX` toggle, :313-315) itself rather than
/// making the window branch: an off rake renders nothing at all.
struct ScanlineRake: View {
    @Environment(\.hudTheme) private var theme
    @Environment(\.hudScanlines) private var enabled

    /// the gradient's period
    var pitch: CGFloat = 4
    /// the inked part of each period
    var thickness: CGFloat = 1

    var body: some View {
        if enabled {
            Canvas(opaque: false, rendersAsynchronously: false) { context, size in
                let ink = theme.scanColor
                var y: CGFloat = 0
                while y < size.height {
                    context.fill(Path(CGRect(x: 0, y: y, width: size.width, height: thickness)), with: .color(ink))
                    y += pitch
                }
            }
            .allowsHitTesting(false)
        }
    }
}

extension View {
    /// Puts the grid behind the receiver and the edge strip and rake in front
    /// of it, in the sheet's own order. The window's root view calls this
    /// ONCE, around everything including the overlay stack — applying it
    /// anywhere lower would put the rake underneath whatever is above that
    /// point, which is the one thing :302-308 says it must never be.
    func hudWindowLayers() -> some View {
        background(BlueprintGrid())
            .overlay(alignment: .top) { HudTopEdge() }
            .overlay { ScanlineRake() }
    }
}
