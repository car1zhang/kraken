import SwiftUI
import Observation
import Foundation
import UniformTypeIdentifiers

@Observable
final class BoardViewModel {
    var board: WebBoard?
    var errorMessage: String?

    func refresh(baseURL: String) async {
        // Read once and handed to the request, so the token compared on a 401
        // below is the one this request actually carried.
        let stored = PairingStore.load()
        let sentToken = APIClient.tokenSent(to: baseURL, stored: stored)
        do {
            let board = try await APIClient.shared.fetchBoard(baseURL: baseURL, stored: stored)
            self.board = board
            self.errorMessage = nil
            ingestBoardUpdate(board.update)
            ingestBoardShip(board.shipIOS)
            // Every successful fetch (this foreground poll, and the
            // BGAppRefreshTask in NotificationManager) runs the same
            // blocked-job diff — one notification per newly-blocked job.
            NotificationManager.shared.processBoard(board)
        } catch KrakenError.unauthorized {
            // The gate refused THIS phone's token: revoked from the board, or
            // the password changed. Only then is the pairing forgotten (and
            // only once — the next 401 finds a different or no record) and
            // BoardView's gate swaps the board for the pair screen.
            //
            // A 401 to a request that carried no token (the server field
            // names another host) or a token that is no longer the stored one
            // (an in-flight request from before a re-pair) says nothing about
            // the current pairing: it is shown as an error and wipes nothing.
            if let sentToken, sentToken == PairingStore.load()?.token {
                PairingSession.shared.clear()
                NotificationManager.shared.resetConditionTracking()
                self.board = nil
            }
            self.errorMessage = KrakenError.unauthorized.errorDescription
        } catch {
            // An update's last act restarts the server this poll talks to,
            // so mid-run the board goes dark for a few seconds. Say what is
            // actually happening rather than flashing URLSession's "Could
            // not connect to the server." for the whole window.
            self.errorMessage = updatePolling
                ? "daemon — restarting"
                : ((error as? KrakenError)?.errorDescription ?? error.localizedDescription)
        }
    }

    /// Every card in `column`, local and remote, minus the remote ones when
    /// Settings' "Hide others' jobs" switch is on. The switch is read straight
    /// from UserDefaults: BoardView observes the same key through @AppStorage,
    /// which is what re-runs this when it flips.
    func jobs(in column: BoardColumn) -> [WebJob] {
        let hideOthers = UserDefaults.standard.bool(forKey: Self.hideOthersKey)
        return FleetFilter.apply(board?.jobs ?? [], hideOthers: hideOthers)
            .filter { BoardTheme.column(for: $0) == column }
    }

    /// The `@AppStorage` key of Settings' "Hide others' jobs" switch.
    static let hideOthersKey = "hideOthers"

    /// Non-blocking contract-handshake warnings for the latest board fetch —
    /// most severe first. `contract` absent or below what this app expects
    /// means the server predates the handshake or hasn't been restarted onto
    /// a build that speaks it; above means this app is the one that's stale.
    var contractWarnings: [String] {
        guard let board else { return [] }
        var warnings: [String] = []
        let contract = board.contract ?? 0
        if contract < APIClient.expectedContract {
            warnings.append("server is running an older kraken build — restart k web on the Mac")
        } else if contract > APIClient.expectedContract {
            warnings.append("server is newer than this app — update the Kraken app")
        }
        if board.stale == true {
            warnings.append("kraken was reinstalled since this server started — restart k web")
        }
        if board.daemonOK && board.daemon.stale == true {
            warnings.append("kraken was reinstalled since the daemon started — restart the daemon")
        }
        return warnings
    }

    /// Whether any job in `column` needs attention — a pending question or a
    /// non-empty health string — for the overview strip/sheet's flag badge.
    func hasFlag(in column: BoardColumn) -> Bool {
        jobs(in: column).contains { job in
            job.question != nil || !(job.health ?? "").isEmpty
        }
    }

    // MARK: - self-update
    //
    // The ⟳ update control — which lives in Settings' "Mac" section since the
    // nav restructure, not in the board's daemon bar where it grew up — is a
    // view over one server record (WebUpdate
    // — carried both by /api/board's `update` key and by GET
    // /api/update/status) plus two facts no server record can hold: the last
    // refusal, because a refused update never became a run, and how long the
    // server has been unreachable mid-run.
    //
    // While a run is live the status endpoint is polled on its own ~1.2s
    // cadence and its failures are SWALLOWED. That is the whole trick: an
    // update's last act is `launchctl kickstart -k` on the very server
    // answering these requests, so for a few seconds every fetch fails. That
    // gap is the normal middle of a SUCCESSFUL update — treating it as an
    // error would be wrong on every single run.

    /// jobs.UpdateTerminal: no run is in progress, so the control is live.
    private static let terminalPhases: Set<String> = ["idle", "done", "failed", "uptodate"]
    /// How long a finished run keeps the control. ~/.kraken/update.json holds
    /// its last record forever, so without a window every launch for the rest
    /// of time would reopen a green ✓ — or a pink failure — from days ago.
    private static let updateRecentWindow: TimeInterval = 60
    /// Past this, a restart that hasn't come back has stopped being a
    /// restart. The web board allows 120s, which is sized for a browser on
    /// the same machine; the phone reaches the Mac over the tailnet and may
    /// itself be re-establishing that link (Wi-Fi hop, radio wake) while the
    /// server is down, so this side is deliberately far more generous.
    private static let updateGapCeiling: TimeInterval = 300
    private static let updatePollInterval: Duration = .milliseconds(1200)
    /// The most any ONE tick may contribute to the gap clock. Six seconds:
    /// five times the 1.2s poll interval, sitting just under the longest
    /// legitimate tick — a fetch that times out (5s) plus the sleep after it
    /// (1.2s) is 6.2s, which this clamps to 6 rather than rejecting.
    ///
    /// Clamping, not discarding, and the distinction is the whole point. A
    /// discard rule reads as "an over-long delta is time nobody was polling
    /// across" — true of a suspension or a tab switch, and false of exactly
    /// the case the ceiling exists for: when `kraken serve` never comes back,
    /// EVERY tick is a timeout, so every delta was over the ceiling and the
    /// clock stayed at zero forever. Clamping keeps both properties: a 600s
    /// suspension credits 6s (the protection survives), and a 6.2s timeout
    /// tick credits 6s (the ceiling is reachable in every failure mode).
    /// See `updateGapObserved`.
    private static let updateGapTickCeiling: TimeInterval = 6

    /// Last record seen, from either poll.
    private(set) var update: WebUpdate?
    /// The one sentence the control has to say that no record carries: a
    /// refusal (nothing ran), or a recorded failure carl tapped to read.
    /// Sticky until the next tap — unlike `errorMessage`, which the very next
    /// successful board poll clears 4s later, far too fast to read.
    private(set) var updateMessage: String?
    /// True while `updateMessage` holds something carl asked to see, rather
    /// than something that merely happened to him — see `barMessage`.
    private(set) var updateMessageRevealed = false
    /// A run is live and `runUpdatePoll` should be (or already is) running.
    /// Only `runUpdatePoll` ever clears it, in a `defer`, so there is no exit
    /// from that loop — terminal phase, cancellation, gap ceiling — that can
    /// leave the flag set with nothing polling. That state used to be
    /// unrecoverable: the control paints itself disabled off this flag, and
    /// `ingestBoardUpdate`'s `guard !updatePolling` would then drop every
    /// later board record on the floor.
    private(set) var updatePolling = false
    /// Bumped every time a poll is REQUESTED. BoardView's `.task(id:)` keys
    /// off this rather than off `updatePolling`, because `.task(id:)` only
    /// restarts when the id differs between two body evaluations: a flag that
    /// goes false (poll finished) and true again (a stale board record from
    /// /api/board's 2s cache, still carrying the non-terminal phase) inside
    /// one render frame reads as `true` both times, and no task starts. A
    /// counter that only ever increases cannot collide with itself.
    private(set) var updatePollGeneration = 0
    /// Which `runUpdatePoll` invocation currently owns `updatePolling`. The
    /// poll task restarts on a scene-phase change as well as a generation
    /// bump (see UpdatePollKey), and SwiftUI does not wait for the cancelled
    /// loop to unwind before running the new task body — so two invocations
    /// briefly overlap, and the OLD one's `defer` would otherwise clear the
    /// flag out from under the new one. That leaves the flag false with a
    /// live run, the mirror image of the stuck state the defer exists to
    /// prevent. Each invocation stamps itself here and only writes shared
    /// state while it is still the stamp holder.
    private var updatePollEpoch = 0
    /// The tap has fired but POST /api/update hasn't answered yet. Set
    /// SYNCHRONOUSLY in the button's action, before the request is even
    /// awaited, because `state.disabled` is derived from the record and the
    /// record can't move until the response lands: without this, two taps
    /// 50ms apart both reach the handler and the second earns a 409 whose
    /// "update already in flight" then sits in the bar for the whole run.
    private(set) var updateStarting = false
    /// How many seconds of restart gap this RUN has actually been WATCHED for,
    /// and the timestamp of the last failing tick, contributing or not (see
    /// `updateGapLastTick`'s own doc — the assignment is unconditional). Both
    /// live on the model rather than inside `runUpdatePoll`, and that is the
    /// whole fix for a real bug: the loop is restarted every time its `.task`
    /// re-enters, and since the ship/update polls now run from BOTH the Board
    /// and Settings (a `.task` dies when its view leaves the screen), a
    /// loop-local clock was reset on every tab switch. Flipping tabs more
    /// often than once per ceiling — trivially easy while watching an update —
    /// meant the ceiling could never fire, and a serve that never came back
    /// spun a cyan "restarting" control forever.
    ///
    /// Accumulating OBSERVED time rather than diffing a start date is what
    /// makes that safe to persist across loops. The old loop-local clock was
    /// reset per loop for a real reason: it measured wall-clock time while the
    /// loop itself was frozen, so locking the phone for ten minutes during a
    /// perfectly normal restart synthesised a failure on resume. Here every
    /// tick contributes at most `updateGapTickCeiling` seconds, so a
    /// suspension or a tab switch contributes six seconds rather than the ten
    /// minutes it actually spanned, while ordinary ticks accumulate honestly.
    /// Reset when a new RUN starts and on any successful fetch — never on task
    /// re-entry.
    private var updateGapObserved: TimeInterval = 0
    /// When the last FAILING tick happened, contributing or not — the
    /// assignment is unconditional, because this is the anchor the next
    /// delta is measured from and skipping it would silently double the next
    /// one. It doubles as the "a gap is open" flag (`updateInGap`).
    private var updateGapLastTick: Date?
    /// Failing ticks in a row, on the model for the same reason. The ceiling
    /// needs two BACK TO BACK so that a single blip on resume — one failure
    /// followed 1.2s later by a success — can't trip it.
    private var updateFailureStreak = 0
    /// Whether a gap is open right now, for `updateButton`'s "restarting" paint.
    private var updateInGap: Bool { updateGapLastTick != nil }

    /// Drops the gap clock and the failure streak. Called when a new update RUN
    /// begins and on every successful status fetch — deliberately NOT when a
    /// poll loop re-enters, which is the distinction that keeps the ceiling
    /// reachable across a tab switch.
    private func resetUpdateGapClock() {
        updateGapObserved = 0
        updateGapLastTick = nil
        updateFailureStreak = 0
    }

    /// Folds the 4s board poll's copy of the record in. The 1.2s status poll
    /// wins outright while it is running: the two race, and a board response
    /// can easily be older than a status response that landed just before it
    /// — applying it would rewind the phase on screen.
    private func ingestBoardUpdate(_ u: WebUpdate?) {
        guard !updatePolling, let u else { return }
        update = u
        resetUpdateGapClock()
        if !Self.terminalPhases.contains(u.phase ?? "idle") {
            // a run someone else started — the CLI, the web board, another
            // phone. Follow it just as closely as one started from here.
            updateMessage = nil
            updateMessageRevealed = false
            beginUpdatePolling()
        }
    }

    /// Asks BoardView's `.task(id: updatePollGeneration)` for a fresh poll
    /// loop. The generation bump is the whole point — see its declaration.
    private func beginUpdatePolling() {
        // A new RUN is exactly one of the two moments the gap clock resets
        // (the other is a successful fetch). A poll loop merely re-entering is
        // NOT — see updateGapObserved.
        resetUpdateGapClock()
        updatePolling = true
        updatePollGeneration &+= 1
    }

    /// Flips the control off in the tap handler itself — see updateStarting.
    /// Separate from `startUpdate` because a `Task { await … }` body does not
    /// begin running synchronously with the tap that creates it, so anything
    /// inside `startUpdate` is already too late to stop a second tap.
    func markUpdateStarting() {
        updateStarting = true
    }

