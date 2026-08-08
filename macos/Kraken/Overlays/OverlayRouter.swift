import SwiftUI

// OverlayRouter.swift — the cross-slice seam every other slice opens an
// overlay through (50-contract.md § Cross-slice seams).
//
// The web board keeps its overlay state as flat one-field-per-overlay App
// state (App.tsx:103-122), deliberately stackable. This is that, plus the
// two DOCUMENT-level registries the vanilla board also carries: the Escape
// chain (here, `OverlayStack`) and the ⌘V paste chain. Both are ordered by
// the same `OverlayKind` rank, so "closes first" and "receives the paste
// first" cannot disagree — that agreement is the whole reason the two live
// in one object rather than in the two overlays that use them.
//
// Injected as an `@EnvironmentObject` from the root (S6 owns the wiring).

/// What the steer inspector was opened FOR. `requireText` is set only by a
/// done→working drag; every other entry point leaves reactivate's message
/// optional.
struct SteerTarget: Equatable {
    var slug: String
    var mode: SteerMode
    var requireText: Bool
}

/// The editor opens either on a job (by jobKey — NEVER a bare slug, or an
/// archived job and an active one that reused its freed slug would open each
/// other's form) or on the new-job row.
enum EditorTarget: Equatable {
    case newJob
    case job(key: String)
}

struct ArtifactTarget: Equatable {
    var url: String
    /// nil degrades the viewer's pager to a singleton — see 50-contract S5.
    var jobKey: String?
}

struct ThreadTarget: Equatable {
    var slug: String
    /// The tid EXACTLY as the wire spells it — `"t7"`, not `7`. nil = the ⑂
    /// branch dialog rather than an existing thread.
    ///
    /// This was `Int?` in the first cut of the seam and was amended at the
    /// join, after S4 and S5 independently flagged the same defect: a tid is
    /// minted as `"t" + N` (`validateThreadID`, internal/store/threads.go), so
    /// an `Int` spelling drops the prefix and every caller has to re-attach it.
    /// Carrying the string means there is no conversion left to get wrong, and
    /// no shape the router can fail to carry — which matters because nil MEANS
    /// branch mode here, so a tid that failed to convert used to risk silently
    /// starting a new thread instead of opening the one that was clicked. An
    /// unrecognised tid now rides verbatim and the SERVER is the validator,
    /// which is the only place that can be one.
    var tid: String?
}

@MainActor
final class OverlayRouter: ObservableObject {
    /// The Escape chain this router drives. Public because S5/S6 layers ask it
    /// what is open; mutated only through the `open`/`close` methods below, so
    /// the published mirror can never fall out of step with it.
    let stack = OverlayStack()

    /// The mirror SwiftUI actually observes. `OverlayStack` is a plain class
    /// by contract (the blind tests construct one), so it publishes nothing —
    /// every mutation here re-reads it into this set.
    @Published private(set) var openLayers: Set<OverlayKind> = []

    @Published private(set) var steerTarget: SteerTarget?
    @Published private(set) var editorTarget: EditorTarget?
    @Published private(set) var artifactTarget: ArtifactTarget?
    @Published private(set) var threadTarget: ThreadTarget?

    // MARK: - the seam

    /// Frozen signature (50-contract § Cross-slice seams). Text is optional in
    /// reactivate mode unless the caller says otherwise — see the overload.
    func openSteer(slug: String, mode: SteerMode) {
        openSteer(slug: slug, mode: mode, requireText: false)
    }

    /// The drag layer's entry point: a done→working drop opens reactivate with
    /// the message REQUIRED (`MoveTable.action`'s `steer(.reactivate,
    /// requireText: true)`), which is the one caller that needs this.
    func openSteer(slug: String, mode: SteerMode, requireText: Bool) {
        steerTarget = SteerTarget(slug: slug, mode: mode, requireText: requireText)
        open(.steer)
    }

    /// nil = the new-job row.
    func openEditor(jobKey: String?) {
        editorTarget = jobKey.map { EditorTarget.job(key: $0) } ?? .newJob
        open(.editor)
    }

    func openArtifact(url: String, jobKey: String?) {
        artifactTarget = ArtifactTarget(url: url, jobKey: jobKey)
        open(.artifact)
    }

    /// nil `tid` = branch mode; otherwise the wire's own `"t<digits>"`.
    func openThread(slug: String, tid: String?) {
        threadTarget = ThreadTarget(slug: slug, tid: tid)
        open(.thread)
    }

    func openUsage() {
        open(.usage)
    }

