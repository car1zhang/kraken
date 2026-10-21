import XCTest
import Foundation
import Network
@testable import Kraken

// AskThreadContractTests — athena-logic's BLIND contract tests for the
// ask-threads port (kraken-core PR #5), Slice E (iOS): the additive WebThread /
// WebBoard / ThreadReadResponse keys, `isHeadless`, ThreadActionRequest.headless,
// the free function `threadOriginBadge`, `expectedContract`, and the URL
// `APIClient.fetchNodeThreadRead` requests. Written from
// artifacts/50-contract.md alone; nothing under ios/Kraken was opened (existing
// test files were read for fixture conventions). LOGIC dimension. Every helper
// is prefixed askContract. Meant to sit at ios/KrakenTests/.

final class AskThreadContractTests: XCTestCase {

    // MARK: fixtures (the contract's running example)

    private func askContractThread(_ extra: String = "") -> String {
        #"{"tid":"t3","title":"why is CI red","state":"live","created_at":1,"role":"ask""# + extra + "}"
    }

    private let askContractOrigin = #","kind":"headless","origin":{"node_id":"nSTUDIO","label":"studio-mini","login":"jess@probook.ai"}"#

    private func askContractDecodeThread(_ json: String) throws -> WebThread {
        try JSONDecoder().decode(WebThread.self, from: Data(json.utf8))
    }

    private func askContractBoard(_ extra: String) -> String {
        """
        {"daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 1},
         "daemon_ok": true, "cell_cap": 4, "live_cells": 1, "jobs": [], "contract": 7, "stale": false\(extra)}
        """
    }

    private func askContractReadJSON(_ extra: String) -> String {
        #"{"blocks":[],"counted":0,"live":true,"pane":"","fork_uuid":"","source":"none","session":"s""# + extra + "}"
    }

    // MARK: WebThread

