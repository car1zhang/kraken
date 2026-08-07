import SwiftUI

// The knowledge-graph viewer on the phone: a native SwiftUI `Canvas` over the
// server's own layout, with an iOS drawer instead of a desktop inspector rail.
//
// This screen used to be a WKWebView onto the web board's `#/graph` route, on
// the argument that one server-computed layout should mean one implementation.
// The layout half of that still holds and is why there is still no client-side
// force simulation here — positions come from `core.ComputeLayout` and both
// clients draw the same shape. What did not hold is the rest: WebGL inside a
// sheet fights the sheet's own gestures, sigma's labels are sized for a
// desktop, the three-column rail/canvas/rail layout is nonsense at 390pt, and
// the screen could not render at all without the embedded SPA. Everything the
// web view does on top of the layout — viewport, culling, label LOD,
// hit-testing — is a few hundred lines, and doing it natively buys real touch
// gestures and theme-correct colours from Theme.swift.
//
// The pure half lives in GraphModel.swift, and the state plus the derived
// draw model in GraphViewModel.swift; a Canvas closure is unreachable from a
// test, so what is left in here is the drawing calls themselves, the gesture
// plumbing they hang off, and the screen's own URL gate (`serverBase`, which
// decides between the graph and the "No server URL set" message — it stays
// here because it reads this view's one stored property).

// `graphTapReach` moved to GraphModel.swift when the drag gesture arrived: the
// press-vs-pan fork is defined in terms of it, and that decision has to be
// reachable from a test.

/// A live node grab, as the gesture system holds it.
///
/// It exists to give `@GestureState`'s reset closure everything it needs to
/// end the pin, since that closure is written before the view exists and can
/// capture nothing from it. Holding the view model in gesture state is sound
/// rather than merely convenient: SwiftUI already retains the same object for
/// the life of the screen through `@StateObject`, this reference lives only
/// for the duration of one gesture, and it is only ever touched on the main
/// actor — the same actor the model is confined to.
private struct GraphGrab {
    let id: Int64
    let vm: GraphViewModel
}

//
/// A node draws its label once its own screen radius reaches this many points.
///
/// Node ink here is drawn at a FIXED screen size — the camera moves nodes, it
/// does not scale them, which is what keeps a leaf tappable at every zoom
/// level. So unlike sigma's `labelRenderedSizeThreshold`, where the drawn size
/// follows the camera, this is effectively a DEGREE threshold: hubs carry
/// their names at any zoom, leaves take one only when selected. Labels thin
/// out by importance rather than by zoom, which is the trade this canvas made.
private let graphLabelMinRadius: CGFloat = 8
/// The most labels one frame will draw, largest node first.
///
/// The radius rule above is a threshold, not a bound, and on a dense graph
/// that is not enough. `graphNodeRadius` puts the 8pt line at degree 4
/// exactly, so on a graph of ~3k nodes averaging degree 4 it is roughly HALF
/// the graph that clears it — about 1,500 text layouts per frame, the single
/// most expensive thing this canvas does, on every frame of a pan.
/// 40 is chosen against the phone rather than the graph: a 9pt name
/// is 60-120pt wide, so on a 390pt-wide canvas 40 labels already overlap into
/// a smear the reader takes nothing from. The cap therefore costs no legible
/// label at any zoom — it only refuses to render the illegible ones. The
/// selected node's label is drawn outside the cap.
private let graphLabelCap = 40
/// One step of the +/− buttons.
private let graphZoomStep: CGFloat = 1.5
/// The drawer's resting height. Small enough that the tapped node and its
/// neighbourhood stay on screen above it, which is what makes
/// `presentationBackgroundInteraction` worth having.
private let graphDrawerDetent: CGFloat = 0.45
/// How often the payload refreshes while the screen is up.
private let graphPollSeconds: UInt64 = 30

/// The GRAPH screen. `baseURL` is the same `@AppStorage("serverURL")` value
/// every other screen is handed (SettingsView owns it), and the init is
/// unchanged from the WKWebView version — BoardView's `showingGraph` sheet and
/// the `kraken://graph` deep link both call exactly this.
struct GraphView: View {
    let baseURL: String

