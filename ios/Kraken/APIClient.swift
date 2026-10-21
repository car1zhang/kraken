import Foundation

enum KrakenDefaults {
    /// Empty on a fresh install: no address is right for every operator's
    /// Mac, and a preset one pointed every new install at somebody else's
    /// tailnet. Every `@AppStorage("serverURL")` defaults to this, so an empty
    /// string IS "no server set" — `isConfigured` is the one test of it, and
    /// the board shows a first-run "set your server" state instead of polling.
    /// A URL an install already stored is untouched: @AppStorage only falls
    /// back to this default when the key has never been written.
    static let serverURL = ""

    /// Whether a stored server URL names a server at all (anything but
    /// whitespace). It does not validate the URL — a malformed one still
    /// fails loudly at the request, which is the useful error for a typo.
    static func isConfigured(_ raw: String?) -> Bool {
        !(raw ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// The stored server URL for code outside a SwiftUI view (background
    /// refresh, draft sync), `serverURL` when none was ever stored.
    static var storedServerURL: String {
        UserDefaults.standard.string(forKey: "serverURL") ?? serverURL
    }
}

enum KrakenError: Error, LocalizedError {
    case message(String)
    /// HTTP 401 from the request gate: this phone holds no pairing the server
    /// accepts (never paired, revoked, or the password changed). The board
    /// model treats it as "unpaired" — record and cookie cleared, back to the
    /// pair screen — rather than as an ordinary failed request.
    case unauthorized

    var errorDescription: String? {
        switch self {
        case .message(let m): return m
        case .unauthorized: return "not paired — pair this phone with your Mac"
        }
    }
}

private struct ErrorBody: Decodable {
    let error: String?
}

/// POST /api/reorder body — the full top-to-bottom slug order of one column.
/// Defined here rather than in Models.swift since this is the only call site.
private struct ReorderRequest: Encodable {
    let column: String
    let slugs: [String]
}

/// `{}` — the body POST /api/update wants. The handler decodes leniently
/// (an absent body is a valid "just update"), but `post` always sends one,
/// and an empty object is the honest spelling of "no force, no test".
private struct EmptyRequest: Encodable {}

/// POST /api/job/visibility body.
private struct JobVisibilityRequest: Encodable {
    let slug: String
    let visibility: String
}

/// POST /api/nodes/action body.
private struct NodeActionRequest: Encodable {
    let node: String
    let slug: String
    let action: String
    let payload: NodeActionPayload
}

/// The `payload` of a remote action: `text` for steer and question, `color`
/// for colour, `title` + `text` for thread-ask, `tid` + `text` for
/// thread-send, `tid` for thread-kill, nothing for the rest. A nil field is
/// omitted from the JSON.
struct NodeActionPayload: Encodable {
    var text: String?
    var color: String?
    var title: String?
    var tid: String?
}

/// GET /api/nodes/job/raw's reply, decoded down to the one key the phone
/// reads. The owner also sends `slug`, `mtime` and `editable`; they are
/// ignored, and `editable` would be the owner's LOCAL rule anyway — the phone
/// offers no remote edit in either mode.
struct NodeJobRaw: Decodable {
    let content: String
}

/// One networking client for the whole app: plain async/await URLSession,
/// generic get/post plumbing, and a typed convenience method per endpoint.
/// An actor so it's safe to share a single instance (and its URLSession)
/// across concurrently-refreshing views without any manual locking.
actor APIClient {
    static let shared = APIClient()

    // Mirrors web.go's apiContract constant — bump together, breaking
    // changes only (additive fields don't need a bump). See ios/API.md.
    // v5: skip_doc removed from the job form shapes, doc_questions_err
    // removed from /api/board's job objects.
    // v6: ready + prereqs removed from the job form shapes, start_error
    // removed from /api/board's job objects, the queued state retired.
    // v7: the request gate — every board route answers 401 to a phone that
    // has not paired, so a build without the pair screen cannot load at all.
    static let expectedContract = 7

    /// The cookie the server's gate reads when a request cannot carry the
    /// Authorization header (WKWebView artifact pages, AsyncImage thumbnails).
    static let pairCookieName = "kraken_pair"

    /// Adds `Authorization: Bearer <token>` when a record is present, and
    /// returns the request unchanged otherwise. `perform` runs every request
    /// this client builds through it.
    static func authorize(_ request: URLRequest, record: PairingRecord?) -> URLRequest {
        guard let record else { return request }
        var out = request
        out.setValue("Bearer \(record.token)", forHTTPHeaderField: "Authorization")
        return out
    }

    /// The `kraken_pair` cookie for a record: the token, scoped to the record
    /// URL's host (and its port when the URL names one), path `/`. nil when
    /// the record's URL has no host.
    static func pairCookie(for record: PairingRecord) -> HTTPCookie? {
        guard let url = URL(string: record.url), let host = url.host, !host.isEmpty else { return nil }
        var props: [HTTPCookiePropertyKey: Any] = [
            .name: pairCookieName,
            .value: record.token,
            .domain: host,
            .path: "/",
            .originURL: url,
        ]
        if let port = url.port { props[.port] = String(port) }
        return HTTPCookie(properties: props)
    }

    static func installCookie(record: PairingRecord, into storage: HTTPCookieStorage) {
        guard let cookie = pairCookie(for: record) else { return }
        storage.setCookie(cookie)
    }

    /// Removes every `kraken_pair` cookie set for `url`'s host.
    static func clearCookie(for url: String, in storage: HTTPCookieStorage) {
        guard let host = URL(string: url)?.host?.lowercased() else { return }
        for cookie in storage.cookies ?? [] where cookie.name == pairCookieName
            && cookie.domain.trimmingCharacters(in: CharacterSet(charactersIn: ".")).lowercased() == host {
            storage.deleteCookie(cookie)
        }
    }

    /// The record `perform` authorizes with: `stored`, but only for a request
    /// to the host (case-insensitive) and port it was issued by — a missing
    /// port reads as the scheme's default (80 for http, 443 for https). The
    /// token is a credential for ONE node; sending it to whatever host the
    /// server field happens to name would hand that node a token it could
    /// replay against ours. Pure.
    static func record(for request: URLRequest, stored: PairingRecord?) -> PairingRecord? {
        guard let stored,
              let paired = URL(string: stored.url), let target = request.url,
              let pairedHost = paired.host?.lowercased(), !pairedHost.isEmpty,
              pairedHost == target.host?.lowercased(),
              effectivePort(paired) == effectivePort(target) else { return nil }
        return stored
    }

    private static func effectivePort(_ url: URL) -> Int {
        if let port = url.port { return port }
        return url.scheme?.lowercased() == "https" ? 443 : 80
    }

    private let session: URLSession
    // Multi-MB image uploads over cellular can easily take longer than the
    // snappy 5s resource cap every other call uses — a dedicated session
    // with a generous resource timeout, used only by uploadImage.
    private let uploadSession: URLSession
    // POST /api/queue is the SUBMIT, and since the queued state was retired
    // it starts the cell inline — a git worktree, a tmux session and a claude
    // spawn all inside the request. That is seconds, not milliseconds, so it
    // cannot ride the 5s cap: a timeout there reports failure for a cell that
    // is in fact coming up, which is the one wrong answer available here.
    private let submitSession: URLSession
    // A remote `thread-ask` is relayed to the owner, which looks the cell up
    // and starts a claude process before it answers; the paired node gives
    // the `thread-ask` relay 30 s. On the 5s cap a slow owner would read as
    // a failure for a thread that was in fact created, and the natural retry
    // would open a second one. `thread-send` shares this session only for
    // symmetry: the relay caps a send at its 5 s default anyway, so the
    // longer client timeout buys a send nothing. Used only by nodeAction for
    // those two actions.
    private let askSession: URLSession
    private let decoder = JSONDecoder()
    private let encoder = JSONEncoder()

    private init() {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 5
        config.timeoutIntervalForResource = 5
        session = URLSession(configuration: config)

        let uploadConfig = URLSessionConfiguration.ephemeral
        uploadConfig.timeoutIntervalForResource = 60
        uploadSession = URLSession(configuration: uploadConfig)

        let submitConfig = URLSessionConfiguration.ephemeral
        submitConfig.timeoutIntervalForRequest = 60
        submitConfig.timeoutIntervalForResource = 60
        submitSession = URLSession(configuration: submitConfig)

        let askConfig = URLSessionConfiguration.ephemeral
        askConfig.timeoutIntervalForRequest = 30
        askConfig.timeoutIntervalForResource = 30
        askSession = URLSession(configuration: askConfig)
    }

    // MARK: - low-level plumbing

    // URLQueryItem's automatic percent-encoding is not reliable for "%" and
    // "+" across Foundation versions, so every query value is escaped by
    // hand against the strictest allowed set (letters/digits only). This
    // matters most for tmux pane ids like "%3": the server expects the raw
    // "%" to arrive as "%25" so its one round of URL-decoding yields "%3"
    // back rather than "3".
    private func percentEncode(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? s
    }

    private func buildURL(baseURL: String, path: String, query: [(String, String)] = []) throws -> URL {
        // No server set (a fresh install): refuse before building anything.
        // "" + "/api/board" parses as a RELATIVE URL, so without this the
        // request would be issued and fail later with a confusing
        // "unsupported URL" instead of saying what is actually missing.
        guard KrakenDefaults.isConfigured(baseURL) else {
            throw KrakenError.message("no server set — enter your Mac's address in Settings")
        }
        // Settings lets the user type a server URL freehand; trim a trailing
        // slash so "http://host:7777/" + "/api/board" doesn't produce a
        // double slash (net/http's ServeMux would 301-redirect it, which
        // URLSession follows anyway, but there's no reason to pay for it).
        let trimmedBase = baseURL.hasSuffix("/") ? String(baseURL.dropLast()) : baseURL
        var urlString = trimmedBase + path
        if !query.isEmpty {
            let pairs = query.map { "\($0.0)=\(percentEncode($0.1))" }
            urlString += "?" + pairs.joined(separator: "&")
        }
        guard let url = URL(string: urlString) else {
            throw KrakenError.message("invalid server URL: \(urlString)")
        }
        return url
    }

    /// The token a request to `baseURL` would carry given `stored` — the
    /// same host/port rule `perform` applies, so a caller that hands `perform`
    /// that record knows exactly which token (if any) a 401 answered.
    static func tokenSent(to baseURL: String, stored: PairingRecord?) -> String? {
        guard let url = URL(string: baseURL) else { return nil }
        return record(for: URLRequest(url: url), stored: stored)?.token
    }

    /// `stored` is the record to authorize with; it defaults to a fresh
    /// Keychain read. A caller that must tell "my token was refused" from "a
    /// tokenless or stale request was refused" reads the record once and
    /// passes it, so the token it compares is the one actually sent.
    private func perform(_ request: URLRequest, session: URLSession? = nil,
                         stored: PairingRecord? = PairingStore.load()) async throws -> Data {
        let request = Self.authorize(request, record: Self.record(for: request, stored: stored))
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await (session ?? self.session).data(for: request)
        } catch {
            throw KrakenError.message(error.localizedDescription)
        }
        guard let http = response as? HTTPURLResponse else {
            throw KrakenError.message("no HTTP response from server")
        }
        if http.statusCode == 401 {
            throw KrakenError.unauthorized
        }
        guard (200...299).contains(http.statusCode) else {
            if let body = try? decoder.decode(ErrorBody.self, from: data),
               let msg = body.error, !msg.isEmpty {
                throw KrakenError.message(msg)
            }
            throw KrakenError.message("server returned HTTP \(http.statusCode)")
        }
        return data
    }

    // Turns a DecodingError into a message naming the request and the exact
    // key path/expectation that failed, instead of Swift's generic
    // localizedDescription — this is what used to show up as an opaque
    // "couldn't parse server response" no matter what actually drifted on
    // the wire. Non-DecodingError failures (e.g. malformed JSON entirely)
    // keep the generic wording, since there's no key path to report.
    private func decodeErrorMessage(_ error: Error, method: String, path: String) -> String {
        guard let decodingError = error as? DecodingError else {
            return "couldn't parse server response: \(error.localizedDescription)"
        }
        let prefix = "\(method) \(path) — "
        // array indices render as bracketed segments (jobs[3].live) rather
        // than dotted ones (jobs.Index 3.live), and attach without a
        // leading dot.
        func keyPath(_ codingPath: [CodingKey]) -> String {
            var result = ""
            for key in codingPath {
                if let index = key.intValue {
                    result += "[\(index)]"
                } else {
                    result += result.isEmpty ? key.stringValue : ".\(key.stringValue)"
                }
            }
            return result
        }
        switch decodingError {
        case .typeMismatch(_, let context):
            let fullPath = keyPath(context.codingPath)
            return fullPath.isEmpty
                ? "\(prefix)\(context.debugDescription)"
                : "\(prefix)key \"\(fullPath)\": \(context.debugDescription)"
        case .keyNotFound(let key, let context):
            let fullPath = context.codingPath.isEmpty ? key.stringValue : keyPath(context.codingPath) + "." + key.stringValue
            return "\(prefix)missing key \"\(fullPath)\": \(context.debugDescription)"
        case .valueNotFound(let type, let context):
            let fullPath = keyPath(context.codingPath)
            return fullPath.isEmpty
                ? "\(prefix)expected \(type), found null"
                : "\(prefix)key \"\(fullPath)\": expected \(type), found null"
        case .dataCorrupted(let context):
            let fullPath = keyPath(context.codingPath)
            return fullPath.isEmpty
                ? "\(prefix)malformed response: \(context.debugDescription)"
                : "\(prefix)key \"\(fullPath)\": \(context.debugDescription)"
        @unknown default:
            return "\(prefix)couldn't parse server response: \(decodingError.localizedDescription)"
        }
    }

    private func get<T: Decodable>(baseURL: String, path: String, query: [(String, String)] = [],
                                   stored: PairingRecord? = PairingStore.load()) async throws -> T {
        let url = try buildURL(baseURL: baseURL, path: path, query: query)
        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        let data = try await perform(request, stored: stored)
        do {
            return try decoder.decode(T.self, from: data)
        } catch {
            throw KrakenError.message(decodeErrorMessage(error, method: "GET", path: path))
        }
    }

    private func post<B: Encodable, T: Decodable>(baseURL: String, path: String, body: B,
                                                  session: URLSession? = nil) async throws -> T {
        let url = try buildURL(baseURL: baseURL, path: path)
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try encoder.encode(body)
        let data = try await perform(request, session: session)
        do {
            return try decoder.decode(T.self, from: data)
        } catch {
            throw KrakenError.message(decodeErrorMessage(error, method: "POST", path: path))
        }
    }

    // MARK: - board

    func fetchBoard(baseURL: String) async throws -> WebBoard {
        try await get(baseURL: baseURL, path: "/api/board")
    }

    /// The board, authorized with exactly `stored` (see `perform`'s `stored`).
    func fetchBoard(baseURL: String, stored: PairingRecord?) async throws -> WebBoard {
        try await get(baseURL: baseURL, path: "/api/board", stored: stored)
    }

    /// Submits a draft: the server starts its cell inline and answers only
    /// once that has happened (or 409s with why it could not). The URL kept
    /// its name through the queued state's retirement; the meaning did not —
    /// there is nothing to wait in any more, so this is the start.
    ///
    /// Runs on `submitSession` rather than the shared 5s one — see its
    /// declaration for why.
    func queueJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/queue",
                                           body: SlugRequest(slug: slug), session: submitSession)
    }

    func markDone(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/done", body: SlugRequest(slug: slug))
    }

    func retryJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/retry", body: SlugRequest(slug: slug))
    }

    func killJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/kill", body: SlugRequest(slug: slug))
    }

    /// Answers a pending WebQuestionItem by its 1-based option index within
    /// that question's own options list.
    func answerQuestion(baseURL: String, slug: String, digit: Int) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/question",
            body: QuestionAnswerRequest(slug: slug, text: String(digit))
        )
    }

    /// Answers (or re-answers) one open doc question by id with a free-text
    /// answer — distinct from answerQuestion's 1-based digit answer to an
    /// AskUserQuestion prompt. No liveness requirement server-side: this is a
    /// plain `questions` row UPDATE in kraken.db, so it lands even if zeus/
    /// the cell's tmux session is dead, or the job is done — rows are always
    /// answerable, and editability is free (a re-answer just overwrites).
    func answerDocQuestion(baseURL: String, slug: String, id: String, text: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/doc-question",
            body: DocQuestionAnswerRequest(slug: slug, id: id, text: text)
        )
    }

    /// Attaches a new feedback note to an artifact (home: "cell"|"pluto"|
    /// "archive", matching the artifact-route home set). Works for done/
    /// archived jobs too — feedback rows are the only store.
    func createFeedback(baseURL: String, slug: String, home: String, name: String, feedback: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/feedback",
            body: FeedbackCreateRequest(slug: slug, home: home, name: name, feedback: feedback)
        )
    }

    /// Edits an existing feedback note's text; the server re-freezes its
    /// snapshot from the artifact as currently served.
    func editFeedback(baseURL: String, slug: String, id: Int, feedback: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/feedback",
            body: FeedbackEditRequest(slug: slug, id: id, feedback: feedback)
        )
    }

    /// Deletes a feedback note.
    func deleteFeedback(baseURL: String, slug: String, id: Int) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/feedback",
            body: FeedbackDeleteRequest(slug: slug, id: id, delete: true)
        )
    }

    /// Free-text direction routed to a live cell like a terminal message.
    /// 409s (e.g. the cell isn't actually live) surface via KrakenError same
    /// as every other action.
    func steer(baseURL: String, slug: String, text: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/steer",
            body: SteerRequest(slug: slug, text: text)
        )
    }

    /// Moves a job (file + preserved artifacts) into the archive, freeing
    /// its slug. Server refuses jobs with a cell in flight — kill first.
    func archiveJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/archive", body: SlugRequest(slug: slug))
    }

    /// Restores an archived job (slug = the numbered archive file base)
    /// under its original slug, state preserved.
    func unarchiveJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/unarchive", body: SlugRequest(slug: slug))
    }

    /// Permanently deletes an archived job file and its archived artifact
    /// copies. Only archive names resolve server-side — nothing active can
    /// be deleted.
    func deleteArchivedJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/delete", body: SlugRequest(slug: slug))
    }

    /// Resumes a done job's original zeus session for adjustments; `text`
    /// is carl's optional steering message, folded into the resume prompt.
    func reactivateJob(baseURL: String, slug: String, text: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/reactivate",
            body: SteerRequest(slug: slug, text: text)
        )
    }

    /// Persists a column's full top-to-bottom slug order after a board drag
    /// reorder (see BoardView's within-column drag). `column` is a
    /// BoardColumn rawValue ("draft", "working", ...).
    func reorderJob(baseURL: String, column: String, slugs: [String]) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/reorder",
            body: ReorderRequest(column: column, slugs: slugs)
        )
    }

    /// Interrupts a working cell, moving the job to blocked.
    func stopJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/stop", body: SlugRequest(slug: slug))
    }

    // MARK: - the dream loop

    /// Every night report the machine has kept, newest first. Additive
    /// endpoint, no contract bump. A night still in progress has no report
    /// yet and simply isn't in the list.
    func fetchDreamReports(baseURL: String) async throws -> DreamReportsResponse {
        try await get(baseURL: baseURL, path: "/api/dream")
    }

    /// Watchlist rows, newest-observation-first. `state` narrows to one of
    /// watching|shipped|declined; nil/empty means every row (the server 400s
    /// an unrecognized state rather than serving an empty list, so a typo is
    /// loud rather than indistinguishable from "nothing to review").
    func fetchWatchlist(baseURL: String, state: String? = nil) async throws -> WatchlistResponse {
        let query = (state?.isEmpty == false) ? [("state", state!)] : []
        return try await get(baseURL: baseURL, path: "/api/watchlist", query: query)
    }

    /// carl's verdict on one watchlist row. `verdict` is ""|fine|fix — empty
    /// with an empty comment clears the judgement outright. `commented_at` is
    /// stamped server-side and is deliberately not ours to send.
    func setWatchVerdict(baseURL: String, id: Int64, verdict: String, comment: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/watchlist",
            body: WatchVerdictRequest(id: id, verdict: verdict, comment: comment)
        )
    }

    // MARK: - side threads

    /// One side-thread mutation, all five ops through the one endpoint the
    /// server exposes: "branch" (needs `title` and a seed `text` — optional
    /// only for a pane thread, `headless: false` — and is the only op whose
    /// response carries a `tid`), "kill"/"revive" (`tid`),
    /// "rename" (`tid` + `title`), "send" (`tid` + `text`). Unused arguments
    /// stay nil and never ride on the wire. Errors — a dead cell, an unknown
    /// tid, a send to a closed thread — surface as KrakenError verbatim like
    /// every other action here.
    /// `headless` rides only on "branch": nil leaves the key off (the server
    /// then opens a headless ask thread), false asks for a pane thread.
    @discardableResult
    func threadAction(
        baseURL: String, slug: String, op: ThreadOp,
        tid: String? = nil, title: String? = nil, text: String? = nil,
        headless: Bool? = nil
    ) async throws -> ThreadActionResponse {
        try await post(
            baseURL: baseURL, path: "/api/threads",
            body: ThreadActionRequest(slug: slug, op: op.rawValue, tid: tid, title: title, text: text,
                                      headless: headless)
        )
    }

    /// One thread's last `n` conversation blocks (inherited pre-fork history
    /// included), plus its liveness and pane. Readable whether or not the
    /// thread is live — a closed thread still has its transcript.
    func readThread(baseURL: String, slug: String, tid: String, n: Int) async throws -> ThreadReadResponse {
        try await get(
            baseURL: baseURL, path: "/api/thread/read",
            query: [("slug", slug), ("tid", tid), ("n", String(n))]
        )
    }

    // MARK: - drafts

    /// A job-scoped composer draft (empty `text` means none stored).
    func getDraft(baseURL: String, slug: String) async throws -> DraftResponse {
        try await get(baseURL: baseURL, path: "/api/draft", query: [("slug", slug)])
    }

    /// Uploads the current draft text for a job; empty text clears it
    /// server-side. Last write wins.
    func postDraft(baseURL: String, slug: String, text: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/draft", body: DraftRequest(slug: slug, text: text))
    }

    // MARK: - job authoring

    func fetchJobRaw(baseURL: String, slug: String) async throws -> JobRaw {
        try await get(baseURL: baseURL, path: "/api/job/raw", query: [("slug", slug)])
    }

    func createJob(baseURL: String, slug: String) async throws -> JobRaw {
        try await post(baseURL: baseURL, path: "/api/job/create", body: CreateJobRequest(slug: slug))
    }

    func fetchJobForm(baseURL: String, slug: String) async throws -> JobForm {
        try await get(baseURL: baseURL, path: "/api/job/form", query: [("slug", slug)])
    }

    func saveJobForm(baseURL: String, form: JobFormSaveRequest) async throws -> SaveResponse {
        try await post(baseURL: baseURL, path: "/api/job/saveform", body: form)
    }

    /// Renames a job. `slug` is its CURRENT name; `to` is free text the server
    /// slugifies. Its own endpoint rather than a saveform field for
    /// setJobColor's reason and one more: saveform full-replaces the job file
    /// and is refused outright while a cell is in flight, whereas identity is
    /// not a form field and a live cell is exactly where renaming is useful.
    ///
    /// A refusal (a taken slug, a reserved dream-/graph- prefix, an archived
    /// job's name, another active job's former name) comes back as the
    /// endpoint's own 409 sentence, which the caller shows beside the input.
    func renameJob(baseURL: String, slug: String, to: String) async throws -> RenameJobResponse {
        try await post(baseURL: baseURL, path: "/api/job/rename", body: RenameJobRequest(slug: slug, to: to))
    }

    /// Sets (or, with an empty string, clears) a job's color. Its own
    /// endpoint rather than a saveform field — see JobColorRequest — so it
    /// never full-replaces the rest of the job file and works on a live cell.
    func setJobColor(baseURL: String, slug: String, color: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/job/color",
            body: JobColorRequest(slug: slug, color: color)
        )
    }

    /// Sets a LOCAL job's visibility: "private" (never leaves this Mac),
    /// "readonly" (the default — other boards see it, nobody touches it) or
    /// "public" (other krakens may drive it). A one-field route like colour,
    /// so it works on a live cell.
    func setJobVisibility(baseURL: String, slug: String, visibility: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/job/visibility",
            body: JobVisibilityRequest(slug: slug, visibility: visibility)
        )
    }

    // MARK: - pairing and the fleet

    /// Revokes THIS phone's pairing on the paired node (no id: a paired caller
    /// revokes itself). Log out calls it before wiping the Keychain record.
    func revokePairing(baseURL: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/pair/revoke", body: EmptyRequest())
    }

    /// One action on a REMOTE cell, through the paired node's proxy: `node`
    /// is the owner's node id, `action` one of steer, question, kill, done,
    /// submit, archive, color, stop (public cards only) or thread-ask,
    /// thread-send, thread-kill (readonly and public). The remote node's own
    /// answer is relayed back, so a refusal reads exactly as it would
    /// locally. Submit starts the remote cell inline and gets the long session
    /// for the same reason `queueJob` does; thread-ask and thread-send get
    /// `askSession`'s 30 s (see its declaration).
    func nodeAction(baseURL: String, node: String, slug: String, action: String,
                    payload: NodeActionPayload = NodeActionPayload()) async throws {
        let url = try buildURL(baseURL: baseURL, path: "/api/nodes/action")
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try encoder.encode(NodeActionRequest(node: node, slug: slug, action: action, payload: payload))
        let session: URLSession?
        switch action {
        case "submit": session = submitSession
        case "thread-ask", "thread-send": session = askSession
        default: session = nil
        }
        _ = try await perform(request, session: session)
    }

    /// A remote job's raw text, relayed by the paired node from its owner's
    /// `GET /api/node/job/raw` — the remote sheet's Spec section reads it.
    /// Readonly and public jobs both answer; private or unknown is a 404.
    func fetchNodeJobRaw(baseURL: String, node: String, slug: String) async throws -> NodeJobRaw {
        try await get(baseURL: baseURL, path: "/api/nodes/job/raw", query: [("node", node), ("slug", slug)])
    }

    /// A remote job's side thread, relayed by the paired node from its
    /// owner's `GET /api/node/thread/read` — the same body as the local
    /// `readThread`. `n` rides only when given.
    func fetchNodeThreadRead(baseURL: String, node: String, slug: String, tid: String,
                             n: Int?) async throws -> ThreadReadResponse {
        var query = [("node", node), ("slug", slug), ("tid", tid)]
        if let n { query.append(("n", String(n))) }
        return try await get(baseURL: baseURL, path: "/api/nodes/thread/read", query: query)
    }

    // MARK: - goals

    /// Ticks (or unticks) one goal. The only write the phone makes to
    /// /api/goals: adding, editing and deleting goals are web-only in v1, so
    /// there is no addGoal/deleteGoal twin here on purpose. The new state
    /// rides back on the next /api/board poll (`goals`), same as every other
    /// board write — nothing to echo.
    func setGoalDone(baseURL: String, id: Int64, done: Bool) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/goals",
            body: GoalUpdateRequest(id: id, done: done)
        )
    }

    // MARK: - sessions

    // MARK: - unified event log

    func fetchLogFiles(baseURL: String) async throws -> LogFilesResponse {
        try await get(baseURL: baseURL, path: "/api/logs/files")
    }

    /// file == nil fetches the newest log file; a name (from a previous
    /// response's `prev`) fetches that older file for scroll-back.
    func fetchLog(baseURL: String, file: String? = nil) async throws -> LogReadResponse {
        var path = "/api/logs"
        if let file, !file.isEmpty {
            let escaped = file.addingPercentEncoding(withAllowedCharacters: .urlQueryAllowed) ?? file
            path += "?file=" + escaped
        }
        return try await get(baseURL: baseURL, path: path)
    }

    func fetchHealth(baseURL: String) async throws -> HealthResponse {
        try await get(baseURL: baseURL, path: "/api/health")
    }

    func fetchPeers(baseURL: String) async throws -> PeersResponse {
        try await get(baseURL: baseURL, path: "/api/peers")
    }

    /// Spawns a new peer — a plain shell or a Claude session — in a fresh
    /// tmux pane. `dir` nil/empty defaults to home server-side; `name`/
    /// `prompt` only apply to `kind: "claude"`, `cmd` only to `kind: "shell"`.
    func spawnPeer(
        baseURL: String, kind: String, dir: String?, name: String?, prompt: String?, cmd: String?
    ) async throws -> SpawnResponse {
        try await post(
            baseURL: baseURL, path: "/api/peers/spawn",
            body: SpawnRequest(kind: kind, dir: dir, name: name, prompt: prompt, cmd: cmd)
        )
    }

    /// Kills a peer's tmux pane. The server refuses job-cell panes (those
    /// come down via the job's own Kill action); the UI hides the affordance
    /// for them too rather than relying solely on the 409.
    func killPeer(baseURL: String, pane: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/peers/kill", body: PaneRequest(pane: pane))
    }

    func readPeer(baseURL: String, pane: String, n: Int) async throws -> ReadResponse {
        try await get(baseURL: baseURL, path: "/api/peers/read", query: [("pane", pane), ("n", String(n))])
    }

    func sendMessage(baseURL: String, pane: String, message: String) async throws -> SendResponse {
        try await post(baseURL: baseURL, path: "/api/peers/send", body: SendRequest(pane: pane, message: message))
    }

    /// Sends a single named key (esc, tab, an arrow, ctrl-<letter>...) to a
    /// pane via tmux send-keys — no trailing Enter, for interrupting a
    /// running turn or navigating a prompt from a keyboard that can't type
    /// these keys. `message` is sent as "" (not omitted): the server's
    /// mutual-exclusion check only rejects a *non-empty* message alongside a
    /// key.
    func sendKey(baseURL: String, pane: String, key: String) async throws -> SendResponse {
        try await post(baseURL: baseURL, path: "/api/peers/send", body: SendRequest(pane: pane, message: "", key: key))
    }

    /// Raw tmux capture-pane text — SessionDetailView's Terminal mode for
    /// shells (and the Terminal toggle for claude/codex peers). Same
    /// percent-encoding rule as readPeer's pane query param.
    func capturePane(baseURL: String, pane: String) async throws -> String {
        let resp: CaptureResponse = try await get(baseURL: baseURL, path: "/api/peers/capture", query: [("pane", pane)])
        return resp.content
    }

    // MARK: - uploads

    /// Uploads one composer image for `slug` — multipart/form-data with a
    /// `slug` text field and a `file` field carrying the image bytes.
    /// Foundation has no multipart encoder, so the body is hand-assembled;
    /// everything else (URL building, non-2xx {"error":...} decoding into
    /// KrakenError, decode-failure messaging) reuses the same plumbing as
    /// every JSON call above.
    func uploadImage(
        baseURL: String, slug: String, data: Data, filename: String, mimeType: String
    ) async throws -> UploadResponse {
        let url = try buildURL(baseURL: baseURL, path: "/api/upload")
        let boundary = "KrakenBoundary-\(UUID().uuidString)"
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")

        var body = Data()
        func appendString(_ s: String) {
            body.append(s.data(using: .utf8)!)
        }
        appendString("--\(boundary)\r\n")
        appendString("Content-Disposition: form-data; name=\"slug\"\r\n\r\n")
        appendString("\(slug)\r\n")
        appendString("--\(boundary)\r\n")
        appendString("Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n")
        appendString("Content-Type: \(mimeType)\r\n\r\n")
        body.append(data)
        appendString("\r\n--\(boundary)--\r\n")
        request.httpBody = body

        let respData = try await perform(request, session: uploadSession)
        do {
            return try decoder.decode(UploadResponse.self, from: respData)
        } catch {
            throw KrakenError.message(decodeErrorMessage(error, method: "POST", path: "/api/upload"))
        }
    }

    // MARK: - usage

    /// The daemon's last usage-poller pass — one row per Claude account.
    /// Additive endpoint, no contract bump.
    func fetchUsage(baseURL: String) async throws -> UsageResponse {
        try await get(baseURL: baseURL, path: "/api/usage")
    }

    // MARK: - self-update

    /// Starts a self-update: fetch → merge → `make install` → restart the
    /// daemon. Returns as soon as the claim is written and the detached
    /// updater is forked (202) — the run itself is followed via
    /// `updateStatus`, never by this call returning.
    ///
    /// Refusals are 409s carrying the machine's own sentence ("… has
    /// uncommitted changes", "… is on carl/foo, not main", "update already
    /// in flight"), which `perform` turns into a `KrakenError.message` — so
    /// a refusal is a thrown error whose text is worth showing verbatim,
    /// not a generic failure. Additive endpoint, no contract bump.
    func startUpdate(baseURL: String) async throws -> UpdateStartResponse {
        try await post(baseURL: baseURL, path: "/api/update", body: EmptyRequest())
    }

    /// The same `webUpdate` shape /api/board carries under `update`, on its
    /// own endpoint so a ~1s in-flight poll doesn't re-run discovery every
    /// tick. Failures here are EXPECTED for a few seconds around the end of
    /// a run — the update's last act restarts the server answering this —
    /// so the caller swallows them rather than reporting them.
    func updateStatus(baseURL: String) async throws -> WebUpdate {
        try await get(baseURL: baseURL, path: "/api/update/status")
    }

    // MARK: - ship to TestFlight

    /// Starts a TestFlight ship: xcodegen → archive → export → upload to App
    /// Connect. Returns as soon as the claim is written and the detached
    /// shipper is forked (202) — the run itself, which takes minutes, is
    /// followed via `shipIOSStatus`.
    ///
    /// Note what a completed run does and does not mean: the .ipa reaches App
    /// Store Connect, TestFlight then processes it, and only afterwards can
    /// this very app be replaced by the new build — from the TestFlight app,
    /// by hand. Nothing here installs anything.
    ///
    /// Refusals are 409s carrying the machine's own sentence ("App Store
    /// Connect credentials missing: ASC_KEY_ID not set…", "ship already in
    /// flight"), which `perform` turns into a `KrakenError.message` — worth
    /// showing verbatim. Additive endpoint, no contract bump.
    func startShipIOS(baseURL: String) async throws -> ShipIOSStartResponse {
        try await post(baseURL: baseURL, path: "/api/ship-ios", body: EmptyRequest())
    }

    /// The same `webShipIOS` shape /api/board carries under `ship_ios`, on its
    /// own endpoint so a ~1.2s in-flight poll doesn't re-run discovery every
    /// tick. Unlike `updateStatus`, failures here are NOT expected: a ship
    /// restarts neither `kraken serve` nor the daemon, so the server should be
    /// answering for the whole run and the caller reports a persistent failure
    /// rather than swallowing it.
    func shipIOSStatus(baseURL: String) async throws -> WebShipIOS {
        try await get(baseURL: baseURL, path: "/api/ship-ios/status")
    }

    // MARK: - artifact library

    /// Every saved artifact, newest first — the server's own order, which the
    /// client never re-sorts. Additive endpoint, no contract bump.
    func fetchLibrary(baseURL: String) async throws -> LibraryPayload {
        try await get(baseURL: baseURL, path: "/api/library")
    }

    /// Freezes one artifact's CURRENT bytes into the library and returns the
    /// row id. The {slug, home, name} triple is the artifact-route address,
    /// exactly as `createFeedback` sends it — recovered from the artifact's
    /// own URL by `artifactParts(from:)`, never from a display name.
    ///
    /// Re-saving the same artifact is an upsert server-side: the snapshot and
    /// the timestamp are replaced, the row id stays, nothing duplicates.
    @discardableResult
    func saveToLibrary(baseURL: String, slug: String, home: String, name: String) async throws -> Int64 {
        let response: LibrarySaveResponse = try await post(
            baseURL: baseURL, path: "/api/library",
            body: LibrarySaveRequest(slug: slug, home: home, name: name)
        )
        return response.id
    }

    /// Drops a saved row by its library id (never by slug/name — the job it
    /// came from may not exist any more).
    func removeFromLibrary(baseURL: String, id: Int64) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/library/remove",
            body: LibraryRemoveRequest(id: id)
        )
    }

    // MARK: - mental model

    /// Everything carl has marked, newest first — invalidated rows included,
    /// because those are half the point (the frozen text is what he knew, and
    /// the app renders it back against a source that no longer says it).
    ///
    /// No `?live=1` variant: the memory pane filters client-side so the same
    /// one payload serves the pane, the artifact viewer's ✓ toggle and the
    /// job sheet's decision marks, and re-reading the wire per surface would
    /// give three surfaces three different opinions about one list.
    func fetchMM(baseURL: String) async throws -> MMPage {
        try await get(baseURL: baseURL, path: "/api/mm")
    }

    /// Marks a whole artifact read, freezing its CURRENT bytes as the item's
    /// snapshot. The {slug, home, name} triple is the artifact-route address
    /// `saveToLibrary` sends, recovered the same way (`artifactParts`).
    ///
    /// Idempotent on identical bytes — the server answers `already` with the
    /// existing id. DIFFERENT bytes supersede the earlier mark and insert a
    /// new one, so the list never shows two live "I read this" rows for one
    /// file. Either way the answer names the row that is now live.
    @discardableResult
    func markArtifactRead(baseURL: String, slug: String, home: String, name: String) async throws -> MMCreated {
        try await post(
            baseURL: baseURL, path: "/api/mm/artifact",
            body: MMArtifactRequest(slug: slug, home: home, name: name)
        )
    }

    /// Marks one SELECTION inside an artifact read. `text` is what the client
    /// RENDERED — the server locates that span in the artifact's markdown
    /// source and stores the source lines, so structure (pipes, list markers,
    /// fences) survives a mark made from a rendered view.
    @discardableResult
    func markElementRead(baseURL: String, slug: String, home: String, name: String, text: String) async throws -> MMCreated {
        try await post(
            baseURL: baseURL, path: "/api/mm/element",
            body: MMElementRequest(slug: slug, home: home, name: name, text: text)
        )
    }

    /// Records an ANSWERED doc question as a decision carl has taken. The
    /// server copies the row's own text, answer, context and recommendation;
    /// an unanswered question is a 400, since there is no decision yet.
    @discardableResult
    func markDecision(baseURL: String, slug: String, qid: String) async throws -> MMCreated {
        try await post(
            baseURL: baseURL, path: "/api/mm/decision",
            body: MMDecisionRequest(slug: slug, qid: qid)
        )
    }

    /// Marks a cell's rendered detail line read. The text goes on the wire
    /// because it exists nowhere else — the line is replaced on every zeus
    /// turn, so there is no address to send instead of the sentence itself.
    @discardableResult
    func markDetail(baseURL: String, slug: String, text: String) async throws -> MMCreated {
        try await post(
            baseURL: baseURL, path: "/api/mm/detail",
            body: MMDetailRequest(slug: slug, text: text)
        )
    }

    /// Writes a free-standing note into the mental model. Belongs to no job
    /// and anchors to nothing, and has no idempotence probe: two identical
    /// notes are two things carl thought.
    @discardableResult
    func addMMNote(baseURL: String, text: String, context: String) async throws -> MMCreated {
        try await post(
            baseURL: baseURL, path: "/api/mm/note",
            body: MMNoteRequest(text: text, context: context)
        )
    }

    /// Carl unmarking: invalidates the item with reason `removed` and closes
    /// the facts compiled from it. Never a delete — the row stays readable —
    /// and re-removing an already-invalidated item is a 200 that changes
    /// nothing, so a double tap is safe.
    func dropMM(baseURL: String, id: Int64) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/mm/remove",
            body: MMRemoveRequest(id: id)
        )
    }

    /// Routes highlighted text to the job's mind thread — a fork of zeus that
    /// turns it into a committed item. Answers the thread's tid; `created`
    /// says whether this capture branched the thread or was sent to the live
    /// one. A job with no live cell is a 409 whose message is surfaced as is.
    func captureToMind(
        baseURL: String, slug: String, home: String, name: String, text: String
    ) async throws -> MMCaptured {
        try await post(
            baseURL: baseURL, path: "/api/mm/capture",
            body: MMCaptureRequest(
                slug: slug, text: text,
                source: WebCaptureSource(kind: "artifact", slug: slug, home: home, name: name)
            )
        )
    }

    /// Contradiction flags, newest first; `state` narrows to one of
    /// pending | raised | dismissed | resolved, nil lists every flag.
    func fetchMMFlags(baseURL: String, state: String?) async throws -> MMFlagsPage {
        var query: [(String, String)] = []
        if let state, !state.isEmpty { query.append(("state", state)) }
        return try await get(baseURL: baseURL, path: "/api/mm/flags", query: query)
    }

    /// Carl's verdict on a RAISED flag: `drop` also invalidates the flagged
    /// item, `keep` only resolves the flag.
    func resolveMMFlag(baseURL: String, id: Int64, resolution: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/mm/flag/resolve",
            body: MMFlagResolveRequest(id: id, resolution: resolution)
        )
    }

    /// An item's source text (frozen artifact bytes, the transcript window,
    /// a dir listing, a code anchor's text), or `available: false`.
    func fetchMMSource(baseURL: String, id: Int64) async throws -> MMSource {
        try await get(baseURL: baseURL, path: "/api/mm/source", query: [("id", String(id))])
    }

    // NB: there is deliberately no markCode verb. `POST /api/mm/code` needs an
    // absolute WORKTREE path, a repo-relative file and a start row — the Zed
    // capture task's world, not the phone's. A phone has no checkout to name.

    // MARK: - artifacts

    /// Fetches a non-html artifact's raw body as text (webArtifact.url is
    /// already a full server-relative path, e.g. "/api/artifact/x/cell/y").
    /// Used by ArtifactView to markdown-render .md files and dark-shell
    /// .log/.txt files instead of loading them raw in the WKWebView.
    func fetchArtifactText(baseURL: String, path: String) async throws -> String {
        let url = try buildURL(baseURL: baseURL, path: path)
        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        let data = try await perform(request)
        guard let text = String(data: data, encoding: .utf8) else {
            throw KrakenError.message("artifact wasn't valid UTF-8 text")
        }
        return text
    }
}
