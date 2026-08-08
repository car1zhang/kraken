import SwiftUI

// DreamScreen.swift — the nightly corroboration loop's two surfaces side by
// side (R2 §2.3; `web/_ui/src/components/dream/{DreamView,WatchRow}.tsx` and
// `dreamText.ts`).
//
// Left: one collapsible card per night's report, newest first. Right: the open
// watchlist, where carl rules on each parked observation. Read-only on the
// left, the loop's only INBOUND channel on the right — the next run reads
// these verdicts and comments FIRST, ahead of any evidence, so a row marked
// `fine` is never proposed again and a `fix` comment becomes the specification
// the run follows. That ordering is why the comment box is the primary control
// here rather than an afterthought behind an edit toggle.
//
// The two panes poll INDEPENDENTLY, one poller each: the watchlist endpoint
// failing must not blank a report carl is mid-read, and vice versa. A failed
// tick EMPTIES ITS OWN PANE and shows the reason in place of the empty state —
// the last good payload is still in hand, but rendering it under a failure
// would claim a freshness the pane does not have.

// MARK: - report text

enum DreamText {
    /// Counts the bullets under the FIRST heading whose text matches `heading`,
    /// stopping at the next heading of any level.
    ///
    /// The report's section names are fixed by `.claude/skills/dream/SKILL.md`
    /// ("Shipped", "Judgment calls", "Watching", …) but the heading LEVEL it
    /// writes them at is not, so the match is level-agnostic. A section that
    /// spells out "None" carries no bullets and counts 0 — exactly the header a
    /// quiet night should get.
    static func sectionCount(markdown: String, heading: String) -> Int {
        let wanted = heading.trimmingCharacters(in: .whitespaces).lowercased()
        var inside = false
        var n = 0
        for line in markdown.split(separator: "\n", omittingEmptySubsequences: false) {
            if let title = headingText(String(line)) {
                if inside { break }
                inside = title.trimmingCharacters(in: .whitespaces).lowercased() == wanted
                continue
            }
            if inside, isBullet(String(line)) { n += 1 }
        }
        return n
    }

    /// `^#{1,6}\s+(.*)$`
    static func headingText(_ line: String) -> String? {
        var hashes = 0
        var rest = Substring(line)
        while let c = rest.first, c == "#", hashes < 6 {
            hashes += 1
            rest = rest.dropFirst()
        }
        guard hashes > 0, let c = rest.first, c == " " || c == "\t" else { return nil }
        return String(rest.drop(while: { $0 == " " || $0 == "\t" }))
    }

    /// `^\s*[-*]\s+\S` — a dash or asterisk bullet with real content after it.
    static func isBullet(_ line: String) -> Bool {
        var rest = Substring(line).drop(while: { $0 == " " || $0 == "\t" })
        guard let marker = rest.first, marker == "-" || marker == "*" else { return false }
        rest = rest.dropFirst()
        guard let space = rest.first, space == " " || space == "\t" else { return false }
        rest = rest.drop(while: { $0 == " " || $0 == "\t" })
        return !rest.isEmpty
    }

    /// The DISTINCT PRs a night shipped, straight out of the report body — the
    /// loop's PRs belong to no job's `prs:` list (they are kraken's own
    /// hardening, opened from the dream cell), so the report text is the only
    /// place they are recorded.
    static func prCount(_ markdown: String) -> Int {
        guard let re = try? NSRegularExpression(pattern: #"https?://\S*?/pull/\d+"#) else {
            return 0
        }
        let ns = markdown as NSString
        let matches = re.matches(in: markdown, range: NSRange(location: 0, length: ns.length))
        return Set(matches.map { ns.substring(with: $0.range) }).count
    }

