import SwiftUI

/// One editable prereq row for the Form UI. Wraps Prereq with a stable UUID
/// identity so ForEach/add/remove behave correctly independent of content
/// (two prereq rows can legitimately have the same slug mid-edit).
private struct PrereqRow: Identifiable {
    let id = UUID()
    var job: String
    var gated: Bool
}

/// Autosave's compact status readout — idle until the first real edit after
/// load, then syncing/synced/failed as each debounced saveform round-trips.
private enum SyncState: Equatable {
    case idle
    case syncing
    case synced
    case failed(String)
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

    @State private var repo = ""
    @State private var base = ""
    @State private var ready = false
    @State private var solo = false
    @State private var model = ""
    @State private var prereqRows: [PrereqRow] = []
    @State private var spec = ""
    @State private var mtime: Int64 = 0
    // Defaults to false on purpose: the form (and autosave, gated on
    // `editable` below) must stay inert until a load actually succeeds, so a
    // transient network failure can never render an editable-looking form
    // with blank fields and silently autosave them over the real file.
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
    @State private var syncState: SyncState = .idle
    // The in-flight or pending debounced autosave, if any — cancelled and
    // replaced on every edit (scheduleAutosave) or run immediately
    // (flushAutosave), so at most one saveform call is ever in flight.
    @State private var autosaveTask: Task<Void, Never>?
    // Monotonic id for the task above: Task is a struct (no identity), so a
    // finishing closure needs this to know it's still the *latest* task
    // before nilling autosaveTask — a stale closure clearing a newer task
    // would re-open the canQueueForm debounce gap it exists to close.
    @State private var autosaveGeneration = 0
    @State private var isQueueing = false
    // Surfaced inline near the sync status, separate from loadError (the
    // job's own parse problem) and fetchError (a failed initial load) — this
    // is specifically a failed queue attempt, e.g. the server's 409 on an
    // empty spec.
    @State private var queueError: String?
    // Attachment upload errors and a brief post-insert confirmation, shown
    // near the spec editor — separate from loadError/queueError since this
    // is neither a parse problem nor a queue failure.
    @State private var attachmentError: String?
    @State private var insertedConfirmation: String?
    @State private var isUploading = false

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
                // solo full-screen recovery CTA, not a circular pill in a row —
                // keeps its label (icon-only rule is for the compact buttons)
                Label("Retry", systemImage: "arrow.clockwise")
            }
            .buttonStyle(.borderedProminent)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(KTheme.ground)
        .navigationTitle(slug)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton(label: "Cancel") { dismiss() }
            }
        }
    }

    private var form: some View {
        Form {
            if isNew {
                kSection {
                    Text("Created — fill in the spec below; it autosaves as you type. Queue arms once it's synced.")
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
            kSection("Model") {
                TextField("model for zeus (optional, default = claude's default)", text: $model)
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                    .font(.data(13))
                    .kField()
                    .listRowBackground(Color.clear)
                // suggestion chips, same pattern as the repo suggestions above
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(["fable", "opus", "sonnet", "haiku"], id: \.self) { m in
                            Button(m) { model = m }
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
            kSection("Flags") {
                Toggle("Ready (queue on save)", isOn: $ready)
                    .font(.voice(13))
                    .foregroundStyle(KTheme.text)
                Toggle("Solo (no subagent crew)", isOn: $solo)
                    .font(.voice(13))
                    .foregroundStyle(KTheme.text)
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
                if editable {
                    CmdChipsRow(text: $spec)
                }
                HighlightingTextEditor(text: $spec)
                    .kField(minHeight: 160)
                    .listRowBackground(Color.clear)
                if editable {
                    ImageAttachmentBar(
                        baseURL: baseURL, slug: slug,
                        isUploading: $isUploading,
                        onUploaded: { insertAttachment($0) },
                        onError: { attachmentError = $0 }
                    )
                    if let insertedConfirmation {
                        Text("added \(insertedConfirmation)")
                            .font(.voice(11))
                            .foregroundStyle(KTheme.solved)
                    }
                }
                if let attachmentError {
                    Text(attachmentError).font(.voice(11)).foregroundStyle(KTheme.counter)
                }
            }
            if let loadError, !loadError.isEmpty {
                kSection("File currently won't run") {
                    Text(loadError).font(.voice(12)).foregroundStyle(KTheme.counter)
                }
            }
            if syncState != .idle {
                kSection { syncStatusRow }
            }
            if let queueError, !queueError.isEmpty {
                kSection { Text(queueError).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle(slug)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton(label: "Cancel") { dismiss() }
            }
            // Explicit, tap-only arming — no auto-queue on type. Disabled
            // until the spec has real content and the latest edit has
            // actually landed server-side (not mid-flight, not failed), so a
            // tap can never race a still-syncing keystroke.
            ToolbarItem(placement: .primaryAction) {
                Button {
                    queueTapped()
                } label: {
                    Label("Queue", systemImage: "play.fill")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Queue")
                .buttonStyle(.borderedProminent)
                .tint(KTheme.accent)
                .disabled(!canQueueForm || isQueueing)
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    finishEditing()
                } label: {
                    Label("Done", systemImage: "checkmark")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Done")
                .buttonStyle(.borderedProminent)
                .disabled(isUploading)
            }
        }
        // autosaveSnapshotKey covers every field except `ready`, which gets
        // its own immediate flush below — toggling "queue on save" should
        // act right away rather than wait out the debounce.
        .onChange(of: autosaveSnapshotKey) { scheduleAutosave() }
        .onChange(of: ready) { flushAutosave() }
    }

    @ViewBuilder
    private var syncStatusRow: some View {
        switch syncState {
        case .idle:
            EmptyView()
        case .syncing:
            statusLabel("arrow.triangle.2.circlepath", "syncing…", KTheme.text2)
        case .synced:
            statusLabel("checkmark.circle.fill", "synced", KTheme.solved)
        case .failed(let message):
            statusLabel("exclamationmark.triangle.fill", "sync failed: \(message)", KTheme.counter)
        }
    }

    private func statusLabel(_ icon: String, _ text: String, _ color: Color) -> some View {
        HStack(spacing: 6) {
            Image(systemName: icon)
            Text(text)
        }
        .font(.voice(12))
        .foregroundStyle(color)
    }

    // Gates the toolbar Queue button: editable, real spec content, no image
    // upload pending insert, and no autosave still in flight or waiting out
    // its debounce (autosaveTask non-nil covers the debounce gap; the
    // syncState checks cover the in-flight request itself and a just-failed
    // one) — a tap can only ever fire against a spec the server has already
    // accepted.
    private var canQueueForm: Bool {
        guard editable, !spec.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return false }
        guard !isUploading else { return false }
        guard autosaveTask == nil else { return false }
        switch syncState {
        case .syncing, .failed: return false
        case .idle, .synced: return true
        }
    }

    // Every autosaved field except `ready` (see the dedicated onChange
    // above) — a snapshot string so a single onChange can watch them all at
    // once, the same idiom SpecSection uses for its `.task(id:)` key.
    private var autosaveSnapshotKey: String {
        let prereqsPart = prereqRows.map { "\($0.job)=\($0.gated)" }.joined(separator: ",")
        return "\(repo)|\(base)|\(model)|\(solo)|\(prereqsPart)|\(spec)"
    }

    private func load() async {
        isLoading = true
        fetchError = nil
        do {
            let f = try await APIClient.shared.fetchJobForm(baseURL: baseURL, slug: slug)
            repo = f.repo
            base = f.base
            ready = f.ready
            solo = f.solo
            model = f.model ?? ""
            prereqRows = f.prereqs.map { PrereqRow(job: $0.job, gated: $0.gated) }
            spec = f.spec
            mtime = f.mtime
            editable = f.editable
            loadError = f.err.isEmpty ? nil : f.err
            slugSuggestions = f.slugs ?? []
            repoSuggestions = f.repos ?? []
            syncState = .idle
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

    // Unlike SteerView's deferred chips-then-append-on-send, the spec editor
    // inserts each uploaded image straight into the spec text as it lands
    // (appended at the end — the field doesn't track cursor position) and
    // fades a brief confirmation instead of keeping a removable attachment
    // list, since the line is already part of the editable text at that
    // point and can just be deleted like any other text.
    private func insertAttachment(_ attachment: ComposerAttachment) {
        attachmentError = nil
        let separator = spec.isEmpty || spec.hasSuffix("\n") ? "" : "\n"
        spec += separator + "[image: \(attachment.path)]\n"
        insertedConfirmation = attachment.name
        Task {
            try? await Task.sleep(for: .seconds(2))
            if insertedConfirmation == attachment.name {
                insertedConfirmation = nil
            }
        }
    }

    // Debounced path: cancels any pending/in-flight autosave and schedules a
    // fresh one 600ms out, so a burst of typing collapses into one saveform
    // call after the user pauses rather than one per keystroke.
    private func scheduleAutosave() {
        guard editable else { return }
        autosaveTask?.cancel()
        autosaveGeneration += 1
        let generation = autosaveGeneration
        autosaveTask = Task {
            try? await Task.sleep(for: .milliseconds(600))
            guard !Task.isCancelled else { return }
            await performAutosave()
            if autosaveGeneration == generation { autosaveTask = nil }
        }
    }

    // Immediate path: same cancel-and-replace, but no debounce delay — used
    // for the Ready toggle (queueing shouldn't wait out the debounce) and
    // for the Done button flushing any pending edit before closing.
    private func flushAutosave() {
        guard editable else { return }
        autosaveTask?.cancel()
        autosaveGeneration += 1
        let generation = autosaveGeneration
        autosaveTask = Task {
            await performAutosave()
            if autosaveGeneration == generation { autosaveTask = nil }
        }
    }

    // Always sends mtime: 0 — this is a deliberate, permanent choice for
    // autosave (never the loaded mtime, never `force`): the optimistic lock
    // that guards a manual Save against a stale copy doesn't apply to a
    // continuously-syncing editor, same reasoning as the job-scoped draft
    // sync being last-write-wins.
    private func performAutosave() async {
        syncState = .syncing
        let req = JobFormSaveRequest(
            slug: slug, repo: repo, base: base,
            ready: ready, solo: solo,
            model: model.trimmingCharacters(in: .whitespaces),
            prereqs: prereqRows.map { Prereq(job: $0.job, gated: $0.gated) },
            spec: spec, mtime: 0
        )
        do {
            let resp = try await APIClient.shared.saveJobForm(baseURL: baseURL, form: req)
            guard !Task.isCancelled else { return }
            mtime = resp.mtime
            loadError = resp.warn.isEmpty ? nil : resp.warn
            syncState = .synced
        } catch {
            guard !Task.isCancelled else { return }
            syncState = .failed((error as? KrakenError)?.errorDescription ?? error.localizedDescription)
        }
    }

    // The toolbar's Queue action: flushes any pending edit via the same
    // immediate-save path the Ready toggle uses, waits for that specific
    // save to land, then queues — matching the web's queue-then-close
    // behavior. A 409 (e.g. an empty spec slipping through a race, or a
    // server-side rule the client doesn't know about) surfaces inline next
    // to the sync status rather than dismissing.
    private func queueTapped() {
        guard canQueueForm, !isQueueing else { return }
        queueError = nil
        isQueueing = true
        flushAutosave()
        let pending = autosaveTask
        Task {
            await pending?.value
            if case .failed(let message) = syncState {
                isQueueing = false
                queueError = "autosave failed, can't queue: \(message)"
                return
            }
            do {
                try await APIClient.shared.queueJob(baseURL: baseURL, slug: slug)
                isQueueing = false
                onSaved()
            } catch {
                isQueueing = false
                queueError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    // The toolbar's Done action: flushes any pending edit immediately (so
    // the last few keystrokes before closing aren't left stranded behind the
    // debounce timer) before handing back to the caller, which dismisses.
    private func finishEditing() {
        autosaveTask?.cancel()
        guard editable else {
            onSaved()
            return
        }
        Task {
            await performAutosave()
            onSaved()
        }
    }
}
