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
/// `.readonly`: the card, its detail and question, every control drawn and
/// disabled — the owner has not opened it to the net. `.publicCell`: the same
/// controls live, each one posting `POST /api/nodes/action` on the PAIRED node,
/// which forwards it to the owner tagged with who sent it. Nothing here ever
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
    @FocusState private var composerFocused: Bool

    private var owner: WebJobOwner? { job.owner }
    private var column: BoardColumn { BoardTheme.column(for: job) }
    /// Whether any control may act: a public cell on a node that is up.
    private var canAct: Bool { job.remoteMode == .publicCell && owner?.online == true }

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
                header
                if let note = modeNote {
                    JobSectionCard {
                        Text(note)
                            .font(.voice(12))
                            .foregroundStyle(KTheme.revealed)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                if let detail = job.detail, !detail.isEmpty {
                    JobSectionCard("Detail") { DetailCard(text: detail) }
                }
                if let question = job.question {
                    JobSectionCard("Question") { questionContent(question) }
                }
                if canSteer {
                    JobSectionCard("Steer") { composer }
                }
                JobSectionCard("Actions") { actions }
                JobSectionCard("Color") {
                    JobColorStrip(baseURL: baseURL, slug: job.slug, serverColor: job.color, onSaved: { _ in onUpdate() },
                                  save: { name in try await send("color", NodeActionPayload(color: name)) })
                        .disabled(!canAct)
                        .opacity(canAct ? 1 : 0.45)
                }
                // A remote card arrives with its artifacts stripped by the
                // owner, and no route reaches them from here.
                JobSectionCard("Artifacts") {
                    Text("artifacts stay on the owner's board")
                        .font(.voice(12))
                        .foregroundStyle(KTheme.text2)
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
    }

    private var modeNote: String? {
        if job.remoteMode != .publicCell {
            return "This cell is readonly — its owner has not opened it to the net."
        }
        if owner?.online != true {
            return "\(owner?.label ?? "Its Mac") is offline — this is the last card it served."
        }
        return nil
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
                    Text(job.remoteMode == .publicCell ? "public" : "readonly")
                        .font(.data(10))
                        .foregroundStyle(job.remoteMode == .publicCell ? KTheme.solved : KTheme.text2)
                }
            }
        }
    }

    @ViewBuilder
    private func questionContent(_ question: WebQuestion) -> some View {
        if question.questions.count == 1, let only = question.questions.first, !only.multiSelect {
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
            TextField(canAct ? "message to \(owner?.label ?? "their") zeus…" : "readonly", text: $steerText, axis: .vertical)
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

    /// Every lifecycle control the card's state allows, drawn even when the
    /// card is readonly (disabled) so the sheet says what it would offer.
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
