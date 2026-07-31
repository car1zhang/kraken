import SwiftUI

// Carl's per-artifact feedback: the read rows on JobDetailView, the compose
// sheet hung off ArtifactView, the board card's count chip, and the pure
// pieces underneath all three.
//
// Nothing on the server changes for any of this. `feedback` already rides
// every /api/board poll, is already served for archived jobs, is already
// decoded as WebFeedback (Models.swift), and createFeedback/editFeedback/
// deleteFeedback already sit on APIClient. This file is the wiring.

// MARK: - artifact url → {slug, home, name}

/// The triple POST /api/feedback's create form needs, recovered from an
/// artifact's own URL. Its own type rather than a tuple so the parse can be
/// asserted on directly (see FeedbackTests).
struct ArtifactRef: Equatable {
    let slug: String
    /// "cell" | "pluto" | "archive" — the artifact-route home set.
    let home: String
    /// The artifact's SERVER-side name, which for a nested artifact still
    /// carries its slashes ("icon-variations/v2.png").
    let name: String
}

/// Swift port of the web board's `artifactParts` (app.js): recovers
/// {slug, home, name} from an `/api/artifact/<slug>/<home>/<name>` URL.
///
/// The URL is the source of truth on purpose, and this is the one trap in
/// the whole surface: `artifactTree` (JobDetailView) rewrites
/// `WebArtifact.name` to a RELATIVE leaf while preserving `url`, so a nested
/// artifact opened from a folder row carries "v2.png" in `name` and
/// "icon-variations/v2.png" in its URL — and only the latter is what the
/// server will resolve. Hence: derive the name here, never from
/// WebArtifact.name.
///
/// Only the first two segments are split off; a cell-home name may itself
/// contain slashes, so everything after the home is the name. Returns nil for
/// anything that isn't an artifact URL (an upload url, a malformed path, a
/// url with no name segment) rather than guessing.
func artifactParts(from url: String) -> ArtifactRef? {
    let prefix = "/api/artifact/"
    guard url.hasPrefix(prefix) else { return nil }
    let rest = url.dropFirst(prefix.count)
    // Two splits, not `split(separator:)`: the name keeps every slash it has.
    guard let firstSlash = rest.firstIndex(of: "/") else { return nil }
    let slug = String(rest[..<firstSlash])
    let afterSlug = rest[rest.index(after: firstSlash)...]
    guard let secondSlash = afterSlug.firstIndex(of: "/") else { return nil }
    let home = String(afterSlug[..<secondSlash])
    let name = String(afterSlug[afterSlug.index(after: secondSlash)...])
    guard !slug.isEmpty, !home.isEmpty, !name.isEmpty else { return nil }
    return ArtifactRef(slug: slug, home: home, name: name)
}

// MARK: - the note cap

/// The server refuses a note over 8000 **runes** after trim with a 400, and
/// the note rides every /api/board poll for as long as the card is visible,
/// which is why the cap exists at all. No composer in the app caps anything
/// today, so without enforcing it here a long note is a silent failure.
let feedbackNoteRuneCap = 8000

/// Go's `utf8.RuneCountInString` in Swift: Unicode SCALARS, not Characters.
/// A Character is a grapheme cluster (an emoji with a skin-tone modifier is
/// one Character but two runes), so counting Characters would under-report
/// exactly where the server is about to refuse.
func feedbackRuneCount(_ text: String) -> Int {
    text.trimmingCharacters(in: .whitespacesAndNewlines).unicodeScalars.count
}

// MARK: - optimistic overlay

/// Local optimistic overlay over a job's feedback rows.
///
/// `job` is a frozen snapshot for a JobDetailView sheet's whole lifetime (see
/// that view's docQuestionOverlay), so without this a create/edit/delete
/// wouldn't visibly land until the sheet is reopened — the extra board poll
/// `onUpdate()` fires refreshes the BOARD, not this sheet's captured job.
///
/// Deletes need `hidden` rather than a value in `edits`: an edit replaces a
/// row's text, but a delete has to remove the row entirely, and a map keyed
/// by id has no way to say "gone" that isn't also a legal note.
// Not Equatable: WebFeedback is Decodable-only (no synthesized ==), and
// nothing diffs an overlay — SwiftUI re-renders off the @State write itself.
struct FeedbackOverlay {
    /// row id → replacement note text.
    var edits: [Int: String] = [:]
    /// row ids removed locally.
    var hidden: Set<Int> = []
    /// Rows created from this sheet. They carry a NEGATIVE synthetic id (see
    /// nextOptimisticID): POST /api/feedback answers `{"ok": true}` with no
    /// row echoed back, so there is no real id to address one with until the
    /// next poll — which is also why an optimistic row renders without edit
    /// and delete controls instead of offering an action that would 404.
    var created: [WebFeedback] = []

