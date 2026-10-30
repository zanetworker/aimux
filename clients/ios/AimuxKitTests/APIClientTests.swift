import Foundation
import Testing
@testable import AimuxKit

@Suite struct ServerAddressTests {
    @Test(arguments: [
        ("https://my-mac.tail1234.ts.net:8443", "https://my-mac.tail1234.ts.net:8443"),
        ("my-mac.tail1234.ts.net:8443", "https://my-mac.tail1234.ts.net:8443"),
        ("  https://mac.ts.net:8443/app/ ", "https://mac.ts.net:8443"),
        ("https://mac.ts.net:8443/v1/health", "https://mac.ts.net:8443"),
        ("http://mac.ts.net:8443", "http://mac.ts.net:8443")
    ])
    func normalizesWhatPeopleType(input: String, expected: String) {
        #expect(ServerAddress.parse(input)?.absoluteString == expected)
    }

    @Test(arguments: ["", "   ", "ftp://mac.ts.net", "https://", "not a host"])
    func rejectsUnusableInput(input: String) {
        #expect(ServerAddress.parse(input) == nil)
    }
}

/// Answers requests from a per-host table so parallel tests never share a handler.
final class StubProtocol: URLProtocol, @unchecked Sendable {
    typealias Reply = (status: Int, body: Data)
    nonisolated(unsafe) private static var replies: [String: Result<Reply, URLError>] = [:]
    nonisolated(unsafe) private static var seen: [String: [URLRequest]] = [:]
    private static let lock = NSLock()

    static func stub(host: String, _ reply: Result<Reply, URLError>) {
        lock.withLock { replies[host] = reply; seen[host] = [] }
    }

    static func requests(host: String) -> [URLRequest] {
        lock.withLock { seen[host] ?? [] }
    }

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [StubProtocol.self]
        return URLSession(configuration: config)
    }

    override static func canInit(with request: URLRequest) -> Bool { true }
    override static func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func stopLoading() {}

    override func startLoading() {
        let host = request.url?.host() ?? ""
        let reply = Self.lock.withLock {
            Self.seen[host, default: []].append(request)
            return Self.replies[host]
        }
        switch reply {
        case .success(let answer)?:
            let response = HTTPURLResponse(url: request.url!, statusCode: answer.status, httpVersion: "HTTP/1.1",
                                           headerFields: ["Content-Type": "application/json"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: answer.body)
            client?.urlProtocolDidFinishLoading(self)
        case .failure(let error)?:
            client?.urlProtocol(self, didFailWithError: error)
        case nil:
            client?.urlProtocol(self, didFailWithError: URLError(.unsupportedURL))
        }
    }
}

@Suite struct APIClientTests {
    private func client(_ host: String) -> APIClient {
        APIClient(baseURL: URL(string: "https://\(host):8443")!, session: StubProtocol.session())
    }

    private func json(_ text: String) -> Data { Data(text.utf8) }

