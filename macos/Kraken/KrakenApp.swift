import SwiftUI
import WebKit

// KrakenApp.swift — the macOS board app in one file: the SPA served by
// `kraken serve`, rendered edge-to-edge in a WKWebView under a FRAMELESS
// titlebar — the system traffic lights float inside the web app's own chrome
// bar row, so there is one top bar and it is the web one. This layout won the
// manual-gate A/B against a full native SwiftUI board (preserved in history;
// see macos/project.yml's header).
//
// The native layer does exactly four jobs, the first three invisible when
// they work:
//
//   1. Window chrome — a window-level mouse monitor makes the chrome row's
//      left cluster drag the window and zoom on double-click, and the traffic
//      lights are re-seated into that row (`WindowChromeRegion`).
//   2. A user script injected into the page: pads the chrome's leading edge
//      clear of the traffic lights (a zoom-derived CSS variable the shell
//      re-sets), hides the window-edge accent strip the shell's own edge
//      replaces, re-enables the page's ::-webkit-scrollbar styling, and
//      guards PR-chip clicks against a pre-fix embedded bundle.
//   3. Link policy — window.open/target=_blank and external main-frame
//      navigations leave for the default browser; a shell with no tabs has
//      nowhere else to put them.
//   4. Page zoom — Cmd+= / Cmd+- / Cmd+0 drive `WKWebView.pageZoom` on a
//      browser-style ladder, persisted like a browser's remembered zoom.
//
// After first paint, liveness is the SPA's own job: its chrome already says
// `daemon — restarting` / `kraken web server unreachable`, so the native
// failure notice exists only for loads that never commit.

/// The handful of pixel constants the native chrome and the injected CSS must
/// agree on — plus the zoom ladder, its persisted key and default, and the
/// sanitizer and derivations that scale those constants — in one place so
/// they cannot drift apart silently.
enum ShellMetrics {
    /// Where the traffic lights END after the 8pt right-shift (their zoom
    /// button's trailing edge, measured in the running app at the gate).
    /// Everything left of this passes through the drag monitor untouched so
    /// the buttons keep their clicks.
    static let trafficLightsEnd: CGFloat = 84
    /// The `padding-left` injected onto `.chrome` so its wordmark starts clear
    /// of the lights. Must stay > `trafficLightsEnd` or the seam between them
    /// becomes a dead gap over the lights themselves.
    static let chromePadPx: Int = 92
    /// The drag band's zoom-1 BASELINE: the chrome row's display-only left
    /// cluster (wordmark, LED, daemon stat) at actual size. The live band is
    /// `dragBandWidth(at:)` — this scaled — further capped to
    /// `dragBandMaxWindowFraction` of the window in the mouse monitor, which
    /// is what bounds it when zoomed chrome outgrows a narrow window. Stops
    /// short of the first clickable widget at the board's normal window
    /// widths and zoom ≤ 1; the root view's minWidth holds that at actual
    /// size only (see its comment).
    static let dragWidth: CGFloat = 240
    /// The chrome row's height as designed against: `.chrome` computes its
    /// height from padding + line boxes (~36px rendered) rather than
    /// declaring one, so this is the agreed seat for the lights and the drag
    /// band, not a measurement — carl approved the resulting placement at the
    /// gate, and a `.chrome` font/padding change moves the row without moving
    /// this constant. Being a seat rather than a measurement, it overshoots
    /// the ~36px rendered row by a few points, and the band's
    /// `barHeight * zoom` scales that overshoot with the zoom — the lights'
    /// seat deliberately does NOT scale the same way (it clamps at zoom 1;
    /// see `recenterTrafficLights`). Nor can this constant grow: the zoom-1
    /// seat it implies already sits within a point of the ~28pt system
    /// titlebar container's bottom edge, and a lower seat kills the lights'
    /// clicks.
    static let barHeight: CGFloat = 40

    /// The zoom key + default both sides read (`KrakenApp`'s commands write
    /// it, `KrakenRoot` renders it), named once for the same no-silent-drift
    /// reason as the pixel constants above.
    static let zoomKey = "pageZoom"
    static let zoomDefault = 1.0

