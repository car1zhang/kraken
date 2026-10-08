import SwiftUI
import BackgroundTasks

@main
struct KrakenApp: App {
    @State private var router = AppRouter()
    // Owned here (above every tab/sheet) rather than per-view, so a draft
    // survives dismissing a sheet or switching tabs — see DraftStore.swift.
    @State private var draftStore = DraftStore()
    // Owned here rather than by BoardView because TWO tabs now read it: the
    // Board (jobs, daemon LED, ship/contract banners) and Settings, whose
    // "Mac" section holds the ship + update controls that used to live in the
    // daemon bar. A @State inside BoardView would give Settings a second,
    // separate model — two poll loops, two divergent ship phases — so the one
    // instance lives above the TabView and both tabs read the same object.
    @State private var boardVM = BoardViewModel()
    /// Whether the `-KrakenRoute` launch argument has already been acted on.
    /// Lives on the App (not on the identified view below), so `.id(themeTint)`
    /// tearing the tree down cannot reset it — see `consumeLaunchRoute`.
    @State private var didConsumeLaunchRoute = false
    @Environment(\.scenePhase) private var scenePhase
    @AppStorage("appearance") private var appearance = "system"
    // The persisted theme tint. Read here (rather than only inside KTheme's
    // colour providers) so SwiftUI OBSERVES it: a Settings tap writes the
    // rawValue, this re-evaluates, and the `.id(themeTint)` below tears the
    // whole tree down and rebuilds it so every KTheme token re-resolves
    // against the new hue — no relaunch.
    @AppStorage(KTheme.themeTintKey) private var themeTintRaw = ThemeTint.violet.rawValue

    private var themeTint: ThemeTint { ThemeTint.resolve(themeTintRaw) }

    private var preferredScheme: ColorScheme? {
        switch appearance {
        case "light": return .light
        case "dark": return .dark
        default: return nil
        }
    }

    init() {
        NotificationManager.shared.attachDelegate()
        // BGTaskScheduler registration must happen before the app finishes
        // launching, so init() (there's no AppDelegate in this SwiftUI-only
        // app) — not a button/toggle handler — is the only correct place.
        // Registering is unconditional and harmless; only the actual
        // schedule/handle calls check the "notify me" toggle.
        BGTaskScheduler.shared.register(forTaskWithIdentifier: NotificationManager.refreshTaskIdentifier, using: nil) { task in
            guard let refreshTask = task as? BGAppRefreshTask else {
                task.setTaskCompleted(success: false)
                return
            }
            NotificationManager.shared.handleBackgroundRefresh(task: refreshTask)
        }
    }

    var body: some Scene {
        WindowGroup {
            RootTabView()
                .environment(router)
                .environment(draftStore)
                .environment(boardVM)
                .environment(\.theme, GlassTheme(tint: themeTint))
                .preferredColorScheme(preferredScheme)
                .tint(KTheme.accent)
                // Re-identify on a tint change: KTheme's tokens are dynamic
                // UIColors that read the stored tint at RESOLVE time, so they
                // only pick up a new hue once SwiftUI re-renders the views
                // holding them. Discarding the identity is the blunt but
                // reliable way to guarantee that everywhere at once.
                .id(themeTint)
                .onAppear { consumeLaunchRoute() }
                .onOpenURL { url in
                    router.handle(url: url)
                }
        }
        .onChange(of: scenePhase) {
            if scenePhase == .background {
                NotificationManager.shared.scheduleBackgroundRefresh()
            }
            if scenePhase == .active {
                // Forget failures recorded before the suspension — they say the
                // radio was off, not that the Mac is gone. Here rather than in
                // either tab's poll task, because those are keyed on this same
                // transition and would race it; here rather than in BoardView,
                // because the ship poll also runs from Settings and this has to
                // fire whichever tab carl came back to.
                boardVM.noteBecameActive()
            }
        }
    }

    /// The launch-argument route (`simctl launch … -KrakenRoute kraken://job/x`),
    /// used by the orchestrator's simulator test drive because `openurl` stalls
    /// behind the "Open in Kraken?" system dialog.
    ///
    /// Exactly ONCE per process, and the flag is what enforces it. The obvious
    /// spelling — read it, `removeObject`, done — silently doesn't work: a value
    /// supplied as a launch argument lives in `NSArgumentDomain`, which is
    /// read-only, so `removeObject` writes nothing and the key still reads back.
    /// That was harmless only while this `onAppear` ran once; it no longer does,
    /// because `.id(themeTint)` above discards the tree's identity on a tint
    /// change and the fresh tree appears again. Net effect before this flag, in
    /// any run LAUNCHED with `-KrakenRoute` — so: the orchestrator's test
    /// drives, not carl's phone, where the argument is never present and the
    /// key never set — picking a new theme colour in Settings teleported you
    /// back to whatever screen the app was launched at.
    private func consumeLaunchRoute() {
        guard !didConsumeLaunchRoute else { return }
        didConsumeLaunchRoute = true
        guard let raw = UserDefaults.standard.string(forKey: "KrakenRoute"),
              let url = URL(string: raw) else { return }
        router.handle(url: url)
    }
}

/// Four tabs, in this order: Board · Sessions · Memory · Settings.
///
/// "Memory" is the third tab: the saved-artifact library and the mental model
/// (`MemoryRootView`). Its tag is still `AppRouter.Tab.graph`, and
/// `kraken://graph` still selects it — names kept from when the tab hosted the
/// knowledge graph, so older deep links keep working.
///
/// Usage and Log used to be tabs three and four. They are drawers now — Usage
/// off the Board's toolbar, Log off a Settings row — which bought the slot the
/// Memory tab took. Five tabs is the ceiling before iOS collapses a TabView
/// into a "More" list.
struct RootTabView: View {
    @Environment(AppRouter.self) private var router
    // The Memory tab talks to the same Mac as every other screen; the tab root
    // reads the stored URL and hands MemoryRootView a validated one.
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL

    var body: some View {
        @Bindable var router = router
        TabView(selection: $router.selectedTab) {
            BoardView()
                .tabItem { Label("Board", systemImage: "square.grid.2x2") }
                .tag(AppRouter.Tab.board)
            SessionsView()
                .tabItem { Label("Sessions", systemImage: "terminal") }
                .tag(AppRouter.Tab.sessions)
            NavigationStack {
                if let base = memoryServerBase(serverURL) {
                    MemoryRootView(baseURL: base)
                } else {
                    MemoryNoServerView()
                }
            }
            // The glyph is the old graph tab's, kept so the tab bar looks the
            // same — carl on the alternative: "this brain is kinda buns".
            //
            // The tag stays `AppRouter.Tab.graph` and `kraken://graph` still
            // selects it, so a deep link shipped in an older build lands here.
            .tabItem { Label("Memory", systemImage: "point.3.connected.trianglepath.dotted") }
            .tag(AppRouter.Tab.graph)
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape") }
                .tag(AppRouter.Tab.settings)
        }
        .background { ThemeBackgroundView() }
    }
}
