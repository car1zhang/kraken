import SwiftUI
import Observation

// The DREAM screen: the nightly corroboration loop's two outputs, and the one
// place carl talks back to it.
//
// Reached from the Board's toolbar (the moon control) rather than a tab of its
// own. That used to be forced — five tabs was the ceiling before iOS collapses
// a TabView into a "More" list, and the bar was full. The nav restructure took
// the bar down to four (Board · Sessions · Graph · Settings), so a fifth slot
// is available and this is now a CHOICE: DREAM is a read-and-judge surface
// visited once in a while, not a peer of the four places the app lives, and
// the web board can afford a fourth view tab only because a browser has the
// width for it.
//
// It is presented as a sheet but READ as a full page, so it takes the full-page
// header treatment rather than a drawer's small centred one: the explicit
// `.navigationBarTitleDisplayMode(.inline)` is dropped and the title falls back
// to the default, which is large and left-aligned. It keeps sheet chrome (the
// close button) because it is still a sheet; see the contract's
// header-treatment paragraph.
//
// The watchlist is the only genuinely new interaction in the app: `fine`
// suppresses a row permanently, `fix` asks for it to be done — and a `fix`
// WITH a comment is the high-value path, because that comment becomes the
// specification the next dream run follows and the PR body quotes it. Hence
// the comment box is a primary control on every row, never hidden behind an
// edit toggle.

@Observable
final class DreamViewModel {
    var reports: [WebDreamReport] = []
    var watchlist: [WebWatchItem] = []
    var errorMessage: String?
    var loaded = false
    /// Ids with a verdict POST in flight — the row's controls disable so a
    /// double tap can't race two writes against one row.
    var inFlight: Set<Int64> = []

