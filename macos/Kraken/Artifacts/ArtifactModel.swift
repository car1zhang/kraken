import Foundation

// ArtifactModel.swift — the view-free half of the artifact viewer (contract
// 50-contract.md, S5 table + amendments 7-9).
//
// Everything here is pure: an extension classifier, a sequence walker, the
// per-artifact draft stash, and the two strings a `⑂ ask` branch is named and
// seeded with. The SwiftUI panel in ArtifactViewer.swift holds no rules of its
// own beyond what these four types decide, which is what lets the blind suites
// hold the whole behaviour without touching a view body.

// MARK: - what an artifact renders as

/// How an artifact's bytes reach the screen, decided from the URL's extension
/// ALONE — never from a Content-Type, never from sniffing the body. That is the
/// web board's rule (`isPage`/`isImage`, ArtifactOverlay.tsx:31-32) and it is
/// the one input available before the fetch that the `text` case needs.
///
/// The three cases are the three renderers: a native image view, one WKWebView
/// loading the URL, and one WKWebView loading a locally-built markdown shell.
enum ArtifactKind: Equatable {
    /// `png jpg jpeg gif webp` — drawn natively, no web view involved.
    case image
    /// `html svg pdf` — WebKit loads the server URL directly. This is the
    /// deliberate non-native surface (53-concessions.md §E): artifacts are
    /// agent-authored HTML pages, SMIL diagram cards included.
    case web
    /// everything else (md, txt, log, no extension at all) — fetched as text
    /// and rendered through the markdown shell.
    case text

    /// Case-INsensitive, and tolerant of a leading dot so a caller may pass
    /// either `"png"` or `".png"`. An unknown or empty extension is `text`,
    /// which is the safe end of the fork: a text render of an unrecognised
    /// file shows its bytes, while a web render of one shows whatever WebKit
    /// guesses.
    static func kind(forExtension ext: String) -> ArtifactKind {
        var e = ext.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        while e.hasPrefix(".") { e.removeFirst() }
        switch e {
        case "png", "jpg", "jpeg", "gif", "webp": return .image
        case "html", "svg", "pdf": return .web
        default: return .text
        }
    }

    /// The same decision from a whole `/api/artifact/...` URL. Split by hand
    /// rather than through `URL.pathExtension` because these strings are
    /// server-relative paths (no scheme, no host) and a cell-home artifact
    /// name may itself carry slashes — `URL(string:)` on one of those is a
    /// second failure mode for no gain.
    static func kind(forURL url: String) -> ArtifactKind {
        let last = url.split(separator: "/").last.map(String.init) ?? url
        guard let dot = last.lastIndex(of: "."), dot != last.startIndex else { return .text }
        return kind(forExtension: String(last[last.index(after: dot)...]))
    }
}

// MARK: - the pager's arithmetic

/// Where the open artifact sits in its job's set, and where a `‹`/`›` step
/// lands. Two static functions rather than a stored cursor, deliberately: the
/// sequence is re-derived from the live board payload on every render (a cell
/// writing a new artifact under an open viewer grows N without a reopen), so a
/// carried index would silently slide onto the wrong artifact the moment the
/// list gained an entry AHEAD of the open one.
struct ArtifactSequence {
    /// Position by URL equality. The FIRST match on a duplicated URL
    /// (amendment 7) — duplicates are not reachable from a real board payload,
    /// and first-match is the answer that keeps `index` a pure lookup.
    static func index(of url: String, in urls: [String]) -> Int? {
        urls.firstIndex(of: url)
    }

    /// A step of `delta`, CLAMPED at both ends — never a wrap. `count: 0`
    /// returns 0 (amendment 7): an empty sequence has no valid position, and 0
    /// is the one index every caller already treats as "the singleton".
    static func step(from i: Int, by delta: Int, count: Int) -> Int {
        guard count > 0 else { return 0 }
        return min(max(i + delta, 0), count - 1)
    }
}

/// One step of the viewer's sequence: where to fetch it, what to title it —
/// the Swift twin of `SeqEntry` in `web/_ui/src/hooks/useArtifactSeq.ts`.
struct ArtifactSeqEntry: Equatable {
    let url: String
    let name: String
}

