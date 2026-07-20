import Foundation
import Observation
import UserNotifications
import BackgroundTasks

/// Local-notification "job blocked" alerts. v1 deliberately has no APNs —
/// that needs a paid Apple developer account carl doesn't have — so this is
/// entirely polling-driven: every successful board fetch (the foreground
/// poll in BoardViewModel.refresh, and the BGAppRefreshTask below) diffs
/// currently-blocked jobs against a persisted "already notified" set and
/// fires one local notification per newly-blocked job.
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
    private static let notifiedSlugsKey = "notifiedBlockedSlugs"
    private static let backgroundRefreshInterval: TimeInterval = 15 * 60

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

    // MARK: - blocked-job diffing

    private func notifiedSlugs() -> Set<String> {
        Set(UserDefaults.standard.stringArray(forKey: Self.notifiedSlugsKey) ?? [])
    }

    private func setNotifiedSlugs(_ slugs: Set<String>) {
        UserDefaults.standard.set(Array(slugs), forKey: Self.notifiedSlugsKey)
    }

    /// Call after every successful board fetch. `BoardTheme.column(for:)`
    /// already folds "health non-empty" into .blocked, so this one check
    /// covers both the blocked state and unhealthy cells — exactly what the
    /// board itself shows in the BLOCKED column.
    func processBoard(_ board: WebBoard) {
        guard UserDefaults.standard.bool(forKey: Self.notifyEnabledKey) else { return }

        let blockedJobs = board.jobs.filter { BoardTheme.column(for: $0) == .blocked }
        let blockedSlugs = Set(blockedJobs.map(\.slug))
        let alreadyNotified = notifiedSlugs()

        for job in blockedJobs where !alreadyNotified.contains(job.slug) {
            fire(for: job)
        }

        // The notified set always mirrors "currently blocked": jobs that
        // dropped out (answered, retried, resolved) are implicitly removed
        // here, so a future re-block notifies again instead of staying
        // silently suppressed forever.
        setNotifiedSlugs(blockedSlugs)
    }

    private func fire(for job: WebJob) {
        let content = UNMutableNotificationContent()
        content.title = "\(job.slug) blocked"
        content.body = job.question?.questions.first?.header
            ?? job.detail
            ?? job.health
            ?? "needs attention"
        content.sound = .default
        // Stable per-slug identifier: if this ever fired twice for the same
        // still-blocked job it replaces the pending one instead of stacking
        // duplicates.
        let request = UNNotificationRequest(identifier: "blocked-\(job.slug)", content: content, trigger: nil)
        UNUserNotificationCenter.current().add(request)
    }

    // MARK: - background refresh

    /// Submits the next BGAppRefreshTask, ~15 minutes out (iOS treats this
    /// as an earliest-begin-date, not a promise). No-ops when the toggle is
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
