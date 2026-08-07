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

/// The three session groups. `Identifiable` with the rawValue as `id` so the
/// carousel's `ForEach` tags each page with a `String`, which is the identity
/// `scrollPosition(id:)` reads and writes — same arrangement as `BoardColumn`.
private enum SessionsTab: String, CaseIterable, Hashable, Identifiable {
    case claudes = "Claudes"
    case codexes = "Codexes"
    case shells = "Shells"

    var id: String { rawValue }
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
            VStack(spacing: 8) {
                errorBanner
                groupChipBar
                sessionCarousel
            }
            .padding(.top, 4)
            // ONE field for the whole screen, on the root, ignoring the safe
            // area — exactly the Board's arrangement. The pre-existing
            // clipping defect was the opposite: `kListStyle()` hangs a
            // `ThemeBackgroundView` off EVERY List it is applied to, so each
            // carousel page carried its own gradient sized to that page, and
            // side-scrolling dragged three independent fields past each other
            // with hard seams at the page edges and bare background in the
            // peek gutters. The pages are transparent now
            // (`.scrollContentBackground(.hidden)` only) and this one field
            // shows through under the peeks, the nav bar and the tab bar.
            .background { ThemeBackgroundView().ignoresSafeArea() }
            .navigationTitle("Sessions")
            // Large, and NEVER with a per-view opaque scroll-edge appearance:
            // the deleted appearance proxy is what used to paint over this
            // title at rest, and nothing here may reintroduce it.
            .navigationBarTitleDisplayMode(.large)
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    // Native glass toolbar button — tint flows from the app's
                    // `.tint`, no hand-painted accent foreground.
                    Button {
                        showingSpawn = true
                    } label: {
                        Label("New Session", systemImage: "plus")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("New Session")
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

    /// Poll/kill errors. Left WRAPPING on purpose: the truncation obligation
    /// is scoped to session rows, and an error string is the one thing on this
    /// screen whose tail is the part you need.
    @ViewBuilder
    private var errorBanner: some View {
        if let err = killError ?? vm.errorMessage {
            Text(err)
                .font(.voice(12))
                .foregroundStyle(KTheme.counter)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .glassEffect(
                    .regular.tint(KTheme.counter.opacity(0.16)),
                    in: RoundedRectangle(cornerRadius: 16, style: .continuous)
                )
                .padding(.horizontal)
        }
    }

    /// The Board's `chipScroller`, three chips wide: tap to jump, the current
    /// group lit, a live count per group. Three capsules fit a phone at
    /// reading size, so the scroll view never actually scrolls today — it is
    /// here because this bar is the Board's bar, and a fourth group (or a
    /// larger Dynamic Type size) must widen it rather than clip it. The
    /// follow-the-selection recentre is kept for the same reason: this bar is
    /// the carousel's only position indicator, full stop — the `TabView` it
    /// replaced was `.page(indexDisplayMode: .never)`, so there were never any
    /// dots to inherit and a lit chip scrolled out of sight would leave the
    /// screen saying nothing at all about which group you are on.
    private var groupChipBar: some View {
        ScrollViewReader { proxy in
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(SessionsTab.allCases) { tab in
                        groupChip(tab).id(tab)
                    }
                }
                .padding(.vertical, 2)
            }
            .contentMargins(.horizontal, 16, for: .scrollContent)
            .onAppear { proxy.scrollTo(selectedTab, anchor: .center) }
            .onChange(of: selectedTab) { _, tab in
                withAnimation(.easeInOut(duration: 0.25)) {
                    proxy.scrollTo(tab, anchor: .center)
                }
            }
        }
    }

    /// One group's capsule. Same two channels as the Board's resting chip —
    /// the fill says where you are, the stroke seconds it — minus the drop
    /// target and the attention dot, neither of which has a meaning here.
    private func groupChip(_ tab: SessionsTab) -> some View {
        let count = peers(for: tab).count
        let isCurrent = selectedTab == tab
        let fill: Color = isCurrent ? KTheme.accent.opacity(0.16) : Color.clear
        let stroke: Color = isCurrent ? KTheme.accent.opacity(0.5) : KTheme.hairline
        return Button {
            // Animated: the carousel underneath is a scroll view, and a scroll
            // view teleports to a new `scrollPosition` unless told otherwise.
            withAnimation(.easeInOut(duration: 0.3)) { selectedTab = tab }
        } label: {
            HStack(spacing: 5) {
                Text(tab.rawValue)
                    .font(.data(11, weight: .semibold))
                    .kerning(0.5)
                    .foregroundStyle(isCurrent ? KTheme.accent : KTheme.text2)
                    .lineLimit(1)
                    .minimumScaleFactor(0.8)
                Text("\(count)")
                    .font(.data(11, weight: .bold))
                    .foregroundStyle(count > 0 ? KTheme.accent : KTheme.text2)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 7)
            .background(fill, in: Capsule())
            .overlay(Capsule().stroke(stroke, lineWidth: 1))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(tab.rawValue), \(count)")
    }

    /// The three groups as one view-aligned carousel — the paged `TabView`'s
    /// replacement.
    ///
    /// The pager mechanism is the Board's `columnCarousel` line for line: the
    /// same `LazyHStack(alignment: .top, spacing: 12)`, the same
    /// `containerRelativeFrame(.horizontal)` making each page exactly one
    /// container wide, the same `scrollTargetLayout()` +
    /// `.scrollTargetBehavior(.viewAligned)` landing a swipe on a group rather
    /// than anywhere in between, the same `.contentMargins(.horizontal, 20)`
    /// letting the neighbours peek, and the same nil-dropping
    /// `scrollPosition(id:anchor: .center)`. What differs is only what the
    /// pages are OF — three session groups instead of six board columns, and
    /// no drag in flight to route, so there is no `dragTargetStrip` counterpart
    /// and no drop delegate on the chips.
    private var sessionCarousel: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            LazyHStack(alignment: .top, spacing: 12) {
                ForEach(SessionsTab.allCases) { tab in
                    sessionList(for: tab)
                        .containerRelativeFrame(.horizontal)
                }
            }
            .scrollTargetLayout()
        }
        .scrollTargetBehavior(.viewAligned)
        // The Board's exact mechanism and exact number. `.contentMargins`
        // propagates into nested scroll containers, which is why an earlier
        // pass reached for `safeAreaPadding` instead — but that was a symptom
        // of the pages being Lists. Now that a page is a plain ScrollView with
        // no inset-grouped margin of its own, there is no second inset to
        // double and no reason for the two carousels to differ.
        .contentMargins(.horizontal, 20, for: .scrollContent)
        .scrollPosition(id: carouselTab, anchor: .center)
    }

    /// `selectedTab` as the optional identity `scrollPosition(id:)` wants.
    /// The setter DROPS nil: the scroll view reports nil while the content is
    /// mid-flight between two pages, and writing that back would cost the chip
    /// bar its highlight — and every deep link its target — on every swipe.
    private var carouselTab: Binding<String?> {
        Binding(
            get: { selectedTab.rawValue },
            set: { raw in
                guard let raw, let tab = SessionsTab(rawValue: raw), tab != selectedTab else { return }
                selectedTab = tab
            }
        )
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

    /// One group's page: `ScrollView` + `LazyVStack`, which is the Board's
    /// `columnView` shape and NOT the `List` this started as.
    ///
    /// The List had to go for a gesture reason, not a styling one. Its rows
    /// carried `swipeActions`, whose pan recognizer lives deeper in the view
    /// tree than the carousel's — so a horizontal drag begun on a row is
    /// claimed by swipe-to-kill and never reaches the pager, leaving
    /// page-switching to work only from the gutters and the chip bar.
    /// Removing the List
    /// removes `swipeActions` with it (they are a List-only affordance), so
    /// Kill now lives ONLY in the context menu it already shared — same
    /// destructive role, same `peerToKill` confirmation dialog, one fewer way
    /// in. The row group keeps the inset rounded-card look via `ThemedCard`,
    /// so nothing about the idiom changes.
    private func sessionList(for tab: SessionsTab) -> some View {
        let rows = peers(for: tab)
        return ScrollView {
            ThemedCard {
                LazyVStack(spacing: 0) {
                    if rows.isEmpty {
                        Text("no live sessions")
                            .font(.data(11))
                            .foregroundStyle(KTheme.text2)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal, 14)
                            .padding(.vertical, 16)
                    } else {
                        ForEach(Array(rows.enumerated()), id: \.element.id) { index, peer in
                            sessionRowLink(peer)
                            // Hairline between rows only — a trailing rule
                            // under the last one would read as a torn card
                            // edge. This is what `.insetGrouped` drew for
                            // free, in the same `KTheme.hairline`.
                            if index < rows.count - 1 {
                                Rectangle()
                                    .fill(KTheme.hairline)
                                    .frame(height: 1)
                                    .padding(.leading, 14)
                            }
                        }
                    }
                }
            }
            .padding(.bottom, 8)
        }
        .refreshable { await vm.refresh(baseURL: serverURL) }
    }

    /// One tappable row. The WHOLE row is the target (`contentShape` over the
    /// padded frame), and the disclosure chevron is drawn explicitly — outside
    /// a List, `NavigationLink` no longer supplies one, and dropping it would
    /// quietly remove the only signal that a row opens anything.
    @ViewBuilder
    private func sessionRowLink(_ peer: Peer) -> some View {
        let link = NavigationLink(value: peer) {
            HStack(spacing: 8) {
                SessionRow(peer: peer)
                Image(systemName: "chevron.right")
                    .font(.system(size: 11, weight: .semibold))
                    .foregroundStyle(KTheme.text2)
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        // The GATE IS ON THE MODIFIER, not on the button inside it. Since the
        // swipe action went away this menu is the row's only kill affordance,
        // so a job cell or a paneless peer — every row `canKill` rejects —
        // would long-press into a menu with nothing in it, which reads as
        // broken rather than as "nothing to do here". Non-killable rows get no
        // long-press menu at all.
        if canKill(peer) {
            link.contextMenu {
                Button(role: .destructive) {
                    peerToKill = peer
                } label: {
                    Label("Kill", systemImage: "xmark")
                }
            }
        } else {
            link
        }
    }

    /// Falls through EMPTY strings, not just nil. This is defensive, NOT a
    /// claim about the daemon: `internal/core/peer.go` marks all three fields
    /// `omitempty`, so the current producer never puts `""` on the wire. It
    /// guards a future producer or hand-crafted JSON, where `??` alone would
    /// pick an empty string and put an empty confirmation dialog on screen
    /// ("Kill ? The tmux pane is closed immediately."). The Go side's own
    /// `Peer.DisplayName` skips empties for the same reason.
    private func displayName(_ peer: Peer) -> String {
        for candidate in [peer.agentName, peer.name, peer.tmuxSession] {
            if let candidate, !candidate.isEmpty { return candidate }
        }
        return "this session"
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

    /// Falls through EMPTY strings, not just nil — `??` only catches the
    /// latter. Defensive, NOT a claim about the daemon: `internal/core/peer.go`
    /// marks all three fields `omitempty`, so `""` never reaches us today. It
    /// guards a future producer or hand-crafted JSON, where an empty string
    /// would win the `??` chain and render a nameless row.
    private var displayName: String {
        for candidate in [peer.agentName, peer.name, peer.tmuxSession] {
            if let candidate, !candidate.isEmpty { return candidate }
        }
        return "-"
    }

    private var project: String {
        guard let cwd = peer.cwd, !cwd.isEmpty else { return "~" }
        let trimmed = cwd.hasSuffix("/") ? String(cwd.dropLast()) : cwd
        return trimmed.split(separator: "/").last.map(String.init) ?? "~"
    }

    // The status mapping is SessionTheme's (running/waiting/question/polling/
    // blocked/error/shell), NOT BoardTheme's job-state mapping — so this stays
    // its own dot rather than adopting StateCapsule, which would recolour
    // every session status through the wrong table.
    private var statusColor: Color {
        SessionTheme.color(forStatus: peer.status, role: peer.role)
    }

    var body: some View {
        // Every string in this row is server-supplied and unbounded — an agent
        // name, a cwd leaf, a branch. NOTHING here wraps: each Text takes
        // `lineLimit(1)` + tail truncation, and the row is laid out so the
        // squeeze lands on the long free-form text rather than on the fixed
        // vocabulary beside it.
        HStack(spacing: 10) {
            Circle()
                .fill(statusColor)
                .frame(width: 8, height: 8)
                .shadow(color: statusColor.opacity(0.75), radius: 3)
                // A circle in a compressible HStack gets squeezed too; this
                // pins it at 8pt so a long name can never eat the status dot.
                .fixedSize()
            VStack(alignment: .leading, spacing: 2) {
                Text(displayName)
                    .font(.data(13, weight: .bold))
                    .foregroundStyle(KTheme.text)
                    .lineLimit(1)
                    .truncationMode(.tail)
                HStack(spacing: 6) {
                    // EQUAL priority, both truncating. Priority on either one
                    // lets a single long unbroken string swallow the other
                    // whole — and the branch is precisely the disambiguator
                    // when several sessions share a repo, so it must never be
                    // the field that vanishes. Equal priority makes the HStack
                    // split the shortfall between them, leaving a readable
                    // head of each.
                    Text(project)
                        .foregroundStyle(KTheme.text2)
                        .lineLimit(1)
                        .truncationMode(.tail)
                    if let branch = peer.branch, !branch.isEmpty {
                        Text(branch)
                            .foregroundStyle(KTheme.accent)
                            .lineLimit(1)
                            .truncationMode(.tail)
                    }
                }
                .font(.data(11))
            }
            // The name/project block is the compressible one, and it must
            // claim the slack rather than let `Spacer()` push the status off
            // the row: `maxWidth: .infinity` + no layout priority is what
            // makes truncation happen HERE.
            .frame(maxWidth: .infinity, alignment: .leading)
            Text(peer.status)
                .font(.data(11))
                .foregroundStyle(statusColor)
                .lineLimit(1)
                .truncationMode(.tail)
                // Statuses come from a small fixed vocabulary (running /
                // waiting / question / …), so the status keeps its intrinsic
                // width and the free-form text above yields instead.
                .layoutPriority(1)
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
                .buttonStyle(.glassProminent)
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
