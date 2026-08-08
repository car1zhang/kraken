import Foundation

// InspectorAPI.swift — the two things the inspectors need that neither
// `BoardStore.mutate` nor the referenced iOS `APIClient` provides.
//
// Everything else goes through one of those two on purpose. `mutate` owns the
// board-refresh-on-both-outcomes policy and the "the server's own sentence
// goes in the status corner" rule, so a write that bypassed it would be a
// write with no refresh and no error surface. `APIClient` already carries the
// drafts, the job form, the create and the multipart upload.
//
// What is left is `POST /api/peers/open`, which is not on either: it is a
// web-only surface (the phone has no Ghostty to raise), and its `focus_error`
// is ADDITIVE on a 2xx rather than an error status — the pane WAS raised and
// only the AppleScript focus failed — so it toasts instead of going down the
// failure path. `mutate` collapses a response to "error sentence or nil" and
// would throw that distinction away.

enum InspectorAPI {
    /// Where this app points its calls. The stored value always wins; the
    /// fallback is loopback rather than the iOS app's tailnet address, because
    /// the mac app runs on the same machine as `kraken serve`.
    static var baseURL: String {
        ServerConfig.baseURL(stored: UserDefaults.standard.string(forKey: "serverURL"))
            .absoluteString
    }

    /// Raise a tmux target's Ghostty window.
    ///
    /// A PANE on the target wins over a slug, and the difference is not
    /// cosmetic: a thread's button raises the THREAD's own window, while a
    /// slug-only post resolves to zeus' pane — a different window, and the one
    /// window a side thread exists in order not to disturb.
    ///
    /// Returns the `focus_error` sentence when the pane was raised but the
    /// focus AppleScript failed, nil when everything worked. Throws only for a
    /// real failure.
    @discardableResult
    static func openTmux(slug: String? = nil, pane: String? = nil) async throws -> String? {
        guard let url = URL(string: baseURL + "/api/peers/open") else {
            throw KrakenError.message("bad server URL")
        }
        var body: [String: String] = [:]
        if let pane { body["pane"] = pane } else if let slug { body["slug"] = slug }

        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)

        let (data, response) = try await URLSession.shared.data(for: request)
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        let parsed = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any]
        guard (200..<300).contains(status) else {
            throw KrakenError.message(
                (parsed?["error"] as? String) ?? "HTTP \(status)"
            )
        }
        let focusError = parsed?["focus_error"] as? String
        return (focusError?.isEmpty ?? true) ? nil : focusError
    }
}
