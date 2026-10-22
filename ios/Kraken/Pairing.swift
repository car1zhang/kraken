// Protocol parity: this file is part of the MULTIPLAYER protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

import Foundation
import Observation
import Security
import CommonCrypto
import CryptoKit
import WebKit

// Phone pairing (multiplayer kraken, apiContract 7).
//
// The server gates every board route behind a pairing token, so this file is
// the phone's whole half of that protocol: the Keychain-held record of which
// Mac it is paired to, the PBKDF2/HMAC math that proves the pairing password
// to a node without ever sending it, the seed-then-fan-out flow that finds the
// one node whose password matches, and the tailnet probe the pair screen uses
// to say whether the phone can reach any node at all. ios/API.md's "Nodes and
// pairing" section is the wire; 53-security-model.md of job 608 is the why.

// MARK: - the record

/// What a successful pairing leaves behind: the bearer token (shown by the
/// node exactly once), which node issued it, that node's human label, and the
/// base URL the phone talks to from now on. Stored ONLY in the Keychain —
/// never `UserDefaults`/`@AppStorage` — because the token is a credential.
struct PairingRecord: Codable, Equatable {
    let token: String
    let nodeID: String
    let label: String
    let url: String
}

/// The storage seam under `PairingStore`. The default is the Keychain; tests
/// swap an in-memory one in.
protocol PairingStoreBackend {
    func read() -> Data?
    func write(_ data: Data) throws
    func delete()
}

/// One Keychain generic-password item holding the JSON-encoded record.
/// `kSecAttrAccessibleAfterFirstUnlock` because the background refresh task
/// needs the token after a reboot once the phone has been unlocked once, and
/// must not need it before.
struct KeychainPairingBackend: PairingStoreBackend {
    static let service = "dev.carl.Kraken.pairing"
    static let account = "pairing-record"

    private var baseQuery: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: Self.service,
            kSecAttrAccount as String: Self.account,
        ]
    }

    func read() -> Data? {
        var query = baseQuery
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &out) == errSecSuccess else { return nil }
        return out as? Data
    }

    func write(_ data: Data) throws {
        SecItemDelete(baseQuery as CFDictionary)
        var attrs = baseQuery
        attrs[kSecValueData as String] = data
        attrs[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        let status = SecItemAdd(attrs as CFDictionary, nil)
        guard status == errSecSuccess else {
            // The OSStatus only — never the data, which carries the token.
            throw KrakenError.message("couldn't save the pairing to the Keychain (status \(status))")
        }
    }

    func delete() {
        SecItemDelete(baseQuery as CFDictionary)
    }
}

enum PairingStore {
    static var backend: PairingStoreBackend = KeychainPairingBackend()

    /// The stored record, or nil when none is stored OR what is stored does
    /// not decode — a corrupt item reads as "not paired", which sends the
    /// phone back to the pair screen rather than wedging every request.
    static func load() -> PairingRecord? {
        guard let data = backend.read() else { return nil }
        return try? JSONDecoder().decode(PairingRecord.self, from: data)
    }

    static func save(_ r: PairingRecord) throws {
        try backend.write(JSONEncoder().encode(r))
    }

    static func clear() {
        backend.delete()
    }
}

// MARK: - the math

enum PairingMath {
    /// PBKDF2-HMAC-SHA256, 32 bytes out — byte-for-byte the server's
    /// `deriveVerifier` (Go `crypto/pbkdf2` over the password's UTF-8 bytes).
    /// An iteration count below 1 is clamped to 1: CommonCrypto refuses zero
    /// rounds outright, and a server never serves one.
    static func deriveKey(password: String, salt: Data, iterations: Int) -> Data {
        let pw = Array(password.utf8)
        let saltBytes = [UInt8](salt)
        var out = [UInt8](repeating: 0, count: 32)
        let rounds = UInt32(clamping: max(1, iterations))
        // Non-empty backing arrays for the pointers: an empty Swift array may
        // hand CommonCrypto a nil base address, and the lengths below are
        // what it actually reads.
        let pwBuf = pw.isEmpty ? [UInt8(0)] : pw
        let saltBuf = saltBytes.isEmpty ? [UInt8(0)] : saltBytes
        let status = pwBuf.withUnsafeBufferPointer { pwPtr in
            saltBuf.withUnsafeBufferPointer { saltPtr in
                pwPtr.baseAddress!.withMemoryRebound(to: CChar.self, capacity: pwBuf.count) { pwChars in
                    CCKeyDerivationPBKDF(
                        CCPBKDFAlgorithm(kCCPBKDF2),
                        pwChars, pw.count,
                        saltPtr.baseAddress!, saltBytes.count,
                        CCPseudoRandomAlgorithm(kCCPRFHmacAlgSHA256),
                        rounds,
                        &out, out.count
                    )
                }
            }
        }
        guard status == kCCSuccess else { return Data() }
        return Data(out)
    }

