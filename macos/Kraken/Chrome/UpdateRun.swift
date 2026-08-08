import AppKit
import Foundation

// UpdateRun.swift — the ⟳ UPDATE control's state machine, ported from
// `web/_ui/src/components/chrome/UpdateControl.tsx`.
//
// The button is a view over ONE server record — `board.update`, and GET
// /api/update/status, which are the same `webUpdate` shape — plus two facts
// only the client knows: whether this window WATCHED a run start, and the last
// refusal, which no server record carries because a refused update never
// happened.
//
// While a run is live it polls /api/update/status every second, with failures
// SWALLOWED. That is the whole trick and it is not defensive coding: an
// update's last act is `launchctl kickstart -k` on the very server answering
// these requests, so for a couple of seconds every fetch fails. That gap is
// the normal middle of a SUCCESSFUL update.
//
// ONE DELIBERATE OMISSION against the web original: there is no RELOAD. The
// web board reloads itself because the SPA is embedded in the binary that just
// got replaced, so a browser that stays put runs the old bundle against the new
// server. Kraken.app is a separate binary that `k update` does not touch, so
// there is nothing here to reload — and with it go `UPD_RELOAD_MS`, the
// `RELOAD NOW` banner action, the `reloading` flag and the `onBeforeReload`
// hook. `watchedRun` survives anyway: it is what keeps a window that merely
// READS a stale `done` record from claiming it watched anything.

/// One painted control. Shared with the ship button — the two are one widget
/// with two records behind them, and giving ship its own is exactly how they
/// drift apart.
struct HudRunButton: Equatable {
    /// "" | "busy" | "gap" | "ok" | "bad" | "avail" — the web's `.upd` classes,
    /// kept as a token so one painter can map them to theme colours.
    var cls: String = ""
    /// Whether the leading mark is a glyph, the availability pip, or the ring
    /// spinner.
    var mark: Mark = .glyph("⟳")
    var label: String = "UPDATE"
    var disabled: Bool = false
    var help: String = ""

    enum Mark: Equatable {
        case glyph(String)
        case spinner
        case pip
    }
}

/// One banner strip under the bar.
struct HudBanner: Equatable {
    /// "" | "cyan" | "bad".
    var cls: String = ""
    var text: String = ""
    var action: Action?
    /// The run's `updated_at`. Dismissal is keyed to the RUN so the next run —
    /// which necessarily has a newer stamp — still gets to speak.
    var stamp: Int64 = 0

    struct Action: Equatable {
        var act: String
        var label: String
    }
}

@MainActor
final class UpdateRun: ObservableObject {
    /// `jobs.UpdateTerminal` — no run in progress, so the button is live again.
    static let terminal: Set<String> = ["idle", "done", "failed", "uptodate"]
    /// How long a finished run keeps the button. `~/.kraken/update.json` holds
    /// its last record forever, so without a window every launch for the rest
    /// of time would reopen a green ✓ from days ago.
    static let recent: TimeInterval = 60
    /// Past this, a restart that has not come back has stopped being a restart.
    static let gapMax: TimeInterval = 120
    static let pollInterval: TimeInterval = 1

    @Published private(set) var button = HudRunButton()
    @Published private(set) var banner: HudBanner?
    /// The header's `@sha` chip. Empty on an unstamped `go build`, which the
    /// bar hides rather than showing "dev".
    @Published private(set) var buildChip = ""
    /// True while the server is expected to be down — the daemon stat says
    /// "daemon — restarting" rather than "unreachable".
    @Published private(set) var restarting = false
    /// True while any update run is in flight — the ship poll's fail ceiling.
    @Published private(set) var updating = false

    private var state: WebUpdate?
    private var polling = false
    private var refusal = ""
    private var refusalAt: Date?
    private var starting = false
    private var gapSince: Date?
    private var failStreak = 0
    private var failBase = 0
    /// This window saw a run start. Only such a window treats the run as its
    /// own; one opened afterwards reads the same `done` record and must not.
    private var watchedRun = false
    private var poller: Poller?
    private var observers: [NSObjectProtocol] = []

    var onOpenLog: () -> Void = {}
    var onToast: (String) -> Void = { _ in }

    init() {
        poller = Poller(interval: Self.pollInterval) { [weak self] in
            await self?.pollTick()
        }
        // The web resets its gap clock on `visibilitychange` because Chrome
        // freezes hidden tabs. A Mac app is not frozen, but a SLEEPING Mac has
        // the identical failure: the wall clock jumps across the sleep and the
        // first tick back measures the whole stretch as one outage. Waking and
        // re-activating both reset it.
        let center = NSWorkspace.shared.notificationCenter
        observers.append(center.addObserver(
            forName: NSWorkspace.didWakeNotification, object: nil, queue: .main
        ) { _ in Task { @MainActor [weak self] in self?.resetGap() } })
        observers.append(NotificationCenter.default.addObserver(
            forName: NSApplication.didBecomeActiveNotification, object: nil, queue: .main
        ) { _ in Task { @MainActor [weak self] in self?.resetGap() } })
        repaint()
    }

