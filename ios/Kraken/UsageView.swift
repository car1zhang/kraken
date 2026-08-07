import SwiftUI
import Observation

@Observable
final class UsageViewModel {
    var usage: UsageResponse?
    var errorMessage: String?

    func refresh(baseURL: String) async {
        do {
            usage = try await APIClient.shared.fetchUsage(baseURL: baseURL)
            errorMessage = nil
        } catch {
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    var current: UsageAccount? {
        usage?.accounts.first { $0.current == true || $0.slug == "main" }
    }

    var pool: [UsageAccount] {
        guard let usage else { return [] }
        let currentSlug = current?.slug
        return usage.accounts.filter { $0.slug != currentSlug }
    }
}

/// Formats a unix-millis deadline against wall-clock `now` into a compact
/// countdown: "3h12m" / "4d" / "8m" / "now" (past or already-elapsed).
func formatCountdown(millis: Int64, now: Date = Date()) -> String {
    let delta = Double(millis) / 1000 - now.timeIntervalSince1970
    if delta <= 0 { return "now" }
    let totalMinutes = Int(delta / 60)
    if totalMinutes < 1 { return "now" }
    let days = totalMinutes / (24 * 60)
    if days >= 1 { return "\(days)d" }
    let hours = totalMinutes / 60
    let minutes = totalMinutes % 60
    if hours >= 1 { return "\(hours)h\(minutes)m" }
    return "\(totalMinutes)m"
}

/// Threshold-tinted gauge color: ≥80% red, ≥50% yellow/orange, else green —
/// mirrors BoardTheme's working/blocked semantic mapping.
func usageGaugeColor(percent: Double) -> Color {
    if percent >= 80 { return KTheme.counter }
    if percent >= 50 { return KTheme.revealed }
    return KTheme.solved
}

/// One labeled window row: name on the left, "NN% · countdown" right-aligned,
/// and a thin capsule meter underneath — the same bar treatment as the
/// job-detail tokens card, so the two screens' meters read as one family.
///
/// The meter is hand-drawn rather than a native `Gauge`, for two reasons
/// checked on the iOS 26.3 simulator: `.labelsHidden()` does not suppress a
/// `Gauge`'s label on iOS, so the label renders twice (small in the header
/// row, body-sized inside the Gauge), and a `Gauge` cannot be drawn this
/// thin. The whole row collapses to one spoken sentence for VoiceOver.
private struct UsageGaugeRow: View {
    let label: String
    let percent: Double
    let resetsAt: Int64?

    private var clamped: Double { min(max(percent, 0), 100) }

    private var readout: String {
        resetsAt.map { "\(Int(percent))% · \(formatCountdown(millis: $0))" } ?? "\(Int(percent))%"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack {
                Text(label)
                    .font(.data(11, weight: .semibold))
                    .foregroundStyle(KTheme.text2)
                Spacer()
                Text(readout)
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Capsule().fill(KTheme.raised)
                    Capsule()
                        .fill(usageGaugeColor(percent: percent))
                        .frame(width: max(geo.size.width * clamped / 100, clamped > 0 ? 5 : 0))
                }
            }
            .frame(height: 5)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
        .accessibilityValue(resetsAt.map { "\(Int(percent)) percent, resets in \(formatCountdown(millis: $0))" } ?? "\(Int(percent)) percent")
    }
}

/// Small colored capsule for account status — cooling/reauth/no-creds/stale.
/// Rendered through the shared `KChip` so it matches every other chip in the
/// app; each status gets an SF Symbol, and the TEXT of each chip is verbatim
/// what it always was.
private struct UsageChip: View {
    let icon: String
    let text: String
    let color: Color

    var body: some View {
        KChip(icon: icon, text: text, tint: color)
    }
}

private struct UsageAccountCard: View {
    let account: UsageAccount

    private var displayTier: String? {
        guard let tier = account.tier, !tier.isEmpty else { return nil }
        return tier.hasPrefix("default_claude_") ? String(tier.dropFirst("default_claude_".count)) : tier
    }

    /// (icon, text, color). Texts are unchanged from the pre-glass build; the
    /// icons are new, one per status, so each chip reads at a glance.
    private struct StatusChip: Identifiable {
        var id: String { text }
        let icon: String
        let text: String
        let color: Color
    }

