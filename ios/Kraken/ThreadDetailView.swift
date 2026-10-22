import SwiftUI
import Observation

// One side thread's full view: the conversation it forked out of zeus'
// session (inherited history dimmed, with a "branched here" divider at the
// fork point), the live tmux pane behind it while it's running, and a
// composer that talks to it. Deliberately shaped like SessionDetailView —
// same view model layout, same two-mode picker, same bottom-anchor scroll
// discipline, and literally the same block renderer (TranscriptBlockView,
// shared by both rather than copied) — since a thread IS just another claude
// in a pane; the differences are that it's addressed by (slug, tid) through
// /api/threads rather than by pane through /api/peers, and that it can be
// killed and revived without losing the conversation. That describes a pane
// thread; an ask thread (`kind: "headless"`) has no pane, so it gets no
// Terminal mode — only the conversation, its answering/note status and the
// composer. The kind VALUE is "headless", not "ask": `role: "ask"` already
// names carl's question thread (versus the mind thread) on this wire, and
// the two are orthogonal.

@Observable
final class ThreadDetailViewModel {
    var blocks: [ReadBlock] = []
    var counted = 0
    // Server-reported liveness/pane, refreshed on every transcript poll —
    // the source of truth once `loaded` flips, since the WebThread the view
    // was pushed with is a frozen board snapshot.
    var live = false
    var pane: String?
    var source = ""
    // The fork's uuid in zeus' session — rendered in the "branched here"
    // divider, matching the web board's "⑂ branched here · <uuid>".
    var forkUUID = ""
    // The read's kind ("pane"/"headless"; nil from a server that predates it),
    // whether an ask thread's question is in flight, and the last run's note.
    var kind: String?
    var answering = false
    var note = ""
    var loaded = false
    var loadError: String?

    var terminalContent = ""
    var terminalError: String?

    var isSending = false
    var sendError: String?

    var isBusy = false
    var actionError: String?
    var toastMessage: String?

