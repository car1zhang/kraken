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

/// One networking client for the whole app: plain async/await URLSession,
/// generic get/post plumbing, and a typed convenience method per endpoint.
/// An actor so it's safe to share a single instance (and its URLSession)
/// across concurrently-refreshing views without any manual locking.
actor APIClient {
    static let shared = APIClient()

    // Mirrors web.go's apiContract constant — bump together, breaking
    // changes only (additive fields don't need a bump). See ios/API.md.
    static let expectedContract = 3

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

    // MARK: - sessions

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
