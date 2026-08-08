import SwiftUI

// Shapes.swift — the angular corner cuts. The board has no rounded corner
// anywhere: style.css's opening comment states it outright ("Angular
// clip-path cuts stand in for radii"), and the sheet carries exactly eleven
// `clip-path: polygon(…)` declarations, in two families.
//
// FAMILY ONE — a 45° corner CUT, one or two corners chamfered:
//   :690   `.bb-input`          top-left 8      (the board search field)
//   :847   `.card`              top-right 12    (every job card)
//   :1487  toast                top-right 10
//   :1664  `.panel`             top-right 16 + bottom-left 16  (modals)
//   :2032  `#editor-slug`       top-left 6
//   :2088  `.colorsel-btn`      top-left 5
//   :2108  colour-select menu   top-right 8
//   :2308  log scroll row       top-right 14
//
// FAMILY TWO — a trailing SHEAR: the right edge leans, pulling the
// bottom-right corner in by 8px while the top-right stays put:
//   :515   `.viewtab.active`
//   :1733  `.btn.primary`
//   :1755  `.btn-big`
//
// Both conform to `InsettableShape` rather than plain `Shape`, and that is
// the whole reason this file is not four `Path` helpers: `.strokeBorder`
// (which is what keeps a 1px border INSIDE the shape, following the cut edge
// instead of straddling it) is only available on an `InsettableShape`. A
// `.stroke` on a plain Shape centres the line on the path and spills half of
// it outside the clip, which reads as a half-width border on the diagonal.

// MARK: - polygon inset

/// Insets a convex polygon by `d` along each edge's inward normal, returning
/// the new vertices.
///
/// The naive alternative — inset the bounding rect and keep the cut sizes —
/// is wrong on a diagonal, and provably so: offsetting a 45° chamfer inward
/// by `d` shrinks the chamfer by `d(2 − √2)` (offset the chamfer's line by
/// `d` along its normal, intersect with the inset rect's edges, subtract).
/// At the 1px borders this app draws that is a third of a point, which is
/// invisible — but the same formula is not derivable for family two's shear,
/// where the lean angle depends on the control's height, so one correct
/// routine is cheaper than two approximations plus the note explaining them.
///
/// Vertices must be given clockwise in SwiftUI's y-down space, which is what
/// both shapes below produce.
enum PolygonInset {
    static func inset(_ points: [CGPoint], by d: CGFloat) -> [CGPoint] {
        guard d != 0, points.count >= 3 else { return points }
        let n = points.count
        // Each edge, offset inward. In a y-down space a clockwise polygon's
        // interior lies to the LEFT of travel in screen terms, which is the
        // direction (dx, dy) rotated to (-dy, dx).
        var origins = [CGPoint](repeating: .zero, count: n)
        var dirs = [CGPoint](repeating: .zero, count: n)
        for i in 0..<n {
            let a = points[i]
            let b = points[(i + 1) % n]
            var dx = b.x - a.x
            var dy = b.y - a.y
            let len = (dx * dx + dy * dy).squareRoot()
            guard len > 0 else { return points }
            dx /= len
            dy /= len
            let nx = -dy
            let ny = dx
            origins[i] = CGPoint(x: a.x + nx * d, y: a.y + ny * d)
            dirs[i] = CGPoint(x: dx, y: dy)
        }
        // Each vertex is where its two offset edges now meet.
        var out = [CGPoint](repeating: .zero, count: n)
        for i in 0..<n {
            let prev = (i + n - 1) % n
            if let p = intersect(origins[prev], dirs[prev], origins[i], dirs[i]) {
                out[i] = p
            } else {
                // Collinear neighbours (a zero-size cut leaves one): the
                // vertex just slides along its own offset edge.
                out[i] = origins[i]
            }
        }
        return out
    }

    private static func intersect(_ p1: CGPoint, _ d1: CGPoint, _ p2: CGPoint, _ d2: CGPoint) -> CGPoint? {
        let denom = d1.x * d2.y - d1.y * d2.x
        guard abs(denom) > 1e-6 else { return nil }
        let t = ((p2.x - p1.x) * d2.y - (p2.y - p1.y) * d2.x) / denom
        return CGPoint(x: p1.x + d1.x * t, y: p1.y + d1.y * t)
    }
}

extension Path {
    /// A closed polygon through `points`, in order.
    init(hudPolygon points: [CGPoint]) {
        self.init()
        guard let first = points.first else { return }
        move(to: first)
        for p in points.dropFirst() { addLine(to: p) }
        closeSubpath()
    }
}

// MARK: - family one: corner cuts

/// How much of each corner is chamfered, in points. Zero means a square
/// corner, which is what most of them are — every site in the sheet cuts one
/// corner, except the modal panel, which cuts two diagonally opposite ones.
struct CornerCuts: Equatable {
    var topLeft: CGFloat = 0
    var topRight: CGFloat = 0
    var bottomRight: CGFloat = 0
    var bottomLeft: CGFloat = 0

    init(topLeft: CGFloat = 0, topRight: CGFloat = 0, bottomRight: CGFloat = 0, bottomLeft: CGFloat = 0) {
        self.topLeft = topLeft
        self.topRight = topRight
        self.bottomRight = bottomRight
        self.bottomLeft = bottomLeft
    }

