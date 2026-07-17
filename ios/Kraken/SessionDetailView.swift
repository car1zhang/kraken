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
            Divider()
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 8) {
                    if peer.pane == nil {
                        Text("no pane — nothing to read").foregroundStyle(.secondary).padding()
                    } else {
                        if let err = vm.loadError {
                            Text(err).foregroundStyle(.red).padding()
                        }
                        ForEach(Array(vm.blocks.enumerated()), id: \.offset) { _, block in
                            blockView(block)
                        }
                    }
                }
                .padding()
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            Divider()
            composer
        }
        .navigationTitle(peer.agentName ?? peer.name ?? peer.tmuxSession ?? "session")
        .navigationBarTitleDisplayMode(.inline)
        .overlay(alignment: .top) {
            if vm.showQueuedToast {
                Text("queued")
                    .font(.caption)
                    .padding(.horizontal, 10).padding(.vertical, 4)
                    .background(.thinMaterial, in: Capsule())
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
            Circle().fill(SessionTheme.color(forStatus: peer.status, role: peer.role)).frame(width: 8, height: 8)
            Text(peer.status).font(.caption).foregroundStyle(.secondary)
            if let branch = peer.branch, !branch.isEmpty {
                Text(branch).font(.caption).foregroundStyle(.purple)
            }
            Spacer()
        }
        .padding(.horizontal)
        .padding(.vertical, 6)
    }

    @ViewBuilder
    private func blockView(_ block: ReadBlock) -> some View {
        switch block.kind {
        case "user":
            Text("❯ " + block.text).bold()
        case "thinking":
            Text(block.text).italic().foregroundStyle(.secondary)
        case "tool":
            Text("→ " + block.text)
                .font(.system(.footnote, design: .monospaced))
                .foregroundStyle(.secondary)
        default:
            Text(block.text)
        }
    }

    private var composer: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let sendError = vm.sendError {
                Text(sendError).font(.caption).foregroundStyle(.red)
            }
            HStack {
                if let reason = disabledReason {
                    Text(reason).font(.caption).foregroundStyle(.secondary)
                } else {
                    TextField("message", text: $messageText, axis: .vertical)
                        .textFieldStyle(.roundedBorder)
                    Button("Send") { send() }
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
            if await vm.send(baseURL: baseURL, pane: pane, message: text) {
                messageText = ""
            }
        }
    }
}
