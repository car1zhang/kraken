import SwiftUI

// Multiplayer kraken (apiContract 7): the job sheet's two fleet additions —
// the visibility control on a LOCAL job, and the whole detail of a REMOTE
// card (one another Mac served, carrying `owner`).

/// The three-way visibility control on a local job's sheet, saving straight
/// to `POST /api/job/visibility` on tap — the colour strip's shape: optimistic,
/// rolled back with the server's sentence on failure.
struct JobVisibilityPicker: View {
    let baseURL: String
    let slug: String
    /// What the board last said; nil (an older server) reads as the default.
    let serverValue: String?
    var onSaved: () -> Void = {}

    static let values = ["private", "readonly", "public"]

    @State private var selected = "readonly"
    @State private var seeded = false
    @State private var inFlight = 0
    @State private var errorText: String?

    private var explanation: String {
        switch selected {
        case "private": return "never leaves this Mac"
        case "public": return "anyone on the net, person or agent, may steer, answer, stop, kill, mark done, submit, archive, recolour and edit it — each action arrives tagged with who sent it."
        default: return "every board on the net sees it, nobody else can touch it"
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Picker("Visibility", selection: Binding(get: { selected }, set: { apply($0) })) {
                ForEach(Self.values, id: \.self) { Text($0).tag($0) }
            }
            .pickerStyle(.segmented)
            Text(explanation)
                .font(.voice(11))
                .foregroundStyle(KTheme.text2)
                .fixedSize(horizontal: false, vertical: true)
            if let errorText, !errorText.isEmpty {
                Text(errorText).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
        }
        .task {
            guard !seeded else { return }
            selected = Self.normalized(serverValue)
            seeded = true
        }
        .onChange(of: serverValue) { _, new in
            guard inFlight == 0 else { return }
            selected = Self.normalized(new)
        }
    }

    private static func normalized(_ v: String?) -> String {
        guard let v, values.contains(v) else { return "readonly" }
        return v
    }

    private func apply(_ value: String) {
        guard value != selected else { return }
        let previous = selected
        selected = value
        errorText = nil
        inFlight += 1
        Task {
            defer { inFlight -= 1 }
            do {
                try await APIClient.shared.setJobVisibility(baseURL: baseURL, slug: slug, visibility: value)
                onSaved()
            } catch {
                if selected == value { selected = previous }
                errorText = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// The sheet for a card another Mac owns.
///
/// Both modes show everything the local sheet shows where the card carries it
/// — detail, question, PRs, crew, threads, spec, artifacts in the real viewer,
/// doc questions, feedback, triage estimate, token spend — through the same
/// views the local sheet uses. `.readonly` stops there: no control is drawn at
/// all, not even disabled. `.publicCell` adds the controls — steer,
/// question answers, lifecycle actions, colour — each one posting
/// `POST /api/nodes/action` on the PAIRED node, which forwards it to the owner
/// tagged with who sent it. Writes only the owner may make (doc-question
/// answers, feedback, threads) stay read-only in both modes. Nothing here ever
/// calls a local job route: the slug names a job on another machine.
struct RemoteJobDetailView: View {
    let job: WebJob
    let baseURL: String
    var onUpdate: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var isBusy = false
    @State private var errorMessage: String?
    @State private var steerText = ""
    @State private var sentToast = false
    @State private var showingKillConfirm = false
    @State private var showingArtifact: WebArtifact?
    /// The spec text with its frontmatter dropped; nil until the relay
    /// answers, and for good when it refuses (no Spec section then).
    @State private var spec: String?
    @FocusState private var composerFocused: Bool

    private var owner: WebJobOwner? { job.owner }
    private var column: BoardColumn { BoardTheme.column(for: job) }
    private var isPublic: Bool { job.remoteMode == .publicCell }
    /// Whether any control may act: a public cell on a node that is up.
    private var canAct: Bool { isPublic && owner?.online == true }

    // The same rules the local sheet derives its actions from (each mirrors
    // the owning node's own 409), so a public remote card offers exactly what
    // its owner's board would.
    private var canSteer: Bool { job.live }
    private var canStop: Bool { job.live && column == .working }
    private var canKill: Bool { job.live && [BoardColumn.working, .blocked].contains(column) }
    private var canMarkDone: Bool { column == .blocked }
    private var canSubmit: Bool { column == .draft && (job.err ?? "").isEmpty }
    private var canArchive: Bool { !job.live && [BoardColumn.draft, .done].contains(column) }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                // Sections in JobDetailView's own order, each present only
                // where the card carries its data.
                header
                if let note = modeNote {
                    JobSectionCard {
                        Text(note)
                            .font(.voice(12))
                            .foregroundStyle(KTheme.revealed)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                JobTriageSection(load: job.load)
                JobTokensSection(tokens: job.tokens)
                if isPublic {
                    JobSectionCard("Color") {
                        JobColorStrip(baseURL: baseURL, slug: job.slug, serverColor: job.color, onSaved: { _ in onUpdate() },
                                      save: { name in try await send("color", NodeActionPayload(color: name)) })
                            .disabled(!canAct)
                            .opacity(canAct ? 1 : 0.45)
                    }
                }
                if let err = job.err, !err.isEmpty {
                    JobSectionCard("Error") { Text(err).font(.voice(13)).foregroundStyle(KTheme.counter) }
                }
                if let health = job.health, !health.isEmpty {
                    JobSectionCard("Health") { Text(health).font(.voice(13)).foregroundStyle(KTheme.revealed) }
                }
                if let outcome = job.outcome, !outcome.isEmpty, column == .done {
                    JobSectionCard("Outcome") { Text(outcome).font(.voice(13)).foregroundStyle(KTheme.solved) }
                }
                JobRosterSection(job: job)
                if let threads = job.threads, !threads.isEmpty {
                    JobSectionCard("Threads") { threadChips(threads) }
                }
                if let spec, !spec.isEmpty {
                    JobSectionCard {
                        DisclosureGroup {
                            DetailCard(text: spec)
                        } label: {
                            Text("Spec").kLabelStyle()
                        }
                    }
                }
                if let detail = job.detail, !detail.isEmpty {
                    JobSectionCard("Detail") { DetailCard(text: detail) }
                }
                if let lastSent = job.lastSent, !lastSent.isEmpty {
                    JobSectionCard("Last sent") {
                        Text(lastSent).font(.data(12)).foregroundStyle(KTheme.text2)
                    }
                }
                if let wakeup = job.wakeup, !wakeup.reason.isEmpty, job.state == "working" {
                    JobSectionCard("Poll timer") {
                        Text("↻ " + wakeup.reason).font(.data(12)).foregroundStyle(KTheme.text2)
                    }
                }
                if let question = job.question {
                    JobSectionCard("Question") { questionContent(question) }
                }
                if isPublic {
                    if canSteer {
                        JobSectionCard("Steer") { composer }
                    }
                    JobSectionCard("Actions") { actions }
                }
                if let docQuestions = job.docQuestions, !docQuestions.isEmpty {
                    JobSectionCard("Doc questions") {
                        ForEach(sortDocQuestions(docQuestions)) { q in
                            DocQuestionCard(item: q, readOnly: true)
                        }
                    }
                }
                if let feedback = job.feedback, !feedback.isEmpty {
                    JobSectionCard("Feedback · \(feedback.count)") {
                        ForEach(sortFeedback(feedback)) { f in
                            FeedbackRow(item: f)
                        }
                    }
                }
                if let artifacts = job.artifacts, !artifacts.isEmpty {
                    JobSectionCard("Artifacts") {
                        // The urls arrive already rewritten by the paired node
                        // to its own artifact proxy, relative to `baseURL`, so
                        // the viewer loads them exactly as it loads a local one.
                        CollapsedArtifactList(artifacts: artifacts, onOpen: { showingArtifact = $0 })
                    }
                }
                if let errorMessage {
                    JobSectionCard { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 10)
            .padding(.bottom, 20)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background(ThemeBackgroundView())
        .navigationTitle(job.title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton { dismiss() }
            }
        }
        .disabled(isBusy)
        .overlay(alignment: .top) {
            if sentToast {
                Text("sent")
                    .font(.data(11))
                    .foregroundStyle(KTheme.accentInk)
                    .padding(.horizontal, 14).padding(.vertical, 6)
                    .background(KTheme.accent, in: Capsule())
                    .padding(.top, 8)
            }
        }
        .confirmationDialog(
            "Kill \(job.slug) on \(owner?.label ?? "its Mac")? The cell is torn down and the job returns to draft.",
            isPresented: $showingKillConfirm, titleVisibility: .visible
        ) {
            Button("Kill", role: .destructive) { run("kill", dismissOnSuccess: true) }
            Button("Cancel", role: .cancel) {}
        }
        .sheet(item: $showingArtifact) { artifact in
            NavigationStack {
                // No feedback pencil: a note on another Mac's artifact is an
                // owner-only write. The proxy urls never parse as a local
                // artifact address, so the pager's save and ◆ mind controls
                // stay away too — the mind is this phone's paired Mac's.
                ArtifactPagerView(
                    artifacts: job.artifacts ?? [], selected: artifact, baseURL: baseURL,
                    feedbackEnabled: false
                )
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        SheetCloseButton { showingArtifact = nil }
                    }
                }
            }
        }
        .task(id: job.boardKey) { await loadSpec() }
    }

    /// The Spec section's text, relayed from the owner. Any failure — a 403,
    /// 404 or 502 from the relay, an offline owner — leaves `spec` nil, which
    /// draws no Spec section rather than an error in its place.
    private func loadSpec() async {
        guard let node = owner?.nodeID else { return }
        guard let raw = try? await APIClient.shared.fetchNodeJobRaw(baseURL: baseURL, node: node, slug: job.slug) else { return }
        spec = FrontmatterText.body(of: raw.content)
    }

    private var modeNote: String? {
        if !isPublic {
            return "This cell is readonly — its owner shares it with the net to read, not to steer."
        }
        if owner?.online != true {
            return "\(owner?.label ?? "Its Mac") is offline — this is the last card it served."
        }
        return nil
    }

    /// Another Mac's threads, drawn as the local sheet draws them but inert:
    /// opening, branching or reviving a thread happens on its owner's board.
    private func threadChips(_ threads: [WebThread]) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(threads) { thread in
                        ThreadChip(thread: thread)
                    }
                }
            }
            Text("threads open on their owner's board")
                .font(.voice(11))
                .foregroundStyle(KTheme.text2)
        }
    }

