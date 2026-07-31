import CoreGraphics
import Foundation

// Everything about the GRAPH screen that is a pure function of its data —
// the Swift twin of web/_ui/src/components/graph/graphModel.ts.
//
// It exists for the same reason that file does, one step further along: a
// SwiftUI `Canvas` closure is not reachable from a unit test at all (there is
// no context to hand it and no pixels to read back), so the invalidation
// predicate, the ring fallback, the sizes, the filters, the fact ordering and
// the whole camera live out here and GraphView.swift's canvas holds nothing
// but the drawing calls that consume them.
//
// The semantics are PORTED from graphModel.ts rather than reinvented — the
// two clients draw the same graph, so a node the web view rings must be a
// node this one rings. Where the two differ it is because a phone differs
// from a desktop (touch reach, sheet drawer), never because the rule drifted.
//
// No SwiftUI import: CoreGraphics carries CGPoint/CGSize/CGRect, and keeping
// the view framework out is what lets every function here be called from a
// test with no view hierarchy standing up.

// MARK: - validity

/// Has something superseded this fact?
///
/// Two independent axes, both unix millis and both meaning "0 = not set":
/// `expiredAt` is SYSTEM time (the row was retired) and `validUntil` is WORLD
/// time (the fact stopped holding). Either one is enough. A FUTURE
/// `validUntil` is a fact that is currently true and known to end — "true
/// until March" hasn't ended yet — so it reads as live, which is the exact
/// correction graphModel.ts carries in its own comment.
func graphIsInvalidated(_ e: GraphEdge, now: Int64) -> Bool {
    e.expiredAt > 0 || (e.validUntil > 0 && e.validUntil <= now)
}

/// When it was superseded, unix millis, or 0. System time wins where both
/// axes are stamped: it is the moment the row actually stopped being served.
func graphInvalidatedAt(_ e: GraphEdge) -> Int64 {
    if e.expiredAt > 0 { return e.expiredAt }
    if e.validUntil > 0 { return e.validUntil }
    return 0
}

/// A unix-millis instant as a local calendar day, matching how the web
/// inspector writes an invalidation ("invalidated 2026-07-28").
///
/// Local timezone for the same reason the dream loop's day boundary is local:
/// the reader is in one timezone, always this one. The calendar is pinned to
/// Gregorian rather than taken from `Calendar.current` because the format is a
/// wire-ish `YYYY-MM-DD` shared with the web view — a phone set to the
/// Buddhist or Japanese calendar would otherwise render a different year for
/// the same edge than the board does.
func graphShortDate(ms: Int64) -> String {
    guard ms > 0 else { return "" }
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = TimeZone.current
    let parts = calendar.dateComponents(
        [.year, .month, .day],
        from: Date(timeIntervalSince1970: Double(ms) / 1000)
    )
    guard let year = parts.year, let month = parts.month, let day = parts.day else { return "" }
    return String(format: "%04d-%02d-%02d", year, month, day)
}

// MARK: - size

/// Degree per node id, over EVERY edge regardless of validity or filters.
///
/// Node size is a property of the ENTITY, so letting a filter resize the
/// canvas would make the picture unrecognizable at each toggle — the opposite
/// of a graph navigated by remembered shape. A self-loop touches its node
/// twice and is counted twice, which is what "edges at this node" means.
func graphDegrees(edges: [GraphEdge]) -> [Int64: Int] {
    var out: [Int64: Int] = [:]
    for e in edges {
        out[e.src, default: 0] += 1
        out[e.dst, default: 0] += 1
    }
    return out
}

/// Radius from degree. sqrt so a hub with 200 edges is bigger than one with 9
/// without being 20x the area; clamped at both ends so an isolated node is
/// still tappable and a hub cannot swallow its neighbourhood.
func graphNodeRadius(degree: Int) -> CGFloat {
    guard degree > 0 else { return 4 }
    return min(16, 4 + 2 * CGFloat(Double(degree).squareRoot()))
}

// MARK: - position

/// A node the server has not laid out yet — or one it could not.
private func graphUnpositioned(_ n: GraphNode) -> Bool {
    if !n.x.isFinite || !n.y.isFinite { return true }
    return n.x == 0 && n.y == 0
}

