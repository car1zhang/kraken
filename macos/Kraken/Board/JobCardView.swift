import AppKit
import SwiftUI

// JobCardView.swift — one job card, in SIX FIXED BANDS, this order, whatever
// column the card is in. Port of `web/_ui/src/components/board/JobCard.tsx`,
// band for band; the numbers are shared vocabulary with style.css and the iOS
// JobCardView, so renumbering here means renumbering there.
//
//   1 IDENTITY  the slug alone, then repo · cell · age · prereqs · STATUS.
//               Contiguous — nothing goes between the two lines. The status
//               token sits at the END of the second line, right-aligned.
//   2 CHIPS     two rows: the CREW badges, then the counts. Counts, never
//               lists — see Chips.swift.
//   3 STATUS    what is true right now, worst-first: outcome, err, health,
//               start failure, then the me→zeus/zeus pair (chronological, so
//               it reads as a transcript), then the poll countdown with its
//               reason.
//   4 NEEDS YOU EMPTY on a card. The pending AskUserQuestion's answerable
//               block used to render here and made one job's card several
//               times the height of every card around it; band 2's `⚑` flag
//               is all the board says about it. The band NUMBER is kept
//               rather than renumbering 5 and 6.
//   5 PRS       their own row. A PR is the job's OUTPUT rather than part of
//               its inventory, and the one chip that leaves the app.
//   6 ACTIONS   tmux, retry, reactivate.
//
// The card is a click target in its own right: a plain click that is not on a
// button opens something. A LIVE job whose display column is working or
// blocked opens the steer composer (mode `steer`/`unblock`); everything else
// opens the editor, keyed by jobKey so an archived job and an active one that
// reused its freed slug cannot open each other. The web board spells the
// precedence with `stopPropagation` on each control; SwiftUI spells it by a
// Button consuming its own tap before the container's gesture sees it.

struct JobCardView: View {
    let job: WebJob
    /// The column the board FILED this card under. Everything that reads a
    /// column reads this one, so the card and the drop table cannot disagree.
    let column: BoardColumn
    /// Why the search kept this card, when one is running. Only `.text`
    /// renders anything: a card kept by its NAME needs no explanation, while
    /// one kept by its status text has a name with nothing to do with what was
    /// typed, and without the marker it reads as the filter being broken.
    var matchReason: MatchReason?

    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var router: OverlayRouter
    @EnvironmentObject private var store: BoardStore
    @State private var hovering = false

    private var openDocQuestions: [WebDocQuestion] { DocQuestionChip.openQuestions(job) }
    private var threads: [WebThread] { job.threads ?? [] }
    private var artifacts: [WebArtifact] { job.artifacts ?? [] }
    private var feedback: [WebFeedback] { job.feedback ?? [] }
    private var prereqs: [WebPrereq] { job.prereqs ?? [] }

    /// The card's own reading of its column, spelled out rather than taken
    /// from `column`: the drag overlay renders this same view and passes a
    /// column of its own.
    private var displayColumn: BoardColumn {
        Bucketing.bucket(state: job.state, health: job.health, archived: false)
    }

    private var reactivatable: Bool {
        (column == .done || job.cellGone == true) && job.reactivatable == true
    }

    private var hasCrewRow: Bool { CrewChips.visible(job) }

    private var hasCountsRow: Bool {
        ChipRules.loadChipVisible(hasLoad: job.load?.roi != nil, column: column)
            || job.question != nil
            || !threads.isEmpty
            || !openDocQuestions.isEmpty
            || !artifacts.isEmpty
            || !feedback.isEmpty
    }

    private var hasActions: Bool { job.live || BoardRules.retryable(job) || reactivatable }

    /// The 3px left bar's colour.
    ///
    /// `JobPalette.swatch` answering nil is load-bearing rather than a mere
    /// absence: it is what makes an uncoloured job fall back to the 40%
    /// -transparent accent edge instead of painting a default that would claim
    /// the job had a colour. On hover that fallback goes FULL opacity — a
    /// coloured job's bar is already opaque, so only the fallback moves.
    private var barColor: Color {
        if let swatch = JobPalette.swatch(job.color ?? "", theme.mode) { return swatch }
        return hovering ? theme.accentColor : theme.accentEdge
    }

