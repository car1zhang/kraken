import AppKit
import SwiftUI

// UsagePanel.swift — the claude-code-pool usage widget and its drilldown
// (R2 §3.5; `web/_ui/src/components/overlays/UsageOverlay.tsx` and
// `components/chrome/UsageStat.tsx`).
//
// The "main" account is whichever entry carries `current: true` (the server
// sorts it first); everything else is pool. The whole surface is absent on an
// older server or offline — the widget stays HIDDEN rather than flashing broken
// markup, which is why revealing it is the header's call and never this file's.
//
// One reader of the payload shape, two mount points: the header widget and the
// panel are two surfaces of ONE rule, and a second copy of the 50/80 thresholds
// is a threshold that can silently disagree with itself. Hence `UsageBand`.

// MARK: - the band

/// The ok/warn/danger thresholds every usage number is coloured by.
enum UsageBand {
    case ok, warn, danger

    static func band(_ pct: Int) -> UsageBand {
        if pct >= 80 { return .danger }
        if pct >= 50 { return .warn }
        return .ok
    }

    /// The same rule off the wire's `Double`. FLOORED rather than rounded, so
    /// the band agrees with the web: `usageColorClass` there tests the raw
    /// percent, so 79.6 is `warn` even though it renders as "80%". Rounding
    /// here would tip exactly those values into `danger` and make the two
    /// clients disagree about the same account.
    static func band(percent: Double) -> UsageBand {
        band(Int(max(0, percent).rounded(.down)))
    }

    func color(_ theme: HudTheme) -> Color {
        switch self {
        case .ok: return theme.okColor
        case .warn: return theme.warnColor
        case .danger: return theme.counterColor
        }
    }
}

/// `resets_at` (unix MILLIS) as a compact countdown — "3h12m", "4d", "8m",
/// "now" — at day/hour/minute granularity.
func usageCountdown(_ millis: Int64) -> String {
    let remaining = Int((Double(millis) - Date().timeIntervalSince1970 * 1000) / 1000)
    if remaining <= 0 { return "now" }
    let d = remaining / 86400
    if d >= 1 { return "\(d)d" }
    let h = remaining / 3600
    let m = (remaining % 3600) / 60
    return h >= 1 ? "\(h)h\(m)m" : "\(m)m"
}

// MARK: - the poll

/// The 60s `/api/usage` poll, shared by the header widget and the panel: a
/// rotation action does NOT render from its own response, it calls `refetch`,
/// which repaints both together.
@MainActor
final class UsageModel: ObservableObject {
    static let pollInterval: TimeInterval = 60

    @Published private(set) var data: UsageResponse?
    private var poller: Poller?

    init() {
        poller = Poller(interval: Self.pollInterval) { [weak self] in
            await self?.tick()
        }
    }

    func start() { poller?.start() }
    func stop() { poller?.pause() }

    /// Swallows its own errors — which is exactly why every caller re-enables
    /// its button in a `defer`: a POST success followed by a silently-failed
    /// refetch would otherwise leave a dead control until the next 60s tick.
    func refetch() async { await tick() }

    private func tick() async {
        guard let next = try? await APIClient.shared.fetchUsage(baseURL: MacAPI.baseURL) else {
            return
        }
        data = next
    }
}

// MARK: - the header widget

/// `#usage-stat`: `5h N%`, `7d N%`, one segment per model, `· pool N`, a
/// `manual` chip when pinned, `(stale Xm)` when the daemon is down or the
/// snapshot is over 15 minutes old. (The web's `worst 7d` clause was retired
/// from both apps at this job's manual gate.)
struct UsageStat: View {
    let usage: UsageResponse?
    let onOpen: () -> Void
    @Environment(\.hudTheme) private var theme

    var body: some View {
        // Hidden until /api/usage parses ONCE — a 404 from an older server or a
        // transient network error never flashes broken markup into view.
        if let usage {
            Button(action: onOpen) {
                content(usage)
                    .hudMono(size: 9, tracking: HudTracking.stat)
                    .lineLimit(1)
                    .truncationMode(.tail)
                    .padding(.horizontal, 7)
                    .padding(.vertical, 2)
                    .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
            }
            .buttonStyle(.plain)
            .help("usage — claude code pool")
            .hudCursor(.actable)
        }
    }

