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

/// One labeled window row: name on the left, a horizontal fill gauge in the
/// middle, "NN% · countdown" right-aligned.
private struct UsageGaugeRow: View {
    let label: String
    let percent: Double
    let resetsAt: Int64?

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(label)
                    .font(.data(11, weight: .semibold))
                    .foregroundStyle(KTheme.text2)
                Spacer()
                Text(resetsAt.map { "\(Int(percent))% · \(formatCountdown(millis: $0))" } ?? "\(Int(percent))%")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
            }
            GeometryReader { geo in
                ZStack(alignment: .leading) {
                    Rectangle().fill(KTheme.raised)
                    Rectangle()
                        .fill(usageGaugeColor(percent: percent))
                        .frame(width: geo.size.width * min(max(percent, 0), 100) / 100)
                }
            }
            .frame(height: 6)
            .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 1))
        }
    }
}

/// Small colored pill for account status — cooling/reauth/no-creds/stale.
private struct UsageChip: View {
    let text: String
    let color: Color

    var body: some View {
        Text(text)
            .font(.data(10, weight: .semibold))
            .foregroundStyle(color)
            .padding(.horizontal, 6)
            .padding(.vertical, 3)
            .overlay(Rectangle().stroke(color, lineWidth: 1))
    }
}

private struct UsageAccountCard: View {
    let account: UsageAccount

    private var displayTier: String? {
        guard let tier = account.tier, !tier.isEmpty else { return nil }
        return tier.hasPrefix("default_claude_") ? String(tier.dropFirst("default_claude_".count)) : tier
    }

    private var chips: [(String, Color)] {
        var chips: [(String, Color)] = []
        if let cooldownUntil = account.cooldownUntil, cooldownUntil > Int64(Date().timeIntervalSince1970 * 1000) {
            chips.append(("cooling \(formatCountdown(millis: cooldownUntil))", KTheme.revealed))
        }
        if account.reauth == true {
            chips.append(("reauth needed — ccp login", KTheme.counter))
        }
        if account.noCreds == true {
            chips.append(("no creds", KTheme.counter))
        }
        if account.stale == true {
            chips.append(("stale", KTheme.text2))
        }
        return chips
    }

    var body: some View {
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
                HStack(spacing: 6) {
                    ForEach(chips, id: \.0) { chip in
                        UsageChip(text: chip.0, color: chip.1)
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
        .padding(12)
        .background(KTheme.surface)
        .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 1))
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
                            Text("daemon down — data frozen")
                                .font(.voice(12))
                                .foregroundStyle(KTheme.counter)
                                .padding(10)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(KTheme.surface)
                                .overlay(Rectangle().stroke(KTheme.counter, lineWidth: 1))
                        } else if !usage.available {
                            Text("no usage snapshot yet")
                                .font(.voice(12))
                                .foregroundStyle(KTheme.text2)
                                .padding(10)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(KTheme.surface)
                                .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 1))
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
            .background(KTheme.ground)
            .navigationTitle("Usage")
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
