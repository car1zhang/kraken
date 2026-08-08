import Combine
import SwiftUI

// ChromeBar.swift — the HUD header bar (style.css :414-619, R2 §4).
//
// Two rows, diverging from `Chrome.tsx`'s single strip at carl's manual-gate
// direction. Row 1 doubles as the TITLEBAR (traffic lights over its leading
// edge, drag + double-click-zoom gestures): wordmark · daemon LED · daemon
// stat · cells stat · flag stat · build chip · usage widget · spacer · view
// tabs. Row 2: spacer · UPDATE · SHIP iOS · ◈ ACCENT · ▦ FX · ◐ THEME · the
// error slot (the web's clock corner minus the clock, also carl's call). Then
// the three banner strips, which are SIBLINGS of the bar rather than children —
// the same reason Chrome.tsx returns a fragment.
//
// The bar is one line, always. `flex-wrap: nowrap` becomes `.lineLimit(1)`
// plus `.layoutPriority`: the STATS give (they clip), and everything the
// pointer aims at keeps its full width.

// MARK: - the ⚑ number

/// The header's flag count: pending questions + unhealthy cells + open P0 doc
/// questions, over ACTIVE jobs.
enum FlagCount {
    /// The plain sum. Frozen separately from the extraction below so a test can
    /// reach the arithmetic without a board payload.
    static func count(pendingQuestions: Int, unhealthyCells: Int, openP0DocQuestions: Int) -> Int {
        pendingQuestions + unhealthyCells + openP0DocQuestions
    }

    /// The three inputs, read off a board. An ARCHIVED job contributes nothing
    /// at all — it is a drawer, not a column — and a SETTLED job (archived or
    /// done) contributes no doc questions, because its questions are not
    /// visible on the board and flagging the board for them would point at
    /// something nothing on that surface shows.
    static func forJobs(_ jobs: [WebJob]) -> Int {
        var questions = 0
        var unhealthy = 0
        var p0 = 0
        for job in jobs {
            let archived = job.archived == true
            if archived { continue }
            if job.question != nil { questions += 1 }
            if !(job.health ?? "").isEmpty { unhealthy += 1 }
            if job.state != "done" {
                p0 += (job.docQuestions ?? []).filter {
                    $0.importance == "P0" && ($0.answer ?? "").isEmpty
                }.count
            }
        }
        return count(pendingQuestions: questions, unhealthyCells: unhealthy, openP0DocQuestions: p0)
    }
}

// MARK: - the chrome's own state

/// The bar's small mutable corner: the ten-second mutation-error slot.
///
/// SEAM: `BoardStore.mutate` returns the server's error SENTENCE on failure and
/// also publishes it as `BoardStore.lastError` (50-contract.md, cross-slice
/// seams). The chrome bar MIRRORS that publisher into here rather than asking
/// every caller to push, so no other slice has to know this type exists — and
/// the bound is what the mirror is for: `lastError` is only cleared by the next
/// SUCCESSFUL mutation, so one refused drag would otherwise pin "✕ job is not
/// queued" over the corner indefinitely, and the corner is the board's only
/// "am I still ticking" signal. `reportError` stays callable directly for
/// anything that fails outside the board's write path.
@MainActor
final class ChromeModel: ObservableObject {
    /// How long a failed mutation keeps the error slot. Long enough to read a
    /// server refusal, short enough that a stale sentence never lingers.
    static let errorWindow: TimeInterval = 10

    @Published private(set) var errorText: String?
    private var clearTask: Task<Void, Never>?

    func reportError(_ message: String) {
        guard !message.isEmpty else { return }
        errorText = message
        clearTask?.cancel()
        clearTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(Self.errorWindow * 1_000_000_000))
            guard !Task.isCancelled else { return }
            self?.errorText = nil
        }
    }
}

// MARK: - the bar

