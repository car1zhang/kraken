import SwiftUI

// The MEMORY tab's root: a two-segment switch over the saved-artifact library
// and carl's mental model. It replaces the knowledge-graph screen that used to
// host both panes as its second and third modes. With the graph gone, what is
// left is the switch and nothing else, so this file stays small.
//
// The tab's internal names did not change with it: the tag is still
// `AppRouter.Tab.graph` and `kraken://graph` still selects the tab, so a deep
// link from an older build still lands here.

/// The Memory tab root: `Library` / `Model` over `LibraryList` and
/// `MentalModelList`.
///
/// It takes a `URL` rather than the raw `@AppStorage` string. The tab root
/// decides whether the stored value is usable at all (`memoryServerBase`) and
/// shows the "No server URL set" message in its place when it is not.
struct MemoryRootView: View {
    let baseURL: URL

    init(baseURL: URL) {
        self.baseURL = baseURL
    }

    /// Which half of the tab is on screen. It is not persisted: the tab opens
    /// on the library every time.
    enum Mode: String, CaseIterable {
        case library = "Library"
        case model = "Model"
    }

    @State private var mode: Mode = .library

    /// The base the two lists append their `/api/...` paths to. Both take a
    /// String; `memoryServerBase` has already trimmed any trailing slash.
    private var base: String { baseURL.absoluteString }

    var body: some View {
        // The unselected pane is out of the tree, not hidden, so its poll
        // stops while the other one is up.
        VStack(spacing: 0) {
            modeSwitch
            switch mode {
            case .library:
                LibraryList(baseURL: base)
            case .model:
                MentalModelList(baseURL: base)
            }
        }
        .background { ThemeBackgroundView() }
        .navigationTitle("Memory")
    }

    private var modeSwitch: some View {
        Picker("Mode", selection: $mode) {
            ForEach(Mode.allCases, id: \.self) { value in
                Text(value.rawValue).tag(value)
            }
        }
        .pickerStyle(.segmented)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.ultraThinMaterial)
    }
}

/// The stored server URL as a usable base, or nil when there is none.
///
/// The same gate the graph screen applied to its own `baseURL`: whitespace
/// trimmed, trailing slashes removed rather than tolerated, and anything with
/// interior whitespace or that `URL` refuses reads as unset.
func memoryServerBase(_ raw: String) -> URL? {
    let trimmed = raw.trimmingCharacters(in: .whitespaces)
    guard !trimmed.isEmpty else { return nil }
    var base = trimmed
    while base.hasSuffix("/") { base.removeLast() }
    guard !base.isEmpty,
          base.rangeOfCharacter(from: .whitespacesAndNewlines) == nil
    else { return nil }
    return URL(string: base)
}

/// The Memory tab's placeholder when no server URL is set.
struct MemoryNoServerView: View {
    var body: some View {
        VStack(spacing: 12) {
            Text("No server URL set")
                .font(.voice(15))
                .foregroundStyle(KTheme.counter)
            Text("Set it in Settings, then reopen this screen.")
                .font(.voice(13))
                .foregroundStyle(KTheme.text2)
                .multilineTextAlignment(.center)
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background { ThemeBackgroundView() }
        .navigationTitle("Memory")
    }
}

/// `YYYY-MM-DD` in the phone's time zone for a unix-millis instant, "" for a
/// non-positive one. It moved here from the deleted GraphModel.swift (where it
/// was `graphShortDate`) because the library and model rows date themselves
/// with it.
///
/// Gregorian rather than taken from `Calendar.current` because the format is
/// shared with the web view: a phone set to the Buddhist or Japanese calendar
/// would otherwise render a different year than the board does.
func memoryShortDate(ms: Int64) -> String {
    guard ms > 0 else { return "" }
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = TimeZone.current
    let parts = calendar.dateComponents(
        [.year, .month, .day],
        from: Date(timeIntervalSince1970: Double(ms) / 1000)
    )
    guard let year = parts.year, let month = parts.month, let day = parts.day else { return "" }
    return String(format: "%04d-%02d-%02d", year, month, day)
}
