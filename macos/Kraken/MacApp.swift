import SwiftUI

/// The `@AppStorage` keys the app persists, in one place so the Settings scene
/// and the readers cannot drift on a string literal.
enum SettingsKey {
    static let serverURL = "serverURL"
    static let themeMode = "themeMode"
    static let hudAccent = "hudAccent"
    static let hudFX = "hudFX"
    static let notificationsEnabled = "notificationsEnabled"
}

/// Kraken.app.
///
/// ONE window (`id: "board"`) rather than a `WindowGroup`: the board is a
/// singleton instrument, and a second copy of it would poll the same server
/// twice and answer the same deep link twice. `.hiddenTitleBar` is what lets
/// the HUD chrome own the top of the window.
@main
struct MacApp: App {
    @StateObject private var router = MacRouter()

    var body: some Scene {
        Window("Kraken", id: "board") {
            RootView()
                .environmentObject(router)
                .frame(minWidth: 900, minHeight: 600)
                // kraken://… links, registered by the CFBundleURLTypes entry
                // in macos/project.yml. An unparseable link is ignored rather
                // than surfaced: the app was already showing something valid.
                .onOpenURL { url in
                    guard let route = MacRoute.parse(url) else { return }
                    router.apply(route)
                }
        }
        .windowStyle(.hiddenTitleBar)

        Settings {
            SettingsScreen()
        }
    }
}

/// The Settings scene (⌘,).
///
/// Theme, accent and FX are stored as RAW STRINGS rather than as the S2 enums:
/// `@AppStorage` needs a `RawRepresentable` bound at compile time, and keeping
/// the stored form a string means a value written by an older build that names
/// an accent this build dropped reads back as an unrecognised string the
/// resolver falls back on, instead of failing to decode.
struct SettingsScreen: View {
    @AppStorage(SettingsKey.serverURL) private var serverURL = ""
    @AppStorage(SettingsKey.themeMode) private var themeMode = "system"
    @AppStorage(SettingsKey.hudAccent) private var hudAccent = "violet"
    @AppStorage(SettingsKey.hudFX) private var hudFX = "full"
    @AppStorage(SettingsKey.notificationsEnabled) private var notificationsEnabled = true

    // Mirrors the raw values of S2's `ThemeMode` / `HudAccent` (50-contract.md).
    private static let themeModes = ["system", "dark", "light"]
    private static let accents = ["violet", "cyan", "amber", "crimson", "green"]
    private static let fxLevels = ["full", "reduced", "off"]

    var body: some View {
        Form {
            Section("Server") {
                TextField("Server URL", text: $serverURL, prompt: Text(ServerConfig.fallbackURL.absoluteString))
                    .textFieldStyle(.roundedBorder)
                Text("Resolves to \(ServerConfig.baseURL(stored: serverURL).absoluteString)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Section("Appearance") {
                Picker("Theme", selection: $themeMode) {
                    ForEach(Self.themeModes, id: \.self) { Text($0.capitalized).tag($0) }
                }
                Picker("Accent", selection: $hudAccent) {
                    ForEach(Self.accents, id: \.self) { Text($0.capitalized).tag($0) }
                }
                Picker("Effects", selection: $hudFX) {
                    ForEach(Self.fxLevels, id: \.self) { Text($0.capitalized).tag($0) }
                }
            }
            Section("Notifications") {
                Toggle("Notify on blocked / started / erroring", isOn: $notificationsEnabled)
            }
        }
        // Turning the toggle OFF drops the three tracking sets, so switching it
        // back on seeds silently instead of firing one banner per transition
        // that happened while it was off. `NotificationEngine.process` does the
        // same on its next pass; this makes it immediate rather than waiting on
        // a board tick that may not come while Settings is frontmost.
        .onChange(of: notificationsEnabled) { _, on in
            if !on { NotificationEngine.shared.resetConditionTracking() }
        }
        .formStyle(.grouped)
        .frame(width: 460)
        .padding()
    }
}
