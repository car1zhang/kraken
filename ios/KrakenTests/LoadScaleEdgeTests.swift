import XCTest
@testable import Kraken

// The anchor tables' refusal surface.
//
// `LoadAnchors.label` is a lookup into a fixed ten-element table, and the whole
// of its interesting behaviour is what it does with an index that is not a
// rung. Both out-of-range directions are genuinely reachable: the numbers
// arrive over the wire from a row a human can hand-edit, and the CLEAR is
// stored as the all-zero pair — so 0 in particular is not a hypothetical, it is
// the exact value a cleared estimate carries in the store.
//
// Returning nil rather than clamping is what keeps the drilldown honest: a
// clamp would label a 0 as "Mechanical" and an 11 as "A day's thought", which
// reads as a real estimate at a real rung.
final class LoadScaleEdgeTests: XCTestCase {

    func testLabelRefusesEveryIndexOutsideOneThroughTen() {
        for n in [0, 11, -1, Int.max] {
            XCTAssertNil(LoadAnchors.label(unblock: n), "unblock label for \(n)")
            XCTAssertNil(LoadAnchors.label(scope: n), "scope label for \(n)")
        }
    }

    // The two ends of the accepted range, from the inside. Paired with the test
    // above, these are what make the refusal a BOUNDARY rather than a blanket
    // nil — a `label` that returned nil for everything would pass the refusals
    // on its own.
    func testLabelAnswersAtBothEndsOfTheRange() {
        for n in [1, 10] {
            XCTAssertNotNil(LoadAnchors.label(unblock: n), "unblock label for \(n)")
            XCTAssertNotNil(LoadAnchors.label(scope: n), "scope label for \(n)")
        }
    }

    // Ten rungs per axis, no more and no fewer. The count is the invariant the
    // refusal above is derived from: an eleventh entry would be a label with no
    // reachable index, and a ninth-and-last would make `label(_: 10)` nil while
    // the API still accepts a 10.
    func testEachLadderHasExactlyTenRungs() {
        XCTAssertEqual(LoadAnchors.unblock.count, 10)
        XCTAssertEqual(LoadAnchors.scope.count, 10)
    }
}