    private var chips: [StatusChip] {
        var chips: [StatusChip] = []
        if let cooldownUntil = account.cooldownUntil, cooldownUntil > Int64(Date().timeIntervalSince1970 * 1000) {
            chips.append(StatusChip(
                icon: "snowflake",
                text: "cooling \(formatCountdown(millis: cooldownUntil))",
                color: KTheme.revealed
            ))
        }
        if account.reauth == true {
            chips.append(StatusChip(
                icon: "key.horizontal.fill", text: "reauth needed — ccp login", color: KTheme.counter
            ))
        }
        if account.noCreds == true {
            chips.append(StatusChip(
                icon: "exclamationmark.triangle.fill", text: "no creds", color: KTheme.counter
            ))
        }
        if account.stale == true {
            chips.append(StatusChip(
                icon: "clock.arrow.circlepath", text: "stale", color: KTheme.text2
            ))
        }
        return chips
    }

    var body: some View {
        ThemedCard {
            cardContent.padding(14)
        }
    }

    private var cardContent: some View {
        VStack(alignment: .leading, spacing: 10) {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 6) {
                    Text(account.slug)
                        .font(.data(13, weight: .bold))
                        .foregroundStyle(KTheme.text)
                    if let displayTier {
                        Text(displayTier)
                            .font(.data(11))
                            .foregroundStyle(KTheme.accent)
                    }
                }
                if let email = account.email, !email.isEmpty {
                    Text(email)
                        .font(.voice(11))
                        .foregroundStyle(KTheme.text2)
                }
            }

            if !chips.isEmpty {
                // Wrapping-free horizontal scroll: "reauth needed — ccp login"
                // is long enough to overflow a 390pt row on its own.
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 6) {
                        ForEach(chips) { chip in
                            UsageChip(icon: chip.icon, text: chip.text, color: chip.color)
                        }
                    }
                }
            }

            if let error = account.error, !error.isEmpty {
                Text(error)
                    .font(.voice(11))
                    .foregroundStyle(KTheme.counter)
            }

            if let session = account.session {
                UsageGaugeRow(label: "session", percent: session.percent, resetsAt: session.resetsAt)
            }
            if let weekly = account.weekly {
                UsageGaugeRow(label: "weekly", percent: weekly.percent, resetsAt: weekly.resetsAt)
            }
            ForEach(account.models ?? [], id: \.name) { model in
                UsageGaugeRow(label: model.name.lowercased(), percent: model.percent, resetsAt: model.resetsAt)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// The two full-width state banners at the top of the screen. Rounded glass
/// with a tinted hairline, replacing the old sharp `Rectangle` stroke; the
/// sentences are unchanged.
private struct UsageBanner: View {
    let text: String
    let tint: Color

    private var shape: RoundedRectangle {
        RoundedRectangle(cornerRadius: 18, style: .continuous)
    }

    var body: some View {
        Text(text)
            .font(.voice(12))
            .foregroundStyle(tint)
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(.ultraThinMaterial, in: shape)
            .overlay(shape.stroke(tint.opacity(0.55), lineWidth: 1))
    }
}

struct UsageView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @Environment(\.scenePhase) private var scenePhase
    @State private var vm = UsageViewModel()

    private var footerText: String? {
        guard let usage = vm.usage, usage.available else { return nil }
        let minutes = usage.ageSeconds / 60
        let ago = minutes < 1 ? "just now" : "\(minutes)m ago"
        return "fetched \(ago) by daemon"
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    if let usage = vm.usage {
                        if usage.daemonOK == false {
                            UsageBanner(text: "daemon down — data frozen", tint: KTheme.counter)
                        } else if !usage.available {
                            UsageBanner(text: "no usage snapshot yet", tint: KTheme.text2)
                        }

                        if let current = vm.current {
                            VStack(alignment: .leading, spacing: 8) {
                                Text("Current account").kLabelStyle()
                                UsageAccountCard(account: current)
                            }
                        }

                        if !vm.pool.isEmpty {
                            VStack(alignment: .leading, spacing: 8) {
                                Text("Pool — \(vm.pool.count) accounts").kLabelStyle()
                                ForEach(vm.pool) { account in
                                    UsageAccountCard(account: account)
                                }
                            }
                        }

                        if let footerText {
                            Text(footerText)
                                .font(.voice(11))
                                .foregroundStyle(KTheme.text2)
                        }
                    }

                    if let err = vm.errorMessage {
                        Text(err)
                            .font(.voice(12))
                            .foregroundStyle(KTheme.counter)
                    }
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .scrollContentBackground(.hidden)
            .background { ThemeBackgroundView() }
            .navigationTitle("Usage")
            // Large title, and no per-view opaque scroll-edge appearance —
            // this screen is one of the three the deleted proxy used to hide.
            .navigationBarTitleDisplayMode(.large)
            .refreshable { await vm.refresh(baseURL: serverURL) }
            .task(id: scenePhase) {
                guard scenePhase == .active else { return }
                await vm.refresh(baseURL: serverURL)
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(30))
                    if Task.isCancelled { break }
                    await vm.refresh(baseURL: serverURL)
                }
            }
        }
    }
}
