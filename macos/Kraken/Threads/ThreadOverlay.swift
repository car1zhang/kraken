import AppKit
import SwiftUI

// ThreadOverlay.swift — a side thread: a fork of zeus' session running as a
// second claude in its own tmux window of the same cell, somewhere to ASK
// without disturbing the job.
//
// ONE overlay, two modes, split on `tid`:
//   nil  "branch" — a required name + an optional seed question
//   set  "thread" — read / steer / kill / revive one that exists
// They share a head, a body column and a close path, which is why they are one
// view rather than two.
//
// Every op goes through `/api/threads`, so a server refusal ("no live cell to
// branch", "3 live threads already", "t2 can't be revived: …") reaches the
// reader IN THE SERVER'S OWN WORDS. This file never invents error copy for a
// failed op — it only points at the status bar.
//
// The accent throughout is `--polling` cyan (`theme.polling`), not the board
// accent: a thread is the one surface that is deliberately NOT the job, and the
// colour is what says so at a glance.
//
// The panel chrome, the scrim and the Escape stack are `OverlayHost`'s, which
// wraps this in `HudPanel` — nothing below draws a background or a corner cut.

// There is no tid conversion anywhere in this slice, and that is the point.
// The wire mints a tid as a STRING — `fmt.Sprintf("t%d", max+1)` in
// `store.AddThreadNext` (`internal/store/threads.go:485`) — and `WebThread.tid`,
// `ThreadActionRequest.tid`, `/api/thread/read`'s query and the `OverlayRouter`
// seam now all carry that same string. The `ThreadTID` enum that used to sit
// here bridged an `Int?` seam; S4 landed the string form (contract amendment
// 22) and its last caller went with it, so the bridge is gone rather than left
// as an unused pair of functions that would invite a second spelling back.

// MARK: - the overlay

struct ThreadOverlay: View {
    /// The job the thread hangs off.
    let slug: String
    /// nil opens the branch dialog; a tid opens that thread.
    ///
    /// A wire-shaped STRING (`"t7"`), which is now the only spelling anywhere:
    /// the board's `threads` rows, `/api/threads`, `/api/thread/read` and the
    /// router seam all carry it unchanged, so this value reaches the server
    /// exactly as it arrived and nothing in between can lose the prefix.
    let initialTID: String?
    /// Closes this overlay. Owned by the host, because the artifact and thread
    /// layers' closes are also what the Escape stack drives.
    let onClose: () -> Void

    @EnvironmentObject private var board: BoardStore
    @EnvironmentObject private var router: OverlayRouter
    @Environment(\.hudTheme) private var theme
    @AppStorage(SettingsKey.serverURL) private var serverURLRaw = ""
    /// Opt-in and STICKY, under the same key the web board writes: a capture
    /// nobody opened is a second tmux round-trip every 3s for a pane nobody is
    /// looking at. One click, remembered from then on.
    @AppStorage("kraken-thread-mirror") private var mirror = false

    /// Local, so a successful branch can flip the OPEN overlay onto the thread
    /// it created rather than closing and reopening.
    @State private var tid: String?

    @State private var note = ""
    @State private var busy = false
    @State private var messageText = ""
    @State private var newName = ""
    @State private var seedText = ""

    @State private var renaming = false
    @State private var renameValue = ""
    /// A just-branched or just-renamed thread's title, until the board catches
    /// its row.
    @State private var titleOverride: String?

    // What the last /api/thread/read landed.
    @State private var blocks: [ReadBlock] = []
    @State private var blockSignature = ""
    @State private var forkUUID = ""
    @State private var counted = 0
    @State private var readLive: Bool?
    @State private var readPane: String?
    @State private var readFailure: String?

    @State private var captureText = ""
    @State private var captureOK = true

    @State private var nearBottom = true
    @State private var sawJob = false

    @FocusState private var nameFocused: Bool
    @FocusState private var renameFocused: Bool

    private var baseURL: String { ServerConfig.baseURL(stored: serverURLRaw).absoluteString }

    /// The `n` the web board deliberately omits so the server picks its own —
    /// `threadReadDefaultN` in `internal/api/threads.go`, mirrored here only
    /// because the referenced `APIClient.readThread` takes it as a required
    /// argument. A big number means re-reading and re-parsing multi-megabyte
    /// transcript tails every tick to redraw a column nobody scrolled.
    private static let readBlockCount = 50