struct ChromeBar: View {
    @EnvironmentObject private var router: MacRouter
    @EnvironmentObject private var store: BoardStore
    @EnvironmentObject private var overlays: OverlayRouter
    @EnvironmentObject private var chrome: ChromeModel
    /// Shared with the usage overlay — the widget and the drilldown are two
    /// surfaces of one 60s poll, which is why the root owns it.
    @EnvironmentObject private var usage: UsageModel
    @StateObject private var update = UpdateRun()
    @StateObject private var ship = ShipRun()
    @Environment(\.hudTheme) private var theme

    @AppStorage(SettingsKey.themeMode) private var themeMode = ThemeMode.dark.rawValue
    @AppStorage(SettingsKey.hudAccent) private var hudAccent = HudAccent.violet.rawValue
    @AppStorage(SettingsKey.hudFX) private var hudFX = "full"

    var body: some View {
        VStack(spacing: 0) {
            header
            // Siblings of the bar, in the web's own order.
            StaleBannerStrip(messages: staleMessages)
            BannerStrip(model: update.banner) { update.bannerAction($0) }
            BannerStrip(model: ship.banner) { ship.bannerAction($0) }
        }
        .onAppear {
            update.onOpenLog = { router.tab = .log }
            update.onToast = { overlays.toast($0) }
            ship.onOpenLog = { router.tab = .log }
            ship.onToast = { overlays.toast($0) }
        }
        .onReceive(store.$board) { board in
            update.ingestBoardRecord(board?.update)
            ship.updating = update.updating
            ship.ingestBoardRecord(board?.shipIOS)
            // Both machines paint off the wall clock (freshness windows,
            // elapsed seconds), so every board tick is also a repaint tick —
            // exactly what the web gets for free by re-rendering on each poll.
            update.repaint()
            ship.repaint()
        }
        .onReceive(store.$lastError) { sentence in
            guard let sentence, !sentence.isEmpty else { return }
            chrome.reportError(sentence)
        }
    }

    private var header: some View {
        VStack(spacing: 0) {
            // ROW 1 doubles as the window's TITLEBAR: the window style is
            // `.hiddenTitleBar`, the traffic lights float over the leading
            // ~78pt (hence the leading pad), and the drag gesture makes the
            // strip move the window exactly as a real titlebar would —
            // carl's manual-gate ask: one integrated top bar, not a system
            // strip above a HUD strip.
            HStack(spacing: 8) {
                wordmark
                DaemonLED(ok: ledOK)
                stat(daemonText)
                stat(store.board.map { "cells \($0.liveCells)/\($0.cellCap)" } ?? "")
                stat(flags > 0 ? "⚑ \(flags)" : "", tone: .hot)
                stat(update.buildChip, tone: .build)
                UsageStat(usage: usage.data) { overlays.openUsage() }
                Spacer(minLength: 8)
                viewTabs
            }
            .padding(.leading, 78)
            .padding(.trailing, 16)
            .padding(.top, 6)
            .padding(.bottom, 4)
            .contentShape(Rectangle())
            // Titlebar parity, both halves: double-click ZOOMS (the system
            // titlebar's own double-click action — "expand to fill the
            // screen"), and a drag moves the window. The double-tap gesture is
            // attached FIRST so a stationary double-click wins; any movement
            // hands the sequence to the drag.
            .gesture(TapGesture(count: 2).onEnded {
                NSApp.keyWindow?.performZoom(nil)
            })
            .gesture(WindowDragGesture())
            // ROW 2: every control that was crowding row 1's right side —
            // update/ship, the three HUD toggles, and the error slot.
            HStack(spacing: 8) {
                Spacer(minLength: 8)
                HudRunControl(model: update.button) { update.click() }
                HudRunControl(model: ship.button) { ship.click() }
                hudButton("◈ " + hudAccent.uppercased(), minWidth: 72, help: "accent palette: violet → cyan → amber → crimson → green") {
                    cycleAccent()
                }
                hudButton("▦ FX " + (hudFX == "off" ? "OFF" : "ON"), minWidth: 60, help: "CRT scanlines on/off") {
                    hudFX = hudFX == "off" ? "full" : "off"
                }
                hudButton("◐ " + themeMode.uppercased(), minWidth: 72, help: "theme: system → light → dark") {
                    cycleTheme()
                }
                errorSlot
            }
            .padding(.horizontal, 16)
            .padding(.bottom, 6)
        }
        .background(theme.chromeFill)
        .overlay(alignment: .bottom) {
            Rectangle().fill(theme.borderColor).frame(height: 1)
        }
    }

