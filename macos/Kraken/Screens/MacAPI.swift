import Foundation

// MacAPI.swift — the two endpoints the mac app needs and the referenced iOS
// `APIClient` does not have, plus the one place a screen asks "which server?".
//
// `/api/account/switch` and `/api/account/mode` are a WEB-ONLY surface: the
// phone never rotates the Mac's Claude login, so `ios/Kraken/APIClient.swift`
// carries no method for either. `ios/` is read-only to this job (50-contract.md,
// "Cross-slice seams"), so they are added here as an extension on the same
// actor instead — one client for the whole app, exactly as on the phone.
//
// The bodies are hand-rolled rather than routed through APIClient's own
// `post`, and that is forced rather than chosen: `post`/`get`/`perform` are
// `private` to APIClient.swift, so an extension declared in a different file
// cannot see them. What IS reproduced from them is the part that matters —
// a non-2xx answer carrying `{"error": …}` becomes a `KrakenError.message`
// with the server's own sentence, since every caller of these two shows that
// sentence verbatim in a toast.

extension APIClient {
    /// Pins the machine's main Claude account to a `~/.claude-pool` slot.
    /// Rotation flips to MANUAL as a side effect, server-side — which is why
    /// the UsagePanel confirms before calling this.
    func switchAccount(baseURL: String, slug: String) async throws {
        try await MacAPI.post(baseURL: baseURL, path: "/api/account/switch", body: ["slug": slug])
    }

    /// `"auto"` releases the manual pin; `"manual"` sets it. The panel only
    /// ever sends `auto` — pinning happens as a side effect of a switch.
    func setRotationMode(baseURL: String, mode: String) async throws {
        try await MacAPI.post(baseURL: baseURL, path: "/api/account/mode", body: ["mode": mode])
    }
}

/// Where a screen gets its base URL, and the minimal POST the two account
/// routes need.
enum MacAPI {
    /// The Settings-stored server, resolved through `ServerConfig` so a
    /// half-typed value falls back to loopback rather than producing a URL
    /// every request fails against.
    ///
    /// Read fresh per call rather than cached: Settings can change it while
    /// the window is open, and a cached copy would keep polling the old host
    /// until relaunch.
    static var baseURL: String {
        ServerConfig.baseURL(stored: UserDefaults.standard.string(forKey: SettingsKey.serverURL))
            .absoluteString
    }

    /// One JSON POST whose 2xx body is ignored. Errors carry the server's own
    /// sentence when it sent one — see this file's header for why the
    /// plumbing is repeated here rather than reused.
    static func post(baseURL: String, path: String, body: [String: String]) async throws {
        let trimmed = baseURL.hasSuffix("/") ? String(baseURL.dropLast()) : baseURL
        guard let url = URL(string: trimmed + path) else {
            throw KrakenError.message("invalid server URL: \(trimmed + path)")
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONEncoder().encode(body)
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await URLSession.shared.data(for: request)
        } catch {
            throw KrakenError.message(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else {
            throw KrakenError.message("no HTTP response from server")
        }
        guard (200...299).contains(http.statusCode) else {
            if let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let msg = obj["error"] as? String, !msg.isEmpty {
                throw KrakenError.message(msg)
            }
            throw KrakenError.message("server returned HTTP \(http.statusCode)")
        }
    }
}
