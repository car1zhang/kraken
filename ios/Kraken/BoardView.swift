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
        } catch {
            self.errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    func jobs(in column: BoardColumn) -> [WebJob] {
        (board?.jobs ?? []).filter { BoardTheme.column(for: $0) == column }
    }
}

struct BoardView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @Environment(\.scenePhase) private var scenePhase
    @State private var vm = BoardViewModel()
    @State private var selectedJob: WebJob?
    @State private var showingNewJob = false
    @State private var selectedColumn: BoardColumn = .draft

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                daemonBar
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

    private var openEscalationCount: Int {
        (job.escalations ?? []).filter { $0.status == "open" }.count
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Text(job.slug)
                    .font(.data(13, weight: .bold))
                    .foregroundStyle(KTheme.text)
                Spacer()
                if openEscalationCount > 0 {
                    Image(systemName: "flag.fill")
                        .foregroundStyle(KTheme.counter)
                        .font(.system(size: 10))
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
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardBackground(stateColor: BoardTheme.color(forState: job.state), flagged: openEscalationCount > 0)
    }
}
