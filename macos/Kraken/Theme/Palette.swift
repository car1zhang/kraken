import SwiftUI

// Palette.swift — the two FIXED palettes: the eight job colours and the six
// skill colours. Fixed means they do not follow the accent and never derive
// from anything; they are the one part of the HUD whose hexes are pinned
// against sources OUTSIDE this app.
//
// The job palette is pinned three ways over: `internal/jobs/color.go`'s
// `Palette` (the only copy that reaches tmux and the TUI), style.css's two
// `--job-*` blocks (:130-131 dark, :211-212 light), and
// `ios/Kraken/Theme.swift`'s `jobXxxUI` pairs. They must agree to the byte,
// because the whole promise of the feature is that a board swatch is the same
// pixel colour as the prompt bar of the pane that job opens.
// `web/jobpalette_test.go` is what keeps the existing three in step, and it
// works by parsing each file with a regex — which is why the tables below are
// written as one `name: "#hex"` pair per line and nothing cleverer. A
// cross-source test pointed at this file has to be able to read it without a
// Swift parser.
//
// The skill palette is pinned the same way, against style.css :99-112 (dark)
// and :184-193 (light), plus `web/graphpalette_contract_test.go` for
// `--skill-graph` specifically.
//
// Order is `/color`'s own for jobs and the pipeline's own for skills, and in
// both cases it is the order the swatch rows render in — so `names` is part of
// the contract, not a convenience.

/// The eight colours Claude Code's `/color` accepts, in `/color`'s order.
enum JobPalette {
    /// `/color`'s own order — the order the editor's swatch row renders.
    static let names: [String] = ["red", "blue", "green", "yellow", "purple", "orange", "pink", "cyan"]

    /// Dark-theme hexes — Claude Code's exact RGB
    /// (`internal/jobs/color.go` `Hex`, style.css :130-131).
    static let dark: [String: String] = [
        "red": "#DC2626",
        "blue": "#6A9BCC",
        "green": "#16A34A",
        "yellow": "#CA8A04",
        "purple": "#827DBD",
        "orange": "#D97757",
        "pink": "#C46686",
        "cyan": "#0891B2",
    ]

    /// Light-theme hexes — each darkened until it clears WCAG AA 4.5:1
    /// against both the light raised surface and plain white
    /// (`internal/jobs/color.go` `HexLight`, style.css :211-212).
    static let light: [String: String] = [
        "red": "#CA2323",
        "blue": "#496B8D",
        "green": "#107937",
        "yellow": "#8B5F03",
        "purple": "#676395",
        "orange": "#9C563F",
        "pink": "#9B516A",
        "cyan": "#06718B",
    ]

    /// The job colour for a `/color` name, or nil for anything not on the
    /// list — including `""`, which is what an uncoloured job carries. nil is
    /// load-bearing rather than a mere absence: it is what makes a card fall
    /// back to the 40%-accent edge (`--accent-edge`) instead of painting a
    /// default colour that would claim the job had one.
    static func color(_ name: String, _ mode: ThemeMode) -> RGB? {
        let table = mode == .dark ? dark : light
        guard let hex = table[name] else { return nil }
        return RGB(hex: hex)
    }

    /// The same lookup as a SwiftUI colour, for call sites that only paint.
    static func swatch(_ name: String, _ mode: ThemeMode) -> Color? {
        color(name, mode)?.color
    }
}

/// The six pipeline commands, each with a solid colour and a `-soft` fill.
/// Used by the composer's command highlighting, the `/skill` chips, and — for
/// dream and graph — by those two views' whole accent.
enum SkillPalette {
    /// The pipeline's order, which is the order the chips sit in.
    static let names: [String] = ["info", "aim", "do", "pr", "dream", "graph"]

    /// Solid skill colours, dark theme (style.css :99-112).
    static let dark: [String: String] = [
        "info": "#5EA8FF",
        "aim": "#FF6B6B",
        "do": "#F5D34D",
        "pr": "#4ADE80",
        "dream": "#C084FC",
        "graph": "#87DF49",
    ]

    /// Soft fills, dark theme (style.css :99-112).
    static let softDark: [String: String] = [
        "info": "#102A45",
        "aim": "#3A1414",
        "do": "#332B0E",
        "pr": "#0F2E1D",
        "dream": "#2A1440",
        "graph": "#1E3010",
    ]

    /// Solid skill colours, light theme (style.css :184-193). info/do/pr/graph
    /// are darkened past the naive "same pattern" hex to clear 4.5:1 against
    /// both the raised surface and their own soft fill.
    static let light: [String: String] = [
        "info": "#1866C4",
        "aim": "#C42B2B",
        "do": "#7B6505",
        "pr": "#14773B",
        "dream": "#7E22CE",
        "graph": "#456F0B",
    ]

    /// Soft fills, light theme (style.css :184-193).
    static let softLight: [String: String] = [
        "info": "#DCEAFB",
        "aim": "#FBE0E0",
        "do": "#F7F0D4",
        "pr": "#DCF3E4",
        "dream": "#F3E8FF",
        "graph": "#EAF6D8",
    ]

    /// The solid colour for a skill token (`"do"`, never `"/do"`), or nil for
    /// anything that is not one of the six.
    static func color(_ name: String, _ mode: ThemeMode) -> RGB? {
        let table = mode == .dark ? dark : light
        guard let hex = table[name] else { return nil }
        return RGB(hex: hex)
    }

    /// The soft fill for a skill token, or nil for anything not on the list.
    static func soft(_ name: String, _ mode: ThemeMode) -> RGB? {
        let table = mode == .dark ? softDark : softLight
        guard let hex = table[name] else { return nil }
        return RGB(hex: hex)
    }
}