    // MARK: pieces

    private var wordmark: some View {
        // The trailing `▞` is an ACCENT MARK — style.css styles it like the
        // web clock's block cursor but only the cursor blinked, and the mac
        // bar dropped the clock entirely, so this glyph stays still.
        HStack(spacing: 0) {
            Text("KRAKEN").foregroundStyle(theme.textColor)
            Text("▞").foregroundStyle(theme.accentColor).hudGlow(theme.textGlowStyle)
        }
        .hudDisplay(size: 16, weight: .bold, tracking: HudTracking.wordmark)
        .lineLimit(1)
        .layoutPriority(2)
    }

    private enum StatTone { case plain, hot, build }

    @ViewBuilder
    private func stat(_ text: String, tone: StatTone = .plain) -> some View {
        // `.stat:empty { display: none }` — an empty segment draws no border.
        if !text.isEmpty {
            Text(text)
                .hudMono(size: 9, tracking: HudTracking.stat)
                .foregroundStyle(tone == .hot ? theme.counterColor : theme.mutedColor)
                .opacity(tone == .build ? 0.6 : 1)
                .lineLimit(1)
                .truncationMode(.tail)
                .padding(.horizontal, 7)
                .padding(.vertical, 2)
                .overlay {
                    Rectangle().strokeBorder(
                        tone == .hot ? HudTheme.mix(theme.ac2, 40, over: theme.b1).color
                                     : theme.borderColor,
                        lineWidth: 1
                    )
                }
        }
    }

    private var viewTabs: some View {
        HStack(spacing: 1) {
            ForEach(RootTab.allCases) { tab in
                ViewTab(tab: tab, active: router.tab == tab) { router.tab = tab }
            }
        }
        .padding(.leading, 4)
        .layoutPriority(2)
    }

    private func hudButton(
        _ label: String, minWidth: CGFloat, help: String, action: @escaping () -> Void
    ) -> some View {
        // min-widths sized to each button's WIDEST label: these are clicked
        // repeatedly while watching this exact corner, and a label that resizes
        // on every click shifts its neighbours sideways mid-cycle.
        Button(action: action) {
            Text(label)
                .hudDisplay(size: 9, tracking: HudTracking.control)
                .foregroundStyle(theme.mutedColor)
                .lineLimit(1)
                .frame(minWidth: minWidth)
                .padding(.horizontal, 8)
                .padding(.vertical, 2)
                .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
        }
        .buttonStyle(.plain)
        .help(help)
        .hudCursor(.actable)
        .layoutPriority(2)
    }

    /// The web's tick-stat clock was dropped at the manual gate (a menu-bar
    /// Mac already shows the time); what SURVIVES is the corner's other job —
    /// the failure line for the shared mutation path, rendered only while a
    /// refusal is live and nothing at all otherwise.
    @ViewBuilder
    private var errorSlot: some View {
        if let err = chrome.errorText {
            Text("✕ " + err)
                .lineLimit(1)
                .truncationMode(.tail)
                .hudMono(size: 10, tracking: HudTracking.tight)
                .foregroundStyle(theme.accentColor)
                .opacity(0.7)
                .padding(.horizontal, 2)
        }
    }

    // MARK: derived text

    private var flags: Int { FlagCount.forJobs(store.board?.jobs ?? []) }

    private var ledOK: Bool { store.board?.daemonOK == true }

    /// A board fetch that failed says so; mid-update the server is gone ON
    /// PURPOSE, and "unreachable" would read as a crash.
    private var daemonText: String {
        guard let b = store.board else {
            return update.restarting ? "daemon — restarting" : "connecting…"
        }
        guard b.daemonOK else { return "daemon not running" }
        let elapsed = Int(Date().timeIntervalSince1970) - Int(b.daemon.lastTick / 1000)
        return "daemon pid \(b.daemon.pid) · tick \(compactAgeLabel(secondsElapsed: elapsed)) ago"
    }

