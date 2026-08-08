import Foundation

// Moves.swift — S3's PURE layer: the board's bucketing, its drag transition
// table, the optimistic-reorder arithmetic, the chip visibility rules and the
// job identity key. No SwiftUI, no I/O, no state — every function here is a
// value in and a value out, which is what lets the blind tests hold them.
//
// Each rule below is a port of one file under `web/_ui/src/`, read this
// session:
//
//   Bucketing          lib/format.ts   `bucketJobs` / `columnFor`
//   MoveTable          lib/moves.ts    `TRANSITIONS` / `resolveMove`
//   ReorderPrediction  lib/predictReorder.ts
//   ReorderOverride    hooks/useDragReorder.ts (the settle effect)
//   ChipRules          components/board/chips/{LoadChip,TokensPanel}.tsx
//                      + components/board/JobCard.tsx's `STATUS_TEXT`
//   JobKey             lib/jobKey.ts
//
// `BoardColumn` itself is NOT declared here. It is frozen under S3 in
// artifacts/50-contract.md but its declaration currently lives in
// `macos/Kraken/MacRoute.swift`, and Amendment 20 makes that location
// contract-neutral: S3 must not redeclare it.

// MARK: - the columns

extension BoardColumn {
    /// The five LIVE columns, in display order. `archived` is deliberately
    /// absent: it is a drawer inside DONE and a drop target, never a column of
    /// the grid (`web/_ui/src/lib/columns.ts` carries exactly these five, and
    /// `lib/moves.ts` keeps `ARCHIVED` apart from them for the same reason).
    static let live: [BoardColumn] = [.draft, .queued, .working, .blocked, .done]

    /// The column head's label — `DRAFT`, `QUEUED`, … (`Column.tsx` renders
    /// `col.toUpperCase()`).
    var headLabel: String { rawValue.uppercased() }
}

// MARK: - bucketing

/// Which column a job is DRAWN in — and, because there is exactly one of
/// these, which column a drag reports it was dragged FROM.
///
/// One bucketing for render and drag is the point of the type, not an
/// implementation detail: `hooks/useBoard.ts` records what two of them cost —
/// a health-flagged `working` job renders in BLOCKED while the drop table
/// still thinks it is in WORKING, so nudging the card a row down inside its
/// own column resolves working→blocked and posts `/api/stop`, interrupting the
/// agent with a gesture that looked like a reorder.
enum Bucketing {
    /// `archived` wins over everything; a non-empty `health` puts any live
    /// state in `.blocked`; otherwise the state names its own column, and an
    /// unknown state is a draft.
    ///
    /// Note what the last rule means for the string `"archived"`: it is NOT a
    /// state that maps to `.archived` (Amendment 4). The archived column is
    /// reachable only through the `archived` flag, so a job whose state field
    /// says "archived" while the flag is false buckets as `.draft` like every
    /// other unrecognised state.
    static func bucket(state: String, health: String?, archived: Bool) -> BoardColumn {
        if archived { return .archived }
        if let health, !health.isEmpty { return .blocked }
        switch state {
        case "draft": return .draft
        case "queued": return .queued
        case "working": return .working
        case "blocked": return .blocked
        case "done": return .done
        default: return .draft
        }
    }

    /// The same rule read off a decoded job.
    static func bucket(_ job: WebJob) -> BoardColumn {
        bucket(state: job.state, health: job.health, archived: job.archived == true)
    }

    /// The column a LIVE job would be steered in, or nil when it is not live —
    /// what decides whether a whole-card click opens the steer composer or the
    /// editor (`JobCard.tsx`'s `openDrilldown`). A job that is not live has no
    /// steerable column whatever its state says.
    static func liveColumn(_ job: WebJob) -> BoardColumn? {
        guard job.live else { return nil }
        return bucket(state: job.state, health: job.health, archived: false)
    }
}

// MARK: - the transition table

/// The three modes of the steer composer. Only two are reachable from a drag;
/// `steer` arrives from a card click on a live working job.
enum SteerMode: String {
    case steer, unblock, reactivate
}

/// What a drop does. Three shapes, and the difference between the first two is
/// the whole reason the table exists: a `post` asserts a state change on its
/// own, while a `steer` only chooses a composer mode and writes NOTHING — the
/// board must not claim a transition it has no message to justify.
enum MoveAction: Equatable {
    case post(endpoint: String)
    case steer(mode: SteerMode, requireText: Bool)
    case reorder
}