    private static let bottomAnchor = "thread-convo-bottom"

    // MARK: - derived state

    /// The job's freshest board entry. Its `threads` array is where the head
    /// reads title/state/pane between reads — nil means the job has left the
    /// board, which is the ONE condition that force-closes this overlay.
    private var job: WebJob? {
        board.board?.jobs.first { $0.slug == slug && !($0.archived ?? false) }
    }

    private var record: WebThread? {
        guard let tid else { return nil }
        return job?.threads?.first { $0.tid == tid }
    }

    /// Two sources of truth, merged the way the web board merges them: the
    /// board payload (every 3s, knows the title) and the read endpoint's own
    /// live/pane (fresher — it just looked at tmux). Whichever landed last
    /// wins, which is safe only because head AND body paint from this one
    /// derivation: the two out-of-phase timers can disagree about the AGE of
    /// the truth, never about what is on screen.
    private var live: Bool { readLive ?? (record?.isLive ?? false) }

    private var pane: String {
        if let readPane, !readPane.isEmpty { return readPane }
        return record?.pane ?? ""
    }

    private var shownTitle: String { titleOverride ?? record?.title ?? "" }

    /// A read failure gets its own note and clears its OWN note on recovery, so
    /// it cannot eat a "send failed" / "message is empty" the reader just
    /// triggered.
    private var shownNote: String { readFailure ?? note }

