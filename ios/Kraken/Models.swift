// Codable models mirroring kraken's `k web` JSON API (see web.go, peer.go,
// daemon.go, job.go in the repo root — this file has no logic, just shapes).
//
// Rule of thumb applied throughout: a Go struct field tagged `,omitempty`
// becomes an Optional in Swift (the key may be entirely absent, and for
// slices Go's nil-vs-empty distinction can even surface as JSON `null`;
// Swift's synthesized Decodable treats a missing key and an explicit null
// the same way for Optional properties, so this is safe either way). Fields
// with no `omitempty` tag are always present and modeled as non-optional.

// MARK: - board

struct WebBoard: Decodable {
    let daemon: DaemonStatus
    let daemonOK: Bool
    let cellCap: Int
    let liveCells: Int
    let jobs: [WebJob]

    enum CodingKeys: String, CodingKey {
        case daemon
        case daemonOK = "daemon_ok"
        case cellCap = "cell_cap"
        case liveCells = "live_cells"
        case jobs
    }
}

struct DaemonStatus: Decodable {
    let pid: Int
    let startedAt: Int64
    let lastTick: Int64
    let lastError: String?
    let cells: Int

    enum CodingKeys: String, CodingKey {
        case pid
        case startedAt = "started_at"
        case lastTick = "last_tick"
        case lastError = "last_error"
        case cells
    }
}

struct WebJob: Decodable, Identifiable {
    let slug: String
    let type: String
    let repo: String
    let state: String
    let err: String?
    let outcome: String?
    let success: String?
    let cell: String?
    let live: Bool
    let prereqs: [WebPrereq]?
    let escalations: [WebEsc]?
    let artifacts: [WebArtifact]?
    let health: String?
    let mtime: Int64

    var id: String { slug }
}

struct WebPrereq: Decodable {
    let job: String
    let gated: Bool
    let done: Bool
}

// webEsc carries the escalation body (the board-list Escalation type omits
// it); mtime is the escalation file's modtime as a unix seconds int64.
struct WebEsc: Decodable {
    let file: String
    let status: String
    let category: String
    let title: String
    let body: String
    let mtime: Int64
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
    let autoApprove: Bool
    let success: String
    let prereqs: [Prereq]
    let spec: String
    let mtime: Int64
    let editable: Bool
    let err: String
    let slugs: [String]?
    let repos: [String]?

    enum CodingKeys: String, CodingKey {
        case slug, type, repo, base, ready, solo
        case autoApprove = "auto_approve"
        case success, prereqs, spec, mtime, editable, err, slugs, repos
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
    let autoApprove: Bool
    let success: String
    let prereqs: [Prereq]
    let spec: String
    let mtime: Int64

    enum CodingKeys: String, CodingKey {
        case slug, type, repo, base, ready, solo
        case autoApprove = "auto_approve"
        case success, prereqs, spec, mtime
    }
}

// MARK: - board action requests/responses

struct SlugRequest: Encodable {
    let slug: String
}

struct AnswerRequest: Encodable {
    let slug: String
    let file: String
    let text: String
}

struct CreateJobRequest: Encodable {
    let slug: String
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

struct ReadBlock: Decodable {
    let kind: String
    let text: String
}

struct ReadResponse: Decodable {
    let blocks: [ReadBlock]?
    let counted: Int
}

struct SendRequest: Encodable {
    let pane: String
    let message: String
}

struct SendResponse: Decodable {
    let ok: Bool
    let queued: Bool?
}

struct HealthResponse: Decodable {
    let ok: Bool
    let host: String
}
