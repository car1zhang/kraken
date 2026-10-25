// Codable models mirroring kraken's `kraken serve` JSON API (see the wire
// types in internal/api/, web/serve.go, and the model layers behind them —
// internal/jobs/{daemon,job}.go, internal/core/peer.go. This file has no
// logic, just shapes; ios/API.md documents them and
// internal/api/contract_test.go pins them).
//
// Foundation is imported explicitly for FleetFilter's JSONSerialization —
// everything else here is Codable shapes the stdlib already covers.
import Foundation
//
// Rule of thumb applied throughout: a Go struct field tagged `,omitempty`
// becomes an Optional in Swift (the key may be entirely absent, and for
// slices Go's nil-vs-empty distinction can even surface as JSON `null`;
// Swift's synthesized Decodable treats a missing key and an explicit null
// the same way for Optional properties, so this is safe either way). Fields
// with no `omitempty` tag are always present and modeled as non-optional.
//
// The implication runs one way only — Optional here does NOT imply
// omitempty there — and two exceptions rely on that. (1) Absence on an
// older server: keys like `contract`/`stale`/`update` are always present on
// new servers but absent on ones predating them, and the absence itself is
// what the app checks for. (2) Blast-radius containment: every field of
// `WebUpdate` is always present on the wire and Optional anyway, so that a
// drifted sub-key costs the ⟳ control instead of the whole /api/board
// decode — see that struct's own comment, which is where the reasoning
// lives.

// MARK: - board

struct WebBoard: Decodable {
    let daemon: DaemonStatus
    let daemonOK: Bool
    let cellCap: Int
    let liveCells: Int
    let jobs: [WebJob]
    // Absent on servers older than this contract handshake — that absence is
    // itself the signal the app uses to warn about a stale server.
    let contract: Int?
    let stale: Bool?
    // Absent on servers older than the self-update button (added additively,
    // no contract bump) — nil reads as a plain idle control.
    let update: WebUpdate?
    // Absent on servers older than the TestFlight ship button (additive too)
    // — nil reads as a plain idle control, same as `update`.
    let shipIOS: WebShipIOS?
    // The current week's and today's goals, always present on a server new
    // enough to have them and absent on any older one — the same
    // absence-on-an-older-server Optional `update` and `ship_ios` are, and
    // for the same reason (additive key, no contract bump). nil reads as
    // "this server has no goals to show", which is the drawer drawing
    // nothing rather than an empty drawer claiming zero goals.
    let goals: WebGoals?
    // The web board's optional background image (additive; absent both on an
    // older server AND whenever no background is set — absence is the wire's
    // only "no background"). Decoded so the shape is modeled, but the app
    // does not render a board background yet; the follow-up is an
    // AsyncImage on /api/background?v=<updatedAt> behind the board list with
    // the same 80% surface tint the web board draws — gated on
    // updatedAt > 0, never on background != nil alone, since the drift
    // degrade below yields a non-nil object with a zero stamp while the
    // server's wire treats zero as unset.
    let background: WebBackground?
    // The name kraken's agents use for the person running it (`operator` on
    // the wire — a Swift keyword, hence the property name). Additive and
    // absent on older servers; decoded so the shape is modeled, rendered
    // nowhere in the app yet.
    let operatorName: String?
    // The other krakens on the tailnet this node has registered (v7,
    // multiplayer). `omitempty` server-side: absent whenever the registry is
    // empty, which is the singleplayer board. Stored behind a tolerant box
    // (TolerantWebNodes) so a drifted node row costs that row, never the
    // whole board decode; read through `nodes`.
    private let nodesBox: TolerantWebNodes?
    /// The registry, or nil when the server sent none.
    var nodes: [WebNode]? { nodesBox?.nodes }
    // This node's own tailscale stable id (`node_id`, additive and
    // `omitempty`: absent with no tailscale or the fleet off). Compared
    // against a thread's origin node by `threadOriginBadge`, so an ask
    // thread this node opened carries no "from" badge.
    let nodeId: String?
    // This node's device label (`node_label`, additive and `omitempty`:
    // absent with the fleet off, and on a server older than the key). Names
    // the "this Mac" row of Settings' machine filter; absent, that row reads
    // the bare words.
    let nodeLabel: String?

    enum CodingKeys: String, CodingKey {
        case daemon
        case daemonOK = "daemon_ok"
        case cellCap = "cell_cap"
        case liveCells = "live_cells"
        case jobs
        case contract, stale
        case update
        case shipIOS = "ship_ios"
        case goals
        case background
        case operatorName = "operator"
        case nodesBox = "nodes"
        case nodeId = "node_id"
        case nodeLabel = "node_label"
    }
}

// MARK: - fleet (multiplayer)

/// Which node a REMOTE card belongs to — present on exactly the cards another
/// kraken served to this one (`owner` is absent on every local card).
struct WebJobOwner: Decodable, Equatable {
    let nodeID: String
    let label: String
    let `operator`: String
    let online: Bool

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case label
        case `operator`
        case online
    }

    /// The owner chip: `label · operator` (space, U+00B7, space), or the
    /// label alone when the operator is empty.
    var chipText: String {
        `operator`.isEmpty ? label : "\(label) \u{00B7} \(`operator`)"
    }
}

/// One registered node on `/api/board`'s `nodes` (webNode in
/// internal/api/board.go). `last_err` is omitempty; `jobs` rides only on
/// GET /api/nodes and is not modeled here.
struct WebNode: Decodable, Equatable {
    let nodeID: String
    let label: String
    let `operator`: String
    let dnsName: String
    let ip: String
    let port: Int
    let build: String
    let contract: Int
    let online: Bool
    let lastOK: Int64
    let lastErr: String?

    enum CodingKeys: String, CodingKey {
        case nodeID = "node_id"
        case label
        case `operator`
        case dnsName = "dns_name"
        case ip, port, build, contract, online
        case lastOK = "last_ok"
        case lastErr = "last_err"
    }
}

/// `nodes` decoded element by element: a row that fails to decode is dropped
/// and its neighbours kept, and a non-array value reads as no registry.
private struct TolerantWebNodes: Decodable {
    let nodes: [WebNode]?

    private struct Skip: Decodable { init(from decoder: Decoder) throws {} }

    init(from decoder: Decoder) throws {
        guard var c = try? decoder.unkeyedContainer() else {
            nodes = nil
            return
        }
        var out: [WebNode] = []
        while !c.isAtEnd {
            if (try? c.decodeNil()) == true { continue }
            if let node = try? c.decode(WebNode.self) {
                out.append(node)
            } else if (try? c.decode(Skip.self)) == nil {
                break
            }
        }
        nodes = out
    }
}

/// How the phone may treat a card: its own (`local`), another kraken's that
/// only shows (`readonly`), or another kraken's opened to the net
/// (`publicCell` — every action travels through the paired node's proxy).
enum RemoteMode {
    case local, readonly, publicCell
}

extension WebJob {
    var isRemote: Bool { owner != nil }

    var remoteMode: RemoteMode {
        guard owner != nil else { return .local }
        return visibility == "public" ? .publicCell : .readonly
    }

    /// The card's identity on the merged board: `<node id>/<slug>` for a
    /// remote card (two Macs can each have a `dream-<date>`), otherwise the
    /// local identity (slug, or `archive:<slug>` for an archived job).
    var boardKey: String {
        if let owner { return "\(owner.nodeID)/\(slug)" }
        return archived == true ? "archive:" + slug : slug
    }
}

/// The spec text of a job's raw document: its YAML frontmatter dropped. The
/// Swift twin of the web board's `stripFrontmatter` — same rule, same results.
enum FrontmatterText {
    /// When `text` opens with a `---` line (`\n` or `\r\n`) and a LATER line
    /// is exactly `---` (a trailing `\r` allowed), everything after that
    /// closing line with its leading run of newlines trimmed; otherwise
    /// `text` unchanged.
    ///
    /// Walked as UTF-8 bytes rather than Characters because Swift folds
    /// `\r\n` into ONE Character, so a Character-level `hasPrefix("---\n")`
    /// misses a CRLF document and a split on `"\n"` never splits its lines.
    /// Every byte compared is ASCII, so each cut lands on a scalar boundary.
    ///
    /// A leading UTF-8 byte-order mark (EF BB BF) is skipped before the fence
    /// test, so a job file saved with one still loses its frontmatter instead
    /// of rendering raw YAML. Text with no frontmatter comes back unchanged,
    /// mark included.
    static func body(of text: String) -> String {
        let all = Array(text.utf8)
        let bom: [UInt8] = [0xEF, 0xBB, 0xBF]
        let bytes = all.starts(with: bom) ? Array(all.dropFirst(bom.count)) : all
        let dash = UInt8(ascii: "-"), cr = UInt8(ascii: "\r"), lf = UInt8(ascii: "\n")
        let fence: [UInt8] = [dash, dash, dash]
        var i: Int
        if bytes.starts(with: fence + [lf]) {
            i = 4
        } else if bytes.starts(with: fence + [cr, lf]) {
            i = 5
        } else {
            return text
        }
        while i < bytes.count {
            let end = bytes[i...].firstIndex(of: lf) ?? bytes.count
            var line = bytes[i..<end]
            if line.last == cr { line = line.dropLast() }
            if line.elementsEqual(fence) {
                var k = min(end + 1, bytes.count)
                while k < bytes.count, bytes[k] == lf || bytes[k] == cr { k += 1 }
                return String(decoding: bytes[k...], as: UTF8.self)
            }
            i = end + 1
        }
        return text
    }
}

/// The board's machine filter — the iOS port of the web board bar's machines
/// dropdown (job 634). The selection is the list of machine KEYS to show:
/// `localNode` for this Mac's own cards, a node's tailscale stable id for a
/// sibling's. Empty is no filter.
enum FleetFilter {
    /// The key of this node's own cards. A sentinel rather than the board's
    /// `nodeId`: local cards carry no `owner` and are matched by that absence,
    /// and `nodeId` is absent whenever the server's tailscale is unreadable. A
    /// tailscale stable id is never the bare word `local`.
    static let localNode = "local"

    /// The machine a card belongs to, in the selection's vocabulary.
    static func nodeKey(of job: WebJob) -> String {
        job.owner?.nodeID ?? localNode
    }

    /// Empty `selected` returns `jobs` unchanged, order kept; otherwise exactly
    /// the cards whose machine key is in `selected`, order kept. A machine that
    /// joins the tailnet while a selection is active is hidden until picked.
    static func apply(_ jobs: [WebJob], selected: [String]) -> [WebJob] {
        if selected.isEmpty { return jobs }
        let shown = Set(selected)
        return jobs.filter { shown.contains(nodeKey(of: $0)) }
    }

    /// The persisted form: a JSON array of keys in one UserDefaults string
    /// (`@AppStorage` takes a String, not an array). Anything unreadable — nil,
    /// malformed JSON, a non-array — is the empty selection; duplicates and
    /// empty strings are dropped.
    static func selection(from raw: String?) -> [String] {
        guard let raw, let data = raw.data(using: .utf8),
              let any = try? JSONSerialization.jsonObject(with: data),
              let list = any as? [Any] else { return [] }
        var out: [String] = []
        for case let s as String in list where !s.isEmpty && !out.contains(s) { out.append(s) }
        return out
    }

    /// The inverse of `selection(from:)`; an empty selection encodes as `[]`.
    static func encode(_ selected: [String]) -> String {
        guard let data = try? JSONSerialization.data(withJSONObject: selected) else { return "[]" }
        return String(decoding: data, as: UTF8.self)
    }
}

// The `background` key on /api/board: the upload stamp (unix millis), never
// the bytes — GET /api/background serves those, with the stamp as the URL's
// cache-buster.
//
// Hand-decoded with `try?` for WebGoals' blast-radius reason: this is the
// same kind of key — an additive object hanging off the board root — and a
// drifted or non-object value must degrade to a zeroed stamp, never take
// the ENTIRE /api/board decode (and every job on the board) with it.
// `decodeIfPresent` on WebBoard still absorbs an absent or null key before
// this init ever runs.
struct WebBackground: Decodable, Equatable {
    let updatedAt: Int64

    enum CodingKeys: String, CodingKey {
        case updatedAt = "updated_at"
    }

    init(from decoder: Decoder) throws {
        let c = try? decoder.container(keyedBy: CodingKeys.self)
        updatedAt = (try? c?.decode(Int64.self, forKey: .updatedAt)) ?? 0
    }
}

struct DaemonStatus: Decodable {
    let pid: Int
    let startedAt: Int64
    let lastTick: Int64
    let lastError: String?
    let cells: Int
    // Absent on daemons older than this contract handshake — see the note
    // atop this file.
    let stale: Bool?

    enum CodingKeys: String, CodingKey {
        case pid
        case startedAt = "started_at"
        case lastTick = "last_tick"
        case lastError = "last_error"
        case cells
        case stale
    }
}

// MARK: - goals

// One goal row (internal/store/goals.go's Goal, served by api/goals.go and
// carried on /api/board's `goals` key). `scope` is "week" or "day" and
// `period` is that scope's key — a `2006-01-02` LOCAL date for a day goal,
// the LOCAL Monday of the week for a week goal. Both stay Strings rather
// than an enum/Date: an unknown scope from a newer server must degrade to
// "not one of the two groups I draw", never fail the decode.
//
// `parent_id` is `omitempty` server-side and only a DAY goal can carry one
// (it points at the week goal it hangs under), so absence is the common
// case, not a drift signal.
struct Goal: Decodable, Identifiable, Equatable {
    let id: Int64
    let scope: String
    let period: String
    let text: String
    let done: Bool
    let position: Int64
    /// The week goal this day goal hangs under; nil (key absent, or 0 in the
    /// store) when it hangs under nothing.
    let parentID: Int64?

