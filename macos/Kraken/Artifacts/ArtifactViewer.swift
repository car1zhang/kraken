import AppKit
import SwiftUI
import WebKit

// ArtifactViewer.swift — the artifact overlay's contents.
//
// Four render modes picked from the URL's extension alone (ArtifactKind), a
// pager that walks the job's artifact set, and ONE composer box with two modes
// (leave feedback / branch a side thread and ask). The rules all live in
// ArtifactModel.swift; this file is the panel body that applies them.
//
// The WKWebView is the deliberate non-native surface (53-concessions.md §E):
// artifacts are agent-authored HTML pages and always will be, SMIL diagram
// cards included. Everything AROUND them is SwiftUI. `.text` artifacts also go
// through the web view — via the locally-built MarkdownHTMLMac shell rather
// than the server URL — for the reason the ledger gives: the artifact md
// surface is the one place the design wants real HTML tables, code blocks and
// prose you can sweep across paragraphs, and `HudMarkdown` (which the in-app
// surfaces use) cannot do the third of those at all.
//
// What this view does NOT own: the panel chrome, the scrim, the z-order and the
// Escape stack. `OverlayHost` wraps it in `HudPanel` and drives all four, which
// is why nothing below draws a background or a corner cut.

struct ArtifactViewer: View {
    /// The `/api/artifact/...` URL the viewer is open on. A change from outside
    /// (a chip clicked while the viewer is already up) re-points the panel
    /// exactly as a pager step does.
    let url: String
    /// The jobKey the chip carried. Its job's `artifacts` list IS the pager's
    /// sequence, re-read on every board poll so a live cell's new artifact
    /// comes within reach without a reopen.
    ///
    /// Looked up by jobKey and NEVER by bare slug — an archived job and an
    /// active one that reused its freed slug would otherwise resolve to each
    /// other's artifacts.
    let jobKey: String?

    @EnvironmentObject private var board: BoardStore
    @EnvironmentObject private var router: OverlayRouter
    @Environment(\.hudTheme) private var theme
    @AppStorage(SettingsKey.serverURL) private var serverURLRaw = ""

    /// Where the pager currently is. Local rather than pushed back through the
    /// router: a step must not change which JOB the sequence came from, and
    /// keeping it here is the cheapest way to guarantee that.
    @State private var currentURL = ""

    @State private var raw = false
    /// nil = not fetched yet. A web/image artifact is never fetched up front
    /// (the web view does its own request), so flipping `raw` on one triggers
    /// the fetch late.
    @State private var text: String?
    @State private var loadError: String?

    @State private var mode: ArtifactComposerMode?
    @State private var composerText = ""
    @State private var note = ""
    @State private var sending = false
    /// Which composer last succeeded, for the head button's transient ✓.
    @State private var sentMode: ArtifactComposerMode?

    @StateObject private var hold = ArtifactSeqHold()
    @FocusState private var composerFocused: Bool

    private var stash: ArtifactDraftStash { .shared }
    private var baseURL: String { ServerConfig.baseURL(stored: serverURLRaw).absoluteString }

    // MARK: - sequence derivation

    /// The job the open artifact belongs to, by jobKey — never by bare slug.
    private var job: WebJob? {
        guard let jobKey, let jobs = board.board?.jobs else { return nil }
        return jobs.first { JobKey.key(slug: $0.slug, archived: $0.archived ?? false) == jobKey }
    }

