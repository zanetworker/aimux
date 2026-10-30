import Foundation
import Synchronization
import AimuxKit

#if DEBUG
/// The chat queues of `-demo chat-queue`, kept in memory like the Mac would: demo-2 works with three messages
/// waiting, demo-3 is paused with one answering and one waiting, demo-18 (blocked on the Mac) waits with a note.
enum DemoQueue {
    private static let queues = Mutex<[String: QueueStatus]>([
        "demo-2": QueueStatus(state: "running", items: [
            item("dq1", "Then run the linter and fix what it finds"),
            item("dq2", "Summarize the changes in three bullets"),
            item("dq3", "Open a draft pull request")
        ]),
        "demo-3": QueueStatus(state: "paused", reason: "You used this session on your Mac", items: [
            item("dq4", "Draft the platform section", state: "answering"),
            item("dq5", "Add the customer notes under it")
        ]),
        "demo-18": QueueStatus(state: "running", note: "Waiting: you're using this session on your Mac", items: [
            item("dq6", "Fold the sections you are not reading")
        ])
    ])
    private static let next = Mutex(7)

    private static func item(_ id: String, _ text: String, state: String = "waiting") -> QueueItem {
        QueueItem(id: id, text: text, state: state, created: DemoData.ago(minutes: 1))
    }

    /// The queue for a session's detail: nil when it holds nothing.
    static func status(_ sessionID: String) -> QueueStatus? {
        queues.withLock { $0[sessionID] }.flatMap { $0.items.isEmpty ? nil : $0 }
    }

    static func get(_ sessionID: String) -> QueueStatus {
        queues.withLock { $0[sessionID] } ?? QueueStatus(state: "running", items: [])
    }

    static func add(_ sessionID: String, text: String) -> QueueItem {
        let id = next.withLock { value in
            defer { value += 1 }
            return "dq\(value)"
        }
        let added = item(id, text)
        change(sessionID) { QueueStatus(state: $0.state, reason: $0.reason, note: $0.note, items: $0.items + [added]) }
        return added
    }

    static func edit(_ sessionID: String, id: String, text: String) throws -> QueueStatus {
        try waiting(sessionID, id)
        return change(sessionID) { queue in
            QueueStatus(state: queue.state, reason: queue.reason, note: queue.note, items: queue.items.map {
                $0.id == id ? QueueItem(id: id, text: text, state: $0.state, created: $0.created) : $0
            })
        }
    }

    static func move(_ sessionID: String, id: String, position: Int) throws -> QueueStatus {
        try waiting(sessionID, id)
        return change(sessionID) { ChatQueue.moved($0, id: id, to: position) }
    }

    static func delete(_ sessionID: String, id: String) -> QueueStatus {
        change(sessionID) { queue in
            let items = queue.items.filter { $0.id != id }
            // A queue with no messages always runs.
            return items.isEmpty ? QueueStatus(state: "running", items: [])
                : QueueStatus(state: queue.state, reason: queue.reason, note: queue.note, items: items)
        }
    }

    static func resume(_ sessionID: String) -> QueueStatus {
        change(sessionID) { QueueStatus(state: "running", note: $0.note, items: $0.items) }
    }

    private static func waiting(_ sessionID: String, _ id: String) throws {
        guard let found = get(sessionID).items.first(where: { $0.id == id }) else {
            throw APIError.server(status: 404, code: .unknownItem, message: "no such item")
        }
        guard found.state == "waiting" else {
            throw APIError.server(status: 409, code: .itemSending, message: "being sent")
        }
    }

    @discardableResult
    private static func change(_ sessionID: String, _ update: (QueueStatus) -> QueueStatus) -> QueueStatus {
        queues.withLock { all in
            let changed = update(all[sessionID] ?? QueueStatus(state: "running", items: []))
            all[sessionID] = changed
            return changed
        }
    }
}
#endif
