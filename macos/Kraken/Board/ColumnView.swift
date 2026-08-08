import SwiftUI

// ColumnView.swift — one board column: a coloured head, the cards, and (for
// DONE alone) the archive drawer as a footer.
//
// The head is the kit's filled bar with a 3px left rule in the column's own
// colour, never a hairline underline — five heads, five hues, so a column is
// identifiable without reading its word. DRAFT keeps the neutral muted
// treatment; QUEUED takes `--tint`, the one hue left that separates from the
// other four; WORKING the accent, BLOCKED the counter, DONE ok.
//
// `+ NEW JOB` renders ONLY in draft, and the `·` empty placeholder renders in
// every column EXCEPT draft — a `·` under the button would read as a second,
// broken row.

struct ColumnView<Footer: View>: View {
    let column: BoardColumn
    /// The jobs to RENDER, already narrowed by the search and already in the
    /// order the drag layer's optimistic override says to draw them in.
    let jobs: [WebJob]
    /// How many the column holds BEFORE the search narrowed it. Present only
    /// while a query is active, and its presence is what switches the head
    /// count from `3` to `1/3`.
    let total: Int?
    /// Resolves a finished drag, built by the screen so the column stays free
    /// of the move table: (where it landed, what was dragged, where from).
    let perform: (BoardDropTarget, WebJob, BoardColumn) -> Void
    /// Why the search kept each visible card, keyed by jobKey. Empty when no
    /// query is running.
    let matchReasons: [String: MatchReason]
    let isHighlighted: Bool
    let isHovered: Bool
    @ViewBuilder var footer: Footer

    @Environment(\.hudTheme) private var theme
    @EnvironmentObject private var router: OverlayRouter
    @EnvironmentObject private var drag: BoardDragModel

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            head
                .padding(.bottom, 10)
            cards
            footer
        }
        .frame(minWidth: 200, maxWidth: .infinity, alignment: .top)
    }

    // MARK: - head

    private var head: some View {
        HStack {
            Text(column.headLabel)
                .hudDisplay(size: 10, tracking: 0.2)
            Spacer(minLength: 0)
            Text(total == nil ? "\(jobs.count)" : "\(jobs.count)/\(total ?? 0)")
                .hudMono(size: 10)
        }
        .foregroundStyle(headColor)
        .padding(.horizontal, 8)
        .padding(.vertical, 5)
        .background(headFill)
        .overlay(alignment: .leading) { Rectangle().fill(headColor).frame(width: 3) }
    }

    private var headColor: Color {
        switch column {
        case .queued: return theme.tintColor
        case .working: return theme.accentColor
        case .blocked: return theme.counterColor
        case .done: return theme.okColor
        default: return theme.mutedColor
        }
    }

    private var headFill: Color {
        switch column {
        case .queued: return HudTheme.mix(theme.tint, 10, over: theme.b1).color
        case .working: return HudTheme.mix(theme.ac, 13, over: theme.b1).color
        case .blocked: return HudTheme.mix(theme.ac2, 12, over: theme.b1).color
        case .done: return HudTheme.mix(theme.ok, 10, over: theme.b1).color
        default: return HudTheme.mix(theme.ac, 7, over: theme.b1).color
        }
    }

    // MARK: - cards

    private var cards: some View {
        VStack(alignment: .leading, spacing: 8) {
            if column == .draft {
                Button { router.openEditor(jobKey: nil) } label: {
                    Text("+ NEW JOB")
                        .hudDisplay(size: 10, tracking: 0.12)
                        .foregroundStyle(theme.accentColor)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 10)
                        .overlay {
                            Rectangle().strokeBorder(
                                theme.ac.color(0.31),
                                style: StrokeStyle(lineWidth: 1, dash: [4, 3])
                            )
                        }
                }
                .buttonStyle(.plain)
                .hudCursor(.actable)
            }
            if jobs.isEmpty {
                if column != .draft {
                    Text("·")
                        .hudMono(size: 10)
                        .foregroundStyle(theme.mutedColor)
                        .opacity(0.4)
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 14)
                }
            } else {
                ForEach(Array(jobs.enumerated()), id: \.element.id) { index, job in
                    let key = JobKey.key(job)
                    JobCardView(job: job, column: column, matchReason: matchReasons[key])
                        .opacity(drag.draggedKey == key ? 0.35 : 1)
                        // Every card registers its frame in the drag space —
                        // that registry IS the drop hit test (a pointer over a
                        // card lands on its slot; the gaps and the padding
                        // fall through to the column, which means "the end").
                        // `onGeometryChange` re-fires on scroll, so the
                        // registry follows the viewport.
                        .onGeometryChange(for: CGRect.self) {
                            $0.frame(in: .named(BoardDragModel.spaceName))
                        } action: { frame in
                            drag.registerSlot(key: key, column: column, index: index, frame: frame)
                        }
                        .onDisappear { drag.unregisterSlot(key: key) }
                        // The pointer drag. 4pt of movement starts it (clicks
                        // stay clicks — the card's own tap gesture wins a
                        // stationary release); the ghost and the target
                        // resolution live on the model, and the screen renders
                        // the ghost.
                        .gesture(
                            DragGesture(minimumDistance: 4, coordinateSpace: .named(BoardDragModel.spaceName))
                                .onChanged { value in
                                    drag.update(key: key, job: job, column: column, location: value.location)
                                }
                                .onEnded { _ in
                                    if let drop = drag.finishDrag() {
                                        perform(drop.target, drop.job, drop.from)
                                    }
                                }
                        )
                }
            }
        }
        .frame(maxWidth: .infinity, minHeight: 60, alignment: .top)
        .padding(4)
        .background(dropWash)
        .overlay {
            if isHighlighted {
                Rectangle().strokeBorder(
                    theme.accentColor,
                    style: StrokeStyle(lineWidth: 1, dash: [3, 3])
                )
            }
        }
        .contentShape(Rectangle())
        // The cards area is the column's registered drop region: a pointer
        // inside it but on no card slot resolves to (column, index: nil) —
        // "the end of the list". Deliberately the CARDS area, not the whole
        // column, so the head is dead space exactly as on the web.
        .onGeometryChange(for: CGRect.self) {
            $0.frame(in: .named(BoardDragModel.spaceName))
        } action: { frame in
            drag.registerColumn(column, frame: frame)
        }
    }

    private var dropWash: Color {
        if isHovered { return theme.dropTintHover }
        if isHighlighted { return theme.dropTint }
        return .clear
    }
}
