import SwiftUI

@main
struct KrakenApp: App {
    init() {
        KTheme.applyGlobalAppearance()
    }

    var body: some Scene {
        WindowGroup {
            RootTabView()
                .preferredColorScheme(.dark)
                .tint(KTheme.accent)
        }
    }
}

struct RootTabView: View {
    var body: some View {
        TabView {
            BoardView()
                .tabItem { Label("Board", systemImage: "square.grid.2x2") }
            SessionsView()
                .tabItem { Label("Sessions", systemImage: "terminal") }
            SettingsView()
                .tabItem { Label("Settings", systemImage: "gearshape") }
        }
        .background(KTheme.ground)
    }
}
