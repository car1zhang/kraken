import SwiftUI
import WebKit

// WebShellApp.swift — the whole "web shell" experiment in one file: the board
// SPA served by `kraken serve`, rendered edge-to-edge in a WKWebView under a
// FRAMELESS titlebar — the system traffic lights float inside the web app's
// own chrome bar row, so there is one top bar and it is the web one (carl's
// manual-gate direction; the first cut's separate native strip "looked kind of
// ass").
//
// Three native duties survive, all invisible: a CSS injection that pads the
// web chrome's leading edge so the wordmark clears the traffic lights; a
// transparent drag region over the chrome's non-interactive left cluster
// (WKWebView consumes mouse events, so titlebar dragging cannot work THROUGH
// it — the region sits on top, sized to stop short of the first clickable
// widget); and the link policy — window.open/target=_blank and external link
// clicks leave for the default browser, since a shell with no tabs has
// nowhere else to put them (the first cut dropped them on the floor, which is
// the "links don't work" report).

@main
struct WebShellApp: App {
    var body: some Scene {
        Window("Kraken", id: "web-shell") {
            WebShellRoot()
        }
        .windowStyle(.hiddenTitleBar)
    }
}

struct WebShellRoot: View {
    /// The same stored key the native app's Settings writes, read raw so this
    /// target needs none of the native app's settings machinery.
    @AppStorage("serverURL") private var serverURL = ""
    @State private var failed = false
    @State private var reloadToken = 0

    private var url: URL {
        ServerConfig.baseURL(stored: serverURL.isEmpty ? nil : serverURL)
    }

    var body: some View {
        Group {
            if failed {
                failureNotice
            } else {
                BoardWebView(url: url, reloadToken: reloadToken) { failed = true }
            }
        }
        // Edge to edge: the web chrome's own bar is the titlebar row.
        .ignoresSafeArea(.container, edges: .top)
        // The drag region: a real AppKit view, NOT a SwiftUI gesture — a
        // gesture overlay above a WKWebView loses the event arbitration to
        // the web view's own NSView machinery (measured: dragging simply
        // stopped), while an NSView's mouseDown is unambiguous. It sits over
        // the web chrome's left cluster (wordmark + LED + daemon stat —
        // display-only; the first clickable widget is further right, so 240pt
        // keeps every real control reachable), drags via performDrag, zooms on
        // double-click, and re-centers the traffic lights in the 40px bar.
        .overlay(alignment: .topLeading) {
            WindowChromeRegion()
                .frame(width: 240, height: 40)
        }
        .background(Color(red: 0x0a / 255, green: 0x09 / 255, blue: 0x0d / 255))
    }