    @Test func listsSessionsWithLimitAndNoOrigin() async throws {
        let host = "list.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"sessions":[{"id":"a","provider":"claude","title":"T","project":"/p","# +
            #""lastActive":"2026-10-25T18:35:03.34Z","state":"idle"}]}"#
        ))))
        let sessions = try await client(host).sessions(limit: 7)
        #expect(sessions.map(\.id) == ["a"])
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.url?.path() == "/v1/sessions")
        #expect(request.url?.query() == "limit=7")
        #expect(request.httpMethod == "GET")
        // The server rejects browser-style requests; a native app must not send Origin.
        #expect(request.value(forHTTPHeaderField: "Origin") == nil)
    }

    @Test func readsHealth() async throws {
        let host = "health.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"status":"ok","login":"me","version":"v1","acpProtocolVersion":1,"capabilities":["sessions"]}"#
        ))))
        let health = try await client(host).health()
        #expect(health.login == "me")
        #expect(StubProtocol.requests(host: host).first?.url?.path() == "/v1/health")
    }

    @Test func searchesWithQueryAndDecodesSnippets() async throws {
        let host = "search.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"results":[{"id":"s1","provider":"claude","title":"Openshell slides","project":"/src/research","# +
            #""lastActive":"2026-10-06T09:33:21.985245368+02:00","state":"idle","snippet":"…the [OpenShell] parts"}]}"#
        ))))
        let hits = try await client(host).search("open shell & co", limit: 30)
        #expect(hits.map(\.session.id) == ["s1"])
        #expect(hits.first?.snippet == "…the [OpenShell] parts")
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.url?.path() == "/v1/search")
        let items = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []
        #expect(items.first { $0.name == "q" }?.value == "open shell & co")
        #expect(items.first { $0.name == "limit" }?.value == "30")
    }

    @Test func sendsAMessageWithItsIdempotencyKeyInTheBody() async throws {
        let host = "send.test"
        StubProtocol.stub(host: host, .success((202, json(
            #"{"id":"s1","turn":"t7","after":41,"events":"/v1/sessions/s1/events?after=41"}"#
        ))))
        let accepted = try await client(host).sendMessage(sessionID: "s1", text: "run the tests again", key: "k-1")
        #expect(accepted == TurnAccepted(id: "s1", turn: "t7", after: 41, events: "/v1/sessions/s1/events?after=41"))
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/v1/sessions/s1/messages")
        #expect(request.value(forHTTPHeaderField: "Content-Type") == "application/json")
        // URLProtocol sees the body as a stream.
        let body = try #require(request.httpBody ?? request.httpBodyStream.map(readAll))
        let sent = try JSONSerialization.jsonObject(with: body) as? [String: String]
        #expect(sent == ["text": "run the tests again", "key": "k-1"])
    }

    @Test func readsStatsWithoutAStartTime() async throws {
        let host = "nostart.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-25T18:35:03.34Z","#
            + #""state":"idle","turns":[],"stats":{"costUsd":1.25,"tokensIn":10,"tokensOut":20,"#
            + #""cacheReadTokens":30,"cacheWriteTokens":40,"turns":3,"durationMin":35.5}}"#
        ))))
        let detail = try await client(host).session(id: "s1", turns: 30)
        let stats = try #require(detail.stats, "the other numbers still show")
        #expect(stats.startTime == nil)
        #expect(stats.costUsd == 1.25)
        #expect(stats.turns == 3)
    }

    @Test func readsSessionDetailWithTurnsAndStats() async throws {
        let host = "detail.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-25T18:35:03.34Z","#
            + #""state":"idle","turns":[{"number":1,"time":"2026-10-25T18:30:00.123456789Z","user":"Hi","#
            + #""reply":"Hello","tools":[{"name":"Read","snippet":"/p/a.go","ok":true},{"name":"Bash","ok":false}]}],"#
            + #""stats":{"costUsd":1.25,"tokensIn":10,"tokensOut":20,"cacheReadTokens":30,"cacheWriteTokens":40,"#
            + #""turns":3,"startTime":"2026-10-25T18:00:00Z","durationMin":35.5,"model":"sample-model"}}"#
        ))))
        let detail = try await client(host).session(id: "s1", turns: 30)
        #expect(detail.turns.map(\.user) == ["Hi"])
        #expect(detail.turns.first?.tools == [TurnTool(name: "Read", snippet: "/p/a.go", ok: true),
                                              TurnTool(name: "Bash", snippet: "", ok: false)])
        #expect(detail.turns.first?.time == APIDate.parse("2026-10-25T18:30:00.123456789Z"))
        #expect(detail.stats?.costUsd == 1.25)
        #expect(detail.stats?.model == "sample-model")
        #expect(detail.stats?.gitBranch == nil)
        #expect(detail.state == .idle)
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.url?.path() == "/v1/sessions/s1")
        #expect(request.url?.query() == "turns=30")
    }

    @Test func detailWithoutStatsOrWithBadStatsStillReads() async throws {
        let host = "detail-nostats.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-25T18:35:03Z","#
            + #""state":"busy","turns":[],"stats":{"costUsd":"lots"}}"#
        ))))
        let detail = try await client(host).session(id: "s1", turns: 30)
        #expect(detail.stats == nil)
        #expect(detail.turns.isEmpty)
    }

    @Test func cancelPostsToTheSession() async throws {
        let host = "cancel.test"
        StubProtocol.stub(host: host, .success((202, json(#"{"status":"stopping"}"#))))
        try await client(host).cancel(sessionID: "s1")
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/v1/sessions/s1/cancel")
    }

    @Test func cancelWithNothingRunningIsTyped() async {
        let host = "cancel-idle.test"
        StubProtocol.stub(host: host, .success((409, json(#"{"error":"nothing to stop","code":"not_running"}"#))))
        await #expect(throws: APIError.server(status: 409, code: .notRunning, message: "nothing to stop")) {
            try await client(host).cancel(sessionID: "s1")
        }
    }

    @Test func eventPageAsksAfterACursorInALog() async throws {
        let host = "page.test"
        StubProtocol.stub(host: host, .success((200, json(#"{"events":[],"log":"L2"}"#))))
        let page = try await client(host).eventPage(sessionID: "s1", after: 12, log: "L1")
        #expect(page.log == "L2")
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.url?.path() == "/v1/sessions/s1/events")
        let items = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []
        #expect(items == [URLQueryItem(name: "wait", value: "0"), URLQueryItem(name: "after", value: "12"),
                          URLQueryItem(name: "log", value: "L1")])
    }

    @Test func busySessionRefusesTheMessage() async {
        let host = "send-busy.test"
        StubProtocol.stub(host: host, .success((409, json(#"{"error":"session is busy","code":"busy"}"#))))
        await #expect(throws: APIError.server(status: 409, code: .busy, message: "session is busy")) {
            try await client(host).sendMessage(sessionID: "s1", text: "hi", key: "k-2")
        }
    }

    @Test func readsTheTerminalStatusAndItsPrompt() async throws {
        let host = "terminal-detail.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-25T18:35:03Z","#
            + #""state":"open_in_terminal","turns":[],"terminal":{"status":"busy","canType":false,"#
            + #""reason":"not_at_input","prompt":{"promptId":"p-1","question":"Do you want to create a.txt?","#
            + #""detail":"Create file\na.txt","options":[{"key":"1","label":"Yes"},{"key":"2","label":"No"}]}}}"#
        ))))
        let detail = try await client(host).session(id: "s1", turns: 30)
        let terminal = try #require(detail.terminal)
        #expect(terminal.status == "busy")
        #expect(terminal.isBusy)
        #expect(!terminal.canType)
        #expect(terminal.reason == "not_at_input")
        #expect(terminal.prompt == TerminalPrompt(promptId: "p-1", question: "Do you want to create a.txt?",
                                                  detail: "Create file\na.txt",
                                                  options: [TerminalOption(key: "1", label: "Yes"),
                                                            TerminalOption(key: "2", label: "No")]))
    }

    @Test func detailWithoutOrWithABadTerminalHasNone() async throws {
        let host = "terminal-none.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-25T18:35:03Z","#
            + #""state":"open_in_terminal","turns":[]}"#
        ))))
        #expect(try await client(host).session(id: "s1", turns: 30).terminal == nil)
        let bad = "terminal-bad.test"
        StubProtocol.stub(host: bad, .success((200, json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-25T18:35:03Z","#
            + #""state":"open_in_terminal","turns":[],"terminal":{"status":"idle"}}"#
        ))))
        let detail = try await client(bad).session(id: "s1", turns: 30)
        #expect(detail.terminal == nil, "a terminal without canType is dropped, the rest still reads")
        #expect(detail.state == .openInTerminal)
    }

    @Test func typesIntoTheTerminalWithItsKey() async throws {
        let host = "terminal-send.test"
        StubProtocol.stub(host: host, .success((202, json(#"{"status":"typed"}"#))))
        try await client(host).terminalSend(sessionID: "s1", text: "are you still blocked?", key: "k-9")
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/v1/sessions/s1/terminal/messages")
        let body = try #require(request.httpBody ?? request.httpBodyStream.map(readAll))
        let sent = try JSONSerialization.jsonObject(with: body) as? [String: String]
        #expect(sent == ["text": "are you still blocked?", "key": "k-9"])
    }

    @Test func pressesThePromptOption() async throws {
        let host = "terminal-key.test"
        StubProtocol.stub(host: host, .success((202, json(#"{"status":"typed"}"#))))
        try await client(host).terminalPress(sessionID: "s1", promptID: "p-1", option: "2", key: "k-3")
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/v1/sessions/s1/terminal/keys")
        let body = try #require(request.httpBody ?? request.httpBodyStream.map(readAll))
        let sent = try JSONSerialization.jsonObject(with: body) as? [String: String]
        #expect(sent == ["promptId": "p-1", "option": "2", "key": "k-3"])
    }

    @Test(arguments: [
        ("disabled", 403, APIErrorCode.disabled), ("bad_text", 400, .badText),
        ("not_in_terminal", 409, .notInTerminal), ("not_claude", 409, .notClaude), ("mac_in_use", 409, .macInUse),
        ("no_terminal_driver", 409, .noTerminalDriver), ("automation_denied", 409, .automationDenied),
        ("not_at_input", 409, .notAtInput), ("no_prompt", 409, .noPrompt), ("prompt_changed", 409, .promptChanged),
        ("not_submitted", 409, .notSubmitted)
    ])
    func terminalRefusalsAreTyped(code: String, status: Int, expected: APIErrorCode) async {
        let host = "terminal-\(code.replacingOccurrences(of: "_", with: "-")).test"
        StubProtocol.stub(host: host, .success((status, json(#"{"error":"refused","code":"\#(code)"}"#))))
        await #expect(throws: APIError.server(status: status, code: expected, message: "refused")) {
            try await client(host).terminalSend(sessionID: "s1", text: "hi", key: "k")
        }
    }

    @Test func serverErrorBodyBecomesTypedError() async {
        let host = "forbidden.test"
        StubProtocol.stub(host: host, .success((403, json(#"{"error":"login not allowed","code":"forbidden"}"#))))
        await #expect(throws: APIError.server(status: 403, code: .forbidden, message: "login not allowed")) {
            try await client(host).sessions(limit: 50)
        }
    }

    @Test func gatewayWithoutBodyBecomesHTTPError() async {
        let host = "down.test"
        StubProtocol.stub(host: host, .success((502, Data())))
        await #expect(throws: APIError.http(status: 502)) {
            try await client(host).sessions(limit: 50)
        }
    }

    @Test func networkFailureBecomesTransportError() async {
        let host = "offline.test"
        StubProtocol.stub(host: host, .failure(URLError(.notConnectedToInternet)))
        await #expect {
            try await client(host).health()
        } throws: { error in
            if case .transport = error as? APIError { return true }
            return false
        }
    }

    @Test func unreadableSuccessBecomesDecodingError() async {
        let host = "garbage.test"
        StubProtocol.stub(host: host, .success((200, json("<html>login</html>"))))
        await #expect {
            try await client(host).sessions(limit: 50)
        } throws: { error in
            if case .decoding = error as? APIError { return true }
            return false
        }
    }
}

func readAll(_ stream: InputStream) -> Data {
    var data = Data()
    stream.open()
    defer { stream.close() }
    var buffer = [UInt8](repeating: 0, count: 4096)
    while stream.hasBytesAvailable {
        let count = stream.read(&buffer, maxLength: buffer.count)
        if count <= 0 { break }
        data.append(buffer, count: count)
    }
    return data
}
