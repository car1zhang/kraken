import XCTest
@testable import Kraken

// FleetEdgeTests — the EDGE dimension of 50-contract.md slice I: the tailnet
// detector's boundaries and garbage, PairingStore on corrupt and cleared
// data, PairFlow's zero-match / two-match / single-node outcomes, Base64URL
// on empty and short inputs, remoteMode with an owner but no visibility, a
// board whose `nodes` entry has drifted, and authorize with no record.
// Written blind from the contract text, with no sight of ios/Kraken. Happy
// paths are FleetContractTests'.
final class FleetEdgeTests: XCTestCase {

    // MARK: - fakes

    private final class EdgeFleetBackend: PairingStoreBackend {
        var data: Data?
        init(_ data: Data?) { self.data = data }
        func read() -> Data? { data }
        func write(_ d: Data) throws { data = d }
        func delete() { data = nil }
    }

    private struct EdgeFleetClient: PairClient {
        let urls: [URL]
        /// node URL string → the label it answers with; nil means "no match"
        let matches: [String: String]

        func nodeURLs(seed: URL) async throws -> [URL] { urls }

        func challenge(at node: URL) async throws -> PairChallenge {
            let label = matches[node.absoluteString] ?? "node-\(node.port ?? 0)"
            return PairChallenge(nodeID: "id-\(label)", label: label,
                                 salt: Base64URL.encode(Data([1, 2, 3, 4])), iters: 1,
                                 nonce: Base64URL.encode(Data(repeating: 7, count: 16)))
        }

        func attempt(at node: URL, nonce: String, proof: String, deviceLabel: String) async throws -> PairResult? {
            guard let label = matches[node.absoluteString] else { return nil }
            return PairResult(token: "tok-\(label)", nodeID: "id-\(label)", label: label)
        }
    }

    private var savedBackend: PairingStoreBackend!

    override func setUp() {
        super.setUp()
        savedBackend = PairingStore.backend
    }

    override func tearDown() {
        PairingStore.backend = savedBackend
        super.tearDown()
    }

    // MARK: - TailnetDetector

    // "`isTailnetAddress` true for 100.64.0.0/10 IPv4 only (`100.90.94.3` yes;
    // `10.0.0.1`, `192.168.1.2`, `100.128.0.1` no)"
    func testEdgeTailnetDetectorRejectsNonTailnet() {
        for ip in ["100.128.0.1", "10.0.0.1", "192.168.1.2", "100.63.255.255", "fd7a:115c:a1e0::1",
                   "fe80::1", "::ffff:100.90.94.3", "garbage", "", "100.64.0", "100.64.0.256", "100.64.0.1.5"] {
            XCTAssertFalse(TailnetDetector.isTailnetAddress(ip), "\(ip) read as a tailnet address")
        }
    }

    func testEdgeTailnetDetectorBlockEdges() {
        XCTAssertTrue(TailnetDetector.isTailnetAddress("100.64.0.0"))
        XCTAssertTrue(TailnetDetector.isTailnetAddress("100.127.255.255"))
    }

    // "`isOnTailnet` true iff any address qualifies" — none qualifying, and none at all.
    func testEdgeIsOnTailnetWithNoQualifyingAddress() {
        XCTAssertFalse(TailnetDetector.isOnTailnet(addresses: []))
        XCTAssertFalse(TailnetDetector.isOnTailnet(addresses: ["10.0.0.1", "192.168.1.2", "fe80::1", "garbage"]))
        XCTAssertTrue(TailnetDetector.isOnTailnet(addresses: ["10.0.0.1", "garbage", "100.90.94.3"]))
    }

    // MARK: - PairingStore

    // "corrupt data loads as nil."
    func testEdgePairingStoreCorruptDataLoadsNil() {
        for bytes in [Data("not json".utf8), Data(), Data([0xff, 0x00, 0x13]), Data("{\"token\":\"t\"}".utf8)] {
            PairingStore.backend = EdgeFleetBackend(bytes)
            XCTAssertNil(PairingStore.load(), "corrupt record \(bytes as NSData) loaded")
        }
    }

    // "`load` after `clear` is nil."
    func testEdgePairingStoreClearThenLoadIsNil() throws {
        let backend = EdgeFleetBackend(nil)
        PairingStore.backend = backend
        try PairingStore.save(PairingRecord(token: "t", nodeID: "n", label: "Carl's Mac", url: "http://100.90.94.3:7777"))
        XCTAssertNotNil(PairingStore.load())
        PairingStore.clear()
        XCTAssertNil(PairingStore.load())
        // clearing an already-empty store stays empty
        PairingStore.clear()
        XCTAssertNil(PairingStore.load())
    }

    // MARK: - PairFlow

    // "Zero → `.noMatch`"
    func testEdgePairFlowZeroMatches() async {
        let a = URL(string: "http://100.90.94.3:7777")!
        let b = URL(string: "http://100.90.94.4:7777")!
        let client = EdgeFleetClient(urls: [a, b], matches: [:])
        do {
            _ = try await PairFlow.pair(seed: a, password: "octopus-garden-2026", deviceLabel: "iPhone", client: client)
            XCTFail("pairing with no match returned a record")
        } catch let e as PairFlowError {
            XCTAssertEqual(e, .noMatch)
        } catch {
            XCTFail("unexpected error \(error)")
        }
    }

