import SwiftUI
import Observation

// The MENTAL MODEL: the second pane of the Memory tab beside the library, plus
// the pure decisions and the one shared model every capture surface in the app
// asks the same question through.
//
// The library is what carl chose to KEEP, and this is what he has actually
// READ. One row per thing in his head, frozen at the moment he marked it.
//
// Two rules run through every line here, and both are the LibraryView posture
// taken literally:
//
// * SERVER state is the only truth for a toggle. There is no local "I tapped
//   it" latch anywhere — a write posts and then refetches, so a refused mark
//   leaves the control reading unmarked and an invalidation made by the
//   daemon's verify pass drops it back on the next read without anything here
//   noticing. That matters more than it does for ☆ save, because the verify
//   pass genuinely does invalidate rows behind the app's back.
// * A row outlives its job. There is no FK and no cascade, so an item whose
//   job was archived, torn down or hard deleted renders whole — which is
//   exactly why the frozen content is stored ON the row rather than looked up.
//   Nothing in this file resolves a slug to anything.

// MARK: - is this marked?

/// The LIVE whole-artifact reading `(slug, name)` names, or nil.
///
/// `libraryItemMatching`'s contract with two differences, both about what a
/// MARK means rather than about how the lookup works:
///
/// * Only LIVE items match. An invalidated reading is history the pane keeps,
///   not a claim that the bytes on screen are ones carl has read — so the ✓
///   must drop back to unmarked the moment the verify pass says the artifact
///   moved.
/// * `home` is not a parameter at all. An item records the slug and the name
///   it was frozen under, and the same artifact reached through the `archive`
///   namespace is the same artifact; the web's `artifactReadMatch` ignores the
///   home segment for this reason and this is the same decision spelled as an
///   absent argument.
///
/// A free function rather than a method on the model, for `libraryItemMatching`
/// and `flattenArtifactOrder`'s reason: it is the whole of the "is this
/// marked?" decision, the artifact viewer's toggle depends on it, and a
/// closure inside a View is unreachable from a test.
func mmMatch(items: [WebMMItem], slug: String, name: String) -> WebMMItem? {
    let want = mmTrimLeadingSlashes(name)
    return items.first {
        $0.source == "artifact" && $0.live
            && $0.slug == slug && mmTrimLeadingSlashes($0.sourceRef) == want
    }
}

/// The server stores `source_ref` with its leading slashes trimmed; so does
/// every comparison against one.
func mmTrimLeadingSlashes(_ s: String) -> String {
    var out = Substring(s)
    while out.hasPrefix("/") { out = out.dropFirst() }
    return String(out)
}


// MARK: - filters

/// The pane's chips, in the web pane's order. Five cases: `all`, then THREE
/// that are a KIND — the store's whole closed vocabulary, and every item has
/// exactly one of them — then `invalidated`, which cuts ACROSS all of them.
/// An invalidated reading is still a reading, so `invalidated` is a subset of
/// `all` rather than a fourth bucket, and it is the three KIND counts that sum
/// to `all`.
enum MMFilter: String, CaseIterable, Identifiable {
    case all
    case decision
    case reading
    case note
    case invalidated

    var id: String { rawValue }

    /// The chip's word. `reading` reads as "read" — it is what carl did, not a
    /// genre — matching the row badge and the web pane's label.
    var label: String {
        switch self {
        case .all: return "all"
        case .decision: return "decisions"
        case .reading: return "read"
        case .note: return "notes"
        case .invalidated: return "dead"
        }
    }

    func matches(_ it: WebMMItem) -> Bool {
        switch self {
        case .all: return true
        case .invalidated: return !it.live
        case .decision, .reading, .note: return it.kind == rawValue
        }
    }
}