    /// The sequence and the position in it, re-derived on EVERY render — which
    /// is the whole mechanism behind the 3s re-sync.
    ///
    /// Three properties are load-bearing, all of them the web hook's
    /// (`useArtifactSeq.ts`):
    ///
    ///  1. the index is re-found BY URL, never carried — the list can gain
    ///     entries ahead of the open one, which would slide a carried position
    ///     onto the wrong artifact;
    ///  2. a sequence that no longer contains the open artifact is KEPT, so an
    ///     artifact deleted or renamed under the viewer leaves its NEIGHBOURS
    ///     reachable instead of stranding the reader mid-set;
    ///  3. an unresolvable job opens a SINGLETON, pager hidden.
    ///
    /// `hold.held` is written here, during body evaluation. That is deliberate
    /// and is the same latest-value mirror the web hook documents: it is never
    /// read by the render that writes it, only by a LATER one deciding whether
    /// the sequence it was handed still contains the open artifact — and it is
    /// idempotent, so a discarded evaluation writes what the kept one would. It
    /// is a plain stored property, not `@Published`, so writing it invalidates
    /// nothing.
    private func resolved() -> (seq: [ArtifactSeqEntry], idx: Int) {
        let open = currentURL.isEmpty ? url : currentURL
        if let fresh = ArtifactSequence.entries(for: job),
           let at = ArtifactSequence.index(of: open, in: fresh.map(\.url)) {
            hold.held = fresh
            return (fresh, at)
        }
        if let prev = hold.held,
           let was = ArtifactSequence.index(of: open, in: prev.map(\.url)) {
            return (prev, was)
        }
        let single = [ArtifactSeqEntry(url: open, name: Self.fallbackName(for: open))]
        hold.held = single
        return (single, 0)
    }

    /// The title for an artifact with no resolvable job.
    ///
    /// `ArtifactTarget` carries the url and the jobKey but no display name, so
    /// the singleton case has to build its own — and the URL already holds
    /// exactly the two halves the chips label with (`<slug>/<artifact>`).
    private static func fallbackName(for url: String) -> String {
        guard let parts = ArtifactURLParts.parse(url) else { return url }
        return parts.slug + "/" + parts.name
    }

    // MARK: - render decisions

    private var openURL: String { currentURL.isEmpty ? url : currentURL }
    private var kind: ArtifactKind { ArtifactKind.kind(forURL: openURL) }
    private var isPDF: Bool { openURL.lowercased().hasSuffix(".pdf") }
    /// A fetch is needed for every text artifact, and for `raw` on anything —
    /// `raw` on an html artifact shows its source.
    private var needsText: Bool { raw || kind == .text }

    var body: some View {
        let (seq, idx) = resolved()
        return VStack(alignment: .leading, spacing: 0) {
            head(seq: seq, idx: idx)
            if mode != nil { composer }
            content
        }
        .onAppear {
            if currentURL.isEmpty { adopt(url: url) }
        }
        .onChange(of: url) { _, next in
            guard next != currentURL else { return }
            switchTo(url: next)
        }
        .onDisappear {
            // The other way of leaving an artifact: the viewer closing. Escape
            // and a backdrop click both land here, so there is no
            // "leaving banks the note, except by Escape" split to get wrong.
            bankCurrentDraft()
        }
        // The fetch. Keyed on both the artifact and whether text is wanted, so
        // flipping `raw` on a web artifact fetches it the first time.
        .task(id: openURL + (needsText ? "|text" : "|none")) {
            await loadTextIfNeeded()
        }
        // A lingering "saved ✓" must never carry over to another artifact.
        .task(id: sentMode) {
            guard sentMode != nil else { return }
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            guard !Task.isCancelled else { return }
            sentMode = nil
        }
        .modifier(ArrowKeyMonitor { delta in step(delta, in: seq, from: idx) })
    }

    // MARK: - head

