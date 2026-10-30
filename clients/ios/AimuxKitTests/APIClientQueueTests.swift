import Foundation
import Testing
@testable import AimuxKit

/// The chat queue's routes, and the queue and `working` on the session detail.
@Suite struct APIClientQueueTests {
    private func client(_ host: String) -> APIClient {
        APIClient(baseURL: URL(string: "https://\(host):8443")!, session: StubProtocol.session())
    }

    private func json(_ text: String) -> Data { Data(text.utf8) }

    @Test func readsTheQueueAndWorkingFromTheDetail() async throws {
        let host = "queue-detail.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-25T18:35:03Z","#
            + #""state":"open_in_terminal","turns":[],"terminal":{"status":"busy","canType":true,"working":true},"#
            + #""queue":{"state":"running","note":"Waiting","items":[{"id":"q1","text":"Next","state":"waiting","#
            + #""created":"2026-10-25T18:35:03Z"},{"id":"bad"}]}}"#
        ))))
        let detail = try await client(host).session(id: "s1", turns: 30)
        #expect(detail.terminal?.working == true)
        let queue = try #require(detail.queue)
        #expect(queue.state == "running")
        #expect(queue.note == "Waiting")
        #expect(queue.reason == nil)
        #expect(queue.items.map(\.id) == ["q1"], "a bad item is dropped, the rest still reads")
    }

    @Test func aDetailWithoutAQueueOrWorkingReadsAsNone() async throws {
        let host = "queue-none.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"id":"s1","provider":"claude","title":"T","project":"/p","lastActive":"2026-10-25T18:35:03Z","#
            + #""state":"open_in_terminal","turns":[],"terminal":{"status":"idle","canType":true}}"#
        ))))
        let detail = try await client(host).session(id: "s1", turns: 30)
        #expect(detail.queue == nil)
        #expect(detail.terminal?.working == false)
    }

    private static let queueBody = #"{"state":"running","items":[{"id":"q1","text":"Next","state":"waiting","#
        + #""created":"2026-10-25T18:35:03Z"}]}"#

    private func body(_ request: URLRequest) throws -> [String: Any] {
        let data = try #require(request.httpBody ?? request.httpBodyStream.map(readAll))
        return try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    /// A change the transport drops reaches the model as that transport error, and the queue's line names it.
    @Test func aDroppedChangeSaysTheConnectionDropped() async throws {
        let host = "queue-dropped.test"
        StubProtocol.stub(host: host, .failure(URLError(.networkConnectionLost)))
        do {
            _ = try await client(host).resumeQueue(sessionID: "s1")
            Issue.record("the resume succeeded")
        } catch {
            #expect(error as? APIError == .transport(.networkConnectionLost))
            #expect(ChatQueue.errorText(error).contains("the connection to your Mac dropped"))
        }
    }

    @Test func addsToTheQueueWithItsKey() async throws {
        let host = "queue-add.test"
        StubProtocol.stub(host: host, .success((200, json(
            #"{"id":"q1","text":"Next","state":"waiting","created":"2026-10-25T18:35:03Z"}"#))))
        let added = try await client(host).addToQueue(sessionID: "s 1", text: "Next", key: "k-1")
        #expect(added.id == "q1")
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "POST")
        #expect(request.url?.path() == "/v1/sessions/s%201/queue")
        #expect(try body(request) as? [String: String] == ["text": "Next", "key": "k-1"])
        #expect(request.value(forHTTPHeaderField: "Aimux-Interaction") == nil, "a change counts as use by itself")
    }

    @Test func readsTheQueue() async throws {
        let host = "queue-get.test"
        StubProtocol.stub(host: host, .success((200, json(Self.queueBody))))
        let queue = try await client(host).queue(sessionID: "s1")
        #expect(queue.items.map(\.text) == ["Next"])
        let request = try #require(StubProtocol.requests(host: host).first)
        #expect(request.httpMethod == "GET")
        #expect(request.url?.path() == "/v1/sessions/s1/queue")
    }

    @Test func editsAndMovesWithExactlyOneField() async throws {
        let host = "queue-patch.test"
        StubProtocol.stub(host: host, .success((200, json(Self.queueBody))))
        _ = try await client(host).editQueueItem(sessionID: "s1", itemID: "q1", text: "New")
        _ = try await client(host).moveQueueItem(sessionID: "s1", itemID: "q1", position: 2)
        let requests = StubProtocol.requests(host: host)
        #expect(requests.map(\.httpMethod) == ["PATCH", "PATCH"])
        #expect(requests.map { $0.url?.path() } == ["/v1/sessions/s1/queue/q1", "/v1/sessions/s1/queue/q1"])
        #expect(try body(requests[0]) as? [String: String] == ["text": "New"])
        #expect(try body(requests[1]) as? [String: Int] == ["position": 2])
    }

    @Test func deletesAndResumes() async throws {
        let host = "queue-delete.test"
        StubProtocol.stub(host: host, .success((200, json(Self.queueBody))))
        _ = try await client(host).deleteQueueItem(sessionID: "s1", itemID: "q1")
        _ = try await client(host).resumeQueue(sessionID: "s1")
        let requests = StubProtocol.requests(host: host)
        #expect(requests.map(\.httpMethod) == ["DELETE", "POST"])
        #expect(requests.map { $0.url?.path() } == ["/v1/sessions/s1/queue/q1", "/v1/sessions/s1/queue/resume"])
    }

    @Test(arguments: [("queue_full", APIErrorCode.queueFull), ("item_sending", .itemSending),
                      ("unknown_item", .unknownItem)])
    func queueRefusalsAreTyped(code: String, expected: APIErrorCode) async {
        let host = "queue-\(code.replacingOccurrences(of: "_", with: "-")).test"
        StubProtocol.stub(host: host, .success((409, json(#"{"error":"refused","code":"\#(code)"}"#))))
        await #expect(throws: APIError.server(status: 409, code: expected, message: "refused")) {
            _ = try await client(host).addToQueue(sessionID: "s1", text: "Next", key: "k")
        }
    }
}
