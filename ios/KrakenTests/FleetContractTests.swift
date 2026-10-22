import XCTest
import Foundation
import CommonCrypto
@testable import Kraken

// FleetContractTests — athena-logic's BLIND contract tests for the
// multiplayer-kraken job, Slice I (ios/Kraken). Written from
// artifacts/50-contract.md alone; nothing under ios/Kraken was opened by the
// author. LOGIC dimension: the stated invariants and happy paths. Zero/multiple
// pairing matches, corrupt-ish inputs and drifted decodes beyond one case are
// athena-edge's.
//
// Pairing vectors: computed by this author with Python
//   hashlib.pbkdf2_hmac('sha256', b'octopus-garden-2026',
//                       bytes.fromhex('7f1c3a5b9d2e4f60718293a4b5c6d7e8'), 1000)
//   hmac.new(K, nonce + b'nea6fba5q821CNTRL' + b'carl-iphone', hashlib.sha256)
// with nonce = bytes.fromhex('00112233445566778899aabbccddeeff'). The same
// vectors pin the Go side (internal/api/fleet_contract_test.go), so both halves
// are checked against one truth. The fake pairing server below derives its
// expected proofs independently through CommonCrypto (never through
// PairingMath).

// MARK: - helpers

private func contractFleetHex(_ s: String) -> Data {
    var out = Data()
    var i = s.startIndex
    while i < s.endIndex {
        let j = s.index(i, offsetBy: 2)
        out.append(UInt8(s[i..<j], radix: 16)!)
        i = j
    }
    return out
}

private func contractFleetHexString(_ d: Data) -> String {
    d.map { String(format: "%02x", $0) }.joined()
}

/// Independent PBKDF2-HMAC-SHA256 (CommonCrypto), 32 bytes.
private func contractFleetPBKDF2(_ password: String, _ salt: Data, _ iters: Int) -> Data {
    var out = [UInt8](repeating: 0, count: 32)
    let s = [UInt8](salt)
    _ = CCKeyDerivationPBKDF(CCPBKDFAlgorithm(kCCPBKDF2),
                             password, password.utf8.count,
                             s, s.count,
                             CCPseudoRandomAlgorithm(kCCPRFHmacAlgSHA256), UInt32(iters),
                             &out, out.count)
    return Data(out)
}

/// Independent HMAC-SHA256 (CommonCrypto).
private func contractFleetHMAC(_ key: Data, _ msg: Data) -> Data {
    var out = [UInt8](repeating: 0, count: Int(CC_SHA256_DIGEST_LENGTH))
    let k = [UInt8](key), m = [UInt8](msg)
    CCHmac(CCHmacAlgorithm(kCCHmacAlgSHA256), k, k.count, m, m.count, &out)
    return Data(out)
}

/// Independent base64 RawURL (no padding), Go's base64.RawURLEncoding.
private func contractFleetRawURL(_ d: Data) -> String {
    d.base64EncodedString()
        .replacingOccurrences(of: "+", with: "-")
        .replacingOccurrences(of: "/", with: "_")
        .replacingOccurrences(of: "=", with: "")
}

private final class ContractFleetMemoryBackend: PairingStoreBackend {
    var data: Data?
    func read() -> Data? { data }
    func write(_ data: Data) throws { self.data = data }
    func delete() { data = nil }
}

/// A fake fleet: each node URL has its own password, salt and node id; attempt
/// answers a PairResult only for a proof the node itself would compute.
private final class ContractFleetPairClient: PairClient {
    struct Node { let password: String; let salt: Data; let nodeID: String; let label: String; let nonce: Data }
    let seedNodes: [URL]
    let nodes: [URL: Node]
    var attemptedLabels: [String] = []
    let iters = 1000

    init(seedNodes: [URL], nodes: [URL: Node]) {
        self.seedNodes = seedNodes
        self.nodes = nodes
    }

    func nodeURLs(seed: URL) async throws -> [URL] { seedNodes }

    func challenge(at node: URL) async throws -> PairChallenge {
        let n = nodes[node]!
        let json = """
        {"node_id":"\(n.nodeID)","label":"\(n.label)","salt":"\(contractFleetRawURL(n.salt))","iters":\(iters),"nonce":"\(contractFleetRawURL(n.nonce))"}
        """
        return try JSONDecoder().decode(PairChallenge.self, from: Data(json.utf8))
    }