    @ViewBuilder
    private func head(seq: [ArtifactSeqEntry], idx: Int) -> some View {
        HudPanelHead(seq.indices.contains(idx) ? seq[idx].name : Self.fallbackName(for: openURL)) {
            // Between the title and the spacer, so ↗ tab / raw / ask / feedback
            // keep their right-hand positions.
            pager(seq: seq, idx: idx)

            Spacer(minLength: 0)

            HudButton("↗ tab", style: .ghost) {
                if let u = absoluteURL(for: openURL) { NSWorkspace.shared.open(u) }
            }
            .help("open this artifact in the default browser")

            // A PDF has no useful raw text and an image has none at all.
            if kind != .image && !isPDF {
                HudButton(raw ? "raw ✓" : "raw", style: .ghost) { raw.toggle() }
                    .help("show the file's own bytes")
            }

            // Branching forks the cell's zeus session, so it needs a LIVE cell
            // — the same `job.live` gate the steer inspector puts on the ⑂ it
            // renders. Without a job resolved (a singleton open) there is
            // nothing to fork either.
            if job?.live == true {
                HudButton(sentMode == .ask ? "asked ✓" : "⑂ ask", style: .ghost) {
                    toggleComposer(.ask)
                }
                .disabled(sending)
                .help("ask about this artifact in a side thread — forks zeus, doesn't interrupt it")
            }

            HudButton(sentMode == .feedback ? "saved ✓" : "feedback", style: .ghost) {
                toggleComposer(.feedback)
            }
            .disabled(sending)
            .help("leave feedback on this artifact for the job to read")
        }
    }

    /// `‹ n / N ›`. Hidden outright below two entries — "1 / 1" between two
    /// dead chevrons is noise, and what is left without it is exactly the
    /// behaviour the viewer had before the pager existed. Each end button is
    /// disabled at its end, because the step CLAMPS rather than wrapping.
    @ViewBuilder
    private func pager(seq: [ArtifactSeqEntry], idx: Int) -> some View {
        if seq.count >= 2 {
            HStack(spacing: 4) {
                HudButton("‹", style: .ghost) { step(-1, in: seq, from: idx) }
                    .disabled(idx <= 0)
                    .help("previous artifact (←)")
                Text("\(idx + 1) / \(seq.count)")
                    .hudMono(size: 10)
                    .monospacedDigit()
                    .foregroundStyle(theme.mutedColor)
                    .accessibilityLabel("artifact \(idx + 1) of \(seq.count)")
                HudButton("›", style: .ghost) { step(1, in: seq, from: idx) }
                    .disabled(idx >= seq.count - 1)
                    .help("next artifact (→)")
            }
        }
    }

    // MARK: - composer

