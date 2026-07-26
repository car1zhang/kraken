import XCTest
@testable import Kraken

// Exercises BoardDragState's transitions and NSItemProvider.dragEnding's
// release callback — the two halves of the board's drag-end signal. The
// behavior under test is the one SwiftUI does not provide: a drag that ends
// WITHOUT a drop (released over empty space, the column header, the nav bar)
// gets no DropDelegate callback at all, so the reset has to come from the
// item provider's release instead. That arrives late and on an arbitrary
// thread, which is what the generation token defends against —
// testLateDragEndDoesNotKillTheDragThatFollowedIt is the test that fails if
// the token is removed.
//
// BoardView itself isn't exercised here: a SwiftUI View's @State can't be
// driven from XCTest, so the gesture-level checks live in the manual matrix
// in the artifact set's 52-testing-plan.md instead.
final class BoardDragStateTests: XCTestCase {

    // MARK: - BoardDragState

    func testBeginSetsIdentity() {
        var drag = BoardDragState()
        let generation = drag.begin(slug: "ios-dragging-fix", column: .working)

        XCTAssertEqual(drag.slug, "ios-dragging-fix")
        XCTAssertEqual(drag.sourceColumn, .working)
        XCTAssertNil(drag.hoveredColumn)
        XCTAssertTrue(drag.isActive)
        XCTAssertGreaterThan(generation, 0)
    }

    func testBeginBumpsGenerationEachTime() {
        var drag = BoardDragState()
        let first = drag.begin(slug: "a", column: .draft)
        drag.endActive()
        let second = drag.begin(slug: "b", column: .draft)

        XCTAssertNotEqual(first, second)
    }

    // A second begin without an intervening end (a drag starting while the
    // board still thinks one is live) must still hand back a fresh token —
    // otherwise the abandoned drag's provider could end the new one.
    func testBeginWhileActiveReplacesTheDragAndBumpsGeneration() {
        var drag = BoardDragState()
        let first = drag.begin(slug: "a", column: .draft)
        drag.hover(.queued)
        let second = drag.begin(slug: "b", column: .queued)

        XCTAssertNotEqual(first, second)
        XCTAssertEqual(drag.slug, "b")
        XCTAssertEqual(drag.sourceColumn, .queued)
        XCTAssertNil(drag.hoveredColumn, "a fresh drag starts with nothing hovered")
        XCTAssertFalse(drag.end(generation: first), "the replaced drag's token is stale")
        XCTAssertTrue(drag.isActive)
    }

    func testHoverSetsAndClearsWithoutDisturbingIdentity() {
        var drag = BoardDragState()
        drag.begin(slug: "ios-dragging-fix", column: .working)

        drag.hover(.blocked)
        XCTAssertEqual(drag.hoveredColumn, .blocked)
        XCTAssertEqual(drag.slug, "ios-dragging-fix")
        XCTAssertEqual(drag.sourceColumn, .working)

        drag.hover(nil)
        XCTAssertNil(drag.hoveredColumn)
        XCTAssertEqual(drag.slug, "ios-dragging-fix")
        XCTAssertEqual(drag.sourceColumn, .working)
    }

    // No drag in flight means no strip cell may light up.
    func testHoverIsIgnoredWhenIdle() {
        var drag = BoardDragState()
        drag.hover(.blocked)

        XCTAssertNil(drag.hoveredColumn)
        XCTAssertFalse(drag.isActive)
    }

    func testEndActiveClearsEverything() {
        var drag = BoardDragState()
        drag.begin(slug: "ios-dragging-fix", column: .working)
        drag.hover(.blocked)

        drag.endActive()

        XCTAssertNil(drag.slug)
        XCTAssertNil(drag.sourceColumn)
        XCTAssertNil(drag.hoveredColumn)
        XCTAssertFalse(drag.isActive)
    }

    func testEndActiveInvalidatesTheOutstandingToken() {
        var drag = BoardDragState()
        let generation = drag.begin(slug: "ios-dragging-fix", column: .working)
        drag.endActive()

        XCTAssertFalse(drag.end(generation: generation))
    }

