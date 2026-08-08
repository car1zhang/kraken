import Foundation

// OverlayStack.swift — the Escape chain, as data rather than as an if/else.
//
// The web board's `useEscapeStack.ts` is the source: five overlays, each
// registering a PRIORITY, and one press closing exactly the highest open one.
// The priorities are ranks rather than z-indexes, but they deliberately agree
// with style.css's stacking (`#overlay` 30 / `#thread-overlay` 25 / the rest
// 20) so "topmost visually" and "closes first" cannot disagree.
//
// Two things this file is deliberately NOT:
//
//   - it is not `@MainActor`. The contract freezes a plain `init()` and the
//     blind tests construct one directly; an actor-isolated type would make
//     every one of those constructions a concurrency diagnostic rather than a
//     test.
//   - it is not a view model. It holds open/closed state and nothing else —
//     no payloads, no SwiftUI publishing. `OverlayRouter` owns both of those
//     and DRIVES this, which is what keeps the ordering rule testable without
//     a window.

/// The five in-window overlay layers. The raw values ARE the Escape
/// priorities — one number, so a layer cannot rank differently in the two
/// stacks (Escape and ⌘V) that read it.
enum OverlayKind: Int, Comparable, CaseIterable {
    case usage = 10
    case editor = 20
    case steer = 30
    case thread = 40
    case artifact = 50

    static func < (lhs: OverlayKind, rhs: OverlayKind) -> Bool {
        lhs.rawValue < rhs.rawValue
    }
}

/// Which overlays are open, and which one an Escape press belongs to.
///
/// Each kind tracks open/closed INDEPENDENTLY: opening the artifact viewer
/// from inside a thread replaces nothing, because on the web both really are
/// open at once (the thread's panel is still behind the artifact's) and
/// closing the artifact has to reveal it rather than land on the board.
final class OverlayStack {
    private var openKinds: Set<OverlayKind> = []

    init() {}

    func open(_ k: OverlayKind) {
        openKinds.insert(k)
    }

    func close(_ k: OverlayKind) {
        openKinds.remove(k)
    }

    /// Whether one layer is open. Not on the frozen surface, but every
    /// renderer needs it and deriving it from `top` would be wrong the moment
    /// two layers are open.
    func isOpen(_ k: OverlayKind) -> Bool {
        openKinds.contains(k)
    }

    /// Every open layer, for a renderer that has to paint them in order.
    var openLayers: [OverlayKind] {
        openKinds.sorted()
    }

    /// The highest-priority open layer — the one an Escape press is for.
    var top: OverlayKind? {
        openKinds.max()
    }

    /// Closes exactly the topmost open layer and says which it was. nil (and
    /// nothing closed) when none are open, so a caller can pass the press on
    /// rather than swallowing it — which is what keeps Escape working for
    /// AppKit's own uses when the board is bare.
    @discardableResult
    func handleEscape() -> OverlayKind? {
        guard let t = top else { return nil }
        close(t)
        return t
    }
}
