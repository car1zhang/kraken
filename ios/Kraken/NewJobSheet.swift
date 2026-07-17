import SwiftUI

/// The "+" toolbar flow: type a slug, create the file server-side, then drop
/// straight into the structured form editor for it.
struct NewJobSheet: View {
    let baseURL: String
    var onCreated: () -> Void

    @Environment(\.dismiss) private var dismiss
    @FocusState private var slugFocused: Bool
    @State private var slug = ""
    @State private var isBusy = false
    @State private var errorMessage: String?
    @State private var createdSlug: String?

    var body: some View {
        Group {
            if let createdSlug {
                JobFormView(slug: createdSlug, baseURL: baseURL, isNew: true) {
                    onCreated()
                    dismiss()
                }
            } else {
                Form {
                    kSection("New job") {
                        TextField("slug", text: $slug)
                            .autocorrectionDisabled()
                            .textInputAutocapitalization(.never)
                            .focused($slugFocused)
                            .font(.data(13))
                            .kField(focused: slugFocused)
                            .listRowBackground(Color.clear)
                    }
                    if let errorMessage {
                        kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
                    }
                }
                .kListStyle()
                .navigationTitle("New Job")
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .cancellationAction) {
                        Button("Cancel") { dismiss() }.buttonStyle(.bordered)
                    }
                    ToolbarItem(placement: .confirmationAction) {
                        Button {
                            create()
                        } label: {
                            Label("Create", systemImage: "plus")
                        }
                        .labelStyle(.iconOnly)
                        .accessibilityLabel("Create")
                        .buttonStyle(.borderedProminent)
                        .disabled(isBusy || slug.trimmingCharacters(in: .whitespaces).isEmpty)
                    }
                }
                .disabled(isBusy)
            }
        }
    }

    private func create() {
        isBusy = true
        errorMessage = nil
        Task {
            do {
                let raw = try await APIClient.shared.createJob(baseURL: baseURL, slug: slug)
                isBusy = false
                createdSlug = raw.slug
            } catch {
                isBusy = false
                errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