    @ViewBuilder
    private func content(_ data: UsageResponse) -> some View {
        let main = mainAccount(data)
        let pool = data.accounts.filter { $0.slug != main?.slug }
        let stale = !data.daemonOK || data.ageSeconds > 900
        let manual = data.rotationMode == "manual"
        if !data.available || data.accounts.isEmpty {
            Text("usage —").foregroundStyle(theme.mutedColor).opacity(0.7)
        } else {
            HStack(spacing: 5) {
                if let w = main?.session { seg("5h", w.percent) }
                if let w = main?.weekly { seg("7d", w.percent) }
                ForEach(main?.models ?? [], id: \.name) { m in
                    seg(m.name.lowercased(), m.percent)
                }
                if !pool.isEmpty {
                    Text("·").foregroundStyle(theme.mutedColor).opacity(0.4)
                    Text("pool \(pool.count)").foregroundStyle(theme.mutedColor)
                }
                if manual {
                    Text("·").foregroundStyle(theme.mutedColor).opacity(0.4)
                    Text("manual").foregroundStyle(theme.warnColor)
                }
                if stale {
                    Text("(stale \(data.ageSeconds / 60)m)")
                        .foregroundStyle(theme.mutedColor).opacity(0.55)
                }
            }
        }
    }

    private func seg(_ label: String, _ percent: Double) -> some View {
        Text("\(label) \(Int(percent.rounded()))%")
            .foregroundStyle(UsageBand.band(percent: percent).color(theme))
    }
}

/// The main account: whichever entry carries `current`, else the first.
func mainAccount(_ data: UsageResponse) -> UsageAccount? {
    data.accounts.first(where: { $0.current == true }) ?? data.accounts.first
}

// MARK: - the drilldown

struct UsagePanel: View {
    @ObservedObject var usage: UsageModel
    @Environment(\.hudTheme) private var theme
    /// Gates BOTH rotation handlers against a double-fire: a rapid double
    /// click, or the 60s poll replacing the button mid-click. A per-button
    /// disabled flag alone cannot catch the second — a freshly re-rendered
    /// button starts out enabled again while the first request is in flight.
    @State private var inFlight = false
    @State private var toast: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            head
            Divider().overlay(theme.dividerColor)
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if let data = usage.data, data.rotationMode == "manual" {
                        manualBanner(data)
                    }
                    if let accounts = usage.data?.accounts, !accounts.isEmpty {
                        ForEach(accounts) { account in
                            AccountCard(
                                account: account,
                                main: usage.data.flatMap(mainAccount),
                                rotationKnown: usage.data?.rotationMode != nil,
                                disabled: inFlight,
                                onMakeMain: { makeMain(account) }
                            )
                        }
                    } else {
                        Text("no usage data yet")
                            .hudMono(size: 11)
                            .foregroundStyle(theme.mutedColor)
                    }
                    if let toast {
                        Text(toast).hudMono(size: 10).foregroundStyle(theme.counterColor)
                    }
                }
                .padding(14)
            }
        }
        .background(theme.surfaceColor)
        .onAppear { usage.start() }
    }

    private var head: some View {
        HStack(spacing: 10) {
            Text("usage — claude code pool")
                .hudDisplay(size: 11, tracking: HudTracking.stat)
                .foregroundStyle(theme.textColor)
            // Absent `rotation_mode` (an older server) renders nothing — the
            // same gate as everywhere else in this section.
            if let mode = usage.data?.rotationMode {
                chip("rotation: \(mode == "manual" ? "manual" : "auto")",
                     tone: mode == "manual" ? theme.warnColor : theme.okColor)
            }
            Spacer()
            fetchedNote
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 10)
    }

    @ViewBuilder
    private var fetchedNote: some View {
        if let data = usage.data, data.available {
            if !data.daemonOK {
                chip("daemon down — data frozen", tone: theme.counterColor)
            } else {
                let elapsed = Int(Date().timeIntervalSince1970) - Int(data.fetchedAt / 1000)
                Text("fetched \(compactAgeLabel(secondsElapsed: elapsed)) ago · daemon")
                    .hudMono(size: 9).foregroundStyle(theme.mutedColor)
            }
        } else {
            Text("no data yet").hudMono(size: 9).foregroundStyle(theme.mutedColor)
        }
    }

    private func manualBanner(_ data: UsageResponse) -> some View {
        let who = mainAccount(data)?.email ?? "main"
        let setBy = data.rotationSetBy ?? "unknown"
        let setAt = data.rotationSetAt ?? 0
        let when = setAt != 0
            ? " " + compactAgeLabel(
                secondsElapsed: Int(Date().timeIntervalSince1970) - Int(setAt / 1000)
            ) + " ago"
            : ""
        return HStack(spacing: 10) {
            Text("pinned to \(who) by \(setBy)\(when) — auto-rotation off")
                .hudMono(size: 10)
                .foregroundStyle(theme.warnColor)
            Spacer()
            Button("switch back to auto") {
                run("couldn't switch to auto") {
                    try await APIClient.shared.setRotationMode(baseURL: MacAPI.baseURL, mode: "auto")
                }
            }
            .buttonStyle(.plain)
            .hudMono(size: 10)
            .foregroundStyle(theme.accentColor)
            .disabled(inFlight)
            .hudCursor(.actable)
        }
        .padding(10)
        .background(theme.warnSoft)
    }

    private func chip(_ text: String, tone: Color) -> some View {
        Text(text)
            .hudMono(size: 9, tracking: HudTracking.stat)
            .foregroundStyle(tone)
            .padding(.horizontal, 6)
            .padding(.vertical, 1)
            .overlay { Rectangle().strokeBorder(tone.opacity(0.45), lineWidth: 1) }
    }

    /// `make main` confirms first — every running session migrates and rotation
    /// pins to manual, which is not something to do by mis-click.
    private func makeMain(_ account: UsageAccount) {
        let who = account.email ?? account.slug
        let alert = NSAlert()
        alert.messageText = "switch main to \(account.slug)?"
        alert.informativeText = "\(who) — every running session migrates at its next token "
            + "refresh, and rotation pins to manual."
        alert.addButton(withTitle: "switch")
        alert.addButton(withTitle: "cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        run("couldn't switch to \(account.slug)") {
            try await APIClient.shared.switchAccount(baseURL: MacAPI.baseURL, slug: account.slug)
        }
    }

    private func run(_ errLabel: String, _ op: @escaping () async throws -> Void) {
        guard !inFlight else { return }
        inFlight = true
        Task {
            // ALWAYS cleared, not just on the catch path: `refetch()` swallows
            // its own network errors rather than throwing, so a POST success
            // followed by a silently-failed refetch would skip both the catch
            // branch and the re-render that would otherwise discard a disabled
            // button — leaving a dead control until the next 60s poll.
            defer { inFlight = false }
            do {
                try await op()
                toast = nil
            } catch {
                let msg = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
                toast = "\(errLabel): \(msg)"
            }
            await usage.refetch()
        }
    }
}