    func testEndWithCurrentGenerationClears() {
        var drag = BoardDragState()
        let generation = drag.begin(slug: "ios-dragging-fix", column: .working)
        drag.hover(.blocked)

        XCTAssertTrue(drag.end(generation: generation))
        XCTAssertNil(drag.slug)
        XCTAssertNil(drag.sourceColumn)
        XCTAssertNil(drag.hoveredColumn)
        XCTAssertFalse(drag.isActive)
    }

    func testEndWithStaleGenerationIsANoOp() {
        var drag = BoardDragState()
        let stale = drag.begin(slug: "a", column: .draft)
        drag.endActive()
        drag.begin(slug: "b", column: .queued)

        XCTAssertFalse(drag.end(generation: stale))
        XCTAssertEqual(drag.slug, "b", "the live drag survives a stale end")
    }

    // THE RACE. carl drops one card, immediately starts dragging another,
    // and only then does the first drag's item provider finally deallocate.
    // Without the generation token that late deinit blanks a gesture still
    // in progress.
    func testLateDragEndDoesNotKillTheDragThatFollowedIt() {
        var drag = BoardDragState()
        let first = drag.begin(slug: "ios-dragging-fix", column: .working)   // gesture 1 starts
        drag.endActive()                                                      // …and drops on a card
        let second = drag.begin(slug: "btw-on-jobs", column: .working)        // gesture 2 starts

        XCTAssertFalse(drag.end(generation: first), "gesture 1's provider deallocates late")

        XCTAssertTrue(drag.isActive)
        XCTAssertEqual(drag.slug, "btw-on-jobs")
        XCTAssertEqual(drag.sourceColumn, .working)
        XCTAssertTrue(drag.end(generation: second), "gesture 2 can still end normally")
    }

    func testEndWhenIdleIsANoOpAndDoesNotBumpTheGeneration() {
        var drag = BoardDragState()
        let generation = drag.begin(slug: "ios-dragging-fix", column: .working)
        drag.endActive()
        let afterDrop = drag.generation

        XCTAssertFalse(drag.end(generation: generation))
        XCTAssertFalse(drag.end(generation: afterDrop))
        XCTAssertEqual(drag.generation, afterDrop, "an idle end must not burn a generation")
    }

    // Generation 0 is the pristine state and no token is ever handed out for
    // it — begin returns 1 first — but it is the documented boundary.
    func testEndWithGenerationZeroOnAPristineStateIsANoOp() {
        var drag = BoardDragState()

        XCTAssertEqual(drag.generation, 0)
        XCTAssertFalse(drag.end(generation: 0))
        XCTAssertEqual(drag.begin(slug: "a", column: .draft), 1)
    }

    func testEndActiveWhenIdleIsANoOp() {
        var drag = BoardDragState()
        let generation = drag.generation

        drag.endActive()

        XCTAssertEqual(drag.generation, generation)
        XCTAssertFalse(drag.isActive)
    }

    // MARK: - NSItemProvider.dragEnding

    func testProviderFiresOnEndWhenReleased() {
        let fired = expectation(description: "onEnd called after the last reference goes away")
        autoreleasepool {
            _ = NSItemProvider.dragEnding(slug: "ios-dragging-fix") { fired.fulfill() }
        }
        wait(for: [fired], timeout: 2)
    }

    func testProviderFiresOnEndExactlyOnce() {
        let fired = expectation(description: "onEnd called")
        let count = Counter()
        autoreleasepool {
            _ = NSItemProvider.dragEnding(slug: "ios-dragging-fix") {
                count.bump()
                fired.fulfill()
            }
        }
        wait(for: [fired], timeout: 2)
        XCTAssertEqual(count.value, 1)
    }

    // A provider held for the length of a drag must not fire early — this is
    // what makes the release a drag-END signal rather than a drag-start one.
    func testProviderDoesNotFireWhileHeld() {
        let notFired = expectation(description: "onEnd not called")
        notFired.isInverted = true
        let held = NSItemProvider.dragEnding(slug: "ios-dragging-fix") { notFired.fulfill() }

        wait(for: [notFired], timeout: 0.5)
        XCTAssertNotNil(held)
    }

