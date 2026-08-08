import SwiftUI

// LogScreen.swift — the unified event log (R2 §2.2;
// `web/_ui/src/hooks/useLogPaging.ts` + `components/log/LogView.tsx`).
//
// Newest at the bottom, reverse infinite scroll: the pagination unit is a log
// FILE (in store mode a virtual token — "events", then "events:<id>"), and
// /api/logs hands back one page plus a `prev` cursor naming the page before it.
// The client walks that cursor backwards. `chain` holds names oldest→newest so
// the newest page can be re-fetched in place on every tick while older pages
// stay frozen.

// MARK: - paging

/// One loaded page.
struct LogFileData {
    var entries: [LogEntry]
    /// The page before this one; empty at the start of history.
    var prev: String
}

enum LogPaging {
    /// The board tick's cadence, which is what the log rides.
    static let pollInterval: TimeInterval = 3
    /// Within this many points of the bottom counts as "watching the tail".
    static let tailFollowPx: CGFloat = 40
    /// Scrolling above this many points from the top pulls the previous page.
    static let olderTriggerPx: CGFloat = 40

    /// Did the newest page actually change?
    ///
    /// Count ALONE is not enough, and that is a fixed bug rather than
    /// belt-and-braces. Store mode's virtual "events" name never rotates
    /// (unlike a file-mode log file, which gets a fresh name — and therefore an
    /// immediate "unseen file" hit — on every rotation). Once the store holds
    /// more than one page of events, the count pins at the page size FOREVER
    /// while the window keeps sliding: oldest entries drop off the top as
    /// newest ones enter the tail. A count-only comparison reads that as
    /// "unchanged" and the view freezes.
    static func changed(
        prevCount: Int, prevLast: String?, nextCount: Int, nextLast: String?
    ) -> Bool {
        if prevCount != nextCount { return true }
        return prevLast != nextLast
    }

    /// The identity of one entry, for the comparison above — the whole row,
    /// since two events can share a millisecond.
    static func signature(_ entry: LogEntry?) -> String? {
        guard let entry else { return nil }
        return "\(entry.ts)|\(entry.src)|\(entry.level)|\(entry.event)|\(entry.msg)"
    }
}

@MainActor
final class LogModel: ObservableObject {
    /// File names, oldest → newest; rendered in this order.
    @Published private(set) var chain: [String] = []
    @Published private(set) var data: [String: LogFileData] = [:]
    /// What the next commit wants the scroll to do.
    @Published var pending: PendingScroll?

    enum PendingScroll: Equatable {
        /// Follow the tail.
        case bottom
        /// Keep this row where it was — the id that was FIRST before the
        /// prepend.
        case preserve(String)
    }

    /// The head of the chain still has a prev, so more history exists.
    var hasMore: Bool {
        guard let head = chain.first else { return false }
        return !(data[head]?.prev ?? "").isEmpty
    }

    /// Emptiness is measured in ENTRIES, not in loaded files: a fresh
    /// kraken.db returns a page with zero rows, and counting FILES would draw
    /// an `events` divider with nothing under it and never the empty message.
    /// A lone divider is not a log.
    var totalEntries: Int {
        chain.reduce(0) { $0 + (data[$1]?.entries.count ?? 0) }
    }

    /// The row id of the first entry currently rendered — the anchor a prepend
    /// preserves.
    var firstRowID: String? {
        for name in chain where !(data[name]?.entries.isEmpty ?? true) {
            return LogModel.rowID(file: name, index: 0)
        }
        return nil
    }

    static func rowID(file: String, index: Int) -> String { "\(file)#\(index)" }

    private var poller: Poller?
    private var loadingOlder = false
    /// Written by the view before each commit, since only the view can measure
    /// the scroller.
    var readerIsAtBottom = true

    init() {
        poller = Poller(interval: LogPaging.pollInterval) { [weak self] in
            await self?.refresh()
        }
    }

    func start() { poller?.start() }
    func stop() { poller?.pause() }

    /// Re-fetches the NEWEST page in place.
    func refresh() async {
        guard let page = try? await APIClient.shared.fetchLog(baseURL: MacAPI.baseURL) else {
            return  // the chrome's daemon stat already reports an unreachable server
        }
        guard !page.file.isEmpty else { return }
        let firstLoad = chain.isEmpty
        let entries = page.entries
        let prevEntries = data[page.file]?.entries
        let didChange = LogPaging.changed(
            prevCount: prevEntries?.count ?? -1,
            prevLast: LogPaging.signature(prevEntries?.last),
            nextCount: entries.count,
            nextLast: LogPaging.signature(entries.last)
        )
        if didChange || firstLoad {
            // Tail-follow ONLY for a reader already looking at the newest
            // lines; anyone else keeps their place.
            pending = (firstLoad || readerIsAtBottom) ? .bottom : nil
        }
        if !chain.contains(page.file) { chain.append(page.file) }
        data[page.file] = LogFileData(entries: entries, prev: page.prev)
    }

    /// Pulls the page before the head of the chain.
    func loadOlder() async {
        guard let head = chain.first else { return }
        let prev = data[head]?.prev ?? ""
        // Already loaded, already loading, or at the start of history.
        guard !prev.isEmpty, !loadingOlder, data[prev] == nil else { return }
        loadingOlder = true
        defer { loadingOlder = false }
        guard let page = try? await APIClient.shared.fetchLog(baseURL: MacAPI.baseURL, file: prev),
              !page.file.isEmpty, data[page.file] == nil
        else { return }
        // Never tail-follow on a prepend: the reader is at the top by
        // definition, and that is the position being preserved.
        let anchor = firstRowID
        chain.insert(page.file, at: 0)
        data[page.file] = LogFileData(entries: page.entries, prev: page.prev)
        if let anchor { pending = .preserve(anchor) }
    }
}

