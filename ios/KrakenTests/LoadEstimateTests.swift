import XCTest
@testable import Kraken

// The board card's ROI badge, end to end on the client side: the shared
// `compactAgeLabel` ladder, the `webJobLoad` decode (see ios/API.md's
// `webJobLoad` table and internal/api/jobload.go), and the four derivations
// the badge is drawn from — `roi`, `band`, `badgeText`, `summary`.
//
// The badge text and the band boundaries are pinned IDENTICALLY in the web
// board's own suite (web/_ui/src/lib/format.test.ts). That duplication is the
// point: the two clients must render one number the same way, and a test in
// only one of them cannot catch the day they stop.

final class CompactAgeLabelTests: XCTestCase {

    // Every boundary of the ladder, from both sides. Extracted from
    // JobDetailView's token note, which was its only caller until the ROI
    // badge needed the same thing, so these also pin that the extraction did
    // not move a threshold.
    func testLadderBoundaries() {
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 0), "0s")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 1), "1s")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 59), "59s")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 60), "1m")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 119), "1m")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 3599), "59m")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 3600), "1h")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 86_399), "23h")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 86_400), "1d")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: 90 * 86_400), "90d")
    }

    // A negative span is floored rather than rendered. This phone's clock and
    // the Mac that stamped the estimate are independent, so a stamp from the
    // future is reachable; "-3s ago" would read as a bug in the estimate
    // rather than in the clocks.
    func testFutureStampFloorsAtZero() {
        XCTAssertEqual(compactAgeLabel(secondsElapsed: -1), "0s")
        XCTAssertEqual(compactAgeLabel(secondsElapsed: -99_999), "0s")
    }
}

final class LoadEstimateTests: XCTestCase {

    private func decode(_ json: String) throws -> WebJobLoad {
        try JSONDecoder().decode(WebJobLoad.self, from: Data(json.utf8))
    }

    // No key inside the object is omitempty server-side, so a present object
    // always carries all four — including an empty note, which is a real
    // answer rather than an absence.
    func testDecodesTheWireObject() throws {
        let l = try decode("""
        {"unblock": 2, "scope": 8, "note": "one decision: which store keys the row", "at": 1769040000000}
        """)
        XCTAssertEqual(l.unblock, 2)
        XCTAssertEqual(l.scope, 8)
        XCTAssertEqual(l.note, "one decision: which store keys the row")
        XCTAssertEqual(l.at, 1_769_040_000_000)
    }

