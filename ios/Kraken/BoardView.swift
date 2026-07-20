import SwiftUI
import Observation

@Observable
final class BoardViewModel {
    var board: WebBoard?
    var errorMessage: String?

    func refresh(baseURL: String) async {
        do {
            let board = try await APIClient.shared.fetchBoard(baseURL: baseURL)
            self.board = board
            self.errorMessage = nil
            // Every successful fetch (this foreground poll, and the
            // BGAppRefreshTask in NotificationManager) runs the same
            // blocked-job diff — one notification per newly-blocked job.
            NotificationManager.shared.processBoard(board)
        } catch {
            self.errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    func jobs(in column: BoardColumn) -> [WebJob] {
        (board?.jobs ?? []).filter { BoardTheme.column(for: $0) == column }
    }

    /// Non-blocking contract-handshake warnings for the latest board fetch —
    /// most severe first. `contract` absent or below what this app expects
    /// means the server predates the handshake or hasn't been restarted onto
    /// a build that speaks it; above means this app is the one that's stale.
    var contractWarnings: [String] {
        guard let board else { return [] }
        var warnings: [String] = []
        let contract = board.contract ?? 0
        if contract < APIClient.expectedContract {
            warnings.append("server is running an older kraken build — restart k web on the Mac")
        } else if contract > APIClient.expectedContract {
            warnings.append("server is newer than this app — update the Kraken app")
        }
        if board.stale == true {
            warnings.append("kraken was reinstalled since this server started — restart k web")
        }
        if board.daemonOK && board.daemon.stale == true {
            warnings.append("kraken was reinstalled since the daemon started — restart the daemon")
        }
        return warnings
    }

    /// Whether any job in `column` needs attention — a pending question or a
    /// non-empty health string — for the overview strip/sheet's flag badge.
    func hasFlag(in column: BoardColumn) -> Bool {
        jobs(in: column).contains { job in
            job.question != nil || !(job.health ?? "").isEmpty
        }
    }
}

struct BoardView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @Environment(\.scenePhase) private var scenePhase
    @Environment(AppRouter.self) private var router
    @State private var vm = BoardViewModel()
    @State private var selectedJob: WebJob?
    @State private var showingNewJob = false
    @State private var showingOverview = false
    @State private var selectedColumn: BoardColumn = .draft

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                daemonBar
                contractBanner
                overviewStrip
                TabView(selection: $selectedColumn) {
                    ForEach(BoardColumn.allCases) { column in
                        columnView(column)
                            .tag(column)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: .always))
            }
            .background(KTheme.ground)
            .navigationTitle("Board")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showingNewJob = true
                    } label: {
                        Image(systemName: "plus")
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(KTheme.accent)
                }
            }
            .task(id: scenePhase) {
                guard scenePhase == .active else { return }
                await vm.refresh(baseURL: serverURL)
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(4))
                    if Task.isCancelled { break }
                    await vm.refresh(baseURL: serverURL)
                }
            }
            .sheet(item: $selectedJob) { job in
                NavigationStack {
                    JobDetailView(job: job, baseURL: serverURL) {
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            .sheet(isPresented: $showingNewJob) {
                NavigationStack {
                    NewJobSheet(baseURL: serverURL) {
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            .sheet(isPresented: $showingOverview) {
                NavigationStack {
                    BoardOverviewSheet(vm: vm, baseURL: serverURL) {
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            // Deep links (kraken://board/<column>, kraken://overview,
            // kraken://job/<slug>) stamp a pending value on the shared
            // router; this view is the one that owns each destination, so
            // it's the one that consumes and clears it. onAppear covers a
            // route stamped before this view mounted (cold launch via the
            // -KrakenRoute launch argument); onChange covers routes arriving
            // while it's already up.
            .onAppear { consumePendingBoardRoutes() }
            .onChange(of: router.pendingBoardColumn) { consumePendingBoardRoutes() }
            .onChange(of: router.pendingOverview) { consumePendingBoardRoutes() }
            .task(id: router.pendingJobSlug) {
                guard let slug = router.pendingJobSlug else { return }
                // The board may not have loaded yet (e.g. a cold launch
                // straight into a kraken://job/<slug> link) — retry briefly
                // rather than silently dropping the link.
                for _ in 0..<10 {
                    if vm.board == nil {
                        await vm.refresh(baseURL: serverURL)
                    }
                    if let job = vm.board?.jobs.first(where: { $0.slug == slug }) {
                        selectedJob = job
                        break
                    }
                    try? await Task.sleep(for: .seconds(0.5))
                }
                router.pendingJobSlug = nil
            }
        }
    }

    private func consumePendingBoardRoutes() {
        if let column = router.pendingBoardColumn {
            selectedColumn = column
            router.pendingBoardColumn = nil
        }
        if router.pendingOverview {
            showingOverview = true
            router.pendingOverview = false
        }
    }

    // Non-blocking contract-mismatch notice — never hides the board or blocks
    // actions (approved decision), just flags that the client/server pairing
    // is off so carl knows to restart k web or update the app.
    @ViewBuilder
    private var contractBanner: some View {
        let warnings = vm.contractWarnings
        if !warnings.isEmpty {
            Text(warnings.joined(separator: " · "))
                .font(.voice(11))
                .foregroundStyle(KTheme.revealed)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal)
                .padding(.vertical, 6)
                .background(KTheme.revealedSoft)
                .overlay(alignment: .bottom) {
                    Rectangle().fill(KTheme.hairline).frame(height: 1)
                }
        }
    }

    // 7x7 square LED with a soft glow — mirrors .led / .led.ok.
    private var daemonBar: some View {
        HStack(spacing: 8) {
            if let board = vm.board {
                Rectangle()
                    .fill(board.daemonOK ? KTheme.solved : KTheme.counter)
                    .frame(width: 7, height: 7)
                    .shadow(color: (board.daemonOK ? KTheme.solved : KTheme.counter).opacity(0.55), radius: 4)
                Text(board.daemonOK ? "daemon up" : "daemon down")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
                Spacer()
                if let err = vm.errorMessage {
                    Text(err).font(.data(10)).foregroundStyle(KTheme.counter)
                }
                Text("cells \(board.liveCells)/\(board.cellCap)")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
            } else if let err = vm.errorMessage {
                Text(err).font(.data(11)).foregroundStyle(KTheme.counter)
            } else {
                Text("loading…").font(.data(11)).foregroundStyle(KTheme.text2)
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 7)
        .background(KTheme.surface)
        .overlay(alignment: .bottom) {
            Rectangle().fill(KTheme.hairline).frame(height: 1)
        }
    }

    // Always-visible table-of-contents strip: one compact cell per column so
    // the whole board's shape is readable without swiping the pager. Tapping
    // a cell jumps the pager there; the chevron opens the full breakdown.
    private var overviewStrip: some View {
        HStack(spacing: 0) {
            ForEach(BoardColumn.allCases) { column in
                overviewCell(column)
            }
            Button {
                showingOverview = true
            } label: {
                Image(systemName: "chevron.down")
            }
            .buttonStyle(.plain)
            .foregroundStyle(KTheme.text2)
            .padding(.leading, 6)
            .padding(.trailing, 10)
        }
        .padding(.leading, 6)
        .padding(.vertical, 4)
        .background(KTheme.surface)
        .overlay(alignment: .bottom) {
            Rectangle().fill(KTheme.hairline).frame(height: 1)
        }
    }

    private func overviewCell(_ column: BoardColumn) -> some View {
        let count = vm.jobs(in: column).count
        let flagged = vm.hasFlag(in: column)
        let isCurrent = selectedColumn == column
        return Button {
            selectedColumn = column
        } label: {
            VStack(spacing: 2) {
                HStack(spacing: 3) {
                    Text(column.shortTitle)
                        .font(.data(9, weight: .semibold))
                        .kerning(0.5)
                        .foregroundStyle(isCurrent ? KTheme.accent : KTheme.text2)
                        .lineLimit(1)
                        .minimumScaleFactor(0.8)
                    if flagged {
                        Rectangle().fill(KTheme.counter).frame(width: 5, height: 5)
                    }
                }
                Text("\(count)")
                    .font(.data(13, weight: .semibold))
                    .foregroundStyle(count > 0 ? KTheme.accent : KTheme.text2)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 4)
            .overlay(alignment: .bottom) {
                if isCurrent {
                    Rectangle().fill(KTheme.accent).frame(height: 2)
                }
            }
        }
        .buttonStyle(.plain)
    }

    private func columnView(_ column: BoardColumn) -> some View {
        let jobs = vm.jobs(in: column)
        return ScrollView {
            VStack(spacing: 0) {
                HStack(alignment: .lastTextBaseline) {
                    Text(column.title)
                        .kLabelStyle(size: 10)
                    Spacer()
                    Text("\(jobs.count)")
                        .font(.data(10))
                        .foregroundStyle(jobs.isEmpty ? KTheme.text2 : KTheme.accent)
                }
                .padding(.horizontal)
                .padding(.top)
                .padding(.bottom, 6)
                .overlay(alignment: .bottom) {
                    Rectangle().fill(KTheme.hairline).frame(height: 1)
                }
                .padding(.horizontal)

                if jobs.isEmpty {
                    Text("·")
                        .font(.data(11))
                        .foregroundStyle(KTheme.text2.opacity(0.5))
                        .padding(.top, 40)
                } else {
                    LazyVStack(spacing: 10) {
                        ForEach(jobs) { job in
                            JobCardView(job: job)
                                .onTapGesture { selectedJob = job }
                        }
                    }
                    .padding()
                }
            }
        }
        .background(KTheme.ground)
        .refreshable { await vm.refresh(baseURL: serverURL) }
    }
}

private struct JobCardView: View {
    let job: WebJob

    private var hasPendingQuestion: Bool { job.question != nil }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Text(job.title)
                    .font(.data(13, weight: .bold))
                    .foregroundStyle(KTheme.text)
                if job.archived == true, job.title != job.slug {
                    Text("as \(job.slug)")
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                }
                Spacer()
                if hasPendingQuestion {
                    Image(systemName: "flag.fill")
                        .foregroundStyle(KTheme.counter)
                        .font(.voice(10))
                }
            }
            Text("\(job.type) · \(job.repo)")
                .font(.data(11))
                .foregroundStyle(KTheme.text2)
            HStack(spacing: 6) {
                Text(job.state)
                    .font(.data(10))
                    .foregroundStyle(BoardTheme.color(forState: job.state))
                if job.live, let cell = job.cell {
                    Text("· \(cell)").font(.data(10)).foregroundStyle(KTheme.text2)
                }
            }
            if let health = job.health, !health.isEmpty {
                Text("⚠ " + health).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            if let err = job.err, !err.isEmpty {
                Text("✕ " + err).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            if let roster = job.roster, !roster.isEmpty {
                VStack(alignment: .leading, spacing: 1) {
                    ForEach(Array(roster.enumerated()), id: \.offset) { _, agent in
                        Text("\(agent.name) (\(agent.model))")
                            .font(.data(9))
                            .foregroundStyle(KTheme.text2)
                    }
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardBackground(stateColor: BoardTheme.color(forState: job.state), flagged: hasPendingQuestion)
    }
}

/// The "table of contents" reached from the overview strip's chevron: all
/// six columns as sections, every job as a compact row, tapping one opens
/// JobDetail. Reads `vm` (the shared @Observable board view model) directly
/// rather than a snapshot, so it stays in sync as JobDetail actions refresh
/// the board underneath it — no separate fetch of its own.
private struct BoardOverviewSheet: View {
    let vm: BoardViewModel
    let baseURL: String
    var onUpdate: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(AppRouter.self) private var router
    @State private var selectedJob: WebJob?

    var body: some View {
        List {
            ForEach(BoardColumn.allCases) { column in
                let jobs = vm.jobs(in: column)
                kSection(column.title) {
                    if jobs.isEmpty {
                        Text("empty")
                            .font(.voice(12))
                            .foregroundStyle(KTheme.text2.opacity(0.5))
                    } else {
                        ForEach(jobs) { job in
                            Button {
                                selectedJob = job
                            } label: {
                                overviewRow(job)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
        }
        .listStyle(.plain)
        .kListStyle()
        .navigationTitle("Board Overview")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton { dismiss() }
            }
        }
        .sheet(item: $selectedJob) { job in
            NavigationStack {
                JobDetailView(job: job, baseURL: baseURL, onUpdate: onUpdate)
            }
        }
        // JobDetailView's Cell row switches tabs and dismisses itself, but
        // that only closes the innermost sheet — this sheet (its presenter)
        // has to close itself too, or it's left covering the Sessions tab.
        .onChange(of: router.selectedTab) {
            dismiss()
        }
    }

    private func overviewRow(_ job: WebJob) -> some View {
        let flagged = job.question != nil || !(job.health ?? "").isEmpty
        return HStack(spacing: 8) {
            Rectangle()
                .fill(BoardTheme.color(forState: job.state))
                .frame(width: 6, height: 6)
            Text(job.title)
                .font(.data(13))
                .foregroundStyle(KTheme.text)
            Spacer()
            if flagged {
                Image(systemName: "flag.fill")
                    .foregroundStyle(KTheme.counter)
                    .font(.voice(10))
            }
            Text(job.state)
                .font(.data(10))
                .foregroundStyle(KTheme.text2)
        }
    }
}