    @ViewBuilder
    private var composer: some View {
        VStack(alignment: .leading, spacing: 6) {
            if mode == .ask {
                // The ask box is one gesture away from the feedback box and
                // does something quite different with what you type — this line
                // is what keeps them apart at a glance.
                Text("⑂ forks zeus' session into a side thread and asks it there — zeus itself is not interrupted. Threads are for asking, not for doing.")
                    .hudMono(size: 10)
                    .foregroundStyle(theme.mutedColor)
                    .fixedSize(horizontal: false, vertical: true)
            }
            TextEditor(text: $composerText)
                .font(.hudMono(11))
                .hudEditor(minHeight: 54)
                .frame(maxHeight: 120)
                .focused($composerFocused)

            HStack(spacing: 8) {
                Text(note)
                    .hudMono(size: 10)
                    .foregroundStyle(theme.counterColor)
                Spacer(minLength: 0)
                // All three composer-closing controls are locked while a send
                // is in flight: the request is already out and cannot be
                // recalled, so a live cancel would only let the reader express
                // an intention the code cannot honour.
                HudButton("cancel", style: .ghost) {
                    // The one gesture that means "throw this away": it drops
                    // the stash too, or the note reappears the next time you
                    // page onto this artifact.
                    stash.clear(url: openURL)
                    mode = nil
                    composerText = ""
                    note = ""
                }
                .disabled(sending)
                HudButton(mode == .ask ? "⑂ branch" : "send", style: .big) {
                    Task { await sendComposer() }
                }
                .disabled(sending)
                // ⌘⏎. A keyboard SHORTCUT rather than a keyDown handler is the
                // IME guard by construction: a CJK candidate's commit Enter
                // carries no ⌘, so it can never reach this.
                .keyboardShortcut(.return, modifiers: .command)
                .help("⌘⏎")
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 9)
        .background(theme.raisedColor)
        .overlay(alignment: .leading) {
            // .feedback-form.ask's accent rail (style.css:1866).
            Rectangle()
                .fill(mode == .ask ? theme.pollingColor : Color.clear)
                .frame(width: 2)
        }
        .overlay(alignment: .bottom) {
            Rectangle().fill(theme.borderColor).frame(height: 1)
        }
    }

    // MARK: - body

    @ViewBuilder
    private var content: some View {
        Group {
            if needsText {
                if let loadError {
                    message(loadError)
                } else if let text {
                    ArtifactWebView(
                        content: .html(
                            raw
                                ? plainTextHTMLMac(text, theme: theme)
                                : markdownHTMLMac(text, theme: theme)
                        ),
                        onArrow: stepFromWebContent
                    )
                } else {
                    message("loading…")
                }
            } else if kind == .image {
                imageBody
            } else if let u = absoluteURL(for: openURL) {
                ArtifactWebView(content: .url(u), onArrow: stepFromWebContent)
            } else {
                message("invalid artifact URL")
            }
        }
        .frame(maxWidth: .infinity, minHeight: 320, alignment: .topLeading)
    }

    /// Images render NATIVELY — no web view. They are the one artifact kind
    /// with nothing to lay out, so handing them to WebKit would buy a second
    /// process to draw a bitmap.
    @ViewBuilder
    private var imageBody: some View {
        if let u = absoluteURL(for: openURL) {
            ScrollView([.horizontal, .vertical]) {
                AsyncImage(url: u) { phase in
                    switch phase {
                    case .success(let image):
                        image.resizable().aspectRatio(contentMode: .fit)
                    case .failure:
                        message("failed to load this image")
                    default:
                        message("loading…")
                    }
                }
                .frame(maxWidth: .infinity)
                .padding(12)
            }
        } else {
            message("invalid artifact URL")
        }
    }

    @ViewBuilder
    private func message(_ s: String) -> some View {
        Text(s)
            .hudMono(size: 10)
            .foregroundStyle(theme.mutedColor)
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .topLeading)
    }

    // MARK: - navigation

    /// Adopts an artifact WITHOUT banking anything — the first open.
    private func adopt(url next: String) {
        currentURL = next
        let draft = stash.restore(url: next)
        raw = false
        text = nil
        loadError = nil
        note = ""
        sending = false
        sentMode = nil
        mode = draft.flatMap { ArtifactComposerMode(rawValue: $0.mode) }
        composerText = draft?.text ?? ""
    }

    /// Leaves the artifact on screen for another one: bank what is typed
    /// against the artifact it was typed ABOUT, then reset onto the new one and
    /// restore ITS draft (mode included).
    private func switchTo(url next: String) {
        bankCurrentDraft()
        adopt(url: next)
    }

    /// The ONE navigation command, shared by `‹ ›` and both arrow paths so they
    /// cannot drift. ±1, clamped at both ends: a step past either end is a
    /// no-op, never a wrap.
    private func step(_ delta: Int, in seq: [ArtifactSeqEntry], from idx: Int) {
        let next = ArtifactSequence.step(from: idx, by: delta, count: seq.count)
        guard next != idx, seq.indices.contains(next) else { return }
        switchTo(url: seq[next].url)
    }

    /// The arrow path from INSIDE the web view, which has no `seq`/`idx` in
    /// hand — so it re-derives them, which is free and cannot be stale.
    private func stepFromWebContent(_ delta: Int) {
        let (seq, idx) = resolved()
        step(delta, in: seq, from: idx)
    }

    private func bankCurrentDraft() {
        let open = openURL
        guard !open.isEmpty else { return }
        if let mode {
            stash.bank(url: open, mode: mode.rawValue, text: composerText)
        } else {
            stash.clear(url: open)
        }
    }

    private func absoluteURL(for path: String) -> URL? {
        URL(string: baseURL + path)
    }