    /// The Cmd+= / Cmd+- ladder: Chrome's zoom steps, trimmed to 0.75–3.0.
    /// Both trims are choices about THIS shell rather than derivations: below
    /// 0.75 the pieces that hold still (the lights, the band's constant 92pt
    /// baseline) visibly dwarf the shrinking row, and past 3.0 the board has
    /// already collapsed out from under itself: `pageZoom` shrinks the CSS
    /// viewport, so at 3.0 a laptop sits under the SPA's own 700px breakpoint
    /// and the four-column board renders as a single column. A one-column
    /// board is not a board.
    static let zoomLevels: [Double] = [0.75, 0.8, 0.9, 1.0, 1.1, 1.25, 1.5, 1.75, 2.0, 2.5, 3.0]

    static func zoomIn(from zoom: Double) -> Double {
        zoomLevels.first { $0 > zoom + 0.001 } ?? zoomLevels.last!
    }

    static func zoomOut(from zoom: Double) -> Double {
        zoomLevels.last { $0 < zoom - 0.001 } ?? zoomLevels.first!
    }
    // ^ the 0.001 treats a value within a thousandth of a rung as ON it, so a
    // hand-set `defaults write` value a hair off a rung steps to the next
    // rung instead of snapping back onto the one it already sat at — the code
    // itself only ever stores ladder literals. Off a rung, in/out snap to the
    // nearest rung in the pressed direction.

    /// Sanitizes the persisted zoom before any arithmetic sees it. The key is
    /// hand-editable via `defaults write` exactly like `serverURL`, and a 0,
    /// NaN, or tiny-magnitude value read back from there would reach
    /// `chromePadCSSPx`, whose `Int(_:)` conversion traps on the infinite,
    /// NaN, or Int-overflowing quotient — a launch crash at first paint with
    /// no in-app escape, where `serverURL` at least gets a reset button. (A
    /// negative doesn't trap; it merely yields invalid CSS the page drops.)
    /// Non-finite falls to the default; finite garbage clamps to the ladder's
    /// ends.
    static func clampZoom(_ zoom: Double) -> Double {
        guard zoom.isFinite else { return zoomDefault }
        return min(max(zoom, zoomLevels.first!), zoomLevels.last!)
    }

    /// The chrome padding in CSS px at a given zoom. `pageZoom` scales CSS px,
    /// but the traffic lights the padding clears are native and do not move —
    /// so the padding is divided back down to hold ~`chromePadPx` in window
    /// points at every level (rounded up: too much clearance is cosmetic, too
    /// little puts the wordmark under the lights).
    static func chromePadCSSPx(at zoom: Double) -> Int {
        Int((Double(chromePadPx) / zoom).rounded(.up))
    }

    /// The drag band's width at a given zoom. The padding holds still in
    /// window points (see `chromePadCSSPx`); the left cluster's CONTENT after
    /// it scales with the page, so only that part of the band scales.
    static func dragBandWidth(at zoom: CGFloat) -> CGFloat {
        CGFloat(chromePadPx) + (dragWidth - CGFloat(chromePadPx)) * zoom
    }

    /// The band's ceiling as a fraction of the window, applied in the mouse
    /// monitor. It exists to bound the SCALED band in a narrow window, but it
    /// also binds unzoomed below 600pt (240 / 0.4): at the 560pt minWidth
    /// floor it trims the baseline band to 224pt — 16pt of drag surface over
    /// display-only stat text, traded so the band stays under half the window
    /// everywhere.
    static let dragBandMaxWindowFraction: CGFloat = 0.4
}

@main
struct KrakenApp: App {
    /// Shared with KrakenRoot through UserDefaults (`ShellMetrics.zoomKey`):
    /// commands live on the Scene, the web view lives in the root view, and
    /// @AppStorage is the one channel both sides already speak. Persisted
    /// like a browser's remembered zoom.
    @AppStorage(ShellMetrics.zoomKey) private var pageZoom = ShellMetrics.zoomDefault

