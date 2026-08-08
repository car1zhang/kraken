import Foundation

// CmdHighlighter.swift — the four pure pieces of the composer: which spans of
// typed text are pipeline commands, when a draft is written upstream, what a
// question option posts, and whose text wins when a server draft lands late.
//
// All four are frozen in 50-contract.md § S4 and none of them touches AppKit,
// SwiftUI or the network — which is the point. The composer's VIEW is out of
// blind-test scope (an `NSViewRepresentable` wrapping an `NSTextView`); its
// rules are not.

// MARK: - command highlighting

/// Where the pipeline commands sit in a block of composer text.
///
/// SIX tokens, not the four the chip row offers: `/dream` and `/graph` are
/// typed rather than chipped (a dream or graph job is created by the daemon,
/// not by carl reaching for a chip), but they still read as commands and
/// `SkillPalette` carries a colour for each. The web's `CMD_HL_RE`
/// (useCmdHighlight.ts:56) is the four-token ancestor of this; the contract
/// widened it.
///
/// Boundary rules, and each is load-bearing:
///
///   - LEADING: start-of-text or whitespace. Without it a mid-word slash run
///     like `web/static/do` or `[image: /Users/x/pr-notes.png]` lights up.
///   - TRAILING: end-of-text, or a character that is not a word character.
///     `/door` therefore matches nothing while `ledger accepted — /do` does.
///   - CASE-SENSITIVE. `/DO` is not a command; the skills are lowercase and a
///     shouted one would be a different string to Claude Code too.
///
/// The returned range covers exactly the token and never the boundary
/// whitespace, and the array is sorted by `lowerBound` ascending — which falls
/// out of the single left-to-right scan below rather than from a sort call, so
/// there is no ordering to get wrong.
enum CmdHighlighter {
    /// The six, longest-first so a scan can take the first match without
    /// worrying about prefixes. (None of the six is currently a prefix of
    /// another; ordering by length keeps that from becoming a silent trap if
    /// one ever is.)
    static let tokens: [String] = ["/dream", "/graph", "/info", "/aim", "/pr", "/do"]
        .sorted { $0.count > $1.count }

    /// The four the chip row inserts, in the pipeline's own order — a strict
    /// subset of `tokens`, and the reason the two lists are not one: a chip is
    /// an affordance and a highlight is a fact about text.
    static let chips: [String] = ["/info", "/aim", "/do", "/pr"]

    static func ranges(in text: String) -> [(range: Range<String.Index>, skill: String)] {
        var out: [(range: Range<String.Index>, skill: String)] = []
        guard !text.isEmpty else { return out }

        var i = text.startIndex
        while i < text.endIndex {
            guard text[i] == "/", leadingBoundary(text, at: i) else {
                i = text.index(after: i)
                continue
            }
            var matched = false
            for token in tokens {
                guard let end = matchEnd(text, from: i, token: token) else { continue }
                out.append((range: i..<end, skill: String(token.dropFirst())))
                i = end
                matched = true
                break
            }
            if !matched { i = text.index(after: i) }
        }
        return out
    }

    private static func leadingBoundary(_ text: String, at i: String.Index) -> Bool {
        if i == text.startIndex { return true }
        return text[text.index(before: i)].isWhitespace
    }

    /// The index just past `token` when it matches at `from` with a clean
    /// trailing boundary, else nil.
    private static func matchEnd(_ text: String, from: String.Index, token: String) -> String.Index? {
        var t = token.startIndex
        var i = from
        while t < token.endIndex {
            guard i < text.endIndex, text[i] == token[t] else { return nil }
            i = text.index(after: i)
            t = token.index(after: t)
        }
        if i == text.endIndex { return i }
        return isWordCharacter(text[i]) ? nil : i
    }

    /// What "not a boundary" means on the trailing side. Letters, digits and
    /// `_` continue a word; whitespace, punctuation and symbols end one — which
    /// is what makes `— /do` a command and `/door` prose.
    private static func isWordCharacter(_ c: Character) -> Bool {
        c.isLetter || c.isNumber || c == "_"
    }
}

// MARK: - the upstream draft

/// When a composer's text is written back to `POST /api/draft`, and — more
/// importantly — when it is NOT.
///
/// The web board's asymmetry here is one of the ten behaviours a rewrite
/// silently drops (r2-web-functional §b7), so it is stated as five verbs
/// rather than left to call sites:
///
///   - `typed` debounces a save, superseding any earlier pending one.
///   - `willSend` and `didSend` both CANCEL. Send cancels twice — before the
///     POST and again after — because a keystroke landing during the flight
///     schedules a fresh save that the pre-send cancel never saw, and that
///     save would land after the server's own draft clear and resurrect the
///     message that was just sent.
///   - `jobDone` cancels: the server clears the draft on a done.
///   - `jobKilled` FLUSHES, synchronously. Kill does not clear the draft
///     server-side, so the half-typed message is landed rather than lost — and
///     synchronously because the caller closes the modal on the next line,
///     which on the web is what tears the debounce down.
///
/// An EMPTY `typed("")` still schedules (contract amendment 17): an empty save
/// is how a draft gets CLEARED, and suppressing it would resurrect a draft the
/// user had deliberately emptied.
final class ComposerDraft {
    private let debounce: TimeInterval
    private let save: (String) -> Void

    private var pending: DispatchWorkItem?
    /// The text a scheduled save would write. nil means nothing is pending —
    /// and `""` is emphatically NOT nil, for the reason above.
    private var pendingText: String?

    init(debounce: TimeInterval, save: @escaping (String) -> Void) {
        self.debounce = debounce
        self.save = save
    }

    func typed(_ text: String) {
        cancelPending()
        pendingText = text
        let item = DispatchWorkItem { [weak self] in
            guard let self, let text = self.pendingText else { return }
            self.pending = nil
            self.pendingText = nil
            self.save(text)
        }
        pending = item
        DispatchQueue.main.asyncAfter(deadline: .now() + debounce, execute: item)
    }

    func willSend() { cancelPending() }

    func didSend() { cancelPending() }

    func jobDone() { cancelPending() }

    func jobKilled() {
        let text = pendingText
        cancelPending()
        // Only a PENDING save flushes. After any cancel there is nothing owed,
        // and inventing a save from the last text seen would re-post a message
        // the send path just cleared.
        if let text { save(text) }
    }

    private func cancelPending() {
        pending?.cancel()
        pending = nil
        pendingText = nil
    }
}

// MARK: - question answers

/// What `POST /api/question` carries for an option click.
///
/// The handler decodes the same `{slug, text}` shape as every other board
/// action, and the answer rides as `text`: a single 1-based digit STRING, not
/// a separate field and not the zero-based index the button loop counts with.
enum QuestionAnswer {
    static func payload(optionIndex: Int) -> String {
        String(optionIndex + 1)
    }
}

/// Whether a late-arriving server draft may land in the composer.
///
/// `GET /api/draft` is fired after the composer is already showing empty, so
/// its answer can arrive after the user has started typing. Anything already
/// typed wins — a draft written on another device is worth less than the
/// sentence being written right now.
enum DraftSeed {
    static func accept(serverDraft: String, typedSoFar: String) -> String {
        typedSoFar.isEmpty ? serverDraft : typedSoFar
    }
}