/// The board's drag transition table — the highest-consequence surface in the
/// app: every entry starts a cell, stops an agent, or archives real work.
///
/// A pair that is not in the table is a NO-OP, never a "close enough"
/// fallback. Ten rows; over a full 6×6 sweep exactly 14 pairs answer non-nil
/// (9 cross-column posts/steers plus one reorder per live column —
/// archived→archived is nil, since nothing is ever dragged inside the drawer).
enum MoveTable {
    static func action(from: BoardColumn, to: BoardColumn) -> MoveAction? {
        // A drop back onto the source column is a reorder — except in the
        // drawer, which nothing is dragged out of OR within (unarchive is a
        // button, `ArchiveDrawer.tsx`).
        if from == to { return from == .archived ? nil : .reorder }
        switch (from, to) {
        case (.draft, .queued): return .post(endpoint: "/api/queue")
        case (.queued, .draft): return .post(endpoint: "/api/dequeue")
        case (.queued, .working): return .post(endpoint: "/api/start")
        case (.working, .blocked): return .post(endpoint: "/api/stop")
        case (.blocked, .done): return .post(endpoint: "/api/done")
        case (.blocked, .working): return .steer(mode: .unblock, requireText: false)
        // `text: ""` is the wire's "reactivate without steering" — the same
        // endpoint the composer sends to, with an empty message.
        case (.done, .blocked): return .post(endpoint: "/api/reactivate")
        case (.done, .working): return .steer(mode: .reactivate, requireText: true)
        case (.done, .archived): return .post(endpoint: "/api/archive")
        default: return nil
        }
    }

    /// The columns that light up as legal targets while a card from `from` is
    /// in the air — DERIVED from the table above rather than listed beside it,
    /// so a highlighted pair is by construction a pair that does something.
    /// The source column is never in its own list: a same-column drop shows
    /// itself by the cards shifting.
    static func highlightTargets(from: BoardColumn?) -> Set<BoardColumn> {
        guard let from else { return [] }
        var out: Set<BoardColumn> = []
        for to in BoardColumn.allCases where to != from {
            if action(from: from, to: to) != nil { out.insert(to) }
        }
        return out
    }
}

// MARK: - optimistic reorder

/// The client-side simulation of the server's within-column reorder.
///
/// A same-column drag is the board's ONE optimistic path: the card is already
/// where it was dropped, and refetching after the POST would repaint identical
/// content and flash. So the client predicts the next payload's order and
/// holds it (see `ReorderOverride`) until the server agrees.
enum ReorderPrediction {
    /// `slugs` with `dragged` moved to `dropIndex`. nil when `dragged` is not
    /// a member — the client's picture of the column no longer lines up with
    /// what it is about to post, and predicting from a stale picture is worse
    /// than a repaint.
    ///
    /// The clamp is TWO-SIDED (Amendment 6): past the end lands last, negative
    /// lands first. The result always contains exactly the input slugs.
    static func predict(slugs: [String], dragged: String, dropIndex: Int) -> [String]? {
        guard let old = slugs.firstIndex(of: dragged) else { return nil }
        var out = slugs
        out.remove(at: old)
        let clamped = min(max(dropIndex, 0), out.count)
        out.insert(dragged, at: clamped)
        return out
    }
}

/// The predicted order, held over the payloads that follow the POST.
///
/// The settle protocol has one subtlety and it is measured rather than
/// stylistic: `/api/board` memoizes for 2s, so a poll that STARTED before the
/// reorder landed can be delivered after it and carry the pre-drag order. That
/// payload is indistinguishable from a real change made by another client, so
/// exactly one disagreement is tolerated (≈ one 3s tick) before the column is
/// handed back. Releasing on the first would flash the pre-drag order; never
/// releasing would mask a real change forever.
struct ReorderOverride {
    /// The column this override governs. Only one is held for the whole board,
    /// deliberately: a drag ends before the next one begins.
    let column: BoardColumn
    /// The predicted top-to-bottom slug order.
    private(set) var slugs: [String]
    /// How many payloads have disagreed so far.
    private(set) var disagreements = 0

    init(column: BoardColumn, slugs: [String]) {
        self.column = column
        self.slugs = slugs
    }

