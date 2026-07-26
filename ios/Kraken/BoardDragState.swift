import Foundation

/// All in-flight drag state for the board, in one value.
///
/// A drag on the board can end four ways: dropped on another card (a
/// within-column reorder), dropped on an overview-strip cell (a lifecycle
/// action), dropped on a strip cell whose (source, target) pair is illegal,
/// or — the case that has no callback at all — released over anything that
/// is not a registered drop target: empty space below the last card, the
/// column header, the page-indicator dots, the nav bar. SwiftUI hands a
/// `.onDrag` source no drag-end event, so the first two are the only exits
/// the board ever heard about, and the last two used to leave every drag
/// visual (the source card's 40% dim, the accent outline on legal strip
/// cells, the optimistic `localOrder` overlay) rendering a gesture that was
/// already over. `NSItemProvider.dragEnding` below supplies the missing
/// signal; this type is what it resets.
///
/// **The generation token.** Every mutation that leaves a drag — `begin`,
/// `endActive`, and a winning `end` — bumps `generation`. `end(generation:)`
/// mutates only when its argument still matches, so a drag-end arriving
/// after its drag already finished (the item provider deallocating a beat
/// late, possibly after carl has started a *second* drag) is a no-op rather
/// than something that blanks a gesture still in progress.
struct BoardDragState: Equatable {
    /// Bumped by every transition out of a live drag. Starts at 0, so the
    /// first `begin` returns 1 and no caller can hold a "generation 0"
    /// token that accidentally matches a fresh state.
    private(set) var generation = 0
    /// The slug of the card being dragged; `nil` when no drag is in flight.
    private(set) var slug: String?
    /// The column the drag started in — drives which strip cells outline as
    /// legal targets, and which column's optimistic overlay is at risk.
    private(set) var sourceColumn: BoardColumn?
    /// The strip cell currently under the finger, if it is a legal target.
    private(set) var hoveredColumn: BoardColumn?

    var isActive: Bool { slug != nil }

    /// Starts a drag and returns the generation token identifying it. The
    /// caller hands that token to `end(generation:)` later; it is the only
    /// thing that distinguishes this drag from the next one.
    @discardableResult
    mutating func begin(slug: String, column: BoardColumn) -> Int {
        generation += 1
        self.slug = slug
        sourceColumn = column
        hoveredColumn = nil
        return generation
    }

    /// Records (or clears) the strip cell under the finger. Ignored when no
    /// drag is in flight, so a stray `dropEntered` can't light up a cell
    /// with nothing being dragged.
    mutating func hover(_ column: BoardColumn?) {
        guard isActive else { return }
        hoveredColumn = column
    }

    /// Ends whatever drag is live, whether or not the caller knows its
    /// generation — the exit used by the two `performDrop` paths, which by
    /// definition are handling the current drag.
    mutating func endActive() {
        guard isActive else { return }
        generation += 1
        slug = nil
        sourceColumn = nil
        hoveredColumn = nil
    }

    /// Ends the drag identified by `generation`, and reports whether it did.
    /// A stale token — the drag already ended, by a drop or by an earlier
    /// drag-end — returns `false` and mutates nothing.
    mutating func end(generation: Int) -> Bool {
        guard isActive, generation == self.generation else { return false }
        endActive()
        return true
    }
}

/// Fires `onEnd` when the last reference to it goes away — nothing more.
/// Reaching an object's death is the whole mechanism; see
/// `NSItemProvider.dragEnding(slug:onEnd:)` below for what holds one.
private final class DragEndSentinel {
    private let onEnd: @MainActor @Sendable () -> Void

    init(onEnd: @escaping @MainActor @Sendable () -> Void) {
        self.onEnd = onEnd
    }

    deinit {
        // Copied to a local first: the closure has to outlive the
        // deallocating instance that stores it. Always async, never a direct
        // call even when deinit already happens to be on the main thread — a
        // release landing inside a SwiftUI update pass would otherwise mutate
        // @State mid-render. `assumeIsolated` rather than `Task { @MainActor }`
        // because the queue hop is what orders this against that update pass;
        // an unstructured task would give the compiler its proof and take
        // away the ordering.
        let end = onEnd
        DispatchQueue.main.async { MainActor.assumeIsolated { end() } }
    }
}

/// The private type identifier the sentinel is registered under. Nothing
/// ever loads it: the registration exists purely so the item provider owns a
/// reference to the sentinel. `.ownProcess` keeps it off the pasteboard and
/// out of any other app's view of the drag.
private let dragLifetimeTypeIdentifier = "dev.carl.Kraken.drag-lifetime"

extension NSItemProvider {
    /// An item provider that reports when the drag session carrying it ends.
    ///
    /// UIKit holds the item provider for exactly the lifetime of the drag
    /// session and releases it when the session finishes — drop, cancel, or
    /// release over nothing at all. Observing that release is the only
    /// drag-end signal available to a SwiftUI `.onDrag` source, and it is the
    /// iOS twin of the `document`-level `dragend` listener the web board has
    /// always had (`web/static/app.js`).
    ///
    /// The signal is carried by a sentinel captured inside a registered load
    /// handler rather than by subclassing `NSItemProvider` and overriding
    /// `deinit`. That is deliberate. `NSItemProvider` is `NSCopying`, and a
    /// subclass is fragile in both directions if anything in the drag
    /// pipeline copies rather than retains it: its `NSCopying` implementation
    /// allocates via `init()`, which a subclass storing a non-optional
    /// closure does not inherit — an uncatchable runtime trap, verified, not
    /// theorised — and even with that plugged, a copy would carry no callback
    /// while the original's release fired one early, killing a drag that had
    /// only just begun. A copy of an item provider copies its registrations,
    /// so a sentinel reached through one is retained by every copy and dies
    /// only once they all do. Copies extend the signal instead of breaking it.
    ///
    /// `onEnd` arrives on the main queue, and can arrive long after the drag
    /// it belonged to finished — which is why `BoardDragState` hands out a
    /// generation token rather than trusting the callback to be timely.
    static func dragEnding(slug: String, onEnd: @escaping @MainActor @Sendable () -> Void) -> NSItemProvider {
        let provider = NSItemProvider(object: slug as NSString)
        let sentinel = DragEndSentinel(onEnd: onEnd)
        provider.registerDataRepresentation(
            forTypeIdentifier: dragLifetimeTypeIdentifier,
            visibility: .ownProcess
        ) { completion in
            // Capturing the sentinel IS the registration's purpose; the data
            // it vends is empty and never asked for.
            withExtendedLifetime(sentinel) { completion(Data(), nil) }
            return nil
        }
        return provider
    }
}
