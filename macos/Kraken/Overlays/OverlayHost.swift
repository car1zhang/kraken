import AppKit
import SwiftUI
import UniformTypeIdentifiers

// OverlayHost.swift — the in-window overlay layer.
//
// IN-WINDOW, not `.sheet`, and that is a mandate rather than a preference
// (53-concessions A5, 54-scope): the scanline rake is drawn as the LAST layer
// of the window's ZStack and has to sit over modals too, because scanlines are
// the CRT glass and glass is in front of whatever the screen shows. An
// NSWindow sheet is a separate window, and no in-window layer can be drawn
// over one. So every overlay is a ZStack layer here, the root applies
// `.hudWindowLayers()` around the whole thing, and this file never draws a
// rake of its own.
//
// Three behaviours the web board carries and this reproduces:
//
//   - the backdrop is a FLAT 72% scrim (`--scrim`) with no blur. The web
//     deliberately has zero `backdrop-filter` (style.css :1455-1467 says why:
//     a per-frame viewport blur for as long as a modal is open), so
//     `.ultraThinMaterial` here would be a departure, not a translation.
//   - a backdrop click closes only when the click landed on the backdrop
//     ITSELF, and only the TOPMOST layer. `Overlay.tsx` spells the first half
//     (`e.target === e.currentTarget`); the second falls out of each layer
//     having its own scrim on the web, and is written explicitly here.
//   - the panel enters over three DISCRETE steps (`panel-in 140ms
//     steps(3, end)`), frozen at rest under Reduce Motion.

// MARK: - pasted images

/// One image lifted off the pasteboard (or a drop, or the file picker) on its
/// way to `POST /api/upload`. The four types the endpoint accepts and nothing
/// else — `pickImageFiles`'s `IMAGE_TYPE_RE` (Overlay.tsx:168).
struct HudPastedImage {
    let data: Data
    let filename: String
    let mimeType: String

    /// png/jpeg/gif/webp only. Anything else answers nil, which is what lets a
    /// caller tell "nothing was pasted" apart from "files were pasted but none
    /// were images" — the second earns a note rather than silence.
    static func mime(forType type: UTType) -> String? {
        switch type {
        case .png: return "image/png"
        case .jpeg: return "image/jpeg"
        case .gif: return "image/gif"
        case .webP: return "image/webp"
        default: return nil
        }
    }

    static func mime(forPathExtension ext: String) -> String? {
        switch ext.lowercased() {
        case "png": return "image/png"
        case "jpg", "jpeg": return "image/jpeg"
        case "gif": return "image/gif"
        case "webp": return "image/webp"
        default: return nil
        }
    }

    /// Reads a dropped/picked file URL, or nil when it is not one of the four.
    static func load(url: URL) -> HudPastedImage? {
        guard let mime = mime(forPathExtension: url.pathExtension),
              let data = try? Data(contentsOf: url)
        else { return nil }
        return HudPastedImage(data: data, filename: url.lastPathComponent, mimeType: mime)
    }

    /// Every image on the general pasteboard. Empty means "let the paste land
    /// as text", which is the whole reason ⌘V is only intercepted when this is
    /// non-empty.
    static func fromPasteboard(_ pb: NSPasteboard = .general) -> [HudPastedImage] {
        var out: [HudPastedImage] = []
        // File promises / real file URLs first: a screenshot dragged out of
        // Finder arrives this way and carries a real name, which the upload's
        // filename should keep.
        if let urls = pb.readObjects(forClasses: [NSURL.self]) as? [URL] {
            for url in urls {
                if let img = load(url: url) { out.append(img) }
            }
        }
        if !out.isEmpty { return out }
        // Raw bytes (⌘⇧4 straight into the clipboard). PNG is what macOS puts
        // there; the other three are checked anyway so a paste from a browser
        // is not silently dropped.
        for (type, mime, ext) in [
            (NSPasteboard.PasteboardType.png, "image/png", "png"),
            (NSPasteboard.PasteboardType(UTType.jpeg.identifier), "image/jpeg", "jpg"),
            (NSPasteboard.PasteboardType(UTType.gif.identifier), "image/gif", "gif"),
            (NSPasteboard.PasteboardType(UTType.webP.identifier), "image/webp", "webp"),
        ] {
            if let data = pb.data(forType: type), !data.isEmpty {
                out.append(HudPastedImage(data: data, filename: "pasted.\(ext)", mimeType: mime))
                break
            }
        }
        return out
    }
}

// MARK: - the panel chrome

/// The modal panel: `min(1120px, 96vw)` wide, top-aligned under a 6vh gap,
/// double-cut at 16pt (top-right and bottom-left), a 2pt accent gradient strip
/// along its top edge, and NO glow ring — style.css :1660-1672 is explicit
/// that the shear is what makes it read as lifted.
struct HudPanel<Content: View>: View {
    @Environment(\.hudTheme) private var theme
    let content: Content