    func attempt(at node: URL, nonce: String, proof: String, deviceLabel: String) async throws -> PairResult? {
        let n = nodes[node]!
        attemptedLabels.append(deviceLabel)
        let k = contractFleetPBKDF2(n.password, n.salt, iters)
        var msg = n.nonce
        msg.append(Data(n.nodeID.utf8))
        msg.append(Data(deviceLabel.utf8))
        let want = contractFleetRawURL(contractFleetHMAC(k, msg))
        guard nonce == contractFleetRawURL(n.nonce), proof == want else { return nil }
        let json = #"{"token":"tok-\#(n.nodeID)","node_id":"\#(n.nodeID)","label":"\#(n.label)"}"#
        return try JSONDecoder().decode(PairResult.self, from: Data(json.utf8))
    }
}

private func contractFleetJob(_ extra: [String: Any]) throws -> WebJob {
    var obj: [String: Any] = ["slug": "dream-2026-10-20", "repo": "kraken", "state": "done", "live": false, "mtime": 0]
    for (k, v) in extra { obj[k] = v }
    return try JSONDecoder().decode(WebJob.self, from: JSONSerialization.data(withJSONObject: obj))
}

private let contractFleetOwnerB: [String: Any] = ["node_id": "nSTUDIOmini77CNTRL", "label": "studio-mini", "operator": "Jess", "online": true]
private let contractFleetOwnerC: [String: Any] = ["node_id": "nKITCHEN42CNTRL", "label": "kitchen-mac", "operator": "Sam", "online": false]

final class FleetContractTests: XCTestCase {

    // MARK: PairingMath — "PBKDF2-HMAC-SHA256, 32 bytes …; proof =
    // HMAC-SHA256(key, nonce ‖ nodeID.utf8 ‖ deviceLabel.utf8). Both equal the
    // Go functions for the same inputs."

    func testContractPairingMathDeriveKeyMatchesPythonVector() {
        let k = PairingMath.deriveKey(password: "octopus-garden-2026",
                                      salt: contractFleetHex("7f1c3a5b9d2e4f60718293a4b5c6d7e8"),
                                      iterations: 1000)
        XCTAssertEqual(contractFleetHexString(k),
                       "cbb90feff1015a0c2eb8d634252cc4cccef587efde7351a75e99138631c6594f")
    }

    func testContractPairingMathProofMatchesPythonVector() {
        let k = contractFleetHex("cbb90feff1015a0c2eb8d634252cc4cccef587efde7351a75e99138631c6594f")
        let p = PairingMath.proof(key: k, nonce: contractFleetHex("00112233445566778899aabbccddeeff"),
                                  nodeID: "nea6fba5q821CNTRL", deviceLabel: "carl-iphone")
        XCTAssertEqual(contractFleetHexString(p),
                       "a0ac8b885181a4718e66c9949f68645e2ef0db3d75eeff027589e4248eccefe4")
        XCTAssertEqual(p.count, 32)
    }

    // MARK: Base64URL — "RawURL alphabet, no padding, matching Go's
    // base64.RawURLEncoding."

    func testContractBase64URLEncodesRawURL() {
        XCTAssertEqual(Base64URL.encode(Data([0xfb, 0xff, 0xbf])), "-_-_")
        XCTAssertEqual(Base64URL.encode(Data([0xfb, 0xff])), "-_8")
        XCTAssertEqual(Base64URL.encode(contractFleetHex("7f1c3a5b9d2e4f60718293a4b5c6d7e8")), "fxw6W50uT2BxgpOktcbX6A")
        XCTAssertEqual(Base64URL.encode(contractFleetHex("a0ac8b885181a4718e66c9949f68645e2ef0db3d75eeff027589e4248eccefe4")),
                       "oKyLiFGBpHGOZsmUn2hkXi7w2z117v8CdYnkJI7M7-Q")
    }

    func testContractBase64URLDecodesRawURL() {
        XCTAssertEqual(Base64URL.decode("-_8"), Data([0xfb, 0xff]))
        XCTAssertEqual(Base64URL.decode("ABEiM0RVZneImaq7zN3u_w"), contractFleetHex("00112233445566778899aabbccddeeff"))
        let d = Data((0..<40).map { UInt8($0 * 7 & 0xff) })
        XCTAssertEqual(Base64URL.decode(Base64URL.encode(d)), d)
    }

    // MARK: TailnetDetector — "isTailnetAddress true for 100.64.0.0/10 IPv4
    // only (100.90.94.3 yes; 10.0.0.1, 192.168.1.2, 100.128.0.1 no);
    // isOnTailnet true iff any address qualifies".

