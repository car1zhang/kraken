import SwiftUI

@main
struct KrakenApp: App {
    var body: some Scene {
        WindowGroup {
            RootTabView()
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
    }
}