    /// A watchlist timestamp shortened to a local "MM-DD". Two shapes because
    /// the row carries both: `first_seen`/`last_seen` are unix MILLIS while
    /// `commented_at` is an RFC3339 string stamped server-side.
    static func day(millis: Int64) -> String {
        guard millis != 0 else { return "" }
        let f = DateFormatter()
        f.dateFormat = "MM-dd"
        return f.string(from: Date(timeIntervalSince1970: Double(millis) / 1000))
    }

    /// The ISO prefix is already local-clock text, so it is READ rather than
    /// parsed.
    static func day(rfc3339: String?) -> String {
        guard let s = rfc3339, s.count >= 10 else { return "" }
        let parts = s.prefix(10).split(separator: "-")
        guard parts.count == 3 else { return "" }
        return "\(parts[1])-\(parts[2])"
    }
}

// MARK: - watchlist ordering

/// One row reduced to what the ordering reads.
struct WatchSortRow: Equatable {
    let id: Int
    let verdict: String?
    let lastSeen: Date
}

enum WatchSort {
    /// Everything still OPEN first (untouched and `fix` alike — both are work
    /// the loop has not finished with), then the rows ruled `fine`, dimmed at
    /// the bottom. Within each group this reproduces the server's own order
    /// (`ListWatchItems` is `last_seen DESC, id DESC`) rather than inventing a
    /// second one, with id as the tiebreak so the order is stable across polls.
    ///
    /// Any verdict other than `"fine"` — nil, `"fix"`, `""`, an unknown string
    /// from a newer server — sorts as OPEN: an unexpected value must read as
    /// "not ruled on yet" rather than quietly sinking a row nobody has judged.
    static func sort(_ rows: [WatchSortRow]) -> [WatchSortRow] {
        rows.sorted { a, b in
            let ra = a.verdict == "fine" ? 1 : 0
            let rb = b.verdict == "fine" ? 1 : 0
            if ra != rb { return ra < rb }
            if a.lastSeen != b.lastSeen { return a.lastSeen > b.lastSeen }
            return a.id > b.id
        }
    }
}

// MARK: - the model

@MainActor
final class DreamModel: ObservableObject {
    static let pollInterval: TimeInterval = 3

    @Published private(set) var reports: [WebDreamReport] = []
    @Published private(set) var watch: [WebWatchItem] = []
    @Published private(set) var reportsNote = ""
    @Published private(set) var watchNote = ""

    private var reportsPoller: Poller?
    private var watchPoller: Poller?

    init() {
        reportsPoller = Poller(interval: Self.pollInterval) { [weak self] in
            await self?.tickReports()
        }
        watchPoller = Poller(interval: Self.pollInterval) { [weak self] in
            await self?.tickWatch()
        }
    }

    func start() {
        reportsPoller?.start()
        watchPoller?.start()
    }

    func stop() {
        reportsPoller?.pause()
        watchPoller?.pause()
    }

    private func tickReports() async {
        do {
            reports = try await APIClient.shared.fetchDreamReports(baseURL: MacAPI.baseURL).reports
            reportsNote = ""
        } catch is CancellationError {
            return
        } catch {
            // The pane EMPTIES itself and prints the reason — see the header.
            reports = []
            reportsNote = "couldn't load reports: "
                + ((error as? KrakenError)?.errorDescription ?? error.localizedDescription)
        }
    }

    func tickWatch() async {
        do {
            watch = try await APIClient.shared
                .fetchWatchlist(baseURL: MacAPI.baseURL, state: "watching").watchlist
            watchNote = ""
        } catch is CancellationError {
            return
        } catch {
            watch = []
            watchNote = "couldn't load the watchlist: "
                + ((error as? KrakenError)?.errorDescription ?? error.localizedDescription)
        }
    }

    /// The sorted rows the right pane renders.
    var sortedWatch: [WebWatchItem] {
        let order = WatchSort.sort(watch.map {
            WatchSortRow(
                id: Int($0.id),
                verdict: $0.verdict,
                lastSeen: Date(timeIntervalSince1970: Double($0.lastSeen) / 1000)
            )
        })
        var byID: [Int: WebWatchItem] = [:]
        for item in watch { byID[Int(item.id)] = item }
        return order.compactMap { byID[$0.id] }
    }

