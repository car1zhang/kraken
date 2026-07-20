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

private enum SessionsTab: String, CaseIterable, Hashable {
    case claudes = "Claudes"
    case codexes = "Codexes"
    case shells = "Shells"
}

struct SessionsView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @Environment(\.scenePhase) private var scenePhase
    @Environment(AppRouter.self) private var router
    @State private var vm = SessionsViewModel()
    @State private var path = NavigationPath()
    @State private var selectedTab: SessionsTab = .claudes
    @State private var showingSpawn = false
    @State private var peerToKill: Peer?
    // Its own state (not vm.errorMessage) so the next successful poll
    // doesn't silently clear it out from under the user.
    @State private var killError: String?

    var body: some View {
        NavigationStack(path: $path) {
            VStack(spacing: 0) {
                Picker("Tab", selection: $selectedTab) {
                    ForEach(SessionsTab.allCases, id: \.self) { tab in
                        Text(tab.rawValue).tag(tab)
                    }
                }
                .pickerStyle(.segmented)
                .padding(.horizontal)
                .padding(.vertical, 6)

                if let err = killError ?? vm.errorMessage {
                    Text(err)
                        .font(.voice(12))
                        .foregroundStyle(KTheme.counter)
                        .padding(.horizontal)
                        .padding(.bottom, 4)
                }

                TabView(selection: $selectedTab) {
                    ForEach(SessionsTab.allCases, id: \.self) { tab in
                        sessionList(for: tab).tag(tab)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: .never))
            }
            .background(KTheme.ground)
            .navigationTitle("Sessions")
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showingSpawn = true
                    } label: {
                        Image(systemName: "plus")
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(KTheme.accent)
                }
            }
            .navigationDestination(for: Peer.self) { peer in
                SessionDetailView(peer: peer, baseURL: serverURL) {
                    Task { await vm.refresh(baseURL: serverURL) }
                }
            }
            .sheet(isPresented: $showingSpawn) {
                NavigationStack {
                    SpawnSheet(baseURL: serverURL) {
                        showingSpawn = false
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            .confirmationDialog(
                "Kill this session?",
                isPresented: Binding(get: { peerToKill != nil }, set: { if !$0 { peerToKill = nil } }),
                titleVisibility: .visible,
                presenting: peerToKill
            ) { peer in
                Button("Kill", role: .destructive) { kill(peer) }
                Button("Cancel", role: .cancel) {}
            } message: { peer in
                Text("Kill \(displayName(peer))? The tmux pane is closed immediately.")
            }
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
                    await vm.refresh(baseURL: serverURL)
                    if let peer = vm.peers.first(where: { $0.pane == pane }) {
                        selectedTab = tab(for: peer)
                        path.append(peer)
                        break
                    }
                    do {
                        try await Task.sleep(for: .seconds(0.5))
                    } catch {
                        break
                    }
                }
                router.pendingSessionPane = nil
            }
            // JobDetailView's Cell row: resolve the job's zeus session by its
            // tmux session name and push it, same retry-while-loading shape
            // as the pane deep link above.
            .task(id: router.pendingSessionCell) {
                guard let cell = router.pendingSessionCell else { return }
                for _ in 0..<10 {
                    await vm.refresh(baseURL: serverURL)
                    if let peer = vm.peers.first(where: { $0.agent == "claude" && $0.tmuxSession == cell })
                        ?? vm.peers.first(where: { $0.tmuxSession == cell }) {
                        selectedTab = tab(for: peer)
                        path.append(peer)
                        break
                    }
                    do {
                        try await Task.sleep(for: .seconds(0.5))
                    } catch {
                        break
                    }
                }
                router.pendingSessionCell = nil
            }
        }
    }

    private func peers(for tab: SessionsTab) -> [Peer] {
        switch tab {
        case .claudes: return vm.claudes
        case .codexes: return vm.codexes
        case .shells: return vm.shells
        }
    }

    // Which segmented tab a deep-linked peer actually lives under, so a
    // session/cell link lands on a visible row instead of pushing detail
    // onto a hidden tab's stack.
    private func tab(for peer: Peer) -> SessionsTab {
        if peer.role == "shell" { return .shells }
        if peer.agent == "codex" { return .codexes }
        return .claudes
    }

    private func sessionList(for tab: SessionsTab) -> some View {
        let rows = peers(for: tab)
        return List {
            if rows.isEmpty {
                kSection {
                    Text("no live sessions").font(.data(11)).foregroundStyle(KTheme.text2)
                }
            } else {
                kSection {
                    ForEach(rows) { peer in
                        NavigationLink(value: peer) {
                            SessionRow(peer: peer)
                        }
                        .swipeActions(edge: .trailing) {
                            if canKill(peer) {
                                Button(role: .destructive) {
                                    peerToKill = peer
                                } label: {
                                    Label("Kill", systemImage: "xmark")
                                }
                            }
                        }
                        .contextMenu {
                            if canKill(peer) {
                                Button(role: .destructive) {
                                    peerToKill = peer
                                } label: {
                                    Label("Kill", systemImage: "xmark")
                                }
                            }
                        }
                    }
                }
            }
        }
        .listStyle(.plain)
        .kListStyle()
        .refreshable { await vm.refresh(baseURL: serverURL) }
    }

    private func displayName(_ peer: Peer) -> String {
        peer.agentName ?? peer.name ?? peer.tmuxSession ?? "this session"
    }

    // Job cells are killed via the job's own Kill action (server enforces
    // this too — a "cell-" tmux session 409s here); a peer with no pane has
    // nothing to kill.
    private func canKill(_ peer: Peer) -> Bool {
        guard let pane = peer.pane, !pane.isEmpty else { return false }
        if let session = peer.tmuxSession, session.hasPrefix("cell-") { return false }
        return true
    }

    private func kill(_ peer: Peer) {
        guard let pane = peer.pane else {
            peerToKill = nil
            return
        }
        peerToKill = nil
        killError = nil
        Task {
            do {
                try await APIClient.shared.killPeer(baseURL: serverURL, pane: pane)
                await vm.refresh(baseURL: serverURL)
            } catch {
                killError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
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

/// The Sessions tab's "+" sheet: spawn a plain shell or a fresh Claude
/// session in a new tmux pane. Directory is shared by both kinds (empty →
/// server default of home); Name/Prompt are Claude-only, Command is
/// shell-only. Mirrors SteerView's Form/toolbar/error shape.
private struct SpawnSheet: View {
    let baseURL: String
    var onSpawned: () -> Void

    @Environment(\.dismiss) private var dismiss
    @FocusState private var focusedField: Field?
    @State private var kind: SpawnKind = .shell
    @State private var dir = ""
    @State private var name = ""
    @State private var prompt = ""
    @State private var cmd = ""
    @State private var isBusy = false
    @State private var errorMessage: String?

    private enum SpawnKind: String, CaseIterable {
        case shell = "Shell"
        case claude = "Claude"
    }

    private enum Field {
        case dir, name, prompt, cmd
    }

    private var canSpawn: Bool {
        switch kind {
        case .shell: return true
        case .claude: return !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
    }

    var body: some View {
        Form {
            kSection {
                Picker("Kind", selection: $kind) {
                    ForEach(SpawnKind.allCases, id: \.self) { k in
                        Text(k.rawValue).tag(k)
                    }
                }
                .pickerStyle(.segmented)
            }
            kSection("Directory") {
                TextField("defaults to home", text: $dir)
                    .focused($focusedField, equals: .dir)
                    .font(.data(13))
                    .kField(focused: focusedField == .dir)
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
            }
            switch kind {
            case .claude:
                kSection("Name") {
                    TextField("required", text: $name)
                        .focused($focusedField, equals: .name)
                        .font(.data(13))
                        .kField(focused: focusedField == .name)
                }
                kSection("Prompt (optional)") {
                    TextEditor(text: $prompt)
                        .focused($focusedField, equals: .prompt)
                        .scrollContentBackground(.hidden)
                        .kField(focused: focusedField == .prompt, minHeight: 100)
                        .listRowBackground(Color.clear)
                }
            case .shell:
                kSection("Command (optional)") {
                    TextField("optional", text: $cmd)
                        .focused($focusedField, equals: .cmd)
                        .font(.data(13))
                        .kField(focused: focusedField == .cmd)
                }
            }
            if let errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle("New Session")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton(label: "Cancel") { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    spawn()
                } label: {
                    Label("Spawn", systemImage: "plus")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Spawn")
                .buttonStyle(.borderedProminent)
                .disabled(isBusy || !canSpawn)
            }
        }
        .disabled(isBusy)
    }

    private func spawn() {
        isBusy = true
        errorMessage = nil
        let trimmedDir = dir.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedPrompt = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        let trimmedCmd = cmd.trimmingCharacters(in: .whitespacesAndNewlines)
        Task {
            do {
                _ = try await APIClient.shared.spawnPeer(
                    baseURL: baseURL,
                    kind: kind == .claude ? "claude" : "shell",
                    dir: trimmedDir.isEmpty ? nil : trimmedDir,
                    name: kind == .claude ? trimmedName : nil,
                    prompt: (kind == .claude && !trimmedPrompt.isEmpty) ? trimmedPrompt : nil,
                    cmd: (kind == .shell && !trimmedCmd.isEmpty) ? trimmedCmd : nil
                )
                isBusy = false
                onSpawned()
            } catch {
                isBusy = false
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
