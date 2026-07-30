import Foundation

// The board search's match rule, and nothing else — no SwiftUI, no state, no
// I/O, so it is testable on its own (KrakenTests/JobSearchTests.swift) and the
// view layer above it only has to decide what to draw.
//
// This rule is written TWICE, here and in the web board's
// web/_ui/src/lib/jobSearch.ts, because the two clients share no code. Nothing
// structural keeps the halves in step: the only mitigation is a shared table of
// cases, reproduced case for case in both suites (52-testing-plan.md), and it
// is an ongoing cost rather than a solved problem. Any change here belongs in
// that file, that table, and both suites in the same breath.
//
// Two haystacks, two rules, because they are two different kinds of text:
//
//   NAMES  — slug, displaySlug (only when it DIFFERS from slug — see the
//            haystack comment in matchJob), repo. Short kebab-case
//            identifiers, matched FUZZILY (the term's characters in order,
//            gaps allowed), which is what makes a half-remembered name
//            findable and survives a typo.
//   TEXT   — detail (zeus' last message), last_sent. Freeform prose, matched by
//            plain SUBSTRING and only by terms of three code units or more: a
//            fuzzy subsequence over a few thousand characters of prose matches
//            essentially any query, so applied here it would return the whole
//            board on every keystroke.
//
// A job matches when EVERY term matches by EITHER rule.
//
// The two halves agree on the length floor and do NOT fully agree below it.
// The floor is counted in UTF-16 code units here (`term.utf16.count`) because
// that is what the web's `String.length` counts, so both admit exactly the same
// terms to the prose haystack. The fuzzy scan is not aligned that way: it walks
// Swift `Character`s — grapheme clusters — while the web walks code units, so a
// term and a name that differ only in Unicode normalisation can match on one
// client and not the other (a decomposed "café x" is found by `cafe` on the
// web, not here). That is left unfixed deliberately: chasing grapheme-vs-code-
// unit parity through a fuzzy matcher costs more than it buys for a board of
// ASCII kebab-case slugs. It is worth knowing that the shared case table cannot
// catch it either — every query in it is ASCII.

/// Why a job survived the query — the whole reason `matchJob` answers with a
/// named type rather than a `Bool`. A row that matched only through its status
/// text says so on screen; without that, a result whose name has nothing to do
/// with what was typed reads as the search being broken.
enum MatchReason: Equatable {
    case name
    case text
}

/// One job's verdict. A struct rather than a bare `MatchReason?` so the web
/// half's `JobMatch` type has a counterpart here, and so a second fact about
/// the match (a score, a matched range) has somewhere to go without changing
/// every call site.
struct JobMatch: Equatable {
    let reason: MatchReason
}

/// Split a raw query into lowercased terms. Whitespace-only is an empty term
/// list — i.e. an empty query, which matches everything. Split apart from the
/// match rule because it runs once per query rather than once per job, and
/// because it is worth its own tests.
func parseTerms(_ query: String) -> [String] {
    query.lowercased()
        .split(whereSeparator: { $0.isWhitespace })
        .map(String.init)
}

/// Does `term` appear in `haystack` as a SUBSEQUENCE — every character in
/// order, gaps allowed? Both are expected lowercased already; this does no
/// case folding of its own, so the caller owns that once instead of per term.
///
/// Ordering is the whole rule and the half most likely to be quietly wrong:
/// `jb` finds `search-jobs`, `bj` must not. (`js` finds it too, and correctly —
/// the slug ends in "jobs" — which is why the test that pins ordering uses the
/// `jb`/`bj` pair rather than the `sj`/`js` one it looks like it should.) An
/// empty term matches, matching the empty query's "keep everything".
func fuzzyMatch(_ haystack: String, _ term: String) -> Bool {
    var next = term.startIndex
    guard next != term.endIndex else { return true }
    for ch in haystack {
        if ch == term[next] {
            next = term.index(after: next)
            if next == term.endIndex { return true }
        }
    }
    return false
}

/// The minimum term length, in UTF-16 code units, that may reach the prose
/// haystack. Two units of prose is a syllable, not a search: `th` would keep
/// nearly every job that has ever had a status line, which is the same "returns
/// the whole board" failure the fuzzy rule is kept off prose for.
private let minTextTermLength = 3

/// Decide whether one job survives `terms`, and say why. `nil` is no match.
/// `terms` must already be lowercased and non-empty per element — i.e. the
/// output of `parseTerms`.
func matchJob(_ job: WebJob, terms: [String]) -> JobMatch? {
    // An empty query keeps everything, never nothing. It is also the state a
    // fresh launch sits in, so this is the common path, not an edge case.
    guard !terms.isEmpty else { return JobMatch(reason: .name) }

    // `slug` + `displaySlug` + `repo`, in that order, deliberately NOT
    // `title` + `slug` + `repo`. `WebJob.title` is `displaySlug ?? slug`
    // (Models.swift), so for the non-archived jobs that are almost all of them
    // title IS slug, and joining both would put the same name in the haystack
    // twice. A doubled name lets one term be satisfied by characters taken
    // from two different copies, which is not what "these characters appear in
    // this name, in order" means: over "search-jobs search-jobs kraken" the
    // term `bh` matches — `b` from the first copy, `h` from the second — while
    // over "search-jobs kraken" it does not.
    //
    // `displaySlug` is dropped for the SAME reason whenever it equals `slug`,
    // and that case is the rule rather than the exception: archivedWebJob
    // (internal/api/board.go) sets DisplaySlug to the job's own slug, and the
    // two differ only when archiving hit a name conflict and numbered the file.
    // Measured against the live board, 126 of 132 archived jobs carry
    // displaySlug == slug — so without this guard the doubling is the NORMAL
    // shape for archived jobs, which is exactly where a search is most likely
    // to be a half-remembered name. Observed there: `manifest` does not match
    // "interview-platform-agent-reset kraken" but does match it doubled.
    //
    // Skipping the nil/empty parts also makes this the same list, in the same
    // order, as the web half's `nameHaystack`, and an identical haystack is the
    // only way two fuzzy implementations can agree on every term.
    let distinctDisplay = job.displaySlug == job.slug ? nil : job.displaySlug
    let names = [job.slug, distinctDisplay, job.repo]
        .compactMap { $0 }
        .filter { !$0.isEmpty }
        .joined(separator: " ")
        .lowercased()
    let text = [job.detail, job.lastSent]
        .compactMap { $0 }
        .filter { !$0.isEmpty }
        .joined(separator: " ")
        .lowercased()

    // `.name` only if EVERY term matched a name; one term that had to fall
    // through to the prose makes the whole match a text match, since that is
    // the case the row has to explain to whoever is reading it.
    var allNames = true
    for term in terms {
        if fuzzyMatch(names, term) { continue }
        // `utf16.count`, not `count`: the floor has to admit exactly the terms
        // the web half admits, and its `String.length` counts UTF-16 code
        // units. Two emoji are 4 there and 2 here; a flag is 4 there and 1
        // here. Counting Characters would refuse prose matches the web board
        // makes. `.literal` for the same reason on the other side of the
        // comma — plain code-unit containment, as String.includes does, rather
        // than a locale- or canonical-equivalence-aware search.
        if term.utf16.count >= minTextTermLength, text.range(of: term, options: .literal) != nil {
            allNames = false
            continue
        }
        return nil
    }
    return JobMatch(reason: allNames ? .name : .text)
}