// MARK: - one account card

struct AccountCard: View {
    let account: UsageAccount
    let main: UsageAccount?
    let rotationKnown: Bool
    let disabled: Bool
    let onMakeMain: () -> Void
    @Environment(\.hudTheme) private var theme
    @State private var copyLabel = "⧉ ccp"

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            head
            if hasRows {
                VStack(alignment: .leading, spacing: 4) {
                    if let w = account.session { MeterRow(label: "session", window: w) }
                    if let w = account.weekly { MeterRow(label: "weekly", window: w) }
                    ForEach(account.models ?? [], id: \.name) { m in
                        MeterRow(
                            label: m.name.lowercased(),
                            window: UsageWindowValue(percent: m.percent, resetsAt: m.resetsAt)
                        )
                    }
                }
            }
        }
        .padding(10)
        .background(theme.raisedColor)
        .overlay { Rectangle().strokeBorder(theme.borderColor, lineWidth: 1) }
    }

    private var head: some View {
        HStack(spacing: 8) {
            Text(account.slug)
                .hudMono(size: 11, weight: .semibold)
                .foregroundStyle(theme.textColor)
            if account.current == true { chip("current", tone: theme.accentColor) }
            if let email = account.email, !email.isEmpty {
                Text(email).hudMono(size: 9).foregroundStyle(theme.mutedColor)
            }
            if let tier = strippedTier, !tier.isEmpty { chip(tier, tone: theme.mutedColor) }
            stateChips
            Spacer(minLength: 8)
            if canMakeMain {
                Button("make main", action: onMakeMain)
                    .buttonStyle(.plain)
                    .hudMono(size: 10)
                    .foregroundStyle(theme.accentColor)
                    .disabled(disabled)
                    .help("switch the main account to \(account.slug)")
                    .hudCursor(.actable)
            }
            if account.current != true {
                Button(copyLabel) { copyCcp() }
                    .buttonStyle(.plain)
                    .hudMono(size: 10)
                    .foregroundStyle(theme.mutedColor)
                    .help("copy ccp \(account.slug)")
                    .hudCursor(.actable)
            }
        }
    }

    @ViewBuilder
    private var stateChips: some View {
        if let cooldown = account.cooldownUntil,
           Double(cooldown) > Date().timeIntervalSince1970 * 1000 {
            chip("cooling \(usageCountdown(cooldown))", tone: theme.warnColor)
        }
        if account.reauth == true { chip("reauth needed — ccp login", tone: theme.counterColor) }
        if account.noCreds == true { chip("no creds", tone: theme.counterColor) }
        if account.stale == true { chip("stale", tone: theme.mutedColor) }
        if let err = account.error, !err.isEmpty {
            chip("error", tone: theme.counterColor).help(err)
        }
    }

    private var strippedTier: String? {
        guard let tier = account.tier else { return nil }
        return tier.hasPrefix("default_claude_")
            ? String(tier.dropFirst("default_claude_".count))
            : tier
    }

    private var hasRows: Bool {
        account.session != nil || account.weekly != nil || !(account.models ?? []).isEmpty
    }

    /// Any non-current account with usable creds. Cooling/stale rows still get
    /// the button (the server decides whether the switch succeeds); only
    /// `no_creds`/`reauth` do not. A row whose email MIRRORS the current main's
    /// is also excluded — that is the save-back slot a rotation just parked the
    /// outgoing main's creds into, and core refuses to ever rotate onto it
    /// (`core.ErrSaveBackIsTarget`), so the button would be guaranteed to fail.
    private var canMakeMain: Bool {
        let email = account.email ?? ""
        let mainEmail = main?.email ?? ""
        let mirrors = !email.isEmpty && !mainEmail.isEmpty
            && email.lowercased() == mainEmail.lowercased()
        return rotationKnown && account.current != true && account.noCreds != true
            && account.reauth != true && !mirrors
    }

    private func chip(_ text: String, tone: Color) -> some View {
        Text(text)
            .hudMono(size: 9, tracking: HudTracking.stat)
            .foregroundStyle(tone)
            .padding(.horizontal, 6)
            .padding(.vertical, 1)
            .overlay { Rectangle().strokeBorder(tone.opacity(0.45), lineWidth: 1) }
    }

    /// The web needs a `document.execCommand` fallback because the board is
    /// served over plain http on the tailnet, where `navigator.clipboard` is
    /// absent. `NSPasteboard` has no such gate, so the fallback has no mac
    /// twin — the 1.2s `copied` flip does.
    private func copyCcp() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString("ccp \(account.slug)", forType: .string)
        copyLabel = "copied"
        Task {
            try? await Task.sleep(nanoseconds: 1_200_000_000)
            copyLabel = "⧉ ccp"
        }
    }
}

