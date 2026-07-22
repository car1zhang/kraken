import SwiftUI
import PhotosUI

// MARK: - image attachments
//
// One acquired-and-uploaded image on a composer (steer/unblock, and the
// spec editor in JobFormView.swift — ReactivateView deliberately has no
// attachment bar). `path` is the absolute
// server-side path embedded in outgoing text as "[image: <path>]"; `url` is
// the server-relative thumbnail fetch used only when no local `thumbnail`
// is available. Not private — JobFormView.swift shares this model and the
// picker bar below rather than duplicating the upload plumbing.
struct ComposerAttachment: Identifiable {
    let id = UUID()
    let name: String
    let path: String
    let url: String
    let thumbnail: UIImage?
}

/// PhotosPicker + paste-image affordance + upload progress, shared by
/// SteerView's composer and JobFormView's spec editor. Owns no attachment
/// list itself — each acquired image uploads immediately and is handed to
/// the caller via `onUploaded`, so SteerView can collect them into a chips
/// row while JobFormView instead inserts straight into the spec text.
struct ImageAttachmentBar: View {
    let baseURL: String
    let slug: String
    @Binding var isUploading: Bool
    var onUploaded: (ComposerAttachment) -> Void
    var onError: (String) -> Void

    @State private var pickerItems: [PhotosPickerItem] = []

    var body: some View {
        HStack(spacing: 12) {
            PhotosPicker(selection: $pickerItems, matching: .images) {
                Label("Add Image", systemImage: "photo.badge.plus")
            }
            .font(.voice(12))
            .foregroundStyle(KTheme.accent)
            .onChange(of: pickerItems) { _, items in
                guard !items.isEmpty else { return }
                let toLoad = items
                pickerItems = []
                Task { await loadAndUpload(toLoad) }
            }

            if UIPasteboard.general.hasImages {
                Button {
                    pasteImage()
                } label: {
                    Label("Paste", systemImage: "doc.on.clipboard")
                }
                .buttonStyle(.plain)
                .font(.voice(12))
                .foregroundStyle(KTheme.accent)
            }

            if isUploading {
                ProgressView().controlSize(.small)
            }
        }
    }

    private func pasteImage() {
        guard let image = UIPasteboard.general.image else { return }
        Task { await upload(image: image, suggestedName: "pasted-\(Int(Date().timeIntervalSince1970))") }
    }

    private func loadAndUpload(_ items: [PhotosPickerItem]) async {
        for item in items {
            guard let data = try? await item.loadTransferable(type: Data.self),
                  let image = UIImage(data: data) else {
                onError("couldn't load image")
                continue
            }
            await upload(image: image, suggestedName: item.itemIdentifier ?? "image")
        }
    }

    // The picker's native format is often HEIC, which the server rejects —
    // every acquired image is re-encoded to JPEG before upload regardless of
    // its original source format, sidestepping the HEIC/PNG/etc. question
    // entirely rather than sniffing and branching per format.
    private func upload(image: UIImage, suggestedName: String) async {
        guard let jpegData = image.jpegData(compressionQuality: 0.9) else {
            onError("couldn't encode image")
            return
        }
        isUploading = true
        defer { isUploading = false }
        do {
            let resp = try await APIClient.shared.uploadImage(
                baseURL: baseURL, slug: slug, data: jpegData,
                filename: "\(suggestedName).jpg", mimeType: "image/jpeg"
            )
            onUploaded(ComposerAttachment(name: resp.name, path: resp.path, url: resp.url, thumbnail: image))
        } catch {
            onError((error as? KrakenError)?.errorDescription ?? error.localizedDescription)
        }
    }
}

/// One horizontal-scrolling row of attachment thumbnails with a ✕ to remove
/// each client-side. Not private — reused verbatim if JobFormView ever wants
/// a chips row too, though it currently opts for direct text insertion.
struct AttachmentChipsRow: View {
    let baseURL: String
    let attachments: [ComposerAttachment]
    var onRemove: (ComposerAttachment) -> Void

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(attachments) { att in
                    AttachmentChip(attachment: att, baseURL: baseURL) { onRemove(att) }
                }
            }
        }
    }
}

private struct AttachmentChip: View {
    let attachment: ComposerAttachment
    let baseURL: String
    var onRemove: () -> Void

    private var thumbnailURL: URL? {
        let trimmed = baseURL.hasSuffix("/") ? String(baseURL.dropLast()) : baseURL
        return URL(string: trimmed + attachment.url)
    }

