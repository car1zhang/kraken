import SwiftUI
import Foundation

// The board page's goals drawer: this week's goals with today's nested
// underneath, and a tap to tick one off.
//
// What it deliberately is NOT is the web board's GoalsPanel. That panel is
// where goals are WRITTEN — inline add rows, click-to-edit, hover delete —
// and none of that is here in v1 (carl's call): a goal is composed at the
// Mac, and the phone is for glancing at the list and checking things off.
// So the only write this file makes is the done toggle, and the only
// endpoint it knows is `POST /api/goals` with `{"op":"update",...}`.
//
// It is also a SEPARATE view struct rather than a section of BoardView's
// `body` on purpose. BoardView's body is already at the size where swiftc's
// expression type-checker has timed out on it before; a drawer built inline
// there would be paid for by everyone who touches that file afterwards.

// MARK: - grouping

/// One week goal and the day goals hanging under it. Mirrors web's
/// `WeekGroup` (web/_ui/src/lib/goals.ts) — same shape, same ordering rule,
/// so the two clients group an identical payload identically.
struct GoalGroup: Identifiable {
    let goal: Goal
    let days: [Goal]

    var id: Int64 { goal.id }
}

/// The drawer's whole view of a `goals` payload, computed once per render.
///
/// The unassigned rule is web's, restated: a day goal lands in `unassigned`
/// when it names no parent AND when it names a parent that isn't in THIS
/// payload's week list. The second half is what stops a day goal linked to
/// LAST week's goal from vanishing — its parent is not in the current week,
/// so nothing would draw it if the only home were a group.
struct GoalGrouping {
    let weeks: [GoalGroup]
    let unassigned: [Goal]
    let weekDone: Int
    let weekTotal: Int
    let dayDone: Int
    let dayTotal: Int

    init(_ goals: WebGoals) {
        let weekGoals = goals.week.filter { $0.isWeek }
        let weekIDs = Set(weekGoals.map(\.id))
        var byParent: [Int64: [Goal]] = [:]
        var loose: [Goal] = []
        for day in goals.day {
            let parent = day.parentID ?? 0
            if parent != 0 && weekIDs.contains(parent) {
                byParent[parent, default: []].append(day)
            } else {
                loose.append(day)
            }
        }
        weeks = weekGoals.map { GoalGroup(goal: $0, days: byParent[$0.id] ?? []) }
        unassigned = loose
        weekDone = weekGoals.filter(\.done).count
        weekTotal = weekGoals.count
        // Day counts cover nested and unassigned alike — "today 1/3" is a
        // count of today's goals, not of one of the two places they render.
        dayDone = goals.day.filter(\.done).count
        dayTotal = goals.day.count
    }

    var isEmpty: Bool { weeks.isEmpty && unassigned.isEmpty }

    /// The one-line summary the collapsed pill and the expanded head both
    /// carry, so collapsing never costs the count.
    var summary: String { "week \(weekDone)/\(weekTotal) · today \(dayDone)/\(dayTotal)" }
}

// MARK: - the card chip

/// Web's `goalChipText` (web/_ui/src/lib/goals.ts), restated: a goal's text
/// cut to fit one chip. Unchanged at or under `max`; otherwise the first
/// `max` characters with any trailing WHITESPACE dropped, plus a single `…`,
/// so the result is never longer than `max + 1`.
///
/// `\s+$` rather than ` +$`: the TS twin trims whitespace, and a goal cut on
/// a tab or a non-breaking space would otherwise keep it and render "word …"
/// on one client and "word…" on the other — a two-client rule that disagrees
/// on any input is not a pinned rule.
///
/// A `max` of 0 or less yields just `…` for any non-empty text (`> limit` is
/// true, the prefix is empty), which is what the TS expression does at 0; a
/// NEGATIVE limit is clamped to 0 rather than trapping, since `prefix(-1)`
/// is a runtime trap in Swift and no caller has any business passing one.
///
/// One honest divergence from the TS twin, which counts CODE POINTS: Swift
/// counts `Character`s, i.e. grapheme clusters. The two agree on ASCII,
/// PRECOMPOSED (NFC) accented letters and single-scalar emoji, and disagree
/// on any multi-scalar grapheme — a ZWJ family emoji, a flag, a skin-toned
/// emoji, and equally a DECOMPOSED accent (`e` + combining acute is two code
/// points to TS and one Character here) — where Swift counts 1 and the TS
/// side counts several. The precomposed/decomposed split matters because
/// nothing normalizes a goal on its way from the keyboard to the store, so
/// both forms genuinely arrive. Both sides are safe (each cuts on a boundary
/// its own renderer respects) and neither can exceed its own `max + 1`; the
/// strings can differ only for a goal whose first ~24 characters contain
/// such a cluster.
func goalChipText(_ text: String, max limit: Int = 24) -> String {
    guard text.count > limit else { return text }
    let cut = String(text.prefix(Swift.max(0, limit)))
    return cut.replacingOccurrences(of: "\\s+$", with: "", options: .regularExpression) + "…"
}