    /// Fires the run. No confirmation step: the tap IS the decision, same as
    /// the web board's button.
    func startUpdate(baseURL: String) async {
        defer { updateStarting = false }
        updateMessage = nil
        updateMessageRevealed = false
        do {
            let resp = try await APIClient.shared.startUpdate(baseURL: baseURL)
            // The handler writes the claim (phase preflight) BEFORE it forks
            // the updater, so paint that phase now rather than leaving the
            // control idle until the first poll tick lands.
            let now = Int64(Date().timeIntervalSince1970 * 1000)
            update = WebUpdate(
                phase: resp.phase.isEmpty ? "preflight" : resp.phase,
                fromSHA: resp.fromSHA, toSHA: "",
                behind: update?.behind ?? 0, error: "",
                startedAt: now, updatedAt: now,
                buildSHA: update?.buildSHA ?? "", checkedAt: update?.checkedAt ?? 0
            )
            beginUpdatePolling()
        } catch {
            // A 409's text is one specific sentence about the machine's state
            // — the only thing worth putting on screen here.
            updateMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    /// Surfaces a recorded failure carl tapped. A failure after the updater
    /// detached can't come back as an HTTP status — the request returned long
    /// ago — so it arrives in the record instead.
    func revealUpdateError(_ message: String) {
        updateMessage = message.isEmpty ? "update failed — see ~/.kraken/update.log" : message
        updateMessageRevealed = true
    }

    /// Whether the last record is recent enough to still speak for the
    /// control. Shared by `updateButton` and `barMessage` so a revealed
    /// failure and the ✕ that revealed it always age out together.
    ///
    /// abs(), not a one-sided comparison: an `updated_at` in the FUTURE
    /// (clock skew between phone and Mac, a corrupt update.json) is
    /// otherwise fresh forever, and a `failed` record that is fresh forever
    /// pins the control at "✕ update failed" — which is not a button that
    /// can start a run, so the control never works again.
    private var updateRecordFresh: Bool {
        let updatedAt = update?.updatedAt ?? 0
        return updatedAt > 0
            && abs(Date().timeIntervalSince1970 - Double(updatedAt) / 1000) < Self.updateRecentWindow
    }

    /// The single line the daemon bar's error slot shows.
    ///
    /// `errorMessage` normally wins: it means the board on screen is stale
    /// RIGHT NOW, which outranks a refusal that is sticky precisely because
    /// it may be minutes old by the time it's read. The exception is a
    /// failure carl just TAPPED to reveal. The commonest way to have one is
    /// "kraken serve never came back", and in exactly that case the next
    /// board poll fails too and fills `errorMessage` with URLSession's
    /// "Could not connect to the server." — which would shadow the one
    /// sentence the tap existed to show, so the tap would produce no visible
    /// change at all. A user-initiated reveal outranks the connectivity
    /// error; a merely-sticky refusal still does not.
    ///
    /// That precedence is bounded by the revealed record's own freshness. A
    /// reveal is cleared only by the next run, so an outranking sentence
    /// would otherwise outlive its cause indefinitely: once the record ages
    /// out the control has already reverted to a plain "update", yet the
    /// failure text would still be sitting in the bar — and still hiding
    /// every genuine connectivity error behind it — until carl launched a
    /// real update just to dismiss it.
    var barMessage: String? {
        if updateMessageRevealed {
            if updateRecordFresh, let updateMessage { return updateMessage }
            return errorMessage
        }
        return errorMessage ?? updateMessage
    }

    /// The entry point BoardView's poll task calls. `updatePolling` alone is
    /// not a sound gate here: on a scene-phase restart the previous loop is
    /// cancelled and may have already cleared the flag in its `defer`, so
    /// gating on it would silently abandon a run that is still going. Fall
    /// back to the record — a non-terminal phase means the run outlived the
    /// suspension and deserves a fresh loop (and a fresh gap clock).
    func resumeUpdatePoll(baseURL: String) async {
        guard updatePolling || !Self.terminalPhases.contains(update?.phase ?? "idle") else { return }
        updatePolling = true
        await runUpdatePoll(baseURL: baseURL)
    }

    /// The in-flight poll loop, driven by BoardView's `.task(id:)` so it
    /// inherits the view's actor and cancellation. Polls the narrow status
    /// endpoint rather than the board (a 1.2s board poll would re-run
    /// discovery every tick) and swallows failures — see the section header.
    func runUpdatePoll(baseURL: String) async {
        updatePollEpoch &+= 1
        let epoch = updatePollEpoch
        // The flag is this loop's to hold and this loop's to drop, on EVERY
        // exit — terminal phase, cancellation, gap ceiling, the guard at the
        // top going false. Anything else can leave `updatePolling` true with
        // no poller behind it, which disables the control and makes
        // ingestBoardUpdate ignore every subsequent board record. Dropped
        // only if this invocation still owns the flag: a scene-phase restart
        // has a successor loop already running by the time this one unwinds
        // (see updatePollEpoch).
        defer { if updatePollEpoch == epoch { updatePolling = false } }
        // NOTE what is deliberately NOT here: a gap-clock reset. It used to
        // live on this line, and that made the ceiling unreachable — this loop
        // restarts on every tab switch (the poll runs from both the Board and
        // Settings, and a `.task` dies with its view), so the clock was zeroed
        // far more often than the 300s it was counting to. The clock now lives
        // on the model, accumulates only OBSERVED seconds, and resets when a
        // RUN starts or a fetch succeeds. See updateGapObserved for why that
        // is still safe across a suspension.
        while !Task.isCancelled && updatePolling && updatePollEpoch == epoch {
            do {
                let u = try await APIClient.shared.updateStatus(baseURL: baseURL)
                update = u
                resetUpdateGapClock()
                if Self.terminalPhases.contains(u.phase ?? "idle") { return }
            } catch {
                // Cancellation is not a gap. This view is a TabView child, so
                // switching to Settings or Sessions mid-run cancels the fetch
                // in flight against a server that is perfectly healthy —
                // recording that as the start of a gap would start the 300s
                // clock running while carl is on another tab, and paint
                // "restarting" at him on the way back.
                //
                // `Task.isCancelled` is the ONLY signal that can reach here:
                // APIClient.perform re-wraps every URLSession failure —
                // cancellation included — as KrakenError.message, so neither
                // `error is CancellationError` nor a URLError with
                // `.cancelled` can ever match. Don't add those casts back.
                if Task.isCancelled { return }
                updateFailureStreak += 1
                let now = Date()
                // Each tick credits its own elapsed time, CLAMPED. A delta
                // far past the ceiling is mostly time nobody was polling
                // across — a suspension, or the hand-over from a loop on the
                // other tab — so crediting the clamp instead of the whole
                // thing is what the old per-loop reset was really trying to
                // achieve. Crediting the clamp rather than nothing is what
                // keeps the ceiling reachable when every tick is a timeout;
                // see updateGapTickCeiling.
                if let last = updateGapLastTick {
                    updateGapObserved += min(now.timeIntervalSince(last), Self.updateGapTickCeiling)
                }
                updateGapLastTick = now
                if updateFailureStreak >= 2, updateGapObserved > Self.updateGapCeiling {
                    // Five minutes of silence is no longer a restart. Say so
                    // rather than spinning a cyan control at a dead socket
                    // forever — update.log on the Mac is the only thing that
                    // can explain a serve which never bound.
                    let stamp = Int64(now.timeIntervalSince1970 * 1000)
                    update = WebUpdate(
                        phase: "failed",
                        fromSHA: update?.fromSHA ?? "", toSHA: update?.toSHA ?? "",
                        behind: update?.behind ?? 0,
                        error: "kraken serve did not come back after the restart — see ~/.kraken/update.log",
                        startedAt: update?.startedAt ?? stamp, updatedAt: stamp,
                        buildSHA: update?.buildSHA ?? "", checkedAt: update?.checkedAt ?? 0
                    )
                    resetUpdateGapClock()
                    return
                }
            }
            try? await Task.sleep(for: Self.updatePollInterval)
        }
    }

    /// What the control shows right now, derived here so the view is a pure
    /// paint. Precedence mirrors the web board's renderUpdate(): the gap and
    /// the live run outrank any record, and a terminal record only speaks for
    /// the control while it is fresh.
    var updateButton: UpdateButtonState {
        let phase = update?.phase ?? "idle"
        let running = !Self.terminalPhases.contains(phase)
        let fresh = updateRecordFresh

        if updateStarting && !running {
            // the tap landed, the POST hasn't answered — the record still
            // reads terminal, so without this the control would stay live
            // and a second tap would earn a 409 (see updateStarting)
            return UpdateButtonState(glyph: "⟳", label: "starting", color: KTheme.accent, disabled: true)
        }
        if running && (updateInGap || phase == "restarting") {
            // either the updater says it's restarting, or the endpoint
            // stopped answering mid-run — which IS the restart, from here
            return UpdateButtonState(glyph: "⟳", label: "restarting", color: KTheme.polling, disabled: true)
        }
        if running {
            return UpdateButtonState(glyph: "⟳", label: phase, color: KTheme.accent, disabled: true)
        }
        if fresh, phase == "failed" {
            return UpdateButtonState(
                glyph: "✕", label: "update failed", color: KTheme.counter,
                disabled: false, revealsError: update?.error ?? ""
            )
        }
        if fresh, phase == "done" {
            let sha = update?.toSHA ?? ""
            return UpdateButtonState(
                glyph: "✓", label: sha.isEmpty ? "updated" : "updated \(sha)",
                color: KTheme.solved, disabled: false
            )
        }
        if fresh, phase == "uptodate" {
            // a run that found nothing to do built and restarted nothing.
            // Without this the tap would flash busy and land back on a bare
            // "update", which reads as "did that even do anything".
            return UpdateButtonState(glyph: "✓", label: "up to date", color: KTheme.solved, disabled: false)
        }
        if let behind = update?.behind, behind > 0 {
            // the daemon's background availability check, every 10min
            return UpdateButtonState(glyph: "⟳", label: "\(behind) behind", color: KTheme.revealed, disabled: false)
        }
        return UpdateButtonState(glyph: "⟳", label: "update", color: KTheme.text2, disabled: false)
    }

    // MARK: - ship to TestFlight
    //
    // The ⇧ ship control, the update control's sibling and its neighbour in
    // Settings' "Mac" section: one server
    // record (WebShipIOS, carried both by /api/board's `ship_ios` key and by
    // GET /api/ship-ios/status) plus the one refusal no record can carry.
    //
    // Two things make it a separate state machine rather than a second caller
    // of the code above:
    //
    //  1. There is no restart gap OF ITS OWN. A ship restarts nothing — serve,
    //     the daemon and the Mac are up throughout — so a status fetch that
    //     keeps failing is a genuine fault and is reported as one, after a
    //     single retry to absorb a dropped request. The exception belongs to
    //     the update control, not this one: its last act is `launchctl
    //     kickstart -k`, which takes serve down for a couple of seconds, and
    //     the shipper is given its own session (api/shipios.go's Setsid)
    //     exactly so it survives that. Reporting "✕ ship failed" over the one
    //     outage the run is engineered to live through would be backwards, so
    //     the ceiling stretches while an update is in flight — see
    //     shipFailureCeilingNow.
    //  2. `done` does NOT mean this app has been replaced. It means the .ipa
    //     reached App Store Connect. TestFlight still has to process it, and
    //     carl still has to install it from the TestFlight app — while looking
    //     at this very screen, running the OLD build. The control therefore
    //     says "uploaded", never "shipped", and shipBanner spells out the rest.

    /// jobs.ShipTerminal: no run is in flight, so the control is live.
    private static let shipTerminalPhases: Set<String> = ["idle", "done", "failed"]
    /// How long a finished run keeps the control. Twice the update control's
    /// 60s: that one is sized for a surface that is about to be replaced,
    /// while this record carries the build number and the "not installed yet"
    /// note, which is the whole reason to look at it.
    private static let shipRecentWindow: TimeInterval = 120
    /// Two failed fetches in a row, not one — a single blip on a tailnet hop
    /// is not evidence of anything. Deliberately NOT a time ceiling: there is
    /// no expected outage here to wait out…
    private static let shipFailureCeiling = 2
    /// …except the one a concurrent UPDATE causes, which is real, expected, and
    /// survivable by design. 25 ticks at 1.2s is ~30s: ten times the couple of
    /// seconds `launchctl kickstart -k` takes, and still short enough to report
    /// a serve that never came back while anyone is still looking. The update
    /// control's own 120s gap clock owns that question; this only has to avoid
    /// lying in the meantime.
    private static let shipFailureCeilingUpdating = 25
    private static let shipPollInterval: Duration = .milliseconds(1200)

    /// Last record seen, from either poll.
    private(set) var ship: WebShipIOS?
    /// The refusal a 409 carried — the one thing no record remembers, because
    /// nothing ran. A recorded FAILURE is deliberately not routed through here:
    /// it lives in the record and `shipBanner` prints it straight from there.
    private(set) var shipMessage: String? {
        didSet { shipMessageAt = shipMessage == nil ? nil : Date() }
    }
    /// When `shipMessage` was set. It needs its own clock because a refusal has
    /// no record to age out with, and this text occupies a whole banner strip:
    /// left unbounded it would sit under the daemon bar for the rest of the
    /// session, long after the credential it complains about was fixed.
    private var shipMessageAt: Date?
    /// A run is live and `runShipPoll` should be (or already is) running. Only
    /// `runShipPoll` clears it, in a `defer` — see updatePolling for the stuck
    /// state that discipline exists to prevent.
    private(set) var shipPolling = false
    /// Bumped every time a poll is REQUESTED; BoardView's `.task(id:)` keys off
    /// it for the same frame-collision reason updatePollGeneration explains.
    private(set) var shipPollGeneration = 0
    /// Which `runShipPoll` invocation currently owns `shipPolling` — see
    /// updatePollEpoch for why a plain flag isn't enough across a scene-phase
    /// restart.
    private var shipPollEpoch = 0
    /// The tap has fired but POST /api/ship-ios hasn't answered yet. Set
    /// synchronously in the button's action so two taps 50ms apart can't both
    /// reach the handler and leave a "ship already in flight" 409 in the bar.
    private(set) var shipStarting = false
    /// Failing ticks in a row for the ship poll. On the model, not inside
    /// `runShipPoll`, for the reason `updateGapObserved` spells out: the poll
    /// runs from both the Board and Settings and its loop restarts on every
    /// tab switch, so a loop-local counter could be zeroed indefinitely and
    /// the ceiling never reached. There is no gap CLOCK here — a ship takes
    /// nothing down, so the count alone is the signal.
    ///
    /// Which is exactly why it needs `noteBecameActive`. The update ceiling is
    /// an AND of a streak and 300 observed seconds, so a suspension can't trip
    /// it; this one is the streak alone, and two is the ceiling. Without the
    /// foreground reset, one blip before locking the phone and one blip on
    /// unlocking — two ticks that can be days apart, neither of them evidence
    /// of anything — would synthesise "✕ ship failed" over a run that was
    /// fine.
    private var shipFailureStreak = 0

    /// The app came back to the foreground.
    ///
    /// A failure recorded before a suspension says the radio was off, not that
    /// the Mac is gone, and the ship ceiling cannot tell those apart on its own
    /// (see shipFailureStreak). Called from the scene-phase transition rather
    /// than from a poll loop, because the loops are keyed on that same
    /// transition and would race it.
    func noteBecameActive() {
        shipFailureStreak = 0
    }

    /// Folds the 4s board poll's copy of the record in. The 1.2s status poll
    /// wins outright while it is running — the two race, and applying an older
    /// board response would rewind the phase on screen.
    private func ingestBoardShip(_ s: WebShipIOS?) {
        guard !shipPolling, let s else { return }
        ship = s
        if !Self.shipTerminalPhases.contains(s.phase ?? "idle") {
            // a run someone else started — the CLI, the web board. Follow it
            // just as closely. This is also how the fast poll recovers after a
            // lost-contact failure: the board poll comes back first.
            shipMessage = nil
            beginShipPolling()
        }
    }

    private func beginShipPolling() {
        // A new RUN resets the streak; a poll loop re-entering does not. Same
        // reasoning as beginUpdatePolling — see shipFailureStreak.
        shipFailureStreak = 0
        shipPolling = true
        shipPollGeneration &+= 1
    }

    /// Flips the control off in the tap handler itself — see shipStarting.
    func markShipStarting() {
        shipStarting = true
    }

    /// Fires the run. No confirmation step: the tap IS the decision, same as
    /// the update control and the web board.
    func startShipIOS(baseURL: String) async {
        defer { shipStarting = false }
        shipMessage = nil
        do {
            let resp = try await APIClient.shared.startShipIOS(baseURL: baseURL)
            // The handler writes the claim (phase preflight) BEFORE it forks
            // the shipper, so paint that phase now rather than leaving the
            // control idle until the first poll tick lands.
            let now = Int64(Date().timeIntervalSince1970 * 1000)
            ship = WebShipIOS(
                phase: resp.phase.isEmpty ? "preflight" : resp.phase,
                build: 0, error: "", startedAt: now, updatedAt: now
            )
            beginShipPolling()
        } catch {
            // A 409's text is one specific paragraph about the machine's state
            // — missing App Store Connect credentials, most often — and it is
            // the only thing worth putting on screen here.
            shipMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    /// How many consecutive failed fetches mean "the server is really gone"
    /// right now. Reads the UPDATE record because that is the only thing on the
    /// Mac that legitimately takes serve down mid-ship — see the section
    /// comment above.
    private var shipFailureCeilingNow: Int {
        let updating = updatePolling || !Self.terminalPhases.contains(update?.phase ?? "idle")
        return updating ? Self.shipFailureCeilingUpdating : Self.shipFailureCeiling
    }

    /// Whether the last ship record is recent enough to still speak for the
    /// control. abs(), not a one-sided comparison, for the same reason
    /// updateRecordFresh uses it: a future `updated_at` from clock skew would
    /// otherwise be fresh forever and pin the control at "✕ ship failed".
    private var shipRecordFresh: Bool {
        let updatedAt = ship?.updatedAt ?? 0
        return updatedAt > 0
            && abs(Date().timeIntervalSince1970 - Double(updatedAt) / 1000) < Self.shipRecentWindow
    }

    /// The entry point BoardView's ship poll task calls — see
    /// resumeUpdatePoll for why the record, not just the flag, is consulted.
    func resumeShipPoll(baseURL: String) async {
        guard shipPolling || !Self.shipTerminalPhases.contains(ship?.phase ?? "idle") else { return }
        shipPolling = true
        await runShipPoll(baseURL: baseURL)
    }

    /// The in-flight poll loop. Same structure as runUpdatePoll minus the gap
    /// clock: nothing IN a ship takes the server down, so consecutive failures
    /// are a fault to report rather than a window to ride out — with the one
    /// borrowed exception shipFailureCeilingNow covers.
    func runShipPoll(baseURL: String) async {
        shipPollEpoch &+= 1
        let epoch = shipPollEpoch
        defer { if shipPollEpoch == epoch { shipPolling = false } }
        while !Task.isCancelled && shipPolling && shipPollEpoch == epoch {
            do {
                let s = try await APIClient.shared.shipIOSStatus(baseURL: baseURL)
                ship = s
                shipFailureStreak = 0
                if Self.shipTerminalPhases.contains(s.phase ?? "idle") { return }
            } catch {
                // Cancellation is not a failure: this view is a TabView child,
                // so switching tabs mid-run cancels the fetch in flight against
                // a perfectly healthy server. `Task.isCancelled` is the ONLY
                // signal that can reach here — APIClient.perform re-wraps every
                // URLSession failure, cancellation included, as
                // KrakenError.message, so no cast can match. Don't add one.
                if Task.isCancelled { return }
                shipFailureStreak += 1
                if shipFailureStreak >= shipFailureCeilingNow {
                    // Say what is and isn't known. The shipper is a detached
                    // child of a `kraken serve` that may have died under it, so
                    // the upload could well still be running; the phone simply
                    // has no way to see it any more.
                    let now = Int64(Date().timeIntervalSince1970 * 1000)
                    ship = WebShipIOS(
                        phase: "failed", build: ship?.build ?? 0,
                        error: "kraken serve stopped answering mid-ship — the run itself may still be going; see ~/.kraken/ship-ios.log",
                        startedAt: ship?.startedAt ?? now, updatedAt: now
                    )
                    return
                }
            }
            try? await Task.sleep(for: Self.shipPollInterval)
        }
    }

    /// What the ⇧ control shows right now, derived here so the view is a pure
    /// paint. Same precedence as `updateButton` — starting, then running, then
    /// a fresh terminal record, then the idle default. Deliberately NOT "the
    /// web board's renderShipIOS() precedence": that one opens with a refusal
    /// state this control does not have, because a refusal here goes to
    /// `shipBanner`, which has room for the whole paragraph.
    var shipButton: ShipButtonState {
        let phase = ship?.phase ?? "idle"
        let running = !Self.shipTerminalPhases.contains(phase)
        let build = ship?.build ?? 0
        let suffix = build > 0 ? " b\(build)" : ""

        if shipStarting && !running {
            // the tap landed, the POST hasn't answered — the record still reads
            // terminal, so without this a second tap would earn a 409
            return ShipButtonState(glyph: "⇧", label: "starting", color: KTheme.accent, disabled: true)
        }
        if running {
            return ShipButtonState(glyph: "⇧", label: phase, color: KTheme.accent, disabled: true)
        }
        if shipRecordFresh, phase == "failed" {
            // Tapping this re-runs the ship rather than "revealing" the error,
            // which the update control has to do because its message has no
            // strip of its own. A ship's error is already on screen in full
            // twice over — the board's shipBanner, and the Mac section's own
            // message area right under the two controls — so a reveal tap here
            // would change nothing.
            return ShipButtonState(glyph: "✕", label: "ship failed", color: KTheme.counter, disabled: false)
        }
        if shipRecordFresh, phase == "done" {
            // "uploaded", never "shipped": the .ipa is at Apple and this app is
            // still the old build. See shipBanner for the rest of the sentence.
            return ShipButtonState(
                glyph: "✓", label: "uploaded" + suffix, color: KTheme.solved, disabled: false
            )
        }
        return ShipButtonState(glyph: "⇧", label: "ship iOS", color: KTheme.text2, disabled: false)
    }

    /// The one sentence the button has no room for, shown as a strip under the
    /// daemon bar while a ship is in flight or has just finished. This is where
    /// the uploaded-vs-installed distinction actually gets made in full — on
    /// the phone especially, since the app reading it is the thing being
    /// replaced, and a bare green ✓ would read as "you're on the new build".
    var shipBanner: (text: String, color: Color)? {
        // A refusal never became a run, so no record carries it. It belongs
        // here rather than in the daemon bar's one-line slot: a preflight
        // refusal is a paragraph naming which credential is missing and where
        // to set it, and truncating that to one line throws away the answer.
        if let shipMessage, let at = shipMessageAt,
           Date().timeIntervalSince(at) < Self.shipRecentWindow {
            return ("✕ " + shipMessage, KTheme.counter)
        }
        let phase = ship?.phase ?? "idle"
        let build = ship?.build ?? 0
        if !Self.shipTerminalPhases.contains(phase) {
            let named = build > 0 ? "build \(build)" : "the iOS app"
            return ("⇧ \(phase) \(named) — archiving and uploading takes several minutes", KTheme.accent)
        }
        guard shipRecordFresh else { return nil }
        if phase == "failed" {
            return ("✕ ship failed — \(ship?.error ?? "no error recorded")", KTheme.counter)
        }
        if phase == "done" {
            let named = build > 0 ? "build \(build)" : "the new build"
            return (
                "✓ \(named) uploaded to App Store Connect — not installed yet. TestFlight has to process it (a few minutes), then install it from the TestFlight app. This app is still the old build until you do.",
                KTheme.solved
            )
        }
        return nil
    }
}

/// One paint of the ⟳ update control (Settings' "Mac" section; the state
/// machine behind it stays here on the model) — see BoardViewModel.updateButton.
struct UpdateButtonState {
    let glyph: String
    let label: String
    let color: Color
    let disabled: Bool
    /// Non-nil when the control is a recorded failure: tapping surfaces this
    /// in the bar instead of starting another run. `var` among four `let`s
    /// on purpose — Swift's memberwise init defaults an Optional *var* to
    /// nil and an Optional `let` to nothing, so this is what lets five of
    /// the six construction sites omit the parameter entirely.
    var revealsError: String?
}

/// One paint of the ⇧ ship control (Settings' "Mac" section; the state machine
/// behind it stays here on the model) — see BoardViewModel.shipButton.
/// A separate type from UpdateButtonState rather than a shared one: they
/// happen to carry the same four fields today, and merging them would invite
/// the next state either one grows (the update control's gap/behind states,
/// say) onto the other, which is exactly the drift this feature is trying not
/// to have. Note what it does NOT carry: UpdateButtonState's `revealsError`.
/// That exists because an update's error has nowhere to go but the bar's
/// shared one-line slot, so a tap is what puts it there; a ship's error is
/// already on screen in `shipBanner`, and a tap that changed nothing would be
/// a dead control wearing a live one's clothes.
struct ShipButtonState {
    let glyph: String
    let label: String
    let color: Color
    let disabled: Bool
}

/// The ship poll's `.task(id:)` key — the update poll's key, for the same two
/// reasons. The generation covers a run STARTING (see shipPollGeneration). The
/// scene phase is what gets a loop RUNNING again after a suspension: a
/// suspended `.task` is not resumed, it is cancelled, so without this key
/// nothing would poll a ship that was in flight when the phone was locked.
/// The failure streak it used to also protect now lives on the model and is
/// cleared by `BoardViewModel.noteBecameActive`, not by this restart.
struct ShipPollKey: Hashable {
    let generation: Int
    let phase: ScenePhase
}

/// The update poll's `.task(id:)` key. Two independent things restart that
/// loop, and it must restart for either. The generation covers a run STARTING
/// (see updatePollGeneration); the scene phase covers the app coming back from
/// suspension, where the point is simply to have a loop again — a suspended
/// `.task` is cancelled, not paused. It is no longer about throwing the gap
/// clock away: the clock lives on the model now and survives on purpose, and
/// the minutes it was not awake for are handled by clamping each tick's
/// contribution rather than by resetting — see updateGapTickCeiling.
struct UpdatePollKey: Hashable {
    let generation: Int
    let phase: ScenePhase
}

/// The job route's `.task(id:)` key. The slug alone would be enough if the
/// route always acted immediately, but it can be HELD — a link arriving over a
/// composer waits rather than eating half-typed text (see BoardSheet.isComposer)
/// — and a held route needs something to wake it. `resume` is bumped when a
/// sheet is dismissed, which is the only moment a held link can proceed.
struct JobRouteKey: Hashable {
    let slug: String?
    let resume: Int
}

/// The surface the board's three chrome strips — daemon bar, ship banner,
/// contract banner — all sit on.
///
/// A material rather than `ThemedCard`'s glass, and that is the distinction
/// rather than an approximation of it: these three are status readouts that
/// sit still while five columns of glass cards scroll past under them, and a
/// tinted glass strip layered over a tinted glass card is two lensings of the
/// same background field with nothing between them. `ThemedCard` is for the
/// things you scroll and tap; this is for the things that stay.
///
/// `tint` colours the hairline alone, so a ship failure's strip carries its
/// own edge without the text having to fight a fill for it.
private extension View {
    func boardBarSurface(tint: Color? = nil) -> some View {
        background(.regularMaterial, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .strokeBorder((tint ?? KTheme.hairline).opacity(tint == nil ? 1 : 0.35), lineWidth: 1)
            )
    }
}

/// Whether a job sheet's NavigationStack currently has a child view pushed on
/// top of JobDetailView (today: ThreadDetailView).
///
/// The sheet auto-dismisses itself when the presented job's `state` moves on
/// (see the .onChange handlers below), which is right for the short-lived
/// forms it was built around but wrong for a pushed view meant to be sat in:
/// a live job flips working↔blocked every time zeus finishes or starts a
/// turn, and tearing the whole stack down mid-read would drop the reader back
/// on the board with no way back but re-navigating. The pushed child sets
/// this on appear / clears it on disappear, and the auto-dismiss stands down
/// while it's true — the sheet still shows the (now stale) job header, but a
/// pushed view reads live data of its own, so there's nothing stale to hide.
@Observable
final class SheetNavState {
    var childPushed = false
}

/// Every sheet the board can present, as ONE value.
///
/// This used to be several independent `.sheet` modifiers hung off one
/// presenter, and SwiftUI presents at most one sheet per presenter: whichever
/// modifier wins, the others' bindings stay set with nothing on screen. That is
/// not a cosmetic loss — the flag LATCHES. `kraken://graph` arriving while the
/// job sheet was up consumed `pendingGraph`, presented nothing, and left
/// `showingGraph == true`; the toolbar's graph button then set a `true` that
/// was already `true`, so the screen was unreachable for the rest of the
/// session. `showingDream` had the identical shape. The mirror case cost job
/// routes: a `kraken://job/<slug>` link tapped while one of those was up went
/// nowhere.
///
/// One enum behind one `.sheet(item:)` makes exclusivity STRUCTURAL rather than
/// a rule every presenter has to remember. Assigning replaces whatever is up —
/// last route wins, and nothing can latch, because there is no second flag to
/// leave set.
///
/// The two drag-presented sheets (steer / reactivate, raised by a cross-column
/// drop on the chip bar) are in here too. They share the same presenter, so
/// they shared the same defect; that a drop is hard to perform WITH a sheet
/// already up made them the least likely to hit it, not immune, and leaving
/// them outside would have meant keeping the exact modifier-stacking shape this
/// type exists to delete.
///
/// The overview's search-focus flag rides IN its case rather than beside it as
/// separate state: the sheet has three entry points and only the magnifier
/// means "I am here to search", so carrying it in the value is what keeps a
/// stale `true` from a previous magnifier tap out of a later chevron tap. It is
/// part of the `id` so switching entry points re-presents.
enum BoardSheet: Identifiable {
    case job(WebJob)
    case newJob
    case dream
    case usage
    case overview(searchFocused: Bool)
    case dragSteer(WebJob)
    case dragReactivate(WebJob)

    var id: String {
        switch self {
        case .job(let job): "job:\(job.id)"
        case .newJob: "newJob"
        case .dream: "dream"
        case .usage: "usage"
        case .overview(let focused): "overview:\(focused)"
        case .dragSteer(let job): "dragSteer:\(job.id)"
        case .dragReactivate(let job): "dragReactivate:\(job.id)"
        }
    }

    /// The presented job's board identity (`WebJob.id` — owner-qualified for
    /// a remote card), when a job sheet is what's up. The staleness
    /// auto-dismiss and the pushed-child reset both key off this.
    var jobID: String? {
        if case .job(let job) = self { return job.id }
        return nil
    }

    /// Whether this sheet holds unsaved typing.
    ///
    /// "Last route wins" is right for readers — a drawer, an overview, a job
    /// detail all cost nothing to reopen — and wrong for these three, where the
    /// thing on screen is a half-written brief, steer, or resume message that
    /// exists nowhere else. A notification tapped mid-sentence must not eat it,
    /// so a route arriving over a composer WAITS: the pending flag stays set
    /// and is consumed the moment the composer closes.
    var isComposer: Bool {
        switch self {
        case .newJob, .dragSteer, .dragReactivate: true
        case .job, .dream, .usage, .overview: false
        }
    }
}

/// The board's route stamp, in a reference box.
///
/// `kraken://job/<slug>` is the one route that cannot act immediately — on a
/// cold launch the board may not have loaded, so it retries for up to five
/// seconds. Five seconds is long enough for a SECOND route to arrive and be
/// acted on, and the job task would then present over it, silently inverting
/// "last route wins". So each consumed route stamps a new generation and the
/// job task presents only if its own stamp is still the current one.
///
/// A class rather than a plain `@State Int` because the check happens INSIDE
/// the task: a `.task` closure captures the view struct by value, so reading an
/// `Int` field there would read the value as of when the body was evaluated,
/// which is always the stamp the task started with. A reference gives the
/// closure a live read. Nothing observes it — it is only ever compared — so it
/// is deliberately not `@Observable`.
final class BoardRouteStamp {
    var generation = 0
}

/// The board's unpaired state — the pair screen (it was BoardNoServerView, the
/// "set your server" view, before pairing generalised it). Shown instead of the board
/// composition whenever no pairing is stored (a fresh install, a Log out, or
/// a 401 that cleared the record), so none of the board's polls exist while
/// it shows.
///
/// The seed field IS the stored server URL (typed, or scanned from the board's pairing QR), and
/// Pair runs `PairFlow` — a challenge and a proof per kraken the seed knows,
/// binding to the single one whose password matches. The password never
/// leaves the phone and is never stored.
struct BoardPairView: View {
    /// Switches to the Settings tab.
    let openSettings: () -> Void

    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @State private var password = ""
    @State private var isPairing = false
    @State private var status: String?
    @State private var statusIsError = false
    @State private var showingScanner = false
    @State private var tailnetAddress: String?
    @FocusState private var focused: Field?

    private enum Field { case seed, password }

    private var canPair: Bool {
        !isPairing && KrakenDefaults.isConfigured(serverURL) && !password.isEmpty
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                VStack(alignment: .leading, spacing: 6) {
                    Text("Pair this phone")
                        .font(.voice(20, weight: .semibold))
                        .foregroundStyle(KTheme.text)
                    Text(tailnetLine)
                        .font(.voice(13))
                        .foregroundStyle(tailnetAddress == nil ? KTheme.revealed : KTheme.text2)
                        .fixedSize(horizontal: false, vertical: true)
                }
                seedCard
                passwordCard
                Button("Open Settings", action: openSettings)
                    .font(.voice(13))
                    .foregroundStyle(KTheme.accent)
            }
            .padding(20)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .background { ThemeBackgroundView().ignoresSafeArea() }
        .navigationTitle("Board")
        .task { refreshTailnet() }
        .sheet(isPresented: $showingScanner) {
            PairScannerSheet { scanned in
                serverURL = scanned
                showingScanner = false
                focused = .password
            }
        }
    }

    private var seedCard: some View {
        ThemedCard {
            VStack(alignment: .leading, spacing: 10) {
                Text("Seed — any kraken on the tailnet").kLabelStyle()
                TextField(SettingsView.serverURLPlaceholder, text: $serverURL)
                    .keyboardType(.URL)
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .focused($focused, equals: .seed)
                    .font(.data(13))
                    .kField(focused: focused == .seed)
                Button {
                    showingScanner = true
                } label: {
                    Label("Scan the board's pairing code", systemImage: "qrcode.viewfinder")
                        .frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered)
                .disabled(!PairScannerView.isAvailable)
                if !PairScannerView.isAvailable {
                    Text("the camera scanner isn't available here — type the address instead")
                        .font(.voice(11))
                        .foregroundStyle(KTheme.text2)
                }
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var passwordCard: some View {
        ThemedCard {
            VStack(alignment: .leading, spacing: 10) {
                Text("Pairing password").kLabelStyle()
                SecureField("set on the board's ◉ Devices panel", text: $password)
                    .textContentType(.password)
                    .focused($focused, equals: .password)
                    .font(.data(13))
                    .kField(focused: focused == .password)
                    .submitLabel(.go)
                    .onSubmit { if canPair { pair() } }
                Button {
                    pair()
                } label: {
                    if isPairing {
                        ProgressView().frame(maxWidth: .infinity)
                    } else {
                        Label("Pair", systemImage: "link")
                            .frame(maxWidth: .infinity)
                    }
                }
                .buttonStyle(.borderedProminent)
                .disabled(!canPair)
                if let status {
                    Text(status)
                        .font(.voice(12))
                        .foregroundStyle(statusIsError ? KTheme.counter : KTheme.text2)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(14)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var tailnetLine: String {
        if let tailnetAddress {
            return "On the tailnet as \(tailnetAddress). Pick a kraken by its pairing password."
        }
        return "Not on a tailnet — turn Tailscale on, then pair. Krakens are only reachable over it."
    }

    private func refreshTailnet() {
        tailnetAddress = TailnetDetector.currentAddresses().first(where: TailnetDetector.isTailnetAddress)
    }

    private func pair() {
        let raw = serverURL.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let seed = URL(string: raw), seed.host != nil else {
            statusIsError = true
            status = "that seed isn't a URL — e.g. \(SettingsView.serverURLPlaceholder)"
            return
        }
        let pw = password
        isPairing = true
        statusIsError = false
        status = "asking each kraken…"
        refreshTailnet()
        Task {
            do {
                let record = try await PairFlow.pair(
                    seed: seed, password: pw, deviceLabel: UIDevice.current.name,
                    client: URLSessionPairClient()
                )
                try PairingSession.shared.adopt(record)
                password = ""
                status = nil
            } catch {
                statusIsError = true
                status = (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
            }
            isPairing = false
        }
    }
}

struct BoardView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    // Observed so flipping Settings' switch re-renders the columns, whose
    // `vm.jobs(in:)` reads the same key (see BoardViewModel.jobs).
    @AppStorage(BoardViewModel.hideOthersKey) private var hideOthers = false
    // The pairing gate: pairing, Log out and a 401 all change this.
    @State private var pairing = PairingSession.shared
    @Environment(\.scenePhase) private var scenePhase
    @Environment(AppRouter.self) private var router
    // Owned by KrakenApp, not by this view: Settings' "Mac" section drives the
    // same ship/update state from the other end of the tab bar, and a @State
    // here would hand it a second model. See KrakenApp.boardVM.
    @Environment(BoardViewModel.self) private var vm
    // THE board's one sheet slot. Every screen the board can raise — job
    // detail, new job, DREAM, the USAGE drawer, the overview, and the two
    // drag-raised composers — is a case of this, because a presenter can only
    // show one and independent flags latched when it didn't. See BoardSheet.
    @State private var sheet: BoardSheet?
    // The presented job's `state` as of whenever the job sheet was (re-)raised
    // — the baseline the staleness check below diffs the next polling refresh
    // against. WebJob isn't Equatable (Decodable-only), so this is captured
    // explicitly at every assignment site rather than via .onChange.
    @State private var presentedJobState: String?
    // Set by whatever JobDetailView has pushed on the job sheet's stack; see
    // SheetNavState above.
    @State private var jobSheetNav = SheetNavState()
    // Which route is the current one — see BoardRouteStamp.
    @State private var routeStamp = BoardRouteStamp()
    // Bumped when ANY sheet is dismissed (see JobRouteKey) — the case that
    // needs it is a composer closing, so the job route's `.task` re-runs and
    // picks up a link that was held back while it was open (see isComposer).
    @State private var routeResume = 0
    @State private var selectedColumn: BoardColumn = .draft

    // MARK: - drag reorder / cross-column drag state
    //
    // A single drag session (started by JobCardView.onDrag in one column's
    // list) can be dropped either back into that same list — a within-column
    // reorder — or onto the always-visible overview strip above the pager —
    // a cross-column lifecycle action. Both paths read/write this shared
    // state; see columnView, overviewCell, ReorderDropDelegate,
    // OverviewDropDelegate below.

    // Optimistic local top-to-bottom slug order per column, applied over the
    // server's own order while a drag is in flight or just after it commits.
    // Cleared once the next board poll lands (see clearSettledReorders) so
    // the server's own order takes back over without a visible flicker.
    @State private var localOrder: [BoardColumn: [String]] = [:]
    // Columns whose localOrder overlay should be dropped as soon as the next
    // poll's board data arrives (the commit succeeded — the server now
    // agrees, so the overlay has served its purpose).
    @State private var pendingReorderColumns: Set<BoardColumn> = []
    // Columns whose reorder POST is still in flight — their overlay is
    // neither settled nor abandoned, so clearSettledReorders leaves it be.
    @State private var inFlightReorderColumns: Set<BoardColumn> = []
    // Everything about the card currently mid-drag — its slug, the column it
    // started in, the strip cell under the finger — shared across both the
    // in-column and overview-strip drop targets. One value rather than three
    // loose vars so that every exit resets all of it together: SwiftUI gives
    // a `.onDrag` source no drag-end callback, so a drag released over
    // anything that isn't a drop target used to leave these set forever, and
    // the board went on rendering a gesture that was over. See
    // BoardDragState.swift.
    @State private var drag = BoardDragState()

    // body is deliberately a composition of four small expressions, not one
    // chain. It used to hang every modifier below — seven sheets, seven
    // onChanges plus an onAppear, four tasks, the toolbar — off the VStack
    // as a single expression, and Swift's constraint solver gave up on it
    // ("unable to type-check this expression in reasonable time" at this
    // declaration) once f01ce8f's graph additions landed, failing every
    // build including the TestFlight archive. The split is load-bearing:
    // each helper type-checks on its own, so hang a new sheet, task,
    // onChange or toolbar item inside the matching helper below, never as a
    // new link chained here. The composition applies innermost-first — withPolling
    // runs against boardStack, withRouting is outermost — so a new helper's
    // nesting position here IS its position in the modifier chain.
    var body: some View {
        NavigationStack {
            // A fresh install has no server URL (KrakenDefaults.serverURL is
            // empty). The whole board composition — and with it every poll
            // `.task` hung off it in withPolling — is simply not rendered
            // then, so no board request is issued until a URL is saved; the
            // @AppStorage read re-evaluates this the moment Settings writes
            // one, and the polls start on the composition's first appear.
            //
            // v7 adds the second half: a server is not enough, the phone
            // must also hold a pairing, or every board call is a 401. Until
            // it does, the pair screen stands in for the board.
            if KrakenDefaults.isConfigured(serverURL) && pairing.isPaired {
                withRouting(withSheets(withPolling(boardStack)))
            } else {
                BoardPairView {
                    router.selectedTab = .settings
                }
            }
        }
    }

    // The visible board — the stack, its chrome, and the toolbar.
    //
    // The pager is a view-aligned horizontal carousel rather than the paged
    // TabView it replaced: same one-column-per-page side-scroll (carl's
    // explicit requirement), but with the neighbouring columns peeking at
    // both edges, so the board reads as five columns you are looking along
    // rather than five unrelated screens. The page dots went with the
    // TabView — the only way to colour them was one of the global styling
    // proxies this redesign deletes wholesale, and an untinted system dot
    // row under a tinted board is worse than no dots at all. Position
    // awareness moved to the column chip bar, which was already the
    // always-visible table of contents and now also says which column you
    // are on.
    private var boardStack: some View {
        VStack(spacing: 8) {
            daemonBar
            shipBanner
            contractBanner
            columnChipBar
            columnCarousel
            // The goals drawer sits at the foot of the board, under the
            // columns rather than over them: web puts its panel in the
            // viewport's bottom-right corner because a desktop board has
            // dead space there, and a phone has none — an overlay would
            // cover the very cards the drawer is meant to sit beside.
            // Everything else about it mirrors that panel (same nesting,
            // same counts, same collapsed pill, persisted collapse state),
            // minus the CRUD, which is web-only in v1. It draws nothing at
            // all on a server with no goals, so a board that never uses the
            // feature is untouched.
            GoalsDrawer(goals: vm.board?.goals) {
                await vm.refresh(baseURL: serverURL)
            }
        }
        .padding(.top, 4)
        // `.ignoresSafeArea()` on the BACKGROUND only, never on the content:
        // the field has to run under the nav bar's and the floating tab
        // bar's glass or they lens a system background instead of this
        // screen's, while the stack itself stays inside the safe area so
        // nothing ends up hidden beneath either bar.
        .background { ThemeBackgroundView().ignoresSafeArea() }
        .navigationTitle("Board")
        .toolbar { boardToolbar }
    }

    // The board's trailing toolbar buttons.
    //
    // Plain-styled, hand-coloured glyphs before; stock toolbar buttons now.
    // The nav bar draws its own glass and takes its colour from the app's
    // `.tint`, which is the theme accent — so `.buttonStyle(.plain)` plus a
    // per-button `foregroundStyle` would opt each one back out of exactly
    // the material this redesign exists to adopt. The button set, their
    // order, their symbols and their accessibility labels are unchanged.
    @ToolbarContentBuilder
    private var boardToolbar: some ToolbarContent {
        // Search presents the EXISTING overview sheet with its search
        // field focused, rather than opening a screen of its own: that
        // sheet already lists every job flat, sectioned by column, with
        // a tap opening JobDetailView — which is the result list a
        // search would have had to build anyway.
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                sheet = .overview(searchFocused: true)
            } label: {
                Image(systemName: "magnifyingglass")
            }
            .accessibilityLabel("Search jobs")
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                sheet = .dream
            } label: {
                Image(systemName: "moon.stars")
            }
            .accessibilityLabel("Dream")
        }
        // The old graph button is gone (the Memory tab took its place in
        // RootTabView). The gauge that replaced it opens the Usage drawer, which used to be
        // a tab of its own.
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                sheet = .usage
            } label: {
                Image(systemName: "gauge.with.dots.needle.50percent")
            }
            .accessibilityLabel("Usage")
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button {
                sheet = .newJob
            } label: {
                Image(systemName: "plus")
            }
        }
    }

    // The board's three polling loops plus the drag-end backstop — everything
    // keyed on scenePhase or a poll generation.
    private func withPolling(_ content: some View) -> some View {
        content
            .task(id: scenePhase) {
                guard scenePhase == .active else { return }
                await vm.refresh(baseURL: serverURL)
                clearSettledReorders()
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(4))
                    if Task.isCancelled { break }
                    await vm.refresh(baseURL: serverURL)
                    clearSettledReorders()
                }
            }
            // Backstop for the drag-end signal: a gesture interrupted by a
            // real backgrounding may never see its item provider released
            // while the board is on screen to observe it, and coming back to
            // a board still dressed for a drag that ended minutes ago is the
            // exact symptom this change exists to remove. `.background`
            // specifically, matching KrakenApp's own scene handling — the
            // transient `.inactive` that a call banner or a control-centre
            // pull produces does NOT end the drag session, and resetting on
            // it would tear the board's drag state down with the user's
            // finger still on the card.
            .onChange(of: scenePhase) { _, phase in
                guard phase == .background, drag.isActive else { return }
                endActiveDrag(committedReorderIn: nil)
            }
            // The in-flight update poll, kept separate from the 4s board poll
            // above: it runs ~3x faster, hits a much narrower endpoint, and
            // has to survive the window where the board poll can't reach the
            // server at all. Keyed on the poll GENERATION, not on
            // `updatePolling`: a run started anywhere — here, the CLI, the
            // web board — bumps the generation, which always differs from
            // the last one this body saw, so the loop always restarts (see
            // updatePollGeneration for the frame-collision this avoids).
            // Keyed on `scenePhase` as well, matching the board poll above,
            // because a suspended `.task` is CANCELLED rather than paused —
            // without that key a run that was in flight when the phone locked
            // would come back to no poller at all. The gap clock is no longer
            // part of this: it lives on the model, survives the suspension
            // deliberately, and discounts the unwatched minutes by clamping
            // each tick (see updateGapTickCeiling and UpdatePollKey).
            .task(id: UpdatePollKey(generation: vm.updatePollGeneration, phase: scenePhase)) {
                guard scenePhase == .active else { return }
                await vm.resumeUpdatePoll(baseURL: serverURL)
            }
            // The ship poll, on its own task for the same reasons: a run can
            // last ten minutes, and the board poll is both too slow to watch it
            // and too expensive to speed up. Independent of the update poll —
            // the two runs are unrelated and can legitimately overlap.
            .task(id: ShipPollKey(generation: vm.shipPollGeneration, phase: scenePhase)) {
                guard scenePhase == .active else { return }
                await vm.resumeShipPoll(baseURL: serverURL)
            }
    }

    // The board's ONE sheet, plus the two onChanges that manage the job
    // sheet's lifetime. Every screen lives in the switch below — see
    // BoardSheet for why this is one modifier and not seven.
    private func withSheets(_ content: some View) -> some View {
        content
            .sheet(item: $sheet) { presented in
                sheetContent(presented)
            }
            // Presenting a different job (or dismissing entirely) starts the
            // stack over at JobDetailView's root, so the pushed-child flag
            // resets with it — belt and braces against an unbalanced
            // appear/disappear pair leaving auto-dismiss suppressed forever.
            // Keyed on the presented JOB, not on the sheet as a whole: raising
            // the usage drawer over nothing is not a reason to touch it.
            .onChange(of: sheet?.jobID) { jobSheetNav.childPushed = false }
            // Auto-dismiss the presented job sheet once the board's own
            // polling refresh (vm.refresh, every 4s above) shows that job's
            // `state` has moved on since it was opened — unblocked, killed,
            // marked done, etc. from another client — so the sheet stops
            // showing stale action buttons/question/detail against a job.go
            // state that no longer exists. Nested sheets (steer/reactivate,
            // presented from within JobDetailView) tear down along with
            // their dismissed parent. DraftStore-backed drafts (e.g. a
            // half-typed steer message) are untouched — this only clears
            // presentation state, never calls draftStore.clear. Suppressed
            // entirely while a child view is pushed on the sheet's stack
            // (jobSheetNav): a pushed thread view is a place to sit and read,
            // and a working↔blocked flip underneath it is routine, not a
            // reason to tear the reader's stack down (see SheetNavState).
            .onChange(of: vm.board?.jobs.first(where: { $0.id == sheet?.jobID })?.state) { _, newState in
                // `case .job` and not merely "a slug is presented": clearing
                // the slot has to be conditional on a JOB being the thing in
                // it, or a state flip could dismiss a drawer.
                guard case .job = sheet, !jobSheetNav.childPushed,
                      let presentedJobState, let newState, newState != presentedJobState else { return }
                sheet = nil
            }
    }

    @ViewBuilder
    private func sheetContent(_ presented: BoardSheet) -> some View {
        switch presented {
        case .job(let job):
            NavigationStack {
                JobDetailView(job: job, baseURL: serverURL) {
                    Task { await vm.refresh(baseURL: serverURL) }
                }
            }
            .environment(jobSheetNav)
        case .newJob:
            NavigationStack {
                NewJobSheet(baseURL: serverURL) {
                    Task { await vm.refresh(baseURL: serverURL) }
                }
            }
        case .dream:
            NavigationStack {
                DreamView(baseURL: serverURL)
            }
        case .usage:
            // UsageView carries its own NavigationStack and close button, so
            // it is presented bare rather than wrapped like the others.
            UsageView()
        case .overview(let searchFocused):
            NavigationStack {
                BoardOverviewSheet(vm: vm, baseURL: serverURL, searchFocused: searchFocused) {
                    Task { await vm.refresh(baseURL: serverURL) }
                }
            }
        case .dragSteer(let job):
            // blocked→working overview-strip drag: reuses JobDetailView's
            // SteerView with unblockFraming — same delivery as the in-detail
            // Unblock button, just reached from the strip instead.
            NavigationStack {
                SteerView(job: job, baseURL: serverURL, unblockFraming: true) {
                    sheet = nil
                    Task { await vm.refresh(baseURL: serverURL) }
                }
            }
        case .dragReactivate(let job):
            // done→working overview-strip drag: reuses ReactivateView with
            // requireText, since this entry path has no separate
            // confirmation step to catch an accidental empty resume.
            NavigationStack {
                ReactivateView(job: job, baseURL: serverURL, requireText: true) {
                    sheet = nil
                    Task { await vm.refresh(baseURL: serverURL) }
                }
            }
        }
    }

    // Deep-link routing — consuming the pending values AppRouter stamps for
    // the board.
    private func withRouting(_ content: some View) -> some View {
        content
            // Deep links (kraken://board/<column>, kraken://overview,
            // kraken://dream, kraken://usage, kraken://job/<slug>) stamp a
            // pending value on the shared
            // router; this view is the one that owns each destination, so
            // it's the one that consumes and clears it. onAppear covers a
            // route stamped before this view mounted (cold launch via the
            // -KrakenRoute launch argument); onChange covers routes arriving
            // while it's already up.
            .onAppear { consumePendingBoardRoutes() }
            .onChange(of: router.pendingBoardColumn) { consumePendingBoardRoutes() }
            .onChange(of: router.pendingOverview) { consumePendingBoardRoutes() }
            .onChange(of: router.pendingDream) { consumePendingBoardRoutes() }
            .onChange(of: router.pendingUsage) { consumePendingBoardRoutes() }
            // A composer closing releases whatever routes were held back while
            // it was open (see BoardSheet.isComposer). Routes first, so the
            // job task restarts against the final stamp; the resume bump is
            // what restarts it, since its own key can't see a sheet change.
            .onChange(of: sheet == nil) { _, dismissed in
                guard dismissed else { return }
                consumePendingBoardRoutes()
                routeResume &+= 1
            }
            .task(id: JobRouteKey(slug: router.pendingJobSlug, resume: routeResume)) {
                guard let slug = router.pendingJobSlug else { return }
                // Held, not dropped: leaving the pending slug set is what gets
                // this link presented once the composer is closed, instead of
                // the link eating a half-written brief (see isComposer).
                guard sheet?.isComposer != true else { return }
                let stamp = routeStamp.generation
                // The board may not have loaded yet (e.g. a cold launch
                // straight into a kraken://job/<slug> link) — retry briefly
                // rather than silently dropping the link.
                for _ in 0..<10 {
                    if vm.board == nil {
                        await vm.refresh(baseURL: serverURL)
                    }
                    // A kraken://job link names a LOCAL job — a remote card
                    // with the same slug belongs to another Mac.
                    if let job = vm.board?.jobs.first(where: { $0.slug == slug && !$0.isRemote }) {
                        // Up to five seconds have passed and a later route may
                        // already be on screen. Presenting now would put this
                        // OLDER link on top of it — so the stamp decides, and
                        // last route still wins (see BoardRouteStamp).
                        if stamp == routeStamp.generation { presentJob(job) }
                        break
                    }
                    try? await Task.sleep(for: .seconds(0.5))
                }
                router.pendingJobSlug = nil
            }
    }

    // Presents a job sheet and records its state as the staleness baseline
    // — see presentedJobState's doc comment and the onChange next to the
    // sheet(item:) modifier above. Assigning REPLACES whatever sheet is up,
    // which is the point: a kraken://job link tapped while the usage drawer
    // was open used to go nowhere at all.
    private func presentJob(_ job: WebJob) {
        sheet = .job(job)
        presentedJobState = job.state
    }

    // Each pending flag is cleared as it is read, and each writes the ONE
    // sheet slot — so two routes arriving in the same frame end with the last
    // one on screen rather than one presented and the others latched forever
    // (the defect BoardSheet documents). "Last route wins" is the whole rule,
    // with one exception above it: a composer is never interrupted.
    //
    // Each consumed route stamps a new generation, which is how the job
    // route's slow retry loop finds out it has been superseded. The stamp is
    // bumped per CONSUMED route rather than once per call, so that the
    // sheet-dismissal call — which usually finds nothing pending — cannot
    // invalidate a job task that is legitimately in flight.
    private func consumePendingBoardRoutes() {
        // Nothing is consumed and nothing is cleared: the flags stay set and
        // this runs again the moment the composer closes. See isComposer.
        guard sheet?.isComposer != true else { return }
        if let column = router.pendingBoardColumn {
            // Animated for the same reason the chip tap is: the carousel is a
            // scroll view, and a scroll view teleports unless told otherwise.
            withAnimation(.easeInOut(duration: 0.3)) { selectedColumn = column }
            router.pendingBoardColumn = nil
            routeStamp.generation &+= 1
        }
        if router.pendingOverview {
            sheet = .overview(searchFocused: false)
            router.pendingOverview = false
            routeStamp.generation &+= 1
        }
        if router.pendingDream {
            sheet = .dream
            router.pendingDream = false
            routeStamp.generation &+= 1
        }
        if router.pendingUsage {
            sheet = .usage
            router.pendingUsage = false
            routeStamp.generation &+= 1
        }
    }

    // Non-blocking contract-mismatch notice — never hides the board or blocks
    // actions (approved decision), just flags that the client/server pairing
    // is off so carl knows to restart k web or update the app.
    @ViewBuilder
    private var contractBanner: some View {
        let warnings = vm.contractWarnings
        if !warnings.isEmpty {
            Text(warnings.joined(separator: " · "))
                .font(.voice(11))
                .foregroundStyle(KTheme.revealed)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .boardBarSurface(tint: KTheme.revealed)
                .padding(.horizontal, 16)
        }
    }

    // 7pt LED with a soft glow — mirrors .led / .led.ok. Round rather than
    // square now: the sharp-cornered rects are what this redesign is
    // replacing, and the glow is the part that was ever load-bearing.
    private var daemonBar: some View {
        HStack(spacing: 8) {
            if let board = vm.board {
                Circle()
                    .fill(board.daemonOK ? KTheme.solved : KTheme.counter)
                    .frame(width: 7, height: 7)
                    .shadow(color: (board.daemonOK ? KTheme.solved : KTheme.counter).opacity(0.55), radius: 4)
                Text(board.daemonOK ? "daemon up" : "daemon down")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
                Spacer(minLength: 6)
                // The ⇧ ship and ⟳ update controls used to sit here. They are
                // Mac-side maintenance, not board state, so they moved to
                // Settings' "Mac" section (MacControlsSection in
                // SettingsView.swift) with their states, strings and
                // no-confirmation tap semantics intact. What stays is what
                // reads on every glance: the LED, the daemon label, the shared
                // error slot below, and the cell counter. The update poll and
                // the ship banner stay here too — the banner is board-adjacent
                // news, and BoardViewModel is shared, so a run started from
                // Settings still paints here.
                //
                // One error slot, shared — precedence lives in barMessage.
                // lineLimit is not cosmetic here: a preflight refusal embeds
                // an absolute path and `update.error` can carry git's whole
                // CombinedOutput, newlines and all, which would grow the
                // daemon bar into a paragraph.
                //
                // .middle, not .tail: update.go formats a git failure as
                // "git <argv>: <exit status>: <CombinedOutput>", so the
                // informative half is at the END. With ~30 characters of
                // room .tail shows "git merge --ff-only origin/ma…" and
                // nothing else — and unlike the web board there is no OPEN
                // LOG affordance here, so the reveal would surface none of
                // its diagnostic content at all.
                if let err = vm.barMessage {
                    Text(err)
                        .font(.data(10))
                        .foregroundStyle(KTheme.counter)
                        .lineLimit(1)
                        .truncationMode(.middle)
                }
                Text("cells \(board.liveCells)/\(board.cellCap)")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
            } else if let err = vm.errorMessage {
                Text(err).font(.data(11)).foregroundStyle(KTheme.counter)
                Spacer(minLength: 0)
            } else {
                Text("loading…").font(.data(11)).foregroundStyle(KTheme.text2)
                Spacer(minLength: 0)
            }
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 8)
        .boardBarSurface()
        .padding(.horizontal, 16)
    }

    // The ship run's own strip, under the daemon bar. It exists because the
    // sentence that matters — a finished ship means the build reached App
    // Store Connect, NOT that this phone has it — does not fit in a control
    // that has to share a bar with the daemon LED and the cell counter.
    @ViewBuilder
    private var shipBanner: some View {
        if let banner = vm.shipBanner {
            Text(banner.text)
                .font(.voice(11))
                .foregroundStyle(banner.color)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 14)
                .padding(.vertical, 8)
                .boardBarSurface(tint: banner.color)
                .padding(.horizontal, 16)
        }
    }

    // Always-visible table of contents, now the capsule chip bar: one chip
    // per column so the whole board's shape is readable without swiping the
    // carousel. It carries four jobs at once, all of which survive the
    // restyle — tap to jump, the current-column highlight (which is also
    // what replaces the departed page dots), the attention dot for a column
    // holding a question or a health string, and the drop target for every
    // legal cross-column drag.
    //
    // THE BAR HAS TWO SHAPES, and which one is showing is decided by whether
    // a card is in the air. Six capsules at reading size measure ~465-497pt
    // against a phone's ~346-402pt, so at rest the bar scrolls and follows
    // the current column (see `chipScroller`). That is fine for reading and
    // fatal for dropping: `.onDrop` is attached per chip, nothing auto-
    // scrolls a plain ScrollView while a drag is in flight, and a target you
    // cannot bring on screen with a card in your hand is a target that does
    // not exist. Centering on the source column happens to keep every legal
    // pair's target near the middle today, but that is an accident of the
    // current pair table and scroll offset, not a guarantee. So for the
    // duration of a drag the bar becomes the old overview strip's geometry:
    // six equal-width compact cells, all six on screen by construction,
    // every legal pair reachable without letting go. The pre-redesign strip
    // used exactly this geometry, which is the proof those six fit.
    //
    // The chevron is outside both shapes and moves in neither: it is one of
    // three ways into the overview sheet (see BoardOverviewSheet's doc
    // comment), and an entry point that can be scrolled off screen — or that
    // vanishes when the bar changes shape — is one that has been quietly
    // removed.
    private var columnChipBar: some View {
        HStack(spacing: 8) {
            if drag.isActive {
                dragTargetStrip
            } else {
                chipScroller
            }
            Button {
                sheet = .overview(searchFocused: false)
            } label: {
                Image(systemName: "chevron.down")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(KTheme.text2)
                    .padding(9)
                    .background(.regularMaterial, in: Circle())
                    .overlay(Circle().strokeBorder(KTheme.hairline, lineWidth: 1))
            }
            .buttonStyle(.plain)
            .padding(.trailing, 16)
        }
        .animation(.easeInOut(duration: 0.18), value: drag.isActive)
    }

    /// The resting shape: capsules that scroll, and that FOLLOW the current
    /// column. The follow is not a nicety — this bar is what replaced the
    /// pager's dots, so a lit chip that has scrolled out of sight leaves the
    /// board with no position indicator at all, and does so exactly when the
    /// user is furthest from home. Re-centred on every column change, and on
    /// appear, which is also what re-centres it when a drag ends and the
    /// strip hands the bar back.
    private var chipScroller: some View {
        ScrollViewReader { proxy in
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    ForEach(BoardColumn.allCases) { column in
                        overviewCell(column, compact: false)
                            .id(column)
                    }
                }
                .padding(.vertical, 2)
            }
            .contentMargins(.horizontal, 16, for: .scrollContent)
            .onAppear { proxy.scrollTo(selectedColumn, anchor: .center) }
            .onChange(of: selectedColumn) { _, column in
                withAnimation(.easeInOut(duration: 0.25)) {
                    proxy.scrollTo(column, anchor: .center)
                }
            }
        }
    }

