import SwiftUI
import UIKit

// KTheme ports kraken web's "socrat cyber-dark" design system (web/style.css)
// to SwiftUI: one calm dark surface, mono for data, sans for prose, sharp
// corners everywhere, neon only where something is hot or interactive.
// Every screen should build on the pieces here rather than reach for
// .primary/.secondary/systemBackground/RoundedRectangle.

extension Color {
    init(hex: UInt32) {
        self.init(
            red: Double((hex >> 16) & 0xFF) / 255,
            green: Double((hex >> 8) & 0xFF) / 255,
            blue: Double(hex & 0xFF) / 255
        )
    }
}

enum KTheme {
    // --- palette (exact hex from style.css's :root custom properties) ---
    static let ground = Color(hex: 0x0A0E14)
    static let surface = Color(hex: 0x10151E)
    static let raised = Color(hex: 0x151C28)
    static let hairline = Color(hex: 0x1E2836)
    static let hairline2 = Color(hex: 0x2A3A50)
    static let text = Color(hex: 0xD7E2EC)
    static let text2 = Color(hex: 0x6B7A8C)
    static let accent = Color(hex: 0x9E7BFF)
    static let accentInk = Color(hex: 0x0A0E14) // == ground; on-accent text
    static let accentSoft = Color(hex: 0x221844)
    static let counter = Color(hex: 0xFF4D9D)
    static let counterSoft = Color(hex: 0x3D1428)
    static let solved = Color(hex: 0x4ADE80)
    static let solvedSoft = Color(hex: 0x0F2E1D)
    static let revealed = Color(hex: 0xF5B84D)
    static let revealedSoft = Color(hex: 0x33260E)

    // --- global chrome (nav bar / tab bar / page dots) ---
    // Configured once via UIKit appearance proxies rather than threading
    // .toolbarBackground through every NavigationStack (there are many —
    // one per sheet) and there's no SwiftUI API for UIPageControl at all.
    static func applyGlobalAppearance() {
        let nav = UINavigationBarAppearance()
        nav.configureWithOpaqueBackground()
        nav.backgroundColor = UIColor(surface)
        nav.shadowColor = UIColor(hairline)
        nav.titleTextAttributes = [.foregroundColor: UIColor(text)]
        nav.largeTitleTextAttributes = [.foregroundColor: UIColor(text)]
        UINavigationBar.appearance().standardAppearance = nav
        UINavigationBar.appearance().scrollEdgeAppearance = nav
        UINavigationBar.appearance().compactAppearance = nav
        UINavigationBar.appearance().tintColor = UIColor(accent)

        let tab = UITabBarAppearance()
        tab.configureWithOpaqueBackground()
        tab.backgroundColor = UIColor(surface)
        tab.shadowColor = UIColor(hairline)
        UITabBar.appearance().standardAppearance = tab
        UITabBar.appearance().scrollEdgeAppearance = tab
        UITabBar.appearance().tintColor = UIColor(accent)
        UITabBar.appearance().unselectedItemTintColor = UIColor(text2)

        UIPageControl.appearance().currentPageIndicatorTintColor = UIColor(accent)
        UIPageControl.appearance().pageIndicatorTintColor = UIColor(hairline2)

        UISwitch.appearance().onTintColor = UIColor(accent)

        let segmented = UISegmentedControl.appearance()
        segmented.backgroundColor = UIColor(raised)
        segmented.selectedSegmentTintColor = UIColor(accent)
        segmented.setTitleTextAttributes([.foregroundColor: UIColor(text2)], for: .normal)
        segmented.setTitleTextAttributes([.foregroundColor: UIColor(accentInk)], for: .selected)

        UITextField.appearance().tintColor = UIColor(accent)
    }
}

// MARK: - typography

extension Font {
    /// The data accent: SF Mono / Menlo equivalent (system monospaced design).
    /// Slugs, states, meta lines, column titles, section labels, buttons.
    static func data(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight, design: .monospaced)
    }

    /// The voice: system sans. Prose — spec/success text, escalation bodies,
    /// transcript text blocks.
    static func voice(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: size, weight: weight, design: .default)
    }
}

// MARK: - state → color

/// The six kanban columns, mirroring web/app.js's COLUMNS table.
enum BoardColumn: String, CaseIterable, Identifiable, Hashable {
    case draft, queued, working, blocked, review, done

    var id: String { rawValue }

    var title: String {
        switch self {
        case .draft: return "DRAFT"
        case .queued: return "QUEUED"
        case .working: return "WORKING"
        case .blocked: return "BLOCKED"
        case .review: return "REVIEW"
        case .done: return "DONE"
        }
    }
}

