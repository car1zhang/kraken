import AppKit
import SwiftUI

// EditorForm.swift — the job editor. THREE modes:
//
//   jobKey == nil          the new-job row: a slug field and "create"
//   jobKey, editable       the form — repo/base/model/colour/solo/prereqs/spec
//   jobKey, !editable      the same form, every field locked: a cell is in
//                          flight and the file belongs to the crew
//
// AUTOSAVE, not a save button: any change debounces 600ms into
// `POST /api/job/saveform` with **mtime: 0**, which deliberately opts OUT of
// the optimistic lock. Not an oversight — the editor is the only writer of the
// file while it is open, and a conflict dialog mid-typing is worse than
// last-writer-wins on a single-user board.

struct EditorForm: View {
    /// nil opens the new-job row. Otherwise a jobKey — `archive:<slug>` for an
    /// archived entry, the bare slug for everything else.
    let jobKey: String?

    @EnvironmentObject private var board: BoardStore
    @EnvironmentObject private var router: OverlayRouter
    @Environment(\.hudTheme) private var theme

    @State private var newSlug = ""
    @State private var note = ""
    @State private var noteTone: NoteTone = .plain

    @State private var form: JobForm?
    @State private var repo = ""
    @State private var base = ""
    @State private var model = ""
    @State private var solo = false
    @State private var prereqs: [PrereqRow] = []
    @State private var spec = ""

    /// The freshest save/load says the current spec won't run (`loadForm`'s
    /// `err`, or a save's `warn`) — one of the queue button's arming gates.
    @State private var syncWarn = false
    @State private var busy = false
    @State private var firstPass = true
    @State private var saveTask: Task<Void, Never>?

    @State private var baselineState: String?
    @State private var captured = false
    @State private var pasteClaim: UUID?
    @State private var pendingColor: String?
    @State private var nextPrereqID = 0

    @StateObject private var handle = ComposerHandle()
    @State private var uploader: AttachmentUploader?

    enum NoteTone { case plain, dim, ok }

    /// One prereq row. `id` is a client-side identity and is never sent: `job`
    /// cannot be the row key because "+ add" appends an EMPTY row and two empty
    /// rows would collide, which reconciles the wrong row away on removal.
    struct PrereqRow: Identifiable, Equatable {
        let id: Int
        var job: String
        var gated: Bool
    }

    // MARK: - derived

    private var isNew: Bool { jobKey == nil }

    private var job: WebJob? {
        guard let jobKey else { return nil }
        return board.board?.jobs.first { $0.id == jobKey }
    }

    /// The slug every POST carries. An archived job's KEY is the synthetic
    /// `archive:<slug>`, and unarchive/delete/saveform all take the bare one.
    private var postSlug: String? {
        if let job { return job.slug }
        guard let jobKey else { return nil }
        return jobKey.hasPrefix("archive:") ? String(jobKey.dropFirst("archive:".count)) : jobKey
    }

    private var archived: Bool { job?.archived == true }

    /// `readOnly` is forced by ARCHIVED first, and the ordering is load-bearing
    /// rather than stylistic. An archived job has no job file, so there is no
    /// form to load — which means `form` stays nil, which means the
    /// `form != nil && !editable` rule below would answer FALSE and leave the
    /// paste layer and the panel drop ARMED. A ⌘V there would upload against a
    /// job file that does not exist.
    private var readOnly: Bool {
        archived || (form != nil && form?.editable != true)
    }

    private var formVisible: Bool { !isNew && form != nil }
    /// Missing from the board means JUST CREATED — treat that as a fresh draft
    /// rather than as unarmable.
    private var state: String { job?.state ?? "draft" }
    private var live: Bool { job?.live ?? false }
    /// A board-level error (an unhealthy cell), distinct from `syncWarn`'s
    /// form-level one.
    private var boardErr: Bool { !(job?.err ?? "").isEmpty }

    private var queuedMode: Bool { formVisible && !readOnly && state == "queued" && !live }
    private var draftMode: Bool {
        formVisible && !readOnly && !queuedMode && state == "draft" && !live
    }
    /// A draft's queue button is always PRESENT — merely disabled until the
    /// spec is non-empty and clean — so the affordance is discoverable from the
    /// moment the modal opens.
    private var armed: Bool {
        draftMode && !spec.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !syncWarn && !boardErr
    }

