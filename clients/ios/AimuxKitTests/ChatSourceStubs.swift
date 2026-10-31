import Foundation
@testable import AimuxKit

/// Chat sources and streams for ChatModelTests.
struct Unreachable: ChatSource {
    func eventSnapshot(sessionID: String) async throws -> EventPage {
        throw APIError.transport(.notConnectedToInternet)
    }
    func eventPage(sessionID: String, after: Int, log: String) async throws -> EventPage {
        EventPage(events: [], log: "L1")
    }
    func session(id: String, turns: Int) async throws -> SessionDetail { throw APIError.http(status: 502) }
    func cancel(sessionID: String) async throws {}
    func answer(approvalID: String, optionID: String) async throws {}
    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        TurnAccepted(id: sessionID, turn: "t", after: 0, events: "")
    }
    func terminalSend(sessionID: String, text: String, key: String) async throws {}
    func terminalPress(sessionID: String, promptID: String, option: String, key: String) async throws {}
}

struct MissingSession: ChatSource {
    func eventSnapshot(sessionID: String) async throws -> EventPage { EventPage(events: [], log: "L1") }
    func eventPage(sessionID: String, after: Int, log: String) async throws -> EventPage {
        EventPage(events: [], log: "L1")
    }
    func session(id: String, turns: Int) async throws -> SessionDetail {
        throw APIError.server(status: 404, code: .unknownSession, message: "no such session")
    }
    func cancel(sessionID: String) async throws {}
    func answer(approvalID: String, optionID: String) async throws {}
    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        TurnAccepted(id: sessionID, turn: "t", after: 0, events: "")
    }
    func terminalSend(sessionID: String, text: String, key: String) async throws {}
    func terminalPress(sessionID: String, promptID: String, option: String, key: String) async throws {}
}

actor ChatGate {
    private var isOpen = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    func wait() async {
        if isOpen { return }
        await withCheckedContinuation { waiters.append($0) }
    }
    func open() {
        isOpen = true
        waiters.forEach { $0.resume() }
        waiters = []
    }
}

/// Results per call: the 1st, 2nd, ... call gets the matching entry; the last one repeats.
final class StubChat: ChatSource, @unchecked Sendable {
    var snapshots: [EventPage] = [EventPage(events: [], log: "L1")]
    var sinces: [EventPage] = [EventPage(events: [], log: "L1")]
    var details: [SessionDetail] = []
    struct Sent: Equatable { let text: String; let key: String }
    var sendFailures: [Error?] = []
    var sendGate: ChatGate?
    var answerFailure: Error?
    var terminalFailures: [Error?] = []
    var pressFailure: Error?
    struct Press: Equatable { let promptID: String; let option: String; let key: String }
    private(set) var terminalSent: [Sent] = []
    private(set) var presses: [Press] = []
    var cancelFailure: Error?
    private(set) var calls: [String] = []
    private(set) var sent: [Sent] = []
    private(set) var cancels = 0
    private(set) var answers: [[String]] = []
    private let lock = NSLock()

    private func pick<T>(_ list: [T], _ name: String) -> T {
        lock.withLock {
            calls.append(name)
            let count = calls.filter { $0 == name }.count
            return list[min(count, list.count) - 1]
        }
    }

    func count(_ name: String) -> Int { lock.withLock { calls.filter { $0 == name }.count } }
    var sentList: [Sent] { lock.withLock { sent } }
    var terminalSentList: [Sent] { lock.withLock { terminalSent } }
    private(set) var terminalReplaced: [Sent] = []
    var terminalReplacedList: [Sent] { lock.withLock { terminalReplaced } }
    var pressList: [Press] { lock.withLock { presses } }

    /// Whether each snapshot read ran as something the person asked for.
    private(set) var snapshotInteractions: [Bool] = []
    var snapshotInteractionList: [Bool] { lock.withLock { snapshotInteractions } }

    func eventSnapshot(sessionID: String) async throws -> EventPage {
        lock.withLock { snapshotInteractions.append(Interaction.isActive) }
        return pick(snapshots, "snapshot")
    }
    func eventPage(sessionID: String, after: Int, log: String) async throws -> EventPage { pick(sinces, "since") }
    /// When set, each detail read waits for it after being counted.
    var detailGate: ChatGate?
    /// A failure is taken from the front for each detail read, before the details.
    var detailFailures: [Error?] = []
    func session(id: String, turns: Int) async throws -> SessionDetail {
        let failure = lock.withLock { detailFailures.isEmpty ? nil : detailFailures.removeFirst() }
        if let failure { throw failure }
        let detail = pick(details, "detail")
        await detailGate?.wait()
        return detail
    }
    func cancel(sessionID: String) async throws {
        lock.withLock { cancels += 1 }
        if let cancelFailure { throw cancelFailure }
    }

