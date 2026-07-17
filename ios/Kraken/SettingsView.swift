import SwiftUI

// Deliberately not a Form: SwiftUI's grouped Form/List chrome (rounded
// cards, gray insets, doubled-background bordered buttons) fought the flat
// cyber-dark look here worse than anywhere else in the app — carl: "works
// but ui looks broken." A plain ScrollView + VStack gives full control over
// spacing/backgrounds with none of that baggage.
struct SettingsView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @AppStorage("notifyBlocked") private var notifyBlocked = false
    @FocusState private var serverURLFocused: Bool
    @State private var testResult: String?
    @State private var testIsError = false
    @State private var isTesting = false
    @State private var notificationManager = NotificationManager.shared

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 24) {
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
                                Label("Test Connection", systemImage: "bolt.horizontal")
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
                    .padding(12)
                    .background(KTheme.surface)
                    .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 1))

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
                    .padding(12)
                    .background(KTheme.surface)
                    .overlay(Rectangle().stroke(KTheme.hairline, lineWidth: 1))

                    Text("Server must run `k web --tailnet` on the Mac; traffic stays inside your tailnet.")
                        .font(.voice(11))
                        .foregroundStyle(KTheme.text2)
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(KTheme.ground)
            .navigationTitle("Settings")
            .task {
                if notifyBlocked {
                    notificationManager.refreshAuthorizationStatus()
                }
            }
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