extension ArtifactSequence {
    /// The ordered `{url, name}` sequence for one job, built from the job's
    /// artifact list AS THE BOARD CURRENTLY HAS IT (`artifactSeqFor`).
    ///
    /// Titles are labelled the way the chips label theirs — `display_slug` when
    /// the job carries one (a renamed archive), else `slug` — so the panel
    /// title reads identically whether the reader arrived by clicking a chip or
    /// by paging onto it.
    ///
    /// nil for a job that resolves to nothing: no job, or one with no
    /// artifacts. That nil is what the viewer's singleton fallback feeds on.
    static func entries(for job: WebJob?) -> [ArtifactSeqEntry]? {
        guard let job, let list = job.artifacts, !list.isEmpty else { return nil }
        let label = (job.displaySlug?.isEmpty == false) ? job.displaySlug! : job.slug
        return list.map { ArtifactSeqEntry(url: $0.url, name: "\(label)/\($0.name)") }
    }
}

// MARK: - the per-artifact composer stash

/// An unsent composer, banked against the artifact it was typed about.
///
/// `‹ ›` sit a few pixels from the box, and every step resets the composer, so
/// without this one click on `›` destroys a half-typed note with no undo.
/// Refusing to step instead would make the pager look broken; carrying the
/// draft with its artifact is what the rest of the board does.
///
/// One draft per URL rather than one per (URL, mode): the head drives ONE box,
/// and switching modes carries the text across rather than hiding it — so the
/// mode is stored ALONGSIDE the text and restored with it.
///
/// Not persisted anywhere. Surviving the process is a bigger promise than a
/// scratch composer should make; surviving a close is not, which is why the
/// shared instance below exists.
final class ArtifactDraftStash {
    /// The viewer's own stash. A single shared instance mirrors the web's
    /// module-lifetime `Map`: the panel's state is rebuilt on every open, and
    /// an ask you abandoned on `50-contract.md` should still be there when you
    /// come back to it. Tests construct their own instance instead.
    static let shared = ArtifactDraftStash()

    private struct Entry {
        let mode: String
        let text: String
    }

    private var entries: [String: Entry] = [:]

    init() {}

    /// Banks `text` (with the mode it was typed in) against `url`.
    ///
    /// EMPTY text clears instead of storing — an emptied composer has nothing
    /// to restore, and a stored empty draft would re-open the box on an
    /// artifact the reader deliberately cleared. Whitespace-only text is real
    /// typed text and IS stored (amendment 8): only `""` exactly is the erase.
    func bank(url: String, mode: String, text: String) {
        guard !text.isEmpty else {
            entries.removeValue(forKey: url)
            return
        }
        entries[url] = Entry(mode: mode, text: text)
    }

    /// The banked (mode, text) for `url`, or nil when nothing is banked.
    func restore(url: String) -> (mode: String, text: String)? {
        guard let e = entries[url] else { return nil }
        return (mode: e.mode, text: e.text)
    }

    /// Drops the bank for `url` — what a successful send and an explicit
    /// `cancel` both do. A sent note is no longer a draft, and "throw this
    /// away" has to mean it, or the note reappears on the next visit.
    func clear(url: String) {
        entries.removeValue(forKey: url)
    }
}

/// The two things the head's one composer can be. They share a box because
/// they are the same gesture — "I have something to say about THIS artifact,
/// while looking at it" — and differ only in where it goes: a `feedback` row
/// the job's zeus reads on its next working session, or an `ask` that branches
/// a side thread seeded with the question.
enum ArtifactComposerMode: String, Equatable {
    case feedback
    case ask
}

// MARK: - the /api/artifact URL triple

/// The `{slug, home, name}` triple `POST /api/feedback`'s create form needs,
/// recovered from an artifact's own URL.
///
/// The URL is the viewer's source of truth for what is open, so a note
/// addresses the exact artifact on screen by re-deriving from it rather than by
/// threading a separate triple through every chip that can open one.
struct ArtifactURLParts: Equatable {
    let slug: String
    /// `cell` | `pluto` | `archive` — the artifact-route home set.
    let home: String
    /// The artifact's SERVER-side name, which for a nested artifact still
    /// carries its slashes (`icon-variations/v2.png`).
    let name: String

    /// Splits `/api/artifact/<slug>/<home>/<name>`.
    ///
    /// Only the first two segments split off: a cell-home artifact name may
    /// itself contain slashes, and everything after the home belongs to the
    /// name. Returns nil for anything that is not an artifact URL rather than
    /// guessing — the viewer answers that with "can't identify this artifact".
    static func parse(_ url: String) -> ArtifactURLParts? {
        let prefix = "/api/artifact/"
        guard url.hasPrefix(prefix) else { return nil }
        let rest = url.dropFirst(prefix.count)
        guard let firstSlash = rest.firstIndex(of: "/") else { return nil }
        let slug = String(rest[..<firstSlash])
        let afterSlug = rest[rest.index(after: firstSlash)...]
        guard let secondSlash = afterSlug.firstIndex(of: "/") else { return nil }
        let home = String(afterSlug[..<secondSlash])
        let name = String(afterSlug[afterSlug.index(after: secondSlash)...])
        guard !slug.isEmpty, !home.isEmpty, !name.isEmpty else { return nil }
        return ArtifactURLParts(slug: slug, home: home, name: name)
    }
}