    var body: some Scene {
        Window("Kraken", id: "board") {
            KrakenRoot()
        }
        .windowStyle(.hiddenTitleBar)
        .commands {
            // Safari's View-menu trio. ⌘= rather than ⌘+ as the zoom-in key:
            // a "+" key equivalent only matches with shift held, while the
            // gesture everyone types is bare Cmd and the =/+ key. The clamp
            // heals a hand-edited defaults value on the first keypress.
            CommandGroup(after: .toolbar) {
                Button("Actual Size") { pageZoom = ShellMetrics.zoomDefault }
                    .keyboardShortcut("0", modifiers: .command)
                Button("Zoom In") { pageZoom = ShellMetrics.zoomIn(from: ShellMetrics.clampZoom(pageZoom)) }
                    .keyboardShortcut("=", modifiers: .command)
                Button("Zoom Out") { pageZoom = ShellMetrics.zoomOut(from: ShellMetrics.clampZoom(pageZoom)) }
                    .keyboardShortcut("-", modifiers: .command)
            }
        }
    }
}

struct KrakenRoot: View {
    /// The server base URL override. NOTHING in this app writes the key — it
    /// is reachable only via `defaults write dev.carl.KrakenMac serverURL …`,
    /// and the failure notice offers a reset back to the loopback default so
    /// a stale value (a dead port, a non-local host ATS would refuse) cannot
    /// wedge the app with no in-app exit.
    @AppStorage("serverURL") private var serverURL = ""
    /// Written by the Scene's zoom commands; read here so a change re-renders
    /// the representables below, which is what actually applies it.
    @AppStorage(ShellMetrics.zoomKey) private var pageZoom = ShellMetrics.zoomDefault
    @State private var failed = false
    @State private var failureDetail = ""

    private var url: URL {
        ServerConfig.baseURL(stored: serverURL.isEmpty ? nil : serverURL)
    }

    /// Every consumer takes the CLAMPED zoom — the raw key is hand-editable,
    /// and a garbage value must degrade to a sane level, never crash the
    /// first paint (see `clampZoom`).
    private var zoom: Double { ShellMetrics.clampZoom(pageZoom) }

    var body: some View {
        Group {
            if failed {
                failureNotice
            } else {
                // Toggling `failed` swaps the branch and rebuilds the web view
                // from scratch, so "retry" IS a fresh load — no reload channel
                // into the representable is needed.
                BoardWebView(url: url, zoom: zoom) { detail in
                    failureDetail = detail
                    failed = true
                }
            }
        }
        // Edge to edge: the web chrome's own bar is the titlebar row.
        .ignoresSafeArea(.container, edges: .top)
        // Below ~560pt the SPA's view tabs slide left into the drag band and
        // clicks there would move the window instead of switching views. The
        // floor keeps the two rects disjoint at actual size only: zoomed in,
        // the scaled chrome can outgrow any window this floor permits, so the
        // monitor separately caps the band at dragBandMaxWindowFraction of
        // the window, bounding it so most of the row stays clickable.
        .frame(minWidth: 560, minHeight: 320)
        // The native half of the window chrome. The view draws nothing and
        // takes no direct events (its work happens in a window-level monitor —
        // see ChromeRegionView); it lives in the hierarchy only to know the
        // window.
        .overlay(alignment: .topLeading) { WindowChromeRegion(zoom: zoom) }
        .background(Color(red: 0x0a / 255, green: 0x09 / 255, blue: 0x0d / 255))
    }

