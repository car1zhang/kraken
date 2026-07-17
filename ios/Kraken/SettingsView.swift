import SwiftUI

struct SettingsView: View {
    @AppStorage("serverURL") private var serverURL = KrakenDefaults.serverURL
    @State private var testResult: String?
    @State private var testIsError = false
    @State private var isTesting = false

    var body: some View {
        NavigationStack {
            Form {
                Section("Server") {
                    TextField("http://100.90.94.3:7777", text: $serverURL)
                        .keyboardType(.URL)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    Button {
                        testConnection()
                    } label: {
                        if isTesting {
                            ProgressView()
                        } else {
                            Text("Test Connection")
                        }
                    }
                    .disabled(isTesting || serverURL.isEmpty)
                    if let testResult {
                        Text(testResult).foregroundStyle(testIsError ? .red : .green)
                    }
                }
                Section {
                    Text("Server must run `k web --tailnet` on the Mac; traffic stays inside your tailnet.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
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
