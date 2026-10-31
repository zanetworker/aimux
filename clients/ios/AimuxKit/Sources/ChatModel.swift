import Foundation
import Observation

public protocol ChatSource: Sendable {
    func eventSnapshot(sessionID: String) async throws -> EventPage
    func eventPage(sessionID: String, after: Int, log: String) async throws -> EventPage
    func session(id: String, turns: Int) async throws -> SessionDetail
    func cancel(sessionID: String) async throws
    func answer(approvalID: String, optionID: String) async throws
    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted
    func terminalSend(sessionID: String, text: String, key: String) async throws
    func terminalPress(sessionID: String, promptID: String, option: String, key: String) async throws
    func queue(sessionID: String) async throws -> QueueStatus
    func addToQueue(sessionID: String, text: String, key: String) async throws -> QueueItem
    func editQueueItem(sessionID: String, itemID: String, text: String) async throws -> QueueStatus
    func moveQueueItem(sessionID: String, itemID: String, position: Int) async throws -> QueueStatus
    func deleteQueueItem(sessionID: String, itemID: String) async throws -> QueueStatus
    func resumeQueue(sessionID: String) async throws -> QueueStatus
}

extension APIClient: ChatSource {}

public enum ChatState: Equatable, Sendable {
    case loading
    /// Following the event stream.
    case live
    /// The stream ended or failed; loading again after a pause.
    case reconnecting
    /// Open in a terminal on the Mac, or never started: history only, no stream, no sending.
    case readOnly
    /// Open in a terminal on the Mac that this server can type into: history read again on a timer, no stream;
    /// the composer shows while `terminal.canType`.
    case terminal
    /// Could not load; the message says why.
    case failed(String)
}

/// A message the person sent that no event has shown yet.
public struct PendingMessage: Equatable, Sendable {
    public let text: String
    /// The idempotency key; a retry of the same message keeps it.
    public let key: String
    /// Why the last attempt failed; nil while sending or once accepted.
    public var failure: APIError?
    /// The turn the server started for it, once accepted.
    public var turn: String?
    /// When the person sent it (this device's clock).
    public let sentAt: Date
}

/// One session's chat: history and events merged into a timeline, kept live from the event stream.
@MainActor @Observable
public final class ChatModel {
    public private(set) var result: TimelineResult = .empty
    /// A newly read detail also says the queue now, and ends an offer to queue after a refused send.
    public internal(set) var session: SessionDetail? {
        didSet {
            queue = session?.queue
            refusedBusy = false
        }
    }
    public internal(set) var state: ChatState = .loading
    public internal(set) var pending: PendingMessage? {
        didSet { drafts?.setPending(pending, for: sessionID) }
    }
    /// Approvals whose answer is on its way; a second answer for one of them is ignored.
    public internal(set) var answering: Set<String> = []
    /// Why the last answer for an approval failed; cleared when it is tried again.
    public internal(set) var answerErrors: [String: APIError] = [:]
    /// Approvals answered here (or already answered elsewhere): their card collapses.
    public private(set) var answered: Set<String> = []
    /// Terminal prompts answered here (or gone before the answer landed): their card collapses. One leaves the
    /// set once the screen no longer shows it, so the same prompt asked again later gets a card again.
    public internal(set) var answeredPrompts: Set<String> = []
    /// Why the last Stop failed.
    public private(set) var stopError: APIError?
    /// Why the last read of a terminal session failed; nil once one works. The timeline shown stays.
    public internal(set) var refreshFailure: APIError?

    // The chat queue (ChatModelQueue.swift), when the server has one (`queueEnabled`).
    /// The session's queue, from the detail, a `queue` event or a queue request's answer; nil when empty.
    public internal(set) var queue: QueueStatus? {
        didSet { queueRevision &+= 1 }
    }
    /// Counts queue changes, so a detail read before a newer one never puts its older queue back.
    private(set) var queueRevision = 0
    /// A direct send was refused `409 busy`: the composer offers Add to queue until the next detail or turn end.
    public internal(set) var refusedBusy = false
    /// Why the text in the composer was not queued; cleared by the next add.
    public internal(set) var addNotice: String?
    /// Why the last change to the queue failed; cleared by the next one.
    public internal(set) var queueError: String?
    /// Edited texts whose message was sent before the edit was saved, kept while the composer held other text.
    public internal(set) var lostEdits: [LostEdit] = []
    /// A change to the queue is on its way; another one meanwhile is ignored.
    public internal(set) var isChangingQueue = false
    /// The server keeps a chat queue (its health says so); set again once health is read.
    public var queueEnabled: Bool
    /// The last add that failed, so a retry of the same text keeps its key.
    var failedAdd: (text: String, key: String)?
    var isAdding = false

