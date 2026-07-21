// Codable models mirroring kraken's `k web` JSON API (see web.go, peer.go,
// daemon.go, job.go in the repo root — this file has no logic, just shapes).
//
// Rule of thumb applied throughout: a Go struct field tagged `,omitempty`
// becomes an Optional in Swift (the key may be entirely absent, and for
// slices Go's nil-vs-empty distinction can even surface as JSON `null`;
// Swift's synthesized Decodable treats a missing key and an explicit null
// the same way for Optional properties, so this is safe either way). Fields
// with no `omitempty` tag are always present and modeled as non-optional.
// Exception: keys like `contract`/`stale` are always present on new servers
// but absent on older ones (added after this app's server-version handshake
// shipped) — those are modeled Optional too, even without omitempty, since
// the absence itself is what the app checks for.

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

    enum CodingKeys: String, CodingKey {
        case daemon
        case daemonOK = "daemon_ok"
        case cellCap = "cell_cap"
        case liveCells = "live_cells"
        case jobs
        case contract, stale
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

struct WebJob: Decodable, Identifiable {
    let slug: String
    let type: String
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
    // A done job whose zeus session id is recorded — the only ones the
    // reactivate action can actually resume.
    let reactivatable: Bool?
    // carl's own most recent message to the cell (the last me→zeus mid-run
    // log entry), independent of whether the cell is currently live.
    let lastSent: String?
    // Open/merged/closed PRs associated with this job, if any — additive key,
    // absent on older servers just like every other omitempty field here.
    let prs: [JobPR]?

    var id: String { slug }
    /// What the UI shows as the job's name (archived jobs may be numbered).
    var title: String { displaySlug ?? slug }

    enum CodingKeys: String, CodingKey {
        case slug, type, repo, state, err, outcome, cell, live
        case prereqs, detail, question, roster, artifacts, health, mtime
        case archived
        case displaySlug = "display_slug"
        case reactivatable
        case lastSent = "last_sent"
        case prs
    }
}

// JobPR mirrors one entry of a job's `prs` array: a PR url plus its
// open/merged/closed state, as returned by the board.
struct JobPR: Decodable {
    let url: String
    let state: String
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

// WebRosterAgent mirrors one live subagent entry in job.go's roster list.
struct WebRosterAgent: Decodable {
    let name: String
    let model: String
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
    let type: String
    let repo: String
    let base: String
    let ready: Bool
    let solo: Bool
    let prereqs: [Prereq]
    let spec: String
    let mtime: Int64
    let editable: Bool
    let err: String
    let slugs: [String]?
    let repos: [String]?

    enum CodingKeys: String, CodingKey {
        case slug, type, repo, base, ready, solo
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
struct JobFormSaveRequest: Encodable {
    let slug: String
    let type: String
    let repo: String
    let base: String
    let ready: Bool
    let solo: Bool
    let prereqs: [Prereq]
    let spec: String
    let mtime: Int64

    enum CodingKeys: String, CodingKey {
        case slug, type, repo, base, ready, solo
        case prereqs, spec, mtime
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

struct SteerRequest: Encodable {
    let slug: String
    let text: String
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