/// Every chip's count in one pass over the list.
///
/// A dictionary keyed by the filter itself rather than five named fields: the
/// chips are built by iterating `MMFilter.allCases`, so a lookup by case is
/// what the view actually needs, and a struct would make adding a chip a
/// two-place edit. Every case is present, so a subscript never has to fall
/// back — `?? 0` at the call site is belt only.
func mmCounts(items: [WebMMItem]) -> [MMFilter: Int] {
    var out: [MMFilter: Int] = [:]
    for f in MMFilter.allCases { out[f] = 0 }
    out[.all] = items.count
    for it in items {
        if let kind = MMFilter(rawValue: it.kind), kind != .all, kind != .invalidated {
            out[kind, default: 0] += 1
        }
        if !it.live { out[.invalidated, default: 0] += 1 }
    }
    return out
}

// MARK: - row labels

/// The row's badge word.
func mmKindLabel(_ kind: String) -> String {
    switch kind {
    case "reading": return "read"
    case "decision": return "decision"
    case "note": return "note"
    default: return kind
    }
}

/// The one-line provenance under the headline — how much to trust the frozen
/// text is mostly a question of which surface minted it.
func mmSourceLabel(_ source: String) -> String {
    switch source {
    case "question": return "question"
    case "artifact": return "artifact"
    case "artifact-element": return "selection"
    case "code": return "code"
    case "agent-output": return "agent"
    case "manual": return "manual"
    default: return source
    }
}

/// How much of a marked SELECTION's first line the collapsed row shows —
/// `ELEMENT_HEAD_MAX` on the board, held at the same 80 so the two clients cut
/// one row at the same place.
let mmHeadlineRuneCap = 80

/// What this row IS, in one line.
///
/// For a code reading and a whole-artifact reading the frozen CONTENT is the
/// payload — a block of source, an artifact's name — and the useful headline
/// is where it came from, which is what carl recognizes. For a decision and a
/// note the content IS the thing, so it leads and the provenance goes below.
///
/// A marked SELECTION is the third shape and it gets its own branch, which is
/// load-bearing rather than cosmetic: its content is a multi-line block of
/// markdown SOURCE (the server locates the rendered capture in the file and
/// stores whole lines), so returning it whole put a pipe table into a
/// two-line headline AND — because the expansion body is "the content unless
/// the headline already is it" — left the expansion with nothing to render,
/// which made the markdown branch below unreachable. The headline is
/// therefore its first non-empty line, truncated (`mmElementHeadline`); the
/// block itself belongs in the expansion, rendered.
func mmHeadline(_ it: WebMMItem) -> String {
    if it.source == "code" {
        guard let sha = it.anchor?.anchorSHA, sha.count >= 7 else { return it.sourceRef }
        return it.sourceRef + " @ " + String(sha.prefix(7))
    }
    if it.source == "artifact" { return it.slug + "/" + it.sourceRef }
    if it.source == "artifact-element" { return mmElementHeadline(it) }
    return it.content
}

/// The row's title line: the committed `title` when the item has one, else
/// the first non-empty line of its content.
///
/// Two legacy sources keep their old headline instead of a content line,
/// because their content is not prose: a whole-artifact reading's content is
/// the artifact's NAME (the headline already says `slug/name`), and a code
/// reading's is a block of source whose first line is rarely what carl would
/// recognize — its coordinates are. Everything else (decisions, notes, agent
/// output, selections) leads with its first line, cut like a selection's.
func mmRowTitle(_ it: WebMMItem) -> String {
    if let title = it.title?.trimmingCharacters(in: .whitespacesAndNewlines), !title.isEmpty {
        return title
    }
    if it.source == "code" || it.source == "artifact" { return mmHeadline(it) }
    let first = it.content
        .split(separator: "\n", omittingEmptySubsequences: false)
        .map { $0.trimmingCharacters(in: .whitespaces) }
        .first { !$0.isEmpty }
    guard let first else { return mmHeadline(it) }
    guard first.count > mmHeadlineRuneCap else { return first }
    return String(first.prefix(mmHeadlineRuneCap)) + "…"
}

/// The scope chip's text: `universal`, `<repo>/<dir>`, or `<repo>` alone for
/// the repo root. nil for a row with no scope to show — a legacy row reads
/// scope `repo` (the column default) with no repo named, and a chip saying
/// nothing would be noise.
func mmScopeLabel(_ it: WebMMItem) -> String? {
    switch it.scope {
    case "universal":
        return "universal"
    case "repo":
        guard let repo = it.repo, !repo.isEmpty else { return nil }
        let dir = mmTrimLeadingSlashes(it.dir ?? "")
        return dir.isEmpty ? repo : repo + "/" + dir
    default:
        return nil
    }
}

