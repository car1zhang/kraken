import XCTest
@testable import Kraken

// The board search's match rule, pinned case for case against the SHARED CASE
// TABLE in the spec's 52-testing-plan.md. The same table is pinned in the web
// board's own suite (web/_ui/src/lib/jobSearch.test.ts) over the same six-job
// fixture, because the rule is implemented twice — once in TypeScript, once in
// Swift — and nothing structural keeps the two in step. The case numbers below
// are the join key between the two suites: keep them, and keep them meaning the
// same query.
//
// Four of the cases are less obvious than they look — 3, 6, 7 and 12, the ones
// the table names as what a naive second implementation gets wrong. The
// reasoning behind each of their expected values is written out at the case
// itself, because the value alone does not show its own working: which job a
// hit belongs to, and which character of which field satisfied it, is the part
// worth checking by hand when the two suites disagree.

final class JobSearchTests: XCTestCase {

    // MARK: - fixture
    //
    // The six-job board from the spec's 11-search-flow.html, built by DECODING
    // wire JSON rather than by calling WebJob's memberwise initialiser: WebJob
    // has ~30 stored properties, of which a real payload carries five, and a
    // fixture that had to name all of them would be unreadable and would need
    // editing every time an additive key lands. This also exercises the same
    // decode path the app runs on.
    private func makeJob(
        slug: String,
        repo: String,
        state: String,
        displaySlug: String? = nil,
        detail: String? = nil,
        lastSent: String? = nil
    ) throws -> WebJob {
        var obj: [String: Any] = [
            "slug": slug,
            "repo": repo,
            "state": state,
            "live": false,
            "mtime": 0,
        ]
        if let displaySlug {
            obj["display_slug"] = displaySlug
            obj["archived"] = true
        }
        if let detail { obj["detail"] = detail }
        if let lastSent { obj["last_sent"] = lastSent }
        let data = try JSONSerialization.data(withJSONObject: obj)
        return try JSONDecoder().decode(WebJob.self, from: data)
    }

    private func fixture() throws -> [WebJob] {
        [
            try makeJob(slug: "search-jobs", repo: "kraken", state: "working",
                        detail: "doc ready for review: +4 diagrams, 4 sections"),
            try makeJob(slug: "dream-2026-07-29", repo: "kraken", state: "done",
                        detail: "research pass over yesterday's transcripts — 3 findings"),
            try makeJob(slug: "job-load-categorizing", repo: "kraken", state: "done",
                        detail: "merged; board now ranks blocked cards by roi"),
            try makeJob(slug: "yelp-dedupe", repo: "monolith", state: "blocked",
                        detail: "need a call on the dedupe threshold — 0.82 too loose?"),
            try makeJob(slug: "asr-wer-metrics", repo: "monolith", state: "queued"),
            // Archived: its `slug` is the numbered archive file base and
            // `display_slug` carries the original name (see WebJob.title).
            try makeJob(slug: "search-index-2", repo: "monolith", state: "archived",
                        displaySlug: "search-index"),
        ]
    }

    /// One surviving job and why it survived. A named type rather than a pair,
    /// so an assertion failure prints which reason belonged to which job.
    private struct Hit: Equatable, CustomStringConvertible {
        let slug: String
        let reason: MatchReason
        var description: String { "\(slug):\(reason == .name ? "name" : "text")" }
    }

    /// Run one query over the whole fixture, in fixture order.
    private func hits(_ query: String) throws -> [Hit] {
        let terms = parseTerms(query)
        return try fixture().compactMap { job in
            matchJob(job, terms: terms).map { Hit(slug: job.slug, reason: $0.reason) }
        }
    }

    private let allSix = [
        Hit(slug: "search-jobs", reason: .name),
        Hit(slug: "dream-2026-07-29", reason: .name),
        Hit(slug: "job-load-categorizing", reason: .name),
        Hit(slug: "yelp-dedupe", reason: .name),
        Hit(slug: "asr-wer-metrics", reason: .name),
        Hit(slug: "search-index-2", reason: .name),
    ]

    // MARK: - the shared case table

    // 1 — an empty query matches everything, never nothing. This is the state
    // every launch starts in, so it is the common path.
    func testCase01EmptyQueryKeepsAll() throws {
        XCTAssertEqual(try hits(""), allSix)
    }