    init(@ViewBuilder content: () -> Content) {
        self.content = content()
    }

    var body: some View {
        GeometryReader { geo in
            VStack(spacing: 0) { content }
                .frame(width: min(1120, geo.size.width * 0.96))
                .frame(maxHeight: geo.size.height * 0.86, alignment: .top)
                .background(HudTheme.mix(theme.ac, 5, over: theme.b0).color)
                .overlay(alignment: .top) {
                    // .panel::before — the 2px accent gradient, ac2 → ac at
                    // 40% → transparent at 70%. A different stop set from the
                    // WINDOW's top edge (30%/60%, Background.swift), so the two
                    // are not one shared gradient.
                    LinearGradient(
                        stops: [
                            .init(color: theme.ac2.color, location: 0),
                            .init(color: theme.ac.color, location: 0.40),
                            .init(color: theme.ac.color(0), location: 0.70),
                            .init(color: theme.ac.color(0), location: 1),
                        ],
                        startPoint: .leading,
                        endPoint: .trailing
                    )
                    .frame(height: 2)
                }
                .hudCut(CornerCuts.panel, border: theme.borderStrongColor)
                .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
                .padding(.top, geo.size.height * 0.06)
        }
    }
}

/// `.panel-head` — ONE LINE, always, the same rule the chrome bar follows.
/// Every control is rigid and only the title gives.
struct HudPanelHead<Content: View>: View {
    @Environment(\.hudTheme) private var theme
    let title: String
    let content: Content

    init(_ title: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        HStack(spacing: 8) {
            HudPanelTitle(title)
            content
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(HudTheme.mix(theme.ac, 8, over: theme.b1).color)
        .overlay(alignment: .bottom) {
            Rectangle().fill(theme.borderColor).frame(height: 1)
        }
    }
}

/// `.panel-title` plus its `::after` block cursor — a `▊` on the shared 1s
/// blink driver, which is why this is its own view: the modifier is
/// main-actor and reaches the shared `SteppedTimer` rather than starting a
/// timer per panel.
struct HudPanelTitle: View {
    @Environment(\.hudTheme) private var theme
    let text: String

    init(_ text: String) { self.text = text }

    var body: some View {
        HStack(spacing: 4) {
            Text(text)
                .hudDisplay(size: 12, tracking: 0.18)
                .foregroundStyle(theme.textColor)
                .lineLimit(1)
                .truncationMode(.tail)
            Text("▊")
                .hudDisplay(size: 12)
                .foregroundStyle(theme.accentColor)
                .hudBlink()
        }
        .layoutPriority(0)
    }
}

// MARK: - the 3-step entrance

/// `@keyframes panel-in { from { opacity: 0; transform: translateY(6px) } }`
/// run over `140ms steps(3, end)`.
///
/// `steps(3, end)` samples the eased progress at `floor(t·3)/3`, so the panel
/// shows FOUR states — (0, +6pt), (⅓, +4pt), (⅔, +2pt), (1, 0) — and never an
/// intermediate one. A per-panel timer rather than one of `Motion.swift`'s
/// shared drivers, deliberately: those exist for the three INFINITE animations
/// (the LED pulse, the ▊ blink, the ring spinner) where lockstep across views
/// is the point. This is a one-shot that starts when its own panel appears.
private struct PanelEntrance: ViewModifier {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var step = 0

    private static let duration: TimeInterval = 0.140
    private static let steps = 3

    func body(content: Content) -> some View {
        let t = Double(step) / Double(Self.steps)
        return content
            .opacity(t)
            .offset(y: 6 * (1 - t))
            .onAppear {
                // "frozen at rest" under Reduce Motion is the END state, not
                // the start one: the web kills the animation outright
                // (style.css :2292-2298), which leaves the element at its
                // un-animated value.
                guard !reduceMotion else {
                    step = Self.steps
                    return
                }
                advance()
            }
    }

    private func advance() {
        guard step < Self.steps else { return }
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.duration / Double(Self.steps)) {
            step += 1
            advance()
        }
    }
}

extension View {
    func hudPanelEntrance() -> some View { modifier(PanelEntrance()) }
}

// MARK: - the host

/// The overlay layer of the window's ZStack.
///
/// It takes no arguments on purpose: the root constructs it as `OverlayHost()`
/// and every layer's dependencies reach it through the environment the root
/// already publishes — the board store, this router, and the usage poll (which
/// is shared with the chrome's widget, so it is created above both rather than
/// inside either).
struct OverlayHost: View {
    @EnvironmentObject private var router: OverlayRouter
    @EnvironmentObject private var usage: UsageModel
    @Environment(\.hudTheme) private var theme

