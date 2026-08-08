import SwiftUI

// BoardScreen.swift — the board: a search strip over five columns, with the
// archive drawer under DONE.
//
// It owns the three things no column can own alone:
//
//   1. ONE bucketing, built here and handed down, so the column a card is
//      DRAWN in is by construction the column a drag reports it was dragged
//      FROM. Two bucketings is a real bug rather than a duplication: `health`
//      relocates a `working` job into BLOCKED, so an independent drop layer
//      resolves working→blocked for a gesture that looks like a reorder and
//      posts `/api/stop`, interrupting the agent.
//   2. The drop resolution — `MoveTable.action` asked once, on that same
//      bucketing.
//   3. The optimistic reorder: predict, hold, settle, and revert.
//
// The search narrows what each column RENDERS. It never reaches the reorder
// body, which is always built from the whole column — posting the visible
// subset of a filtered column would send every hidden card to the top of it
// permanently, with nothing on screen saying so.

struct BoardScreen: View {
    @EnvironmentObject private var store: BoardStore
    @EnvironmentObject private var router: OverlayRouter
    @Environment(\.hudTheme) private var theme

    @StateObject private var drag = BoardDragModel()
    @State private var search = BoardSearch()
    /// The board viewport's width, for the responsive column wrap. Zero until
    /// the first geometry pass; the wrap treats zero as "all columns".
    @State private var boardWidth: CGFloat = 0

    /// Columns per row at the current width — the web's breakpoints.
    private var columnsPerRow: Int {
        if boardWidth <= 0 { return BoardColumn.live.count }
        if boardWidth <= 700 { return 1 }
        if boardWidth <= 1200 { return 3 }
        return BoardColumn.live.count
    }

    /// `BoardColumn.live` chunked into display rows of `columnsPerRow`.
    private var columnRows: [[BoardColumn]] {
        let per = columnsPerRow
        return stride(from: 0, to: BoardColumn.live.count, by: per).map {
            Array(BoardColumn.live[$0..<min($0 + per, BoardColumn.live.count)])
        }
    }

    private var jobs: [WebJob] { store.board?.jobs ?? [] }

    var body: some View {
        let buckets = self.buckets
        VStack(spacing: 0) {
            BoardBar(
                search: $search,
                total: jobs.count,
                matched: search.visibleKeys?.count ?? jobs.count,
                overlayOpen: router.hasOpenOverlay
            )
            ScrollView(.vertical) {
                // The web's responsive grid (style.css :2224-2225): whole
                // COLUMNS wrap onto rows below as the window narrows —
                // ≤700pt one per row, ≤1200pt three, else all five. Clear
                // fillers pad the last row so its column tracks keep the same
                // width as full rows (the CSS grid keeps its track count).
                VStack(alignment: .leading, spacing: 12) {
                    ForEach(Array(columnRows.enumerated()), id: \.offset) { _, row in
                        HStack(alignment: .top, spacing: 12) {
                            ForEach(row, id: \.self) { column in
                                columnView(column, buckets: buckets)
                            }
                            ForEach(0..<(columnsPerRow - row.count), id: \.self) { _ in
                                Color.clear.frame(minWidth: 200, maxWidth: .infinity, maxHeight: 1)
                            }
                        }
                    }
                }
                .padding(EdgeInsets(top: 14, leading: 16, bottom: 20, trailing: 16))
            }
            .onGeometryChange(for: CGFloat.self) { $0.size.width } action: { boardWidth = $0 }
        }
        // The drag's one coordinate space: gesture events, registered frames
        // and the ghost's offset all speak it, so nothing converts anything.
        .coordinateSpace(name: BoardDragModel.spaceName)
        // The ghost — the REAL card, full opacity, tracking the pointer 1:1
        // from the exact point it was grabbed at, hit-testing nothing. This is
        // the web board's drag overlay (the dimmed in-column card is the
        // placeholder; this is the thing that moves).
        .overlay(alignment: .topLeading) {
            if let session = drag.session {
                JobCardView(job: session.job, column: session.sourceColumn, matchReason: nil)
                    .frame(width: session.cardSize.width)
                    .allowsHitTesting(false)
                    .offset(
                        x: session.location.x - session.grabOffset.width,
                        y: session.location.y - session.grabOffset.height
                    )
            }
        }
        .background(theme.groundColor)
        .hudCursor(.rest)
        .environmentObject(drag)
        .onAppear {
            store.start()
            search.refresh(jobs: jobs)
        }
        // The two triggers that can change what the filter sees: a new payload
        // and a new query.
        .onChange(of: store.revision) {
            search.refresh(jobs: jobs)
            drag.reconcileOverride(revision: store.revision) { serverOrder($0) }
        }
        .onChange(of: search.query) { search.refresh(jobs: jobs) }
        // Poll suppression: the board must not rebuild under the pointer, and
        // the drop is what makes it refetch.
        .onChange(of: drag.isDragging) {
            if drag.isDragging {
                store.pausePolling()
            } else {
                store.resumePolling()
            }
        }
    }

    // MARK: - the one bucketing

    private struct Buckets {
        var byColumn: [BoardColumn: [WebJob]] = [:]
        var archived: [WebJob] = []
        var byKey: [String: WebJob] = [:]
    }

