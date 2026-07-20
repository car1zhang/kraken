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

    private let session: URLSession
    private let decoder = JSONDecoder()
    private let encoder = JSONEncoder()

    private init() {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 5
        config.timeoutIntervalForResource = 5
        session = URLSession(configuration: config)
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

    private func perform(_ request: URLRequest) async throws -> Data {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
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

    private func get<T: Decodable>(baseURL: String, path: String, query: [(String, String)] = []) async throws -> T {
        let url = try buildURL(baseURL: baseURL, path: path, query: query)
        var request = URLRequest(url: url)
        request.httpMethod = "GET"
        let data = try await perform(request)
        do {
            return try decoder.decode(T.self, from: data)
        } catch {
            throw KrakenError.message("couldn't parse server response: \(error.localizedDescription)")
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
            throw KrakenError.message("couldn't parse server response: \(error.localizedDescription)")
        }
    }

    // MARK: - board

    func fetchBoard(baseURL: String) async throws -> WebBoard {
        try await get(baseURL: baseURL, path: "/api/board")
    }

    func queueJob(baseURL: String, slug: String) async throws {
        let _: OkResponse = try await post(baseURL: baseURL, path: "/api/queue", body: SlugRequest(slug: slug))
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

    func readPeer(baseURL: String, pane: String, n: Int) async throws -> ReadResponse {
        try await get(baseURL: baseURL, path: "/api/peers/read", query: [("pane", pane), ("n", String(n))])
    }

    func sendMessage(baseURL: String, pane: String, message: String) async throws -> SendResponse {
        try await post(baseURL: baseURL, path: "/api/peers/send", body: SendRequest(pane: pane, message: message))
    }

    /// Raw tmux capture-pane text — SessionDetailView's Terminal mode for
    /// shells (and the Terminal toggle for claude/codex peers). Same
    /// percent-encoding rule as readPeer's pane query param.
    func capturePane(baseURL: String, pane: String) async throws -> String {
        let resp: CaptureResponse = try await get(baseURL: baseURL, path: "/api/peers/capture", query: [("pane", pane)])
        return resp.content
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