    enum CodingKeys: String, CodingKey {
        case id, scope, period, text, done, position
        case parentID = "parent_id"
    }

    var isWeek: Bool { scope == "week" }
    var isDay: Bool { scope == "day" }
}

// The `goals` key on /api/board: the CURRENT week's and today's goals, both
// arrays always present server-side (never null).
//
// Hand-decoded with `try?` for the same blast-radius reason WebUpdate and
// WebShipIOS are — the three of them are the same kind of key, an additive
// object hanging off the board root that a cosmetic control reads. A
// failure inside this object would otherwise propagate out of
// `decodeIfPresent` and take the ENTIRE /api/board decode with it, losing
// every job on the board over a drawer. The worst a drifted goal can now do
// is empty one of the two groups.
//
// The tolerance has to start at the CONTAINER, not at the two keys. If
// `goals` arrives as anything other than an object — a string, a number, an
// array, whatever a future server or a hand-edited response serves —
// `container(keyedBy:)` itself throws, and that throw escapes the
// synthesized `decodeIfPresent` on WebBoard exactly the way an inner
// failure would, taking the whole board with it. Which is to say: the
// per-key `try?`s alone would leave the headline case of this comment
// unhandled, so the container is `try?`'d too and a non-object degrades to
// no goals at all. `decodeIfPresent` still absorbs an absent or null key
// before this init ever runs.
struct WebGoals: Decodable, Equatable {
    let week: [Goal]
    let day: [Goal]

    enum CodingKeys: String, CodingKey { case week, day }

    init(from decoder: Decoder) throws {
        guard let c = try? decoder.container(keyedBy: CodingKeys.self) else {
            week = []
            day = []
            return
        }
        week = (try? c.decode([Goal].self, forKey: .week)) ?? []
        day = (try? c.decode([Goal].self, forKey: .day)) ?? []
    }

    /// For tests and previews.
    init(week: [Goal], day: [Goal]) {
        self.week = week
        self.day = day
    }
}

// The `goal` key on a webJob: the PRIMARY goal that job is linked to,
// present exactly when it links at least one (any period, done or not) and
// always equal to `goals[0]`. Strictly decoded, unlike WebGoals above — it
// sits among webJob's other sub-objects (`question`, `wakeup`, `tokens`,
// `load`), every one of which is synthesized, and matching its own
// neighbours is what keeps the job shape readable.
struct WebJobGoal: Decodable, Equatable {
    let id: Int64
    let text: String
    let done: Bool
}

// The `goals` key on a webJob: EVERY goal that job links, in link-position
// order, present exactly when it links at least one. `goal` above is this
// array's head, served alongside it so a build that predates multi-goal
// links keeps rendering the primary; both keys are omitted together when
// the job links nothing.
//
// A wrapper struct rather than a bare `[WebJobGoal]?` property, because the
// tolerance below needs an `init(from:)` of its own and an array property
// cannot carry one. `items` is the array; the type is Equatable so a test
// can compare a whole list in one assertion.
//
// Hand-decoded, and tolerant in two distinct places — the card's chips are
// cosmetic, the board is not, so a drifted server may cost chips and must
// never cost the job it is drawn on. A failure here would NOT stop at the
// chip: `WebBoard.jobs` is a strictly decoded `[WebJob]`, so a throw inside
// one job's `goals` takes the ENTIRE board decode with it. The two places:
//
//   - The CONTAINER: `goals` present but not an array (a string, a number,
//     an object — whatever a future server or a hand-edited response
//     serves) degrades to an empty `items` rather than throwing. Absent or
//     null is a different case entirely, absorbed by WebJob's synthesized
//     `decodeIfPresent` before this init ever runs: it nils the whole
//     property.
//   - Each ELEMENT: a malformed one is DROPPED and the rest survive, which
//     a plain `[WebJobGoal]` decode cannot do — one bad element throws for
//     the whole array. That is what `TolerantWebJobGoal` below is for: a
//     wrapper whose own decode never throws, so the array decode always
//     succeeds and the failures arrive as nils to be filtered out.
struct WebJobGoalList: Decodable, Equatable {
    let items: [WebJobGoal]

    init(from decoder: Decoder) throws {
        guard let c = try? decoder.singleValueContainer(),
              let raw = try? c.decode([TolerantWebJobGoal].self) else {
            items = []
            return
        }
        items = raw.compactMap(\.goal)
    }

    /// For tests and previews.
    init(items: [WebJobGoal]) {
        self.items = items
    }
}

// One element of the `goals` array, decoded so that it cannot throw: a
// malformed element yields `goal == nil` instead of failing the array. It
// exists only inside WebJobGoalList's init — `[WebJobGoal?]` would NOT do
// the same job, since Optional's own decoding still calls WebJobGoal's
// throwing init for any non-null value.
private struct TolerantWebJobGoal: Decodable {
    let goal: WebJobGoal?

    init(from decoder: Decoder) throws {
        goal = try? WebJobGoal(from: decoder)
    }
}

// MARK: - self-update

// WebUpdate mirrors api/update.go's webUpdate: the whole state behind the
// daemon bar's ⟳ control. Served twice from one assembly function
// (currentUpdate) — as /api/board's `update` key and as the entire body of
// GET /api/update/status — so the board and the in-flight poller can never
// disagree about the same run. Nothing here is `omitempty` server-side:
// every key is always present, zeroed when it doesn't apply.
//
// Every property is nonetheless Optional, decoded field-by-field with `try?`
// below, and that is a blast-radius decision rather than a claim about the
// server. `WebBoard.update` is decoded with `decodeIfPresent`, which only
// nils on an ABSENT or null `update` KEY — a failure INSIDE the object (a
// sub-key missing, null, or the wrong type) propagates and takes the ENTIRE
// /api/board decode down with it, so every job on the board would be lost
// over a cosmetic ⟳ control. That is the wrong trade at any probability.
// Optional alone isn't enough for it, either: synthesized decoding of an
// Optional property calls `decodeIfPresent`, which returns nil for
// absent/null but still THROWS on a type mismatch — hence the hand-written
// init. The worst a drifted sub-key can now do is paint the button idle.
// This is the same reasoning that already makes every other tolerated-drift
// field on WebBoard Optional.
//
// `error` is the RUN's error only — a failed availability check is
// deliberately never surfaced here — while `behind`/`checked_at` come from
// the daemon's 10-minute availability check and describe "is there anything
// to update to" whether or not a run has ever happened.
struct WebUpdate: Decodable {
    /// idle|preflight|fetching|merging|building|installing|restarting|
    /// mac app|done|failed|uptodate — render unknown values generically, the
    /// list is additive. `idle` when no run has ever happened.
    let phase: String?
    let fromSHA: String?
    /// "" until the merge lands.
    let toSHA: String?
    let behind: Int?
    let error: String?
    /// unix millis; 0 = never run.
    let startedAt: Int64?
    /// unix millis of the last phase transition.
    let updatedAt: Int64?
    /// the running binary's ldflags-stamped short sha, "dev" unstamped.
    let buildSHA: String?
    /// unix millis of the last availability check; 0 = never checked.
    let checkedAt: Int64?

    enum CodingKeys: String, CodingKey {
        case phase
        case fromSHA = "from_sha"
        case toSHA = "to_sha"
        case behind, error
        case startedAt = "started_at"
        case updatedAt = "updated_at"
        case buildSHA = "build_sha"
        case checkedAt = "checked_at"
    }
}

// Decoding lives in an extension purely so the memberwise initializer
// survives — BoardView synthesizes WebUpdate values locally (the optimistic
// `preflight` paint, the gap-ceiling `failed` record) and declaring an init
// in the struct body would suppress it.
extension WebUpdate {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        // `try?` per field, not `try`: this is the blast-radius cap described
        // above, and it has to cover type mismatches too, which
        // `decodeIfPresent` alone would still throw on.
        phase = try? c.decodeIfPresent(String.self, forKey: .phase)
        fromSHA = try? c.decodeIfPresent(String.self, forKey: .fromSHA)
        toSHA = try? c.decodeIfPresent(String.self, forKey: .toSHA)
        behind = try? c.decodeIfPresent(Int.self, forKey: .behind)
        error = try? c.decodeIfPresent(String.self, forKey: .error)
        startedAt = try? c.decodeIfPresent(Int64.self, forKey: .startedAt)
        updatedAt = try? c.decodeIfPresent(Int64.self, forKey: .updatedAt)
        buildSHA = try? c.decodeIfPresent(String.self, forKey: .buildSHA)
        checkedAt = try? c.decodeIfPresent(Int64.self, forKey: .checkedAt)
    }
}

// UpdateStartResponse mirrors api/update.go's updateResp — POST /api/update's
// 202 body. 202, not 200: the run has been *started* (the claim is written,
// the detached child forked), not finished. `phase` is always "preflight",
// the phase that claim was written at. A refusal never reaches this type —
// it's a 409 with {"error": …}, which APIClient turns into KrakenError.
struct UpdateStartResponse: Decodable {
    let ok: Bool
    let phase: String
    let fromSHA: String

    enum CodingKeys: String, CodingKey {
        case ok, phase
        case fromSHA = "from_sha"
    }
}

// MARK: - ship to TestFlight

// WebShipIOS mirrors api/shipios.go's webShipIOS: the whole state behind the
// daemon bar's ⇧ control. Served twice from one assembly function
// (currentShipIOS) — as /api/board's `ship_ios` key and as the entire body of
// GET /api/ship-ios/status — so the board and the in-flight poller can never
// disagree about the same run. Nothing here is `omitempty` server-side.
//
// Every property is Optional and hand-decoded with `try?` for exactly the
// blast-radius reason WebUpdate is (see its header): a failure INSIDE this
// object would otherwise propagate through `decodeIfPresent` and take the
// ENTIRE /api/board decode down with it, losing every job on the board over a
// cosmetic control.
//
// One thing this model does NOT have, and the difference is worth stating: no
// gap/restart concept. An update restarts the server answering these polls; a
// ship replaces nothing on the Mac, so a failed status fetch is a genuine
// fault rather than the expected middle of a successful run.
struct WebShipIOS: Decodable {
    /// idle|preflight|generating|archiving|exporting|uploading|done|failed.
    /// `idle` when no run has ever happened.
    let phase: String?
    /// The CFBundleVersion this run is uploading (git rev-list --count HEAD);
    /// 0 until the shipper announces it. Worth showing: App Store Connect
    /// burns a build number permanently, so it's the first thing anyone needs
    /// after a failure — and the only way to tell which build landed.
    let build: Int?
    let error: String?
    /// unix millis; 0 = never run.
    let startedAt: Int64?
    /// unix millis of the last phase transition.
    let updatedAt: Int64?

    enum CodingKeys: String, CodingKey {
        case phase, build, error
        case startedAt = "started_at"
        case updatedAt = "updated_at"
    }
}

// Decoding lives in an extension so the memberwise initializer survives —
// BoardView synthesizes WebShipIOS values locally (the optimistic `preflight`
// paint, the lost-contact `failed` record), and declaring an init in the
// struct body would suppress it. Same arrangement as WebUpdate.
extension WebShipIOS {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        // `try?` per field, not `try`: the blast-radius cap described above,
        // covering type mismatches too, which `decodeIfPresent` would throw on.
        phase = try? c.decodeIfPresent(String.self, forKey: .phase)
        build = try? c.decodeIfPresent(Int.self, forKey: .build)
        error = try? c.decodeIfPresent(String.self, forKey: .error)
        startedAt = try? c.decodeIfPresent(Int64.self, forKey: .startedAt)
        updatedAt = try? c.decodeIfPresent(Int64.self, forKey: .updatedAt)
    }
}

// ShipIOSStartResponse mirrors api/shipios.go's shipIOSResp — POST
// /api/ship-ios' 202 body. 202, not 200: the run has been *started* (the claim
// written, the detached child forked), and it is a long way from finished.
// `phase` is always "preflight". A refusal never reaches this type — it's a
// 409 with {"error": …}, which APIClient turns into KrakenError.
struct ShipIOSStartResponse: Decodable {
    let ok: Bool
    let phase: String
}