    deinit {
        let workspace = NSWorkspace.shared.notificationCenter
        for observer in observers {
            workspace.removeObserver(observer)
            NotificationCenter.default.removeObserver(observer)
        }
    }

    private func resetGap() {
        gapSince = nil
        failBase = failStreak
        repaint()
    }

    // MARK: - ingest

    /// The 3s board poll's copy of the record. The 1s status poll wins outright
    /// while it is running: the two race, and a board response can easily be
    /// older than a status response that landed just before it — applying it
    /// would rewind the phase on screen.
    func ingestBoardRecord(_ record: WebUpdate?) {
        guard !polling, let record else { return }
        if !Self.terminal.contains(record.phase ?? "idle") {
            // A run someone else started — the CLI, another window. Nothing
            // here reloads, but the gap/restart paint is still the truth about
            // the server this window is talking to.
            watchedRun = true
            refusal = ""
            refusalAt = nil
            startPolling()
        }
        ingest(record)
    }

    private func ingest(_ record: WebUpdate?) {
        guard let record else { return }
        state = record
        gapSince = nil
        repaint()
    }

    private func startPolling() {
        guard !polling else { return }
        polling = true
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
            let record = try await APIClient.shared.updateStatus(baseURL: MacAPI.baseURL)
            failStreak = 0
            failBase = 0
            ingest(record)
            if Self.terminal.contains(record.phase ?? "idle") { stopPolling() }
        } catch {
            // SWALLOWED — the restart is the normal middle of a successful run.
            failStreak += 1
            noteGap()
        }
    }

    /// The restart gap, and the point past which it stops being one.
    private func noteGap() {
        guard failStreak > failBase else { return }
        if gapSince == nil { gapSince = Date() }
        // Two resets are not enough on their own: a sleep can land between one
        // and the next tick. The ceiling additionally needs two failures BACK
        // TO BACK — at 1s a real two-minute outage racks up a hundred, while a
        // single blip on wake is followed a second later by a success.
        if failStreak - failBase >= 2,
           let since = gapSince, Date().timeIntervalSince(since) > Self.gapMax {
            state = Self.synthesize(
                state,
                phase: "failed",
                error: "kraken serve did not come back after the restart — see ~/.kraken/update.log"
            )
            stopPolling()
        }
        repaint()
    }

    /// A locally-synthesized record. `WebUpdate` keeps its memberwise init
    /// (its decoding lives in an extension for exactly this reason), so the
    /// optimistic `preflight` paint and the gap ceiling can both write one.
    private static func synthesize(
        _ prev: WebUpdate?, phase: String, error: String, fromSHA: String? = nil,
        startedAt: Int64? = nil
    ) -> WebUpdate {
        let now = Int64(Date().timeIntervalSince1970 * 1000)
        return WebUpdate(
            phase: phase,
            fromSHA: fromSHA ?? prev?.fromSHA,
            toSHA: fromSHA == nil ? prev?.toSHA : "",
            behind: prev?.behind,
            error: error,
            startedAt: startedAt ?? prev?.startedAt,
            updatedAt: now,
            buildSHA: prev?.buildSHA,
            checkedAt: prev?.checkedAt
        )
    }

    // MARK: - the click

    /// No confirmation step: the click IS the decision.
    ///
    /// Deliberately not routed through `BoardStore.mutate` — a refusal here is
    /// one specific sentence about the machine's state ("… has uncommitted
    /// changes", "… is on carl/foo, not main"), and the clock corner's ten
    /// seconds is exactly the wrong place for a paragraph.
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
            let resp = try await APIClient.shared.startUpdate(baseURL: MacAPI.baseURL)
            watchedRun = true
            // The handler writes its claim (phase preflight) before spawning,
            // so paint it now rather than waiting out the first poll tick.
            let now = Int64(Date().timeIntervalSince1970 * 1000)
            ingest(Self.synthesize(
                state,
                phase: resp.phase.isEmpty ? "preflight" : resp.phase,
                error: "",
                fromSHA: resp.fromSHA,
                startedAt: now
            ))
            startPolling()
        } catch {
            let msg = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            refusal = msg.isEmpty ? "update refused" : msg
            refusalAt = Date()
            onToast("✕ " + refusal)
        }
        starting = false
        repaint()
    }

    func bannerAction(_ act: String) {
        if act == "log" { onOpenLog() }
        if act == "dismiss", let stamp = state?.updatedAt, stamp != 0 {
            UserDefaults.standard.set(String(stamp), forKey: "kraken-dismissed-upd")
            repaint()
        }
    }

    // MARK: - paint

    private enum ButtonAction { case update, log }
    private var buttonAction: ButtonAction = .update

    /// Recomputed whenever anything the paint reads changes. The web does this
    /// in its render body; here it is one function so the published button and
    /// banner cannot disagree about which branch won.
    func repaint() {
        let u = state
        let phase = u?.phase ?? "idle"
        let running = !Self.terminal.contains(phase)
        let updatedAt = u?.updatedAt ?? 0
        let startedAt = u?.startedAt ?? 0
        let now = Date().timeIntervalSince1970 * 1000
        // Bounded on BOTH sides: an `updated_at` in the future (clock skew, a
        // corrupt update.json) is otherwise fresh forever, and a `failed`
        // record that is fresh forever pins the button at UPDATE FAILED, whose
        // click opens the log instead of starting a run — so it would never
        // update again.
        let fresh = updatedAt != 0 && abs(now - Double(updatedAt)) < Self.recent * 1000
        let buildSHA = u?.buildSHA ?? ""
        let buildClause = buildSHA.isEmpty ? "" : " · running " + buildSHA
        let behind = u?.behind ?? 0
        let errText = u?.error ?? ""

        var next = HudRunButton()
        next.help = "update kraken — fast-forward main, rebuild, reinstall, restart the daemon"
            + buildClause
        var nextBanner: HudBanner?
        buttonAction = .update

        if !refusal.isEmpty, let at = refusalAt, Date().timeIntervalSince(at) < Self.recent {
            // Refused before anything was spawned (a 409 from the POST, already
            // toasted): nothing ran, so the record behind it is untouched and
            // the button stays clickable for the retry once the reason is fixed.
            next.cls = "bad"
            next.mark = .glyph("✕")
            next.label = "UPDATE BLOCKED"
            next.help = refusal
        } else if running && (gapSince != nil || phase == "restarting") {
            // Either the updater says it is restarting, or the endpoint stopped
            // answering mid-run — which IS the restart, seen from this side.
            next.cls = "gap"
            next.mark = .spinner
            next.label = "RESTARTING…"
            next.disabled = true
            nextBanner = HudBanner(
                cls: "cyan",
                text: "⟳ kraken serve is restarting on the new binary — board data is stale for a few seconds. Cells are unaffected.",
                stamp: updatedAt
            )
        } else if running {
            let secs = startedAt != 0 ? max(0, Int(((now - Double(startedAt)) / 1000).rounded())) : 0
            next.cls = "busy"
            next.mark = .spinner
            next.label = "\(phase.uppercased())… \(secs)s"
            next.disabled = true
        } else if phase == "failed" && fresh {
            // A failure after the updater detached cannot come back as an HTTP
            // status — the request returned long ago — so it arrives in the
            // record instead.
            next.cls = "bad"
            next.mark = .glyph("✕")
            next.label = "UPDATE FAILED"
            next.help = errText.isEmpty ? "update failed" : errText
            buttonAction = .log
            nextBanner = HudBanner(
                cls: "bad",
                text: "✕ update failed — " + (errText.isEmpty ? "no error recorded" : errText),
                action: .init(act: "log", label: "OPEN LOG"),
                stamp: updatedAt
            )
        } else if phase == "done" && fresh {
            let secs = startedAt != 0 && updatedAt != 0
                ? max(0, Int((Double(updatedAt - startedAt) / 1000).rounded())) : 0
            next.cls = "ok"
            next.mark = .glyph("✓")
            next.label = ("UPDATED " + (u?.toSHA ?? "")).trimmingCharacters(in: .whitespaces)
            nextBanner = HudBanner(
                cls: "",
                // No `· reloading…` and no RELOAD NOW: nothing this app is
                // running was replaced — see this file's header.
                text: "✓ updated \(u?.fromSHA ?? "?") → \(u?.toSHA ?? "?")"
                    + (secs != 0 ? " in \(secs)s" : ""),
                stamp: updatedAt
            )
        } else if phase == "uptodate" && fresh {
            // A run that found nothing to do built and restarted nothing.
            // Without this the click would flash busy and land back on a bare
            // UPDATE, which reads as "did that even do anything".
            next.cls = "ok"
            next.mark = .glyph("✓")
            next.label = "UP TO DATE"
        } else if behind > 0 {
            // The daemon's background availability check (every 10 minutes).
            next.cls = "avail"
            next.mark = .pip
            next.label = "UPDATE · \(behind) BEHIND"
            let checkedAt = u?.checkedAt ?? 0
            let checked = checkedAt != 0
                ? ", checked " + compactAgeLabel(
                    secondsElapsed: Int(now / 1000) - Int(checkedAt / 1000)
                ) + " ago"
                : ""
            next.help = "origin/main is \(behind) commit(s) ahead\(checked)\(buildClause)"
        }

        if let b = nextBanner, b.stamp != 0,
           UserDefaults.standard.string(forKey: "kraken-dismissed-upd") == String(b.stamp) {
            nextBanner = nil
        }

        next.disabled = next.disabled || starting
        button = next
        banner = nextBanner
        buildChip = (!buildSHA.isEmpty && buildSHA != "dev") ? "@" + buildSHA : ""
        restarting = polling
        updating = polling || running
    }
}