// MARK: - naming and seeding an ask

/// The thread name and seed prompt a question about an artifact branches with.
enum AskThread {
    /// How much of the 80 a long artifact name may take before it is itself
    /// cut. Budgeted rather than simply written first: a single cut at the end
    /// would let a long section-doc name eat the whole cap and truncate the
    /// question away entirely, leaving every chip on the job identical — the
    /// exact failure this split exists to prevent.
    static let baseBudget = 40
    /// Matches ThreadOverlay's rename input `maxLength`. A title longer than
    /// this could be branched but never edited back to in place.
    static let cap = 80

    /// The name the branched thread carries wherever threads are listed.
    ///
    /// "Which artifact" alone is not enough — ask two things about
    /// `00-overview.md` and you get two chips nobody can tell apart — so the
    /// question's first line rides along after an em-dash separator.
    ///
    /// Both budgets count Swift `Character`s (grapheme clusters), which is what
    /// makes "never splits a grapheme/emoji" true by construction rather than
    /// by a surrogate check (amendment 9). The cut is HARD: no ellipsis, no
    /// marker. An empty or whitespace-only question yields the bare budgeted
    /// basename with NO separator, since a trailing " — " on its own names
    /// nothing.
    static func title(artifactName: String, question: String) -> String {
        var base = basename(artifactName)
        if base.count > baseBudget { base = String(base.prefix(baseBudget)) }
        let first = firstLine(of: question)
        let title = first.isEmpty ? base : base + " — " + first
        return title.count > cap ? String(title.prefix(cap)) : title
    }

    /// The seed prompt the branched thread comes up with.
    ///
    /// It names the artifact rather than its URL or its on-disk path: the
    /// thread is a fork of zeus in the cell's own worktree, so the artifact set
    /// is already the thing it knows by name.
    ///
    /// The last line restates the thread contract — this composer sits on a
    /// doc, and without it "elaborate on this section" reads as a licence to go
    /// and edit it.
    ///
    /// The question is carried VERBATIM — the whole of it, byte for byte, not
    /// the title's budgeted first line and not a trimmed copy (amendment 22:
    /// "contains the question" is unqualified). Only the TITLE is budgeted,
    /// because a title is a chip label; the seed is the prompt a claude wakes
    /// up on, and a question whose second paragraph carried the actual ask
    /// would arrive as a thread that answers something else. The composer
    /// already trims before it calls this, so the in-app text is unchanged —
    /// what changes is that the obligation now holds for ANY input.
    static func seed(artifactName: String, question: String) -> String {
        "I'm reading the artifact `" + artifactName
            + "` from this job's artifact set. A question about it:\n\n"
            + question
            + "\n\nAnswer here in the thread — this is a question about the work, "
            + "not a request to change it. Don't edit the artifact."
    }

    /// Last path component. An empty tail (a name ending in the separator)
    /// falls back to the whole name rather than to nothing, which would title
    /// the thread with a bare em-dash.
    static func basename(_ name: String) -> String {
        guard let slash = name.lastIndex(of: "/") else { return name }
        let tail = String(name[name.index(after: slash)...])
        return tail.isEmpty ? name : tail
    }

    /// The question's first line, trimmed. The whole question is trimmed first
    /// so a leading blank line does not make the "first line" empty.
    ///
    /// A line ends at ANY Unicode line terminator, not at `"\n"` alone
    /// (amendment 22): `Character.isNewline` is the set — `\n`, `\r`, `\r\n`,
    /// U+0085, U+2028, U+2029. Splitting on `"\n"` was wrong in two directions
    /// at once. A `\r`-only or U+2028-separated question is ONE line to that
    /// split, so the whole multi-paragraph question became the chip label; and
    /// a CRLF question's first line kept a trailing `\r`, an invisible
    /// character inside a stored thread title that nothing but a rename could
    /// remove. Swift treats `\r\n` as a single `Character` and `isNewline`
    /// answers true for it, so both fall out of the same test.
    static func firstLine(of question: String) -> String {
        let trimmed = question.trimmingCharacters(in: .whitespacesAndNewlines)
        let line = trimmed.prefix { !$0.isNewline }
        return line.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
