import Foundation
import SwiftUI

// BoardStore.swift — the board's data seam, declared here (S3) and consumed
// by every other slice as an `@EnvironmentObject` (artifacts/50-contract.md,
// "Cross-slice seams").
//
// The shape is the web board's `useBoard` hook, one for one:
//
//   - a 3s poll of `/api/board`, one request in flight, paused while dragging
//     (the board must not rebuild under the pointer);
//   - `mutate` is PESSIMISTIC — await the write, then refetch on BOTH
//     outcomes, because the server is the truth and a rejected action usually
//     means the board already moved on without us;
//   - the failure's own sentence is RETURNED rather than thrown, so a caller
//     can put it in the status corner without a try/catch at every site.
//
// The one deliberate departure from the referenced `APIClient`: the board's
// GET goes through it (`fetchBoard`), while `mutate` does not. `APIClient`'s
// generic `post` is private and its typed methods are one per endpoint, and
// the drop table names its endpoints as STRINGS (`MoveAction.post(endpoint:)`)
// precisely so a pair that lights up is the pair that posts. Threading those
// strings through a switch onto fifteen typed methods would reintroduce the
// second table `lib/moves.ts` exists to abolish, so the JSON POST is
// hand-rolled below and stays the only networking this file owns.

/// The board payload plus everything the board's own polling and writing need.
///
/// Injected from the root (S6) as an `@EnvironmentObject`; nothing constructs
/// a second one, since two would poll the same server twice and disagree.
@MainActor
final class BoardStore: ObservableObject {
    /// The last decoded `/api/board` payload — nil until the first tick lands.
    @Published var board: WebBoard?

    /// Bumped once per SUCCESSFUL payload. The optimistic-reorder settle has
    /// to say "a payload arrived AFTER my write returned", and `WebBoard` is a
    /// struct with no identity to compare — two decodes of the same bytes are
    /// indistinguishable values. A counter is the identity the type does not
    /// carry.
    @Published private(set) var revision = 0

    /// The last transport/decode failure of the POLL, for the chrome's
    /// unreachable state. Cleared by the next success.
    @Published var pollError: String?

    /// The last MUTATION failure's own sentence — what the web board paints
    /// into `#tick-stat` for ten seconds. Cleared by the next success.
    @Published var lastError: String?

    /// How many consecutive poll ticks have failed. Exposed rather than
    /// logged: one failure is invisible, a run of them is a banner.
    var failureStreak: Int { poller?.failureStreak ?? 0 }

    private var poller: Poller?
    private let session: URLSession

    init() {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 5
        config.timeoutIntervalForResource = 5
        session = URLSession(configuration: config)
    }

    /// Where this process points its calls. Read per request rather than
    /// cached, so editing the Settings field takes effect on the next tick
    /// instead of on the next launch.
    private var baseURL: String {
        let stored = UserDefaults.standard.string(forKey: SettingsKey.serverURL)
        return ServerConfig.baseURL(stored: stored).absoluteString
    }

    // MARK: - polling

    /// Begins the 3s poll. Idempotent — `Poller.start()` refuses to stack a
    /// second loop, so a screen that starts this in `onAppear` cannot double
    /// it by appearing twice. The first fetch is immediate.
    func start() {
        if poller == nil {
            poller = Poller(interval: 3) { [weak self] in
                try await self?.load()
            }
        }
        poller?.start()
    }

    /// Suspends the poll for the duration of a drag. The board rebuilding
    /// under the pointer is the failure this prevents: the card being dragged
    /// would be re-created from a new payload mid-gesture.
    func pausePolling() {
        poller?.pause()
    }

    /// Resumes from an immediate fire, exactly like `start()`.
    func resumePolling() {
        poller?.resume()
    }

    /// One out-of-band fetch, without disturbing the tick schedule. Failures
    /// are swallowed into `pollError` — a refetch that fails is not a reason
    /// to throw at a caller who asked for a repaint.
    func refetch() async {
        do {
            try await load()
        } catch {
            pollError = Self.sentence(error)
        }
    }

    private func load() async throws {
        let payload = try await APIClient.shared.fetchBoard(baseURL: baseURL)
        board = payload
        revision &+= 1
        pollError = nil
    }

    // MARK: - writing

    /// POSTs `body` to `path`, then refetches — on success AND on failure.
    ///
    /// Returns the server's own error sentence when the write failed, nil when
    /// it succeeded. The server's sentence is worth showing verbatim ("job
    /// isn't queued", "cell already in flight"); a generic failure string is
    /// not, which is why `KrakenError.message` is unwrapped rather than
    /// `localizedDescription`-ed at the call site.
    @discardableResult
    func mutate(path: String, body: [String: Any]) async -> String? {
        let failure = await post(path: path, body: body)
        // Both outcomes. The server is the truth either way.
        await refetch()
        lastError = failure
        return failure
    }

    /// The same write WITHOUT the refetch — the one caller is the optimistic
    /// same-column reorder, where refetching on success is precisely what must
    /// not happen: the board already shows the final order and a repaint of
    /// identical content is the flash the whole optimistic path exists to
    /// avoid. Every other caller wants `mutate`.
    @discardableResult
    func post(path: String, body: [String: Any]) async -> String? {
        var failure: String?
        do {
            try await postJSON(path: path, body: body)
        } catch {
            failure = Self.sentence(error)
        }
        lastError = failure
        return failure
    }

    /// The one JSON POST. `[String: Any]` rather than a typed body because the
    /// caller is the drop table, which knows an endpoint string and a slug —
    /// see this file's header.
    private func postJSON(path: String, body: [String: Any]) async throws {
        var base = baseURL
        if base.hasSuffix("/") { base.removeLast() }
        guard let url = URL(string: base + path) else {
            throw KrakenError.message("invalid server URL: \(base + path)")
        }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: body)

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw KrakenError.message(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else {
            throw KrakenError.message("no HTTP response from server")
        }
        guard (200...299).contains(http.statusCode) else {
            // The server's refusals carry their own prose; surface it rather
            // than the status code whenever there is one.
            if let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let message = object["error"] as? String, !message.isEmpty {
                throw KrakenError.message(message)
            }
            throw KrakenError.message("server returned HTTP \(http.statusCode)")
        }
    }

    /// One sentence out of any error. `KrakenError` already IS a sentence
    /// (the referenced `APIClient` turns a non-2xx `{"error": …}` body into
    /// one), so it is unwrapped rather than described.
    private static func sentence(_ error: Error) -> String {
        if let kraken = error as? KrakenError {
            return kraken.errorDescription ?? String(describing: kraken)
        }
        return error.localizedDescription
    }
}