/// ONE goal chip on a job card — `◎` plus the truncated goal text, muted
/// when the goal is already ticked off.
///
/// One chip per goal and no count: a job may link several goals, and the
/// card draws every one of them (`JobCardView.cardGoals`, wrapped by
/// CardChipFlow) rather than one chip plus a `+N` badge — carl's call,
/// 2026-08-17. Naming the goals IS the summary here, which is the card's
/// one departure from its counts-not-lists rule. The full untruncated text
/// lives in the accessibility label rather than a tooltip — web hangs it
/// off `title=`, and a phone has no hover to reach that with.
///
/// This view always draws something, so the enclosing row must not exist
/// when there are no goals: `JobCardView.hasChipRow` therefore tests
/// `!cardGoals.isEmpty`, the very list the ForEach draws from, and nothing
/// weaker — a chip row that renders empty still makes the enclosing VStack
/// pay its gap for a band you cannot see. A linked goal always earns a
/// chip, including a done one (muted) and one whose text is empty.
struct GoalChip: View {
    let goal: WebJobGoal

    var body: some View {
        KChip(
            icon: "circle.circle",
            text: goalChipText(goal.text),
            // nil is KChip's muted rendering — the "done" state. An open
            // goal takes the theme accent, which is this app's equivalent of
            // web's `--tint`: identity-adjacent, deliberately not the
            // counter colour, because a goal is not an alarm.
            tint: goal.done ? nil : KTheme.accent
        )
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("goal: \(goal.text)\(goal.done ? ", done" : "")")
    }
}

// MARK: - the drawer

struct GoalsDrawer: View {
    /// nil on a server older than goals, which draws NOTHING — an empty
    /// drawer on such a server would claim "no goals" when the truth is
    /// "this server cannot answer".
    let goals: WebGoals?
    /// The board's own refresh. Every write is pessimistic — POST, then
    /// refetch the board and let the payload be the truth — which is the
    /// same mutate ritual the web panel uses and the reason there is no
    /// local copy of a goal's done flag anywhere in this file.
    let refresh: () async -> Void

    @AppStorage("goalsDrawerExpanded") private var expanded = true
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL

    /// Goals with a toggle in flight — one tap at a time per row, so a
    /// double tap cannot post two conflicting writes for the same id.
    @State private var inFlight: Set<Int64> = []
    /// Sticky until the next successful toggle: the board poll behind this
    /// drawer refreshes every 4s and would otherwise wipe the message long
    /// before it could be read.
    @State private var errorMessage: String?

    /// What the drawer would draw, or nil for "draw nothing at all".
    ///
    /// The emptiness test is the BUILT grouping's, not the raw payload's,
    /// and the two are genuinely different predicates: `GoalGrouping` keeps
    /// only `scope == "week"` rows as week rows, so a payload carrying a
    /// goal with a scope neither client recognizes (a newer server, a
    /// hand-edited row) is non-empty as a payload and empty as a grouping.
    /// Gating on the payload would then draw exactly the drawer `goals`'
    /// own comment above rules out — a `▾ GOALS week 0/0 · today 0/0` strip
    /// with nothing under it. The counts read 0/0 rather than 0/1 because
    /// the summary counts the FILTERED list too, which is what makes the
    /// header-only case so easy to miss: nothing in it looks wrong, there
    /// is simply a drawer for no goals. Whatever renders the rows must be
    /// what decides whether there are any.
    private var grouping: GoalGrouping? {
        guard let goals else { return nil }
        let built = GoalGrouping(goals)
        return built.isEmpty ? nil : built
    }

