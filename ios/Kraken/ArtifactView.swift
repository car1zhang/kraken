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
        webView.backgroundColor = KTheme.groundUI
        webView.scrollView.backgroundColor = KTheme.groundUI
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

/// Cap on a fetched text artifact before it's handed to plainTextHTML/
/// markdownHTML. loadHTMLString's escape-then-load path transiently holds
/// several multiples of the input's size in memory (escaped copy + the HTML
/// string + WKWebView's own parse), so an oversized .log/.md artifact can
/// push the app over its memory limit on its own. Tail-truncating bounds the
/// artifact path regardless of the underlying file size — approved default
/// per spec Q6.
private let textArtifactByteCap = 1_000_000

/// Tail-truncates `text` to `textArtifactByteCap` UTF-8 bytes if it's over
/// the cap, cutting on a Unicode-scalar boundary (always valid UTF-8; at
/// worst the first kept character loses leading combining marks, and the
/// newline trim below usually discards that line anyway), then through the
/// first newline so the kept tail starts at a whole line rather than
/// mid-sentence. When truncated, prefixes a one-line plain-text banner to
/// the returned text itself — plainTextHTML/markdownHTML both render a full
/// document shell with no separate slot for one, and this keeps the change
/// contained to this file.
private func truncatedForRender(_ text: String) -> String {
    let byteCount = text.utf8.count
    guard byteCount > textArtifactByteCap else { return text }

    // Single reverse pass accumulating each scalar's UTF-8 width until the
    // next one would cross the cap — O(kept), and unlike a Character-based
    // walk it can't degenerate on a pathological megabyte-wide grapheme
    // cluster, so the memory bound holds unconditionally.
    let scalars = text.unicodeScalars
    var kept = 0
    var cut = scalars.endIndex
    while cut > scalars.startIndex {
        let prev = scalars.index(before: cut)
        let v = scalars[prev].value
        let width = v < 0x80 ? 1 : v < 0x800 ? 2 : v < 0x1_0000 ? 3 : 4
        if kept + width > textArtifactByteCap { break }
        kept += width
        cut = prev
    }

    var tail = scalars[cut...]
    // Drop through the first "\n" so the visible text opens on a whole line.
    // Scalar-level on purpose: a CRLF file's "\r\n" is a single Character,
    // so a Character-level search would never match. A no-op when the kept
    // tail has no newline (e.g. one giant line).
    if let newline = tail.firstIndex(of: "\n") {
        tail = tail[tail.index(after: newline)...]
    }

    let totalMB = Double(byteCount) / 1_000_000
    let banner = String(format: "… truncated: showing last 1 MB of %.1f MB …\n\n", totalMB)
    return banner + String(tail)
}

/// Artifact viewer reached from a job's Artifacts list. `path` is
/// webArtifact.url — already a full server-relative path like
/// "/api/artifact/<slug>/cell/<name>" — joined with `baseURL` here.
struct ArtifactView: View {
    let name: String
    let baseURL: String
    let path: String
    /// Called after a note has been accepted by the server, with the artifact
    /// it was attached to and the note text — so a presenting JobDetailView
    /// can fold it into its optimistic overlay. nil (the default) simply means
    /// nobody is listening: the note still lands, it just isn't mirrored
    /// anywhere until the next poll. SteerView presents this view that way.
    var onFeedbackCreated: ((ArtifactRef, String) -> Void)?

    @State private var content: ArtifactContent?
    @State private var errorMessage: String?
    @State private var composingFeedback = false

    private var kind: ArtifactKind { ArtifactKind(name: name) }

    /// This is the only screen that knows WHICH artifact is on screen, which
    /// is why creating a note lives here and not on the job sheet — the same
    /// reason the web board hangs its feedback chip off the artifact overlay.
    /// The {slug, home, name} triple comes from the URL rather than from
    /// `name`: the artifact tree rewrites a nested artifact's displayed name
    /// to a relative leaf while preserving the url (see artifactParts).
    private var artifactRef: ArtifactRef? { artifactParts(from: path) }

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
                        // solo full-screen recovery CTA, not a circular pill —
                        // keeps its label
                        Label("Retry", systemImage: "arrow.clockwise")
                    }
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
        .toolbar {
            // Only when the path actually parses as an artifact URL: without
            // a {slug, home, name} triple there is nothing to address a
            // create at, and a chip that always 400s is worse than no chip.
            if let ref = artifactRef {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        composingFeedback = true
                    } label: {
                        Label("Feedback", systemImage: "square.and.pencil")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Leave feedback on \(ref.name)")
                    .tint(KTheme.skillDream)
                }
            }
        }
        .sheet(isPresented: $composingFeedback) {
            if let ref = artifactRef {
                NavigationStack {
                    FeedbackComposeView(baseURL: baseURL, target: .create(ref)) { note in
                        onFeedbackCreated?(ref, note)
                    }
                }
            }
        }
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
                let rendered = truncatedForRender(text)
                content = .html(plainTextHTML(rendered))
            } catch {
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        case .markdown:
            do {
                let text = try await APIClient.shared.fetchArtifactText(baseURL: baseURL, path: path)
                let rendered = truncatedForRender(text)
                content = .html(markdownHTML(rendered))
            } catch {
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
