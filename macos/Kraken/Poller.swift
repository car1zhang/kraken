import Foundation

/// A fixed-interval poll driver with exactly one request in flight.
///
/// The board, log, dream and graph screens each own one of these. The shape is
/// deliberately narrow:
///
/// - `start()` fires the work IMMEDIATELY and then every `interval` seconds —
///   a poller that waited a full interval before its first fetch would leave
///   the window empty for that long after every launch and every resume.
/// - A tick that arrives while the previous one is still running CANCELS it.
///   Overlapping requests are worse than a skipped one: they arrive out of
///   order, so the older response can overwrite the newer state.
/// - `pause()`/`resume()` are what the window's occlusion and the app's
///   background transition drive; pausing cancels the in-flight request too.
/// - `failureStreak` is exposed rather than logged, because the chrome renders
///   it (one failure is invisible, a run of them is the "server unreachable"
///   banner). Any success resets it to zero.
///
/// Foundation + Swift concurrency only — no Combine, no `Timer`, and no
/// URLSession knowledge: the caller supplies the work.
@MainActor
final class Poller {
    /// Seconds between ticks. Fixed for the poller's lifetime.
    let interval: TimeInterval

    /// Consecutive failed ticks. Reset to 0 by any tick that returns without
    /// throwing. A CANCELLED tick is not a failure — it is this poller's own
    /// doing — so cancellation never touches it.
    private(set) var failureStreak = 0

    /// True between `start()`/`resume()` and `pause()`/`stop()`.
    private(set) var isRunning = false

    private let work: () async throws -> Void
    private var loopTask: Task<Void, Never>?
    private var tickTask: Task<Void, Never>?

    init(interval: TimeInterval, work: @escaping () async throws -> Void) {
        self.interval = interval
        self.work = work
    }

    deinit {
        loopTask?.cancel()
        tickTask?.cancel()
    }

    /// Begins polling. Idempotent: calling it while already running does
    /// nothing, so a view that starts its poller in `onAppear` cannot stack up
    /// two loops by appearing twice.
    func start() {
        guard !isRunning else { return }
        isRunning = true
        loopTask = Task { [weak self] in
            while !Task.isCancelled {
                guard let self, self.isRunning else { return }
                self.fire()
                // Sleep AFTER firing: start() must not cost an interval.
                do {
                    try await Task.sleep(nanoseconds: UInt64(self.interval * 1_000_000_000))
                } catch {
                    return  // cancelled mid-sleep
                }
            }
        }
    }

    /// Stops ticking and cancels any in-flight work. `resume()` restarts from
    /// an immediate fire, exactly like `start()`.
    func pause() {
        isRunning = false
        loopTask?.cancel()
        loopTask = nil
        tickTask?.cancel()
        tickTask = nil
    }

    func resume() { start() }

    /// Permanent stop: pause, plus the failure streak cleared so a later
    /// `start()` does not inherit a stale banner.
    func stop() {
        pause()
        failureStreak = 0
    }

    /// Runs the work once, out of band, without disturbing the tick schedule —
    /// what a manual refresh button calls. Subject to the same one-in-flight
    /// rule as a tick.
    func fireNow() { fire() }

    private func fire() {
        // The new tick supersedes the old one. Cancellation is cooperative:
        // URLSession's async API honours it, and `work` is expected to.
        tickTask?.cancel()
        tickTask = Task { [weak self] in
            guard let self else { return }
            do {
                try await self.work()
                if Task.isCancelled { return }
                self.failureStreak = 0
            } catch is CancellationError {
                return
            } catch {
                if Task.isCancelled { return }
                self.failureStreak += 1
            }
        }
    }
}
