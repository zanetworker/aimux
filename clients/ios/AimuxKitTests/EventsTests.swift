import Foundation
import Testing
@testable import AimuxKit

@Suite struct EventsTests {
    private func client(_ host: String) -> APIClient {
        APIClient(baseURL: URL(string: "https://\(host):8443")!, session: StubProtocol.session())
    }

    private func fixture() throws -> Data {
        let bundle = Bundle(for: BundleToken.self)
        let url = try #require(bundle.url(forResource: "events-approval", withExtension: "json"))
        return try Data(contentsOf: url)
    }

    private func bodyData(_ request: URLRequest) -> Data? {
        if let body = request.httpBody { return body }
        guard let stream = request.httpBodyStream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 1024)
        while stream.hasBytesAvailable {
            let count = stream.read(&buffer, maxLength: buffer.count)
            if count <= 0 { break }
            data.append(buffer, count: count)
        }
        return data
    }

    @Test func decodesEveryEventKind() throws {
        let page = try APIDecoder.decode(EventPage.self, from: fixture())
        #expect(page.log == "L1")
        #expect(page.events.map(\.seq) == [1, 2, 3, 4, 5, 6, 7])
        #expect(page.events[0].data == .turnStarted(text: "Run the sample checks"))
        #expect(page.events[1].data == .update(sessionUpdate: "tool_call", title: "Run sample tests"))
        guard case .approval(let approval) = page.events[2].data else {
            Issue.record("expected approval"); return
        }
        #expect(approval.approvalId == "a1")
        #expect(page.events[3].data == .approvalResolved(approvalId: "a1", outcome: "allowed"))
        #expect(page.events[4].data == .turnDone(stopReason: "end_turn"))
        #expect(page.events[5].data == .turnError(error: "sample failure"))
        #expect(page.events[0].kind == .turnStarted)
        #expect(page.events[0].turn == "t1")
        #expect(page.events[0].time == APIDate.parse("2026-10-25T18:35:03.34Z"))
    }

    @Test func approvalCarriesOptionsAndCommand() throws {
        let page = try APIDecoder.decode(EventPage.self, from: fixture())
        guard case .approval(let approval) = page.events[2].data else {
            Issue.record("expected approval"); return
        }
        #expect(approval.options.map(\.kind) == ["allow_once", "allow_always", "reject_once"])
        #expect(approval.options.map(\.optionId) == ["o1", "o2", "o3"])
        #expect(approval.title == "Run sample tests")
        #expect(approval.command == "go test ./internal/remoteapi/... -run Push")
    }

    @Test func approvalWithoutStringCommandHasNoCommand() throws {
        let text = #"{"log":"L","events":[{"seq":1,"turn":"t","kind":"approval","time":"2026-10-25T18:35:05Z","#
            + #""data":{"approvalId":"a","title":"T","#
            + #""toolCall":{"toolCallId":"c","rawInput":{"command":7}},"options":[]}},"#
            + #"{"seq":2,"turn":"t","kind":"approval","time":"2026-10-25T18:35:05Z","#
            + #""data":{"approvalId":"b","title":"T","toolCall":{"toolCallId":"c"},"options":[]}}]}"#
        let page = try APIDecoder.decode(EventPage.self, from: Data(text.utf8))
        let commands = page.events.map { event -> String? in
            guard case .approval(let approval) = event.data else { return "wrong" }
            return approval.command
        }
        #expect(commands == [nil, nil])
    }

    @Test func unknownKindIsSkippedNotFatal() throws {
        let page = try APIDecoder.decode(EventPage.self, from: fixture())
        let last = try #require(page.events.last)
        #expect(last.kind == .unknown("telemetry"))
        #expect(last.data == .other)
    }

    @Test func malformedKnownEventIsDroppedNotFatal() throws {
        let text = #"{"log":"L","events":["#
            + #"{"seq":1,"turn":"t","kind":"turn_done","time":"2026-10-25T18:35:05Z","data":{}},"#
            + #"{"seq":2,"turn":"t","kind":"turn_done","time":"2026-10-25T18:35:06Z","#
            + #""data":{"stopReason":"end_turn"}}]}"#
        let page = try APIDecoder.decode(EventPage.self, from: Data(text.utf8))
        #expect(page.events.map(\.seq) == [2])
    }

    @Test func snapshotRequestsWaitZero() async throws {
        let host = "events.test"
        StubProtocol.stub(host: host, .success((200, try fixture())))
        let page = try await client(host).eventSnapshot(sessionID: "s1")
        #expect(page.events.count == 7)
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.url?.path() == "/v1/sessions/s1/events")
        #expect(request.url?.query() == "wait=0")
        #expect(request.httpMethod == "GET")
    }

    @Test func answerPostsOption() async throws {
        let host = "answer.test"
        StubProtocol.stub(host: host, .success((200, Data(#"{"status":"answered"}"#.utf8))))
        try await client(host).answer(approvalID: "a1", optionID: "o1")
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/v1/approvals/a1")
        #expect(request.value(forHTTPHeaderField: "Content-Type") == "application/json")
        let body = try #require(bodyData(request))
        let object = try JSONSerialization.jsonObject(with: body) as? [String: String]
        #expect(object == ["option": "o1"])
    }

    @Test func answerErrorIsTyped() async {
        let host = "answer-gone.test"
        StubProtocol.stub(host: host, .success((404, Data(#"{"error":"gone","code":"no_such_approval"}"#.utf8))))
        await #expect(throws: APIError.server(status: 404, code: .noSuchApproval, message: "gone")) {
            try await client(host).answer(approvalID: "a1", optionID: "o1")
        }
    }
}

private final class BundleToken {}