    // The fix must not change what the drop side actually receives.
    func testProviderStillVendsItsSlugAsPlainText() {
        let loaded = expectation(description: "slug loaded back out of the provider")
        let provider = NSItemProvider.dragEnding(slug: "ios-dragging-fix") {}

        XCTAssertTrue(provider.canLoadObject(ofClass: NSString.self))
        // What `.onDrop(of: [.text])` actually filters on is UTType
        // conformance, not canLoadObject — and the lifetime registration must
        // neither break that nor displace the text type from the front of the
        // list, which some drop paths treat as the preferred one.
        XCTAssertTrue(provider.hasItemConformingToTypeIdentifier("public.text"))
        XCTAssertEqual(provider.registeredTypeIdentifiers.first, "public.utf8-plain-text")
        provider.loadObject(ofClass: NSString.self) { object, error in
            XCTAssertNil(error)
            XCTAssertEqual(object as? NSString, "ios-dragging-fix")
            loaded.fulfill()
        }
        wait(for: [loaded], timeout: 2)
    }

    // NSItemProvider is NSCopying, and nothing stops the drag pipeline from
    // copying one. Copying must not trap (an NSItemProvider subclass storing
    // a non-optional closure does, since NSCopying allocates via an init()
    // it no longer inherits), must not fire the callback early, and must
    // still vend the slug.
    func testCopyingAProviderIsSafeAndDoesNotFireEarly() {
        let notFired = expectation(description: "onEnd not called while a copy is alive")
        notFired.isInverted = true
        let provider = NSItemProvider.dragEnding(slug: "ios-dragging-fix") { notFired.fulfill() }

        let copy = provider.copy()
        XCTAssertNotNil(copy)
        XCTAssertTrue((copy as? NSItemProvider)?.canLoadObject(ofClass: NSString.self) ?? false)

        wait(for: [notFired], timeout: 0.5)
        XCTAssertNotNil(provider)
    }

    // The point of carrying the signal in a registration rather than a
    // subclass's deinit: a copy holds the same sentinel, so the drag-end
    // fires when the LAST reference dies, never when the first one does.
    func testOnEndWaitsForEveryCopyToBeReleased() {
        let fired = expectation(description: "onEnd called once the copy goes too")
        var copy: Any?

        autoreleasepool {
            let provider = NSItemProvider.dragEnding(slug: "ios-dragging-fix") { fired.fulfill() }
            copy = provider.copy()
        }
        // The original is gone; the copy still holds the sentinel.
        XCTAssertNotNil(copy)
        copy = nil

        wait(for: [fired], timeout: 2)
    }

    // The hop exists so a release landing inside a SwiftUI update pass can't
    // mutate @State mid-render; without it this test would see whatever
    // background queue did the releasing.
    func testOnEndIsDeliveredOnTheMainThreadEvenWhenReleasedOffIt() {
        let fired = expectation(description: "onEnd called on the main thread")
        DispatchQueue.global(qos: .userInitiated).async {
            autoreleasepool {
                _ = NSItemProvider.dragEnding(slug: "ios-dragging-fix") {
                    XCTAssertTrue(Thread.isMainThread)
                    fired.fulfill()
                }
            }
        }
        wait(for: [fired], timeout: 2)
    }

    // MARK: - the two together

    // The ordering the real board hits on every successful drop: performDrop
    // resets first, the provider is released afterwards. The second reset
    // must find nothing to do.
    func testProviderReleaseAfterADropIsAbsorbed() {
        let fired = expectation(description: "onEnd called")
        var drag = BoardDragState()
        var generation = 0

        autoreleasepool {
            let provider = NSItemProvider.dragEnding(slug: "ios-dragging-fix") { fired.fulfill() }
            generation = drag.begin(slug: "ios-dragging-fix", column: .working)
            drag.endActive()   // the drop landed
            XCTAssertNotNil(provider)
        }
        wait(for: [fired], timeout: 2)

        XCTAssertFalse(drag.end(generation: generation))
        XCTAssertFalse(drag.isActive)
    }
}

/// Reference box so a `let` closure can count its own invocations.
private final class Counter {
    private(set) var value = 0
    func bump() { value += 1 }
}
