import SwiftUI

// Chips.swift — band 2 of the card, plus the two rows that are chips by
// geometry rather than by band (the wakeup countdown and the PR row).
//
// The whole row is COUNTS, not lists. That is the rule band 2 exists to
// enforce, and it is why the artifact NAMES moved out rather than being
// capped and why the thread names followed them: a thread title is renamable
// to any length, so N of them set the width of the column they sit in. What a
// count stands for is one click away, in the drilldown the card's own click
// already opens — so nothing here is a click target of its own. On the web
// that is spelled by NOT taking the `.chip` class (which the artifact-overlay
// handler claims); here it is spelled by these being plain views with no
// gesture, so a tap falls through to the card.
//
// Every geometry below is style.css's: 9px mono, 2px/6px padding, a 1px
// border, and ONE fixed 20pt height for every token in the row — the crew
// glyphs render at 12-14px against the counts' 9px, so tokens sizing their own
// boxes sat visibly unequal (`.chips > *` :1323-1326).

// MARK: - the shared token box

/// One chip's box. `dashed` is the cold load band's only channel that survives
/// with no colour perception at all, which is why it is a parameter rather
/// than a colour choice.
struct HudChip<Content: View>: View {
    var foreground: Color
    var border: Color
    var fill: Color = .clear
    var dashed: Bool = false
    var chipOpacity: Double = 1
    @ViewBuilder var content: Content

    var body: some View {
        content
            .hudMono(size: 9)
            .foregroundStyle(foreground)
            .lineLimit(1)
            .fixedSize(horizontal: true, vertical: false)
            .padding(.horizontal, 6)
            .frame(height: 20)
            .background(fill)
            .overlay {
                Rectangle()
                    .strokeBorder(
                        border,
                        style: StrokeStyle(lineWidth: 1, dash: dashed ? [3, 2] : [])
                    )
            }
            .opacity(chipOpacity)
    }
}

// MARK: - the triage badge

/// zeus' two 1-10 estimates for a BLOCKED job, rendered as the RATIO between
/// them and nothing else — so scanning the blocked column down the page ranks
/// the jobs by what an unblock buys. The pair it came from is a detail you ask
/// for rather than one you read past on every card, so it rides the tooltip
/// (`WebJobLoad.summary(nowMillis:)`) with the estimate's AGE, which is what
/// makes a three-day-old number read as stale rather than as current.
///
/// Four bands, and hue is deliberately not the only channel separating them:
/// red/green is the common colour-blind failure and two of these four sit
/// either side of it. So `hot` fills, `warm` outlines amber, `even` takes the
/// resting look and `cold` is the row's only DASHED border.
struct LoadChipView: View {
    let load: WebJobLoad
    @Environment(\.hudTheme) private var theme

    var body: some View {
        if let roi = load.roi, let band = load.band {
            HudChip(
                foreground: foreground(band),
                border: border(band),
                fill: band == .hot ? theme.ac2.color(0.12) : .clear,
                dashed: band == .cold,
                chipOpacity: band == .cold ? 0.75 : 1
            ) {
                Text("⚡ " + LoadChipView.ratioText(roi) + "×")
            }
            .help(load.summary(nowMillis: Int64(Date().timeIntervalSince1970 * 1000)) ?? "")
        }
    }

    private func foreground(_ band: LoadBand) -> Color {
        switch band {
        case .hot: return theme.counterColor
        case .warm: return theme.warnColor
        case .even, .cold: return theme.mutedColor
        }
    }

    private func border(_ band: LoadBand) -> Color {
        switch band {
        case .hot: return theme.counterColor
        case .warm: return theme.warn.color(0.5)
        case .even, .cold: return theme.borderStrongColor
        }
    }

    /// The ratio at ONE decimal place, rounded half AWAY FROM ZERO.
    ///
    /// This duplicates `WebJobLoad`'s own `roiText`, which is `private` in the
    /// referenced `Models.swift` and so unreachable — and the public
    /// `badgeText` is not a substitute, since it appends the `2→8` pair the
    /// web card deliberately dropped. The rounding mode is the part that must
    /// not drift: `String(format: "%.1f")` rounds half to EVEN and would print
    /// 0.25 as "0.2" where web's `toFixed(1)` prints "0.3".
    static func ratioText(_ roi: Double) -> String {
        let tenths = Int((roi * 10).rounded(.toNearestOrAwayFromZero))
        return "\(tenths / 10).\(tenths % 10)"
    }
}

// MARK: - the pending-question flag

