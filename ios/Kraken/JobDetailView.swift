import SwiftUI

struct JobDetailView: View {
    let job: WebJob
    let baseURL: String
    var onUpdate: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(AppRouter.self) private var router
    @State private var isBusy = false
    @State private var errorMessage: String?
    @State private var showingKillConfirm = false
    @State private var showingAnswer = false
    @State private var showingEdit = false
    @State private var showingSteer = false
    @State private var showingSteerToast = false
    @State private var showingArtifact: WebArtifact?

    private var column: BoardColumn { BoardTheme.column(for: job) }
    private var isUnhealthy: Bool { !(job.health ?? "").isEmpty }
    private var openEscalations: [WebEsc] { (job.escalations ?? []).filter { $0.status == "open" } }
    private var isEditable: Bool {
        !job.live && [BoardColumn.draft, .queued, .done].contains(column)
    }
    // Server refuses a retry once the cell is confirmed gone or stuck at a
    // startup prompt — mirrors app.js's `!/is gone|startup prompt/.test(health)`.
    private var canRetry: Bool {
        guard let health = job.health, !health.isEmpty else { return false }
        return !(health.contains("is gone") || health.contains("startup prompt"))
    }
    // webKill 409s unless the job is actually started and not done; mirrors
    // app.js's `["working", "blocked", "review"].includes(col) && j.live`.
    private var canKill: Bool {
        job.live && [BoardColumn.working, .blocked, .review].contains(column)
    }
    // webQueue 409s on an unparseable job even though it renders in draft.
    private var canQueue: Bool {
        column == .draft && (job.err ?? "").isEmpty
    }
    // webAnswer 409s unless displayState is blocked/plan-review, both of
    // which map to .blocked here.
    private var canAnswer: Bool {
        column == .blocked && !openEscalations.isEmpty
    }
    // Same visibility rule as Kill minus the column restriction: any live
    // cell can take free-text steering, regardless of which of the three
    // live columns it's currently sitting in.
    private var canSteer: Bool {
        job.live
    }
    private var hasAnyAction: Bool {
        canQueue || canRetry || column == .review || canAnswer || canSteer || isEditable || canKill
    }