// The `account` key on a webJob (api/board.go's webJobAccount): the pool slot
// this cell is BOUND to — the login every claude in it bills, delivered to the
// cell's panes as CLAUDE_SECURESTORAGE_CONFIG_DIR and stamped on the job row at
// spawn (schema v28).
//
// "account", never "usage": `usage` in this app already means account QUOTA
// (UsageView, UsageResponse, /api/usage) and the two are unrelated numbers —
// the same collision WebTokens refuses in the other direction. The join between
// them is `slug`, which is the key /api/usage's own rows carry.
//
// DECODE-ONLY this pass: nothing renders it yet. It is modeled now rather than
// later so the phone's decode of the whole board cannot be surprised by the
// key, and so the wire contract lands in one commit with its Swift model, which
// is the repo's API-contract rule.
//
// `slug` is non-Optional and the other two are, which mirrors the Go type only
// loosely and is deliberate. Nothing on this object carries omitempty
// server-side — presence of the OBJECT is the whole answer — but `email` is ""
// for a slot ccp has no identity for and `at` is 0 for a binding written with no
// stamp, and a client that must not fail a board decode over a cosmetic key
// treats "absent" and "empty" the same way. `slug` stays required because it IS
// the binding: an object without one names no account at all — and that
// strictness is what the tolerant wrapper below converts into a nil rather than
// into a thrown board.
//
// `Decodable`, not `Codable`: nothing in this app encodes a webJob, and every
// other model in this file states the one direction it travels.
struct WebJobAccount: Decodable {
    /// The pool slot's directory name under `~/.claude-pool`, and the key the
    /// `/api/usage` rows share — join on this, never on the email.
    let slug: String
    /// The account identity ccp recorded; nil or "" when the slot has none yet,
    /// in which case the slug is what a badge should show.
    let email: String?
    /// Unix MILLIS of the bind stamp (WebJob.mtime's convention, never
    /// seconds). nil or 0 means the binding carries no time — render no age
    /// rather than "now".
    let at: Int64?
}

// One `account` object, decoded so that it CANNOT throw: a present-but-
// malformed one yields nil instead of failing the decode above it. It is the
// single-object sibling of TolerantWebJobGoal, exists for the same reason, and
// the reason is the same size: `WebBoard.jobs` is a strictly decoded
// `[WebJob]`, so a throw inside one job's `account` takes the ENTIRE board
// payload with it — and the account badge is cosmetic while the board is not.
//
// A plain `WebJobAccount?` property would NOT do this job. Optional's own
// decoding still calls WebJobAccount's throwing init for any non-null value,
// so a `{"account": {"slug": 7}}` — a retyped key, a future server, a
// hand-edited response — would throw exactly where it must not. Absent or null
// is a different case entirely and never reaches here: WebJob's synthesized
// `decodeIfPresent` nils the property before this init runs.
private struct TolerantWebJobAccount: Decodable {
    let account: WebJobAccount?

    init(from decoder: Decoder) throws {
        account = try? WebJobAccount(from: decoder)
    }
}

// JobSpawnLink mirrors one end of a spawn link (ios/API.md's `jobSpawnLink`,
// internal/api/board.go's webSpawnLink): the other job's current slug and its
// display state — draft | working | blocked | done | archived, a plain String
// so a future word decodes. Decode-only this pass: nothing on the phone
// renders it yet.
struct JobSpawnLink: Decodable {
    let slug: String
    let state: String
}

// TolerantJobSpawnLink and TolerantJobSpawnLinks are `spawned_by`'s and
// `spawned`'s wrappers, TolerantWebJobAccount's arrangement for the same
// reason: a malformed object or array costs this card its (unrendered) links,
// never the board its decode.
private struct TolerantJobSpawnLink: Decodable {
    let link: JobSpawnLink?

    init(from decoder: Decoder) throws {
        link = try? JobSpawnLink(from: decoder)
    }
}

private struct TolerantJobSpawnLinks: Decodable {
    let links: [JobSpawnLink]?

    init(from decoder: Decoder) throws {
        links = try? [JobSpawnLink](from: decoder)
    }
}

// WebJobContext mirrors board.go's webJobContext — the cell lead's
// context-window fill as its statusline last reported it (the kv row
// `cell_ctx:<id>:<session>` that `kraken ctx-hook` writes on every render and
// the daemon's auto-handoff pass reads). Nothing here is recomputed: `pct` is
// Claude Code's own input-only fill over the model's REAL window.
//
// "context", never "tokens" or "usage": WebTokens is the job's summed SPEND
// and usage is account QUOTA; this is how much of one live session's window
// is occupied right now.
//
// Decode-only this pass — nothing on the phone renders it yet (Q6 of job
// 630). Every field but `handoff` rides on every present object (no omitempty
// on the Go type); absence is said one level up by `WebJob.context` being
// nil. Two zeros carry meaning and neither is an empty window: `used` 0 means
// the reporting hook predates the exact-count field, `window` 0 means the
// hook sent no size.
struct WebJobContext: Decodable {
    /// `context_window.used_percentage` as reported — may carry decimals.
    let pct: Double
    /// `context_window_size` in tokens; 0 when the hook sent none.
    let window: Int64
    /// `total_input_tokens`, the exact count `pct` came from; 0 when unreported.
    let used: Int64
    /// Unix MILLIS of the report (WebJob.mtime's convention).
    let at: Int64
    /// The fill at which the daemon types /handoff into the pane — served so
    /// no client hardcodes it.
    let handoffPct: Double
    /// The auto-handoff pass' state for THIS session; nil when it holds none.
    let handoff: WebJobHandoff?

    enum CodingKeys: String, CodingKey {
        case pct, window, used, at
        case handoffPct = "handoff_pct"
        case handoff
    }
}

// WebJobHandoff mirrors board.go's webJobHandoff: fires in the CURRENT streak
// (the pass resets the count once the last fire is two hours old, so never a
// lifetime total), the last fire's unix millis (0 when never fired), and
// whether the streak hit its cap.
struct WebJobHandoff: Decodable {
    let fires: Int
    let lastAt: Int64
    let exhausted: Bool

    enum CodingKeys: String, CodingKey {
        case fires
        case lastAt = "last_at"
        case exhausted
    }
}

// One `context` object, decoded so that it CANNOT throw — TolerantWebJobAccount's
// twin, for its reason: `WebBoard.jobs` is a strictly decoded `[WebJob]`, so a
// malformed `context` on one job would take the whole board with it, and a
// meter nobody renders yet must never cost the board it would be drawn on.
private struct TolerantWebJobContext: Decodable {
    let context: WebJobContext?

    init(from decoder: Decoder) throws {
        context = try? WebJobContext(from: decoder)
    }
}

struct WebJob: Decodable, Identifiable {
    let slug: String
    let repo: String
    let state: String
    let err: String?
    let outcome: String?
    let cell: String?
    let live: Bool
    // The cell's last words / blocked reason — freeform prose, markdown-rendered.
    let detail: String?
    // A pending interactive question (e.g. an AskUserQuestion tool call)
    // blocking the cell, if any.
    let question: WebQuestion?
    // zeus' own refined status (running/polling/waiting/question), distinct
    // from `state` — absent when zeus isn't resolvable yet.
    let zeusStatus: String?
    // The pipeline skill zeus most recently used (info/aim/do/dream),
    // read off zeus' transcript server-side — see webJob.Stage
    // (internal/api/board.go) for what absence covers. Additive key, no
    // contract bump.
    let stage: String?
    // A pending ScheduleWakeup from zeus' transcript — the cell is on a poll
    // timer (mid /loop or /poll-pr-style tick) rather than genuinely idle.
    // Still counts as "working"; the UI renders a countdown chip from it.
    // Additive key, no contract bump.
    let wakeup: WebWakeup?
    // Live subagents currently working the cell, if any.
    let roster: [WebRosterAgent]?
    let artifacts: [WebArtifact]?
    let health: String?
    let mtime: Int64
    // Archived jobs ride the same list with state "archived": their `slug`
    // is the numbered archive file base (what unarchive/delete target) and
    // `displaySlug` carries the original slug for rendering.
    let archived: Bool?
    let displaySlug: String?
    // A started, non-done job whose tmux session vanished: the worktree
    // (uncommitted work included) is still on disk, and reactivate is the
    // recovery that preserves it. Additive key, absent on older servers.
    let cellGone: Bool?
    // A job whose zeus session id is recorded and whose cell is resumable
    // (done jobs, plus gone-cell started jobs) — the only ones the
    // reactivate action can actually resume.
    let reactivatable: Bool?
    // carl's own most recent message to the cell (the last me→zeus mid-run
    // log entry), independent of whether the cell is currently live.
    let lastSent: String?
    // Open/merged/closed PRs associated with this job, if any — additive key,
    // absent on older servers just like every other omitempty field here.
    let prs: [JobPR]?
    // The cell's manual-test URLs (`kraken stack add`, job 622): one entry
    // per link the cell registered, absent when it registered none and always
    // absent on an archived job (the rows die with the cell). Decoded,
    // tolerant of any `status` string, and NOT rendered yet: a localhost
    // link means nothing on the phone until it is rewritten to the owner's
    // tailnet host (Q3 of that job, open). Additive key.
    let stack: [JobStackLink]?
    // The cells this cell FILED and the cell that filed it (job 647): the
    // board's `spawned` array and `spawned_by` object, both absent when there
    // are none and both present on an archived card too — the link is history
    // like `prs`. Stored behind tolerant wrappers and read through the
    // computed `spawned` / `spawnedBy` below, `account`'s arrangement.
    // Decode-only this pass. Additive keys.
    private let spawnedBox: TolerantJobSpawnLinks?
    private let spawnedByBox: TolerantJobSpawnLink?
    // zeus' open doc questions (answered or not), served straight from
    // `questions` rows in kraken.db — no file, no parse step, so there is
    // no more malformed-questions-file case (docQuestionsErr is gone, v5).
    // Additive key, absent on older servers.
    let docQuestions: [WebDocQuestion]?
    // Per-job feedback entries (open + addressed), served straight from
    // `feedback` rows in kraken.db. Additive key, absent on older servers.
    let feedback: [WebFeedback]?
    // Side threads branched off zeus' session for this job — live ones run
    // as a second claude in their own window of the same cell, closed ones
    // are revivable conversations. Additive key, absent on older servers
    // (and on any job that never had one), so no contract bump.
    let threads: [WebThread]?
    // This job's summed token spend, straight from its `job_tokens` row.
    // Optional for a reason that is NOT just "older servers": absent means
    // the job has never been measured, which is a different answer from a
    // measured zero (that arrives as an object of zeros). Never render nil
    // as 0 — see WebTokens. Additive key, no contract bump.
    let tokens: WebTokens?
    // zeus' two 1–10 triage estimates for this job: what unblocking it costs
    // carl, and how big the claude work that reply releases. Optional for the
    // same reason `tokens` is and not merely "older servers" — but absence
    // here does NOT mean only "never estimated", and that is where it parts
    // company with `tokens`. Three distinct cases arrive as nil:
    //
    //   - never estimated;
    //   - the estimate was CLEARED (api/jobload.go writes the zero row, which
    //     zeroes `at`, and the server serves no object for a zero `at`);
    //   - the job is ARCHIVED, where api/board.go's `archivedWebJob` withholds
    //     the key deliberately even when the row still holds an estimate — the
    //     argument for that is in the comment beside the omission there. The
    //     columns survive archival and the key comes back on unarchive.
    //
    // So nil is "no estimate to act on", never "this job cost nothing". Never
    // render it as 0. Additive key, no contract bump.
    let load: WebJobLoad?
    // The job's optional color — one of red/blue/green/yellow/purple/orange/
    // pink/cyan (the names Claude Code's `/color` accepts), absent or "" when
    // the job has none. Additive key, absent on older servers, so no contract
    // bump; render through BoardTheme.jobColor, which degrades an unknown
    // name from a newer server to "no color".
    let color: String?
    // Which coding agent the cell's zeus runs — "codex" for a codex cell,
    // absent for the claude default. Additive key, absent on older servers, so
    // no contract bump; a client that ignores it renders as it always has.
    let agent: String?
    // Who wrote the job (schema v37): operator | agent | linear | kraken |
    // unknown. Additive key, absent on older servers; decoded, not rendered.
    let author: String?
    // The PRIMARY goal this job is linked to: the head of `goals` below,
    // present under exactly the same condition and never on its own. From
    // ANY period and done or not, so a job carried over from last week
    // still names the goal it belongs to. Additive key, absent on older
    // servers, so no contract bump.
    let goal: WebJobGoal?
    // Every goal this job links, in link-position order, present exactly
    // when it links at least one. A job may link several, so the card draws
    // one chip per goal (`GoalChip` in GoalsDrawer.swift, laid out by
    // `CardChipFlow`), which is the only place the phone shows a job's
    // goals — picking them is web-only. Additive key, absent on servers
    // that predate multi-goal links (where `goal` alone still carries the
    // one link), so no contract bump; see WebJobGoalList for why a drifted
    // value degrades to an empty list rather than failing the board.
    let goals: WebJobGoalList?
    // The pool account this cell is bound to (WebJobAccount above). nil is the
    // ONLY spelling of UNBOUND — the cell runs on the machine's main login,
    // exactly as every cell did before per-cell binding — and it covers three
    // states no client separates: the pool was dry or fully excluded at spawn,
    // rotation is pinned MANUAL (kraken takes no account decision then), and
    // the job is ARCHIVED, where api/board.go's `archivedWebJob` withholds the
    // key deliberately (`load`'s call, for `load`'s reason: a card with no cell
    // cannot pose the question a badge would answer). The three columns survive
    // archival, so the key returns on unarchive.
    //
    // Served for every ACTIVE bound job whatever its display state, so the key
    // never flickers as a card moves between working and blocked; the
    // live-cards-only rule is a RENDERING rule each client applies. Decode-only
    // this pass — nothing on the phone renders it yet. Additive key, absent on
    // older servers, so no contract bump.
    //
    // Stored behind TolerantWebJobAccount and read through the computed
    // `account` below, so nil covers a FOURTH case the wire does not have a
    // spelling for: an account object that arrived MALFORMED. Every other case
    // costs this card its badge; that one would otherwise cost the whole board
    // its decode. Call sites see `WebJobAccount?` either way — the wrapper is
    // the decode's business and nothing else's.
    private let accountBox: TolerantWebJobAccount?
    // The cell lead's context-window fill (WebJobContext above). nil is the
    // ONLY spelling of UNREPORTED — no stamped session yet, a cell whose
    // statusLine kraken left alone, a codex cell — and ARCHIVED, where
    // api/board.go's `archivedWebJob` withholds the key (`account`'s call); a
    // done job keeps its last report, since a reactivation resumes the same
    // session near that fill. Served whatever the display state, so the key
    // never flickers as a card moves column. Decode-only this pass. Additive
    // key, absent on older servers, so no contract bump.
    //
    // Stored behind TolerantWebJobContext and read through the computed
    // `context` below, exactly as `account` is, so a MALFORMED object costs
    // this card its (unrendered) meter rather than the board its decode.
    private let contextBox: TolerantWebJobContext?
    // The node that owns this card when another kraken served it (v7). Absent
    // on every local card — its presence IS "remote".
    let owner: WebJobOwner?
    // private | readonly | public — present on every card a v7 server sends
    // (a local card's is its effective visibility, readonly by default).
    // Optional for absence on an older server; compare, never switch, so an
    // unknown word from a newer one reads as "not public".
    let visibility: String?
    // `start_error` is gone with the queued state (v6): a start is now the
    // synchronous answer to POST /api/queue, so a failure surfaces as that
    // call's own 409 rather than as a field on a job parked in a queue.