    /// HMAC-SHA256(key, nonce ‖ nodeID ‖ deviceLabel) — raw concatenation,
    /// no separators, the server's `pairProof`.
    static func proof(key: Data, nonce: Data, nodeID: String, deviceLabel: String) -> Data {
        var message = Data()
        message.append(nonce)
        message.append(Data(nodeID.utf8))
        message.append(Data(deviceLabel.utf8))
        let mac = HMAC<SHA256>.authenticationCode(for: message, using: SymmetricKey(data: key))
        return Data(mac)
    }
}

/// base64url without padding — Go's `base64.RawURLEncoding`, the encoding of
/// every binary value on the pairing wire (salt, nonce, proof, token).
enum Base64URL {
    static func encode(_ d: Data) -> String {
        d.base64EncodedString()
            .replacingOccurrences(of: "+", with: "-")
            .replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "=", with: "")
    }

    /// nil for anything Go's RawURLEncoding would refuse: padding, the
    /// standard alphabet's `+`/`/`, any other character, or a length that
    /// leaves a lone sextet. Go's decoder skips `\r`/`\n`, so this does too.
    static func decode(_ s: String) -> Data? {
        let stripped = s.filter { $0 != "\r" && $0 != "\n" }
        let allowed = Set("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_")
        guard stripped.allSatisfy({ allowed.contains($0) }) else { return nil }
        let remainder = stripped.count % 4
        guard remainder != 1 else { return nil }
        var standard = stripped
            .replacingOccurrences(of: "-", with: "+")
            .replacingOccurrences(of: "_", with: "/")
        if remainder > 0 { standard += String(repeating: "=", count: 4 - remainder) }
        return Data(base64Encoded: standard)
    }
}

// MARK: - tailnet awareness

enum TailnetDetector {
    /// True for a dotted-quad IPv4 address inside 100.64.0.0/10 (Tailscale's
    /// CGNAT range: first octet 100, second 64…127). Anything else — IPv6,
    /// a hostname, a malformed quad, an octet with a leading zero (which Go's
    /// net.ParseIP refuses) — is false.
    static func isTailnetAddress(_ ip: String) -> Bool {
        let parts = ip.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 4 else { return false }
        var octets: [Int] = []
        for part in parts {
            guard !part.isEmpty, part.count <= 3,
                  part.allSatisfy({ $0.isASCII && $0.isNumber }),
                  !(part.count > 1 && part.first == "0"),
                  let value = Int(part), value <= 255 else { return false }
            octets.append(value)
        }
        return octets[0] == 100 && (64...127).contains(octets[1])
    }

    static func isOnTailnet(addresses: [String]) -> Bool {
        addresses.contains(where: isTailnetAddress)
    }

    /// Every IPv4/IPv6 address on this device's interfaces, numeric form.
    /// Tailscale's iOS VPN shows up as a utun interface carrying the 100.x
    /// address, which is what `isOnTailnet` looks for.
    static func currentAddresses() -> [String] {
        var head: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&head) == 0, let first = head else { return [] }
        defer { freeifaddrs(head) }
        var result: [String] = []
        var cursor: UnsafeMutablePointer<ifaddrs>? = first
        while let ifa = cursor {
            defer { cursor = ifa.pointee.ifa_next }
            guard let addr = ifa.pointee.ifa_addr else { continue }
            let family = Int32(addr.pointee.sa_family)
            guard family == AF_INET || family == AF_INET6 else { continue }
            let len = socklen_t(family == AF_INET ? MemoryLayout<sockaddr_in>.size : MemoryLayout<sockaddr_in6>.size)
            var host = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            if getnameinfo(addr, len, &host, socklen_t(host.count), nil, 0, NI_NUMERICHOST) == 0 {
                result.append(String(cString: host))
            }
        }
        return result
    }
}

// MARK: - the pair flow

/// `GET /api/pair/challenge`.
struct PairChallenge: Decodable {
    let nodeID: String
    let label: String
    let salt: String
    let iters: Int
    let nonce: String

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case label, salt, iters, nonce
    }
}

/// `POST /api/pair`'s 200 body. The token appears here once and nowhere else.
struct PairResult: Decodable {
    let token: String
    let nodeID: String
    let label: String

    enum CodingKeys: String, CodingKey {
        case token
        case nodeID = "node_id"
        case label
    }
}

