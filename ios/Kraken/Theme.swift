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
    // "polling" status (a claude mid poll-loop on a pending ScheduleWakeup)
    // — cyan, mirroring util/ansi.go's StatusColor("polling") = Cyan on the CLI side.
    static let pollingUI = dyn(light: 0x0E7490, dark: 0x22D3EE)
    static let polling = Color(uiColor: pollingUI)
    static let pollingSoftUI = dyn(light: 0xD9F2F7, dark: 0x0E2E33)
    static let pollingSoft = Color(uiColor: pollingSoftUI)

    // Per-skill colors: /info /aim /do /pr each get their own hue so a spec
    // full of pipeline commands reads as a colored pipeline, both in the
    // iOS composer inline highlight (HighlightingTextEditor) and in
    // CmdChipsRow. aim-red is a true red, kept distinct from the pink
    // `counter` above; do-yellow is yellower than the amber `revealed` above
    // so "do" doesn't read as "question pending"; pr-green mirrors `solved`
    // via its own token so the two can diverge later; info-blue is a new
    // hue (the palette otherwise has no true blue — `polling` is cyan).
    static let skillInfoUI = dyn(light: 0x1A6FD4, dark: 0x5EA8FF)
    static let skillInfo = Color(uiColor: skillInfoUI)
    static let skillInfoSoftUI = dyn(light: 0xDCEAFB, dark: 0x102A45)
    static let skillInfoSoft = Color(uiColor: skillInfoSoftUI)
    static let skillAimUI = dyn(light: 0xC42B2B, dark: 0xFF6B6B)
    static let skillAim = Color(uiColor: skillAimUI)
    static let skillAimSoftUI = dyn(light: 0xFBE0E0, dark: 0x3A1414)
    static let skillAimSoft = Color(uiColor: skillAimSoftUI)
    static let skillDoUI = dyn(light: 0x8A7206, dark: 0xF5D34D)
    static let skillDo = Color(uiColor: skillDoUI)
    static let skillDoSoftUI = dyn(light: 0xF7F0D4, dark: 0x332B0E)
    static let skillDoSoft = Color(uiColor: skillDoSoftUI)
    static let skillPrUI = dyn(light: 0x178A44, dark: 0x4ADE80)
    static let skillPr = Color(uiColor: skillPrUI)
    static let skillPrSoftUI = dyn(light: 0xDCF3E4, dark: 0x0F2E1D)
    static let skillPrSoft = Color(uiColor: skillPrSoftUI)
    // /dream is the fifth skill hue, and violet is the pick because the four
    // above are blue/red/yellow/green — the five chips sit side by side in
    // CmdChipsRow, so separation from all four at once is what matters. Its
    // nearest neighbour is the generic `accent` purple, which never appears
    // IN the chip row, so that collision is cosmetic rather than confusing.
    // Doubles as the DREAM surface's own accent (DreamView, the feedback
    // section header, the artifact-viewer feedback chip).
    static let skillDreamUI = dyn(light: 0x7E22CE, dark: 0xC084FC)
    static let skillDream = Color(uiColor: skillDreamUI)
    static let skillDreamSoftUI = dyn(light: 0xF3E8FF, dark: 0x2A1440)
    static let skillDreamSoft = Color(uiColor: skillDreamSoftUI)
    // /graph is the sixth skill hue, and it doubles as the GRAPH screen's own
    // accent — the selection ring on the canvas. Both hexes are `--skill-graph`
    // in web/_ui/src/styles/style.css (dark from `:root`, light from the
    // `[data-theme="light"]` block), and web/graphpalette_contract_test.go
    // PARSES the line below to hold the two files in step, exactly as
    // TestJobPaletteMatchesSwift does for the eight job colors above. Keep it
    // as one literal `dyn(light: 0x…, dark: 0x…)` line: a hex assembled from
    // constants would read as a changed palette to that check.
    static let skillGraphUI = dyn(light: 0x456F0B, dark: 0x87DF49)
    static let skillGraph = Color(uiColor: skillGraphUI)

    // Per-job colors: the eight names Claude Code's own `/color` command
    // accepts, in `/color`'s order. The DARK values are Claude Code's exact
    // RGB, so a swatch here is the same pixel color as the prompt bar of the
    // pane that job's cell opens; light is the darkened sibling of each,
    // chosen to clear WCAG AA 4.5:1 against `raised` (#E3E9F1) and white.
    // The same sixteen hexes are spelled out in web/static/style.css's two
    // --job-* blocks and in internal/jobs/color.go's `Palette`, which is the
    // source of truth; TestJobPaletteMatchesCSS and TestJobPaletteMatchesSwift
    // (web/jobpalette_test.go) are what hold all three in step — the second
    // reads THIS file — so an edit here without the other two fails the
    // build rather than drifting silently, which is exactly how
    // --skill-info and --skill-do drifted before any such test existed. Keep
    // each entry as one literal `dyn(light: 0x…, dark: 0x…)` line: a hex
    // assembled from constants would read as a changed palette to that check.
    static let jobRedUI = dyn(light: 0xCA2323, dark: 0xDC2626)
    static let jobRed = Color(uiColor: jobRedUI)
    static let jobBlueUI = dyn(light: 0x496B8D, dark: 0x6A9BCC)
    static let jobBlue = Color(uiColor: jobBlueUI)
    static let jobGreenUI = dyn(light: 0x107937, dark: 0x16A34A)
    static let jobGreen = Color(uiColor: jobGreenUI)
    static let jobYellowUI = dyn(light: 0x8B5F03, dark: 0xCA8A04)
    static let jobYellow = Color(uiColor: jobYellowUI)
    static let jobPurpleUI = dyn(light: 0x676395, dark: 0x827DBD)
    static let jobPurple = Color(uiColor: jobPurpleUI)
    static let jobOrangeUI = dyn(light: 0x9C563F, dark: 0xD97757)
    static let jobOrange = Color(uiColor: jobOrangeUI)
    static let jobPinkUI = dyn(light: 0x9B516A, dark: 0xC46686)
    static let jobPink = Color(uiColor: jobPinkUI)
    static let jobCyanUI = dyn(light: 0x06718B, dark: 0x0891B2)
    static let jobCyan = Color(uiColor: jobCyanUI)

    /// A knowledge-graph node's fill, by entity type.
    ///
    /// The nine mappings are the port of web graphModel.ts's `TYPE_COLOR_VARS`
    /// — the same palette TOKEN per type, which is the claim this makes: a
    /// `component` takes `--polling` on both surfaces. Whether the two resolve
    /// to the same pixel is a property of each token, and seven of the nine do
    /// in both appearances; `--skill-do` (tool) and `--skill-info`
    /// (external-system) are drifted in LIGHT only — style.css has #7B6505 /
    /// #1866C4 where skillDoUI / skillInfoUI above have 0x8A7206 / 0x1A6FD4.
    /// That drift predates this map, is not the graph's to fix, and no test
    /// pins those two (TestJobPaletteMatchesSwift covers the eight job colors
    /// only); it is written down here so the next reader does not take the
    /// resemblance for a guarantee.
    ///
    /// All NINE seed types of the `/graph` skill's vocabulary are named: a
    /// seed type falling through to the invented-type grey made three
    /// documented types indistinguishable from unrecognised ones, which is a
    /// review finding the web side already carries.
    ///
    /// UIColor rather than Color because the canvas resolves it against a
    /// trait collection before drawing; the SwiftUI wrapper is one
    /// `Color(uiColor:)` at the call site.
    static func graphTypeColorUI(_ type: String) -> UIColor {
        switch type {
        case "convention": return accentUI
        case "component": return pollingUI
        case "failure-mode": return revealedUI
        case "decision": return solvedUI
        case "person": return counterUI
        case "tool": return skillDoUI
        case "external-system": return skillInfoUI
        // file and job are structural context rather than knowledge, so they
        // deliberately share the hairline grey instead of taking hues of their
        // own — they should recede behind the concepts they carry.
        case "file", "job": return hairline2UI
        // Anything the extraction agent invented that the map does not name.
        default: return text2UI
        }
    }

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
    // everything else in between consistently. fileprivate: nothing outside
    // this file needs the raw arithmetic — UIFont.voice below (UIKit's twin
    // of Font.voice, for HighlightingTextEditor's UITextView) lives in this
    // same file and reuses this exact function rather than a duplicated
    // literal that could drift from this rule.
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