    // 2 — whitespace-only is an empty query.
    func testCase02WhitespaceOnlyIsEmpty() throws {
        XCTAssertEqual(try hits("   "), allSix)
        XCTAssertEqual(parseTerms("   "), [])
        XCTAssertEqual(parseTerms("\t\n "), [])
    }

    // 3 — three hits for three different reasons: a slug hit, a hit inside the
    // word `research` in a detail, and a display_slug hit.
    //
    // The archived search-index-2 belongs here and is easy to forget: its
    // display_slug is literally "search-index", and display_slug is in the name
    // haystack — which is the whole of case 10. A query for `search` that
    // returned only two of these would mean the archived job had dropped out of
    // the name haystack, and case 10 would be the only thing left to catch it.
    func testCase03SlugSubstringAndTextMatch() throws {
        XCTAssertEqual(try hits("search"), [
            Hit(slug: "search-jobs", reason: .name),
            Hit(slug: "dream-2026-07-29", reason: .text),
            Hit(slug: "search-index-2", reason: .name),
        ])
    }

    // 4 — fuzzy subsequence, non-adjacent characters.
    func testCase04FuzzyNonAdjacent() throws {
        XCTAssertEqual(try hits("sj"), [Hit(slug: "search-jobs", reason: .name)])
    }

    // 5 — fuzzy with several gaps.
    func testCase05FuzzySeveralGaps() throws {
        XCTAssertEqual(try hits("srchjb"), [Hit(slug: "search-jobs", reason: .name)])
    }

    // 6 — a typo still finds it, and fuzzy matching is genuinely loose.
    //
    // Three hits, and only the first is the one the typo was aimed at. `serch`
    // is a subsequence of "search-index-2 search-index monolith" with one gap,
    // all of it inside the word `search` (the skipped character is the `a`),
    // and also of "asr-wer-metrics monolith": `s` from asr, `e` and
    // `r` from wer, `c` from metrics, `h` from monolith — four different words
    // across two fields. That asr-wer-metrics hit is the real
    // cost of fuzzy matching: a five-character term over short names finds
    // names nobody had in mind. It is pinned rather than hidden, because it is
    // what carl sees.
    func testCase06TypoStillFinds() throws {
        XCTAssertEqual(try hits("serch"), [
            Hit(slug: "search-jobs", reason: .name),
            Hit(slug: "asr-wer-metrics", reason: .name),
            Hit(slug: "search-index-2", reason: .name),
        ])
    }

    // 7 — the subsequence is ORDERED, which takes two queries to say.
    //
    // `js` is NOT `sj` reversed over this fixture, however much it looks like
    // it: "search-jobs" ends in "jobs", so `j` then `s` really does appear in
    // order inside the slug, and matching it is correct. The ordering property
    // needs a pair whose reversal is genuinely absent — `jb` (j…b, present in
    // two slugs) against `bj` (b…j, present in none). `js` stays here at its
    // true value so the case still says out loud that it is not the
    // counter-example it resembles.
    func testCase07SubsequenceIsOrdered() throws {
        XCTAssertEqual(try hits("bj"), [])
        XCTAssertEqual(try hits("jb"), [
            Hit(slug: "search-jobs", reason: .name),
            Hit(slug: "job-load-categorizing", reason: .name),
        ])
        XCTAssertEqual(try hits("js"), [Hit(slug: "search-jobs", reason: .name)])
    }

    // 8 — case-insensitive: identical to case 3, asserted as identical rather
    // than by repeating its three expected hits, so the two can never drift.
    func testCase08CaseInsensitive() throws {
        XCTAssertEqual(try hits("SEARCH"), try hits("search"))
        XCTAssertEqual(try hits("SeArCh"), try hits("search"))
    }

    // 9 — `repo` is in the name haystack.
    func testCase09RepoIsSearchable() throws {
        XCTAssertEqual(try hits("monolith"), [
            Hit(slug: "yelp-dedupe", reason: .name),
            Hit(slug: "asr-wer-metrics", reason: .name),
            Hit(slug: "search-index-2", reason: .name),
        ])
    }

    // 10 — an archived job, found through its display_slug.
    func testCase10ArchivedMatchesDisplaySlug() throws {
        XCTAssertEqual(try hits("index"), [Hit(slug: "search-index-2", reason: .name)])
    }