    // "more than one → `.multipleMatches(labels)`"
    func testEdgePairFlowTwoMatches() async {
        let a = URL(string: "http://100.90.94.3:7777")!
        let b = URL(string: "http://100.90.94.4:7777")!
        let client = EdgeFleetClient(urls: [a, b], matches: [a.absoluteString: "Carl's Mac", b.absoluteString: "studio-mini"])
        do {
            _ = try await PairFlow.pair(seed: a, password: "octopus-garden-2026", deviceLabel: "iPhone", client: client)
            XCTFail("pairing with two matches returned a record")
        } catch let e as PairFlowError {
            guard case .multipleMatches(let labels) = e else {
                return XCTFail("error \(e), want multipleMatches")
            }
            // amended: "`multipleMatches` labels in node-URL order"
            XCTAssertEqual(labels, ["Carl's Mac", "studio-mini"])
        } catch {
            XCTFail("unexpected error \(error)")
        }
    }

    // "A single-URL registry (singleplayer) pairs on its one proof."
    func testEdgePairFlowSingleNodeRegistry() async throws {
        let a = URL(string: "http://100.90.94.3:7777")!
        let client = EdgeFleetClient(urls: [a], matches: [a.absoluteString: "Carl's Mac"])
        let rec = try await PairFlow.pair(seed: a, password: "octopus-garden-2026", deviceLabel: "iPhone", client: client)
        XCTAssertEqual(rec.token, "tok-Carl's Mac")
        XCTAssertEqual(rec.nodeID, "id-Carl's Mac")
        XCTAssertEqual(rec.label, "Carl's Mac")
        XCTAssertEqual(rec.url, a.absoluteString)
    }

    // MARK: - Base64URL

    // "RawURL alphabet, no padding, matching Go's `base64.RawURLEncoding`."
    func testEdgeBase64URLEmptyAndShortInputs() {
        XCTAssertEqual(Base64URL.encode(Data()), "")
        XCTAssertEqual(Base64URL.decode(""), Data())
        let cases: [([UInt8], String)] = [([0xfb], "-w"), ([0xfb, 0xff], "-_8"), ([0xfb, 0xff, 0xbf], "-_-_")]
        for (bytes, want) in cases {
            let d = Data(bytes)
            let s = Base64URL.encode(d)
            XCTAssertEqual(s, want)
            XCTAssertFalse(s.contains("="), "\(s) is padded")
            XCTAssertFalse(s.contains("+") || s.contains("/"), "\(s) uses the standard alphabet")
            XCTAssertEqual(Base64URL.decode(s), d)
        }
    }

    func testEdgeBase64URLRejectsGarbage() {
        XCTAssertNil(Base64URL.decode("*not base64*"))
    }

    // MARK: - wire models

    private func board(jobs: String, extra: String = "") -> String {
        """
        {
          "daemon": {"pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 1},
          "daemon_ok": true,
          "cell_cap": 4,
          "live_cells": 1,
          "jobs": \(jobs),
          "contract": 7,
          "stale": false\(extra)
        }
        """
    }

    // "`remoteMode` is `.local` without owner, `.publicCell` iff `visibility ==
    // "public"`, else `.readonly`" — an owner with no visibility key.
    func testEdgeRemoteModeOwnerWithoutVisibility() throws {
        let json = """
        {"slug": "dream-2026-10-20", "repo": "kraken", "state": "working", "live": true, "mtime": 1,
         "owner": {"node_id": "nA", "label": "studio-mini", "operator": "Jess", "online": true}}
        """
        let j = try JSONDecoder().decode(WebJob.self, from: Data(json.utf8))
        XCTAssertNil(j.visibility)
        XCTAssertEqual(j.remoteMode, .readonly)
        XCTAssertTrue(j.isRemote)
        XCTAssertEqual(j.boardKey, "nA/dream-2026-10-20")
    }

    private let edgeLocalJobs = """
    [{"slug": "local-job", "repo": "kraken", "state": "draft", "live": false, "mtime": 1, "visibility": "readonly"}]
    """

    private func edgeNode(_ id: String) -> String {
        """
        {"node_id": "\(id)", "label": "\(id)", "operator": "Jess", "dns_name": "\(id).tail072ad8.ts.net",
         "ip": "100.70.0.1", "port": 7777, "build": "abc", "contract": 7, "online": true, "last_ok": 1}
        """
    }

    // Amended: "`nodes` decoded PER ELEMENT with `try?` (… a malformed entry is
    // dropped and its neighbours kept …)" — the board survives, and the good
    // rows on either side of the drifted one are kept in order.
    func testEdgeBoardDecodeDropsMalformedNodeKeepsNeighbours() throws {
        let json = board(jobs: edgeLocalJobs, extra: """
        ,
          "nodes": [\(edgeNode("nA")), {"node_id": 42, "label": null, "online": "yes"}, \(edgeNode("nB"))]
        """)
        let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
        XCTAssertEqual(b.jobs.count, 1)
        XCTAssertEqual(b.nodes?.map(\.nodeID), ["nA", "nB"])
    }