enum BoardTheme {
    private static let columnStates: [BoardColumn: [String]] = [
        .draft: ["draft", "error"],
        .queued: ["ready", "queued", "gate-review"],
        .working: ["planning", "executing"],
        .blocked: ["blocked", "plan-review"],
        .review: ["testing", "pr-open"],
        .done: ["done", "failed", "abandoned"],
    ]

    /// Which column a job belongs on. Mirrors app.js's render(): a job with
    /// a non-empty `health` always renders under BLOCKED regardless of its
    /// file state — the daemon isn't going to fix it, only a retry will.
    static func column(for job: WebJob) -> BoardColumn {
        if let health = job.health, !health.isEmpty {
            return .blocked
        }
        for (column, states) in columnStates where states.contains(job.state) {
            return column
        }
        if job.state.hasPrefix("waits:") || job.state.hasPrefix("gated:") {
            return .queued
        }
        return .draft
    }

    /// Exact port of app.js's STATE_COLOR / stateColor().
    static func color(forState state: String) -> Color {
        if state.hasPrefix("waits:") || state.hasPrefix("gated:") {
            return KTheme.revealed
        }
        switch state {
        case "executing", "planning":
            return KTheme.solved
        case "plan-review", "blocked", "gate-review", "error", "failed":
            return KTheme.counter
        case "ready", "queued":
            return KTheme.accent
        case "testing", "pr-open":
            return KTheme.revealed
        case "done", "draft", "abandoned":
            return KTheme.text2
        default:
            return KTheme.text2
        }
    }
}

enum SessionTheme {
    /// running=solved, waiting=text2, question=revealed, blocked/error=counter,
    /// shell=text2 — the same semantic mapping ls.go uses, expressed in the
    /// cyber-dark palette.
    static func color(forStatus status: String, role: String) -> Color {
        if role == "shell" {
            return KTheme.text2
        }
        switch status {
        case "running":
            return KTheme.solved
        case "waiting":
            return KTheme.text2
        case "question":
            return KTheme.revealed
        case "blocked", "error":
            return KTheme.counter
        default:
            return KTheme.text2
        }
    }
}

// MARK: - reusable view pieces

/// One kanban card's chrome: surface bg, 1px hairline border, 2px leading
/// bar in the state color. Flagged (open escalations) swaps the leading bar
/// to counter and adds a soft pink glow — mirrors .card / .card.flagged.
struct CardBackground: ViewModifier {
    var stateColor: Color
    var flagged: Bool = false

    func body(content: Content) -> some View {
        content
            .background(KTheme.surface)
            .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 1))
            .overlay(alignment: .leading) {
                Rectangle()
                    .fill(flagged ? KTheme.counter : stateColor)
                    .frame(width: 2)
            }
            .shadow(color: flagged ? KTheme.counter.opacity(0.28) : .clear, radius: flagged ? 5 : 0)
    }
}

extension View {
    func cardBackground(stateColor: Color, flagged: Bool = false) -> some View {
        modifier(CardBackground(stateColor: stateColor, flagged: flagged))
    }
}

/// Uppercase, letterspaced, dim mono — column headers, section labels.
struct KLabelStyle: ViewModifier {
    var size: CGFloat = 10

    func body(content: Content) -> some View {
        content
            .font(.data(size, weight: .semibold))
            .kerning(2)
            .foregroundStyle(KTheme.text2)
            .textCase(.uppercase)
    }
}

extension View {
    func kLabelStyle(size: CGFloat = 10) -> some View {
        modifier(KLabelStyle(size: size))
    }
}

/// A Form/List section with a kraken-styled mono header and surface-colored
/// rows, instead of the system's grouped-gray uppercase header. Use in place
/// of `Section("Title") { ... }` throughout.
@ViewBuilder
func kSection<Content: View>(_ title: String? = nil, @ViewBuilder content: () -> Content) -> some View {
    if let title {
        Section {
            content()
        } header: {
            Text(title).kLabelStyle()
        }
        .listRowBackground(KTheme.surface)
        .listRowSeparatorTint(KTheme.hairline)
    } else {
        Section {
            content()
        }
        .listRowBackground(KTheme.surface)
        .listRowSeparatorTint(KTheme.hairline)
    }
}

/// Strips the system grouped-list chrome (rounded cards, gray background)
/// down to the flat ground/surface look. Apply to every Form/List root.
struct KListStyle: ViewModifier {
    func body(content: Content) -> some View {
        content
            .scrollContentBackground(.hidden)
            .background(KTheme.ground)
    }
}

