import SwiftUI

/// One editable prereq row for the Form UI. Wraps Prereq with a stable UUID
/// identity so ForEach/add/remove behave correctly independent of content
/// (two prereq rows can legitimately have the same slug mid-edit).
private struct PrereqRow: Identifiable {
    let id = UUID()
    var job: String
    var gated: Bool
}

/// Structured spec editor: SwiftUI Form bound to GET /api/job/form,
/// saved back via POST /api/job/saveform. Used both for editing an existing
/// job (from JobDetailView) and, chained after creation, for a brand new one
/// (from NewJobSheet).
struct JobFormView: View {
    let slug: String
    let baseURL: String
    let isNew: Bool
    var onSaved: () -> Void

    @Environment(\.dismiss) private var dismiss

    @State private var type = "build"
    @State private var repo = ""
    @State private var base = ""
    @State private var ready = false
    @State private var solo = false
    @State private var autoApprove = false
    @State private var success = ""
    @State private var prereqRows: [PrereqRow] = []
    @State private var spec = ""
    @State private var mtime: Int64 = 0
    // Defaults to false on purpose: the form (and Save) must stay hidden
    // until a load actually succeeds, so a transient network failure can
    // never render an editable form with blank fields and mtime 0 — saving
    // that would skip the server's optimistic lock (web.go's saveform only
    // checks the mtime when it's non-zero) and wipe the real file.
    @State private var editable = false
    @State private var loaded = false
    // The job's own parse problem (JobForm.err) — "saved but won't run".
    // Kept separate from load failures below; conflating the two used to
    // render network errors under a misleading "won't run" header.
    @State private var loadError: String?
    // A failure to fetch/decode the form itself. Non-nil only when `loaded`
    // is false — gates the whole form behind its own retry screen.
    @State private var fetchError: String?
    @State private var slugSuggestions: [String] = []
    @State private var repoSuggestions: [String] = []

    @State private var isLoading = true
    @State private var isSaving = false
    @State private var note: String?
    @State private var noteIsWarning = false
    @State private var showingConflict = false

    var body: some View {
        Group {
            if isLoading {
                ProgressView()
                    .tint(KTheme.accent)
                    .navigationTitle(slug)
            } else if !loaded {
                failedLoad
            } else {
                form
            }
        }
        .task { await load() }
    }