    @StateObject private var vm = GraphViewModel()
    @State private var camera = GraphCamera(scale: 1, offset: .zero)
    @State private var viewport: CGSize = .zero
    /// The first payload frames itself; every later one leaves the camera
    /// alone, or a poll would yank the reader back out of wherever they panned.
    @State private var didFit = false
    // Gesture values are CUMULATIVE from their sequence's start while both
    // `pan` and `pinch` take an INCREMENT, so each accumulator holds what has
    // already been applied. They are keyed by the sequence's own start
    // location as well as reset in `onEnded`, because SwiftUI does not
    // guarantee `onEnded` runs when a gesture is CANCELLED (the drawer takes
    // the touch, a screen-edge swipe wins it), and a stale accumulator makes
    // the next gesture's first frame teleport the camera by the whole of the
    // abandoned one.
    //
    // The key is a heuristic, not an identity: two gestures whose start
    // locations compare EQUAL as CGPoints alias into one sequence, and the
    // second then applies the first's accumulator. That needs a cancelled
    // gesture followed by a touch landing on the identical sub-pixel
    // coordinate, and it costs one frame of jump that the next frame corrects,
    // so it is accepted rather than fixed — the alternatives (a timeout, an
    // ended-flag) each misfire on a legitimate slow drag, which is the worse
    // trade.
    @State private var panSequence: CGPoint?
    @State private var appliedPan: CGSize = .zero
    @State private var pinchSequence: CGPoint?
    @State private var appliedMagnification: CGFloat = 1
    /// What the live press is doing, decided ONCE at its start location and
    /// held for the whole sequence — see `GraphDragTarget`. nil between
    /// gestures.
    @State private var grabbed: GraphDragTarget?
    /// The pin's LIFETIME, held by SwiftUI rather than by this view.
    ///
    /// `onEnded` is not guaranteed to run when a gesture is CANCELLED — the
    /// drawer takes the touch, a screen-edge swipe wins it, the app is
    /// backgrounded mid-drag — and a pin nobody releases holds a 60 fps
    /// simulation up for the life of the screen. `@GestureState`'s reset is
    /// the hook that IS guaranteed: SwiftUI documents it to run when the
    /// gesture ends *or is cancelled*, and reverting this to nil is what
    /// releases the node.
    ///
    /// It carries the view model because a `reset` closure is supplied at
    /// property-initialisation time, before `self` exists — it cannot capture
    /// the view, so everything the release needs has to ride in the VALUE. See
    /// `GraphGrab` for why holding the model here is sound.
    @GestureState(reset: { grab, _ in
        guard let grab else { return }
        // Synchronous, deliberately. `Task { @MainActor in … }` would compile
        // just as well and cannot trap, but it defers the release past the
        // start of whatever gesture comes next — and if that gesture grabs the
        // SAME node, the deferred release lands on the new pin and kills it.
        // A reset fires during view update on the main thread, so the
        // assumption is the one SwiftUI already guarantees.
        MainActor.assumeIsolated { grab.vm.releaseNode(id: grab.id) }
    }) private var grabPin: GraphGrab?
    /// The system's reduce-motion setting, which is one half of whether
    /// physics runs at all. Read here rather than in the view model because
    /// this is the half of the screen with an `@Environment` to read it from.
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// The server base to talk to, or nil when there is none to talk to.
    ///
    /// Both halves matter and the second is easy to lose: a BLANK base is a
    /// server never configured, and an UNPARSEABLE one — `192.168.1.5 :7777`,
    /// a stray space away from correct — is a settings problem too. Without
    /// the parse check that one sails through to the fetch and comes back as
    /// an error line, which reads as "the Mac is down" rather than "fix the
    /// typo". The whitespace test is belt to `URL(string:)`'s braces: which
    /// spellings Foundation's parser tolerates has moved between OS versions,
    /// and a raw space is never legal in a URL on any of them.
    private var serverBase: String? {
        let trimmed = baseURL.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return nil }
        var base = trimmed
        // Trailing slashes are trimmed rather than tolerated, as the WKWebView
        // version did before appending its route.
        while base.hasSuffix("/") { base.removeLast() }
        guard !base.isEmpty,
              base.rangeOfCharacter(from: .whitespacesAndNewlines) == nil,
              URL(string: base) != nil
        else { return nil }
        return base
    }

    var body: some View {
        Group {
            if let serverBase {
                withPolling(withDrawer(screen), base: serverBase)
            } else {
                noServer
            }
        }
        .background { ThemeBackgroundView() }
        .navigationTitle("Graph")
        .navigationBarTitleDisplayMode(.inline)
    }

    // body is deliberately a composition of small expressions rather than one
    // chain, for the reason BoardView's own body records: Swift's constraint
    // solver gives up ("unable to type-check this expression in reasonable
    // time") on a long modifier chain over a stack this deep, and it fails the
    // TestFlight archive along with every local build. Hang a new sheet, task
    // or gesture inside the matching helper below, never as a new link here.

    private var screen: some View {
        VStack(spacing: 0) {
            header
            filterBar
            canvasStage
        }
        .onAppear {
            vm.reduceMotion = reduceMotion
            vm.startDisplayLoop()
        }
        .onChange(of: reduceMotion) { _, on in vm.reduceMotion = on }
        .onDisappear { vm.stopDisplayLoop() }
    }

    private func withDrawer<Content: View>(_ content: Content) -> some View {
        content.sheet(isPresented: drawerPresented) { drawer }
    }

    private var drawer: some View {
        // Handed the view model and nothing else: the names and the instant it
        // renders against are read off `vm.render` inside its own body, so they
        // are the same values the canvas is drawing with rather than a copy
        // taken whenever SwiftUI last chose to re-invoke this closure.
        GraphNodeDrawer(vm: vm)
            .presentationDetents([.fraction(graphDrawerDetent), .large])
            .presentationBackgroundInteraction(
                .enabled(upThrough: .fraction(graphDrawerDetent))
            )
            .presentationDragIndicator(.visible)
    }

    private func withPolling<Content: View>(_ content: Content, base: String) -> some View {
        // Keyed on the base URL: SettingsView can change the server under a
        // presented sheet, and an unkeyed task would go on polling the old
        // host for the life of the screen.
        content.task(id: base) {
            while !Task.isCancelled {
                await vm.load(baseURL: base)
                try? await Task.sleep(nanoseconds: graphPollSeconds * 1_000_000_000)
            }
        }
    }

    private var drawerPresented: Binding<Bool> {
        Binding(
            get: { vm.selectedID != nil },
            // The only way this arrives as false is the reader dismissing the
            // sheet, which must clear the canvas' selection ring too.
            set: { if !$0 { vm.select(nil) } }
        )
    }

    // MARK: - chrome

    private var header: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(statsLine)
                .font(.data(10))
                .foregroundStyle(KTheme.text2)
                .lineLimit(2)
            if let errorText = vm.errorText {
                // The stale picture stays on screen underneath this — a
                // dropped poll is not a reason to blank a graph the reader is
                // mid-way through navigating.
                Text("couldn't refresh: \(errorText)")
                    .font(.data(10))
                    .foregroundStyle(KTheme.counter)
                    .lineLimit(2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.ultraThinMaterial)
    }

    private var statsLine: String {
        let render = vm.render
        let stats = vm.payload?.stats
        var parts = [
            "\(stats?.nodes ?? Int64(render.nodes.count)) nodes",
            "\(stats?.edges ?? Int64(render.edges.count)) edges",
            // "current" is counted CLIENT-side from the same predicate the
            // canvas draws with. The server's `live_edges` counts system-time
            // liveness only, so quoting it here would put a bigger number in
            // the header than there are edges on the canvas, with nothing
            // explaining the gap.
            "\(render.currentEdges) current",
        ]
        if let stats {
            parts.append("\(stats.episodes) episodes")
            if stats.pending > 0 { parts.append("\(stats.pending) pending") }
        }
        return parts.joined(separator: " · ")
    }

    private var filterBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            GlassEffectContainer(spacing: 6) {
                HStack(spacing: 6) {
                    ForEach(vm.render.types, id: \.self) { type in
                        typeChip(type)
                    }
                    invalidatedChip
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 8)
        }
        .background(.ultraThinMaterial)
        .overlay(alignment: .bottom) { Divider().allowsHitTesting(false) }
    }

    private func typeChip(_ type: String) -> some View {
        let on = !vm.hiddenTypes.contains(type)
        // The type's own dot colour is meaning, not theme — it stays
        // graphTypeColorUI's. Only the chip's shell became a glass capsule.
        let dot = on ? (vm.render.typeColors[type] ?? KTheme.text2) : KTheme.hairline2
        return Button {
            if on { vm.hiddenTypes.insert(type) } else { vm.hiddenTypes.remove(type) }
        } label: {
            HStack(spacing: 5) {
                Circle()
                    .fill(dot)
                    .frame(width: 7, height: 7)
                Text(type)
                    .font(.data(10, weight: .semibold))
                    .foregroundStyle(on ? KTheme.text : KTheme.text2)
            }
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .glassEffect(on ? .regular.tint(dot.opacity(0.28)) : .regular, in: Capsule())
        .opacity(on ? 1 : 0.6)
        .accessibilityLabel("\(type) nodes")
        .accessibilityAddTraits(on ? [.isSelected] : [])
    }

    /// The one validity control. The web rail spells it as two mutually
    /// exclusive buttons ("current only" / "include invalidated"); a phone
    /// chip row has no width for a pair that says one bit, so it is one
    /// toggle. There is deliberately no min-sightings slider: a horizontal
    /// slider inside a horizontally-scrolling row cannot be dragged.
    private var invalidatedChip: some View {
        let on = vm.includeInvalidated
        return Button {
            vm.includeInvalidated.toggle()
        } label: {
            Text(on ? "incl. invalidated" : "current only")
                .font(.data(10, weight: .semibold))
                .foregroundStyle(on ? KTheme.revealed : KTheme.text2)
                .padding(.horizontal, 10)
                .padding(.vertical, 6)
                .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .glassEffect(on ? .regular.tint(KTheme.revealed.opacity(0.28)) : .regular, in: Capsule())
        .opacity(on ? 1 : 0.6)
    }

    // MARK: - the canvas

    private var canvasStage: some View {
        GeometryReader { geo in
            ZStack(alignment: .bottomTrailing) {
                GraphCanvas(render: vm.render, camera: camera, selectedID: vm.selectedID)
                    .equatable()
                    .contentShape(Rectangle())
                    .onTapGesture { location in handleTap(location) }
                    .gesture(panGesture)
                    .simultaneousGesture(pinchGesture)
                stateOverlay
                zoomControls
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .onAppear { fitIfNeeded(size: geo.size) }
            .onChange(of: geo.size) { _, size in fitIfNeeded(size: size) }
            .onChange(of: vm.render.nodes.count) { _, _ in fitIfNeeded(size: geo.size) }
        }
        // No opaque slab here any more — the canvas draws straight onto the
        // screen's tint field. Drawing, physics and gestures are untouched.
    }

    /// Named `stateOverlay` rather than `overlay`: the latter would be an
    /// overload of SwiftUI's own modifier on this very view, which is a
    /// needless puzzle for the type checker and for a reader.
    ///
    /// The arms are ordered by what is KNOWN, not by what is likely. In
    /// particular the empty-graph sentence may only be said once a payload has
    /// actually arrived carrying zero nodes: a first load that FAILED also has
    /// no nodes, and claiming the graph is empty there put a confident, false
    /// sentence in the middle of the canvas directly under a header saying the
    /// refresh had failed.
    @ViewBuilder
    private var stateOverlay: some View {
        if let payload = vm.payload {
            if payload.nodes.isEmpty {
                overlayText("the graph is empty — the nightly ingest job fills it")
            } else if vm.render.nodes.isEmpty {
                // Not the same state: the graph HAS nodes, the chips are
                // hiding every one of them, and saying "empty" here would send
                // the reader looking for a server problem that is a tap away
                // from fixed.
                overlayText("every node type is switched off")
            }
        } else if vm.errorText != nil {
            // The header already carries the error's own words; this only says
            // there is no picture to look at behind them.
            overlayText("couldn't reach the server")
        } else {
            overlayText("loading the graph…")
        }
    }

    private func overlayText(_ text: String) -> some View {
        Text(text)
            .font(.voice(13))
            .foregroundStyle(KTheme.text2)
            .multilineTextAlignment(.center)
            .padding(24)
            .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var zoomControls: some View {
        // Three floating glass pucks in one container so they read as a single
        // stacked control. Still 36×36 each — the tap targets are unchanged.
        GlassEffectContainer(spacing: 6) {
            VStack(spacing: 6) {
                zoomButton("+", label: "zoom in") { zoomAboutCenter(by: graphZoomStep) }
                zoomButton("−", label: "zoom out") { zoomAboutCenter(by: 1 / graphZoomStep) }
                zoomButton("⤢", label: "fit to graph") { fit(size: viewport) }
            }
        }
        .padding(12)
    }

    private func zoomButton(_ glyph: String, label: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(glyph)
                .font(.data(15, weight: .semibold))
                .foregroundStyle(KTheme.text)
                .frame(width: 36, height: 36)
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .glassEffect(.regular, in: Circle())
        .accessibilityLabel(label)
    }

    // MARK: - gestures

    /// One gesture, two jobs: a press that begins ON a node drags that node,
    /// one that begins on empty canvas pans the camera exactly as it always
    /// did. The fork is `vm.grabTarget`, evaluated once at the sequence's
    /// start location and then held — see `GraphDragTarget` for why it is
    /// never re-asked mid-drag.
    private var panGesture: some Gesture {
        // A non-zero minimum distance is what lets a tap through to
        // `onTapGesture`: at 0 the drag claims every touch and nothing on this
        // canvas is ever selectable. It is also what keeps a completed
        // node-drag from selecting — the drag gesture wins the sequence, so
        // the tap handler never runs.
        DragGesture(minimumDistance: 4)
            // Mirrors the fork's decision into gesture-owned state purely for
            // its LIFETIME — nothing reads this value, the reset closure above
            // is the whole point of it. The decision itself stays in
            // `onChanged` so there is one hit-test and one source of truth.
            //
            // `state == nil` makes this write-once per sequence: SwiftUI does
            // not document whether this body or `onChanged` runs first, so on
            // the very first event `grabbed` may still be nil and the pin is
            // captured on the second one instead. That leaves exactly one
            // uncovered case — a gesture cancelled after a single event — and
            // the view model's watchdog is the backstop for it.
            .updating($grabPin) { _, state, _ in
                guard state == nil, case .node(let id) = grabbed else { return }
                state = GraphGrab(id: id, vm: vm)
            }
            .onChanged { value in
                // A start location this accumulator has not seen is a NEW
                // sequence, whatever happened to the last one.
                if panSequence != value.startLocation {
                    // A pin still held here belongs to a sequence that was
                    // CANCELLED — `onEnded` is not guaranteed to run, per the
                    // accumulator comment above — and a pin nobody releases
                    // holds the simulation awake forever. Releasing it as the
                    // next press starts is the same recovery the accumulators
                    // use, applied to the one piece of state that does not
                    // heal itself a frame later.
                    if case .node(let stale) = grabbed { vm.releaseNode(id: stale) }
                    panSequence = value.startLocation
                    appliedPan = .zero
                    grabbed = vm.grabTarget(at: value.startLocation, camera: camera)
                }
                if case .node(let id) = grabbed {
                    vm.dragNode(id: id, toScreen: value.location, camera: camera)
                    return
                }
                camera.pan(by: CGSize(
                    width: value.translation.width - appliedPan.width,
                    height: value.translation.height - appliedPan.height
                ))
                appliedPan = value.translation
            }
            .onEnded { _ in
                // Released here rather than on the next gesture's start: a
                // node left pinned holds the whole simulation awake, which is
                // the one state that never settles on its own.
                if case .node(let id) = grabbed { vm.releaseNode(id: id) }
                grabbed = nil
                panSequence = nil
                appliedPan = .zero
            }
    }

    private var pinchGesture: some Gesture {
        MagnifyGesture()
            .onChanged { value in
                if pinchSequence != value.startLocation {
                    pinchSequence = value.startLocation
                    appliedMagnification = 1
                }
                let previous = appliedMagnification
                // A zero or non-finite frame is SKIPPED, never stored: storing
                // it would make every later frame of the same pinch divide by
                // it, wedging the gesture until the finger lifts.
                guard previous > 0, value.magnification > 0, value.magnification.isFinite else {
                    return
                }
                appliedMagnification = value.magnification
                zoom(by: value.magnification / previous, anchor: value.startLocation)
            }
            .onEnded { _ in
                pinchSequence = nil
                appliedMagnification = 1
            }
    }

    /// Zooms about a screen point, clamping the RESULT into the camera's
    /// range rather than the factor. `GraphCamera.pinch` stays unclamped so
    /// its anchor invariant holds for any factor; the clamp lives here, at the
    /// one layer that knows a human is pinching.
    private func zoom(by factor: CGFloat, anchor: CGPoint) {
        guard camera.scale > 0, factor > 0, factor.isFinite else { return }
        let target = min(max(camera.scale * factor, graphMinScale), graphMaxScale)
        guard target != camera.scale else { return }
        camera.pinch(by: target / camera.scale, anchor: anchor)
    }

    private func zoomAboutCenter(by factor: CGFloat) {
        zoom(by: factor, anchor: CGPoint(x: viewport.width / 2, y: viewport.height / 2))
    }

    private func handleTap(_ location: CGPoint) {
        let render = vm.render
        // Only VISIBLE nodes are candidates — a chip-hidden node draws
        // nothing, so letting it win a tap would open a drawer for something
        // the reader cannot see.
        var visible: [Int64: CGPoint] = [:]
        for n in render.nodes {
            if let p = render.positions[n.id] { visible[n.id] = p }
        }
        // nil is a tap on empty canvas, which deselects.
        vm.select(graphHitTest(
            screenPoint: location, positions: visible,
            camera: camera, maxDistance: graphTapReach
        ))
    }

    // MARK: - framing

    private func fitIfNeeded(size: CGSize) {
        viewport = size
        guard !didFit, size.width > 0, size.height > 0, !vm.render.nodes.isEmpty else { return }
        didFit = true
        fit(size: size)
    }

    private func fit(size: CGSize) {
        guard size.width > 0, size.height > 0, let bounds = vm.render.worldBounds else { return }
        camera = .fitting(worldRect: bounds, viewport: size)
    }

    // MARK: - states

    private var noServer: some View {
        VStack(spacing: 12) {
            Text("No server URL set")
                .font(.voice(15))
                .foregroundStyle(KTheme.counter)
            Text("Set it in Settings, then reopen this screen.")
                .font(.voice(13))
                .foregroundStyle(KTheme.text2)
                .multilineTextAlignment(.center)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

// MARK: - canvas

/// The drawing pass, and nothing else.
///
/// Its own `View` rather than a method on GraphView, conformed to `Equatable`
/// and applied through `.equatable()` at the call site. That combination is
/// what buys the skip, and neither half does alone: SwiftUI does not call `==`
/// on a view's properties just because they happen to be Equatable, so without
/// `EquatableView` wrapping it the conformance would be decoration. With it, a
/// parent body pass that moved neither the render, the camera nor the
/// selection — a chip repaint, a drawer detent drag — leaves this body unrun
/// and the drawing closure uninvoked.
private struct GraphCanvas: View, Equatable {
    let render: GraphRender
    let camera: GraphCamera
    let selectedID: Int64?

    /// Hand-written for the ORDER, which is the whole point: the camera is
    /// what a gesture frame changes, and comparing it first short-circuits
    /// before the render's O(nodes + edges) comparison ever runs. The
    /// synthesized `==` would compare in declaration order and pay that cost
    /// on every frame of every pan. Total over all three stored properties, so
    /// no input can go stale behind it.
    static func == (a: GraphCanvas, b: GraphCanvas) -> Bool {
        a.camera == b.camera && a.selectedID == b.selectedID && a.render == b.render
    }

    var body: some View {
        Canvas { context, size in
            // Everything is culled against the viewport grown by one node's
            // worth of ink, so a node whose centre is just off-screen still
            // contributes its edge and its rim.
            let bounds = CGRect(origin: .zero, size: size).insetBy(dx: -48, dy: -48)
            drawEdges(context: &context, bounds: bounds)
            drawNodes(context: &context, bounds: bounds)
        }
    }

    /// Edges first, so a node always sits on top of the lines into it.
    ///
    /// Both weights accumulate into ONE path each and are stroked twice
    /// total: a stroke per edge is a separate draw call, and this canvas is
    /// heading for thousands of them under a moving finger.
    private func drawEdges(context: inout GraphicsContext, bounds: CGRect) {
        var livePath = Path()
        var deadPath = Path()
        for e in render.edges {
            guard let s = render.positions[e.src], let d = render.positions[e.dst] else { continue }
            let a = camera.worldToScreen(s)
            let b = camera.worldToScreen(d)
            // The inset is load-bearing, not slack: `CGRect.intersects` is
            // documented to return false when EITHER rect is empty, and an
            // exactly horizontal or vertical edge has a zero-height or
            // zero-width box — the two orientations a force layout produces
            // plenty of, all silently culled without it.
            let box = CGRect(
                x: min(a.x, b.x), y: min(a.y, b.y),
                width: abs(a.x - b.x), height: abs(a.y - b.y)
            ).insetBy(dx: -1, dy: -1)
            guard box.intersects(bounds) else { continue }
            // An invalidated fact shown under the toggle is drawn thin and
            // faint rather than in a colour of its own: it is the same
            // relationship, superseded, and the drawer's strike-through is
            // where that is said in words.
            if graphIsInvalidated(e, now: render.now) {
                deadPath.move(to: a)
                deadPath.addLine(to: b)
            } else {
                livePath.move(to: a)
                livePath.addLine(to: b)
            }
        }
        let ink = Color(uiColor: KTheme.hairline2UI)
        context.stroke(livePath, with: .color(ink.opacity(0.7)), lineWidth: 1)
        context.stroke(deadPath, with: .color(ink.opacity(0.28)), lineWidth: 0.5)
    }

    /// The selected node is drawn even when its own type is switched off.
    ///
    /// One id, and it is the one the reader is looking for: a fact's endpoint
    /// tap-through can land on a node whose type the chip row hid, and
    /// selecting it with no ring anywhere on the canvas looks like the tap did
    /// nothing. It is appended LAST so it draws over its neighbourhood.
    private var drawList: [GraphNode] {
        guard let selectedID,
              !render.nodes.contains(where: { $0.id == selectedID }),
              let hiddenSelection = render.nodesByID[selectedID]
        else { return render.nodes }
        return render.nodes + [hiddenSelection]
    }

    private func drawNodes(context: inout GraphicsContext, bounds: CGRect) {
        let ring = Color(uiColor: KTheme.skillGraphUI)
        let labelColor = Color(uiColor: KTheme.textUI)
        let fallback = Color(uiColor: KTheme.text2UI)
        var labels: [GraphLabel] = []
        // Held back rather than drawn in the loop: a node visited later would
        // overdraw it, which would make the one FORCED label the only one
        // sitting under the ink. It goes last, after the capped pass.
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
                Path(ellipseIn: rect),
                with: .color(render.typeColors[n.type] ?? fallback)
            )
            let anchor = CGPoint(x: p.x, y: p.y + radius + 7)
            if selected {
                context.stroke(
                    Path(ellipseIn: rect.insetBy(dx: -2.5, dy: -2.5)),
                    with: .color(ring), lineWidth: 2
                )
                // Kept out of `labels` rather than queued into it: this label
                // is FORCED, and letting it compete would let 40 hubs outrank
                // the one node the reader just asked about.
                selectedLabel = GraphLabel(
                    id: n.id, radius: radius, at: anchor, text: n.name
                )
                continue
            }
            // Label level-of-detail — see graphLabelMinRadius. `radius` is
            // already in SCREEN points (the camera translates nodes, it does
            // not scale them), so multiplying by the camera's scale here would
            // be a unit error: it hid every label at a fitting zoom and gave
            // one to every leaf when zoomed in.
            guard radius >= graphLabelMinRadius else { continue }
            labels.append(GraphLabel(id: n.id, radius: radius, at: anchor, text: n.name))
        }
        // Which labels survive the cap is a total order over the candidates
        // and nothing else, so it lives in GraphModel.swift where a test can
        // reach it — see graphLabelSelection for why the order has to be total.
        for label in graphLabelSelection(labels, cap: graphLabelCap) {
            context.draw(
                Text(label.text).font(.data(9)).foregroundStyle(labelColor),
                at: label.at, anchor: .top
            )
        }
        // Last, so "forced" means on top of the ink as well as exempt from the
        // cap.
        if let selectedLabel {
            context.draw(
                Text(selectedLabel.text).font(.data(9)).foregroundStyle(ring),
                at: selectedLabel.at, anchor: .top
            )
        }
    }
}

// MARK: - drawer

/// The selected node, as a sheet the canvas stays live behind.
///
/// The web board puts this in a right-hand rail. On a phone the same content
/// is a partial-detent sheet: `presentationBackgroundInteraction` keeps the
/// canvas pannable and tappable underneath, so tapping another node swaps the
/// drawer's contents in place rather than making the reader dismiss and
/// re-open, which is the whole interaction this screen is for.
private struct GraphNodeDrawer: View {
    @ObservedObject var vm: GraphViewModel

    /// Read off the live render rather than taken as init parameters, so the
    /// instant every fact row asks the validity question at IS the instant the
    /// canvas asked it, in the same pass — not whichever pass last happened to
    /// rebuild this sheet's content.
    private var names: [Int64: String] { vm.render.names }
    private var now: Int64 { vm.render.now }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if let node = vm.selectedNode {
                        // Identity + description + counts are one themed card;
                        // Facts stay a loose list underneath so a long fact
                        // history doesn't sit inside one enormous card.
                        ThemedCard {
                            VStack(alignment: .leading, spacing: 8) {
                                heading(node)
                                if !node.description.isEmpty {
                                    Text(node.description)
                                        .font(.voice(13))
                                        .foregroundStyle(KTheme.text)
                                }
                                meta(node)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(14)
                        }
                        facts
                    } else {
                        // Selected, but the payload does not carry it and the
                        // detail read has not landed (or failed). Naming the
                        // id beats an empty sheet.
                        Text("node #\(vm.selectedID.map { String($0) } ?? "?") — no details yet")
                            .font(.voice(13))
                            .foregroundStyle(KTheme.text2)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(16)
            }
            .scrollContentBackground(.hidden)
            .background { ThemeBackgroundView() }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    SheetCloseButton { vm.select(nil) }
                }
            }
        }
    }

    private func heading(_ node: GraphNode) -> some View {
        let tint = Color(uiColor: KTheme.graphTypeColorUI(node.type))
        return VStack(alignment: .leading, spacing: 6) {
            Text(node.name)
                .font(.voice(17, weight: .semibold))
                .foregroundStyle(KTheme.text)
            Text(node.type)
                .font(.data(10, weight: .semibold))
                .foregroundStyle(tint)
                .padding(.horizontal, 9)
                .padding(.vertical, 4)
                .background(tint.opacity(0.16), in: Capsule())
                .overlay(Capsule().stroke(tint.opacity(0.5), lineWidth: 1))
        }
    }

    private func meta(_ node: GraphNode) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            // The count comes from the view model, which prefers the detail
            // read's own edge list where it has one — see edgeCount(for:).
            Text("\(vm.edgeCount(for: node)) edges")
                .font(.data(10))
                .foregroundStyle(KTheme.text2)
            // Aliases exist only on the detail read; while it is in flight the
            // payload node has none and this row is simply absent, rather than
            // claiming the entity has no aliases.
            if let aliases = node.aliases, !aliases.isEmpty {
                Text("aliases: \(aliases.joined(separator: ", "))")
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
            }
        }
    }

    private var facts: some View {
        let rows = vm.selectedFacts
        let names = self.names
        let now = self.now
        return VStack(alignment: .leading, spacing: 10) {
            Text("Facts").kLabelStyle()
            if rows.isEmpty {
                Text("no facts recorded")
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
            } else {
                ForEach(rows) { edge in
                    FactRow(edge: edge, names: names, now: now) { vm.select($0) }
                }
            }
        }
    }
}