    var body: some View {
        HStack(alignment: .top, spacing: 0) {
            // The 3px left bar — the job's COLOUR CHANNEL and nothing else. It
            // carries neither state (an uncoloured job looks the same in every
            // column) nor alerts; both are legible elsewhere on the card.
            Rectangle()
                .fill(barColor)
                .frame(width: 3)
            VStack(alignment: .leading, spacing: 0) {
                identityBand
                if hasCrewRow {
                    CrewChips(job: job).padding(.top, 8)
                }
                if hasCountsRow {
                    countsRow.padding(.top, hasCrewRow ? 4 : 8)
                }
                statusBand
                if let prs = job.prs, !prs.isEmpty {
                    PRChipsRow(prs: prs).padding(.top, 6)
                }
                if hasActions {
                    actionsBand.padding(.top, 8)
                }
            }
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(theme.surfaceColor)
        .hudCut(.card, border: hovering ? theme.accentColor : theme.borderColor)
        // A card's height is its content's, never a share of the column's —
        // the left color bar is an infinitely-flexible Rectangle, and without
        // this pin any surplus height proposal reaching the card stretches the
        // bar and the card with it (seen at the manual gate as cards filling
        // the column). `horizontal: false` keeps the width flexible for the
        // column's minWidth 200 → 1fr behavior.
        .fixedSize(horizontal: false, vertical: true)
        .animation(.easeInOut(duration: 0.14), value: hovering)
        .contentShape(Rectangle())
        .onHover { hovering = $0 }
        .hudCursor(.grab)
        .onTapGesture { openDrilldown() }
    }

    // MARK: - band 1

    private var identityBand: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Text(job.slug)
                    .hudMono(size: 11, weight: .semibold)
                    .foregroundStyle(theme.textColor)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
                // A control, not a label — which is why it survived the state
                // token's move off this line.
                if column == .queued && !job.live {
                    Button {
                        Task { await store.mutate(path: "/api/dequeue", body: ["slug": job.slug]) }
                    } label: {
                        Text("✕").hudMono(size: 9).foregroundStyle(theme.mutedColor)
                    }
                    .buttonStyle(.plain)
                    .hudCursor(.actable)
                    .help("cancel — return to draft (spec is kept)")
                }
            }
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(job.repo)
                if job.live, let cell = job.cell, !cell.isEmpty { Text(cell) }
                Text(BoardRules.age(seconds: job.mtime))
                if !prereqs.isEmpty { prereqChips }
                Spacer(minLength: 0)
                // LAST in the row, always: it is right-aligned off the end of
                // the line, so anything appended after it lands outside the
                // alignment it is put there for.
                if let token = ChipRules.statusToken(column: displayColumn) {
                    Text(token).foregroundStyle(statusTokenColor)
                }
            }
            .hudMono(size: 9)
            .foregroundStyle(theme.mutedColor)
        }
    }

    private var statusTokenColor: Color {
        switch displayColumn {
        case .queued: return theme.tintColor
        case .done: return theme.okColor
        default: return theme.mutedColor
        }
    }

    private var prereqChips: some View {
        // Indexed rather than keyed by slug: `parsePrereqs` neither dedupes
        // nor rejects a repeated `- job:` line, so the slug alone is not
        // unique.
        HStack(spacing: 4) {
            ForEach(Array(prereqs.enumerated()), id: \.offset) { _, p in
                Text(p.job + (p.done ? "✓" : p.gated ? "⚑" : "✗"))
                    .foregroundStyle(p.done ? theme.okColor : p.gated ? theme.counterColor : theme.warnColor)
            }
        }
    }

    // MARK: - band 2

    private var countsRow: some View {
        HStack(spacing: 4) {
            // The ROI badge leads the row on BLOCKED cards only. It is a
            // judgement about the job rather than something the job contains,
            // and it sits here because it is read at the same first glance —
            // leftmost is what lets the badges line up down the column and be
            // compared without reading the cards around them.
            if let load = job.load, ChipRules.loadChipVisible(hasLoad: load.roi != nil, column: column) {
                LoadChipView(load: load)
            }
            if job.question != nil { QuestionFlagChip() }
            ThreadCountChip(threads: threads)
            DocQuestionChip(open: openDocQuestions)
            ArtifactCountChip(count: artifacts.count)
            FeedbackChip(feedback: feedback)
        }
    }

    // MARK: - band 3

    @ViewBuilder
    private var statusBand: some View {
        // "accepted" is the outcome of every job ticked off by hand, so the
        // line fired on nearly every DONE card and said nothing `✓ done` had
        // not. Every OTHER outcome is a departure from that default.
        if let outcome = job.outcome, !outcome.isEmpty, outcome != "accepted", column == .done {
            Text("→ " + outcome)
                .hudMono(size: 9)
                .foregroundStyle(theme.okColor)
                .padding(.top, 8)
        }
        if let err = job.err, !err.isEmpty {
            Text("✕ " + err).hudMono(size: 9).foregroundStyle(theme.counterColor).padding(.top, 8)
        }
        if let health = job.health, !health.isEmpty {
            Text("⚠ " + health).hudMono(size: 9).foregroundStyle(theme.counterColor).padding(.top, 8)
        }
        if let startError = job.startError, !startError.isEmpty {
            VStack(alignment: .leading, spacing: 3) {
                Text("START FAILED")
                    .hudDisplay(size: 9, tracking: 0.14)
                    .foregroundStyle(theme.warnColor)
                Text(startError)
                    .hudMono(size: 9)
                    .foregroundStyle(theme.bodyColor)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 6)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(theme.warn.color(0.08))
            .overlay(alignment: .leading) { Rectangle().fill(theme.warnColor).frame(width: 2) }
            .padding(.top, 8)
        }
        if let lastSent = job.lastSent, !lastSent.isEmpty {
            leftRuled(theme.borderStrongColor) {
                Text("me→zeus: " + lastSent)
                    .hudMono(size: 9)
                    .foregroundStyle(theme.mutedColor)
                    .lineLimit(1)
            }
        }
        if let detail = job.detail, !detail.isEmpty {
            leftRuled(theme.counterColor) {
                Text(detail)
                    .hudMono(size: 9)
                    .foregroundStyle(theme.bodyColor)
                    // Clamped to two lines: a card is not allowed to outgrow
                    // its column, and the full text is in the drilldown the
                    // card's own click opens.
                    .lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        // The marker sits UNDER the lines it is about, so the eye reads the
        // matched words first and then what they mean.
        if matchReason == .text {
            Text("matched in status text")
                .hudMono(size: 9, tracking: 0.06)
                .foregroundStyle(theme.warnColor)
                .padding(.horizontal, 5)
                .padding(.vertical, 1)
                .overlay { Rectangle().strokeBorder(theme.warn.color(0.45), lineWidth: 1) }
                .opacity(0.85)
                .padding(.top, 6)
        }
        if let wakeup = job.wakeup, job.state == "working" {
            WakeupChip(wakeup: wakeup).padding(.top, 4)
            if !wakeup.reason.isEmpty {
                Text("↻ " + wakeup.reason)
                    .hudMono(size: 9)
                    .foregroundStyle(theme.mutedColor)
                    .opacity(0.7)
                    .lineLimit(1)
                    .padding(.top, 4)
            }
        }
    }

    /// The card's prose treatment: a 2px rule down the left with 6px beside
    /// it, which is what marks its ONE prose surface off from the numbers.
    private func leftRuled<Content: View>(_ rule: Color, @ViewBuilder _ content: () -> Content) -> some View {
        HStack(alignment: .top, spacing: 6) {
            Rectangle().fill(rule).frame(width: 2)
            content()
            Spacer(minLength: 0)
        }
        .fixedSize(horizontal: false, vertical: true)
        .padding(.top, 8)
    }

    // MARK: - band 6

    private var actionsBand: some View {
        HStack(spacing: 6) {
            if job.live {
                cardButton("⌁ attach tmux", tone: theme.warnColor, fill: theme.warn.color(0.09)) {
                    Task { await store.mutate(path: "/api/peers/open", body: ["slug": job.slug]) }
                }
                .help("raise zeus' pane in Ghostty")
            }
            if BoardRules.retryable(job) {
                cardButton("↻ retry", tone: theme.mutedColor, fill: .clear) {
                    Task { await store.mutate(path: "/api/retry", body: ["slug": job.slug]) }
                }
            }
            if reactivatable {
                // Never a bare POST: reactivate routes into the composer, the
                // same as the done→working drag and the editor's own button.
                cardButton("↺ reactivate", tone: theme.warnColor, fill: .clear) {
                    router.openSteer(slug: job.slug, mode: .reactivate)
                }
            }
            Spacer(minLength: 0)
        }
    }

    private func cardButton(
        _ label: String, tone: Color, fill: Color, action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Text(label)
                .hudDisplay(size: 9, tracking: 0.1)
                .foregroundStyle(tone)
                .padding(.horizontal, 9)
                .padding(.vertical, 5)
                .background(fill)
                .overlay { Rectangle().strokeBorder(tone.opacity(0.5), lineWidth: 1) }
        }
        .buttonStyle(.plain)
        .hudCursor(.actable)
    }

    // MARK: - the whole-card click

    private func openDrilldown() {
        // Only a LIVE job has a steerable column, and an unhealthy one reads
        // as blocked whatever its file says.
        switch Bucketing.liveColumn(job) {
        case .working: router.openSteer(slug: job.slug, mode: .steer)
        case .blocked: router.openSteer(slug: job.slug, mode: .unblock)
        default: router.openEditor(jobKey: JobKey.key(job))
        }
    }
}