    // Identity is (slug, archived), NOT the bare slug — the same pairing
    // `jobKey` uses in web/static/app.js, and for the same reason. An archived
    // job rides this very `jobs` array under its NUMBERED archive name, and
    // that name is numbered against the archive's own contents only, so
    // archiving `foo` and then creating a new active `foo` puts two entries
    // with slug "foo" on the board. Returning the bare slug here hands both
    // the same Identifiable id, which is duplicate identities in every
    // ForEach over jobs — SwiftUI's rows then reuse and swap state between two
    // unrelated jobs. The wire keeps carrying the slug deliberately (it is the
    // human-facing name); disambiguating is the client's job, on both clients.
    //
    // A remote card widens the same argument to owners: two Macs each have a
    // `dream-<date>`, so the identity is `boardKey` — `<node id>/<slug>` for
    // an owned card, the (slug, archived) pairing otherwise.
    var id: String { boardKey }
    /// What the UI shows as the job's name (archived jobs may be numbered).
    var title: String { displaySlug ?? slug }
    /// The pool account this cell is bound to, or nil — unbound, archived, or
    /// (see accountBox) an object that failed to decode. The stored wrapper is
    /// private precisely so this is the only spelling a reader ever uses.
    var account: WebJobAccount? { accountBox?.account }
    /// The cell lead's last context-window report, or nil — unreported,
    /// archived, or (see contextBox) an object that failed to decode.
    var context: WebJobContext? { contextBox?.context }
    /// The cells this cell filed, or nil — none, or a malformed array.
    var spawned: [JobSpawnLink]? { spawnedBox?.links }
    /// The cell that filed this one, or nil — none, or a malformed object.
    var spawnedBy: JobSpawnLink? { spawnedByBox?.link }

    enum CodingKeys: String, CodingKey {
        case slug, repo, state, err, outcome, cell, live
        case detail, question, wakeup
        case zeusStatus = "zeus_status"
        case stage
        case roster, artifacts, health, mtime
        case archived
        case displaySlug = "display_slug"
        case cellGone = "cell_gone"
        case reactivatable
        case lastSent = "last_sent"
        case prs
        case stack
        // the wire names are `spawned` / `spawned_by`; the stored properties
        // are the tolerant wrappers, read back through the computed pair above
        case spawnedBox = "spawned"
        case spawnedByBox = "spawned_by"
        case docQuestions = "doc_questions"
        case feedback
        case threads
        case tokens
        case load
        case color
        case agent
        case author
        case goal
        case goals
        // the wire name is `account`; the stored property is the tolerant
        // wrapper, read back through the computed `account` above
        case accountBox = "account"
        // same arrangement for `context`
        case contextBox = "context"
        case owner, visibility
    }
}

// WebTokens mirrors one `job_tokens` row (board.go's webTokens /
// store.JobTokens): what a job actually cost, summed from its own Claude Code
// transcripts by kraken's daemon.
//
// "tokens", never "usage" — `usage` in this app already means ACCOUNT QUOTA
// (UsageView, UsageResponse, /api/usage) and the two are unrelated numbers.
//
// The four counters arrive RAW with no weighted total, deliberately: they are
// billed at wildly different rates, and a server-side "cost" would bake a
// price ratio into the wire and go stale the moment pricing moves. Any
// single-number rendering weights them here and owns that choice.
//
// Every field is non-optional even though the rest of this file makes
// omitempty keys Optional: none of these carries `omitempty` on the wire,
// because a measured zero is a real answer that must still ride. Absence is
// expressed one level up, by `WebJob.tokens` being nil — so nil must never
// render as 0.
//
// A row can be a FLOOR rather than an exact total (a transcript scanned only
// part way still contributes what was read), and nothing on the wire says
// which — so this is spend-so-far, never "exact".
// The wire names are `in`/`out`; the Swift properties are `input`/`output`
// because `in` is a Swift keyword and a backticked `tokens.\`in\`` at every
// call site is a footgun, not a contract. The CodingKeys below are the whole
// of the difference.
//
// THE ZEUS SPLIT — the four `zeus*` properties — is how much of the same four
// counters the cell's LEAD agent spent, as opposed to everything it delegated
// to (teammates plus in-process subagents; a side thread counts as the lead,
// being a fork of zeus' own session rather than work handed out). Two rules,
// both structural:
//
//   - Each is a SUBSET of the counter it shadows, never a sibling. `input`
//     already INCLUDES `zeusInput`; adding them doubles the lead's spend.
//   - SUBAGENT spend has no key on the wire and never will — it is
//     `input - zeusInput` and so on, subtracted here. Two halves on the wire
//     could arrive disagreeing with the total they add up to; a derived half
//     cannot.
//
// These four alone are Optional, breaking this type's own everything-required
// rule, and for a reason the rest of the file's optionality does NOT cover: an
// installed build outlives the `kraken serve` on this machine that it talks to,
// so a hard-required key would fail the WHOLE job's decode — not just the
// tokens object — against a server that predates the split. `zeusTotal` is the
// accessor a view should ask: nil exactly when the server did not send them,
// which is what tells the view to omit the split row rather than render a `0`
// the server never claimed.
struct WebTokens: Decodable {
    /// `input_tokens` — uncached input (wire key `in`).
    let input: Int64
    /// `output_tokens` — generated (wire key `out`).
    let output: Int64
    /// `cache_creation_input_tokens`.
    let cacheWrite: Int64
    /// `cache_read_input_tokens`.
    let cacheRead: Int64
    /// How many transcript FILES were summed (root transcripts + subagent
    /// sidecars). It is what tells "this job was cheap" apart from a row of
    /// zeros that means something else.
    let sessions: Int64
    /// Unix millis: when a refresh last found new spend.
    let updatedAt: Int64

    /// The lead agent's share of `input` — nil on a server predating the split.
    let zeusInput: Int64?
    /// The lead agent's share of `output`.
    let zeusOutput: Int64?
    /// The lead agent's share of `cacheWrite`.
    let zeusCacheWrite: Int64?
    /// The lead agent's share of `cacheRead`.
    let zeusCacheRead: Int64?

    /// The four zeus counters summed, or nil when the server did not send
    /// them at all.
    ///
    /// nil is the whole point of this accessor: it means "this server does not
    /// report the split", which is a different answer from a lead that spent
    /// nothing, and a view must omit the split rather than draw a `0`. A
    /// server that sends the keys sends all four, so one present is all four
    /// present — the flatMap-free shape below reads the first and trusts the
    /// rest, matching how the wire actually behaves (no key here carries
    /// `omitempty`).
    var zeusTotal: Int64? {
        guard let input = zeusInput else { return nil }
        return input + (zeusOutput ?? 0) + (zeusCacheWrite ?? 0) + (zeusCacheRead ?? 0)
    }

    /// Everything the job DELEGATED, summed: the four totals minus the four
    /// zeus counters. nil for the same reason `zeusTotal` is, and derived
    /// rather than read because the server deliberately serves only one half.
    ///
    /// Clamped at zero. The subtraction cannot legitimately go negative — zeus
    /// is a subset by construction, on both the server and the wire — so a
    /// negative here would be a server bug, and a job card reading `-2.1M
    /// subagent` is a worse way to learn about it than a zero is.
    var subagentTotal: Int64? {
        guard let zeus = zeusTotal else { return nil }
        return max(0, (input + output + cacheWrite + cacheRead) - zeus)
    }

    enum CodingKeys: String, CodingKey {
        case input = "in"
        case output = "out"
        case sessions
        case cacheWrite = "cache_write"
        case cacheRead = "cache_read"
        case updatedAt = "updated_at"
        case zeusInput = "zeus_in"
        case zeusOutput = "zeus_out"
        case zeusCacheWrite = "zeus_cache_write"
        case zeusCacheRead = "zeus_cache_read"
    }
}

/// Which ROI band an estimate falls in. The raw values are the band names the
/// shared contract spells, and the web board's CSS classes carry the same four,
/// so the two clients cannot drift into different vocabularies for one number.
enum LoadBand: String {
    case hot, warm, even, cold

    /// The band a ratio falls in. Closed-open from the top, so every finite
    /// ratio lands in exactly one: `>= 3.0` hot, `>= 1.5` warm, `>= 0.75` even,
    /// anything below that cold.
    static func forROI(_ roi: Double) -> LoadBand {
        if roi >= 3.0 { return .hot }
        if roi >= 1.5 { return .warm }
        if roi >= 0.75 { return .even }
        return .cold
    }
}

/// The anchor ladders — what each rung of `unblock` and `scope` MEANS, in
/// words. The two numbers a `WebJobLoad` carries are 1..10 with nothing on the
/// wire to say what a 4 is, so every reader (zeus writing one, carl reading
/// one) has to share a vocabulary or the ratio is two people's private scales
/// divided by each other. These ten-entry ladders are that vocabulary.
///
/// The SAME twenty strings are carried in three places — `.claude/skills/load/SKILL.md`
/// (what zeus reads when it estimates), `web/_ui/src/lib/format.ts` (the web
/// board's drilldown) and here — and a Go test parses all three and asserts
/// they are byte-identical, which is why the entries below are written as
/// plain string literals in a bracketed array literal rather than assembled,
/// interpolated or reordered: that test reads this source, not a running app.
/// Editing one label means editing all three in the same commit.
///
/// Index n-1 is rung n. `label(unblock:)`/`label(scope:)` are the safe way in
/// — they take the rung NUMBER and answer nil for anything off the ladder, so
/// no caller does its own off-by-one arithmetic against a fixed-size array.
enum LoadAnchors {
    /// Rungs 1-10 of `unblock`: what unblocking the job costs CARL.
    static let unblock: [String] = [
        "Mechanical",
        "Read one page, pick",
        "Answer a few concrete questions",
        "Local smoke, or a credential",
        "Live E2E drive",
        "Genuine review",
        "Decision batch needing new context",
        "Deep decision, real consequences",
        "Many deep decisions in one sitting",
        "A day's thought",
    ]

    /// Rungs 1-10 of `scope`: how big the claude work that reply releases is.
    static let scope: [String] = [
        "One-message close-out",
        "One small round",
        "One focused artifact or round",
        "A single-slice implementation",
        "A moderate slice",
        "A full /aim set draft",
        "A delegated build",
        "Full phase-2 of a substantial spec",
        "A multi-PR program",
        "The largest coordinated effort",
    ]

    /// The unblock ladder's label for rung `n`, or nil when `n` is off the
    /// ladder (0, 11, negative). nil rather than a placeholder string: a rung
    /// nobody defined is a number the server should never have served, and a
    /// view that omits the label says that more honestly than one printing
    /// "unknown" beside a real-looking digit.
    static func label(unblock n: Int) -> String? { rung(n, in: unblock) }

    /// The scope ladder's label for rung `n`, nil off the ladder — same rule.
    static func label(scope n: Int) -> String? { rung(n, in: scope) }

    /// The one bounds check both accessors share. Written against the array's
    /// actual `count` rather than a literal 10 so it cannot disagree with the
    /// ladder it is indexing, and the guard comes BEFORE the subscript because
    /// a Swift array subscript out of range traps the process — this is asked
    /// with numbers straight off the wire.
    private static func rung(_ n: Int, in ladder: [String]) -> String? {
        guard n >= 1, n <= ladder.count else { return nil }
        return ladder[n - 1]
    }
}

