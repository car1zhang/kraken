import AppKit
import SwiftUI

// SteerInspector.swift — message zeus. ONE inspector, THREE modes, which is
// why the mode is a discriminator on almost everything below rather than three
// views: the composer, the draft sync, the attachment flow, the context block,
// the spec drawer and the close paths are identical in all three, and only
// five things differ.
//
//   mode         title suffix                        endpoint          text
//   ----------   ---------------------------------   ---------------   --------
//   steer        "— message zeus"                    /api/steer        required
//   unblock      "— unblock"                         /api/steer        required
//   reactivate   "— reactivate (same zeus session)"   /api/reactivate   optional*
//
// * unless `requireText`, which ONLY a done→working drag sets. The card's and
//   the editor's ↺ buttons open reactivate without it.
//
// Reopening for another job (or flipping steer→unblock) must be a fresh start
// — text, chips, note, spec drawer, the lot. On the web that falls out of
// keying the body on `slug + mode`; here the host builds this view from
// `OverlayRouter.steerTarget`, which is cleared on close, and the `.id()` below
// makes a mode flip a new identity rather than a mutated one.

struct SteerInspector: View {
    let slug: String
    let mode: SteerMode
    var requireText: Bool = false

    @EnvironmentObject private var board: BoardStore
    @EnvironmentObject private var router: OverlayRouter
    @Environment(\.hudTheme) private var theme

    @State private var text = ""
    @State private var note = ""
    @State private var sending = false
    @State private var busyHead = false
    @State private var specOpen = false
    @State private var form: JobForm?

    @StateObject private var handle = ComposerHandle()
    @StateObject private var uploader: AttachmentUploader

    /// The debounced upstream draft. Built in `onAppear` rather than as a
    /// `@StateObject` because its save closure needs the slug, and because the
    /// five verbs it exposes are called imperatively from the send/kill/done
    /// paths — there is nothing here for SwiftUI to observe.
    @State private var draft: ComposerDraft?

    @State private var baselineState: String?
    @State private var captured = false
    @State private var pasteClaim: UUID?

    init(slug: String, mode: SteerMode, requireText: Bool = false) {
        self.slug = slug
        self.mode = mode
        self.requireText = requireText
        _uploader = StateObject(wrappedValue: AttachmentUploader(slug: slug))
    }

    // MARK: - derived

    /// The job's freshest board entry. A steer target is always a LIVE job, so
    /// the bare slug is the right lookup here — the archived namespace cannot
    /// collide with it.
    private var job: WebJob? {
        board.board?.jobs.first { $0.slug == slug && $0.archived != true }
    }

    /// The board's own bucketing for one job: an unhealthy live cell displays
    /// as BLOCKED whatever its file state says. nil for a job that is not live,
    /// which every caller reads as "not steerable".
    private var liveColumn: String? {
        guard let job, job.live else { return nil }
        return (job.health?.isEmpty == false) ? "blocked" : job.state
    }

    private var titleSuffix: String {
        switch mode {
        case .steer: return " — message zeus"
        case .unblock: return " — unblock"
        case .reactivate: return " — reactivate (same zeus session)"
        }
    }

    private var placeholder: String {
        switch mode {
        case .steer:
            return "message zeus directly — steering / pivot instruction, delivered straight to its pane"
        case .unblock:
            return "tell the crew how to proceed — routed straight to the cell"
        case .reactivate:
            return "optional: what to adjust — delivered as the resume prompt to the job's original zeus session (leave empty to just wake it)"
        }
    }

    private var sendLabel: String {
        switch mode {
        case .steer: return "send ⌘⏎"
        case .unblock: return "unblock ⌘⏎"
        case .reactivate: return "reactivate ⌘⏎"
        }
    }

    /// Is an empty message a mistake in this mode?
    private var requiresText: Bool { mode != .reactivate || requireText }

    /// The card's own band-6 rule: an unhealthy cell is retryable unless the
    /// health text says the cell is beyond retrying, which a retry would only
    /// re-fail on.
    private var retryable: Bool {
        guard let health = job?.health, !health.isEmpty else { return false }
        return !health.contains("is gone") && !health.contains("startup prompt")
    }

