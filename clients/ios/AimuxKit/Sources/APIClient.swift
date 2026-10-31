import Foundation

/// The remote API v1 client. Identity comes from Tailscale; with device login the requests also carry the
/// session's Bearer token, and a `401 login_required` waits for the unlock and is retried once.
/// URLSession sends no Origin header (the server rejects browser origins).
public final class APIClient: Sendable {
    public let baseURL: URL
    private let session: URLSession
    private let auth: DeviceLogin?

    public init(baseURL: URL, session: URLSession = .shared, auth: DeviceLogin? = nil) {
        self.baseURL = baseURL
        self.session = session
        self.auth = auth
    }

    public func health() async throws -> Health {
        try await send("GET", "/v1/health", signedIn: false)
    }

    /// Newest first; the server has no paging yet, only a limit (1-500).
    public func sessions(limit: Int = 50) async throws -> [Session] {
        try await sessionList(limit: limit).sessions
    }

    /// The sessions and the one last in front on the Mac.
    public func sessionList(limit: Int) async throws -> SessionList {
        try await get("/v1/sessions", query: [URLQueryItem(name: "limit", value: String(limit))])
    }

    /// Writes the owner's title or description, or the pin (capability `describe`).
    public func editMeta(sessionID: String, _ change: MetaChange) async throws -> SessionMeta {
        try await send("PATCH", "/v1/sessions/\(sessionID)/meta", body: try JSONEncoder().encode(change))
    }

    /// Update summary: a new title and description from the model now (capability `describe`).
    public func describe(sessionID: String) async throws -> DescribeResult {
        try await send("POST", "/v1/sessions/\(sessionID)/describe", timeout: Self.describeTimeout)
    }

    /// Keyword search over session content (needs the `search` capability); 1-100 hits.
    public func search(_ query: String, limit: Int = 30) async throws -> [SearchHit] {
        let items = [URLQueryItem(name: "q", value: query), URLQueryItem(name: "limit", value: String(limit))]
        let results: SearchResults = try await get("/v1/search", query: items)
        return results.results
    }

    /// One JSON page of the session's events (`wait=0`), from the start of the server's log.
    public func eventSnapshot(sessionID: String) async throws -> EventPage {
        try await get("/v1/sessions/\(sessionID)/events", query: [URLQueryItem(name: "wait", value: "0")])
    }

    /// One JSON page of the session's events after `after` in the log named `log` (`wait=0`). A cursor
    /// from an earlier log (the server restarted) gets the current log from its start.
    public func eventPage(sessionID: String, after: Int, log: String) async throws -> EventPage {
        let items = [URLQueryItem(name: "wait", value: "0"), URLQueryItem(name: "after", value: String(after)),
                     URLQueryItem(name: "log", value: log)]
        return try await get("/v1/sessions/\(sessionID)/events", query: items)
    }

    /// The session and its latest `turns` turns (1-200).
    public func session(id: String, turns: Int) async throws -> SessionDetail {
        try await get("/v1/sessions/\(id)", query: [URLQueryItem(name: "turns", value: String(turns))])
    }

    /// Stops the session's running turn; it ends with `turn_done` (stopReason cancelled).
    public func cancel(sessionID: String) async throws {
        let _: AnswerStatus = try await send("POST", "/v1/sessions/\(sessionID)/cancel")
    }

    /// Answers a permission request with one of its `optionId`s.
    public func answer(approvalID: String, optionID: String) async throws {
        let body = try JSONEncoder().encode(["option": optionID])
        let _: AnswerStatus = try await send("POST", "/v1/approvals/\(approvalID)", body: body)
    }

    private struct AnswerStatus: Decodable { let status: String }