    /// `.card` — style.css :847
    static let card = CornerCuts(topRight: 12)
    /// `.panel` (every modal) — style.css :1664
    static let panel = CornerCuts(topRight: 16, bottomLeft: 16)
    /// the toast — style.css :1487
    static let toast = CornerCuts(topRight: 10)
    /// the colour-select drop menu — style.css :2108
    static let colorMenu = CornerCuts(topRight: 8)
    /// a log scroll row — style.css :2308
    static let logRow = CornerCuts(topRight: 14)
    /// `.bb-input`, the board search field — style.css :690
    static let input = CornerCuts(topLeft: 8)
    /// `#editor-slug` — style.css :2032
    static let slugInput = CornerCuts(topLeft: 6)
    /// `.colorsel-btn` — style.css :2088
    static let colorButton = CornerCuts(topLeft: 5)
}

/// A rectangle with any subset of its corners chamfered at 45°.
struct CutCorners: InsettableShape {
    var cuts: CornerCuts
    var insetAmount: CGFloat = 0

    init(_ cuts: CornerCuts) {
        self.cuts = cuts
    }

    init(topLeft: CGFloat = 0, topRight: CGFloat = 0, bottomRight: CGFloat = 0, bottomLeft: CGFloat = 0) {
        self.cuts = CornerCuts(topLeft: topLeft, topRight: topRight, bottomRight: bottomRight, bottomLeft: bottomLeft)
    }

    func path(in rect: CGRect) -> Path {
        Path(hudPolygon: PolygonInset.inset(vertices(in: rect), by: insetAmount))
    }

    func inset(by amount: CGFloat) -> CutCorners {
        var copy = self
        copy.insetAmount += amount
        return copy
    }

    /// Clockwise from the top-left, skipping the vertices a zero-size cut
    /// would duplicate — a repeated point has no edge direction, which is
    /// exactly what the inset routine cannot work with.
    private func vertices(in rect: CGRect) -> [CGPoint] {
        // A cut can never eat more than half of either side, or opposite cuts
        // meet and the polygon self-intersects.
        let limit = min(rect.width, rect.height) / 2
        let tl = min(max(cuts.topLeft, 0), limit)
        let tr = min(max(cuts.topRight, 0), limit)
        let br = min(max(cuts.bottomRight, 0), limit)
        let bl = min(max(cuts.bottomLeft, 0), limit)
        let (x0, y0) = (rect.minX, rect.minY)
        let (x1, y1) = (rect.maxX, rect.maxY)

        var pts: [CGPoint] = []
        pts.append(CGPoint(x: x0 + tl, y: y0))
        pts.append(CGPoint(x: x1 - tr, y: y0))
        if tr > 0 { pts.append(CGPoint(x: x1, y: y0 + tr)) }
        pts.append(CGPoint(x: x1, y: y1 - br))
        if br > 0 { pts.append(CGPoint(x: x1 - br, y: y1)) }
        pts.append(CGPoint(x: x0 + bl, y: y1))
        if bl > 0 { pts.append(CGPoint(x: x0, y: y1 - bl)) }
        if tl > 0 { pts.append(CGPoint(x: x0, y: y0 + tl)) }
        return pts
    }
}

// MARK: - family two: the trailing shear

/// A rectangle whose right edge leans: the top-right corner stays at the full
/// width and the bottom-right pulls in by `shear`. Worn by the active view
/// tab and by every primary (accent-filled) button.
struct ShearTrailing: InsettableShape {
    /// style.css draws all three sites at 8px.
    var shear: CGFloat = 8
    var insetAmount: CGFloat = 0

    init(shear: CGFloat = 8) {
        self.shear = shear
    }

    func path(in rect: CGRect) -> Path {
        let s = min(max(shear, 0), rect.width)
        let pts = [
            CGPoint(x: rect.minX, y: rect.minY),
            CGPoint(x: rect.maxX, y: rect.minY),
            CGPoint(x: rect.maxX - s, y: rect.maxY),
            CGPoint(x: rect.minX, y: rect.maxY),
        ]
        return Path(hudPolygon: PolygonInset.inset(pts, by: insetAmount))
    }

    func inset(by amount: CGFloat) -> ShearTrailing {
        var copy = self
        copy.insetAmount += amount
        return copy
    }
}

// MARK: - convenience

extension View {
    /// Clip to a corner-cut rectangle and stroke a hairline border along the
    /// resulting edge — the two halves of every bordered HUD surface, which
    /// are always applied together and always in this order.
    ///
    /// `lineWidth` defaults to 1 POINT, not `1/displayScale`: the web board's
    /// borders are 1 CSS pixel, which a retina browser paints as 2 device
    /// pixels, and 1 SwiftUI point is the same 2 device pixels. A true
    /// hairline would be half the weight the design asks for.
    func hudCut(_ cuts: CornerCuts, border: Color? = nil, lineWidth: CGFloat = 1) -> some View {
        let shape = CutCorners(cuts)
        return clipShape(shape)
            .overlay {
                if let border {
                    shape.strokeBorder(border, lineWidth: lineWidth)
                }
            }
    }

    /// The shear equivalent of `hudCut`.
    func hudShear(_ shear: CGFloat = 8, border: Color? = nil, lineWidth: CGFloat = 1) -> some View {
        let shape = ShearTrailing(shear: shear)
        return clipShape(shape)
            .overlay {
                if let border {
                    shape.strokeBorder(border, lineWidth: lineWidth)
                }
            }
    }
}