    /// The drag shape: the pre-redesign overview strip's geometry, in the new
    /// idiom. Six equal-width cells in a plain HStack — no scroll view, so
    /// every one of the nine legal pairs has its target under the finger from
    /// the moment the card lifts.
    private var dragTargetStrip: some View {
        HStack(spacing: 4) {
            ForEach(BoardColumn.allCases) { column in
                overviewCell(column, compact: true)
            }
        }
        .padding(.leading, 16)
    }

    /// One column's cell, in whichever shape the bar is currently wearing.
    /// Both shapes carry the same four signals and the same drop delegate —
    /// `compact` changes the geometry and nothing else, so a drag cannot
    /// silently lose a target or a badge on the way into strip mode.
    private func overviewCell(_ column: BoardColumn, compact: Bool) -> some View {
        let count = vm.jobs(in: column).count
        let flagged = vm.hasFlag(in: column)
        let isCurrent = selectedColumn == column
        let isLegalTarget = drag.sourceColumn.map { isLegalCrossColumnDrop(from: $0, to: column) } ?? false
        let isHovered = drag.hoveredColumn == column
        // Three signals share one cell, so each gets its own channel and they
        // compose rather than overwrite: the FILL says where you are (and,
        // while a drag hovers, that the finger is here), the STROKE says this
        // cell will accept the card in flight, and the dot says the column
        // wants attention. A hovered cell therefore still reads as the
        // current one, and a legal target still reads as flagged.
        let fill: Color = isHovered
            ? KTheme.accent.opacity(0.30)
            : (isCurrent ? KTheme.accent.opacity(0.16) : Color.clear)
        let stroke: Color = isLegalTarget
            ? KTheme.accent
            : (isCurrent ? KTheme.accent.opacity(0.5) : KTheme.hairline)
        // A capsule reads right on a chip sized to its text; on the strip's
        // squat equal-width cell the same capsule becomes a lozenge, so that
        // shape is a rounded rect. One erased value so the fill and the
        // stroke can never disagree about which outline they are drawing.
        let shape: AnyShape = compact
            ? AnyShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
            : AnyShape(Capsule())
        return Button {
            // Animated, because the carousel it drives is a scroll view and a
            // scroll view teleports unless told otherwise — the paged TabView
            // this replaced animated its jumps for free.
            withAnimation(.easeInOut(duration: 0.3)) { selectedColumn = column }
        } label: {
            Group {
                if compact {
                    VStack(spacing: 2) {
                        HStack(spacing: 3) {
                            Text(column.shortTitle)
                                .font(.data(9, weight: .semibold))
                                .kerning(0.5)
                                .foregroundStyle(isCurrent ? KTheme.accent : KTheme.text2)
                                .lineLimit(1)
                                .minimumScaleFactor(0.8)
                            if flagged {
                                Circle().fill(KTheme.counter).frame(width: 5, height: 5)
                            }
                        }
                        Text("\(count)")
                            .font(.data(13, weight: .semibold))
                            .foregroundStyle(count > 0 ? KTheme.accent : KTheme.text2)
                    }
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, 5)
                } else {
                    HStack(spacing: 5) {
                        Text(column.shortTitle)
                            .font(.data(11, weight: .semibold))
                            .kerning(0.5)
                            .foregroundStyle(isCurrent ? KTheme.accent : KTheme.text2)
                            .lineLimit(1)
                            .minimumScaleFactor(0.8)
                        Text("\(count)")
                            .font(.data(11, weight: .bold))
                            .foregroundStyle(count > 0 ? KTheme.accent : KTheme.text2)
                        if flagged {
                            Circle().fill(KTheme.counter).frame(width: 5, height: 5)
                        }
                    }
                    .padding(.horizontal, 12)
                    .padding(.vertical, 7)
                }
            }
            .background(fill, in: shape)
            .overlay(shape.stroke(stroke, lineWidth: isLegalTarget && isHovered ? 2 : 1))
        }
        .buttonStyle(.plain)
        .onDrop(of: [.text], delegate: OverviewDropDelegate(
            target: column,
            drag: $drag,
            isLegal: isLegalCrossColumnDrop,
            onDrop: performCrossColumnDrop,
            onDragFinished: endActiveDrag
        ))
    }

    // The five columns as one view-aligned carousel — the paged TabView's
    // replacement. `containerRelativeFrame` is what keeps it one column per
    // page (each page is exactly the container's width) and `contentMargins`
    // is what lets the neighbours peek; `.viewAligned` is what makes a swipe
    // land on a column rather than anywhere in between.
    //
    // Both drag paths cross this scroll view and both still work. A
    // within-column reorder happens inside one page's vertical stack, whose
    // drop targets are unchanged; a cross-column drag leaves the carousel
    // entirely for the chip bar above it, which is not a scroll target at
    // all.
    private var columnCarousel: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            LazyHStack(alignment: .top, spacing: 12) {
                ForEach(BoardColumn.allCases) { column in
                    columnView(column)
                        .containerRelativeFrame(.horizontal)
                }
            }
            .scrollTargetLayout()
        }
        .scrollTargetBehavior(.viewAligned)
        .contentMargins(.horizontal, 20, for: .scrollContent)
        .scrollPosition(id: carouselColumn, anchor: .center)
    }

    /// `selectedColumn` as the optional identity `scrollPosition(id:)` wants.
    /// Optional in both directions: the scroll view reports `nil` while the
    /// content is mid-flight between two pages, and writing that back would
    /// mean the chip bar's highlight — and every deep link's target — losing
    /// track of the column on every single swipe. The identity is the raw
    /// value because that is `BoardColumn.id`, which is what the `ForEach`
    /// above tags each page with.
    private var carouselColumn: Binding<String?> {
        Binding(
            get: { selectedColumn.rawValue },
            set: { raw in
                guard let raw, let column = BoardColumn(rawValue: raw), column != selectedColumn else { return }
                selectedColumn = column
            }
        )
    }

    /// The tap/drag hit region of a job card: `ThemedCard`'s own surface,
    /// corner radius and style included, so the target is exactly the glass
    /// the reader sees rather than its bounding box.
    private var cardTapShape: RoundedRectangle {
        RoundedRectangle(cornerRadius: 24, style: .continuous)
    }

    private func columnView(_ column: BoardColumn) -> some View {
        let jobs = orderedJobs(column)
        return ScrollView {
            LazyVStack(spacing: 10) {
                HStack(alignment: .lastTextBaseline) {
                    Text(column.title)
                        .kLabelStyle(size: 11)
                    Spacer()
                    Text("\(jobs.count)")
                        .font(.data(11))
                        .foregroundStyle(jobs.isEmpty ? KTheme.text2 : KTheme.accent)
                }
                .padding(.horizontal, 4)
                .padding(.top, 2)

                if jobs.isEmpty {
                    Text("·")
                        .font(.data(11))
                        .foregroundStyle(KTheme.text2.opacity(0.5))
                        .padding(.top, 40)
                } else {
                    Group {
                        ForEach(jobs) { job in
                            // Archived cards aren't draggable: the server
                            // rejects a reorder of the "archived" column,
                            // and no cross-column drop has an archived
                            // source (mirrors the web board, where archived
                            // cards render without draggable at all).
                            //
                            // Remote (owned) cards aren't draggable either:
                            // their order and lifecycle belong to another
                            // Mac, and a drop would act on a LOCAL slug.
                            if column == .archived || job.isRemote {
                                JobCardView(job: job)
                                    // See the non-archived branch below for
                                    // why the shape is spelled out.
                                    .contentShape(cardTapShape)
                                    .onTapGesture { presentJob(job) }
                            } else {
                                JobCardView(job: job)
                                    .opacity(drag.slug == job.slug ? 0.4 : 1.0)
                                    // The WHOLE card surface opens the detail
                                    // sheet, not just the glyphs and text runs
                                    // inside it. Without this the tap only
                                    // lands on the card's own subviews — the
                                    // gaps between the title row, the chips and
                                    // the card's 14pt padding are transparent,
                                    // and a transparent region is not hit-
                                    // tested, so half the card read as dead.
                                    // `.contentShape` with the card's own
                                    // rounded rect makes the surface the target
                                    // and stops the corners claiming taps the
                                    // card does not visually cover.
                                    //
                                    // It sits ABOVE `.onDrag`, so the drag
                                    // source gets the same shape and the two
                                    // gestures keep coexisting exactly as
                                    // before: SwiftUI hands a press to the tap
                                    // and a press-and-move to the drag, and
                                    // widening the hit region changes which
                                    // pixels are live, not which gesture wins.
                                    // The PR chips inside are `Link`s and stay
                                    // Links — a child's own hit region is
                                    // tested first, so they still open Safari.
                                    .contentShape(cardTapShape)
                                    .onTapGesture { presentJob(job) }
                                    // The item provider is the drag session's
                                    // lifetime: UIKit releases it when the
                                    // session ends however it ends, and that
                                    // release is the only drag-end signal a
                                    // SwiftUI drag source gets. `dragEnding`
                                    // carries it in a registration rather
                                    // than in an NSItemProvider subclass's
                                    // deinit, for reasons worth reading
                                    // before changing (BoardDragState.swift).
                                    // The token captured here is what makes a
                                    // late signal — one arriving after this
                                    // drag already dropped, possibly after
                                    // another has begun — a no-op instead of
                                    // a gesture-killer.
                                    .onDrag {
                                        let generation = drag.begin(slug: job.slug, column: column)
                                        return .dragEnding(slug: job.slug) {
                                            endDrag(generation: generation)
                                        }
                                    }
                                    .onDrop(of: [.text], delegate: ReorderDropDelegate(
                                        item: job.slug,
                                        column: column,
                                        slugs: slugsBinding(for: column),
                                        drag: $drag,
                                        onCommit: commitReorder,
                                        onDragFinished: endActiveDrag
                                    ))
                            }
                        }
                    }
                }
            }
            .padding(.bottom, 8)
        }
        .scrollIndicators(.hidden)
        .refreshable {
            await vm.refresh(baseURL: serverURL)
            clearSettledReorders()
        }
    }

    // MARK: - drag reorder / cross-column drag helpers

    /// `vm.jobs(in:)` server order, overridden by `localOrder`'s optimistic
    /// slug order where one is set (mid-drag or just after a commit — see
    /// localOrder's doc comment). Jobs absent from the overlay (arrived from
    /// the server after the drag started) go on top, in server order —
    /// matching the server's own unlisted-first rule (jobs/order.go
    /// ApplyColumnOrder), so nothing jumps when the overlay hands off.
    private func orderedJobs(_ column: BoardColumn) -> [WebJob] {
        let serverJobs = vm.jobs(in: column)
        guard let overlay = localOrder[column] else { return serverJobs }
        // Keyed by board identity, not slug: a remote card can share a local
        // card's slug, and `uniqueKeysWithValues` traps on a duplicate key.
        // The overlay only ever holds LOCAL slugs (remote cards never drag),
        // and a local non-archived card's identity is its slug.
        var byID = Dictionary(serverJobs.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        var listed: [WebJob] = []
        for slug in overlay {
            if let job = byID.removeValue(forKey: slug) { listed.append(job) }
        }
        let unlisted = serverJobs.filter { byID[$0.id] != nil }
        return unlisted + listed
    }

    /// A live-editable binding over one column's slug order, seeded from the
    /// current displayed order (overlay if present, else server order) on
    /// first read. ReorderDropDelegate mutates this directly as a drag
    /// crosses other cards.
    private func slugsBinding(for column: BoardColumn) -> Binding<[String]> {
        Binding(
            // Local cards only: /api/reorder orders THIS Mac's column, and a
            // remote slug in it would be a name the server does not have.
            get: { localOrder[column] ?? orderedJobs(column).filter { !$0.isRemote }.map(\.slug) },
            set: { localOrder[column] = $0 }
        )
    }

    /// Called right after each board refresh: drops the optimistic overlay
    /// for any column whose reorder commit is settled (the poll now reflects
    /// it), and for any column whose overlay was orphaned by a drag that
    /// never dropped (cancelled/loose-ended drags get no callback, so their
    /// live-swapped order would otherwise shadow the server's forever). A
    /// column mid-drag right now — `drag.sourceColumn` — is left alone.
    /// This is now a second line of defence rather than the only cleanup: a
    /// drag that ends without dropping resets its own overlay immediately
    /// (see the two drag-end helpers below), instead of stranding one here
    /// forever.
    private func clearSettledReorders() {
        guard !localOrder.isEmpty else { return }
        for column in localOrder.keys {
            if pendingReorderColumns.contains(column) {
                localOrder[column] = nil
                pendingReorderColumns.remove(column)
            } else if !inFlightReorderColumns.contains(column) && column != drag.sourceColumn {
                localOrder[column] = nil
            }
        }
    }

    /// Ends whatever drag is live without needing its generation — the exit
    /// for callers that are by definition handling the current drag: both
    /// delegates' `performDrop`, and the scene-phase backstop.
    ///
    /// `committedColumn` is the column whose order the caller is about to
    /// persist, or nil for an end that commits no order at all: any OTHER
    /// column's overlay is a live-swap that never became real, so it is
    /// dropped here and the list snaps back to the server's order rather than
    /// showing an order nobody committed until the next poll. Named as a
    /// column rather than a bool because the committing delegate belongs to
    /// the column it dropped ON, which is not necessarily the one the drag
    /// started in.
    private func endActiveDrag(committedReorderIn committedColumn: BoardColumn?) {
        let column = drag.sourceColumn
        drag.endActive()
        guard let column, column != committedColumn else { return }
        dropUncommittedOverlay(in: column)
    }

    /// Ends the drag identified by `generation`, if it is still the live one
    /// — the guarded twin of `endActiveDrag`, and the mirror of
    /// `BoardDragState`'s own `end(generation:)` / `endActive()` pair.
    ///
    /// Its only caller is the item provider's release callback (see
    /// `NSItemProvider.dragEnding`), which is the path with no drop behind
    /// it, so there is never a commit to preserve. A stale token means the
    /// drag already ended — by a drop, or by the scene-phase backstop — and
    /// possibly that a *new* drag is in flight, so nothing is touched.
    private func endDrag(generation: Int) {
        let column = drag.sourceColumn
        guard drag.end(generation: generation), let column else { return }
        dropUncommittedOverlay(in: column)
    }

    /// Drops `column`'s optimistic overlay unless it belongs to a commit
    /// that is still settling — in flight, or waiting for the poll that
    /// confirms it. Those overlays are the server's order-to-be, not a
    /// half-finished gesture's.
    private func dropUncommittedOverlay(in column: BoardColumn) {
        guard !inFlightReorderColumns.contains(column),
              !pendingReorderColumns.contains(column) else { return }
        localOrder[column] = nil
    }

    /// Persists a within-column reorder. A no-op drop (order unchanged)
    /// skips the round-trip and just drops any overlay the drag's
    /// live-swaps left behind. The column is marked in-flight before the
    /// POST launches so a poll landing mid-flight can't mistake the
    /// overlay for an abandoned drag's; on success it graduates to
    /// pending-settle (cleared on the next poll), on error everything
    /// reverts to the server's order immediately.
    private func commitReorder(column: BoardColumn, slugs: [String]) {
        guard slugs != vm.jobs(in: column).filter({ !$0.isRemote }).map(\.slug) else {
            localOrder[column] = nil
            return
        }
        localOrder[column] = slugs
        inFlightReorderColumns.insert(column)
        Task {
            do {
                try await APIClient.shared.reorderJob(baseURL: serverURL, column: column.rawValue, slugs: slugs)
                inFlightReorderColumns.remove(column)
                pendingReorderColumns.insert(column)
            } catch {
                inFlightReorderColumns.remove(column)
                localOrder[column] = nil
                vm.errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    /// The legal overview-strip lifecycle drops, keyed by (source, target)
    /// column — see the slice brief for the exact action each pair maps to.
    private func isLegalCrossColumnDrop(from: BoardColumn, to: BoardColumn) -> Bool {
        switch (from, to) {
        case (.draft, .working), (.blocked, .done),
             (.working, .blocked), (.blocked, .working),
             (.done, .blocked), (.done, .working), (.done, .archived):
            return true
        default:
            return false
        }
    }

    /// Executes (or, for the two cases needing user input, presents a sheet
    /// for) the lifecycle action for a legal overview-strip drop. `slug`'s
    /// WebJob is looked up fresh from the board rather than threaded through
    /// the drag, since drag state only ever carried the slug.
    private func performCrossColumnDrop(from: BoardColumn, to: BoardColumn, slug: String) {
        // Only local cards drag, so the dragged slug names a local job.
        let job = vm.board?.jobs.first { $0.slug == slug && !$0.isRemote }
        switch (from, to) {
        // Draft → working IS the submit: one POST that starts the cell and
        // only answers once it is up, so this drop can hold for several
        // seconds. `runBoardAction` already refreshes on success and surfaces
        // a 409 (empty spec, unresolvable repo) as the board error.
        case (.draft, .working):
            runBoardAction { try await APIClient.shared.queueJob(baseURL: serverURL, slug: slug) }
        case (.blocked, .done):
            runBoardAction { try await APIClient.shared.markDone(baseURL: serverURL, slug: slug) }
        case (.working, .blocked):
            runBoardAction { try await APIClient.shared.stopJob(baseURL: serverURL, slug: slug) }
        // The two sheet-presenting drops are the only ones that need the
        // WebJob itself rather than just its slug, and the only ones that
        // would otherwise fail in silence: a job archived or killed from
        // another client mid-drag is gone from vm.board by the time the
        // finger comes up, and no sheet appearing is indistinguishable from
        // an action that ran and was reverted. Every other case fires an API
        // call that surfaces its own error.
        case (.blocked, .working):
            if let job { sheet = .dragSteer(job) } else { vm.errorMessage = missingJobMessage(slug) }
        case (.done, .blocked):
            runBoardAction { try await APIClient.shared.reactivateJob(baseURL: serverURL, slug: slug, text: "") }
        // (the second of the two sheet-presenting drops — see above)
        case (.done, .working):
            if let job { sheet = .dragReactivate(job) } else { vm.errorMessage = missingJobMessage(slug) }
        case (.done, .archived):
            runBoardAction { try await APIClient.shared.archiveJob(baseURL: serverURL, slug: slug) }
        default:
            break
        }
    }

    private func missingJobMessage(_ slug: String) -> String {
        "\(slug) is no longer on the board"
    }

    private func runBoardAction(_ action: @escaping () async throws -> Void) {
        Task {
            do {
                try await action()
                await vm.refresh(baseURL: serverURL)
            } catch {
                vm.errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}

/// Drives a within-column drag reorder: as the dragged card hovers over
/// another card in the same LazyVStack, live-swaps their positions in
/// `slugs` so the list visually reorders during the drag itself; the final
/// `slugs` value at drop time is what gets committed to the server.
private struct ReorderDropDelegate: DropDelegate {
    let item: String
    let column: BoardColumn
    let slugs: Binding<[String]>
    let drag: Binding<BoardDragState>
    let onCommit: (BoardColumn, [String]) -> Void
    let onDragFinished: (_ committedReorderIn: BoardColumn?) -> Void

    func dropEntered(info: DropInfo) {
        guard let dragging = drag.wrappedValue.slug, dragging != item,
              let from = slugs.wrappedValue.firstIndex(of: dragging),
              let to = slugs.wrappedValue.firstIndex(of: item) else { return }
        if slugs.wrappedValue[to] != dragging {
            slugs.wrappedValue.move(fromOffsets: IndexSet(integer: from), toOffset: to > from ? to + 1 : to)
        }
    }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        DropProposal(operation: .move)
    }

    // Naming the column is what protects the write: onCommit is about to
    // put this column's order into localOrder, and onDragFinished leaves
    // exactly that column's overlay alone. Ending before committing keeps
    // the reset from ever seeing the committed value at all.
    func performDrop(info: DropInfo) -> Bool {
        let finalOrder = slugs.wrappedValue
        onDragFinished(column)
        onCommit(column, finalOrder)
        return true
    }
}

/// Drives an overview-strip lifecycle drop: highlights the cell while a
/// legal drag hovers it, and fires the mapped action on drop. Illegal
/// targets never highlight and their drop is a no-op (returns false so the
/// card visually snaps back).
private struct OverviewDropDelegate: DropDelegate {
    let target: BoardColumn
    let drag: Binding<BoardDragState>
    let isLegal: (BoardColumn, BoardColumn) -> Bool
    let onDrop: (BoardColumn, BoardColumn, String) -> Void
    let onDragFinished: (_ committedReorderIn: BoardColumn?) -> Void

    func dropEntered(info: DropInfo) {
        guard let source = drag.wrappedValue.sourceColumn, isLegal(source, target) else { return }
        drag.wrappedValue.hover(target)
    }

    func dropExited(info: DropInfo) {
        if drag.wrappedValue.hoveredColumn == target { drag.wrappedValue.hover(nil) }
    }

    func dropUpdated(info: DropInfo) -> DropProposal? {
        guard let source = drag.wrappedValue.sourceColumn, isLegal(source, target) else {
            return DropProposal(operation: .forbidden)
        }
        return DropProposal(operation: .move)
    }

    // The drag ends on every path this method has, legal or not — it used to
    // return through the guard below without clearing anything, leaving the
    // source card dim and the whole strip outlined with no drag in flight.
    // In practice UIKit does not deliver performDrop at all once dropUpdated
    // has answered `.forbidden`, so an illegal drop is really reset by the
    // item provider's release a runloop hop later; the unconditional reset
    // here is defence for the case where it IS delivered, not the mechanism
    // the illegal path depends on.
    func performDrop(info: DropInfo) -> Bool {
        let source = drag.wrappedValue.sourceColumn
        let slug = drag.wrappedValue.slug
        // nil: a cross-column drop commits no column's order, so any
        // live-swapping the card did on its way to the strip is rolled back
        // with the rest of the drag.
        onDragFinished(nil)
        guard let source, let slug, isLegal(source, target) else { return false }
        onDrop(source, target, slug)
        return true
    }
}

/// The board card, laid out in the same bands as the web board's (see
/// `cardHTML` in web/static/app.js — the two are meant to read as one design,
/// so a change to either belongs in both). WEB'S BAND NUMBERS ARE KEPT, gaps
/// included, because the numbers are shared vocabulary across app.js,
/// style.css and this file — renumbering them here would make a bare
/// "band 4" resolve to two different things depending on which file you were
/// last reading.
///
///   1. IDENTITY — title (+ "as <slug>" when an archived job was renamed), the
///      ⚑ pending-question flag, repo, state · cell.
///   2. CHIPS — one wrapped row directly under the identity block, and the
///      card's inventory surface: crew, branches, then a bare count for each of
///      questions, artifacts and feedback. Nothing here names an individual
///      thing except a branch. Everything the counts stand for is one tap away
///      in the job sheet — that is the rule this row exists to enforce, and it
///      is why the per-teammate roster list that used to sit at the foot of
///      this card is gone: `CrewChipsView` already counts the same roster, and
///      two renderings of it was the card's only outright duplication.
///   3. STATUS — health, err, then the poll countdown WITH its stated reason.
///      Those two are adjacent on purpose: web's band 3 exists partly to undo
///      a layout that had them nine rows apart, and this card used to
///      reproduce that split (chip on the state row, reason down here) while
///      claiming to be the same design.
///   5. PRS — their own row at the foot. A PR is the job's OUTPUT rather than
///      part of its inventory, it is the one chip that leaves the app, and it
///      is the only one naming a specific external thing — so it sits apart
///      from band 2 rather than inside it.
///
/// No band 4 (NEEDS YOU) and no band 6 (ACTIONS) on phone. A pending question
/// is the ⚑ flag in band 1 and is answered in the job sheet. Of web's three
/// action buttons, retry and reactivate are sheet actions here; there is no
/// tmux equivalent anywhere in this app, because that button asks the server
/// to raise a Ghostty pane on the Mac and a phone has nothing to raise.
private struct JobCardView: View {
    let job: WebJob

    private var hasPendingQuestion: Bool { job.question != nil }

    /// Mirrors web's `settledJob`: a done or archived job's open questions are
    /// no longer something owed, so the chip is suppressed for it exactly as
    /// the board suppresses `docQuestionsChipHTML`.
    private var isSettled: Bool { job.archived == true || job.state == "done" }

    private var openQuestions: [WebDocQuestion] {
        isSettled ? [] : (job.docQuestions ?? []).filter { !$0.isAnswered }
    }

    /// The ROI badge is a BLOCKED-card control: on a working, draft, done or
    /// archived card it would answer a question that card doesn't pose,
    /// and an estimate left from an earlier block would read as a current fact.
    ///
    /// "Reads as blocked" is `BoardTheme.column(for:)`, not `state == "blocked"`
    /// — the same call that decides which column this card is sitting in, so a
    /// job whose non-empty `health` files it under BLOCKED despite its state
    /// gets the badge too. Web derives its own column the same way
    /// (`columnFor(job.health ? "blocked" : job.state)` in App.tsx), which is
    /// what keeps the two boards showing the badge on the same cards.
    ///
    /// The `roi != nil` half is `LoadBadge`'s OWN draw condition, repeated here
    /// on purpose: this must be at least as strict as what the badge actually
    /// draws, or the card gets a child that renders nothing while the enclosing
    /// VStack still pays its 6pt gap — verbatim the failure `hasChipRow` below
    /// spells out for the chip row. Web's `loadChipVisible` calls the same
    /// `loadROI` its chip does, for the same reason.
    private var showsLoadBadge: Bool {
        BoardTheme.column(for: job) == .blocked && job.load?.roi != nil
    }

    /// Must be at least as strict as every chip's own render condition, or the
    /// row exists with nothing in it and the enclosing VStack pays its 6pt gap
    /// twice for a band you cannot see. The zeus test is `isEmpty == false`
    /// rather than `!= nil` for exactly that reason: `CrewChipsView` draws
    /// nothing for an empty-string status, which `!= nil` would let through.
    /// Web's `crewChipsHTML` gets this free — `j.zeus_status` is falsy for
    /// both, so its `!j.zeus_status` guard fires either way — and
    /// JobDetailView's Roster section already spells it this way.
    /// The goals this card draws a chip for, in link order: the `goals`
    /// array whenever the server sent one, and otherwise the single `goal`
    /// key a server predating multi-goal links carries alone. Presence of
    /// the array — not its contents — is what decides, so a drifted `goals`
    /// that decoded to an empty list draws no chip rather than falling back
    /// to a `goal` the same payload may have drifted too; web's JobCard
    /// spells the identical `job.goals ?? (job.goal ? [job.goal] : [])`, and
    /// a card that reads differently on the two clients is the failure this
    /// avoids. Chips are cosmetic either way — see WebJobGoalList.
    private var cardGoals: [WebJobGoal] {
        if let items = job.goals?.items { return items }
        return job.goal.map { [$0] } ?? []
    }

    private var hasChipRow: Bool {
        job.zeusStatus?.isEmpty == false || !(job.roster ?? []).isEmpty
            || !(job.threads ?? []).isEmpty || !openQuestions.isEmpty
            || !(job.artifacts ?? []).isEmpty || !(job.feedback ?? []).isEmpty
            // `cardGoals` IS the chips' own draw condition — the row below
            // draws exactly one GoalChip per element of it — so this test is
            // neither weaker nor stricter by construction rather than by
            // agreement. The chip draws for any linked goal, including a
            // done one (muted) and one whose text is empty.
            || !cardGoals.isEmpty
            // `isEmpty == false` for the zeusStatus reason above: the wire's
            // omitempty never sends "", but StageChip's own draw condition
            // guards it anyway, and this row test must be exactly as strict.
            || job.stage?.isEmpty == false
    }

    var body: some View {
        ThemedCard {
            cardBody
                .padding(14)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        // A remote card is outlined dashed — the web board's owned-card
        // treatment — so it reads as "another Mac's" before the chip is read.
        .overlay {
            if job.isRemote {
                RoundedRectangle(cornerRadius: 24, style: .continuous)
                    .strokeBorder(KTheme.text2.opacity(0.55), style: StrokeStyle(lineWidth: 1, dash: [5, 4]))
            }
        }
    }

    /// The owner chip (`label · operator`) and, for a public remote cell, a
    /// `public` tag — the two facts that say whose card this is and whether
    /// the phone may drive it.
    @ViewBuilder
    private var ownerRow: some View {
        if let owner = job.owner {
            HStack(spacing: 6) {
                Text(owner.chipText)
                    .font(.data(10, weight: .semibold))
                    .foregroundStyle(owner.online ? KTheme.accent : KTheme.text2)
                    .padding(.horizontal, 7).padding(.vertical, 2)
                    .overlay(Capsule().stroke(KTheme.accent.opacity(owner.online ? 0.6 : 0.25), lineWidth: 1))
                if job.remoteMode == .publicCell {
                    Text("public")
                        .font(.data(10))
                        .foregroundStyle(KTheme.solved)
                }
                if !owner.online {
                    Text("offline")
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                }
            }
        }
    }

    // Split out of `body` so the card is one expression the type-checker can
    // chew alone — the same discipline BoardView.body's four-helper split
    // exists for, and this stack has grown to ten conditional children.
    private var cardBody: some View {
        VStack(alignment: .leading, spacing: 7) {
            HStack(spacing: 6) {
                // The job colour, and ONLY the job colour: a leading capsule
                // beside the title rather than the flush left edge-bar
                // `cardBackground(barColor:)` used to draw. Same channel,
                // same source (`BoardTheme.jobColor`, which returns nil for
                // an unrecognized name and takes the capsule with it), read
                // against a rounded card that has no square edge to paint.
                if let bar = BoardTheme.jobColor(job.color) {
                    Capsule()
                        .fill(bar)
                        .frame(width: 4, height: 15)
                }
                Text(job.title)
                    .font(.data(15, weight: .semibold))
                    .foregroundStyle(KTheme.text)
                if job.archived == true, job.title != job.slug {
                    Text("as \(job.slug)")
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                }
                Spacer(minLength: 0)
                if hasPendingQuestion {
                    Image(systemName: "flag.fill")
                        .foregroundStyle(KTheme.counter)
                        .font(.voice(10))
                }
            }
            Text(job.repo)
                .font(.data(11))
                .foregroundStyle(KTheme.text2)
            ownerRow
            HStack(spacing: 8) {
                StateCapsule(state: job.state)
                if job.live, let cell = job.cell {
                    Text("· \(cell)").font(.data(11)).foregroundStyle(KTheme.text2)
                }
            }
            // The triage badge, on its own line directly under the state row.
            // It answers "is this one worth unblocking right now", which is the
            // first question a blocked card poses, so it sits above the counts
            // rather than wrapping in among them where CardChipFlow could push
            // it to a second line behind a crew.
            if showsLoadBadge, let load = job.load {
                LoadBadge(load: load)
            }
            // band 2. Every chip here is passive display — no tap target and no
            // long-press menu, since both gestures already contend with the
            // card's drag-to-reorder. Tapping the card opens the job sheet,
            // which is where each count's contents live.
            //
            // One honest divergence from web: CrewChipsView is its own HStack,
            // so CardChipFlow sees the whole crew as ONE subview and cannot
            // wrap BETWEEN the zeus pill and the role chips the way web's
            // .chips wraps between flex items. A four-bucket crew is the only
            // realistic case that gets near a phone's card width; it compresses
            // rather than wrapping, and CardChipFlow's placement clamp is what
            // keeps it inside the card instead of drawing over the border.
            // Flattening it would mean CrewChipsView emitting bare chips for a
            // Layout to flatten, which changes a view the job sheet also uses.
            if hasChipRow {
                CardChipFlow {
                    CrewChipsView(zeusStatus: job.zeusStatus, roster: job.roster ?? [])
                    ForEach(job.threads ?? [], id: \.tid) { ThreadChip(thread: $0) }
                    // The stage badge sits with the identity chips too, just
                    // ahead of the goal — the same slot web's .st-chip takes;
                    // StageChip's own doc comment carries the rationale.
                    if let stage = job.stage, !stage.isEmpty {
                        StageChip(stage: stage)
                    }
                    // After the crew and the threads, before the counts.
                    // The authority for that position is the approved
                    // mockup (40-ui-mockups.html §3, which draws the goal
                    // between `⚒︎ 2/3` and the counts — only the goal's slot
                    // is taken from it; this card's counts keep the order
                    // they already had), not the web card as it stands —
                    // web is being moved to this same order in this round,
                    // so citing it would be citing a sibling mid-change.
                    // The reason behind the mockup's order: the goal says
                    // what this job is FOR, which reads with the identity
                    // chips rather than among the inventory counts.
                    //
                    // ONE chip per linked goal, in link order, and no `+N`
                    // badge for the rest (carl's call, 2026-08-17): the
                    // chips wrap in CardChipFlow like every other chip here,
                    // so a job serving three goals says all three. `offset`
                    // is the ForEach id rather than the goal id because
                    // nothing on the wire promises the ids are distinct, and
                    // a duplicate id is a SwiftUI state-reuse bug rather
                    // than a visible one.
                    ForEach(Array(cardGoals.enumerated()), id: \.offset) { item in
                        GoalChip(goal: item.element)
                    }
                    DocQuestionCountChip(open: openQuestions)
                    ArtifactCountChip(count: (job.artifacts ?? []).count)
                    FeedbackCountChip(feedback: job.feedback ?? [])
                }
            }
            // band 3. The ⚠ / ✕ / ↻ glyphs became SF Symbols with the rest of
            // the card — same two strings, same counter colour, drawn in the
            // system's own vocabulary instead of literal characters that had
            // to be typed to match the web board's.
            if let health = job.health, !health.isEmpty {
                Label(health, systemImage: "exclamationmark.triangle.fill")
                    .font(.voice(11))
                    .foregroundStyle(KTheme.counter)
            }
            if let err = job.err, !err.isEmpty {
                Label(err, systemImage: "xmark.octagon.fill")
                    .font(.voice(11))
                    .foregroundStyle(KTheme.counter)
            }
            if let wakeup = job.wakeup, job.state == "working" {
                WakeupChip(wakeup: wakeup)
                if !wakeup.reason.isEmpty {
                    Label(wakeup.reason, systemImage: "arrow.trianglehead.clockwise")
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                        .lineLimit(1)
                }
            }
            // band 5
            if !(job.prs ?? []).isEmpty {
                CardChipFlow {
                    ForEach(job.prs ?? [], id: \.url) { PRChip(pr: $0) }
                }
            }
        }
    }
}

/// A wrapping chip row — the direct equivalent of the web card's
/// `flex-wrap: wrap` on `.chips`. An `HStack` would compress its children past
/// legibility or run off the card edge (the card is not horizontally
/// scrollable), and a job with a crew, two branches and three counts overflows
/// a phone-width card easily.
///
/// A real `Layout` rather than a `ViewThatFits` pair. `ViewThatFits` chooses
/// among a fixed set of hand-authored arrangements, so it cannot wrap an
/// arbitrary number of chips into as many rows as the width happens to need —
/// that would mean pre-authoring one branch per row count. Its usual fallback,
/// a `VStack`, would be selected (a vertical stack IS narrower than the
/// horizontal one, so the branches do differ in width), but it throws away the
/// horizontal space the wrap exists to use: one chip per line.
///
/// Cross-axis alignment is centre, for the same reason web's `.chips` sets
/// `align-items: center`: crew pills are taller than the count chips beside
/// them, and top-aligning would leave the shorter chips' text riding high.
private struct CardChipFlow: Layout {
    var spacing: CGFloat = 5
    var lineSpacing: CGFloat = 4

    /// Rows of (subview index, size), plus each row's height, for a given width.
    private func rows(for proposal: ProposedViewSize, subviews: Subviews) -> (rows: [[(Int, CGSize)]], heights: [CGFloat]) {
        let maxWidth = proposal.width ?? .infinity
        var rows: [[(Int, CGSize)]] = []
        var heights: [CGFloat] = []
        var current: [(Int, CGSize)] = []
        var currentWidth: CGFloat = 0
        var currentHeight: CGFloat = 0
        // subviews.indices, NOT enumerated(): placeSubviews' trailing
        // zero-placement loop iterates indices, and the two must key off the
        // same index space. They agree today only because LayoutSubviews is
        // 0-based — with enumerated() the offsets stored here are subscripted
        // back as `subviews[index]`, which on a non-zero-based collection
        // would index outside its range and trap. This form is correct by
        // construction instead.
        for i in subviews.indices {
            let size = subviews[i].sizeThatFits(.unspecified)
            // an empty chip (every count chip renders nothing at zero) must not
            // consume a slot or a gap
            if size.width == 0, size.height == 0 { continue }
            let needed = current.isEmpty ? size.width : currentWidth + spacing + size.width
            if !current.isEmpty, needed > maxWidth {
                rows.append(current)
                heights.append(currentHeight)
                current = [(i, size)]
                currentWidth = size.width
                currentHeight = size.height
            } else {
                current.append((i, size))
                currentWidth = needed
                currentHeight = max(currentHeight, size.height)
            }
        }
        if !current.isEmpty {
            rows.append(current)
            heights.append(currentHeight)
        }
        return (rows, heights)
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let (rows, heights) = rows(for: proposal, subviews: subviews)
        guard !rows.isEmpty else { return .zero }
        let width = rows.map { row in
            row.reduce(0) { $0 + $1.1.width } + spacing * CGFloat(max(0, row.count - 1))
        }.max() ?? 0
        let height = heights.reduce(0, +) + lineSpacing * CGFloat(max(0, heights.count - 1))
        return CGSize(width: min(width, proposal.width ?? width), height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let (rows, heights) = rows(for: proposal, subviews: subviews)
        // Layout's contract is that every subview gets placed. rows() drops the
        // zero-size ones (see the skip there) so they can't earn a gap, so they
        // are placed here at zero — cheap, and it means a future child whose
        // IDEAL size is zero but which draws when actually proposed one (a
        // Spacer, say) is laid out rather than silently vanishing.
        var placed = Set<Int>()
        var y = bounds.minY
        for (rowIndex, row) in rows.enumerated() {
            let rowHeight = heights[rowIndex]
            var x = bounds.minX
            for (index, size) in row {
                placed.insert(index)
                subviews[index].place(
                    at: CGPoint(x: x, y: y + (rowHeight - size.height) / 2), // centre in the line
                    // clamped to the container: rows() never wraps a subview
                    // that is the first on its line, so one wider than the
                    // whole row would otherwise be placed at its natural width
                    // and drawn straight past the card's edge — nothing here
                    // clips. Proposing the clamp lets it wrap or ellipsise
                    // itself instead.
                    proposal: ProposedViewSize(
                        width: min(size.width, bounds.width), height: size.height))
                x += size.width + spacing
            }
            y += rowHeight + lineSpacing
        }
        for index in subviews.indices where !placed.contains(index) {
            subviews[index].place(at: CGPoint(x: bounds.minX, y: bounds.minY),
                                  proposal: ProposedViewSize(width: 0, height: 0))
        }
    }
}

/// The stage badge: which pipeline skill zeus most recently used
/// (info/aim/do/pr/dream), read off zeus' transcript server-side —
/// see `webJob.Stage` (internal/api/board.go) for what absence covers.
/// Like the goal it is a fact ABOUT the job rather than something it
/// contains, so the card slots it with the identity chips (after the
/// threads, before the goal — the slot web's `.st-chip` takes).
///
/// Each stage wears its skill's own icon and palette colour (`KTheme.skill*`
/// — the same five tokens the steer modal's command chips and the DREAM
/// surface wear, mirrored from web's `--skill-*`), so a column of badges
/// sorts by eye; web's `.st-chip-<stage>` carries the text-glyph sibling of
/// each symbol and the two lists move together. A stage this build does not
/// name (including the retired `graph`) falls back to the accent
/// look with a neutral symbol rather than drawing nothing — a symbol rather
/// than web's bare-name treatment because `KChip.icon` is non-optional, so
/// this chip structurally always draws one: the name is the payload, the
/// icon and colour are decoration.
private struct StageChip: View {
    let stage: String

    private var spec: (icon: String, tint: Color) {
        switch stage {
        case "info": return ("info.circle", KTheme.skillInfo)
        case "aim": return ("scope", KTheme.skillAim)
        case "do": return ("gearshape.fill", KTheme.skillDo)
        case "pr": return ("arrow.triangle.branch", KTheme.skillPr)
        case "dream": return ("moon.fill", KTheme.skillDream)
        default: return ("arrow.triangle.turn.up.right.diamond", KTheme.accent)
        }
    }

    var body: some View {
        let s = spec
        KChip(icon: s.icon, text: stage, tint: s.tint)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("stage: \(stage)")
    }
}

/// "⚑ N questions" — the board card's passive open-question summary, mirroring
/// web's `.dq-chip`. Counter-coloured the moment a P0 is open, which is the
/// same signal the web chip and the board's flag counter carry. Renders
/// nothing when nothing is open.
///
/// The ⚑ is `flag.fill` in a `KChip` now rather than a literal character in a
/// stroked rect; the count, the pluralisation, the P0 rule and the spoken
/// sentence are all the ones web's chip has always carried.
private struct DocQuestionCountChip: View {
    let open: [WebDocQuestion]

    var body: some View {
        if !open.isEmpty {
            let p0 = open.filter { $0.importance == "P0" }.count
            // KChip's nil tint IS the muted rendering the old `text2` stroke
            // gave — so the P0 rule stays a two-state rule, not a colour
            // literal repeated in a third place.
            KChip(
                icon: "flag.fill",
                text: "\(open.count) question\(open.count == 1 ? "" : "s")",
                tint: p0 > 0 ? KTheme.counter : nil
            )
            .accessibilityElement(children: .ignore)
            .accessibilityLabel("\(open.count) open questions\(p0 > 0 ? ", \(p0) P0" : "")")
        }
    }
}

/// "⚡ 4.0× 2→8   3h ago" — zeus' triage estimate for a blocked job, as an ROI:
/// the ratio of the claude work released (`scope`) to what carl pays to release
/// it (`unblock`), then both halves so the ratio can be read back, then how old
/// the estimate is. Colour-graded by band, so a column of blocked cards sorts
/// itself by eye.
///
/// The AGE is a separate muted label rather than part of the badge, and that
/// split is required rather than cosmetic. The badge text is pinned identical
/// across this app and the web board, so it must not absorb a client-specific
/// string — but the age still needs a VISIBLE surface here, because iOS has no
/// hover: web puts it in the badge's `title` tooltip and a pointer reaches it,
/// whereas on a phone an age that lived only in the accessibility label would
/// leave every sighted viewer unable to tell a three-day-old estimate from one
/// written a minute ago. That is precisely the failure the age exists to close
/// — zeus estimates, carl answers, the cell later dies, the daemon's `health`
/// files the card back under BLOCKED, and with zeus gone nothing rewrites it.
///
/// Renders nothing when the estimate has no usable ROI — `WebJobLoad.badgeText`
/// is nil exactly then. Neither shape that makes it nil can reach a client from
/// this server (a clear drops the whole key, a half-estimate is a 400), so that
/// is defence against a hand-edited `jobs` row rather than a case to expect;
/// see `WebJobLoad.roi`, which is where the reasoning lives. Whether the card
/// is blocked at all is the CALLER's test (JobCardView.showsLoadBadge); this
/// view only knows how to draw one.
private struct LoadBadge: View {
    let load: WebJobLoad

    var body: some View {
        if let text = load.badgeText, let band = load.band {
            let style = BoardTheme.style(forLoadBand: band)
            // The clock is read here rather than inside the model, so
            // WebJobLoad stays pure — the same split wakeupCountdownLabel and
            // its WakeupChip use. Recomputed on each render, which the board's
            // few-second poll already drives; no separate ticker, and the age
            // is coarse enough (one unit, no decimals) that it does not need one.
            let now = Int64(Date().timeIntervalSince1970 * 1000)
            // The badge's leading "⚡ " is U+26A1, and the bucketed system
            // MONOSPACED face `Font.data` now resolves to has no glyph for it
            // — verified on the 26.3 simulator against the live board, where
            // it drew as a missing-glyph box in front of every ROI. The
            // string itself is NOT touched: `badgeText` is built client-side
            // (`WebJobLoad`, Models.swift) and pinned byte-identical against
            // the web board's own badge format. Only the RENDER changes —
            // the prefix comes off here and an SF Symbol takes its place
            // inside the same capsule.
            //
            // Conditional on the prefix actually being there, never assumed.
            // Today the guard branch is dead: `WebJobLoad.badgeText`
            // (Models.swift) builds the string client-side and always
            // prepends the bolt. The guard is there so a future badgeText
            // shape without it renders untouched instead of losing its first
            // two characters.
            let boltPrefix = "⚡ "
            let hasBolt = text.hasPrefix(boltPrefix)
            let badgeLabel = hasBolt ? String(text.dropFirst(boltPrefix.count)) : text
            HStack(spacing: 5) {
                HStack(spacing: 4) {
                    if hasBolt {
                        Image(systemName: "bolt.horizontal.fill")
                            .font(.system(size: 9, weight: .semibold))
                    }
                    Text(badgeLabel)
                        // The one label on this card that must not wrap: it
                        // carries an internal space ("4.0× 2→8") and breaking
                        // at it splits the ratio from the pair it came from.
                        .lineLimit(1)
                        .font(.data(10, weight: .semibold))
                }
                    .foregroundStyle(style.text)
                    .padding(.horizontal, 9)
                    .padding(.vertical, 3)
                    // A tinted capsule now, matching every other pill on the
                    // card. The band's three channels are untouched: its
                    // fill, its border colour, and the dash that marks a
                    // stale estimate all still come from
                    // `BoardTheme.style(forLoadBand:)`.
                    .background(style.fill ?? .clear, in: Capsule())
                    .overlay(
                        Capsule().stroke(
                            style.border,
                            style: StrokeStyle(lineWidth: 1, dash: style.dashed ? [3, 2] : [])
                        )
                    )
                    // On the badge alone, never the row: `cold`'s dim is part
                    // of that band's treatment, and dimming the age with it
                    // would make the freshest information hardest to read on
                    // exactly the cards worth doing last.
                    .opacity(style.opacity)
                if let age = load.age(nowMillis: now) {
                    // Muted, unboxed and a size down, so it reads as an
                    // annotation ON the badge rather than a second chip. "3h
                    // ago" rather than a bare "3h" for the same reason the
                    // token note spells it out: an unqualified duration beside
                    // a ratio invites being read as part of the ratio.
                    Text("\(age) ago")
                        .lineLimit(1)
                        .font(.data(9))
                        .foregroundStyle(KTheme.text2)
                        .opacity(0.7)
                }
            }
            // Collapses the HStack into ONE a11y element so the sentence below
            // replaces both children — otherwise VoiceOver reads the badge and
            // then "3h ago" again, since the sentence already carries the age.
            // Same reason (and same shape) as JobDetailView's chipLabel.
            .accessibilityElement(children: .ignore)
            // The visible badge is a bolt symbol over "4.0× 2→8", which has no
            // spoken form worth hearing; the sentence replaces it entirely.
            // The fallback is still the UNSTRIPPED `text`, so a summary-less
            // estimate is spoken exactly as it always was.
            .accessibilityLabel(load.summary(nowMillis: now) ?? text)
        }
    }
}

/// "▤ N" (`doc.on.doc` + N, since the restyle) — the board card's entire
/// artifact surface, mirroring web's
/// `.ct-chip`. A count and nothing else: the names live one tap in, in the job
/// sheet. Always muted, because unlike an open question a pile of artifacts is
/// never something owed. (This used to contrast with an unaddressed note too —
/// that stopped being an example of something owed when `addressed` became the
/// dream pass' column, and FeedbackCountChip is muted now for the same reason.)
private struct ArtifactCountChip: View {
    let count: Int

    var body: some View {
        if count > 0 {
            KChip(icon: "doc.on.doc", text: "\(count)", tint: nil)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(count) artifact\(count == 1 ? "" : "s")")
        }
    }
}

/// The "table of contents" reached from the overview strip's chevron and from
/// the board toolbar's magnifier: all five columns as sections, every job as a
/// compact row, tapping one opens JobDetail. Reads `vm` (the shared
/// @Observable board view model) directly rather than a snapshot, so it stays
/// in sync as JobDetail actions refresh the board underneath it — no separate
/// fetch of its own.
///
/// This is also the app's whole job-search surface. It filters its OWN rows
/// and nothing else: `vm.jobs(in:)` keeps returning every job, because the
/// paged board and the drag-reorder overlay both read it and a reorder inside
/// a narrowed column would post a partial column order to the server (see
/// BoardView's localOrder/commit path, and internal/jobs/order.go's
/// ApplyColumnOrder, which puts every member absent from the posted list at
/// the top). Search narrows what this sheet draws; the board is untouched.
private struct BoardOverviewSheet: View {
    let vm: BoardViewModel
    let baseURL: String
    /// Present with the search field already up and focused — true only from
    /// the toolbar's magnifier, since only that entry point means "search".
    var searchFocused: Bool = false
    var onUpdate: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(AppRouter.self) private var router
    @State private var selectedJob: WebJob?
    @State private var query = ""
    /// Whether the search field itself is showing. Bound rather than left to
    /// the system so the magnifier can raise and focus it on appear; it is
    /// also what the user's own dismissal of the field writes back to.
    @State private var searchPresented = false
    // See BoardView's identically-named property + presentJob(_:) above —
    // same staleness-baseline pattern, duplicated here because this sheet
    // reads `vm` directly rather than owning its own selectedJob binding at
    // the BoardView level.
    @State private var presentedJobState: String?
    // Same pushed-child suppression as BoardView's job sheet — see
    // SheetNavState above.
    @State private var jobSheetNav = SheetNavState()

    var body: some View {
        let terms = parseTerms(query)
        // Matched once, read twice — by the sections below and by the
        // no-results overlay — so a query is never run over the board twice in
        // one body evaluation.
        let sections = BoardColumn.allCases.map {
            ColumnRows(column: $0, rows: matchedRows(in: $0, terms: terms))
        }
        List {
            ForEach(sections) { section in
                // With a query up, a column with no matches is dropped whole —
                // header included. Six "empty" placeholders around one hit is
                // the answer buried in its own scaffolding. With no query the
                // sheet is exactly what it was: every section, placeholder and
                // all.
                if terms.isEmpty || !section.rows.isEmpty {
                    kSection(section.column.title) {
                        if section.rows.isEmpty {
                            Text("empty")
                                .font(.voice(12))
                                .foregroundStyle(KTheme.text2.opacity(0.5))
                        } else {
                            ForEach(section.rows) { row in
                                Button {
                                    selectedJob = row.job
                                    presentedJobState = row.job.state
                                } label: {
                                    overviewRow(row.job, reason: row.reason)
                                }
                                .buttonStyle(.plain)
                            }
                        }
                    }
                }
            }
        }
        // Dropping every empty section means a query that matches nothing
        // leaves NOTHING — a blank sheet under the search field, which reads
        // like a failed fetch rather than an answer. This says the search ran
        // and came back empty, the same job the web board's "0 of 6 jobs"
        // readout does, in the app's own voice rather than system chrome —
        // `.voice(12)`/`KTheme.text2`, the same font and token as the "empty"
        // placeholder above, at full opacity rather than its 0.5 (this one is
        // the whole answer on screen; that one labels a section you can see).
        //
        // `vm.board != nil` is the load-bearing half of the condition, not
        // belt and braces: `jobs(in:)` reads `board?.jobs ?? []`, and
        // `refresh(baseURL:)` leaves `board` alone when the fetch throws, so
        // until the first successful poll lands EVERY query matches nothing.
        // Without this the sheet would answer "no jobs match" for a board it
        // never fetched — a confident wrong answer, which is worse than the
        // blank sheet this replaced. This sheet has no error surface of its
        // own; the board's daemon bar behind it is what reports an unreachable
        // server, so with no board at all the honest thing here is silence.
        .overlay {
            if vm.board != nil, !terms.isEmpty, sections.allSatisfy({ $0.rows.isEmpty }) {
                Text("no jobs match \"\(query)\"")
                    .font(.voice(12))
                    .foregroundStyle(KTheme.text2)
                    .padding()
            }
        }
        .listStyle(.plain)
        .kListStyle()
        .navigationTitle("Board Overview")
        .navigationBarTitleDisplayMode(.inline)
        // The query is view state and nothing else — no defaults key, no route
        // parameter, matching the web board. Every fresh presentation of this
        // sheet starts from an unfiltered board.
        .searchable(text: $query, isPresented: $searchPresented, prompt: "Search jobs")
        // Raise the field for the magnifier's entry path. Presenting it is what
        // focuses it, which is the whole reason `isPresented:` is bound rather
        // than left to the system.
        .onAppear {
            if searchFocused { searchPresented = true }
        }
        .toolbar {
            ToolbarItem(placement: .cancellationAction) {
                SheetCloseButton { dismiss() }
            }
        }
        .sheet(item: $selectedJob) { job in
            NavigationStack {
                JobDetailView(job: job, baseURL: baseURL, onUpdate: onUpdate)
            }
            .environment(jobSheetNav)
        }
        .onChange(of: selectedJob?.id) { jobSheetNav.childPushed = false }
        // Same auto-dismiss-on-staleness as BoardView's own job sheet — this
        // sheet reads `vm` live (see its doc comment above), so the same
        // polling refresh drives it here too, and it stands down the same way
        // while a child view is pushed on the stack.
        .onChange(of: vm.board?.jobs.first(where: { $0.id == selectedJob?.id })?.state) { _, newState in
            guard selectedJob != nil, !jobSheetNav.childPushed,
                  let presentedJobState, let newState, newState != presentedJobState else { return }
            selectedJob = nil
        }
        // JobDetailView's Cell row switches tabs and dismisses itself, but
        // that only closes the innermost sheet — this sheet (its presenter)
        // has to close itself too, or it's left covering the Sessions tab.
        .onChange(of: router.selectedTab) {
            dismiss()
        }
    }

    /// One rendered row: the job plus why it survived the query. A named type
    /// rather than a pair so the reason travels with the job it belongs to,
    /// and `id` mirrors WebJob's own (slug, archived) identity — an archived
    /// job rides the same list under its numbered name, so the bare slug is
    /// not unique across this sheet.
    private struct OverviewRow: Identifiable {
        let job: WebJob
        let reason: MatchReason
        var id: String { job.id }
    }

    /// One column's section, prepared before the List is built so the same
    /// match pass answers both what to draw and whether anything matched at
    /// all.
    private struct ColumnRows: Identifiable {
        let column: BoardColumn
        let rows: [OverviewRow]
        var id: String { column.id }
    }

    /// The rows this sheet draws for one column. This is the ONLY place the
    /// search filters — `vm.jobs(in:)` is read whole, exactly as it is by the
    /// paged board, and narrowed here for rendering alone.
    private func matchedRows(in column: BoardColumn, terms: [String]) -> [OverviewRow] {
        vm.jobs(in: column).compactMap { job in
            matchJob(job, terms: terms).map { OverviewRow(job: job, reason: $0.reason) }
        }
    }

    private func overviewRow(_ job: WebJob, reason: MatchReason) -> some View {
        let flagged = job.question != nil || !(job.health ?? "").isEmpty
        return HStack(spacing: 8) {
            Circle()
                .fill(BoardTheme.color(forState: job.state))
                .frame(width: 6, height: 6)
            Text(job.title)
                .font(.data(13))
                .foregroundStyle(KTheme.text)
            // Why this row is here at all, when its name says otherwise: the
            // query only matched its status text (zeus' last message, or the
            // last thing carl sent it). Without the marker a result that looks
            // unrelated to what was typed reads as a broken search. An empty
            // query can never produce it — every job matches by name then.
            if reason == .text {
                Text("in text")
                    .font(.data(10))
                    .foregroundStyle(KTheme.revealed)
                    .accessibilityLabel("matched in status text")
            }
            Spacer()
            if flagged {
                Image(systemName: "flag.fill")
                    .foregroundStyle(KTheme.counter)
                    .font(.voice(10))
            }
            Text(job.state)
                .font(.data(10))
                .foregroundStyle(KTheme.text2)
        }
    }
}
