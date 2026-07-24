import SwiftUI
import Observation
import Foundation
import UniformTypeIdentifiers

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
    // The presented job's `state` as of whenever it was (re-)assigned to
    // selectedJob — the baseline the staleness check below diffs the next
    // polling refresh against. WebJob isn't Equatable (Decodable-only), so
    // this is captured explicitly at every assignment site rather than via
    // .onChange(of: selectedJob).
    @State private var presentedJobState: String?
    @State private var showingNewJob = false
    @State private var showingOverview = false
    @State private var selectedColumn: BoardColumn = .draft

    // MARK: - drag reorder / cross-column drag state
    //
    // A single drag session (started by JobCardView.onDrag in one column's
    // list) can be dropped either back into that same list — a within-column
    // reorder — or onto the always-visible overview strip above the pager —
    // a cross-column lifecycle action. Both paths read/write this shared
    // state; see columnView, overviewCell, ReorderDropDelegate,
    // OverviewDropDelegate below.

    // Optimistic local top-to-bottom slug order per column, applied over the
    // server's own order while a drag is in flight or just after it commits.
    // Cleared once the next board poll lands (see clearSettledReorders) so
    // the server's own order takes back over without a visible flicker.
    @State private var localOrder: [BoardColumn: [String]] = [:]
    // Columns whose localOrder overlay should be dropped as soon as the next
    // poll's board data arrives (the commit succeeded — the server now
    // agrees, so the overlay has served its purpose).
    @State private var pendingReorderColumns: Set<BoardColumn> = []
    // Columns whose reorder POST is still in flight — their overlay is
    // neither settled nor abandoned, so clearSettledReorders leaves it be.
    @State private var inFlightReorderColumns: Set<BoardColumn> = []
    // The slug/column of the card currently mid-drag, shared across both the
    // in-column and overview-strip drop targets.
    @State private var draggingSlug: String?
    @State private var draggingSourceColumn: BoardColumn?
    // Which overview-strip cell is currently hovered by a legal drop, for
    // highlighting.
    @State private var hoveredDropColumn: BoardColumn?
    // Sheets presented for cross-column drops that need user input rather
    // than a bare API call (blocked→working, done→working).
    @State private var dragSteerJob: WebJob?
    @State private var dragReactivateJob: WebJob?

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
                clearSettledReorders()
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(4))
                    if Task.isCancelled { break }
                    await vm.refresh(baseURL: serverURL)
                    clearSettledReorders()
                }
            }
            .sheet(item: $selectedJob) { job in
                NavigationStack {
                    JobDetailView(job: job, baseURL: serverURL) {
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            // Auto-dismiss the presented job sheet once the board's own
            // polling refresh (vm.refresh, every 4s above) shows that job's
            // `state` has moved on since it was opened — unblocked, killed,
            // marked done, etc. from another client — so the sheet stops
            // showing stale action buttons/question/detail against a job.go
            // state that no longer exists. Nested sheets (steer/reactivate,
            // presented from within JobDetailView) tear down along with
            // their dismissed parent. DraftStore-backed drafts (e.g. a
            // half-typed steer message) are untouched — this only clears
            // presentation state, never calls draftStore.clear.
            .onChange(of: vm.board?.jobs.first(where: { $0.slug == selectedJob?.slug })?.state) { _, newState in
                guard selectedJob != nil, let presentedJobState, let newState, newState != presentedJobState else { return }
                selectedJob = nil
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
            // blocked→working overview-strip drag: reuses JobDetailView's
            // SteerView with unblockFraming — same delivery as the in-detail
            // Unblock button, just reached from the strip instead.
            .sheet(item: $dragSteerJob) { job in
                NavigationStack {
                    SteerView(job: job, baseURL: serverURL, unblockFraming: true) {
                        dragSteerJob = nil
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            // done→working overview-strip drag: reuses ReactivateView with
            // requireText, since this entry path has no separate
            // confirmation step to catch an accidental empty resume.
            .sheet(item: $dragReactivateJob) { job in
                NavigationStack {
                    ReactivateView(job: job, baseURL: serverURL, requireText: true) {
                        dragReactivateJob = nil
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
                        presentJob(job)
                        break
                    }
                    try? await Task.sleep(for: .seconds(0.5))
                }
                router.pendingJobSlug = nil
            }
        }
    }

    // Presents a job sheet and records its state as the staleness baseline
    // — see presentedJobState's doc comment and the onChange next to the
    // sheet(item:) modifier above.
    private func presentJob(_ job: WebJob) {
        selectedJob = job
        presentedJobState = job.state
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
        let isLegalTarget = draggingSourceColumn.map { isLegalCrossColumnDrop(from: $0, to: column) } ?? false
        let isHovered = hoveredDropColumn == column
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
            .background(isHovered ? KTheme.accent.opacity(0.18) : Color.clear)
            .overlay(alignment: .bottom) {
                if isCurrent {
                    Rectangle().fill(KTheme.accent).frame(height: 2)
                }
            }
            .overlay {
                if isLegalTarget {
                    Rectangle().stroke(KTheme.accent, lineWidth: isHovered ? 2 : 1)
                }
            }
        }
        .buttonStyle(.plain)
        .onDrop(of: [.text], delegate: OverviewDropDelegate(
            target: column,
            draggingSlug: $draggingSlug,
            draggingSourceColumn: $draggingSourceColumn,
            hoveredColumn: $hoveredDropColumn,
            isLegal: isLegalCrossColumnDrop,
            onDrop: performCrossColumnDrop
        ))
    }

    private func columnView(_ column: BoardColumn) -> some View {
        let jobs = orderedJobs(column)
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
                            // Archived cards aren't draggable: the server
                            // rejects a reorder of the "archived" column,
                            // and no cross-column drop has an archived
                            // source (mirrors the web board, where archived
                            // cards render without draggable at all).
                            if column == .archived {
                                JobCardView(job: job)
                                    .onTapGesture { presentJob(job) }
                            } else {
                                JobCardView(job: job)
                                    .opacity(draggingSlug == job.slug ? 0.4 : 1.0)
                                    .onTapGesture { presentJob(job) }
                                    .onDrag {
                                        draggingSlug = job.slug
                                        draggingSourceColumn = column
                                        return NSItemProvider(object: job.slug as NSString)
                                    }
                                    .onDrop(of: [.text], delegate: ReorderDropDelegate(
                                        item: job.slug,
                                        column: column,
                                        slugs: slugsBinding(for: column),
                                        draggingSlug: $draggingSlug,
                                        draggingSourceColumn: $draggingSourceColumn,
                                        onCommit: commitReorder
                                    ))
                            }
                        }
                    }
                    .padding()
                }
            }
        }
        .background(KTheme.ground)
        .refreshable {
            await vm.refresh(baseURL: serverURL)
            clearSettledReorders()
        }
    }

    // MARK: - drag reorder / cross-column drag helpers

    /// `vm.jobs(in:)` server order, overridden by `localOrder`'s optimistic
    /// slug order where one is set (mid-drag or just after a commit — see
    /// localOrder's doc comment). Jobs absent from the overlay (arrived from
    /// the server after the drag started) go on top, in server order —
    /// matching the server's own unlisted-first rule (jobs/order.go
    /// ApplyColumnOrder), so nothing jumps when the overlay hands off.
    private func orderedJobs(_ column: BoardColumn) -> [WebJob] {
        let serverJobs = vm.jobs(in: column)
        guard let overlay = localOrder[column] else { return serverJobs }
        var bySlug = Dictionary(uniqueKeysWithValues: serverJobs.map { ($0.slug, $0) })
        var listed: [WebJob] = []
        for slug in overlay {
            if let job = bySlug.removeValue(forKey: slug) { listed.append(job) }
        }
        let unlisted = serverJobs.filter { bySlug[$0.slug] != nil }
        return unlisted + listed
    }

    /// A live-editable binding over one column's slug order, seeded from the
    /// current displayed order (overlay if present, else server order) on
    /// first read. ReorderDropDelegate mutates this directly as a drag
    /// crosses other cards.
    private func slugsBinding(for column: BoardColumn) -> Binding<[String]> {
        Binding(
            get: { localOrder[column] ?? orderedJobs(column).map(\.slug) },
            set: { localOrder[column] = $0 }
        )
    }

    /// Called right after each board refresh: drops the optimistic overlay
    /// for any column whose reorder commit is settled (the poll now reflects
    /// it), and for any column whose overlay was orphaned by a drag that
    /// never dropped (cancelled/loose-ended drags get no callback, so their
    /// live-swapped order would otherwise shadow the server's forever). A
    /// column mid-drag right now — `draggingSourceColumn` — is left alone.
    private func clearSettledReorders() {
        guard !localOrder.isEmpty else { return }
        for column in localOrder.keys {
            if pendingReorderColumns.contains(column) {
                localOrder[column] = nil
                pendingReorderColumns.remove(column)
            } else if !inFlightReorderColumns.contains(column) && column != draggingSourceColumn {
                localOrder[column] = nil
            }
        }
    }

    /// Persists a within-column reorder. A no-op drop (order unchanged)
    /// skips the round-trip and just drops any overlay the drag's
    /// live-swaps left behind. The column is marked in-flight before the
    /// POST launches so a poll landing mid-flight can't mistake the
    /// overlay for an abandoned drag's; on success it graduates to
    /// pending-settle (cleared on the next poll), on error everything
    /// reverts to the server's order immediately.
    private func commitReorder(column: BoardColumn, slugs: [String]) {
        guard slugs != vm.jobs(in: column).map(\.slug) else {
            localOrder[column] = nil
            return
        }
        localOrder[column] = slugs
        inFlightReorderColumns.insert(column)
        Task {
            do {
                try await APIClient.shared.reorderJob(baseURL: serverURL, column: column.rawValue, slugs: slugs)
                inFlightReorderColumns.remove(column)
                pendingReorderColumns.insert(column)
            } catch {
                inFlightReorderColumns.remove(column)
                localOrder[column] = nil
                vm.errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    /// The legal overview-strip lifecycle drops, keyed by (source, target)
    /// column — see the slice brief for the exact action each pair maps to.
    private func isLegalCrossColumnDrop(from: BoardColumn, to: BoardColumn) -> Bool {
        switch (from, to) {
        case (.draft, .queued), (.queued, .draft), (.blocked, .done),
             (.queued, .working), (.working, .blocked), (.blocked, .working),
             (.done, .blocked), (.done, .working), (.done, .archived):
            return true
        default:
            return false
        }
    }

    /// Executes (or, for the two cases needing user input, presents a sheet
    /// for) the lifecycle action for a legal overview-strip drop. `slug`'s
    /// WebJob is looked up fresh from the board rather than threaded through
    /// the drag, since drag state only ever carried the slug.
    private func performCrossColumnDrop(from: BoardColumn, to: BoardColumn, slug: String) {
        let job = vm.board?.jobs.first { $0.slug == slug }
        switch (from, to) {
        case (.draft, .queued):
            runBoardAction { try await APIClient.shared.queueJob(baseURL: serverURL, slug: slug) }
        case (.queued, .draft):
            runBoardAction { try await APIClient.shared.dequeueJob(baseURL: serverURL, slug: slug) }
        case (.blocked, .done):
            runBoardAction { try await APIClient.shared.markDone(baseURL: serverURL, slug: slug) }
        case (.queued, .working):
            runBoardAction { try await APIClient.shared.startJob(baseURL: serverURL, slug: slug) }
        case (.working, .blocked):
            runBoardAction { try await APIClient.shared.stopJob(baseURL: serverURL, slug: slug) }
        case (.blocked, .working):
            if let job { dragSteerJob = job }
        case (.done, .blocked):
            runBoardAction { try await APIClient.shared.reactivateJob(baseURL: serverURL, slug: slug, text: "") }
        case (.done, .working):
            if let job { dragReactivateJob = job }
        case (.done, .archived):
            runBoardAction { try await APIClient.shared.archiveJob(baseURL: serverURL, slug: slug) }
        default:
            break
        }
    }

    private func runBoardAction(_ action: @escaping () async throws -> Void) {
        Task {
            do {
                try await action()
                await vm.refresh(baseURL: serverURL)
            } catch {
                vm.errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// Drives a within-column drag reorder: as the dragged card hovers over
/// another card in the same LazyVStack, live-swaps their positions in
/// `slugs` so the list visually reorders during the drag itself; the final
/// `slugs` value at drop time is what gets committed to the server.
private struct ReorderDropDelegate: DropDelegate {
    let item: String
    let column: BoardColumn
    let slugs: Binding<[String]>
    let draggingSlug: Binding<String?>
    let draggingSourceColumn: Binding<BoardColumn?>
    let onCommit: (BoardColumn, [String]) -> Void

    func dropEntered(info: DropInfo) {
        guard let dragging = draggingSlug.wrappedValue, dragging != item,
              let from = slugs.wrappedValue.firstIndex(of: dragging),
              let to = slugs.wrappedValue.firstIndex(of: item) else { return }
        if slugs.wrappedValue[to] != dragging {
            slugs.wrappedValue.move(fromOffsets: IndexSet(integer: from), toOffset: to > from ? to + 1 : to)
        }
    }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        DropProposal(operation: .move)
    }

    func performDrop(info: DropInfo) -> Bool {
        let finalOrder = slugs.wrappedValue
        draggingSlug.wrappedValue = nil
        draggingSourceColumn.wrappedValue = nil
        onCommit(column, finalOrder)
        return true
    }
}

/// Drives an overview-strip lifecycle drop: highlights the cell while a
/// legal drag hovers it, and fires the mapped action on drop. Illegal
/// targets never highlight and their drop is a no-op (returns false so the
/// card visually snaps back).
private struct OverviewDropDelegate: DropDelegate {
    let target: BoardColumn
    let draggingSlug: Binding<String?>
    let draggingSourceColumn: Binding<BoardColumn?>
    let hoveredColumn: Binding<BoardColumn?>
    let isLegal: (BoardColumn, BoardColumn) -> Bool
    let onDrop: (BoardColumn, BoardColumn, String) -> Void

    func dropEntered(info: DropInfo) {
        guard let source = draggingSourceColumn.wrappedValue, isLegal(source, target) else { return }
        hoveredColumn.wrappedValue = target
    }

    func dropExited(info: DropInfo) {
        if hoveredColumn.wrappedValue == target { hoveredColumn.wrappedValue = nil }
    }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        guard let source = draggingSourceColumn.wrappedValue, isLegal(source, target) else {
            return DropProposal(operation: .forbidden)
        }
        return DropProposal(operation: .move)
    }

    func performDrop(info: DropInfo) -> Bool {
        defer { hoveredColumn.wrappedValue = nil }
        guard let source = draggingSourceColumn.wrappedValue, let slug = draggingSlug.wrappedValue,
              isLegal(source, target) else {
            return false
        }
        draggingSlug.wrappedValue = nil
        draggingSourceColumn.wrappedValue = nil
        onDrop(source, target, slug)
        return true
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
            Text(job.repo)
                .font(.data(11))
                .foregroundStyle(KTheme.text2)
            HStack(spacing: 6) {
                Text(job.state)
                    .font(.data(10))
                    .foregroundStyle(BoardTheme.color(forState: job.state))
                if job.live, let cell = job.cell {
                    Text("· \(cell)").font(.data(10)).foregroundStyle(KTheme.text2)
                }
                if let wakeup = job.wakeup, job.state == "working" {
                    WakeupChip(wakeup: wakeup)
                }
            }
            if let wakeup = job.wakeup, !wakeup.reason.isEmpty, job.state == "working" {
                Text("↻ " + wakeup.reason)
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
                    .lineLimit(1)
            }
            if let prs = job.prs, !prs.isEmpty {
                HStack(spacing: 6) {
                    ForEach(prs, id: \.url) { pr in
                        PRChip(pr: pr)
                    }
                }
            }
            if let health = job.health, !health.isEmpty {
                Text("⚠ " + health).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            if let err = job.err, !err.isEmpty {
                Text("✕ " + err).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            CrewChipsView(zeusStatus: job.zeusStatus, roster: job.roster ?? [])
            if let roster = job.roster, !roster.isEmpty {
                VStack(alignment: .leading, spacing: 1) {
                    ForEach(Array(roster.enumerated()), id: \.offset) { _, agent in
                        HStack(spacing: 4) {
                            Text(agent.working == true ? "●" : "○")
                                .foregroundStyle(agent.working == true ? KTheme.solved : KTheme.text2)
                            Text("\(agent.name) (\(agent.model))")
                                .foregroundStyle(KTheme.text2)
                        }
                        .font(.data(9))
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
    // See BoardView's identically-named property + presentJob(_:) above —
    // same staleness-baseline pattern, duplicated here because this sheet
    // reads `vm` directly rather than owning its own selectedJob binding at
    // the BoardView level.
    @State private var presentedJobState: String?

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
                                presentedJobState = job.state
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
        // Same auto-dismiss-on-staleness as BoardView's own job sheet — this
        // sheet reads `vm` live (see its doc comment above), so the same
        // polling refresh drives it here too.
        .onChange(of: vm.board?.jobs.first(where: { $0.slug == selectedJob?.slug })?.state) { _, newState in
            guard selectedJob != nil, let presentedJobState, let newState, newState != presentedJobState else { return }
            selectedJob = nil
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
