import AppKit
import Combine
import SwiftUI

// GraphScreen.swift — the knowledge-graph viewer (R2 §2.4).
//
// A native SwiftUI `Canvas` over the server's own ForceAtlas2 layout, with the
// hand-ported d3-force engine warm-started from it. The pattern is the iOS
// screen's, and the two pure halves are LITERALLY the iOS ones: `GraphModel`
// (validity, degrees, positions, filters, fact ordering, the camera, the
// gesture fork) and `GraphPhysics` (the simulation) are referenced into this
// target rather than copied. What is written here is the part that differs —
// a desktop's two rails instead of a phone's sheet, and colours out of the HUD
// theme instead of the iOS one.
//
// `GraphRender` and `graphNow()` live in `ios/Kraken/GraphViewModel.swift`,
// which this target does NOT reference (it is an `@Observable` iOS view model
// with a `CADisplayLink` and `UIColor` bridging in it), so both are re-declared
// below. They are the only two things in this file that are a port rather than
// a reuse.
//
// Read-only, exactly as on the web and the phone: nothing in this view mutates
// the graph, and a node drag is EPHEMERAL — `kg_layout` stays daemon-owned and
// a relaunch reseeds from the server.

// MARK: - render model

/// Everything the canvas draws from, derived once per data or filter change
/// rather than per frame. The camera is deliberately NOT an input, which is
/// what makes the caching sound: it changes what the canvas DRAWS, never what
/// it draws FROM.
struct GraphRender: Equatable {
    var nodes: [GraphNode] = []
    var edges: [GraphEdge] = []
    // The maps below are built over the WHOLE payload, never the filtered set:
    // a position and a degree are properties of the ENTITY, and letting a
    // filter move or resize the survivors would make the picture unrecognizable
    // at each toggle.
    var positions: [Int64: CGPoint] = [:]
    var degrees: [Int64: Int] = [:]
    var names: [Int64: String] = [:]
    var nodesByID: [Int64: GraphNode] = [:]
    var typeColors: [String: Color] = [:]
    var types: [String] = []
    var currentEdges: Int = 0
    /// The instant every validity question on this screen is asked at, so the
    /// canvas, the header count and the inspector cannot answer differently
    /// within one pass.
    var now: Int64 = 0

    func withPositions(_ positions: [Int64: CGPoint]) -> GraphRender {
        var copy = self
        copy.positions = positions
        return copy
    }

    var worldBounds: CGRect? {
        var minX = Double.greatestFiniteMagnitude
        var minY = Double.greatestFiniteMagnitude
        var maxX = -Double.greatestFiniteMagnitude
        var maxY = -Double.greatestFiniteMagnitude
        var any = false
        for n in nodes {
            guard let p = positions[n.id] else { continue }
            any = true
            minX = min(minX, p.x); minY = min(minY, p.y)
            maxX = max(maxX, p.x); maxY = max(maxY, p.y)
        }
        guard any else { return nil }
        return CGRect(x: minX, y: minY, width: maxX - minX, height: maxY - minY)
    }
}

/// The one clock every validity question on this screen is asked against.
func graphNow() -> Int64 { Int64(Date().timeIntervalSince1970 * 1000) }

/// Label level-of-detail and its cap — the iOS screen's values, kept identical
/// so the two clients do not disagree about which nodes are worth naming.
let graphLabelMinRadius: CGFloat = 8
let graphLabelCap = 40

// MARK: - the model

@MainActor
final class GraphModelState: ObservableObject {
    static let pollInterval: TimeInterval = 30
    /// The simulation's frame rate while awake.
    static let frameInterval: TimeInterval = 1.0 / 60

    @Published private(set) var payload: GraphPayload?
    /// A refresh failure is a NOTE, never an empty canvas: 30s of blank is
    /// worse than a stale picture, which is the opposite call from DreamView's
    /// and deliberately so.
    @Published private(set) var errorText: String?
    @Published private(set) var render = GraphRender()
    @Published private(set) var selectedID: Int64?
    @Published private(set) var detail: GraphNodeDetail?