    func testContractTailnetDetectorAddress() {
        XCTAssertTrue(TailnetDetector.isTailnetAddress("100.90.94.3"))
        XCTAssertFalse(TailnetDetector.isTailnetAddress("10.0.0.1"))
        XCTAssertFalse(TailnetDetector.isTailnetAddress("192.168.1.2"))
        XCTAssertFalse(TailnetDetector.isTailnetAddress("100.128.0.1"))
    }

    func testContractTailnetDetectorAnyAddress() {
        XCTAssertTrue(TailnetDetector.isOnTailnet(addresses: ["192.168.1.2", "fe80::1", "100.90.94.3"]))
        XCTAssertFalse(TailnetDetector.isOnTailnet(addresses: ["192.168.1.2", "10.0.0.1"]))
    }

    // MARK: PairingStore — "load after save round-trips the record; load after
    // clear is nil; corrupt data loads as nil."

    private var savedBackend: PairingStoreBackend?

    override func setUp() {
        super.setUp()
        savedBackend = PairingStore.backend
    }

    override func tearDown() {
        if let b = savedBackend { PairingStore.backend = b }
        super.tearDown()
    }

    func testContractPairingStoreRoundTripAndClear() throws {
        let mem = ContractFleetMemoryBackend()
        PairingStore.backend = mem
        XCTAssertNil(PairingStore.load())
        let rec = PairingRecord(token: "tok-abc", nodeID: "nSELFstable11CNTRL",
                                label: "Carl's MacBook Bro", url: "http://carls-macbook-bro.tail072ad8.ts.net:7777")
        try PairingStore.save(rec)
        XCTAssertNotNil(mem.data, "save wrote nothing to the backend")
        XCTAssertEqual(PairingStore.load(), rec)
        PairingStore.clear()
        XCTAssertNil(PairingStore.load())
    }

    func testContractPairingStoreCorruptLoadsNil() {
        let mem = ContractFleetMemoryBackend()
        mem.data = Data("not a pairing record".utf8)
        PairingStore.backend = mem
        XCTAssertNil(PairingStore.load())
    }

    // MARK: APIClient

    func testContractExpectedContractIsSeven() {
        XCTAssertEqual(APIClient.expectedContract, 7)
    }

    // "Adds Authorization: Bearer <token> when a record is present; unchanged
    // otherwise."
    func testContractAuthorizeAddsBearer() {
        let req = URLRequest(url: URL(string: "http://100.90.94.3:7777/api/board")!)
        let rec = PairingRecord(token: "tok-abc", nodeID: "n1", label: "mac", url: "http://100.90.94.3:7777")
        let authed = APIClient.authorize(req, record: rec)
        XCTAssertEqual(authed.value(forHTTPHeaderField: "Authorization"), "Bearer tok-abc")
        XCTAssertEqual(authed.url, req.url)
        let plain = APIClient.authorize(req, record: nil)
        XCTAssertEqual(plain, req)
        XCTAssertNil(plain.value(forHTTPHeaderField: "Authorization"))
    }

    // AMENDED 14:40 `APIClient.record(for:stored:)`: "returns stored when the
    // request URL's host (case-insensitive) and port (nil port and 80/absent
    // treated alike for http) equal the record URL's, else nil".
    func testContractRecordScopedToRecordHost() {
        let rec = PairingRecord(token: "tok", nodeID: "n1", label: "mac", url: "http://carls-macbook-bro.tail072ad8.ts.net:7777")
        let same = URLRequest(url: URL(string: "http://Carls-MacBook-Bro.tail072ad8.ts.net:7777/api/board")!)
        XCTAssertEqual(APIClient.record(for: same, stored: rec), rec)
        let otherHost = URLRequest(url: URL(string: "http://studio-mini.tail072ad8.ts.net:7777/api/board")!)
        XCTAssertNil(APIClient.record(for: otherHost, stored: rec))
        let otherPort = URLRequest(url: URL(string: "http://carls-macbook-bro.tail072ad8.ts.net:7878/api/board")!)
        XCTAssertNil(APIClient.record(for: otherPort, stored: rec))
        XCTAssertNil(APIClient.record(for: same, stored: nil))

        let bare = PairingRecord(token: "tok", nodeID: "n1", label: "mac", url: "http://100.90.94.3")
        let explicit80 = URLRequest(url: URL(string: "http://100.90.94.3:80/api/board")!)
        XCTAssertEqual(APIClient.record(for: explicit80, stored: bare), bare)
    }

