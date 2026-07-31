import Foundation

enum KrakenDefaults {
    static let serverURL = "http://100.90.94.3:7777"
}

enum KrakenError: Error, LocalizedError {
    case message(String)

    var errorDescription: String? {
        switch self {
        case .message(let m): return m
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
    static let expectedContract = 5

    private let session: URLSession
    // Multi-MB image uploads over cellular can easily take longer than the
    // snappy 5s resource cap every other call uses — a dedicated session
    // with a generous resource timeout, used only by uploadImage.
    private let uploadSession: URLSession
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

    private func perform(_ request: URLRequest, session: URLSession? = nil) async throws -> Data {
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

    private func get<T: Decodable>(baseURL: String, path: String, query: [(String, String)] = []) async throws -> T {
        let url = try buildURL(baseURL: baseURL, path: path, query: query)
        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        let data = try await perform(request)
        do {
            return try decoder.decode(T.self, from: data)
        } catch {
            throw KrakenError.message(decodeErrorMessage(error, method: "GET", path: path))
        }
    }

    private func post<B: Encodable, T: Decodable>(baseURL: String, path: String, body: B) async throws -> T {
        let url = try buildURL(baseURL: baseURL, path: path)
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try encoder.encode(body)
        let data = try await perform(request)
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

    func queueJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/queue", body: SlugRequest(slug: slug))
    }

    /// Cancels a queued-but-unstarted job back to draft. Server 409s if the
    /// job isn't actually queued or already has a cell coming up.
    func dequeueJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/dequeue", body: SlugRequest(slug: slug))
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
    /// BoardColumn rawValue ("draft", "queued", ...).
    func reorderJob(baseURL: String, column: String, slugs: [String]) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/reorder",
            body: ReorderRequest(column: column, slugs: slugs)
        )
    }

    /// Boots a queued job's cell now, ahead of the daemon's own scheduling.
    func startJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/start", body: SlugRequest(slug: slug))
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
    /// server exposes: "branch" (needs `title`, optional seed `text`, and is
    /// the only op whose response carries a `tid`), "kill"/"revive" (`tid`),
    /// "rename" (`tid` + `title`), "send" (`tid` + `text`). Unused arguments
    /// stay nil and never ride on the wire. Errors — a dead cell, an unknown
    /// tid, a send to a closed thread — surface as KrakenError verbatim like
    /// every other action here.
    @discardableResult
    func threadAction(
        baseURL: String, slug: String, op: ThreadOp,
        tid: String? = nil, title: String? = nil, text: String? = nil
    ) async throws -> ThreadActionResponse {
        try await post(
            baseURL: baseURL, path: "/api/threads",
            body: ThreadActionRequest(slug: slug, op: op.rawValue, tid: tid, title: title, text: text)
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

    /// Sets (or, with an empty string, clears) a job's color. Its own
    /// endpoint rather than a saveform field — see JobColorRequest — so it
    /// never full-replaces the rest of the job file and works on a live cell.
    func setJobColor(baseURL: String, slug: String, color: String) async throws {
        let _: OkResponse = try await post(
            baseURL: baseURL, path: "/api/job/color",
            body: JobColorRequest(slug: slug, color: color)
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

    // MARK: - knowledge graph

    /// The whole viewer payload — every node with its server-computed layout
    /// position, every edge (live AND invalidated, so the screen can strike
    /// retired facts through rather than hide history), and the stats block.
    /// Additive endpoint, no contract bump.
    func fetchGraph(baseURL: String) async throws -> GraphPayload {
        try await get(baseURL: baseURL, path: "/api/graph")
    }

    /// One node's detail: its aliases (which the viewer payload deliberately
    /// omits) plus every edge touching it. The drawer asks for this on each
    /// selection; until it lands the caller falls back to the payload's own
    /// edges, so a slow or failed read degrades to fewer facts, never to none.
    func fetchGraphNode(baseURL: String, id: Int64) async throws -> GraphNodeDetail {
        try await get(baseURL: baseURL, path: "/api/graph/node", query: [("id", String(id))])
    }

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