    private var optimisticCounter = 0

    /// A fresh negative id. Negative because every real `feedback` row id is
    /// a positive kraken.db rowid, so the two can never collide and
    /// `isOptimistic` is a total test rather than a heuristic.
    mutating func nextOptimisticID() -> Int {
        optimisticCounter -= 1
        return optimisticCounter
    }

    /// `server` with edits applied, hidden rows dropped, and locally-created
    /// rows appended. Created rows whose id somehow already appears in
    /// `server` are dropped rather than duplicated — belt and braces; a
    /// negative id can't be served.
    func apply(to server: [WebFeedback]) -> [WebFeedback] {
        var out: [WebFeedback] = []
        var known = Set<Int>()
        for f in server {
            known.insert(f.id)
            if hidden.contains(f.id) { continue }
            guard let note = edits[f.id] else {
                out.append(f)
                continue
            }
            out.append(WebFeedback(
                id: f.id, artifact: f.artifact, note: note, at: f.at,
                addressed: f.addressed, addressedAt: f.addressedAt
            ))
        }
        out += created.filter { !known.contains($0.id) && !hidden.contains($0.id) }
        return out
    }
}

extension WebFeedback {
    /// True for a row this client invented while waiting for the next poll —
    /// see FeedbackOverlay.created.
    var isOptimistic: Bool { id < 0 }
}

// MARK: - read rows

/// One feedback note: the artifact it hangs off, the note itself, and — when
/// the nightly dream pass has shipped a durable fix for it — that pass' own
/// free-text account of the fix. No other agent writes that column, so a row
/// without one is not a row nobody handled; see WebFeedback in Models.swift.
///
/// An addressed row dims but KEEPS its edit and delete affordances, matching
/// the web board's deliberate call: a note staying accurate is worth more
/// than freezing it the moment something reacts to it.
struct FeedbackRow: View {
    let item: WebFeedback
    var onEdit: (() -> Void)?
    var onDelete: (() -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(item.artifact)
                    .font(.data(11))
                    .foregroundStyle(KTheme.skillDream)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer(minLength: 8)
                if item.isOptimistic {
                    Text("sending…")
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                } else {
                    if let onEdit {
                        Button("edit", action: onEdit)
                            .buttonStyle(.plain)
                            .font(.data(11, weight: .semibold))
                            .foregroundStyle(KTheme.accent)
                    }
                    if let onDelete {
                        Button("delete", action: onDelete)
                            .buttonStyle(.plain)
                            .font(.data(11, weight: .semibold))
                            .foregroundStyle(KTheme.counter)
                    }
                }
            }
            Text(item.note)
                .font(.voice(13))
                .foregroundStyle(KTheme.text)
            if item.isAddressed {
                HStack(alignment: .firstTextBaseline, spacing: 4) {
                    Text("✓")
                    Text(item.addressed ?? "")
                }
                .font(.voice(11))
                .foregroundStyle(KTheme.solved)
            }
        }
        .padding(.vertical, 2)
        .opacity(item.isAddressed ? 0.6 : 1)
    }
}

// MARK: - count chip

/// "✎ <total> · <n> no durable fix" — the board card's passive feedback
/// summary, and the exact counterpart of the web board's FeedbackChip.
/// Passive on purpose: a long-press context menu would contend with the
/// card's own drag-to-reorder gesture, so the chip only ever displays.
/// Renders nothing when the job has no notes.
///
/// The second count is notes with no `addressed` line, and it is NOT a count
/// of work owed — see WebFeedback in Models.swift. `addressed` is written by
/// the nightly dream pass alone and an ordinary cell acts on its feedback
/// while deliberately leaving the column empty, so this is a corroboration
/// backlog and it is non-zero on nearly every job carrying feedback. That is
/// why the chip is muted rather than carrying skillDream's filled treatment:
/// an emphasis that fires on nearly everything is one nobody reads, which is
/// the same call the web board made when it dropped `.fb-chip.open`.
struct FeedbackCountChip: View {
    let feedback: [WebFeedback]