    // "Sets cookie kraken_pair=<token> for the record URL's host and port, path
    // /; clearCookie(for url: String, in storage:) removes it."
    func testContractInstallAndClearCookie() {
        let storage = HTTPCookieStorage.shared
        let urlString = "http://contract-fleet-host.tail072ad8.ts.net:7878"
        let url = URL(string: urlString)!
        let rec = PairingRecord(token: "tok-cookie", nodeID: "n1", label: "mac", url: urlString)
        defer {
            for c in storage.cookies ?? [] where c.name == "kraken_pair" && c.domain.contains("contract-fleet-host") {
                storage.deleteCookie(c)
            }
        }
        APIClient.installCookie(record: rec, into: storage)
        let found = (storage.cookies(for: url.appendingPathComponent("api/board")) ?? []).filter { $0.name == "kraken_pair" }
        XCTAssertEqual(found.count, 1)
        if let c = found.first {
            XCTAssertEqual(c.value, "tok-cookie")
            XCTAssertEqual(c.path, "/")
            XCTAssertTrue(c.domain.hasSuffix("contract-fleet-host.tail072ad8.ts.net"), "cookie domain \(c.domain)")
            XCTAssertEqual(c.portList?.map { $0.intValue }, [7878])
        }
        APIClient.clearCookie(for: urlString, in: storage)
        let after = (storage.cookies(for: url) ?? []).filter { $0.name == "kraken_pair" }
        XCTAssertTrue(after.isEmpty, "kraken_pair survived clearCookie")
    }

    // MARK: PairChallenge / PairResult keys