    var body: some View {
        VStack(spacing: 0) {
            if hasAnyAction {
                actionBar
            }
            Form {
                kSection {
                    HStack {
                        Rectangle()
                            .fill(BoardTheme.color(forState: job.state))
                            .frame(width: 8, height: 8)
                        Text(job.state)
                            .font(.data(11))
                            .foregroundStyle(BoardTheme.color(forState: job.state))
                        Spacer()
                        Text(job.type)
                            .font(.data(11))
                            .foregroundStyle(KTheme.text2)
                    }
                    fieldRow("Repo", job.repo)
                    if job.live, let cell = job.cell {
                        fieldRow("Cell", cell)
                    }
                }

                if let err = job.err, !err.isEmpty {
                    kSection("Error") { Text(err).font(.voice(13)).foregroundStyle(KTheme.counter) }
                }
                if isUnhealthy {
                    kSection("Health") { Text(job.health ?? "").font(.voice(13)).foregroundStyle(KTheme.revealed) }
                }
                if let outcome = job.outcome, !outcome.isEmpty {
                    kSection("Outcome") { Text(outcome).font(.voice(13)).foregroundStyle(KTheme.solved) }
                }
                if let success = job.success, !success.isEmpty {
                    kSection("Success criterion") { Text(success).font(.voice(13)).foregroundStyle(KTheme.text) }
                }
                if let prereqs = job.prereqs, !prereqs.isEmpty {
                    kSection("Prereqs") {
                        ForEach(prereqs, id: \.job) { p in
                            HStack {
                                Text(p.job).font(.data(12)).foregroundStyle(KTheme.text)
                                Spacer()
                                Text(p.done ? "done" : (p.gated ? "gated" : "pending"))
                                    .font(.data(10))
                                    .foregroundStyle(p.done ? KTheme.solved : (p.gated ? KTheme.counter : KTheme.revealed))
                            }
                        }
                    }
                }
                if let escalations = job.escalations, !escalations.isEmpty {
                    kSection("Escalations") {
                        ForEach(escalations, id: \.file) { e in
                            EscalationCard(e: e)
                        }
                    }
                }
                if let artifacts = job.artifacts, !artifacts.isEmpty {
                    kSection("Artifacts") {
                        ForEach(artifacts, id: \.url) { a in
                            Button {
                                showingArtifact = a
                            } label: {
                                Label(a.name, systemImage: "doc.text")
                            }
                            .buttonStyle(.plain)
                            .font(.data(13))
                            .foregroundStyle(KTheme.accent)
                        }
                    }
                }

                if let errorMessage {
                    kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
                }
            }
            .kListStyle()
        }
        .navigationTitle(job.slug)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Close") { dismiss() }.buttonStyle(.bordered)
            }
        }
        .disabled(isBusy)
        .onAppear {
            // a deep link like kraken://job/<slug>/steer lands here with the
            // follow-on action still pending — consume it once
            switch router.pendingJobAction {
            case .answer where canAnswer:
                showingAnswer = true
            case .steer where canSteer:
                showingSteer = true
            case .artifact(let name):
                showingArtifact = (job.artifacts ?? []).first { $0.name == name }
            default:
                break
            }
            router.pendingJobAction = nil
        }
        .overlay(alignment: .top) {
            if showingSteerToast {
                Text("steering sent")
                    .font(.data(11))
                    .foregroundStyle(KTheme.accentInk)
                    .padding(.horizontal, 10).padding(.vertical, 4)
                    .background(KTheme.accent)
                    .padding(.top, 8)
            }
        }
        .confirmationDialog(
            "Kill \(job.slug)? The cell is torn down (artifacts preserved) and the job returns to draft.",
            isPresented: $showingKillConfirm, titleVisibility: .visible
        ) {
            Button("Kill", role: .destructive) {
                runAction { try await APIClient.shared.killJob(baseURL: baseURL, slug: job.slug) }
            }
            Button("Cancel", role: .cancel) {}
        }
        .sheet(isPresented: $showingAnswer) {
            NavigationStack {
                AnswerView(job: job, baseURL: baseURL) {
                    showingAnswer = false
                    onUpdate()
                    dismiss()
                }
            }
        }
        .sheet(isPresented: $showingEdit) {
            NavigationStack {
                JobFormView(slug: job.slug, baseURL: baseURL, isNew: false) {
                    showingEdit = false
                    onUpdate()
                    dismiss()
                }
            }
        }
        .sheet(isPresented: $showingSteer) {
            NavigationStack {
                SteerView(job: job, baseURL: baseURL) {
                    showingSteer = false
                    showSteerToast()
                }
            }
        }
        .sheet(item: $showingArtifact) { artifact in
            NavigationStack {
                ArtifactView(name: artifact.name, baseURL: baseURL, path: artifact.url)
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            Button("Close") { showingArtifact = nil }.buttonStyle(.bordered)
                        }
                    }
            }
        }
    }

    // One horizontal row of compact bordered buttons, outside the Form's row
    // system entirely — a .bordered/.borderedProminent Button placed inside a
    // List/Form row renders as a doubled-background full-row blob (a known
    // SwiftUI Form quirk); living in a plain HStack above the Form sidesteps
    // it completely and reads as a normal iOS action bar. Icon-only per
    // carl: "no text within the circular buttons" — each keeps its tint as
    // the only differentiator, so .accessibilityLabel carries the name for
    // VoiceOver now that there's no visible text.
    private var actionBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                if canQueue {
                    Button {
                        runAction { try await APIClient.shared.queueJob(baseURL: baseURL, slug: job.slug) }
                    } label: {
                        Label("Queue", systemImage: "play.fill")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Queue")
                    .tint(KTheme.accent)
                }
                if canRetry {
                    Button {
                        runAction { try await APIClient.shared.retryJob(baseURL: baseURL, slug: job.slug) }
                    } label: {
                        Label("Retry", systemImage: "arrow.clockwise")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Retry")
                    .tint(KTheme.revealed)
                }
                if column == .review {
                    Button {
                        runAction { try await APIClient.shared.markDone(baseURL: baseURL, slug: job.slug) }
                    } label: {
                        Label("Mark Done", systemImage: "checkmark")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Mark Done")
                    .tint(KTheme.solved)
                }
                if canAnswer {
                    Button {
                        showingAnswer = true
                    } label: {
                        Label("Answer", systemImage: "bubble.left.fill")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Answer")
                    .tint(KTheme.counter)
                }
                if canSteer {
                    Button {
                        showingSteer = true
                    } label: {
                        Label("Steer", systemImage: "arrowshape.turn.up.right.fill")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Steer")
                    .tint(KTheme.accent)
                }
                if isEditable {
                    Button {
                        showingEdit = true
                    } label: {
                        Label("Edit Spec", systemImage: "square.and.pencil")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Edit Spec")
                }
                if canKill {
                    Button(role: .destructive) {
                        showingKillConfirm = true
                    } label: {
                        Label("Kill", systemImage: "xmark")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Kill")
                }
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .padding(.horizontal)
            .padding(.vertical, 8)
        }
        .background(KTheme.surface)
        .overlay(alignment: .bottom) {
            Rectangle().fill(KTheme.hairline).frame(height: 1)
        }
    }

    private func fieldRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).font(.data(10)).foregroundStyle(KTheme.text2)
            Spacer()
            Text(value).font(.voice(13)).foregroundStyle(KTheme.text)
        }
    }

    private func runAction(_ action: @escaping () async throws -> Void) {
        isBusy = true
        errorMessage = nil
        Task {
            do {
                try await action()
                isBusy = false
                onUpdate()
                dismiss()
            } catch {
                isBusy = false
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func showSteerToast() {
        showingSteerToast = true
        onUpdate()
        Task {
            try? await Task.sleep(for: .seconds(2))
            showingSteerToast = false
        }
    }
}

/// One escalation, styled like the web UI's answer modal (title bold sans,
/// category tag in mono counter-color, body markdown-rendered). Open
/// escalations stay expanded and prominent (counterSoft row fill, counter
/// category tag — no left border; carl: "escalations don't need that red
/// line"); answered/closed ones collapse to a single dim row by default —
/// tap to expand and read the resolved body.
private struct EscalationCard: View {
    let e: WebEsc

    @State private var expanded: Bool

    init(e: WebEsc) {
        self.e = e
        _expanded = State(initialValue: e.status == "open")
    }

    private var isOpen: Bool { e.status == "open" }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                if !isOpen { expanded.toggle() }
            } label: {
                HStack(alignment: .top, spacing: 8) {
                    VStack(alignment: .leading, spacing: 3) {
                        HStack(spacing: 6) {
                            Text(e.category)
                                .font(.data(10, weight: .semibold))
                                .foregroundStyle(isOpen ? KTheme.counter : KTheme.text2)
                            if isOpen {
                                Text("open").font(.data(9)).foregroundStyle(KTheme.counter)
                            } else {
                                Text("answered").font(.data(9)).foregroundStyle(KTheme.text2)
                            }
                        }
                        Text(e.title)
                            .font(.voice(13, weight: isOpen ? .semibold : .regular))
                            .foregroundStyle(isOpen ? KTheme.text : KTheme.text2)
                    }
                    Spacer()
                    if !isOpen {
                        Image(systemName: expanded ? "chevron.up" : "chevron.down")
                            .font(.voice(11))
                            .foregroundStyle(KTheme.text2)
                    }
                }
                .padding(10)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(isOpen)

            if expanded {
                MarkdownBody(markdown: e.body)
                    .padding(.horizontal, 10)
                    .padding(.bottom, 10)
            }
        }
        .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 4, trailing: 12))
        .listRowBackground(isOpen ? KTheme.counterSoft : Color.clear)
    }
}