    /// Whether to KEEP holding the override. An agreeing payload releases
    /// (false) — the prediction was right and dropping it changes nothing that
    /// renders, which is the no-flash guarantee. The first disagreeing payload
    /// is tolerated (true). The second releases (false).
    mutating func reconcile(serverOrder: [String]) -> Bool {
        if serverOrder == slugs { return false }
        disagreements += 1
        return disagreements < 2
    }

    /// The column's jobs in the held order. A job the override never heard of
    /// (created between the drop and now) goes LAST rather than being dropped
    /// — it lives there for at most one poll.
    func apply(_ jobs: [WebJob]) -> [WebJob] {
        var bySlug: [String: WebJob] = [:]
        for j in jobs { bySlug[j.slug] = j }
        var out: [WebJob] = []
        for s in slugs {
            if let j = bySlug.removeValue(forKey: s) { out.append(j) }
        }
        for j in jobs where bySlug[j.slug] != nil {
            out.append(j)
            bySlug.removeValue(forKey: j.slug)
        }
        return out
    }
}

// MARK: - chip visibility

/// The rules deciding whether a card renders a given token. They live apart
/// from the views for one reason: each is asked TWICE — once to decide whether
/// the chip row exists at all, once by the chip itself — and a rule written
/// out twice is a rule free to drift.
enum ChipRules {
    /// The triage badge shows on a card only when the card READS AS BLOCKED.
    ///
    /// The column test is half the feature and it is not a second spelling of
    /// `job.health`: `column` already carries the health override, since
    /// `Bucketing.bucket` files an unhealthy `working` cell under `.blocked`.
    /// The badge answers "is this the one to unblock next", which is not a
    /// question a working/queued/draft/done card poses — and an estimate left
    /// on a card that has since started running reads as a current fact. The
    /// DRILLDOWN has no such gate (`SteerOverlay`'s LoadDrill renders on any
    /// live job, with the estimate's age beside it).
    static func loadChipVisible(hasLoad: Bool, column: BoardColumn) -> Bool {
        hasLoad && column == .blocked
    }

    /// Whether a token panel renders at all.
    ///
    /// nil is NOT zero: an absent `tokens` key means the job was never
    /// measured, which is a different answer from a measured zero, and
    /// rendering nil as 0 would claim the job was free. A measured zero with
    /// no transcripts behind it (`sessions == 0`) is likewise nothing to show.
    /// "Total" here is the four counters summed (Amendment 3), matching
    /// `TokensPanel.tsx`.
    static func tokensVisible(tokens: WebTokens?) -> Bool {
        guard let tokens else { return false }
        let total = tokens.input + tokens.output + tokens.cacheWrite + tokens.cacheRead
        if total == 0 && tokens.sessions == 0 { return false }
        return true
    }

    /// The identity line's trailing status token, or nil for a column that
    /// renders none.
    ///
    /// Only the two SETTLED columns carry one. `working`, `blocked` and
    /// `draft` are deliberately absent — a live card's column, its chips and
    /// its zeus pill already say live-ness, and a draft is the ABSENCE of a
    /// state (`JobCard.tsx`'s `STATUS_TEXT`).
    static func statusToken(column: BoardColumn) -> String? {
        switch column {
        case .queued: return "◌ queued"
        case .done: return "✓ done"
        default: return nil
        }
    }
}

// MARK: - identity

/// The board's job identity, and the key of every client-side map, drag id,
/// SwiftUI `id` and overlay lookup.
///
/// A slug is unique only WITHIN a namespace. An archived job rides the same
/// `jobs` array under its numbered archive name, numbered only against the
/// archive's own contents — so archiving `foo` and creating a new active `foo`
/// puts two entries with slug "foo" on the board. Keying by bare slug hands
/// the archived one a last-writer-wins victory, and the symptom is silent: the
/// lookup returns the archived entry (`live: false`), so a click on the LIVE
/// blocked card falls through to the read-only editor instead of the unblock
/// composer, and the job stops being steerable from the board.
///
/// This is NOT a server key — unarchive/delete carry the bare archive name,
/// which is what the server resolves.
enum JobKey {
    static func key(slug: String, archived: Bool) -> String {
        archived ? "archive:" + slug : slug
    }

    static func key(_ job: WebJob) -> String {
        key(slug: job.slug, archived: job.archived == true)
    }
}