    func refresh(baseURL: String) async {
        do {
            // Two independent reads; a failure in either is one message.
            async let reportsTask = APIClient.shared.fetchDreamReports(baseURL: baseURL)
            async let watchTask = APIClient.shared.fetchWatchlist(baseURL: baseURL, state: "watching")
            let (r, w) = try await (reportsTask, watchTask)
            reports = r.reports
            watchlist = w.watchlist
            errorMessage = nil
        } catch {
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        loaded = true
    }

    /// Rows still asking for a decision: everything except the ones carl has
    /// marked `fine`. A `fix` row STAYS here — it is pending work, and its
    /// comment is the thing the next run reads.
    var open: [WebWatchItem] { watchlist.filter { ($0.verdict ?? "") != "fine" } }
    /// Marked `fine`: suppressed, dimmed, out of the way — but still listed,
    /// because "stop even watching it" should be reversible from the same
    /// screen that decided it.
    var settled: [WebWatchItem] { watchlist.filter { ($0.verdict ?? "") == "fine" } }

    /// Posts carl's judgement and mirrors it locally on success, so the row
    /// moves without waiting for a refetch. `commented_at` is server-stamped;
    /// the local copy carries the server's own convention (RFC3339) only so
    /// the row's rendering doesn't flicker between "judged" and "not" — the
    /// next refresh replaces it with the real value.
    func judge(baseURL: String, item: WebWatchItem, verdict: String, comment: String) async {
        guard !inFlight.contains(item.id) else { return }
        inFlight.insert(item.id)
        defer { inFlight.remove(item.id) }
        do {
            try await APIClient.shared.setWatchVerdict(
                baseURL: baseURL, id: item.id, verdict: verdict, comment: comment
            )
            errorMessage = nil
            guard let index = watchlist.firstIndex(where: { $0.id == item.id }) else { return }
            let old = watchlist[index]
            watchlist[index] = WebWatchItem(
                id: old.id, observation: old.observation, watchClass: old.watchClass,
                state: old.state, sightings: old.sightings, firstSeen: old.firstSeen,
                lastSeen: old.lastSeen, evidence: old.evidence, suggestedFix: old.suggestedFix,
                prURL: old.prURL, updatedAt: old.updatedAt,
                verdict: verdict, comment: comment,
                commentedAt: (verdict.isEmpty && comment.isEmpty) ? nil : old.commentedAt
            )
        } catch {
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }
}

struct DreamView: View {
    let baseURL: String

    @Environment(\.dismiss) private var dismiss
    @State private var vm = DreamViewModel()

    var body: some View {
        Form {
            kSection {
                HStack(spacing: 8) {
                    Image(systemName: "moon.stars.fill")
                        .foregroundStyle(KTheme.skillDream)
                    Text(summary)
                        .font(.data(11))
                        .foregroundStyle(KTheme.text2)
                    Spacer()
                }
            }
            if let errorMessage = vm.errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
            if !vm.open.isEmpty {
                kSection("Watchlist · \(vm.open.count) open") {
                    ForEach(vm.open) { item in
                        WatchItemCard(item: item, busy: vm.inFlight.contains(item.id)) { verdict, comment in
                            Task { await vm.judge(baseURL: baseURL, item: item, verdict: verdict, comment: comment) }
                        }
                    }
                }
            }
            if !vm.settled.isEmpty {
                kSection("Fine as-is · \(vm.settled.count)") {
                    ForEach(vm.settled) { item in
                        WatchItemCard(item: item, busy: vm.inFlight.contains(item.id)) { verdict, comment in
                            Task { await vm.judge(baseURL: baseURL, item: item, verdict: verdict, comment: comment) }
                        }
                        .opacity(0.55)
                    }
                }
            }
            if !vm.reports.isEmpty {
                kSection("Reports · \(vm.reports.count) nights") {
                    // Newest first, straight from the server's own ordering.
                    // Only the newest opens by default: a night's report is a
                    // full page, and the list is the history.
                    ForEach(Array(vm.reports.enumerated()), id: \.element.id) { index, report in
                        DreamReportCard(report: report, initiallyExpanded: index == 0)
                    }
                }
            }
            if vm.loaded && vm.reports.isEmpty && vm.watchlist.isEmpty && vm.errorMessage == nil {
                kSection {
                    Text("Nothing dreamt yet — the nightly pass creates its first report after a day with sessions or feedback in it.")
                        .font(.voice(12))
                        .foregroundStyle(KTheme.text2)
                }
            }
        }
        .kListStyle()
        .navigationTitle("Dream")
        // Full-page treatment: large, left-aligned. Sheet-hosted, but carl
        // reads DREAM as a page, not a drawer — see this file's header.
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton { dismiss() }
            }
        }
        .refreshable { await vm.refresh(baseURL: baseURL) }
        .task { await vm.refresh(baseURL: baseURL) }
    }

    private var summary: String {
        var parts: [String] = []
        if let last = vm.reports.first { parts.append("last run \(last.date)") }
        parts.append("\(vm.reports.count) nights")
        parts.append("\(vm.open.count) watching")
        return parts.joined(separator: " · ")
    }
}

/// One night's report: the date plus a couple of headline counts, expanding to
/// the report's own markdown. Reuses MarkdownText (the app's one renderer)
/// rather than a second one — the body IS markdown, and everything from the
/// "your prompting" quotes to the coverage table already renders there.
private struct DreamReportCard: View {
    let report: WebDreamReport
    let initiallyExpanded: Bool

    @State private var expanded: Bool

    init(report: WebDreamReport, initiallyExpanded: Bool) {
        self.report = report
        self.initiallyExpanded = initiallyExpanded
        _expanded = State(initialValue: initiallyExpanded)
    }

    var body: some View {
        DisclosureGroup(isExpanded: $expanded) {
            MarkdownText(markdown: report.body)
                .padding(.vertical, 4)
        } label: {
            HStack(spacing: 8) {
                Text(report.date)
                    .font(.data(12, weight: .semibold))
                    .foregroundStyle(KTheme.text)
                Spacer()
                Text("\(report.body.count) chars")
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
            }
        }
    }
}

/// One watchlist row and its verdict controls.
///
/// The three states, spelled out because they are not interchangeable:
/// UNTOUCHED — the agent keeps counting sightings. FINE — suppressed
/// permanently; distinct from "declined" (which means "you proposed this and
/// I said no"), this means "stop even watching it". FIX — carl wants it done;
/// with no comment that is an explicit instruction on its own, with one the
/// COMMENT IS THE SPECIFICATION.
private struct WatchItemCard: View {
    let item: WebWatchItem
    let busy: Bool
    /// (verdict, comment) — "" verdict clears the judgement.
    var onJudge: (String, String) -> Void

