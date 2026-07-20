import SwiftUI
import Observation

@Observable
final class SessionsViewModel {
    var peers: [Peer] = []
    var errorMessage: String?

    func refresh(baseURL: String) async {
        do {
            let resp = try await APIClient.shared.fetchPeers(baseURL: baseURL)
            peers = resp.peers
            errorMessage = nil
        } catch {
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    // Server order (orderAll in peer.go) already places teammates directly
    // after their lead; grouping here only needs to split by agent/shell,
    // never reorder within a group.
    var claudes: [Peer] { peers.filter { $0.role != "shell" && ($0.agent ?? "claude") == "claude" } }
    var codexes: [Peer] { peers.filter { $0.role != "shell" && $0.agent == "codex" } }
    var shells: [Peer] { peers.filter { $0.role == "shell" } }
}

struct SessionsView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @Environment(\.scenePhase) private var scenePhase
    @Environment(AppRouter.self) private var router
    @State private var vm = SessionsViewModel()
    @State private var path = NavigationPath()

    var body: some View {
        NavigationStack(path: $path) {
            List {
                if let err = vm.errorMessage {
                    kSection { Text(err).font(.voice(12)).foregroundStyle(KTheme.counter) }
                }
                if !vm.claudes.isEmpty {
                    kSection("Claude") {
                        ForEach(vm.claudes) { peer in
                            NavigationLink(value: peer) {
                                SessionRow(peer: peer)
                            }
                        }
                    }
                }
                if !vm.codexes.isEmpty {
                    kSection("Codex") {
                        ForEach(vm.codexes) { peer in
                            NavigationLink(value: peer) {
                                SessionRow(peer: peer)
                            }
                        }
                    }
                }
                if !vm.shells.isEmpty {
                    kSection("Shells") {
                        ForEach(vm.shells) { peer in
                            NavigationLink(value: peer) {
                                SessionRow(peer: peer)
                            }
                        }
                    }
                }
                if vm.peers.isEmpty && vm.errorMessage == nil {
                    kSection {
                        Text("no live sessions").font(.data(11)).foregroundStyle(KTheme.text2)
                    }
                }
            }
            .listStyle(.plain)
            .kListStyle()
            .navigationTitle("Sessions")
            .navigationBarTitleDisplayMode(.large)
            .navigationDestination(for: Peer.self) { peer in
                SessionDetailView(peer: peer, baseURL: serverURL)
            }
            .refreshable { await vm.refresh(baseURL: serverURL) }
            .task(id: scenePhase) {
                guard scenePhase == .active else { return }
                await vm.refresh(baseURL: serverURL)
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(6))
                    if Task.isCancelled { break }
                    await vm.refresh(baseURL: serverURL)
                }
            }
            // kraken://session/<pane-without-%> deep link: wait for peers to
            // load if needed, then push that peer's detail onto the stack.
            .task(id: router.pendingSessionPane) {
                guard let pane = router.pendingSessionPane else { return }
                for _ in 0..<10 {
                    if vm.peers.isEmpty {
                        await vm.refresh(baseURL: serverURL)
                    }
                    if let peer = vm.peers.first(where: { $0.pane == pane }) {
                        path.append(peer)
                        break
                    }
                    try? await Task.sleep(for: .seconds(0.5))
                }
                router.pendingSessionPane = nil
            }
        }
    }
}

private struct SessionRow: View {
    let peer: Peer

    private var indent: CGFloat { peer.role == "teammate" ? 16 : 0 }

    private var displayName: String {
        peer.agentName ?? peer.name ?? peer.tmuxSession ?? "-"
    }

    private var project: String {
        guard let cwd = peer.cwd, !cwd.isEmpty else { return "~" }
        let trimmed = cwd.hasSuffix("/") ? String(cwd.dropLast()) : cwd
        return trimmed.split(separator: "/").last.map(String.init) ?? "~"
    }

    var body: some View {
        HStack(spacing: 10) {
            Rectangle()
                .fill(SessionTheme.color(forStatus: peer.status, role: peer.role))
                .frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 2) {
                Text(displayName)
                    .font(.data(13, weight: .bold))
                    .foregroundStyle(KTheme.text)
                HStack(spacing: 6) {
                    Text(project).foregroundStyle(KTheme.text2)
                    if let branch = peer.branch, !branch.isEmpty {
                        Text(branch).foregroundStyle(KTheme.accent)
                    }
                }
                .font(.data(11))
            }
            Spacer()
            Text(peer.status)
                .font(.data(11))
                .foregroundStyle(SessionTheme.color(forStatus: peer.status, role: peer.role))
        }
        .padding(.leading, indent)
    }
}
