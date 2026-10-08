import Foundation
import Observation

/// Deep-link routing (`kraken://...`), kept thin on purpose: it only parses
/// a URL into "where to go" intent and stamps a handful of pending targets.
/// The views that own that destination (BoardView, SessionsView,
/// SettingsView) observe this object, act on the relevant pending value, and
/// clear it — the router itself never reaches into another view's state.
///
/// Supported URLs:
///   kraken://board                 → Board tab
///   kraken://board/<column>        → Board tab, jump pager to that column
///   kraken://overview              → Board tab, open the overview sheet
///   kraken://dream                 → Board tab, open the DREAM sheet
///                                     (the nightly loop's reports + watchlist)
///   kraken://graph                 → the MEMORY tab (the saved-artifact
///                                     library and the mental model). The
///                                     host stays `graph` and the case stays
///                                     `.graph`, names kept from when the tab
///                                     hosted the knowledge graph, so links in
///                                     shipped builds keep working.
///   kraken://job/<slug>            → Board tab, open that job's detail
///                                     (waits for the board to load if needed)
///   kraken://job/<slug>/answer     → …and open the answer sheet
///   kraken://job/<slug>/steer      → …and open the steer sheet
///   kraken://job/<slug>/artifact/<name> → …and open that artifact
///   kraken://sessions              → Sessions tab
///   kraken://session/<pane>        → Sessions tab, open that peer's detail
///                                     ("3" → pane "%3"; the % is implicit
///                                     so the scheme stays URL-safe)
///   kraken://usage                 → Board tab, open the USAGE drawer
///   kraken://log                   → Settings tab, open the LOG drawer
///   kraken://settings              → Settings tab
@Observable
final class AppRouter {
    enum Tab: Hashable {
        case board, sessions, graph, settings
    }

    var selectedTab: Tab = .board

    /// A follow-on action inside a deep-linked job detail.
    enum JobAction: Equatable {
        case answer, steer
        case artifact(name: String)
    }

    // Consumed (read + reset to nil/false) by the owning view once acted on.
    var pendingBoardColumn: BoardColumn?
    var pendingOverview = false
    // The DREAM screen is a sheet over the Board (not a tab — see DreamView),
    // so it routes exactly the way the overview sheet does.
    var pendingDream = false
    // USAGE and LOG are drawers (sheets) rather than tabs since the nav
    // restructure: Usage hangs off the Board's toolbar, Log off a Settings
    // row. Each deep link therefore selects the tab that OWNS the drawer and
    // stamps a flag that view consumes — the same shape as pendingDream.
    var pendingUsage = false
    var pendingLog = false
    var pendingJobSlug: String?
    var pendingJobAction: JobAction?
    var pendingSessionPane: String?
    // Set by JobDetailView's Cell row: resolves to the first live peer in
    // that tmux session once SessionsView's peer list is loaded.
    var pendingSessionCell: String?

    func handle(url: URL) {
        guard url.scheme?.lowercased() == "kraken" else { return }
        let host = (url.host ?? "").lowercased()
        let components = url.pathComponents.filter { $0 != "/" }

        switch host {
        case "board":
            selectedTab = .board
            if let raw = components.first, let column = BoardColumn(rawValue: raw) {
                pendingBoardColumn = column
            }
        case "overview":
            selectedTab = .board
            pendingOverview = true
        case "dream":
            selectedTab = .board
            pendingDream = true
        case "graph":
            selectedTab = .graph
        case "job":
            selectedTab = .board
            if let slug = components.first, !slug.isEmpty {
                pendingJobSlug = slug
                switch components.dropFirst().first {
                case "answer":
                    pendingJobAction = .answer
                case "steer":
                    pendingJobAction = .steer
                case "artifact":
                    if let name = components.dropFirst(2).first, !name.isEmpty {
                        pendingJobAction = .artifact(name: name)
                    }
                default:
                    pendingJobAction = nil
                }
            }
        case "sessions":
            selectedTab = .sessions
        case "session":
            selectedTab = .sessions
            if let pane = components.first, !pane.isEmpty {
                pendingSessionPane = "%" + pane
            }
        case "usage":
            selectedTab = .board
            pendingUsage = true
        case "log":
            selectedTab = .settings
            pendingLog = true
        case "settings":
            selectedTab = .settings
        default:
            break
        }
    }
}