    private var mirrorable: Bool { live && !pane.isEmpty && captureOK }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            head
            if tid == nil { branchBody } else { threadBody }
            Text(shownNote)
                .hudMono(size: 10)
                .foregroundStyle(theme.counterColor)
                .frame(minHeight: 14, alignment: .leading)
                .padding(.horizontal, 14)
                .padding(.bottom, 10)
        }
        .onAppear {
            if tid == nil { tid = initialTID }
            nameFocused = (tid == nil)
        }
        // checkStaleModals, the ASYMMETRIC half: a thread outlives
        // working→blocked→done, and a closed one outlives the cell. So unlike
        // steer and editor this closes ONLY when the job itself leaves the
        // board entirely — archived, deleted, renamed out from under it.
        .onChange(of: job == nil) { _, gone in
            if !gone {
                sawJob = true
            } else if sawJob {
                onClose()
            }
        }
        // The 3s read, only while the overlay is open ON A THREAD. Re-armed by
        // a branch flipping `tid`, and cancelled by the view going away.
        .task(id: tid ?? "") { await readLoop() }
        .task(id: "\(tid ?? "")|\(live)|\(pane)|\(mirror)") { await captureLoop() }
    }

    // MARK: - head

    @ViewBuilder
    private var head: some View {
        HudPanelHead(tid.map { "⑂ " + $0 } ?? "⑂ branch — " + slug) {
            if tid != nil {
                if renaming { renameField } else { renameLabel }
                statePill
            }

            Spacer(minLength: 0)

            // Raises THIS THREAD's window, BY PANE — pointless once the window
            // is gone, and hidden without a pane too: a slug-only post would
            // raise zeus' window instead, the one window a thread exists not to
            // disturb.
            if tid != nil, live, !pane.isEmpty {
                HudButton("⧉ tmux", style: .ghost) {
                    let target = pane
                    Task {
                        if let problem = await openTmux(pane: target) {
                            router.toast("couldn't focus the window: " + problem)
                        }
                    }
                }
                .help("raise this thread's window in Ghostty")
            }
            if tid != nil, !live {
                HudButton("⟲ revive", style: .ghost) { Task { await op(.revive) } }
                    .disabled(busy)
                    .help("resume this thread's session in a fresh window of the cell")
            }
            // No confirm, deliberately, and the web board has none either:
            // killing a thread only closes its tmux window — the row and the
            // conversation stay on the job, and ⟲ revive brings it back.
            if tid != nil, live {
                HudButton("✕ kill thread", style: .danger) { Task { await op(.kill) } }
                    .disabled(busy)
                    .help("close this thread's tmux window — the conversation stays and can be revived")
            }
        }
    }

    @ViewBuilder
    private var renameLabel: some View {
        Button {
            renameValue = shownTitle
            renaming = true
        } label: {
            Text(shownTitle.isEmpty ? "name this thread" : shownTitle)
                .hudMono(size: 11)
                .foregroundStyle(shownTitle.isEmpty ? theme.mutedColor : theme.bodyColor)
                .lineLimit(1)
        }
        .buttonStyle(.plain)
        .help("rename this thread")
    }

    @ViewBuilder
    private var renameField: some View {
        TextField("thread name", text: $renameValue)
            .textFieldStyle(.plain)
            .focusEffectDisabled()
            .hudMono(size: 11)
            .foregroundStyle(theme.textColor)
            .padding(.horizontal, 6)
            .frame(height: 22)
            .frame(maxWidth: 260)
            .background(HudTheme.mix(theme.ac, 3, over: theme.b1).color)
            .overlay { Rectangle().strokeBorder(theme.pollingColor, lineWidth: 1) }
            .focused($renameFocused)
            .disabled(busy)
            .onAppear { renameFocused = true }
            // The same 80 the branch field caps at and the same one
            // AskThread.title budgets against, so a name branched from an
            // artifact question can always be edited back to in place.
            .onChange(of: renameValue) { _, next in
                if next.count > AskThread.cap { renameValue = String(next.prefix(AskThread.cap)) }
            }
            // Enter commits. `.onSubmit` is not reached by a CJK candidate's
            // commit Enter — AppKit consumes that inside the input context —
            // so this is the IME guard the web board has to write by hand.
            .onSubmit { Task { await commitRename() } }
            // Escape here means "stop renaming", not "close the overlay".
            // `.onExitCommand` consumes the press before the host's key
            // monitor is reached, which is the AppKit spelling of the web's
            // `stopPropagation`.
            .onExitCommand { renaming = false }
            .onChange(of: renameFocused) { _, focused in
                // blur cancels — the web board's own rule
                if !focused { renaming = false }
            }
    }

    @ViewBuilder
    private var statePill: some View {
        Text(live ? "live" : "closed")
            .hudMono(size: 9, tracking: HudTracking.stat)
            .foregroundStyle(live ? theme.pollingColor : theme.mutedColor)
            .padding(.horizontal, 7)
            .frame(height: 18)
            .background(live ? theme.pollingSoft : theme.chipBgColor)
            .overlay {
                Rectangle().strokeBorder(live ? theme.pollingColor : theme.borderColor, lineWidth: 1)
            }
    }

    // MARK: - branch mode

    @ViewBuilder
    private var branchBody: some View {
        VStack(alignment: .leading, spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                Text("⑂ branch a side thread")
                    .hudDisplay(size: 11, tracking: HudTracking.control)
                    .foregroundStyle(theme.pollingColor)
                Text("Forks zeus' session as it stands right now into a new window of the same cell, in the same worktree. Zeus is not interrupted and this conversation is unaffected. Threads are for asking, not for doing.")
                    .hudMono(size: 10)
                    .foregroundStyle(theme.mutedColor)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .padding(10)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(theme.pollingSoft)
            .overlay { Rectangle().strokeBorder(theme.pollingColor.opacity(0.5), lineWidth: 1) }

            field("what is this thread for?") {
                TextField("short name — e.g. short-clip WER", text: $newName)
                    .hudField(size: 11)
                    .focused($nameFocused)
                    .onChange(of: newName) { _, next in
                        if next.count > AskThread.cap { newName = String(next.prefix(AskThread.cap)) }
                    }
            }

            field("seed question — optional") {
                TextEditor(text: $seedText)
                    .font(.hudMono(11))
                    .hudEditor(minHeight: 80)
                    .frame(maxHeight: 160)
            }

            HStack(spacing: 8) {
                ThreadBigButton(title: "⑂ branch thread ⌘⏎") { Task { await branch() } }
                    .disabled(busy)
                    // ⌘⏎ from EITHER field: a keyboard shortcut is window-wide
                    // while this button is on screen, which is exactly the two
                    // fields the web board wires it to by hand.
                    .keyboardShortcut(.return, modifiers: .command)
                HudButton("cancel", style: .ghost) { onClose() }
                Spacer(minLength: 0)
            }
        }
        .padding(14)
    }

    // MARK: - thread mode

    @ViewBuilder
    private var threadBody: some View {
        VStack(alignment: .leading, spacing: 8) {
            conversation

            if mirrorable {
                // The pane head IS the mirror's toggle: capture-pane runs on
                // every 3s tick while it is open, so it is opt-in and sticky.
                Button {
                    mirror.toggle()
                } label: {
                    HStack(spacing: 6) {
                        Circle().fill(theme.pollingColor).frame(width: 6, height: 6)
                        Text(paneLabel)
                            .hudMono(size: 10, tracking: HudTracking.stat)
                            .foregroundStyle(theme.mutedColor)
                    }
                }
                .buttonStyle(.plain)
                .help("mirror this thread's tmux pane — polls while open")

                if mirror {
                    ScrollView {
                        Text(captureText)
                            .hudMono(size: 10)
                            .foregroundStyle(theme.bodyColor)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(8)
                    }
                    .frame(maxHeight: 220)
                    .background(theme.b0.color)
                    .overlay { Rectangle().strokeBorder(theme.dividerColor, lineWidth: 1) }
                }
            }

            if live {
                HStack(alignment: .bottom, spacing: 8) {
                    TextEditor(text: $messageText)
                        .font(.hudMono(11))
                        .hudEditor(minHeight: 54)
                        .frame(maxHeight: 120)
                    ThreadBigButton(title: "send ⌘⏎") { Task { await send() } }
                        .disabled(busy)
                        .keyboardShortcut(.return, modifiers: .command)
                }
            } else {
                Text(closedNote)
                    .hudMono(size: 10)
                    .foregroundStyle(theme.mutedColor)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(14)
    }

    private var paneLabel: String {
        (mirror ? "▾ live pane · " : "▸ live pane · ")
            + ((job?.cell.map { $0 + " · " }) ?? "")
            + pane
    }

    /// `tmux window closed · N blocks kept on the job · killed Xs ago · revive
    /// to continue` — each clause dropped when absent. A `counted` of 0 means
    /// "no read has landed yet", not "nothing was kept", so it says nothing
    /// rather than undercounting the conversation sitting right above.
    private var closedNote: String {
        var s = "tmux window closed"
        if counted > 0 { s += " · \(counted) blocks kept on the job" }
        if let closed = record?.closedAt, closed > 0 {
            // created_at/closed_at are not pinned to a unit by the wire
            // contract, so a millisecond stamp is normalised rather than
            // rendered as a 56-year-old thread.
            let secs = closed > 1_000_000_000_000 ? closed / 1000 : closed
            let elapsed = Int(Int64(Date().timeIntervalSince1970) - secs)
            s += " · killed \(compactAgeLabel(secondsElapsed: elapsed)) ago"
        }
        return s + " · revive to continue"
    }

    // MARK: - conversation

    @ViewBuilder
    private var conversation: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 10) {
                    if blocks.isEmpty {
                        Text(readFailure == nil ? "no messages yet" : "")
                            .hudMono(size: 10)
                            .foregroundStyle(theme.mutedColor)
                    }
                    ForEach(Array(blocks.enumerated()), id: \.offset) { index, block in
                        if index == splitIndex { divider }
                        ThreadBlockView(
                            block: block,
                            inherited: index < splitIndex,
                            tid: tid ?? ""
                        )
                    }
                    // A thread whose every block is inherited (branched,
                    // nothing asked yet) still gets its divider — at the very
                    // end, which is exactly where it is.
                    if !blocks.isEmpty, splitIndex >= blocks.count { divider }
                    Color.clear.frame(height: 1).id(Self.bottomAnchor)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.vertical, 4)
            }
            .frame(minHeight: 220, maxHeight: 520)
            .onScrollGeometryChange(for: Bool.self) { geo in
                geo.contentOffset.y + geo.containerSize.height >= geo.contentSize.height - 24
            } action: { _, atBottom in
                nearBottom = atBottom
            }
            // Scroll is pinned to the bottom ONLY when the reader was already
            // near it (the live-tail case). Someone reading back through a long
            // thread must not be yanked forward by a poll.
            .onChange(of: blockSignature) { _, _ in
                guard nearBottom else { return }
                proxy.scrollTo(Self.bottomAnchor, anchor: .bottom)
            }
        }
    }

    /// One past the last inherited block. The server tags every block with
    /// `inherited` and that is the only shape there is, so the split is 0 when
    /// the thread has said everything itself and `blocks.count` when it has
    /// said nothing yet.
    private var splitIndex: Int {
        var last = -1
        for (i, b) in blocks.enumerated() where b.inherited == true { last = i }
        return last + 1
    }

    @ViewBuilder
    private var divider: some View {
        HStack(spacing: 6) {
            Rectangle().fill(theme.pollingColor.opacity(0.4)).frame(height: 1)
            Text("⑂ branched here" + (forkUUID.isEmpty ? "" : " · " + forkUUID))
                .hudMono(size: 9, tracking: HudTracking.stat)
                .foregroundStyle(theme.pollingColor)
                .fixedSize()
            Rectangle().fill(theme.pollingColor.opacity(0.4)).frame(height: 1)
        }
        .padding(.vertical, 2)
    }

    // MARK: - polling

    private func readLoop() async {
        guard let tid else { return }
        while !Task.isCancelled {
            do {
                let res = try await APIClient.shared.readThread(
                    baseURL: baseURL, slug: slug, tid: tid, n: Self.readBlockCount
                )
                guard !Task.isCancelled else { return }
                apply(res)
                readFailure = nil
            } catch is CancellationError {
                return
            } catch {
                guard !Task.isCancelled else { return }
                readFailure = "couldn't read this thread: "
                    + ((error as? KrakenError)?.errorDescription ?? error.localizedDescription)
            }
            do { try await Task.sleep(nanoseconds: 3_000_000_000) } catch { return }
        }
    }

    /// Repaints only when the conversation ACTUALLY changed. The web board
    /// compares the rendered HTML string before writing it; the same idea here
    /// is a cheap signature over everything a block renders FROM, which is what
    /// keeps a no-op 3s tick from rebuilding the column and resetting the
    /// scroll under a reader.
    private func apply(_ res: ThreadReadResponse) {
        let next = res.blocks ?? []
        let signature = next
            .map { "\($0.kind)\u{1}\($0.inherited == true ? 1 : 0)\u{1}\($0.from ?? "")\u{1}\($0.text)" }
            .joined(separator: "\u{2}")
        if signature != blockSignature {
            blocks = next
            blockSignature = signature
        }
        forkUUID = res.forkUUID
        counted = res.counted
        readLive = res.live
        readPane = res.pane
    }

    private func captureLoop() async {
        guard tid != nil, mirror, live, !pane.isEmpty else { return }
        let target = pane
        while !Task.isCancelled {
            do {
                let content = try await APIClient.shared.capturePane(baseURL: baseURL, pane: target)
                guard !Task.isCancelled else { return }
                captureText = content
                captureOK = true
            } catch is CancellationError {
                return
            } catch {
                guard !Task.isCancelled else { return }
                // A capture that fails is almost always a pane that just died,
                // and the next read re-derives live/pane — so it stays QUIET
                // and the mirror goes away rather than freezing the last frame
                // or explaining itself in the note.
                captureOK = false
                return
            }
            do { try await Task.sleep(nanoseconds: 3_000_000_000) } catch { return }
        }
    }

    // MARK: - ops

    @discardableResult
    private func op(_ kind: ThreadOp, title: String? = nil, text: String? = nil) async -> Bool {
        guard let tid else { return false }
        busy = true
        var failure: String?
        do {
            _ = try await APIClient.shared.threadAction(
                baseURL: baseURL, slug: slug, op: kind, tid: tid, title: title, text: text
            )
        } catch {
            failure = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        await board.refetch()
        busy = false
        if let failure {
            router.toast("✕ " + failure)
            note = kind.rawValue + " failed — see status bar"
            return false
        }
        note = ""
        return true
    }

    private func branch() async {
        let name = newName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !name.isEmpty else {
            note = "name this thread — one line on what it's for"
            nameFocused = true
            return
        }
        busy = true
        var failure: String?
        var newTID: String?
        do {
            let res = try await APIClient.shared.threadAction(
                baseURL: baseURL, slug: slug, op: .branch, title: name,
                text: seedText.trimmingCharacters(in: .whitespacesAndNewlines)
            )
            newTID = res.tid
        } catch {
            failure = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
        await board.refetch()
        busy = false
        if let failure {
            router.toast("✕ " + failure)
            note = "branch failed — see status bar"
            return
        }
        note = ""
        if let newTID {
            // Flip onto the new thread IN PLACE. The board has not caught its
            // row yet, so the name just typed carries the head until it does.
            titleOverride = name
            tid = newTID
        } else {
            // Branched, but no id came back — the card carries it.
            onClose()
        }
    }

    private func send() async {
        let body = messageText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else {
            note = "message is empty"
            return
        }
        if await op(.send, text: body) { messageText = "" }
    }

    private func commitRename() async {
        let next = renameValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !next.isEmpty, next != (record?.title ?? "") else {
            renaming = false
            return
        }
        let ok = await op(.rename, title: next)
        if ok { titleOverride = next }
        renaming = false
    }

    /// `POST /api/peers/open` — the one endpoint this slice needs that the
    /// referenced iOS `APIClient` has no method for (it is a web-only surface
    /// there, and `ios/` is read-only to this job).
    ///
    /// Returns a sentence to toast, or nil when the window came up clean: the
    /// handler answers 2xx with an additive `focus_error` when the RAISE failed
    /// even though the request succeeded, which is a real failure to report.
    private func openTmux(pane: String) async -> String? {
        guard let u = URL(string: baseURL + "/api/peers/open") else { return "bad server URL" }
        var req = URLRequest(url: u)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: ["pane": pane])
        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
            if let http = resp as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
                return (obj["error"] as? String) ?? "HTTP \(http.statusCode)"
            }
            return obj["focus_error"] as? String
        } catch {
            return error.localizedDescription
        }
    }

    // MARK: - local chrome

    @ViewBuilder
    private func field<Content: View>(
        _ label: String, @ViewBuilder content: () -> Content
    ) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .hudMono(size: 10, tracking: HudTracking.stat)
                .foregroundStyle(theme.mutedColor)
            content()
        }
    }
}