    private var failureNotice: some View {
        VStack(spacing: 10) {
            Text("kraken serve is not answering at \(url.absoluteString)")
                .foregroundStyle(.white.opacity(0.7))
            // The load error verbatim: "connection refused" and an ATS
            // refusal (a non-local host) need different fixes, and one
            // sentence for both misdiagnoses the second.
            if !failureDetail.isEmpty {
                Text(failureDetail)
                    .font(.system(size: 11))
                    .foregroundStyle(.white.opacity(0.4))
            }
            HStack(spacing: 12) {
                Button("retry") { failed = false }
                if !serverURL.isEmpty {
                    Button("reset to \(ServerConfig.fallbackURL.absoluteString)") {
                        serverURL = ""
                        failed = false
                    }
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// The native half of the frameless titlebar: window drag + double-click zoom
/// over the chrome row's left cluster, and the traffic lights re-seated into
/// that row. All of it hangs off one invisible NSView whose only role is
/// knowing the window.
struct WindowChromeRegion: NSViewRepresentable {
    /// The page zoom, threaded in because the chrome ROW is page content: it
    /// scales with the zoom, and the band and the lights' seat must follow it
    /// or a zoomed-out row leaves the band dragging the tabs row below.
    let zoom: Double

    func makeNSView(context: Context) -> ChromeRegionView { ChromeRegionView() }
    func updateNSView(_ view: ChromeRegionView, context: Context) {
        view.zoom = CGFloat(zoom)
    }

    final class ChromeRegionView: NSView {
        /// The current page zoom; the band's rect and the lights' seat both
        /// derive from it. Re-seating on change keeps the lights centered in
        /// the row at its new rendered height.
        var zoom: CGFloat = 1 {
            didSet { if oldValue != zoom { recenterTrafficLights() } }
        }

        private var windowObservers: [NSObjectProtocol] = []
        /// Weak, and load-bearing: the window retains this view through its
        /// hierarchy, so a strong reference here would be a retain cycle —
        /// the closed window (and its whole WKWebView tree) would never
        /// deallocate and `deinit`'s monitor removal would never run.
        private weak var observedWindow: NSWindow?
        private var mouseMonitor: Any?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            recenterTrafficLights()
            // Re-bound per window, not once: `viewDidMoveToWindow` fires with
            // nil on detach and with a NEW window if the view is ever
            // re-hosted, and an observer pinned to the first window would go
            // quietly inert on the second.
            if let window, observedWindow !== window {
                for token in windowObservers { NotificationCenter.default.removeObserver(token) }
                windowObservers = []
                observedWindow = window
                // Resize is the common relayout; becoming key again is the
                // cheap proxy for the relayouts that are NOT resizes
                // (fullscreen exit, display moves), after which AppKit has
                // reset the buttons to their system seats.
                for name in [NSWindow.didResizeNotification, NSWindow.didBecomeKeyNotification] {
                    windowObservers.append(NotificationCenter.default.addObserver(
                        forName: name, object: window, queue: .main
                    ) { [weak self] _ in
                        MainActor.assumeIsolated { self?.recenterTrafficLights() }
                    })
                }
            }
            // The drag lives in a WINDOW-LEVEL event monitor, installed once:
            // both in-hierarchy attempts lost the event arbitration to
            // WKWebView's own NSView machinery (measured twice — a SwiftUI
            // gesture overlay first, this view's own mouseDown second; both
            // dead). A local monitor sees the event BEFORE any view dispatch,
            // so there is no fight left to lose. The trade, accepted at the
            // gate: the band is window geometry, not page knowledge — with a
            // modal overlay open, a click on the scrim inside the band drags
            // the window instead of dismissing the modal.
            if mouseMonitor == nil {
                mouseMonitor = NSEvent.addLocalMonitorForEvents(matching: .leftMouseDown) { [weak self] event in
                    guard let self, let window = self.window, event.window === window else { return event }
                    let loc = event.locationInWindow
                    // Window-frame arithmetic, correct for the hidden-titlebar
                    // full-size-content window this app always is (frame ==
                    // content there). In fullscreen the lights hide and
                    // performDrag is a no-op, so the band degrades to eating
                    // clicks in dead space rather than misbehaving.
                    let fromTop = window.frame.height - loc.y
                    // The chrome row is page content, so its rendered height
                    // and its left cluster's width scale with the page zoom —
                    // the band scales with them, or zooming out would leave it
                    // dragging the view tabs that slide up under it. The
                    // fraction cap bounds the scaled band in a narrow window,
                    // where zoomed-up chrome overflows and the tabs slide
                    // left as the stats ahead of them collapse: it bounds the
                    // band so most of the row stays clickable.
                    let band = min(ShellMetrics.dragBandWidth(at: self.zoom),
                                   window.frame.width * ShellMetrics.dragBandMaxWindowFraction)
                    guard fromTop >= 0, fromTop <= ShellMetrics.barHeight * self.zoom,
                          loc.x <= band else { return event }
                    // Traffic lights live in this rect too — never eat their
                    // clicks.
                    guard loc.x > ShellMetrics.trafficLightsEnd else { return event }
                    if event.clickCount == 2 {
                        window.performZoom(nil)
                    } else {
                        window.performDrag(with: event)
                    }
                    return nil
                }
            }
        }

        deinit {
            for token in windowObservers { NotificationCenter.default.removeObserver(token) }
            if let mouseMonitor { NSEvent.removeMonitor(mouseMonitor) }
        }

        /// The view itself never takes an event — it exists only to hold the
        /// monitor's lifetime and the traffic-light recentering.
        override func hitTest(_ point: NSPoint) -> NSView? { nil }

        /// The buttons' system x, captured the FIRST time each is seen — the
        /// right-shift must be computed from the system position, never from
        /// the current frame, or every re-apply would add another 8pt.
        private var systemButtonX: [NSWindow.ButtonType.RawValue: CGFloat] = [:]

        /// The system centers the buttons in its own ~28pt titlebar and hugs
        /// them to the left edge; the web chrome row is taller and its
        /// wordmark starts further in, so the lights are re-seated: centered
        /// on the agreed bar height and 8pt further right (carl's eye at the
        /// manual gate). AppKit resets their frames on layout, hence the
        /// re-apply on the notifications above.
        private func recenterTrafficLights() {
            guard let window else { return }
            let rightShift: CGFloat = 8
            for kind in [NSWindow.ButtonType.closeButton, .miniaturizeButton, .zoomButton] {
                guard let button = window.standardWindowButton(kind),
                      let titlebar = button.superview else { continue }
                let systemCenter = titlebar.bounds.height / 2
                // The seat follows the zoom only DOWNWARD. The buttons stay
                // subviews of the system's ~28pt titlebar container, and a
                // frame outside a superview's bounds is dead to hit-testing —
                // the approved zoom-1 seat already sits within a point of
                // that container's bottom edge, so seating any lower would
                // walk the buttons out of it and kill their clicks. Zoomed
                // in, the lights keep the zoom-1 seat and ride the top of the
                // taller row; zoomed out, the seat lifts to match the
                // shrunken row, which moves TOWARD the system seat and has
                // headroom.
                let wantedCenterFromTop = ShellMetrics.barHeight * min(zoom, 1) / 2
                // titlebar coordinates are flipped (origin top-left in the
                // titlebar container view); shift down by the center delta
                let delta = wantedCenterFromTop - systemCenter
                var frame = button.frame
                frame.origin.y = (titlebar.bounds.height - button.frame.height) / 2
                    + (titlebar.isFlipped ? delta : -delta)
                let baseX = systemButtonX[kind.rawValue] ?? frame.origin.x
                systemButtonX[kind.rawValue] = baseX
                frame.origin.x = baseX + rightShift
                button.frame = frame
            }
        }
    }
}

struct BoardWebView: NSViewRepresentable {
    let url: URL
    /// Browser-style page zoom (Cmd+= / Cmd+- / Cmd+0), applied as
    /// `WKWebView.pageZoom` so it reflows CSS layout exactly the way the same
    /// board zooms in a browser.
    let zoom: Double
    /// Called with the load error's own description when a navigation fails
    /// for real (cancellations filtered — see the delegate).
    let onFailure: (String) -> Void

    /// The one statement that moves `--shell-pad`, shared by the user
    /// script's seed, the zoom-change path, and the per-commit re-assert so
    /// its three writers cannot drift on the value they compute.
    private static func shellPadJS(at zoom: Double) -> String {
        "document.documentElement.style.setProperty('--shell-pad', '\(ShellMetrics.chromePadCSSPx(at: zoom))px')"
    }

    func makeCoordinator() -> Coordinator { Coordinator(onFailure: onFailure) }

    func makeNSView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        // Injected rather than upstreamed into style.css: every rule below is
        // a fact about THIS shell's window, not about the board, and the
        // browser board must not grow a gap for traffic lights it does not
        // have. `.atDocumentStart` + documentElement because the SPA is a
        // module script that can commit its first render inside the same
        // parse window `.atDocumentEnd` runs in — injecting at start removes
        // the one-frame flash of unpadded chrome (head may not exist yet at
        // that point; documentElement always does).
        let clearance = """
        const s = document.createElement("style");
        s.textContent = `
          /* the padding rides a variable because pageZoom scales CSS px while
             the traffic lights it clears are native and hold still — the
             shell re-sets the var on zoom changes and on every commit to keep
             the clearance constant in window points (see chromePadCSSPx). The
             zoom-1 fallback only covers a throw between the style append and
             the setProperty below; the same commit's re-assert repairs it */
          .chrome { padding-left: var(--shell-pad, \(ShellMetrics.chromePadPx)px) !important; }
          /* the 2px accent edge strip marks the WINDOW's top edge on the web;
             in the shell the titlebar row already is the window's edge and the
             strip just reads as a stray line through it (carl's call) */
          body::after { display: none !important; }
          /* style.css sets BOTH the standard scrollbar-width/color AND the
             ::-webkit-scrollbar rules; when both are present WebKit lets the
             standard properties win, and on macOS that means auto-hiding
             overlay scrollers — the 8px accent thumb never renders. Resetting
             the standard properties re-enables the page's own
             ::-webkit-scrollbar styling — the same rendering Chrome shows.
             Shell-only: in a browser the standard-property path is fine. */
          * { scrollbar-width: auto !important; scrollbar-color: auto !important; }
        `;
        /* A PR chip is an <a> inside the card's whole-card click target; the
           SPA source now stops the bubble itself (PRChips.tsx), but a server
           embedding a pre-fix bundle would still open GitHub AND the job
           card. Capture phase runs before React's root listener, and
           stopPropagation leaves the anchor's default (the link itself)
           untouched. Redundant against a current bundle; harmless then.
           Registered BEFORE the style append so a throw in either half never
           costs the other. */
        document.addEventListener("click", (e) => {
          if (e.target.closest && e.target.closest(".card a.pr-chip")) e.stopPropagation();
        }, true);
        document.documentElement.appendChild(s);
        \(Self.shellPadJS(at: zoom));
        """
        config.userContentController.addUserScript(
            WKUserScript(source: clearance, injectionTime: .atDocumentStart, forMainFrameOnly: true)
        )
        let view = WKWebView(frame: .zero, configuration: config)
        view.navigationDelegate = context.coordinator
        view.uiDelegate = context.coordinator
        // Web Inspector stays available in Release too, deliberately: this
        // app is a local dev tool inspecting its own board, and the person
        // launching it is the person who would reach for the inspector.
        view.isInspectable = true
        // NO back/forward swipe gestures, considered and rejected: the board
        // itself scrolls horizontally at every ordinary window width (four
        // 200px-minimum columns), and an overscroll past its edge would
        // trigger a history swipe — breaking a core gesture to defend against
        // a same-host main-frame navigation nothing in the SPA emits.
        view.underPageBackgroundColor = .clear
        // serverHost is set BEFORE the load: the policy delegate treats a nil
        // serverHost as "not the server", so the reverse order would leave the
        // app's own initial load racing the assignment — losing that race
        // would hand the board to the default browser and show nothing.
        context.coordinator.serverHost = url.host()
        context.coordinator.zoom = zoom
        view.pageZoom = zoom
        view.load(URLRequest(url: url))
        return view
    }

    func updateNSView(_ view: WKWebView, context: Context) {
        context.coordinator.serverHost = url.host()
        context.coordinator.zoom = zoom
        // Guarded: updateNSView fires for unrelated state too, and this path
        // only owns zoom CHANGES on a live document — the user script seeds a
        // fresh view, and the coordinator's didCommit re-asserts across the
        // page's own reloads, which updateNSView never sees.
        if view.pageZoom != zoom {
            view.pageZoom = zoom
            view.evaluateJavaScript(Self.shellPadJS(at: zoom))
        }
    }

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
        let onFailure: (String) -> Void
        var serverHost: String?
        /// The zoom in force, for the per-commit `--shell-pad` re-assert.
        var zoom: Double = ShellMetrics.zoomDefault

        init(onFailure: @escaping (String) -> Void) {
            self.onFailure = onFailure
        }

        /// Every main-frame commit re-runs the user script, whose SOURCE was
        /// interpolated once at makeNSView — so the page's own reloads (the
        /// UPDATE flow's post-update reload, its banner's reload action)
        /// re-stamp the pad for whatever zoom was current back then, and the
        /// zoom-equality guard in updateNSView can never repair it. Re-assert
        /// the live value on every commit instead; it runs after the
        /// document-start script, so it wins.
        func webView(_ webView: WKWebView, didCommit navigation: WKNavigation!) {
            // The pageZoom re-set is belt-and-braces beside the pad: a no-op
            // when the view kept it across the load, the repair if it didn't.
            webView.pageZoom = zoom
            webView.evaluateJavaScript(BoardWebView.shellPadJS(at: zoom))
        }

        /// Host equality with loopback's spellings unified — `localhost` and
        /// `127.0.0.1` are one origin to this app, and a stored URL using one
        /// spelling must not classify the other's links as external.
        private func isServerHost(_ host: String?) -> Bool {
            guard let host = host?.lowercased(), let server = serverHost?.lowercased() else { return false }
            let loopback: Set<String> = ["localhost", "127.0.0.1", "::1"]
            if loopback.contains(host), loopback.contains(server) { return true }
            return host == server
        }

        // window.open / target=_blank — the SPA's `↗ tab`, PR chips, every
        // markdown link. A web view with no UI delegate drops these SILENTLY;
        // a shell has no tabs, so the default browser is the only right
        // answer.
        func webView(
            _ webView: WKWebView,
            createWebViewWith configuration: WKWebViewConfiguration,
            for navigationAction: WKNavigationAction,
            windowFeatures: WKWindowFeatures
        ) -> WKWebView? {
            if let target = navigationAction.request.url {
                NSWorkspace.shared.open(target)
            }
            return nil
        }

        // <input type="file"> — the board's ▨ BG background upload. WebKit
        // routes a file-input click to this delegate method, and a UI
        // delegate WITHOUT it drops the click SILENTLY (the same
        // no-delegate-no-behavior shape as createWebViewWith above) — the
        // picker simply never opens, which is exactly how the shell shipped
        // broken while Safari on the same board worked.
        func webView(
            _ webView: WKWebView,
            runOpenPanelWith parameters: WKOpenPanelParameters,
            initiatedByFrame frame: WKFrameInfo,
            completionHandler: @escaping ([URL]?) -> Void
        ) {
            let panel = NSOpenPanel()
            panel.canChooseFiles = true
            panel.canChooseDirectories = parameters.allowsDirectories
            panel.allowsMultipleSelection = parameters.allowsMultipleSelection
            // A sheet on the shell window where there is one, a floating
            // panel otherwise — begin(completionHandler:) covers the
            // no-window edge without a second code path.
            if let window = webView.window {
                panel.beginSheetModal(for: window) { response in
                    completionHandler(response == .OK ? panel.urls : nil)
                }
            } else {
                panel.begin { response in
                    completionHandler(response == .OK ? panel.urls : nil)
                }
            }
        }

        // Any main-frame navigation to an EXTERNAL host goes to the browser —
        // not just link clicks: a JS `location.href`, a meta refresh or a
        // redirect chain would equally replace the resident SPA. The initial
        // load and the SPA's own reloads are same-host and pass; same-document
        // hash routing never consults this delegate at all.
        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationAction: WKNavigationAction,
            decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
        ) {
            if navigationAction.targetFrame?.isMainFrame == true,
               let target = navigationAction.request.url,
               target.scheme?.hasPrefix("http") == true,
               !isServerHost(target.host()) {
                NSWorkspace.shared.open(target)
                decisionHandler(.cancel)
                return
            }
            decisionHandler(.allow)
        }

        /// The two error codes that are NOT "the server is down": a provisional
        /// load superseded by another, and WebKit reporting the very cancel
        /// our own policy handler issued. Flipping the app to the failure
        /// notice on either replaces a healthy board with a lie. Domains
        /// checked alongside the codes — a bare 102 in some third domain is
        /// not this.
        private func isRoutineCancellation(_ error: Error) -> Bool {
            let e = error as NSError
            if e.domain == NSURLErrorDomain, e.code == NSURLErrorCancelled { return true }
            if e.domain == "WebKitErrorDomain", e.code == 102 { return true }
            return false
        }

        func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
            guard !isRoutineCancellation(error) else { return }
            onFailure(error.localizedDescription)
        }

        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            guard !isRoutineCancellation(error) else { return }
            onFailure(error.localizedDescription)
        }
    }
}