    var body: some View {
        VStack(spacing: 2) {
            ZStack(alignment: .topTrailing) {
                Group {
                    if let thumbnail = attachment.thumbnail {
                        Image(uiImage: thumbnail).resizable().scaledToFill()
                    } else {
                        AsyncImage(url: thumbnailURL) { image in
                            image.resizable().scaledToFill()
                        } placeholder: {
                            KTheme.raised
                        }
                    }
                }
                .frame(width: 56, height: 56)
                .clipped()
                .overlay(Rectangle().stroke(KTheme.hairline2, lineWidth: 1))

                Button(action: onRemove) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.white, KTheme.counter)
                }
                .accessibilityLabel("Remove \(attachment.name)")
                .offset(x: 6, y: -6)
            }
            Text(attachment.name)
                .font(.data(8))
                .foregroundStyle(KTheme.text2)
                .lineLimit(1)
                .frame(width: 56)
        }
    }
}

// MARK: - artifact folder tree
//
// The board now sends nested artifact names as relative paths (e.g.
// "icon-variations/icon-v1.png"), one flat array. This groups them into a
// browsable tree so the Artifacts section can show folders instead of a
// flat list of slash-separated names. No fixed depth cap: each recursion
// level strips exactly one leading path component, so "a/b/c.png" nests
// three levels deep the same way "a/c.png" nests one — arbitrarily deep
// paths degrade gracefully to more folders, never a crash or a raw path
// string standing in for a real row.
private enum ArtifactNode: Identifiable {
    case file(WebArtifact)
    case folder(name: String, children: [ArtifactNode])

    var id: String {
        switch self {
        case .file(let a): return "file:\(a.url)"
        case .folder(let name, _): return "folder:\(name)"
        }
    }
}

/// Groups a flat artifact list by leading path component, recursively. A
/// name with no "/" is a top-level file; "icon-variations/icon-v1.png"
/// becomes a "icon-variations" folder containing a file named "icon-v1.png"
/// (the prefix is stripped at each level, so nested folders always show
/// just the next path segment, not the accumulated path).
private func artifactTree(_ artifacts: [WebArtifact]) -> [ArtifactNode] {
    var topFiles: [WebArtifact] = []
    var groups: [String: [WebArtifact]] = [:]
    var groupOrder: [String] = []

    for a in artifacts {
        guard let slash = a.name.firstIndex(of: "/") else {
            topFiles.append(a)
            continue
        }
        let folder = String(a.name[..<slash])
        let rest = String(a.name[a.name.index(after: slash)...])
        if groups[folder] == nil { groupOrder.append(folder) }
        groups[folder, default: []].append(WebArtifact(name: rest, url: a.url))
    }

    var nodes: [ArtifactNode] = topFiles.map { .file($0) }
    for folder in groupOrder {
        nodes.append(.folder(name: folder, children: artifactTree(groups[folder] ?? [])))
    }
    return nodes
}

/// Total file count under a folder, counting nested subfolders' files too —
/// so a folder's displayed count reflects everything inside it, not just
/// its direct children.
private func artifactFileCount(_ nodes: [ArtifactNode]) -> Int {
    nodes.reduce(0) { total, node in
        switch node {
        case .file: return total + 1
        case .folder(_, let children): return total + artifactFileCount(children)
        }
    }
}

// A recursive View STRUCT, not a @ViewBuilder function: a function returning
// `some View` can't call itself (the opaque type would be defined in terms of
// itself), but a struct referencing itself in its own body is fine. A file
// opens via onOpen; a folder is a DisclosureGroup whose children are more
// ArtifactRows, so arbitrary nesting depth just falls out of the recursion.
private struct ArtifactRow: View {
    let node: ArtifactNode
    let onOpen: (WebArtifact) -> Void

    var body: some View {
        switch node {
        case .file(let a):
            Button {
                onOpen(a)
            } label: {
                Label(a.name, systemImage: "doc.text")
            }
            .buttonStyle(.plain)
            .font(.data(13))
            .foregroundStyle(KTheme.accent)
        case .folder(let name, let children):
            DisclosureGroup {
                ForEach(children) { child in
                    ArtifactRow(node: child, onOpen: onOpen)
                }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: "folder")
                        .foregroundStyle(KTheme.text2)
                    Text(name)
                        .font(.data(12, weight: .semibold))
                        .foregroundStyle(KTheme.text)
                    Spacer()
                    Text("\(artifactFileCount(children))")
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                }
            }
        }
    }
}

