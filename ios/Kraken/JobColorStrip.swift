import SwiftUI

/// The job-color picker: a "none" chip followed by one chip per palette
/// color, saving straight to `POST /api/job/color` on tap.
///
/// It is its own view, used by BOTH JobFormView and JobDetailView, for a
/// reason worth stating: the form is only reachable through JobDetailView's
/// "Edit Spec" button, which is gated on `!job.live` — so a picker that
/// lived only there could be reached precisely when recoloring is least
/// interesting, and never while a cell was up. `/api/job/color` carries no
/// editable-job gate at all (unlike `/api/job/saveform`, a full replace that
/// 409s on a live cell), so this strip is deliberately rendered OUTSIDE
/// every `isEditable`/`editable` guard in both hosts. It also never touches
/// the form payload: a tap here mutates nothing but the color.
struct JobColorStrip: View {
    let baseURL: String
    let slug: String
    /// The color the server last reported, from whatever payload the host
    /// already has (`JobForm.color` in the form, `WebJob.color` on the
    /// board). nil/"" means no color.
    let serverColor: String?
    /// Called with the newly-saved value once a save lands, so a host that
    /// keeps its own copy can stay in step.
    var onSaved: (String) -> Void = { _ in }
    /// How a pick is saved. nil (every local host) posts `/api/job/color`;
    /// a remote public card passes its proxy call instead.
    var save: ((String) async throws -> Void)? = nil

    /// The chip currently lit. Local rather than derived from `serverColor`
    /// so a tap is optimistic — the board's own refresh is seconds away.
    @State private var selected = ""
    @State private var seeded = false
    /// In-flight save count. While non-zero, an incoming `serverColor` is
    /// ignored: a board poll that started before this tap would otherwise
    /// hand back the pre-tap value and visibly un-pick the chip.
    @State private var inFlight = 0
    @State private var errorText: String?

    private let chip: CGFloat = 22

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            // Same horizontal chip strip as the form's model suggestions
            // (and for the same reason — .bordered inside a List row renders
            // as a doubled-background blob), with each chip's fill standing
            // in for its label.
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 8) {
                    // "none" clears the color rather than picking one, so it
                    // is drawn hollow instead of filled.
                    Button {
                        apply("")
                    } label: {
                        Circle()
                            .fill(KTheme.raised)
                            .frame(width: chip, height: chip)
                            .overlay(Circle().stroke(KTheme.hairline, lineWidth: 1))
                            .overlay(
                                Text("—")
                                    .font(.data(11))
                                    .foregroundStyle(KTheme.text2)
                            )
                            .overlay(ring(on: selected.isEmpty))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("no color")
                    ForEach(BoardTheme.jobColorNames, id: \.self) { name in
                        Button {
                            apply(name)
                        } label: {
                            Circle()
                                .fill(BoardTheme.jobColor(name) ?? KTheme.raised)
                                .frame(width: chip, height: chip)
                                .overlay(ring(on: selected == name))
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel(name)
                    }
                }
                // room for the 2pt selection ring, which is drawn outside
                // each chip's own frame
                .padding(5)
            }
            if let errorText, !errorText.isEmpty {
                Text(errorText).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
        }
        // Seed once from whatever the host loaded, then let taps drive it.
        .task {
            guard !seeded else { return }
            selected = serverColor ?? ""
            seeded = true
        }
        // Keep in step when the host's copy genuinely changes underneath
        // (a board refresh, a recolor from the web board) — but never while
        // a save of our own is in flight, see `inFlight`.
        .onChange(of: serverColor) { _, new in
            guard inFlight == 0 else { return }
            selected = new ?? ""
        }
    }

    private func ring(on: Bool) -> some View {
        Circle()
            .stroke(on ? KTheme.text : Color.clear, lineWidth: 2)
            .padding(-3)
    }

    /// Optimistic: the chip lights up immediately, then the POST lands. On
    /// failure the previous value is restored — but only if nothing newer
    /// has been picked since, so a later tap's value always wins over an
    /// earlier tap's rollback — and the reason is surfaced under the strip.
    private func apply(_ name: String) {
        guard name != selected else { return }
        let previous = selected
        selected = name
        errorText = nil
        inFlight += 1
        Task {
            defer { inFlight -= 1 }
            do {
                if let save {
                    try await save(name)
                } else {
                    try await APIClient.shared.setJobColor(baseURL: baseURL, slug: slug, color: name)
                }
                onSaved(name)
            } catch {
                if selected == name { selected = previous }
                errorText = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }
}
