import Foundation

/// Where the app points its HTTP calls.
///
/// The mac app runs on the SAME machine as `kraken serve`, so its fallback is
/// loopback — deliberately not the iOS app's tailnet address
/// (`KrakenDefaults.serverURL` over in `ios/Kraken/APIClient.swift`), which
/// exists because a phone has to cross the network to reach this Mac.
/// A stored value always wins; the fallback is only for "nothing usable is
/// stored". Nothing in this app writes the key (`defaults write
/// dev.carl.KrakenMac serverURL …` is the override path), and the failure
/// notice offers the way back to the default.
enum ServerConfig {
    /// `http://127.0.0.1:7777` — the address `kraken serve` binds by default
    /// (`jobs.ServeBaseURL`).
    static let fallbackURL = URL(string: "http://127.0.0.1:7777")!

    /// Resolves the stored server string to a usable base URL.
    ///
    /// "Usable" is stricter than `URL(string:)` succeeding: that initializer
    /// accepts a bare `127.0.0.1:7777` by reading `127.0.0.1` as the SCHEME,
    /// producing a hostless URL that every request built on it fails against.
    /// So a stored value only wins when it carries both a scheme and a host.
    static func baseURL(stored: String?) -> URL {
        guard let raw = stored?.trimmingCharacters(in: .whitespacesAndNewlines),
              !raw.isEmpty,
              let url = URL(string: raw),
              let scheme = url.scheme, !scheme.isEmpty,
              let host = url.host(), !host.isEmpty
        else { return fallbackURL }
        return url
    }
}

/// The owner key `kraken serve` writes to `~/.kraken/owner-key`
/// (internal/jobs/ownerkey.go). The server's request gate treats a local
/// caller as the owner only when it carries this key, so the board's web view
/// gets it as the `kraken_owner` cookie before its first load. This app is not
/// sandboxed, so the home directory is the user's real one.
enum OwnerKey {
    static let cookieName = "kraken_owner"

    /// The key, or nil when the file is missing or empty (serve not started
    /// yet): the board then shows its own "no owner key" line.
    static func read(home: URL = FileManager.default.homeDirectoryForCurrentUser) -> String? {
        let file = home.appendingPathComponent(".kraken/owner-key")
        guard let raw = try? String(contentsOf: file, encoding: .utf8) else { return nil }
        let key = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        return key.isEmpty ? nil : key
    }

    /// The cookie for the server at `url`: its host, path `/`, HttpOnly and
    /// SameSite=Strict, the same attributes `/api/owner` sets in a browser.
    static func cookie(key: String, for url: URL) -> HTTPCookie? {
        guard let host = url.host(), !host.isEmpty else { return nil }
        return HTTPCookie(properties: [
            .name: cookieName,
            .value: key,
            .domain: host,
            .path: "/",
            .sameSitePolicy: HTTPCookieStringPolicy.sameSiteStrict,
            HTTPCookiePropertyKey("HttpOnly"): "TRUE",
        ])
    }
}