// WebJobLoad mirrors board.go's webJobLoad: zeus' two triage estimates for a
// job — `unblock`, what it costs carl to unblock it (reading, deciding,
// explaining, gathering context), and `scope`, how big the claude work that
// reply releases. Both are 1..10.
//
// Every field is non-optional even though the rest of this file makes
// omitempty keys Optional: none of these carries `omitempty` on the wire,
// because a present object always carries all four. Absence is expressed one
// level up, by `WebJob.load` being nil — so nil must never render as 0, the
// same arrangement (and the same reason) as WebTokens above.
//
// A CLEARED estimate arrives as no key at all, exactly like one that was never
// written — there is no both-zero object on the wire. api/jobload.go's clear
// writes `store.JobLoad{}`, so `At` is 0; `webJobLoadFrom` returns nil for
// `At == 0`; and `webJob.Load` is a pointer with `omitempty`. So the wire has
// ONE spelling of "no estimate" and it is `WebJob.load == nil`.
//
// `roi` below nonetheless refuses an unusable pair rather than trusting that.
// It costs one comparison, and the two shapes it catches are both things the
// board would otherwise draw as a ratio nobody wrote: a half estimate (refused
// with a 400 on the way in) and a both-zero object (a hand-edited row, since
// nothing serves one). Web's `loadROI` refuses the same two.
//
// ROI is DERIVED here, and is deliberately never on the wire. Serving it would
// put a third number beside the two it divides, free to arrive disagreeing with
// them; a derived one cannot. Same reasoning as WebTokens serving one half of
// its split and subtracting the other.
struct WebJobLoad: Decodable {
    /// 1..10 — what unblocking this costs carl. 1 = a mechanical action;
    /// 5 = moderate thought; 10 = deep thought over a lot of context.
    let unblock: Int
    /// 1..10 — the size of the claude work that unblock releases. 1 = a
    /// one-message reply; 5 = a moderate slice; 10 = a massive scoped build.
    let scope: Int
    /// zeus' one-line rationale; may be "" (server one-lines and truncates it).
    let note: String
    /// Unix millis the estimate was written, stamped server-side.
    let at: Int64

    /// `scope / unblock`, or nil when there is no usable estimate.
    ///
    /// Both numbers are guarded, not just the divisor. Neither shape can reach
    /// a client from the current server — a clear drops the key entirely and a
    /// half estimate is a 400 — so this is defence against a hand-edited `jobs`
    /// row, and it is cheap: `unblock == 0` would make the ratio infinite and
    /// `scope == 0` would draw "⚡ 0.0× 3→0", a ratio nobody wrote. Web's
    /// `loadROI` (lib/format.ts) rejects the same two, which is what keeps the
    /// boards showing the badge on the same cards.
    ///
    /// A view asks for this and omits the badge entirely on nil — including
    /// `JobCardView.showsLoadBadge`, so the card never reserves space for a
    /// badge this returns nil for.
    var roi: Double? {
        guard unblock > 0, scope > 0 else { return nil }
        return Double(scope) / Double(unblock)
    }

    /// The band `roi` falls in, nil for the same reason `roi` is.
    var band: LoadBand? { roi.map(LoadBand.forROI) }

    /// The ratio at ONE decimal place — the only rendering of it either board
    /// shows, so both the badge and the sentence below go through this.
    ///
    /// `.toNearestOrAwayFromZero` is the whole point of doing this by hand.
    /// `String(format: "%.1f",)` rounds half to EVEN, so it prints 0.25 as
    /// "0.2" while web's `toFixed(1)` prints "0.3" — and "0.3×" is the shared
    /// contract's own worked example for `unblock 8, scope 2`. Rounding half
    /// away from zero is what makes the two boards print the same string:
    /// checked over all 100 unblock×scope pairs against `toFixed(1)`, they
    /// agree exactly.
    ///
    /// Assembled from the tenths rather than formatted, which keeps this file
    /// free of `import Foundation` — it has none today, being shapes only, and
    /// `String(format:)` is the one thing here that would need it. Both halves
    /// are non-negative because `roi` is nil unless both inputs are positive.
    ///
    /// Rounding is for DISPLAY only — the band is decided on the raw ratio
    /// (`band` reads `roi`, not this), so a band never depends on a display
    /// choice. Web splits it the same way.
    private func roiText(_ roi: Double) -> String {
        let tenths = Int((roi * 10).rounded(.toNearestOrAwayFromZero))
        return "\(tenths / 10).\(tenths % 10)"
    }

    /// "⚡ 4.0× 2→8" — the badge's own text, the same string the web board
    /// renders.
    var badgeText: String? {
        guard let roi else { return nil }
        return "⚡ " + roiText(roi) + "× \(unblock)→\(scope)"
    }

    /// How long ago this estimate was written, as the app's compact age
    /// ("45s", "3m", "3h", "2d") — or nil when there is no usable stamp.
    ///
    /// The board renders this TWICE from one computation: as a muted label
    /// beside the badge, and inside `summary` below. Both are required and they
    /// are not redundant — iOS has no hover, so an age that lived only in the
    /// accessibility label would leave every sighted phone user unable to tell
    /// a three-day-old estimate from one written a minute ago, which is exactly
    /// the failure the age exists to close: zeus estimates, carl answers, the
    /// cell later dies, the daemon's `health` files the card back under
    /// BLOCKED, and with zeus gone nothing ever rewrites the estimate.
    ///
    /// `nowMillis` is passed in rather than read here — the same arrangement
    /// `wakeupCountdownLabel` uses, keeping the clock at the call site so this
    /// type stays pure. A non-positive `at` or `nowMillis` yields nil rather
    /// than an age computed from a missing stamp; the server always stamps `at`
    /// on a write, so that is defence, not an expected shape. `compactAgeLabel`
    /// floors a stamp from the FUTURE at "0s" — this phone's clock and the
    /// Mac's are independent.
    func age(nowMillis: Int64) -> String? {
        guard at > 0, nowMillis > 0 else { return nil }
        return compactAgeLabel(secondsElapsed: Int(max(0, nowMillis - at) / 1000))
    }

    /// The badge's full sentence — its tooltip on the web board, its
    /// accessibility label here:
    ///
    ///     unblock effort 2/10 · next step 8/10 · roi 4.0× · estimated 3h ago — <note>
    ///
    /// The age clause is required and its position is pinned; the age's exact
    /// wording is not — each client formats it with its own helper, and "3h"
    /// vs "3 h" between the two boards is acceptable drift. The badge TEXT is
    /// the thing that must stay byte-identical, and it never absorbs the age.
    ///
    /// The note is appended only when there is one: `note` may legitimately be
    /// "", and a trailing " — " with nothing after it reads as a truncation.
    func summary(nowMillis: Int64) -> String? {
        guard let roi else { return nil }
        var s = "unblock effort \(unblock)/10 · next step \(scope)/10 · roi " + roiText(roi) + "×"
        if let age = age(nowMillis: nowMillis) { s += " · estimated \(age) ago" }
        if !note.isEmpty { s += " — " + note }
        return s
    }
}

// JobPR mirrors one entry of a job's `prs` array: a PR url plus its
// open/merged/closed state, as returned by the board.
struct JobPR: Decodable {
    let url: String
    let state: String
}

// JobStackLink mirrors one entry of a job's `stack` array (ios/API.md's
// `jobStackLink`, internal/api/jobstack.go's webStackLink): the url the cell
// registered, its optional label, the daemon probe's last verdict on the port
// (`up` | `down` | `unknown` — a plain String so a future word decodes), and
// the registration time in unix millis.
struct JobStackLink: Decodable {
    let url: String
    let label: String
    let status: String
    let at: Int64
}

// WebDocQuestion mirrors one `questions` row (board.go's webDocQuestion /
// store.DocQuestion) — same field set, same json tags as the old
// questions.yaml record it replaces, straight field copy.
struct WebDocQuestion: Decodable, Identifiable {
    let id: String
    let text: String
    let context: String?
    let importance: String
    let recommendation: String?
    let answer: String?
    let answeredAt: String?

    /// An empty/absent `answer` means still open; `answered_at` always rides
    /// alongside a non-empty answer, so `answer` alone is the source of truth.
    var isAnswered: Bool { !(answer ?? "").isEmpty }

    enum CodingKeys: String, CodingKey {
        case id, text, context, importance, recommendation, answer
        case answeredAt = "answered_at"
    }
}

// WebFeedback mirrors one `feedback` row (board.go's webFeedback /
// store.Feedback) — a note carl left on an artifact, plus its addressed
// state. `addressed` is the nightly dream pass' free-text account of the
// DURABLE fix it shipped for the note (prose, not a boolean). That pass is
// its only writer — an ordinary cell acts on its feedback and leaves the
// column empty by design — so an empty/absent value means "no durable fix
// has shipped", NOT "nobody handled it", and a count taken off it is a
// corroboration backlog rather than work owed. Same absence-is-the-signal
// convention as WebDocQuestion.answer above, but a different question.
struct WebFeedback: Decodable, Identifiable {
    let id: Int
    let artifact: String
    let note: String
    let at: Int64
    let addressed: String?
    let addressedAt: String?

    var isAddressed: Bool { !(addressed ?? "").isEmpty }

    enum CodingKeys: String, CodingKey {
        case id, artifact, note, at, addressed
        case addressedAt = "addressed_at"
    }
}

// MARK: - the dream loop

// WebDreamReport mirrors api/dream.go's webDreamReport — one night's report
// from GET /api/dream, newest first. `body` is the raw markdown of that
// night's `00-dream-report.md`, served inline rather than as a URL because a
// report is one short page and the DREAM view renders every visible one.
// Nothing here is omitempty server-side, so nothing is Optional.
struct WebDreamReport: Decodable, Identifiable {
    /// YYYY-MM-DD, taken from the dream job's own `dream-<date>` slug.
    let date: String
    let slug: String
    let body: String

    var id: String { slug }
}

// GET /api/dream — always an array, never null (a machine that has never
// dreamt serves []).
struct DreamReportsResponse: Decodable {
    let reports: [WebDreamReport]
}

// WebWatchItem mirrors api/watchlist.go's webWatchItem — one `watchlist` row,
// both halves of the table's ownership split in one shape. The agent owns
// observation/class/state/sightings/*_seen/evidence/suggested_fix/pr_url;
// carl owns verdict/comment/commented_at, exactly as he owns
// WebDocQuestion.answer. Unlike questions and feedback, a watchlist row is
// GLOBAL — it belongs to no job, so there is no slug or job id here.
//
// Timestamps are unix millis (`first_seen`/`last_seen`/`updated_at`), except
// `commented_at`, which is RFC3339 like WebDocQuestion.answeredAt — that
// asymmetry is the server's, mirrored rather than smoothed over.
struct WebWatchItem: Decodable, Identifiable {
    let id: Int64
    let observation: String
    /// feedback|correction|repetition|waste|prompting.
    let watchClass: String
    /// watching|shipped|declined.
    let state: String
    let sightings: Int64
    let firstSeen: Int64
    let lastSeen: Int64
    /// Local-only JSON sample of the most recent occurrence; never enters git.
    let evidence: String?
    let suggestedFix: String?
    let prURL: String?
    let updatedAt: Int64
    /// ""|fine|fix — absent means carl hasn't judged this row yet.
    let verdict: String?
    /// carl's instruction for a `fix`. When present this IS the spec the next
    /// dream run follows, which is why the UI treats it as a primary control.
    let comment: String?
    let commentedAt: String?

    /// An absent/empty verdict means untouched — the agent is still watching
    /// it. Same absence-is-the-signal convention as WebFeedback.isAddressed.
    var isJudged: Bool { !(verdict ?? "").isEmpty }

    enum CodingKeys: String, CodingKey {
        case id, observation
        case watchClass = "class"
        case state, sightings
        case firstSeen = "first_seen"
        case lastSeen = "last_seen"
        case evidence
        case suggestedFix = "suggested_fix"
        case prURL = "pr_url"
        case updatedAt = "updated_at"
        case verdict, comment
        case commentedAt = "commented_at"
    }
}

// GET /api/watchlist[?state=] — always an array, never null.
struct WatchlistResponse: Decodable {
    let watchlist: [WebWatchItem]
}

// WatchVerdictRequest mirrors POST /api/watchlist's whole body: carl's
// judgement of one row and nothing else. Every agent-owned key is absent BY
// CONSTRUCTION (the server 400s a request that so much as names one), which
// is why this is its own tiny DTO rather than a partial WebWatchItem.
// `commented_at` is stamped server-side and is deliberately not sendable.
struct WatchVerdictRequest: Encodable {
    let id: Int64
    /// ""|fine|fix — empty clears the judgement.
    let verdict: String
    let comment: String
}

// MARK: - side threads

// WebThread mirrors one entry of a job's `threads` array: a side thread is a
// fork of zeus' session running as a second claude in its own tmux window of
// the same cell, so carl can ask questions without disturbing the run. A
// thread can be killed (the window dies, the conversation survives) and
// revived later, which is why `pane` comes and goes while `tid` never does.
// That is the `kind: "pane"` thread. A `kind: "headless"` thread (an ask
// thread) holds no pane at all: kraken serve runs one headless claude per
// question against the fork's session, so it is driven only through the
// thread API (send/kill/revive), and `answering` is set while a question is in
// flight. `kind` is orthogonal to `role` (`ask` | `mind`): the kind VALUE is
// "headless" rather than "ask" precisely because `role: "ask"` already rides
// this wire, so a headless thread reads `role: "ask", kind: "headless"`.
// `title`/`pane`/`model`/`closed_at`/`origin`/`answering`/`note` are
// `omitempty` on the wire → Optional here per the rule atop this file;
// `tid`/`state`/`created_at` are always present, and `kind` is present on
// every thread from a server that has it — Optional only so an older
// server's threads still decode (they read as pane threads: `isHeadless`
// false).
struct WebThread: Decodable, Identifiable {
    let tid: String
    let title: String?
    let state: String
    let pane: String?
    /// The tier the thread was branched on (`haiku`/`sonnet`/`opus`/`fable`),
    /// or nil for one branched with no pick — and equally for any thread read
    /// from a server that predates the field, which is why it is Optional
    /// rather than defaulted to a tier the thread may not be running on.
    let model: String?
    let createdAt: Int64
    let closedAt: Int64?
    /// `ask` | `mind` — additive; nil from a server predating thread roles.
    var role: String? = nil
    // The four below are `var … = nil` for the reason `role` is: the
    // memberwise initializer then lets the app's own call sites (a freshly
    // branched thread, a capture's mind thread) leave them out.
    /// `"pane"` or `"headless"` (see the comment above the struct).
    var kind: String? = nil
    /// The node and login that opened the thread; nil when the row records
    /// none.
    var origin: WebThreadOrigin? = nil
    /// True while an ask thread's question is being answered; nil otherwise.
    var answering: Bool? = nil
    /// What the last ask run left behind when it did not finish cleanly
    /// (e.g. a failure or timeout line); nil when there is none.
    var note: String? = nil