    /// Starts a turn with `text`. A retry with the same `key` returns the same turn;
    /// a new attempt by the person needs a new key.
    public func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        let body = try JSONEncoder().encode(SendMessageRequest(text: text, key: key))
        return try await send("POST", "/v1/sessions/\(sessionID)/messages", body: body)
    }

    private struct SendMessageRequest: Encodable { let text: String; let key: String }
    private struct ReplaceMessageRequest: Encodable { let text: String; let key: String; let replace = true }

    /// terminalSend that first clears the text in the input box on the Mac (`replace: true`); the server refuses
    /// with `box_not_cleared`, typing nothing, when it would not empty.
    public func terminalReplace(sessionID: String, text: String, key: String) async throws {
        let body = try JSONEncoder().encode(ReplaceMessageRequest(text: text, key: key))
        let _: AnswerStatus = try await send("POST", "/v1/sessions/\(sessionID)/terminal/messages", body: body)
    }

    /// Types `text` into the session's terminal pane on the Mac, then Enter. A retry with the same `key` is not
    /// typed twice; the server never retries on its own, and neither should a caller without the person asking.
    public func terminalSend(sessionID: String, text: String, key: String) async throws {
        let body = try JSONEncoder().encode(SendMessageRequest(text: text, key: key))
        let _: AnswerStatus = try await send("POST", "/v1/sessions/\(sessionID)/terminal/messages", body: body)
    }

    /// Presses `option` (one of the prompt's keys) if the prompt showing in the terminal is still `promptID`.
    public func terminalPress(sessionID: String, promptID: String, option: String, key: String) async throws {
        let body = try JSONEncoder().encode(TerminalKeyRequest(promptId: promptID, option: option, key: key))
        let _: AnswerStatus = try await send("POST", "/v1/sessions/\(sessionID)/terminal/keys", body: body)
    }

    private struct TerminalKeyRequest: Encodable { let promptId: String; let option: String; let key: String }

    // The chat queue (capability `queue`). A change counts as use on the server by itself.

    public func queue(sessionID: String) async throws -> QueueStatus {
        try await get("/v1/sessions/\(sessionID)/queue")
    }

    /// Appends `text`; a retry with the same `key` never queues it twice.
    public func addToQueue(sessionID: String, text: String, key: String) async throws -> QueueItem {
        let body = try JSONEncoder().encode(SendMessageRequest(text: text, key: key))
        return try await send("POST", "/v1/sessions/\(sessionID)/queue", body: body)
    }

    public func editQueueItem(sessionID: String, itemID: String, text: String) async throws -> QueueStatus {
        let body = try JSONEncoder().encode(["text": text])
        return try await send("PATCH", "/v1/sessions/\(sessionID)/queue/\(itemID)", body: body)
    }

    /// `position` is 0-based among the messages not being sent.
    public func moveQueueItem(sessionID: String, itemID: String, position: Int) async throws -> QueueStatus {
        let body = try JSONEncoder().encode(["position": position])
        return try await send("PATCH", "/v1/sessions/\(sessionID)/queue/\(itemID)", body: body)
    }

    public func deleteQueueItem(sessionID: String, itemID: String) async throws -> QueueStatus {
        try await send("DELETE", "/v1/sessions/\(sessionID)/queue/\(itemID)")
    }

    /// The only way a paused queue runs again.
    public func resumeQueue(sessionID: String) async throws -> QueueStatus {
        try await send("POST", "/v1/sessions/\(sessionID)/queue/resume")
    }

    private func get<T: Decodable>(_ path: String, query: [URLQueryItem] = []) async throws -> T {
        try await send("GET", path, query: query)
    }

    /// `signedIn: false` for the routes open without a session (health), which never wait for an unlock.
    /// Update summary waits for the model: the server allows the API and then `claude -p` 60 s each.
    static let describeTimeout: TimeInterval = 130

    private func send<T: Decodable>(_ method: String, _ path: String, query: [URLQueryItem] = [],
                                    body: Data? = nil, signedIn: Bool = true,
                                    timeout: TimeInterval = 20) async throws -> T {
        var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)
        components?.path = path
        components?.queryItems = query.isEmpty ? nil : query
        guard let url = components?.url else { throw APIError.transport(.badURL) }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let body {
            request.httpBody = body
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        // Only a GET needs it: a change counts as use on the server by itself.
        if Interaction.isActive && method == "GET" { request.setValue("1", forHTTPHeaderField: Interaction.header) }
        request.timeoutInterval = timeout

        guard signedIn, let auth else { return try await perform(request) }
        var retried = false
        while true {
            let token = try await auth.credential()
            var attempt = request
            if let token { attempt.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
            do {
                return try await perform(attempt)
            } catch APIError.server(401, .loginRequired, _) where !retried {
                await auth.rejected(token: token)
                retried = true
            } catch APIError.server(401, .loginRequired, let message) {
                // Unpaired, unless another login replaced this token meanwhile: then only this request failed.
                guard await auth.refusedAfterLogin(token: token) else { throw APIError.http(status: 401) }
                throw APIError.server(status: 401, code: .loginRequired, message: message)
            }
        }
    }

    private func perform<T: Decodable>(_ request: URLRequest) async throws -> T {
        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch let error as URLError {
            throw APIError.transport(error.code)
        }
        guard let http = response as? HTTPURLResponse else { throw APIError.transport(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.from(status: http.statusCode, body: data)
        }
        do {
            return try APIDecoder.decode(T.self, from: data)
        } catch {
            throw APIError.decoding(String(describing: error))
        }
    }
}

/// Marks requests the person asked for (opening a chat or a tab, pull to refresh, Retry): they carry
/// `Aimux-Interaction: 1`, which keeps a device login from ending after an hour without use. Tasks started
/// inside inherit it, so models start their background work with `Interaction.background`.
public enum Interaction {
    @TaskLocal public static var isActive = false

    static let header = "Aimux-Interaction"

    /// Runs `operation` as something the person did.
    public static func perform<T>(isolation: isolated (any Actor)? = #isolation,
                                  _ operation: () async throws -> T) async rethrows -> T {
        try await $isActive.withValue(true, operation: operation, isolation: isolation)
    }

    /// Runs `start` (which starts a background task) outside any interaction, so the task's requests are polls.
    public static func background<T>(_ start: () throws -> T) rethrows -> T {
        try $isActive.withValue(false, operation: start)
    }
}