/// The shape `MeterRow` needs, so a `UsageModelWindow` and a `UsageWindow` can
/// both feed it without either wire type growing a protocol conformance in the
/// read-only `ios/` sources.
struct UsageWindowValue {
    let percent: Double
    let resetsAt: Int64?
}

extension UsageWindow {
    var asValue: UsageWindowValue { UsageWindowValue(percent: percent, resetsAt: resetsAt) }
}

struct MeterRow: View {
    let label: String
    let window: UsageWindowValue
    @Environment(\.hudTheme) private var theme

    init(label: String, window: UsageWindowValue) {
        self.label = label
        self.window = window
    }

    init(label: String, window: UsageWindow) {
        self.init(label: label, window: window.asValue)
    }

    var body: some View {
        let band = UsageBand.band(percent: window.percent)
        HStack(spacing: 8) {
            Text(label)
                .hudMono(size: 9)
                .foregroundStyle(theme.mutedColor)
                .frame(width: 62, alignment: .leading)
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Rectangle().fill(theme.chipBgColor)
                    Rectangle()
                        .fill(band.color(theme))
                        .frame(width: geo.size.width * min(1, max(0, window.percent / 100)))
                }
            }
            .frame(height: 6)
            Text(valueText)
                .hudMono(size: 9)
                .foregroundStyle(band.color(theme))
                .frame(width: 96, alignment: .trailing)
        }
    }

    private var valueText: String {
        let pct = "\(Int(window.percent.rounded()))%"
        guard let resets = window.resetsAt, resets != 0 else { return pct }
        return pct + " · " + usageCountdown(resets)
    }
}
