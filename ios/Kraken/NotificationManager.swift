import Foundation
import Observation
import UserNotifications
import BackgroundTasks

/// Local-notification job alerts. v1 deliberately has no APNs — that needs
/// a paid Apple developer account carl doesn't have — so this is entirely
/// polling-driven: every successful board fetch (the foreground poll in
/// BoardViewModel.refresh, and the BGAppRefreshTask below) diffs the
/// board's jobs against three persisted "currently in this condition" sets
/// — blocked, started (working), erroring — and fires one local
/// notification per job newly entering any of them. Reaching done is
/// deliberately silent: that's a manual, in-app gate, never a push.
///
/// Known limitation (carl is aware, documented here too): BGAppRefreshTask
/// timing is entirely iOS-discretionary — delivery can lag tens of minutes
/// behind the actual block, and background refresh stops firing once the
/// app is force-quit from the app switcher (iOS won't wake a force-quit app
/// for background tasks — only a respring or next manual launch resumes
/// scheduling). Reliable, timely delivery needs real push (APNs) later.
@Observable
final class NotificationManager: NSObject {
    static let shared = NotificationManager()

    static let refreshTaskIdentifier = "dev.carl.Kraken.refresh"
    private static let notifyEnabledKey = "notifyBlocked"
    private static let notifiedBlockedSlugsKey = "notifiedBlockedSlugs"
    private static let notifiedStartedSlugsKey = "notifiedStartedSlugs"
    private static let notifiedErrorSlugsKey = "notifiedErrorSlugs"
    // carl's call: ~1 minute best-effort. iOS treats this as an
    // earliest-begin-date, not a promise — see handleBackgroundRefresh.
    private static let backgroundRefreshInterval: TimeInterval = 60

    /// Whether the last authorization check found notifications denied —
    /// Settings shows a hint when this is true so the toggle being "on"
    /// doesn't silently do nothing.
    var authorizationDenied = false

    private override init() {
        super.init()
    }

    func attachDelegate() {
        UNUserNotificationCenter.current().delegate = self
    }

    // MARK: - authorization