    /// Contract E `WebThread`: "adds kind, origin, answering, note … with
    /// WebThreadOrigin { nodeId, label, login } (keys node_id, label, login)
    /// and var isHeadless: Bool { kind == "headless" }; the existing role
    /// stays — decodes with and without the keys".
    func testAskContractWebThreadDecodesTheNewKeys() throws {
        let t = try askContractDecodeThread(askContractThread(askContractOrigin + #","answering":true,"note":"ask failed: exit status 1""#))
        XCTAssertEqual(t.kind, "headless")
        XCTAssertTrue(t.isHeadless)
        XCTAssertEqual(t.role, "ask")
        XCTAssertEqual(t.origin?.nodeId, "nSTUDIO")
        XCTAssertEqual(t.origin?.label, "studio-mini")
        XCTAssertEqual(t.origin?.login, "jess@probook.ai")
        XCTAssertEqual(t.answering, true)
        XCTAssertEqual(t.note, "ask failed: exit status 1")
    }

    func testAskContractWebThreadDecodesWithoutTheNewKeys() throws {
        let t = try askContractDecodeThread(askContractThread())
        XCTAssertNil(t.kind)
        XCTAssertNil(t.origin)
        XCTAssertNil(t.answering)
        XCTAssertNil(t.note)
        XCTAssertFalse(t.isHeadless)
        let pane = try askContractDecodeThread(askContractThread(#","kind":"pane","pane":"%7""#))
        XCTAssertEqual(pane.kind, "pane")
        XCTAssertFalse(pane.isHeadless)
    }

    /// `isHeadless` reads `kind`, never `role`: a role-"ask" pane row and a
    /// mind row are not headless, and kraken-core's old kind value "ask" is
    /// not this repo's headless spelling.
    func testAskContractIsHeadlessReadsKindNotRole() throws {
        let askRolePane = try askContractDecodeThread(askContractThread(#","kind":"pane""#))
        XCTAssertFalse(askRolePane.isHeadless)
        let mind = try askContractDecodeThread(#"{"tid":"t2","title":"mind","state":"live","created_at":1,"role":"mind","kind":"pane"}"#)
        XCTAssertFalse(mind.isHeadless)
        XCTAssertEqual(mind.role, "mind")
        let oldKind = try askContractDecodeThread(askContractThread(#","kind":"ask""#))
        XCTAssertFalse(oldKind.isHeadless)
    }

    // MARK: WebBoard / ThreadReadResponse

    /// Contract E `WebBoard`: "adds let nodeId: String? (key node_id)".
    func testAskContractWebBoardNodeId() throws {
        let with = try JSONDecoder().decode(WebBoard.self, from: Data(askContractBoard(#","node_id":"nOWNER""#).utf8))
        XCTAssertEqual(with.nodeId, "nOWNER")
        let without = try JSONDecoder().decode(WebBoard.self, from: Data(askContractBoard("").utf8))
        XCTAssertNil(without.nodeId)
    }

    /// Job 634 `WebBoard`: adds `let nodeLabel: String?` (key `node_label`).
    func testWebBoardNodeLabel() throws {
        let with = try JSONDecoder().decode(WebBoard.self, from: Data(askContractBoard(#","node_label":"owner-mac""#).utf8))
        XCTAssertEqual(with.nodeLabel, "owner-mac")
        let without = try JSONDecoder().decode(WebBoard.self, from: Data(askContractBoard("").utf8))
        XCTAssertNil(without.nodeLabel)
    }

    /// Contract E `ThreadReadResponse`: "adds let kind: String?; let answering:
    /// Bool?; let note: String?".
    func testAskContractThreadReadResponseNewKeys() throws {
        let r = try JSONDecoder().decode(ThreadReadResponse.self, from: Data(askContractReadJSON(
            #","kind":"headless","answering":true,"note":"ask cancelled","origin":{"node_id":"nSTUDIO","label":"studio-mini","login":"jess@probook.ai"}"#).utf8))
        XCTAssertEqual(r.kind, "headless")
        XCTAssertEqual(r.answering, true)
        XCTAssertEqual(r.note, "ask cancelled")
        XCTAssertTrue(r.live)

        let bare = try JSONDecoder().decode(ThreadReadResponse.self, from: Data(askContractReadJSON("").utf8))
        XCTAssertNil(bare.kind)
        XCTAssertNil(bare.answering)
        XCTAssertNil(bare.note)
    }

    // MARK: ThreadActionRequest.headless

    /// Contract E `ThreadActionRequest`: "adds var headless: Bool? = nil —
    /// encoded only when set".
    func testAskContractThreadActionRequestHeadless() throws {
        func keys(_ r: ThreadActionRequest) throws -> [String: Any] {
            let data = try JSONEncoder().encode(r)
            return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
        }
        var on = ThreadActionRequest(slug: "tech-comms-rollout", op: "branch")
        on.headless = true
        XCTAssertEqual(try keys(on)["headless"] as? Bool, true)
        var off = ThreadActionRequest(slug: "tech-comms-rollout", op: "branch")
        off.headless = false
        XCTAssertEqual(try keys(off)["headless"] as? Bool, false)
        let unset = ThreadActionRequest(slug: "tech-comms-rollout", op: "branch")
        XCTAssertNil(unset.headless)
        XCTAssertNil(try keys(unset)["headless"])
    }

    // MARK: threadOriginBadge

    /// Contract E `threadOriginBadge`: "the same rule as the SPA's (nil for a
    /// non-headless row, a row with no origin, or (origin.nodeId) ==
    /// (boardNodeId ?? ""); else label → login → nodeId)".
    func testAskContractThreadOriginBadge() throws {
        let foreign = try askContractDecodeThread(askContractThread(askContractOrigin))
        XCTAssertEqual(threadOriginBadge(foreign, boardNodeId: "nOWNER"), "studio-mini")
        XCTAssertEqual(threadOriginBadge(foreign, boardNodeId: nil), "studio-mini")
        XCTAssertNil(threadOriginBadge(foreign, boardNodeId: "nSTUDIO"))

        let pane = try askContractDecodeThread(askContractThread(
            #","kind":"pane","origin":{"node_id":"nSTUDIO","label":"studio-mini","login":"jess@probook.ai"}"#))
        XCTAssertNil(threadOriginBadge(pane, boardNodeId: "nOWNER"))

        let bare = try askContractDecodeThread(askContractThread(#","kind":"headless""#))
        XCTAssertNil(threadOriginBadge(bare, boardNodeId: "nOWNER"))

        let loginOnly = try askContractDecodeThread(askContractThread(
            #","kind":"headless","origin":{"node_id":"nSTUDIO","label":"","login":"jess@probook.ai"}"#))
        XCTAssertEqual(threadOriginBadge(loginOnly, boardNodeId: "nOWNER"), "jess@probook.ai")
        let idOnly = try askContractDecodeThread(askContractThread(
            #","kind":"headless","origin":{"node_id":"nSTUDIO","label":"","login":""}"#))
        XCTAssertEqual(threadOriginBadge(idOnly, boardNodeId: "nOWNER"), "nSTUDIO")

        let emptyNode = try askContractDecodeThread(askContractThread(
            #","kind":"headless","origin":{"node_id":"","label":"owner-mac","login":"Owner"}"#))
        XCTAssertNil(threadOriginBadge(emptyNode, boardNodeId: nil))
        XCTAssertEqual(threadOriginBadge(emptyNode, boardNodeId: "nOWNER"), "owner-mac")

        let kindless = try askContractDecodeThread(askContractThread(
            #","origin":{"node_id":"nSTUDIO","label":"studio-mini","login":"jess@probook.ai"}"#))
        XCTAssertNil(threadOriginBadge(kindless, boardNodeId: "nOWNER"))
    }

    // MARK: expectedContract

    /// Contract E `expectedContract`: "stays 7".
    func testAskContractExpectedContractStaysSeven() {
        XCTAssertEqual(APIClient.expectedContract, 7)
    }

    // MARK: APIClient.fetchNodeThreadRead

    /// Contract E `APIClient.fetchNodeThreadRead`: "GET
    /// /api/nodes/thread/read?node=&slug=&tid= plus &n= only when given".
    /// Observed on a loopback listener, since no network seam is stated.
    func testAskContractFetchNodeThreadReadURL() async throws {
        let reply = askContractReadJSON(#","kind":"headless","answering":true,"note":"""#)
        for n in [nil, 20] as [Int?] {
            let server = try AskContractLoopback(reply: reply)
            let port = try await server.ready()
            let r = try await APIClient.shared.fetchNodeThreadRead(
                baseURL: "http://127.0.0.1:\(port)", node: "nOWNER", slug: "tech-comms-rollout", tid: "t3", n: n)
            XCTAssertEqual(r.answering, true)
            XCTAssertEqual(r.kind, "headless")
            let line = server.requestLine ?? ""
            let parts = line.split(separator: " ")
            XCTAssertEqual(parts.first.map(String.init), "GET", line)
            guard parts.count >= 2, let comps = URLComponents(string: String(parts[1])) else {
                server.stop()
                return XCTFail("no request target in \(line)")
            }
            XCTAssertEqual(comps.path, "/api/nodes/thread/read")
            var q: [String: String] = [:]
            for item in comps.queryItems ?? [] { q[item.name] = item.value ?? "" }
            var want = ["node": "nOWNER", "slug": "tech-comms-rollout", "tid": "t3"]
            if let n { want["n"] = String(n) }
            XCTAssertEqual(q, want, line)
            server.stop()
        }
    }
}

/// A one-shot loopback HTTP listener: records the first request line and
/// answers 200 with `reply`.
final class AskContractLoopback: @unchecked Sendable {
    private let listener: NWListener
    private let lock = NSLock()
    private var line: String?
    private let reply: String

    init(reply: String) throws {
        self.reply = reply
        listener = try NWListener(using: .tcp, on: .any)
        listener.newConnectionHandler = { [weak self] conn in self?.serve(conn) }
    }

    var requestLine: String? {
        lock.lock(); defer { lock.unlock() }
        return line
    }

    func ready() async throws -> UInt16 {
        try await withCheckedThrowingContinuation { (cont: CheckedContinuation<UInt16, Error>) in
            let once = NSLock()
            var resumed = false
            let listener = self.listener
            listener.stateUpdateHandler = { state in
                once.lock(); defer { once.unlock() }
                guard !resumed else { return }
                switch state {
                case .ready:
                    resumed = true
                    cont.resume(returning: listener.port?.rawValue ?? 0)
                case .failed(let err):
                    resumed = true
                    cont.resume(throwing: err)
                default:
                    break
                }
            }
            listener.start(queue: DispatchQueue(label: "askcontract.loopback"))
        }
    }

    func stop() { listener.cancel() }

    private func serve(_ conn: NWConnection) {
        conn.start(queue: DispatchQueue(label: "askcontract.conn"))
        conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { [weak self] data, _, _, _ in
            guard let self else { return }
            if let data, let text = String(data: data, encoding: .utf8) {
                self.lock.lock()
                if self.line == nil { self.line = text.components(separatedBy: "\r\n").first }
                self.lock.unlock()
            }
            let body = self.reply
            let resp = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\nConnection: close\r\n\r\n\(body)"
            conn.send(content: Data(resp.utf8), completion: .contentProcessed { _ in conn.cancel() })
        }
    }
}
