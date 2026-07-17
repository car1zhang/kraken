import SwiftUI

struct JobDetailView: View {
    let job: WebJob
    let baseURL: String
    var onUpdate: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var isBusy = false
    @State private var errorMessage: String?
    @State private var showingKillConfirm = false
    @State private var showingAnswer = false
    @State private var showingEdit = false
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

    var body: some View {
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
                        escalationRow(e)
                    }
                    if canAnswer {
                        Button("Answer") { showingAnswer = true }
                            .buttonStyle(.kUnblock)
                            .listRowBackground(Color.clear)
                    }
                }
            }
            if let artifacts = job.artifacts, !artifacts.isEmpty {
                kSection("Artifacts") {
                    ForEach(artifacts, id: \.url) { a in
                        Button(a.name) { showingArtifact = a }
                            .font(.data(12))
                            .foregroundStyle(KTheme.accent)
                    }
                }
            }

            kSection {
                if canQueue {
                    Button("Queue") {
                        runAction { try await APIClient.shared.queueJob(baseURL: baseURL, slug: job.slug) }
                    }
                    .buttonStyle(.kPrimary)
                    .listRowBackground(Color.clear)
                }
                if canRetry {
                    Button("Retry") {
                        runAction { try await APIClient.shared.retryJob(baseURL: baseURL, slug: job.slug) }
                    }
                    .buttonStyle(.kRetry)
                    .listRowBackground(Color.clear)
                }
                if column == .review {
                    Button("Mark Done") {
                        runAction { try await APIClient.shared.markDone(baseURL: baseURL, slug: job.slug) }
                    }
                    .buttonStyle(.kPrimary)
                    .listRowBackground(Color.clear)
                }
                if isEditable {
                    Button("Edit Spec") { showingEdit = true }
                        .buttonStyle(.kGhost)
                        .listRowBackground(Color.clear)
                }
                if canKill {
                    Button("Kill", role: .destructive) { showingKillConfirm = true }
                        .buttonStyle(.kDestructiveText)
                }
            }

            if let errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle(job.slug)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Close") { dismiss() }.buttonStyle(.kGhost)
            }
        }
        .disabled(isBusy)
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
        .sheet(item: $showingArtifact) { artifact in
            NavigationStack {
                Group {
                    if let url = URL(string: baseURL + artifact.url) {
                        ArtifactView(name: artifact.name, url: url)
                    } else {
                        Text("invalid artifact URL")
                            .foregroundStyle(KTheme.counter)
                    }
                }
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Close") { showingArtifact = nil }.buttonStyle(.kGhost)
                    }
                }
            }
        }
    }

    private func fieldRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).font(.data(10)).foregroundStyle(KTheme.text2)
            Spacer()
            Text(value).font(.voice(13)).foregroundStyle(KTheme.text)
        }
    }

    // Open escalations: counterSoft bg, 2px counter left border. Answered:
    // transparent, hairline2 left border, text2 — mirrors .esc / .esc.answered.
    private func escalationRow(_ e: WebEsc) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(e.category)
                    .font(.data(10, weight: .semibold))
                    .foregroundStyle(e.status == "open" ? KTheme.counter : KTheme.text2)
                Text(e.title)
                    .font(.voice(13))
                    .foregroundStyle(e.status == "open" ? KTheme.text : KTheme.text2)
                Spacer()
                if e.status == "open" {
                    Text("open").font(.data(9)).foregroundStyle(KTheme.counter)
                }
            }
            Text(e.body)
                .font(.voice(12))
                .foregroundStyle(KTheme.text2)
        }
        .padding(8)
        .overlay(alignment: .leading) {
            Rectangle()
                .fill(e.status == "open" ? KTheme.counter : KTheme.hairline2)
                .frame(width: 2)
        }
        .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 4, trailing: 12))
        .listRowBackground(e.status == "open" ? KTheme.counterSoft : Color.clear)
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
                        HStack {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(e.category).font(.data(10, weight: .semibold)).foregroundStyle(KTheme.counter)
                                Text(e.title).font(.voice(13)).foregroundStyle(KTheme.text)
                                Text(e.body).font(.voice(12)).foregroundStyle(KTheme.text2).lineLimit(4)
                            }
                            Spacer()
                            if selectedFile == e.file || (selectedFile == nil && e.file == openEscalations.first?.file) {
                                Image(systemName: "checkmark.circle.fill").foregroundStyle(KTheme.accent)
                            }
                        }
                    }
                    .buttonStyle(.plain)
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
                Button("Cancel") { dismiss() }.buttonStyle(.kGhost)
            }
            ToolbarItem(placement: .confirmationAction) {
                Button("Send") { send() }
                    .buttonStyle(.kPrimary)
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
