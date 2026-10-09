import SwiftUI
import PhotosUI

// MARK: - command chips

/// Row of six tappable command chips — /info /aim /do /pr /dream /handoff —
/// for one-tap insertion into a composer's bound text, mirroring the web
/// SPA's command chips (app.js's attachCmdChips). This still appends rather
/// than inserting
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

    // /handoff sits last: it is the one chip that names no phase (it hands
    // the cell over behind a written record and returns it to the phase it
    // was in), and the one carl reaches for on a cell already mid-phase.
    static let commands = ["/info", "/aim", "/do", "/pr", "/dream", "/handoff"]

    var body: some View {
        // A horizontal ScrollView rather than a bare HStack: six chips come
        // to roughly 360pt of capsules at this font, against the ~315-330pt
        // a Form row offers on a 393pt phone, and a bare HStack answers that
        // by squeezing the Text inside its capsules (web's row wraps instead,
        // via flex-wrap). `.fixedSize()` on each label keeps every chip at
        // its natural width so the row scrolls rather than truncates. The
        // scroll view would otherwise clip the active chip's glow and the
        // outer half of its stroke (a bare HStack never clipped), and would
        // rubber-band on a Pro Max where all six already fit.
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ForEach(Self.commands, id: \.self) { cmd in
                    let active = Self.isPresent(cmd, in: text)
                    let style = Self.style(for: cmd)
                    Button {
                        append(cmd)
                    } label: {
                        // Capsule rather than the old sharp 1px rect — the glass
                        // idiom's chip shape. The skill hues themselves are
                        // untouched (they are NOT tint-derived; see KTheme).
                        Text(cmd)
                            .font(.data(11, weight: .semibold))
                            .lineLimit(1)
                            .fixedSize()
                            .foregroundStyle(style.color)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 5)
                            .background(active ? style.soft : .clear, in: Capsule())
                            .overlay(Capsule().stroke(active ? style.color : style.soft, lineWidth: 1))
                            .shadow(color: active ? style.color.opacity(0.5) : .clear, radius: 4)
                            .contentShape(Capsule())
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .scrollClipDisabled()
        .scrollBounceBehavior(.basedOnSize, axes: .horizontal)
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
        case "/handoff": return (KTheme.skillHandoffUI, KTheme.skillHandoffSoftUI)
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
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: 12, style: .continuous)
                        .stroke(KTheme.hairline2, lineWidth: 1)
                )

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

/// The artifact list in DISPLAY order: the flattened `artifactTree`
/// traversal — top-level files first (in wire order), then each folder in
/// first-seen order with its own files after it, recursively. That is NOT the
/// wire array's order, and the difference is the whole point: the pager walks
/// this so "next" means "the next row in the list I just tapped from".
///
/// Entries carry the tree's REWRITTEN leaf names, exactly what the tapped row
/// displayed — and, as ever, the url is what still knows the real server-side
/// name (see artifactParts).
///
/// A free function rather than logic inside a View body because it is the
/// pager's one testable seam; a SwiftUI View's body is not reachable from
/// XCTest. See ArtifactOrderTests.
func flattenArtifactOrder(_ artifacts: [WebArtifact]) -> [WebArtifact] {
    flattenArtifactNodes(artifactTree(artifacts))
}

private func flattenArtifactNodes(_ nodes: [ArtifactNode]) -> [WebArtifact] {
    var out: [WebArtifact] = []
    for node in nodes {
        switch node {
        case .file(let a):
            out.append(a)
        case .folder(_, let children):
            out.append(contentsOf: flattenArtifactNodes(children))
        }
    }
    return out
}

/// The page sequence ArtifactPagerView shows: display order when `selected`
/// is actually part of it, a SINGLETON of `selected` otherwise.
///
/// The fallback is not defensive noise: `showingArtifact` is @State and
/// outlives the `job` value it was picked from, so a poll that drops the
/// artifact leaves the sheet holding one the flattened order no longer
/// contains. It degrades to a single page with no chevrons rather than an
/// out-of-range index.
///
/// The `kraken://job/<slug>/artifact/<name>` deep link is NOT such a case,
/// despite resolving by NAME against the unrewritten list (see .onAppear
/// below): artifactTree rewrites names but carries every url through
/// untouched, so a deep-linked artifact's url is always in the order. That
/// mismatch — name in, url out — is the reason these two lists relate through
/// url and nothing else.
func artifactPages(in artifacts: [WebArtifact], selected: WebArtifact) -> [WebArtifact] {
    let order = flattenArtifactOrder(artifacts)
    return order.contains { $0.url == selected.url } ? order : [selected]
}

/// Position of `url` in an already-flattened sequence. Matching on url and
/// never on name is the invariant the whole pager rests on: artifactTree
/// rewrites nested names to leaves, so a name is neither unique nor stable,
/// while `WebArtifact.id` is already the url.
func artifactIndex(of url: String, in pages: [WebArtifact]) -> Int? {
    pages.firstIndex { $0.url == url }
}

/// What sits either side of `url`. Its own type rather than a tuple so the
/// lookup can be asserted on directly, matching ArtifactRef's reasoning.
struct ArtifactNeighbours {
    let prev: WebArtifact?
    let next: WebArtifact?
}