    // Amended: "a non-array value reads as nil".
    func testEdgeBoardDecodeNonArrayNodesIsNil() throws {
        for bad in ["{\"nA\": 1}", "\"nodes\"", "42", "null"] {
            let json = board(jobs: edgeLocalJobs, extra: ",\n  \"nodes\": \(bad)")
            let b = try JSONDecoder().decode(WebBoard.self, from: Data(json.utf8))
            XCTAssertEqual(b.jobs.count, 1, "nodes=\(bad) cost the jobs")
            XCTAssertNil(b.nodes, "nodes=\(bad) did not read as nil")
        }
    }

    // MARK: - APIClient.record(for:stored:)

    private let edgeRecord = PairingRecord(token: "t", nodeID: "n", label: "Carl's Mac",
                                           url: "http://carls-macbook-bro.tail072ad8.ts.net:7777")

    // "returns `stored` when the request URL's host (case-insensitive) and
    // port … equal the record URL's, else nil" — another host or port gets nil.
    func testEdgeRecordForOtherHostOrPortIsNil() {
        for raw in ["http://studio-mini.tail072ad8.ts.net:7777/api/board",
                    "http://carls-macbook-bro.tail072ad8.ts.net:7878/api/board",
                    "http://carls-macbook-bro.tail072ad8.ts.net/api/board",
                    "http://100.90.94.3:7777/api/board",
                    "http://carls-macbook-bro.tail072ad8.ts.net.evil.example:7777/api/board"] {
            let req = URLRequest(url: URL(string: raw)!)
            XCTAssertNil(APIClient.record(for: req, stored: edgeRecord), "\(raw) was handed the record")
        }
    }

    func testEdgeRecordForHostCaseInsensitiveAndNilStored() {
        let req = URLRequest(url: URL(string: "http://CARLS-MACBOOK-BRO.tail072ad8.ts.net:7777/api/board")!)
        XCTAssertEqual(APIClient.record(for: req, stored: edgeRecord), edgeRecord)
        XCTAssertNil(APIClient.record(for: req, stored: nil))
    }

    // "`WebJobOwner.chipText`: … or `label` when `operator` is empty."
    func testEdgeChipTextEmptyOperator() throws {
        let json = #"{"node_id": "nA", "label": "studio-mini", "operator": "", "online": false}"#
        let o = try JSONDecoder().decode(WebJobOwner.self, from: Data(json.utf8))
        XCTAssertEqual(o.chipText, "studio-mini")
    }

    // `FleetFilter.apply` (job 634) with nothing owned: `["local"]` keeps
    // everything in order, an unknown node id keeps nothing, an empty board
    // stays empty, and a local card whose SLUG equals a selected node id is
    // not matched by it.
    func testEdgeFleetFilterNoOwnedCards() throws {
        let jobs = """
        [{"slug": "b", "repo": "kraken", "state": "draft", "live": false, "mtime": 1},
         {"slug": "nZ", "repo": "kraken", "state": "draft", "live": false, "mtime": 1}]
        """
        let list = try JSONDecoder().decode([WebJob].self, from: Data(jobs.utf8))
        XCTAssertEqual(FleetFilter.apply(list, selected: ["local"]).map(\.slug), ["b", "nZ"])
        XCTAssertEqual(FleetFilter.apply(list, selected: ["nZ"]).count, 0)
        XCTAssertEqual(FleetFilter.apply(list, selected: []).map(\.slug), ["b", "nZ"])
        XCTAssertEqual(FleetFilter.apply([], selected: ["local"]).count, 0)
    }

    // `FleetFilter.selection(from:)` on unreadable input is the empty
    // selection, never a throw.
    func testEdgeFleetSelectionUnreadable() {
        XCTAssertEqual(FleetFilter.selection(from: nil), [])
        XCTAssertEqual(FleetFilter.selection(from: ""), [])
        XCTAssertEqual(FleetFilter.selection(from: "not json"), [])
        XCTAssertEqual(FleetFilter.selection(from: #"{"local": true}"#), [])
        XCTAssertEqual(FleetFilter.selection(from: #"[1, null, "nB"]"#), ["nB"])
    }

    // MARK: - APIClient.authorize

    // "Adds `Authorization: Bearer <token>` when a record is present; unchanged
    // otherwise." — including a request that already carries headers.
    func testEdgeAuthorizeNilRecordLeavesRequestUnchanged() {
        var req = URLRequest(url: URL(string: "http://100.90.94.3:7777/api/board")!)
        XCTAssertEqual(APIClient.authorize(req, record: nil), req)
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        req.httpMethod = "POST"
        let out = APIClient.authorize(req, record: nil)
        XCTAssertEqual(out, req)
        XCTAssertNil(out.value(forHTTPHeaderField: "Authorization"))
    }
}