/// The network seam under `PairFlow` — a URLSession-backed one for the app
/// (`URLSessionPairClient`), fakes for tests.
protocol PairClient {
    func nodeURLs(seed: URL) async throws -> [URL]
    func challenge(at node: URL) async throws -> PairChallenge
    /// nil when the node answers 401 (the proof did not match its verifier).
    func attempt(at node: URL, nonce: String, proof: String, deviceLabel: String) async throws -> PairResult?
}

enum PairFlowError: Error, Equatable, LocalizedError {
    case noMatch
    case multipleMatches([String])

    var errorDescription: String? {
        switch self {
        case .noMatch:
            return "No kraken on this tailnet has that password."
        case .multipleMatches(let labels):
            return "Two Macs share this password (\(labels.joined(separator: ", "))) — change one of them on its board."
        }
    }
}

enum PairFlow {
    /// Seed-then-fan-out: every node the seed knows (plus the seed) gets its
    /// own challenge and its own proof, and the phone binds to the single node
    /// that accepts. A node whose challenge or attempt fails outright (no
    /// password set, rate limited, unreachable, a salt that does not decode)
    /// is simply not a match — one dead Mac on the tailnet must not stop the
    /// phone pairing with the live one. Nothing is stored here: the caller
    /// saves the returned record, so a failure leaves no trace.
    static func pair(seed: URL, password: String, deviceLabel: String, client: PairClient) async throws -> PairingRecord {
        var seen = Set<String>()
        let urls = try await client.nodeURLs(seed: seed).filter { seen.insert($0.absoluteString).inserted }

        let results: [(Int, PairResult)] = await withTaskGroup(of: (Int, PairResult?).self) { group in
            for (index, url) in urls.enumerated() {
                group.addTask {
                    (index, await tryNode(url, password: password, deviceLabel: deviceLabel, client: client))
                }
            }
            var matched: [(Int, PairResult)] = []
            for await (index, result) in group {
                if let result { matched.append((index, result)) }
            }
            return matched.sorted { $0.0 < $1.0 }
        }

        switch results.count {
        case 0:
            throw PairFlowError.noMatch
        case 1:
            let (index, result) = results[0]
            return PairingRecord(token: result.token, nodeID: result.nodeID, label: result.label,
                                 url: urls[index].absoluteString)
        default:
            throw PairFlowError.multipleMatches(results.map { $0.1.label })
        }
    }

    private static func tryNode(_ url: URL, password: String, deviceLabel: String, client: PairClient) async -> PairResult? {
        do {
            let ch = try await client.challenge(at: url)
            guard let salt = Base64URL.decode(ch.salt), let nonce = Base64URL.decode(ch.nonce) else { return nil }
            let key = PairingMath.deriveKey(password: password, salt: salt, iterations: ch.iters)
            let proof = PairingMath.proof(key: key, nonce: nonce, nodeID: ch.nodeID, deviceLabel: deviceLabel)
            return try await client.attempt(at: url, nonce: ch.nonce, proof: Base64URL.encode(proof), deviceLabel: deviceLabel)
        } catch {
            return nil
        }
    }
}

/// The app's PairClient: plain unauthenticated URLSession calls (a pairing
/// phone has no token to send, and must not send a stale one to a node that
/// did not issue it).
struct URLSessionPairClient: PairClient {
    private let session: URLSession

    init() {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 8
        config.timeoutIntervalForResource = 15
        config.httpCookieAcceptPolicy = .never
        config.httpShouldSetCookies = false
        session = URLSession(configuration: config)
    }

    /// `/api/nodes`, decoded element by element: a malformed entry is dropped
    /// and its neighbours kept, so one drifted row cannot collapse the
    /// fan-out to the seed alone.
    private struct NodesBody: Decodable {
        struct Node: Decodable {
            let ip: String
            let port: Int
        }
        private struct Skip: Decodable { init(from decoder: Decoder) throws {} }
        private enum CodingKeys: String, CodingKey { case nodes }

