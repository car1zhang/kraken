import Foundation

// ShipRun.swift — the ⇧ SHIP iOS control, ported from
// `web/_ui/src/components/chrome/ShipControl.tsx`.
//
// Structurally the update button's twin — same painted shape, the same "one
// server record plus the one refusal no record can carry" — with two
// deliberate differences that are the reason it is a separate state machine
// rather than a second caller of the update one:
//
//  1. NO restart-gap machinery of its own. A ship replaces nothing on this
//     Mac — serve, the daemon and the binary all stay up from preflight
//     through upload — so a failed status fetch here is a genuine error. The
//     one exception is not the ship's doing: a concurrent UPDATE ends in
//     `launchctl kickstart -k`, which takes serve down for a couple of
//     seconds, and the shipper is given its own session precisely so it
//     SURVIVES that. So the fail ceiling STRETCHES while an update is in
//     flight rather than the ship growing a gap clock.
//  2. Nothing to reload, and nothing that ever was — see UpdateRun's header.
//
// And the wording, which is the part worth getting right: `done` means the
// .ipa reached App Store Connect. TestFlight still has to process it, and
// someone still has to install it from the TestFlight app afterwards. So the
// control says UPLOADED, never SHIPPED.

@MainActor
final class ShipRun: ObservableObject {
    /// `jobs.ShipTerminal` — no run in flight, so the button is live again.
    static let terminal: Set<String> = ["idle", "done", "failed"]
    /// Twice update's window: update's is sized for a page that reloads itself
    /// the instant the run ends, whereas nothing here interrupts whoever is
    /// reading — and the thing they most need to read is the build number and
    /// the "not installed yet" note.
    static let recent: TimeInterval = 120
    static let pollInterval: TimeInterval = 1
    /// One dropped request is a hiccup; two in a row means the server really
    /// has stopped answering.
    static let failMax = 2
    /// …unless an update is in flight, whose `kickstart -k` is a real,
    /// expected, seconds-long outage the ship is built to survive. ~30s: ten
    /// times what a kickstart takes, and still short enough that a serve which
    /// never comes back is reported while anyone is still watching.
    static let failMaxUpdating = 30

    @Published private(set) var button = HudRunButton(mark: .glyph("⇧"), label: "SHIP iOS")
    @Published private(set) var banner: HudBanner?

    /// Written by the chrome from `UpdateRun.updating` — read at TICK time, so
    /// the ceiling sees the update's current state rather than whatever it was
    /// when the poll started.
    var updating = false

    private var state: WebShipIOS?
    private var polling = false
    private var refusal = ""
    private var refusalAt: Date?
    /// The POST is in flight but has not answered. A flag rather than the
    /// button's own `disabled`, because a board tick repaints the button and
    /// would hand a freshly enabled one back mid-flight — earning a 409 on the
    /// second click.
    private var starting = false
    private var failStreak = 0
    private var failBase = 0
    private var poller: Poller?

    var onOpenLog: () -> Void = {}
    var onToast: (String) -> Void = { _ in }

    init() {
        poller = Poller(interval: Self.pollInterval) { [weak self] in
            await self?.pollTick()
        }
        repaint()
    }

    // MARK: - ingest

    /// The 3s board poll's copy. As with update, the 1s status poll wins while
    /// it is running, or a board response older than a status response would
    /// rewind the phase on screen. This is also how the poll recovers after a
    /// lost-contact failure: the board poll is the first thing back, and it
    /// restarts the fast one.
    func ingestBoardRecord(_ record: WebShipIOS?) {
        guard !polling, let record else { return }
        if !Self.terminal.contains(record.phase ?? "idle") { startPolling() }
        ingest(record)
    }

    /// Also where a stale refusal dies. Two ways it can outlive its cause: a
    /// 409 refused this click because a run started elsewhere was already in
    /// flight (that run IS the answer to "what is happening"), and that same
    /// run then finishing, whose "uploaded build N" banner is the one thing
    /// worth reading.
    private func ingest(_ record: WebShipIOS?) {
        guard let record else { return }
        let running = !Self.terminal.contains(record.phase ?? "idle")
        let stamp = record.updatedAt ?? 0
        if !refusal.isEmpty {
            let refusedAtMillis = Int64((refusalAt?.timeIntervalSince1970 ?? 0) * 1000)
            if running || stamp > refusedAtMillis {
                refusal = ""
                refusalAt = nil
            }
        }
        state = record
        repaint()
    }

    private func startPolling() {
        guard !polling else { return }
        polling = true
        // Baselined on each re-arm: tripping the ceiling stops the fast poll
        // and the board poll restarts it, so without this the stale streak
        // rides through and the FIRST failed tick re-synthesizes the failure.
        failBase = failStreak
        poller?.start()
        repaint()
    }

    private func stopPolling() {
        guard polling else { return }
        polling = false
        poller?.pause()
        repaint()
    }

    private func pollTick() async {
        do {
            let record = try await APIClient.shared.shipIOSStatus(baseURL: MacAPI.baseURL)
            failStreak = 0
            failBase = 0
            ingest(record)
            if Self.terminal.contains(record.phase ?? "idle") { stopPolling() }
        } catch {
            failStreak += 1
            noteFailure()
        }
    }

