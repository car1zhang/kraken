import SwiftUI
import UIKit

// KTheme ports kraken web's "socrat cyber-dark" design system (web/style.css)
// to SwiftUI: one calm surface, mono for data, sans for prose, sharp corners
// on cards/fields/sections, neon only where something is hot or interactive.
// Buttons are the one deliberate exception — native .bordered/.borderedProminent
// with SF Symbol icons and their normal rounded geometry, per carl's
// device-test feedback; everything else should still build on the pieces
// here rather than reach for .primary/.secondary/systemBackground/RoundedRectangle.
//
// The palette is adaptive: every entry below is a dynamic light/dark UIColor
// (built via dyn()) plus the SwiftUI Color derived from it, so the whole app
// re-resolves on a light/dark scheme flip with no call-site changes. Dark
// stays exactly the original cyber-dark hex values; light is its sibling
// palette, chosen to match in feel rather than reused from the web UI (which
// has no light mode).

extension Color {
    init(hex: UInt32) {
        self.init(
            red: Double((hex >> 16) & 0xFF) / 255,
            green: Double((hex >> 8) & 0xFF) / 255,
            blue: Double(hex & 0xFF) / 255
        )
    }
}

extension UIColor {
    convenience init(hex: UInt32) {
        self.init(
            red: CGFloat((hex >> 16) & 0xFF) / 255,
            green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255,
            alpha: 1
        )
    }
}

enum KTheme {
    /// Builds a dynamic UIColor that resolves to `light` or `dark` hex per
    /// the current trait collection's userInterfaceStyle.
    private static func dyn(light: UInt32, dark: UInt32) -> UIColor {
        UIColor { $0.userInterfaceStyle == .light ? UIColor(hex: light) : UIColor(hex: dark) }
    }

    // --- palette (dark hex matches style.css's :root custom properties
    // exactly; light is the adaptive sibling — see file header) ---
    static let groundUI = dyn(light: 0xEEF2F7, dark: 0x0A0E14)
    static let ground = Color(uiColor: groundUI)
    static let surfaceUI = dyn(light: 0xFFFFFF, dark: 0x10151E)
    static let surface = Color(uiColor: surfaceUI)
    static let raisedUI = dyn(light: 0xE3E9F1, dark: 0x151C28)
    static let raised = Color(uiColor: raisedUI)
    static let hairlineUI = dyn(light: 0xD8E0EA, dark: 0x1E2836)
    static let hairline = Color(uiColor: hairlineUI)
    static let hairline2UI = dyn(light: 0xB9C6D6, dark: 0x2A3A50)
    static let hairline2 = Color(uiColor: hairline2UI)
    static let textUI = dyn(light: 0x1C2733, dark: 0xD7E2EC)
    static let text = Color(uiColor: textUI)
    static let text2UI = dyn(light: 0x5D6C7E, dark: 0x6B7A8C)
    static let text2 = Color(uiColor: text2UI)
    static let accentUI = dyn(light: 0x6B3FE0, dark: 0x9E7BFF)
    static let accent = Color(uiColor: accentUI)
    static let accentInkUI = dyn(light: 0xFFFFFF, dark: 0x0A0E14) // on-accent text
    static let accentInk = Color(uiColor: accentInkUI)
    static let accentSoftUI = dyn(light: 0xE7DFFB, dark: 0x221844)
    static let accentSoft = Color(uiColor: accentSoftUI)
    static let counterUI = dyn(light: 0xC0105F, dark: 0xFF4D9D)
    static let counter = Color(uiColor: counterUI)
    static let counterSoftUI = dyn(light: 0xFBDFEC, dark: 0x3D1428)
    static let counterSoft = Color(uiColor: counterSoftUI)
    static let solvedUI = dyn(light: 0x178A44, dark: 0x4ADE80)
    static let solved = Color(uiColor: solvedUI)
    static let solvedSoftUI = dyn(light: 0xDCF3E4, dark: 0x0F2E1D)
    static let solvedSoft = Color(uiColor: solvedSoftUI)
    static let revealedUI = dyn(light: 0x8A5106, dark: 0xF5B84D)
    static let revealed = Color(uiColor: revealedUI)
    static let revealedSoftUI = dyn(light: 0xF7ECD4, dark: 0x33260E)
    static let revealedSoft = Color(uiColor: revealedSoftUI)