/// Clamped, never wrapping (spec Q1): nil at each end, and nil both ways for
/// a url the sequence doesn't hold.
func artifactNeighbours(of url: String, in pages: [WebArtifact]) -> ArtifactNeighbours {
    guard let i = artifactIndex(of: url, in: pages) else {
        return ArtifactNeighbours(prev: nil, next: nil)
    }
    return ArtifactNeighbours(
        prev: i > 0 ? pages[i - 1] : nil,
        next: i + 1 < pages.count ? pages[i + 1] : nil
    )
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

    // Capsule with a soft tint fill — the glass idiom's chip shape. The
    // leading pull-request glyph is decorative (no a11y label of its own), so
    // the spoken sentence is still exactly the chip's text.
    private var chipLabel: some View {
        HStack(spacing: 4) {
            Image(systemName: "arrow.triangle.pull")
            Text(label)
        }
        .font(.data(10, weight: .semibold))
        .foregroundStyle(color)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(color.opacity(0.13), in: Capsule())
        .overlay(Capsule().stroke(color.opacity(0.45), lineWidth: 1))
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
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(color.opacity(0.13), in: Capsule())
        .overlay(Capsule().stroke(color.opacity(0.45), lineWidth: 1))
        .accessibilityElement(children: .ignore)
    }
}

/// Formats an elapsed span as the app's compact age — "45s", "3m", "3h", "2d",
/// one unit and no decimals. Floored at 0: a stamp from the future (clock skew
/// between this phone and the Mac) reads as "0s ago", never as a negative.
///
/// Extracted from `tokensNote`, which held the app's only multi-unit age
/// LADDER until the board card's ROI badge needed the same thing — a grep for
/// `3600`/`86400` across `ios/Kraken/` returns three lines and all three are
/// now here. It is not the app's only relative-age rendering: `UsageView`'s
/// footer has a minutes-only one-off (`"just now"` under a minute, else
/// `"Nm ago"`), left as it is — it renders a different sentence with a
/// different vocabulary, and nothing in this change needed it.
///
/// A free function taking pre-computed seconds rather than a `Date`, exactly as
/// `wakeupCountdownLabel` below is: the clock read stays at the call site, so
/// this stays pure and testable.
func compactAgeLabel(secondsElapsed: Int) -> String {
    let s = max(0, secondsElapsed)
    if s < 60 { return "\(s)s" }
    if s < 3600 { return "\(s / 60)m" }
    if s < 86400 { return "\(s / 3600)h" }
    return "\(s / 86400)d"
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
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(KTheme.pollingSoft, in: Capsule())
    }
}

/// One themed card standing in for what used to be a `kSection` row group.
///
/// The detail sheet left the `Form` behind in the glass redesign: its sections
/// are now cards on a scrolling field, which is what lets the action bar float
/// over them. This is the shape every section shares — an optional
/// letterspaced mono caption, then the section's own rows — so a section reads
/// the same whether it holds two labelled rows or a whole question card.
///
/// Not private: it is the detail screen's section primitive and the composers'
/// sibling of `kSection`.
struct JobSectionCard<Content: View>: View {
    let title: String?
    let content: Content

    init(_ title: String? = nil, @ViewBuilder content: () -> Content) {
        self.title = title
        self.content = content()
    }