    private var buckets: Buckets {
        var out = Buckets()
        for column in BoardColumn.live { out.byColumn[column] = [] }
        for job in jobs {
            out.byKey[JobKey.key(job)] = job
            let column = Bucketing.bucket(job)
            if column == .archived {
                out.archived.append(job)
            } else {
                out.byColumn[column, default: []].append(job)
            }
        }
        return out
    }

    /// The column's slug order as the SERVER last sent it — what the held
    /// override is reconciled against.
    private func serverOrder(_ column: BoardColumn) -> [String] {
        (buckets.byColumn[column] ?? []).map(\.slug)
    }

    // MARK: - one column

    @ViewBuilder
    private func columnView(_ column: BoardColumn, buckets: Buckets) -> some View {
        let whole = drag.ordered(column, buckets.byColumn[column] ?? [])
        let shown = search.visible(whole)
        ColumnView(
            column: column,
            jobs: shown,
            total: search.active ? whole.count : nil,
            perform: { target, job, from in handleDrop(target, job: job, from: from) },
            matchReasons: search.reasons,
            isHighlighted: highlighted(column),
            isHovered: drag.hoveredColumn == column && highlighted(column)
        ) {
            if column == .done {
                ArchiveDrawerView(
                    archived: search.visible(buckets.archived),
                    total: search.active ? buckets.archived.count : nil,
                    // A search reaches an archived job ONLY through this: with
                    // no status filter, a match inside a collapsed drawer would
                    // otherwise be counted in the readout and invisible.
                    forceOpen: search.active && !search.visible(buckets.archived).isEmpty,
                    isHighlighted: highlighted(.archived),
                    isHovered: drag.hoveredColumn == .archived && highlighted(.archived)
                )
            }
        }
    }

    private func highlighted(_ column: BoardColumn) -> Bool {
        drag.isDragging && drag.highlightTargets.contains(column)
    }

    // MARK: - drops

    /// Resolves one finished drag through `MoveTable`, on the SAME bucketing
    /// the render used. An unlisted pair is a NO-OP — never a "close enough"
    /// fallback. The session is already over when this runs (`finishDrag`
    /// cleared it), so the payload arrives as arguments rather than being read
    /// back off the model.
    private func handleDrop(_ target: BoardDropTarget, job: WebJob, from: BoardColumn) {
        let key = JobKey.key(job)
        guard let action = MoveTable.action(from: from, to: target.column) else { return }

        switch action {
        case .post(let endpoint):
            // `text: ""` is the wire's "reactivate without steering" — the
            // done→blocked drag is the ONE reactivate that is a bare POST.
            var body: [String: Any] = ["slug": job.slug]
            if endpoint == "/api/reactivate" { body["text"] = "" }
            Task { await store.mutate(path: endpoint, body: body) }
        case .steer(let mode, let requireText):
            // NO network write on this path. The drag only chooses the
            // composer's mode; the typed message is what eventually moves the
            // job, or does not. `requireText` rides through S4's overload of
            // the seam — the done→working drop is the one caller that sets it,
            // which is exactly the caller the overload exists for.
            router.openSteer(slug: job.slug, mode: mode, requireText: requireText)
        case .reorder:
            Task { await commitReorder(column: from, key: key, index: target.index) }
        }
    }

    /// The board's one optimistic path.
    private func commitReorder(column: BoardColumn, key: String, index: Int?) async {
        // The WHOLE column, never the filtered list.
        let ordered = drag.ordered(column, buckets.byColumn[column] ?? [])
        guard let oldIndex = ordered.firstIndex(where: { JobKey.key($0) == key }) else {
            await store.refetch()
            return
        }
        // A drop on the column's own body means the END of the list — which
        // under a search is the last card ON SCREEN, clamped at the card's own
        // index so a "put this at the end" gesture can only ever move a card
        // DOWN. The clamp makes that structural rather than an argument about
        // which render produced the visible set.
        let newIndex = index ?? max(oldIndex, lastVisibleIndex(ordered))
        // A 4px twitch, or a drop back into the same slot: the rendered order
        // is already the server's, so there is nothing to write.
        if oldIndex == newIndex { return }

        let slugs = ordered.map(\.slug)
        guard let predicted = ReorderPrediction.predict(
            slugs: slugs, dragged: ordered[oldIndex].slug, dropIndex: newIndex
        ) else {
            // The client's picture no longer lines up with what it is about to
            // post. Predicting from a stale picture is worse than a repaint,
            // so this one drop goes down the pessimistic path.
            await store.mutate(path: "/api/reorder", body: ["column": column.rawValue, "slugs": slugs])
            return
        }

        drag.beginOverride(column: column, slugs: predicted)
        let failure = await store.post(
            path: "/api/reorder",
            body: ["column": column.rawValue, "slugs": predicted]
        )
        if let failure {
            drag.dropOverride()
            router.toast("reorder failed: \(failure) — reverted")
            await store.refetch()
            return
        }
        // Deliberately NOT a refetch: the board already shows the final order.
        drag.settleOverride(atRevision: store.revision)
    }

    /// The index, in the WHOLE column, of the last card the board is drawing.
    /// `count - 1` when nothing is filtered — and also when a filter somehow
    /// hides every card in this column, which a real drag cannot produce.
    private func lastVisibleIndex(_ ordered: [WebJob]) -> Int {
        guard let keys = search.visibleKeys else { return ordered.count - 1 }
        for index in stride(from: ordered.count - 1, through: 0, by: -1)
        where keys.contains(JobKey.key(ordered[index])) {
            return index
        }
        return ordered.count - 1
    }
}
