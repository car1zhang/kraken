import SwiftUI

struct SettingsView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @FocusState private var serverURLFocused: Bool
    @State private var testResult: String?
    @State private var testIsError = false
    @State private var isTesting = false

    var body: some View {
        NavigationStack {
            Form {
                kSection("Server") {
                    TextField("http://100.90.94.3:7777", text: $serverURL)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .focused($serverURLFocused)
                        .font(.data(13))
                        .kField(focused: serverURLFocused)
                        .listRowBackground(Color.clear)
                    Button {
                        testConnection()
                    } label: {
                        if isTesting {
                            ProgressView()
                        } else {
                            Label("Test Connection", systemImage: "bolt.horizontal")
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(isTesting || serverURL.isEmpty)
                    .listRowBackground(Color.clear)
                    if let testResult {
                        Text(testResult)
                            .font(.voice(12))
                            .foregroundStyle(testIsError ? KTheme.counter : KTheme.solved)
                    }
                }
                kSection {
                    Text("Server must run `k web --tailnet` on the Mac; traffic stays inside your tailnet.")
                        .font(.voice(11))
                        .foregroundStyle(KTheme.text2)
                }
            }
            .kListStyle()
            .navigationTitle("Settings")
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