extension UIFont {
    /// UIKit twin of Font.voice, for UIKit-hosted text (HighlightingTextEditor's
    /// UITextView) that can't consume a SwiftUI Font. Calls Font.bumped
    /// directly — fileprivate only blocks access outside this file, and this
    /// extension lives in the same file — so the two can never drift apart
    /// on sizing.
    static func voice(_ size: CGFloat, weight: UIFont.Weight = .regular) -> UIFont {
        .systemFont(ofSize: Font.bumped(size), weight: weight)
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

    /// The eight legal job-color names, in `/color`'s own order — the order
    /// the form's picker strip renders them in.
    static let jobColorNames = ["red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"]

    /// A job's `color` field → its swatch color, or nil for "no color".
    /// nil/empty is the unset case (the card renders exactly as it always
    /// has); an UNRECOGNIZED name — a newer server growing a ninth color —
    /// degrades to nil too rather than crashing or guessing.
    static func jobColor(_ name: String?) -> Color? {
        switch name {
        case "red": return KTheme.jobRed
        case "blue": return KTheme.jobBlue
        case "green": return KTheme.jobGreen
        case "yellow": return KTheme.jobYellow
        case "purple": return KTheme.jobPurple
        case "orange": return KTheme.jobOrange
        case "pink": return KTheme.jobPink
        case "cyan": return KTheme.jobCyan
        default: return nil
        }
    }

    /// An ROI band → how the badge paints. Each band maps to the same palette
    /// tokens web's `.ld-chip` rules name (`--counter`/`--counter-soft`,
    /// `--revealed`/`--revealed-soft`, `--text-2`, `--hairline-2`), which this
    /// enum's entries are the ports of, so the two boards grade one number the
    /// same way with no new hex here to keep in step with that stylesheet.
    /// Every token is already a dynamic light/dark pair, so both appearances
    /// follow for free.
    ///
    /// Hue is deliberately not the only channel — red/green is the common
    /// colour-blind failure and two of these four sit either side of it. So the
    /// box treatment separates them as well: the `hot` FILL is what tells it
    /// from `warm` without relying on pink-vs-amber, and the `cold` DASH is
    /// what tells it from `even`, which shares its colour.
    ///
    ///   hot   counter text on a counter-soft fill — the palette's "a human is
    ///         needed here" colour, which is what a high ratio says
    ///   warm  amber text and border, no fill
    ///   even  muted text, plain hairline border — the resting chip look
    ///   cold  the same muted pair, dashed and dimmed, so it reads with no
    ///         colour perception at all
    static func style(forLoadBand band: LoadBand) -> LoadBandStyle {
        switch band {
        case .hot:
            return LoadBandStyle(text: KTheme.counter, border: KTheme.counterSoft, fill: KTheme.counterSoft, dashed: false, opacity: 1)
        case .warm:
            return LoadBandStyle(text: KTheme.revealed, border: KTheme.revealedSoft, fill: nil, dashed: false, opacity: 1)
        case .even:
            return LoadBandStyle(text: KTheme.text2, border: KTheme.hairline2, fill: nil, dashed: false, opacity: 1)
        case .cold:
            return LoadBandStyle(text: KTheme.text2, border: KTheme.hairline2, fill: nil, dashed: true, opacity: 0.75)
        }
    }
}

/// How one ROI band paints. A value rather than four parallel switches so a
/// band can only ever be described in one place — see
/// `BoardTheme.style(forLoadBand:)`, which is the only thing that builds one.
struct LoadBandStyle {
    let text: Color
    let border: Color
    /// nil = no fill, the card surface shows through.
    let fill: Color?
    /// `cold`'s non-colour channel: the only dashed border on the card.
    let dashed: Bool
    /// `cold`'s second dimming, matching web's `.ld-chip.cold { opacity: .75 }`.
    /// 1 everywhere else.
    let opacity: Double
}

enum SessionTheme {
    /// running=solved, waiting=text2, question=revealed, blocked/error=counter,
    /// polling=cyan (mid poll-loop, idle between ScheduleWakeup ticks by
    /// design), shell=text2 — the same semantic mapping ls.go uses, expressed
    /// in the cyber-dark palette.
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
        case "polling":
            return KTheme.polling
        case "blocked", "error":
            return KTheme.counter
        default:
            return KTheme.text2
        }
    }
}

