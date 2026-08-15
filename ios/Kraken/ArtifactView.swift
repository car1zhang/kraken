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
    /// Loaded instead when `content`'s URL answers non-2xx. Exists for one
    /// case: the app and the daemon ship on independent tracks (`k ship-ios`
    /// vs `k update`), so a phone build that routes .html cards through
    /// /iso.html can meet a Mac whose kraken binary predates that page — the
    /// FileServer then 404s, and without this every diagram card would render
    /// Go's "404 page not found" until the Mac updated. Falling back to the
    /// flat card makes version skew cost the 3D view, never the artifact.
    var fallbackURL: URL?

    final class Coordinator: NSObject, WKNavigationDelegate {
        var lastLoaded: ArtifactContent?
        /// One-shot per load: cleared before the fallback fires, so a broken
        /// fallback target cannot ping-pong.
        var fallbackURL: URL?
        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationResponse: WKNavigationResponse,
            decisionHandler: @escaping (WKNavigationResponsePolicy) -> Void
        ) {
            if let fallback = fallbackURL,
               let http = navigationResponse.response as? HTTPURLResponse,
               !(200...299).contains(http.statusCode),
               navigationResponse.isForMainFrame {
                fallbackURL = nil
                decisionHandler(.cancel)
                // Main-queue hop rather than a synchronous load from inside
                // the policy handler: WebKit is still unwinding the cancelled
                // navigation, and a load issued during that unwind is the
                // known-dropped shape — whose symptom here would be a blank
                // web view, strictly worse than the 404 this path replaces.
                DispatchQueue.main.async {
                    webView.load(URLRequest(url: fallback))
                }
                return
            }
            decisionHandler(.allow)
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let webView = WKWebView()
        webView.navigationDelegate = context.coordinator
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
        context.coordinator.fallbackURL = fallbackURL
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

/// The isoscene viewer URL for an .html diagram card, or nil for every other
/// artifact. kraken serve ships a self-contained /iso.html page (built from
/// the same web/_ui isoscene lib the board uses) that fetches the card named
/// by ?src=, compiles it into the animated isometric scene, and — the half
/// that keeps this function simple — location.replace()s to the flat card
/// when it is bespoke or not a diagram at all. So the app routes every .html
/// card whose path is under /api/artifact/ through the viewer, and never
/// needs its own compilability probe: the page IS the probe, and the fallback
/// is the display-as-is behaviour the spec requires. .svg/.pdf/images stay
/// direct loads; the viewer only means anything for cards.
func isoViewerURL(baseURL: String, path: String, name: String) -> URL? {
    let lower = name.lowercased()
    guard lower.hasSuffix(".html") || lower.hasSuffix(".htm") else { return nil }
    guard path.hasPrefix("/api/artifact/") else { return nil }
    // Hand-encode the query value: URLQueryItem leaves `+` `&` `=` `;` alone
    // (all members of urlQueryAllowed), and the page parses with
    // URLSearchParams' form rules, where a literal `+` reads as a space and a
    // literal `&` truncates the value — either way a legally-named artifact
    // becomes a 404. Percent-encoding those four is what makes the round
    // trip byte-exact. (`%` is already outside urlQueryAllowed, so a
    // filename's own `%` gets escaped without being listed here.)
    var valueAllowed = CharacterSet.urlQueryAllowed
    valueAllowed.remove(charactersIn: "+&=;")
    guard let encoded = path.addingPercentEncoding(withAllowedCharacters: valueAllowed) else { return nil }
    var comps = URLComponents(string: baseURL)
    comps?.path = "/iso.html"
    comps?.percentEncodedQuery = "src=" + encoded
    return comps?.url
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
    /// Whether this instance contributes the feedback pencil to the enclosing
    /// nav bar. ArtifactPagerView is the only caller; it passes false for the
    /// pages it is holding off-screen, because every materialized page's
    /// `.toolbar` resolves against the SAME NavigationStack and an unguarded
    /// one would stack up to three identical pencils in one bar. Exactly the
    /// reason `.navigationTitle` moved up to the pager, applied to the toolbar
    /// item as well.
    ///
    /// Deliberately no default: with one call site a default is unreachable
    /// code, and a second call site should have to answer this rather than
    /// inherit an answer.
    let showsFeedbackButton: Bool
    /// Called after a note has been accepted by the server, with the artifact
    /// it was attached to and the note text — so a presenting JobDetailView
    /// can fold it into its optimistic overlay. nil (the default) simply means
    /// nobody is listening: the note still lands, it just isn't mirrored
    /// anywhere until the next poll. SteerView presents this view that way.
    var onFeedbackCreated: ((ArtifactRef, String) -> Void)?

    @State private var content: ArtifactContent?
    /// The direct artifact URL, kept beside a viewer-routed `content` so the
    /// web view can fall back to the flat card if /iso.html is absent from
    /// the running serve (version skew — see ArtifactWebView.fallbackURL).
    @State private var pageFallback: URL?
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
                ArtifactWebView(content: content, fallbackURL: pageFallback)
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
                    .buttonStyle(.glassProminent)
                }
                .padding()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background { ThemeBackgroundView() }
            } else {
                ProgressView()
                    .tint(KTheme.accent)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background { ThemeBackgroundView() }
            }
        }
        // No .navigationTitle here: this view is always a page inside
        // ArtifactPagerView, and a page's own title inside a TabView is
        // unreliable — the pager titles the bar from its selection instead.
        .toolbar {
            // Only when the path actually parses as an artifact URL: without
            // a {slug, home, name} triple there is nothing to address a
            // create at, and a chip that always 400s is worse than no chip.
            if showsFeedbackButton, let ref = artifactRef {
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
            // Renderable server-side (text/html, image/svg+xml,
            // application/pdf, image/*): WKWebView loads a URL and renders
            // natively — no app-side fetch. An .html card detours through
            // the isoscene viewer page, which compiles it into the animated
            // 3D scene (fetching the card itself, so that path costs a page
            // load plus one same-origin re-read) or bounces straight back to
            // the flat card — see isoViewerURL for why the app never probes
            // compilability. The direct URL rides along as the fallback for
            // a serve that predates /iso.html.
            let viewer = isoViewerURL(baseURL: baseURL, path: path, name: name)
            pageFallback = viewer != nil ? url : nil
            content = .url(viewer ?? url)
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

// MARK: - artifact pager

/// What the artifact sheet actually presents: one ArtifactView per artifact
/// in the job's display order, with a horizontal swipe moving between them.
/// The sheet's own dismiss gesture is vertical, so the horizontal axis was
/// free — nothing had to be given up to page sideways.
///
/// `TabView(selection:) … .tabViewStyle(.page)` is the app's own paging idiom
/// (BoardView's columns, SessionsView's tabs), and as on both of those the
/// swipe is paired with a visible non-swipe selector — here the bottom bar's
/// chevrons. On this screen that pairing is load-bearing rather than a
/// courtesy: a swipe that starts over horizontally-scrollable web content is
/// swallowed by the WKWebView, so the chevrons are the only path that always
/// works.
///
/// **Memory — the one thing to be honest about.** MarkdownText.swift:6-11
/// records that inline WKWebViews once got this app jetsam-killed, and that
/// the only one left is ArtifactView's. A page-style TabView materializes the
/// pages either side of the selected one, so this holds up to THREE live
/// ArtifactViews where today there is one — and a materialized page is not
/// idle: each runs its own `.task { await load() }`, which on the plain and
/// markdown paths really does fetch its artifact, cap it at 1 MB and render it
/// into a WKWebView. Opening the pager therefore fetches and renders BOTH
/// neighbours immediately, and every swipe pulls in one more. Three live web
/// views each holding a rendered document is exactly the shape MarkdownText's
/// history is about, which is why 52-testing-plan.md still owes an Instruments
/// pass on a ≥10-artifact set — that measurement is not discharged by anything
/// here.
///
/// What this code does guarantee is only that it never WIDENS that window:
/// nothing pre-renders, nothing reaches past the adjacent pages, `pages` is
/// read by index and count alone, and the page count in flight is independent
/// of the artifact count. The ±1 window itself is SwiftUI's materialization
/// policy — undocumented, and it has moved between releases. If it ever
/// widens, the ceiling widens with it, and the fallback is spec Q4's single
/// WKWebView driven by a DragGesture.
struct ArtifactPagerView: View {
    let baseURL: String
    /// Already flattened to display order with the singleton fallback applied
    /// (artifactPages), so `selection` is always one of these — which is what
    /// lets every index lookup below treat a miss as impossible rather than
    /// clamping defensively.
    private let pages: [WebArtifact]
    private let onFeedbackCreated: ((ArtifactRef, String) -> Void)?

    /// The URL, never the name: artifactTree rewrites a nested artifact's name
    /// to its leaf, so a name is neither unique across a set nor equal to
    /// anything in the job's wire list. WebArtifact.id is already the url.
    @State private var selection: String

    init(
        artifacts: [WebArtifact],
        selected: WebArtifact,
        baseURL: String,
        onFeedbackCreated: ((ArtifactRef, String) -> Void)? = nil
    ) {
        self.baseURL = baseURL
        self.pages = artifactPages(in: artifacts, selected: selected)
        self.onFeedbackCreated = onFeedbackCreated
        _selection = State(initialValue: selected.url)
    }

    private var index: Int { artifactIndex(of: selection, in: pages) ?? 0 }
    private var neighbours: ArtifactNeighbours { artifactNeighbours(of: selection, in: pages) }

    var body: some View {
        TabView(selection: $selection) {
            ForEach(pages) { artifact in
                ArtifactView(
                    name: artifact.name, baseURL: baseURL, path: artifact.url,
                    showsFeedbackButton: artifact.url == selection,
                    onFeedbackCreated: onFeedbackCreated
                )
                // Tagging by url — the same value ForEach identifies by — is
                // what keeps a page's identity stable across a selection
                // change, and so what keeps ArtifactWebView's `lastLoaded`
                // guard effective: an unstable id would rebuild the coordinator
                // and reload the page on every swipe.
                .tag(artifact.url)
            }
        }
        .tabViewStyle(.page(indexDisplayMode: .never))
        .background { ThemeBackgroundView() }
        .navigationTitle(pages[index].name)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            // Hidden outright for a one-page sheet (a single-artifact job, or
            // the singleton fallback): "1 / 1" between two dead chevrons is
            // noise, and today's behavior is exactly what's left without it.
            if pages.count > 1 {
                ToolbarItemGroup(placement: .bottomBar) {
                    Button {
                        step(to: neighbours.prev)
                    } label: {
                        Image(systemName: "chevron.left")
                    }
                    .buttonStyle(.glass)
                    .controlSize(.large)
                    .tint(KTheme.accent)
                    .disabled(neighbours.prev == nil)
                    .accessibilityLabel("Previous artifact")

                    Spacer()
                    // Text, not the page dots BoardView uses for its five
                    // columns: an artifact set routinely runs past ten, where
                    // dots stop being countable and "n / N" still reads.
                    Text("\(index + 1) / \(pages.count)")
                        .font(.data(11))
                        .foregroundStyle(KTheme.text2)
                        .accessibilityLabel("Artifact \(index + 1) of \(pages.count)")
                    Spacer()

                    Button {
                        step(to: neighbours.next)
                    } label: {
                        Image(systemName: "chevron.right")
                    }
                    .buttonStyle(.glass)
                    .controlSize(.large)
                    .tint(KTheme.accent)
                    .disabled(neighbours.next == nil)
                    .accessibilityLabel("Next artifact")
                }
            }
        }
    }

    /// nil target = an end of the sequence, so this is a no-op rather than a
    /// wrap — the second half of the same clamp the disabled chevrons express,
    /// kept here so the invariant survives a caller that forgets the first.
    private func step(to target: WebArtifact?) {
        guard let target else { return }
        withAnimation { selection = target.url }
    }
}
