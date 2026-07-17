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
            Section {
                HStack {
                    Circle().fill(BoardTheme.color(forState: job.state)).frame(width: 10, height: 10)
                    Text(job.state)
                    Spacer()
                    Text(job.type).foregroundStyle(.secondary)
                }
                LabeledContent("Repo", value: job.repo)
                if job.live, let cell = job.cell {
                    LabeledContent("Cell", value: cell)
                }
            }

            if let err = job.err, !err.isEmpty {
                Section("Error") { Text(err).foregroundStyle(.red) }
            }
            if isUnhealthy {
                Section("Health") { Text(job.health ?? "").foregroundStyle(.orange) }
            }
            if let outcome = job.outcome, !outcome.isEmpty {
                Section("Outcome") { Text(outcome) }
            }
            if let success = job.success, !success.isEmpty {
                Section("Success criterion") { Text(success) }
            }
            if let prereqs = job.prereqs, !prereqs.isEmpty {
                Section("Prereqs") {
                    ForEach(prereqs, id: \.job) { p in
                        HStack {
                            Text(p.job)
                            Spacer()
                            Text(p.done ? "done" : (p.gated ? "gated" : "pending"))
                                .font(.caption)
                                .foregroundStyle(p.done ? .green : (p.gated ? .orange : .secondary))
                        }
                    }
                }
            }
            if let escalations = job.escalations, !escalations.isEmpty {
                Section("Escalations") {
                    ForEach(escalations, id: \.file) { e in
                        VStack(alignment: .leading, spacing: 4) {
                            HStack {
                                Text(e.category).font(.caption).bold()
                                Text(e.title)
                                Spacer()
                                if e.status == "open" {
                                    Text("open").font(.caption2).foregroundStyle(.orange)
                                }
                            }
                            Text(e.body).font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    if canAnswer {
                        Button("Answer") { showingAnswer = true }
                    }
                }
            }
            if let artifacts = job.artifacts, !artifacts.isEmpty {
                Section("Artifacts") {
                    ForEach(artifacts, id: \.url) { a in
                        Button(a.name) { showingArtifact = a }
                    }
                }
            }

            Section {
                if canQueue {
                    Button("Queue") {
                        runAction { try await APIClient.shared.queueJob(baseURL: baseURL, slug: job.slug) }
                    }
                }
                if canRetry {
                    Button("Retry") {
                        runAction { try await APIClient.shared.retryJob(baseURL: baseURL, slug: job.slug) }
                    }
                }
                if column == .review {
                    Button("Mark Done") {
                        runAction { try await APIClient.shared.markDone(baseURL: baseURL, slug: job.slug) }
                    }
                }
                if canKill {
                    Button("Kill", role: .destructive) { showingKillConfirm = true }
                }
                if isEditable {
                    Button("Edit Spec") { showingEdit = true }
                }
            }

            if let errorMessage {
                Section { Text(errorMessage).foregroundStyle(.red) }
            }
        }
        .navigationTitle(job.slug)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Close") { dismiss() }
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
                    }
                }
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Close") { showingArtifact = nil }
                    }
                }
            }
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
}

/// The blocked → working move: pick an open escalation, compose an answer,
/// send. Embedded here (rather than its own file) since it's only ever
/// reached from JobDetailView.
private struct AnswerView: View {
    let job: WebJob
    let baseURL: String
    var onSent: () -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var selectedFile: String?
    @State private var text = ""
    @State private var isBusy = false
    @State private var errorMessage: String?

    private var openEscalations: [WebEsc] { (job.escalations ?? []).filter { $0.status == "open" } }

    var body: some View {
        Form {
            Section("Open escalations") {
                ForEach(openEscalations, id: \.file) { e in
                    Button {
                        selectedFile = e.file
                    } label: {
                        HStack {
                            VStack(alignment: .leading, spacing: 4) {
                                Text(e.category).font(.caption).bold()
                                Text(e.title)
                                Text(e.body).font(.caption).foregroundStyle(.secondary).lineLimit(4)
                            }
                            Spacer()
                            if selectedFile == e.file || (selectedFile == nil && e.file == openEscalations.first?.file) {
                                Image(systemName: "checkmark.circle.fill").foregroundStyle(Color.accentColor)
                            }
                        }
                    }
                    .buttonStyle(.plain)
                }
            }
            Section("Answer") {
                TextEditor(text: $text).frame(minHeight: 120)
            }
            if let errorMessage {
                Section { Text(errorMessage).foregroundStyle(.red) }
            }
        }
        .navigationTitle("Answer \(job.slug)")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button("Send") { send() }
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