// MARK: - one conversation block

/// Block kinds → icon, speaker and voice, from the web board's own table
/// (`THREAD_BLOCK_KINDS`). An unknown kind falls back to `text` rather than
/// rendering nothing — a server that grows a sixth kind must still show it.
private struct ThreadBlockView: View {
    @Environment(\.hudTheme) private var theme

    let block: ReadBlock
    let inherited: Bool
    let tid: String

    private struct Spec {
        let icon: String
        let who: String
    }

    private static let specs: [String: Spec] = [
        "user": Spec(icon: "❯", who: "carl"),
        "text": Spec(icon: "↯", who: ""),
        "thinking": Spec(icon: "✳", who: "thinking"),
        "tool": Spec(icon: "→", who: ""),
        "teammate": Spec(icon: "✉", who: "teammate"),
    ]

    private var kind: String { Self.specs[block.kind] != nil ? block.kind : "text" }
    private var spec: Spec { Self.specs[kind] ?? Spec(icon: "↯", who: "") }

    private var speaker: String {
        if let from = block.from, !from.isEmpty { return from }
        if !spec.who.isEmpty { return spec.who }
        return kind == "text" ? "thread " + tid : kind
    }

    var body: some View {
        Group {
            if kind == "tool" {
                // Tool lines are already terse and mono — no speaker row, no
                // markdown.
                Text(spec.icon + " " + block.text)
                    .hudMono(size: 10)
                    .foregroundStyle(theme.mutedColor)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                VStack(alignment: .leading, spacing: 3) {
                    Text(spec.icon + " " + speaker)
                        .hudMono(size: 9, tracking: HudTracking.stat)
                        .foregroundStyle(kind == "user" ? theme.pollingColor : theme.mutedColor)
                    // The shared markdown surface (S4's `HudMarkdown`), so a
                    // thread turn reads exactly like the same prose does in the
                    // steer drilldown — and gets its `.textSelection` with it.
                    HudMarkdown(source: block.text, size: 11)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        // Inherited turns are the pre-fork history this thread came up WITH —
        // real context, but not this conversation, so they read one step back.
        .opacity(inherited ? 0.55 : 1)
    }
}

// MARK: - local chrome

/// `.btn-big` in the thread's own hue.
///
/// `HudButton(style: .big)` is the shared one and takes the BOARD accent; a
/// thread's two primary actions are cyan for the reason stated at the top of
/// this file, and the shared style has no per-instance tint. One local style
/// rather than a parameter added to S4's file, which this slice does not own.
private struct ThreadBigButton: View {
    @Environment(\.hudTheme) private var theme
    @Environment(\.isEnabled) private var isEnabled

    let title: String
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text(title)
                .hudDisplay(size: 9, tracking: 0.10)
                .foregroundStyle(theme.accentInk)
                .padding(.horizontal, 14)
                .padding(.vertical, 4)
                .background(theme.pollingColor)
                .hudShear(8, border: theme.pollingColor)
        }
        .buttonStyle(.plain)
        .opacity(isEnabled ? 1 : 0.45)
    }
}