/// A marked selection's one-line headline: its FIRST non-empty line, trimmed
/// and hard-truncated.
///
/// Deliberately the same rule as the board's `elementHeadline`
/// (MentalModelPane.tsx) down to the fallback wording, so one row does not
/// read as two different things on the two clients: first non-empty line,
/// trimmed, cut at 80 with `…`, and `selection from <source_ref>` when the
/// content has no non-empty line at all.
///
/// The first line rather than a "selection from X" label: the provenance is
/// already on the meta line below, and what a reader scanning the list needs
/// is WHICH selection this is — a table's first line is its header row, a
/// list's is its first item, both recognizable at a glance.
///
/// Two deliberate divergences from the web's elementHeadline, both display-
/// only. The cut counts CHARACTERS (grapheme clusters) where the web counts
/// code points — both cuts cannot split a code point, and Swift's Character
/// additionally cannot split a combining sequence or a ZWJ emoji, which is
/// what a phone renders. And the per-line trim is .whitespaces (Zs + tab)
/// where the web's String.trim also strips \r and the unicode line
/// separators — immaterial because the element route stores \r\n-normalized
/// content, so only a lone CR could ever reach this trim.
func mmElementHeadline(_ it: WebMMItem) -> String {
    let first = it.content
        .split(separator: "\n", omittingEmptySubsequences: false)
        .map { $0.trimmingCharacters(in: .whitespaces) }
        .first { !$0.isEmpty }
    guard let first else { return "selection from " + it.sourceRef }
    guard first.count > mmHeadlineRuneCap else { return first }
    return String(first.prefix(mmHeadlineRuneCap)) + "…"
}

// MARK: - the model

/// The app's mental-model state: one list, and every write that touches it.
///
/// Each surface holds its own instance (`LibraryViewModel`'s shape) rather
/// than a singleton, because the three surfaces have disjoint lifetimes — the
/// memory pane, an artifact sheet and a job sheet are never usefully sharing
/// one refresh — and a shared observable would make every job sheet re-render
/// on the memory pane's poll.
///
/// EVERY write refetches, and refetches on failure too. That is the whole
/// consistency story: no branch here mirrors an intent locally, so a refused
/// mark cannot leave a control claiming something the server does not hold.
@Observable
final class MMViewModel {
    /// Served order, held as served: newest first, never re-sorted.
    var items: [WebMMItem] = []
    var errorMessage: String?
    var loaded = false
    /// Item ids with a remove in flight — the row's ✕ disables, so a double
    /// tap can't race two writes against one row (the DreamView/LibraryView
    /// idiom). Per row rather than a whole-list lock: the rows are
    /// independent.
    var inFlight: Set<Int64> = []
    /// A CREATE is in flight. One flag rather than a set, because a create has
    /// no id yet to key one by — and because every create surface in the app
    /// is a single button whose only job while posting is to be disabled.
    var writing = false