        let nodes: [Node]

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            guard var list = try? c.nestedUnkeyedContainer(forKey: .nodes) else {
                nodes = []
                return
            }
            var out: [Node] = []
            while !list.isAtEnd {
                if (try? list.decodeNil()) == true { continue }
                if let node = try? list.decode(Node.self) {
                    out.append(node)
                } else if (try? list.decode(Skip.self)) == nil {
                    break
                }
            }
            nodes = out
        }
    }

    private struct AttemptBody: Encodable {
        let nonce: String
        let proof: String
        let deviceLabel: String

        enum CodingKeys: String, CodingKey {
            case nonce, proof
            case deviceLabel = "device_label"
        }
    }

    private struct ErrorBody: Decodable { let error: String? }

    private func endpoint(_ base: URL, _ path: String) -> URL {
        var s = base.absoluteString
        while s.hasSuffix("/") { s.removeLast() }
        return URL(string: s + path) ?? base.appendingPathComponent(path)
    }

    /// The seed plus every registered node as `http://<ip>:<port>`. The
    /// registry read degrades to the seed alone on ANY failure — an older
    /// server, a gated route, an offline seed's registry — since the seed is
    /// still worth a challenge of its own.
    func nodeURLs(seed: URL) async throws -> [URL] {
        var urls = [seed]
        if let (data, response) = try? await session.data(from: endpoint(seed, "/api/nodes")),
           (response as? HTTPURLResponse)?.statusCode == 200,
           let body = try? JSONDecoder().decode(NodesBody.self, from: data) {
            for node in body.nodes where !node.ip.isEmpty && node.port > 0 {
                if let url = URL(string: "http://\(node.ip):\(node.port)") { urls.append(url) }
            }
        }
        var seen = Set<String>()
        return urls.filter { seen.insert($0.absoluteString).inserted }
    }

    func challenge(at node: URL) async throws -> PairChallenge {
        let (data, response) = try await session.data(from: endpoint(node, "/api/pair/challenge"))
        try Self.check(response, data)
        return try JSONDecoder().decode(PairChallenge.self, from: data)
    }

    func attempt(at node: URL, nonce: String, proof: String, deviceLabel: String) async throws -> PairResult? {
        var request = URLRequest(url: endpoint(node, "/api/pair"))
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONEncoder().encode(AttemptBody(nonce: nonce, proof: proof, deviceLabel: deviceLabel))
        let (data, response) = try await session.data(for: request)
        if (response as? HTTPURLResponse)?.statusCode == 401 { return nil }
        try Self.check(response, data)
        return try JSONDecoder().decode(PairResult.self, from: data)
    }

    private static func check(_ response: URLResponse, _ data: Data) throws {
        guard let http = response as? HTTPURLResponse else {
            throw KrakenError.message("no HTTP response from node")
        }
        guard (200...299).contains(http.statusCode) else {
            if let body = try? JSONDecoder().decode(ErrorBody.self, from: data), let msg = body.error, !msg.isEmpty {
                throw KrakenError.message(msg)
            }
            throw KrakenError.message("node returned HTTP \(http.statusCode)")
        }
    }
}

// MARK: - the live pairing state

/// The app's observable view of the stored record — what the board's gate and
/// Settings' Server card read, so pairing and unpairing re-render them. The
/// Keychain (`PairingStore`) stays the only store; this mirrors it and owns
/// the side effects that go with a change: the `kraken_pair` cookie in both
/// cookie jars (URLSession's for AsyncImage, WebKit's for artifact pages).
@Observable
final class PairingSession {
    static let shared = PairingSession()

    private(set) var record: PairingRecord?

    private init() {
        record = PairingStore.load()
        if let record { Self.installCookies(record) }
    }

    var isPaired: Bool { record != nil }

    /// Stores a fresh pairing and points the app's server URL at the node
    /// that issued it — every later request goes there and nowhere else.
    func adopt(_ r: PairingRecord) throws {
        try PairingStore.save(r)
        UserDefaults.standard.set(r.url, forKey: "serverURL")
        record = r
        Self.installCookies(r)
    }

    /// Forgets the pairing locally: Keychain item and both cookies. The
    /// server-side revoke (Log out) is the caller's business; a 401 means the
    /// server already forgot it.
    func clear() {
        let old = record ?? PairingStore.load()
        PairingStore.clear()
        record = nil
        if let old {
            APIClient.clearCookie(for: old.url, in: .shared)
            Task { @MainActor in
                let store = WKWebsiteDataStore.default().httpCookieStore
                let cookies = await store.allCookies()
                let host = URL(string: old.url)?.host?.lowercased()
                for c in cookies where c.name == APIClient.pairCookieName
                    && c.domain.trimmingCharacters(in: CharacterSet(charactersIn: ".")).lowercased() == host {
                    await store.deleteCookie(c)
                }
            }
        }
    }

    private static func installCookies(_ r: PairingRecord) {
        APIClient.installCookie(record: r, into: .shared)
        guard let cookie = APIClient.pairCookie(for: r) else { return }
        Task { @MainActor in
            await WKWebsiteDataStore.default().httpCookieStore.setCookie(cookie)
        }
    }
}