// MARK: - reusable view pieces

/// One kanban card's chrome: surface bg, 1px hairline border, and a 2px
/// leading bar that is the job's COLOR channel and nothing else — mirrors
/// .card / .card.flagged in style.css, including its precedence.
///
/// The bar is the job's color and NOTHING else — no state, and no longer any
/// flagged override. An uncolored job is grey whether it is draft, queued,
/// working, blocked or done, so the bar answers "which job is this", never
/// "how is it going" or "does this need me". State stays fully readable from
/// the card's column and its `● working` chip, which still use
/// BoardTheme.color(forState:); a pending question still shows its own
/// flag icon in the card's title row and its own square on the column
/// header, so dropping the card-wide pink glow cost no signal.
///
/// The parameter is `barColor` rather than `jobColor` or `stateColor`
/// because two different callers fill it: a board card passes the job's
/// colour, while JobDetailView's DetailCard passes a state colour, which is
/// meaningful there — you are already looking at exactly one job. nil means
/// grey either way.
struct CardBackground: ViewModifier {
    var barColor: Color?

    private var resolvedBar: Color { barColor ?? KTheme.text2 }

    func body(content: Content) -> some View {
        content
            .background(KTheme.surface)
            .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 1))
            .overlay(alignment: .leading) {
                Rectangle()
                    .fill(resolvedBar)
                    .frame(width: 2)
            }
    }
}

extension View {
    func cardBackground(barColor: Color? = nil) -> some View {
        modifier(CardBackground(barColor: barColor))
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