    @Published var hiddenTypes: Set<String> = [] { didSet { rebuild() } }
    @Published var includeInvalidated = false { didSet { rebuild() } }
    @Published var minOccurrences = 1 { didSet { rebuild() } }
    @Published var reduceMotion = false { didSet { rebuild() } }
    /// "on" | "off" | "" (auto). The reader's explicit choice outranks the cap
    /// only downwards — `off` always wins, `on` cannot exceed the node cap.
    @Published var physicsPref = "" { didSet { rebuild() } }
    /// Repainted by the tick loop; the canvas reads `render`.
    @Published private(set) var frame = 0

    private(set) var physics: GraphPhysics?
    private var membership: GraphPhysicsMembership?
    private var serverPositions: [Int64: CGPoint] = [:]
    private var simPositions: [Int64: CGPoint] = [:]
    private var livePositions: [Int64: CGPoint] = [:]
    private var poller: Poller?
    private var ticker: AnyCancellable?
    private var detailTask: Task<Void, Never>?
    /// Set by the view so the palette follows the theme.
    var themeColors: [Color] = []

    init() {
        poller = Poller(interval: Self.pollInterval) { [weak self] in
            await self?.load()
        }
    }

    func start() { poller?.start() }

    func stop() {
        poller?.pause()
        stopTicking()
    }

    // MARK: data

