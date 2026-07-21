import Observation

/// Composer drafts, keyed by a stable identity per composer (e.g.
/// "steer:<slug>", "send:<pane>", "shell:<pane>"). A single instance lives
/// above every sheet and tab (owned by KrakenApp, injected via
/// `.environment(draftStore)`), so it survives dismissing a sheet, switching
/// tabs, or navigating away and back, which is the original ask ("I don't
/// have to go back and forth ... and lose progress").
///
/// Job-scoped keys ("steer:<slug>") additionally sync upstream via
/// POST /api/draft, debounced, so a typed-but-unsent message also survives
/// an app kill or reinstall — carl's "in memory" ask covered same-session
/// continuity, this covers the rest. Pane-scoped keys ("send:<pane>",
/// "shell:<pane>") have no server-side counterpart and stay purely local.
@Observable
final class DraftStore {
    private static let jobKeyPrefix = "steer:"

    var drafts: [String: String] = [:]

    // Debounced upstream-sync tasks, keyed the same as `drafts` — cancel and
    // reschedule on every keystroke so a burst of typing collapses into one
    // postDraft call ~600ms after the last change rather than one per key.
    private var syncTasks: [String: Task<Void, Never>] = [:]

    func get(_ key: String) -> String {
        drafts[key] ?? ""
    }

    func set(_ key: String, _ text: String) {
        // No-op on an unchanged value: hydrate() writes the fetched draft
        // straight into `drafts`, and the composer then mirrors it into its
        // local @State, which fires .onChange -> set with the identical text.
        // Skipping here stops that from re-POSTing the server's own draft
        // back to it (harmless but noisy) while still syncing real edits.
        guard drafts[key] != text else { return }
        drafts[key] = text
        if key.hasPrefix(Self.jobKeyPrefix) {
            scheduleSync(key: key, text: text)
        }
    }

    func clear(_ key: String) {
        drafts.removeValue(forKey: key)
        syncTasks[key]?.cancel()
        syncTasks.removeValue(forKey: key)
    }

    private func scheduleSync(key: String, text: String) {
        let slug = String(key.dropFirst(Self.jobKeyPrefix.count))
        syncTasks[key]?.cancel()
        syncTasks[key] = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(600))
            guard !Task.isCancelled else { return }
            let baseURL = UserDefaults.standard.string(forKey: "serverURL") ?? KrakenDefaults.serverURL
            try? await APIClient.shared.postDraft(baseURL: baseURL, slug: slug, text: text)
            self?.syncTasks.removeValue(forKey: key)
        }
    }

    /// Pulls a job's server-side draft into the local store — but only into
    /// an empty slot, so text the user already typed (or a draft restored by
    /// an earlier hydrate) is never clobbered by a slower-arriving fetch.
    /// Call this once when a job's composer sheet opens.
    func hydrate(slug: String) async {
        let key = "\(Self.jobKeyPrefix)\(slug)"
        guard get(key).isEmpty else { return }
        let baseURL = UserDefaults.standard.string(forKey: "serverURL") ?? KrakenDefaults.serverURL
        guard let resp = try? await APIClient.shared.getDraft(baseURL: baseURL, slug: slug),
              !resp.text.isEmpty else { return }
        // Re-check after the await: the user may have started typing (or a
        // sync from that typing may have started) while this was in flight.
        guard get(key).isEmpty else { return }
        drafts[key] = resp.text
    }
}
