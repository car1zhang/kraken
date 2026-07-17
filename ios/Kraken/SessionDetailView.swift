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

    private func showToast(_ message: String) {
        toastMessage = message
        Task {
            try? await Task.sleep(for: .seconds(2))
            toastMessage = nil
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

    @Environment(\.scenePhase) private var scenePhase
    @Environment(DraftStore.self) private var draftStore
    @FocusState private var messageFocused: Bool
    @State private var vm = SessionDetailViewModel()
    @State private var messageText = ""
    @State private var mode: SessionViewMode

    init(peer: Peer, baseURL: String) {
        self.peer = peer
        self.baseURL = baseURL
        _mode = State(initialValue: peer.role == "shell" ? .terminal : .transcript)
    }

    private var isShell: Bool { peer.role == "shell" }

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
            header
            Rectangle().fill(KTheme.hairline).frame(height: 1)
            if !isShell {
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
                .background(KTheme.ground)
                // Transcript scroll-to-bottom: the blocks are self-sizing
                // WKWebViews (MarkdownBody) that measure their height async, so
                // a scroll fired the instant data arrives lands on ~zero-height
                // content. Fire it a beat after the block count changes — which
                // covers both the first load (0→N) and later appends — giving
                // the web views time to report their heights first. The
                // terminal deliberately does NOT scroll: capture-pane returns
                // the current fixed-size screen, so top-aligned already shows
                // the latest output (its bottom rows), and a nested horizontal
                // ScrollView doesn't bottom-anchor cleanly anyway.
                .onChange(of: vm.blocks.count) { oldCount, newCount in
                    guard mode == .transcript, newCount > oldCount else { return }
                    Task {
                        try? await Task.sleep(for: .milliseconds(350))
                        withAnimation { proxy.scrollTo(Self.bottomAnchorID, anchor: .bottom) }
                    }
                }
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
            Rectangle().fill(KTheme.hairline).frame(height: 1)
            composer
        }
        .background(KTheme.ground)
        .navigationTitle(peer.agentName ?? peer.name ?? peer.tmuxSession ?? "session")
        .navigationBarTitleDisplayMode(.inline)
        .overlay(alignment: .top) {
            if let toast = vm.toastMessage {
                Text(toast)
                    .font(.data(11))
                    .foregroundStyle(KTheme.accentInk)
                    .padding(.horizontal, 10).padding(.vertical, 4)
                    .background(KTheme.accent)
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

    private var header: some View {
        HStack(spacing: 8) {
            Rectangle()
                .fill(SessionTheme.color(forStatus: peer.status, role: peer.role))
                .frame(width: 8, height: 8)
            Text(peer.status)
                .font(.data(11))
                .foregroundStyle(SessionTheme.color(forStatus: peer.status, role: peer.role))
            if let branch = peer.branch, !branch.isEmpty {
                Text(branch).font(.data(11)).foregroundStyle(KTheme.accent)
            }
            Spacer()
        }
        .padding(.horizontal)
        .padding(.vertical, 7)
        .background(KTheme.surface)
    }

    private var modePicker: some View {
        Picker("View", selection: $mode) {
            ForEach(SessionViewMode.allCases, id: \.self) { m in
                Text(m.rawValue).tag(m)
            }
        }
        .pickerStyle(.segmented)
        .padding(.horizontal)
        .padding(.vertical, 6)
        .background(KTheme.surface)
    }

    private var transcriptContent: some View {
        // A plain VStack, not LazyVStack: the blocks are capped to the last 20
        // by the `n: 20` fetch, so realizing them all up front is cheap — and
        // it's what makes scroll-to-bottom reliable. A LazyVStack only builds
        // visible rows, so a programmatic scrollTo(bottom) into not-yet-built
        // MarkdownBody web views landed on empty space; realizing every row
        // means their async heights are all known by the time we scroll.
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
                    blockView(block)
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

    @ViewBuilder
    private func blockView(_ block: ReadBlock) -> some View {
        switch block.kind {
        case "user":
            Text("❯ " + block.text)
                .font(.voice(13, weight: .bold))
                .foregroundStyle(KTheme.text)
        case "thinking":
            Text(block.text)
                .font(.voice(13))
                .italic()
                .foregroundStyle(KTheme.text2)
        case "tool":
            Text("→ " + block.text)
                .font(.data(11))
                .foregroundStyle(KTheme.text2)
        default:
            // Assistant prose — the only block kind rendered as markdown, so
            // **bold**/lists/`code`/links/headings show up properly instead
            // of raw markdown syntax.
            MarkdownBody(markdown: block.text)
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
                    .buttonStyle(.borderedProminent)
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Send")
                    .disabled(vm.isSending || messageText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
        .padding()
        .background(KTheme.surface)
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
}