    /// Requests authorization the first time the Settings toggle is
    /// switched on; a no-op (beyond refreshing `authorizationDenied`) once
    /// the user has already answered the system prompt once.
    func requestAuthorizationIfNeeded() {
        UNUserNotificationCenter.current().getNotificationSettings { settings in
            switch settings.authorizationStatus {
            case .notDetermined:
                UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge]) { granted, _ in
                    DispatchQueue.main.async { self.authorizationDenied = !granted }
                }
            case .denied:
                DispatchQueue.main.async { self.authorizationDenied = true }
            default:
                DispatchQueue.main.async { self.authorizationDenied = false }
            }
        }
    }

    /// Re-checks (never prompts) current authorization — used when Settings
    /// appears, so a permission revoked in iOS Settings while the app was
    /// closed shows up as denied again rather than a stale "granted".
    func refreshAuthorizationStatus() {
        UNUserNotificationCenter.current().getNotificationSettings { settings in
            DispatchQueue.main.async { self.authorizationDenied = settings.authorizationStatus == .denied }
        }
    }

    // MARK: - board diffing

    private func persistedSlugs(forKey key: String) -> Set<String> {
        Set(UserDefaults.standard.stringArray(forKey: key) ?? [])
    }

    private func setPersistedSlugs(_ slugs: Set<String>, forKey key: String) {
        UserDefaults.standard.set(Array(slugs), forKey: key)
    }

    /// Removes all three persisted "currently in this condition" sets — call
    /// when the notify toggle turns off so a stale, frozen snapshot doesn't
    /// cause a burst of "newly entered" notifications for every transition
    /// that happened while the toggle was off, once it's re-enabled.
    func resetConditionTracking() {
        let defaults = UserDefaults.standard
        defaults.removeObject(forKey: Self.notifiedBlockedSlugsKey)
        defaults.removeObject(forKey: Self.notifiedStartedSlugsKey)
        defaults.removeObject(forKey: Self.notifiedErrorSlugsKey)
    }

    /// Call after every successful board fetch. Diffs each of three
    /// conditions against a persisted "currently in this condition" set and
    /// fires one local notification per job newly entering it. Each set
    /// always mirrors "currently in that condition": a job that drops out
    /// (resolved, finished a run, error cleared) is implicitly removed, so a
    /// future re-entry notifies again instead of staying silently
    /// suppressed forever. Done is intentionally not one of the three —
    /// it's a manual, in-app gate and never pages.
    func processBoard(_ board: WebBoard) {
        guard UserDefaults.standard.bool(forKey: Self.notifyEnabledKey) else { return }

        // BoardTheme.column(for:) already folds "health non-empty" into
        // .blocked, so this one check covers both the blocked state and
        // unhealthy cells — exactly what the board itself shows in the
        // BLOCKED column.
        let blockedJobs = board.jobs.filter { BoardTheme.column(for: $0) == .blocked }
        let blockedSlugs = Set(blockedJobs.map(\.slug))
        // A working job with an unhealthy cell also satisfies .blocked
        // above — excluding it here keeps that one transition to a single
        // "blocked" notification instead of also firing "started".
        let startedJobs = board.jobs.filter { $0.state == "working" && !blockedSlugs.contains($0.slug) }
        // A done job can carry a stale, unrelated Err (e.g. it finished
        // before picking up a parse error) — excluding done/archived here
        // keeps "never notify done" true even in that case.
        let erroringJobs = board.jobs.filter {
            !($0.err ?? "").isEmpty && $0.state != "done" && $0.state != "archived"
        }

        diffAndNotify(current: blockedJobs, persistedKey: Self.notifiedBlockedSlugsKey, fire: fireBlocked)
        diffAndNotify(current: startedJobs, persistedKey: Self.notifiedStartedSlugsKey, fire: fireStarted)
        diffAndNotify(current: erroringJobs, persistedKey: Self.notifiedErrorSlugsKey, fire: fireError)
    }

    private func diffAndNotify(current: [WebJob], persistedKey: String, fire: (WebJob) -> Void) {
        let currentSlugs = Set(current.map(\.slug))
        defer { setPersistedSlugs(currentSlugs, forKey: persistedKey) }

        // Per-key seeding: an install that already has an older persisted
        // key (e.g. blocked, from before started/error existed) keeps that
        // key's existing behavior unchanged, while a key that's never been
        // persisted seeds silently instead of blasting a notification for
        // every job already in that condition.
        guard UserDefaults.standard.object(forKey: persistedKey) != nil else { return }

        let previouslyTracked = persistedSlugs(forKey: persistedKey)
        for job in current where !previouslyTracked.contains(job.slug) {
            fire(job)
        }
    }

    private func fireBlocked(for job: WebJob) {
        let content = UNMutableNotificationContent()
        content.title = "\(job.slug) blocked"
        content.body = job.question?.questions.first?.header
            ?? job.detail
            ?? job.health
            ?? "needs attention"
        content.sound = .default
        fire(identifier: "blocked-\(job.slug)", content: content)
    }

    private func fireStarted(for job: WebJob) {
        let content = UNMutableNotificationContent()
        content.title = "\(job.slug) started"
        content.body = job.detail ?? "cell started"
        content.sound = .default
        fire(identifier: "started-\(job.slug)", content: content)
    }

    private func fireError(for job: WebJob) {
        let content = UNMutableNotificationContent()
        content.title = "\(job.slug) error"
        content.body = job.err ?? "unknown error"
        content.sound = .default
        fire(identifier: "error-\(job.slug)", content: content)
    }

    /// Stable per-slug+kind identifier: if this ever fired twice for the
    /// same still-active condition it replaces the pending one instead of
    /// stacking duplicates.
    private func fire(identifier: String, content: UNMutableNotificationContent) {
        let request = UNNotificationRequest(identifier: identifier, content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request)
    }

    // MARK: - background refresh

    /// Submits the next BGAppRefreshTask, ~1 minute out (iOS treats this as
    /// an earliest-begin-date, not a promise). No-ops when the toggle is
    /// off — nothing to poll for if the user hasn't asked for the feature.
    func scheduleBackgroundRefresh() {
        guard UserDefaults.standard.bool(forKey: Self.notifyEnabledKey) else { return }
        let request = BGAppRefreshTaskRequest(identifier: Self.refreshTaskIdentifier)
        request.earliestBeginDate = Date(timeIntervalSinceNow: Self.backgroundRefreshInterval)
        try? BGTaskScheduler.shared.submit(request)
    }

    /// The BGAppRefreshTask launch handler, registered once in KrakenApp's
    /// init. Reschedules the next run immediately (Apple's own guidance —
    /// don't wait until the end of this one), then fetches the board with
    /// the same APIClient every other screen uses (already a short 5s
    /// timeout) and runs the identical diff-notify pass.
    func handleBackgroundRefresh(task: BGAppRefreshTask) {
        scheduleBackgroundRefresh()

        guard UserDefaults.standard.bool(forKey: Self.notifyEnabledKey) else {
            task.setTaskCompleted(success: true)
            return
        }

        let work = Task {
            await performBackgroundFetch()
            task.setTaskCompleted(success: true)
        }
        task.expirationHandler = {
            work.cancel()
        }
    }

    private func performBackgroundFetch() async {
        let baseURL = UserDefaults.standard.string(forKey: "serverURL") ?? KrakenDefaults.serverURL
        guard let board = try? await APIClient.shared.fetchBoard(baseURL: baseURL) else { return }
        processBoard(board)
    }
}

extension NotificationManager: UNUserNotificationCenterDelegate {
    // A block that appears while the app is already open still banners
    // instead of arriving silently.
    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }
}