    func testContractPairChallengeAndResultDecode() throws {
        let ch = try JSONDecoder().decode(PairChallenge.self, from: Data("""
        {"node_id":"nSELFstable11CNTRL","label":"Carl's MacBook Bro","salt":"fxw6W50uT2BxgpOktcbX6A","iters":600000,"nonce":"ABEiM0RVZneImaq7zN3u_w"}
        """.utf8))
        XCTAssertEqual(ch.nodeID, "nSELFstable11CNTRL")
        XCTAssertEqual(ch.label, "Carl's MacBook Bro")
        XCTAssertEqual(ch.salt, "fxw6W50uT2BxgpOktcbX6A")
        XCTAssertEqual(ch.iters, 600000)
        XCTAssertEqual(ch.nonce, "ABEiM0RVZneImaq7zN3u_w")
        let res = try JSONDecoder().decode(PairResult.self, from: Data(#"{"token":"t0k","node_id":"n1","label":"mac"}"#.utf8))
        XCTAssertEqual(res.token, "t0k")
        XCTAssertEqual(res.nodeID, "n1")
        XCTAssertEqual(res.label, "mac")
    }

    // MARK: PairFlow — "For each URL: challenge, deriveKey(password,
    // Base64URL.decode(salt), iters), proof, attempt … Exactly one non-nil
    // result → PairingRecord(token, nodeID, label, url: that node's URL). … A
    // single-URL registry (singleplayer) pairs on its one proof."

    func testContractPairFlowBindsToTheMatchingNode() async throws {
        let a = URL(string: "http://100.90.94.3:7777")!
        let b = URL(string: "http://100.70.1.2:7777")!
        let client = ContractFleetPairClient(seedNodes: [a, b], nodes: [
            a: .init(password: "carls-own-password", salt: contractFleetHex("7f1c3a5b9d2e4f60718293a4b5c6d7e8"),
                     nodeID: "nSELFstable11CNTRL", label: "Carl's MacBook Bro",
                     nonce: contractFleetHex("00112233445566778899aabbccddeeff")),
            b: .init(password: "jess-studio-password", salt: contractFleetHex("0102030405060708090a0b0c0d0e0f10"),
                     nodeID: "nSTUDIOmini77CNTRL", label: "studio-mini",
                     nonce: contractFleetHex("ffeeddccbbaa99887766554433221100")),
        ])
        let rec = try await PairFlow.pair(seed: a, password: "jess-studio-password",
                                          deviceLabel: "Carl's iPhone", client: client)
        XCTAssertEqual(rec, PairingRecord(token: "tok-nSTUDIOmini77CNTRL", nodeID: "nSTUDIOmini77CNTRL",
                                          label: "studio-mini", url: b.absoluteString))
        XCTAssertTrue(client.attemptedLabels.allSatisfy { $0 == "Carl's iPhone" })
    }

    func testContractPairFlowSingleplayerPairsOnOneProof() async throws {
        let a = URL(string: "http://100.90.94.3:7777")!
        let client = ContractFleetPairClient(seedNodes: [a], nodes: [
            a: .init(password: "carls-own-password", salt: contractFleetHex("7f1c3a5b9d2e4f60718293a4b5c6d7e8"),
                     nodeID: "nSELFstable11CNTRL", label: "Carl's MacBook Bro",
                     nonce: contractFleetHex("00112233445566778899aabbccddeeff")),
        ])
        let rec = try await PairFlow.pair(seed: a, password: "carls-own-password",
                                          deviceLabel: "Carl's iPhone", client: client)
        XCTAssertEqual(rec.nodeID, "nSELFstable11CNTRL")
        XCTAssertEqual(rec.label, "Carl's MacBook Bro")
        XCTAssertEqual(rec.token, "tok-nSELFstable11CNTRL")
        XCTAssertEqual(rec.url, a.absoluteString)
        XCTAssertEqual(client.attemptedLabels.count, 1)
    }

    // MARK: WebJobOwner / WebJob / WebBoard

    // `WebJobOwner` keys node_id,label,operator,online; `chipText` is
    // "\(label) · \(operator)", or label when operator is empty.
    func testContractOwnerDecodeAndChipText() throws {
        let o = try JSONDecoder().decode(WebJobOwner.self, from: JSONSerialization.data(withJSONObject: contractFleetOwnerB))
        XCTAssertEqual(o.nodeID, "nSTUDIOmini77CNTRL")
        XCTAssertEqual(o.label, "studio-mini")
        XCTAssertEqual(o.`operator`, "Jess")
        XCTAssertEqual(o.online, true)
        XCTAssertEqual(o.chipText, "studio-mini \u{00B7} Jess")
        var bare = contractFleetOwnerB
        bare["operator"] = ""
        let b = try JSONDecoder().decode(WebJobOwner.self, from: JSONSerialization.data(withJSONObject: bare))
        XCTAssertEqual(b.chipText, "studio-mini")
    }

    // `remoteMode` is .local without owner, .publicCell iff visibility ==
    // "public", else .readonly; `boardKey` is "\(owner.nodeID)/\(slug)" when
    // owned.
    func testContractRemoteModeAndBoardKey() throws {
        let local = try contractFleetJob(["visibility": "public"])
        XCTAssertNil(local.owner)
        XCTAssertEqual(local.visibility, "public")
        XCTAssertEqual(local.remoteMode, .local)
        XCTAssertFalse(local.isRemote)

        let pub = try contractFleetJob(["owner": contractFleetOwnerB, "visibility": "public"])
        XCTAssertEqual(pub.remoteMode, .publicCell)
        XCTAssertTrue(pub.isRemote)
        XCTAssertEqual(pub.boardKey, "nSTUDIOmini77CNTRL/dream-2026-10-20")
        XCTAssertEqual(pub.owner?.label, "studio-mini")

        let ro = try contractFleetJob(["owner": contractFleetOwnerB, "visibility": "readonly"])
        XCTAssertEqual(ro.remoteMode, .readonly)
        let unspecified = try contractFleetJob(["owner": contractFleetOwnerB])
        XCTAssertEqual(unspecified.remoteMode, .readonly)
    }

    // "a decoded board with two same-slug cards from two owners yields two
    // cards" (testing plan, iOS row) + `WebBoard.nodes`.
    func testContractBoardWithTwoOwnersAndNodes() throws {
        let board: [String: Any] = [
            "daemon": ["pid": 4242, "started_at": 1780000000000, "last_tick": 1780000004000, "cells": 2],
            "daemon_ok": true, "cell_cap": 4, "live_cells": 2, "contract": 7, "stale": false,
            "jobs": [
                ["slug": "dream-2026-10-20", "repo": "kraken", "state": "done", "live": false, "mtime": 0, "visibility": "readonly"],
                ["slug": "dream-2026-10-20", "repo": "kraken", "state": "done", "live": false, "mtime": 0, "visibility": "readonly", "owner": contractFleetOwnerB],
                ["slug": "dream-2026-10-20", "repo": "kraken", "state": "done", "live": false, "mtime": 0, "visibility": "readonly", "owner": contractFleetOwnerC],
            ],
            "nodes": [
                ["node_id": "nSTUDIOmini77CNTRL", "label": "studio-mini", "operator": "Jess", "dns_name": "studio-mini.tail072ad8.ts.net",
                 "ip": "100.70.1.2", "port": 7777, "build": "abc", "contract": 7, "online": true, "last_ok": 1780000000000],
            ],
        ]
        let b = try JSONDecoder().decode(WebBoard.self, from: JSONSerialization.data(withJSONObject: board))
        XCTAssertEqual(b.jobs.count, 3)
        let owned = b.jobs.filter { $0.isRemote }
        XCTAssertEqual(owned.count, 2)
        XCTAssertEqual(Set(owned.map(\.boardKey)), ["nSTUDIOmini77CNTRL/dream-2026-10-20", "nKITCHEN42CNTRL/dream-2026-10-20"])
        XCTAssertEqual(b.nodes?.count, 1)
        let n = b.nodes?.first
        XCTAssertEqual(n?.nodeID, "nSTUDIOmini77CNTRL")
        XCTAssertEqual(n?.dnsName, "studio-mini.tail072ad8.ts.net")
        XCTAssertEqual(n?.port, 7777)
        XCTAssertEqual(n?.online, true)
        XCTAssertEqual(n?.lastOK, 1780000000000)
    }

    // `FleetFilter.apply` (job 634): an empty selection keeps every card in
    // order; `["local"]` keeps exactly the unowned cards; a node id keeps
    // exactly that node's cards; a mix is the union in server order.
    func testContractFleetFilterSelectsExactlyTheMachines() throws {
        let jobs = [
            try contractFleetJob(["slug": "local-a"]),
            try contractFleetJob(["slug": "remote-b", "owner": contractFleetOwnerB]),
            try contractFleetJob(["slug": "local-c"]),
            try contractFleetJob(["slug": "remote-d", "owner": contractFleetOwnerC]),
        ]
        XCTAssertEqual(FleetFilter.apply(jobs, selected: []).map(\.slug), ["local-a", "remote-b", "local-c", "remote-d"])
        XCTAssertEqual(FleetFilter.apply(jobs, selected: [FleetFilter.localNode]).map(\.slug), ["local-a", "local-c"])
        XCTAssertEqual(FleetFilter.apply(jobs, selected: ["nSTUDIOmini77CNTRL"]).map(\.slug), ["remote-b"])
        XCTAssertEqual(FleetFilter.apply(jobs, selected: ["nKITCHEN42CNTRL", FleetFilter.localNode]).map(\.slug), ["local-a", "local-c", "remote-d"])
        XCTAssertEqual(FleetFilter.nodeKey(of: jobs[0]), "local")
        XCTAssertEqual(FleetFilter.nodeKey(of: jobs[1]), "nSTUDIOmini77CNTRL")
    }

    // `FleetFilter.selection(from:)` / `encode`: the persisted JSON array
    // round-trips, deduped and with empty strings dropped.
    func testContractFleetSelectionRoundTrips() {
        XCTAssertEqual(FleetFilter.selection(from: FleetFilter.encode(["local", "nB"])), ["local", "nB"])
        XCTAssertEqual(FleetFilter.selection(from: #"["local","nB","nB",""]"#), ["local", "nB"])
        XCTAssertEqual(FleetFilter.encode([]), "[]")
        XCTAssertEqual(FleetFilter.selection(from: "[]"), [])
    }

    // `BoardViewModel.migrateHideOthers`: `hideOthers=true` with no new key
    // becomes `["local"]`; the legacy key is removed either way; a present new
    // key is left alone.
    func testContractFleetMigratesHideOthers() {
        let d = UserDefaults(suiteName: "FleetContractTests.migrate.\(UUID().uuidString)")!
        d.set(true, forKey: "hideOthers")
        BoardViewModel.migrateHideOthers(d)
        XCTAssertEqual(FleetFilter.selection(from: d.string(forKey: BoardViewModel.fleetNodesKey)), ["local"])
        XCTAssertNil(d.object(forKey: "hideOthers"))

        let e = UserDefaults(suiteName: "FleetContractTests.migrate.\(UUID().uuidString)")!
        e.set(FleetFilter.encode(["nB"]), forKey: BoardViewModel.fleetNodesKey)
        e.set(true, forKey: "hideOthers")
        BoardViewModel.migrateHideOthers(e)
        XCTAssertEqual(FleetFilter.selection(from: e.string(forKey: BoardViewModel.fleetNodesKey)), ["nB"])
        XCTAssertNil(e.object(forKey: "hideOthers"))

        let f = UserDefaults(suiteName: "FleetContractTests.migrate.\(UUID().uuidString)")!
        d.set(false, forKey: "hideOthers")
        BoardViewModel.migrateHideOthers(f)
        XCTAssertNil(f.object(forKey: BoardViewModel.fleetNodesKey))
    }
}
