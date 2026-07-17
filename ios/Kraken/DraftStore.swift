import Observation

/// In-memory-only composer drafts, keyed by a stable identity per composer
/// (e.g. "answer:<escalation-file>", "steer:<slug>", "send:<pane>",
/// "shell:<pane>"). Deliberately not persisted to disk — carl asked for "in
/// memory," and drafts clearing on app kill is expected/fine — but a single
/// instance lives above every sheet and tab (owned by KrakenApp, injected
/// via `.environment(draftStore)`), so it survives dismissing a sheet,
/// switching tabs, or navigating away and back, which is the actual ask
/// ("I don't have to go back and forth ... and lose progress").
@Observable
final class DraftStore {
    var drafts: [String: String] = [:]

    func get(_ key: String) -> String {
        drafts[key] ?? ""
    }

    func set(_ key: String, _ text: String) {
        drafts[key] = text
    }

    func clear(_ key: String) {
        drafts.removeValue(forKey: key)
    }
}
