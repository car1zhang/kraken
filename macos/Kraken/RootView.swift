import Combine
import SwiftUI

/// The four top-level views the window switches between — the mac equivalent
/// of the web board's `#/`, `#/log`, `#/dream`, `#/graph` hash routes.
enum RootTab: String, CaseIterable, Identifiable {
    case board, log, dream, graph
    var id: String { rawValue }
}

/// The window's navigation state, and the one thing `.onOpenURL` writes into.
///
/// Deliberately thin: a route arriving from a deep link is applied here and the
/// views read it, rather than each screen owning its own link handling.
@MainActor
final class MacRouter: ObservableObject {
    @Published var tab: RootTab = .board
    /// The column a `kraken://board/<column>` link asked to focus, if any.
    @Published var focusColumn: BoardColumn?
    /// The slug a `kraken://job/<slug>` link asked to open, if any.
    @Published var focusJob: String?

    /// Applies a parsed deep link. Unhandled-here cases (`settings`, which the
    /// Settings scene owns) are simply not a tab change.
    func apply(_ route: MacRoute) {
        switch route {
        case .board(let column):
            tab = .board
            focusColumn = column
        case .job(let slug):
            tab = .board
            focusJob = slug
        case .graph: tab = .graph
        case .log: tab = .log
        case .dream: tab = .dream
        case .settings: break
        }
    }
}

/// The window's content: the HUD chrome over the four screens, wrapped in the
/// three static window layers.
///
/// TWO STRUCTURAL RULES, both load-bearing:
///
///  1. **Every screen stays MOUNTED.** The web hides views rather than
///     unmounting them (`App.tsx:254-264`) precisely so the LOG view keeps its
///     paged history and scroll, the DREAM view keeps its expanded dates and
///     half-typed comments, and the GRAPH view keeps its camera and its
///     simulation. Switching tabs here changes opacity and hit testing, never
///     the view tree — and each screen takes a `visible` flag so its POLL still
///     stops, which is the other half of the web's behaviour.
///  2. **`.hudWindowLayers()` wraps EVERYTHING, overlays included.** The
///     scanline rake is CRT glass and sits in front of whatever the screen
///     shows (style.css :302-308), which is only expressible if it is the last
///     layer of the window's own ZStack. Applying it any lower would put it
///     underneath whatever is above that point.
struct RootView: View {
    @EnvironmentObject private var router: MacRouter
    /// Declared by S3 (`macos/Kraken/Board/BoardStore.swift`) and by S4
    /// (`macos/Kraken/Overlays/OverlayRouter.swift`) — 50-contract.md's
    /// cross-slice seams. The root is the one place either is constructed.
    @StateObject private var store = BoardStore()
    @StateObject private var overlays = OverlayRouter()
    @StateObject private var chrome = ChromeModel()
    /// SEAM: the usage poll is shared by the chrome's widget and the usage
    /// overlay — two surfaces of ONE 60s poll — so it is created here rather
    /// than inside either. `OverlayHost` reads it out of the environment and
    /// mounts `UsagePanel(usage:)` with it.
    @StateObject private var usage = UsageModel()

    @AppStorage(SettingsKey.themeMode) private var themeMode = "system"
    @AppStorage(SettingsKey.hudAccent) private var hudAccent = HudAccent.violet.rawValue
    @AppStorage(SettingsKey.hudFX) private var hudFX = "full"

    /// The SYSTEM appearance, read here — above `.hudTheme`, which overrides
    /// `colorScheme` for everything below it. Reading it any lower returns the
    /// theme's own answer, which would pin `system` to whatever it last was.
    @Environment(\.colorScheme) private var systemScheme

    /// The stored preference is three-valued (`system`/`light`/`dark`, like
    /// the web board's `kraken-theme` where absent means follow-system); the
    /// resolved `ThemeMode` stays the contract's two, and `system` is decided
    /// here per render so a macOS appearance flip re-resolves live.
    private var resolvedMode: ThemeMode {
        switch themeMode {
        case "light": return .light
        case "dark": return .dark
        default: return systemScheme == .light ? .light : .dark
        }
    }

    /// The resolved palette. Recomputed only when the inputs change — the
    /// derivations are cheap, but `HudTheme` is `Equatable` and lands in the
    /// environment, so a stable value is one SwiftUI comparison per
    /// invalidation rather than a re-derivation per read.
    private var theme: HudTheme {
        HudTheme.resolve(resolvedMode, HudAccent(rawValue: hudAccent) ?? .violet)
    }

    var body: some View {
        VStack(spacing: 0) {
            ChromeBar()
            ZStack {
                screen(.board) { BoardScreen() }
                screen(.log) { LogScreen(visible: router.tab == .log) }
                screen(.dream) { DreamScreen(visible: router.tab == .dream) }
                screen(.graph) { GraphScreen(visible: router.tab == .graph) }
                OverlayHost()
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        // The chrome bar IS the titlebar. `.hiddenTitleBar` removes the
        // system bar's backing but the window still insets content below the
        // titlebar AREA via the safe area — which left the HUD bar sitting
        // under a strip of empty window (carl: "distinct from the top bar of
        // the application"). Ignoring the top inset slides row 1 up behind the
        // traffic lights; its 78pt leading pad is what keeps them clear.
        .ignoresSafeArea(.container, edges: .top)
        .environmentObject(store)
        .environmentObject(overlays)
        .environmentObject(chrome)
        .environmentObject(usage)
        // ORDER IS LOAD-BEARING. `.hudWindowLayers()`'s rake/grid/strip and the
        // window-level rest cursor are ATTACHED here, so the environment
        // modifiers must come AFTER them in the chain to wrap them — with
        // `.hudTheme`/`.hudScanlines` applied first (inside), the overlays sat
        // outside both environments and rendered the DEFAULT theme: dark-ink
        // scanlines in light mode, an inert ▦ FX toggle, and a white (dark-art)
        // cursor over light-mode modals. Found at the manual gate.
        .hudWindowLayers()
        .hudCursor(.rest)
        .hudTheme(theme)
        .hudScanlines(hudFX != "off")
        .onAppear {
            NotificationEngine.shared.attachDelegate()
            store.start()
            usage.start()
        }
        // `WebBoard` is not `Equatable`, so this is `onReceive` on the store's
        // own publisher rather than `onChange` — the notifier has to see EVERY
        // successful poll, since the diff is what decides whether anything
        // changed.
        .onReceive(store.$board.compactMap { $0 }) { board in
            NotificationEngine.shared.process(board)
        }
    }

    /// One mounted-but-possibly-hidden screen. `allowsHitTesting` is what makes
    /// a hidden screen inert: a transparent view still swallows clicks.
    @ViewBuilder
    private func screen<Content: View>(
        _ tab: RootTab, @ViewBuilder content: () -> Content
    ) -> some View {
        let active = router.tab == tab
        content()
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .opacity(active ? 1 : 0)
            .allowsHitTesting(active)
    }
}
