import SwiftUI
import Observation

@Observable
final class SessionDetailViewModel {
    var blocks: [ReadBlock] = []
    var counted = 0
    var loadError: String?

    var terminalContent = ""
    var terminalError: String?

    var sendError: String?
    var isSending = false
    // "queued" (agent message queued behind a busy turn) or "ran" (shell
    // command executed) — same toast chip, different word.
    var toastMessage: String?

    var isKilling = false
    var killError: String?

    func loadTranscript(baseURL: String, pane: String, n: Int) async {
        do {
            let resp = try await APIClient.shared.readPeer(baseURL: baseURL, pane: pane, n: n)
            blocks = resp.blocks ?? []
            counted = resp.counted
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
            terminalError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    @discardableResult
    func send(baseURL: String, pane: String, message: String) async -> Bool {
        isSending = true
        sendError = nil
        do {
            let resp = try await APIClient.shared.sendMessage(baseURL: baseURL, pane: pane, message: message)
            isSending = false
            if resp.shell == true {
                showToast("ran")
            } else if resp.queued == true {
                showToast("queued")
            }
            return true
        } catch {
            isSending = false
            // 409s (e.g. an open question prompt) and other server errors are
            // surfaced verbatim, per the API contract.
            sendError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            return false
        }
    }

    /// Sends a single named key (esc, tab, an arrow, ctrl-<letter>...)
    /// instead of typed text. Errors route into the same `sendError` the
    /// message composer uses; the text draft is left untouched either way —
    /// a key press isn't "what carl was typing."
    func sendKey(baseURL: String, pane: String, key: String) async {
        do {
            _ = try await APIClient.shared.sendKey(baseURL: baseURL, pane: pane, key: key)
            sendError = nil
        } catch {
            sendError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func showToast(_ message: String) {
        toastMessage = message
        Task {
            try? await Task.sleep(for: .seconds(2))
            toastMessage = nil
        }
    }

    @discardableResult
    func kill(baseURL: String, pane: String) async -> Bool {
        isKilling = true
        killError = nil
        do {
            try await APIClient.shared.killPeer(baseURL: baseURL, pane: pane)
            isKilling = false
            return true
        } catch {
            isKilling = false
            killError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            return false
        }
    }
}

/// Transcript reads the agent's parsed turn history; Terminal shows the raw
/// live pane instead (tool spinners, permission prompts, anything the
/// transcript parser doesn't surface). Shells have no transcript at all, so
/// they're Terminal-only.
private enum SessionViewMode: String, CaseIterable, Hashable {
    case transcript = "Transcript"
    case terminal = "Terminal"
}

struct SessionDetailView: View {
    let peer: Peer
    let baseURL: String
    let onKilled: () -> Void

    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.dismiss) private var dismiss
    @Environment(DraftStore.self) private var draftStore
    @FocusState private var messageFocused: Bool
    @State private var vm = SessionDetailViewModel()
    @State private var messageText = ""
    @State private var mode: SessionViewMode
    @State private var showingKillConfirm = false
    // Set by the drag gesture on the transcript/terminal ScrollView the
    // moment the user takes the scroll position into their own hands —
    // every programmatic bottom-anchor scroll below (initial-landing
    // retries, new-block appends, terminal arrival/mode-switch) checks this
    // first so it never yanks a manually-scrolled-up reader back down.
    @State private var userScrolled = false

    init(peer: Peer, baseURL: String, onKilled: @escaping () -> Void = {}) {
        self.peer = peer
        self.baseURL = baseURL
        self.onKilled = onKilled
        _mode = State(initialValue: peer.role == "shell" ? .terminal : .transcript)
    }

    private var isShell: Bool { peer.role == "shell" }

    // Job cells are killed via the job's own Kill action (server enforces
    // this too); a peer with no pane has nothing to kill.
    private var canKill: Bool {
        guard let pane = peer.pane, !pane.isEmpty else { return false }
        if let session = peer.tmuxSession, session.hasPrefix("cell-") { return false }
        return true
    }

    // "shell:<pane>" vs "send:<pane>" — a given pane is only ever one or the
    // other, but keeping the prefix distinct matches the kind of composer
    // it belongs to (run-a-command vs message-an-agent) if that ever changes.
    private var draftKey: String? {
        guard let pane = peer.pane else { return nil }
        return (isShell ? "shell:" : "send:") + pane
    }

    private var disabledReason: String? {
        if peer.pane == nil || peer.pane?.isEmpty == true {
            return isShell ? "no pane to run in" : "no pane to message"
        }
        // Shells accept any line now (the server types it and runs it as a
        // command) — only agent peers need the messageable/role guard.
        if isShell { return nil }
        if !peer.messageable { return "not directly messageable" }
        return nil
    }

    // Re-polls whenever the app (fore/back)grounds OR the mode toggles —
    // switching Transcript↔Terminal cancels whichever loop was running and
    // starts the other.
    private struct PollKey: Equatable {
        let phase: ScenePhase
        let mode: SessionViewMode
    }

    private static let bottomAnchorID = "bottom"

    var body: some View {
        VStack(spacing: 0) {
            // Header + mode picker are one glass bar at the top, and the key
            // bar + composer are one glass bar at the bottom, instead of the
            // old four hand-drawn 1px hairline rects between five opaque
            // `surface` slabs. Slot order and every string are unchanged.
            VStack(spacing: 0) {
                header
                if !isShell {
                    modePicker
                }
            }
            .background(.ultraThinMaterial)
            .overlay(alignment: .bottom) { Divider().allowsHitTesting(false) }
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
                // Bottom-anchor scroll-to-bottom, both modes. Transcript's
                // blocks are native MarkdownText views that lay out
                // synchronously, so a scroll fired the instant data arrives
                // already sees final content sizes — but the ScrollView's
                // own content-size measurement can lag a frame behind SwiftUI
                // committing the new rows, so every scroll (initial landing
                // or a later append) still pairs an immediate scroll with one
                // ~350ms delayed retry as cheap insurance. Terminal has no
                // incremental block count to key off (capture-pane always
                // replaces the whole screen), so it lands at the bottom once
                // content first arrives, and again on every switch into
                // Terminal — the transcript/terminal Group above share one
                // ScrollView, so switching modes leaves the offset wherever
                // the other mode's very differently sized content last put
                // it. `userScrolled` (set by the drag gesture below) opts a
                // reader out of every programmatic scroll here the moment
                // they've taken the position into their own hands.
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
                .task(id: PollKey(phase: scenePhase, mode: mode)) {
                    guard scenePhase == .active, let pane = peer.pane else { return }
                    switch mode {
                    case .transcript:
                        await vm.loadTranscript(baseURL: baseURL, pane: pane, n: 20)
                        while !Task.isCancelled {
                            try? await Task.sleep(for: .seconds(5))
                            if Task.isCancelled { break }
                            await vm.loadTranscript(baseURL: baseURL, pane: pane, n: 20)
                        }
                    case .terminal:
                        await vm.loadTerminal(baseURL: baseURL, pane: pane)
                        while !Task.isCancelled {
                            try? await Task.sleep(for: .seconds(2))
                            if Task.isCancelled { break }
                            await vm.loadTerminal(baseURL: baseURL, pane: pane)
                        }
                    }
                }
            }
            VStack(spacing: 0) {
                if peer.pane?.isEmpty == false {
                    keyBar
                }
                composer
            }
            .background(.ultraThinMaterial)
            .overlay(alignment: .top) { Divider().allowsHitTesting(false) }
        }
        .background { ThemeBackgroundView() }
        .navigationTitle(peer.agentName ?? peer.name ?? peer.tmuxSession ?? "session")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            if canKill {
                ToolbarItem(placement: .topBarTrailing) {
                    Button(role: .destructive) {
                        showingKillConfirm = true
                    } label: {
                        Label("Kill", systemImage: "xmark")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Kill")
                }
            }
        }
        .confirmationDialog(
            "Kill this session? The tmux pane is closed immediately.",
            isPresented: $showingKillConfirm, titleVisibility: .visible
        ) {
            Button("Kill", role: .destructive) { killPeer() }
            Button("Cancel", role: .cancel) {}
        }
        .overlay(alignment: .top) {
            if let toast = vm.toastMessage {
                Text(toast)
                    .font(.data(11))
                    .foregroundStyle(KTheme.accentInk)
                    .padding(.horizontal, 12).padding(.vertical, 6)
                    .background(KTheme.accent, in: Capsule())
                    .padding(.top, 8)
            }
        }
        // Restores whatever was mid-typed the last time this pane's
        // composer was open (a fresh push after popping back to the list
        // gets a fresh @State, so the shared DraftStore — not local state —
        // is what actually survives the round trip). Every keystroke saves
        // back to it; a successful send clears it in send() below.
        .onAppear {
            if let draftKey { messageText = draftStore.get(draftKey) }
        }
        .onChange(of: messageText) {
            if let draftKey { draftStore.set(draftKey, messageText) }
        }
    }

    // Session status, not job state — SessionTheme's own mapping, so this is
    // deliberately a glowing dot rather than a StateCapsule (which reads
    // BoardTheme's job-state table and would mis-colour every status here).
    private var statusColor: Color {
        SessionTheme.color(forStatus: peer.status, role: peer.role)
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Circle()
                    .fill(statusColor)
                    .frame(width: 8, height: 8)
                    .shadow(color: statusColor.opacity(0.75), radius: 3)
                Text(peer.status)
                    .font(.data(11))
                    .foregroundStyle(statusColor)
                if let branch = peer.branch, !branch.isEmpty {
                    Text(branch).font(.data(11)).foregroundStyle(KTheme.accent)
                }
                Spacer()
            }
            if let killError = vm.killError {
                Text(killError).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 8)
    }

    private var modePicker: some View {
        Picker("View", selection: $mode) {
            ForEach(SessionViewMode.allCases, id: \.self) { m in
                Text(m.rawValue).tag(m)
            }
        }
        .pickerStyle(.segmented)
        .padding(.horizontal)
        .padding(.bottom, 8)
    }

    private var transcriptContent: some View {
        // A plain VStack, not LazyVStack: the blocks are capped to the last 20
        // by the `n: 20` fetch (native MarkdownText rows, cheap to realize
        // even all at once), and a LazyVStack only builds visible rows — a
        // programmatic scrollTo(bottom) into not-yet-built rows would land on
        // empty space. Realizing every row up front keeps scroll-to-bottom exact.
        VStack(alignment: .leading, spacing: 8) {
            if peer.pane == nil {
                Text("no pane — nothing to read")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
                    .padding()
            } else {
                if let err = vm.loadError {
                    Text(err).font(.voice(12)).foregroundStyle(KTheme.counter).padding()
                }
                ForEach(Array(vm.blocks.enumerated()), id: \.offset) { _, block in
                    TranscriptBlockView(block: block)
                }
            }
        }
        .padding()
    }

    @ViewBuilder
    private var terminalContent: some View {
        if peer.pane == nil {
            Text("no pane — nothing to capture")
                .font(.data(11))
                .foregroundStyle(KTheme.text2)
                .padding()
        } else {
            VStack(alignment: .leading, spacing: 8) {
                if let err = vm.terminalError {
                    Text(err).font(.voice(12)).foregroundStyle(KTheme.counter)
                }
                // Raw tmux capture-pane text: fixed-width terminal layout,
                // so it scrolls horizontally instead of reflowing (wrapping
                // would mangle alignment) — terminal-authentic over tidy.
                ScrollView(.horizontal, showsIndicators: true) {
                    Text(vm.terminalContent.isEmpty ? " " : vm.terminalContent)
                        .font(.data(12))
                        .foregroundStyle(KTheme.text)
                        .fixedSize(horizontal: true, vertical: true)
                        .textSelection(.enabled)
                }
            }
            .padding()
        }
    }

    // (label, key-name) pairs for the key bar — key-name is what the server's
    // namedKeys whitelist (actions.go) expects. Visible for both agent and
    // shell panes: the whole point is keys a phone keyboard can't type,
    // which applies equally to interrupting an agent's turn and to
    // navigating a shell.
    private static let keyBarItems: [(label: String, key: String)] = [
        ("esc", "esc"), ("tab", "tab"), ("⏎", "enter"),
        ("↑", "up"), ("↓", "down"), ("←", "left"), ("→", "right"),
        ("^C", "ctrl-c"), ("^D", "ctrl-d"), ("^U", "ctrl-u"),
        ("^Z", "ctrl-z"), ("^R", "ctrl-r"), ("^L", "ctrl-l"),
    ]

    @ViewBuilder
    private var keyBar: some View {
        if let pane = peer.pane, !pane.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                // Glass key caps in one GlassEffectContainer so neighbouring
                // caps blend rather than each carrying its own separate
                // lensing pass. Same 13 keys, same key names, same order.
                GlassEffectContainer(spacing: 6) {
                    HStack(spacing: 6) {
                        ForEach(Self.keyBarItems, id: \.key) { item in
                            Button {
                                sendKey(pane: pane, key: item.key)
                            } label: {
                                Text(item.label)
                                    .font(.data(11))
                                    .foregroundStyle(KTheme.text)
                                    .frame(minWidth: 28)
                                    .padding(.horizontal, 10)
                                    .padding(.vertical, 7)
                                    .contentShape(Capsule())
                            }
                            .buttonStyle(.plain)
                            .glassEffect(.regular, in: Capsule())
                        }
                    }
                }
                .padding(.horizontal)
                .padding(.vertical, 8)
            }
        }
    }

    private func sendKey(pane: String, key: String) {
        Task {
            await vm.sendKey(baseURL: baseURL, pane: pane, key: key)
        }
    }

    // Fires the bottom-anchor scroll immediately, then once more after a
    // ~350ms delay as cheap insurance while the ScrollView's own content-size
    // measurement catches up to the native MarkdownText rows SwiftUI just
    // committed. Same two-fire shape for both callers (initial landing and a
    // later append) — native blocks lay out synchronously, so neither case
    // needs the old multi-rung ladder that waited out async web-view heights.
    // Every retry rechecks `userScrolled` right before it fires, not just
    // once up front, so a drag that happens mid-retry-sequence still cancels
    // the rest.
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

    private var composer: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let sendError = vm.sendError {
                Text(sendError).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            HStack {
                if let reason = disabledReason {
                    Text(reason).font(.data(11)).foregroundStyle(KTheme.text2)
                } else {
                    TextField(isShell ? "$ command to run" : "message", text: $messageText, axis: .vertical)
                        .focused($messageFocused)
                        .font(isShell ? .data(13) : .voice(13))
                        .kField(focused: messageFocused)
                    Button {
                        send()
                    } label: {
                        Label("Send", systemImage: "paperplane.fill")
                    }
                    .buttonStyle(.glassProminent)
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Send")
                    // Send gating is unchanged: in flight, or nothing but
                    // whitespace typed.
                    .disabled(vm.isSending || messageText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
        .padding()
    }

    private func send() {
        guard let pane = peer.pane else { return }
        let text = messageText
        Task {
            // vm.send() returns false on any server error (409s included),
            // which — deliberately — leaves the draft in place.
            if await vm.send(baseURL: baseURL, pane: pane, message: text) {
                messageText = ""
                if let draftKey { draftStore.clear(draftKey) }
            }
        }
    }

    private func killPeer() {
        guard let pane = peer.pane else { return }
        Task {
            if await vm.kill(baseURL: baseURL, pane: pane) {
                onKilled()
                dismiss()
            }
        }
    }
}