    var id: String { tid }

    /// A mind thread: legacy — captures routed to one before mind runs
    /// replaced it; existing rows are read-only history.
    var isMind: Bool { role == "mind" }

    /// An ask thread: no pane, one headless claude per question.
    var isHeadless: Bool { kind == "headless" }

    /// "live" means a claude is actually running in the thread's window;
    /// anything else (closed) is a conversation waiting to be revived —
    /// same absence-is-the-signal spirit as WebDocQuestion.isAnswered.
    var isLive: Bool { state == "live" }

    /// What the UI shows for the thread: carl's name for it, falling back to
    /// the tid for a thread branched without one.
    var displayTitle: String {
        if let title, !title.isEmpty { return title }
        return tid
    }

    enum CodingKeys: String, CodingKey {
        case tid, title, state, pane, model, role
        case createdAt = "created_at"
        case closedAt = "closed_at"
        case kind, origin, answering, note
    }
}

/// Who opened a thread: `origin` on a thread object (webThreadOrigin in
/// internal/api). All three keys are present whenever `origin` is.
struct WebThreadOrigin: Decodable {
    let nodeId: String
    let label: String
    let login: String

    enum CodingKeys: String, CodingKey {
        case nodeId = "node_id"
        case label, login
    }
}

/// The "from …" badge a thread chip carries — the same rule as the web
/// board's `threadOriginBadge` (web/_ui/src/lib/threadquick.ts): nil for a
/// non-headless thread, for a thread with no origin, and for an ask thread
/// this board's own node opened (`origin.nodeId == boardNodeId`); otherwise
/// the origin's label, falling back to its login, then its node id, and nil
/// when all three are blank. A nil `boardNodeId` compares as "": a board with
/// no tailscale or with `KRAKEN_NODES=off` states no node id, and the server
/// stamps "" as the origin node of the rows such a node opens (SelfNodeID),
/// so they are its own.
func threadOriginBadge(_ t: WebThread, boardNodeId: String?) -> String? {
    guard t.isHeadless, let origin = t.origin else { return nil }
    if origin.nodeId == (boardNodeId ?? "") { return nil }
    for candidate in [origin.label, origin.login, origin.nodeId] where !candidate.isEmpty {
        return candidate
    }
    return nil
}

// The five ops POST /api/threads accepts, spelled once so no call site can
// mistype one: the wire value is the case name (see APIClient.threadAction,
// which is the only thing that reads the rawValue).
enum ThreadOp: String {
    case branch, kill, revive, rename, send
}

// ThreadActionRequest mirrors POST /api/threads' single body shape across all
// five ops — "branch" (title + seed text, which the server requires unless
// `headless` is false), "kill"/"revive" (tid),
// "rename" (tid + title), "send" (tid + text). Unused fields stay nil and
// JSONEncoder omits them entirely, same as SpawnRequest above.
//
// The body's one other key, `model` (branch-only — the tier the new thread's
// claude runs on), is deliberately absent here: it comes from a picker, and
// this client has none, so every thread branched from iOS takes the job's
// model and then kraken's default. API.md names this struct as the body's
// mirror, so the omission is stated rather than left to be noticed.
struct ThreadActionRequest: Encodable {
    let slug: String
    let op: String
    var tid: String? = nil
    var title: String? = nil
    var text: String? = nil
    /// "branch" only: true (or absent) opens a headless ask thread, false a
    /// pane thread. nil leaves the key off, as for the fields above.
    var headless: Bool? = nil
}

// POST /api/threads' success body: the uniform {"ok": true}, plus the new
// thread's id on a "branch" (absent on every other op).
struct ThreadActionResponse: Decodable {
    let ok: Bool
    let tid: String?
}

// POST /api/job/rename's body. Here rather than beside its one call site in
// APIClient.swift, which is where the single-call-site RULE would have put it:
// ReorderRequest is the precedent for that placement and it is `private`,
// while these two are not — RenameJobResponse cannot be, because it is what
// `renameJob` RETURNS, and the pair belongs together. ThreadActionRequest
// above is the closer match: one call site, internal, in this file. The
// API-contract rule in CLAUDE.md names Models.swift as one of the three files
// a wire change lands in, which settles it.
//
// `to` is the free text the user typed, NOT a slug: the server slugifies it
// and the response says what it stored.
struct RenameJobRequest: Encodable {
    let slug: String
    let to: String
}

// POST /api/job/rename's answer. `slug` is the name the server actually
// stored and the only thing a caller may re-point to — the typed text is a
// request, never the result. `skipped` is ABSENT rather than empty when
// nothing was left behind (the Go side omits it), which is why it is the one
// Optional here; every entry is the SOURCE path of a preserved artifact copy
// that KEPT THE OLD NAME because something already sat at the new one — never
// the destination that blocked it, which is the file a reader must not act on
// — and it holds true name conflicts ONLY, a copy whose rename failed outright
// going to `notes` instead. The rename itself committed either way, so it is
// something to render rather than a failure.
//
// `notes` is the other half and is Optional for the same omitted-when-empty
// reason: the post-commit steps that FAILED, each carrying its cause — a
// preserved copy whose rename failed outright, a tmux convergence step that
// did not take, a zeus notice that did not deliver. None of them can fail a
// rename (they all run after the row has moved), which is why they need
// saying: there is no refusal to read them off.
//
// Two lists rather than one because the reader's move differs: a `skipped`
// entry is a file to move or delete, a `notes` entry is something that broke.
// Decoded by RenameDecodeTests.
struct RenameJobResponse: Decodable {
    let ok: Bool
    let slug: String
    let skipped: [String]?
    let notes: [String]?
}

// GET /api/thread/read's body: the thread's conversation (inherited pre-fork
// history included, see ReadBlock.inherited), plus the liveness/pane the
// view needs to decide whether a pane capture and a composer make sense.
// None of `counted`/`live`/`pane`/`fork_uuid`/`source` is omitempty on the
// Go side — the handler fills the whole envelope before any early return, so
// they're modeled non-optional per the rule atop this file (a closed thread
// sends `pane: ""`, not a missing key). `blocks` stays Optional only to
// mirror ReadResponse below, whose null-vs-[] the same decoder path handles.
struct ThreadReadResponse: Decodable {
    let blocks: [ReadBlock]?
    let counted: Int
    let live: Bool
    let pane: String
    let forkUUID: String
    // "transcript" when the fork's transcript was readable, "none" when
    // there's nothing to read yet (freshly branched, never spoken to).
    let source: String
    // The thread's kind ("pane"/"headless"), whether a question is in flight,
    // and the last run's note ("" when none). The server sends all three on
    // every read; Optional only so a server that predates them still decodes.
    let kind: String?
    let answering: Bool?
    let note: String?

    enum CodingKeys: String, CodingKey {
        case blocks, counted, live, pane
        case forkUUID = "fork_uuid"
        case source
        case kind, answering, note
    }
}

// WebQuestion mirrors job.go's pending-question shape: one tool call id and
// one or more questions, each with its own header and answer options.
struct WebQuestion: Decodable {
    let toolUseID: String
    let questions: [WebQuestionItem]

    enum CodingKeys: String, CodingKey {
        case toolUseID = "tool_use_id"
        case questions
    }
}

struct WebQuestionItem: Decodable {
    let question: String
    let header: String
    // `multi_select` follows this file's omitempty-is-Optional convention (a
    // Go `false` may be entirely absent on the wire), so it's decoded
    // defensively rather than via the synthesized initializer.
    let multiSelect: Bool
    let options: [WebQuestionOption]

    enum CodingKeys: String, CodingKey {
        case question, header
        case multiSelect = "multi_select"
        case options
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        question = try c.decode(String.self, forKey: .question)
        header = try c.decode(String.self, forKey: .header)
        multiSelect = try c.decodeIfPresent(Bool.self, forKey: .multiSelect) ?? false
        options = try c.decode([WebQuestionOption].self, forKey: .options)
    }
}

struct WebQuestionOption: Decodable {
    let label: String
    let description: String
}

// WebWakeup mirrors board.go's webWakeup (job wire) and peer.go's Wakeup
// (peer wire) — both serialize `reason` + `fires_at`; the peer shape also
// carries `delay_seconds`, which the UI doesn't need (it derives its own
// countdown from firesAt against wall-clock time), so one Swift type covers
// both. Hashable so it can sit on Peer, which is itself Hashable.
struct WebWakeup: Decodable, Hashable {
    let reason: String
    let firesAt: Int64

    enum CodingKeys: String, CodingKey {
        case reason
        case firesAt = "fires_at"
    }
}

// WebRosterAgent mirrors one live subagent entry in job.go's roster list.
// role/status/working are Optionals per the omitempty-is-Optional rule even
// though the server always sends them (no omitempty tag) — older servers
// predate these keys entirely, so they're absent rather than empty.
struct WebRosterAgent: Decodable {
    let name: String
    let model: String
    let role: String?
    let status: String?
    let working: Bool?
    // "teammate", or a background task's kind ("subagent" | "monitor" |
    // "shell") riding the roster — additive (job 612), decoded only: nil on a
    // pre-612 server means every entry is a teammate.
    let kind: String?
}

struct WebArtifact: Decodable, Identifiable {
    let name: String
    let url: String

    var id: String { url }
}

// MARK: - job authoring

struct JobRaw: Decodable {
    let slug: String
    let content: String
    let mtime: Int64
    let editable: Bool
}

// JobForm mirrors the map[string]any returned by GET /api/job/form. slugs
// and repos come from Go `var []string` locals that stay nil (→ JSON null)
// when empty, hence Optional even though the map always includes the key.
struct JobForm: Decodable {
    let slug: String
    let repo: String
    let base: String
    let solo: Bool
    // model is Optional for wire tolerance: an older server's payload
    // simply lacks the key, which must decode as the default.
    // skip_doc is gone (v5, breaking): the doc phase now runs purely from
    // zeus' own read of the job spec text, never a stored flag.
    // `ready` and `prereqs` are gone (v6, breaking) with the queued state:
    // there is no queue to arm into and no gate to wait on — submit starts
    // the cell synchronously. Non-optional here while they lasted, which is
    // exactly why their removal is a contract bump rather than an additive
    // one: a v6 server's payload would fail this type's decode outright.
    let model: String?
    // The job's color, "" when unset — read-only: it comes back on the form
    // GET so the picker can show the current selection, but it is written by
    // POST /api/job/color, never by saveform (see JobColorRequest). Optional
    // here for the same wire tolerance as `model`: an older server's payload
    // simply lacks the key.
    let color: String?
    // Which coding agent the cell's zeus runs — "codex" or "" (claude
    // default). Optional for the same wire tolerance as `model`/`color`; unlike
    // color it IS form-writable (see JobFormSaveRequest below).
    let agent: String?
    let spec: String
    let mtime: Int64
    let editable: Bool
    let err: String
    let slugs: [String]?
    let repos: [String]?

    enum CodingKeys: String, CodingKey {
        case slug, repo, base, solo
        case model
        case color
        case agent
        case spec, mtime, editable, err, slugs, repos
    }
}

// JobFormSaveRequest mirrors jobFormFields for POST /api/job/saveform.
// skip_doc is gone (v5, breaking); ready and prereqs are gone (v6, breaking)
// — see JobForm above. The server IGNORES unknown keys on this route (plain
// json decode, no DisallowUnknownFields), so a re-added field would be
// silently dropped — worse than a refusal; the loud retired-key refusal
// lives on POST /api/job/draft only.
struct JobFormSaveRequest: Encodable {
    let slug: String
    let repo: String
    let base: String
    let solo: Bool
    let model: String
    // agent rides here (unlike color, which has its own endpoint): it is
    // form-writable, so the create/edit form persists the claude/codex choice.
    let agent: String
    let spec: String
    let mtime: Int64

    enum CodingKeys: String, CodingKey {
        case slug, repo, base, solo
        case model
        case agent
        case spec, mtime
    }
}

// JobColorRequest is POST /api/job/color's body. Color deliberately does NOT
// ride on JobFormSaveRequest above: saveform is a FULL REPLACE, so any client
// that doesn't emit the key (every build shipped before this one) would
// silently wipe a color set from the web board. Its own endpoint also skips
// saveform's editable-job gate, which matters because recoloring a running
// job is exactly when the picker is useful. "" clears the color.
struct JobColorRequest: Encodable {
    let slug: String
    let color: String
}

