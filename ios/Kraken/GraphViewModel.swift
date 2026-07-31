import CoreGraphics
import Foundation
import SwiftUI

// The GRAPH screen's state: the payload, the filters, the selection, the two
// network reads that feed them, and the derived render model the canvas draws.
//
// Split out of GraphView.swift rather than nested in it so a test can drive
// the whole selection/fallback dance with no view standing up — the two
// fetches arrive as CLOSURES for exactly that reason, and the production
// `init()` below is the only place APIClient is named.
//
// `ObservableObject` + `@Published` rather than the `@Observable` macro every
// other view model in the app uses (DreamView, SessionsView, UsageView…), and
// the honest reason is that the frozen contract for this slice spells the type
// out that way — not that the macro could not do the job. Two things that
// might look like technical reasons are not: nothing here is bound two-way
// (both chips mutate through Button actions; there is no `$vm` anywhere in
// GraphView.swift), and the `didSet` hook that drives the render rebuild below
// works under `@Observable` too — checked this session with a standalone
// probe, where a `didSet` on an `@Observable` stored property both compiles
// and fires. The one real difference runs the other way: `@Observable` tracks
// per-property, so a view reading only `render` would not re-render when
// `errorText` changes, while `objectWillChange` invalidates every observer.
// Worth revisiting if this screen ever gets hot; it is not a bug today.

/// Everything one frame of the canvas needs, derived ONCE per rebuild.
///
/// Its inputs are four: the payload, the two filters, and the CLOCK — `now` is
/// sampled at rebuild time and every validity question in the pass is asked
/// against it, so a fact whose `valid_until` passes between two rebuilds keeps
/// rendering as current until the next one (at most one poll, 30s).
///
/// It lives on the view model rather than being computed in the view's body
/// for a measured reason: the camera is `@State` on GraphView, so a pan or a
/// pinch re-evaluates that body on every frame of the gesture. A body-computed
/// render would redo the positions, the degrees and both filter passes sixty
/// times a second on a graph heading for thousands of nodes. The camera is
/// deliberately NOT an input, which is what makes the caching sound: it
/// changes what the canvas DRAWS, never what it draws FROM.
struct GraphRender: Equatable {
    /// Type-filtered — what the canvas draws.
    let nodes: [GraphNode]
    /// Type- and validity-filtered.
    let edges: [GraphEdge]
    // The four maps below are built over the WHOLE payload, never the filtered
    // set: a position and a degree are properties of the ENTITY, and letting a
    // chip toggle move or resize the surviving nodes would make the picture
    // unrecognizable at each tap.
    let positions: [Int64: CGPoint]
    let degrees: [Int64: Int]
    let names: [Int64: String]
    let nodesByID: [Int64: GraphNode]
    /// Type → fill, resolved once per rebuild. Bridging a UIColor through
    /// `Color(uiColor:)` per node per frame is pure waste on a canvas that
    /// redraws under a finger.
    let typeColors: [String: Color]
    /// Present types in first-seen order — the chip row.
    let types: [String]
    /// Edges that are current by `graphIsInvalidated`, counted over the whole
    /// payload for the header.
    let currentEdges: Int
    /// The instant every validity question on this screen is asked at, so the
    /// canvas, the header count and the drawer cannot answer differently
    /// within one pass. 0 before the first payload, where nothing renders.
    let now: Int64

    static let empty = GraphRender(
        nodes: [], edges: [], positions: [:], degrees: [:], names: [:],
        nodesByID: [:], typeColors: [:], types: [], currentEdges: 0, now: 0
    )

    /// The world rectangle enclosing every drawable node — what a fit-reset
    /// frames. nil when there is nothing to frame.
    var worldBounds: CGRect? {
        var minX = Double.greatestFiniteMagnitude
        var minY = Double.greatestFiniteMagnitude
        var maxX = -Double.greatestFiniteMagnitude
        var maxY = -Double.greatestFiniteMagnitude
        var any = false
        for n in nodes {
            guard let p = positions[n.id] else { continue }
            any = true
            minX = min(minX, p.x)
            minY = min(minY, p.y)
            maxX = max(maxX, p.x)
            maxY = max(maxY, p.y)
        }
        guard any else { return nil }
        return CGRect(x: minX, y: minY, width: maxX - minX, height: maxY - minY)
    }
}

@MainActor
final class GraphViewModel: ObservableObject {
    @Published private(set) var payload: GraphPayload?
    @Published private(set) var errorText: String?
    @Published private(set) var selectedID: Int64?
    @Published private(set) var detail: GraphNodeDetail?
    /// Types the chip row has switched OFF. Empty by default — see
    /// graphHiddenNodeIDs for why a hidden-set rather than a visible one.
    @Published var hiddenTypes: Set<String> = [] { didSet { rebuildRender() } }
    /// false = current facts only, which is what the canvas opens on.
    @Published var includeInvalidated = false { didSet { rebuildRender() } }
    /// The derived draw model. Rebuilt from the payload, the two filters above
    /// and the clock — those four and nothing else; see `GraphRender`.
    @Published private(set) var render = GraphRender.empty

