import CoreGraphics
import Foundation
import QuartzCore
import SwiftUI
import UIKit

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

    /// The same render with a new position map.
    ///
    /// The simulation moves nodes sixty times a second and nothing else about
    /// the render changes while it does — not the filters, not the colours,
    /// not the degrees. Rebuilding the whole thing per frame would redo two
    /// filter passes and a colour resolve for a graph heading for thousands of
    /// nodes; this copies the eight fields that did not move and swaps the one
    /// that did.
    func withPositions(_ positions: [Int64: CGPoint]) -> GraphRender {
        GraphRender(
            nodes: nodes, edges: edges, positions: positions, degrees: degrees,
            names: names, nodesByID: nodesByID, typeColors: typeColors, types: types,
            currentEdges: currentEdges, now: now
        )
    }

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
    /// and the clock — those four and nothing else; see `GraphRender`. Its
    /// POSITIONS are the one part the simulation writes between rebuilds.
    @Published private(set) var render = GraphRender.empty
    /// The accessibility setting. Seeded from `UIAccessibility` at
    /// construction so the FIRST payload is already governed by it, then kept
    /// live by the view, which is the only half of this screen with an
    /// `@Environment` to watch it through. Turning it on tears the simulation
    /// down; turning it off builds one.
    @Published var reduceMotion: Bool {
        didSet { if oldValue != reduceMotion { syncPhysics() } }
    }

    /// The live simulation, or nil when physics is off — over the node cap, or
    /// reduce-motion. Readable so a test can drive it and assert on it
    /// directly; only this class ever calls its mutating half.
    private(set) var physics: GraphPhysics?
    /// What the current `physics` was built for. A poll that changes nothing
    /// about the visible node set or the visible edges must leave the
    /// simulation exactly where it is, so the rebuild is keyed on this rather
    /// than on the payload: an unchanged poll would otherwise restart a
    /// settled graph thirty seconds after it went to sleep, every time.
    private var membership: GraphPhysicsMembership?
    /// Positions the SIMULATION owns, id → world point.
    ///
    /// It survives every poll and every filter toggle, which is what makes two
    /// separate obligations hold: a reload keeps dragged nodes where the
    /// reader left them rather than snapping back to the server layout, and a
    /// type switched back on re-enters at its last simulated position rather
    /// than at the origin.
    private var simPositions: [Int64: CGPoint] = [:]
    /// The server layout, straight from `graphPositions` — the seed the map
    /// above is laid over.
    private var serverPositions: [Int64: CGPoint] = [:]
    /// server ∪ simulated: what the canvas draws, kept as one map so a frame
    /// is one dictionary write per moved node rather than a re-merge.
    private var livePositions: [Int64: CGPoint] = [:]

    private var displayLink: CADisplayLink?
    private var tickProxy: GraphTickProxy?
    /// Whether the screen is up and wants frames — see `startDisplayLoop`.
    private var wantsFrames = false

    /// A pin the gesture layer has taken and not yet given back.
    private struct HeldPin {
        /// Where it is pinned, in world coordinates. Kept so a simulation
        /// rebuilt mid-drag can be handed the pin again.
        var point: CGPoint
        /// Frames since the gesture last said anything about it.
        var idleFrames: Int
    }

    /// Live pins, keyed by node id. The view model's own record of what the
    /// gesture layer is holding — the engine knows a node is pinned but not
    /// which gesture put it there, and neither knows when a gesture dies.
    private var heldPins: [Int64: HeldPin] = [:]

    /// Frames a pin may go unrefreshed before it is assumed abandoned. See
    /// `expireAbandonedPins` for why it is this loose rather than tighter.
    private static let abandonedPinFrames = 300

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
        fetchNode: @escaping (String, Int64) async throws -> GraphNodeDetail,
        reduceMotion: Bool = false
    ) {
        self.fetchGraph = fetchGraph
        self.fetchNode = fetchNode
        self.reduceMotion = reduceMotion
    }

    /// The production wiring. Separate from the designated init so the app has
    /// one line to write and tests never touch the network.
    convenience init() {
        self.init(
            fetchGraph: { try await APIClient.shared.fetchGraph(baseURL: $0) },
            fetchNode: { try await APIClient.shared.fetchGraphNode(baseURL: $0, id: $1) },
            // Read HERE rather than left to the view's `onAppear`, which is
            // the difference between honouring reduce-motion and honouring it
            // a beat late. The first payload can land before that callback
            // runs, and it is the first payload that builds the simulation —
            // so a reader who asked the system for no motion got a graph that
            // settled in front of them once, on entry, every time. The view
            // still pushes the value on appear and on change; this is only
            // about the state the model starts in.
            reduceMotion: UIAccessibility.isReduceMotionEnabled
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

    /// The selected entity. The detail read wins where it has an answer (it
    /// serves the node's edge list pre-cut server-side); the payload node —
    /// aliases included — is the fallback while it is in flight or failed.
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
        // The server layout is the SEED; wherever the simulation has since
        // moved a node, its position wins. Simulated positions for entities
        // the payload no longer carries are dropped here and nowhere else —
        // this map is otherwise append-only, and a `/graph` revision job
        // retiring entities all week would grow it without bound.
        serverPositions = graphPositions(nodes: allNodes)
        simPositions = simPositions.filter { serverPositions[$0.key] != nil }
        var live = serverPositions
        for (id, p) in simPositions { live[id] = p }
        livePositions = live
        render = GraphRender(
            nodes: allNodes.filter { !hidden.contains($0.id) },
            edges: allEdges.filter {
                graphEdgeVisible(
                    $0, hiddenNodeIDs: hidden,
                    includeInvalidated: includeInvalidated, now: now
                )
            },
            positions: livePositions,
            degrees: graphDegrees(edges: allEdges),
            names: names,
            nodesByID: byID,
            typeColors: colors,
            types: types,
            currentEdges: allEdges.filter { !graphIsInvalidated($0, now: now) }.count,
            now: now
        )
        syncPhysics()
    }

    // MARK: - physics

    /// Should the per-frame loop run another frame?
    ///
    /// The single predicate the display link is started and stopped from: an
    /// idle screen — settled, nothing pinned, or physics off entirely —
    /// schedules no per-frame work at all.
    var physicsShouldTick: Bool { physics?.isAwake ?? false }

    /// Builds, rebuilds or tears down the simulation to match what the canvas
    /// currently draws. Called at the end of every render rebuild, which is
    /// the only thing that can change any of its inputs.
    private func syncPhysics() {
        guard graphPhysicsAllowed(visibleNodes: render.nodes.count, reduceMotion: reduceMotion) else {
            physics = nil
            membership = nil
            stopTicking()
            return
        }
        let wanted = currentMembership()
        // The early return is the whole of "an unchanged poll does not restart
        // the simulation": same visible nodes, same visible edges, same
        // engine, untouched — so a settled graph stays asleep across a poll.
        if wanted == membership, physics != nil { return }
        membership = wanted
        // Seeded from the LIVE positions rather than the payload's, so a
        // rebuild — a new node arriving, a type chip toggled — carries every
        // existing node forward from where the simulation had already put it.
        // A node the map has never seen enters at its server position, which
        // is the only place it can honestly start.
        //
        // Deduplicated for the reason `nodes` below is: a repeated id would
        // put two rows in the engine under one name, and every lookup — the
        // pin, the position write-back — would reach a different one of them
        // than the reader is looking at.
        var seen: Set<Int64> = []
        var seeds: [(id: Int64, x: CGFloat, y: CGFloat, radius: CGFloat)] = []
        seeds.reserveCapacity(render.nodes.count)
        for n in render.nodes where seen.insert(n.id).inserted {
            let p = livePositions[n.id] ?? .zero
            seeds.append((
                id: n.id, x: p.x, y: p.y,
                radius: graphNodeRadius(degree: render.degrees[n.id] ?? 0)
            ))
        }
        physics = GraphPhysics(
            nodes: seeds,
            links: wanted.links.map { (src: $0.src, dst: $0.dst, occurrences: $0.occurrences) }
        )
        // A rebuild must not drop a finger. Membership changes for reasons
        // that have nothing to do with the gesture — a poll delivering one new
        // node, a chip toggled with the other hand — and the fresh engine
        // knows nothing about the pin, so the node it was holding would be
        // handed straight back to the forces and walk out from under a
        // stationary finger. Re-pinning at the point the pin is still at costs
        // nothing when there is no drag, since the map is then empty.
        for id in heldPins.keys.sorted() {
            if let pin = heldPins[id] { physics?.drag(id: id, to: pin.point) }
        }
        startTicking()
    }

    /// The visible nodes and the visible edges — what the simulation is built
    /// from, and therefore exactly what a change to it must be detected on.
    /// Filtered-out nodes and edges are ABSENT rather than hidden: a node
    /// nobody can see exerting charge would push the visible ones around an
    /// invisible obstacle.
    ///
    /// Both lists are SORTED, which is the difference between a membership
    /// test and an accident. Equality here is over sequences, so an unsorted
    /// comparison says "this changed" whenever the server merely returns the
    /// same graph in a different order — and the only reason it does not today
    /// is that `internal/store/graph.go` happens to `ORDER BY`. That is an
    /// undeclared dependency three layers down on the other side of a wire
    /// protocol, and the failure it would cause is silent: the simulation
    /// would go back to restarting on every 30 s poll with nothing logged and
    /// nothing to see but a graph that never quite settles.
    private func currentMembership() -> GraphPhysicsMembership {
        var seen: Set<Int64> = []
        var ids: [Int64] = []
        ids.reserveCapacity(render.nodes.count)
        for n in render.nodes where seen.insert(n.id).inserted { ids.append(n.id) }
        let links = render.edges
            .map { GraphPhysicsLink(src: $0.src, dst: $0.dst, occurrences: $0.occurrences) }
            // Endpoints are NOT normalised into a canonical order along with
            // this, and the reason is that there is nothing to normalise
            // AGAINST. Sorting exists to absorb a change in ROW order; `src`
            // and `dst` are two stored columns of one row, so they cannot
            // reorder — a row that comes back flipped is the server saying the
            // fact points the other way, which is a content change and ought
            // to rebuild.
            //
            // Normalising would be harmless, just pointless: an endpoint swap
            // is invisible to the engine. d3's link force negates the impulse
            // when the pair is swapped and turns the degree bias `b` into
            // `1 - b`, and the two write-backs cancel both changes exactly —
            // measured here at 200 ticks on a degree-6 hub joined to a
            // degree-1 leaf, whose positions came out bit-identical in either
            // orientation.
            .sorted {
                ($0.src, $0.dst, $0.occurrences) < ($1.src, $1.dst, $1.occurrences)
            }
        return GraphPhysicsMembership(nodes: ids.sorted(), links: links)
    }

    /// One frame: advance the simulation and publish what it moved.
    ///
    /// Public because the display link is not the only caller that matters —
    /// it is also the seam a test drives the engine through, since the render
    /// only learns about a tick from here.
    func tickPhysics() {
        guard let physics else { return }
        expireAbandonedPins()
        physics.tick()
        for n in physics.nodes() {
            let p = CGPoint(x: n.x, y: n.y)
            simPositions[n.id] = p
            livePositions[n.id] = p
        }
        render = render.withPositions(livePositions)
        if !physicsShouldTick { stopTicking() }
    }

    /// Moves a node to a WORLD point under the reader's finger.
    ///
    /// With physics on this pins it in the simulation, so its linked
    /// neighbours follow. With physics off it is a plain position override on
    /// that one node: the reduced-motion reader still gets to move a node out
    /// of the way, they just get no cascade and nothing wakes.
    ///
    /// Nothing here writes anywhere. Drags are ephemeral per Q12 — `kg_layout`
    /// stays daemon-owned and a relaunch reseeds from the server.
    func dragNode(id: Int64, to point: CGPoint) {
        if let physics {
            physics.drag(id: id, to: point)
            heldPins[id] = HeldPin(point: point, idleFrames: 0)
            startTicking()
            return
        }
        guard livePositions[id] != nil else { return }
        simPositions[id] = point
        livePositions[id] = point
        render = render.withPositions(livePositions)
    }

    /// The gesture layer's spelling of the same call: it has a screen point
    /// and the camera, and this is the one place the two become a world point.
    func dragNode(id: Int64, toScreen point: CGPoint, camera: GraphCamera) {
        dragNode(id: id, to: camera.screenToWorld(point))
    }

    /// Ends a node drag. The node keeps the velocity the finger gave it and
    /// glides into its new equilibrium; with physics off there is no pin to
    /// end and this does nothing.
    func releaseNode(id: Int64) {
        physics?.release(id: id)
        heldPins.removeValue(forKey: id)
        startTicking()
    }

    /// Drops a pin the gesture layer has stopped talking about. A BACKSTOP,
    /// and nothing more.
    ///
    /// The primary release is the gesture's own: GraphView hangs a
    /// `@GestureState` off the drag whose reset closure ends the pin, and that
    /// hook is documented to run when a gesture ends OR IS CANCELLED — unlike
    /// `onEnded`, which SwiftUI only promises for the ordinary path. So a
    /// cancelled drag is handled where it happens, at the moment it happens,
    /// and this pass is only for a pin that escaped even that: a lifecycle
    /// path SwiftUI does not guarantee, a view torn down between two frames.
    ///
    /// Which is why the threshold is FIVE SECONDS rather than the fraction of
    /// one it used to be. A finger genuinely held still emits no `onChanged`,
    /// so a live hold is indistinguishable here from a dead gesture, and at
    /// three-quarters of a second this cut real ones: an ordinary pick-up,
    /// hold, decide, keep-dragging motion crosses that in one pause, and the
    /// node drifted ~90 units away over the following second before the next
    /// `onChanged` teleported it back under the finger. Now that the reset
    /// closure catches the case this was written for, the number costs nothing
    /// to make large, and large is what keeps it off legitimate holds. Five
    /// seconds of a simulation nobody asked for is a rounding error; a
    /// visible teleport is not.
    private func expireAbandonedPins() {
        guard !heldPins.isEmpty else { return }
        for id in heldPins.keys.sorted() {
            guard var pin = heldPins[id] else { continue }
            pin.idleFrames += 1
            if pin.idleFrames > GraphViewModel.abandonedPinFrames {
                physics?.release(id: id)
                heldPins.removeValue(forKey: id)
            } else {
                heldPins[id] = pin
            }
        }
    }

    /// What a press starting at `point` is doing — dragging a node, or panning
    /// the camera. Decided against the VISIBLE nodes only, at the same reach
    /// the tap path uses.
    func grabTarget(at point: CGPoint, camera: GraphCamera) -> GraphDragTarget {
        var visible: [Int64: CGPoint] = [:]
        for n in render.nodes {
            if let p = render.positions[n.id] { visible[n.id] = p }
        }
        return graphDragTarget(start: point, positions: visible, camera: camera)
    }

    /// The screen's physics gate, said the way a caller who just wants it off
    /// says it.
    ///
    /// One stored bit under two names, and deliberately so: `reduceMotion` is
    /// how the SETTING arrives (the view reads it from the environment and
    /// pushes it), while this is how the DECISION reads at a call site that
    /// does not care where the setting came from. There is no third source —
    /// the phone has no physics toggle of its own, so `false` here means
    /// exactly "this screen may not simulate", whatever the reason.
    ///
    /// No production call site, and that is correct rather than dead weight:
    /// the view has the environment value and writes `reduceMotion` directly,
    /// while this is the spelling the frozen contract's physics-off
    /// obligations are stated through, so the blind test suite is its only
    /// caller. It is contract surface, not an unused convenience.
    func setPhysicsEnabled(_ enabled: Bool) {
        reduceMotion = !enabled
    }

    /// The view saying it is on screen and wants frames.
    ///
    /// The per-frame loop is OPT-IN rather than started wherever the
    /// simulation happens to wake, and the reason is not only that a display
    /// link is a view-lifetime resource. Without this gate a view model
    /// standing on its own — which is exactly how every test drives it —
    /// creates a real `CADisplayLink` the moment a payload lands, and that
    /// link fires from the main run loop the next time the caller awaits
    /// anything. The simulation would then advance a nondeterministic number
    /// of frames between two lines of a test that never asked for one.
    func startDisplayLoop() {
        wantsFrames = true
        startTicking()
    }

    /// The view going away. Ends the loop and refuses to start another until
    /// the screen comes back — a simulation ticking behind a screen nobody is
    /// looking at is the one cost this feature must not have.
    func stopDisplayLoop() {
        wantsFrames = false
        // Every pin goes with the screen. Leaving one held would mean the loop
        // is asleep only because nobody is asking it to run, so the next
        // `startDisplayLoop` — the reader coming back to a screen they left
        // half a second ago — would find `isAwake` still true and resurrect a
        // 60 fps simulation around a finger that is long gone.
        for id in heldPins.keys.sorted() { physics?.release(id: id) }
        heldPins.removeAll()
        stopTicking()
    }

    private func startTicking() {
        guard wantsFrames, physicsShouldTick, displayLink == nil else { return }
        let proxy = GraphTickProxy()
        proxy.vm = self
        let link = CADisplayLink(target: proxy, selector: #selector(GraphTickProxy.step))
        proxy.link = link
        // `.common` so a drawer detent drag or a chip-row scroll does not
        // freeze the simulation for the duration of the tracking run loop.
        link.add(to: .main, forMode: .common)
        displayLink = link
        tickProxy = proxy
    }

    /// Tears the link down. Called when the simulation settles and when
    /// physics is switched off — a display link nobody invalidates goes on
    /// firing for the life of the app. It leaves `wantsFrames` alone: settling
    /// is not the screen going away, and the next drag must be able to start
    /// the loop again without the view saying anything.
    private func stopTicking() {
        displayLink?.invalidate()
        displayLink = nil
        tickProxy = nil
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

/// What the display link actually retains.
///
/// `CADisplayLink` holds its target STRONGLY and there is no version of that
/// which does not leak when the target is the view model: the link keeps the
/// screen's whole model — payload, render, simulation — alive after the screen
/// is gone, and the model's `deinit` can never run to invalidate it. So the
/// link retains this instead, and this holds the model weakly.
@MainActor
private final class GraphTickProxy: NSObject {
    weak var vm: GraphViewModel?
    /// Weak because the link retains this proxy; the run loop and the view
    /// model are what keep the link itself alive.
    weak var link: CADisplayLink?

    @objc func step() {
        guard let vm else {
            // The model went away without stopping the loop. Nothing is left
            // to tick and nobody else holds this link, so it ends here.
            link?.invalidate()
            return
        }
        vm.tickPhysics()
    }
}

/// Wall-clock in unix millis. One function so no surface on this screen
/// derives "now" a second way; within a pass they share a single reading —
/// `GraphRender.now`, which the canvas, the header and the drawer are all
/// handed — rather than each sampling the clock.
func graphNow() -> Int64 {
    Int64(Date().timeIntervalSince1970 * 1000)
}
