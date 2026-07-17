import SwiftUI

/// The six kanban columns, mirroring web/app.js's COLUMNS table.
enum BoardColumn: String, CaseIterable, Identifiable, Hashable {
    case draft, queued, working, blocked, review, done

    var id: String { rawValue }

    var title: String {
        switch self {
        case .draft: return "DRAFT"
        case .queued: return "QUEUED"
        case .working: return "WORKING"
        case .blocked: return "BLOCKED"
        case .review: return "REVIEW"
        case .done: return "DONE"
        }
    }
}

enum BoardTheme {
    private static let columnStates: [BoardColumn: [String]] = [
        .draft: ["draft", "error"],
        .queued: ["ready", "queued", "gate-review"],
        .working: ["planning", "executing"],
        .blocked: ["blocked", "plan-review"],
        .review: ["testing", "pr-open"],
        .done: ["done", "failed", "abandoned"],
    ]

    /// Which column a job belongs on. Mirrors app.js's render(): a job with
    /// a non-empty `health` always renders under BLOCKED regardless of its
    /// file state — the daemon isn't going to fix it, only a retry will.
    static func column(for job: WebJob) -> BoardColumn {
        if let health = job.health, !health.isEmpty {
            return .blocked
        }
        for (column, states) in columnStates where states.contains(job.state) {
            return column
        }
        if job.state.hasPrefix("waits:") || job.state.hasPrefix("gated:") {
            return .queued
        }
        return .draft
    }

    /// Mirrors app.js's STATE_COLOR / stateColor().
    static func color(forState state: String) -> Color {
        if state.hasPrefix("waits:") || state.hasPrefix("gated:") {
            return .purple
        }
        switch state {
        case "executing", "planning":
            return .green
        case "plan-review", "blocked":
            return .orange
        case "ready", "queued":
            return .accentColor
        case "gate-review":
            return .orange
        case "testing", "pr-open":
            return .purple
        case "done", "draft", "abandoned":
            return .secondary
        case "error", "failed":
            return .red
        default:
            return .secondary
        }
    }
}

enum SessionTheme {
    /// running=green, waiting=cyan, question=orange, blocked/error=red,
    /// shell=gray — matching ls.go's palette intent for the Sessions tab.
    static func color(forStatus status: String, role: String) -> Color {
        if role == "shell" {
            return .gray
        }
        switch status {
        case "running":
            return .green
        case "waiting":
            return .cyan
        case "question":
            return .orange
        case "blocked", "error":
            return .red
        default:
            return .gray
        }
    }
}