    public let sessionID: String
    let source: ChatSource
    private let stream: EventStreaming
    /// Keeps a failed message for the next chat on this session.
    private let drafts: ChatDrafts?
    let sleep: @Sendable (Duration) async throws -> Void
    let now: @Sendable () -> Date
    var events: [SessionEvent] = []
    private var log = ""
    private var lastSeq = 0
    private var delay = ChatModel.firstDelay
    /// Follows the stream, or waits to load again; one at a time.
    var follower: Task<Void, Never>?
    /// Counts loads, so an older load that finishes late is dropped.
    var generation = 0
    var closed = false
    /// When a message was last typed into the terminal; reads are faster for 2 minutes after.
    var lastTerminalSend: Date?
    /// The idempotency key of a prompt answer that failed, per prompt and option, so a retry keeps it.
    var pressKeys: [String: String] = [:]

    private static let firstDelay: Duration = .seconds(2)
    private static let maxDelay: Duration = .seconds(30)
    static let historyTurns = 30
    private static let loadAttempts = 3
    private static let clockSlack: TimeInterval = 2
    static let terminalFast: Duration = .seconds(2)
    static let terminalSlow: Duration = .seconds(15)
    static let terminalFastWindow: TimeInterval = 120

    /// `drafts` restores a message that failed to send in an earlier chat on this session, and keeps one for the next.
    public init(sessionID: String, source: ChatSource, stream: EventStreaming, drafts: ChatDrafts? = nil,
                queueEnabled: Bool = false,
                sleep: @escaping @Sendable (Duration) async throws -> Void = { try await Task.sleep(for: $0) },
                now: @escaping @Sendable () -> Date = { .now }) {
        self.sessionID = sessionID
        self.source = source
        self.stream = stream
        self.drafts = drafts
        self.queueEnabled = queueEnabled
        self.pending = drafts?.pending(for: sessionID)
        self.sleep = sleep
        self.now = now
    }

    /// The pause after `delay`: doubled, at most 30 s.
    static func backoff(after delay: Duration) -> Duration { min(delay * 2, maxDelay) }

    /// Reads the events and the history (again when a turn started meanwhile or the server restarted),
    /// builds the timeline and follows the stream from there.
    public func load() async {
        guard !closed else { return }
        generation += 1
        let mine = generation
        let page: EventPage
        let detail: SessionDetail
        let revision = queueRevision
        do {
            (page, detail) = try await readConsistent()
        } catch {
            guard mine == generation, !closed else { return }
            loadFailed(error)
            return
        }
        guard mine == generation, !closed else { return }
        adopt(detail, readSince: revision)
        refreshFailure = nil
        delay = Self.firstDelay
        if Self.isTerminal(detail) {
            events = []
            state = .terminal
            pruneAnsweredPrompts()
            rebuild()
            pollTerminal()
            return
        }
        if Self.isHistoryOnly(detail) {
            follower?.cancel()
            follower = nil
            events = []
            state = .readOnly
            rebuild()
            return
        }
        events = page.events
        log = page.log
        lastSeq = page.events.map(\.seq).max() ?? 0
        state = .live
        rebuild()
        follow()
    }

    private func readConsistent() async throws -> (EventPage, SessionDetail) {
        var attempt = 1
        while true {
            let page = try await source.eventSnapshot(sessionID: sessionID)
            let detail = try await source.session(id: sessionID, turns: Self.historyTurns)
            if Self.isHistoryOnly(detail) || attempt >= Self.loadAttempts { return (page, detail) }
            // A turn that started after the snapshot may already be in the history and would then also
            // arrive on the stream; a new log means the server restarted. Either way, read both again.
            let since = try await source.eventPage(sessionID: sessionID, after: page.events.last?.seq ?? 0,
                                                   log: page.log)
            if since.log == page.log && !since.events.contains(where: { $0.kind == .turnStarted }) {
                return (page, detail)
            }
            attempt += 1
        }
    }

    /// A session open in a terminal, or one that never started, is history only.
    private static func isHistoryOnly(_ detail: SessionDetail) -> Bool {
        detail.state == .openInTerminal || detail.state == .unavailable
    }

    /// Open in a terminal on the Mac, from a server that reports whether it can type into it.
    static func isTerminal(_ detail: SessionDetail) -> Bool {
        detail.state == .openInTerminal && detail.terminal != nil
    }

    private func loadFailed(_ error: Error) {
        let failure = error as? APIError ?? .transport(.unknown)
        if case .server(404, _, _) = failure {
            state = .failed("No session with that id.")
            return
        }
        state = session == nil ? .failed(failure.userMessage.title) : .reconnecting
        retryLater()
    }

    private func follow() {
        follower?.cancel()
        let events = stream.events(sessionID: sessionID, after: lastSeq, log: log)
        let log = log
        follower = Interaction.background { Task { [weak self] in
            do {
                for try await item in events {
                    guard let self, !Task.isCancelled else { return }
                    // A new log means the server restarted: load again rather than mix two logs.
                    if item.log != log { break }
                    self.receive(item)
                }
            } catch {}
            guard let self, !Task.isCancelled, !self.closed else { return }
            self.state = .reconnecting
            self.retryLater()
        } }
    }

    /// Waits, then loads again; each wait is longer, up to 30 s.
    private func retryLater() {
        follower?.cancel()
        let pause = delay
        delay = Self.backoff(after: delay)
        let sleep = sleep
        follower = Interaction.background { Task { [weak self] in
            do { try await sleep(pause) } catch { return }
            guard let self, !Task.isCancelled, !self.closed else { return }
            await self.load()
        } }
    }