    private var header: some View {
        JobSectionCard {
            VStack(alignment: .leading, spacing: 6) {
                Text(job.title)
                    .font(.data(16, weight: .semibold))
                    .foregroundStyle(KTheme.text)
                Text(job.repo)
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
                HStack(spacing: 8) {
                    StateCapsule(state: job.state)
                    if let owner {
                        Text(owner.chipText)
                            .font(.data(10, weight: .semibold))
                            .foregroundStyle(KTheme.accent)
                            .padding(.horizontal, 7).padding(.vertical, 2)
                            .overlay(Capsule().stroke(KTheme.accent.opacity(0.6), lineWidth: 1))
                    }
                    Text(isPublic ? "public" : "readonly")
                        .font(.data(10))
                        .foregroundStyle(isPublic ? KTheme.solved : KTheme.text2)
                    if let load = job.load, load.badgeText != nil {
                        JobROICapsule(load: load)
                    }
                }
                if let wakeup = job.wakeup, job.state == "working" {
                    WakeupChip(wakeup: wakeup)
                }
                if let prs = job.prs, !prs.isEmpty {
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 6) {
                            ForEach(prs, id: \.url) { pr in
                                PRChip(pr: pr)
                            }
                        }
                    }
                }
            }
        }
    }

    @ViewBuilder
    private func questionContent(_ question: WebQuestion) -> some View {
        // Only a public cell gets answer buttons; a readonly one reads its
        // pending question as summary rows.
        if isPublic, question.questions.count == 1, let only = question.questions.first, !only.multiSelect {
            QuestionCard(item: only) { digit in
                run("question", payload: NodeActionPayload(text: String(digit)), dismissOnSuccess: true)
            }
            .disabled(!canAct)
            .opacity(canAct ? 1 : 0.45)
        } else {
            ForEach(Array(question.questions.enumerated()), id: \.offset) { _, q in
                QuestionSummary(item: q)
            }
        }
    }

    private var composer: some View {
        VStack(alignment: .leading, spacing: 8) {
            TextField(canAct ? "message to \(owner?.label ?? "their") zeus…" : "offline", text: $steerText, axis: .vertical)
                .lineLimit(1...6)
                .focused($composerFocused)
                .font(.voice(13))
                .kField(focused: composerFocused)
            Button {
                let text = steerText.trimmingCharacters(in: .whitespacesAndNewlines)
                run("steer", payload: NodeActionPayload(text: text), dismissOnSuccess: false) {
                    steerText = ""
                    flashSent()
                }
            } label: {
                Label("Send", systemImage: "paperplane.fill").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .disabled(steerText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            Text("arrives tagged with this phone's Mac and you as the sender")
                .font(.voice(11))
                .foregroundStyle(KTheme.text2)
        }
        .disabled(!canAct)
    }

    /// Every lifecycle control the card's state allows — a public card only
    /// (a readonly one draws none), disabled while its owner is offline.
    private var actions: some View {
        let items: [(String, String, Bool, () -> Void)] = [
            ("Submit", "play.fill", canSubmit, { run("submit", dismissOnSuccess: true) }),
            ("Stop", "stop.fill", canStop, { run("stop", dismissOnSuccess: false) }),
            ("Done", "checkmark", canMarkDone, { run("done", dismissOnSuccess: true) }),
            ("Kill", "xmark.octagon", canKill, { showingKillConfirm = true }),
            ("Archive", "archivebox", canArchive, { run("archive", dismissOnSuccess: true) }),
        ]
        let shown = items.filter { $0.2 }
        return VStack(alignment: .leading, spacing: 8) {
            if shown.isEmpty {
                Text("nothing to do in this state")
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
            }
            ForEach(shown, id: \.0) { item in
                Button(action: item.3) {
                    Label(item.0, systemImage: item.1).frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .tint(item.0 == "Kill" ? KTheme.counter : KTheme.accent)
            }
        }
        .disabled(!canAct)
        .opacity(canAct ? 1 : 0.45)
    }

    private func send(_ action: String, _ payload: NodeActionPayload = NodeActionPayload()) async throws {
        guard let node = owner?.nodeID, canAct else {
            throw KrakenError.message("this cell is readonly")
        }
        try await APIClient.shared.nodeAction(baseURL: baseURL, node: node, slug: job.slug, action: action, payload: payload)
    }

    private func run(_ action: String, payload: NodeActionPayload = NodeActionPayload(),
                     dismissOnSuccess: Bool, onSuccess: (() -> Void)? = nil) {
        isBusy = true
        errorMessage = nil
        Task {
            do {
                try await send(action, payload)
                isBusy = false
                onSuccess?()
                onUpdate()
                if dismissOnSuccess { dismiss() }
            } catch {
                isBusy = false
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func flashSent() {
        sentToast = true
        Task {
            try? await Task.sleep(for: .seconds(2))
            sentToast = false
        }
    }
}