/// The one token in the counts row that is not an inventory number: it says a
/// human is being waited on, which outranks everything the job contains, so it
/// leads the row behind only the triage badge. The answerable block itself is
/// in the steer composer — which is where the card's own click already goes.
///
/// It PULSES, on the shared 2s two-phase driver (`SteppedTimer.pulse`), so it
/// and the daemon LED flip in lockstep. Reduce Motion freezes it fully visible
/// without the driver ever being retained.
struct QuestionFlagChip: View {
    @Environment(\.hudTheme) private var theme

    var body: some View {
        HudChip(
            foreground: theme.counterColor,
            border: theme.counterColor,
            fill: theme.ac2.color(0.09)
        ) {
            Text("⚑")
        }
        .hudPulse()
        .help("pending question — open the card to answer")
    }
}

// MARK: - the inventory counts

/// `⑂ N` — the card's collapsed form of the thread list. A count rather than a
/// row of names for band 2's reason; the names stay readable in the tooltip,
/// one line each, live/closed noted.
struct ThreadCountChip: View {
    let threads: [WebThread]
    @Environment(\.hudTheme) private var theme

    var body: some View {
        if !threads.isEmpty {
            HudChip(
                foreground: theme.pollingColor,
                border: HudTheme.mix(theme.polling, 14, over: theme.b1).color
            ) {
                Text("⑂ \(threads.count)")
            }
            .help(
                threads
                    .map { t in
                        let title = (t.title?.isEmpty == false) ? " · " + (t.title ?? "") : ""
                        return "\(t.tid)\(title) — \(t.isLive ? "live" : "closed")"
                    }
                    .joined(separator: "\n")
            )
        }
    }
}

/// `⚑ N` — the board's whole doc-question surface: the OPEN count, and the
/// P0 highlight. An answered question is a decision rather than a standing
/// ask, and a settled job (done or archived) contributes nothing at all —
/// nobody is waiting on its answers, so flagging the board for them would
/// point at something the board does not show.
struct DocQuestionChip: View {
    let open: [WebDocQuestion]
    @Environment(\.hudTheme) private var theme

    var body: some View {
        if !open.isEmpty {
            let p0 = open.filter { $0.importance == "P0" }.count
            HudChip(
                foreground: p0 > 0 ? theme.counterColor : theme.mutedColor,
                border: p0 > 0 ? theme.counterColor : theme.borderStrongColor
            ) {
                Text("⚑ \(open.count)")
            }
            .help(
                "\(open.count) question\(open.count == 1 ? "" : "s") open"
                    + (p0 > 0 ? " (\(p0) P0)" : "")
                    + " — click the card to read and answer"
            )
        }
    }

    /// The open (unanswered) doc questions of a job that is not settled.
    static func openQuestions(_ job: WebJob) -> [WebDocQuestion] {
        guard !BoardRules.settled(job) else { return [] }
        return (job.docQuestions ?? []).filter { !$0.isAnswered }
    }
}

/// `▤ N` — every artifact the job has, as a number. Muted always: unlike an
/// open question, a pile of artifacts is never something owed.
struct ArtifactCountChip: View {
    let count: Int
    @Environment(\.hudTheme) private var theme

    var body: some View {
        if count > 0 {
            HudChip(foreground: theme.mutedColor, border: theme.borderStrongColor) {
                Text("▤ \(count)")
            }
            .help("\(count) artifact\(count == 1 ? "" : "s") — click the card to open them")
        }
    }
}

/// `✎ N` — the feedback notes on the job.
///
/// It carries NO emphasis state, which is where it stops being parallel to the
/// question chip. `addressed` is the nightly dream pass' column alone — an
/// ordinary cell acts on its feedback and leaves it empty by design — so an
/// un-addressed count is a corroboration backlog rather than work owed, it is
/// non-zero on nearly every job carrying feedback, and an emphasis that fires
/// on nearly everything is one nobody reads. There is likewise no settled-job
/// suppression: most notes are left ON done work, and an ARCHIVED card carries
/// this chip too.
struct FeedbackChip: View {
    let feedback: [WebFeedback]
    @Environment(\.hudTheme) private var theme

    var body: some View {
        if !feedback.isEmpty {
            let unfixed = feedback.filter { !$0.isAddressed }.count
            HudChip(foreground: theme.mutedColor, border: theme.borderStrongColor) {
                Text("✎ \(feedback.count)")
            }
            .help(
                "\(feedback.count) feedback note\(feedback.count == 1 ? "" : "s")"
                    + (unfixed > 0
                        ? ", \(unfixed) with no durable fix shipped yet (the nightly pass writes that, not the cell)"
                        : ", all have a durable fix recorded")
                    + " — click the card to read them"
            )
        }
    }
}