    /// Clicking the mode that is already open closes it. Clicking the OTHER one
    /// switches the box over and KEEPS what is typed: those are still your
    /// words, and "actually this is a question, not a complaint" is a real
    /// thing to notice halfway through a paragraph.
    private func toggleComposer(_ next: ArtifactComposerMode) {
        if mode == next {
            mode = nil
            return
        }
        mode = next
        note = ""
        composerFocused = true
    }

    // MARK: - loading

    private func loadTextIfNeeded() async {
        guard needsText, text == nil else { return }
        let openedOn = openURL
        do {
            let body = try await APIClient.shared.fetchArtifactText(baseURL: baseURL, path: openedOn)
            guard openURL == openedOn else { return }
            text = body
            loadError = nil
        } catch is CancellationError {
            return
        } catch {
            guard openURL == openedOn else { return }
            loadError = "failed to load: "
                + ((error as? KrakenError)?.errorDescription ?? error.localizedDescription)
        }
    }

    // MARK: - sending

    private func sendComposer() async {
        guard !sending, let mode else { return }
        switch mode {
        case .feedback: await sendFeedback()
        case .ask: await sendAsk()
        }
    }

    private func sendFeedback() async {
        guard let parts = ArtifactURLParts.parse(openURL) else {
            note = "can't identify this artifact"
            return
        }
        let body = composerText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else {
            note = "feedback is empty"
            return
        }
        let sentOn = openURL
        sending = true
        var failure: String?
        do {
            try await APIClient.shared.createFeedback(
                baseURL: baseURL, slug: parts.slug, home: parts.home,
                name: parts.name, feedback: body
            )
        } catch {
            failure = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        // Feedback rides the board payload, so the card's count is only right
        // after a refetch — and the refetch runs on BOTH outcomes, exactly as
        // the web board's `mutate` does, since a failed write may still have
        // moved something.
        await board.refetch()
        if let failure {
            router.toast("✕ " + failure)
        } else {
            // It is sent; it is no longer a draft. Dropped even if the reader
            // has paged on, because the note IS gone.
            stash.clear(url: sentOn)
        }
        // "Is the composer still showing the note I sent?" — an IDENTITY
        // question, asked AFTER the await because a step across it must be
        // seen. The post is right either way, but the panel belongs to whatever
        // is on screen: clearing here after a step would wipe THAT artifact's
        // restored draft, and "saved ✓" would appear over a note nobody sent.
        guard openURL == sentOn else { return }
        sending = false
        if failure != nil {
            note = "failed — see status bar"
            return
        }
        mode = nil
        composerText = ""
        note = ""
        sentMode = .feedback
    }

    private func sendAsk() async {
        guard let parts = ArtifactURLParts.parse(openURL) else {
            note = "can't identify this artifact"
            return
        }
        // Reachable without the head button: an ask draft restored onto an
        // artifact whose job has since left the board comes back with the
        // composer open while `⑂ ask` itself is hidden.
        guard let job else {
            note = "this job is no longer on the board"
            return
        }
        let question = composerText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !question.isEmpty else {
            note = "question is empty"
            return
        }
        let sentOn = openURL
        sending = true
        var failure: String?
        var newTID: String?
        do {
            // Nothing new on the wire: the SAME /api/threads branch the thread
            // overlay's own dialog posts, with the title and seed filled in
            // from the artifact on screen instead of typed by hand.
            let res = try await APIClient.shared.threadAction(
                baseURL: baseURL, slug: job.slug, op: .branch,
                title: AskThread.title(artifactName: parts.name, question: question),
                text: AskThread.seed(artifactName: parts.name, question: question)
            )
            newTID = res.tid
        } catch {
            failure = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        await board.refetch()
        if let failure { router.toast("✕ " + failure) } else { stash.clear(url: sentOn) }
        guard openURL == sentOn else { return }
        sending = false
        if failure != nil {
            note = "failed — see status bar"
            return
        }
        mode = nil
        composerText = ""
        note = ""
        sentMode = .ask
        // CLOSE the artifact overlay as the thread opens — never stack them.
        // The artifact layer outranks the thread layer in both z-order and the
        // Escape chain, so a thread opened underneath would be invisible AND
        // the one Escape you reach for would close the wrong thing.
        router.close(.artifact)
        if let newTID {
            // Straight through: the seam now carries the tid in the one shape
            // the wire has (`"t7"`), so nothing on this path parses or rebuilds
            // it.
            router.openThread(slug: job.slug, tid: newTID)
        } else {
            // Defensive — the branch op always returns a tid. Without one the
            // thread still exists and the board lists it, so say so out of
            // band: the note region lives inside the composer this very success
            // just closed.
            router.toast("branched — the thread is listed on the job")
        }
    }
}

/// The sequence the viewer was last handed, surviving across renders. A plain
/// stored property on a reference type, deliberately NOT `@Published` — see
/// `ArtifactViewer.resolved()`.
final class ArtifactSeqHold: ObservableObject {
    var held: [ArtifactSeqEntry]?
}

// MARK: - the web view

/// What the web view is showing: a live page loaded by URL (html/svg/pdf — the
/// server sends a Content-Type WebKit renders natively), or a locally-built
/// HTML string (the markdown and raw paths).
private enum ArtifactContent: Equatable {
    case url(URL)
    case html(String)
}

/// The single WKWebView every non-image artifact renders through.
///
/// **Arrow keys — the mechanism, stated once.** The web board had to attach its
/// arrow handler to the artifact IFRAME's own document, because a keydown
/// inside a frame never bubbles to the parent. AppKit has the mirror-image
/// problem: an `NSEvent` local monitor (`ArrowKeyMonitor` below) DOES see every
/// key the app receives, web content included — but it cannot see whether the
/// caret is inside a text field INSIDE that content, because the first
/// responder is only ever the web view itself. So the work splits:
///
///   - the monitor handles ←/→ everywhere EXCEPT while the web view has focus;
///   - an injected user script handles them inside it, applying the same
///     text-entry skip the web board's `isTextEntry` applies, and posting the
///     decision back over a script message handler.
///
/// PDFs are the accepted exception, exactly as on the web: WebKit renders them
/// with its own viewer, into which no user script is injected — so on a `.pdf`
/// the arrows stay with the viewer and `‹ ›` are the pager.
private struct ArtifactWebView: NSViewRepresentable {
    let content: ArtifactContent
    /// -1 / +1, from inside the web content.
    let onArrow: (Int) -> Void

    private static let messageName = "krakenArtifactKeys"

    /// Attached at document end to EVERY frame, so a diagram card that nests
    /// one still routes its arrows out. Guarded against double-install because
    /// a page that navigates within itself re-runs it.
    private static let keyScript = """
    (function () {
      if (window.__krakenArtifactKeys) { return; }
      window.__krakenArtifactKeys = true;
      document.addEventListener('keydown', function (e) {
        if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') { return; }
        if (e.metaKey || e.ctrlKey || e.altKey) { return; }
        var t = e.target;
        if (t) {
          if (t.isContentEditable === true) { return; }
          var tag = String(t.tagName || '').toUpperCase();
          if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') { return; }
        }
        e.preventDefault();
        try {
          window.webkit.messageHandlers.krakenArtifactKeys.postMessage(e.key);
        } catch (err) { /* handler torn down mid-teardown */ }
      }, true);
    })();
    """

    final class Coordinator: NSObject, WKScriptMessageHandler {
        var lastLoaded: ArtifactContent?
        var onArrow: (Int) -> Void

        init(onArrow: @escaping (Int) -> Void) {
            self.onArrow = onArrow
        }

        func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
            guard let key = message.body as? String else { return }
            if key == "ArrowRight" {
                onArrow(1)
            } else if key == "ArrowLeft" {
                onArrow(-1)
            }
        }
    }

    func makeCoordinator() -> Coordinator { Coordinator(onArrow: onArrow) }

    func makeNSView(context: Context) -> WKWebView {
        let controller = WKUserContentController()
        controller.add(context.coordinator, name: Self.messageName)
        controller.addUserScript(
            WKUserScript(source: Self.keyScript, injectionTime: .atDocumentEnd, forMainFrameOnly: false)
        )
        let config = WKWebViewConfiguration()
        config.userContentController = controller

        let webView = WKWebView(frame: .zero, configuration: config)
        // 53-concessions.md's named mitigation for the devtools concession: the
        // artifact surface is the one place the design still wants inspect.
        webView.isInspectable = true
        // Only the container/loading ground is ours; a loaded html/svg/pdf
        // artifact's own content is left exactly as the server renders it, and
        // the markdown path builds its own themed shell. `underPageBackgroundColor`
        // rather than the `drawsBackground` KVC every WKWebView snippet reaches
        // for: that key is private, and a KVC set against a key WebKit renames
        // raises an ObjC exception rather than failing quietly.
        webView.underPageBackgroundColor = .clear
        return webView
    }

    func updateNSView(_ webView: WKWebView, context: Context) {
        context.coordinator.onArrow = onArrow
        // SwiftUI calls this on unrelated state changes too; only (re)load when
        // the content actually changed, or every ambient re-render would
        // restart the page and throw away its scroll position.
        guard context.coordinator.lastLoaded != content else { return }
        context.coordinator.lastLoaded = content
        switch content {
        case .url(let u):
            webView.load(URLRequest(url: u))
        case .html(let html):
            webView.loadHTMLString(html, baseURL: nil)
        }
    }

    /// The user-content controller holds the coordinator STRONGLY, and the
    /// coordinator is what the web view's configuration reaches — so without
    /// this the pair outlives the view and every artifact opened this session
    /// keeps a live handler.
    static func dismantleNSView(_ nsView: WKWebView, coordinator: Coordinator) {
        nsView.configuration.userContentController.removeScriptMessageHandler(forName: messageName)
        nsView.configuration.userContentController.removeAllUserScripts()
    }
}

// MARK: - the window-level arrow keys

/// ←/→ on the WINDOW, for as long as the viewer is on screen.
///
/// Skipped when a text control has focus (the composer sits pixels below the
/// artifact you are reading about, and its caret moves are its own), when the
/// key is modified (those are the system's — word jump, history), and when
/// focus is inside the artifact web view, where the injected script decides
/// instead. Returning nil swallows the event so it cannot also scroll
/// something underneath.
private struct ArrowKeyMonitor: ViewModifier {
    let onArrow: (Int) -> Void