/// Positions for every node, server coordinates preserved and the rest ringed.
///
/// Layout is server-side, so a graph the layout pass has not reached yet
/// arrives with every node at the origin, and drawing all of them on one point
/// reads as "the view is broken" rather than as "no layout yet". The ring sits
/// outside whatever IS positioned, so a partially laid-out graph keeps its
/// real structure in the middle and parks the new arrivals around it.
///
/// Placement is deterministic in the node ORDER the server sent: this view
/// re-polls every 30s, and a random or time-seeded scatter would make the
/// canvas jump under the reader's finger on every tick.
func graphPositions(nodes: [GraphNode]) -> [Int64: CGPoint] {
    var extent: Double = 0
    var missing = 0
    for n in nodes {
        if graphUnpositioned(n) {
            missing += 1
        } else {
            extent = max(extent, (n.x * n.x + n.y * n.y).squareRoot())
        }
    }
    let radius = extent > 0 ? extent * 1.15 : 1
    var out: [Int64: CGPoint] = [:]
    var k = 0
    for n in nodes {
        if !graphUnpositioned(n) {
            out[n.id] = CGPoint(x: n.x, y: n.y)
            continue
        }
        let angle = missing > 0 ? (2 * Double.pi * Double(k)) / Double(missing) : 0
        k += 1
        out[n.id] = CGPoint(x: radius * cos(angle), y: radius * sin(angle))
    }
    return out
}

// MARK: - filtering

/// Every type present in the payload, in first-seen order — the order the
/// chip row renders, which is therefore stable across polls of an unchanged
/// graph rather than alphabetical-by-accident.
func graphTypeList(nodes: [GraphNode]) -> [String] {
    var seen: Set<String> = []
    var out: [String] = []
    for n in nodes where !seen.contains(n.type) {
        seen.insert(n.type)
        out.append(n.type)
    }
    return out
}

/// The node ids the type chips have switched off. A hidden-SET rather than a
/// visible-set so a type the extraction agent invents tomorrow shows up by
/// default instead of silently never rendering.
func graphHiddenNodeIDs(nodes: [GraphNode], hiddenTypes: Set<String>) -> Set<Int64> {
    var out: Set<Int64> = []
    for n in nodes where hiddenTypes.contains(n.type) { out.insert(n.id) }
    return out
}

/// An edge is drawn only if BOTH its endpoints are, and only if its validity
/// passes the toggle: an edge whose source the chip row switched off must go
/// with it, or the canvas fills with lines to nothing.
func graphEdgeVisible(
    _ e: GraphEdge, hiddenNodeIDs: Set<Int64>, includeInvalidated: Bool, now: Int64
) -> Bool {
    if hiddenNodeIDs.contains(e.src) || hiddenNodeIDs.contains(e.dst) { return false }
    if !includeInvalidated && graphIsInvalidated(e, now: now) { return false }
    return true
}

// MARK: - the drawer's facts

/// Live facts first, then invalidated ones; strength descending within each
/// group; input order preserved on a tie.
///
/// Stability is spelled out with an index rather than left to `sorted(by:)`,
/// which Swift does not guarantee: the drawer re-renders on every poll, and an
/// unstable sort would shuffle equal-strength facts under the reader's eye.
func graphSortFacts(_ edges: [GraphEdge], now: Int64) -> [GraphEdge] {
    edges.enumerated()
        .sorted { a, b in
            let ai = graphIsInvalidated(a.element, now: now) ? 1 : 0
            let bi = graphIsInvalidated(b.element, now: now) ? 1 : 0
            if ai != bi { return ai < bi }
            if a.element.strength != b.element.strength { return a.element.strength > b.element.strength }
            return a.offset < b.offset
        }
        .map(\.element)
}

/// A fact's endpoint, named. `#<id>` when the payload does not carry that node
/// — the detail route returns every edge touching a node, including ones whose
/// other end a type filter or a mid-ingest payload left out.
func graphEndpointName(id: Int64, names: [Int64: String]) -> String {
    if let name = names[id], !name.isEmpty { return name }
    return "#\(id)"
}

/// What one fact row says. `fact` is the sentence the extraction agent wrote —
/// the edge's short description IS its name — and predicate plus the other
/// endpoint is what the row degrades to when that sentence is blank, so a
/// half-filled edge still reads as something rather than as an empty line.
func graphFactText(_ e: GraphEdge, names: [Int64: String]) -> String {
    if !e.fact.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return e.fact }
    return "\(e.predicate) \(graphEndpointName(id: e.dst, names: names))"
        .trimmingCharacters(in: .whitespacesAndNewlines)
}

// MARK: - labels

/// One label a frame's node pass decided it wanted, before the cap has had its
/// say. Collected rather than drawn on the spot because which labels win is
/// not knowable until every node has been visited.
struct GraphLabel: Equatable {
    let id: Int64
    /// Screen radius of the node this names — the rank key.
    let radius: CGFloat
    /// Where the label's top-centre goes, in screen space.
    let at: CGPoint
    let text: String
}

/// The labels a frame actually draws: the `cap` largest, ties broken by id.
///
/// The id tiebreak is not cosmetic. Degree is a small integer, so equal radii
/// are the common case rather than the exception, and any order that is not
/// TOTAL would let two equal-radius nodes trade the last slot from frame to
/// frame — which reads as a label flickering on and off under a stationary
/// finger, with the graph not moving. Same input, same output, always.
///
/// A non-positive cap draws nothing, which is the honest reading of "at most
/// zero" rather than a reason to ignore the argument.
func graphLabelSelection(_ candidates: [GraphLabel], cap: Int) -> [GraphLabel] {
    guard cap > 0 else { return [] }
    let ordered = candidates.sorted {
        $0.radius == $1.radius ? $0.id < $1.id : $0.radius > $1.radius
    }
    return Array(ordered.prefix(cap))
}