    // MARK: - body

    var body: some View {
        VStack(spacing: 0) {
            head
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    contextBlock
                    actionsBlock
                    specDrawer
                    composer
                }
                .padding(14)
            }
        }
        .hudPanelFileDrop { uploader.add($0) }
        .onAppear { start() }
        .onDisappear { stop() }
        .onChange(of: stateToken) { _, _ in checkStale() }
        .onChange(of: text) { _, new in draft?.typed(new) }
        .id(slug + "|" + mode.rawValue)
    }

    /// What auto-close watches. A single string so one `onChange` covers both
    /// "the state moved" and "the job left the board".
    private var stateToken: String {
        job.map { $0.state } ?? "\u{0}gone"
    }

    // MARK: - head

    private var head: some View {
        HudPanelHead(slug + titleSuffix) {
            // Ungated by mode and by liveness alike: /api/job/color is its own
            // endpoint precisely so a RUNNING job can be recoloured, and the
            // board routes a live working/blocked card HERE and never to the
            // editor — so without this copy the picker would be reachable only
            // while the cell is down.
            SwatchStrip(value: job?.color) { colour in
                Task {
                    _ = await board.mutate(
                        path: "/api/job/color", body: ["slug": slug, "color": colour]
                    )
                }
            }
            Spacer(minLength: 8)
            if mode != .reactivate, job?.live == true {
                HudButton("⧉ tmux", style: .warn) { openTmux() }
                    .help("raise zeus' pane in Ghostty")
            }
            if mode != .reactivate, liveColumn == "blocked" {
                HudButton("✓ mark done", style: .ok) { markDone() }
                    .disabled(busyHead)
                    .help("mark this job done")
            }
            if mode != .reactivate, liveColumn == "working" || liveColumn == "blocked" {
                HudButton("✕ cancel job", style: .danger) { kill() }
                    .disabled(busyHead)
            }
        }
    }

    // MARK: - context

    @ViewBuilder
    private var contextBlock: some View {
        if let job {
            let artifacts = job.artifacts ?? []
            let threads = job.threads ?? []
            let prs = job.prs ?? []
            let docq = job.docQuestions ?? []
            let feedback = job.feedback ?? []
            VStack(alignment: .leading, spacing: 8) {
                // 1 — crew. reactivate targets a dead cell: no crew to show.
                if mode != .reactivate, job.zeusStatus != nil || !(job.roster ?? []).isEmpty {
                    CrewRow(job: job)
                }
                // 2 — PRs are job HISTORY rather than cell state, so they show
                // in every mode: "what already shipped" is exactly the
                // reactivation context.
                if !prs.isEmpty { PRRow(prs: prs) }
                // 3 — threads, likewise history (a closed thread outlives its
                // cell). This is the app's only mount of the ⑂ branch button.
                if !threads.isEmpty || job.live {
                    ThreadRow(slug: slug, threads: threads, canBranch: job.live)
                }
                // 4 — the cell's last words
                if let detail = job.detail, !detail.isEmpty {
                    HudMarkdown(source: detail)
                }
                // 5 — carl's last message to the cell
                if let last = job.lastSent, !last.isEmpty {
                    Text("me→zeus: " + last)
                        .hudMono(size: 10)
                        .foregroundStyle(theme.mutedColor)
                        .textSelection(.enabled)
                }
                // 6 — the load estimate, no column gate (see LoadDrill)
                if let load = job.load { LoadDrill(load: load) }
                // 7 — every artifact, no cap: the card carries a bare count and
                // a modal scrolls, so the drilldown just shows the list.
                if mode != .reactivate, !artifacts.isEmpty {
                    ArtifactChipRow(
                        artifacts: artifacts,
                        label: job.title,
                        jobKey: job.id
                    )
                }
                if !docq.isEmpty {
                    DocQuestionsPanel(slug: slug, questions: docq)
                }
                if !feedback.isEmpty {
                    FeedbackPanel(slug: slug, entries: feedback)
                }
                // last because it is ambient: nothing here is actionable
                if TokensPanel.visible(job.tokens) { TokensPanel(tokens: job.tokens) }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.bottom, 10)
            .overlay(alignment: .bottom) {
                Rectangle().fill(theme.dividerColor).frame(height: 1)
            }
        }
    }

    // MARK: - actions

    @ViewBuilder
    private var actionsBlock: some View {
        if mode != .reactivate, let job, retryable || job.question != nil {
            VStack(alignment: .leading, spacing: 8) {
                if retryable {
                    HudButton("↻ retry", style: .warn) {
                        Task { _ = await board.mutate(path: "/api/retry", body: ["slug": slug]) }
                    }
                }
                if let question = job.question {
                    QuestionBlock(slug: slug, cell: job.cell, question: question)
                        // A NEW question always mounts a fresh block: the old
                        // one may have disabled its options on a click that has
                        // since been answered in the pane.
                        .id(question.toolUseID)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    // MARK: - spec drawer

    @ViewBuilder
    private var specDrawer: some View {
        if let form {
            VStack(alignment: .leading, spacing: 6) {
                Button {
                    specOpen.toggle()
                } label: {
                    HStack(spacing: 6) {
                        Text(specOpen ? "▾" : "▸").hudMono(size: 9)
                        Text("spec").hudDisplay(size: 9, tracking: HudTracking.stat)
                        Spacer(minLength: 0)
                    }
                    .foregroundStyle(theme.mutedColor)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                if specOpen {
                    VStack(alignment: .leading, spacing: 3) {
                        specRow("repo", form.repo.isEmpty ? "—" : form.repo)
                        specRow("base", form.base.isEmpty ? "origin/main" : form.base)
                        specRow("model", (form.model ?? "").isEmpty ? "claude-fable-5" : form.model!)
                        specRow("solo", form.solo ? "✓" : "✗")
                        if !form.prereqs.isEmpty {
                            specRow(
                                "prereqs",
                                form.prereqs
                                    .map { $0.job + ($0.gated ? " gated ⚑" : "") }
                                    .joined(separator: ", ")
                            )
                        }
                        HudMarkdown(source: form.spec)
                    }
                }
            }
        }
    }

    private func specRow(_ key: String, _ value: String) -> some View {
        HStack(alignment: .top, spacing: 8) {
            Text(key)
                .hudMono(size: 9)
                .foregroundStyle(theme.mutedColor)
                .frame(width: 54, alignment: .leading)
            Text(value).hudMono(size: 9).foregroundStyle(theme.bodyColor)
            Spacer(minLength: 0)
        }
    }

    // MARK: - composer

    private var composer: some View {
        VStack(alignment: .leading, spacing: 6) {
            CommandChipRow(text: $text, handle: handle)
            ZStack(alignment: .topLeading) {
                ComposerTextView(
                    text: $text,
                    theme: theme,
                    onSubmit: send,
                    handle: handle
                )
                .frame(height: 96)
                .background(theme.surfaceColor)
                .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
                if text.isEmpty {
                    Text(placeholder)
                        .hudMono(size: 11)
                        .foregroundStyle(theme.mutedColor)
                        .padding(.horizontal, 7)
                        .padding(.vertical, 6)
                        .allowsHitTesting(false)
                }
            }
            AttachmentBar(uploader: uploader)
            HStack {
                Spacer(minLength: 0)
                HudButton(sendLabel, style: .big) { send() }
                    .disabled(sending)
            }
            if !displayNote.isEmpty {
                Text(displayNote).hudMono(size: 9).foregroundStyle(theme.counterColor)
            }
        }
    }

    /// The uploader owns its own note (an upload failure, a non-image drop) and
    /// the composer owns the send-path ones. One line shows whichever spoke
    /// last, with the send path winning — it is the one the user is waiting on.
    private var displayNote: String {
        note.isEmpty ? uploader.note : note
    }

    // MARK: - lifecycle

    private func start() {
        let target = slug
        draft = ComposerDraft(debounce: 0.6) { text in
            // fire-and-forget: the next keystroke's debounce is the retry
            Task {
                try? await APIClient.shared.postDraft(
                    baseURL: InspectorAPI.baseURL, slug: target, text: text
                )
            }
        }
        // steer OUTRANKS the editor in the ⌘V chain — if both are somehow
        // open, a paste lands in exactly one of them.
        pasteClaim = router.claimPaste(.steer) { images in uploader.add(images) }

        baselineState = job?.state
        captured = true

        Task {
            // Fetched AFTER the composer is already showing empty, and landed
            // only if nothing has been typed in the meantime.
            if let d = try? await APIClient.shared.getDraft(
                baseURL: InspectorAPI.baseURL, slug: target
            ) {
                let accepted = DraftSeed.accept(serverDraft: d.text, typedSoFar: text)
                if accepted != text { text = accepted }
            }
        }
        // reactivate skips the spec drawer: /api/job/form describes the cell
        // that WOULD run, and reactivate targets one that is already gone.
        guard mode != .reactivate else { return }
        Task {
            if let d = try? await APIClient.shared.fetchJobForm(
                baseURL: InspectorAPI.baseURL, slug: target
            ), d.err.isEmpty, !d.spec.isEmpty {
                form = d
            }
        }
    }

    private func stop() {
        if let id = pasteClaim { router.releasePaste(id) }
        pasteClaim = nil
    }

    /// Close when the job moves to a different state than it had at open time:
    /// the card behind the inspector already reflects the new state, so an
    /// "unblock" composer sitting on a job that unblocked itself is only
    /// confusing. A job that leaves the board entirely closes it too.
    ///
    /// Deliberately asymmetric with the thread overlay, which closes ONLY on
    /// disappearance — a thread outlives working→blocked→done.
    private func checkStale() {
        guard captured else {
            baselineState = job?.state
            captured = true
            return
        }
        if job == nil || job?.state != baselineState { router.close(.steer) }
    }

    // MARK: - actions

    private func send() {
        guard !sending else { return }
        if uploader.pending > 0 {
            note = "uploading image…"
            return
        }
        let raw = text.trimmingCharacters(in: .whitespacesAndNewlines)
        // an images-only steer (no typed text) is still a legitimate send: in
        // this composer the CHIPS are the message, composed at send time
        let lines = uploader.imageLines
        let body = lines.isEmpty ? raw : (raw.isEmpty ? lines : raw + "\n\n" + lines)
        if body.isEmpty, requiresText {
            note = "message is empty"
            return
        }
        sending = true
        // cancel FIRST — a debounce firing mid-send would land after the
        // server's own draft clear and resurrect the message just sent
        draft?.willSend()
        Task {
            let path = mode == .reactivate ? "/api/reactivate" : "/api/steer"
            let err = await board.mutate(path: path, body: ["slug": slug, "text": body])
            sending = false
            if err == nil {
                // again: a keystroke during the in-flight send scheduled a
                // fresh save the pre-send cancel never saw
                draft?.didSend()
                router.close(.steer)
            } else {
                note = "send failed — see status bar"
            }
        }
    }

    private func kill() {
        let alert = NSAlert()
        alert.messageText = "kill \(slug)?"
        alert.informativeText =
            "the cell is torn down (artifacts preserved) and the job returns to draft."
        alert.addButton(withTitle: "kill")
        alert.addButton(withTitle: "cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        busyHead = true
        Task {
            let err = await board.mutate(path: "/api/kill", body: ["slug": slug])
            busyHead = false
            if err == nil {
                // kill does NOT clear the draft server-side — LAND it rather
                // than lose it. Synchronous, because the close on the next line
                // is what tears the debounce down.
                draft?.jobKilled()
                router.close(.steer)
            } else {
                note = "cancel failed — see status bar"
            }
        }
    }

    private func markDone() {
        busyHead = true
        Task {
            let err = await board.mutate(path: "/api/done", body: ["slug": slug])
            busyHead = false
            if err == nil {
                draft?.jobDone()  // done clears the draft server-side too
                router.close(.steer)
            }
        }
    }

    private func openTmux() {
        Task {
            do {
                if let focusError = try await InspectorAPI.openTmux(slug: slug) {
                    router.toast("couldn't focus the window: " + focusError)
                }
            } catch {
                router.toast("✕ " + error.localizedDescription)
            }
            await board.refetch()
        }
    }
}

// MARK: - context rows

/// The crew tokens: a zeus status pill whose COLOUR is the whole indicator,
/// plus one working/total chip per subagent role present.
///
/// The role's name lives in the tooltip rather than in the chip, and "other"
/// takes a plain `?` rather than a god glyph — an unknown role is a question,
/// not a deity.
private struct CrewRow: View {
    @Environment(\.hudTheme) private var theme
    let job: WebJob

    private static let roles = ["hephaestus", "athena", "hermes", "other"]
    private static let icons: [String: String] = [
        "hephaestus": "⚒︎", "athena": "⚖︎", "hermes": "✉︎", "other": "?",
    ]
    /// A native tooltip does not scroll, so a large hephaestus fleet would
    /// otherwise render a name list taller than the window with no way to see
    /// its end.
    private static let titleMax = 12

    var body: some View {
        HStack(spacing: 6) {
            if let status = job.zeusStatus {
                Text("↯")
                    .hudMono(size: 11, weight: .semibold)
                    .foregroundStyle(zeusColour(status))
                    .help("zeus — " + status)
            }
            ForEach(Self.roles, id: \.self) { role in
                if let count = counts[role] { chip(role, count) }
            }
            Spacer(minLength: 0)
        }
    }

    private func zeusColour(_ status: String) -> Color {
        switch status {
        case "running": return theme.okColor
        case "polling": return theme.pollingColor
        case "question": return theme.warnColor
        default: return theme.mutedColor
        }
    }

    private struct RoleCount {
        var working = 0
        var total = 0
        var names: [String] = []
    }

    private var counts: [String: RoleCount] {
        var out: [String: RoleCount] = [:]
        for agent in job.roster ?? [] {
            let role = Self.roles.contains(agent.role ?? "") ? (agent.role ?? "other") : "other"
            var c = out[role] ?? RoleCount()
            c.total += 1
            if agent.working == true { c.working += 1 }
            // `?? ""` per FIELD, not on the joined string: composing first
            // would make this the one place that prints "undefined".
            c.names.append("\((agent.working == true) ? "●" : "○") \(agent.name) (\(agent.model))")
            out[role] = c
        }
        return out
    }

    private func chip(_ role: String, _ c: RoleCount) -> some View {
        let shown = Array(c.names.prefix(Self.titleMax))
        let rest = c.names.count - shown.count
        let body = rest > 0 ? shown + ["…and \(rest) more"] : shown
        return HStack(spacing: 4) {
            Text(Self.icons[role] ?? "?").hudMono(size: 9)
            Text("\(c.working)/\(c.total)").hudMono(size: 9)
        }
        .foregroundStyle(c.working > 0 ? theme.tintColor : theme.mutedColor)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .background(theme.chipBgColor)
        .overlay { Rectangle().strokeBorder(theme.chipBorder, lineWidth: 1) }
        .help("\(role) — \(c.working) of \(c.total) working\n" + body.joined(separator: "\n"))
    }
}

/// One chip per PR shipped from the job's branch.
///
/// The state is said by COLOUR alone; the NUMBER is the half you cannot infer
/// from anything else. The scheme check is load-bearing rather than a
/// formality — `prs:` frontmatter is hand-editable, and a non-http(s) url
/// degrades to a plain unclickable chip that still says which PR it is.
private struct PRRow: View {
    @Environment(\.hudTheme) private var theme
    let prs: [JobPR]

    var body: some View {
        HStack(spacing: 6) {
            ForEach(Array(prs.enumerated()), id: \.offset) { _, pr in
                let label = Self.label(pr)
                let colour = Self.colour(pr, theme)
                if let url = URL(string: pr.url), pr.url.lowercased().hasPrefix("http") {
                    Link(destination: url) { chip(label, colour) }
                } else {
                    chip(label, colour)
                }
            }
            Spacer(minLength: 0)
        }
    }

    private func chip(_ label: String, _ colour: Color) -> some View {
        Text(label)
            .hudMono(size: 9, weight: .medium)
            .foregroundStyle(colour)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(theme.chipBgColor)
            .overlay { Rectangle().strokeBorder(colour.opacity(0.45), lineWidth: 1) }
    }

    static func label(_ pr: JobPR) -> String {
        guard let range = pr.url.range(of: #"/pull/\d+"#, options: .regularExpression) else {
            return pr.state
        }
        return "#" + pr.url[range].dropFirst("/pull/".count)
    }

    static func colour(_ pr: JobPR, _ theme: HudTheme) -> Color {
        switch pr.state {
        case "merged": return theme.tintColor
        case "open": return theme.okColor
        default: return theme.mutedColor
        }
    }
}

/// The job's side threads plus the ⑂ branch affordance — the app's only mount
/// of the latter, since the card delegates it here.
///
/// The seam carries the wire's own `"t<digits>"` tid, so a chip hands
/// `WebThread.tid` to the router UNTOUCHED — no parse, and therefore no shape
/// that can fail to route. That matters more than it looks: nil MEANS branch
/// mode, so under the earlier `Int?` seam a tid that failed to convert had to
/// be rendered inert rather than opened, or a click would have started a NEW
/// thread instead of opening the one that was clicked. With the string riding
/// through, an unrecognised tid reaches the server and the server answers —
/// which is the only place that can validate one.
private struct ThreadRow: View {
    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var router: OverlayRouter

    let slug: String
    let threads: [WebThread]
    let canBranch: Bool

    var body: some View {
        HStack(spacing: 6) {
            ForEach(threads) { t in
                let closed = !t.isLive
                let label = "⑂ " + t.tid + (t.title.map { $0.isEmpty ? "" : " · " + $0 } ?? "")
                Button {
                    router.openThread(slug: slug, tid: t.tid)
                } label: {
                    Text(label)
                        .hudMono(size: 9)
                        .foregroundStyle(closed ? theme.mutedColor : theme.pollingColor)
                        .lineLimit(1)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(theme.chipBgColor)
                        .overlay { Rectangle().strokeBorder(theme.chipBorder, lineWidth: 1) }
                }
                .buttonStyle(.plain)
                .help(closed ? "\(t.tid) — closed thread; open it to revive" : "\(t.tid) — live thread")
            }
            if canBranch {
                HudButton("⑂ branch", style: .ghost) { router.openThread(slug: slug, tid: nil) }
                    .help("branch a side thread off zeus' session to ask about the work")
            }
            Spacer(minLength: 0)
        }
    }
}

/// EVERY artifact as an openable chip — no cap and no "+N more". The card's
/// surface is a bare COUNT and a modal scrolls, so the drilldown just shows the
/// list where the card had to bound it.
///
/// `jobKey` is `WebJob.id` and NEVER a bare slug: the viewer's pager rebuilds
/// the job's ORDERED list from it, and an archived job sharing a live one's
/// slug would otherwise page through the wrong job's docs.
struct ArtifactChipRow: View {
    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var router: OverlayRouter

    let artifacts: [WebArtifact]
    let label: String
    let jobKey: String

    private let columns = [GridItem(.adaptive(minimum: 160), spacing: 6, alignment: .leading)]

    var body: some View {
        LazyVGrid(columns: columns, alignment: .leading, spacing: 6) {
            ForEach(artifacts) { artifact in
                Button {
                    router.openArtifact(url: artifact.url, jobKey: jobKey)
                } label: {
                    Text("▤ " + artifact.name)
                        .hudMono(size: 9)
                        .foregroundStyle(theme.bodyColor)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 3)
                        .background(theme.chipBgColor)
                        .overlay { Rectangle().strokeBorder(theme.chipBorder, lineWidth: 1) }
                }
                .buttonStyle(.plain)
                .help(label + "/" + artifact.name)
            }
        }
    }
}