    private var failedLoad: some View {
        VStack(spacing: 16) {
            Text(fetchError ?? "failed to load")
                .font(.voice(13))
                .foregroundStyle(KTheme.counter)
                .multilineTextAlignment(.center)
            Button {
                Task { await load() }
            } label: {
                Label("Retry", systemImage: "arrow.clockwise")
            }
            .labelStyle(.iconOnly)
            .accessibilityLabel("Retry")
            .buttonStyle(.borderedProminent)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(KTheme.ground)
        .navigationTitle(slug)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }.buttonStyle(.bordered)
            }
        }
    }

    private var form: some View {
        Form {
            if isNew {
                kSection {
                    Text("Created — fill in the spec below, then Save. It stays a draft until you flip Ready or queue it from the board.")
                        .font(.voice(12))
                        .foregroundStyle(KTheme.text2)
                }
            }
            if !editable {
                kSection {
                    Text("This job has a cell in flight — its file belongs to the crew right now.")
                        .font(.voice(12))
                        .foregroundStyle(KTheme.revealed)
                }
            }
            kSection("Type") {
                Picker("Type", selection: $type) {
                    Text("build").tag("build")
                    Text("explore").tag("explore")
                    Text("question").tag("question")
                }
                .pickerStyle(.segmented)
            }
            kSection("Repo") {
                TextField("repo", text: $repo)
                    .autocorrectionDisabled()
                    .font(.data(13))
                    .kField()
                    .listRowBackground(Color.clear)
                if !repoSuggestions.isEmpty {
                    // .bordered/.borderedProminent inside a List row renders
                    // as a doubled-background blob (a known Form quirk) —
                    // .plain + manual border reads as a clean tappable chip
                    // instead, same fix as the row-button audit elsewhere.
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 6) {
                            ForEach(repoSuggestions, id: \.self) { r in
                                Button(r) { repo = r }
                                    .buttonStyle(.plain)
                                    .font(.data(11))
                                    .foregroundStyle(KTheme.accent)
                                    .padding(.horizontal, 8)
                                    .padding(.vertical, 4)
                                    .overlay(Rectangle().stroke(KTheme.accentSoft, lineWidth: 1))
                            }
                        }
                    }
                    .listRowBackground(Color.clear)
                }
                TextField("base ref (optional, default origin/main)", text: $base)
                    .autocorrectionDisabled()
                    .font(.data(13))
                    .kField()
                    .listRowBackground(Color.clear)
            }
            kSection("Flags") {
                Toggle("Ready (queue on save)", isOn: $ready)
                    .font(.voice(13))
                    .foregroundStyle(KTheme.text)
                Toggle("Solo (no subagent crew)", isOn: $solo)
                    .font(.voice(13))
                    .foregroundStyle(KTheme.text)
                Toggle("Auto-approve plan", isOn: $autoApprove)
                    .font(.voice(13))
                    .foregroundStyle(KTheme.text)
            }
            kSection("Success criterion") {
                TextEditor(text: $success)
                    .scrollContentBackground(.hidden)
                    .kField(minHeight: 80)
                    .listRowBackground(Color.clear)
            }
            kSection("Prereqs") {
                ForEach($prereqRows) { $row in
                    HStack {
                        TextField("job-slug", text: $row.job)
                            .autocorrectionDisabled()
                            .font(.data(12))
                            .kField()
                        Text("gated")
                            .font(.data(10))
                            .foregroundStyle(row.gated ? KTheme.counter : KTheme.text2)
                        Toggle("", isOn: $row.gated)
                            .labelsHidden()
                            .fixedSize()
                        Button {
                            prereqRows.removeAll { $0.id == row.id }
                        } label: {
                            Image(systemName: "minus.circle.fill")
                        }
                        .buttonStyle(.plain)
                        .foregroundStyle(KTheme.text2)
                    }
                }
                if !slugSuggestions.isEmpty {
                    Menu {
                        ForEach(slugSuggestions, id: \.self) { s in
                            Button(s) { prereqRows.append(PrereqRow(job: s, gated: false)) }
                        }
                    } label: {
                        Label("Add prereq", systemImage: "plus.circle")
                    }
                    .buttonStyle(.plain)
                    .font(.data(12))
                    .foregroundStyle(KTheme.text2)
                } else {
                    Button {
                        prereqRows.append(PrereqRow(job: "", gated: false))
                    } label: {
                        Label("Add prereq", systemImage: "plus.circle")
                    }
                    .buttonStyle(.plain)
                    .font(.data(12))
                    .foregroundStyle(KTheme.text2)
                }
            }
            kSection("Spec") {
                TextEditor(text: $spec)
                    .scrollContentBackground(.hidden)
                    .kField(minHeight: 160)
                    .listRowBackground(Color.clear)
            }
            if let loadError, !loadError.isEmpty {
                kSection("File currently won't run") {
                    Text(loadError).font(.voice(12)).foregroundStyle(KTheme.counter)
                }
            }
            if let note {
                kSection {
                    Text(note).font(.voice(12)).foregroundStyle(noteIsWarning ? KTheme.revealed : KTheme.solved)
                }
            }
        }
        .kListStyle()
        .navigationTitle(slug)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { dismiss() }.buttonStyle(.bordered)
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    save()
                } label: {
                    Label("Save", systemImage: "square.and.arrow.down")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Save")
                .buttonStyle(.borderedProminent)
                .disabled(isSaving || !editable || mtime == 0)
            }
        }
        .disabled(isSaving)
        .confirmationDialog(
            "The file changed on disk since you opened it. Reload to discard your edits and see the latest version.",
            isPresented: $showingConflict, titleVisibility: .visible
        ) {
            Button("Reload") { Task { await load() } }
            Button("Cancel", role: .cancel) {}
        }
    }

    private func load() async {
        isLoading = true
        fetchError = nil
        do {
            let f = try await APIClient.shared.fetchJobForm(baseURL: baseURL, slug: slug)
            type = f.type
            repo = f.repo
            base = f.base
            ready = f.ready
            solo = f.solo
            autoApprove = f.autoApprove
            success = f.success
            prereqRows = f.prereqs.map { PrereqRow(job: $0.job, gated: $0.gated) }
            spec = f.spec
            mtime = f.mtime
            editable = f.editable
            loadError = f.err.isEmpty ? nil : f.err
            slugSuggestions = f.slugs ?? []
            repoSuggestions = f.repos ?? []
            note = nil
            loaded = true
        } catch {
            // Leave editable/mtime at their safe defaults (false / 0) rather
            // than whatever a prior successful load left behind — a failed
            // Reload must not resurrect a stale, now-unverified mtime.
            editable = false
            mtime = 0
            loaded = false
            fetchError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        isLoading = false
    }

    private func save() {
        // Second line of defense behind the disabled Save button: mtime == 0
        // is exactly the condition under which web.go's optimistic-lock
        // check (`req.MTime != 0 && ...`) is skipped server-side, so a save
        // here would silently overwrite the file with whatever's on screen.
        guard mtime != 0 else {
            note = "can't save — reload the job first"
            noteIsWarning = true
            return
        }
        isSaving = true
        note = nil
        let req = JobFormSaveRequest(
            slug: slug, type: type, repo: repo, base: base,
            ready: ready, solo: solo, autoApprove: autoApprove,
            success: success,
            prereqs: prereqRows.map { Prereq(job: $0.job, gated: $0.gated) },
            spec: spec, mtime: mtime
        )
        Task {
            do {
                let resp = try await APIClient.shared.saveJobForm(baseURL: baseURL, form: req)
                isSaving = false
                mtime = resp.mtime
                if resp.warn.isEmpty {
                    note = "saved"
                    noteIsWarning = false
                    onSaved()
                } else {
                    note = "saved — but won't run: \(resp.warn)"
                    noteIsWarning = true
                }
            } catch {
                isSaving = false
                let message = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
                if message.lowercased().contains("changed on disk") {
                    showingConflict = true
                } else {
                    note = message
                    noteIsWarning = true
                }
            }
        }
    }
}
