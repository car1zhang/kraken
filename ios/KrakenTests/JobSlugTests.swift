import XCTest
@testable import Kraken

// JobSlugTests — the Swift end of the cross-source pin on `util.Slugify`
// (internal/util/util.go), which now has three implementations: the Go one
// that DECIDES, `web/_ui/src/lib/slug.ts`, and `JobSlug.slugify` here.
//
// The repo's rule for a value mirrored across sources is a pinning test rather
// than a comment — TestJobPaletteMatchesCSS/…MatchesSwift for the sixteen
// palette hexes, LoadScaleContractTests for the twenty load anchors — and two
// hand ports of a rune loop are exactly that shape. The stakes are lower than
// the palette's (a drifted slugifier costs a wrong PREVIEW, never a wrong
// rename: the server slugifies the text it is sent and its answer is what the
// form re-points to) and that is an argument for a cheap table, not for none.
//
// `sharedCases` mirrors web/_ui/src/lib/slug.test.ts's table VALUE-identically,
// row for row — not byte-identically, which two rows cannot be: this file
// spells the non-ASCII literals as `\u{…}` escapes and TypeScript does not
// always have to. Nothing mechanically enforces the mirroring today — there is
// no Go-side parser pinning these two files against each other, the way
// TestJobPaletteMatchesCSS pins the palette — so the agreement is a convention
// this comment states rather than a guarantee. The ORACLE, though, is no
// longer unpinned: `TestSlugify` and `TestSlugifyIsIdempotent` in
// internal/util/util_test.go hold these same rows, in this order, against the
// Go implementation, which is what makes "verified by running the Go
// implementation itself" a re-checkable claim rather than a historical one.
//
// The two places the three implementations genuinely DISAGREE get their own
// block at the bottom, pinned to what Swift actually produces — an unpinned
// known divergence is how a second one arrives beside it unnoticed, which is
// exactly what the port's own header comment left open.

final class JobSlugTests: XCTestCase {

    /// input → `util.Slugify(input)`, as the Go implementation actually
    /// answers it. Only cases all three implementations agree on may live
    /// here; the disagreements are recorded below.
    ///
    /// EVERY non-ASCII character is written as a `\u{…}` escape, not just the
    /// ones with more than one Unicode spelling: macOS normalizes filenames
    /// and editors normalize text, and "which normalization is this é" is the
    /// whole subject of the divergence block below — a rule that applied only
    /// to the characters someone had already noticed were ambiguous would be
    /// no rule at all. It is also why these rows are value-identical to the
    /// TypeScript table rather than byte-identical.
    private static let sharedCases: [(String, String)] = [
        // the empty and all-punctuation inputs — the shapes that slugify to
        // nothing, which is what the server's gate 1 refuses and what disarms
        // the Rename button
        ("", ""),
        (" ", ""),
        ("!!!", ""),
        ("-", ""),
        ("\u{65E5}\u{672C}\u{8A9E}", ""),          // 日本語
        // the canonical demo: what makes a typed "My Job!" unsurprising
        ("My Job!", "my-job"),
        // separators at each end and in runs
        ("  leading", "leading"),
        ("trailing  ", "trailing"),
        ("--dashes--", "dashes"),
        ("a   b", "a-b"),
        ("a - b", "a-b"),
        // ordinary names
        ("Job 123", "job-123"),
        ("UPPER", "upper"),
        ("a.b.c", "a-b-c"),
        ("foo_bar", "foo-bar"),
        // `linearSlug`'s own literal (internal/jobs/linear_contract_test.go)
        ("A Title", "a-title"),
        // already-slugified inputs are FIXED POINTS — the property the cell
        // dir, the uploads dir and the branch name all lean on
        ("already-slug", "already-slug"),
        ("kraken-renaming-cells", "kraken-renaming-cells"),
        ("2026-08-12", "2026-08-12"),
        // a precomposed non-ASCII letter is a separator, not a letter: NFC
        // "café" holds é as ONE scalar, which fails the ASCII test in all
        // three ports
        ("caf\u{E9}", "caf"),
    ]

    func testSharedCrossSourceTable() {
        for (input, want) in Self.sharedCases {
            XCTAssertEqual(
                JobSlug.slugify(input), want,
                "slugify(\(input.debugDescription)) — this table is mirrored in web/_ui/src/lib/slug.test.ts and verified against internal/util's Slugify"
            )
        }
    }

    /// The property every caller of a slug leans on without saying so: a cell
    /// dir is `<slugify(slug)>-<id>`, the uploads dir re-slugifies, and the
    /// rename control seeds its input with the CURRENT slug — so a slug that
    /// changed under a second pass would move a job's directories on what the
    /// user typed as a no-op.
    func testIsIdempotent() {
        for (input, _) in Self.sharedCases {
            let once = JobSlug.slugify(input)
            XCTAssertEqual(JobSlug.slugify(once), once, "not a fixed point: \(once.debugDescription)")
        }
    }

    // ---- known divergences from Go ---------------------------------------
    //
    // Both are about CLUSTERING and CASE MAPPING, the two places "port the
    // rune loop" cannot be followed literally in Swift, and both are safe for
    // the same structural reason rather than by luck: this port only renders a
    // preview and gates its own Rename button, so the worst it can do is
    // REFUSE a name the server would have accepted. It can never store one,
    // because `renameTapped` sends the raw typed text and re-points to
    // `res.slug`. Disarm-only, in both cases.

    /// Swift's `String` iterates GRAPHEME CLUSTERS where Go's
    /// range-over-string iterates runes. Decomposed é — "e" plus U+0301
    /// COMBINING ACUTE — is one Character here and two runes there, so Go
    /// keeps the "e" (and turns the mark into a trailing dash the trim takes)
    /// while this port sees a single non-ASCII Character and drops the whole
    /// thing.
    ///
    /// Go:    util.Slugify("e\u{301}") == "e"
    /// TS:    slugify("e\u{301}")      == "e"   (for…of iterates code points)
    /// Swift: this                     == ""
    ///
    /// Unreachable from an iOS keyboard, which emits NFC. Pinned anyway: it is
    /// the case that says WHY the port is preview-only.
    func testCombiningMarkClustersWhereGoDoesNot() {
        XCTAssertEqual(JobSlug.slugify("e\u{301}"), "")
        // and the precomposed spelling of the same letter, where all three agree
        XCTAssertEqual(JobSlug.slugify("caf\u{E9}"), "caf")
    }

    /// Go's `strings.ToLower` applies SIMPLE case mapping — one rune in, one
    /// rune out — so U+0130 (LATIN CAPITAL LETTER I WITH DOT ABOVE) becomes
    /// plain "i" and "İstanbul" slugifies to "istanbul". Swift's
    /// `lowercased()` applies FULL case mapping, expanding it to "i" +
    /// U+0307 COMBINING DOT ABOVE, which then clusters into one non-ASCII
    /// Character and disappears entirely.
    ///
    /// Go:    "istanbul"
    /// TS:    "i-stanbul"   (expands like Swift, but iterates code points, so
    ///                       the mark survives as a separator)
    /// Swift: "stanbul"
    ///
    /// Three ports, three answers, one of which is the truth — and the truth
    /// is the server's. This preview is wrong and the rename is still right.
    func testDottedCapitalIExpandsWhereGoMapsIt() {
        XCTAssertEqual(JobSlug.slugify("\u{130}stanbul"), "stanbul")
    }
}
