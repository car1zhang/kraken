import AppKit
import Foundation
import UserNotifications

// NotificationEngine.swift — local job alerts, diffed off the board poll.
//
// The diff is ported from `ios/Kraken/NotificationManager.swift` MINUS its
// whole background half: `BGTaskScheduler` is iOS-only, and on a Mac it has
// nothing to do anyway — the window's 3s board poll runs for as long as the
// app is open, and an app that is not open is not one anybody is waiting on a
// banner from.
//
// Three conditions, each a SET of slugs currently in it; a job newly ENTERING
// a set fires one notification, and a job that drops out is implicitly removed
// so a later re-entry fires again rather than staying silently suppressed
// forever. Reaching `done` is deliberately not one of the three: it is a
// manual, in-app gate and never pages.
//
// TWO DELIBERATE DIVERGENCES from the iOS original, both because 50-contract.md
// says so and the contract wins:
//
//   - the ERRORING set considers every non-archived job REGARDLESS of column
//     (amendment 11). iOS additionally excluded `done`, on the grounds that a
//     done job can carry a stale unrelated error. The contract's rule is the
//     one the blind tests are written against.
//   - the order of the returned array is UNSPECIFIED (amendment 11 again), so
//     nothing here sorts it.

/// One job as the diff sees it: everything the three conditions are decided
/// from and nothing else.
struct JobSnapshot {
    let slug: String
    let column: BoardColumn
    let erroring: Bool
}

/// One notification's worth of change.
enum JobTransition: Equatable {
    case blocked(String)
    case started(String)
    case erroring(String)
}

/// The three "currently in this condition" sets, as they are persisted.
///
/// `Codable` is the whole point of the type: it IS the persistence shape, and
/// a round trip through it must be the identity so a relaunch resumes the diff
/// where it left off instead of re-firing every currently-blocked job.
struct NotifiedSets: Codable, Equatable {
    var blocked: Set<String>
    var working: Set<String>
    var erroring: Set<String>

    init(blocked: Set<String> = [], working: Set<String> = [], erroring: Set<String> = []) {
        self.blocked = blocked
        self.working = working
        self.erroring = erroring
    }
}

/// The pure half: given the three previous sets and the board, which jobs are
/// newly in each condition.
enum NotificationDiff {
    static func transitions(
        prevBlocked: Set<String>,
        prevWorking: Set<String>,
        prevErroring: Set<String>,
        board: [JobSnapshot]
    ) -> [JobTransition] {
        var out: [JobTransition] = []
        for job in board {
            switch job.column {
            case .blocked:
                if !prevBlocked.contains(job.slug) { out.append(.blocked(job.slug)) }
            case .working:
                if !prevWorking.contains(job.slug) { out.append(.started(job.slug)) }
            default:
                break
            }
            // Every non-archived job, whatever column it reads as — amendment
            // 11. A job can therefore fire `blocked` and `erroring` from one
            // pass, which is correct: they are two different facts about it.
            if job.column != .archived, job.erroring, !prevErroring.contains(job.slug) {
                out.append(.erroring(job.slug))
            }
        }
        return out
    }

    /// The sets a board payload IS — what gets persisted after a pass.
    static func sets(board: [JobSnapshot]) -> NotifiedSets {
        var next = NotifiedSets()
        for job in board {
            switch job.column {
            case .blocked: next.blocked.insert(job.slug)
            case .working: next.working.insert(job.slug)
            default: break
            }
            if job.column != .archived, job.erroring { next.erroring.insert(job.slug) }
        }
        return next
    }

    /// The wire payload reduced to what the diff reads. Bucketing goes through
    /// S3's `Bucketing.bucket` rather than a second copy of the rule: an
    /// unhealthy cell reads as BLOCKED whatever its file state says, and a
    /// notifier that disagreed with the board about which column a job is in
    /// would page about a card nobody can find.
    static func snapshots(_ board: WebBoard) -> [JobSnapshot] {
        board.jobs.map { job in
            JobSnapshot(
                slug: job.slug,
                column: Bucketing.bucket(
                    state: job.state, health: job.health, archived: job.archived == true
                ),
                erroring: !(job.err ?? "").isEmpty
            )
        }
    }
}

/// The stateful half: authorization, persistence, delivery, and the dock badge.
///
/// PERSISTENCE IS `UserDefaults`, one JSON blob under `notifiedSets`. Chosen
/// over a file in Application Support because the payload is three small string
/// sets written once per 3s poll: `UserDefaults` coalesces those writes itself,
/// where a file would need its own debounce to avoid a synchronous write per
/// tick, and there is nothing here worth surviving a defaults reset.
@MainActor
final class NotificationEngine: NSObject {
    static let shared = NotificationEngine()

    private static let setsKey = "notifiedSets"
    /// The Settings toggle (`SettingsKey.notificationsEnabled`).
    private static let enabledKey = SettingsKey.notificationsEnabled

    /// True once the system prompt has been asked for in this launch, so a
    /// board poll every 3s does not re-request authorization 20 times a minute.
    private var authorizationAsked = false
    /// Set when the user has said no, so a future feature can explain why the
    /// toggle appears to do nothing.
    private(set) var authorizationDenied = false

    private override init() {
        super.init()
    }