    // 11 — AND across terms, each term free to match by either rule: `dedupe`
    // hits the name, `threshold` only the detail. One text term makes the whole
    // match a text match, since that is what the row has to explain.
    func testCase11MixedNameAndTextTerms() throws {
        XCTAssertEqual(try hits("dedupe threshold"), [Hit(slug: "yelp-dedupe", reason: .text)])
    }

    // 12 — a 2-character term never reaches detail/last_sent, which also takes
    // two queries to say.
    //
    // `th` still matches three jobs, and that is not a leak: they live in repo
    // "monolith", which contains "th" outright, so they match through the NAME
    // haystack, where no length floor applies. What the floor forbids is the
    // OTHER path, and `th` cannot isolate it — hence `0.`, which is in
    // yelp-dedupe's detail ("0.82 too loose?") and in no job's name, so its
    // matching nothing is the floor and only the floor. Case 13 runs the same
    // prose one character longer and does reach it.
    func testCase12TwoCharTermNeverReachesText() throws {
        XCTAssertEqual(try hits("th"), [
            Hit(slug: "yelp-dedupe", reason: .name),
            Hit(slug: "asr-wer-metrics", reason: .name),
            Hit(slug: "search-index-2", reason: .name),
        ])
        XCTAssertTrue(try hits("th").allSatisfy { $0.reason == .name })
        // The floor itself, with no name path to muddy it.
        XCTAssertEqual(try hits("0."), [])
    }

    // 13 — three characters does reach the prose, and says so.
    func testCase13ThreeCharTermReachesText() throws {
        XCTAssertEqual(try hits("thr"), [Hit(slug: "yelp-dedupe", reason: .text)])
    }

    // 14 — a clean miss.
    func testCase14CleanMiss() throws {
        XCTAssertEqual(try hits("zzz"), [])
    }

    // 15 — one failing term rejects the job, however well the others matched.
    func testCase15OneFailingTermRejects() throws {
        XCTAssertEqual(try hits("search zzz"), [])
    }

    // 16 — a displaySlug EQUAL to the slug must not put the name in the
    // haystack twice. Found by driving the real board, not by reading code:
    // `manifest` returned archived cards that plainly do not contain it,
    // because a fuzzy match is a subsequence test and a doubled name lets one
    // term take some characters from the first copy and the rest from the
    // second.
    //
    // The fixture's own archived job cannot catch this: it has the RARE shape,
    // where archiving hit a name conflict and numbered the file so slug and
    // displaySlug genuinely differ. The common shape is the other one —
    // archivedWebJob (internal/api/board.go) sets DisplaySlug to the job's own
    // slug, and 126 of the 132 archived jobs on the live board carry them
    // equal. Hence a job built here rather than taken from the fixture.
    func testCase16DuplicateDisplaySlugIsNotASecondCopy() throws {
        let job = try makeJob(slug: "interview-platform-agent-reset", repo: "kraken",
                              state: "archived", displaySlug: "interview-platform-agent-reset")
        // the mechanism: the term is absent from the name once, present twice
        XCTAssertFalse(fuzzyMatch("interview-platform-agent-reset kraken", "manifest"))
        XCTAssertTrue(fuzzyMatch(
            "interview-platform-agent-reset interview-platform-agent-reset kraken", "manifest"))
        // so the job must not match — the haystack carries the name once
        XCTAssertNil(matchJob(job, terms: parseTerms("manifest")))
    }

    // 16b — the guard drops displaySlug only when it is a duplicate. Case 10's
    // job keeps both names, which is the whole reason `index` reaches it.
    func testCase16bDistinctDisplaySlugStaysSearchable() throws {
        XCTAssertEqual(try hits("index"), [Hit(slug: "search-index-2", reason: .name)])
    }

    // MARK: - parseTerms

    func testParseTermsLowercasesSplitsAndDropsEmpties() {
        XCTAssertEqual(parseTerms("Search Jobs"), ["search", "jobs"])
        XCTAssertEqual(parseTerms("  leading and trailing  "), ["leading", "and", "trailing"])
        XCTAssertEqual(parseTerms("collapsed   runs"), ["collapsed", "runs"])
        XCTAssertEqual(parseTerms("tab\tand\nnewline"), ["tab", "and", "newline"])
        XCTAssertEqual(parseTerms(""), [])
    }

