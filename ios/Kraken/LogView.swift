import SwiftUI

/// Unified event log viewer — the mobile twin of the web board's LOG tab.
/// Loads the newest log file (GET /api/logs) and renders entries newest-
/// first; scrolling to the end of loaded history offers the previous
/// (older) file, one fetch per file via each response's `prev` name.
/// Pull-to-refresh re-fetches the newest file.
struct LogView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL

    // Chain of loaded files oldest→newest; the last is the live "latest".
    // Mirrors app.js's logChain/logData so the two viewers paginate the
    // same way.
    @State private var chain: [String] = []
    @State private var filesData: [String: LogReadResponse] = [:]
    @State private var loadingOlder = false
    @State private var loadError: String?

    private struct Row: Identifiable {
        let id: String
        let entry: LogEntry
    }

    // Newest first: files newest→oldest, each file's entries reversed.
    private var rows: [Row] {
        var out: [Row] = []
        for name in chain.reversed() {
            let entries = filesData[name]?.entries ?? []
            for (i, e) in entries.enumerated().reversed() {
                out.append(Row(id: "\(name)#\(i)", entry: e))
            }
        }
        return out
    }

    private var hasOlder: Bool {
        guard let first = chain.first else { return false }
        return !(filesData[first]?.prev ?? "").isEmpty
    }

    var body: some View {
        NavigationStack {
            List {
                ForEach(rows) { row in
                    LogRowView(entry: row.entry)
                        .listRowBackground(rowBackground(for: row.entry))
                        .listRowSeparator(.hidden)
                }
                if hasOlder {
                    Button {
                        Task { await loadOlder() }
                    } label: {
                        HStack {
                            Spacer()
                            if loadingOlder {
                                ProgressView().tint(KTheme.accent)
                            } else {
                                Label("Load older", systemImage: "arrow.down.circle")
                                    .font(.voice(12))
                            }
                            Spacer()
                        }
                        .padding(.vertical, 6)
                        .contentShape(Capsule())
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(KTheme.text2)
                    .glassEffect(.regular, in: Capsule())
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
                    // reaching the tail of loaded history auto-pulls the next
                    // older file, so a long scroll just keeps going
                    .onAppear { Task { await loadOlder() } }
                }
            }
            // A log is a dense stream, so the rows deliberately stay flat and
            // full-bleed over the tint field rather than becoming one card
            // each — cards would halve the lines per screen. The glass shows
            // up as the field behind them, the error rows' soft capsule, and
            // the "Load older" pill.
            .listStyle(.plain)
            .overlay { if rows.isEmpty { emptyState } }
            .kListStyle()
            .navigationTitle("Log")
            .navigationBarTitleDisplayMode(.inline)
            .task { await refreshLatest() }
            .refreshable { await refreshLatest() }
        }
    }

    /// Error lines get a soft counter-tinted rounded row so they're findable
    /// in a long scroll; everything else stays transparent over the field.
    @ViewBuilder
    private func rowBackground(for entry: LogEntry) -> some View {
        if entry.level == "error" {
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(KTheme.counterSoft.opacity(0.6))
                .padding(.horizontal, 8)
                .padding(.vertical, 1)
        } else {
            Color.clear
        }
    }

    private var emptyState: some View {
        Text(loadError ?? "no log entries yet")
            .font(.voice(12))
            .foregroundStyle(loadError == nil ? KTheme.text2 : KTheme.counter)
            .multilineTextAlignment(.center)
            .padding()
    }

    private func refreshLatest() async {
        do {
            let d = try await APIClient.shared.fetchLog(baseURL: serverURL)
            guard !d.file.isEmpty else { return }
            filesData[d.file] = d
            if !chain.contains(d.file) { chain.append(d.file) }
            loadError = nil
        } catch {
            loadError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    private func loadOlder() async {
        guard let first = chain.first, let prev = filesData[first]?.prev, !prev.isEmpty,
              !loadingOlder, filesData[prev] == nil else { return }
        loadingOlder = true
        defer { loadingOlder = false }
        do {
            let d = try await APIClient.shared.fetchLog(baseURL: serverURL, file: prev)
            guard !d.file.isEmpty else { return }
            filesData[d.file] = d
            chain.insert(d.file, at: 0)
        } catch {
            loadError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }
}

/// One log line: time + src + event badges over the message, error-tinted
/// when level == "error".
private struct LogRowView: View {
    let entry: LogEntry

    private var time: String {
        let d = Date(timeIntervalSince1970: TimeInterval(entry.ts) / 1000)
        let f = DateFormatter()
        f.dateFormat = "MM/dd HH:mm:ss"
        return f.string(from: d)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 8) {
                Text(time)
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
                Text(entry.src)
                    .font(.data(10, weight: .semibold))
                    .foregroundStyle(KTheme.accent)
                Text(entry.event)
                    .font(.data(10))
                    .foregroundStyle(entry.level == "error" ? KTheme.counter : KTheme.text2)
            }
            Text(entry.msg)
                .font(.data(12))
                .foregroundStyle(entry.level == "error" ? KTheme.counter : KTheme.text)
        }
        .padding(.vertical, 1)
    }
}