// MARK: - the crew row

/// The crew tokens: a zeus status pill plus one working/total chip per role
/// present in the roster.
///
/// The zeus pill is one glyph whose COLOUR is the whole status — running
/// green, polling cyan, question amber, anything else (including "waiting")
/// muted rather than echoing an arbitrary status string. The role chips lead
/// with the god's icon and carry the count; the NAMES live in the tooltip,
/// capped, because a native tooltip does not scroll and a large hephaestus
/// fleet would otherwise render a list taller than the window.
struct CrewChips: View {
    let job: WebJob
    @Environment(\.hudTheme) private var theme

    private static let roles = ["hephaestus", "athena", "hermes", "other"]
    private static let icons = ["hephaestus": "⚒︎", "athena": "⚖︎", "hermes": "✉︎", "other": "?"]
    private static let titleMax = 12

    static func visible(_ job: WebJob) -> Bool {
        (job.zeusStatus?.isEmpty == false) || !(job.roster ?? []).isEmpty
    }

    var body: some View {
        HStack(spacing: 4) {
            if let status = job.zeusStatus, !status.isEmpty {
                HudChip(
                    foreground: zeusColor(status),
                    border: zeusBorder(status),
                    fill: theme.chipBgColor
                ) {
                    Text("↯").hudMono(size: 12)
                }
                .help("zeus — " + zeusWord(status))
            }
            ForEach(roleCounts(), id: \.role) { entry in
                HudChip(
                    foreground: entry.working > 0 ? theme.okColor : theme.tintColor,
                    border: entry.working > 0 ? theme.ok.color(0.40) : theme.chipBorder,
                    fill: theme.chipBgColor
                ) {
                    HStack(spacing: 3) {
                        Text(CrewChips.icons[entry.role] ?? "?")
                            .hudMono(size: entry.role == "hephaestus" ? 12 : (entry.role == "other" ? 10 : 14))
                            .frame(width: 14)
                        Text("\(entry.working)/\(entry.total)")
                    }
                }
                .help(entry.tooltip)
            }
        }
    }

    private struct RoleCount {
        let role: String
        var working = 0
        var total = 0
        var names: [String] = []

        var tooltip: String {
            let shown = Array(names.prefix(CrewChips.titleMax))
            let rest = names.count - shown.count
            let body = rest > 0 ? shown + ["…and \(rest) more"] : shown
            return "\(role) — \(working) of \(total) working\n" + body.joined(separator: "\n")
        }
    }

    private func roleCounts() -> [RoleCount] {
        var byRole: [String: RoleCount] = [:]
        for agent in job.roster ?? [] {
            let raw = agent.role ?? ""
            let role = CrewChips.roles.contains(raw) ? raw : "other"
            var entry = byRole[role] ?? RoleCount(role: role)
            entry.total += 1
            if agent.working == true { entry.working += 1 }
            // `working` is the only Optional of the three on the referenced
            // `WebRosterAgent`; absent reads as not working, which is the same
            // degradation every other missing value on the card takes.
            entry.names.append("\(agent.working == true ? "●" : "○") \(agent.name) (\(agent.model))")
            byRole[role] = entry
        }
        return CrewChips.roles.compactMap { byRole[$0] }
    }

    private func zeusColor(_ status: String) -> Color {
        switch status {
        case "running": return theme.okColor
        case "polling": return theme.pollingColor
        case "question": return theme.warnColor
        default: return theme.tintColor
        }
    }

    private func zeusBorder(_ status: String) -> Color {
        switch status {
        case "running": return theme.ok.color(0.40)
        case "polling": return theme.polling.color(0.40)
        case "question": return theme.warn.color(0.40)
        default: return theme.chipBorder
        }
    }

    private func zeusWord(_ status: String) -> String {
        ["running", "polling", "question"].contains(status) ? status : "waiting"
    }
}

// MARK: - band 3's countdown, and band 5

/// A live cell's poll-timer chip: a countdown to its next ScheduleWakeup tick,
/// at MINUTE precision — a wakeup is a poll timer measured in minutes, so an
/// hours unit would round every value on the board to `1h`. The long form,
/// seconds included, is the tooltip.
struct WakeupChip: View {
    let wakeup: WebWakeup
    @Environment(\.hudTheme) private var theme

