import Foundation

// The chat queue: messages the Mac sends one at a time when the session is free (GET/POST
// /v1/sessions/{id}/queue, PATCH/DELETE …/queue/{item}, POST …/queue/resume). What the composer offers,
// the queue section and its rows follow the shared fixtures in api/testdata/clients/queue/; the web client
// shows the same (clients/web/webapp/queue.mjs).

/// One queued message. `state` is `waiting`, `sending`, `answering`, or `done` (only the answer to a
/// retried add; its text is then empty).
public struct QueueItem: Decodable, Equatable, Hashable, Sendable, Identifiable {
    public let id: String
    public let text: String
    public let state: String
    public let created: Date

    public init(id: String, text: String, state: String, created: Date) {
        self.id = id
        self.text = text
        self.state = state
        self.created = created
    }
}

/// A session's queue: `running` or `paused` (with `reason`); `note` is a waiting line while it runs.
public struct QueueStatus: Decodable, Equatable, Hashable, Sendable {
    public let state: String
    public let reason: String?
    public let note: String?
    public let items: [QueueItem]

    public init(state: String, reason: String? = nil, note: String? = nil, items: [QueueItem]) {
        self.state = state
        self.reason = reason
        self.note = note
        self.items = items
    }

    private enum CodingKeys: String, CodingKey { case state, reason, note, items }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        state = try container.decode(String.self, forKey: .state)
        reason = try? container.decodeIfPresent(String.self, forKey: .reason)
        note = try? container.decodeIfPresent(String.self, forKey: .note)
        items = try container.decode(LossyList<QueueItem>.self, forKey: .items).elements
    }
}

/// The composer's main button.
public enum ComposerAction: String, Sendable {
    case send, queue

    public var label: String { self == .send ? "Send" : "Add to queue" }
}

/// What the composer offers after a direct send was refused.
public struct SendRefusalOffer: Equatable, Sendable {
    public let action: ComposerAction
    /// The text stays (in the composer, or as the failed message).
    public let keepText: Bool
    public let message: String?
}

/// The queue section: hidden when empty; paused shows its reason and Resume, running its waiting note if any.
public struct QueueSection: Equatable, Sendable {
    public let visible: Bool
    public let state: String?
    public let line: String?
    public let resume: Bool

    public init(visible: Bool, state: String? = nil, line: String? = nil, resume: Bool = false) {
        self.visible = visible
        self.state = state
        self.line = line
        self.resume = resume
    }
}

/// One row of the queue section and what may be done to it.
public struct QueueRow: Equatable, Sendable, Identifiable {
    public let id: String
    public let text: String
    public let state: String
    public let label: String
    public let canEdit: Bool
    public let canMove: Bool
    public let canDelete: Bool
}

/// A queued message open for editing: its text when opened, and as typed since.
public struct QueueEdit: Equatable, Sendable {
    public let id: String
    public let original: String
    public var text: String

    public init(id: String, original: String, text: String) {
        self.id = id
        self.original = original
        self.text = text
    }
}

/// What a queue update means for an open edit.
public enum QueueEditOutcome: Equatable, Sendable {
    /// Its message still waits: the edit stays open, untouched.
    case keep
    /// Its message was sent or deleted, and the text was not changed: the edit closes.
    case closed
    /// Its message was sent or deleted before the changed text was saved: the text is handed over.
    case lost(String)
}

/// An edited text whose message was sent before the edit was saved, kept with its own offer.
public struct LostEdit: Equatable, Sendable, Identifiable {
    public let id: UUID
    public let text: String

    public init(id: UUID = UUID(), text: String) {
        self.id = id
        self.text = text
    }
}

public enum ChatQueue {
    static let noText = "A queued message needs some text."

    /// What VoiceOver says when an open edit's message was sent meanwhile, and where its text went.
    public static func lostEditAnnouncement(inComposer: Bool) -> String {
        "This message was sent before your edit was saved. "
            + (inComposer ? "Your text is in the message box." : "Your text is kept above the message box.")
    }

    private static let labels = ["waiting": "Waiting", "sending": "Sending", "answering": "Answer in progress"]

    /// Send while the session is free; Add to queue while it works, while its terminal cannot take typing (the
    /// Mac sends when it can), while its queue is sending (`isActive`: the server refuses a direct send then),
    /// and after a direct send was refused busy (until the next detail or event).
    public static func composerAction(state: SessionState?, terminal: TerminalStatus?, queue: QueueStatus?,
                                      refusedBusy: Bool) -> ComposerAction {
        if refusedBusy || state == .busy || isActive(queue) { return .queue }
        if let terminal, terminal.working || !terminal.canType || terminal.status == "unknown" { return .queue }
        return .send
    }

    /// For 409 busy, Add to queue with the text kept and no message (nothing is queued by itself); nil for any
    /// other refusal, which stays a failed send.
    public static func sendRefusal(_ error: Error) -> SendRefusalOffer? {
        guard case APIError.server(_, .busy, _) = error else { return nil }
        return SendRefusalOffer(action: .queue, keepText: true, message: nil)
    }

    /// The queue has a message in flight, or is running with messages waiting; the server then answers a
    /// direct send 409 busy.
    public static func isActive(_ queue: QueueStatus?) -> Bool {
        let items = shown(queue)
        return items.contains { $0.state != "waiting" } || (!items.isEmpty && queue?.state == "running")
    }