// MARK: - the view

struct LogScreen: View {
    /// Whether LOG is the visible tab. The view stays MOUNTED either way (the
    /// loaded chain and the scroll position are the whole reason), but the poll
    /// runs only while it is showing.
    var visible: Bool = true

    @StateObject private var model = LogModel()
    @Environment(\.hudTheme) private var theme
    @State private var metrics = LogScrollMetrics()

    var body: some View {
        GeometryReader { outer in
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        if model.hasMore {
                            Text("· scroll up for older ·")
                                .hudMono(size: 9)
                                .foregroundStyle(theme.mutedColor)
                                .frame(maxWidth: .infinity)
                                .padding(.vertical, 8)
                        }
                        if model.totalEntries == 0 {
                            Text("no log entries yet")
                                .hudMono(size: 11)
                                .foregroundStyle(theme.mutedColor)
                                .frame(maxWidth: .infinity)
                                .padding(.vertical, 24)
                        } else {
                            ForEach(model.chain, id: \.self) { name in
                                Text(name)
                                    .hudMono(size: 9, tracking: HudTracking.stat)
                                    .foregroundStyle(theme.mutedColor)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.vertical, 4)
                                    .padding(.horizontal, 10)
                                    .background(theme.chipBgColor)
                                let entries = model.data[name]?.entries ?? []
                                ForEach(Array(entries.enumerated()), id: \.offset) { index, entry in
                                    LogRow(entry: entry)
                                        .id(LogModel.rowID(file: name, index: index))
                                }
                            }
                        }
                        Color.clear.frame(height: 1).id(Self.tailID)
                    }
                    .background {
                        GeometryReader { inner in
                            Color.clear.preference(
                                key: LogScrollKey.self,
                                value: LogScrollMetrics(
                                    top: -inner.frame(in: .named(Self.space)).minY,
                                    contentHeight: inner.size.height,
                                    viewportHeight: outer.size.height
                                )
                            )
                        }
                    }
                }
                .coordinateSpace(name: Self.space)
                .onPreferenceChange(LogScrollKey.self) { next in
                    metrics = next
                    model.readerIsAtBottom = next.isAtBottom
                    if next.top < LogPaging.olderTriggerPx, model.hasMore {
                        Task { await model.loadOlder() }
                    }
                }
                .onChange(of: model.pending) { _, pending in
                    guard let pending else { return }
                    model.pending = nil
                    switch pending {
                    case .bottom:
                        proxy.scrollTo(Self.tailID, anchor: .bottom)
                    case .preserve(let id):
                        // The web preserves the offset arithmetically
                        // (`prevTop + (nextHeight - prevHeight)`) because it can
                        // write `scrollTop` directly. SwiftUI has no such
                        // setter, so the same intent is expressed by re-pinning
                        // the row that WAS at the top — which is exact at the
                        // moment a prepend fires, since the trigger only fires
                        // within 40pt of the top.
                        proxy.scrollTo(id, anchor: .top)
                    }
                }
            }
        }
        .background(theme.groundColor)
        .onAppear { if visible { model.start() } }
        .onDisappear { model.stop() }
        .onChange(of: visible) { _, showing in
            if showing { model.start() } else { model.stop() }
        }
    }

    private static let space = "log-scroll"
    private static let tailID = "log-tail"
}

struct LogScrollMetrics: Equatable {
    var top: CGFloat = 0
    var contentHeight: CGFloat = 0
    var viewportHeight: CGFloat = 0

    var isAtBottom: Bool {
        contentHeight - top - viewportHeight < LogPaging.tailFollowPx
    }
}

private struct LogScrollKey: PreferenceKey {
    static let defaultValue = LogScrollMetrics()
    static func reduce(value: inout LogScrollMetrics, nextValue: () -> LogScrollMetrics) {
        value = nextValue()
    }
}

// MARK: - one row

/// `MM/DD HH:MM:SS` local · src · event · msg, the level riding the colour.
struct LogRow: View {
    let entry: LogEntry
    @Environment(\.hudTheme) private var theme

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Text(LogRow.time(entry.ts))
                .foregroundStyle(theme.mutedColor)
                .frame(width: 108, alignment: .leading)
            Text(entry.src)
                .foregroundStyle(theme.mutedColor)
                .frame(width: 78, alignment: .leading)
            Text(entry.event)
                .foregroundStyle(eventColor)
                .frame(width: 132, alignment: .leading)
            Text(entry.msg)
                .foregroundStyle(msgColor)
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
        }
        .hudMono(size: 10)
        .lineLimit(3)
        .padding(.horizontal, 10)
        .padding(.vertical, 2)
    }

    private var eventColor: Color {
        entry.level == "error" ? theme.counterColor : theme.tintColor
    }

    private var msgColor: Color {
        switch entry.level {
        case "error": return theme.counterColor
        case "warn": return theme.warnColor
        default: return theme.bodyColor
        }
    }

    /// `ts` is unix MILLIS (`internal/util/eventlog.go`), so no ×1000 anywhere.
    /// A missing stamp renders BLANK rather than as a date: the obvious port
    /// (`ts ?? 0`) prints a perfectly plausible 1970 timestamp instead, and a
    /// log line whose time is quietly wrong is worse than one that admits it
    /// has none.
    static func time(_ ts: Int64) -> String {
        guard ts != 0 else { return "" }
        let f = DateFormatter()
        f.dateFormat = "MM/dd HH:mm:ss"
        return f.string(from: Date(timeIntervalSince1970: Double(ts) / 1000))
    }
}
