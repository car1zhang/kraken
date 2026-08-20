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

/// A live handle on the artifact screen's one WKWebView, so the pager's
/// chrome can ask the DOCUMENT a question the SwiftUI tree cannot answer:
/// what, if anything, is selected right now.
///
/// A reference type passed down rather than a value passed up, for the reason
/// the ✓ control exists at all: the toolbar lives on the PAGER (one instance,
/// see `showsFeedbackButton`) while the web view lives on a page, and the two
/// are separated by a TabView that materializes up to three pages. The handle
/// is claimed by whichever page is SELECTED, so the chrome always reads the
/// document it is drawn over.
///
/// `@MainActor` because `evaluateJavaScript` is: this is UIKit's thread, not a
/// concurrency decoration.
@MainActor
final class ArtifactWebViewHandle {
    /// Weak: the web view belongs to SwiftUI's representable lifecycle, and a
    /// strong reference here would keep a swiped-away page's rendered document
    /// alive — the exact memory shape ArtifactPagerView's header is about.
    fileprivate weak var webView: WKWebView?

    /// The document's current selection, trimmed; `""` for none, for no web
    /// view yet, and for any evaluation failure.
    ///
    /// The expression always yields a JS string (`|| ''`) rather than
    /// `undefined`: the async `evaluateJavaScript` bridges its result as a
    /// non-optional `Any`, and a null result is a known trap on that overload.
    func selectionText() async -> String {
        guard let webView else { return "" }
        let js = "(window.getSelection && window.getSelection().toString()) || ''"
        let result = try? await webView.evaluateJavaScript(js)
        return ((result as? String) ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Drops the document's selection.
    ///
    /// Called after a selection has been marked, and it has to reach the DOM
    /// rather than only the app's mirror of it: the mirror is refilled by the
    /// next poll off the web view's own still-live selection, so clearing one
    /// side alone would flip the control back within a tick. Wrapped in an IIFE
    /// returning `''` for `selectionText`'s reason — `removeAllRanges()`
    /// evaluates to `undefined`, which the async bridge is a known trap on.
    func collapseSelection() async {
        guard let webView else { return }
        let js = "(function(){ if (window.getSelection) { window.getSelection().removeAllRanges(); } return ''; })()"
        _ = try? await webView.evaluateJavaScript(js)
    }
}

private struct ArtifactWebView: UIViewRepresentable {
    let content: ArtifactContent
    /// Set on every update while this page is the selected one — never
    /// cleared, since the next selected page claims it and a clear racing that
    /// claim would blank a live handle.
    var handle: ArtifactWebViewHandle?
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
        // Claimed BEFORE the content guard below, not after: a page that is
        // already loaded returns there, and a selection change is exactly the
        // case where nothing about the content moved.
        handle?.webView = webView
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

    /// Whether a text selection on this page may be marked into the mental
    /// model.
    ///
    /// FALSE for `.page`, and that is a data-integrity gate rather than a
    /// nicety: a `.page` artifact is loaded by URL and the document on screen
    /// is the server's own rendering — an `.html` diagram card (via /iso.html),
    /// an `.svg`, a `.pdf`. `window.getSelection()` there returns text out of
    /// MARKUP, while `POST /api/mm/element` locates what it is sent in the
    /// artifact's RAW bytes and freezes the source lines it lands on — so a
    /// selection in a diagram card would be recorded as "what carl read" in
    /// the form of a line of html. The web is immune by construction — its
    /// `selectionchange` listener drops any selection whose body does not
    /// carry the `md` class — and this gate closes the same hole, spelled
    /// where the app decides how to render.
    ///
    /// It does NOT close it identically: `.plain` is allowed here, where the
    /// web's own plain-text body (`panel-body raw`) has no `md` class and so
    /// offers nothing. Deliberate — a `.log`/`.txt` is shown as its own bytes,
    /// so a selection there IS a selection of the file, and the server locates
    /// it in those same bytes. The exclusion that matters is `.page`, where
    /// the document on screen and the file on disk are different things.
    var allowsTextMarks: Bool {
        switch self {
        case .page: return false
        case .plain, .markdown: return true
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
    /// Handed down by the pager for the SELECTED page only, so the ✓ control
    /// on its toolbar can read this document's selection. nil on the two
    /// off-screen neighbours — see `ArtifactWebViewHandle`.
    var webViewHandle: ArtifactWebViewHandle?

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
                ArtifactWebView(content: content, handle: webViewHandle, fallbackURL: pageFallback)
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

/// The one thing the pager's bookmark control can do for the page on screen.
///
/// A two-case enum rather than a bool plus an optional triple because the two
/// halves carry different payloads and neither is derivable from the other: a
/// remove needs a library row id, a save needs the artifact-route triple, and
/// a library SNAPSHOT url has the first while having no second. Deciding once
/// and carrying the decision is also what keeps the glyph and the press from
/// disagreeing.
private enum ArtifactBookmarkAction {
    /// Not in the library: freeze this live artifact's current bytes.
    case save(ArtifactRef)
    /// Already a library row — either this live artifact's own saved copy, or
    /// (opened from the Memory tab) that copy itself. Removable by id either
    /// way.
    case remove(WebLibraryItem)

    var isSaved: Bool {
        switch self {
        case .save: return false
        case .remove: return true
        }
    }

    /// The artifact's name, for the accessibility sentence.
    var name: String {
        switch self {
        case .save(let ref): return ref.name
        case .remove(let item): return item.name
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

    /// The saved-artifact library as the server currently holds it — what the
    /// bookmark paints from. SERVER state, never a local latch: a save that is
    /// refused leaves the glyph unfilled because the refreshed list still has
    /// no row for this artifact, which is the honest answer and needs no undo
    /// path of its own.
    @State private var library: [WebLibraryItem] = []
    /// A save/remove POST is in the air — the button disables, so a double tap
    /// can't post a save and a remove of the same artifact past each other.
    @State private var bookmarkInFlight = false

    /// The mental model as the server currently holds it — what the ✓ control
    /// paints from, on exactly the terms `library` above is held: SERVER
    /// state, never a local latch. It matters more here than it does for ☆,
    /// because the daemon's verify pass invalidates marks behind the app's
    /// back, and a latched ✓ would keep claiming a reading the server has
    /// already retired.
    @State private var mm = MMViewModel()
    /// The selected page's live WKWebView, for reading its selection.
    @State private var webHandle = ArtifactWebViewHandle()
    /// What is selected in that document right now, "" for nothing.
    ///
    /// POLLED rather than observed. WebKit reports `selectionchange` only to
    /// the page's own JS, so surfacing it would mean injecting a script and a
    /// message handler into every artifact — including live server-rendered
    /// ones — and unwinding that handler on teardown to avoid the classic
    /// `userContentController` retain cycle. A cheap `evaluateJavaScript` on
    /// an idle web view buys the same fact with none of that, and the cost is
    /// bounded by the `.task`'s own lifetime: it runs only while an artifact
    /// sheet is on screen.
    @State private var selectionInPage = ""

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
                    onFeedbackCreated: onFeedbackCreated,
                    // The SELECTED page only, for `showsFeedbackButton`'s
                    // reason: there is one toolbar, so there is one document
                    // it can be reading.
                    webViewHandle: artifact.url == selection ? webHandle : nil
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
        .task(id: baseURL) { await loadLibrary() }
        .task(id: baseURL) { await mm.refresh(baseURL: baseURL) }
        // Restarted on every page change (SwiftUI cancels a `.task(id:)` when
        // its id moves), which is what clears the selection carried over from
        // the page just swiped away — synchronously, in `watchSelection`'s
        // first statement, rather than whenever the first poll of the new
        // document answers. See there for why the gap mattered.
        .task(id: selection) { await watchSelection() }
        .toolbar {
            // Beside ☆ save and painted the same way — one control, on the
            // pager's own bar, for the reason the title and the bookmark are
            // here (see `showsFeedbackButton`): a per-page ✓ would stack up to
            // three of them in one bar.
            if let action = readAction {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        Task { await toggleRead(action) }
                    } label: {
                        Label(action.title, systemImage: action.icon)
                    }
                    .labelStyle(.iconOnly)
                    .disabled(mm.writing || !mm.inFlight.isEmpty)
                    .tint(action.isMarked ? KTheme.solved : KTheme.accent)
                    .accessibilityLabel(action.accessibility)
                    .accessibilityAddTraits(action.isMarked ? [.isSelected] : [])
                }
            }
            // The save toggle lives HERE, on the pager's own bar, for exactly
            // the reason the title does (see `showsFeedbackButton`): every
            // materialized page's `.toolbar` resolves against the same
            // NavigationStack, so a per-page bookmark would stack up to three
            // of them. One toolbar, one instance, painted from the SELECTED
            // page — which is also the only page a save could mean.
            //
            // Present only when there is something for it to do — see
            // `bookmarkAction`, which is also where the two ways a page can
            // be removable are reconciled.
            if let action = bookmarkAction {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        Task { await toggleBookmark(action) }
                    } label: {
                        Label(
                            action.isSaved ? "Saved" : "Save",
                            systemImage: action.isSaved ? "bookmark.fill" : "bookmark"
                        )
                    }
                    .labelStyle(.iconOnly)
                    .disabled(bookmarkInFlight)
                    .tint(KTheme.accent)
                    .accessibilityLabel(
                        action.isSaved
                            ? "Remove \(action.name) from the library"
                            : "Save \(action.name) to the library"
                    )
                    .accessibilityAddTraits(action.isSaved ? [.isSelected] : [])
                }
            }
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

    // MARK: - the save toggle

    /// What the bookmark can do for the page on screen, or nil for no control
    /// at all.
    ///
    /// The library row is consulted FIRST, and that ordering is the whole of
    /// the cross-client parity fix. Two different urls can name a library row:
    /// a LIVE artifact url that a saved row froze (matched on `(slug, name)`),
    /// and the SNAPSHOT url of the row itself — which is what a page opened
    /// from the Memory tab carries, and which yields no `artifactParts` triple
    /// at all. Both are removable, by id, and both therefore paint filled;
    /// gating the control on the triple alone left a snapshot showing no
    /// bookmark where the web board shows ★ saved and offers the removal.
    ///
    /// Save is the fallback, not the default: a page reaches it only by
    /// parsing as a live artifact address AND matching no row. A url that is
    /// neither — an upload, a malformed path — gets no control, since its only
    /// available outcome would be a refusal.
    private var bookmarkAction: ArtifactBookmarkAction? {
        if let saved = libraryItemMatching(url: selection, in: library) { return .remove(saved) }
        if let ref = artifactParts(from: selection) { return .save(ref) }
        return nil
    }

    /// Reads the library once per sheet (and again after every toggle). Not
    /// polled: nothing else on this phone can change the library while this
    /// sheet is up, so on this phone a repeat read could only ever return what
    /// the first one did.
    ///
    /// A failed read leaves `library` as it was and says nothing: the bookmark
    /// is a secondary affordance, and an error banner over an artifact the
    /// reader opened to READ would be the wrong trade. The glyph then reads
    /// unsaved, and the save it invites is an upsert server-side — so the
    /// worst case of getting this wrong is a re-save that changes nothing.
    private func loadLibrary() async {
        guard let payload = try? await APIClient.shared.fetchLibrary(baseURL: baseURL) else { return }
        library = payload.items
    }

    /// Acts on the action the button was PAINTED from, rather than re-deciding
    /// here: the two must agree, and re-running the decision against a
    /// `library` that a concurrent `loadLibrary` may have replaced is exactly
    /// how a press on ★ could post a save.
    private func toggleBookmark(_ action: ArtifactBookmarkAction) async {
        guard !bookmarkInFlight else { return }
        bookmarkInFlight = true
        defer { bookmarkInFlight = false }
        do {
            switch action {
            case .remove(let saved):
                try await APIClient.shared.removeFromLibrary(baseURL: baseURL, id: saved.id)
            case .save(let ref):
                try await APIClient.shared.saveToLibrary(
                    baseURL: baseURL, slug: ref.slug, home: ref.home, name: ref.name
                )
            }
        } catch {
            // Swallowed deliberately, and then re-read below: the button
            // paints from the server's list, so a refusal shows up as the
            // glyph simply not changing. Mirroring the intent locally is the
            // one thing that must not happen — it would claim a save the
            // server refused.
        }
        await loadLibrary()
    }

    // MARK: - the ✓ read toggle

    /// What the ✓ can do for the page on screen, or nil for no control.
    ///
    /// The SELECTION is consulted first, and that ordering IS the feature
    /// (carl, gate R1: "leave read the same button as mark selection"). With
    /// nothing selected the control is a whole-artifact toggle painted from
    /// the server's list; the moment a non-empty selection exists in a
    /// document whose kind allows text marks (allowsTextMarks — never a
    /// .page) it becomes the mark-selection form, and the artifact's own
    /// marked state stops being what it means — which is why that state is not
    /// even computed on this branch.
    ///
    /// Every branch needs the artifact-route triple, so a page that does not
    /// parse as one — a library SNAPSHOT url opened from the Memory tab, an
    /// upload — gets no control at all: there is nothing for
    /// `POST /api/mm/artifact` to address.
    ///
    /// That is where ✓ and ★ genuinely differ, and the asymmetry is worth
    /// stating because the two controls sit side by side. `bookmarkAction`
    /// above still offers a snapshot url a filled ★ with a working removal,
    /// because a library row is addressable BY ID and the url carries that id.
    /// A mark has no such second address: `/api/mm/artifact` takes the
    /// {slug, home, name} triple and nothing else, so where the star is
    /// offered the read mark cannot be.
    private var readAction: ArtifactReadAction? {
        guard let ref = artifactParts(from: selection) else { return nil }
        if !selectionInPage.isEmpty, ArtifactKind(name: pages[index].name).allowsTextMarks {
            return .markSelection(ref, selectionInPage)
        }
        if let item = mmMatch(items: mm.items, slug: ref.slug, name: ref.name) {
            return .unmark(ref, item)
        }
        return .mark(ref)
    }

    /// Acts on the action the button was PAINTED from rather than re-deciding
    /// here, for `toggleBookmark`'s reason: re-running the decision against an
    /// `mm` a concurrent refresh may have replaced is how a press on a ✓ could
    /// post a mark. Every branch refetches inside the model, so the control's
    /// next state is the server's answer either way.
    private func toggleRead(_ action: ArtifactReadAction) async {
        switch action {
        case .mark(let ref):
            await mm.markArtifact(baseURL: baseURL, ref: ref)
        case .unmark(_, let item):
            await mm.remove(baseURL: baseURL, id: item.id)
        case .markSelection(let ref, let text):
            // The RENDERED text, exactly as the web sends it: the server
            // locates that span in the artifact's markdown source and stores
            // the source lines, so structure survives a mark made here.
            let landed = await mm.markElement(baseURL: baseURL, ref: ref, text: text)
            guard landed else { return }
            // Both sides of the selection, on success only — the web clears
            // its own for the same reason. A selection mark has no pressed
            // state (there is none on the wire), so the control returning to
            // the whole-artifact toggle IS the confirmation the press landed;
            // left standing it invites a second press whose only outcome is
            // `already: true`, and it holds the artifact toggle out of reach.
            // A REFUSED mark deliberately leaves the selection alone: nothing
            // on this screen renders the error, so clearing it there would be
            // the app's only signal, saying the opposite of what happened.
            selectionInPage = ""
            await webHandle.collapseSelection()
        }
    }

    /// Keeps `selectionInPage` in step with the document while this page is up.
    ///
    /// A poll rather than an event stream — see `selectionInPage` for why —
    /// and an unconditional loop rather than a repeating timer, so its lifetime
    /// is exactly the `.task`'s: SwiftUI cancels it when the page changes or
    /// the sheet closes, and the sleep returns immediately once cancelled.
    private func watchSelection() async {
        // FIRST, before the first read: `selection` has already moved to the
        // new page while `selectionInPage` still holds the old page's text,
        // and the two are paired in `readAction` — so until this runs, a tap
        // could post page A's selected text as a reading of artifact B. The
        // handle does not close that window on its own: it is claimed inside
        // `updateUIView`, which never runs on a page whose content is still
        // loading or has failed, and a paging TabView keeps the swiped-away
        // page alive with its selection intact.
        selectionInPage = ""
        while !Task.isCancelled {
            let text = await webHandle.selectionText()
            if text != selectionInPage { selectionInPage = text }
            try? await Task.sleep(for: .milliseconds(700))
        }
    }
}

/// The one thing the pager's ✓ control can do for the page on screen.
///
/// Three cases rather than a bool plus an optional payload, for
/// `ArtifactBookmarkAction`'s reason: the branches carry different payloads,
/// none is derivable from another, and deciding once and carrying the decision
/// is what keeps the glyph and the press from disagreeing about which of the
/// two verbs — mark the artifact, or mark this selection — a tap means.
private enum ArtifactReadAction {
    /// Nothing selected, no live reading: mark the whole artifact read.
    case mark(ArtifactRef)
    /// Nothing selected, and a live whole-artifact reading exists: drop it.
    case unmark(ArtifactRef, WebMMItem)
    /// A non-empty selection is on screen in a kind that allows text marks
    /// (allowsTextMarks — never a .page): mark that span instead.
    case markSelection(ArtifactRef, String)

    /// Only the whole-artifact toggle has a pressed state. A selection mark is
    /// a verb, not a switch — the server may or may not already hold that
    /// span, and the app never claims to know which.
    var isMarked: Bool {
        if case .unmark = self { return true }
        return false
    }

    var icon: String {
        switch self {
        case .mark: return "checkmark.circle"
        case .unmark: return "checkmark.circle.fill"
        case .markSelection: return "text.badge.checkmark"
        }
    }

    var title: String {
        switch self {
        case .mark: return "Mark read"
        case .unmark: return "Read"
        case .markSelection: return "Mark selection"
        }
    }

    var accessibility: String {
        switch self {
        case .mark(let ref): return "Mark \(ref.name) read"
        case .unmark(let ref, _): return "\(ref.name) is marked read — drop the mark"
        case .markSelection: return "Mark the selected text read"
        }
    }
}
