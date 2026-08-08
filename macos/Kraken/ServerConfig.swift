import Foundation

/// Where the app points its HTTP calls.
///
/// The mac app runs on the SAME machine as `kraken serve`, so its fallback is
/// loopback — deliberately not the iOS app's tailnet address
/// (`KrakenDefaults.serverURL` in the referenced `ios/Kraken/APIClient.swift`),
/// which exists because a phone has to cross the network to reach this Mac.
/// A stored value always wins; the fallback is only for "nothing usable is
/// stored", which includes a value the user half-typed into Settings.
enum ServerConfig {
    /// `http://127.0.0.1:7777` — the address `kraken serve` binds by default
    /// (`jobs.ServeBaseURL`).
    static let fallbackURL = URL(string: "http://127.0.0.1:7777")!

    /// Resolves the Settings-stored server string to a usable base URL.
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
