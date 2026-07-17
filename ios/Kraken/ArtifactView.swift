import SwiftUI
import WebKit
import UIKit

// MARK: - WKWebView wrapper

/// What the web view is showing: a live page loaded by URL (html/svg/pdf/
/// images — the server sends a proper Content-Type WKWebView renders
/// natively), or a locally-built HTML string (the markdown/plain-text path
/// below).
private enum ArtifactContent: Equatable {
    case url(URL)
    case html(String)
}

private struct ArtifactWebView: UIViewRepresentable {
    let content: ArtifactContent

    final class Coordinator {
        var lastLoaded: ArtifactContent?
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let webView = WKWebView()
        // Only the container/loading background moves to the theme's ground
        // color — a loaded .html/.svg/.pdf/image artifact's own content is
        // left exactly as the server renders it; the markdown/plain path
        // builds its own dark-themed shell (MarkdownHTML.swift) instead.
        webView.isOpaque = false
        webView.backgroundColor = UIColor(KTheme.ground)
        webView.scrollView.backgroundColor = UIColor(KTheme.ground)
        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        // SwiftUI calls updateUIView on unrelated state changes too; only
        // (re)load when the content actually changed, else every ambient
        // re-render would restart the page / re-inject the HTML string.
        guard context.coordinator.lastLoaded != content else { return }
        context.coordinator.lastLoaded = content
        switch content {
        case .url(let url):
            webView.load(URLRequest(url: url))
        case .html(let html):
            webView.loadHTMLString(html, baseURL: nil)
        }
    }
}

// MARK: - artifact view

/// Whether an artifact renders as a live page (server sends a renderable
/// content type directly), preformatted text, or markdown — chosen by
/// filename extension, mirroring the web UI's isPage() but extended to the
/// content types web.go's artifact handler now serves properly (pdf/images).
private enum ArtifactKind {
    case page      // .html/.htm/.svg/.pdf/.png/.jpg/.jpeg/.gif — WKWebView loads the URL directly
    case plain     // .log/.txt/etc — dark <pre> shell, no markdown transform
    case markdown  // everything else (.md, unknown) — full markdown render

    private static let pageExtensions = [
        ".html", ".htm", ".svg", ".pdf", ".png", ".jpg", ".jpeg", ".gif",
    ]

    init(name: String) {
        let lower = name.lowercased()
        if Self.pageExtensions.contains(where: lower.hasSuffix) {
            self = .page
        } else if lower.hasSuffix(".log") || lower.hasSuffix(".txt") {
            self = .plain
        } else {
            self = .markdown
        }
    }
}

/// Artifact viewer reached from a job's Artifacts list. `path` is
/// webArtifact.url — already a full server-relative path like
/// "/api/artifact/<slug>/cell/<name>" — joined with `baseURL` here.
struct ArtifactView: View {
    let name: String
    let baseURL: String
    let path: String

    @State private var content: ArtifactContent?
    @State private var errorMessage: String?

    private var kind: ArtifactKind { ArtifactKind(name: name) }

    var body: some View {
        Group {
            if let content {
                ArtifactWebView(content: content)
                    .background(KTheme.ground)
            } else if let errorMessage {
                VStack(spacing: 16) {
                    Text(errorMessage)
                        .font(.voice(15))
                        .foregroundStyle(KTheme.counter)
                        .multilineTextAlignment(.center)
                    Button {
                        Task { await load() }
                    } label: {
                        Label("Retry", systemImage: "arrow.clockwise")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Retry")
                    .buttonStyle(.borderedProminent)
                }
                .padding()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(KTheme.ground)
            } else {
                ProgressView()
                    .tint(KTheme.accent)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(KTheme.ground)
            }
        }
        .navigationTitle(name)
        .navigationBarTitleDisplayMode(.inline)
        .task { await load() }
    }

    private func load() async {
        errorMessage = nil
        guard let url = URL(string: baseURL + path) else {
            errorMessage = "invalid artifact URL"
            return
        }
        switch kind {
        case .page:
            // Already renderable server-side (text/html, image/svg+xml,
            // application/pdf, image/*); no fetch needed, WKWebView loads
            // it directly and renders natively.
            content = .url(url)
        case .plain:
            do {
                let text = try await APIClient.shared.fetchArtifactText(baseURL: baseURL, path: path)
                content = .html(plainTextHTML(text))
            } catch {
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        case .markdown:
            do {
                let text = try await APIClient.shared.fetchArtifactText(baseURL: baseURL, path: path)
                content = .html(markdownHTML(text))
            } catch {
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
