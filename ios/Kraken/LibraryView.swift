import SwiftUI
import Observation

// The saved-artifact library: the LIBRARY half of the Memory tab, plus the one
// pure function both halves of the app's save affordance ask the same question
// through.
//
// It lives in the Memory tab rather than on the board because a saved artifact
// has outlived its job by design — the server stores frozen BYTES, so a row
// stays readable after the job is torn down, archived or hard deleted. There is
// no card to hang it off any more, which is the whole reason it is here: the
// Memory tab is where the things kraken keeps after the work is over live.
//
// Nothing here re-sorts. The wire contract states `saved_at` DESC with `id`
// DESC as the tiebreak, and a client that sorted again would be a second
// opinion about ordering that could only ever disagree with the first.

// MARK: - is this artifact saved?

/// The library row an artifact URL refers to, or nil.
///
/// A free function rather than a method for the reason `flattenArtifactOrder`
/// is one: it is the whole of the "is this saved?" decision, both the pager's
/// bookmark glyph and the library list's own identity depend on it, and a
/// closure inside a View is unreachable from a test.
///
/// Two URL shapes reach it, and they are answered differently on purpose:
///
/// * `/api/library/artifact/<id>/<name>` — a SNAPSHOT url, which carries the
///   library's own identity, so it matches by id and the name segment is
///   ignored entirely. That mirrors the server, where the name is display and
///   extension only and the lookup is by id.
/// * `/api/artifact/<slug>/<home>/<name>` — a LIVE artifact url, which has no
///   library id in it, so it matches on the `(slug, name)` pair the row froze
///   at save time. `home` is deliberately not compared: it is a namespace the
///   same artifact moves between (a cell job archived becomes `archive`) while
///   staying the one thing that was saved.
///
/// Anything else — an upload url, a malformed path, a library url whose id
/// segment is not a number — is nil rather than a guess.
func libraryItemMatching(url: String, in items: [WebLibraryItem]) -> WebLibraryItem? {
    if let id = libraryArtifactID(from: url) {
        return items.first { $0.id == id }
    }
    if let ref = artifactParts(from: url) {
        return items.first { $0.slug == ref.slug && $0.name == ref.name }
    }
    return nil
}

/// The row id out of a `/api/library/artifact/<id>/<name>` path, or nil.
///
/// The id segment is taken as everything up to the next slash OR the end of
/// the string: the contract calls the trailing `<name>` display-only, so a url
/// that has lost it still addresses the same row, and answering it by id is
/// the same answer the server gives. A non-numeric or empty segment is nil —
/// there is no row it could mean.
private func libraryArtifactID(from url: String) -> Int64? {
    let prefix = "/api/library/artifact/"
    guard url.hasPrefix(prefix) else { return nil }
    let rest = url.dropFirst(prefix.count)
    let segment = rest.firstIndex(of: "/").map { rest[..<$0] } ?? rest
    return Int64(segment)
}

// MARK: - the model

@Observable
final class LibraryViewModel {
    /// Served order, held as served.
    var items: [WebLibraryItem] = []
    var errorMessage: String?
    var loaded = false
    /// Ids with a remove POST in flight — the row's control disables so a
    /// double tap can't race two writes against one row (the DreamView idiom).
    var inFlight: Set<Int64> = []

    func refresh(baseURL: String) async {
        do {
            items = try await APIClient.shared.fetchLibrary(baseURL: baseURL).items
            errorMessage = nil
        } catch {
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        loaded = true
    }

    /// Drops a row and mirrors the removal locally on success, so the list
    /// closes up without waiting for a refetch.
    func remove(baseURL: String, item: WebLibraryItem) async {
        guard !inFlight.contains(item.id) else { return }
        inFlight.insert(item.id)
        defer { inFlight.remove(item.id) }
        do {
            try await APIClient.shared.removeFromLibrary(baseURL: baseURL, id: item.id)
            items.removeAll { $0.id == item.id }
            errorMessage = nil
        } catch {
            errorMessage = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }
}

// MARK: - the list

/// LIBRARY mode's screen: the saved rows, newest first, each opening the
/// artifact viewer on its own frozen snapshot.
///
/// It owns its own sheet rather than reporting taps upward, which keeps
/// MemoryRootView's mode switch to the one thing it is — a switch.
struct LibraryList: View {
    let baseURL: String

    @State private var vm = LibraryViewModel()
    /// The snapshot being read, as a `WebArtifact` so the existing pager can
    /// present it unchanged. One page: a library row is opened on its own, not
    /// paged across the library — the pager's sequence is a JOB's artifact set,
    /// and these rows come from many jobs and many generations of them.
    @State private var opened: WebArtifact?

    var body: some View {
        List {
            if let errorMessage = vm.errorMessage {
                kSection { Text(errorMessage).font(.voice(12)).foregroundStyle(KTheme.counter) }
            }
            if !vm.items.isEmpty {
                kSection("Saved · \(vm.items.count)") {
                    ForEach(vm.items) { item in
                        row(item)
                    }
                }
            }
            if vm.loaded && vm.items.isEmpty && vm.errorMessage == nil {
                kSection {
                    Text("Nothing saved yet — the bookmark in an artifact's toolbar freezes a copy here.")
                        .font(.voice(12))
                        .foregroundStyle(KTheme.text2)
                }
            }
        }
        .kListStyle()
        .refreshable { await vm.refresh(baseURL: baseURL) }
        .task(id: baseURL) { await vm.refresh(baseURL: baseURL) }
        .sheet(item: $opened) { artifact in
            NavigationStack {
                ArtifactPagerView(artifacts: [artifact], selected: artifact, baseURL: baseURL)
                    .toolbar {
                        ToolbarItem(placement: .cancellationAction) {
                            SheetCloseButton { opened = nil }
                        }
                    }
            }
        }
    }

    private func row(_ item: WebLibraryItem) -> some View {
        Button {
            opened = WebArtifact(name: item.name, url: item.url)
        } label: {
            HStack(spacing: 10) {
                Image(systemName: "bookmark.fill")
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(KTheme.accent)
                VStack(alignment: .leading, spacing: 2) {
                    Text(item.name)
                        .font(.data(12, weight: .semibold))
                        .foregroundStyle(KTheme.text)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                    Text(subtitle(item))
                        .font(.data(10))
                        .foregroundStyle(KTheme.text2)
                }
                Spacer(minLength: 0)
                if vm.inFlight.contains(item.id) { ProgressView().controlSize(.small) }
            }
            .contentShape(Rectangle())
            .padding(.vertical, 2)
        }
        .buttonStyle(.plain)
        .swipeActions(edge: .trailing) {
            // Not `.destructive` role on the button's own label alone: the
            // guard that actually matters is the model's `inFlight` check,
            // since a swipe action stays tappable while its POST is in the
            // air and two removes of one row would 404 the second.
            Button(role: .destructive) {
                Task { await vm.remove(baseURL: baseURL, item: item) }
            } label: {
                Label("Remove", systemImage: "trash")
            }
            .disabled(vm.inFlight.contains(item.id))
        }
    }

    /// `slug · date` — the job it came from and the day it was frozen. Both
    /// are the SAVED values: the slug may since have been freed by an archive
    /// and the job may be gone entirely, which is exactly why the row carries
    /// its own copy rather than looking one up.
    private func subtitle(_ item: WebLibraryItem) -> String {
        let day = memoryShortDate(ms: item.savedAt)
        return day.isEmpty ? item.slug : "\(item.slug) · \(day)"
    }
}
