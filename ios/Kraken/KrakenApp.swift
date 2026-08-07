import SwiftUI
import BackgroundTasks

@main
struct KrakenApp: App {
    @State private var router = AppRouter()
    // Owned here (above every tab/sheet) rather than per-view, so a draft
    // survives dismissing a sheet or switching tabs — see DraftStore.swift.
    @State private var draftStore = DraftStore()
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
                .environment(\.theme, GlassTheme(tint: themeTint))
                .preferredColorScheme(preferredScheme)
                .tint(KTheme.accent)
                // Re-identify on a tint change: KTheme's tokens are dynamic
                // UIColors that read the stored tint at RESOLVE time, so they
                // only pick up a new hue once SwiftUI re-renders the views
                // holding them. Discarding the identity is the blunt but
                // reliable way to guarantee that everywhere at once.
                .id(themeTint)
                .onAppear {
                    // launch-argument route (simctl launch ... -KrakenRoute
                    // kraken://job/x): lands in UserDefaults; used by the
                    // orchestrator's simulator test drive, where openurl would
                    // stall behind the "Open in Kraken?" system dialog
                    if let s = UserDefaults.standard.string(forKey: "KrakenRoute"),
                       let url = URL(string: s) {
                        UserDefaults.standard.removeObject(forKey: "KrakenRoute")
                        router.handle(url: url)
                    }
                }
                .onOpenURL { url in
                    router.handle(url: url)
                }
        }
        .onChange(of: scenePhase) {
            if scenePhase == .background {
                NotificationManager.shared.scheduleBackgroundRefresh()
            }
        }
    }
}

struct RootTabView: View {
    @Environment(AppRouter.self) private var router

    var body: some View {
        @Bindable var router = router
        TabView(selection: $router.selectedTab) {
            BoardView()
                .tabItem { Label("Board", systemImage: "square.grid.2x2") }
                .tag(AppRouter.Tab.board)
            SessionsView()
                .tabItem { Label("Sessions", systemImage: "terminal") }
                .tag(AppRouter.Tab.sessions)
            UsageView()
                .tabItem { Label("Usage", systemImage: "gauge.with.dots.needle.50percent") }
                .tag(AppRouter.Tab.usage)
            LogView()
                .tabItem { Label("Log", systemImage: "list.bullet.rectangle") }
                .tag(AppRouter.Tab.log)
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape") }
                .tag(AppRouter.Tab.settings)
        }
        .background { ThemeBackgroundView() }
    }
}