/// Caps a top-level artifact listing at 3 rows, revealing the rest behind a
/// "show all (N)" toggle. N counts top-level nodes only — a folder counts as
/// one row here even though it may hold many files, and a folder that's
/// visible (whether in the first 3 or revealed by the toggle) keeps its own
/// DisclosureGroup, unaffected by this view's expand state.
private struct CollapsedArtifactList: View {
    let artifacts: [WebArtifact]
    let onOpen: (WebArtifact) -> Void

    @State private var expanded = false

    private var nodes: [ArtifactNode] { artifactTree(artifacts) }

    var body: some View {
        let visible = expanded ? nodes : Array(nodes.prefix(3))
        ForEach(visible) { node in
            ArtifactRow(node: node, onOpen: onOpen)
        }
        if nodes.count > 3 {
            Button {
                expanded.toggle()
            } label: {
                HStack(spacing: 4) {
                    Text(expanded ? "show less" : "show all (\(nodes.count))")
                    Image(systemName: expanded ? "chevron.up" : "chevron.down")
                }
            }
            .buttonStyle(.plain)
            .font(.data(12))
            .foregroundStyle(KTheme.accent)
        }
    }
}

/// One PR chip — "#<N> <state>", tappable to open the PR in the browser.
/// Not private: BoardView's JobCardView renders the same chip on the board
/// card, so both call sites share one styling/parsing source of truth.
/// Colors reuse KTheme rather than new hex values: merged reads like the
/// accent purple, open like the solved green, closed like muted text2 —
/// mirroring BoardTheme.color(forState:)'s existing semantics as closely as
/// a 3-state PR maps onto a 5-state job.
struct PRChip: View {
    let pr: JobPR

    // Pulls the PR number out of ".../pull/<N>" (with or without a trailing
    // path); falls back to just the state word if the url doesn't match
    // (e.g. a non-GitHub host), per spec.
    private var number: String? {
        guard let range = pr.url.range(of: "/pull/") else { return nil }
        let digits = pr.url[range.upperBound...].prefix { $0.isNumber }
        return digits.isEmpty ? nil : String(digits)
    }

    private var label: String {
        if let number { return "#\(number) \(pr.state)" }
        return pr.state
    }

    private var color: Color {
        switch pr.state {
        case "merged": return KTheme.accent
        case "open": return KTheme.solved
        case "closed": return KTheme.text2
        default: return KTheme.text2
        }
    }

    var body: some View {
        Group {
            if let url = URL(string: pr.url) {
                Link(destination: url) { chipLabel }
            } else {
                chipLabel
            }
        }
    }

    private var chipLabel: some View {
        Text(label)
            .font(.data(10, weight: .semibold))
            .foregroundStyle(color)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .overlay(Rectangle().stroke(color, lineWidth: 1))
    }
}

/// Zeus + crew status chips: a "zeus ●/⚑/○" pill first (only when the server
/// sent a zeus status), then one chip per crew role present on the roster —
/// fixed order hephaestus, athena, hermes, other — each reading "<role>
/// <working>/<total>". Not private: both BoardView's JobCardView and
/// JobDetailView render the same row, mirroring PRChip's sharing pattern
/// above. Renders EmptyView when there's nothing to show, so a job with no
/// zeus status and no roster (including servers that predate these wire
/// fields entirely) falls back to exactly today's UI.
struct CrewChipsView: View {
    let zeusStatus: String?
    let roster: [WebRosterAgent]

    private static let knownRoles = ["hephaestus", "athena", "hermes"]
    private static let chipOrder = knownRoles + ["other"]

    private var zeusChip: (label: String, color: Color)? {
        guard let zeusStatus, !zeusStatus.isEmpty else { return nil }
        switch zeusStatus {
        case "running": return ("zeus ●", KTheme.solved)
        case "polling": return ("zeus ↻", KTheme.polling)
        case "question": return ("zeus ⚑", KTheme.revealed)
        default: return ("zeus ○", KTheme.text2)
        }
    }

    // Buckets each roster entry into hephaestus/athena/hermes/other (nil or
    // any unrecognized role lands in "other"), counting live-vs-total per
    // bucket for the "<working>/<total>" label.
    private var roleChips: [(role: String, working: Int, total: Int)] {
        var counts: [String: (working: Int, total: Int)] = [:]
        for agent in roster {
            let role = agent.role.flatMap { Self.knownRoles.contains($0) ? $0 : nil } ?? "other"
            var entry = counts[role] ?? (working: 0, total: 0)
            entry.total += 1
            if agent.working == true { entry.working += 1 }
            counts[role] = entry
        }
        return Self.chipOrder.compactMap { role in
            guard let c = counts[role] else { return nil }
            return (role, c.working, c.total)
        }
    }