    var body: some View {
        if let grouping {
            drawer(grouping)
        }
    }

    private func drawer(_ grouping: GoalGrouping) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            header(grouping)
            if expanded {
                ScrollView {
                    rows(grouping)
                }
                // The board's five columns are the page; the drawer is a
                // strip at its foot and must never grow into them, however
                // many goals the week holds.
                .frame(maxHeight: 168)
                .padding(.bottom, 6)
            }
            if let errorMessage {
                Text(errorMessage)
                    .font(.data(11))
                    .foregroundStyle(KTheme.stopped)
                    .padding(.bottom, 6)
            }
        }
        .padding(.horizontal, 16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(KTheme.accentSoft)
        .overlay(alignment: .top) { Rectangle().fill(KTheme.hairline).frame(height: 1) }
        .animation(.easeInOut(duration: 0.18), value: expanded)
    }

    private func header(_ grouping: GoalGrouping) -> some View {
        Button {
            expanded.toggle()
        } label: {
            HStack(spacing: 8) {
                Image(systemName: expanded ? "chevron.down" : "chevron.right")
                    .font(.system(size: 9, weight: .semibold))
                Text("GOALS")
                    .font(.data(9, weight: .semibold))
                    .kerning(1.6)
                Spacer(minLength: 8)
                Text(grouping.summary)
                    .font(.data(9))
            }
            .foregroundStyle(KTheme.text2)
            .padding(.vertical, 8)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(expanded ? "Collapse goals" : "Expand goals")
        .accessibilityValue(grouping.summary)
    }

    @ViewBuilder
    private func rows(_ grouping: GoalGrouping) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            ForEach(grouping.weeks) { group in
                row(group.goal, nested: false)
                ForEach(group.days) { day in
                    row(day, nested: true)
                }
            }
            if !grouping.unassigned.isEmpty {
                Text("today")
                    .font(.data(9, weight: .semibold))
                    .kerning(1.4)
                    .foregroundStyle(KTheme.text2)
                    .padding(.top, grouping.weeks.isEmpty ? 0 : 6)
                    .padding(.bottom, 2)
                ForEach(grouping.unassigned) { day in
                    row(day, nested: false)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func row(_ goal: Goal, nested: Bool) -> some View {
        GoalRowView(goal: goal, nested: nested, busy: inFlight.contains(goal.id)) {
            toggle(goal)
        }
    }

    /// Pessimistic write: POST, then refetch. A failure leaves the row
    /// exactly as the server still has it — which is the honest rendering,
    /// and the reason nothing here optimistically flips `done` first.
    private func toggle(_ goal: Goal) {
        guard !inFlight.contains(goal.id) else { return }
        inFlight.insert(goal.id)
        Task {
            do {
                try await APIClient.shared.setGoalDone(baseURL: serverURL, id: goal.id, done: !goal.done)
                errorMessage = nil
                await refresh()
            } catch {
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
            inFlight.remove(goal.id)
        }
    }
}

// MARK: - one row

/// A single goal line: its toggle, its text, and (for a day goal under a
/// week goal) the `↳` that says which one it hangs under.
private struct GoalRowView: View {
    let goal: Goal
    let nested: Bool
    let busy: Bool
    let toggle: () -> Void

    var body: some View {
        Button(action: toggle) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                if nested {
                    Text("↳")
                        .font(.data(11))
                        .foregroundStyle(KTheme.text2)
                }
                Image(systemName: goal.done ? "checkmark.circle.fill" : "circle")
                    .font(.system(size: 11))
                    .foregroundStyle(goal.done ? KTheme.solved : KTheme.text2)
                Text(goal.text)
                    .font(.voice(12))
                    .strikethrough(goal.done)
                    .foregroundStyle(goal.done ? KTheme.text2 : KTheme.text)
                    .multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
            }
            .padding(.leading, nested ? 10 : 0)
            .opacity(busy ? 0.5 : 1)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(busy)
        .accessibilityLabel(goal.text)
        .accessibilityValue(goal.done ? "done" : "not done")
    }
}
