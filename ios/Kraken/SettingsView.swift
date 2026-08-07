import SwiftUI

// Deliberately not a Form: SwiftUI's grouped Form/List chrome fought this
// screen worse than anywhere else in the app — carl: "works but ui looks
// broken." A plain ScrollView + VStack of ThemedCards gives full control
// over spacing/surfaces with none of that baggage.
struct SettingsView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @AppStorage("notifyBlocked") private var notifyBlocked = false
    @AppStorage("appearance") private var appearance = "system"
    /// The theme tint, persisted under the one key KTheme owns. Writing a
    /// rawValue here is the whole picker: KrakenApp observes the same key and
    /// re-identifies the tree, so the app re-tints live.
    @AppStorage(KTheme.themeTintKey) private var themeTintRaw = ThemeTint.violet.rawValue
    @FocusState private var serverURLFocused: Bool
    @State private var testResult: String?
    @State private var testIsError = false
    @State private var isTesting = false
    @State private var notificationManager = NotificationManager.shared

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Appearance").kLabelStyle()

                            Picker("Appearance", selection: $appearance) {
                                Text("System").tag("system")
                                Text("Light").tag("light")
                                Text("Dark").tag("dark")
                            }
                            .pickerStyle(.segmented)

                            Text("System follows the iOS appearance, including scheduled dark mode.")
                                .font(.voice(11))
                                .foregroundStyle(KTheme.text2)
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Theme color").kLabelStyle()
                            themeTintStrip
                            Text("One hue drives the background, the glass and the accent. Job colors, state colors and skill colors never re-tint — those carry meaning.")
                                .font(.voice(11))
                                .foregroundStyle(KTheme.text2)
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Server").kLabelStyle()

                            TextField("http://100.90.94.3:7777", text: $serverURL)
                                .keyboardType(.URL)
                                .textInputAutocapitalization(.never)
                                .autocorrectionDisabled()
                                .focused($serverURLFocused)
                                .font(.data(13))
                                .kField(focused: serverURLFocused)

                            Button {
                                testConnection()
                            } label: {
                                if isTesting {
                                    ProgressView()
                                        .frame(maxWidth: .infinity)
                                } else {
                                    // full-width labeled action, not a circular
                                    // icon button — keeps its text (carl's
                                    // icon-only rule was for the compact pills)
                                    Label("Test Connection", systemImage: "network")
                                        .frame(maxWidth: .infinity)
                                }
                            }
                            .buttonStyle(.borderedProminent)
                            .disabled(isTesting || serverURL.isEmpty)

                            if let testResult {
                                Text(testResult)
                                    .font(.voice(12))
                                    .foregroundStyle(testIsError ? KTheme.counter : KTheme.solved)
                                    .fixedSize(horizontal: false, vertical: true)
                            }
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    ThemedCard {
                        VStack(alignment: .leading, spacing: 10) {
                            Text("Notifications").kLabelStyle()

                            Toggle(isOn: $notifyBlocked) {
                                Text("Notify when a job blocks")
                                    .font(.voice(13))
                                    .foregroundStyle(KTheme.text)
                            }
                            .onChange(of: notifyBlocked) {
                                if notifyBlocked {
                                    notificationManager.requestAuthorizationIfNeeded()
                                } else {
                                    // Sets frozen while the toggle is off go stale —
                                    // clear them so re-enabling seeds fresh instead
                                    // of bursting a week of missed transitions.
                                    notificationManager.resetConditionTracking()
                                }
                            }

                            if notifyBlocked && notificationManager.authorizationDenied {
                                Text("Notifications are denied in iOS Settings — enable them there to get alerts.")
                                    .font(.voice(11))
                                    .foregroundStyle(KTheme.revealed)
                            }

                            Text("Checked in the background every ~15 minutes (iOS-scheduled, not exact) plus whenever the board tab is open. Background delivery stops if the app is force-quit.")
                                .font(.voice(11))
                                .foregroundStyle(KTheme.text2)
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    Text("Server must run `k web --tailnet` on the Mac; traffic stays inside your tailnet.")
                        .font(.voice(11))
                        .foregroundStyle(KTheme.text2)
                        .padding(.horizontal, 4)
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background { ThemeBackgroundView() }
            .navigationTitle("Settings")
            .navigationBarTitleDisplayMode(.large)
            .task {
                if notifyBlocked {
                    notificationManager.refreshAuthorizationStatus()
                }
            }
        }
    }

    /// The eight curated tints, in `ThemeTint` order, same interaction shape
    /// as JobColorStrip: one tap = one swatch lit, persisted immediately. No
    /// free ColorPicker — the eight are curated so every one of them is
    /// legible against the glass in both appearances.
    ///
    /// The lit chip is derived from the STORED value through
    /// `ThemeTint.resolve`, so a defaults entry this build does not recognise
    /// renders as violet rather than as nothing selected.
    private var themeTintStrip: some View {
        let selected = ThemeTint.resolve(themeTintRaw)
        return ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 10) {
                ForEach(ThemeTint.allCases, id: \.self) { tint in
                    Button {
                        themeTintRaw = tint.rawValue
                    } label: {
                        Circle()
                            .fill(tint.swatch)
                            .frame(width: 26, height: 26)
                            .overlay(
                                Circle()
                                    .stroke(selected == tint ? KTheme.text : Color.clear, lineWidth: 2)
                                    .padding(-3)
                            )
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(tint.displayName)
                    .accessibilityAddTraits(selected == tint ? [.isButton, .isSelected] : [.isButton])
                }
            }
            // room for the 2pt selection ring, drawn outside each swatch's
            // own frame
            .padding(5)
        }
    }

    private func testConnection() {
        isTesting = true
        testResult = nil
        let url = serverURL
        Task {
            do {
                let health = try await APIClient.shared.fetchHealth(baseURL: url)
                isTesting = false
                testIsError = !health.ok
                testResult = health.ok ? "connected — \(health.host)" : "server responded but reported not ok"
            } catch {
                isTesting = false
                testIsError = true
                testResult = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