    var body: some View {
        ThemedCard {
            VStack(alignment: .leading, spacing: 8) {
                if let title {
                    Text(title).kLabelStyle()
                }
                content
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(16)
        }
    }
}

/// One action on the job sheet's floating bar.
///
/// The bar's job is placement only. Which actions EXIST is decided by the
/// `canSubmit`/`canKill`/… flags below, each of which mirrors a server 409 and
/// is unchanged by the redesign; this type just carries a visible action's
/// label, icon, tint and closure so the bar can decide whether it draws as the
/// prominent primary, as a glass icon button, or as a row in the overflow
/// menu. Nothing here can hide an action the rules made visible.
struct JobActionItem: Identifiable {
    let id: String
    let title: String
    let icon: String
    /// nil = inherit the bar's tint (Edit Spec and the destructive pair, which
    /// took the system default before the redesign too).
    let tint: Color?
    var isDestructive: Bool = false
    let run: () -> Void
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
    // The edit sheet renamed this job. `job` is frozen for this sheet's whole
    // lifetime, so its slug is stale from that moment on — see the edit
    // sheet's onDismiss, which closes the detail rather than showing a name
    // the board no longer has.
    @State private var wasRenamed = false
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
        !job.live && [BoardColumn.draft, .done].contains(column)
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
    // webSubmit 409s on an unparseable job even though it renders in draft.
    private var canSubmit: Bool {
        column == .draft && (job.err ?? "").isEmpty
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
        !isArchived && !job.live && [BoardColumn.draft, .done].contains(column)
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
    // Whether the floating bar renders at all. The same nine flags
    // `visibleActions` is built from, so the two can only ever agree: if this
    // is true the bar has at least one action, and if it is false there is
    // nothing to float.
    private var hasAnyAction: Bool {
        canSubmit || canRetry || canMarkDone || canSteer || isEditable || canKill
            || canArchive || canReactivate || isArchived
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                headerCard

                // zeus' triage estimate, spelled out. The board card carries
                // the same estimate as a one-line badge ("⚡ 4.0× 2→8"), which
                // is enough to RANK a column of blocked cards but not enough
                // to act on: it says a 2 and an 8 without saying what a 2 is.
                // This is where the two numbers get their words back, so it
                // sits above Tokens — a fact to act on outranks a fact about
                // the past.
                //
                // `load` being nil is what hides the whole section, and nil
                // has three meanings that all render the same way (never
                // estimated, cleared, archived — see WebJob.load). None of
                // them is an estimate of zero, so nothing here draws a 0 in
                // its place.
                //
                // A present but UNUSABLE pair hides it too, and for the same
                // reason rather than a second one: a zero on either side has
                // no ratio, and printing "unblock 0/10" under a Triage heading
                // asserts an estimate of zero — a thing this system says does
                // not exist. `badgeText` is nil on exactly that shape (see
                // WebJobLoad.roi), so it is the gate. Neither client can be
                // sent one by this server — a clear drops the key, a half
                // estimate is a 400 — so this is defence against a hand-edited
                // row, and web's drilldown hides on the same test, which is
                // what keeps the two boards drilling down on the same jobs.
                if let load = job.load, load.badgeText != nil {
                    JobSectionCard("Triage") { loadRows(load) }
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
                    JobSectionCard("Tokens") { tokensRows(tokens) }
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
                    JobSectionCard("Color") {
                        JobColorStrip(baseURL: baseURL, slug: job.slug, serverColor: job.color) { _ in
                            // Refresh the board so the card's swatch and this
                            // sheet's `job` snapshot pick the new color up.
                            onUpdate()
                        }
                    }
                }
                if let err = job.err, !err.isEmpty {
                    JobSectionCard("Error") { Text(err).font(.voice(13)).foregroundStyle(KTheme.counter) }
                }
                if isUnhealthy {
                    JobSectionCard("Health") { Text(job.health ?? "").font(.voice(13)).foregroundStyle(KTheme.revealed) }
                }
                // done/archived only: a reactivated job may still carry a
                // stale outcome in its file — it reads as never-completed
                if let outcome = job.outcome, !outcome.isEmpty, column == .done || isArchived {
                    JobSectionCard("Outcome") { Text(outcome).font(.voice(13)).foregroundStyle(KTheme.solved) }
                }
                if job.zeusStatus?.isEmpty == false || !(job.roster ?? []).isEmpty {
                    JobSectionCard("Roster") {
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
                    JobSectionCard("Threads") {
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
                    JobSectionCard("Detail") {
                        DetailCard(text: detail)
                    }
                }
                if let lastSent = job.lastSent, !lastSent.isEmpty {
                    JobSectionCard("Last sent") {
                        Text(lastSent)
                            .font(.data(12))
                            .foregroundStyle(KTheme.text2)
                    }
                }
                if let wakeup = job.wakeup, !wakeup.reason.isEmpty, job.state == "working" {
                    JobSectionCard("Poll timer") {
                        Text("↻ " + wakeup.reason)
                            .font(.data(12))
                            .foregroundStyle(KTheme.text2)
                    }
                }
                if let question = job.question {
                    JobSectionCard("Question") {
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
                        }
                    }
                }
                if !(job.docQuestions ?? []).isEmpty {
                    JobSectionCard("Doc questions") {
                        ForEach(sortedDocQuestions) { q in
                            DocQuestionCard(
                                item: q,
                                onAnswer: { text in answerDocQuestion(id: q.id, text: text) }
                            )
                        }
                        if sortedDocQuestions.contains(where: { !$0.isAnswered }) {
                            Text("answers land in kraken.db — the agent reads them when it next resumes")
                                .font(.voice(11))
                                .foregroundStyle(KTheme.text2)
                        }
                    }
                }
                // Deliberately NOT gated on `!isArchived`, unlike Doc
                // questions above: `feedback` IS served for archived jobs
                // (rows outlive the cell — see webJob's `feedback` key),
                // so copying that gate would hide exactly the notes worth
                // keeping. Un-addressed notes first, then closed ones, both in
                // server order — see sortedFeedback for why that is no longer
                // the same "needs you first" claim sortedDocQuestions makes.
                if !mergedFeedback.isEmpty {
                    JobSectionCard("Feedback · \(mergedFeedback.count)") {
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
                    }
                }
                if let artifacts = job.artifacts, !artifacts.isEmpty {
                    JobSectionCard("Artifacts") {
                        CollapsedArtifactList(artifacts: artifacts, onOpen: { showingArtifact = $0 })
                    }
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
        // The floating bar is a real safe-area INSET, not bottom padding on
        // the content: the scroll view reserves exactly the bar's measured
        // height (plus the home indicator), so the last card always scrolls
        // clear of it no matter how tall the bar gets — which padding
        // guesswork got wrong in the phase-1 mockup.
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if hasAnyAction {
                actionBar
            }
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
                    .padding(.horizontal, 14).padding(.vertical, 6)
                    .background(KTheme.accent, in: Capsule())
                    .shadow(color: KTheme.accent.opacity(0.35), radius: 10, y: 3)
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
            // A rename inside the sheet re-points the FORM in place, but this
            // view's `job` is a frozen value whose identity just changed and
            // nothing here can rewrite it — so the detail re-points the only
            // honest way it can: it closes back to the board, which the
            // onUpdate() above has already refreshed under the renamed name.
            // Closing on the sheet's dismissal rather than on the rename
            // itself is what lets the form stay open and usable afterwards.
            if wasRenamed { dismiss() }
        } content: {
            NavigationStack {
                JobFormView(
                    slug: job.slug, baseURL: baseURL, isNew: false,
                    onSaved: {
                        showingEdit = false
                        onUpdate()
                        dismiss()
                    },
                    onRenamed: { _ in
                        wasRenamed = true
                        onUpdate()
                    }
                )
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
                // The whole list, not just the tapped row: the sheet pages
                // across it, and `artifact` only picks the page it opens on.
                ArtifactPagerView(
                    artifacts: job.artifacts ?? [], selected: artifact, baseURL: baseURL,
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

    // MARK: - the job's header card

    /// The unlabelled first section: state, the ROI capsule, then the job's
    /// identifying rows. Same content the old header `kSection` carried, in a
    /// themed card — plus the triage ratio, which moved up here from the
    /// Triage card (see `loadRows`): it is the one fact worth reading beside
    /// the state, and it is drawn once, in one place, either way.
    private var headerCard: some View {
        ThemedCard {
            VStack(alignment: .leading, spacing: 10) {
                HStack(spacing: 8) {
                    StateCapsule(state: job.state)
                    if let load = job.load, load.badgeText != nil {
                        roiCapsule(load)
                    }
                    Spacer(minLength: 0)
                }
                if let wakeup = job.wakeup, job.state == "working" {
                    WakeupChip(wakeup: wakeup)
                }
                if let prs = job.prs, !prs.isEmpty {
                    // Horizontal scroller rather than a plain HStack: a job
                    // with several PRs used to run its chips off the row.
                    ScrollView(.horizontal, showsIndicators: false) {
                        HStack(spacing: 6) {
                            ForEach(prs, id: \.url) { pr in
                                PRChip(pr: pr)
                            }
                        }
                    }
                }
                cardDivider
                fieldRow("Repo", job.repo)
                if job.live, let cell = job.cell {
                    Button {
                        router.pendingSessionCell = cell
                        router.selectedTab = .sessions
                        dismiss()
                    } label: {
                        HStack {
                            Text("Cell").font(.voice(13)).foregroundStyle(KTheme.text2)
                            Spacer()
                            Text(cell).font(.data(12, weight: .medium)).foregroundStyle(KTheme.accent)
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
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(16)
        }
    }

    /// The leading bolt on `WebJobLoad.badgeText` — U+26A1 HIGH VOLTAGE SIGN
    /// and a space.
    ///
    /// It is lifted out at RENDER time and redrawn as an SF Symbol because the
    /// bucketed monospaced face `Font.data` now resolves to has no glyph for
    /// U+26A1, and the 26.3 simulator gives it no fallback — it draws as a
    /// missing-glyph box. The string itself is NOT touched: `badgeText` is
    /// built client-side (`WebJobLoad`, Models.swift) and pinned byte-identical
    /// against the web board's own badge format, so the glyph moves to SF
    /// Symbols here and nowhere else. Today the string always carries the
    /// prefix; the guard keeps a future prefix-less shape rendering untouched.
    private static let roiBoltPrefix = "⚡ "

    /// zeus' ROI ratio as a capsule beside the state, with its age.
    ///
    /// Painted by `BoardTheme.style(forLoadBand:)` exactly as the board card's
    /// badge is, so one estimate reads the same on both surfaces — including
    /// `cold`'s dash and dim, which stay on the badge alone and never on the
    /// age beside it. The rounded capsule and the SF Symbol bolt (see
    /// `roiBoltPrefix`) are the only changes: the ratio text, the band, the age
    /// clause and the one-line rule are the badge's own.
    private func roiCapsule(_ load: WebJobLoad) -> some View {
        let now = Int64(Date().timeIntervalSince1970 * 1000)
        let style = BoardTheme.style(forLoadBand: load.band ?? .even)
        let badge = load.badgeText ?? ""
        let hasBolt = badge.hasPrefix(Self.roiBoltPrefix)
        let ratio = hasBolt ? String(badge.dropFirst(Self.roiBoltPrefix.count)) : badge
        return HStack(spacing: 5) {
            HStack(spacing: 4) {
                if hasBolt {
                    // Decorative — no a11y label of its own, so the capsule's
                    // spoken sentence below is unchanged by the swap. Same
                    // pinned symbol size as the board card's LoadBadge, so
                    // one estimate reads the same on both surfaces.
                    Image(systemName: "bolt.horizontal.fill")
                        .font(.system(size: 9, weight: .semibold))
                }
                // one line, for the badge's own reason: the string carries two
                // internal spaces and breaking at either splits the ratio from
                // the pair it came from
                Text(ratio)
            }
                .lineLimit(1)
                .font(.data(10, weight: .semibold))
                .foregroundStyle(style.text)
                .padding(.horizontal, 8)
                .padding(.vertical, 3)
                .background(style.fill ?? .clear, in: Capsule())
                .overlay(
                    Capsule().stroke(
                        style.border,
                        style: StrokeStyle(lineWidth: 1, dash: style.dashed ? [3, 2] : [])
                    )
                )
                .opacity(style.opacity)
            if let age = load.age(nowMillis: now) {
                Text("\(age) ago")
                    .lineLimit(1)
                    .font(.data(9))
                    .foregroundStyle(KTheme.text2)
                    .opacity(0.7)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(load.summary(nowMillis: now) ?? (load.badgeText ?? ""))
    }

    /// The hairline between a card's header row and its rows — the shared
    /// translucent edge token, so it fades with the glass instead of reading
    /// as a drawn rule.
    private var cardDivider: some View {
        Rectangle()
            .fill(KTheme.hairline)
            .frame(height: 1)
    }

    // MARK: - floating action bar

    /// Every action visible under today's rules, in PRIORITY order.
    ///
    /// The `if` on each entry is the untouched visibility rule — each mirrors
    /// a server 409 and is quoted in the computed property it reads. Order is
    /// the only thing this list adds, and it decides placement alone: the
    /// first visible action becomes the prominent primary, the next few draw
    /// as glass icon buttons, and anything left falls into the overflow menu
    /// (`actionBar` below). Nothing is ever dropped, so an action reachable
    /// today is reachable after the restyle under exactly the same rule.
    ///
    /// The priority is "what is this job waiting for": a live cell wants
    /// steering, a draft wants submitting, a done job wants reactivating, an
    /// archived one wants restoring. Reversible bookkeeping (mark done,
    /// retry, edit, archive) sorts under those, and the two destructive
    /// actions sort last so a mis-tap can't reach them from the prominent
    /// slot.
    private var visibleActions: [JobActionItem] {
        var out: [JobActionItem] = []
        if canSteer {
            out.append(JobActionItem(
                id: "steer",
                title: isBlockedColumn ? "Unblock" : "Steer",
                icon: isBlockedColumn ? "lock.open.fill" : "arrowshape.turn.up.right.fill",
                tint: isBlockedColumn ? KTheme.revealed : KTheme.accent
            ) { showingSteer = true })
        }
        if canSubmit {
            out.append(JobActionItem(id: "submit", title: "Start", icon: "play.fill", tint: KTheme.accent) {
                runAction { try await APIClient.shared.queueJob(baseURL: baseURL, slug: job.slug) }
            })
        }
        if canReactivate {
            out.append(JobActionItem(
                id: "reactivate", title: "Reactivate",
                icon: "arrow.counterclockwise", tint: KTheme.revealed
            ) { showingReactivate = true })
        }
        if isArchived {
            out.append(JobActionItem(
                id: "unarchive", title: "Unarchive",
                icon: "tray.and.arrow.up", tint: KTheme.accent
            ) {
                runAction { try await APIClient.shared.unarchiveJob(baseURL: baseURL, slug: job.slug) }
            })
        }
        if canMarkDone {
            out.append(JobActionItem(
                id: "markDone", title: "Mark Done", icon: "checkmark", tint: KTheme.solved
            ) {
                runAction { try await APIClient.shared.markDone(baseURL: baseURL, slug: job.slug) }
            })
        }
        if canRetry {
            out.append(JobActionItem(
                id: "retry", title: "Retry", icon: "arrow.clockwise", tint: KTheme.revealed
            ) {
                runAction { try await APIClient.shared.retryJob(baseURL: baseURL, slug: job.slug) }
            })
        }
        if isEditable {
            out.append(JobActionItem(
                id: "edit", title: "Edit Spec", icon: "square.and.pencil", tint: nil
            ) { showingEdit = true })
        }
        if canArchive {
            out.append(JobActionItem(
                id: "archive", title: "Archive", icon: "archivebox", tint: KTheme.text2
            ) {
                runAction { try await APIClient.shared.archiveJob(baseURL: baseURL, slug: job.slug) }
            })
        }
        if canKill {
            out.append(JobActionItem(
                id: "kill", title: "Kill", icon: "xmark", tint: nil, isDestructive: true
            ) { showingKillConfirm = true })
        }
        if isArchived {
            out.append(JobActionItem(
                id: "delete", title: "Delete Forever", icon: "trash", tint: nil, isDestructive: true
            ) { showingDeleteConfirm = true })
        }
        return out
    }

    /// How many non-primary actions draw as icon buttons before the rest
    /// spill into the menu. Three fit beside the primary pill at phone width;
    /// a fourth would crowd it, and a menu holding ONE item is worse than
    /// showing that item — so the split is "all three, or two plus a menu".
    private static let inlineSecondaryLimit = 3

    private var secondaryActions: [JobActionItem] {
        let rest = Array(visibleActions.dropFirst())
        return rest.count <= Self.inlineSecondaryLimit ? rest : Array(rest.prefix(2))
    }

    private var overflowActions: [JobActionItem] {
        let rest = Array(visibleActions.dropFirst())
        return rest.count <= Self.inlineSecondaryLimit ? [] : Array(rest.dropFirst(2))
    }

    /// The floating glass bar: one prominent contextual primary, up to three
    /// glass icon buttons, and an ellipsis menu for the tail. It replaces the
    /// eleven-button horizontal scroller — which never told you which button
    /// mattered, and hid half of them off-screen — without changing which
    /// buttons exist.
    ///
    /// The icon buttons stay icon-only (carl: "no text within the circular
    /// buttons"), so `.accessibilityLabel` carries each name for VoiceOver
    /// exactly as it did before; the menu rows are labelled, which is what a
    /// menu is for.
    private var actionBar: some View {
        GlassEffectContainer(spacing: 10) {
            HStack(spacing: 10) {
                if let primary = visibleActions.first {
                    Button(role: primary.isDestructive ? .destructive : nil, action: primary.run) {
                        Label(primary.title, systemImage: primary.icon)
                            .font(.voice(15, weight: .semibold))
                            .padding(.horizontal, 4)
                    }
                    .buttonStyle(.glassProminent)
                    .tint(primary.tint ?? KTheme.accent)
                    .accessibilityLabel(primary.title)
                }
                ForEach(secondaryActions) { action in
                    Button(role: action.isDestructive ? .destructive : nil, action: action.run) {
                        Image(systemName: action.icon)
                            .font(.system(size: 16, weight: .semibold))
                            .frame(minWidth: 22, minHeight: 22)
                    }
                    .buttonStyle(.glass)
                    .tint(action.tint ?? KTheme.accent)
                    .accessibilityLabel(action.title)
                }
                if !overflowActions.isEmpty {
                    Menu {
                        ForEach(overflowActions) { action in
                            Button(role: action.isDestructive ? .destructive : nil, action: action.run) {
                                Label(action.title, systemImage: action.icon)
                            }
                        }
                    } label: {
                        Image(systemName: "ellipsis")
                            .font(.system(size: 16, weight: .semibold))
                            .frame(minWidth: 22, minHeight: 22)
                    }
                    .menuStyle(.button)
                    .buttonStyle(.glass)
                    .tint(KTheme.text2)
                    .accessibilityLabel("More actions")
                }
            }
            .controlSize(.large)
        }
        .padding(.horizontal, 16)
        .padding(.bottom, 8)
    }

    // MARK: - triage estimate

    /// zeus' two 1–10 estimates with their anchor labels, the ratio between
    /// them, and zeus' one-line rationale.
    ///
    /// The ladders come from `LoadAnchors`, which carries the same twenty
    /// strings the skill zeus estimates from and the web board's drilldown
    /// render (ten rungs per ladder, two ladders) — a
    /// Go test pins all three byte-identical, so the words here are the words
    /// zeus was reading when it picked the number. A rung off the ladder (a
    /// hand-edited row; the API refuses one) simply loses its label rather
    /// than printing a placeholder beside a real-looking digit.
    ///
    /// The ratio is drawn as the BADGE rather than re-derived here. It is the
    /// app's one rendering of `scope / unblock` (`WebJobLoad.badgeText`, with
    /// the rounding rule and the band both living on the model), and going
    /// through it is what keeps this sheet and the card from ever printing two
    /// different ratios for one estimate — the rounding is half-away-from-zero
    /// to agree with web's `toFixed(1)`, which is exactly the kind of detail a
    /// second copy loses. It repeats `u→s` after the ratio; that repetition is
    /// the badge's own text, pinned identical across both boards, and this
    /// view is not free to trim it.
    ///
    /// Both the badge and the band are nil for the same single reason — an
    /// unusable pair (a zero on either side, which no server serves) — and the
    /// section's gate already refuses exactly that shape, so the unwrap below
    /// is the defence the card's badge keeps, not a case to expect here.
    ///
    /// The AGE is required rather than decoration, for the reason the card and
    /// web's drilldown both carry one: nothing ever refreshes an estimate.
    /// zeus writes it, carl answers, the cell later dies, the daemon's
    /// `health` files the card back under BLOCKED — and with zeus gone a
    /// three-day-old estimate of a situation that has moved on reads exactly
    /// like a current fact. `WebJobLoad.age(nowMillis:)` is the one formatter;
    /// the clock is read at this call site so the model stays pure.
    @ViewBuilder
    private func loadRows(_ load: WebJobLoad) -> some View {
        // Read here rather than inside the model — the same split `tokensNote`
        // and the card's LoadBadge use. Recomputed each render, which this
        // sheet's own refresh already drives; the age is one coarse unit, so
        // it needs no ticker.
        let now = Int64(Date().timeIntervalSince1970 * 1000)
        VStack(alignment: .leading, spacing: 6) {
            loadRungRow("unblock", rung: load.unblock, anchor: LoadAnchors.label(unblock: load.unblock))
            loadRungRow("next step", rung: load.scope, anchor: LoadAnchors.label(scope: load.scope))
            // The badge and its age are no longer drawn HERE: they moved to
            // the header card's state row (`roiCapsule`), which is where the
            // ratio earns its place beside the state. It is still drawn
            // exactly once, still through `WebJobLoad.badgeText` and
            // `BoardTheme.style(forLoadBand:)`, and still under this same
            // section's gate — the two just render in different cards now.
            //
            // `note` is legitimately "" — the estimate is the payload and the
            // server keeps a numbers-only write rather than refusing it — so
            // an empty one drops the row instead of leaving a blank line where
            // a rationale should be.
            if !load.note.isEmpty {
                Text(load.note)
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.vertical, 2)
        // ONE a11y element for the whole section: read row by row, VoiceOver
        // gives "unblock, 2/10, Read one page, pick" and then says the same
        // numbers again inside the badge and once more in the age, which is
        // three passes over one estimate. The sentence replaces all of it.
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(loadA11yLabel(load, nowMillis: now))
    }

    /// The section's spoken form: `WebJobLoad.summary(nowMillis:)` — the same
    /// sentence the card's badge speaks and web's tooltip shows — with the two
    /// anchor labels appended.
    ///
    /// Appended rather than spliced in beside each number, because that
    /// sentence is a pinned shared string: its clause order is fixed and this
    /// view is not free to rewrite it. So the words ride after it, which is
    /// also the reading order that matters — the numbers are what carl answers
    /// zeus in, and the anchors are the gloss on them.
    ///
    /// They are appended at all because `children: .ignore` above means this
    /// label is the section's ONLY spoken content: the anchors are on screen
    /// and a VoiceOver user has no other route to them, so dropping them here
    /// would make the ladder sighted-only. `summary` is nil exactly when the
    /// pair is unusable, which the section's gate has already refused; the
    /// fallback keeps this total rather than speaking an empty label.
    private func loadA11yLabel(_ load: WebJobLoad, nowMillis: Int64) -> String {
        var parts: [String] = []
        if let summary = load.summary(nowMillis: nowMillis) {
            parts.append(summary)
        } else {
            parts.append("unblock effort \(load.unblock)/10 · next step \(load.scope)/10")
        }
        if let anchor = LoadAnchors.label(unblock: load.unblock) {
            parts.append("unblock: " + anchor)
        }
        if let anchor = LoadAnchors.label(scope: load.scope) {
            parts.append("next step: " + anchor)
        }
        return parts.joined(separator: " · ")
    }

    /// One rung: its caption, the raw `n/10` the wire carried, and what that
    /// rung means. The number is kept verbatim beside the words because the
    /// words are a reading of it, not a replacement — carl answers zeus in
    /// numbers.
    ///
    /// The label takes the trailing space and wraps rather than truncating:
    /// the longest anchors run past a phone's width, and half of "Decision
    /// batch needing new…" is worse than two lines.
    private func loadRungRow(_ caption: String, rung: Int, anchor: String?) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(caption).font(.data(10)).foregroundStyle(KTheme.text2)
                .lineLimit(1).fixedSize()
            Text("\(rung)/10").font(.data(11, weight: .semibold)).foregroundStyle(KTheme.text)
                .lineLimit(1).fixedSize()
            if let anchor {
                Text(anchor)
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
                    .multilineTextAlignment(.trailing)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .trailing)
            } else {
                Spacer()
            }
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
                    // The origin split, drawn. Purely a second reading of the
                    // two counters directly above — nothing here is a number
                    // the row doesn't already print — so it carries no label
                    // and no a11y element of its own, and it is dropped
                    // whenever the split itself is (a server predating those
                    // keys sends neither half).
                    originBar(zeus: min(zeus, total), total: total)
                }
            }
            Text(tokensNote(t)).font(.data(9)).foregroundStyle(KTheme.text2).opacity(0.55)
        }
        .padding(.vertical, 2)
    }

    /// zeus' share of the spend as a two-segment capsule bar: the accent for
    /// zeus, the accent at a third for everything it delegated to.
    ///
    /// A zero total would divide by zero, so it degrades to an all-subagent
    /// bar — unreachable through `tokensRows`' own gate (a zero total with a
    /// zeus split can't be served), and cheaper than a second gate here.
    private func originBar(zeus: Int64, total: Int64) -> some View {
        let fraction = total > 0 ? min(1, Double(zeus) / Double(total)) : 0
        return GeometryReader { geo in
            HStack(spacing: 2) {
                // Each half is drawn only when it has width: a zero-width
                // Capsule still takes the HStack's 2pt spacing, which reads as
                // a gouge in the bar rather than as an empty half.
                if fraction > 0 {
                    Capsule()
                        .fill(KTheme.accent)
                        .frame(width: geo.size.width * fraction)
                }
                if fraction < 1 {
                    Capsule().fill(KTheme.accent.opacity(0.35))
                }
            }
        }
        .frame(height: 5)
        .padding(.top, 4)
        .accessibilityHidden(true)
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
            let seconds = Int(Date().timeIntervalSince1970) - Int(t.updatedAt / 1000)
            note += " · \(compactAgeLabel(secondsElapsed: seconds)) ago"
        }
        return note
    }

    /// One `label … value` row inside a card — the LabeledContent shape the
    /// glass idiom uses: prose caption on the left, the value in mono on the
    /// right, since every value here (a repo, a slug) is an identifier.
    private func fieldRow(_ label: String, _ value: String) -> some View {
        HStack {
            Text(label).font(.voice(13)).foregroundStyle(KTheme.text2)
            Spacer()
            Text(value).font(.data(12, weight: .medium)).foregroundStyle(KTheme.text)
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

    // Notes with no `addressed` line first, closed ones after, each keeping
    // server order. The ordering survives but its JUSTIFICATION changed: this
    // is no longer "still needs you first", because `addressed` is written by
    // the nightly dream pass alone and an ordinary cell acts on a note while
    // deliberately leaving the column empty. What sorts to the top is now
    // simply the notes no durable fix has shipped for, which is most of them —
    // still the more useful half to read first, but not a queue of work owed.
    // The explicit offset tiebreak is load-bearing for the same reason it is
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
/// live, red once it's closed (paused, revivable — the CLI's running/waiting
/// status pairing). Same outlined-stroke treatment as PRChip
/// above, so a job's chips all read as one family.
/// Not private: the board card renders these too, in its own chip row (see
/// JobCardView), so a job's branches read the same on the board as they do in
/// the detail sheet.
struct ThreadChip: View {
    let thread: WebThread

    var body: some View {
        let color = thread.isLive ? KTheme.solved : KTheme.stopped
        HStack(spacing: 4) {
            // A mind thread (the one ◆ mind captures route to) wears the
            // diamond instead of the branch glyph — web's `mind` chip.
            Image(systemName: thread.isMind ? "diamond.fill" : "arrow.triangle.branch")
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
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(color.opacity(0.13), in: Capsule())
        .overlay(Capsule().stroke(color.opacity(0.45), lineWidth: 1))
        .contentShape(Capsule())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(thread.isMind ? "mind thread" : "thread") \(thread.displayTitle) — \(thread.isLive ? "live" : "closed")")
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
                .buttonStyle(.glassProminent)
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
                        // `model` is nil deliberately: this optimistic row is
                        // what the list shows for the second or so before the
                        // next board poll replaces it with the server's, and
                        // this call site does not choose a model (the server
                        // resolves the branch's tier itself). Filling in a
                        // guess would flash a model the thread may not run.
                        tid: tid, title: name, state: "live", pane: nil, model: nil,
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
                JobSectionCard("Spec") {
                    DetailCard(text: spec)
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

/// The `detail` field: the cell's last words / blocked reason, rendered as a
/// plain markdown card — no leading state bar (carl's call: the sheet's state
/// capsule already answers "how is it going", so a colored bar here repeated
/// it). Replaces the old escalation body card now that escalations are gone
/// from the wire.
struct DetailCard: View {
    let text: String

    var body: some View {
        MarkdownText(markdown: text)
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .cardBackground(bar: false)
    }
}

/// One pending question (header + prose + option buttons). Tapping an option
/// answers with its 1-based index within THIS question's own options list —
/// there's no free-text composer anymore, the tap IS the answer, sent
/// straight to POST /api/question.
struct QuestionCard: View {
    let item: WebQuestionItem
    var onSelect: (Int) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Label(item.header, systemImage: "flag.fill")
                .font(.data(10, weight: .semibold))
                .foregroundStyle(KTheme.counter)
            Text(item.question)
                .font(.voice(13, weight: .semibold))
                .foregroundStyle(KTheme.text)
                .fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: 8) {
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
                        .padding(.horizontal, 12)
                        .padding(.vertical, 9)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        // Accent-tinted glass idiom, replacing the old sharp
                        // hairline rect: the option still reads as the one
                        // tappable thing in the card, and the tap still sends
                        // this option's 1-based index within THIS question.
                        .background(
                            KTheme.accent.opacity(0.10),
                            in: RoundedRectangle(cornerRadius: 12, style: .continuous)
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: 12, style: .continuous)
                                .stroke(KTheme.accent.opacity(0.35), lineWidth: 1)
                        )
                        .contentShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        // The "a human is needed here" wash the section's List row used to
        // carry — now inside the card, since the card IS the row.
        .background(
            KTheme.counterSoft.opacity(0.7),
            in: RoundedRectangle(cornerRadius: 16, style: .continuous)
        )
    }
}

/// A multi-select or multi-question question's header + prose, read-only —
/// no option buttons, since a single digit keystroke can't answer it (see
/// the count/multiSelect gate above QuestionCard's call site).
struct QuestionSummary: View {
    let item: WebQuestionItem

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(item.header, systemImage: "flag.fill")
                .font(.data(10, weight: .semibold))
                .foregroundStyle(KTheme.counter)
            Text(item.question)
                .font(.voice(13, weight: .semibold))
                .foregroundStyle(KTheme.text)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(
            KTheme.counterSoft.opacity(0.7),
            in: RoundedRectangle(cornerRadius: 16, style: .continuous)
        )
    }
}

/// One doc-question row. Open questions render the full card (badge +
/// text + context + recommendation + accept/free-text answer controls);
/// answered ones collapse to a single dimmed "✓ <answer>" row. `onAnswer`
/// carries the answer text straight up to JobDetailView's answerDocQuestion,
/// which posts it and re-runs the same busy/error/dismiss handling as every
/// other board action.
struct DocQuestionCard: View {
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

    // Soft-filled capsule matching PRChip.chipLabel's glass-idiom treatment —
    // used for both the open and answered states so the badge reads the
    // same regardless of which branch renders it.
    private var badge: some View {
        Text(item.importance)
            .font(.data(9, weight: .semibold))
            .foregroundStyle(badgeColor)
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(badgeColor.opacity(0.15), in: Capsule())
            .overlay(Capsule().stroke(badgeColor.opacity(0.45), lineWidth: 1))
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
                Spacer(minLength: 0)
            }
            // The whole row dims: an answered question is settled business.
            .opacity(0.6)
            .padding(.vertical, 4)
        } else {
            VStack(alignment: .leading, spacing: 8) {
                badge
                Text(item.text)
                    .font(.voice(13, weight: .semibold))
                    .foregroundStyle(KTheme.text)
                    .fixedSize(horizontal: false, vertical: true)
                if let context = item.context, !context.isEmpty {
                    Text(context)
                        .font(.voice(12))
                        .foregroundStyle(KTheme.text2)
                        .fixedSize(horizontal: false, vertical: true)
                }
                if let recommendation = item.recommendation, !recommendation.isEmpty {
                    Label("suggests: \(recommendation)", systemImage: "lightbulb")
                        .font(.voice(12))
                        .foregroundStyle(KTheme.polling)
                        .fixedSize(horizontal: false, vertical: true)
                    Button("Accept recommendation") {
                        onAnswer(recommendation)
                    }
                    .font(.data(11, weight: .semibold))
                    .buttonStyle(.glass)
                }
                HStack(spacing: 8) {
                    TextField("answer", text: $answerText)
                        .kField()
                    Button("Send") {
                        let trimmed = answerText.trimmingCharacters(in: .whitespacesAndNewlines)
                        guard !trimmed.isEmpty else { return }
                        onAnswer(trimmed)
                    }
                    .font(.data(11, weight: .semibold))
                    .buttonStyle(.glassProminent)
                    .disabled(answerText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(12)
            .background(
                KTheme.surface,
                in: RoundedRectangle(cornerRadius: 16, style: .continuous)
            )
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
                // Inline in the section card, matching SteerView's composer
                // (see the comment there), so both steer-family sheets read
                // the same.
                HighlightingTextEditor(text: $text)
                    .frame(minHeight: 120, alignment: .topLeading)
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
                .buttonStyle(.glassProminent)
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
                        DetailCard(text: detail)
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
                // Inline in the section card, not kField chrome: the composer
                // reads as one continuous surface with the chips row above and
                // the attachment rows below, rather than a floating bordered
                // box between two card fragments.
                HighlightingTextEditor(text: $text)
                    .frame(minHeight: 120, alignment: .topLeading)
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
                .buttonStyle(.glassProminent)
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
                // Still no onFeedbackCreated: SteerView has no optimistic
                // overlay to fold a note into, so the note lands server-side
                // and shows up on the next poll — unchanged by the pager.
                ArtifactPagerView(artifacts: job.artifacts ?? [], selected: artifact, baseURL: baseURL)
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