    /// Applies one streamed event.
    func receive(_ item: StreamEvent) {
        let event = item.event
        if case .queue(let status) = event.data {
            // A snapshot of the queue, of no turn: not a timeline event.
            lastSeq = max(lastSeq, event.seq)
            queue = status
            return
        }
        guard !events.contains(where: { $0.seq == event.seq }) else { return }
        events.append(event)
        lastSeq = max(lastSeq, event.seq)
        rebuild()
        if event.kind == .turnDone || event.kind == .turnError {
            refusedBusy = false
            refreshDetail()
        }
    }

    /// Sends a message. The user item shows at once; sending the same text again after a failure
    /// is a retry and keeps the idempotency key, anything else is a new message with a new key.
    /// A read-only session takes no messages; a refusal because it is open in a terminal loads it again.
    public func send(_ text: String) async {
        guard state != .readOnly else { return }
        let key: String
        if let previous = pending, previous.text == text, previous.failure != nil {
            key = previous.key
        } else {
            key = UUID().uuidString
        }
        pending = PendingMessage(text: text, key: key, failure: nil, turn: nil, sentAt: now())
        if state == .terminal {
            await typeIntoTerminal(text, key: key)
            return
        }
        do {
            let accepted = try await source.sendMessage(sessionID: sessionID, text: text, key: key)
            guard pending?.key == key else { return }
            pending?.turn = accepted.turn
            clearShownPending()
        } catch {
            guard pending?.key == key else { return }
            let failure = error as? APIError ?? .transport(.unknown)
            pending?.failure = failure
            await queueIfRefusedBusy(failure)
            // Open in a terminal meanwhile: this view is out of date, and a retry could never succeed.
            if case .server(_, .openInTerminal, _) = failure { await load() }
        }
    }

    /// Stops the running turn; the turn ends with a stopped item when its `turn_done` arrives.
    /// When the server has no turn running, the session is loaded again.
    public func stop() async {
        stopError = nil
        do {
            try await source.cancel(sessionID: sessionID)
        } catch APIError.server(_, .notRunning, _) {
            // The server has no turn (it may have restarted): this view is out of date.
            await load()
        } catch {
            stopError = error as? APIError ?? .transport(.unknown)
        }
    }

    /// Stops following the session; nothing reconnects afterwards.
    public func close() {
        closed = true
        follower?.cancel()
        follower = nil
    }

    func rebuild() {
        clearShownPending()
        let built = buildTimeline(turns: session?.turns ?? [], events: events, now: now())
        var pendingApproval: String?
        for case .approval(let item) in built.items where item.outcome == nil && !answered.contains(item.approvalId) {
            pendingApproval = item.approvalId
        }
        if state == .terminal, let prompt = session?.terminal?.prompt, !answeredPrompts.contains(prompt.promptId) {
            pendingApproval = prompt.promptId
        }
        result = TimelineResult(items: built.items, working: built.working, pendingApproval: pendingApproval)
    }

    /// Drops the pending message once the timeline shows it: its turn is in the events, or a history turn
    /// with its prompt was recorded after it was sent (2 s of clock slack; Claude Code's wrappers around a
    /// message typed into the terminal do not count). A failed send stays.
    func clearShownPending() {
        guard let message = pending, message.failure == nil else { return }
        let inEvents = message.turn.map { turn in events.contains { $0.turn == turn } } ?? false
        let since = message.sentAt.addingTimeInterval(-Self.clockSlack)
        let inHistory = session?.turns.contains {
            $0.time >= since && pendingShownInHistory($0.user, message.text)
        } ?? false
        if inEvents || inHistory { pending = nil }
    }
}

extension ChatModel {
    public func answer(_ approval: Approval, option: PermissionOption) async {
        let id = approval.approvalId
        guard !answering.contains(id) else { return }
        answering.insert(id)
        answerErrors[id] = nil
        defer { answering.remove(id) }
        do {
            try await source.answer(approvalID: id, optionID: option.optionId)
        } catch APIError.server(_, .noSuchApproval, _) {
            // Already answered elsewhere: the card is stale, not failed.
        } catch let failure as APIError {
            answerErrors[id] = failure
            return
        } catch {
            answerErrors[id] = .transport(.unknown)
            return
        }
        answered.insert(id)
        rebuild()
    }
}

extension ChatModel {
    /// Reads the session again once a turn ends, so the turn's record and the stats come from the session file.
    /// A failure is left to the next load; a load or close meanwhile drops the result.
    fileprivate func refreshDetail() {
        let mine = generation
        let revision = queueRevision
        let source = source
        let sessionID = sessionID
        _ = Interaction.background { Task { [weak self] in
            guard let detail = try? await source.session(id: sessionID, turns: Self.historyTurns) else { return }
            guard let self, mine == self.generation, !self.closed else { return }
            self.adopt(detail, readSince: revision)
            self.rebuild()
        } }
    }
}
