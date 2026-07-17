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
            .navigationTitle("Board")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showingNewJob = true
                    } label: {
                        Image(systemName: "plus")
                    }
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

    private var daemonBar: some View {
        HStack(spacing: 8) {
            if let board = vm.board {
                Circle()
                    .fill(board.daemonOK ? Color.green : Color.red)
                    .frame(width: 8, height: 8)
                Text(board.daemonOK ? "daemon up" : "daemon down")
                    .font(.caption)
                Spacer()
                if let err = vm.errorMessage {
                    Text(err).font(.caption).foregroundStyle(.red)
                }
                Text("cells \(board.liveCells)/\(board.cellCap)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else if let err = vm.errorMessage {
                Text(err).font(.caption).foregroundStyle(.red)
            } else {
                Text("loading…").font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 6)
    }

    private func columnView(_ column: BoardColumn) -> some View {
        let jobs = vm.jobs(in: column)
        return ScrollView {
            VStack(spacing: 0) {
                HStack {
                    Text(column.title).font(.headline)
                    Spacer()
                    Text("\(jobs.count)").font(.subheadline).foregroundStyle(.secondary)
                }
                .padding([.horizontal, .top])

                if jobs.isEmpty {
                    Text("·")
                        .foregroundStyle(.secondary)
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
            HStack {
                Text(job.slug).font(.headline)
                Spacer()
                if openEscalationCount > 0 {
                    Image(systemName: "flag.fill")
                        .foregroundStyle(.orange)
                        .font(.caption)
                }
            }
            Text("\(job.type) · \(job.repo)")
                .font(.caption)
                .foregroundStyle(.secondary)
            HStack(spacing: 4) {
                Circle().fill(BoardTheme.color(forState: job.state)).frame(width: 8, height: 8)
                Text(job.state).font(.caption).foregroundStyle(.secondary)
                if job.live, let cell = job.cell {
                    Text("· \(cell)").font(.caption2).foregroundStyle(.secondary)
                }
            }
            if let health = job.health, !health.isEmpty {
                Text("⚠ " + health).font(.caption2).foregroundStyle(.red)
            }
            if let err = job.err, !err.isEmpty {
                Text("✕ " + err).font(.caption2).foregroundStyle(.red)
            }
        }
        .padding()
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 12).fill(Color.secondary.opacity(0.12)))
    }
}