    func refresh(baseURL: String) async {
        do {
            items = try await APIClient.shared.fetchMM(baseURL: baseURL).items
            errorMessage = nil
        } catch {
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        loaded = true
    }

    /// Runs one write and then re-reads, whatever happened. Answers whether
    /// the POST itself came back clean.
    ///
    /// The refetch is deliberately NOT in the success branch: a write that
    /// failed may still have landed (a timeout on a 200 that never arrived),
    /// and the only honest way to find out is to ask. It is also what makes
    /// every caller below three lines long.
    ///
    /// The Bool is about the WRITE and nothing else — deliberately captured
    /// before the refetch, because `refresh` rewrites `errorMessage` and a
    /// caller reading that flag afterwards would be told a successful write
    /// failed whenever the re-read did.
    @discardableResult
    private func write(baseURL: String, _ body: () async throws -> Void) async -> Bool {
        guard !writing else { return false }
        writing = true
        var ok = true
        do {
            try await body()
            errorMessage = nil
        } catch {
            ok = false
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        writing = false
        await refresh(baseURL: baseURL)
        return ok
    }

    @discardableResult
    func addNote(baseURL: String, text: String, context: String) async -> Bool {
        await write(baseURL: baseURL) {
            _ = try await APIClient.shared.addMMNote(baseURL: baseURL, text: text, context: context)
        }
    }

    /// Contradiction flags an agent raised to carl, newest first — the
    /// pane's Flags section. Only `raised` ones: a pending flag is still
    /// zeus' to raise or dismiss, and a resolved one needs nothing from carl.
    var flags: [WebMMFlag] = []
    /// Flag ids with a resolve in flight — both buttons on that flag disable.
    var resolving: Set<Int64> = []

    /// A failed flag read leaves the section as it was and says nothing: the
    /// item list's own error line already covers an unreachable server.
    func refreshFlags(baseURL: String) async {
        guard let page = try? await APIClient.shared.fetchMMFlags(baseURL: baseURL, state: "raised") else { return }
        flags = page.flags.filter { $0.resolution.isEmpty }
    }

    /// Carl's verdict on one raised flag. Re-reads BOTH lists afterwards,
    /// whatever happened: `drop` invalidates the flagged item, so the item
    /// list moves too.
    func resolveFlag(baseURL: String, id: Int64, resolution: String) async {
        guard !resolving.contains(id) else { return }
        resolving.insert(id)
        var failure: String?
        do {
            try await APIClient.shared.resolveMMFlag(baseURL: baseURL, id: id, resolution: resolution)
        } catch {
            failure = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        resolving.remove(id)
        await refreshFlags(baseURL: baseURL)
        await refresh(baseURL: baseURL)
        // After the re-read, which rewrites `errorMessage` on its own: a
        // refused resolve (a 409 on a flag already settled) must still say so.
        if let failure { errorMessage = failure }
    }

    /// Drops one mark. Per-row guard rather than `write`'s single flag: a
    /// remove is addressed by id, so two different rows may legitimately be in
    /// flight at once while the same row twice may not.
    func remove(baseURL: String, id: Int64) async {
        guard !inFlight.contains(id) else { return }
        inFlight.insert(id)
        do {
            try await APIClient.shared.dropMM(baseURL: baseURL, id: id)
            errorMessage = nil
        } catch {
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        inFlight.remove(id)
        // Refetched rather than mirrored: a remove INVALIDATES the row instead
        // of deleting it, so the row stays — with a reason and a dead style —
        // and dropping it locally would show a deletion the server never made.
        await refresh(baseURL: baseURL)
    }
}

// MARK: - the pane

/// MODEL mode's screen: every mark, newest first, filtered by chip, each
/// expandable to the text that was frozen.
///
/// Its own `List` in the LibraryList shape rather than a section inside the
/// Library pane: the two are peers, and the mode switch that selects between
/// them (MemoryRootView.Mode) is a switch and nothing more.
struct MentalModelList: View {
    let baseURL: String

    @State private var vm = MMViewModel()
    @State private var filter: MMFilter = .all
    @State private var noteOpen = false
    @State private var note = ""
    @State private var noteContext = ""

    private var counts: [MMFilter: Int] { mmCounts(items: vm.items) }
    /// ARRIVAL ORDER, narrowed and never re-sorted: the server serves newest
    /// first and a list that reorders itself is a list you cannot aim at.
    private var rows: [WebMMItem] { vm.items.filter { filter.matches($0) } }

    var body: some View {
        List {
            if !vm.flags.isEmpty { flagsSection }
            chipsSection
            if noteOpen { noteSection }
            if let errorMessage = vm.errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
            if !rows.isEmpty {
                kSection("\(filter.label) · \(rows.count)") {
                    ForEach(rows) { it in
                        MMRowView(item: it, baseURL: baseURL, busy: vm.inFlight.contains(it.id)) {
                            Task { await vm.remove(baseURL: baseURL, id: it.id) }
                        }
                    }
                }
            } else {
                kSection { Text(emptyMessage).font(.voice(12)).foregroundStyle(KTheme.text2) }
            }
        }
        .kListStyle()
        .refreshable {
            await vm.refreshFlags(baseURL: baseURL)
            await vm.refresh(baseURL: baseURL)
        }
        .task(id: baseURL) {
            await vm.refreshFlags(baseURL: baseURL)
            await vm.refresh(baseURL: baseURL)
        }
    }

    /// Contradictions an agent found and zeus raised: each names the item it
    /// is about and, when there is one, the item or transcript it disagrees
    /// with. Above the rows because it is the one thing on this pane asking
    /// carl for a decision.
    private var flagsSection: some View {
        kSection("flags · \(vm.flags.count)") {
            ForEach(vm.flags) { flag in
                MMFlagRowView(
                    flag: flag,
                    item: vm.items.first { $0.id == flag.itemId },
                    busy: vm.resolving.contains(flag.id),
                    onResolve: { resolution in
                        Task { await vm.resolveFlag(baseURL: baseURL, id: flag.id, resolution: resolution) }
                    }
                )
            }
        }
    }

    /// An endpoint failure says so IN PLACE OF the empty state — "nothing
    /// marked yet" over a failed read would be a claim this pane cannot make,
    /// so the error branch above renders and this line explains the other two.
    private var emptyMessage: String {
        if !vm.loaded { return "loading…" }
        if vm.items.isEmpty {
            return "Nothing in the mental model yet — select text in an artifact and tap ◆ mind, or + note here."
        }
        return "nothing in that filter"
    }

    private var chipsSection: some View {
        kSection {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    ForEach(MMFilter.allCases) { f in
                        Button {
                            filter = f
                        } label: {
                            Text("\(f.label) \(counts[f] ?? 0)")
                                .font(.data(10, weight: .semibold))
                                .foregroundStyle(filter == f ? KTheme.accentInk : KTheme.text2)
                                .padding(.horizontal, 10)
                                .padding(.vertical, 5)
                                .background(
                                    filter == f ? KTheme.accent : KTheme.raised,
                                    in: Capsule()
                                )
                        }
                        .buttonStyle(.plain)
                        .accessibilityAddTraits(filter == f ? [.isSelected] : [])
                    }
                }
                .padding(.vertical, 2)
            }
            Button {
                noteOpen.toggle()
            } label: {
                Label(noteOpen ? "Cancel note" : "Note", systemImage: noteOpen ? "xmark" : "plus")
                    .font(.data(11, weight: .semibold))
            }
            .buttonStyle(.glass)
            .tint(KTheme.accent)
            .accessibilityLabel("write a note into the mental model")
        }
    }

    private var noteSection: some View {
        kSection("New note") {
            TextField("something you know that no artifact says…", text: $note, axis: .vertical)
                .kField(minHeight: 70)
                .lineLimit(3...6)
            TextField("context (optional)", text: $noteContext)
                .kField()
            Button("Add") {
                Task { await addNote() }
            }
            .font(.data(11, weight: .semibold))
            .buttonStyle(.glassProminent)
            .disabled(vm.writing || note.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
    }

    private func addNote() async {
        let text = note.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        await vm.addNote(
            baseURL: baseURL, text: text,
            context: noteContext.trimmingCharacters(in: .whitespacesAndNewlines)
        )
        // Cleared unconditionally, like the artifact composer's send: a refusal
        // has already put the server's own sentence on screen, and re-typing a
        // note is cheaper than wondering whether the box in front of you was
        // sent.
        note = ""
        noteContext = ""
        noteOpen = false
    }
}

/// One mark: a badge, a headline, its provenance — and, expanded, the text
/// that was frozen.
///
/// Every row expands, not just the invalidated ones. The headline of a code
/// reading is coordinates, and being able to read the text back is the whole
/// point of freezing it; what the contract REQUIRES is the invalidated case
/// (frozen content plus its reason), and that is this same expansion with one
/// more line in it.
struct MMRowView: View {
    let item: WebMMItem
    let baseURL: String
    let busy: Bool
    let onRemove: () -> Void

    @State private var open = false
    /// The item's source as `GET /api/mm/source` answered it, once asked for.
    @State private var source: MMSource?
    @State private var sourceError: String?
    @State private var loadingSource = false

    /// A note's or a decision's headline IS its content, so repeating it in
    /// the expansion would be an empty gesture; the context is what is left.
    ///
    /// A marked SELECTION is exempt by SOURCE rather than by comparison, and
    /// that is the point: its headline is a truncated first line, so on a
    /// one-line capture the two strings are equal and a string comparison
    /// would drop the body — silently, and only for short selections, which
    /// is the worst shape a bug like this can take. The frozen block is what
    /// the row exists to show, so it always renders.
    private var bodyText: String {
        if item.source == "artifact-element" { return item.content }
        let head = mmRowTitle(item)
        return head == item.content ? "" : item.content
    }

    /// A marked SELECTION expands as MARKDOWN, everything else as frozen text
    /// (carl, gate R1: "marking a list or table doesn't preserve the
    /// structure"), and it renders through `MarkdownText` — the app's native
    /// SwiftUI pipeline. Never a WKWebView per block: that is what got this
    /// app jetsam-killed (MarkdownText.swift:6-11), and a memory pane is a
    /// list of arbitrarily many of these.
    ///
    /// Only `artifact-element`, and the reason is what the two contents ARE. A
    /// selection is a span of an artifact's markdown SOURCE — the server
    /// locates it in the file and stores the source lines, pipes and list
    /// markers intact — so rendering it is showing what carl marked. A code
    /// reading's content is source in some other language, where `#` is a
    /// comment and `*` is a dereference; running that through a markdown
    /// renderer would eat characters that matter. A LEGACY element mark, made
    /// before the server located spans, holds the rendered text and comes out
    /// as one paragraph — the honest rendering of what was frozen, not a
    /// degradation to repair.
    /// Keyed on the SOURCE and the raw content, never on `bodyText`: the
    /// branch must not depend on a headline rule to be reachable. It was
    /// `bodyText`-gated once, and a headline that happened to equal the
    /// content emptied the body and took the whole markdown rendering with
    /// it — silently, and only for some rows.
    private var asMarkdown: Bool {
        item.source == "artifact-element" && !item.content.isEmpty
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .top, spacing: 8) {
                Button {
                    open.toggle()
                } label: {
                    rowHead
                }
                .buttonStyle(.plain)
                .accessibilityHint(open ? "collapse" : "show what was frozen")
                Spacer(minLength: 0)
                if busy {
                    ProgressView().controlSize(.small)
                } else {
                    // A sibling of the expand button, never nested inside it:
                    // a button within a button swallows its own tap (the
                    // library row's ✕ makes the same split).
                    //
                    // DISABLED rather than absent on an invalidated row: the
                    // row is already gone in the only sense the store has, and
                    // a control that vanishes reads as "this row is different"
                    // where a dimmed one reads as "nothing left to do here".
                    Button(role: .destructive) {
                        onRemove()
                    } label: {
                        Image(systemName: "xmark")
                            .font(.system(size: 11, weight: .semibold))
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(KTheme.text2)
                    .disabled(!item.live)
                    .accessibilityLabel(
                        item.live
                            ? "drop the mark on \(mmRowTitle(item))"
                            : "\(mmRowTitle(item)) is already invalidated"
                    )
                }
            }
            if open { frozen }
        }
        .padding(.vertical, 4)
        .opacity(item.live ? 1 : 0.65)
    }

    private var rowHead: some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Text(mmKindLabel(item.kind))
                    .font(.data(9, weight: .semibold))
                    .foregroundStyle(badgeColor)
                    .padding(.horizontal, 7)
                    .padding(.vertical, 2)
                    .background(badgeColor.opacity(0.15), in: Capsule())
                Text(mmRowTitle(item))
                    .font(.data(12, weight: .semibold))
                    .foregroundStyle(KTheme.text)
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)
            }
            if let scope = mmScopeLabel(item) {
                Text(scope)
                    .font(.data(9, weight: .semibold))
                    .foregroundStyle(KTheme.text2)
                    .padding(.horizontal, 7)
                    .padding(.vertical, 2)
                    .background(KTheme.raised, in: Capsule())
                    .accessibilityLabel("scope \(scope)")
            }
            if !item.live {
                Text(deadLine)
                    .font(.data(10))
                    .foregroundStyle(KTheme.counter)
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)
            }
            Text(metaLine)
                .font(.data(10))
                .foregroundStyle(KTheme.text2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(Rectangle())
    }