    /// One POST carrying `{id, verdict, comment}`. Returns the failure sentence
    /// or nil.
    func postVerdict(id: Int64, verdict: String, comment: String) async -> String? {
        do {
            try await APIClient.shared.setWatchVerdict(
                baseURL: MacAPI.baseURL, id: id, verdict: verdict, comment: comment
            )
            // Pull the row back now rather than waiting out the tick, so the
            // button lights up the moment the server agrees.
            await tickWatch()
            return nil
        } catch {
            return (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }
}

// MARK: - the view

struct DreamScreen: View {
    /// Whether DREAM is the visible tab. Mounted either way — which report
    /// dates are expanded and any half-typed comment live here — but both polls
    /// run only while it is showing.
    var visible: Bool = true

    @StateObject private var model = DreamModel()
    @EnvironmentObject private var overlays: OverlayRouter
    @Environment(\.hudTheme) private var theme
    /// Which report dates are expanded. View state, so a poll cannot collapse
    /// what carl opened.
    @State private var expanded: Set<String> = []
    /// Seeded ONCE with the newest night, since that is the one he came to
    /// read; an explicit toggle ends the seeding for good.
    @State private var seeded = false

    var body: some View {
        HStack(alignment: .top, spacing: 0) {
            reportsPane
            Rectangle().fill(theme.dividerColor).frame(width: 1)
            watchPane
        }
        .background(theme.groundColor)
        .onAppear { if visible { model.start() } }
        .onDisappear { model.stop() }
        .onChange(of: visible) { _, showing in
            if showing { model.start() } else { model.stop() }
        }
        .onChange(of: model.reports.first?.date) { _, newest in
            guard !seeded, let newest else { return }
            seeded = true
            expanded.insert(newest)
        }
    }

    private var reportsPane: some View {
        VStack(alignment: .leading, spacing: 0) {
            paneHeader(
                "reports",
                count: model.reports.isEmpty
                    ? "" : "· \(model.reports.count) night\(model.reports.count == 1 ? "" : "s")"
            )
            ScrollView {
                VStack(alignment: .leading, spacing: 8) {
                    if model.reports.isEmpty {
                        Text(model.reportsNote.isEmpty
                             ? "no reports yet — the nightly pass writes one per night"
                             : model.reportsNote)
                            .hudMono(size: 10)
                            .foregroundStyle(model.reportsNote.isEmpty
                                             ? theme.mutedColor : theme.counterColor)
                    } else {
                        ForEach(model.reports) { report in
                            ReportCard(
                                report: report,
                                open: expanded.contains(report.date),
                                onToggle: { toggle(report.date) }
                            )
                        }
                    }
                }
                .padding(12)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private var watchPane: some View {
        let rows = model.sortedWatch
        let openCount = rows.filter { $0.verdict != "fine" }.count
        return VStack(alignment: .leading, spacing: 0) {
            paneHeader("watchlist", count: rows.isEmpty ? "" : "· \(openCount) open")
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    if rows.isEmpty {
                        Text(model.watchNote.isEmpty
                             ? "nothing on the watchlist — the loop parks observations here"
                             : model.watchNote)
                            .hudMono(size: 10)
                            .foregroundStyle(model.watchNote.isEmpty
                                             ? theme.mutedColor : theme.counterColor)
                    } else {
                        ForEach(rows) { item in
                            WatchRowView(item: item) { id, verdict, comment in
                                if let err = await model.postVerdict(
                                    id: id, verdict: verdict, comment: comment
                                ) {
                                    // The status corner alone is easy to miss
                                    // here: unlike a board mutation there is no
                                    // card behind this view to re-render, so
                                    // nothing else visibly changes.
                                    overlays.toast("verdict failed: " + err)
                                }
                            }
                        }
                    }
                }
                .padding(12)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    private func paneHeader(_ title: String, count: String) -> some View {
        HStack(spacing: 6) {
            Text(title).hudDisplay(size: 10, tracking: HudTracking.tab)
                .foregroundStyle(theme.textColor)
            Text(count).hudMono(size: 9).foregroundStyle(theme.mutedColor)
            Spacer()
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(theme.surfaceColor)
        .overlay(alignment: .bottom) {
            Rectangle().fill(theme.dividerColor).frame(height: 1)
        }
    }

    private func toggle(_ date: String) {
        seeded = true
        if expanded.contains(date) { expanded.remove(date) } else { expanded.insert(date) }
    }
}

// MARK: - one night

struct ReportCard: View {
    let report: WebDreamReport
    let open: Bool
    let onToggle: () -> Void
    @Environment(\.hudTheme) private var theme

    var body: some View {
        // Derived tags, recomputed only when the body text changes.
        let prs = DreamText.prCount(report.body)
        let watching = DreamText.sectionCount(markdown: report.body, heading: "Watching")
        let judged = judgmentCount
        let quiet = prs == 0 && watching == 0 && judged == 0
        VStack(alignment: .leading, spacing: 0) {
            Button(action: onToggle) {
                HStack(spacing: 8) {
                    Text(open ? "▾" : "▸").foregroundStyle(theme.mutedColor)
                    Text(report.date)
                        .hudMono(size: 11, weight: .semibold)
                        .foregroundStyle(theme.textColor)
                    tag(prs, one: "PR", many: "PRs", tone: theme.okColor)
                    tag(watching, one: "watching", many: "watching", tone: theme.warnColor)
                    tag(judged, one: "judgment call", many: "judgment calls", tone: skillTone)
                    Spacer(minLength: 8)
                    Text(quiet ? "nothing shipped" : report.slug)
                        .hudMono(size: 9)
                        .foregroundStyle(theme.mutedColor)
                        .lineLimit(1)
                }
                .padding(10)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .hudCursor(.actable)
            if open {
                DreamMarkdownView(markdown: report.body)
                    .padding(.horizontal, 12)
                    .padding(.bottom, 12)
            }
        }
        .background(theme.surfaceColor)
        .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
    }

    /// The web matches `/^judgment calls?\b/i`, i.e. either spelling. The
    /// frozen `sectionCount` takes ONE exact heading, so the singular and the
    /// plural are two calls — the section can only appear once, so at most one
    /// of them is non-zero.
    private var judgmentCount: Int {
        let plural = DreamText.sectionCount(markdown: report.body, heading: "Judgment calls")
        if plural > 0 { return plural }
        return DreamText.sectionCount(markdown: report.body, heading: "Judgment call")
    }

    private var skillTone: Color {
        SkillPalette.color("dream", theme.mode)?.color ?? theme.accentColor
    }

    @ViewBuilder
    private func tag(_ n: Int, one: String, many: String, tone: Color) -> some View {
        if n > 0 {
            Text("\(n) \(n == 1 ? one : many)")
                .hudMono(size: 9)
                .foregroundStyle(tone)
                .padding(.horizontal, 5)
                .padding(.vertical, 1)
                .overlay { Rectangle().strokeBorder(tone.opacity(0.4), lineWidth: 1) }
        }
    }
}

// MARK: - one watched observation

struct WatchRowView: View {
    let item: WebWatchItem
    let onVerdict: (Int64, String, String) async -> Void
    @Environment(\.hudTheme) private var theme
    @State private var comment: String
    @State private var lastServer: String
    @State private var busy = false

    init(item: WebWatchItem, onVerdict: @escaping (Int64, String, String) async -> Void) {
        self.item = item
        self.onVerdict = onVerdict
        _comment = State(initialValue: item.comment ?? "")
        _lastServer = State(initialValue: item.comment ?? "")
    }

    /// Anything unexpected reads as "not ruled on yet" rather than rendering a
    /// row whose buttons all look off while the server thinks otherwise.
    private var verdict: String {
        item.verdict == "fine" || item.verdict == "fix" ? (item.verdict ?? "") : ""
    }

    private var settled: Bool { verdict == "fine" }
    private var server: String { item.comment ?? "" }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(inlineAttributed(item.observation))
                .hudMono(size: 11)
                .foregroundStyle(theme.textColor)
                .textSelection(.enabled)
            meta
            if let fix = item.suggestedFix, !fix.isEmpty {
                Text(inlineAttributed("→ " + fix))
                    .hudMono(size: 10)
                    .foregroundStyle(theme.bodyColor)
            }
            controls
            if settled {
                if !server.isEmpty {
                    Text(inlineAttributed("you said: " + server))
                        .hudMono(size: 10)
                        .foregroundStyle(theme.mutedColor)
                }
            } else {
                TextEditor(text: $comment)
                    .hudEditor(minHeight: 44)
                    .frame(height: 52)
                    .disabled(busy)
            }
        }
        .padding(10)
        .background(theme.surfaceColor)
        .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
        .opacity(settled ? 0.6 : 1)
        // A verdict posted from this row comes back on the next poll, and a row
        // whose stored comment genuinely CHANGED must show the new text rather
        // than a stale draft. Keyed on the server value changing, so ordinary
        // typing is never clobbered by a tick.
        .onChange(of: server) { _, next in
            guard lastServer != next else { return }
            lastServer = next
            comment = next
        }
    }

    private var meta: some View {
        HStack(spacing: 6) {
            if !item.watchClass.isEmpty {
                Text(item.watchClass)
                    .hudMono(size: 9)
                    .foregroundStyle(theme.mutedColor)
                    .padding(.horizontal, 5)
                    .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
            }
            Text(seenText).hudMono(size: 9).foregroundStyle(theme.mutedColor)
            if let url = item.prURL, url.lowercased().hasPrefix("http"),
               let link = URL(string: url) {
                Link(prLabel(url), destination: link)
                    .hudMono(size: 9)
                    .foregroundStyle(theme.okColor)
            }
            Spacer()
        }
    }

    private var controls: some View {
        HStack(spacing: 8) {
            button("fine", "fine as-is",
                   help: "stop watching this — the loop never proposes it again")
            button("fix", "needs fixing",
                   help: "the next run fixes this — whatever you write below is the spec it follows")
            if let stamp = item.commentedAt, !stamp.isEmpty {
                Text(DreamText.day(rfc3339: stamp))
                    .hudMono(size: 9)
                    .foregroundStyle(theme.mutedColor)
            }
            Spacer()
        }
    }

    private func button(_ kind: String, _ label: String, help: String) -> some View {
        let on = verdict == kind
        let tone = kind == "fine" ? theme.okColor : theme.warnColor
        return Button(label) { send(kind) }
            .buttonStyle(.plain)
            .hudMono(size: 10)
            .foregroundStyle(on ? theme.accentInk : tone)
            .padding(.horizontal, 8)
            .padding(.vertical, 2)
            .background(on ? tone : Color.clear)
            .overlay { Rectangle().strokeBorder(tone, lineWidth: 1) }
            .disabled(busy)
            .help(help)
            .hudCursor(.actable)
    }

    private func send(_ kind: String) {
        // Clicking the verdict already in force CLEARS it — back to untouched,
        // which is the only way out of a mis-click on `fine`.
        let next = verdict == kind ? "" : kind
        busy = true
        Task {
            // UNCONDITIONALLY re-enabled, success included: nothing else ever
            // hands this row fresh controls. The web's vanilla ancestor only
            // re-enabled on failure and got away with it because it rebuilt the
            // whole list's markup on every success — leaning on a wholesale
            // rebuild is the one assumption this port must never make. Left
            // one-sided, a mis-click on `fine as-is` is unrecoverable without a
            // relaunch, and a `fix` row's comment — the spec the next dream run
            // follows — could never be edited again.
            defer { busy = false }
            // A settled row has no box; its comment comes off what the server
            // last stored, so re-clicking a button cannot silently erase what
            // he wrote.
            let text = (settled ? server : comment)
                .trimmingCharacters(in: .whitespacesAndNewlines)
            await onVerdict(item.id, next, text)
        }
    }

    private var seenText: String {
        let n = max(1, Int(item.sightings))
        var parts = ["\(n) sighting\(n == 1 ? "" : "s")"]
        let first = DreamText.day(millis: item.firstSeen)
        let last = DreamText.day(millis: item.lastSeen)
        if !first.isEmpty { parts.append("first " + first) }
        if !last.isEmpty, last != first { parts.append("last " + last) }
        return parts.joined(separator: " · ")
    }

    private func prLabel(_ url: String) -> String {
        guard let range = url.range(of: #"/pull/(\d+)"#, options: .regularExpression) else {
            return "PR"
        }
        return "#" + url[range].replacingOccurrences(of: "/pull/", with: "")
    }
}

// MARK: - markdown

/// A dream report's body, rendered natively through the module's own parser.
///
/// Named for this screen rather than shared: the artifact viewer (S5) renders
/// agent-authored markdown through the same HTML shell inside a WKWebView, and
/// a second general-purpose `MarkdownView` in this module would be a name two
/// slices race for.
struct DreamMarkdownView: View {
    let markdown: String
    @Environment(\.hudTheme) private var theme

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(parseMarkdownBlocks(markdown).enumerated()), id: \.offset) { _, block in
                blockView(block)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
    }

    @ViewBuilder
    private func blockView(_ block: MarkdownBlock) -> some View {
        switch block {
        case .heading(let level, let text):
            Text(inlineAttributed(text))
                .hudDisplay(size: level <= 2 ? 13 : 11, tracking: HudTracking.stat)
                .foregroundStyle(theme.textColor)
                .padding(.top, 4)
        case .paragraph(let text):
            Text(inlineAttributed(text))
                .hudMono(size: 11)
                .foregroundStyle(theme.bodyColor)
        case .list(let ordered, let items):
            VStack(alignment: .leading, spacing: 3) {
                ForEach(Array(items.enumerated()), id: \.offset) { i, item in
                    HStack(alignment: .top, spacing: 6) {
                        Text(ordered ? "\(i + 1)." : "·")
                            .hudMono(size: 11)
                            .foregroundStyle(theme.mutedColor)
                        Text(inlineAttributed(item))
                            .hudMono(size: 11)
                            .foregroundStyle(theme.bodyColor)
                    }
                }
            }
        case .codeFence(let code):
            Text(code)
                .hudMono(size: 10)
                .foregroundStyle(theme.tintColor)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
                .background(theme.chipBgColor)
        case .blockquote(let text):
            HStack(alignment: .top, spacing: 8) {
                Rectangle().fill(theme.accentEdge).frame(width: 2)
                Text(inlineAttributed(text))
                    .hudMono(size: 11)
                    .foregroundStyle(theme.mutedColor)
            }
        case .table(let header, _, let rows):
            VStack(alignment: .leading, spacing: 2) {
                tableRow(header, tone: theme.textColor)
                ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                    tableRow(row, tone: theme.bodyColor)
                }
            }
        }
    }

    private func tableRow(_ cells: [String], tone: Color) -> some View {
        HStack(alignment: .top, spacing: 10) {
            ForEach(Array(cells.enumerated()), id: \.offset) { _, cell in
                Text(inlineAttributed(cell))
                    .hudMono(size: 10)
                    .foregroundStyle(tone)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }
}
