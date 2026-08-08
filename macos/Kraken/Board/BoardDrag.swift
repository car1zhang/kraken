import SwiftUI

// BoardDrag.swift — the drag session: what is in the air, what is under it,
// and the one optimistic order the board holds after a same-column drop.
//
// The gesture is POINTER-DRIVEN — a `DragGesture` in the board's own named
// coordinate space, a ghost card the screen renders at the pointer, and hit
// testing against frames the columns and cards register here — NOT an
// `NSDraggingSession`. The first build used `.onDrag`/`DropDelegate`, and the
// system machinery is built for cross-app payload transfer, not for a kanban:
// its snapshot preview, drop-denied slide-back animation and insertion timing
// each added latency the web board's dnd-kit pointer model does not have, and
// the result was called out at the manual gate as jank. A pointer drag is what
// the web board actually ships — the ghost tracks the cursor 1:1, targets
// light from plain geometry, and release resolves instantly with no system
// animation in between. (The trade dropped with NSDragging: drags can no
// longer leave the app, which the board never wanted anyway.)
//
// What stays out of this file is every RULE: `Bucketing.bucket`,
// `MoveTable.action`, `ReorderPrediction.predict`, `ReorderOverride.reconcile`
// are the contract-frozen surface (50-contract.md), and this file only decides
// WHEN to ask them.

/// One drag in flight: the card, where it came from, and where the pointer is.
struct BoardDragSession {
    let key: String
    let job: WebJob
    let sourceColumn: BoardColumn
    /// Pointer location in the board's named space, updated per gesture event.
    var location: CGPoint
    /// Pointer-to-card-origin at grab time, so the ghost holds the exact spot
    /// it was picked up by instead of snapping its corner to the cursor.
    let grabOffset: CGSize
    let cardSize: CGSize
}

/// Where a drop landed, in the vocabulary the board resolves moves in.
struct BoardDropTarget {
    let column: BoardColumn
    /// The card slot the pointer was over within `column`, or nil for the
    /// column's own body — which means "the end of the list" and is clamped at
    /// the card's current index so the gesture can only ever move a card DOWN.
    let index: Int?
}

/// The board's whole drag session, observable, plus the frame registry the
/// hit test reads. Frames are registered in `Self.spaceName` coordinates by
/// `onGeometryChange`, which re-fires on scroll — so the registry tracks the
/// viewport with no per-event bookkeeping here.
@MainActor
final class BoardDragModel: ObservableObject {
    /// The named coordinate space the WHOLE drag speaks: the gesture's events,
    /// the registered frames, and the ghost's offset. The board screen owns
    /// the `.coordinateSpace(name:)` declaration. `nonisolated` because
    /// `onGeometryChange`'s transform closure is Sendable and an immutable
    /// String needs no actor.
    nonisolated static let spaceName = "kraken-board-drag"

    @Published private(set) var session: BoardDragSession?
    /// The legal target under the pointer right now — nil over dead space, the
    /// source column with no move, or any pair `MoveTable` refuses.
    @Published private(set) var target: BoardDropTarget?

    private struct SlotFrame {
        let column: BoardColumn
        let index: Int
        var frame: CGRect
    }

    private var columnFrames: [BoardColumn: CGRect] = [:]
    private var slotFrames: [String: SlotFrame] = [:]

    var isDragging: Bool { session != nil }
    var draggedKey: String? { session?.key }
    var sourceColumn: BoardColumn? { session?.sourceColumn }
    var hoveredColumn: BoardColumn? { target?.column }

    /// The columns that outline as legal targets right now.
    var highlightTargets: Set<BoardColumn> {
        MoveTable.highlightTargets(from: session?.sourceColumn)
    }

    // MARK: - the frame registry

    func registerColumn(_ column: BoardColumn, frame: CGRect) {
        columnFrames[column] = frame
    }

    func registerSlot(key: String, column: BoardColumn, index: Int, frame: CGRect) {
        slotFrames[key] = SlotFrame(column: column, index: index, frame: frame)
    }

    /// Cards unregister on disappear so a job that left the board (archived,
    /// filtered out, done) stops being a phantom drop slot.
    func unregisterSlot(key: String) {
        slotFrames.removeValue(forKey: key)
    }

    // MARK: - the gesture

    /// Every `onChanged` event lands here. The first event for a new key
    /// BEGINS the session, seeding the grab offset from the card's registered
    /// frame; every event moves the ghost and re-resolves the target.
    func update(key: String, job: WebJob, column: BoardColumn, location: CGPoint) {
        if session?.key != key {
            let frame = slotFrames[key]?.frame ?? CGRect(origin: location, size: CGSize(width: 240, height: 80))
            session = BoardDragSession(
                key: key,
                job: job,
                sourceColumn: column,
                location: location,
                grabOffset: CGSize(width: location.x - frame.minX, height: location.y - frame.minY),
                cardSize: frame.size
            )
        }
        session?.location = location
        target = hitTest(location)
    }

    /// Resolves the release. Returns what to perform, or nil when the release
    /// was over nothing actionable; either way the session ends and every
    /// visual (ghost, dim, outlines) drops with it.
    func finishDrag() -> (target: BoardDropTarget, job: WebJob, from: BoardColumn)? {
        defer {
            session = nil
            target = nil
        }
        guard let session, let target else { return nil }
        return (target, session.job, session.sourceColumn)
    }

    func cancelDrag() {
        session = nil
        target = nil
    }

    private func hitTest(_ point: CGPoint) -> BoardDropTarget? {
        guard let from = session?.sourceColumn else { return nil }
        guard let column = columnFrames.first(where: { $0.value.contains(point) })?.key else { return nil }
        guard MoveTable.action(from: from, to: column) != nil else { return nil }
        let slot = slotFrames.values.first { $0.column == column && $0.frame.contains(point) }
        return BoardDropTarget(column: column, index: slot?.index)
    }

    // MARK: - the optimistic override

    /// The predicted order held after a same-column drop, and the board
    /// revision its POST returned at. The revision gates the reconcile: a
    /// payload no newer than the write says nothing about it, and
    /// `/api/board` memoizes for 2s, so that case is routine rather than
    /// theoretical.
    @Published private(set) var override: ReorderOverride?
    private var settledAtRevision: Int?

    func beginOverride(column: BoardColumn, slugs: [String]) {
        override = ReorderOverride(column: column, slugs: slugs)
        settledAtRevision = nil
    }

    /// The POST returned. From the NEXT payload on, the server gets a vote.
    func settleOverride(atRevision revision: Int) {
        guard override != nil else { return }
        settledAtRevision = revision
    }

    func dropOverride() {
        override = nil
        settledAtRevision = nil
    }

    /// Offers a fresh payload to the held override. Does nothing until the
    /// write has settled, and nothing for a payload no newer than the one the
    /// write settled at.
    func reconcileOverride(revision: Int, orderIn: (BoardColumn) -> [String]) {
        guard var held = override, let settled = settledAtRevision, revision > settled else { return }
        let keep = held.reconcile(serverOrder: orderIn(held.column))
        if keep {
            override = held
        } else {
            dropOverride()
        }
    }

    /// The column's jobs in whatever order the board should DRAW them —
    /// the held order when the override governs this column, the server's
    /// otherwise.
    func ordered(_ column: BoardColumn, _ jobs: [WebJob]) -> [WebJob] {
        guard let override, override.column == column else { return jobs }
        return override.apply(jobs)
    }
}
