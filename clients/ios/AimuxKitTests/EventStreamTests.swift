import Foundation
import Testing
@testable import AimuxKit

/// Answers a stream request with its body cut into chunks, delivered one by one, per host.
final class SSEStubProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) private static var replies: [String: (status: Int, chunks: [Data])] = [:]
    nonisolated(unsafe) private static var seen: [String: [URLRequest]] = [:]
    private static let lock = NSLock()

    static func stub(host: String, status: Int = 200, chunks: [String]) {
        lock.withLock { replies[host] = (status, chunks.map { Data($0.utf8) }); seen[host] = [] }
    }

    static func requests(host: String) -> [URLRequest] {
        lock.withLock { seen[host] ?? [] }
    }

    static func session() -> URLSession {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [SSEStubProtocol.self]
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
        guard let reply else {
            client?.urlProtocol(self, didFailWithError: URLError(.unsupportedURL))
            return
        }
        let response = HTTPURLResponse(url: request.url!, statusCode: reply.status, httpVersion: "HTTP/1.1",
                                       headerFields: ["Content-Type": "text/event-stream"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        for chunk in reply.chunks { client?.urlProtocol(self, didLoad: chunk) }
        client?.urlProtocolDidFinishLoading(self)
    }
}

@Suite struct EventStreamTests {
    private func stream(_ host: String) -> EventStream {
        EventStream(baseURL: URL(string: "https://\(host):8443")!, session: SSEStubProtocol.session())
    }

    private func collect(_ events: AsyncThrowingStream<StreamEvent, Error>) async throws -> [StreamEvent] {
        var all: [StreamEvent] = []
        for try await event in events { all.append(event) }
        return all
    }

    private let started = #"{"seq":4,"turn":"t1","kind":"turn_started","time":"2026-10-26T08:00:00Z","#
        + #""data":{"text":"Run the checks"}}"#
    private let chunk = #"{"seq":5,"turn":"t1","kind":"update","time":"2026-10-26T08:00:01.5Z","#
        + #""data":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"On it"}}}"#

    @Test func decodesFramesSplitAcrossChunks() async throws {
        let host = "sse.test"
        let body = ": keep-alive\n\n"
            + "id: L7.4\nevent: turn_started\ndata: \(started)\n\n"
            + ": ping\n\n"
            + "id: L7.5\r\nevent: update\r\ndata: \(chunk)\r\n\r\n"
        let cut = body.index(body.startIndex, offsetBy: 60)
        let cut2 = body.index(body.startIndex, offsetBy: 190)
        SSEStubProtocol.stub(host: host, chunks: [String(body[..<cut]), String(body[cut..<cut2]),
                                                  String(body[cut2...])])

        let got = try await collect(stream(host).events(sessionID: "s1", after: 3, log: "L7"))

        #expect(got.map(\.event.seq) == [4, 5])
        #expect(got.map(\.log) == ["L7", "L7"])
        #expect(got[0].event.data == .turnStarted(text: "Run the checks"))
        #expect(got[1].event.update?.text == "On it")
        let request = try #require(SSEStubProtocol.requests(host: host).first)
        #expect(request.url?.path() == "/v1/sessions/s1/events")
        let items = URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems ?? []
        #expect(items.first { $0.name == "after" }?.value == "3")
        #expect(items.first { $0.name == "log" }?.value == "L7")
        #expect(items.first { $0.name == "wait" } == nil)
        #expect(items.first { $0.name == "queue" }?.value == "1", "queue events are asked for")
        #expect(request.value(forHTTPHeaderField: "Accept") == "text/event-stream")
    }

    @Test func aQueueEventCarriesTheQueueAndNoTurn() async throws {
        let host = "sse-queue.test"
        let queue = #"{"seq":6,"turn":"","kind":"queue","time":"2026-10-26T08:00:02Z","data":{"state":"paused","#
            + #""reason":"You used this session on your Mac","items":[{"id":"q1","text":"Run the tests","#
            + #""state":"waiting","created":"2026-10-26T08:00:00Z"}]}}"#
        SSEStubProtocol.stub(host: host, chunks: ["id: L7.6\nevent: queue\ndata: \(queue)\n\n"])

        let got = try await collect(stream(host).events(sessionID: "s1", after: 5, log: "L7"))

        #expect(got.count == 1)
        #expect(got[0].event.kind == .queue)
        #expect(got[0].event.turn == "")
        guard case .queue(let status) = got[0].event.data else {
            Issue.record("expected a queue"); return
        }
        #expect(status.state == "paused")
        #expect(status.reason == "You used this session on your Mac")
        #expect(status.items.map(\.text) == ["Run the tests"])
    }

    @Test func multiLineDataIsJoinedWithNewlines() async throws {
        let host = "sse-multi.test"
        let body = "id: L1.6\nevent: turn_started\n"
            + "data: {\"seq\":6,\"turn\":\"t2\",\"kind\":\"turn_started\",\n"
            + "data: \"time\":\"2026-10-26T08:00:00Z\",\"data\":{\"text\":\"two\"}}\n\n"
        SSEStubProtocol.stub(host: host, chunks: [body])

        let got = try await collect(stream(host).events(sessionID: "s1", after: 5, log: "L1"))

        #expect(got.map(\.event.seq) == [6])
        #expect(got.first?.event.data == .turnStarted(text: "two"))
    }

    @Test func logComesFromTheIdPrefixOrTheRequest() async throws {
        let host = "sse-log.test"
        let body = "id: L9.4\ndata: \(started)\n\n" + "id: 5\ndata: \(chunk)\n\n"
        SSEStubProtocol.stub(host: host, chunks: [body])

        let got = try await collect(stream(host).events(sessionID: "s1", after: 0, log: "L1"))

        #expect(got.map(\.log) == ["L9", "L1"])
    }

    @Test func unreadableFrameIsSkippedAndTrailingFrameWithoutBlankLineIsDropped() async throws {
        let host = "sse-bad.test"
        let body = "data: {not json\n\n" + "data: \(started)\n\n" + "data: \(chunk)\n"
        SSEStubProtocol.stub(host: host, chunks: [body])

        let got = try await collect(stream(host).events(sessionID: "s1", after: 0, log: ""))

        #expect(got.map(\.event.seq) == [4])
    }

    @Test func refusalIsATypedError() async {
        let host = "sse-refused.test"
        SSEStubProtocol.stub(host: host, status: 403, chunks: [#"{"error":"login not allowed","code":"forbidden"}"#])

        await #expect(throws: APIError.server(status: 403, code: .forbidden, message: "login not allowed")) {
            _ = try await collect(stream(host).events(sessionID: "s1", after: 0, log: ""))
        }
    }
}