    var body: some View {
        HudChip(foreground: theme.tintColor, border: theme.chipBorder, fill: theme.chipBgColor) {
            Text("⏱ " + WakeupChip.short(wakeup.firesAt))
        }
        .help("next check in " + WakeupChip.long(wakeup.firesAt))
    }

    static func short(_ firesAt: Int64) -> String {
        let remaining = Int(firesAt - Int64(Date().timeIntervalSince1970 * 1000)) / 1000
        if remaining <= 0 { return "due" }
        if remaining < 60 { return "\(remaining)s" }
        return "\(remaining / 60)m"
    }

    static func long(_ firesAt: Int64) -> String {
        let remaining = Int(firesAt - Int64(Date().timeIntervalSince1970 * 1000)) / 1000
        if remaining <= 0 { return "due" }
        if remaining < 60 { return "\(remaining)s" }
        return "\(remaining / 60)m \(remaining % 60)s"
    }
}

/// One chip per PR shipped from the job's branch — band 5, deliberately its
/// own row rather than folded into band 2: a PR is the job's OUTPUT, the one
/// chip that leaves the app entirely, so it sits low beside the actions.
///
/// The label is `#<N>` parsed from the trailing `/pull/<N>`, and the STATE is
/// said by colour alone. The scheme check is load-bearing rather than a
/// formality: `prs:` frontmatter is hand-editable, so a non-http(s) url
/// degrades to a plain span that still says which PR it is.
struct PRChipsRow: View {
    let prs: [JobPR]
    @Environment(\.hudTheme) private var theme

    var body: some View {
        if !prs.isEmpty {
            HStack(spacing: 4) {
                ForEach(Array(prs.enumerated()), id: \.offset) { _, pr in
                    if let url = PRChipsRow.linkURL(pr) {
                        Link(destination: url) { chip(pr) }
                            .buttonStyle(.plain)
                            .hudCursor(.actable)
                    } else {
                        chip(pr)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func chip(_ pr: JobPR) -> some View {
        HudChip(
            foreground: color(pr),
            border: borderColor(pr),
            chipOpacity: pr.state == "closed" ? 0.8 : 1
        ) {
            Text(PRChipsRow.label(pr))
        }
    }

    static func label(_ pr: JobPR) -> String {
        // The number, when the url carries one — `#9129` is the half you
        // cannot infer from anything else on the card.
        if let range = pr.url.range(of: #"/pull/(\d+)"#, options: .regularExpression) {
            let digits = pr.url[range].dropFirst("/pull/".count)
            if !digits.isEmpty { return "#" + digits }
        }
        return pr.state
    }

    static func linkURL(_ pr: JobPR) -> URL? {
        let lower = pr.url.lowercased()
        guard lower.hasPrefix("http://") || lower.hasPrefix("https://") else { return nil }
        return URL(string: pr.url)
    }

    private func color(_ pr: JobPR) -> Color {
        switch pr.state {
        case "merged": return theme.tintColor
        case "open": return theme.okColor
        default: return theme.mutedColor
        }
    }

    private func borderColor(_ pr: JobPR) -> Color {
        switch pr.state {
        case "merged": return theme.ac.color(0.45)
        case "open": return theme.ok.color(0.45)
        default: return theme.borderStrongColor
        }
    }
}

// MARK: - shared card rules

/// The handful of per-job predicates the card and its chips both ask. They sit
/// here rather than inside a view for the reason `ChipRules` sits in
/// Moves.swift: each is asked twice — once to decide whether a row exists at
/// all, once by the thing inside it.
enum BoardRules {
    /// Done or archived. A settled job's doc questions are not visible on the
    /// board, so flagging the board for them would point at nothing.
    static func settled(_ job: WebJob) -> Bool {
        job.archived == true || job.state == "done"
    }

    /// A health line the retry action can actually act on. The two excluded
    /// texts are the ones retry cannot fix.
    static func retryable(_ job: WebJob) -> Bool {
        guard let health = job.health, !health.isEmpty else { return false }
        return !health.contains("is gone") && !health.contains("startup prompt")
    }

    /// The compact age of a board stamp. `WebJob.mtime` is unix SECONDS on
    /// this route (`internal/api/board.go` builds it with `.Unix()`, unlike
    /// the editor endpoints' millis), and a zero stamp renders as nothing
    /// rather than as an age since the epoch.
    static func age(seconds stamp: Int64) -> String {
        guard stamp > 0 else { return "" }
        let now = Int64(Date().timeIntervalSince1970)
        return compactAgeLabel(secondsElapsed: Int(max(0, now - stamp)))
    }
}
