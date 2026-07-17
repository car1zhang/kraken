import SwiftUI
import Observation

@Observable
final class SessionDetailViewModel {
    var blocks: [ReadBlock] = []
    var counted = 0
    var loadError: String?
    var sendError: String?
    var isSending = false
    var showQueuedToast = false

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

    @discardableResult
    func send(baseURL: String, pane: String, message: String) async -> Bool {
        isSending = true
        sendError = nil
        do {
            let resp = try await APIClient.shared.sendMessage(baseURL: baseURL, pane: pane, message: message)
            isSending = false
            if resp.queued == true {
                showQueuedToast = true
                Task {
                    try? await Task.sleep(for: .seconds(2))
                    showQueuedToast = false
                }
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
}

struct SessionDetailView: View {
    let peer: Peer
    let baseURL: String

    @Environment(\.scenePhase) private var scenePhase
    @FocusState private var messageFocused: Bool
    @State private var vm = SessionDetailViewModel()
    @State private var messageText = ""

    private var disabledReason: String? {
        if peer.role == "shell" { return "shell pane — nothing to message" }
        if peer.pane == nil || peer.pane?.isEmpty == true { return "no pane to message" }
        if !peer.messageable { return "not directly messageable" }
        return nil
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Rectangle().fill(KTheme.hairline).frame(height: 1)
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 8) {
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
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(KTheme.ground)
            Rectangle().fill(KTheme.hairline).frame(height: 1)
            composer
        }
        .background(KTheme.ground)
        .navigationTitle(peer.agentName ?? peer.name ?? peer.tmuxSession ?? "session")
        .navigationBarTitleDisplayMode(.inline)
        .overlay(alignment: .top) {
            if vm.showQueuedToast {
                Text("queued")
                    .font(.data(11))
                    .foregroundStyle(KTheme.accentInk)
                    .padding(.horizontal, 10).padding(.vertical, 4)
                    .background(KTheme.accent)
                    .padding(.top, 8)
            }
        }
        .task(id: scenePhase) {
            guard scenePhase == .active, let pane = peer.pane else { return }
            await vm.loadTranscript(baseURL: baseURL, pane: pane, n: 20)
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(5))
                if Task.isCancelled { break }
                await vm.loadTranscript(baseURL: baseURL, pane: pane, n: 20)
            }
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
            Text(block.text)
                .font(.voice(13))
                .foregroundStyle(KTheme.text)
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
                    TextField("message", text: $messageText, axis: .vertical)
                        .focused($messageFocused)
                        .font(.voice(13))
                        .kField(focused: messageFocused)
                    Button("Send") { send() }
                        .buttonStyle(.kPrimary)
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
            if await vm.send(baseURL: baseURL, pane: pane, message: text) {
                messageText = ""
            }
        }
    }
}
