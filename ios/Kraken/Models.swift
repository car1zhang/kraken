// Codable models mirroring kraken's `kraken serve` JSON API (see the wire
// types in internal/api/, web/serve.go, and the model layers behind them —
// internal/jobs/{daemon,job}.go, internal/core/peer.go. This file has no
// logic, just shapes; ios/API.md documents them and
// internal/api/contract_test.go pins them).
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

    enum CodingKeys: String, CodingKey {
        case daemon
        case daemonOK = "daemon_ok"
        case cellCap = "cell_cap"
        case liveCells = "live_cells"
        case jobs
        case contract, stale
        case update
        case shipIOS = "ship_ios"
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
    /// idle|preflight|fetching|merging|building|installing|restarting|done|
    /// failed|uptodate. `idle` when no run has ever happened.
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

struct WebJob: Decodable, Identifiable {
    let slug: String
    let repo: String
    let state: String
    let err: String?
    let outcome: String?
    let cell: String?
    let live: Bool
    let prereqs: [WebPrereq]?
    // The cell's last words / blocked reason — freeform prose, markdown-rendered.
    let detail: String?
    // A pending interactive question (e.g. an AskUserQuestion tool call)
    // blocking the cell, if any.
    let question: WebQuestion?
    // zeus' own refined status (running/polling/waiting/question), distinct
    // from `state` — absent when zeus isn't resolvable yet.
    let zeusStatus: String?
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
    // The job's optional color — one of red/blue/green/yellow/purple/orange/
    // pink/cyan (the names Claude Code's `/color` accepts), absent or "" when
    // the job has none. Additive key, absent on older servers, so no contract
    // bump; render through BoardTheme.jobColor, which degrades an unknown
    // name from a newer server to "no color".
    let color: String?

    var id: String { slug }
    /// What the UI shows as the job's name (archived jobs may be numbered).
    var title: String { displaySlug ?? slug }

    enum CodingKeys: String, CodingKey {
        case slug, repo, state, err, outcome, cell, live
        case prereqs, detail, question, wakeup
        case zeusStatus = "zeus_status"
        case roster, artifacts, health, mtime
        case archived
        case displaySlug = "display_slug"
        case cellGone = "cell_gone"
        case reactivatable
        case lastSent = "last_sent"
        case prs
        case docQuestions = "doc_questions"
        case feedback
        case threads
        case color
    }
}

// JobPR mirrors one entry of a job's `prs` array: a PR url plus its
// open/merged/closed state, as returned by the board.
struct JobPR: Decodable {
    let url: String
    let state: String
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
// store.Feedback) — a note carl left on an artifact, plus zeus' addressed
// state. `addressed` is zeus' own free-text account of how it addressed the
// note (prose, not a boolean) — an empty/absent value means still open,
// same absence-is-the-signal convention as WebDocQuestion.answer above.
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
// `title`/`pane`/`closed_at` are `omitempty` on the wire → Optional here per
// the rule atop this file; `tid`/`state`/`created_at` are always present.
struct WebThread: Decodable, Identifiable {
    let tid: String
    let title: String?
    let state: String
    let pane: String?
    let createdAt: Int64
    let closedAt: Int64?

    var id: String { tid }

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
        case tid, title, state, pane
        case createdAt = "created_at"
        case closedAt = "closed_at"
    }
}

// The five ops POST /api/threads accepts, spelled once so no call site can
// mistype one: the wire value is the case name (see APIClient.threadAction,
// which is the only thing that reads the rawValue).
enum ThreadOp: String {
    case branch, kill, revive, rename, send
}

// ThreadActionRequest mirrors POST /api/threads' single body shape across all
// five ops — "branch" (title + optional seed text), "kill"/"revive" (tid),
// "rename" (tid + title), "send" (tid + text). Unused fields stay nil and
// JSONEncoder omits them entirely, same as SpawnRequest above.
struct ThreadActionRequest: Encodable {
    let slug: String
    let op: String
    var tid: String? = nil
    var title: String? = nil
    var text: String? = nil
}

// POST /api/threads' success body: the uniform {"ok": true}, plus the new
// thread's id on a "branch" (absent on every other op).
struct ThreadActionResponse: Decodable {
    let ok: Bool
    let tid: String?
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

    enum CodingKeys: String, CodingKey {
        case blocks, counted, live, pane
        case forkUUID = "fork_uuid"
        case source
    }
}

struct WebPrereq: Decodable {
    let job: String
    let gated: Bool
    let done: Bool
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
    // `multi_select` follows the same omitempty-is-Optional convention as
    // Prereq.gated above (a Go `false` may be entirely absent on the wire),
    // so it's decoded defensively rather than via the synthesized initializer.
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
    let ready: Bool
    let solo: Bool
    // model is Optional for wire tolerance: an older server's payload
    // simply lacks the key, which must decode as the default.
    // skip_doc is gone (v5, breaking): the doc phase now runs purely from
    // zeus' own read of the job spec text, never a stored flag.
    let model: String?
    // The job's color, "" when unset — read-only: it comes back on the form
    // GET so the picker can show the current selection, but it is written by
    // POST /api/job/color, never by saveform (see JobColorRequest). Optional
    // here for the same wire tolerance as `model`: an older server's payload
    // simply lacks the key.
    let color: String?
    let prereqs: [Prereq]
    let spec: String
    let mtime: Int64
    let editable: Bool
    let err: String
    let slugs: [String]?
    let repos: [String]?

    enum CodingKeys: String, CodingKey {
        case slug, repo, base, ready, solo
        case model
        case color
        case prereqs, spec, mtime, editable, err, slugs, repos
    }
}

// Prereq mirrors job.go's Prereq{Job, Gated}. `gated` is `omitempty` on the
// wire (absent means false); modeled as a plain non-optional `var` here
// (rather than Bool?) since callers edit it directly in a Toggle and the
// server accepts an explicit `false` just as happily as an absent key.
struct Prereq: Codable {
    let job: String
    var gated: Bool

    enum CodingKeys: String, CodingKey { case job, gated }

    init(job: String, gated: Bool) {
        self.job = job
        self.gated = gated
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        job = try c.decode(String.self, forKey: .job)
        gated = try c.decodeIfPresent(Bool.self, forKey: .gated) ?? false
    }
}

// JobFormSaveRequest mirrors jobFormFields for POST /api/job/saveform.
// skip_doc is gone (v5, breaking) — see JobForm above.
struct JobFormSaveRequest: Encodable {
    let slug: String
    let repo: String
    let base: String
    let ready: Bool
    let solo: Bool
    let model: String
    let prereqs: [Prereq]
    let spec: String
    let mtime: Int64

    enum CodingKeys: String, CodingKey {
        case slug, repo, base, ready, solo
        case model
        case prereqs, spec, mtime
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