    /// The messages shown: every one but `done`.
    static func shown(_ queue: QueueStatus?) -> [QueueItem] {
        (queue?.items ?? []).filter { $0.state != "done" }
    }

    public static func section(_ queue: QueueStatus?) -> QueueSection {
        guard let queue, !shown(queue).isEmpty else { return QueueSection(visible: false) }
        if queue.state == "paused" {
            return QueueSection(visible: true, state: "paused", line: queue.reason, resume: true)
        }
        return QueueSection(visible: true, state: queue.state, line: queue.note)
    }

    /// The rows in order. The one in flight never changes; it is deleted only while paused.
    public static func rows(_ queue: QueueStatus?) -> [QueueRow] {
        let paused = queue?.state == "paused"
        return shown(queue).map { item in
            let waiting = item.state == "waiting"
            return QueueRow(id: item.id, text: item.text, state: item.state, label: labels[item.state] ?? item.state,
                            canEdit: waiting, canMove: waiting, canDelete: waiting || paused)
        }
    }

    /// The PATCH position for moving `id` by `delta` (-1 up, +1 down), counted among the messages not in
    /// flight; nil when it cannot move so.
    public static func position(in queue: QueueStatus?, moving id: String, by delta: Int) -> Int? {
        let waiting = shown(queue).filter { $0.state == "waiting" }
        guard let index = waiting.firstIndex(where: { $0.id == id }) else { return nil }
        let target = index + delta
        return waiting.indices.contains(target) ? target : nil
    }

    /// A drag in the list (`onMove` offsets among the rows shown) as the message moved and its PATCH position;
    /// nil when nothing changes or the row cannot move.
    public static func move(in queue: QueueStatus?, from source: Int,
                            to destination: Int) -> (id: String, position: Int)? {
        var rows = shown(queue)
        guard rows.indices.contains(source), rows[source].state == "waiting" else { return nil }
        let id = rows[source].id
        let before = rows.filter { $0.state == "waiting" }.firstIndex { $0.id == id }
        // As SwiftUI's move(fromOffsets:toOffset:): `destination` is the row it goes before, in the old order.
        let row = rows.remove(at: source)
        let target = min(max(destination, 0), rows.count + 1)
        rows.insert(row, at: target > source ? target - 1 : target)
        let after = rows.filter { $0.state == "waiting" }.firstIndex { $0.id == id }
        guard let after, after != before else { return nil }
        return (id, after)
    }

    /// The queue with `id` moved to `position` among the waiting messages (clamped), as the server will answer.
    public static func moved(_ queue: QueueStatus, id: String, to position: Int) -> QueueStatus {
        guard let item = queue.items.first(where: { $0.id == id }) else { return queue }
        var rest = queue.items.filter { $0.id != id }
        let waiting = rest.indices.filter { rest[$0].state == "waiting" }
        let clamped = min(max(position, 0), waiting.count)
        let index = clamped < waiting.count ? waiting[clamped] : (waiting.last.map { $0 + 1 } ?? rest.endIndex)
        rest.insert(item, at: index)
        return QueueStatus(state: queue.state, reason: queue.reason, note: queue.note, items: rest)
    }

    /// The line shown for a refused or failed queue request: fixed lines for the queue's refusals.
    public static func errorText(_ error: Error) -> String {
        if error is CancellationError { return cancelled }
        let failure = error as? APIError ?? .transport(.unknown)
        if case .transport(let code) = failure, let text = transportText(code) { return text }
        if case .server(_, let code, _) = failure {
            switch code {
            case .badText:
                return "This message can't be queued (it starts with ! or /, holds control characters, "
                    + "or is too long)."
            case .queueFull: return "The queue is full (20 messages). Delete one to add another."
            case .itemSending: return "This message is being sent and can't be changed."
            case .unknownItem: return "This message is no longer in the queue."
            default: break
            }
        }
        return "Could not change the queue: " + failure.userMessage.title
    }

    private static let cancelled = "Could not change the queue: the request was cancelled before it reached your Mac."

    /// A change that failed in transport: never sent, or sent without its answer coming back (so it may have
    /// been saved). Nil for the ones that say "Can't reach your Mac" as every other request does.
    private static func transportText(_ code: URLError.Code) -> String? {
        let mayHaveChanged = " It may have changed; check the queue."
        switch code {
        case .cancelled: return cancelled
        case .timedOut: return "Could not change the queue: your Mac didn't answer in time." + mayHaveChanged
        case .networkConnectionLost:
            return "Could not change the queue: the connection to your Mac dropped." + mayHaveChanged
        default: return nil
        }
    }

    /// Whether an open edit survives `queue`: kept while its message can still be edited.
    public static func editOutcome(_ edit: QueueEdit, in queue: QueueStatus?) -> QueueEditOutcome {
        if rows(queue).contains(where: { $0.id == edit.id && $0.canEdit }) { return .keep }
        let trim = { (text: String) in text.trimmingCharacters(in: .whitespacesAndNewlines) }
        return trim(edit.text) == trim(edit.original) ? .closed : .lost(edit.text)
    }
}
