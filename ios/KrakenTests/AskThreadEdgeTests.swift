import XCTest
@testable import Kraken

// AskThreadEdgeTests — the EDGE dimension of slice E of
// artifacts/50-contract.md (ask threads ported into kraken): WebThread /
// WebBoard / ThreadReadResponse decoding WITHOUT the additive keys (an older
// server, or a pane row) and at their zero values, `isHeadless` on every
// non-"headless" kind (kraken-core's "ask" included — `role: "ask"` is a
// different column here), ThreadActionRequest encoding `headless` only when
// set, and threadOriginBadge's nil cases and fallbacks (the same rule as the
// SPA's). Written blind from the contract, with no sight of ios/Kraken.
// Running example: owner nOWNER, asker nSTUDIO (studio-mini,
// jess@probook.ai), thread t3.
//
// Not pinned here (contract silence, see the report): `fetchNodeThreadRead`
// omitting `n`, and the 30 s timeout on `thread-ask`/`thread-send` — no seam
// the contract names lets a test observe the URLRequest APIClient builds.
final class AskThreadEdgeTests: XCTestCase {

    /// A thread object with every pre-existing key the wire carries (role
    /// included — it rides every thread object), plus `extra`.
    private func thread(_ extra: String = "") -> WebThread {
        let json = #"{"tid":"t3","title":"ci red","session_id":"7c1e9d44-0000-4000-8000-000000000003","state":"live","pane":"","created_at":0,"closed_at":0,"role":"ask""# + extra + "}"
        return try! JSONDecoder().decode(WebThread.self, from: Data(json.utf8))
    }

    private let studio = #","origin":{"node_id":"nSTUDIO","label":"studio-mini","login":"jess@probook.ai"}"#

    // MARK: decode without the keys

    func testWebThreadDecodesWithoutAnyNewKey() throws {
        let t = thread()
        XCTAssertNil(t.kind)
        XCTAssertNil(t.origin)
        XCTAssertNil(t.answering)
        XCTAssertNil(t.note)
        XCTAssertFalse(t.isHeadless)
    }