    /// Both stale fields are absent on an older server, which reads as "not
    /// stale" — never a hidden error.
    private var staleMessages: [String] {
        var out: [String] = []
        guard let b = store.board else { return out }
        if b.stale == true {
            out.append("kraken was reinstalled since this server started — restart kraken serve")
        }
        if b.daemonOK, b.daemon.stale == true {
            out.append("kraken was reinstalled since the daemon started — restart the daemon")
        }
        return out
    }

    // MARK: the two cycles

    /// The web cycles system → light → dark → system. The mac app has no
    /// The stored preference is three-valued like the web's (`system` follows
    /// the macOS appearance live; `RootView.resolvedMode` does the resolving) —
    /// the frozen `ThemeMode` enum stays two-valued, only the PREF grew.
    private func cycleTheme() {
        let cycle = ["system", "light", "dark"]
        let i = cycle.firstIndex(of: themeMode) ?? 0
        themeMode = cycle[(i + 1) % cycle.count]
    }

    private func cycleAccent() {
        let cases = HudAccent.allCases
        let current = HudAccent(rawValue: hudAccent) ?? .violet
        let i = cases.firstIndex(of: current) ?? 0
        hudAccent = cases[(i + 1) % cases.count].rawValue
    }
}

// MARK: - the LED

/// `.led` (style.css :459-465): pink and PULSING when anything is wrong, green
/// and STEADY when the daemon is fine — `animation: none` on `.led.ok`.
struct DaemonLED: View {
    let ok: Bool
    @Environment(\.hudTheme) private var theme

    var body: some View {
        Group {
            if ok {
                dot(theme.okColor).hudGlow(theme.glowOKStyle)
            } else {
                dot(theme.counterColor).hudGlow(theme.glowHotStyle).hudPulse()
            }
        }
        .help(ok ? "daemon ok" : "daemon not answering")
    }

    private func dot(_ color: Color) -> some View {
        Rectangle().fill(color).frame(width: 7, height: 7)
    }
}

// MARK: - view tabs

struct ViewTab: View {
    let tab: RootTab
    let active: Bool
    let action: () -> Void
    @Environment(\.hudTheme) private var theme

    /// DREAM wears its skill colour when active — the /dream chip, the report
    /// accents and this tab are one surface family, and it is the cheapest way
    /// to say "this view belongs to the nightly loop, not the board".
    private var activeFill: Color {
        if tab == .dream, let c = SkillPalette.color("dream", theme.mode) { return c.color }
        return theme.accentColor
    }

    var body: some View {
        Button(action: action) {
            Text(tab.rawValue.uppercased())
                .hudDisplay(size: 10, tracking: HudTracking.tab)
                .foregroundStyle(active ? theme.accentInk : theme.mutedColor)
                .lineLimit(1)
                .padding(.horizontal, 12)
                .padding(.vertical, 4)
                .background {
                    if active {
                        // The kit's angular active tab: accent fill with the
                        // bottom-right corner sheared off.
                        ShearTrailing().fill(activeFill)
                    }
                }
                .overlay {
                    if active {
                        ShearTrailing().strokeBorder(activeFill, lineWidth: 1)
                    } else {
                        Rectangle().strokeBorder(theme.borderColor, lineWidth: 1)
                    }
                }
        }
        .buttonStyle(.plain)
        .hudCursor(.actable)
    }
}

// MARK: - the shared run control

/// One `.upd` button, worn by BOTH long-running-job controls: two buttons that
/// mean "kick off a slow thing and watch it" should not look like two different
/// kinds of thing.
struct HudRunControl: View {
    let model: HudRunButton
    let action: () -> Void
    @Environment(\.hudTheme) private var theme

