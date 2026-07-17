import SwiftUI
import WebKit

/// UIViewRepresentable wrapper so a tapped artifact chip (html/svg deliverable
/// or plain-text file — the server sets Content-Type per web.go's artifact
/// handler, and WKWebView renders either reasonably) opens in-app.
private struct ArtifactWebView: UIViewRepresentable {
    let url: URL

    func makeUIView(context: Context) -> WKWebView {
        WKWebView()
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        // SwiftUI calls updateUIView on unrelated state changes too (e.g. the
        // navigation title); only (re)load when the URL actually changed.
        guard webView.url != url else { return }
        webView.load(URLRequest(url: url))
    }
}

struct ArtifactView: View {
    let name: String
    let url: URL

    var body: some View {
        ArtifactWebView(url: url)
            .navigationTitle(name)
            .navigationBarTitleDisplayMode(.inline)
    }
}