    private let fetchGraph: (String) async throws -> GraphPayload
    private let fetchNode: (String, Int64) async throws -> GraphNodeDetail
    /// The base URL of the last `load`. A selection fetch has no base URL of
    /// its own — the screen holds one — and remembering the last load's is
    /// what keeps `select` a plain synchronous call from a tap handler.
    private var lastBaseURL: String?
    /// Cancels an in-flight detail read when the selection moves on, so a slow
    /// response for the previous node can never land on the current one.
    private var detailTask: Task<Void, Never>?
    /// True from `select` until that selection's detail read settles. The
    /// stranded-selection sweep in `load` reads it: a node absent from the
    /// payload is a legitimate mid-flight state for a tap-through, and
    /// deselecting there would cancel the very read that would have shown it.
    private var detailPending = false

    init(
        fetchGraph: @escaping (String) async throws -> GraphPayload,
        fetchNode: @escaping (String, Int64) async throws -> GraphNodeDetail
    ) {
        self.fetchGraph = fetchGraph
        self.fetchNode = fetchNode
    }

    /// The production wiring. Separate from the designated init so the app has
    /// one line to write and tests never touch the network.
    convenience init() {
        self.init(
            fetchGraph: { try await APIClient.shared.fetchGraph(baseURL: $0) },
            fetchNode: { try await APIClient.shared.fetchGraphNode(baseURL: $0, id: $1) }
        )
    }