    private var showReactivate: Bool {
        guard let job else { return false }
        return !archived && job.state == "done" && job.reactivatable == true
    }

    private var title: String {
        if isNew { return "new job" }
        if archived, let job { return job.title + " — archived" }
        return (postSlug ?? "") + ".job.md"
    }

    /// The archived wording wins over the in-flight one — there is no cell.
    private var hint: String {
        if archived { return "read-only · unarchive to edit" }
        if readOnly { return "read-only — cell in flight" }
        if queuedMode { return "queued — cancel returns it to draft" }
        if draftMode {
            if armed { return "synced ✓ — queueable" }
            if spec.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                return "write a spec to queue"
            }
        }
        return ""
    }

    // MARK: - body

    var body: some View {
        VStack(spacing: 0) {
            head
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    if isNew { newJobRow }
                    settledPanels
                    if formVisible { formBody }
                    if !displayNote.isEmpty {
                        Text(displayNote)
                            .hudMono(size: 9)
                            .foregroundStyle(noteColour)
                    }
                }
                .padding(14)
            }
        }
        .hudPanelFileDrop(readOnly ? nil : { uploader?.add($0) })
        .onAppear { start() }
        .onDisappear { stop() }
        .onChange(of: stateToken) { _, _ in checkStale() }
        .onChange(of: formSignature) { _, _ in fieldChanged() }
        .id(jobKey ?? "__new__")
    }

    private var stateToken: String { job.map { $0.state } ?? "\u{0}gone" }

    /// Every value the payload is built from, in one string. This replaces the
    /// web's delegated input listener on `#editor-form` and closes its one gap:
    /// a PROGRAMMATIC write (the image splice) needed its own explicit change
    /// call there, which is exactly the class of bug a derived signature
    /// removes.
    private var formSignature: String {
        [repo, base, model, solo ? "1" : "0", spec,
         prereqs.map { $0.job + ($0.gated ? "!" : "") }.joined(separator: ",")]
            .joined(separator: "\u{0}")
    }

    private var displayNote: String {
        note.isEmpty ? (uploader?.note ?? "") : note
    }

    private var noteColour: Color {
        switch noteTone {
        case .ok: return theme.okColor
        case .dim: return theme.mutedColor
        case .plain: return theme.counterColor
        }
    }

    // MARK: - head

    private var head: some View {
        HudPanelHead(title) {
            if !hint.isEmpty {
                Text(hint).hudMono(size: 9).foregroundStyle(theme.mutedColor).lineLimit(1)
            }
            Spacer(minLength: 8)
            if showReactivate {
                // Reactivate is a COMPOSER flow, not a one-shot post, so it
                // hands off to the steer inspector exactly as the card's own ↺
                // does — never a bare POST.
                HudButton("↺ reactivate", style: .ghost) {
                    guard let slug = postSlug else { return }
                    router.close(.editor)
                    router.openSteer(slug: slug, mode: .reactivate)
                }
                .help("resume this done job's original zeus session in a fresh cell")
            }
            if formVisible && !readOnly {
                // archive lives HERE, inside the opened job, on purpose: there
                // is no card-level shortcut
                HudButton("archive", style: .ghost) { doArchive() }
                    .help(
                        "move this job (and its preserved artifacts) into the archive — frees the slug; reversible from the ARCHIVED drawer under DONE"
                    )
            }
            if queuedMode {
                HudButton("✕ cancel queue", style: .ghost) { doDequeue() }
                    .disabled(busy)
                    .help("cancel — return this queued job to draft (spec is kept)")
            }
            if draftMode {
                HudButton("queue ⌘⏎") { doQueue() }
                    .disabled(!armed || busy)
                    .help("mark this job ready and queue it — same move as dragging the card to QUEUED")
            }
        }
    }

    // MARK: - new job

    private var newJobRow: some View {
        HStack(spacing: 8) {
            TextField("job-slug", text: $newSlug)
                .textFieldStyle(.plain)
                .focusEffectDisabled()
                .hudMono(size: 11)
                .foregroundStyle(theme.textColor)
                .padding(.horizontal, 7)
                .padding(.vertical, 5)
                .background(theme.surfaceColor)
                .hudCut(CornerCuts.slugInput, border: theme.borderColor)
                .onSubmit { doCreate() }
            HudButton("create") { doCreate() }
        }
    }

    // MARK: - the settled-job panels

    /// Artifacts, questions and feedback — ABOVE the form, not below it.
    ///
    /// On a done job they are what you clicked in for, and the spec box would
    /// push them past the fold. This is also the ONLY route to a done or
    /// archived job's docs: the card carries a bare count, and a settled job
    /// never opens the steer inspector.
    @ViewBuilder
    private var settledPanels: some View {
        if let job {
            let artifacts = job.artifacts ?? []
            let docq = job.docQuestions ?? []
            let feedback = job.feedback ?? []
            if !artifacts.isEmpty {
                section("artifacts") {
                    ArtifactChipRow(artifacts: artifacts, label: job.title, jobKey: job.id)
                }
            }
            if !docq.isEmpty {
                section("questions") {
                    DocQuestionsPanel(slug: job.slug, questions: docq)
                }
            }
            if !feedback.isEmpty {
                section("feedback") {
                    FeedbackPanel(slug: job.slug, entries: feedback)
                }
            }
        }
    }

    private func section<Content: View>(
        _ label: String, @ViewBuilder content: () -> Content
    ) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            Text(label)
                .hudDisplay(size: 9, tracking: HudTracking.stat)
                .foregroundStyle(theme.mutedColor)
            content()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    // MARK: - the form

    private var formBody: some View {
        VStack(alignment: .leading, spacing: 10) {
            fieldRow
            prereqBlock
            specBlock
            if let uploader { AttachmentBar(uploader: uploader) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var fieldRow: some View {
        HStack(alignment: .top, spacing: 12) {
            labelled("repo") {
                // HudDropdown appends an off-list current selection itself, so
                // an existing job with an unusual repo still round-trips
                // instead of silently resetting to "—".
                HudDropdown(options: form?.repos ?? [], emptyLabel: "—", selection: $repo)
                    .disabled(readOnly)
            }
            labelled("base") {
                plainField("origin/main", $base).frame(width: 130)
            }
            labelled("model") {
                HStack(spacing: 4) {
                    plainField("claude-fable-5", $model).frame(width: 130)
                    // `<datalist>` has no SwiftUI equivalent (53-concessions
                    // B): free text stays free, and the known values move into
                    // a menu beside the field rather than disappearing.
                    Menu("▾") {
                        ForEach(Self.modelSuggestions, id: \.self) { m in
                            Button(m) { model = m }
                        }
                    }
                    .menuStyle(.borderlessButton)
                    .frame(width: 22)
                    .disabled(readOnly)
                }
                .help(
                    "claude model for zeus (--model value) — applied once at cell start; empty = kraken's default for zeus, claude-fable-5 (the crew it spawns is opus regardless)"
                )
            }
            labelled("color") {
                // The ONE control not disabled while read-only, and deliberately
                // so: everything else here is job-file state the crew owns while
                // a cell is in flight, so the server refuses it — whereas a
                // colour is carl's own label on the card riding its own ungated
                // endpoint, and recolouring a RUNNING job is the main thing the
                // feature is for.
                SwatchStrip(value: pendingColor ?? job?.color) { colour in
                    pickColor(colour)
                }
            }
            Toggle(isOn: $solo) {
                Text("solo").hudMono(size: 9).foregroundStyle(theme.bodyColor)
            }
            .toggleStyle(HudCheckToggleStyle())
            .disabled(readOnly)
            Spacer(minLength: 0)
        }
    }

    private static let modelSuggestions = [
        "claude-fable-5", "claude-opus-5", "fable", "opus", "sonnet", "haiku",
    ]

    private var prereqBlock: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 8) {
                Text("prereqs")
                    .hudDisplay(size: 9, tracking: HudTracking.stat)
                    .foregroundStyle(theme.mutedColor)
                HudButton("+ add", style: .ghost) {
                    prereqs.append(PrereqRow(id: nextPrereqID, job: "", gated: false))
                    nextPrereqID += 1
                }
                .disabled(readOnly)
                Spacer(minLength: 0)
            }
            ForEach($prereqs) { $row in
                HStack(spacing: 8) {
                    plainField("job-slug", $row.job).frame(width: 200)
                    Toggle(isOn: $row.gated) {
                        Text("gated ⚑").hudMono(size: 9).foregroundStyle(theme.bodyColor)
                    }
                    .toggleStyle(HudCheckToggleStyle())
                    .disabled(readOnly)
                    HudButton("✕", style: .ghost) {
                        prereqs.removeAll { $0.id == row.id }
                    }
                    .disabled(readOnly)
                    .help("remove")
                    Spacer(minLength: 0)
                }
            }
        }
    }

    private var specBlock: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("spec — zeus' initial prompt, as fleshed out as possible")
                .hudDisplay(size: 9, tracking: HudTracking.stat)
                .foregroundStyle(theme.mutedColor)
            if !readOnly {
                CommandChipRow(text: $spec, handle: handle)
            }
            ComposerTextView(
                text: $spec,
                theme: theme,
                isEditable: !readOnly,
                onSubmit: { if armed && !busy { doQueue() } },
                handle: handle
            )
            .frame(minHeight: 220)
            .background(theme.surfaceColor)
            .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
        }
    }

    private func labelled<Content: View>(
        _ label: String, @ViewBuilder content: () -> Content
    ) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(label)
                .hudDisplay(size: 9, tracking: HudTracking.stat)
                .foregroundStyle(theme.mutedColor)
            content()
        }
    }

    private func plainField(_ placeholder: String, _ binding: Binding<String>) -> some View {
        TextField(placeholder, text: binding)
            .hudField()
            .disabled(readOnly)
    }

    // MARK: - lifecycle

    private func start() {
        guard let slug = postSlug else { return }
        // The reference is spliced into the spec text IMMEDIATELY and the chip
        // is only visual confirmation: removing it does NOT remove the text,
        // because that text is now user-editable prose in the spec. (Steer is
        // the opposite — there the chips ARE the message.)
        let up = AttachmentUploader(slug: slug) { chip in
            spec = handle.insert(
                "[image: \(chip.path)]\n", into: spec, ownLineWhenUnfocused: true
            )
        }
        uploader = up
        // steer OUTRANKS the editor, and the editor stays OUT of the chain
        // entirely while read-only.
        if !readOnly {
            pasteClaim = router.claimPaste(.editor) { images in up.add(images) }
        }
        // an archived job has no job file — asking for one 404s and paints
        // "no such job" over a view that is working exactly as intended
        guard !archived else { return }
        Task { await load(slug) }
    }

    private func stop() {
        saveTask?.cancel()
        if let id = pasteClaim { router.releasePaste(id) }
        pasteClaim = nil
    }

    private func load(_ slug: String) async {
        do {
            let d = try await APIClient.shared.fetchJobForm(baseURL: InspectorAPI.baseURL, slug: slug)
            form = d
            repo = d.repo
            base = d.base
            model = d.model ?? ""
            solo = d.solo
            prereqs = d.prereqs.enumerated().map { i, p in
                PrereqRow(id: i, job: p.job, gated: p.gated)
            }
            nextPrereqID = d.prereqs.count
            spec = d.spec
            syncWarn = !d.err.isEmpty
            if !d.err.isEmpty {
                noteTone = .plain
                note = "current file won't run: " + d.err
            }
        } catch {
            noteTone = .plain
            note = error.localizedDescription
        }
    }

    private func fieldChanged() {
        guard !isNew, !readOnly, form != nil else { return }
        if firstPass {
            firstPass = false  // the values just LOADED — not an edit
            return
        }
        noteTone = .dim
        note = "syncing…"
        scheduleSave()
    }

    private func scheduleSave() {
        saveTask?.cancel()
        saveTask = Task {
            try? await Task.sleep(nanoseconds: 600_000_000)
            guard !Task.isCancelled else { return }
            await save()
        }
    }

    @discardableResult
    private func save() async -> Bool {
        guard let slug = postSlug, !readOnly, let form else { return false }
        let body = JobFormSaveRequest(
            slug: slug,
            repo: repo.trimmingCharacters(in: .whitespaces),
            base: base.trimmingCharacters(in: .whitespaces),
            // the ready row is gone from the form; it round-trips as loaded and
            // the queue button is what actually sets it
            ready: form.ready,
            solo: solo,
            model: model.trimmingCharacters(in: .whitespaces),
            prereqs: prereqs
                .filter { !$0.job.trimmingCharacters(in: .whitespaces).isEmpty }
                .map { Prereq(job: $0.job.trimmingCharacters(in: .whitespaces), gated: $0.gated) },
            spec: spec,
            mtime: 0  // no optimistic lock — see the file header
        )
        do {
            let d = try await APIClient.shared.saveJobForm(baseURL: InspectorAPI.baseURL, form: body)
            syncWarn = !d.warn.isEmpty
            noteTone = d.warn.isEmpty ? .ok : .plain
            note = d.warn.isEmpty ? "synced ✓" : "synced — won't run: " + d.warn
            return true
        } catch {
            noteTone = .plain
            note = "sync failed: " + error.localizedDescription
            return false
        }
    }

    /// Same rule as the steer inspector's, with one wrinkle: capture is LAZY.
    /// A freshly-created job is not on the board yet when the form first loads
    /// for it, and misreading "not in the payload yet" as a state change would
    /// close the editor on the job the user just created.
    private func checkStale() {
        guard !isNew else { return }
        guard captured else {
            if let job {
                baselineState = job.state
                captured = true
            }
            return
        }
        if job == nil || job?.state != baselineState { router.close(.editor) }
    }

    // MARK: - actions

    private func doQueue() {
        guard let slug = postSlug else { return }
        if (uploader?.pending ?? 0) > 0 {
            noteTone = .plain
            note = "uploading image…"
            return
        }
        busy = true
        // land any edit still sitting in the debounce window BEFORE queueing —
        // cancel first so the pending timer cannot also fire and post twice
        saveTask?.cancel()
        Task {
            await save()
            let err = await board.mutate(path: "/api/queue", body: ["slug": slug])
            busy = false
            if err == nil {
                router.close(.editor)
            } else {
                noteTone = .plain
                note = "queue failed — see status bar"
            }
        }
    }

    private func doDequeue() {
        guard let slug = postSlug else { return }
        busy = true
        // Rebase the staleness baseline BEFORE posting: `mutate` awaits the
        // board refresh, so the stale check runs against the new queued→draft
        // state while we are still awaiting — a stale "queued" baseline there
        // would read our OWN action as someone else's change and close the
        // editor out from under it.
        let prevState = baselineState
        let prevCaptured = captured
        baselineState = "draft"
        captured = true
        Task {
            let err = await board.mutate(path: "/api/dequeue", body: ["slug": slug])
            busy = false
            if err != nil {
                baselineState = prevState
                captured = prevCaptured
                noteTone = .plain
                note = "cancel failed — see status bar"
            }
        }
    }

    private func doArchive() {
        guard let slug = postSlug else { return }
        Task {
            let err = await board.mutate(path: "/api/archive", body: ["slug": slug])
            if err == nil {
                router.close(.editor)
            } else {
                noteTone = .plain
                note = "archive failed — see status bar"
            }
        }
    }

    private func doCreate() {
        let slug = newSlug.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !slug.isEmpty else {
            noteTone = .plain
            note = "give it a slug"
            return
        }
        Task {
            do {
                let created = try await APIClient.shared.createJob(
                    baseURL: InspectorAPI.baseURL, slug: slug
                )
                await board.refetch()
                // re-point the editor at the job that now exists; the host
                // rebuilds this view under a new identity, which is the
                // wholesale reset the new-job row needs
                router.openEditor(jobKey: created.slug)
            } catch {
                noteTone = .plain
                note = error.localizedDescription
            }
        }
    }

    /// Deliberately NOT the debounced form autosave every other control rides:
    /// `saveform` refuses any job whose cell is live — which is exactly when
    /// recolouring is useful — and a full-replace save from an older client
    /// could wipe the colour by omitting the key.
    ///
    /// The optimistic pick is held only while the POST is in flight and cleared
    /// on BOTH outcomes, which hands authority back to the board payload: a
    /// REFUSED pick snaps back to whatever the server actually has, so the
    /// strip can never sit showing a colour the server rejected.
    private func pickColor(_ colour: String) {
        guard let slug = postSlug else { return }
        pendingColor = colour
        Task {
            _ = await board.mutate(path: "/api/job/color", body: ["slug": slug, "color": colour])
            pendingColor = nil
        }
    }
}