    var body: some View {
        if !feedback.isEmpty {
            let unfixed = feedback.filter { !$0.isAddressed }.count
            Text(unfixed > 0 ? "✎ \(feedback.count) · \(unfixed) no durable fix" : "✎ \(feedback.count)")
                .font(.data(10, weight: .semibold))
                .foregroundStyle(KTheme.text2)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .overlay(Rectangle().stroke(KTheme.text2, lineWidth: 1))
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(feedback.count) feedback notes, \(unfixed) with no durable fix")
        }
    }
}

// MARK: - compose

/// Creating a new note on an artifact, or editing an existing one. One view
/// for both because the two forms differ only in which APIClient method they
/// post to and what they open pre-filled with — the composer, the cap and the
/// counter are the same surface either way.
struct FeedbackComposeView: View {
    enum Target {
        /// Attach a new note to the artifact this ref names.
        case create(ArtifactRef)
        /// Retext an existing row. The slug is carried separately from the id
        /// because the server refuses an edit whose slug doesn't own the row —
        /// an id alone is never enough to mutate another job's feedback.
        case edit(slug: String, id: Int, artifact: String)
    }

    let baseURL: String
    let target: Target
    /// Called with the SAVED note text once the server has taken it, so the
    /// caller can fold it into its optimistic overlay. The sheet dismisses
    /// itself; the caller does not have to.
    var onSaved: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @FocusState private var noteFocused: Bool
    @State private var note: String
    @State private var isBusy = false
    @State private var errorMessage: String?

    init(baseURL: String, target: Target, initialNote: String = "", onSaved: @escaping (String) -> Void) {
        self.baseURL = baseURL
        self.target = target
        self.onSaved = onSaved
        _note = State(initialValue: initialNote)
    }

    private var artifactName: String {
        switch target {
        case .create(let ref): return ref.name
        case .edit(_, _, let artifact): return artifact
        }
    }

    private var slug: String {
        switch target {
        case .create(let ref): return ref.slug
        case .edit(let slug, _, _): return slug
        }
    }

    private var runes: Int { feedbackRuneCount(note) }
    private var remaining: Int { feedbackNoteRuneCap - runes }
    private var isOverCap: Bool { remaining < 0 }
    private var canSend: Bool { !isBusy && runes > 0 && !isOverCap }

    var body: some View {
        Form {
            kSection("on \(artifactName)") {
                TextField("what's wrong with it, or what you want instead", text: $note, axis: .vertical)
                    .focused($noteFocused)
                    .kField(focused: noteFocused, minHeight: 120)
                HStack {
                    Text(slug)
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                    Spacer()
                    // The cap is the server's, and it is the reason this
                    // counter exists rather than a nicety — over it, the POST
                    // is a 400 with nothing typed recoverable from the error.
                    Text(isOverCap ? "\(-remaining) over" : "\(remaining) left")
                        .font(.data(10))
                        .foregroundStyle(isOverCap ? KTheme.counter : KTheme.text2)
                }
            }
            kSection {
                Text("The server freezes the artifact as it looks right now alongside the note, and the agent reads it when it next resumes.")
                    .font(.voice(11))
                    .foregroundStyle(KTheme.text2)
            }
            if let errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
        }
        .kListStyle()
        .navigationTitle("Feedback")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton(label: "Cancel") { dismiss() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button {
                    save()
                } label: {
                    Label("Send", systemImage: "paperplane.fill")
                }
                .labelStyle(.iconOnly)
                .accessibilityLabel("Send")
                .buttonStyle(.borderedProminent)
                .disabled(!canSend)
            }
        }
        .disabled(isBusy)
    }

    private func save() {
        let trimmed = note.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !isOverCap else { return }
        isBusy = true
        errorMessage = nil
        Task {
            do {
                switch target {
                case .create(let ref):
                    try await APIClient.shared.createFeedback(
                        baseURL: baseURL, slug: ref.slug, home: ref.home,
                        name: ref.name, feedback: trimmed
                    )
                case .edit(let slug, let id, _):
                    try await APIClient.shared.editFeedback(
                        baseURL: baseURL, slug: slug, id: id, feedback: trimmed
                    )
                }
                isBusy = false
                onSaved(trimmed)
                dismiss()
            } catch {
                isBusy = false
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