    private var badgeColor: Color {
        if !item.live { return KTheme.counter }
        switch item.kind {
        case "decision": return KTheme.solved
        case "note": return KTheme.revealed
        default: return KTheme.accent
        }
    }

    private var deadLine: String {
        var s = "invalidated"
        if let reason = item.invalidReason, !reason.isEmpty { s += " · " + reason }
        if let detail = item.invalidDetail, !detail.isEmpty { s += " · " + detail }
        return s
    }

    /// `source · slug · ref · age` with the pieces that would repeat the
    /// headline left out — an artifact row's headline already carries both its
    /// slug and its ref, and a code row's carries the ref.
    private var metaLine: String {
        var parts: [String] = [mmSourceLabel(item.source)]
        if !item.slug.isEmpty && item.source != "artifact" { parts.append(item.slug) }
        if !item.sourceRef.isEmpty && item.source != "code" && item.source != "artifact" {
            parts.append(item.sourceRef)
        }
        if item.createdAt > 0 {
            let now = Int64(Date().timeIntervalSince1970 * 1000)
            parts.append(compactAgeLabel(secondsElapsed: Int((now - item.createdAt) / 1000)) + " ago")
        }
        return parts.joined(separator: " · ")
    }

    @ViewBuilder
    private var frozen: some View {
        VStack(alignment: .leading, spacing: 6) {
            // The FROZEN text — for an invalidated row this is the whole
            // payoff: what carl knew, kept exactly as it was on the day he
            // read it, which the source it came from no longer says. The
            // source itself is one tap further (`source` below), fetched on
            // demand rather than carried on the list payload.
            if asMarkdown {
                MarkdownText(markdown: bodyText)
            } else if !bodyText.isEmpty {
                Text(bodyText)
                    .font(.data(11))
                    .foregroundStyle(KTheme.text)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if let context = item.context, !context.isEmpty {
                Text(context)
                    .font(.voice(11))
                    .foregroundStyle(KTheme.text2)
                    .fixedSize(horizontal: false, vertical: true)
            }
            // The highlighted text a capture started from — what carl was
            // looking at, as distinct from the claim the mind thread wrote.
            if let quote = item.quote, !quote.isEmpty {
                Text(quote)
                    .font(.voice(11))
                    .italic()
                    .foregroundStyle(KTheme.text2)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.leading, 8)
                    .overlay(alignment: .leading) {
                        Rectangle().fill(KTheme.text2.opacity(0.4)).frame(width: 2)
                    }
            }
            if let summary = captureSummary {
                Text(summary)
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
            }
            sourceView
            Text(stamps)
                .font(.data(10))
                .foregroundStyle(KTheme.text2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
        .background(KTheme.ground, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }

    /// One line naming where a capture's quote came from, or nil for a row
    /// with no capture source.
    private var captureSummary: String? {
        guard let src = item.captureSource else { return nil }
        switch src.kind {
        case "artifact":
            return "from " + [src.slug, src.name].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: "/")
        case "transcript":
            let session = src.session ?? ""
            return session.isEmpty ? "from transcript" : "from transcript \(session.prefix(8))"
        case "dir":
            return "from " + [src.repo, src.dir].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: "/")
        case "item":
            return src.itemId.map { "from mm #\($0)" } ?? "from an item"
        default:
            return "from " + src.kind
        }
    }

    /// The `source` action and, once fetched, what it answered. Asked for on
    /// demand: a source can be a whole artifact or a transcript window, far
    /// too much to carry on every row of the list payload.
    @ViewBuilder
    private var sourceView: some View {
        if let source {
            if source.available {
                if source.changed {
                    Text("the source has changed since this was captured")
                        .font(.data(10))
                        .foregroundStyle(KTheme.revealed)
                }
                ScrollView {
                    Text(source.text)
                        .font(.data(10))
                        .foregroundStyle(KTheme.text)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 240)
            } else {
                Text("source not available")
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
            }
        } else {
            Button {
                Task { await loadSource() }
            } label: {
                Label(loadingSource ? "loading source…" : "source", systemImage: "doc.text.magnifyingglass")
                    .font(.data(10, weight: .semibold))
            }
            .buttonStyle(.plain)
            .foregroundStyle(KTheme.accent)
            .disabled(loadingSource)
            if let sourceError {
                Text(sourceError)
                    .font(.data(10))
                    .foregroundStyle(KTheme.counter)
            }
        }
    }

    private func loadSource() async {
        guard !loadingSource else { return }
        loadingSource = true
        defer { loadingSource = false }
        do {
            source = try await APIClient.shared.fetchMMSource(baseURL: baseURL, id: item.id)
            sourceError = nil
        } catch {
            sourceError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    private var stamps: String {
        var s = item.createdAt > 0 ? "marked " + memoryShortDate(ms: item.createdAt) : ""
        if let at = item.invalidatedAt, at > 0 {
            s += (s.isEmpty ? "" : " · ") + "invalidated " + memoryShortDate(ms: at)
        }
        return s
    }
}

/// One raised contradiction flag: what it is about, the note the agent and
/// zeus left, and carl's two verdicts — `drop item` (resolve AND invalidate
/// the flagged item) or `keep` (resolve only).
struct MMFlagRowView: View {
    let flag: WebMMFlag
    /// The flagged item when it is in the loaded list, for its title.
    let item: WebMMItem?
    let busy: Bool
    let onResolve: (String) -> Void

    private var subject: String {
        if let item { return "#\(item.id) " + mmRowTitle(item) }
        if flag.itemId != 0 { return "item #\(flag.itemId)" }
        return "no item named"
    }

    private var meta: String {
        var parts: [String] = []
        if flag.againstId != 0 { parts.append("against #\(flag.againstId)") }
        if !flag.slug.isEmpty { parts.append(flag.slug) }
        let at = flag.raisedAt > 0 ? flag.raisedAt : flag.createdAt
        if at > 0 {
            let now = Int64(Date().timeIntervalSince1970 * 1000)
            parts.append(compactAgeLabel(secondsElapsed: Int((now - at) / 1000)) + " ago")
        }
        return parts.joined(separator: " · ")
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(spacing: 6) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .font(.system(size: 10))
                    .foregroundStyle(KTheme.counter)
                Text(subject)
                    .font(.data(12, weight: .semibold))
                    .foregroundStyle(KTheme.text)
                    .lineLimit(2)
            }
            Text(flag.note)
                .font(.voice(12))
                .foregroundStyle(KTheme.text)
                .fixedSize(horizontal: false, vertical: true)
            if !flag.raiseNote.isEmpty {
                Text(flag.raiseNote)
                    .font(.voice(11))
                    .foregroundStyle(KTheme.text2)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if !meta.isEmpty {
                Text(meta)
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
            }
            HStack(spacing: 8) {
                if busy {
                    ProgressView().controlSize(.small)
                } else {
                    // `drop item` only when there is an item to drop: a flag
                    // naming none would resolve with nothing invalidated,
                    // which is what `keep` already says.
                    if flag.itemId != 0 {
                        Button("drop item", role: .destructive) { onResolve("drop") }
                            .font(.data(11, weight: .semibold))
                            .buttonStyle(.glass)
                            .tint(KTheme.counter)
                            .accessibilityLabel("drop item \(flag.itemId) and resolve the flag")
                    }
                    Button("keep") { onResolve("keep") }
                        .font(.data(11, weight: .semibold))
                        .buttonStyle(.glass)
                        .tint(KTheme.accent)
                        .accessibilityLabel("keep the item and resolve the flag")
                }
            }
        }
        .padding(.vertical, 4)
    }
}