    func answer(approvalID: String, optionID: String) async throws {
        lock.withLock { answers.append([approvalID, optionID]) }
        if let answerFailure { throw answerFailure }
    }

    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        let failure = lock.withLock { () -> Error? in
            sent.append(Sent(text: text, key: key))
            return sendFailures.isEmpty ? nil : sendFailures.removeFirst()
        }
        await sendGate?.wait()
        if let failure { throw failure }
        return TurnAccepted(id: sessionID, turn: "t9", after: 7, events: "/v1/sessions/\(sessionID)/events?after=7")
    }

    func terminalSend(sessionID: String, text: String, key: String) async throws {
        let failure = lock.withLock { () -> Error? in
            terminalSent.append(Sent(text: text, key: key))
            return terminalFailures.isEmpty ? nil : terminalFailures.removeFirst()
        }
        if let failure { throw failure }
    }

    func terminalReplace(sessionID: String, text: String, key: String) async throws {
        let failure = lock.withLock { () -> Error? in
            terminalReplaced.append(Sent(text: text, key: key))
            return terminalFailures.isEmpty ? nil : terminalFailures.removeFirst()
        }
        if let failure { throw failure }
    }

    func terminalPress(sessionID: String, promptID: String, option: String, key: String) async throws {
        let failure = lock.withLock { () -> Error? in
            presses.append(Press(promptID: promptID, option: option, key: key))
            return pressFailure
        }
        if let failure { throw failure }
    }

    // The chat queue: each call is recorded ("add <text> <key>", "get", "edit <id> <text>", "move <id> <n>",
    // "delete <id>", "resume"); a failure is taken from the front of its list, else the queue answers.
    var queueAnswer = QueueStatus(state: "running", items: [])
    var addFailures: [Error?] = []
    var getFailures: [Error?] = []
    var changeFailures: [Error?] = []
    /// When set, each change (not add or get) waits for it after being recorded.
    var changeGate: ChatGate?
    private(set) var queueCalls: [String] = []
    var queueCallList: [String] { lock.withLock { queueCalls } }

    private func record(_ call: String, _ failures: ReferenceWritableKeyPath<StubChat, [Error?]>) -> Error? {
        lock.withLock {
            queueCalls.append(call)
            return self[keyPath: failures].isEmpty ? nil : self[keyPath: failures].removeFirst()
        }
    }

    func queue(sessionID: String) async throws -> QueueStatus {
        if let failure = record("get", \.getFailures) { throw failure }
        return lock.withLock { queueAnswer }
    }
    func addToQueue(sessionID: String, text: String, key: String) async throws -> QueueItem {
        if let failure = record("add \(text) \(key)", \.addFailures) { throw failure }
        return QueueItem(id: "new", text: text, state: "waiting", created: .now)
    }
    private func change(_ call: String) async throws -> QueueStatus {
        let failure = record(call, \.changeFailures)
        await changeGate?.wait()
        if let failure { throw failure }
        return lock.withLock { queueAnswer }
    }
    func editQueueItem(sessionID: String, itemID: String, text: String) async throws -> QueueStatus {
        try await change("edit \(itemID) \(text)")
    }
    func moveQueueItem(sessionID: String, itemID: String, position: Int) async throws -> QueueStatus {
        try await change("move \(itemID) \(position)")
    }
    func deleteQueueItem(sessionID: String, itemID: String) async throws -> QueueStatus {
        try await change("delete \(itemID)")
    }
    func resumeQueue(sessionID: String) async throws -> QueueStatus { try await change("resume") }
}

/// The sources that never reach a queue.
extension ChatSource {
    func queue(sessionID: String) async throws -> QueueStatus { throw APIError.http(status: 502) }
    func addToQueue(sessionID: String, text: String, key: String) async throws -> QueueItem {
        throw APIError.http(status: 502)
    }
    func editQueueItem(sessionID: String, itemID: String, text: String) async throws -> QueueStatus {
        throw APIError.http(status: 502)
    }
    func moveQueueItem(sessionID: String, itemID: String, position: Int) async throws -> QueueStatus {
        throw APIError.http(status: 502)
    }
    func deleteQueueItem(sessionID: String, itemID: String) async throws -> QueueStatus {
        throw APIError.http(status: 502)
    }
    func resumeQueue(sessionID: String) async throws -> QueueStatus { throw APIError.http(status: 502) }
}

/// Streams from a queue: each entry is yielded then finished; when the queue is empty the stream stays open.
final class StubStreams: EventStreaming, @unchecked Sendable {
    var queue: [[StreamEvent]] = []
    struct Call: Equatable { let after: Int; let log: String }
    private(set) var calls: [Call] = []
    private var open: [AsyncThrowingStream<StreamEvent, Error>.Continuation] = []
    private let lock = NSLock()

    var callList: [Call] { lock.withLock { calls } }

    func events(sessionID: String, after: Int, log: String) -> AsyncThrowingStream<StreamEvent, Error> {
        let (stream, continuation) = AsyncThrowingStream<StreamEvent, Error>.makeStream()
        lock.withLock {
            calls.append(Call(after: after, log: log))
            if queue.isEmpty {
                open.append(continuation)
            } else {
                queue.removeFirst().forEach { continuation.yield($0) }
                continuation.finish()
            }
        }
        return stream
    }
}

final class Sleeps: @unchecked Sendable {
    private(set) var list: [Duration] = []
    private let lock = NSLock()
    var all: [Duration] { lock.withLock { list } }
    func record(_ duration: Duration) { lock.withLock { list.append(duration) } }
}