    private var failureNotice: some View {
        VStack(spacing: 10) {
            Text("kraken serve is not answering at \(url.absoluteString)")
                .foregroundStyle(.white.opacity(0.7))
            Button("retry") {
                failed = false
                reloadToken += 1
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// The titlebar-replacement region: drags the window, zooms on double-click,
/// and keeps the traffic lights vertically centered in the 40px chrome row.
struct WindowChromeRegion: NSViewRepresentable {
    func makeNSView(context: Context) -> ChromeRegionView { ChromeRegionView() }
    func updateNSView(_ view: ChromeRegionView, context: Context) {}

    final class ChromeRegionView: NSView {
        private var resizeObserver: NSObjectProtocol?
        private var mouseMonitor: Any?

        /// The draggable rect, in window points from the TOP-left: the chrome
        /// row's left cluster — wordmark (starts at the injected 84px pad),
        /// LED, daemon stat. Wide enough to grab comfortably, short of the
        /// first clickable widget.
        private let dragWidth: CGFloat = 240
        private let dragHeight: CGFloat = 40

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            recenterTrafficLights()
            if let window, resizeObserver == nil {
                resizeObserver = NotificationCenter.default.addObserver(
                    forName: NSWindow.didResizeNotification, object: window, queue: .main
                ) { [weak self] _ in
                    MainActor.assumeIsolated { self?.recenterTrafficLights() }
                }
            }
            // The drag lives in a WINDOW-LEVEL event monitor, not this view's
            // mouseDown: as a SwiftUI overlay the view kept losing the AppKit
            // z-order fight with WKWebView's own subview tree (measured twice
            // — SwiftUI gesture first, NSView mouseDown second; both dead).
            // A local monitor sees the event BEFORE any view dispatch, so
            // there is no fight left to lose.
            if mouseMonitor == nil {
                mouseMonitor = NSEvent.addLocalMonitorForEvents(matching: .leftMouseDown) { [weak self] event in
                    guard let self, let window = self.window, event.window === window else { return event }
                    let loc = event.locationInWindow
                    let fromTop = window.frame.height - loc.y
                    guard fromTop >= 0, fromTop <= self.dragHeight, loc.x <= self.dragWidth else { return event }
                    // Traffic lights live in this rect too — never eat their
                    // clicks. With the 8pt right-shift they end near x=84
                    // (before the injected 92px pad puts the wordmark down).
                    guard loc.x > 84 else { return event }
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
            if let resizeObserver { NotificationCenter.default.removeObserver(resizeObserver) }
            if let mouseMonitor { NSEvent.removeMonitor(mouseMonitor) }
        }

        /// The view itself never takes an event — it exists only to hold the
        /// monitor's lifetime and the traffic-light recentering.
        override func hitTest(_ point: NSPoint) -> NSView? { nil }

        /// The buttons' system x, captured the FIRST time each is seen — the
        /// right-shift must be computed from the system position, never from
        /// the current frame, or every resize re-apply would add another 8pt.
        private var systemButtonX: [NSWindow.ButtonType.RawValue: CGFloat] = [:]

        /// The system centers the buttons in its own ~28pt titlebar and hugs
        /// them to the left edge; the web chrome row is 40px and its wordmark
        /// starts further in, so the lights are re-seated: vertically centered
        /// in the 40px row and 8pt further right (carl's eye at the manual
        /// gate). AppKit resets their frames on layout, hence the re-apply on
        /// every window resize.
        private func recenterTrafficLights() {
            guard let window else { return }
            let barHeight: CGFloat = 40
            let rightShift: CGFloat = 8
            for kind in [NSWindow.ButtonType.closeButton, .miniaturizeButton, .zoomButton] {
                guard let button = window.standardWindowButton(kind),
                      let titlebar = button.superview else { continue }
                let systemCenter = titlebar.bounds.height / 2
                let wantedCenterFromTop = barHeight / 2
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
    let reloadToken: Int
    let onFailure: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(onFailure: onFailure) }

    func makeNSView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        // The traffic lights sit over the chrome bar's leading edge, where the
        // SPA puts its wordmark. Injected rather than upstreamed into
        // style.css: the padding is a fact about THIS shell's window chrome,
        // not about the board, and the browser board must not grow a gap for
        // traffic lights it does not have.
        let clearance = """
        const s = document.createElement("style");
        s.textContent = `
          .chrome { padding-left: 92px !important; }
          /* the 2px accent edge strip marks the WINDOW's top edge on the web;
             in the shell the titlebar row already is the window's edge and the
             strip just reads as a stray line through it (carl's call) */
          body::after { display: none !important; }
          /* style.css sets BOTH the standard scrollbar-width/color AND the
             ::-webkit-scrollbar rules. When both are present WebKit lets the
             standard properties win, and on macOS that means auto-hiding
             overlay scrollers — the 8px accent thumb never renders (an
             AppleShowScrollBars per-app default cannot fix this either: the
             WebContent XPC process reads the GLOBAL preference, not this
             app's). Resetting the standard properties re-enables the page's
             own ::-webkit-scrollbar styling — the same rendering Chrome
             shows. Shell-only: in a browser the standard-property path is
             fine and the page should keep it. */
          * { scrollbar-width: auto !important; scrollbar-color: auto !important; }
        `;
        document.head.appendChild(s);
        /* A PR chip is an <a> inside the card's whole-card click target, and
           the SPA build this server embeds predates the source fix that stops
           the bubble — so the click opened GitHub AND the job card. Capture
           phase runs before React's root listener, and stopPropagation leaves
           the anchor's default (the link itself) untouched. Redundant once
           the server embeds the fixed bundle; harmless then. */
        document.addEventListener("click", (e) => {
          if (e.target.closest && e.target.closest(".card a.pr-chip")) e.stopPropagation();
        }, true);
        """
        config.userContentController.addUserScript(
            WKUserScript(source: clearance, injectionTime: .atDocumentEnd, forMainFrameOnly: true)
        )
        let view = WKWebView(frame: .zero, configuration: config)
        view.navigationDelegate = context.coordinator
        view.uiDelegate = context.coordinator
        view.isInspectable = true
        view.underPageBackgroundColor = .clear
        view.load(URLRequest(url: url))
        context.coordinator.lastToken = reloadToken
        context.coordinator.serverHost = url.host
        return view
    }

    func updateNSView(_ view: WKWebView, context: Context) {
        context.coordinator.serverHost = url.host
        if context.coordinator.lastToken != reloadToken {
            context.coordinator.lastToken = reloadToken
            view.load(URLRequest(url: url))
        }
    }

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
        let onFailure: () -> Void
        var lastToken = 0
        var serverHost: String?

        init(onFailure: @escaping () -> Void) {
            self.onFailure = onFailure
        }

        // window.open / target=_blank — the SPA's `↗ tab`, PR chips, artifact
        // links. A web view with no UI delegate drops these SILENTLY; a shell
        // has no tabs, so the default browser is the only right answer.
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

        // A main-frame LINK CLICK that would navigate the shell away from the
        // SPA (an external href without target=_blank) also goes to the
        // browser — the SPA is a resident single page, and replacing it with
        // github.com would strand the board behind a Back gesture the shell
        // does not have. Everything else — the initial load, reloads, the
        // SPA's own hash routing, iframe/artifact subframe navigations — stays
        // in the web view.
        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationAction: WKNavigationAction,
            decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
        ) {
            if navigationAction.navigationType == .linkActivated,
               navigationAction.targetFrame?.isMainFrame == true,
               let target = navigationAction.request.url,
               target.host != serverHost {
                NSWorkspace.shared.open(target)
                decisionHandler(.cancel)
                return
            }
            decisionHandler(.allow)
        }

        func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
            onFailure()
        }

        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            onFailure()
        }
    }
}