    func load() async {
        do {
            payload = try await APIClient.shared.fetchGraph(baseURL: MacAPI.baseURL)
            errorText = nil
            rebuild()
        } catch is CancellationError {
            return
        } catch {
            // Keep the last good graph — see `errorText`.
            errorText = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    func select(_ id: Int64?) {
        selectedID = id
        detail = nil
        detailTask?.cancel()
        guard let id else { return }
        detailTask = Task { [weak self] in
            // A node-detail failure is SILENT: the panel already has everything
            // but the aliases, which is the only thing this route adds.
            guard let next = try? await APIClient.shared
                .fetchGraphNode(baseURL: MacAPI.baseURL, id: id) else { return }
            guard let self, !Task.isCancelled, self.selectedID == id else { return }
            self.detail = next
        }
    }

    var selectedNode: GraphNode? {
        guard let selectedID else { return nil }
        return detail?.node ?? render.nodesByID[selectedID]
    }

    /// The inspector's facts are deliberately UNFILTERED — invalidated ones
    /// show even under "current only" — sorted current-first then strength
    /// descending. The canvas answers "what is true now"; the inspector answers
    /// "what has this entity ever been part of".
    var selectedFacts: [GraphEdge] {
        guard let selectedID else { return [] }
        let all = detail?.edges
            ?? (payload?.edges ?? []).filter { $0.src == selectedID || $0.dst == selectedID }
        return graphSortFacts(all, now: render.now)
    }

    func edgeCount(for node: GraphNode) -> Int { render.degrees[node.id] ?? 0 }

    // MARK: derivation

    private func rebuild() {
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
        for (i, t) in types.enumerated() where !themeColors.isEmpty {
            // Keyed on FIRST-SEEN INDEX rather than on a hash of the name: the
            // chip row renders in that same order, so the dot beside a chip and
            // the ink on the canvas are one lookup, and a type the extraction
            // agent invents tomorrow cannot recolour the existing ones.
            colors[t] = themeColors[i % themeColors.count]
        }
        // The server layout is the SEED; wherever the simulation has since
        // moved a node, its position wins. Simulated positions for entities the
        // payload no longer carries are dropped HERE and nowhere else — this
        // map is otherwise append-only, and a `/graph` revision job retiring
        // entities all week would grow it without bound.
        serverPositions = graphPositions(nodes: allNodes)
        simPositions = simPositions.filter { serverPositions[$0.key] != nil }
        var live = serverPositions
        for (id, p) in simPositions { live[id] = p }
        livePositions = live

        render = GraphRender(
            nodes: allNodes.filter { !hidden.contains($0.id) },
            edges: allEdges.filter { edgeVisible($0, hidden: hidden, now: now) },
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

    /// The web's own rule, plus the min-sightings slider the shared helper does
    /// not know about: visible endpoints, validity per the toggle, and
    /// `occurrences >= minOccurrences`.
    private func edgeVisible(_ e: GraphEdge, hidden: Set<Int64>, now: Int64) -> Bool {
        guard graphEdgeVisible(
            e, hiddenNodeIDs: hidden, includeInvalidated: includeInvalidated, now: now
        ) else { return false }
        return e.occurrences >= Int64(minOccurrences)
    }

    // MARK: physics

    var physicsAllowed: Bool {
        if physicsPref == "off" { return false }
        return graphPhysicsAllowed(visibleNodes: render.nodes.count, reduceMotion: reduceMotion)
    }

    var physicsShouldTick: Bool { physics?.isAwake ?? false }

    private func syncPhysics() {
        guard physicsAllowed else {
            physics = nil
            membership = nil
            stopTicking()
            return
        }
        let wanted = currentMembership()
        // The early return IS "an unchanged poll does not restart the
        // simulation": same visible nodes, same visible edges, same engine,
        // untouched — so a settled graph stays asleep across a poll.
        if wanted == membership, physics != nil { return }
        membership = wanted
        var seen: Set<Int64> = []
        var seeds: [(id: Int64, x: CGFloat, y: CGFloat, radius: CGFloat)] = []
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
        startTicking()
    }

    /// Both lists are SORTED, which is the difference between a membership test
    /// and an accident: equality is over sequences, so an unsorted comparison
    /// says "this changed" whenever the server merely returns the same graph in
    /// a different order — and the simulation would restart on every 30s poll
    /// forever with nothing to see but a graph that never quite settles.
    private func currentMembership() -> GraphPhysicsMembership {
        var seen: Set<Int64> = []
        var ids: [Int64] = []
        for n in render.nodes where seen.insert(n.id).inserted { ids.append(n.id) }
        let links = render.edges
            .map { GraphPhysicsLink(src: $0.src, dst: $0.dst, occurrences: $0.occurrences) }
            .sorted { ($0.src, $0.dst, $0.occurrences) < ($1.src, $1.dst, $1.occurrences) }
        return GraphPhysicsMembership(nodes: ids.sorted(), links: links)
    }

    private func startTicking() {
        guard ticker == nil else { return }
        ticker = Timer.publish(every: Self.frameInterval, on: .main, in: .common)
            .autoconnect()
            .sink { [weak self] _ in
                MainActor.assumeIsolated { self?.tickPhysics() }
            }
    }

    private func stopTicking() {
        ticker?.cancel()
        ticker = nil
    }

    func tickPhysics() {
        guard let physics else {
            stopTicking()
            return
        }
        physics.tick()
        for n in physics.nodes() {
            let p = CGPoint(x: n.x, y: n.y)
            simPositions[n.id] = p
            livePositions[n.id] = p
        }
        render = render.withPositions(livePositions)
        frame &+= 1
        // An idle screen schedules no per-frame work at all.
        if !physicsShouldTick { stopTicking() }
    }

    /// With physics on this pins the node in the simulation, so its linked
    /// neighbours follow. With physics off it is a plain position override on
    /// that one node: a reduced-motion reader still gets to move a node out of
    /// the way, they just get no cascade and nothing wakes.
    func dragNode(id: Int64, to world: CGPoint) {
        if let physics {
            physics.drag(id: id, to: world)
            startTicking()
        } else {
            simPositions[id] = world
            livePositions[id] = world
            render = render.withPositions(livePositions)
        }
    }

    func releaseNode(id: Int64) {
        physics?.release(id: id)
    }

    /// The press target, decided ONCE at the gesture's start location and never
    /// re-evaluated mid-drag: a node the simulation pulls out from under the
    /// cursor must not hand the gesture back to the camera halfway through, and
    /// a pan that happens to sweep over a node must not snatch it.
    func grabTarget(at point: CGPoint, camera: GraphCamera) -> GraphDragTarget {
        var visible: [Int64: CGPoint] = [:]
        for n in render.nodes {
            if let p = render.positions[n.id] { visible[n.id] = p }
        }
        return graphDragTarget(start: point, positions: visible, camera: camera)
    }

    func hitTest(_ point: CGPoint, camera: GraphCamera) -> Int64? {
        var visible: [Int64: CGPoint] = [:]
        for n in render.nodes {
            if let p = render.positions[n.id] { visible[n.id] = p }
        }
        return graphHitTest(
            screenPoint: point, positions: visible,
            camera: camera, maxDistance: graphTapReach
        )
    }
}

// MARK: - the screen

struct GraphScreen: View {
    /// Whether GRAPH is the visible tab. Mounted either way — the camera and
    /// the settled simulation are the whole reason — but the 30s poll runs only
    /// while it is showing. The physics ticker is governed separately, by the
    /// simulation being awake.
    var visible: Bool = true

    @StateObject private var model = GraphModelState()
    @Environment(\.hudTheme) private var theme
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @AppStorage("kraken-graph-physics") private var physicsPref = ""

    @State private var camera = GraphCamera(scale: 1, offset: .zero)
    @State private var viewport = CGSize.zero
    @State private var didFit = false
    /// The press that is in flight, if any — see `GraphModelState.grabTarget`.
    @State private var grab: GraphDragTarget?
    @State private var grabStart = CGPoint.zero
    @State private var panOrigin = CGSize.zero
    /// The click latch: a press that TRAVELLED is a drag and must not also
    /// select. Tap and drag are one gesture here (a `DragGesture` with a zero
    /// minimum distance), so the latch is simply "did it move past the
    /// threshold" read at the end.
    @State private var travelled = false

    private static let dragThreshold: CGFloat = 3

    var body: some View {
        VStack(spacing: 0) {
            header
            HStack(spacing: 0) {
                filtersRail
                Rectangle().fill(theme.dividerColor).frame(width: 1)
                canvas
                Rectangle().fill(theme.dividerColor).frame(width: 1)
                inspectorRail
            }
        }
        .background(theme.groundColor)
        .onAppear {
            model.themeColors = Self.typePalette(theme)
            model.reduceMotion = reduceMotion
            model.physicsPref = physicsPref
            if visible { model.start() }
        }
        .onDisappear { model.stop() }
        .onChange(of: visible) { _, showing in
            if showing { model.start() } else { model.stop() }
        }
        .onChange(of: reduceMotion) { _, next in model.reduceMotion = next }
        .onChange(of: physicsPref) { _, next in model.physicsPref = next }
        .onChange(of: theme) { _, next in model.themeColors = Self.typePalette(next) }
    }

    // MARK: header

    private var header: some View {
        HStack(spacing: 10) {
            Text(statsLine)
                .hudMono(size: 9, tracking: HudTracking.stat)
                .foregroundStyle(theme.mutedColor)
            if let err = model.errorText {
                Text("· refresh failed: " + err)
                    .hudMono(size: 9)
                    .foregroundStyle(theme.counterColor)
                    .lineLimit(1)
            }
            Spacer()
            physicsToggle
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(theme.surfaceColor)
        .overlay(alignment: .bottom) {
            Rectangle().fill(theme.dividerColor).frame(height: 1)
        }
    }

    private var statsLine: String {
        guard let s = model.payload?.stats else { return "loading the graph…" }
        return "\(s.nodes) nodes · \(s.edges) edges · \(model.render.currentEdges) current · "
            + "\(s.episodes) episodes · \(s.pending) pending · \(s.missingEmbeddings) unembedded"
    }

    private var physicsToggle: some View {
        // `on`/`off`/absent — absent is AUTO, which is the node cap plus the
        // Reduce Motion setting deciding for you.
        let on = model.physicsAllowed
        return Button(on ? "◉ physics" : "○ physics") {
            physicsPref = on ? "off" : "on"
        }
        .buttonStyle(.plain)
        .hudMono(size: 9, tracking: HudTracking.stat)
        .foregroundStyle(on ? theme.accentColor : theme.mutedColor)
        .padding(.horizontal, 7)
        .padding(.vertical, 2)
        .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
        .help(reduceMotion
              ? "physics is off under Reduce Motion unless you force it on"
              : "warm-started force layout — never written back to the server")
        .hudCursor(.actable)
    }

    // MARK: rails

    private var filtersRail: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                railTitle("node types")
                ForEach(model.render.types, id: \.self) { type in
                    typeRow(type)
                }
                railTitle("validity")
                radio("current only", on: !model.includeInvalidated) {
                    model.includeInvalidated = false
                }
                radio("include invalidated", on: model.includeInvalidated) {
                    model.includeInvalidated = true
                }
                railTitle("min sightings")
                HStack(spacing: 8) {
                    Slider(
                        value: Binding(
                            get: { Double(model.minOccurrences) },
                            set: { model.minOccurrences = Int($0.rounded()) }
                        ),
                        in: 1...10,
                        step: 1
                    )
                    // the web's range input rides `accent-color: var(--ac)`
                    // (style.css:2502); tint + mini is the closest the native
                    // slider gets without a hand-drawn track
                    .tint(theme.accentColor)
                    .controlSize(.mini)
                    Text("≥ \(model.minOccurrences)")
                        .hudMono(size: 9)
                        .foregroundStyle(theme.mutedColor)
                }
                Spacer()
            }
            .padding(12)
        }
        .frame(width: 190)
    }

    private func typeRow(_ type: String) -> some View {
        let hidden = model.hiddenTypes.contains(type)
        return Button {
            if hidden { model.hiddenTypes.remove(type) } else { model.hiddenTypes.insert(type) }
        } label: {
            HStack(spacing: 6) {
                Text(hidden ? "☐" : "☑").foregroundStyle(theme.mutedColor)
                Circle()
                    .fill(model.render.typeColors[type] ?? theme.mutedColor)
                    .frame(width: 7, height: 7)
                Text(type).foregroundStyle(hidden ? theme.mutedColor : theme.bodyColor)
                Spacer()
            }
            .hudMono(size: 10)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .hudCursor(.actable)
    }

    private func radio(_ label: String, on: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 6) {
                Text(on ? "◉" : "○").foregroundStyle(on ? theme.accentColor : theme.mutedColor)
                Text(label).foregroundStyle(on ? theme.bodyColor : theme.mutedColor)
                Spacer()
            }
            .hudMono(size: 10)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .hudCursor(.actable)
    }

    private func railTitle(_ text: String) -> some View {
        Text(text.uppercased())
            .hudDisplay(size: 9, tracking: HudTracking.tab)
            .foregroundStyle(theme.mutedColor)
    }

    private var inspectorRail: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 10) {
                if let node = model.selectedNode {
                    Text(node.name)
                        .hudMono(size: 12, weight: .semibold)
                        .foregroundStyle(theme.textColor)
                        .textSelection(.enabled)
                    HStack(spacing: 6) {
                        Text(node.type)
                            .hudMono(size: 9)
                            .foregroundStyle(model.render.typeColors[node.type] ?? theme.mutedColor)
                            .padding(.horizontal, 5)
                            .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
                        Text("\(model.edgeCount(for: node)) edges")
                            .hudMono(size: 9)
                            .foregroundStyle(theme.mutedColor)
                    }
                    if !node.description.isEmpty {
                        Text(node.description)
                            .hudMono(size: 10)
                            .foregroundStyle(theme.bodyColor)
                            .textSelection(.enabled)
                    }
                    // Aliases come ONLY from /api/graph/node — the viewer
                    // payload omits them outright rather than sending empty
                    // arrays, so nil here means "the detail has not landed",
                    // never "this entity has none".
                    if let aliases = model.detail?.node.aliases, !aliases.isEmpty {
                        Text("also: " + aliases.joined(separator: " · "))
                            .hudMono(size: 9)
                            .foregroundStyle(theme.mutedColor)
                    }
                    Rectangle().fill(theme.dividerColor).frame(height: 1)
                    ForEach(model.selectedFacts) { fact in
                        factRow(fact)
                    }
                } else {
                    Text("click a node to inspect")
                        .hudMono(size: 10)
                        .foregroundStyle(theme.mutedColor)
                }
                Spacer()
            }
            .padding(12)
        }
        .frame(width: 280)
    }

    private func factRow(_ fact: GraphEdge) -> some View {
        let dead = graphIsInvalidated(fact, now: model.render.now)
        return VStack(alignment: .leading, spacing: 2) {
            Text(graphFactText(fact, names: model.render.names))
                .hudMono(size: 10)
                .foregroundStyle(dead ? theme.mutedColor : theme.bodyColor)
                .strikethrough(dead)
                .textSelection(.enabled)
            Text(dead
                 ? "invalidated " + graphShortDate(ms: graphInvalidatedAt(fact))
                 : "E\(fact.episode) · strength \(fact.strength) · seen \(fact.occurrences)×")
                .hudMono(size: 9)
                .foregroundStyle(theme.mutedColor)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // MARK: canvas

    private var canvas: some View {
        GeometryReader { geo in
            ZStack {
                GraphCanvas(
                    render: model.render,
                    camera: camera,
                    selectedID: model.selectedID,
                    edgeInk: theme.graphEdge,
                    ringInk: SkillPalette.color("graph", theme.mode)?.color ?? theme.accentColor,
                    labelInk: theme.textColor,
                    fallbackInk: theme.mutedColor
                )
                if model.payload == nil {
                    overlayText("loading the graph…")
                } else if model.render.nodes.isEmpty {
                    overlayText("the graph is empty — the nightly ingest job fills it")
                }
                VStack {
                    Spacer()
                    HStack {
                        Text("pinch to zoom · drag to pan · click a node to inspect")
                            .hudMono(size: 9)
                            .foregroundStyle(theme.mutedColor)
                        Spacer()
                        zoomButton("+") { zoomAboutCentre(1.25) }
                        zoomButton("−") { zoomAboutCentre(0.8) }
                        zoomButton("⤢") { fit(geo.size) }
                    }
                    .padding(10)
                }
            }
            .contentShape(Rectangle())
            .gesture(dragGesture)
            .gesture(MagnificationGesture().onChanged { scale in
                // Trackpad pinch about the viewport centre. The web zooms about
                // the pointer with a scroll wheel; SwiftUI hands a magnify
                // gesture no location, so the anchor is the centre — the same
                // anchor the +/− buttons use, which keeps the two consistent
                // rather than one of them being subtly different.
                zoomAboutCentre(1 + (scale - 1) * 0.02)
            })
            .onAppear {
                viewport = geo.size
                fitIfNeeded(geo.size)
            }
            .onChange(of: geo.size) { _, next in
                viewport = next
                fitIfNeeded(next)
            }
            .onChange(of: model.render.nodes.count) { _, _ in fitIfNeeded(geo.size) }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .clipped()
    }

    private var dragGesture: some Gesture {
        DragGesture(minimumDistance: 0)
            .onChanged { value in
                if grab == nil {
                    grabStart = value.startLocation
                    panOrigin = camera.offset
                    travelled = false
                    grab = model.grabTarget(at: value.startLocation, camera: camera)
                }
                let dx = value.location.x - grabStart.x
                let dy = value.location.y - grabStart.y
                if !travelled, (dx * dx + dy * dy).squareRoot() > Self.dragThreshold {
                    travelled = true
                }
                guard travelled else { return }
                switch grab {
                case .node(let id):
                    model.dragNode(id: id, to: camera.screenToWorld(value.location))
                case .camera:
                    camera.offset = CGSize(
                        width: panOrigin.width + dx, height: panOrigin.height + dy
                    )
                case nil:
                    break
                }
            }
            .onEnded { value in
                defer {
                    grab = nil
                    travelled = false
                }
                if case .node(let id) = grab { model.releaseNode(id: id) }
                // The latch: a press that travelled was a DRAG and never also
                // selects. Only a stationary press is a click.
                guard !travelled else { return }
                model.select(model.hitTest(value.location, camera: camera))
            }
    }

    private func overlayText(_ text: String) -> some View {
        Text(text)
            .hudMono(size: 11)
            .foregroundStyle(theme.mutedColor)
    }

    private func zoomButton(_ glyph: String, action: @escaping () -> Void) -> some View {
        Button(glyph, action: action)
            .buttonStyle(.plain)
            .hudMono(size: 12)
            .foregroundStyle(theme.mutedColor)
            .frame(width: 22, height: 22)
            .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
            .hudCursor(.actable)
    }

    private func zoomAboutCentre(_ factor: CGFloat) {
        guard viewport.width > 0, viewport.height > 0 else { return }
        var next = camera
        next.pinch(
            by: factor,
            anchor: CGPoint(x: viewport.width / 2, y: viewport.height / 2)
        )
        // The clamp lives at the gesture layer, never inside `pinch` — see
        // GraphCamera.pinch for why.
        next.scale = min(max(next.scale, graphMinScale), graphMaxScale)
        camera = next
    }

    private func fitIfNeeded(_ size: CGSize) {
        guard !didFit, size.width > 0, size.height > 0, model.render.worldBounds != nil else {
            return
        }
        didFit = true
        fit(size)
    }

    private func fit(_ size: CGSize) {
        guard let bounds = model.render.worldBounds else { return }
        camera = GraphCamera.fitting(worldRect: bounds, viewport: size)
    }

    /// The type palette, out of the theme rather than off a pixel probe — the
    /// tokens are Swift values here, which is the whole reason the web's canvas
    /// probe has no twin. The six skill hues first (they are the app's existing
    /// categorical set), then the status hues, so a graph with more types than
    /// hues wraps onto colours that still belong to this palette.
    static func typePalette(_ theme: HudTheme) -> [Color] {
        var out: [Color] = SkillPalette.names.compactMap {
            SkillPalette.color($0, theme.mode)?.color
        }
        out.append(contentsOf: [theme.accentColor, theme.counterColor, theme.okColor, theme.warnColor])
        return out
    }
}

// MARK: - the canvas

struct GraphCanvas: View, Equatable {
    let render: GraphRender
    let camera: GraphCamera
    let selectedID: Int64?
    let edgeInk: Color
    let ringInk: Color
    let labelInk: Color
    let fallbackInk: Color

    /// Hand-written for the ORDER, which is the whole point: the camera is what
    /// a gesture frame changes, and comparing it first short-circuits before
    /// the render's O(nodes + edges) comparison ever runs.
    static func == (a: GraphCanvas, b: GraphCanvas) -> Bool {
        a.camera == b.camera && a.selectedID == b.selectedID && a.render == b.render
            && a.edgeInk == b.edgeInk && a.ringInk == b.ringInk
    }

    var body: some View {
        Canvas { context, size in
            // Culled against the viewport grown by one node's worth of ink, so
            // a node whose centre is just off-screen still contributes its edge
            // and its rim.
            let bounds = CGRect(origin: .zero, size: size).insetBy(dx: -48, dy: -48)
            drawEdges(&context, bounds)
            drawNodes(&context, bounds)
        }
    }

    /// Edges first, so a node always sits on top of the lines into it. Both
    /// weights accumulate into ONE path each and are stroked twice total: a
    /// stroke per edge is a separate draw call, and this canvas is heading for
    /// thousands of them under a moving cursor.
    private func drawEdges(_ context: inout GraphicsContext, _ bounds: CGRect) {
        var live = Path()
        var dead = Path()
        for e in render.edges {
            guard let s = render.positions[e.src], let d = render.positions[e.dst] else { continue }
            let a = camera.worldToScreen(s)
            let b = camera.worldToScreen(d)
            // The inset is load-bearing, not slack: `CGRect.intersects` returns
            // false when EITHER rect is empty, and an exactly horizontal or
            // vertical edge has a zero-height or zero-width box — the two
            // orientations a force layout produces plenty of.
            let box = CGRect(
                x: min(a.x, b.x), y: min(a.y, b.y),
                width: abs(a.x - b.x), height: abs(a.y - b.y)
            ).insetBy(dx: -1, dy: -1)
            guard box.intersects(bounds) else { continue }
            if graphIsInvalidated(e, now: render.now) {
                dead.move(to: a)
                dead.addLine(to: b)
            } else {
                live.move(to: a)
                live.addLine(to: b)
            }
        }
        context.stroke(live, with: .color(edgeInk), lineWidth: 1)
        // An invalidated fact shown under the toggle is drawn thin and faint
        // rather than in a colour of its own: it is the same relationship,
        // superseded, and the inspector's strike-through is where that is said
        // in words.
        context.stroke(dead, with: .color(edgeInk.opacity(0.4)), lineWidth: 0.5)
    }

    /// The selected node is drawn even when its own type is switched off: a
    /// fact's endpoint can land on a hidden node, and selecting it with no ring
    /// anywhere on the canvas looks like the click did nothing.
    private var drawList: [GraphNode] {
        guard let selectedID,
              !render.nodes.contains(where: { $0.id == selectedID }),
              let hidden = render.nodesByID[selectedID]
        else { return render.nodes }
        return render.nodes + [hidden]
    }

    private func drawNodes(_ context: inout GraphicsContext, _ bounds: CGRect) {
        var labels: [GraphLabel] = []
        var selectedLabel: GraphLabel?
        for n in drawList {
            guard let world = render.positions[n.id] else { continue }
            let p = camera.worldToScreen(world)
            guard bounds.contains(p) else { continue }
            let selected = n.id == selectedID
            let radius = graphNodeRadius(degree: render.degrees[n.id] ?? 0) * (selected ? 1.4 : 1)
            let rect = CGRect(
                x: p.x - radius, y: p.y - radius, width: radius * 2, height: radius * 2
            )
            context.fill(
                Path(ellipseIn: rect), with: .color(render.typeColors[n.type] ?? fallbackInk)
            )
            let anchor = CGPoint(x: p.x, y: p.y + radius + 7)
            if selected {
                context.stroke(
                    Path(ellipseIn: rect.insetBy(dx: -2.5, dy: -2.5)),
                    with: .color(ringInk), lineWidth: 2
                )
                // Kept OUT of `labels` rather than queued into it: this label is
                // forced, and letting it compete would let 40 hubs outrank the
                // one node the reader just asked about.
                selectedLabel = GraphLabel(id: n.id, radius: radius, at: anchor, text: n.name)
                continue
            }
            // `radius` is already in SCREEN points (the camera translates nodes,
            // it does not scale them), so multiplying by the camera's scale here
            // would be a unit error.
            guard radius >= graphLabelMinRadius else { continue }
            labels.append(GraphLabel(id: n.id, radius: radius, at: anchor, text: n.name))
        }
        for label in graphLabelSelection(labels, cap: graphLabelCap) {
            context.draw(
                Text(label.text).font(.hudMono(9)).foregroundStyle(labelInk),
                at: label.at, anchor: .top
            )
        }
        if let selectedLabel {
            context.draw(
                Text(selectedLabel.text).font(.hudMono(9)).foregroundStyle(ringInk),
                at: selectedLabel.at, anchor: .top
            )
        }
    }
}
