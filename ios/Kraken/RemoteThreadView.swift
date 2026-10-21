import SwiftUI

// One side thread of a REMOTE card (a job another Mac owns), read through the
// paired node's relay (`GET /api/nodes/thread/read`) and written through its
// action proxy (`POST /api/nodes/action`, action `thread-send`). Read-mostly
// by design: no kill, revive, rename or terminal — those are the owner's —
// and a composer only for a live ask thread, the one kind any node that can
// see the card may send into. A pane thread is the owner's alone.

struct RemoteThreadView: View {
    let thread: WebThread
    let slug: String
    /// The owner's node id, as `WebJobOwner.nodeID`.
    let node: String
    let baseURL: String
    /// Whether the owner's node is up; the composer is disabled when it isn't.
    var ownerOnline: Bool = true

    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.boardNodeId) private var boardNodeId
    // See ThreadDetailView's identical property: keeps the presenting job
    // sheet from auto-dismissing while this view is pushed on it.
    @Environment(SheetNavState.self) private var sheetNav: SheetNavState?
    @FocusState private var messageFocused: Bool
    @State private var read: ThreadReadResponse?
    @State private var loadError: String?
    @State private var messageText = ""
    @State private var isSending = false
    @State private var sendError: String?
    @State private var sentToast = false

    private static let bottomAnchorID = "bottom"
    private static let pollEvery: Duration = .seconds(5)

    private var blocks: [ReadBlock] { read?.blocks ?? [] }
    // Before the first read lands, the board's snapshot is all there is.
    private var isLive: Bool { read?.live ?? thread.isLive }
    // "headless" is the ask thread's kind value (`role: "ask"` is a separate,
    // orthogonal field on this wire).
    private var isHeadless: Bool { (read?.kind ?? thread.kind) == "headless" }
    private var isAnswering: Bool { read.map { $0.answering ?? false } ?? (thread.answering == true) }
    private var note: String { read.map { $0.note ?? "" } ?? (thread.note ?? "") }
    private var showsComposer: Bool { isHeadless && isLive }
    private var trimmedMessage: String {
        messageText.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(spacing: 0) {
                        transcript
                            .frame(maxWidth: .infinity, alignment: .leading)
                        Color.clear.frame(height: 1).id(Self.bottomAnchorID)
                    }
                }
                .onChange(of: blocks.count) { oldCount, newCount in
                    guard newCount > oldCount else { return }
                    withAnimation { proxy.scrollTo(Self.bottomAnchorID, anchor: .bottom) }
                }
            }
            if showsComposer {
                composer
            }
        }
        .background(ThemeBackgroundView())
        .navigationTitle(thread.displayTitle)
        .navigationBarTitleDisplayMode(.inline)
        .onAppear { sheetNav?.childPushed = true }
        .onDisappear { sheetNav?.childPushed = false }
        .overlay(alignment: .top) {
            if sentToast {
                Text("sent")
                    .font(.data(11))
                    .foregroundStyle(KTheme.accentInk)
                    .padding(.horizontal, 14).padding(.vertical, 6)
                    .background(KTheme.accent, in: Capsule())
                    .padding(.top, 8)
            }
        }
        .task(id: scenePhase) {
            guard scenePhase == .active else { return }
            await load()
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.pollEvery)
                if Task.isCancelled { break }
                await load()
            }
        }
    }

    private func load() async {
        do {
            read = try await APIClient.shared.fetchNodeThreadRead(
                baseURL: baseURL, node: node, slug: slug, tid: thread.tid, n: 30)
            loadError = nil
        } catch {
            loadError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                let stateColor = isLive ? KTheme.solved : KTheme.stopped
                HStack(spacing: 6) {
                    Circle()
                        .fill(stateColor)
                        .frame(width: 7, height: 7)
                    Text(isLive ? "live" : "closed")
                        .font(.data(11))
                        .foregroundStyle(stateColor)
                }
                .padding(.horizontal, 9)
                .padding(.vertical, 4)
                .background(stateColor.opacity(0.13), in: Capsule())
                Text(thread.tid)
                    .font(.data(11))
                    .foregroundStyle(KTheme.accent)
                if let model = thread.model, !model.isEmpty {
                    Text(model).font(.data(11)).foregroundStyle(KTheme.text2)
                }
                Spacer()
            }
            ThreadAskStatusRow(
                badge: threadOriginBadge(thread, boardNodeId: boardNodeId),
                answering: isAnswering, note: note
            )
        }
        .padding(.horizontal)
        .padding(.vertical, 7)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.regularMaterial)
    }

    // The first block the thread produced itself (the first not marked
    // inherited) — where the "⑂ branched here" divider goes; nil when nothing
    // is inherited, `blocks.count` when everything is.
    private var forkIndex: Int? {
        guard blocks.contains(where: { $0.inherited == true }) else { return nil }
        return blocks.firstIndex(where: { $0.inherited != true }) ?? blocks.count
    }

    private var forkDivider: some View {
        let uuid = read?.forkUUID ?? ""
        return HStack(spacing: 6) {
            Rectangle().fill(KTheme.hairline2).frame(height: 1)
            Text(uuid.isEmpty ? "⑂ branched here" : "⑂ branched here · \(uuid)")
                .font(.data(10, weight: .semibold))
                .foregroundStyle(KTheme.accent)
                .lineLimit(1)
                .truncationMode(.middle)
            Rectangle().fill(KTheme.hairline2).frame(height: 1)
        }
        .padding(.vertical, 4)
    }

    private var transcript: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let loadError {
                Text(loadError).font(.voice(12)).foregroundStyle(KTheme.counter)
            }
            if let read, blocks.isEmpty, loadError == nil {
                Text(read.source == "none"
                     ? "nothing said in this thread yet"
                     : "nothing to show in this thread's last turns")
                    .font(.data(11))
                    .foregroundStyle(KTheme.text2)
            }
            ForEach(Array(blocks.enumerated()), id: \.offset) { index, block in
                if index == forkIndex {
                    forkDivider
                }
                TranscriptBlockView(block: block)
                    .opacity(block.inherited == true ? 0.55 : 1)
            }
            if forkIndex == blocks.count {
                forkDivider
            }
        }
        .padding()
    }

    private var composer: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let sendError {
                Text(sendError).font(.voice(11)).foregroundStyle(KTheme.counter)
            }
            HStack {
                TextField(ownerOnline ? "ask again…" : "offline", text: $messageText, axis: .vertical)
                    .lineLimit(1...6)
                    .focused($messageFocused)
                    .font(.voice(13))
                    .kField(focused: messageFocused)
                Button {
                    send()
                } label: {
                    Label("Send", systemImage: "paperplane.fill")
                }
                .buttonStyle(.glassProminent)
                .labelStyle(.iconOnly)
                .accessibilityLabel("Send")
                .disabled(isSending || trimmedMessage.isEmpty)
            }
        }
        .padding()
        .background(.regularMaterial)
        // Visible but disabled while a question is in flight: the owner
        // refuses a second send then (409 "thread is already answering").
        .disabled(!ownerOnline || isAnswering)
    }

    private func send() {
        let text = trimmedMessage
        guard !text.isEmpty else { return }
        isSending = true
        sendError = nil
        Task {
            do {
                try await APIClient.shared.nodeAction(
                    baseURL: baseURL, node: node, slug: slug, action: "thread-send",
                    payload: NodeActionPayload(text: text, tid: thread.tid))
                isSending = false
                // A failed send leaves the typed text in place; a sent one
                // clears it and re-reads so the answering pill shows.
                messageText = ""
                flashSent()
                await load()
            } catch {
                isSending = false
                sendError = (error as? KrakenError)?.errorDescription ?? error.localizedDescription
            }
        }
    }

    private func flashSent() {
        sentToast = true
        Task {
            try? await Task.sleep(for: .seconds(2))
            sentToast = false
        }
    }
}