/// The blocked → working move: pick an open escalation, compose an answer,
/// send. Embedded here (rather than its own file) since it's only ever
/// reached from JobDetailView.
private struct AnswerView: View {
    let job: WebJob
    let baseURL: String
    var onSent: () -> Void

    @Environment(\.dismiss) private var dismiss
    @FocusState private var textFocused: Bool
    @State private var selectedFile: String?
    @State private var text = ""
    @State private var isBusy = false
    @State private var errorMessage: String?

    private var openEscalations: [WebEsc] { (job.escalations ?? []).filter { $0.status == "open" } }

    var body: some View {
        Form {
            kSection("Open escalations") {
                ForEach(openEscalations, id: \.file) { e in
                    Button {
                        selectedFile = e.file
                    } label: {
                        VStack(alignment: .leading, spacing: 6) {
                            HStack {
                                Text(e.category).font(.data(10, weight: .semibold)).foregroundStyle(KTheme.counter)
                                Text(e.title).font(.voice(13, weight: .semibold)).foregroundStyle(KTheme.text)
                                Spacer()
                                if selectedFile == e.file || (selectedFile == nil && e.file == openEscalations.first?.file) {
                                    Image(systemName: "checkmark.circle.fill").foregroundStyle(KTheme.accent)
                                }
                            }
                            MarkdownBody(markdown: e.body, maxHeight: 180)
                        }
                        .padding(10)
                        .overlay(Rectangle().stroke(selectedFile == e.file ? KTheme.counter : KTheme.hairline, lineWidth: 1))
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 4, trailing: 12))
                    .listRowBackground(Color.clear)
                }
            }
            kSection("Answer") {
                TextEditor(text: $text)
                    .focused($textFocused)
                    .scrollContentBackground(.hidden)
                    .kField(focused: textFocused, minHeight: 120)
                    .listRowBackground(Color.clear)
            }
            if let errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle("Answer \(job.slug)")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }.buttonStyle(.bordered)
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    send()
                } label: {
                    Label("Send", systemImage: "paperplane.fill")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Send")
                .buttonStyle(.borderedProminent)
                .disabled(isBusy || text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .disabled(isBusy)
        .onAppear {
            if selectedFile == nil { selectedFile = openEscalations.first?.file }
        }
    }

    private func send() {
        guard let file = selectedFile ?? openEscalations.first?.file else { return }
        isBusy = true
        errorMessage = nil
        Task {
            do {
                try await APIClient.shared.answer(baseURL: baseURL, slug: job.slug, file: file, text: text)
                isBusy = false
                onSent()
            } catch {
                isBusy = false
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// Free-text steering for a live cell — routed like a terminal message.
/// Reached from JobDetailView's action bar for any live job.
private struct SteerView: View {
    let job: WebJob
    let baseURL: String
    var onSteered: () -> Void

    @Environment(\.dismiss) private var dismiss
    @FocusState private var textFocused: Bool
    @State private var text = ""
    @State private var isBusy = false
    @State private var errorMessage: String?

    var body: some View {
        Form {
            kSection {
                Text("Free-text direction for the crew — routed to the cell like a terminal message.")
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
            }
            kSection("Message") {
                TextEditor(text: $text)
                    .focused($textFocused)
                    .scrollContentBackground(.hidden)
                    .kField(focused: textFocused, minHeight: 120)
                    .listRowBackground(Color.clear)
            }
            if let errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle("Steer \(job.slug)")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }.buttonStyle(.bordered)
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    send()
                } label: {
                    Label("Send", systemImage: "paperplane.fill")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Send")
                .buttonStyle(.borderedProminent)
                .disabled(isBusy || text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .disabled(isBusy)
    }

    private func send() {
        isBusy = true
        errorMessage = nil
        Task {
            do {
                try await APIClient.shared.steer(baseURL: baseURL, slug: job.slug, text: text)
                isBusy = false
                onSteered()
            } catch {
                isBusy = false
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