// MARK: - the archived card

/// One archived job's card, inside the drawer under DONE.
///
/// The same bands as a live card minus the ones that cannot arise: identity,
/// the two counts, the outcome line, and unarchive/delete. There is no band 5
/// — an archived job's PRs are not on the wire — and no band 4.
///
/// It is CLICKABLE, and that is load-bearing rather than incidental: its
/// artifact surface is a bare count whose entire affordance is "click the
/// card", so without the click the archive's artifacts would have no path in
/// the UI at all. The click opens the editor by JOB KEY — the synthetic
/// `archive:<slug>`, since there is no such job file — while unarchive and
/// delete carry the BARE archive name, which is what the server resolves.
struct ArchivedCardView: View {
    let job: WebJob

    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var router: OverlayRouter
    @EnvironmentObject private var store: BoardStore
    @State private var hovering = false

    private var label: String { job.displaySlug ?? job.slug }
    private var renamed: Bool { (job.displaySlug.map { !$0.isEmpty && $0 != job.slug }) ?? false }
    private var artifacts: [WebArtifact] { job.artifacts ?? [] }
    private var feedback: [WebFeedback] { job.feedback ?? [] }

    var body: some View {
        HStack(alignment: .top, spacing: 0) {
            // The colour SURVIVES archiving; the card is dimmed as a whole, so
            // the bar reads as muted without a treatment of its own.
            Rectangle()
                .fill(JobPalette.swatch(job.color ?? "", theme.mode) ?? theme.mutedColor)
                .frame(width: 3)
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(label).hudMono(size: 11, weight: .semibold).foregroundStyle(theme.textColor)
                    if renamed {
                        Text("as " + job.slug).hudMono(size: 9).foregroundStyle(theme.mutedColor)
                    }
                    Spacer(minLength: 0)
                    Text("■ archived").hudMono(size: 9, tracking: 0.06).foregroundStyle(theme.mutedColor)
                }
                HStack(spacing: 8) {
                    Text(job.repo)
                    Text(BoardRules.age(seconds: job.mtime))
                    Spacer(minLength: 0)
                }
                .hudMono(size: 9)
                .foregroundStyle(theme.mutedColor)

                if !artifacts.isEmpty || !feedback.isEmpty {
                    HStack(spacing: 4) {
                        ArtifactCountChip(count: artifacts.count)
                        FeedbackChip(feedback: feedback)
                    }
                    .padding(.top, 5)
                }
                if let outcome = job.outcome, !outcome.isEmpty {
                    Text("→ " + outcome).hudMono(size: 9).foregroundStyle(theme.okColor).padding(.top, 8)
                }
                HStack(spacing: 6) {
                    Button {
                        Task { await store.mutate(path: "/api/unarchive", body: ["slug": job.slug]) }
                    } label: { actionLabel("⬆ unarchive", theme.accentColor) }
                        .buttonStyle(.plain)
                        .hudCursor(.actable)
                    Button {
                        guard ArchivedCardView.confirmDelete(job.slug) else { return }
                        Task { await store.mutate(path: "/api/delete", body: ["slug": job.slug]) }
                    } label: { actionLabel("✕ delete forever", theme.counterColor) }
                        .buttonStyle(.plain)
                        .hudCursor(.actable)
                    Spacer(minLength: 0)
                }
                .padding(.top, 8)
            }
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(theme.surfaceColor)
        .hudCut(.card, border: hovering ? theme.accentColor : theme.borderColor)
        // A card's height is its content's, never a share of the column's —
        // the left color bar is an infinitely-flexible Rectangle, and without
        // this pin any surplus height proposal reaching the card stretches the
        // bar and the card with it (seen at the manual gate as cards filling
        // the column). `horizontal: false` keeps the width flexible for the
        // column's minWidth 200 → 1fr behavior.
        .fixedSize(horizontal: false, vertical: true)
        .animation(.easeInOut(duration: 0.14), value: hovering)
        .opacity(hovering ? 1 : 0.75)
        .contentShape(Rectangle())
        .onHover { hovering = $0 }
        .hudCursor(.actable)
        .onTapGesture { router.openEditor(jobKey: JobKey.key(job)) }
    }

    private func actionLabel(_ text: String, _ tone: Color) -> some View {
        Text(text)
            .hudDisplay(size: 9, tracking: 0.1)
            .foregroundStyle(tone)
            .padding(.horizontal, 9)
            .padding(.vertical, 5)
            .overlay { Rectangle().strokeBorder(tone.opacity(0.5), lineWidth: 1) }
    }

    /// The delete gate. `NSAlert` rather than a SwiftUI confirmation dialog
    /// because it is modal and SYNCHRONOUS — the decision is read back before
    /// the POST is even scheduled, which is the same shape `window.confirm`
    /// gives the web board.
    @MainActor
    static func confirmDelete(_ slug: String) -> Bool {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "delete \(slug) forever?"
        alert.informativeText =
            "the archived job file and its artifact copies are permanently removed."
        alert.addButton(withTitle: "Delete forever")
        alert.addButton(withTitle: "Cancel")
        return alert.runModal() == .alertFirstButtonReturn
    }
}