// GoalUpdateRequest is POST /api/goals' body for the ONE op this app sends:
// `update`, carrying only the done flag. The endpoint also takes `add` and
// `remove` (and `text`/`parent_id` on an update) — the phone deliberately
// sends neither in v1: the drawer is for glancing at goals and checking
// them off, and every goal is composed on the Mac. A request struct with
// only the fields this client actually sends is also what keeps the app
// from ever emitting an absent-means-unchanged field as an explicit zero.
struct GoalUpdateRequest: Encodable {
    let op: String
    let id: Int64
    let done: Bool

    init(id: Int64, done: Bool) {
        self.op = "update"
        self.id = id
        self.done = done
    }
}

// MARK: - board action requests/responses

struct SlugRequest: Encodable {
    let slug: String
}

// QuestionAnswerRequest mirrors POST /api/question, which decodes the same
// webReq{slug,text} shape as every other board action (queue/done/steer) —
// the answer is carried as `text`, a single "1"-"9" digit string, not a
// separate `digit` field the Go handler doesn't know about.
struct QuestionAnswerRequest: Encodable {
    let slug: String
    let text: String
}

// DocQuestionAnswerRequest mirrors POST /api/doc-question: answers one
// doc-question row by id with a free-text answer — distinct from
// QuestionAnswerRequest (the AskUserQuestion single-digit answer flow) since
// doc questions are keyed by id and carry prose, not a 1-based option index.
struct DocQuestionAnswerRequest: Encodable {
    let slug: String
    let id: String
    let text: String
}

struct SteerRequest: Encodable {
    let slug: String
    let text: String
}

// FeedbackCreateRequest mirrors POST /api/feedback's create form:
// {slug, home, name, feedback}. `home` matches the artifact-route home set
// ("cell"|"pluto"|"archive").
struct FeedbackCreateRequest: Encodable {
    let slug: String
    let home: String
    let name: String
    let feedback: String
}

// FeedbackEditRequest mirrors POST /api/feedback's edit form:
// {slug, id, feedback} — UPDATEs the note and re-freezes the snapshot.
struct FeedbackEditRequest: Encodable {
    let slug: String
    let id: Int
    let feedback: String
}

// FeedbackDeleteRequest mirrors POST /api/feedback's delete form:
// {slug, id, delete: true}.
struct FeedbackDeleteRequest: Encodable {
    let slug: String
    let id: Int
    let delete: Bool
}

struct CreateJobRequest: Encodable {
    let slug: String
}

// GET/POST /api/draft — a job-scoped composer draft, synced upstream so a
// typed-but-unsent steer/reactivate message survives an app kill, a
// reinstall, or switching devices. `text` is always present on the wire
// ("" means no draft stored); the server clears a job's draft itself after
// a successful steer/reactivate/done/archive/delete, so clients never need
// to post an empty-text clear after one of those actions succeeds.
struct DraftResponse: Decodable {
    let slug: String
    let text: String
    let updatedAt: Int64

    enum CodingKeys: String, CodingKey {
        case slug, text
        case updatedAt = "updated_at"
    }
}

struct DraftRequest: Encodable {
    let slug: String
    let text: String
}

// webAction's uniform success body is always {"ok": true}; errors arrive as
// a non-2xx status with {"error": "..."} and are turned into KrakenError by
// APIClient before a caller ever sees this type.
struct OkResponse: Decodable {
    let ok: Bool
}

// webJobSave / webJobSaveForm always include all three keys (it's a Go map
// literal, not a tagged struct), `warn` just may be "".
struct SaveResponse: Decodable {
    let ok: Bool
    let mtime: Int64
    let warn: String
}

// MARK: - uploads

// UploadResponse mirrors POST /api/upload's success body: the stored
// filename, its absolute server-side path (the value embedded in outgoing
// composer text as "[image: <path>]"), and a server-relative url for
// fetching the image back (thumbnails).
struct UploadResponse: Decodable {
    let name: String
    let path: String
    let url: String
}

// MARK: - sessions (peers)

// Peer mirrors peer.go's Peer struct. `messageable` has no `omitempty` tag
// (always present, including `false`); everything else follows the
// omitempty-is-Optional rule above. Hashable/Identifiable so it can be used
// directly as a NavigationStack path value.
struct Peer: Decodable, Identifiable, Hashable {
    let index: Int
    let pane: String?
    let tmuxSession: String?
    let window: Int
    let pid: Int?
    let sessionID: String?
    let name: String?
    let cwd: String?
    let status: String
    let statusAt: Int64?
    // Present only when status is "polling": the pending ScheduleWakeup
    // call driving it (see core/wakeup.go).
    let wakeup: WebWakeup?
    let sessionIDGuessed: Bool?
    let branch: String?
    let model: String?
    let kind: String?
    let agent: String?
    let role: String
    let cmd: String?
    let cmdSeconds: Int64?
    let selfPeer: Bool?
    let team: String?
    let agentName: String?
    let parentSessionID: String?
    let messageable: Bool

    var id: Int { index }

    enum CodingKeys: String, CodingKey {
        case index, pane
        case tmuxSession = "tmux_session"
        case window, pid
        case sessionID = "session_id"
        case name, cwd, status
        case statusAt = "status_at"
        case wakeup
        case sessionIDGuessed = "session_id_guessed"
        case branch, model, kind, agent, role, cmd
        case cmdSeconds = "cmd_seconds"
        case selfPeer = "self"
        case team
        case agentName = "agent_name"
        case parentSessionID = "parent_session_id"
        case messageable
    }
}

struct PeersResponse: Decodable {
    let peers: [Peer]
}

// SpawnRequest mirrors POST /api/peers/spawn's webReq-style body: `kind` is
// "shell" or "claude"; `dir` empty/nil lets the server default to home;
// `name`/`prompt` are claude-only, `cmd` is shell-only. A plain struct with
// Optionals is fine here — JSONEncoder omits nil keys, so an unused field
// for the other kind never rides along on the wire.
struct SpawnRequest: Encodable {
    let kind: String
    let dir: String?
    let name: String?
    let prompt: String?
    let cmd: String?
}

struct SpawnResponse: Decodable {
    let ok: Bool
    let at: String
}

struct PaneRequest: Encodable {
    let pane: String
}

struct ReadBlock: Decodable {
    let kind: String
    let text: String
    // Present on "teammate" blocks — which teammate sent this. Additive key,
    // absent on older servers and on every other block kind.
    let from: String?
    // Side-thread reads only (GET /api/thread/read): true on the pre-fork
    // turns a thread inherited from zeus' session, so ThreadDetailView can
    // dim them and draw the "branched here" divider at the first block
    // without it. Always absent on /api/peers/read — a plain peer read has
    // no fork point, and the whole transcript renders undimmed.
    let inherited: Bool?
}

struct ReadResponse: Decodable {
    let blocks: [ReadBlock]?
    let counted: Int
}

// `key` is mutually exclusive with `message` server-side (a named key like
// "esc"/"ctrl-c" sent via tmux send-keys instead of typed text) — nil by
// default so every existing SendRequest(pane:message:) call site keeps
// compiling and omits the key on the wire (JSONEncoder skips nil Optionals
// in synthesized Encodable, same as SpawnRequest above).
struct SendRequest: Encodable {
    let pane: String
    let message: String
    var key: String? = nil
}

struct SendResponse: Decodable {
    let ok: Bool
    let queued: Bool?
    // Present + true when the target peer was a shell: the server typed the
    // line and ran it as a command rather than routing it to an agent.
    let shell: Bool?
    // Present + true when this was a key-send rather than a message-send.
    let key: Bool?
}

// GET /api/peers/capture?pane= — raw tmux capture-pane text for a shell (or
// any) pane, used by SessionDetailView's Terminal mode.
struct CaptureResponse: Decodable {
    let pane: String
    let content: String
}

struct HealthResponse: Decodable {
    let ok: Bool
    let host: String
    // Same contract-handshake fields as WebBoard above — absent on older
    // servers.
    let contract: Int?
    let stale: Bool?
}

// MARK: - usage

// UsageResponse mirrors usage.go's webUsage: the daemon's last usage-poller
// pass, one row per Claude account (main + every ~/.claude-pool slot).
// Additive endpoint, no contract bump.
struct UsageResponse: Decodable {
    let available: Bool
    let fetchedAt: Int64
    let ageSeconds: Int64
    let daemonOK: Bool
    let accounts: [UsageAccount]
    // The CODEX pool's rows — ~/.codex-pool/accounts/<slug>, a pool of its
    // own, disjoint from `accounts`' ~/.claude-pool. The server always sends
    // an array; Optional here because an older server omits the key entirely,
    // and the same row shape serves both pools (a codex row carries `session`,
    // `weekly`, `tier` and `no_creds`, and never `models`, `current` or
    // `cooldown_until`). Decode-only today: nothing on the phone renders it.
    // Added additively — no contract bump.
    let codexAccounts: [UsageAccount]?
    let codexFetchedAt: Int64?
    // Rotation mode fields (Slice C), added additively — Optional so an
    // older server (missing these keys entirely) still decodes fine.
    let rotationMode: String?
    let rotationSetBy: String?
    let rotationSetAt: Int64?

    enum CodingKeys: String, CodingKey {
        case available
        case fetchedAt = "fetched_at"
        case ageSeconds = "age_seconds"
        case daemonOK = "daemon_ok"
        case accounts
        case codexAccounts = "codex_accounts"
        case codexFetchedAt = "codex_fetched_at"
        case rotationMode = "rotation_mode"
        case rotationSetBy = "rotation_set_by"
        case rotationSetAt = "rotation_set_at"
    }
}

// UsageAccount mirrors core/usagepool.go's AccountUsage. `session`/`weekly`/
// `models` are absent together when the account hasn't been fetched yet (no
// creds / reauth needed / error with no prior data).
struct UsageAccount: Decodable, Identifiable {
    let slug: String
    let email: String?
    let displayName: String?
    let tier: String?
    let current: Bool?
    let session: UsageWindow?
    let weekly: UsageWindow?
    let models: [UsageModelWindow]?
    let fetchedAt: Int64?
    let stale: Bool?
    let error: String?
    let reauth: Bool?
    let noCreds: Bool?
    let cooldownUntil: Int64?

    var id: String { slug }

    enum CodingKeys: String, CodingKey {
        case slug, email
        case displayName = "display_name"
        case tier, current, session, weekly, models
        case fetchedAt = "fetched_at"
        case stale, error, reauth
        case noCreds = "no_creds"
        case cooldownUntil = "cooldown_until"
    }
}

struct UsageWindow: Decodable {
    let percent: Double
    let resetsAt: Int64?

    enum CodingKeys: String, CodingKey {
        case percent
        case resetsAt = "resets_at"
    }
}

// UsageModelWindow mirrors core/usagepool.go's ModelWindow — a weekly_scoped
// per-model limit (e.g. "Fable").
struct UsageModelWindow: Decodable {
    let name: String
    let percent: Double
    let resetsAt: Int64?

    enum CodingKeys: String, CodingKey {
        case name, percent
        case resetsAt = "resets_at"
    }
}

// MARK: - unified event log

// LogEntry mirrors util.LogEntry: one line of the unified event log
// (~/.kraken/logs). ts is unix millis.
struct LogEntry: Decodable {
    let ts: Int64
    let src: String
    let level: String
    let event: String
    let msg: String
}

// LogFileInfo mirrors util.LogFileInfo — one rotated log file, the viewer's
// pagination unit.
struct LogFileInfo: Decodable {
    let name: String
    let size: Int64
    let mtime: Int64
}

// GET /api/logs/files
struct LogFilesResponse: Decodable {
    let files: [LogFileInfo]
}

// GET /api/logs[?file=] — one file's parsed entries; prev names the
// next-older file ("" at the start of history), the scroll-back cursor.
struct LogReadResponse: Decodable {
    let file: String
    let prev: String
    let entries: [LogEntry]
}

// MARK: - artifact library

// The saved-artifact library (internal/api/library.go) — a snapshot table, so
// every row is FROZEN bytes rather than a pointer at a live file: a saved
// artifact stays readable after its job is torn down, archived or hard
// deleted. Additive endpoints, `apiContract` stays 6.
//
// `Codable` rather than this file's usual `Decodable`: nothing here is ever
// POSTed, but the encode half is what lets a test build a payload
// as values and round-trip it instead of hand-writing JSON. Equatable for the
// same reason.

// One saved artifact, as GET /api/library lists it. All five keys are always
// on the wire (no omitempty anywhere in the Go row type), so none is Optional
// per this file's rule atop.
//
// `Web`-prefixed rather than the bare `LibraryItem` the first cut used, and
// the prefix is load-bearing rather than stylistic: SwiftUI re-exports
// `DeveloperToolsSupport.LibraryItem`, so in any file importing both SwiftUI
// and this module the bare name is "ambiguous for type lookup" and will not
// compile. It also lands on the file's own wire-type convention and matches
// the web client's `WebLibraryItem`.
struct WebLibraryItem: Codable, Equatable, Identifiable {
    /// The `saved_artifacts` row id — the library's own identity, and what
    /// both the remove POST and the snapshot URL address. `Identifiable`'s
    /// `id` requirement is satisfied by this field directly.
    let id: Int64
    /// The job's display slug AS AT SAVE TIME, frozen with the bytes: the
    /// job it came from may since have been archived (freeing the slug) or
    /// deleted, so this is a label, never a lookup key.
    let slug: String
    /// The artifact's server-side name, slashes intact for a nested one.
    let name: String
    /// Unix millis.
    let savedAt: Int64
    /// `/api/library/artifact/<id>/<name>` — the SNAPSHOT route, not the
    /// live-artifact one. Served by the server rather than built here so the
    /// escaping is the server's own.
    let url: String