    @State private var monitor: Any?

    private static let leftKeyCode: UInt16 = 123
    private static let rightKeyCode: UInt16 = 124

    func body(content: Content) -> some View {
        content
            .onAppear {
                guard monitor == nil else { return }
                monitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { event in
                    handle(event)
                }
            }
            .onDisappear {
                if let monitor { NSEvent.removeMonitor(monitor) }
                monitor = nil
            }
    }

    private func handle(_ event: NSEvent) -> NSEvent? {
        guard event.keyCode == Self.leftKeyCode || event.keyCode == Self.rightKeyCode else {
            return event
        }
        let mods = event.modifierFlags.intersection(.deviceIndependentFlagsMask)
        guard mods.isDisjoint(with: [.command, .control, .option]) else { return event }
        let responder = event.window?.firstResponder
        // NSTextView is an NSText, and a SwiftUI TextField's field editor is
        // one too, so this single test covers every text entry the panel owns.
        if responder is NSText { return event }
        if let view = responder as? NSView, Self.isInsideWebView(view) { return event }
        onArrow(event.keyCode == Self.rightKeyCode ? 1 : -1)
        return nil
    }

    /// Focus in web content resolves to a private content view NESTED in the
    /// WKWebView, never to the web view itself, so the ancestry is what has to
    /// be walked.
    private static func isInsideWebView(_ view: NSView) -> Bool {
        var v: NSView? = view
        while let current = v {
            if current is WKWebView { return true }
            v = current.superview
        }
        return false
    }
}