    var body: some View {
        Button(action: action) {
            HStack(spacing: 5) {
                mark
                Text(model.label)
                    .hudDisplay(size: 9, tracking: HudTracking.control)
                    .lineLimit(1)
            }
            .foregroundStyle(tint)
            .padding(.horizontal, 8)
            .padding(.vertical, 2)
            .overlay { Rectangle().strokeBorder(edge, lineWidth: 1) }
            .opacity(model.disabled ? 0.85 : 1)
        }
        .buttonStyle(.plain)
        .disabled(model.disabled)
        .help(model.help)
        .hudCursor(model.disabled ? .rest : .actable)
    }

    @ViewBuilder
    private var mark: some View {
        switch model.mark {
        case .glyph(let g):
            Text(g).font(.hudMono(10))
        case .pip:
            Rectangle().fill(theme.warnColor).frame(width: 5, height: 5)
                .hudGlow(theme.glowWarnStyle)
        case .spinner:
            // A bordered ring with one `currentColor` arc, ticking through 8
            // positions. It replaced spinning the state GLYPH, which rotated
            // about its inline box's centre — below the glyph's optical centre,
            // so the arrow orbited instead of spinning. A square ring box
            // rotates about its true centre by construction.
            Circle()
                .trim(from: 0, to: 0.25)
                .stroke(tint, style: StrokeStyle(lineWidth: 1.5, lineCap: .butt))
                .background { Circle().strokeBorder(tint.opacity(0.25), lineWidth: 1.5) }
                .frame(width: 9, height: 9)
                .hudSpin()
        }
    }

    private var tint: Color {
        switch model.cls {
        case "avail": return theme.warnColor
        case "busy": return theme.accentColor
        case "gap": return theme.pollingColor
        case "ok": return theme.okColor
        case "bad": return theme.counterColor
        default: return theme.mutedColor
        }
    }

    private var edge: Color {
        switch model.cls {
        case "avail": return HudTheme.mix(theme.warn, 40, over: theme.b1).color
        case "busy": return HudTheme.mix(theme.ac, 45, over: theme.b1).color
        case "gap": return HudTheme.mix(theme.polling, 45, over: theme.b1).color
        case "ok": return HudTheme.mix(theme.ok, 45, over: theme.b1).color
        case "bad": return HudTheme.mix(theme.ac2, 45, over: theme.b1).color
        default: return theme.borderColor
        }
    }
}

// MARK: - banner strips

/// One banner strip. Every banner gets the same trailing ✕, appended centrally
/// rather than in each state so a new banner state cannot forget to be
/// dismissable.
struct BannerStrip: View {
    let model: HudBanner?
    let onAction: (String) -> Void
    @Environment(\.hudTheme) private var theme

    var body: some View {
        if let model {
            HStack(spacing: 8) {
                Text(model.text)
                    .hudMono(size: 10)
                    .foregroundStyle(tint)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 8)
                if let action = model.action {
                    Button(action.label) { onAction(action.act) }
                        .buttonStyle(.plain)
                        .hudDisplay(size: 9, tracking: HudTracking.control)
                        .foregroundStyle(tint)
                        .hudCursor(.actable)
                }
                Button("✕") { onAction("dismiss") }
                    .buttonStyle(.plain)
                    .foregroundStyle(tint)
                    .help("dismiss")
                    .hudCursor(.actable)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 6)
            .background(fill)
        }
    }

    private var tint: Color {
        switch model?.cls {
        case "cyan": return theme.pollingColor
        case "bad": return theme.counterColor
        default: return theme.okColor
        }
    }

    private var fill: Color {
        switch model?.cls {
        case "cyan": return theme.pollingSoft
        case "bad": return theme.counterSoft
        default: return theme.okSoft
        }
    }
}

/// The stale-binary strip (see `selfcheck.go`).
struct StaleBannerStrip: View {
    let messages: [String]
    @Environment(\.hudTheme) private var theme

    var body: some View {
        if !messages.isEmpty {
            Text("⚠ " + messages.joined(separator: " · "))
                .hudMono(size: 10)
                .foregroundStyle(theme.warnColor)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 16)
                .padding(.vertical, 6)
                .background(theme.warnSoft)
        }
    }
}