// MARK: - camera

/// The world→screen transform: uniform scale then translate, no rotation.
///
/// A value type rather than state scattered across the view so the pan/pinch
/// arithmetic is testable without a gesture recognizer, and so a fit-reset is
/// an assignment rather than a sequence of mutations.
struct GraphCamera: Equatable {
    var scale: CGFloat
    var offset: CGSize

    func worldToScreen(_ p: CGPoint) -> CGPoint {
        CGPoint(x: p.x * scale + offset.width, y: p.y * scale + offset.height)
    }

    func screenToWorld(_ p: CGPoint) -> CGPoint {
        // A zero scale has no inverse; returning the origin keeps a NaN out of
        // the camera, which would otherwise poison every later transform and
        // blank the canvas with nothing logged.
        guard scale != 0 else { return .zero }
        return CGPoint(x: (p.x - offset.width) / scale, y: (p.y - offset.height) / scale)
    }

    mutating func pan(by delta: CGSize) {
        offset.width += delta.width
        offset.height += delta.height
    }

    /// Zooms about a SCREEN point, keeping whatever is under the fingers under
    /// the fingers. Deliberately unclamped: the clamp belongs to the gesture
    /// layer (GraphView holds the 0.05…20 range the web view's sigma camera
    /// uses), and clamping here would silently break the anchor invariant that
    /// is this method's whole obligation.
    mutating func pinch(by factor: CGFloat, anchor: CGPoint) {
        let world = screenToWorld(anchor)
        scale *= factor
        offset = CGSize(
            width: anchor.x - world.x * scale,
            height: anchor.y - world.y * scale
        )
    }

    /// A camera framing `worldRect` inside `viewport`, centered.
    ///
    /// The 0.9 margin is what makes "all four corners inside the viewport"
    /// true rather than true-to-the-pixel: an exact fit puts the extreme nodes
    /// ON the edge, where a node's own radius and its label hang off-screen.
    static func fitting(worldRect: CGRect, viewport: CGSize) -> GraphCamera {
        let center = CGPoint(x: worldRect.midX, y: worldRect.midY)
        // A single point (or a rect with no extent on either axis) has no
        // scale to derive — 1 centered on it is the only honest answer, and
        // it takes no margin: there is nothing to keep off the edge.
        var scale: CGFloat = 1
        let fitsWidth = worldRect.width > 0 && viewport.width > 0
        let fitsHeight = worldRect.height > 0 && viewport.height > 0
        if fitsWidth && fitsHeight {
            scale = min(viewport.width / worldRect.width, viewport.height / worldRect.height) * 0.9
        } else if fitsWidth {
            scale = (viewport.width / worldRect.width) * 0.9
        } else if fitsHeight {
            scale = (viewport.height / worldRect.height) * 0.9
        }
        if !scale.isFinite { scale = 1 }
        scale = min(max(scale, graphMinScale), graphMaxScale)
        return GraphCamera(
            scale: scale,
            offset: CGSize(
                width: viewport.width / 2 - center.x * scale,
                height: viewport.height / 2 - center.y * scale
            )
        )
    }
}

/// The camera's zoom range, mirroring the web view's `minCameraRatio` /
/// `maxCameraRatio`. Enforced by `GraphCamera.fitting` and by GraphView's
/// gesture layer; see `pinch`'s comment for why not inside `pinch` itself.
let graphMinScale: CGFloat = 0.05
let graphMaxScale: CGFloat = 20

/// The node under a tap, or nil.
///
/// Nearest-in-SCREEN-space within `maxDistance`, and the node's own radius
/// deliberately does not extend that reach: a finger needs the same generous
/// target on a leaf as on a hub, and letting a hub's 16pt radius add to its
/// catchment would make it eat its neighbours' taps at every zoom level.
func graphHitTest(
    screenPoint: CGPoint, positions: [Int64: CGPoint], camera: GraphCamera, maxDistance: CGFloat
) -> Int64? {
    var bestID: Int64?
    var bestDistance = CGFloat.greatestFiniteMagnitude
    for (id, world) in positions {
        let p = camera.worldToScreen(world)
        let dx = p.x - screenPoint.x
        let dy = p.y - screenPoint.y
        let d = (dx * dx + dy * dy).squareRoot()
        if d < bestDistance {
            bestDistance = d
            bestID = id
        }
    }
    guard let bestID, bestDistance <= maxDistance else { return nil }
    return bestID
}
