import SwiftUI

// BoardBar.swift — the board's search strip: one field, a clear button, the
// ⌘K hint and the match readout. Port of
// `web/_ui/src/components/board/BoardBar.tsx`.
//
// The search narrows what is RENDERED and nothing else. That is not a
// simplification, it is the rule: the drag layer always works on the WHOLE
// column, because posting the visible subset of a filtered column sends every
// hidden card to the top of that column permanently, with nothing on screen
// saying so. Only the set of visible keys crosses over — see
// `BoardScreen.commitReorder`'s `lastVisibleIndex`.
//
// Nothing is persisted; a relaunch lands unfiltered.

/// The board search's whole state and its derived render lists.
///
/// The match rule itself is the referenced `ios/Kraken/JobSearch.swift`
/// (`parseTerms` + `matchJob`), unchanged: two haystacks — names matched
/// fuzzily, prose matched by substring and only by terms of three UTF-16 code
/// units or more — and a job matches when EVERY term matches by either rule.
struct BoardSearch {
    var query: String = ""
    private(set) var terms: [String] = []
    /// jobKey → why the job survived. Empty when idle.
    private(set) var reasons: [String: MatchReason] = [:]
    /// The keys that survived. nil when idle — which is a different answer
    /// from "an empty set", and the difference is what stops an idle board
    /// from filtering itself to nothing.
    private(set) var visibleKeys: Set<String>?

    var active: Bool { !terms.isEmpty }

    /// Recomputes against a fresh job list. Cheap by design: ~74 jobs already
    /// arrive in the payload the board polls every 3s, so the filter is a walk
    /// over an array the client is holding anyway.
    mutating func refresh(jobs: [WebJob]) {
        terms = parseTerms(query)
        guard !terms.isEmpty else {
            reasons = [:]
            visibleKeys = nil
            return
        }
        var reasons: [String: MatchReason] = [:]
        var keys: Set<String> = []
        for job in jobs {
            guard let match = matchJob(job, terms: terms) else { continue }
            let key = JobKey.key(job)
            reasons[key] = match.reason
            keys.insert(key)
        }
        self.reasons = reasons
        self.visibleKeys = keys
    }

    /// `jobs` narrowed to what should be drawn. The identity of the answer
    /// when idle is the whole list, untouched.
    func visible(_ jobs: [WebJob]) -> [WebJob] {
        guard let visibleKeys else { return jobs }
        return jobs.filter { visibleKeys.contains(JobKey.key($0)) }
    }
}

struct BoardBar: View {
    @Binding var search: BoardSearch
    /// The count the readout ends in: every active job plus every archived
    /// one, matching the web board's `N jobs`.
    let total: Int
    let matched: Int
    /// True while ANY overlay is open. ⌘K is suppressed then — the steer
    /// composer is what carl types into for minutes at a time, and pulling
    /// focus out of it is worse than losing the shortcut.
    let overlayOpen: Bool

    @Environment(\.hudTheme) private var theme
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: 12) {
            field
            if !search.active {
                Text("⌘K").hudMono(size: 9).foregroundStyle(theme.mutedColor).opacity(0.6)
            }
            Spacer(minLength: 0)
            readout
            // The ⌘K binding, as a zero-sized button rather than a key
            // monitor: a keyboard shortcut on a Button is scoped to the
            // window, which is exactly the scope the web board reaches for
            // with a document listener plus a "is the board even visible"
            // guard. Disabled while an overlay is open, so the shortcut is
            // simply not claimed in that state.
            Button("") { focused = true }
                .keyboardShortcut("k", modifiers: .command)
                .buttonStyle(.plain)
                .disabled(overlayOpen)
                .opacity(0)
                .frame(width: 0, height: 0)
                .accessibilityHidden(true)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
        .overlay(alignment: .bottom) { Rectangle().fill(theme.dividerColor).frame(height: 1) }
    }

    private var field: some View {
        HStack(spacing: 0) {
            // Decoration rather than markup: an inline glyph would be one more
            // thing between the label and the field for assistive tech.
            Text("⌕")
                .hudMono(size: 13)
                .foregroundStyle(theme.mutedColor)
                .padding(.leading, 7)
                .padding(.trailing, 4)
            TextField("", text: $search.query, prompt: Text("search jobs…").foregroundStyle(theme.mutedColor))
                .textFieldStyle(.plain)
                .hudMono(size: 10, tracking: 0.04)
                .foregroundStyle(theme.textColor)
                .focused($focused)
                .accessibilityLabel("search jobs")
                // Escape clears the query and STOPS THERE. `.handled` is the
                // local meaning winning: without it the press would reach the
                // overlay stack and read as "close the top overlay".
                .onKeyPress(.escape) {
                    guard !search.query.isEmpty else { return .ignored }
                    search.query = ""
                    return .handled
                }
            if search.active {
                Button { search.query = "" } label: {
                    Text("✕").hudMono(size: 11).foregroundStyle(theme.mutedColor)
                }
                .buttonStyle(.plain)
                .hudCursor(.actable)
                .help("clear the search")
                .padding(.trailing, 4)
            }
        }
        .padding(.vertical, 5)
        .frame(width: 340)
        .hudCut(.input, border: focused ? theme.accentColor : theme.borderStrongColor)
        .hudGlow(focused ? theme.glowStyle : nil)
        .hudCursor(.text)
    }

    private var readout: some View {
        Group {
            if search.active {
                HStack(spacing: 4) {
                    Text("\(matched)").foregroundStyle(theme.accentColor)
                    Text("of \(total) jobs")
                }
            } else {
                Text("\(total) jobs")
            }
        }
        .hudMono(size: 10)
        .foregroundStyle(theme.mutedColor)
    }
}