/// One fact. The wording is the web inspector's, down to the `E<episode> ·
/// strength N · seen N×` meta line, so the two surfaces read the same.
private struct FactRow: View {
    let edge: GraphEdge
    let names: [Int64: String]
    let now: Int64
    /// Tapping either endpoint moves the selection there — the drawer's
    /// version of following an edge, since there is no canvas gesture that
    /// reaches a node you can only see the name of.
    var onSelect: (Int64) -> Void

    private var dead: Bool { graphIsInvalidated(edge, now: now) }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(graphFactText(edge, names: names))
                .font(.voice(12))
                .foregroundStyle(dead ? KTheme.text2 : KTheme.text)
                .strikethrough(dead, color: KTheme.text2)
            HStack(spacing: 4) {
                endpoint(edge.src)
                Text("→").font(.data(9)).foregroundStyle(KTheme.text2)
                endpoint(edge.dst)
            }
            Text(metaLine)
                .font(.data(9))
                .foregroundStyle(KTheme.text2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.leading, 10)
        .padding(.vertical, 2)
        .overlay(alignment: .leading) {
            // The leading bar is the fact's liveness channel; it keeps
            // skillGraph's own hue and just loses its sharp corners.
            Capsule()
                .fill(dead ? KTheme.hairline2 : KTheme.skillGraph)
                .frame(width: 3)
        }
    }

    private var metaLine: String {
        var line = "E\(edge.episode) · strength \(edge.strength) · seen \(edge.occurrences)×"
        if dead {
            let when = graphShortDate(ms: graphInvalidatedAt(edge))
            line += when.isEmpty ? " · invalidated" : " · invalidated \(when)"
        }
        return line
    }

    private func endpoint(_ id: Int64) -> some View {
        Button {
            onSelect(id)
        } label: {
            Text(graphEndpointName(id: id, names: names))
                .font(.data(9, weight: .semibold))
                .foregroundStyle(KTheme.accent)
        }
        .buttonStyle(.plain)
    }
}