    @FocusState private var commentFocused: Bool
    @State private var comment: String
    /// The verdict as last posted, so the buttons can paint immediately and
    /// the comment box knows which verdict a save should carry.
    @State private var verdict: String

    init(item: WebWatchItem, busy: Bool, onJudge: @escaping (String, String) -> Void) {
        self.item = item
        self.busy = busy
        self.onJudge = onJudge
        _comment = State(initialValue: item.comment ?? "")
        _verdict = State(initialValue: item.verdict ?? "")
    }

    private var trimmedComment: String {
        comment.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// True once the box holds something other than what the server has.
    private var commentDirty: Bool {
        trimmedComment != (item.comment ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(item.observation)
                .font(.voice(13))
                .foregroundStyle(KTheme.text)
            HStack(spacing: 6) {
                Text(item.watchClass)
                    .font(.data(9, weight: .semibold))
                    .foregroundStyle(KTheme.text2)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(KTheme.raised, in: Capsule())
                Text(sightingsLine)
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
                Spacer()
            }
            if let fix = item.suggestedFix, !fix.isEmpty {
                Text("suggests: \(fix)")
                    .font(.voice(12))
                    .foregroundStyle(KTheme.polling)
            }
            if let pr = item.prURL, !pr.isEmpty, let url = URL(string: pr) {
                Link(pr, destination: url)
                    .font(.data(10))
                    .foregroundStyle(KTheme.solved)
            }
            HStack(spacing: 8) {
                verdictButton("fine as-is", value: "fine", color: KTheme.solved, soft: KTheme.solvedSoft)
                verdictButton("needs fixing", value: "fix", color: KTheme.revealed, soft: KTheme.revealedSoft)
                Spacer()
                if busy { ProgressView().controlSize(.small) }
            }
            // A primary control, not an afterthought: a `fix` with a comment
            // is the whole point of this screen.
            TextField("how should this be fixed? (optional)", text: $comment, axis: .vertical)
                .focused($commentFocused)
                .kField(focused: commentFocused, minHeight: 54)
            if commentDirty {
                HStack {
                    Spacer()
                    // Tinted with skillDream, not the theme accent: DREAM keeps
                    // its own hue whatever tint the app is wearing.
                    Button("Save comment") {
                        // A comment typed with no verdict yet means "fix":
                        // nobody writes an instruction for something they
                        // consider fine, and leaving it verdict-less would
                        // park a real request as an untouched row.
                        let v = verdict.isEmpty ? "fix" : verdict
                        verdict = v
                        onJudge(v, trimmedComment)
                    }
                    .font(.data(11, weight: .semibold))
                    .buttonStyle(.glassProminent)
                    .tint(KTheme.skillDream)
                    .disabled(busy)
                }
            }
            if !commentDirty, !(item.comment ?? "").isEmpty {
                Text("you said: \(item.comment ?? "")")
                    .font(.voice(11))
                    .foregroundStyle(KTheme.solved)
            }
        }
        .padding(.vertical, 4)
    }

    private var sightingsLine: String {
        let n = item.sightings
        return "\(n) sighting\(n == 1 ? "" : "s")"
    }

    private func verdictButton(_ label: String, value: String, color: Color, soft: Color) -> some View {
        let on = verdict == value
        return Button {
            // Tapping the active verdict clears it — the way back to
            // untouched, which the server spells as an empty verdict.
            let next = on ? "" : value
            verdict = next
            onJudge(next, trimmedComment)
        } label: {
            Text(label)
                .font(.data(11, weight: .semibold))
                .foregroundStyle(on ? color : KTheme.text2)
                .padding(.horizontal, 12)
                .padding(.vertical, 7)
                .background(on ? soft : Color.clear, in: Capsule())
                .overlay(Capsule().stroke(on ? color : KTheme.hairline2, lineWidth: 1))
                .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .disabled(busy)
    }
}