    private func noteFailure() {
        let streak = failStreak - failBase
        guard streak > 0 else { return }
        let ceiling = updating ? Self.failMaxUpdating : Self.failMax
        guard streak >= ceiling else {
            repaint()
            return
        }
        // Past the ceiling. Say what is and is not known: the shipper is a
        // detached child of a process that may well have died under it, so the
        // upload itself could still be running — the log is the only witness.
        let now = Int64(Date().timeIntervalSince1970 * 1000)
        state = WebShipIOS(
            phase: "failed",
            build: state?.build,
            error: "kraken serve stopped answering mid-ship — the run itself may still be going; see ~/.kraken/ship-ios.log",
            startedAt: state?.startedAt,
            updatedAt: now
        )
        stopPolling()
    }

    // MARK: - the click

    func click() {
        if buttonAction == .log {
            onOpenLog()
            return
        }
        guard !starting else { return }
        Task { await start() }
    }

    private func start() async {
        refusal = ""
        refusalAt = nil
        starting = true
        repaint()
        do {
            let resp = try await APIClient.shared.startShipIOS(baseURL: MacAPI.baseURL)
            let now = Int64(Date().timeIntervalSince1970 * 1000)
            ingest(WebShipIOS(
                phase: resp.phase.isEmpty ? "preflight" : resp.phase,
                build: 0,
                error: "",
                startedAt: now,
                updatedAt: now
            ))
            startPolling()
        } catch {
            let msg = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            refusal = msg.isEmpty ? "ship refused" : msg
            refusalAt = Date()
            onToast("✕ " + refusal)
        }
        starting = false
        repaint()
    }

    func bannerAction(_ act: String) {
        // No RELOAD NOW twin — a ship replaces nothing this app is running.
        if act == "ship-log" { onOpenLog() }
        if act == "dismiss", let stamp = state?.updatedAt, stamp != 0 {
            UserDefaults.standard.set(String(stamp), forKey: "kraken-dismissed-ship")
            repaint()
        }
    }

    // MARK: - paint

    private enum ButtonAction { case ship, log }
    private var buttonAction: ButtonAction = .ship

    func repaint() {
        let s = state
        let phase = s?.phase ?? "idle"
        let running = !Self.terminal.contains(phase)
        let updatedAt = s?.updatedAt ?? 0
        let startedAt = s?.startedAt ?? 0
        let now = Date().timeIntervalSince1970 * 1000
        let fresh = updatedAt != 0 && abs(now - Double(updatedAt)) < Self.recent * 1000
        let buildClause = (s?.build ?? 0) != 0 ? " b\(s?.build ?? 0)" : ""
        let errText = s?.error ?? ""

        var next = HudRunButton(mark: .glyph("⇧"), label: "SHIP iOS")
        // The default help carries the honest version of what the button does,
        // so the distinction is on screen before anyone clicks rather than only
        // after.
        next.help = "build the iOS app and upload it to App Store Connect — TestFlight then "
            + "takes a few minutes to process it, and the new build still has to be "
            + "installed from the TestFlight app on the phone"
        var nextBanner: HudBanner?
        buttonAction = .ship

        if starting && !running {
            // The click landed, the POST has not answered, and the record still
            // reads terminal — without this the next repaint hands the button
            // back and a second click earns a 409.
            next.cls = "busy"
            next.mark = .spinner
            next.label = "STARTING…"
            next.disabled = true
        } else if !refusal.isEmpty, let at = refusalAt,
                  Date().timeIntervalSince(at) < Self.recent {
            next.cls = "bad"
            next.mark = .glyph("✕")
            next.label = "SHIP BLOCKED"
            next.help = refusal
        } else if running {
            let secs = startedAt != 0 ? max(0, Int(((now - Double(startedAt)) / 1000).rounded())) : 0
            next.cls = "busy"
            next.mark = .spinner
            next.label = "\(phase.uppercased())… \(secs)s"
            next.disabled = true
            next.help = "ship in flight — archiving and uploading takes several minutes" + buildClause
        } else if phase == "failed" && fresh {
            next.cls = "bad"
            next.mark = .glyph("✕")
            next.label = "SHIP FAILED"
            next.help = errText.isEmpty ? "ship failed" : errText
            buttonAction = .log
            nextBanner = HudBanner(
                cls: "bad",
                text: "✕ ship" + (buildClause.isEmpty ? "" : " of build \(s?.build ?? 0)")
                    + " failed — " + (errText.isEmpty ? "no error recorded" : errText),
                action: .init(act: "ship-log", label: "OPEN LOG"),
                stamp: updatedAt
            )
        } else if phase == "done" && fresh {
            let secs = startedAt != 0 && updatedAt != 0
                ? max(0, Int((Double(updatedAt - startedAt) / 1000).rounded())) : 0
            next.cls = "ok"
            next.mark = .glyph("✓")
            // UPLOADED, not SHIPPED: the .ipa is at Apple, the phone is
            // unchanged.
            next.label = "UPLOADED" + buildClause
            next.help = "uploaded to App Store Connect — not yet installed anywhere"
            nextBanner = HudBanner(
                cls: "",
                text: "✓ build \((s?.build ?? 0) != 0 ? String(s?.build ?? 0) : "?") uploaded to App Store Connect"
                    + (secs != 0 ? " in \(secs)s" : "")
                    + " · TestFlight still has to process it (usually a few minutes), then install "
                    + "the new build from the TestFlight app on the phone — nothing on this Mac changed",
                stamp: updatedAt
            )
        }

        if let b = nextBanner, b.stamp != 0,
           UserDefaults.standard.string(forKey: "kraken-dismissed-ship") == String(b.stamp) {
            nextBanner = nil
        }

        button = next
        banner = nextBanner
    }
}
