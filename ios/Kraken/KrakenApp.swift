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

    private var preferredScheme: ColorScheme? {
        switch appearance {
        case "light": return .light
        case "dark": return .dark
        default: return nil
        }
    }

    init() {
        KTheme.applyGlobalAppearance()
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
                .preferredColorScheme(preferredScheme)
                .tint(KTheme.accent)
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
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape") }
                .tag(AppRouter.Tab.settings)
        }
        .background(KTheme.ground)
    }
}
