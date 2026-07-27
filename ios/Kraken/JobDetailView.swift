import SwiftUI
import PhotosUI

// MARK: - command chips

/// Row of five tappable command chips — /info /aim /do /pr /dream — for one-tap
/// insertion into a composer's bound text, mirroring the web SPA's command
/// chips (app.js's attachCmdChips). This still appends rather than inserting
/// at the caret even though HighlightingTextEditor is UIKit-backed (and so
/// does expose a cursor position) — caret-aware insertion is an explicit
/// non-goal for this pass. A leading space is added only when the text is
/// non-empty and doesn't already end in whitespace, then the command plus a
/// trailing space. Shared by SteerView/ReactivateView here and JobFormView's
/// spec editor.
///
/// Each chip is colored by its skill (text = skill color, inactive border =
/// skill soft, active = skill-soft fill + skill border + glow) and lights up
/// when its command already appears in `text` — pure render-state off the
/// existing binding, no new state. `commands` is also the single source
/// HighlightingTextEditor's token finder reuses, so the two never drift
/// apart on which commands are recognized.
struct CmdChipsRow: View {
    @Binding var text: String

    static let commands = ["/info", "/aim", "/do", "/pr", "/dream"]

    var body: some View {
        HStack(spacing: 6) {
            ForEach(Self.commands, id: \.self) { cmd in
                let active = Self.isPresent(cmd, in: text)
                let style = Self.style(for: cmd)
                Button {
                    append(cmd)
                } label: {
                    Text(cmd)
                        .font(.data(11, weight: .semibold))
                        .foregroundStyle(style.color)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 4)
                        .background(active ? style.soft : .clear)
                        .overlay(Rectangle().stroke(active ? style.color : style.soft, lineWidth: 1))
                        .shadow(color: active ? style.color.opacity(0.5) : .clear, radius: 4)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
    }

    // command → (UIColor, UIColor soft) — the ONE switch statement both this
    // chip row and HighlightingTextEditor.color(forCommand:) draw from,
    // following the KTheme xxxUI/Color(uiColor:) convention: UIColor is
    // canonical here, with the SwiftUI Color below derived from it for chip
    // rendering. Not private — HighlightingTextEditor.swift calls this too.
    static func styleUI(for cmd: String) -> (color: UIColor, soft: UIColor)? {
        switch cmd {
        case "/info": return (KTheme.skillInfoUI, KTheme.skillInfoSoftUI)
        case "/aim": return (KTheme.skillAimUI, KTheme.skillAimSoftUI)
        case "/do": return (KTheme.skillDoUI, KTheme.skillDoSoftUI)
        case "/pr": return (KTheme.skillPrUI, KTheme.skillPrSoftUI)
        case "/dream": return (KTheme.skillDreamUI, KTheme.skillDreamSoftUI)
        default: return nil
        }
    }

    private static func style(for cmd: String) -> (color: Color, soft: Color) {
        guard let ui = styleUI(for: cmd) else { return (KTheme.accent, KTheme.accentSoft) }
        return (Color(uiColor: ui.color), Color(uiColor: ui.soft))
    }

    private func append(_ cmd: String) {
        if text.isEmpty || text.hasSuffix(" ") || text.hasSuffix("\n") {
            text += cmd + " "
        } else {
            text += " " + cmd + " "
        }
    }

    // isPresent reports whether `cmd` (e.g. "/info") appears in `text` as a
    // whole command token. Delegates to HighlightingTextEditor's shared
    // regex (commandTokenRanges) rather than hand-building a second pattern,
    // so the anchoring rules — a trailing \b (so "/do" doesn't light up on
    // "/doc") and a leading (^|\s) boundary (so "web/static/do" doesn't
    // light up either) — can never drift between the chip row and the
    // inline highlighter.
    private static func isPresent(_ cmd: String, in text: String) -> Bool {
        guard !text.isEmpty else { return false }
        return HighlightingTextEditor.commandTokenRanges(in: text).contains { $0.command == cmd }
    }
}

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

/// Zeus + crew status chips: a zeus pill first (only when the server sent a
/// zeus status) showing just its cloud icon, colored by status — the icon
/// replaces both the "zeus" name and the old ●/↻/⚑/○ status glyph, since
/// color alone now carries the state (the name survives for hover/VoiceOver
/// via accessibilityLabel); then one chip per crew role present on the
/// roster — fixed order hephaestus, athena, hermes, other — each showing its
/// god icon plus "<working>/<total>" (icon color: green if any member is
/// working, muted otherwise). The "other" bucket has no god icon and keeps
/// its plain "other <working>/<total>" text label. Not private: both
/// BoardView's JobCardView and JobDetailView render the same row, mirroring
/// PRChip's sharing pattern above. Renders EmptyView when there's nothing to
/// show, so a job with no zeus status and no roster (including servers that
/// predate these wire fields entirely) falls back to exactly today's UI.
struct CrewChipsView: View {
    let zeusStatus: String?
    let roster: [WebRosterAgent]

    private static let knownRoles = ["hephaestus", "athena", "hermes"]
    private static let chipOrder = knownRoles + ["other"]

    // God → SF Symbol, carl's picks: zeus=lightning bolt, hephaestus=
    // hammer (forge), athena=judge (courthouse), hermes=message (messenger).
    private static let roleIcons: [String: String] = [
        "hephaestus": "hammer.fill",
        "athena": "building.columns.fill",
        "hermes": "envelope.fill",
    ]

    // Just the color — the a11y label is built straight from zeusStatus at
    // the call site instead of this helper echoing it back out.
    private var zeusColor: Color? {
        guard let zeusStatus, !zeusStatus.isEmpty else { return nil }
        switch zeusStatus {
        case "running": return KTheme.solved
        case "polling": return KTheme.polling
        case "question": return KTheme.revealed
        default: return KTheme.text2
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
        let zeus = zeusColor
        let chips = roleChips
        if zeus != nil || !chips.isEmpty {
            HStack(spacing: 6) {
                if let zeus, let zeusStatus {
                    chipLabel(icon: "bolt.fill", color: zeus)
                        .accessibilityLabel("zeus — \(zeusStatus)")
                }
                ForEach(chips, id: \.role) { chip in
                    let color: Color = chip.working > 0 ? KTheme.solved : KTheme.text2
                    if let icon = Self.roleIcons[chip.role] {
                        chipLabel(icon: icon, text: "\(chip.working)/\(chip.total)", color: color)
                            .accessibilityLabel("\(chip.role) — \(chip.working) of \(chip.total) working")
                    } else {
                        chipLabel(text: "\(chip.role) \(chip.working)/\(chip.total)", color: color)
                            .accessibilityLabel("\(chip.role) — \(chip.working) of \(chip.total) working")
                    }
                }
            }
        }
    }

    // `icon` is an optional leading SF Symbol (nil for the iconless "other"
    // bucket); `text` is optional too so the zeus chip can be icon-only.
    // .accessibilityElement(children: .ignore) collapses the HStack into a
    // single a11y element: a bare HStack isn't an a11y element on its own,
    // so without this VoiceOver either skips it entirely or reads its
    // children individually — either way the .accessibilityLabel a call
    // site applies afterward would be discarded or misattributed.
    private func chipLabel(icon: String? = nil, text: String? = nil, color: Color) -> some View {
        HStack(spacing: 3) {
            if let icon {
                // The god icon reads a step larger than the counts beside
                // it (mirrors the web's .crew-pill/.crew-ico size bump).
                Image(systemName: icon)
                    .font(.data(11, weight: .semibold))
            }
            if let text {
                Text(text)
            }
        }
        .font(.data(9, weight: .semibold))
        .foregroundStyle(color)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .overlay(Rectangle().stroke(color, lineWidth: 1))
        .accessibilityElement(children: .ignore)
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
    // Local optimistic overlay of doc-question answers (id -> answer text).
    // `job` is a frozen snapshot for the sheet's whole lifetime (BoardView's
    // sheet(item:) never rewrites it while open — see mergedDocQuestions),
    // so without this an answered row would never visibly flip. Naturally
    // reset on every fresh presentation since a new sheet gets a new View
    // instance (and thus fresh @State).
    @State private var docQuestionOverlay: [String: String] = [:]
    // Local optimistic overlay of feedback creates/edits/deletes, for exactly
    // the frozen-snapshot reason docQuestionOverlay exists — plus a hidden-id
    // set, since a delete has to remove a row rather than rewrite one.
    @State private var feedbackOverlay = FeedbackOverlay()
    @State private var editingFeedback: WebFeedback?
    @State private var deletingFeedback: WebFeedback?
    @State private var showingFeedbackDeleteConfirm = false
    @State private var showingBranchThread = false
    // Threads branched from this sheet since it opened. Same frozen-snapshot
    // problem docQuestionOverlay solves: `job` never refreshes while the
    // sheet is up, so a freshly branched thread would otherwise have no chip
    // to tap until the sheet is reopened.
    @State private var branchedThreads: [WebThread] = []

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
    // A side thread forks zeus' session into a second claude in the cell's
    // own tmux window, so branching only makes sense while that cell is up.
    // Existing threads stay readable either way — a closed thread's
    // conversation outlives its window, and the board keeps serving it.
    private var canBranchThread: Bool { job.live }
    // The server's thread list plus any branched from this sheet, deduped by
    // tid with the server's copy winning (it has the real pane/state).
    private var allThreads: [WebThread] {
        let served = job.threads ?? []
        let known = Set(served.map(\.tid))
        return served + branchedThreads.filter { !known.contains($0.tid) }
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

                // Token spend sits this high — beside Repo rather than down
                // with Artifacts — because it is one glanceable fact about
                // the job as a whole, and the artifact tree below can run
                // long enough to bury anything under it. `tokens` being nil
                // is what hides the whole section: nil means NEVER MEASURED
                // (no transcripts, or the daemon hasn't reached this job),
                // which is a different answer from a measured zero, and a
                // rendered 0 would state the false one.
                //
                // The second half of the gate matches web's `tokensHTML`
                // (`if (!total && !sessions) return ""`): a row that is
                // all-zero AND counted no transcript files says nothing a
                // reader can use, and rendering it prints four counters, an
                // origin split and a "measured across 0 transcripts" line
                // that together read as a measurement rather than as its
                // absence. It is reachable — a first measurement whose files
                // all vanish between the walk and the next pass — and the two
                // surfaces are meant to render this block identically.
                if let tokens = job.tokens,
                   tokens.input + tokens.output + tokens.cacheWrite + tokens.cacheRead > 0
                       || tokens.sessions > 0 {
                    kSection("Tokens") { tokensRows(tokens) }
                }

                // Outside every editability gate on purpose. The spec editor
                // (and the color picker inside it) is only reachable through
                // the Edit Spec button below, which requires `isEditable` —
                // i.e. a cell that is DOWN — while recoloring is most useful
                // on a job that is running, and is exactly what the web board
                // already allows for a working/blocked job whose cell died.
                // POST /api/job/color has no editable-job gate to respect, so
                // neither does this. Archived jobs are the one exclusion:
                // their file has left the active namespace.
                if !isArchived {
                    kSection("Color") {
                        JobColorStrip(baseURL: baseURL, slug: job.slug, serverColor: job.color) { _ in
                            // Refresh the board so the card's swatch and this
                            // sheet's `job` snapshot pick the new color up.
                            onUpdate()
                        }
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
                if !allThreads.isEmpty || canBranchThread {
                    kSection("Threads") {
                        if !allThreads.isEmpty {
                            ScrollView(.horizontal, showsIndicators: false) {
                                HStack(spacing: 6) {
                                    ForEach(allThreads) { thread in
                                        NavigationLink {
                                            ThreadDetailView(
                                                thread: thread, slug: job.slug,
                                                baseURL: baseURL, onChanged: onUpdate
                                            )
                                        } label: {
                                            ThreadChip(thread: thread)
                                        }
                                        .buttonStyle(.plain)
                                    }
                                }
                            }
                        }
                        if canBranchThread {
                            Button {
                                showingBranchThread = true
                            } label: {
                                Label("Branch a thread", systemImage: "arrow.triangle.branch")
                            }
                            .buttonStyle(.plain)
                            .font(.data(12))
                            .foregroundStyle(KTheme.accent)
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
                if !(job.docQuestions ?? []).isEmpty {
                    kSection("Doc questions") {
                        ForEach(sortedDocQuestions) { q in
                            DocQuestionCard(item: q) { text in
                                answerDocQuestion(id: q.id, text: text)
                            }
                        }
                        if sortedDocQuestions.contains(where: { !$0.isAnswered }) {
                            Text("answers land in kraken.db — the agent reads them when it next resumes")
                                .font(.voice(11))
                                .foregroundStyle(KTheme.text2)
                                .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 8, trailing: 12))
                                .listRowBackground(Color.clear)
                        }
                    }
                }
                // Deliberately NOT gated on `!isArchived`, unlike Doc
                // questions above: `feedback` IS served for archived jobs
                // (rows outlive the cell — see webJob's `feedback` key),
                // so copying that gate would hide exactly the notes worth
                // keeping. Open notes first, then addressed ones, both in
                // server order — same shape as sortedDocQuestions.
                if !mergedFeedback.isEmpty {
                    kSection("Feedback · \(mergedFeedback.count)") {
                        ForEach(sortedFeedback) { f in
                            FeedbackRow(
                                item: f,
                                onEdit: f.isOptimistic ? nil : { editingFeedback = f },
                                onDelete: f.isOptimistic ? nil : {
                                    deletingFeedback = f
                                    showingFeedbackDeleteConfirm = true
                                }
                            )
                        }
                        Text("notes land in kraken.db — the agent reads them when it next resumes")
                            .font(.voice(11))
                            .foregroundStyle(KTheme.text2)
                            .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 8, trailing: 12))
                            .listRowBackground(Color.clear)
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
        // Same destructive-confirmation pattern as Kill / Delete Forever
        // above — a note is prose carl typed, and there is no undo.
        .confirmationDialog(
            "Delete this feedback note? The row is removed from kraken.db.",
            isPresented: $showingFeedbackDeleteConfirm, titleVisibility: .visible
        ) {
            Button("Delete", role: .destructive) {
                if let f = deletingFeedback { deleteFeedback(f) }
                deletingFeedback = nil
            }
            Button("Cancel", role: .cancel) { deletingFeedback = nil }
        }
        .sheet(item: $editingFeedback) { f in
            NavigationStack {
                FeedbackComposeView(
                    baseURL: baseURL,
                    target: .edit(slug: job.slug, id: f.id, artifact: f.artifact),
                    initialNote: f.note
                ) { note in
                    feedbackOverlay.edits[f.id] = note
                    onUpdate()
                }
            }
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
        .sheet(isPresented: $showingBranchThread) {
            NavigationStack {
                BranchThreadView(job: job, baseURL: baseURL) { thread in
                    showingBranchThread = false
                    // nil = the server accepted the branch but didn't return a
                    // tid; the chip shows up on the next board refresh instead
                    // of being faked with an id nothing can address.
                    if let thread { branchedThreads.append(thread) }
                    onUpdate()
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
                // onFeedbackCreated folds the new note straight into this
                // sheet's overlay: `job` is frozen, so nothing else would
                // show it until the sheet is reopened.
                ArtifactView(
                    name: artifact.name, baseURL: baseURL, path: artifact.url,
                    onFeedbackCreated: { ref, note in addOptimisticFeedback(ref: ref, note: note) }
                )
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

    // MARK: - token spend

    /// Renders one raw counter at a glance: 15_756_009 -> "15.8M". Precision
    /// shrinks as the magnitude grows (2dp under 10, 1dp under 100, none
    /// above) so every value lands in four characters plus a unit — the four
    /// counters the wire carries are full-width integers, and side by side at
    /// full width they read as noise. Nothing here is exact enough to earn
    /// the digits either: see `tokensRows` on the floor caveat.
    private func fmtTokens(_ n: Int64) -> String {
        let v = Double(max(0, n))
        let units: [(Double, String)] = [(1e9, "B"), (1e6, "M"), (1e3, "K")]
        for (i, unit) in units.enumerated() where v >= unit.0 {
            let scaled = v / unit.0
            let dp = scaled < 10 ? 2 : (scaled < 100 ? 1 : 0)
            var s = String(format: "%.\(dp)f", scaled)
            // rounding can carry a value into the next unit up — 999_999
            // formats as "1000K", which is both wrong-looking and wider than
            // the unit exists to keep it. Promote instead.
            if (Double(s) ?? 0) >= 1000, i > 0 { return "1" + units[i - 1].1 }
            // trim only INSIDE a decimal part — a blanket trim of "0" would
            // turn "100" into "1"
            if s.contains(".") {
                while s.hasSuffix("0") { s.removeLast() }
                if s.hasSuffix(".") { s.removeLast() }
            }
            return s + unit.1
        }
        return String(max(0, n))
    }

    /// The job's token spend: the four raw counters the daemon sums out of
    /// this job's own Claude Code transcripts (`job_tokens`, served as
    /// `WebJob.tokens`). Mirrors the web board's `tokensHTML` row for row —
    /// the two surfaces render the same block and are meant to stay
    /// recognisably the same block.
    ///
    /// Everything is FADED. Four tiers — headline, counters, origin,
    /// provenance — separated by size and opacity rather than by brightness,
    /// with even the headline sitting in `text2`: this is a fact about the
    /// past, and nothing about it is hot or interactive enough to earn full
    /// `text`.
    ///
    /// The headline carries the SUM alone. It used to repeat `out` beside it,
    /// which printed the same number twice in a block whose very next row
    /// already lists all four counters.
    ///
    /// The origin row is the only derived thing here: the daemon stores
    /// zeus' counters as a SUBSET of the totals (`WebTokens.zeusTotal`), so
    /// the delegated half is the REMAINDER. Storing both halves would let
    /// them drift out of agreement with the total; subtracting cannot. "zeus"
    /// is the cell's lead agent and its side threads, "subagents" is every
    /// teammate and in-process subagent it delegated to. A server predating
    /// those keys sends none of them — `zeusTotal` is nil — and the row is
    /// dropped rather than rendering a 100%-subagent lie.
    ///
    /// The counters stay RAW with no blended cost: cache reads bill far below
    /// input, and weighting them here would bake today's price ratio into the
    /// app (the same reason the wire carries no total).
    ///
    /// The wording never claims exactness. A transcript caught mid-write is
    /// resumed on the next pass rather than skipped, so a freshly scanned
    /// job's numbers are a floor.
    @ViewBuilder
    private func tokensRows(_ t: WebTokens) -> some View {
        let total = t.input + t.output + t.cacheWrite + t.cacheRead
        VStack(alignment: .leading, spacing: 5) {
            HStack(alignment: .firstTextBaseline, spacing: 5) {
                Text(fmtTokens(total)).font(.data(15, weight: .semibold)).foregroundStyle(KTheme.text2)
                Text("TOKENS").font(.data(9)).foregroundStyle(KTheme.text2).opacity(0.65)
                Spacer()
            }
            // the counters row and the origin row are two cuts of the SAME
            // headline, so they sit closer to each other than to the tiers
            // around them and read as one stack rather than two panels —
            // web's `.tok-origin { margin-top: 2px }` is the same two points
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 14) {
                    tokenCell("in", t.input)
                    tokenCell("out", t.output)
                    tokenCell("cache w", t.cacheWrite)
                    tokenCell("cache r", t.cacheRead)
                    Spacer()
                }
                // the subagent half comes from WebTokens' own computed
                // accessor rather than being subtracted here: the derivation
                // is the one thing about this row that could be got wrong,
                // and it belongs in one audited place. The zeus clamp is
                // local because it is purely defensive — zeus is a subset by
                // construction, so it can only exceed the total against a
                // row read mid-rescan
                if let zeus = t.zeusTotal, let subagents = t.subagentTotal {
                    HStack(spacing: 14) {
                        tokenCell("zeus", min(zeus, total))
                        tokenCell("subagents", subagents)
                        Spacer()
                    }
                }
            }
            Text(tokensNote(t)).font(.data(9)).foregroundStyle(KTheme.text2).opacity(0.55)
        }
        .padding(.vertical, 2)
    }

    /// One `label value` pair. Both halves are `fixedSize` because the four
    /// counters sit in one HStack that is genuinely tight at phone width:
    /// without it SwiftUI resolves the overflow by WRAPPING a label, and
    /// `cache w` breaks across two lines mid-row, which is both ugly and
    /// misreads as a fifth counter. Refusing to wrap lets the row run to the
    /// trailing Spacer instead — the labels are two words at most, so the
    /// widest possible row still fits.
    private func tokenCell(_ label: String, _ value: Int64) -> some View {
        HStack(spacing: 5) {
            Text(label).font(.data(10)).foregroundStyle(KTheme.text2).opacity(0.6)
                .lineLimit(1).fixedSize()
            Text(fmtTokens(value)).font(.data(10)).foregroundStyle(KTheme.text2).opacity(0.85)
                .lineLimit(1).fixedSize()
        }
    }

    /// Provenance, not a headline: how many transcript FILES the number was
    /// summed across (root transcripts plus subagent sidecars — that count is
    /// what tells "cheap job" apart from "barely measured"), and how long ago
    /// a refresh last found new spend. A zero/absent stamp drops the age
    /// clause rather than rendering "0m ago".
    private func tokensNote(_ t: WebTokens) -> String {
        let files = t.sessions == 1 ? "1 transcript" : "\(t.sessions) transcripts"
        var note = t.sessions > 0 ? "measured across \(files)" : "measured"
        if t.updatedAt > 0 {
            let seconds = max(0, Int(Date().timeIntervalSince1970) - Int(t.updatedAt / 1000))
            let ago: String
            if seconds < 60 { ago = "\(seconds)s" }
            else if seconds < 3600 { ago = "\(seconds / 60)m" }
            else if seconds < 86400 { ago = "\(seconds / 3600)h" }
            else { ago = "\(seconds / 86400)d" }
            note += " · \(ago) ago"
        }
        return note
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
    private func runAction(
        dismissOnSuccess: Bool = true,
        onSuccess: (() -> Void)? = nil,
        _ action: @escaping () async throws -> Void
    ) {
        isBusy = true
        errorMessage = nil
        Task {
            do {
                try await action()
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

    // job.docQuestions with any locally-answered overlay applied (see
    // docQuestionOverlay) — the sheet's `job` never refreshes on its own
    // while open, so this is what lets an answer visibly flip without
    // dismissing.
    private var mergedDocQuestions: [WebDocQuestion] {
        (job.docQuestions ?? []).map { q in
            guard let overlay = docQuestionOverlay[q.id], !q.isAnswered else { return q }
            return WebDocQuestion(
                id: q.id, text: q.text, context: q.context, importance: q.importance,
                recommendation: q.recommendation, answer: overlay, answeredAt: q.answeredAt)
        }
    }

    // Matches web's sortDocQuestions exactly (app.js): open questions first
    // (importance rank P0 < P1 < P2, ties keep server order), answered
    // questions last (server order). The explicit `offset` tiebreak is
    // load-bearing — unlike JS's Array#sort (guaranteed stable since
    // ES2019), Swift's sorted(by:) is documented as NOT guaranteed stable.
    private var sortedDocQuestions: [WebDocQuestion] {
        let rank: [String: Int] = ["P0": 0, "P1": 1, "P2": 2]
        let indexed = Array(mergedDocQuestions.enumerated())
        let open = indexed.filter { !$0.element.isAnswered }
        let answered = indexed.filter { $0.element.isAnswered }
        let sortedOpen = open.sorted { a, b in
            let ra = rank[a.element.importance] ?? 3
            let rb = rank[b.element.importance] ?? 3
            if ra != rb { return ra < rb }
            return a.offset < b.offset
        }
        let sortedAnswered = answered.sorted { $0.offset < $1.offset }
        return sortedOpen.map(\.element) + sortedAnswered.map(\.element)
    }

    // Submitting a Doc Questions answer (free text or "Accept recommendation")
    // — same busy/error/onUpdate handling as answerQuestion above, but
    // non-dismissing: doc questions are an N-item list, not a one-shot
    // action, so answering one shouldn't bounce the user out of the sheet.
    // On success, record it in the local overlay so the row flips to
    // answered immediately (see mergedDocQuestions/docQuestionOverlay).
    private func answerDocQuestion(id: String, text: String) {
        runAction(dismissOnSuccess: false, onSuccess: { docQuestionOverlay[id] = text }) {
            try await APIClient.shared.answerDocQuestion(baseURL: baseURL, slug: job.slug, id: id, text: text)
        }
    }

    // job.feedback with the local overlay applied — the sheet's `job` never
    // refreshes on its own while open, so this is what lets a create/edit/
    // delete visibly land without dismissing (see feedbackOverlay).
    private var mergedFeedback: [WebFeedback] {
        feedbackOverlay.apply(to: job.feedback ?? [])
    }

    // Open notes first, addressed ones after, each keeping server order —
    // the same "still needs you first" shape sortedDocQuestions uses. The
    // explicit offset tiebreak is load-bearing for the same reason it is
    // there: Swift's sorted(by:) is documented as NOT stable.
    private var sortedFeedback: [WebFeedback] {
        let indexed = Array(mergedFeedback.enumerated())
        let open = indexed.filter { !$0.element.isAddressed }.sorted { $0.offset < $1.offset }
        let addressed = indexed.filter { $0.element.isAddressed }.sorted { $0.offset < $1.offset }
        return open.map(\.element) + addressed.map(\.element)
    }

    // A note created from the artifact viewer, folded in as an optimistic row
    // until the next board poll replaces it with the served one. `ref.name`
    // is the artifact's SERVER-side name (derived from its URL), which is
    // what the served row's `artifact` field will read too.
    private func addOptimisticFeedback(ref: ArtifactRef, note: String) {
        let id = feedbackOverlay.nextOptimisticID()
        feedbackOverlay.created.append(WebFeedback(
            id: id, artifact: ref.name, note: note,
            at: Int64(Date().timeIntervalSince1970 * 1000),
            addressed: nil, addressedAt: nil
        ))
        onUpdate()
    }

    // Non-dismissing like answerDocQuestion: feedback is an N-item list, not
    // a one-shot action. onUpdate() still fires the immediate extra board
    // poll so every other surface catches up.
    private func deleteFeedback(_ f: WebFeedback) {
        runAction(dismissOnSuccess: false, onSuccess: { feedbackOverlay.hidden.insert(f.id) }) {
            try await APIClient.shared.deleteFeedback(baseURL: baseURL, slug: job.slug, id: f.id)
        }
    }
}

/// One side-thread chip — "⑂ <title>", green while the thread's window is
/// live, muted once it's closed. Same outlined-stroke treatment as PRChip
/// above, so a job's chips all read as one family.
/// Not private: the board card renders these too, in its own chip row (see
/// JobCardView), so a job's branches read the same on the board as they do in
/// the detail sheet.
struct ThreadChip: View {
    let thread: WebThread

    var body: some View {
        let color = thread.isLive ? KTheme.solved : KTheme.text2
        HStack(spacing: 4) {
            Image(systemName: "arrow.triangle.branch")
                .font(.data(9, weight: .semibold))
            // Capped, not just single-line — one reason per surface, since
            // this chip renders on two. In the detail sheet the chips live in
            // a horizontal scroller, so an unbounded title would stretch one
            // chip off the screen and hide every thread behind it. On the
            // board card they live in CardChipFlow, which wraps rather than
            // scrolls, so the cap is instead what keeps a renamed thread
            // inside the card width — the same job style.css's
            // `.thread-chip { max-width: 100%; text-overflow: ellipsis }`
            // does on web, and what lets CardChipFlow's placement clamp
            // ellipsise a first-on-line chip rather than overflow it.
            Text(thread.displayTitle)
                .lineLimit(1)
                .truncationMode(.tail)
                .frame(maxWidth: 140, alignment: .leading)
        }
        .font(.data(10, weight: .semibold))
        .foregroundStyle(color)
        .padding(.horizontal, 6)
        .padding(.vertical, 2)
        .overlay(Rectangle().stroke(color, lineWidth: 1))
        .contentShape(Rectangle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("thread \(thread.displayTitle) — \(thread.isLive ? "live" : "closed")")
    }
}

/// Branching a new side thread: a name (required — it's how the thread is
/// identified everywhere afterward) and an optional first message. Forks
/// zeus' session into a second claude in its own window of the same cell, so
/// it only appears while the cell is live (see canBranchThread). No draft
/// persistence here on purpose: unlike a steer, this composer is a one-shot
/// naming step, not a message that's worth surviving an app kill.
private struct BranchThreadView: View {
    let job: WebJob
    let baseURL: String
    /// Carries the optimistic thread back to JobDetailView (nil when the
    /// server returned no tid), which also dismisses this sheet.
    var onBranched: (WebThread?) -> Void

    @Environment(\.dismiss) private var dismiss
    @FocusState private var seedFocused: Bool
    @State private var title = ""
    @State private var seed = ""
    @State private var isBusy = false
    @State private var errorMessage: String?

    private var trimmedTitle: String {
        title.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var body: some View {
        Form {
            kSection {
                Text("Forks zeus' session into a second claude in its own window of the cell — ask questions without disturbing the run. Closing the thread later keeps the conversation.")
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
            }
            kSection("Name") {
                TextField("what this thread is for", text: $title)
                    .kField()
            }
            kSection("First message (optional)") {
                TextField("what to ask", text: $seed, axis: .vertical)
                    .focused($seedFocused)
                    .kField(focused: seedFocused, minHeight: 100)
            }
            if let errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle("Branch thread")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton(label: "Cancel") { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    branch()
                } label: {
                    Label("Branch", systemImage: "arrow.triangle.branch")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Branch")
                .buttonStyle(.borderedProminent)
                .disabled(isBusy || trimmedTitle.isEmpty)
            }
        }
        .disabled(isBusy)
    }

    private func branch() {
        let name = trimmedTitle
        guard !name.isEmpty else { return }
        let text = seed.trimmingCharacters(in: .whitespacesAndNewlines)
        isBusy = true
        errorMessage = nil
        Task {
            do {
                let resp = try await APIClient.shared.threadAction(
                    baseURL: baseURL, slug: job.slug, op: .branch,
                    title: name, text: text.isEmpty ? nil : text
                )
                isBusy = false
                if let tid = resp.tid, !tid.isEmpty {
                    onBranched(WebThread(
                        tid: tid, title: name, state: "live", pane: nil,
                        createdAt: Int64(Date().timeIntervalSince1970 * 1000),
                        closedAt: nil
                    ))
                } else {
                    onBranched(nil)
                }
            } catch {
                isBusy = false
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
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
            .cardBackground(barColor: stateColor)
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

/// One doc-question row. Open questions render the full card (badge +
/// text + context + recommendation + accept/free-text answer controls);
/// answered ones collapse to a single dimmed "✓ <answer>" row. `onAnswer`
/// carries the answer text straight up to JobDetailView's answerDocQuestion,
/// which posts it and re-runs the same busy/error/dismiss handling as every
/// other board action.
private struct DocQuestionCard: View {
    let item: WebDocQuestion
    var onAnswer: (String) -> Void

    @State private var answerText = ""

    private var badgeColor: Color {
        switch item.importance {
        case "P0": return KTheme.counter
        case "P1": return KTheme.revealed
        default: return KTheme.text2
        }
    }

    // Outlined-stroke treatment matching PRChip.chipLabel (no filled tint) —
    // used for both the open and answered states so the badge reads the
    // same regardless of which branch renders it.
    private var badge: some View {
        Text(item.importance)
            .font(.data(9, weight: .semibold))
            .foregroundStyle(badgeColor)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .overlay(Rectangle().stroke(badgeColor, lineWidth: 1))
    }

    var body: some View {
        if item.isAnswered {
            HStack(alignment: .top, spacing: 8) {
                badge
                VStack(alignment: .leading, spacing: 2) {
                    Text(item.text)
                        .font(.voice(12))
                        .foregroundStyle(KTheme.text2)
                    Text("✓ \(item.answer ?? "")")
                        .font(.voice(12, weight: .semibold))
                        .foregroundStyle(KTheme.solved)
                }
            }
            .opacity(0.6)
            .padding(.vertical, 2)
            .listRowInsets(EdgeInsets(top: 2, leading: 12, bottom: 2, trailing: 12))
            .listRowBackground(Color.clear)
        } else {
            VStack(alignment: .leading, spacing: 6) {
                badge
                Text(item.text)
                    .font(.voice(13, weight: .semibold))
                    .foregroundStyle(KTheme.text)
                if let context = item.context, !context.isEmpty {
                    Text(context)
                        .font(.voice(12))
                        .foregroundStyle(KTheme.text2)
                }
                if let recommendation = item.recommendation, !recommendation.isEmpty {
                    Text("suggests: \(recommendation)")
                        .font(.voice(12))
                        .foregroundStyle(KTheme.polling)
                    Button("Accept recommendation") {
                        onAnswer(recommendation)
                    }
                    .font(.data(11, weight: .semibold))
                    .buttonStyle(.bordered)
                }
                HStack(spacing: 6) {
                    TextField("answer", text: $answerText)
                        .kField()
                    Button("Send") {
                        let trimmed = answerText.trimmingCharacters(in: .whitespacesAndNewlines)
                        guard !trimmed.isEmpty else { return }
                        onAnswer(trimmed)
                    }
                    .font(.data(11, weight: .semibold))
                    .buttonStyle(.borderedProminent)
                    .disabled(answerText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .padding(10)
            .listRowInsets(EdgeInsets(top: 4, leading: 12, bottom: 4, trailing: 12))
        }
    }
}

/// The reactivate prompt: an optional free-text message folded into the
/// resume prompt of the done job's original zeus session. Mirrors
/// SteerView's shape, but an empty message is fine — reactivating without
/// steering just wakes the session with the standard fresh-branch brief.
/// Not private: BoardView presents this directly for a done→working
/// overview-strip drag, where `requireText` is set — that entry path has no
/// separate confirmation step, so an empty-text resume would silently do
/// nothing but reopen the same session.
struct ReactivateView: View {
    let job: WebJob
    let baseURL: String
    // When true (drag-to-working from the overview strip), the Reactivate
    // button stays disabled until non-empty text is entered. JobDetailView's
    // own entry (the action bar's Reactivate button) leaves this false —
    // an empty-text reactivate there is a deliberate, low-stakes shortcut.
    var requireText: Bool = false
    var onReactivated: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(DraftStore.self) private var draftStore
    // Not @FocusState: SwiftUI's .focused($state) never fires on a
    // UIViewRepresentable, so HighlightingTextEditor bridges focus itself
    // via its own isFocused binding instead (see that type's doc comment).
    @State private var textFocused = false
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
                CmdChipsRow(text: $text)
                HighlightingTextEditor(text: $text, isFocused: $textFocused)
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
                .disabled(isBusy || (requireText && text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty))
            }
        }
        .disabled(isBusy)
        .task {
            await draftStore.hydrate(slug: job.slug)
            // Guard against clobbering a command chip tapped in the window
            // between this view appearing and hydration finishing — only
            // land the stored draft if nothing's been typed yet.
            if text.isEmpty { text = draftStore.get(draftKey) }
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
/// Reached from JobDetailView's action bar for any live job, and directly
/// from BoardView for a blocked→working overview-strip drag (unblockFraming
/// true either way, since both entry points target a blocked cell).
struct SteerView: View {
    let job: WebJob
    let baseURL: String
    // When true (the cell is blocked), the sheet frames itself as "unblock"
    // rather than steering — same /api/steer delivery, different wording so
    // it doesn't read as an optional nudge.
    var unblockFraming: Bool = false
    var onSteered: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(DraftStore.self) private var draftStore
    // Not @FocusState: SwiftUI's .focused($state) never fires on a
    // UIViewRepresentable, so HighlightingTextEditor bridges focus itself
    // via its own isFocused binding instead (see that type's doc comment).
    @State private var textFocused = false
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
                CmdChipsRow(text: $text)
                HighlightingTextEditor(text: $text, isFocused: $textFocused)
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
            // Guard against clobbering a command chip tapped in the window
            // between this view appearing and hydration finishing — only
            // land the stored draft if nothing's been typed yet.
            if text.isEmpty { text = draftStore.get(draftKey) }
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