    @State private var keyMonitor: Any?

    var body: some View {
        ZStack {
            ForEach(router.openLayers.sorted(), id: \.rawValue) { kind in
                layer(kind)
                    .zIndex(Double(kind.rawValue))
            }
            HudToastView()
                // above every overlay: a toast reports on an action the user
                // just took, which may well have been inside one (style.css
                // gives it z-index 40 against the overlays' 20-30).
                .zIndex(1000)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        // The whole host is inert when nothing is open — without this the
        // empty ZStack still eats clicks meant for the board underneath.
        .allowsHitTesting(!router.openLayers.isEmpty)
        .onAppear { installMonitor() }
        .onDisappear { removeMonitor() }
    }

    @ViewBuilder
    private func layer(_ kind: OverlayKind) -> some View {
        ZStack {
            // the scrim IS the backdrop-click target: a click that lands on it
            // closes, a click inside the panel does not, and only the TOPMOST
            // layer listens so a stacked artifact never closes the steer modal
            // under it
            theme.scrim
                .ignoresSafeArea()
                .contentShape(Rectangle())
                .onTapGesture {
                    guard router.stack.top == kind else { return }
                    router.close(kind)
                }
            HudPanel { panelContent(kind) }
                .hudPanelEntrance()
                .padding(.horizontal, 8)
        }
    }

    @ViewBuilder
    private func panelContent(_ kind: OverlayKind) -> some View {
        switch kind {
        case .steer:
            if let t = router.steerTarget {
                SteerInspector(slug: t.slug, mode: t.mode, requireText: t.requireText)
            }
        case .editor:
            if let t = router.editorTarget {
                switch t {
                case .newJob: EditorForm(jobKey: nil)
                case .job(let key): EditorForm(jobKey: key)
                }
            }
        case .artifact:
            if let t = router.artifactTarget {
                ArtifactViewer(url: t.url, jobKey: t.jobKey)
            }
        case .thread:
            if let t = router.threadTarget {
                // Straight through. The seam carries the wire's own
                // `"t<digits>"` since the amendment, so there is no conversion
                // on this edge at all — which is the point of the amendment:
                // the shape that reaches the overlay is the shape the server
                // minted.
                ThreadOverlay(
                    slug: t.slug,
                    initialTID: t.tid,
                    onClose: { router.close(.thread) }
                )
            }
        case .usage:
            UsagePanel(usage: usage)
        }
    }

    // MARK: - the Escape / ⌘V monitor

    // ONE local key monitor for the whole window rather than a `.onKeyPress`
    // per overlay. Three reasons, all of them things a per-view handler cannot
    // do: the ordering rule is "highest open layer", which needs a single
    // reader of all of them; the press must be SWALLOWED when it closed
    // something and passed through when it did not; and it has to see the
    // press even when focus sits on a non-input element inside the panel,
    // which is exactly why the vanilla board's own handler is document-level.
    //
    // `addLocalMonitorForEvents` returning nil consumes the event; returning
    // it passes it on.

    private func installMonitor() {
        guard keyMonitor == nil else { return }
        keyMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
            // The event is CLASSIFIED out here and only a Bool crosses into
            // the main-actor call. `NSEvent` is explicitly non-Sendable, so
            // handing the object itself across the boundary is a Swift 6 error
            // — and there is nothing inside `consume` that needs the event
            // beyond which of the two chains it belongs to.
            let isEscape = event.keyCode == 53
            let isPaste = event.modifierFlags.contains(.command)
                && event.charactersIgnoringModifiers?.lowercased() == "v"
            guard isEscape || isPaste else { return event }
            let consumed = MainActor.assumeIsolated { consume(escape: isEscape) }
            return consumed ? nil : event
        }
    }

    private func removeMonitor() {
        if let m = keyMonitor { NSEvent.removeMonitor(m) }
        keyMonitor = nil
    }

    /// Answers whether the press was CONSUMED.
    @MainActor
    private func consume(escape: Bool) -> Bool {
        guard escape else {
            // ⌘V with images on the pasteboard, routed steer-over-editor. A
            // paste with NO image is never touched, so ordinary text paste into
            // the composer behaves exactly as AppKit intends.
            let images = HudPastedImage.fromPasteboard()
            guard !images.isEmpty else { return false }
            return router.routePaste(images)
        }

        // IME: a text view with MARKED text is mid-composition, and Escape
        // there cancels the candidate rather than the modal. Swallowing it
        // would leave a half-composed CJK word with no way to abandon it.
        if let tv = NSApp.keyWindow?.firstResponder as? NSTextView, tv.hasMarkedText() {
            return false
        }

        return router.routeEscape()
    }
}