    /// Wires the foreground-presentation delegate. Called once from the app's
    /// root; without it a banner for a job that blocks while the window is
    /// already frontmost is delivered silently.
    func attachDelegate() {
        UNUserNotificationCenter.current().delegate = self
    }

    // MARK: - the pass

    /// Runs one diff against a freshly polled board: fires the transitions,
    /// persists the new sets, and repaints the dock badge.
    ///
    /// The dock badge is set on EVERY pass, toggle or no toggle: it is a count
    /// on an icon rather than an interruption, and a user who turned banners
    /// off has not asked to stop being able to see how many jobs are waiting.
    func process(_ board: WebBoard) {
        let snapshots = NotificationDiff.snapshots(board)
        updateBadge(snapshots)

        guard UserDefaults.standard.bool(forKey: Self.enabledKey) else {
            // The toggle being off does not freeze the sets — it CLEARS them,
            // so switching it back on seeds silently instead of firing one
            // banner per transition that happened while it was off.
            UserDefaults.standard.removeObject(forKey: Self.setsKey)
            return
        }
        requestAuthorizationIfNeeded()

        let next = NotificationDiff.sets(board: snapshots)
        defer { store(next) }

        // No stored sets at all means this is the first pass of a fresh
        // install (or of a toggle just switched back on): seed silently rather
        // than blasting one banner per job already in a condition.
        guard let prev = loadSets() else { return }

        let transitions = NotificationDiff.transitions(
            prevBlocked: prev.blocked,
            prevWorking: prev.working,
            prevErroring: prev.erroring,
            board: snapshots
        )
        guard !transitions.isEmpty else { return }
        var byslug: [String: WebJob] = [:]
        for job in board.jobs where byslug[job.slug] == nil { byslug[job.slug] = job }
        for transition in transitions { fire(transition, job: byslug[transition.slug]) }
    }

    // MARK: - persistence

    func loadSets() -> NotifiedSets? {
        guard let data = UserDefaults.standard.data(forKey: Self.setsKey) else { return nil }
        return try? JSONDecoder().decode(NotifiedSets.self, from: data)
    }

    private func store(_ sets: NotifiedSets) {
        guard let data = try? JSONEncoder().encode(sets) else { return }
        UserDefaults.standard.set(data, forKey: Self.setsKey)
    }

    /// Drops the tracking sets — what the Settings toggle turning off should
    /// do, and what `process` does for it when it sees the toggle off.
    func resetConditionTracking() {
        UserDefaults.standard.removeObject(forKey: Self.setsKey)
    }

    // MARK: - authorization

    /// Asks once per launch, and only when there is something to deliver. A
    /// permission prompt at launch, before the app has ever had a reason to
    /// notify, is the prompt everyone denies.
    func requestAuthorizationIfNeeded() {
        guard !authorizationAsked else { return }
        authorizationAsked = true
        UNUserNotificationCenter.current().getNotificationSettings { settings in
            switch settings.authorizationStatus {
            case .notDetermined:
                UNUserNotificationCenter.current()
                    .requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
                        Task { @MainActor in NotificationEngine.shared.authorizationDenied = !granted }
                    }
            case .denied:
                Task { @MainActor in NotificationEngine.shared.authorizationDenied = true }
            default:
                Task { @MainActor in NotificationEngine.shared.authorizationDenied = false }
            }
        }
    }

    // MARK: - delivery

    private func fire(_ transition: JobTransition, job: WebJob?) {
        let content = UNMutableNotificationContent()
        switch transition {
        case .blocked(let slug):
            content.title = "\(slug) blocked"
            content.body = job?.question?.questions.first?.header
                ?? job?.detail
                ?? job?.health
                ?? "needs attention"
        case .started(let slug):
            content.title = "\(slug) started"
            content.body = job?.detail ?? "cell started"
        case .erroring(let slug):
            content.title = "\(slug) error"
            content.body = job?.err ?? "unknown error"
        }
        content.sound = .default
        // Stable per slug+kind: a repeat for the same still-active condition
        // replaces the pending one instead of stacking duplicates.
        let request = UNNotificationRequest(
            identifier: transition.identifier, content: content, trigger: nil
        )
        UNUserNotificationCenter.current().add(request)
    }

    /// The dock badge is the BLOCKED count — the one number that means "this
    /// is waiting on you". An empty label removes the badge; `"0"` would draw
    /// a red pip saying nothing is wrong.
    private func updateBadge(_ snapshots: [JobSnapshot]) {
        let blocked = snapshots.filter { $0.column == .blocked }.count
        NSApp?.dockTile.badgeLabel = blocked > 0 ? String(blocked) : nil
    }
}

extension JobTransition {
    var slug: String {
        switch self {
        case .blocked(let s), .started(let s), .erroring(let s): return s
        }
    }

    /// The notification request id — see `fire`.
    var identifier: String {
        switch self {
        case .blocked(let s): return "blocked-\(s)"
        case .started(let s): return "started-\(s)"
        case .erroring(let s): return "error-\(s)"
        }
    }
}

extension NotificationEngine: UNUserNotificationCenterDelegate {
    /// A job that blocks while the board is already frontmost still banners.
    /// Without this the alert is delivered silently and the whole feature
    /// looks broken to anyone actually watching the board.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }
}
