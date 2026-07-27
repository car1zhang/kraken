import SwiftUI
import Observation
import Foundation
import UniformTypeIdentifiers

@Observable
final class BoardViewModel {
    var board: WebBoard?
    var errorMessage: String?

    func refresh(baseURL: String) async {
        do {
            let board = try await APIClient.shared.fetchBoard(baseURL: baseURL)
            self.board = board
            self.errorMessage = nil
            ingestBoardUpdate(board.update)
            ingestBoardShip(board.shipIOS)
            // Every successful fetch (this foreground poll, and the
            // BGAppRefreshTask in NotificationManager) runs the same
            // blocked-job diff — one notification per newly-blocked job.
            NotificationManager.shared.processBoard(board)
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

    func jobs(in column: BoardColumn) -> [WebJob] {
        (board?.jobs ?? []).filter { BoardTheme.column(for: $0) == column }
    }

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
    // The daemon bar's ⟳ control is a view over one server record (WebUpdate
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
    /// When this gap's first failed fetch happened; nil = the server answers.
    private var updateGapSince: Date?

    /// Folds the 4s board poll's copy of the record in. The 1.2s status poll
    /// wins outright while it is running: the two race, and a board response
    /// can easily be older than a status response that landed just before it
    /// — applying it would rewind the phase on screen.
    private func ingestBoardUpdate(_ u: WebUpdate?) {
        guard !updatePolling, let u else { return }
        update = u
        updateGapSince = nil
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
            updateGapSince = nil
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
        // The gap clock belongs to THIS loop. It measures wall-clock time,
        // but the loop is frozen whenever the app is suspended or the Board
        // tab is off screen — so a gap inherited from an earlier loop counts
        // minutes nobody was watching. Lock the phone during a perfectly
        // normal restart gap, unlock ten minutes later, and the first fetch
        // on resume (against a radio/tailnet that hasn't come up yet) would
        // read 600s > 300s and synthesize a failure for an update that
        // succeeded. Keying the task on scenePhase is what makes this reset
        // actually fire on resume — the loop restarts rather than thawing.
        updateGapSince = nil
        // …and the reset alone isn't enough, because a suspend can also
        // happen between a task restart and the next tick. So the ceiling
        // additionally needs two failures BACK TO BACK: at 1.2s a real
        // five-minute outage racks up hundreds of them, while a single blip
        // on resume is followed 1.2s later by a success that clears the gap.
        var consecutiveFailures = 0
        while !Task.isCancelled && updatePolling && updatePollEpoch == epoch {
            do {
                let u = try await APIClient.shared.updateStatus(baseURL: baseURL)
                update = u
                updateGapSince = nil
                consecutiveFailures = 0
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
                consecutiveFailures += 1
                let since = updateGapSince ?? Date()
                updateGapSince = since
                if consecutiveFailures >= 2, Date().timeIntervalSince(since) > Self.updateGapCeiling {
                    // Five minutes of silence is no longer a restart. Say so
                    // rather than spinning a cyan control at a dead socket
                    // forever — update.log on the Mac is the only thing that
                    // can explain a serve which never bound.
                    let now = Int64(Date().timeIntervalSince1970 * 1000)
                    update = WebUpdate(
                        phase: "failed",
                        fromSHA: update?.fromSHA ?? "", toSHA: update?.toSHA ?? "",
                        behind: update?.behind ?? 0,
                        error: "kraken serve did not come back after the restart — see ~/.kraken/update.log",
                        startedAt: update?.startedAt ?? now, updatedAt: now,
                        buildSHA: update?.buildSHA ?? "", checkedAt: update?.checkedAt ?? 0
                    )
                    updateGapSince = nil
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
        if running && (updateGapSince != nil || phase == "restarting") {
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
    // The daemon bar's ⇧ control, the update control's sibling: one server
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
        var consecutiveFailures = 0
        while !Task.isCancelled && shipPolling && shipPollEpoch == epoch {
            do {
                let s = try await APIClient.shared.shipIOSStatus(baseURL: baseURL)
                ship = s
                consecutiveFailures = 0
                if Self.shipTerminalPhases.contains(s.phase ?? "idle") { return }
            } catch {
                // Cancellation is not a failure: this view is a TabView child,
                // so switching tabs mid-run cancels the fetch in flight against
                // a perfectly healthy server. `Task.isCancelled` is the ONLY
                // signal that can reach here — APIClient.perform re-wraps every
                // URLSession failure, cancellation included, as
                // KrakenError.message, so no cast can match. Don't add one.
                if Task.isCancelled { return }
                consecutiveFailures += 1
                if consecutiveFailures >= shipFailureCeilingNow {
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
            // which the update control has to do because its message shares the
            // daemon bar's one-line slot. A ship has its own banner strip and
            // `shipBanner` is already printing the error in full underneath
            // this very button, so a reveal tap would change nothing on screen.
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

/// One paint of the daemon bar's ⟳ control — see BoardViewModel.updateButton.
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

/// One paint of the daemon bar's ⇧ control — see BoardViewModel.shipButton.
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
/// reasons (a run STARTING bumps the generation; a scene-phase change has to
/// restart rather than thaw the loop, since a suspended loop's failure streak
/// would otherwise resume against a radio that hasn't come back up).
private struct ShipPollKey: Hashable {
    let generation: Int
    let phase: ScenePhase
}

/// The update poll's `.task(id:)` key. Two independent things restart that
/// loop, and it must restart for either. The generation covers a run
/// STARTING (see updatePollGeneration); the scene phase covers the app
/// coming back from suspension, where the point is not to start polling but
/// to throw away a gap clock that has been counting wall-clock minutes while
/// the loop itself was frozen — see runUpdatePoll.
private struct UpdatePollKey: Hashable {
    let generation: Int
    let phase: ScenePhase
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

struct BoardView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @Environment(\.scenePhase) private var scenePhase
    @Environment(AppRouter.self) private var router
    @State private var vm = BoardViewModel()
    @State private var selectedJob: WebJob?
    // The presented job's `state` as of whenever it was (re-)assigned to
    // selectedJob — the baseline the staleness check below diffs the next
    // polling refresh against. WebJob isn't Equatable (Decodable-only), so
    // this is captured explicitly at every assignment site rather than via
    // .onChange(of: selectedJob).
    @State private var presentedJobState: String?
    // Set by whatever JobDetailView has pushed on the job sheet's stack; see
    // SheetNavState above.
    @State private var jobSheetNav = SheetNavState()
    @State private var showingNewJob = false
    @State private var showingOverview = false
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
    // Sheets presented for cross-column drops that need user input rather
    // than a bare API call (blocked→working, done→working).
    @State private var dragSteerJob: WebJob?
    @State private var dragReactivateJob: WebJob?

    var body: some View {
        NavigationStack {
            VStack(spacing: 0) {
                daemonBar
                shipBanner
                contractBanner
                overviewStrip
                TabView(selection: $selectedColumn) {
                    ForEach(BoardColumn.allCases) { column in
                        columnView(column)
                            .tag(column)
                    }
                }
                .tabViewStyle(.page(indexDisplayMode: .always))
            }
            .background(KTheme.ground)
            .navigationTitle("Board")
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        showingNewJob = true
                    } label: {
                        Image(systemName: "plus")
                    }
                    .buttonStyle(.plain)
                    .foregroundStyle(KTheme.accent)
                }
            }
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
            // Keyed on `scenePhase` as well, matching the board poll above:
            // a suspended loop's gap clock keeps counting wall-clock time it
            // was not awake for, so resuming has to restart the loop rather
            // than thaw it (see UpdatePollKey and runUpdatePoll).
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
            .sheet(item: $selectedJob) { job in
                NavigationStack {
                    JobDetailView(job: job, baseURL: serverURL) {
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
                .environment(jobSheetNav)
            }
            // Presenting a different job (or dismissing entirely) starts the
            // stack over at JobDetailView's root, so the pushed-child flag
            // resets with it — belt and braces against an unbalanced
            // appear/disappear pair leaving auto-dismiss suppressed forever.
            .onChange(of: selectedJob?.slug) { jobSheetNav.childPushed = false }
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
            .onChange(of: vm.board?.jobs.first(where: { $0.slug == selectedJob?.slug })?.state) { _, newState in
                guard selectedJob != nil, !jobSheetNav.childPushed,
                      let presentedJobState, let newState, newState != presentedJobState else { return }
                selectedJob = nil
            }
            .sheet(isPresented: $showingNewJob) {
                NavigationStack {
                    NewJobSheet(baseURL: serverURL) {
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            .sheet(isPresented: $showingOverview) {
                NavigationStack {
                    BoardOverviewSheet(vm: vm, baseURL: serverURL) {
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            // blocked→working overview-strip drag: reuses JobDetailView's
            // SteerView with unblockFraming — same delivery as the in-detail
            // Unblock button, just reached from the strip instead.
            .sheet(item: $dragSteerJob) { job in
                NavigationStack {
                    SteerView(job: job, baseURL: serverURL, unblockFraming: true) {
                        dragSteerJob = nil
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            // done→working overview-strip drag: reuses ReactivateView with
            // requireText, since this entry path has no separate
            // confirmation step to catch an accidental empty resume.
            .sheet(item: $dragReactivateJob) { job in
                NavigationStack {
                    ReactivateView(job: job, baseURL: serverURL, requireText: true) {
                        dragReactivateJob = nil
                        Task { await vm.refresh(baseURL: serverURL) }
                    }
                }
            }
            // Deep links (kraken://board/<column>, kraken://overview,
            // kraken://job/<slug>) stamp a pending value on the shared
            // router; this view is the one that owns each destination, so
            // it's the one that consumes and clears it. onAppear covers a
            // route stamped before this view mounted (cold launch via the
            // -KrakenRoute launch argument); onChange covers routes arriving
            // while it's already up.
            .onAppear { consumePendingBoardRoutes() }
            .onChange(of: router.pendingBoardColumn) { consumePendingBoardRoutes() }
            .onChange(of: router.pendingOverview) { consumePendingBoardRoutes() }
            .task(id: router.pendingJobSlug) {
                guard let slug = router.pendingJobSlug else { return }
                // The board may not have loaded yet (e.g. a cold launch
                // straight into a kraken://job/<slug> link) — retry briefly
                // rather than silently dropping the link.
                for _ in 0..<10 {
                    if vm.board == nil {
                        await vm.refresh(baseURL: serverURL)
                    }
                    if let job = vm.board?.jobs.first(where: { $0.slug == slug }) {
                        presentJob(job)
                        break
                    }
                    try? await Task.sleep(for: .seconds(0.5))
                }
                router.pendingJobSlug = nil
            }
        }
    }

    // Presents a job sheet and records its state as the staleness baseline
    // — see presentedJobState's doc comment and the onChange next to the
    // sheet(item:) modifier above.
    private func presentJob(_ job: WebJob) {
        selectedJob = job
        presentedJobState = job.state
    }

    private func consumePendingBoardRoutes() {
        if let column = router.pendingBoardColumn {
            selectedColumn = column
            router.pendingBoardColumn = nil
        }
        if router.pendingOverview {
            showingOverview = true
            router.pendingOverview = false
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
                .padding(.horizontal)
                .padding(.vertical, 6)
                .background(KTheme.revealedSoft)
                .overlay(alignment: .bottom) {
                    Rectangle().fill(KTheme.hairline).frame(height: 1)
                }
        }
    }

    // 7x7 square LED with a soft glow — mirrors .led / .led.ok.
    private var daemonBar: some View {
        HStack(spacing: 8) {
            if let board = vm.board {
                Rectangle()
                    .fill(board.daemonOK ? KTheme.solved : KTheme.counter)
                    .frame(width: 7, height: 7)
                    .shadow(color: (board.daemonOK ? KTheme.solved : KTheme.counter).opacity(0.55), radius: 4)
                Text(board.daemonOK ? "daemon up" : "daemon down")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
                Spacer()
                shipControl
                updateControl
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
            } else {
                Text("loading…").font(.data(11)).foregroundStyle(KTheme.text2)
            }
        }
        .padding(.horizontal)
        .padding(.vertical, 7)
        .background(KTheme.surface)
        .overlay(alignment: .bottom) {
            Rectangle().fill(KTheme.hairline).frame(height: 1)
        }
    }

    // The self-update control (POST /api/update + the status poll that
    // follows the run through the server restart — see BoardViewModel's
    // self-update section). Deliberately bar-weight mono rather than a
    // prominent button: it belongs to the server-health row, not the action
    // toolbar. No confirmation step — the tap IS the decision, matching the
    // web board. Fixed-size so a long error next to it can't squeeze it away.
    private var updateControl: some View {
        let state = vm.updateButton
        return Button {
            if let err = state.revealsError {
                vm.revealUpdateError(err)
            } else {
                // Synchronously, in the tap handler — a Task body doesn't
                // start soon enough to keep a second tap out (see
                // BoardViewModel.updateStarting).
                vm.markUpdateStarting()
                Task { await vm.startUpdate(baseURL: serverURL) }
            }
        } label: {
            HStack(spacing: 3) {
                Text(state.glyph)
                Text(state.label)
            }
            .font(.data(10))
            .foregroundStyle(state.color)
        }
        .buttonStyle(.plain)
        .disabled(state.disabled)
        .fixedSize()
        .accessibilityLabel("update kraken — \(state.label)")
    }

    // The TestFlight ship control (POST /api/ship-ios + the status poll that
    // follows the run — see BoardViewModel's ship section). Sits left of the
    // update control, same bar weight, same no-confirmation tap.
    //
    // The accessibility label carries the distinction the two-word visible
    // label can't: "uploaded" is where this run ends, and installing the build
    // on this phone is a separate manual step in the TestFlight app afterwards.
    private var shipControl: some View {
        let state = vm.shipButton
        return Button {
            // One action, always: start a ship. A fresh failure re-runs rather
            // than "revealing" itself — shipBanner is already showing the error
            // in full right below this control (see BoardViewModel.shipButton).
            //
            // Synchronously, in the tap handler — a Task body doesn't start
            // soon enough to keep a second tap out (see shipStarting).
            vm.markShipStarting()
            Task { await vm.startShipIOS(baseURL: serverURL) }
        } label: {
            HStack(spacing: 3) {
                Text(state.glyph)
                Text(state.label)
            }
            .font(.data(10))
            .foregroundStyle(state.color)
        }
        .buttonStyle(.plain)
        .disabled(state.disabled)
        .fixedSize()
        .accessibilityLabel("ship the iOS app to TestFlight — \(state.label). Uploading is not installing: TestFlight processes the build afterwards and you install it from the TestFlight app.")
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
                .padding(.horizontal)
                .padding(.vertical, 6)
                .background(KTheme.surface)
                .overlay(alignment: .bottom) {
                    Rectangle().fill(KTheme.hairline).frame(height: 1)
                }
        }
    }

    // Always-visible table-of-contents strip: one compact cell per column so
    // the whole board's shape is readable without swiping the pager. Tapping
    // a cell jumps the pager there; the chevron opens the full breakdown.
    private var overviewStrip: some View {
        HStack(spacing: 0) {
            ForEach(BoardColumn.allCases) { column in
                overviewCell(column)
            }
            Button {
                showingOverview = true
            } label: {
                Image(systemName: "chevron.down")
            }
            .buttonStyle(.plain)
            .foregroundStyle(KTheme.text2)
            .padding(.leading, 6)
            .padding(.trailing, 10)
        }
        .padding(.leading, 6)
        .padding(.vertical, 4)
        .background(KTheme.surface)
        .overlay(alignment: .bottom) {
            Rectangle().fill(KTheme.hairline).frame(height: 1)
        }
    }

    private func overviewCell(_ column: BoardColumn) -> some View {
        let count = vm.jobs(in: column).count
        let flagged = vm.hasFlag(in: column)
        let isCurrent = selectedColumn == column
        let isLegalTarget = drag.sourceColumn.map { isLegalCrossColumnDrop(from: $0, to: column) } ?? false
        let isHovered = drag.hoveredColumn == column
        return Button {
            selectedColumn = column
        } label: {
            VStack(spacing: 2) {
                HStack(spacing: 3) {
                    Text(column.shortTitle)
                        .font(.data(9, weight: .semibold))
                        .kerning(0.5)
                        .foregroundStyle(isCurrent ? KTheme.accent : KTheme.text2)
                        .lineLimit(1)
                        .minimumScaleFactor(0.8)
                    if flagged {
                        Rectangle().fill(KTheme.counter).frame(width: 5, height: 5)
                    }
                }
                Text("\(count)")
                    .font(.data(13, weight: .semibold))
                    .foregroundStyle(count > 0 ? KTheme.accent : KTheme.text2)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 4)
            .background(isHovered ? KTheme.accent.opacity(0.18) : Color.clear)
            .overlay(alignment: .bottom) {
                if isCurrent {
                    Rectangle().fill(KTheme.accent).frame(height: 2)
                }
            }
            .overlay {
                if isLegalTarget {
                    Rectangle().stroke(KTheme.accent, lineWidth: isHovered ? 2 : 1)
                }
            }
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

    private func columnView(_ column: BoardColumn) -> some View {
        let jobs = orderedJobs(column)
        return ScrollView {
            VStack(spacing: 0) {
                HStack(alignment: .lastTextBaseline) {
                    Text(column.title)
                        .kLabelStyle(size: 10)
                    Spacer()
                    Text("\(jobs.count)")
                        .font(.data(10))
                        .foregroundStyle(jobs.isEmpty ? KTheme.text2 : KTheme.accent)
                }
                .padding(.horizontal)
                .padding(.top)
                .padding(.bottom, 6)
                .overlay(alignment: .bottom) {
                    Rectangle().fill(KTheme.hairline).frame(height: 1)
                }
                .padding(.horizontal)

                if jobs.isEmpty {
                    Text("·")
                        .font(.data(11))
                        .foregroundStyle(KTheme.text2.opacity(0.5))
                        .padding(.top, 40)
                } else {
                    LazyVStack(spacing: 10) {
                        ForEach(jobs) { job in
                            // Archived cards aren't draggable: the server
                            // rejects a reorder of the "archived" column,
                            // and no cross-column drop has an archived
                            // source (mirrors the web board, where archived
                            // cards render without draggable at all).
                            if column == .archived {
                                JobCardView(job: job)
                                    .onTapGesture { presentJob(job) }
                            } else {
                                JobCardView(job: job)
                                    .opacity(drag.slug == job.slug ? 0.4 : 1.0)
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
                    .padding()
                }
            }
        }
        .background(KTheme.ground)
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
        var bySlug = Dictionary(uniqueKeysWithValues: serverJobs.map { ($0.slug, $0) })
        var listed: [WebJob] = []
        for slug in overlay {
            if let job = bySlug.removeValue(forKey: slug) { listed.append(job) }
        }
        let unlisted = serverJobs.filter { bySlug[$0.slug] != nil }
        return unlisted + listed
    }

    /// A live-editable binding over one column's slug order, seeded from the
    /// current displayed order (overlay if present, else server order) on
    /// first read. ReorderDropDelegate mutates this directly as a drag
    /// crosses other cards.
    private func slugsBinding(for column: BoardColumn) -> Binding<[String]> {
        Binding(
            get: { localOrder[column] ?? orderedJobs(column).map(\.slug) },
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
        guard slugs != vm.jobs(in: column).map(\.slug) else {
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
        case (.draft, .queued), (.queued, .draft), (.blocked, .done),
             (.queued, .working), (.working, .blocked), (.blocked, .working),
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
        let job = vm.board?.jobs.first { $0.slug == slug }
        switch (from, to) {
        case (.draft, .queued):
            runBoardAction { try await APIClient.shared.queueJob(baseURL: serverURL, slug: slug) }
        case (.queued, .draft):
            runBoardAction { try await APIClient.shared.dequeueJob(baseURL: serverURL, slug: slug) }
        case (.blocked, .done):
            runBoardAction { try await APIClient.shared.markDone(baseURL: serverURL, slug: slug) }
        case (.queued, .working):
            runBoardAction { try await APIClient.shared.startJob(baseURL: serverURL, slug: slug) }
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
            if let job { dragSteerJob = job } else { vm.errorMessage = missingJobMessage(slug) }
        case (.done, .blocked):
            runBoardAction { try await APIClient.shared.reactivateJob(baseURL: serverURL, slug: slug, text: "") }
        // (the second of the two sheet-presenting drops — see above)
        case (.done, .working):
            if let job { dragReactivateJob = job } else { vm.errorMessage = missingJobMessage(slug) }
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

private struct JobCardView: View {
    let job: WebJob

    private var hasPendingQuestion: Bool { job.question != nil }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline) {
                Text(job.title)
                    .font(.data(13, weight: .bold))
                    .foregroundStyle(KTheme.text)
                if job.archived == true, job.title != job.slug {
                    Text("as \(job.slug)")
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                }
                Spacer()
                if hasPendingQuestion {
                    Image(systemName: "flag.fill")
                        .foregroundStyle(KTheme.counter)
                        .font(.voice(10))
                }
            }
            Text(job.repo)
                .font(.data(11))
                .foregroundStyle(KTheme.text2)
            HStack(spacing: 6) {
                Text(job.state)
                    .font(.data(10))
                    .foregroundStyle(BoardTheme.color(forState: job.state))
                if job.live, let cell = job.cell {
                    Text("· \(cell)").font(.data(10)).foregroundStyle(KTheme.text2)
                }
                if let wakeup = job.wakeup, job.state == "working" {
                    WakeupChip(wakeup: wakeup)
                }
            }
            if let wakeup = job.wakeup, !wakeup.reason.isEmpty, job.state == "working" {
                Text("↻ " + wakeup.reason)
                    .font(.data(10))
                    .foregroundStyle(KTheme.text2)
                    .lineLimit(1)
            }
            if let prs = job.prs, !prs.isEmpty {
                HStack(spacing: 6) {
                    ForEach(prs, id: \.url) { pr in
                        PRChip(pr: pr)
                    }
                }
            }
            if let health = job.health, !health.isEmpty {
                Text("⚠ " + health).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            if let err = job.err, !err.isEmpty {
                Text("✕ " + err).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            CrewChipsView(zeusStatus: job.zeusStatus, roster: job.roster ?? [])
            if let roster = job.roster, !roster.isEmpty {
                VStack(alignment: .leading, spacing: 1) {
                    ForEach(Array(roster.enumerated()), id: \.offset) { _, agent in
                        HStack(spacing: 4) {
                            Text(agent.working == true ? "●" : "○")
                                .foregroundStyle(agent.working == true ? KTheme.solved : KTheme.text2)
                            Text("\(agent.name) (\(agent.model))")
                                .foregroundStyle(KTheme.text2)
                        }
                        .font(.data(9))
                    }
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .cardBackground(stateColor: BoardTheme.color(forState: job.state),
                        jobColor: BoardTheme.jobColor(job.color),
                        flagged: hasPendingQuestion)
    }
}

/// The "table of contents" reached from the overview strip's chevron: all
/// six columns as sections, every job as a compact row, tapping one opens
/// JobDetail. Reads `vm` (the shared @Observable board view model) directly
/// rather than a snapshot, so it stays in sync as JobDetail actions refresh
/// the board underneath it — no separate fetch of its own.
private struct BoardOverviewSheet: View {
    let vm: BoardViewModel
    let baseURL: String
    var onUpdate: () -> Void

    @Environment(\.dismiss) private var dismiss
    @Environment(AppRouter.self) private var router
    @State private var selectedJob: WebJob?
    // See BoardView's identically-named property + presentJob(_:) above —
    // same staleness-baseline pattern, duplicated here because this sheet
    // reads `vm` directly rather than owning its own selectedJob binding at
    // the BoardView level.
    @State private var presentedJobState: String?
    // Same pushed-child suppression as BoardView's job sheet — see
    // SheetNavState above.
    @State private var jobSheetNav = SheetNavState()

    var body: some View {
        List {
            ForEach(BoardColumn.allCases) { column in
                let jobs = vm.jobs(in: column)
                kSection(column.title) {
                    if jobs.isEmpty {
                        Text("empty")
                            .font(.voice(12))
                            .foregroundStyle(KTheme.text2.opacity(0.5))
                    } else {
                        ForEach(jobs) { job in
                            Button {
                                selectedJob = job
                                presentedJobState = job.state
                            } label: {
                                overviewRow(job)
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
            }
        }
        .listStyle(.plain)
        .kListStyle()
        .navigationTitle("Board Overview")
        .navigationBarTitleDisplayMode(.inline)
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
        .onChange(of: selectedJob?.slug) { jobSheetNav.childPushed = false }
        // Same auto-dismiss-on-staleness as BoardView's own job sheet — this
        // sheet reads `vm` live (see its doc comment above), so the same
        // polling refresh drives it here too, and it stands down the same way
        // while a child view is pushed on the stack.
        .onChange(of: vm.board?.jobs.first(where: { $0.slug == selectedJob?.slug })?.state) { _, newState in
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

    private func overviewRow(_ job: WebJob) -> some View {
        let flagged = job.question != nil || !(job.health ?? "").isEmpty
        return HStack(spacing: 8) {
            Rectangle()
                .fill(BoardTheme.color(forState: job.state))
                .frame(width: 6, height: 6)
            Text(job.title)
                .font(.data(13))
                .foregroundStyle(KTheme.text)
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