    var body: some View {
        let zeus = zeusChip
        let chips = roleChips
        if zeus != nil || !chips.isEmpty {
            HStack(spacing: 6) {
                if let zeus {
                    chipLabel(zeus.label, color: zeus.color)
                }
                ForEach(chips, id: \.role) { chip in
                    chipLabel("\(chip.role) \(chip.working)/\(chip.total)",
                              color: chip.working > 0 ? KTheme.solved : KTheme.text2)
                }
            }
        }
    }

    private func chipLabel(_ text: String, color: Color) -> some View {
        Text(text)
            .font(.data(9, weight: .semibold))
            .foregroundStyle(color)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .overlay(Rectangle().stroke(color, lineWidth: 1))
    }
}

/// Formats seconds-until-fire as "Xm Ys", floored at 0 — mirrors app.js's
/// countdown(). Callers compute `seconds` fresh at render time (the board
/// already polls every few seconds, so that's enough granularity; no
/// separate ticker).
func wakeupCountdownLabel(secondsRemaining: Int) -> String {
    if secondsRemaining <= 0 { return "due" }
    let m = secondsRemaining / 60
    let s = secondsRemaining % 60
    return "\(m)m \(s)s"
}

/// The cell's poll-timer chip — "⏱ next check in Xm Ys" — for a job with a
/// pending wakeup. Not private: shared between BoardView's JobCardView and
/// JobDetailView, same reasoning as PRChip above. Cyan-tinted like
/// SessionTheme's "polling" peer-status color.
struct WakeupChip: View {
    let wakeup: WebWakeup

    private var secondsRemaining: Int {
        Int((Double(wakeup.firesAt) / 1000) - Date().timeIntervalSince1970)
    }

    var body: some View {
        Text("⏱ next check in \(wakeupCountdownLabel(secondsRemaining: secondsRemaining))")
            .font(.data(10, weight: .semibold))
            .foregroundStyle(KTheme.polling)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(KTheme.pollingSoft)
    }
}

struct JobDetailView: View {
    let job: WebJob
    let baseURL: String
    var onUpdate: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(AppRouter.self) private var router
    @State private var isBusy = false
    @State private var errorMessage: String?
    @State private var showingKillConfirm = false
    @State private var showingDeleteConfirm = false
    @State private var showingEdit = false
    @State private var showingSteer = false
    @State private var showingReactivate = false
    @State private var showingSteerToast = false
    @State private var showingArtifact: WebArtifact?

    private var column: BoardColumn { BoardTheme.column(for: job) }
    private var isUnhealthy: Bool { !(job.health ?? "").isEmpty }
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
    // app.js's `["working", "blocked"].includes(col) && j.live`.
    private var canKill: Bool {
        job.live && [BoardColumn.working, .blocked].contains(column)
    }
    // webQueue 409s on an unparseable job even though it renders in draft.
    private var canQueue: Bool {
        column == .draft && (job.err ?? "").isEmpty
    }
    // webDequeue 409s once the cell has actually started, so this only shows
    // for a job that's queued but still waiting — not yet live.
    private var canDequeue: Bool {
        !isArchived && !job.live && column == .queued
    }
    // review was folded into blocked — mark-done now lives there, alongside
    // the inline pending-question buttons rendered in the Form itself (see
    // the Question section below — answering is a direct tap, not a sheet).
    private var canMarkDone: Bool {
        column == .blocked
    }
    // Same visibility rule as Kill minus the column restriction: any live
    // cell can take free-text steering, regardless of which of the three
    // live columns it's currently sitting in.
    private var canSteer: Bool {
        job.live
    }
    // A blocked cell's steer affordance is framed as "unblock" rather than
    // steering — same delivery mechanism, different wording so it doesn't
    // read as an optional nudge. Keyed off the derived column (mirrors
    // canKill above) so an unhealthy live cell forced into blocked gets the
    // same framing as a file-state blocked one.
    private var isBlockedColumn: Bool {
        column == .blocked
    }
    private var isArchived: Bool { job.archived == true }
    // Mirrors the server's rule: only a job with no cell in flight can be
    // archived — a live one shows Kill instead (kill returns it to draft,
    // which then archives cleanly).
    private var canArchive: Bool {
        !isArchived && !job.live && [BoardColumn.draft, .queued, .done].contains(column)
    }
    // Reactivate only where the server can actually resume: a done job
    // whose zeus session id was recorded.
    private var canReactivate: Bool {
        !isArchived && column == .done && job.reactivatable == true
    }
    private var hasAnyAction: Bool {
        canQueue || canDequeue || canRetry || canMarkDone || canSteer || isEditable || canKill
            || canArchive || canReactivate || isArchived
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
                    if let wakeup = job.wakeup, job.state == "working" {
                        WakeupChip(wakeup: wakeup)
                    }
                    if let prs = job.prs, !prs.isEmpty {
                        HStack(spacing: 6) {
                            ForEach(prs, id: \.url) { pr in
                                PRChip(pr: pr)
                            }
                        }
                    }
                    fieldRow("Repo", job.repo)
                    if job.live, let cell = job.cell {
                        Button {
                            router.pendingSessionCell = cell
                            router.selectedTab = .sessions
                            dismiss()
                        } label: {
                            HStack {
                                Text("Cell").font(.data(10)).foregroundStyle(KTheme.text2)
                                Spacer()
                                Text(cell).font(.voice(13)).foregroundStyle(KTheme.accent)
                                Image(systemName: "chevron.right")
                                    .font(.data(10))
                                    .foregroundStyle(KTheme.accent)
                            }
                        }
                        .buttonStyle(.plain)
                    }
                    if isArchived, job.title != job.slug {
                        // numbered archive copy: the file base differs from
                        // the original slug shown in the title
                        fieldRow("Archived as", job.slug)
                    }
                }

