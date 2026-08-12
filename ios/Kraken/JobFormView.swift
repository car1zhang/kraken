import SwiftUI

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
    @State private var solo = false
    @State private var model = ""
    // The job's color as last known from the server — "" means none. Only
    // ever seeds JobColorStrip (which owns the picking, the POST and its own
    // error row); the form's own save payload never carries it.
    @State private var color = ""
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
    // would re-open the canSubmitForm debounce gap it exists to close.
    @State private var autosaveGeneration = 0
    @State private var isSubmitting = false
    // Surfaced inline near the sync status, separate from loadError (the
    // job's own parse problem) and fetchError (a failed initial load) — this
    // is specifically a failed submit attempt, e.g. the server's 409 on an
    // empty spec.
    @State private var submitError: String?
    // Attachment upload errors and a brief post-insert confirmation, shown
    // near the spec editor — separate from loadError/submitError since this
    // is neither a parse problem nor a submit failure.
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
            .buttonStyle(.glassProminent)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background { ThemeBackgroundView() }
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
                    Text("Created — fill in the spec below; it autosaves as you type. Start arms once it's synced.")
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
                    suggestionChips(repoSuggestions) { repo = $0 }
                }
                TextField("base ref (optional, default origin/main)", text: $base)
                    .autocorrectionDisabled()
                    .font(.data(13))
                    .kField()
                    .listRowBackground(Color.clear)
            }
            kSection("Model") {
                TextField("model for zeus (optional, default = claude-fable-5)", text: $model)
                    .autocorrectionDisabled()
                    .textInputAutocapitalization(.never)
                    .font(.data(13))
                    .kField()
                    .listRowBackground(Color.clear)
                // suggestion chips, same pattern as the repo suggestions above
                suggestionChips(["fable", "opus", "sonnet", "haiku"]) { model = $0 }
            }
            kSection("Color") {
                // Deliberately NOT gated on `editable` and deliberately not
                // part of autosave: the strip posts to /api/job/color, which
                // neither full-replaces the job file nor applies saveform's
                // cell-in-flight gate. Same picker JobDetailView shows —
                // see JobColorStrip's header for why it is shared.
                JobColorStrip(baseURL: baseURL, slug: slug, serverColor: color) { saved in
                    color = saved
                }
                .listRowBackground(Color.clear)
            }
            kSection("Flags") {
                Toggle("Solo (no subagent crew)", isOn: $solo)
                    .font(.voice(13))
                    .foregroundStyle(KTheme.text)
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
            if let submitError, !submitError.isEmpty {
                kSection { Text(submitError).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle(slug)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton(label: "Cancel") { dismiss() }
            }
            // Explicit, tap-only arming — no auto-start on type. Disabled
            // until the spec has real content and the latest edit has
            // actually landed server-side (not mid-flight, not failed), so a
            // tap can never race a still-syncing keystroke. It also stays
            // disabled for as long as the submit itself is in flight, which
            // is now seconds rather than an instant: the POST starts the cell
            // before it answers.
            ToolbarItem(placement: .primaryAction) {
                Button {
                    submitTapped()
                } label: {
                    Label("Start", systemImage: "play.fill")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Start")
                .buttonStyle(.glassProminent)
                .tint(KTheme.accent)
                .disabled(!canSubmitForm || isSubmitting)
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    finishEditing()
                } label: {
                    Label("Done", systemImage: "checkmark")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Done")
                .buttonStyle(.glassProminent)
                .disabled(isUploading)
            }
        }
        // autosaveSnapshotKey covers every autosaved field. It used to exempt
        // `ready`, which took an immediate flush of its own; that flag went
        // with the queued state, so there is one debounce path again.
        .onChange(of: autosaveSnapshotKey) { scheduleAutosave() }
    }

    /// The repo/model suggestion strips: KChip-shaped capsules on glass.
    ///
    /// Still `.plain` buttons rather than `.bordered`/`.glass` ones — the
    /// doubled-background blob a bordered button style renders inside a List
    /// row is the same Form quirk that forced the hand-built chip originally.
    /// Only the geometry changed: a sharp `Rectangle` stroke became a tinted
    /// glass capsule.
    private func suggestionChips(_ values: [String], onPick: @escaping (String) -> Void) -> some View {
        ScrollView(.horizontal, showsIndicators: false) {
            GlassEffectContainer(spacing: 6) {
                HStack(spacing: 6) {
                    ForEach(values, id: \.self) { value in
                        Button {
                            onPick(value)
                        } label: {
                            Text(value)
                                .font(.data(11, weight: .semibold))
                                .foregroundStyle(KTheme.accent)
                                .padding(.horizontal, 12)
                                .padding(.vertical, 6)
                                .contentShape(Capsule())
                        }
                        .buttonStyle(.plain)
                        .glassEffect(.regular.tint(KTheme.accent.opacity(0.22)), in: Capsule())
                    }
                }
                .padding(.vertical, 2)
            }
        }
        .listRowBackground(Color.clear)
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

    // Gates the toolbar Start button: editable, real spec content, no image
    // upload pending insert, and no autosave still in flight or waiting out
    // its debounce (autosaveTask non-nil covers the debounce gap; the
    // syncState checks cover the in-flight request itself and a just-failed
    // one) — a tap can only ever fire against a spec the server has already
    // accepted. That matters more than it did: the tap now starts the cell
    // rather than parking the job in a queue for the daemon to pick up.
    private var canSubmitForm: Bool {
        guard editable, !spec.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return false }
        guard !isUploading else { return false }
        guard autosaveTask == nil else { return false }
        switch syncState {
        case .syncing, .failed: return false
        case .idle, .synced: return true
        }
    }

    // Every autosaved field — a snapshot string so a single onChange can
    // watch them all at once, the same idiom SpecSection uses for its
    // `.task(id:)` key. `color` is deliberately NOT here: it doesn't ride on
    // saveform's full-replace payload at all, it posts to its own endpoint on
    // tap, so including it would fire a pointless (and cell-gated) form save.
    private var autosaveSnapshotKey: String {
        "\(repo)|\(base)|\(model)|\(solo)|\(spec)"
    }

    private func load() async {
        isLoading = true
        fetchError = nil
        do {
            let f = try await APIClient.shared.fetchJobForm(baseURL: baseURL, slug: slug)
            repo = f.repo
            base = f.base
            solo = f.solo
            model = f.model ?? ""
            color = f.color ?? ""
            spec = f.spec
            mtime = f.mtime
            editable = f.editable
            loadError = f.err.isEmpty ? nil : f.err
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
    // by the Start button, which must not fire against a spec still sitting
    // behind the debounce timer, and by Done flushing a pending edit before
    // closing.
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
            slug: slug, repo: repo, base: base, solo: solo,
            model: model.trimmingCharacters(in: .whitespaces),
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

    // The toolbar's Start action: flushes any pending edit through the
    // immediate-save path, waits for that specific save to land, then
    // submits — matching the web's submit-then-close behavior. The submit
    // itself starts the cell before it answers, so this Task is live for
    // seconds; `isSubmitting` keeps the button disabled for exactly that
    // long. A 409 (an empty spec slipping through a race, an unresolvable
    // repo, a server-side rule the client doesn't know about) surfaces
    // inline next to the sync status rather than dismissing.
    private func submitTapped() {
        guard canSubmitForm, !isSubmitting else { return }
        submitError = nil
        isSubmitting = true
        flushAutosave()
        let pending = autosaveTask
        Task {
            await pending?.value
            if case .failed(let message) = syncState {
                isSubmitting = false
                submitError = "autosave failed, can't start: \(message)"
                return
            }
            do {
                try await APIClient.shared.queueJob(baseURL: baseURL, slug: slug)
                isSubmitting = false
                onSaved()
            } catch {
                isSubmitting = false
                submitError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
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