    /// Refreshes the whole payload. Called once on appear and every 30s while
    /// the screen is up.
    ///
    /// A failure KEEPS the previous payload and says so on a line above the
    /// canvas: this polls, so a single dropped request would otherwise blank a
    /// graph the reader is mid-way through navigating and throw away the pan
    /// and zoom that got them there.
    func load(baseURL: String) async {
        lastBaseURL = baseURL
        do {
            payload = try await fetchGraph(baseURL)
            errorText = nil
            rebuildRender()
            dropStrandedSelection()
        } catch {
            // A CANCELLED load is not a failure: the screen going away, or the
            // server URL changing under `.task(id:)`, cancels the request
            // in flight and would otherwise write "couldn't refresh: cancelled"
            // across the header of a screen that is doing exactly as told. The
            // task is asked rather than the error inspected because APIClient
            // wraps every URLError into a `KrakenError.message` built from its
            // localizedDescription, so by the time it arrives here the
            // cancellation is a STRING and has no type left to match on. The
            // second clause is for the injected-closure seam, not for that
            // path: a test's `fetchGraph` can throw a `CancellationError`
            // directly, where nothing has flattened it.
            guard !Task.isCancelled, !(error is CancellationError) else { return }
            errorText = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    /// Selects a node (nil deselects) and starts its detail read.
    ///
    /// Synchronous because it is called from a tap: the selection must paint
    /// on the same frame as the tap, and the aliases-and-every-edge read that
    /// enriches the drawer follows behind it.
    func select(_ id: Int64?) {
        // Re-selecting the node already open — an endpoint tap-through onto
        // the node itself, a second tap on the same circle — must NOT throw
        // away a detail that has already landed: clearing it would drop the
        // drawer back to the payload's smaller fact list and refetch, which
        // reads as a flicker for no new information.
        if let id, id == selectedID, detail != nil { return }
        detailTask?.cancel()
        detailTask = nil
        detailPending = false
        selectedID = id
        // Cleared rather than kept: a stale detail belongs to the PREVIOUS
        // node, and `selectedNode`/`selectedFacts` both fall back to the
        // payload, so the drawer is already correct with nothing here.
        detail = nil
        guard let id, let baseURL = lastBaseURL else { return }
        detailPending = true
        detailTask = Task { [weak self] in
            guard let self else { return }
            let fetched = try? await self.fetchNode(baseURL, id)
            guard !Task.isCancelled, self.selectedID == id else { return }
            self.detailPending = false
            guard let fetched else { return }
            self.detail = fetched
        }
        // A detail failure deliberately does NOT touch `errorText`: the
        // payload fallback below IS the degraded mode, and an error line for
        // a read whose only unique contribution is the alias list would
        // report a broken screen that is working.
    }

    /// The selected entity. The detail read wins where it has an answer (it is
    /// the only source of aliases); the payload node is the fallback while it
    /// is in flight, or after it failed.
    var selectedNode: GraphNode? {
        guard let selectedID else { return nil }
        if let node = detail?.node, node.id == selectedID { return node }
        return payload?.nodes.first { $0.id == selectedID }
    }

    /// Every fact touching the selected node, ordered for the drawer.
    ///
    /// Deliberately UNFILTERED by the chip row and the invalidated toggle: the
    /// chips say what the CANVAS draws, while the drawer says everything known
    /// about the one node the reader asked about, which is the whole reason to
    /// tap it.
    var selectedFacts: [GraphEdge] {
        guard let selectedID else { return [] }
        // The render's instant, so the live/invalidated SPLIT this sort makes
        // and the strike-through the drawer paints from the same predicate
        // cannot land on opposite sides of a fact expiring mid-pass. Falls
        // back to the clock only before the first payload, where there is no
        // render to share.
        let now = render.now > 0 ? render.now : graphNow()
        if let detail, detail.node.id == selectedID {
            return graphSortFacts(detail.edges, now: now)
        }
        let own = (payload?.edges ?? []).filter { $0.src == selectedID || $0.dst == selectedID }
        return graphSortFacts(own, now: now)
    }

    /// How many edges the drawer should claim for the selected node. The
    /// detail read is authoritative where it applies — it returns every edge
    /// touching the node, including ones whose other endpoint the payload does
    /// not carry, which is exactly the case where the payload's degree count
    /// says 0 above a non-empty fact list.
    ///
    /// Both branches go through `graphDegrees` so they count in the SAME unit.
    /// A bare `detail.edges.count` would not: that counts ROWS, while a degree
    /// counts ENDPOINTS, and the two differ on a self-loop — a node with one
    /// self-loop would read "2 edges" from the payload and then flip to "1"
    /// the instant the detail landed, a number changing under the reader with
    /// nothing having happened.
    func edgeCount(for node: GraphNode) -> Int {
        if let detail, detail.node.id == node.id {
            return graphDegrees(edges: detail.edges)[node.id] ?? 0
        }
        return render.degrees[node.id] ?? 0
    }

    // MARK: - derivation

    private func rebuildRender() {
        let now = graphNow()
        let allNodes = payload?.nodes ?? []
        let allEdges = payload?.edges ?? []
        let hidden = graphHiddenNodeIDs(nodes: allNodes, hiddenTypes: hiddenTypes)
        var names: [Int64: String] = [:]
        var byID: [Int64: GraphNode] = [:]
        for n in allNodes {
            names[n.id] = n.name
            byID[n.id] = n
        }
        let types = graphTypeList(nodes: allNodes)
        var colors: [String: Color] = [:]
        for t in types { colors[t] = Color(uiColor: KTheme.graphTypeColorUI(t)) }
        render = GraphRender(
            nodes: allNodes.filter { !hidden.contains($0.id) },
            edges: allEdges.filter {
                graphEdgeVisible(
                    $0, hiddenNodeIDs: hidden,
                    includeInvalidated: includeInvalidated, now: now
                )
            },
            positions: graphPositions(nodes: allNodes),
            degrees: graphDegrees(edges: allEdges),
            names: names,
            nodesByID: byID,
            typeColors: colors,
            types: types,
            currentEdges: allEdges.filter { !graphIsInvalidated($0, now: now) }.count,
            now: now
        )
    }

    /// Closes the drawer when the poll it just finished no longer knows the
    /// selected entity. A `/graph` revision job can retire an entity between
    /// two polls, and a drawer left open on one is a page of facts about
    /// something the canvas no longer draws, with no node to tap to escape it.
    ///
    /// The payload is the ONLY authority here, and a landed detail is
    /// deliberately not allowed to veto: the retired-entity case is exactly
    /// the one where a detail for the vanished node has already landed, so a
    /// detail-shaped exemption would pin the drawer open forever in the case
    /// this exists to close. Nothing is lost by that, because `GET /api/graph`
    /// returns EVERY node — anything the detail route can name, the next
    /// payload carries too — and the one genuinely mid-flight case, a
    /// tap-through whose read has not settled, is held off by `detailPending`.
    ///
    /// Related and minor, and accepted rather than fixed: a tap-through onto a
    /// node that is genuinely gone sits on the drawer's "no details yet" line
    /// until the next successful poll sweeps it, rather than closing at once.
    private func dropStrandedSelection() {
        guard let id = selectedID, !detailPending else { return }
        if payload?.nodes.contains(where: { $0.id == id }) == true { return }
        select(nil)
    }
}

/// Wall-clock in unix millis. One function so no surface on this screen
/// derives "now" a second way; within a pass they share a single reading —
/// `GraphRender.now`, which the canvas, the header and the drawer are all
/// handed — rather than each sampling the clock.
func graphNow() -> Int64 {
    Int64(Date().timeIntervalSince1970 * 1000)
}
