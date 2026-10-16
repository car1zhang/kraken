import Foundation
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
/// an app kill or reinstall — the original "in memory" ask covered same-session
/// continuity, this covers the rest. Pane-scoped keys ("send:<pane>",
/// "shell:<pane>") have no server-side counterpart and stay purely local.
/// Each debounced sync re-checks the draft's identity right before it POSTs
/// (see scheduleSync) so a send's clear() — which cancels the task, but
/// can't stop one already past that cancellation check — always wins over a
/// stale write instead of racing it back onto the server.
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

    /// Whether `text` is still the live value stored at `key` — the wake-time
    /// guard a debounced sync re-checks right before it POSTs (see
    /// scheduleSync). Pulled out as its own pure, internal-visibility check
    /// so the race it closes (a send's clear() landing after a sleeping
    /// task's cancellation check but before it POSTs) is directly testable
    /// without depending on the 600ms task timing.
    func isCurrent(key: String, text: String) -> Bool {
        drafts[key] == text
    }

    private func scheduleSync(key: String, text: String) {
        let slug = String(key.dropFirst(Self.jobKeyPrefix.count))
        syncTasks[key]?.cancel()
        syncTasks[key] = Task { [weak self] in
            try? await Task.sleep(for: .milliseconds(600))
            guard !Task.isCancelled else { return }
            // Wake-time identity re-check: cancellation only stops a task
            // that hasn't yet passed the check above. A task already past it
            // when clear() (or another edit) landed would otherwise still
            // POST this now-stale text over whatever won since — isCurrent
            // re-reads the live value and this bails unless it's unchanged.
            guard let self, self.isCurrent(key: key, text: text) else { return }
            let baseURL = KrakenDefaults.storedServerURL
            // No server set: the draft stays local (it is still in `drafts`),
            // and nothing is sent anywhere.
            guard KrakenDefaults.isConfigured(baseURL) else { return }
            try? await APIClient.shared.postDraft(baseURL: baseURL, slug: slug, text: text)
            self.syncTasks.removeValue(forKey: key)
        }
    }

    /// Pulls a job's server-side draft into the local store — but only into
    /// an empty slot, so text the user already typed (or a draft restored by
    /// an earlier hydrate) is never clobbered by a slower-arriving fetch.
    /// Call this once when a job's composer sheet opens.
    func hydrate(slug: String) async {
        let key = "\(Self.jobKeyPrefix)\(slug)"
        guard get(key).isEmpty else { return }
        let baseURL = KrakenDefaults.storedServerURL
        guard KrakenDefaults.isConfigured(baseURL) else { return }
        guard let resp = try? await APIClient.shared.getDraft(baseURL: baseURL, slug: slug),
              !resp.text.isEmpty else { return }
        // Re-check after the await: the user may have started typing (or a
        // sync from that typing may have started) while this was in flight.
        guard get(key).isEmpty else { return }
        drafts[key] = resp.text
    }
}