    // --- global chrome (nav bar / tab bar / page dots) ---
    // Configured once via UIKit appearance proxies rather than threading
    // .toolbarBackground through every NavigationStack (there are many —
    // one per sheet) and there's no SwiftUI API for UIPageControl at all.
    static func applyGlobalAppearance() {
        let nav = UINavigationBarAppearance()
        nav.configureWithOpaqueBackground()
        nav.backgroundColor = surfaceUI
        nav.shadowColor = hairlineUI
        nav.titleTextAttributes = [.foregroundColor: textUI]
        nav.largeTitleTextAttributes = [.foregroundColor: textUI]
        // The at-rest (scroll-edge) bar must be TRANSPARENT: with an opaque
        // scroll-edge appearance, iOS 26 paints the bar background over the
        // large-title text whenever the stack root is scrollable (List /
        // ScrollView) — the title exists in the a11y tree but is invisible.
        // That's exactly the Sessions/Settings "no title" bug carl reported;
        // Board only escaped because its root is a plain VStack. The opaque
        // variant still applies once content scrolls under the bar
        // (standardAppearance), which is the only time it's visible anyway.
        let navEdge = UINavigationBarAppearance()
        navEdge.configureWithTransparentBackground()
        navEdge.titleTextAttributes = [.foregroundColor: textUI]
        navEdge.largeTitleTextAttributes = [.foregroundColor: textUI]
        UINavigationBar.appearance().standardAppearance = nav
        UINavigationBar.appearance().scrollEdgeAppearance = navEdge
        UINavigationBar.appearance().compactAppearance = nav
        UINavigationBar.appearance().tintColor = accentUI

        let tab = UITabBarAppearance()
        tab.configureWithOpaqueBackground()
        tab.backgroundColor = surfaceUI
        tab.shadowColor = hairlineUI
        UITabBar.appearance().standardAppearance = tab
        UITabBar.appearance().scrollEdgeAppearance = tab
        UITabBar.appearance().tintColor = accentUI
        UITabBar.appearance().unselectedItemTintColor = text2UI

        UIPageControl.appearance().currentPageIndicatorTintColor = accentUI
        UIPageControl.appearance().pageIndicatorTintColor = hairline2UI

        UISwitch.appearance().onTintColor = accentUI

        let segmented = UISegmentedControl.appearance()
        segmented.backgroundColor = raisedUI
        segmented.selectedSegmentTintColor = accentUI
        segmented.setTitleTextAttributes([.foregroundColor: text2UI], for: .normal)
        segmented.setTitleTextAttributes([.foregroundColor: accentInkUI], for: .selected)

        UITextField.appearance().tintColor = accentUI
    }
}

// MARK: - typography

extension Font {
    // Carl: "make font size slightly larger." Bumped once, centrally, so
    // every call site (already routed through .data/.voice everywhere) gets
    // it for free instead of hand-editing dozens of literals. +1.5pt below
    // 13pt, +2pt at/above — chosen so it reproduces the sizes called out
    // during review exactly (10→11.5, 11→12.5, 13→15) while still bumping
    // everything else in between consistently.
    fileprivate static func bumped(_ size: CGFloat) -> CGFloat {
        size < 13 ? size + 1.5 : size + 2
    }

    /// The data accent: SF Mono / Menlo equivalent (system monospaced design).
    /// Slugs, states, meta lines, column titles, section labels.
    static func data(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: bumped(size), weight: weight, design: .monospaced)
    }

    /// The voice: system sans. Prose — spec text, job detail bodies,
    /// transcript text blocks.
    static func voice(_ size: CGFloat, weight: Font.Weight = .regular) -> Font {
        .system(size: bumped(size), weight: weight, design: .default)
    }
}

// MARK: - state → color

/// The five kanban columns — draft/queued/working/blocked/done — mirroring
/// the server's five job states 1:1 (review was folded into blocked), plus
/// ARCHIVED as a sixth pager page (the web board shows archived jobs in a
/// collapsible drawer instead; on the phone an extra page is cheaper).
enum BoardColumn: String, CaseIterable, Identifiable, Hashable {
    case draft, queued, working, blocked, done, archived

    var id: String { rawValue }

    var title: String {
        switch self {
        case .draft: return "DRAFT"
        case .queued: return "QUEUED"
        case .working: return "WORKING"
        case .blocked: return "BLOCKED"
        case .done: return "DONE"
        case .archived: return "ARCHIVED"
        }
    }

    /// Truncated for the always-visible overview strip, where six columns
    /// have to fit in one row.
    var shortTitle: String {
        switch self {
        case .draft: return "DRAFT"
        case .queued: return "QUEUE"
        case .working: return "WORK"
        case .blocked: return "BLOCK"
        case .done: return "DONE"
        case .archived: return "ARCH"
        }
    }
}

enum BoardTheme {
    // Exact 1:1 mapping to the server's job states — no aliases, no
    // prefix matching. See BoardColumn's doc comment.
    private static let columnStates: [BoardColumn: [String]] = [
        .draft: ["draft"],
        .queued: ["queued"],
        .working: ["working"],
        .blocked: ["blocked"],
        .done: ["done"],
        .archived: ["archived"],
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
        return .draft
    }

    /// State → color. working=active (green), blocked=attention (red),
    /// queued=neutral (accent), draft/done/archived=muted.
    static func color(forState state: String) -> Color {
        switch state {
        case "working":
            return KTheme.solved
        case "blocked":
            return KTheme.counter
        case "queued":
            return KTheme.accent
        case "draft", "done", "archived":
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
/// bar in the state color. Flagged (a pending question, or unhealthy) swaps
/// the leading bar to counter and adds a soft pink glow — mirrors
/// .card / .card.flagged.
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

// Buttons deliberately stay native (.bordered / .borderedProminent with SF
// Symbol icons) rather than a custom "blocky" ButtonStyle — carl's call
// after device-testing the previous pass. The dark palette above still
// supplies every button's tint via .tint(KTheme.___); see each view for
// which tint/icon/role a given action uses. One carve-out: SheetCloseButton
// below drops .bordered entirely for the default toolbar treatment.

// Icon-only sheet/drawer dismiss control, used in every sheet's cancellation
// toolbar slot: a plain accent xmark with the default toolbar button styling —
// carl's call, replacing the earlier xmark.circle.fill in a bordered pill
// (read as a circle-in-a-circle). Toolbar buttons already carry a >=44pt tap
// area, and the explicit tint keeps it purple on every OS version rather than
// leaning on the nav-bar appearance tint.
struct SheetCloseButton: View {
    var label: String = "Close"
    let action: () -> Void
    var body: some View {
        Button(action: action) {
            Label(label, systemImage: "xmark")
        }
        .labelStyle(.iconOnly)
        .tint(KTheme.accent)
        .accessibilityLabel(label)
    }
}
