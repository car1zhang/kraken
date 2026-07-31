import SwiftUI
import WebKit

// The knowledge-graph viewer on the phone: a full-screen WKWebView onto the
// SAME `#/graph` route the web board serves, not a second implementation.
//
// That is the spec's own call (50-components.md #8) and it is load-bearing
// rather than lazy. Layout is computed SERVER-side and cached per graph
// revision precisely so both clients draw the same picture — a graph is
// navigated by remembered shape, and two independently-laid-out renderings of
// one graph are two different maps. Everything left on the client is
// viewport, culling, label LOD and hit-testing, which is exactly what the web
// view already does; a native SpriteKit/Canvas port would duplicate all of it
// to arrive at the same pixels.
//
// The consequence, stated plainly: this screen needs the server reachable and
// its embedded SPA assets present. There is no offline or degraded native
// rendering, and no graph data is modeled in Models.swift — see this file's
// note below.

/// Minimal WKWebView wrapper. ArtifactView's own wrapper is deliberately not
/// reused: that one is `private` to its file and models an *artifact*'s two
/// content modes (a URL, or a locally-built HTML string), while this view has
/// exactly one mode and one URL for the whole life of the screen. Copying its
/// two conventions that do apply — the theme-colored container so the load
/// gap isn't a white flash, and the reload guard so SwiftUI's ambient
/// re-renders don't restart the page — is the whole overlap.
private struct GraphWebView: UIViewRepresentable {
    let url: URL

    final class Coordinator {
        var lastLoaded: URL?
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let webView = WKWebView()
        // Only the container/loading background takes the theme colour; the
        // page renders exactly as the server draws it, same as ArtifactView's
        // `.page` path.
        webView.isOpaque = false
        webView.backgroundColor = KTheme.groundUI
        webView.scrollView.backgroundColor = KTheme.groundUI
        // The graph canvas does its own pan and zoom. Leaving WKWebView's
        // scroll view to also bounce would fight it on every drag.
        webView.scrollView.bounces = false
        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        // SwiftUI calls updateUIView on unrelated state changes too; reloading
        // unconditionally would throw away the user's pan/zoom on every
        // ambient re-render, which on this screen is the entire interaction.
        guard context.coordinator.lastLoaded != url else { return }
        context.coordinator.lastLoaded = url
        webView.load(URLRequest(url: url))
    }
}

/// The GRAPH screen. `baseURL` is the same `@AppStorage("serverURL")` value
/// every other screen is handed (SettingsView owns it); the route is appended
/// as a HASH, which is what makes this work with no server-side change at
/// all — a fragment never reaches the server, so `web/serve.go` keeps serving
/// the embedded SPA from `/` and needs no history fallback (the reason
/// useHashRoute.ts gives for choosing hash routing in the first place).
struct GraphView: View {
    let baseURL: String

    private var url: URL? {
        // Trailing slashes are trimmed rather than tolerated: a base of
        // "http://host:7777/" would otherwise produce "…//#/graph", which is
        // a different origin path to the SPA and 404s.
        let trimmed = baseURL.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return nil }
        var base = trimmed
        while base.hasSuffix("/") { base.removeLast() }
        return URL(string: base + "/#/graph")
    }

    var body: some View {
        Group {
            if let url {
                GraphWebView(url: url)
                    .ignoresSafeArea(edges: .bottom)
            } else {
                // A blank/unparseable server URL is a settings problem, and
                // saying so beats an empty screen that reads as an empty
                // graph — the two look identical otherwise.
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
        .background(KTheme.ground)
        .navigationTitle("Graph")
        .navigationBarTitleDisplayMode(.inline)
    }
}