    func testWebThreadDecodesAPaneRowWithOnlyKind() throws {
        let t = thread(#","kind":"pane""#)
        XCTAssertEqual(t.kind, "pane")
        XCTAssertNil(t.origin)
        XCTAssertNil(t.answering)
        XCTAssertNil(t.note)
        XCTAssertFalse(t.isHeadless)
    }

    func testWebThreadDecodesAHeadlessRowWithEveryKey() throws {
        let t = thread(#","kind":"headless""# + studio + #","answering":true,"note":"ask cancelled""#)
        XCTAssertTrue(t.isHeadless)
        XCTAssertEqual(t.origin?.nodeId, "nSTUDIO")
        XCTAssertEqual(t.origin?.label, "studio-mini")
        XCTAssertEqual(t.origin?.login, "jess@probook.ai")
        XCTAssertEqual(t.answering, true)
        XCTAssertEqual(t.note, "ask cancelled")
    }

    func testWebThreadDecodesAMindPaneRow() throws {
        let json = #"{"tid":"t1","title":"mind","session_id":"m1","state":"live","pane":"%5","created_at":0,"closed_at":0,"role":"mind","kind":"pane"}"#
        let t = try JSONDecoder().decode(WebThread.self, from: Data(json.utf8))
        XCTAssertEqual(t.kind, "pane")
        XCTAssertFalse(t.isHeadless)
    }

    func testIsHeadlessOnlyForTheExactWord() {
        for kind in ["pane", "ask", "HEADLESS", "Headless", "", "headless ", " headless", "headlesss"] {
            XCTAssertFalse(thread(#","kind":"\#(kind)""#).isHeadless, "kind \(kind.debugDescription)")
        }
        XCTAssertTrue(thread(#","kind":"headless""#).isHeadless)
    }

    private let boardBase = """
    {"daemon":{"pid":4242,"started_at":1780000000000,"last_tick":1780000004000,"cells":2},
     "daemon_ok":true,"cell_cap":4,"live_cells":2,"jobs":[],"contract":7,"stale":false
    """

    func testWebBoardDecodesWithAndWithoutNodeID() throws {
        let without = try JSONDecoder().decode(WebBoard.self, from: Data((boardBase + "}").utf8))
        XCTAssertNil(without.nodeId)
        let with = try JSONDecoder().decode(WebBoard.self, from: Data((boardBase + #","node_id":"nOWNER"}"#).utf8))
        XCTAssertEqual(with.nodeId, "nOWNER")
    }

    func testThreadReadDecodesWithoutTheNewKeys() throws {
        let json = #"{"blocks":[],"counted":0,"live":false,"pane":"","fork_uuid":"","source":"none","session":""}"#
        let r = try JSONDecoder().decode(ThreadReadResponse.self, from: Data(json.utf8))
        XCTAssertNil(r.kind)
        XCTAssertNil(r.answering)
        XCTAssertNil(r.note)
    }

    func testThreadReadDecodesTheNewKeysAtTheirZeroValues() throws {
        let json = #"{"blocks":[],"counted":0,"live":true,"pane":"","fork_uuid":"","source":"none","session":"","kind":"headless","answering":false,"note":""}"#
        let r = try JSONDecoder().decode(ThreadReadResponse.self, from: Data(json.utf8))
        XCTAssertEqual(r.kind, "headless")
        XCTAssertEqual(r.answering, false)
        XCTAssertEqual(r.note, "")
    }

    func testThreadReadDecodesWithAnOriginObjectPresent() throws {
        // the server sends `origin` on the read route; the iOS struct need not
        // model it, but its presence must not break the decode
        let json = #"{"blocks":[],"counted":0,"live":true,"pane":"","fork_uuid":"","source":"none","session":"","kind":"headless","answering":true,"note":"x","origin":{"node_id":"nSTUDIO","label":"studio-mini","login":"jess@probook.ai"}}"#
        let r = try JSONDecoder().decode(ThreadReadResponse.self, from: Data(json.utf8))
        XCTAssertEqual(r.answering, true)
        XCTAssertEqual(r.note, "x")
    }

    // MARK: ThreadActionRequest.headless

    private func encodedKeys(_ req: ThreadActionRequest) throws -> [String: Any] {
        let data = try JSONEncoder().encode(req)
        return try JSONSerialization.jsonObject(with: data) as! [String: Any]
    }

    func testHeadlessOmittedWhenNil() throws {
        let req = ThreadActionRequest(slug: "tech-comms-rollout", op: "branch", title: "ci red", text: "why is CI red on the rollout PR?")
        XCTAssertNil(req.headless)
        XCTAssertFalse(try encodedKeys(req).keys.contains("headless"))
    }

    func testHeadlessEncodedWhenSet() throws {
        for value in [false, true] {
            var req = ThreadActionRequest(slug: "tech-comms-rollout", op: "branch", title: "ci red", text: "q")
            req.headless = value
            let keys = try encodedKeys(req)
            XCTAssertEqual(keys["headless"] as? Bool, value, "headless \(value)")
        }
    }

    // MARK: threadOriginBadge

    func testBadgeNilForAPaneRowEvenWithAnOrigin() {
        let t = thread(#","kind":"pane""# + studio)
        XCTAssertNil(threadOriginBadge(t, boardNodeId: "nOWNER"))
        XCTAssertNil(threadOriginBadge(t, boardNodeId: nil))
    }

    func testBadgeNilForARowWithNoKind() {
        let t = thread(studio)
        XCTAssertNil(threadOriginBadge(t, boardNodeId: "nOWNER"))
        XCTAssertNil(threadOriginBadge(t, boardNodeId: nil))
    }

    func testBadgeNilForKrakenCoresAskKind() {
        let t = thread(#","kind":"ask""# + studio)
        XCTAssertNil(threadOriginBadge(t, boardNodeId: "nOWNER"))
    }

    func testBadgeNilForAHeadlessRowWithNoOrigin() {
        let t = thread(#","kind":"headless""#)
        XCTAssertNil(threadOriginBadge(t, boardNodeId: "nOWNER"))
        XCTAssertNil(threadOriginBadge(t, boardNodeId: nil))
    }

    func testBadgeEmptyOriginNodeMatchesABoardWithNoNodeID() {
        let t = thread(#","kind":"headless","origin":{"node_id":"","label":"owner-mac","login":"Owner"}"#)
        XCTAssertNil(threadOriginBadge(t, boardNodeId: nil))
        XCTAssertNil(threadOriginBadge(t, boardNodeId: ""))
        XCTAssertEqual(threadOriginBadge(t, boardNodeId: "nOWNER"), "owner-mac")
    }

    func testBadgeNilForTheViewingBoardsOwnNode() {
        let t = thread(#","kind":"headless""# + studio)
        XCTAssertNil(threadOriginBadge(t, boardNodeId: "nSTUDIO"))
    }

    func testBadgeComparesNodeIDsExactly() {
        let t = thread(#","kind":"headless""# + studio)
        XCTAssertEqual(threadOriginBadge(t, boardNodeId: "nstudio"), "studio-mini")
    }

    func testBadgeLabelForAForeignHeadlessRowAndWithNoBoardID() {
        let t = thread(#","kind":"headless""# + studio)
        XCTAssertEqual(threadOriginBadge(t, boardNodeId: "nOWNER"), "studio-mini")
        XCTAssertEqual(threadOriginBadge(t, boardNodeId: nil), "studio-mini")
        XCTAssertEqual(threadOriginBadge(t, boardNodeId: ""), "studio-mini")
    }

    func testBadgeFallsBackToLoginThenNodeID() {
        let noLabel = thread(#","kind":"headless","origin":{"node_id":"nSTUDIO","label":"","login":"jess@probook.ai"}"#)
        XCTAssertEqual(threadOriginBadge(noLabel, boardNodeId: "nOWNER"), "jess@probook.ai")
        let bare = thread(#","kind":"headless","origin":{"node_id":"nSTUDIO","label":"","login":""}"#)
        XCTAssertEqual(threadOriginBadge(bare, boardNodeId: "nOWNER"), "nSTUDIO")
    }
}