    func testDecodesAnEmptyNote() throws {
        XCTAssertEqual(try decode(#"{"unblock":5,"scope":5,"note":"","at":1}"#).note, "")
    }

    // Absence is expressed one level up — by the job's `load` key being gone,
    // never by a zeroed object — so a job with no estimate decodes with a nil
    // `load` rather than failing.
    func testAJobWithoutTheKeyDecodesWithNilLoad() throws {
        let json = """
        {"slug":"foo","repo":"kraken","state":"blocked","live":true,"mtime":1}
        """
        let job = try JSONDecoder().decode(WebJob.self, from: Data(json.utf8))
        XCTAssertNil(job.load)
    }

    // The four worked examples from the shared contract, each landing on the
    // ratio and band it names.
    func testContractWorkedExamples() {
        let cases: [(unblock: Int, scope: Int, roi: Double, band: LoadBand, text: String)] = [
            (2, 8, 4.0, .hot, "⚡ 4.0× 2→8"),
            (5, 5, 1.0, .even, "⚡ 1.0× 5→5"),
            (8, 2, 0.25, .cold, "⚡ 0.3× 8→2"),
            (3, 5, 5.0 / 3.0, .warm, "⚡ 1.7× 3→5"),
        ]
        for c in cases {
            let l = WebJobLoad(unblock: c.unblock, scope: c.scope, note: "", at: 1)
            XCTAssertEqual(l.roi ?? .nan, c.roi, accuracy: 1e-12, "roi for \(c.unblock)→\(c.scope)")
            XCTAssertEqual(l.band, c.band, "band for \(c.unblock)→\(c.scope)")
            XCTAssertEqual(l.badgeText, c.text)
        }
    }

    // Closed-open from the top, boundary value in the HIGHER band — the same
    // rule web's `loadROI` applies. Each boundary is exactly representable in
    // binary (3, 1.5, 0.75 all are), so there is no float slop to allow for
    // and the two clients cannot land on different sides of one.
    func testBandBoundaries() {
        XCTAssertEqual(LoadBand.forROI(3.0), .hot)
        XCTAssertEqual(LoadBand.forROI(2.999), .warm)
        XCTAssertEqual(LoadBand.forROI(1.5), .warm)
        XCTAssertEqual(LoadBand.forROI(1.499), .even)
        XCTAssertEqual(LoadBand.forROI(0.75), .even)
        XCTAssertEqual(LoadBand.forROI(0.749), .cold)
        XCTAssertEqual(LoadBand.forROI(0.1), .cold)
    }

    // The badge prints one decimal, rounded half AWAY FROM ZERO — not the
    // half-to-even `String(format: "%.1f",)` would give, which prints 0.25 as
    // "0.2" while web's `toFixed(1)` prints "0.3". Two exact-half ratios that
    // are reachable from 1..10 inputs, both of which that bug got wrong.
    func testOneDecimalRoundsHalfAwayFromZero() {
        XCTAssertEqual(WebJobLoad(unblock: 8, scope: 2, note: "", at: 1).badgeText, "⚡ 0.3× 8→2")
        XCTAssertEqual(WebJobLoad(unblock: 4, scope: 5, note: "", at: 1).badgeText, "⚡ 1.3× 4→5")
    }

    // Neither shape can reach a client from the current server — a clear drops
    // the whole `load` key and a half estimate is a 400 — so this pins the
    // defence against a hand-edited `jobs` row: no ratio, no badge, and never
    // a division by zero.
    func testNoUsableEstimateYieldsNothingToDraw() {
        for (unblock, scope) in [(0, 0), (3, 0), (0, 3)] {
            let l = WebJobLoad(unblock: unblock, scope: scope, note: "n", at: 1)
            XCTAssertNil(l.roi, "roi for \(unblock)→\(scope)")
            XCTAssertNil(l.band)
            XCTAssertNil(l.badgeText)
            XCTAssertNil(l.summary(nowMillis: 2))
        }
    }

    // The accessibility sentence, in the contract's exact shape: the two
    // numbers, the ratio, the age clause, then the note.
    func testSummaryCarriesTheAgeClauseAndTheNote() {
        let now: Int64 = 1_769_040_000_000
        let l = WebJobLoad(unblock: 2, scope: 8, note: "which store keys the row",
                           at: now - 3 * 3600 * 1000)
        XCTAssertEqual(
            l.summary(nowMillis: now),
            "unblock effort 2/10 · next step 8/10 · roi 4.0× · estimated 3h ago — which store keys the row"
        )
    }

    // An empty note drops the separator with it: a trailing " — " with nothing
    // after it reads as a truncation.
    func testSummaryOmitsTheSeparatorForAnEmptyNote() {
        let now: Int64 = 1_769_040_000_000
        let l = WebJobLoad(unblock: 3, scope: 5, note: "", at: now - 2 * 86_400 * 1000)
        XCTAssertEqual(
            l.summary(nowMillis: now),
            "unblock effort 3/10 · next step 5/10 · roi 1.7× · estimated 2d ago"
        )
    }

    // The age is rendered twice from one computation — a muted label beside the
    // badge and this clause — so they cannot disagree. Both disappear together
    // when there is no usable stamp; the rest of the sentence stays.
    func testMissingStampDropsTheAgeEverywhere() {
        let now: Int64 = 1_769_040_000_000
        XCTAssertNil(WebJobLoad(unblock: 2, scope: 8, note: "", at: 0).age(nowMillis: now))
        XCTAssertEqual(
            WebJobLoad(unblock: 2, scope: 8, note: "n", at: 0).summary(nowMillis: now),
            "unblock effort 2/10 · next step 8/10 · roi 4.0× — n"
        )
        XCTAssertNil(WebJobLoad(unblock: 2, scope: 8, note: "", at: now).age(nowMillis: 0))
    }

    func testFutureStampReadsAsZeroRatherThanNegative() {
        let now: Int64 = 1_769_040_000_000
        XCTAssertEqual(WebJobLoad(unblock: 2, scope: 8, note: "", at: now + 99_000).age(nowMillis: now), "0s")
    }
}
