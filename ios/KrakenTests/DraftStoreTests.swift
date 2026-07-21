import XCTest
@testable import Kraken

// Exercises DraftStore's set/get/clear semantics directly, plus `isCurrent`
// — the wake-time re-check a debounced sync performs right before it POSTs
// (see DraftStore.swift's scheduleSync) so a send's clear() always wins over
// a stale write that was already asleep past its cancellation check. The
// networking side of scheduleSync itself isn't exercised here: APIClient
// isn't injectable, so these tests target the pure decision `isCurrent`
// makes rather than timing a real 600ms debounce against a live client.
final class DraftStoreTests: XCTestCase {

    // MARK: - get/set/clear

    func testGetOnUnsetKeyReturnsEmptyString() {
        let store = DraftStore()
        XCTAssertEqual(store.get("steer:foo"), "")
    }

    func testSetThenGetRoundTrips() {
        let store = DraftStore()
        store.set("steer:foo", "hello")
        XCTAssertEqual(store.get("steer:foo"), "hello")
    }

    func testClearRemovesTheDraft() {
        let store = DraftStore()
        store.set("steer:foo", "hello")
        store.clear("steer:foo")
        XCTAssertEqual(store.get("steer:foo"), "")
    }

    // MARK: - wake-time guard (isCurrent)

    func testIsCurrentTrueWhileTextUnchanged() {
        let store = DraftStore()
        store.set("steer:foo", "hello")
        XCTAssertTrue(store.isCurrent(key: "steer:foo", text: "hello"))
    }

    func testIsCurrentFalseAfterClear() {
        // Mirrors the race the wake-time guard closes: a sync was scheduled
        // for "hello", then a send cleared the draft before that debounced
        // task woke up and POSTed — the guard must see the clear and skip.
        let store = DraftStore()
        store.set("steer:foo", "hello")
        store.clear("steer:foo")
        XCTAssertFalse(store.isCurrent(key: "steer:foo", text: "hello"))
    }

    func testIsCurrentFalseAfterNewerEdit() {
        // A second, newer edit landed before the first debounced task's own
        // sync fired — the first task's captured text is stale even though
        // the slot isn't empty (a plain nil check wouldn't catch this).
        let store = DraftStore()
        store.set("steer:foo", "hello")
        store.set("steer:foo", "hello world")
        XCTAssertFalse(store.isCurrent(key: "steer:foo", text: "hello"))
        XCTAssertTrue(store.isCurrent(key: "steer:foo", text: "hello world"))
    }

    func testIsCurrentFalseOnNeverSetKey() {
        let store = DraftStore()
        XCTAssertFalse(store.isCurrent(key: "steer:foo", text: "hello"))
    }

    // MARK: - pane-scoped keys stay local

    func testPaneScopedKeyIsPlainLocalStorage() {
        // "send:<pane>" / "shell:<pane>" keys have no job-key prefix, so
        // set() never schedules an upstream sync for them — get()/set()
        // still round-trip as plain local storage.
        let store = DraftStore()
        store.set("send:%3", "draft text")
        XCTAssertEqual(store.get("send:%3"), "draft text")
    }
}