                if let err = job.err, !err.isEmpty {
                    kSection("Error") { Text(err).font(.voice(13)).foregroundStyle(KTheme.counter) }
                }
                if isUnhealthy {
                    kSection("Health") { Text(job.health ?? "").font(.voice(13)).foregroundStyle(KTheme.revealed) }
                }
                // done/archived only: a reactivated job may still carry a
                // stale outcome in its file — it reads as never-completed
                if let outcome = job.outcome, !outcome.isEmpty, column == .done || isArchived {
                    kSection("Outcome") { Text(outcome).font(.voice(13)).foregroundStyle(KTheme.solved) }
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
                if job.zeusStatus?.isEmpty == false || !(job.roster ?? []).isEmpty {
                    kSection("Roster") {
                        CrewChipsView(zeusStatus: job.zeusStatus, roster: job.roster ?? [])
                        ForEach(Array((job.roster ?? []).enumerated()), id: \.offset) { _, agent in
                            HStack(spacing: 4) {
                                Text(agent.working == true ? "●" : "○")
                                    .foregroundStyle(agent.working == true ? KTheme.solved : KTheme.text2)
                                Text("\(agent.name) (\(agent.model))")
                                    .foregroundStyle(KTheme.text2)
                            }
                            .font(.data(12))
                        }
                    }
                }
                SpecSection(job: job, baseURL: baseURL, editSheetOpen: showingEdit)
                if let detail = job.detail, !detail.isEmpty {
                    kSection("Detail") {
                        DetailCard(text: detail, stateColor: BoardTheme.color(forState: job.state))
                    }
                }
                if let lastSent = job.lastSent, !lastSent.isEmpty {
                    kSection("Last sent") {
                        Text(lastSent)
                            .font(.data(12))
                            .foregroundStyle(KTheme.text2)
                    }
                }
                if let wakeup = job.wakeup, !wakeup.reason.isEmpty, job.state == "working" {
                    kSection("Poll timer") {
                        Text("↻ " + wakeup.reason)
                            .font(.data(12))
                            .foregroundStyle(KTheme.text2)
                    }
                }
                if let question = job.question {
                    kSection("Question") {
                        // Tappable option buttons only work for a single,
                        // single-select question: the server answers with one
                        // digit keystroke, so a multi-select or multi-question
                        // prompt can't be reduced to that (and the server now
                        // rejects the attempt). Those render read-only, with a
                        // pointer to answer from the pane instead.
                        if question.questions.count == 1, let only = question.questions.first, !only.multiSelect {
                            QuestionCard(item: only) { digit in
                                answerQuestion(digit: digit)
                            }
                        } else {
                            ForEach(Array(question.questions.enumerated()), id: \.offset) { _, q in
                                QuestionSummary(item: q)
                            }
                            Text("answer in the pane — k o \(job.cell ?? job.slug)")
                                .font(.voice(11))
                                .foregroundStyle(KTheme.text2)
                                .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 8, trailing: 12))
                                .listRowBackground(Color.clear)
                        }
                    }
                }
                if let artifacts = job.artifacts, !artifacts.isEmpty {
                    kSection("Artifacts") {
                        CollapsedArtifactList(artifacts: artifacts, onOpen: { showingArtifact = $0 })
                    }
                }

                if let errorMessage {
                    kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
                }
            }
            .kListStyle()
        }
        .navigationTitle(job.title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton { dismiss() }
            }
        }
        .disabled(isBusy)
        .onAppear {
            // a deep link like kraken://job/<slug>/steer lands here with the
            // follow-on action still pending — consume it once
            switch router.pendingJobAction {
            case .answer:
                // Answering is now an inline tap on the Question section's
                // option buttons (see body) rather than a separate sheet —
                // this deep link just needs the detail view open, which it
                // already is by the time onAppear runs.
                break
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
                Text(isBlockedColumn ? "sent" : "steering sent")
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
        .confirmationDialog(
            "Delete \(job.title) forever? The archived job file and its artifact copies are permanently removed.",
            isPresented: $showingDeleteConfirm, titleVisibility: .visible
        ) {
            Button("Delete Forever", role: .destructive) {
                runAction { try await APIClient.shared.deleteArchivedJob(baseURL: baseURL, slug: job.slug) }
            }
            Button("Cancel", role: .cancel) {}
        }
        .sheet(isPresented: $showingReactivate) {
            NavigationStack {
                ReactivateView(job: job, baseURL: baseURL) {
                    showingReactivate = false
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
                SteerView(job: job, baseURL: baseURL, unblockFraming: isBlockedColumn) {
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
                            SheetCloseButton { showingArtifact = nil }
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
                if canDequeue {
                    Button {
                        runAction { try await APIClient.shared.dequeueJob(baseURL: baseURL, slug: job.slug) }
                    } label: {
                        Label("Cancel Queue", systemImage: "xmark.circle")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Cancel Queue")
                    .tint(KTheme.text2)
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
                if canMarkDone {
                    Button {
                        runAction { try await APIClient.shared.markDone(baseURL: baseURL, slug: job.slug) }
                    } label: {
                        Label("Mark Done", systemImage: "checkmark")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Mark Done")
                    .tint(KTheme.solved)
                }
                if canSteer {
                    Button {
                        showingSteer = true
                    } label: {
                        if isBlockedColumn {
                            Label("Unblock", systemImage: "lock.open.fill")
                        } else {
                            Label("Steer", systemImage: "arrowshape.turn.up.right.fill")
                        }
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel(isBlockedColumn ? "Unblock" : "Steer")
                    .tint(isBlockedColumn ? KTheme.revealed : KTheme.accent)
                }
                if canReactivate {
                    Button {
                        showingReactivate = true
                    } label: {
                        Label("Reactivate", systemImage: "arrow.counterclockwise")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Reactivate")
                    .tint(KTheme.revealed)
                }
                if canArchive {
                    Button {
                        runAction { try await APIClient.shared.archiveJob(baseURL: baseURL, slug: job.slug) }
                    } label: {
                        Label("Archive", systemImage: "archivebox")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Archive")
                    .tint(KTheme.text2)
                }
                if isArchived {
                    Button {
                        runAction { try await APIClient.shared.unarchiveJob(baseURL: baseURL, slug: job.slug) }
                    } label: {
                        Label("Unarchive", systemImage: "tray.and.arrow.up")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Unarchive")
                    .tint(KTheme.accent)

                    Button(role: .destructive) {
                        showingDeleteConfirm = true
                    } label: {
                        Label("Delete Forever", systemImage: "trash")
                    }
                    .labelStyle(.iconOnly)
                    .accessibilityLabel("Delete Forever")
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
            // Comfortable tap targets — .large gives ~44pt-tall pills; carl
            // found the previous .small pills too small to hit reliably.
            .controlSize(.large)
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

    // One row per artifact tree node: a file opens ArtifactView exactly as
    // before (full relative name + url — the server resolves nested paths,
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

    // Tapping an option in the Question section sends its 1-based index
    // within that question's own options — same busy/error/dismiss handling
    // as every other action.
    private func answerQuestion(digit: Int) {
        runAction { try await APIClient.shared.answerQuestion(baseURL: baseURL, slug: job.slug, digit: digit) }
    }
}

/// The job's spec markdown, read-only — fetched via fetchJobForm (which
/// returns `spec` even when `editable` is false), so a preview is available
/// without opening Edit. A fetch failure just hides the section: this is a
/// nice-to-have preview, not a load-bearing action.
private struct SpecSection: View {
    let job: WebJob
    let baseURL: String
    // Toggling this (the Edit Spec sheet opening or closing) changes the
    // task's id, forcing a refetch — without it, a save inside that sheet
    // would leave this preview showing the pre-edit spec text.
    let editSheetOpen: Bool

    @State private var spec: String?

    var body: some View {
        Group {
            if let spec, !spec.isEmpty {
                kSection("Spec") {
                    DetailCard(text: spec, stateColor: BoardTheme.color(forState: job.state))
                }
            }
        }
        .task(id: "\(job.slug):\(editSheetOpen)") {
            if let form = try? await APIClient.shared.fetchJobForm(baseURL: baseURL, slug: job.slug) {
                spec = form.spec
            }
        }
    }
}

/// The `detail` field: the cell's last words / blocked reason, rendered
/// as a markdown card in the state color — replaces the old escalation body
/// card now that escalations are gone from the wire.
private struct DetailCard: View {
    let text: String
    let stateColor: Color

    var body: some View {
        MarkdownText(markdown: text)
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .cardBackground(stateColor: stateColor)
            .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 4, trailing: 12))
            .listRowBackground(Color.clear)
    }
}

/// One pending question (header + prose + option buttons). Tapping an option
/// answers with its 1-based index within THIS question's own options list —
/// there's no free-text composer anymore, the tap IS the answer, sent
/// straight to POST /api/question.
private struct QuestionCard: View {
    let item: WebQuestionItem
    var onSelect: (Int) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(item.header)
                .font(.data(10, weight: .semibold))
                .foregroundStyle(KTheme.counter)
            Text(item.question)
                .font(.voice(13, weight: .semibold))
                .foregroundStyle(KTheme.text)
            VStack(alignment: .leading, spacing: 6) {
                ForEach(Array(item.options.enumerated()), id: \.offset) { index, option in
                    Button {
                        onSelect(index + 1)
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(option.label)
                                .font(.voice(13, weight: .semibold))
                                .foregroundStyle(KTheme.text)
                            if !option.description.isEmpty {
                                Text(option.description)
                                    .font(.voice(11))
                                    .foregroundStyle(KTheme.text2)
                            }
                        }
                        .padding(10)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .overlay(Rectangle().stroke(KTheme.hairline2, lineWidth: 1))
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .padding(10)
        .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 4, trailing: 12))
        .listRowBackground(KTheme.counterSoft)
    }
}

/// A multi-select or multi-question question's header + prose, read-only —
/// no option buttons, since a single digit keystroke can't answer it (see
/// the count/multiSelect gate above QuestionCard's call site).
private struct QuestionSummary: View {
    let item: WebQuestionItem

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(item.header)
                .font(.data(10, weight: .semibold))
                .foregroundStyle(KTheme.counter)
            Text(item.question)
                .font(.voice(13, weight: .semibold))
                .foregroundStyle(KTheme.text)
        }
        .padding(10)
        .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 4, trailing: 12))
        .listRowBackground(KTheme.counterSoft)
    }
}

/// The reactivate prompt: an optional free-text message folded into the
/// resume prompt of the done job's original zeus session. Mirrors
/// SteerView's shape, but an empty message is fine — reactivating without
/// steering just wakes the session with the standard fresh-branch brief.
private struct ReactivateView: View {
    let job: WebJob
    let baseURL: String
    var onReactivated: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(DraftStore.self) private var draftStore
    @FocusState private var textFocused: Bool
    @State private var text = ""
    @State private var isBusy = false
    @State private var errorMessage: String?

    // Same draft slot as SteerView below — one composer draft per job,
    // shared across steer/unblock/reactivate, since the server clears it on
    // any of those actions succeeding.
    private var draftKey: String { "steer:\(job.slug)" }

    var body: some View {
        Form {
            kSection {
                Text("Resumes the job's original zeus session in a fresh cell (same worktree path, new branch — the old PR is already merged) and moves the job back to working.")
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
            }
            kSection("What to adjust (optional)") {
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
        .navigationTitle("Reactivate \(job.title)")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton(label: "Cancel") { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    send()
                } label: {
                    Label("Reactivate", systemImage: "arrow.counterclockwise")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Reactivate")
                .buttonStyle(.borderedProminent)
                .disabled(isBusy)
            }
        }
        .disabled(isBusy)
        .task {
            await draftStore.hydrate(slug: job.slug)
            text = draftStore.get(draftKey)
        }
        .onChange(of: text) {
            draftStore.set(draftKey, text)
        }
    }

    private func send() {
        isBusy = true
        errorMessage = nil
        Task {
            do {
                try await APIClient.shared.reactivateJob(
                    baseURL: baseURL, slug: job.slug,
                    text: text.trimmingCharacters(in: .whitespacesAndNewlines)
                )
                isBusy = false
                // The server clears the job's draft itself on a successful
                // reactivate — only the local copy needs clearing here.
                draftStore.clear(draftKey)
                onReactivated()
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
    // When true (the cell is blocked), the sheet frames itself as "unblock"
    // rather than steering — same /api/steer delivery, different wording so
    // it doesn't read as an optional nudge.
    var unblockFraming: Bool = false
    var onSteered: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(DraftStore.self) private var draftStore
    @FocusState private var textFocused: Bool
    @State private var text = ""
    @State private var isBusy = false
    @State private var errorMessage: String?
    @State private var showingArtifact: WebArtifact?
    // Session-local only — deliberately NOT persisted to DraftStore (see
    // that file's header comment), so attachments don't survive a killed app
    // the way typed text does.
    @State private var attachments: [ComposerAttachment] = []
    @State private var isUploading = false

    private var draftKey: String { "steer:\(job.slug)" }

    var body: some View {
        Form {
            kSection {
                Text(unblockFraming
                     ? "Tell the crew how to proceed — routed straight to the cell."
                     : "Free-text direction for the crew — routed to the cell like a terminal message.")
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
            }
            if let detail = job.detail, !detail.isEmpty {
                kSection("Cell's last message") {
                    // capped so a long cell message can't push the composer
                    // below the fold — the web modal caps at 40vh for the same
                    // reason; the inner scroll keeps the full text reachable
                    ScrollView {
                        DetailCard(text: detail, stateColor: BoardTheme.color(forState: job.state))
                    }
                    .frame(maxHeight: 280)
                }
            }
            if let lastSent = job.lastSent, !lastSent.isEmpty {
                kSection("Last sent") {
                    Text(lastSent)
                        .font(.data(12))
                        .foregroundStyle(KTheme.text2)
                }
            }
            if let artifacts = job.artifacts, !artifacts.isEmpty {
                kSection("Artifacts") {
                    CollapsedArtifactList(artifacts: artifacts, onOpen: { showingArtifact = $0 })
                }
            }
            kSection("Message") {
                TextEditor(text: $text)
                    .focused($textFocused)
                    .scrollContentBackground(.hidden)
                    .kField(focused: textFocused, minHeight: 120)
                    .listRowBackground(Color.clear)
                ImageAttachmentBar(
                    baseURL: baseURL, slug: job.slug,
                    isUploading: $isUploading,
                    onUploaded: { attachments.append($0) },
                    onError: { errorMessage = $0 }
                )
                if !attachments.isEmpty {
                    AttachmentChipsRow(baseURL: baseURL, attachments: attachments) { att in
                        attachments.removeAll { $0.id == att.id }
                    }
                }
            }
            if let errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle(unblockFraming ? "Unblock \(job.slug)" : "Steer \(job.slug)")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton(label: "Cancel") { dismiss() }
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
                .disabled(isBusy || isUploading || (text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && attachments.isEmpty))
            }
        }
        .disabled(isBusy)
        .task {
            await draftStore.hydrate(slug: job.slug)
            text = draftStore.get(draftKey)
        }
        .onChange(of: text) {
            draftStore.set(draftKey, text)
        }
        // Nested sheet, presented from within SteerView's own sheet — SwiftUI
        // supports stacking a second sheet on top of a presented one.
        // Dismissing it lands back on this composer with the draft intact
        // (draftStore state isn't touched by presentation).
        .sheet(item: $showingArtifact) { artifact in
            NavigationStack {
                ArtifactView(name: artifact.name, baseURL: baseURL, path: artifact.url)
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            SheetCloseButton { showingArtifact = nil }
                        }
                    }
            }
        }
    }

    // One "[image: <path>]" line per attachment, appended after the typed
    // text — an images-only send (empty text, one or more attachments) is
    // valid, since the disabled-state check above allows it.
    private var outgoingText: String {
        guard !attachments.isEmpty else { return text }
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let imageLines = attachments.map { "[image: \($0.path)]" }.joined(separator: "\n")
        return trimmed.isEmpty ? imageLines : trimmed + "\n\n" + imageLines
    }

    private func send() {
        isBusy = true
        errorMessage = nil
        Task {
            do {
                try await APIClient.shared.steer(baseURL: baseURL, slug: job.slug, text: outgoingText)
                isBusy = false
                attachments = []
                // The server clears the job's draft itself on a successful
                // steer — clear() drops the local copy and cancels any pending
                // sync, and that's the last word: deliberately NOT resetting
                // `text` here, because that would fire .onChange -> set("") and
                // schedule a redundant empty-text POST (the sheet dismisses via
                // onSteered anyway, discarding the field).
                draftStore.clear(draftKey)
                onSteered()
            } catch {
                isBusy = false
                // A failed send (409, etc.) keeps the draft and attachments.
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
