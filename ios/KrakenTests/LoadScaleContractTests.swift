import XCTest
@testable import Kraken

// LoadScaleContractTests — the Swift end of the standardized load scale,
// written blind from artifacts/50-contract.md.
//
// The twenty anchor labels are spelled out in THREE places: the repo-owned
// skill file `.claude/skills/load/SKILL.md` (the standard zeus reads when it
// sets an estimate), `web/_ui/src/lib/format.ts`, and this app's `LoadAnchors`.
// `internal/jobs/loadscale_contract_test.go` parses all three off disk and pins
// them against each other, the way `TestJobPaletteMatchesCSS` pins the palette.
// This suite is the half that guard cannot reach: it asks whether the RUNTIME
// tables and the lookup built on them are right, not whether the source text
// agrees with two other files.
//
// The failure this protects against is silent. A drifted label does not crash
// the app or fail a build — the detail view prints a number with the wrong word
// beside it, and the whole promise of the anchors (carl reads meaning without
// memorizing the ladder) is quietly false.

final class LoadScaleContractTests: XCTestCase {

    /// The canonical unblock ladder, rung 1 first, copied byte for byte from
    /// 50-contract.md § Canonical anchor labels. Hardcoded rather than derived
    /// from `LoadAnchors`: this list IS the requirement, and a test that read
    /// the table it checks would only prove the table equals itself.
    private static let contractUnblock = [
        "Mechanical",
        "Read one page, pick",
        "Answer a few concrete questions",
        "Local smoke, or a credential",
        "Live E2E drive",
        "Genuine review",
        "Decision batch needing new context",
        "Deep decision, real consequences",
        "Many deep decisions in one sitting",
        "A day's thought",
    ]

    /// The canonical scope ladder, same source and same rule.
    private static let contractScope = [
        "One-message close-out",
        "One small round",
        "One focused artifact or round",
        "A single-slice implementation",
        "A moderate slice",
        "A full /aim set draft",
        "A delegated build",
        "Full phase-2 of a substantial spec",
        "A multi-PR program",
        "The largest coordinated effort",
    ]

    // Both axes run 1-10. A table short of ten rungs is not a smaller ladder:
    // every rung past its end has no word at all, and those are exactly the
    // states (a spec gate at 6, a day's thought at 10) the standard exists to
    // make comparable across cells.
    func testBothLaddersHaveTenRungs() {
        XCTAssertEqual(LoadAnchors.unblock.count, 10)
        XCTAssertEqual(LoadAnchors.scope.count, 10)
    }

    func testUnblockTableIsTheCanonicalLadder() {
        XCTAssertEqual(LoadAnchors.unblock, Self.contractUnblock)
    }

    func testScopeTableIsTheCanonicalLadder() {
        XCTAssertEqual(LoadAnchors.scope, Self.contractScope)
    }

    // The lookup is 1-BASED, matching the wire and the ladder: rung n is at
    // index n-1. Checked at both ends and in the middle, since an off-by-one
    // here is not a crash — it prints the neighbouring rung's word, which reads
    // as a plausible estimate of something else entirely.
    func testLabelLooksUpTheRungsWord() {
        XCTAssertEqual(LoadAnchors.label(unblock: 1), "Mechanical")
        XCTAssertEqual(LoadAnchors.label(unblock: 5), "Live E2E drive")
        XCTAssertEqual(LoadAnchors.label(unblock: 10), "A day's thought")

        XCTAssertEqual(LoadAnchors.label(scope: 1), "One-message close-out")
        XCTAssertEqual(LoadAnchors.label(scope: 5), "A moderate slice")
        XCTAssertEqual(LoadAnchors.label(scope: 10), "The largest coordinated effort")
    }

    // Every rung of both ladders resolves through the same lookup, so the table
    // and the accessor cannot agree at three sampled points and disagree in
    // between.
    func testEveryRungResolvesThroughTheLookup() {
        for n in 1...10 {
            XCTAssertEqual(LoadAnchors.label(unblock: n), Self.contractUnblock[n - 1], "unblock rung \(n)")
            XCTAssertEqual(LoadAnchors.label(scope: n), Self.contractScope[n - 1], "scope rung \(n)")
        }
    }

    // Outside 1...10 there is no rung and so no word. Nil rather than a crash
    // or an empty string: neither shape can arrive from the current server (a
    // clear drops the whole key, a half estimate is a 400), which is precisely
    // why the defence has to be tested rather than assumed.
    func testOutOfRangeRungsHaveNoLabel() {
        for n in [0, 11, -1, Int.min, Int.max] {
            XCTAssertNil(LoadAnchors.label(unblock: n), "unblock \(n)")
            XCTAssertNil(LoadAnchors.label(scope: n), "scope \(n)")
        }
    }
}