    // MARK: - fuzzyMatch in isolation
    //
    // The half most likely to be quietly wrong, so it is pinned apart from the
    // job-shaped cases above.

    func testFuzzyMatchOrdering() {
        XCTAssertTrue(fuzzyMatch("search-jobs", "sj"))
        XCTAssertTrue(fuzzyMatch("search-jobs", "srchjb"))
        XCTAssertTrue(fuzzyMatch("search-jobs", "search-jobs"))
        // `bs` would be true here — "jobs" ends in `s` — so the negative case
        // has to be a term whose second character really is absent after its
        // first.
        XCTAssertFalse(fuzzyMatch("search-jobs", "bh"))
        XCTAssertFalse(fuzzyMatch("abc", "cba"))
    }

    func testFuzzyMatchEdges() {
        // An empty term matches, mirroring the empty query's "keep everything".
        XCTAssertTrue(fuzzyMatch("search-jobs", ""))
        XCTAssertTrue(fuzzyMatch("", ""))
        // A term longer than the haystack can never be a subsequence of it.
        XCTAssertFalse(fuzzyMatch("", "a"))
        XCTAssertFalse(fuzzyMatch("sj", "search-jobs"))
        // Repeated characters must each be consumed separately: one `l` in the
        // haystack cannot satisfy two in the term.
        XCTAssertTrue(fuzzyMatch("hello", "llo"))
        XCTAssertFalse(fuzzyMatch("helo", "llo"))
    }

    // MARK: - absent fields
    //
    // display_slug, detail and last_sent are all omitempty on the wire, and a
    // draft job that has never run carries none of them. Every one of them
    // absent must be a plain no-match, not a crash and not a false positive.

    func testAbsentOptionalFields() throws {
        let bare = try makeJob(slug: "asr-wer-metrics", repo: "monolith", state: "queued")
        XCTAssertNil(matchJob(bare, terms: parseTerms("threshold")))
        XCTAssertEqual(matchJob(bare, terms: parseTerms("asr"))?.reason, .name)
        XCTAssertEqual(matchJob(bare, terms: parseTerms(""))?.reason, .name)
    }

    // The prose floor is counted in UTF-16 code units, because the web half's
    // `String.length` counts those and the two must admit the same terms. Every
    // term here is 3 or more code units and fewer than 3 Characters, so each
    // one is a term the web board reaches the prose with — and a `count`-based
    // floor would refuse. The last case holds the floor from below: two code
    // units is still too short.
    func testTextFloorCountsUTF16CodeUnits() throws {
        let job = try makeJob(slug: "yelp-dedupe", repo: "monolith", state: "done",
                              detail: "shipped 🙂🙂x today, flag 🇺🇸, e\u{0301}\u{0302}x")
        XCTAssertEqual("🙂🙂".utf16.count, 4)
        XCTAssertEqual("🙂🙂".count, 2)
        XCTAssertEqual(matchJob(job, terms: parseTerms("🙂🙂"))?.reason, .text)
        XCTAssertEqual(matchJob(job, terms: parseTerms("🙂x"))?.reason, .text)
        XCTAssertEqual(matchJob(job, terms: parseTerms("🇺🇸"))?.reason, .text)
        XCTAssertEqual(matchJob(job, terms: parseTerms("e\u{0301}\u{0302}x"))?.reason, .text)
        // Two code units, and present in the detail: still below the floor.
        XCTAssertNil(matchJob(job, terms: parseTerms("y,")))
    }

    // last_sent is the second half of the prose haystack and is easy to forget:
    // a job whose only evidence is what carl last sent it must still be
    // findable, and must report `.text`.
    func testLastSentIsSearchable() throws {
        let job = try makeJob(slug: "yelp-dedupe", repo: "monolith", state: "blocked",
                              lastSent: "go with 0.9 for now")
        XCTAssertEqual(matchJob(job, terms: parseTerms("0.9"))?.reason, .text)
        XCTAssertEqual(matchJob(job, terms: parseTerms("now"))?.reason, .text)
        XCTAssertNil(matchJob(job, terms: parseTerms("nw")))
    }
}