extension View {
    func kListStyle() -> some View {
        modifier(KListStyle())
    }
}

/// raised bg, 1px hairline2 border (accent when focused), no corner radius —
/// TextField/TextEditor chrome.
struct KFieldStyle: ViewModifier {
    var focused: Bool = false
    var minHeight: CGFloat?

    func body(content: Content) -> some View {
        content
            .font(.voice(13))
            .foregroundStyle(KTheme.text)
            .padding(8)
            .frame(minHeight: minHeight, alignment: .topLeading)
            .background(KTheme.raised)
            .overlay(Rectangle().stroke(focused ? KTheme.accent : KTheme.hairline2, lineWidth: 1))
    }
}

extension View {
    func kField(focused: Bool = false, minHeight: CGFloat? = nil) -> some View {
        modifier(KFieldStyle(focused: focused, minHeight: minHeight))
    }
}

// MARK: - button styles

/// Save / Answer-submit / Create / Test Connection — the affirmative CTA.
/// accent bg, ground-colored (accent-ink) text, mono, sharp corners.
struct KPrimaryButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.data(13, weight: .semibold))
            .foregroundStyle(KTheme.accentInk)
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
            .background(KTheme.accent.opacity(configuration.isPressed ? 0.75 : 1))
    }
}

extension ButtonStyle where Self == KPrimaryButtonStyle {
    static var kPrimary: KPrimaryButtonStyle { KPrimaryButtonStyle() }
}

/// Cancel / secondary — transparent, 1px hairline2 border, text2.
struct KGhostButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.data(13))
            .foregroundStyle(KTheme.text2)
            .padding(.horizontal, 16)
            .padding(.vertical, 8)
            .overlay(Rectangle().stroke(KTheme.hairline2, lineWidth: 1))
            .opacity(configuration.isPressed ? 0.6 : 1)
    }
}

extension ButtonStyle where Self == KGhostButtonStyle {
    static var kGhost: KGhostButtonStyle { KGhostButtonStyle() }
}

/// Full-width "hot" action blocks — mirrors .btn.unblock (counter) and
/// .btn.retry (revealed): soft tint bg, 1px solid tint border, tint text.
struct KBlockButtonStyle: ButtonStyle {
    var tint: Color
    var tintSoft: Color

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.data(11, weight: .semibold))
            .kerning(1)
            .foregroundStyle(tint)
            .frame(maxWidth: .infinity)
            .padding(.vertical, 9)
            .background(tintSoft)
            .overlay(Rectangle().stroke(tint, lineWidth: 1))
            .opacity(configuration.isPressed ? 0.75 : 1)
    }
}

extension ButtonStyle where Self == KBlockButtonStyle {
    static var kUnblock: KBlockButtonStyle { KBlockButtonStyle(tint: KTheme.counter, tintSoft: KTheme.counterSoft) }
    static var kRetry: KBlockButtonStyle { KBlockButtonStyle(tint: KTheme.revealed, tintSoft: KTheme.revealedSoft) }
}

/// A quiet mono text button that reads as destructive (Kill) — no block,
/// no border, just counter-tinted text, like .kill in style.css.
struct KDestructiveTextButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.data(13, weight: .semibold))
            .foregroundStyle(KTheme.counter)
            .opacity(configuration.isPressed ? 0.65 : 1)
    }
}

extension ButtonStyle where Self == KDestructiveTextButtonStyle {
    static var kDestructiveText: KDestructiveTextButtonStyle { KDestructiveTextButtonStyle() }
}

/// A quiet mono text button in text2 — mirrors .mini-add / .rm / .edit's
/// idle state (their hover states don't translate to touch, so this is
/// just the resting look).
struct KMiniButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.data(12))
            .foregroundStyle(KTheme.text2)
            .opacity(configuration.isPressed ? 0.6 : 1)
    }
}

extension ButtonStyle where Self == KMiniButtonStyle {
    static var kMini: KMiniButtonStyle { KMiniButtonStyle() }
}

/// Repo/slug suggestion chips — mono, transparent, 1px accentSoft border,
/// accent text. Mirrors .chip.
struct KChipButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.data(11))
            .foregroundStyle(KTheme.accent)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .overlay(Rectangle().stroke(KTheme.accentSoft, lineWidth: 1))
            .opacity(configuration.isPressed ? 0.7 : 1)
    }
}

extension ButtonStyle where Self == KChipButtonStyle {
    static var kChip: KChipButtonStyle { KChipButtonStyle() }
}