    func load(baseURL: String, slug: String, tid: String, n: Int) async {
        do {
            let resp = try await APIClient.shared.readThread(baseURL: baseURL, slug: slug, tid: tid, n: n)
            blocks = resp.blocks ?? []
            counted = resp.counted
            live = resp.live
            pane = resp.pane
            source = resp.source
            forkUUID = resp.forkUUID
            kind = resp.kind
            answering = resp.answering ?? false
            note = resp.note ?? ""
            loaded = true
            loadError = nil
        } catch {
            loadError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    func loadTerminal(baseURL: String, pane: String) async {
        do {
            terminalContent = try await APIClient.shared.capturePane(baseURL: baseURL, pane: pane)
            terminalError = nil
        } catch {
            // Drop the last good frame with the error: tmux recycles pane
            // ids, so a capture that starts failing means this pane is gone
            // (or now belongs to something else) — a frozen screenful of a
            // dead thread's output looks exactly like a live one.
            terminalContent = ""
            terminalError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    @discardableResult
    func send(baseURL: String, slug: String, tid: String, text: String) async -> Bool {
        isSending = true
        sendError = nil
        do {
            _ = try await APIClient.shared.threadAction(
                baseURL: baseURL, slug: slug, op: .send, tid: tid, text: text)
            isSending = false
            showToast("sent")
            return true
        } catch {
            isSending = false
            sendError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            return false
        }
    }

    /// kill / revive / rename — everything that changes the thread itself
    /// rather than its conversation. Errors land in `actionError` (rendered
    /// in the header) instead of the composer's `sendError`.
    @discardableResult
    func act(
        baseURL: String, slug: String, tid: String, op: ThreadOp, title: String? = nil
    ) async -> Bool {
        isBusy = true
        actionError = nil
        do {
            _ = try await APIClient.shared.threadAction(
                baseURL: baseURL, slug: slug, op: op, tid: tid, title: title)
            isBusy = false
            return true
        } catch {
            isBusy = false
            actionError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            return false
        }
    }

    private func showToast(_ message: String) {
        toastMessage = message
        Task {
            try? await Task.sleep(for: .seconds(2))
            toastMessage = nil
        }
    }
}

/// Same split as SessionViewMode: the parsed conversation, or the raw pane
/// behind it. Terminal only exists while the thread is live (a closed thread
/// has no pane to capture), so the picker hides itself in that case.
private enum ThreadViewMode: String, CaseIterable, Hashable {
    case transcript = "Transcript"
    case terminal = "Terminal"
}

struct ThreadDetailView: View {
    let thread: WebThread
    let slug: String
    let baseURL: String
    /// Called after a kill/revive/rename lands, so the board behind this
    /// view refreshes its own chips.
    var onChanged: () -> Void = {}

    @Environment(\.scenePhase) private var scenePhase
    @Environment(DraftStore.self) private var draftStore
    // Present when this view was pushed on a board job sheet's NavigationStack
    // (the only way in today) — set while it's up so the sheet's
    // auto-dismiss-on-state-change stands down and doesn't tear a live
    // conversation out from under the reader. Optional so a future call site
    // that doesn't inject one still renders instead of trapping.
    @Environment(SheetNavState.self) private var sheetNav: SheetNavState?
    @Environment(\.boardNodeId) private var boardNodeId
    @FocusState private var messageFocused: Bool
    @State private var vm = ThreadDetailViewModel()
    @State private var messageText = ""
    @State private var mode: ThreadViewMode = .transcript
    @State private var showingKillConfirm = false
    @State private var showingRename = false
    @State private var renameText = ""
    // A successful rename shows immediately without waiting for the board to
    // re-serve this thread — same optimistic-overlay trick as
    // JobDetailView's docQuestionOverlay.
    @State private var titleOverride: String?
    // Set by the drag gesture the moment the reader takes scroll position
    // into their own hands; every programmatic scroll below rechecks it.
    @State private var userScrolled = false

    // Before the first read lands, the board's snapshot is all we have.
    private var isLive: Bool { vm.loaded ? vm.live : thread.isLive }
    // The read's kind once it carries one, else the board snapshot's.
    private var isHeadless: Bool { (vm.kind ?? thread.kind) == "headless" }
    private var isAnswering: Bool { vm.loaded ? vm.answering : thread.answering == true }
    private var note: String { vm.loaded ? vm.note : (thread.note ?? "") }
    private var displayTitle: String { titleOverride ?? thread.displayTitle }
    private var capturePane: String? {
        let pane = vm.pane ?? thread.pane
        return (pane?.isEmpty == false) ? pane : nil
    }
    // Per-thread composer draft, same shape as SessionDetailView's
    // "send:<pane>" — keyed by (slug, tid) rather than pane because a thread
    // survives its pane: kill it, revive it, and the half-typed message
    // should still be there.
    private var draftKey: String { "thread:\(slug):\(thread.tid)" }
    private var trimmedRename: String {
        renameText.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    // Re-polls whenever the app (fore/back)grounds or the mode toggles. The
    // pane only participates in Terminal mode: it arrives with the first
    // transcript read, and without it in the key a switch into Terminal made
    // before that read landed would never start capturing.
    private struct PollKey: Equatable {
        let phase: ScenePhase
        let mode: ThreadViewMode
        let pane: String?
    }

    // How many 2s Terminal ticks pass between the liveness refreshes folded
    // into that loop — ~6s, close to Transcript's own 5s cadence.
    private static let terminalLivenessEvery = 3

    private static let bottomAnchorID = "bottom"

    var body: some View {
        VStack(spacing: 0) {
            header
            // An ask thread has no pane, so there is no Terminal to pick.
            if isLive && !isHeadless {
                modePicker
            }
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(spacing: 0) {
                        Group {
                            switch mode {
                            case .transcript: transcriptContent
                            case .terminal: terminalContent
                            }
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                        Color.clear.frame(height: 1).id(Self.bottomAnchorID)
                    }
                }
                // Identical bottom-anchor policy to SessionDetailView: an
                // immediate scroll plus one delayed retry while the
                // ScrollView's content-size measurement catches up, and every
                // fire opts out once the reader has scrolled themselves.
                .onChange(of: vm.blocks.count) { oldCount, newCount in
                    guard mode == .transcript, newCount > oldCount, !userScrolled else { return }
                    scrollToBottomRetrying(proxy: proxy)
                }
                .onChange(of: vm.terminalContent) { oldValue, newValue in
                    guard mode == .terminal, oldValue.isEmpty, !newValue.isEmpty, !userScrolled else { return }
                    withAnimation { proxy.scrollTo(Self.bottomAnchorID, anchor: .bottom) }
                }
                .onChange(of: mode) { _, newMode in
                    userScrolled = false
                    if newMode == .transcript, !vm.blocks.isEmpty {
                        scrollToBottomRetrying(proxy: proxy)
                    } else if newMode == .terminal, !vm.terminalContent.isEmpty {
                        withAnimation { proxy.scrollTo(Self.bottomAnchorID, anchor: .bottom) }
                    }
                }
                .simultaneousGesture(
                    DragGesture(minimumDistance: 10).onChanged { _ in userScrolled = true }
                )
                .task(id: PollKey(phase: scenePhase, mode: mode, pane: mode == .terminal ? capturePane : nil)) {
                    guard scenePhase == .active else { return }
                    // Both modes refresh liveness (vm.load is the only writer
                    // of vm.live/vm.pane), because everything chrome-level
                    // hangs off it: the header dot, the composer, the picker,
                    // and the fall-back-to-Transcript on death. A Terminal
                    // loop that only captured would leave a thread killed
                    // elsewhere looking live forever — and a thread branched
                    // seconds ago, whose pane hadn't arrived yet, stuck on
                    // "no pane" with nothing left to change the poll key.
                    switch mode {
                    case .transcript:
                        // 5s in Transcript, 2s in Terminal — the same cadence
                        // SessionDetailView polls a peer at.
                        await vm.load(baseURL: baseURL, slug: slug, tid: thread.tid, n: 30)
                        while !Task.isCancelled {
                            try? await Task.sleep(for: .seconds(5))
                            if Task.isCancelled { break }
                            await vm.load(baseURL: baseURL, slug: slug, tid: thread.tid, n: 30)
                        }
                    case .terminal:
                        // capturePane is re-read every tick rather than bound
                        // once: vm is a reference type, so a pane arriving
                        // mid-loop is picked up here even before the changed
                        // poll key restarts this task.
                        await vm.load(baseURL: baseURL, slug: slug, tid: thread.tid, n: 30)
                        if let pane = capturePane {
                            await vm.loadTerminal(baseURL: baseURL, pane: pane)
                        }
                        var tick = 0
                        while !Task.isCancelled {
                            try? await Task.sleep(for: .seconds(2))
                            if Task.isCancelled { break }
                            tick += 1
                            if let pane = capturePane {
                                await vm.loadTerminal(baseURL: baseURL, pane: pane)
                            }
                            if tick % Self.terminalLivenessEvery == 0 {
                                await vm.load(baseURL: baseURL, slug: slug, tid: thread.tid, n: 30)
                            }
                        }
                    }
                }
            }
            composer
        }
        .background(ThemeBackgroundView())
        // A thread that dies (killed here, or from the pane) takes its pane
        // with it — Terminal has nothing left to show, and its picker is
        // gone, so fall back to the conversation rather than stranding the
        // reader on a frozen capture.
        .onChange(of: isLive) { _, nowLive in
            if !nowLive { mode = .transcript }
        }
        // Two things live on this pair. The sheet flag (see SheetNavState in
        // BoardView) keeps the presenting job sheet from auto-dismissing —
        // and taking this view with it — when the job flips working↔blocked
        // mid-conversation. The draft restores whatever was mid-typed the
        // last time this thread was open, exactly as SessionDetailView does
        // for a pane; every keystroke saves back, a successful send clears it
        // in send() below.
        .onAppear {
            sheetNav?.childPushed = true
            messageText = draftStore.get(draftKey)
        }
        .onDisappear { sheetNav?.childPushed = false }
        .onChange(of: messageText) { draftStore.set(draftKey, messageText) }
        .navigationTitle(displayTitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button {
                    renameText = displayTitle
                    showingRename = true
                } label: {
                    Label("Rename", systemImage: "pencil")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Rename")
                .tint(KTheme.accent)
            }
            ToolbarItem(placement: .topBarTrailing) {
                if isLive {
                    Button(role: .destructive) {
                        showingKillConfirm = true
                    } label: {
                        Label("Kill", systemImage: "xmark")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Kill")
                } else {
                    Button {
                        act(op: .revive)
                    } label: {
                        Label("Revive", systemImage: "arrow.counterclockwise")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Revive")
                    .tint(KTheme.revealed)
                }
            }
        }
        .disabled(vm.isBusy)
        // A thread's window closing is not destructive to the conversation —
        // the dialog says so, since "kill" otherwise reads like the job Kill
        // that tears a whole cell down.
        .confirmationDialog(
            isHeadless
                ? "Close this thread? Any answer in flight is cancelled; the conversation is kept and can be revived later."
                : "Close this thread's window? The conversation is kept and can be revived later.",
            isPresented: $showingKillConfirm, titleVisibility: .visible
        ) {
            Button("Close Thread", role: .destructive) { act(op: .kill) }
            Button("Cancel", role: .cancel) {}
        }
        .alert("Rename thread", isPresented: $showingRename) {
            TextField("title", text: $renameText)
            // Disabled rather than silently no-op: an alert dismisses on any
            // button, so a whitespace-only rename that just returned read as
            // "the rename didn't take" with nothing to say why.
            Button("Rename") {
                act(op: .rename, title: trimmedRename)
            }
            .disabled(trimmedRename.isEmpty)
            Button("Cancel", role: .cancel) {}
        }
        .overlay(alignment: .top) {
            if let toast = vm.toastMessage {
                Text(toast)
                    .font(.data(11))
                    .foregroundStyle(KTheme.accentInk)
                    .padding(.horizontal, 14).padding(.vertical, 6)
                    .background(KTheme.accent, in: Capsule())
                    .shadow(color: KTheme.accent.opacity(0.35), radius: 10, y: 3)
                    .padding(.top, 8)
            }
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                // Glowing dot in a tinted capsule — the same treatment
                // StateCapsule gives a job's state, since this row answers the
                // same question about a thread.
                let stateColor = isLive ? KTheme.solved : KTheme.stopped
                HStack(spacing: 6) {
                    Circle()
                        .fill(stateColor)
                        .frame(width: 7, height: 7)
                        .shadow(color: stateColor.opacity(0.7), radius: 4)
                    Text(isLive ? "live" : "closed")
                        .font(.data(11))
                        .foregroundStyle(stateColor)
                }
                .padding(.horizontal, 9)
                .padding(.vertical, 4)
                .background(stateColor.opacity(0.13), in: Capsule())
                Text(thread.tid)
                    .font(.data(11))
                    .foregroundStyle(KTheme.accent)
                if let pane = capturePane, isLive {
                    Text(pane).font(.data(11)).foregroundStyle(KTheme.text2)
                }
                Spacer()
            }
            ThreadAskStatusRow(
                badge: threadOriginBadge(thread, boardNodeId: boardNodeId),
                answering: isAnswering, note: note
            )
            if let actionError = vm.actionError {
                Text(actionError).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 7)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.regularMaterial)
    }

    private var modePicker: some View {
        Picker("View", selection: $mode) {
            ForEach(ThreadViewMode.allCases, id: \.self) { m in
                Text(m.rawValue).tag(m)
            }
        }
        .pickerStyle(.segmented)
        .padding(.horizontal)
        .padding(.vertical, 6)
        .background(.regularMaterial)
    }

    // Where the "⑂ branched here" divider goes: the first block the thread
    // itself produced, i.e. the first one NOT marked inherited. nil when the
    // server marked nothing as inherited (nothing to dim, no fork point to
    // point at); vm.blocks.count when every block is inherited, i.e. the
    // thread has been branched but not yet spoken to, so the divider lands
    // at the very bottom.
    private var forkIndex: Int? {
        guard vm.blocks.contains(where: { $0.inherited == true }) else { return nil }
        return vm.blocks.firstIndex(where: { $0.inherited != true }) ?? vm.blocks.count
    }

    // "⑂ branched here · <fork uuid>" — the uuid names the exact turn in
    // zeus' session this thread forked from, same label the web board draws.
    private var forkLabel: String {
        vm.forkUUID.isEmpty ? "⑂ branched here" : "⑂ branched here · \(vm.forkUUID)"
    }

    private var forkDivider: some View {
        HStack(spacing: 6) {
            Rectangle().fill(KTheme.hairline2).frame(height: 1)
            Text(forkLabel)
                .font(.data(10, weight: .semibold))
                .foregroundStyle(KTheme.accent)
                .lineLimit(1)
                .truncationMode(.middle)
            Rectangle().fill(KTheme.hairline2).frame(height: 1)
        }
        .padding(.vertical, 4)
    }

    private var transcriptContent: some View {
        // A plain VStack, not LazyVStack, for the same reason as
        // SessionDetailView's: capped rows, and scroll-to-bottom has to land
        // on realized content.
        VStack(alignment: .leading, spacing: 8) {
            if let err = vm.loadError {
                Text(err).font(.voice(12)).foregroundStyle(KTheme.counter)
            }
            if vm.loaded, vm.blocks.isEmpty, vm.loadError == nil {
                // `source` is the server's own word for which of the two
                // empties this is: "none" (no transcript yet — branched and
                // never spoken to) vs a transcript that simply had nothing
                // renderable in the last n turns.
                Text(vm.source == "none"
                     ? "nothing said in this thread yet"
                     : "nothing to show in this thread's last turns")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
            }
            ForEach(Array(vm.blocks.enumerated()), id: \.offset) { index, block in
                if index == forkIndex {
                    forkDivider
                }
                // 0.55 is the web board's own #thread-convo .inherited
                // opacity — the same UI, so the same dimming.
                TranscriptBlockView(block: block)
                    .opacity(block.inherited == true ? 0.55 : 1)
            }
            if forkIndex == vm.blocks.count {
                forkDivider
            }
        }
        .padding()
    }

    @ViewBuilder
    private var terminalContent: some View {
        if let pane = capturePane {
            VStack(alignment: .leading, spacing: 8) {
                if let err = vm.terminalError {
                    Text(err).font(.voice(12)).foregroundStyle(KTheme.counter)
                }
                // Raw capture-pane text scrolls horizontally rather than
                // reflowing — wrapping would mangle the terminal's alignment.
                ScrollView(.horizontal, showsIndicators: true) {
                    Text(vm.terminalContent.isEmpty ? " " : vm.terminalContent)
                        .font(.data(12))
                        .foregroundStyle(KTheme.text)
                        .fixedSize(horizontal: true, vertical: true)
                        .textSelection(.enabled)
                }
                .accessibilityLabel("pane \(pane)")
            }
            .padding()
        } else {
            // A live thread with no pane isn't closed — it's one whose first
            // read hasn't landed yet (freshly branched, board snapshot still
            // pane-less). The poll loop keeps refreshing liveness in this
            // mode, so this resolves itself the moment the pane arrives.
            Text(isLive
                 ? "no pane yet — waiting for the thread's window"
                 : "no pane — the thread's window is closed")
                .font(.data(11))
                .foregroundStyle(KTheme.text2)
                .padding()
        }
    }

    private func scrollToBottomRetrying(proxy: ScrollViewProxy) {
        for delayMs in [0, 350] {
            Task {
                if delayMs > 0 {
                    try? await Task.sleep(for: .milliseconds(delayMs))
                }
                guard !userScrolled else { return }
                withAnimation { proxy.scrollTo(Self.bottomAnchorID, anchor: .bottom) }
            }
        }
    }

    @ViewBuilder
    private var composer: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let sendError = vm.sendError {
                Text(sendError).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            HStack {
                if isLive {
                    TextField("message", text: $messageText, axis: .vertical)
                        .focused($messageFocused)
                        .font(.voice(13))
                        .kField(focused: messageFocused)
                    Button {
                        send()
                    } label: {
                        Label("Send", systemImage: "paperplane.fill")
                    }
                    .buttonStyle(.glassProminent)
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Send")
                    .disabled(vm.isSending || messageText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                } else {
                    Text("thread is closed — revive it to send")
                        .font(.data(11))
                        .foregroundStyle(KTheme.text2)
                }
            }
            // Kept visible, disabled while an ask thread is answering: the
            // server refuses a second send then (409).
            .disabled(isAnswering)
        }
        .padding()
        .background(.regularMaterial)
    }

    private func send() {
        let text = messageText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        Task {
            // A failed send (a dead cell, a thread closed from elsewhere)
            // deliberately leaves the typed text — and its draft — in place.
            if await vm.send(baseURL: baseURL, slug: slug, tid: thread.tid, text: text) {
                messageText = ""
                draftStore.clear(draftKey)
                await vm.load(baseURL: baseURL, slug: slug, tid: thread.tid, n: 30)
            }
        }
    }

    // kill / revive / rename all land here: run the op, re-read so the header
    // and composer reflect the new liveness, then let the board refresh.
    private func act(op: ThreadOp, title: String? = nil) {
        Task {
            if await vm.act(baseURL: baseURL, slug: slug, tid: thread.tid, op: op, title: title) {
                if op == .rename, let title {
                    titleOverride = title
                }
                await vm.load(baseURL: baseURL, slug: slug, tid: thread.tid, n: 30)
                onChanged()
            }
        }
    }
}

/// The ask-thread line under a thread view's header: "from <node>" when the
/// thread came from another node, an "answering…" pill while a question is in
/// flight, and the last run's note. Draws nothing when all three are absent,
/// which is always the case for a pane thread. Shared by ThreadDetailView and
/// RemoteThreadView.
struct ThreadAskStatusRow: View {
    let badge: String?
    let answering: Bool
    let note: String

    var body: some View {
        if badge != nil || answering || !note.isEmpty {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 8) {
                    if let badge {
                        Text("from \(badge)")
                            .font(.data(11))
                            .foregroundStyle(KTheme.skillInfo)
                            .lineLimit(1)
                    }
                    if answering {
                        HStack(spacing: 4) {
                            ProgressView().controlSize(.mini)
                            Text("answering…").font(.data(11))
                        }
                        .foregroundStyle(KTheme.polling)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                        .background(KTheme.pollingSoft, in: Capsule())
                    }
                }
                if !note.isEmpty {
                    Text(note)
                        .font(.voice(11))
                        .foregroundStyle(KTheme.revealed)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }
}