    /// The transient bottom-centre notice. One message at a time; a second
    /// replaces the first and restarts its timer.
    func toast(_ message: String) {
        HudToast.shared.show(message)
    }

    // MARK: - open / close

    func open(_ kind: OverlayKind) {
        stack.open(kind)
        sync()
    }

    func close(_ kind: OverlayKind) {
        stack.close(kind)
        // The payload goes with the layer: an overlay's children unmount while
        // it is closed on the web, which is how "open resets everything" and
        // "polling stops on close" fall out rather than being a dozen
        // imperative resets. Clearing the target is the same statement here —
        // the SwiftUI view for a layer is built only from a non-nil target.
        switch kind {
        case .steer: steerTarget = nil
        case .editor: editorTarget = nil
        case .artifact: artifactTarget = nil
        case .thread: threadTarget = nil
        case .usage: break
        }
        sync()
    }

    func isOpen(_ kind: OverlayKind) -> Bool {
        openLayers.contains(kind)
    }

    /// Any overlay at all — the board's ⌘K handler suppresses itself while one
    /// is up (R2 §6), and the chrome reads it for the same reason.
    var hasOpenOverlay: Bool { !openLayers.isEmpty }

    private func sync() {
        openLayers = Set(stack.openLayers)
    }

    // MARK: - Escape

    // A LOCAL escape claim: a control that must handle Escape itself and never
    // let it reach the overlay chain. Three exist on the web — the board's
    // search field, the thread rename input, and the colour menu — and the
    // third one is the reason this is a claim STACK rather than a boolean: on
    // the web the colour menu wins by listening in the CAPTURE phase, which is
    // a DOM mechanism with no AppKit equivalent. A single `NSEvent` local
    // monitor sees every press once, in one place, so the ordering has to be
    // expressed as data: the most RECENTLY registered claim takes the press,
    // which reproduces "innermost control wins" without depending on view
    // hierarchy or registration-time ordering.
    //
    // A claim is released by id rather than by popping, so a control torn down
    // out of order (a menu still open when its overlay closes) cannot strand
    // the claim above it.

    private struct EscapeClaim {
        let id: UUID
        let handler: () -> Void
    }

    private var escapeClaims: [EscapeClaim] = []

    /// True while any local control has claimed Escape — the flag the host's
    /// key monitor consults before routing a press to `OverlayStack`.
    var localEscapeClaimed: Bool { !escapeClaims.isEmpty }

    /// Register a local Escape handler. Keep the returned id and release it
    /// when the control goes away.
    @discardableResult
    func claimEscape(_ handler: @escaping () -> Void) -> UUID {
        let claim = EscapeClaim(id: UUID(), handler: handler)
        escapeClaims.append(claim)
        return claim.id
    }

    func releaseEscape(_ id: UUID) {
        escapeClaims.removeAll { $0.id == id }
    }

    /// Routes one Escape press. Returns true when the press was CONSUMED (the
    /// host swallows the event); false leaves it for AppKit, which is what
    /// keeps Escape doing its ordinary jobs on a bare board.
    func routeEscape() -> Bool {
        if let claim = escapeClaims.last {
            claim.handler()
            return true
        }
        guard let closed = stack.handleEscape() else { return false }
        close(closed)
        return true
    }

    // MARK: - ⌘V

    // The paste chain, same ranks as Escape and for the reason stated at the
    // top of this file. Only the two composer overlays register, and the
    // editor unregisters entirely while its form is read-only — an archived
    // job's key is the synthetic `archive:<slug>`, and a paste that reached it
    // would upload against a job file that does not exist.

    private struct PasteClaim {
        let id: UUID
        let priority: Int
        let handler: ([HudPastedImage]) -> Void
    }

    private var pasteClaims: [PasteClaim] = []

    @discardableResult
    func claimPaste(_ kind: OverlayKind, _ handler: @escaping ([HudPastedImage]) -> Void) -> UUID {
        let claim = PasteClaim(id: UUID(), priority: kind.rawValue, handler: handler)
        pasteClaims.append(claim)
        return claim.id
    }

    func releasePaste(_ id: UUID) {
        pasteClaims.removeAll { $0.id == id }
    }

    /// Hands a pasted image set to the highest-ranked registered composer.
    /// Returns true when someone took it. A paste carrying no image is never
    /// offered here at all (the host checks first), so ordinary text paste
    /// into the composer is untouched.
    func routePaste(_ images: [HudPastedImage]) -> Bool {
        guard let top = pasteClaims.max(by: { $0.priority < $1.priority }) else { return false }
        top.handler(images)
        return true
    }
}
