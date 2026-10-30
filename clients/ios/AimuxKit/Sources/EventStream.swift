import Foundation

/// One event from the stream and the log it belongs to (the server's log id; it changes when the server restarts).
public struct StreamEvent: Equatable, Sendable {
    public let event: SessionEvent
    public let log: String

    public init(event: SessionEvent, log: String) {
        self.event = event
        self.log = log
    }
}

public protocol EventStreaming: Sendable {
    /// The session's events after `after` in the log `log`, as they happen. Ends when the server closes it.
    func events(sessionID: String, after: Int, log: String) -> AsyncThrowingStream<StreamEvent, Error>
}

/// Follows `GET /v1/sessions/{id}/events` as server-sent events, with the device login's Bearer token
/// (a stream never counts as use, so it never sends `Aimux-Interaction`). Frames are `id: <log>.<seq>`,
/// `event: <kind>` and `data: <Event JSON>`; comment lines keep the connection alive.
public struct EventStream: EventStreaming {
    public let baseURL: URL
    private let session: URLSession
    private let auth: DeviceLogin?

    /// The server sends a keep-alive every 15 s, so a longer silence means the connection is gone.
    private static let idleTimeout: TimeInterval = 45
    private static let errorBodyLimit = 65_536

    public init(baseURL: URL, session: URLSession = .shared, auth: DeviceLogin? = nil) {
        self.baseURL = baseURL
        self.session = session
        self.auth = auth
    }

    public func events(sessionID: String, after: Int, log: String) -> AsyncThrowingStream<StreamEvent, Error> {
        let unsigned = request(sessionID: sessionID, after: after, log: log)
        let session = session
        let auth = auth
        return AsyncThrowingStream { continuation in
            let task = Task {
                var token: String?
                var request = unsigned
                do {
                    token = try await auth?.credential()
                    if let token { request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization") }
                    try await Self.read(request, session: session, log: log) { continuation.yield($0) }
                    continuation.finish()
                } catch let error as URLError {
                    continuation.finish(throwing: APIError.transport(error.code))
                } catch APIError.server(401, .loginRequired, let message) {
                    // The login ended: show the unlock; the caller's next load waits for it.
                    await auth?.rejected(token: token)
                    continuation.finish(throwing: APIError.server(status: 401, code: .loginRequired, message: message))
                } catch {
                    continuation.finish(throwing: error)
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    private func request(sessionID: String, after: Int, log: String) -> URLRequest {
        var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)
        components?.path = "/v1/sessions/\(sessionID)/events"
        // queue=1 asks for `queue` events too (servers without the queue ignore it).
        components?.queryItems = [URLQueryItem(name: "after", value: String(after)),
                                  URLQueryItem(name: "log", value: log), URLQueryItem(name: "queue", value: "1")]
        var request = URLRequest(url: components?.url ?? baseURL)
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        request.timeoutInterval = Self.idleTimeout
        return request
    }

    private static func read(_ request: URLRequest, session: URLSession, log: String,
                             yield: (StreamEvent) -> Void) async throws {
        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse else { throw APIError.transport(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else {
            var body = Data()
            for try await byte in bytes {
                body.append(byte)
                if body.count >= errorBodyLimit { break }
            }
            throw APIError.from(status: http.statusCode, body: body)
        }
        var parser = SSEParser(log: log)
        var line = Data()
        for try await byte in bytes {
            guard byte == UInt8(ascii: "\n") else {
                line.append(byte)
                continue
            }
            if line.last == UInt8(ascii: "\r") { line.removeLast() }
            if let text = String(bytes: line, encoding: .utf8), let event = parser.feed(text) { yield(event) }
            line.removeAll(keepingCapacity: true)
        }
        // A frame without its closing blank line is incomplete and is dropped.
    }
}

/// Reads SSE lines into events. A frame whose data is not an event is skipped.
struct SSEParser {
    /// The log asked for; a bare `id: <seq>` belongs to it.
    let log: String
    private var lastID: String?
    private var data: [String] = []

    init(log: String) { self.log = log }

    /// Feeds one line (without its line ending); returns the event a blank line completes.
    mutating func feed(_ line: String) -> StreamEvent? {
        if line.isEmpty {
            defer { data = [] }
            guard !data.isEmpty,
                  let event = try? APIDecoder.decode(SessionEvent.self, from: Data(data.joined(separator: "\n").utf8))
            else { return nil }
            return StreamEvent(event: event, log: logOf(lastID))
        }
        if line.hasPrefix(":") { return nil }
        let field: Substring
        var value: Substring = ""
        if let colon = line.firstIndex(of: ":") {
            field = line[..<colon]
            value = line[line.index(after: colon)...]
            if value.hasPrefix(" ") { value = value.dropFirst() }
        } else {
            field = Substring(line)
        }
        switch field {
        case "id": lastID = String(value)
        case "data": data.append(String(value))
        default: break
        }
        return nil
    }

    private func logOf(_ id: String?) -> String {
        guard let id, let dot = id.lastIndex(of: "."), dot > id.startIndex else { return log }
        return String(id[..<dot])
    }
}