    enum CodingKeys: String, CodingKey {
        case id, slug, name
        case savedAt = "saved_at"
        case url
    }
}

// GET /api/library — `items` is always an array, never null.
struct LibraryPayload: Codable, Equatable {
    let items: [WebLibraryItem]
}

// POST /api/library's body: the same {slug, home, name} triple
// FeedbackCreateRequest carries, and for the same reason — it is the address
// of one artifact under the artifact route, recovered from its URL by
// `artifactParts(from:)`. `home` is the artifact-route home set
// ("cell"|"pluto"|"archive"). Its own tiny type rather than a reused one: the
// server refuses an unknown field outright, so a request struct may only ever
// carry the keys this endpoint names.
struct LibrarySaveRequest: Encodable {
    let slug: String
    let home: String
    let name: String
}

// POST /api/library's success body. `id` is the saved row — the SAME id on a
// re-save, since the server upserts by (job, name) rather than duplicating.
struct LibrarySaveResponse: Decodable {
    let ok: Bool
    let id: Int64
}

// POST /api/library/remove's body: the row id and nothing else.
struct LibraryRemoveRequest: Encodable {
    let id: Int64
}

// MARK: - mental model

// Carl's own committed memory (internal/api/mm.go) — one row per thing he has
// READ, DECIDED or NOTED, frozen at the moment he marked it. Additive routes
// throughout; `apiContract` stays 6.
//
// Two properties of this family drive every shape below. It is CARL'S: agents
// never write it, so every wire field here is a record of something he did.
// And it is FROZEN: an item keeps the text as it was, and a daemon pass
// invalidates it one-way when the source moves — never deletes it, never
// resurrects it. The invalidated rows are therefore half the payload rather
// than debris, which is why nothing here filters them out on the way in.
//
// `Decodable` only, and no `Equatable`: unlike the library types
// above, nothing here round-trips a payload through an encode — the tests
// build wire JSON as literals (per the contract), and every write sends its
// own small request struct instead.

// One code reading's anchor: where in which repo the frozen text was read, and
// what the last verify pass found there. Mirrors `webMMItem.anchor`, a pointer
// with `omitempty` on every empty field — so this file's rule applies
// literally, and each omitempty key is an Optional.
//
// The one pairing worth knowing when reading a row: `anchorSHA` is absent
// EXACTLY when `anchorKind` is "dirty" (text that exists in no commit has no
// commit to name). The server refuses to store any other combination, so a
// decoded value may rely on it.
struct WebMMAnchor: Decodable {
    let repo: String
    /// Relative to the repo root.
    let path: String
    /// Set once, the first time a verify followed a rename.
    let origPath: String?
    /// 1-based, inclusive.
    let startLine: Int
    let endLine: Int
    let anchorSHA: String?
    /// origin-main | merge-base | head | dirty, in descending durability.
    let anchorKind: String
    /// sha256 of the anchored text — which IS the item's `content`; there is
    /// deliberately no `text` key for a client to render a second spelling of.
    let textSHA: String
    /// Unix millis; absent until the first verify pass has looked at it.
    let checkedAt: Int64?
    let checkedSHA: String?
    /// A failed READ (offline, repo gone, timeout) — disjoint from an
    /// invalidation. A read that failed never says the code changed.
    let checkError: String?
    /// The text occurs more than once and the nearest occurrence was taken.
    /// Never an invalidation.
    let ambiguous: Bool?
    /// Where the item died, best effort.
    let invalidSHA: String?
    let invalidPR: String?

    enum CodingKeys: String, CodingKey {
        case repo, path
        case origPath = "orig_path"
        case startLine = "start_line"
        case endLine = "end_line"
        case anchorSHA = "anchor_sha"
        case anchorKind = "anchor_kind"
        case textSHA = "text_sha"
        case checkedAt = "checked_at"
        case checkedSHA = "checked_sha"
        case checkError = "check_error"
        case ambiguous
        case invalidSHA = "invalid_sha"
        case invalidPR = "invalid_pr"
    }
}

// One mental-model item, as GET /api/mm lists it.
//
// `context` is Optional against this file's usual rule — the server tags it
// without `omitempty` and always sends it, even as "". It is Optional because
// the frozen contract says so, and the reason the contract says so is that an
// absent key and an empty string mean the same thing here (no context), so a
// non-Optional would make an older or hand-built payload a hard decode
// failure over a field nothing renders when empty.
struct WebMMItem: Decodable, Identifiable {
    let id: Int64
    /// decision | reading | note.
    let kind: String
    /// question | artifact | artifact-element | code | agent-output | manual
    /// | capture (an item a mind thread committed).
    let source: String
    /// The job's DISPLAY slug at mark time, or "" for the two sources that
    /// belong to no job (code, manual). Display only — the job may since have
    /// been renamed, archived or deleted, and the row outlives all three.
    let slug: String
    /// What was marked, per source: `Q<n>`, the artifact name, the
    /// `<repo>/<path>:<start>-<end>` triple, the first 16 hex of an agent
    /// detail's sha256, or "" for a note.
    let sourceRef: String
    /// Served WHOLE, never cut. For an artifact mark it is the source ref —
    /// an artifact's content is its snapshot, which rides its own route.
    let content: String
    let context: String?
    /// Unix millis.
    let createdAt: Int64
    /// Legacy: the knowledge-graph episode once minted with the item. The
    /// server still emits the key (0 for every new row) because shipped builds
    /// decode it non-optional; optional here so a server that drops it still
    /// decodes. Nothing reads it.
    let episodeId: Int64?
    /// Unix millis. **The absence of these three keys is the wire's only
    /// spelling of "live"** — there is no boolean to read.
    let invalidatedAt: Int64?
    /// removed | content-changed | artifact-deleted | file-deleted.
    let invalidReason: String?
    let invalidDetail: String?
    /// Present on `source: "code"` only.
    let anchor: WebMMAnchor?

    // The schema-v33 capture fields. Every one Optional: a row marked before
    // the mind thread existed carries none of them (or "" for each), and a
    // server predating them sends no key at all.
    /// The one-line claim a mind thread committed; nil/"" for a legacy row.
    let title: String?
    /// `universal` | `repo`.
    let scope: String?
    let repo: String?
    /// Repo-relative; "" is the repo root.
    let dir: String?
    /// The highlighted text the capture started from.
    let quote: String?
    /// The mind thread's claude session that committed the item.
    let threadSession: String?
    /// Unix millis of the last `mm revise`; 0 or absent means never revised.
    let revisedAt: Int64?
    /// Where a capture's quote came from: the parsed `CaptureSource`, served
    /// as `capture_source` (null for a row with none). Its own key because
    /// `source` above is already the string provenance — `capture` for an
    /// item a mind thread committed.
    let captureSource: WebCaptureSource?
    /// "live" or "proposed" (schema v37). Mind runs write every item live, so
    /// "proposed" survives only on rows from before them. Absent on older
    /// servers.
    let state: String?
    /// The mind run that wrote this item (schema v39); absent means none.
    let runId: Int?
    /// The item this one replaced; that row is invalidated `superseded`.
    let supersedes: Int?
    /// The item that replaced this one.
    let supersededBy: Int?

    /// Live is the ABSENCE of `invalidated_at`, per the paragraph above. Every
    /// predicate in the app tests this rather than a stamp's value, so a
    /// server that ever sent an explicit 0 would still read as live.
    var live: Bool { invalidatedAt == nil }

    enum CodingKeys: String, CodingKey {
        case id, kind, source, slug
        case sourceRef = "source_ref"
        case content, context
        case createdAt = "created_at"
        case episodeId = "episode_id"
        case invalidatedAt = "invalidated_at"
        case invalidReason = "invalid_reason"
        case invalidDetail = "invalid_detail"
        case anchor
        case title, scope, repo, dir, quote
        case threadSession = "thread_session"
        case revisedAt = "revised_at"
        case captureSource = "capture_source"
        case state
        case runId = "run_id"
        case supersedes
        case supersededBy = "superseded_by"
    }
}

// Where a captured quote came from — `jobs.CaptureSource` / web's
// `CaptureSource` (lib/mind.ts). Encodable because the artifact viewer's
// ✓ read sends one; JSONEncoder omits every nil, so an artifact mark goes
// out as exactly `{kind, slug, home, name}`.
struct WebCaptureSource: Codable, Equatable {
    /// artifact | transcript | dir | item | manual, plus the retired nightly
    /// mind cell's job | feedback | question (schema v37; their `qid` /
    /// `feedback_id` keys are not decoded here).
    let kind: String
    var slug: String? = nil
    var home: String? = nil
    var name: String? = nil
    var sha: String? = nil
    var session: String? = nil
    var uuid: String? = nil
    var tid: String? = nil
    var repo: String? = nil
    var dir: String? = nil
    var itemId: Int64? = nil

    enum CodingKeys: String, CodingKey {
        case kind, slug, home, name, sha, session, uuid, tid, repo, dir
        case itemId = "item_id"
    }
}

// POST /api/mm/mark — highlighted text recorded as a read mark for the next
// mind run (the body `/api/mm/capture` took, which is now its alias).
struct MMCaptureRequest: Encodable {
    let slug: String
    let text: String
    let source: WebCaptureSource
}

// What /api/mm/mark answers: the stored (or folded-onto) mark's id and the
// job's pending-mark count after the write. `folded` is Optional so a server
// that ever drops the key still decodes.
struct MMMarkResult: Codable {
    let id: Int
    let pending: Int
    let folded: Bool?
}

// One contradiction flag, as GET /api/mm/flags lists it. Every field is the
// store row's, snake_case on the wire; `against_id`/`item_id` are 0 when the
// flag names no item on that side.
struct WebMMFlag: Decodable, Identifiable {
    let id: Int64
    let itemId: Int64
    let againstId: Int64
    let jobId: Int64
    let slug: String
    let session: String
    let note: String
    /// pending | raised | dismissed.
    let state: String
    let raisedBy: String
    let raiseNote: String
    let raisedAt: Int64
    let resolvedAt: Int64
    /// "" | drop | keep.
    let resolution: String
    let createdAt: Int64

    enum CodingKeys: String, CodingKey {
        case id
        case itemId = "item_id"
        case againstId = "against_id"
        case jobId = "job_id"
        case slug, session, note, state
        case raisedBy = "raised_by"
        case raiseNote = "raise_note"
        case raisedAt = "raised_at"
        case resolvedAt = "resolved_at"
        case resolution
        case createdAt = "created_at"
    }
}

// GET /api/mm/flags — newest first.
struct MMFlagsPage: Decodable {
    let flags: [WebMMFlag]
}

// POST /api/mm/flag/resolve — carl's verdict on a raised flag.
struct MMFlagResolveRequest: Encodable {
    let id: Int64
    /// drop | keep.
    let resolution: String
}

// GET /api/mm/source — an item's source, best effort. `available: false` is
// an answer, not an error.
struct MMSource: Decodable {
    let kind: String
    let available: Bool
    let changed: Bool
    let text: String
    let ext: String
}

// GET /api/mm — `items` is always an array, never null; newest first
// (created_at DESC, id DESC), and never re-sorted on this side.
struct MMPage: Decodable {
    let items: [WebMMItem]
}

// What every successful mm create answers with: `{id, episode}` for a new
// row, `{id, already: true}` for an idempotent re-mark of something already
// live. One type for both shapes rather than two, because the caller's
// question is the same either way — which row is this now? — and the server
// picks the branch, not the client.
struct MMCreated: Decodable {
    let id: Int64
    let episode: Int64?
    let already: Bool?
}

// POST /api/mm/decision — the answered question to record as a decision. The
// server copies the row's own text and answer; nothing here restates them.
struct MMDecisionRequest: Encodable {
    let slug: String
    let qid: String
}

// POST /api/mm/artifact — the {slug, home, name} triple `artifactParts`
// recovers from an artifact's own URL, exactly as LibrarySaveRequest carries
// it. Its own type for that struct's reason: the server refuses an unknown
// field outright, so a request may only ever carry the keys its route names.
struct MMArtifactRequest: Encodable {
    let slug: String
    let home: String
    let name: String
}

// POST /api/mm/element — the same triple plus the RENDERED text of what was
// selected. The client sends what it displayed; the server locates that span
// in the artifact's markdown source and stores the source lines, which is why
// the item's `content` that comes back is usually not this string.
struct MMElementRequest: Encodable {
    let slug: String
    let home: String
    let name: String
    let text: String
}

// POST /api/mm/note — a note belonging to no job and anchoring to nothing.
struct MMNoteRequest: Encodable {
    let text: String
    let context: String
}

// POST /api/mm/detail — the rendered detail line a cell last reported. Its
// text exists nowhere else on disk, which is why the item's content IS the
// record rather than a pointer to one.
struct MMDetailRequest: Encodable {
    let slug: String
    let text: String
}

// POST /api/mm/remove — the item id and nothing else. An invalidation with
// reason `removed`, never a delete.
struct MMRemoveRequest: Encodable {
    let id: Int64
}
