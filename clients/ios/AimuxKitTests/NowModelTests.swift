import Foundation
import Testing
@testable import AimuxKit

private final class StubSessions: SessionSource, @unchecked Sendable {
    let rows: [Session]
    init(_ rows: [Session]) { self.rows = rows }
    func sessions(limit: Int) async throws -> [Session] { rows }
}

private actor Gate {
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

private final class StubActivity: ActivitySource, @unchecked Sendable {
    var pages: [String: Result<EventPage, Error>] = [:]
    private(set) var requested: [String] = []
    private(set) var answers: [[String]] = []
    var answerFailure: Error?
    struct Sent { let session: String; let text: String; let key: String }
    private(set) var sent: [Sent] = []
    var sendFailure: Error?
    /// Per session, the user text of its latest turn, as saved; a session without one has no turns.
    var lastUser: [String: String] = [:]
    var detailFailure: Error?
    /// Per session, results for its 1st, 2nd, ... snapshot call; the last one repeats.
    var sequences: [String: [Result<EventPage, Error>]] = [:]
    var firstSnapshotGate: Gate?
    var answerGate: Gate?
    private let lock = NSLock()

    var requestCount: Int { lock.withLock { requested.count } }
    var answerCount: Int { lock.withLock { answers.count } }

    func eventSnapshot(sessionID: String) async throws -> EventPage {
        let (result, gate) = lock.withLock { () -> (Result<EventPage, Error>, Gate?) in
            requested.append(sessionID)
            let call = requested.filter { $0 == sessionID }.count
            if let queue = sequences[sessionID] {
                return (queue[min(call, queue.count) - 1], call == 1 ? firstSnapshotGate : nil)
            }
            return (pages[sessionID] ?? .success(EventPage(events: [], log: "")), nil)
        }
        await gate?.wait()
        return try result.get()
    }

    func answer(approvalID: String, optionID: String) async throws {
        lock.withLock { answers.append([approvalID, optionID]) }
        await answerGate?.wait()
        if let answerFailure { throw answerFailure }
    }

    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        lock.withLock { sent.append(Sent(session: sessionID, text: text, key: key)) }
        if let sendFailure { throw sendFailure }
        return TurnAccepted(id: sessionID, turn: "t2", after: 1, events: "/v1/sessions/\(sessionID)/events?after=1")
    }

    func session(id: String, turns: Int) async throws -> SessionDetail {
        if let detailFailure { throw detailFailure }
        let user = lock.withLock { lastUser[id] }
        let saved = user.map { [Turn(number: 1, time: now, user: $0, reply: "", tools: [])] } ?? []
        return SessionDetail(id: id, title: "", project: "", lastActive: now, state: .idle, turns: saved)
    }
}

private let now = Date(timeIntervalSince1970: 100_000)

private func row(_ id: String, _ state: SessionState = .idle, ago: TimeInterval = 10) -> Session {
    Session(id: id, title: "T \(id)", project: "/p/\(id)", lastActive: now.addingTimeInterval(-ago), state: state)
}

private let option = PermissionOption(optionId: "allow", name: "Allow", kind: "allow_once")
private let approval = Approval(approvalId: "ap1", title: "Run ls", options: [option], command: "ls")

private func event(_ seq: Int, _ kind: EventKind, _ data: EventData, ago: TimeInterval = 5) -> SessionEvent {
    SessionEvent(seq: seq, turn: "t1", kind: kind, time: now.addingTimeInterval(-ago), data: data)
}

private func page(_ events: [SessionEvent]) -> Result<EventPage, Error> {
    .success(EventPage(events: events, log: ""))
}

private func makeDefaults() -> (UserDefaults, String) {
    let suite = "NowModelTests-\(UUID().uuidString)"
    return (UserDefaults(suiteName: suite)!, suite)
}

@MainActor
private func makeModel(_ rows: [Session], _ activity: StubActivity, _ defaults: UserDefaults) -> NowModel {
    let list = SessionListModel(source: StubSessions(rows), cache: nil)
    return NowModel(list: list, source: activity, defaults: defaults, now: { now })
}

@MainActor @Suite struct NowModelTests {
    private func spin(until condition: () -> Bool) async {
        for _ in 0..<2000 where !condition() { await Task.yield() }
    }

    @Test func olderRefreshNeverOverwritesNewer() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        let gate = Gate()
        activity.firstSnapshotGate = gate
        activity.sequences["a"] = [page([event(1, .approval, .approval(approval))]), page([])]
        let model = makeModel([row("a")], activity, defaults)

        let older = Task { await model.refresh() }
        await spin { activity.requestCount == 1 }
        let newer = Task { await model.refresh() }
        for _ in 0..<50 { await Task.yield() }
        await gate.open()
        await older.value
        await newer.value

        #expect(activity.requestCount == 2)
        #expect(model.view.needsYou.isEmpty)
    }

    @Test func secondAnswerWhileInFlightIsIgnored() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        let gate = Gate()
        activity.answerGate = gate
        activity.pages["a"] = page([event(1, .approval, .approval(approval))])
        let model = makeModel([row("a")], activity, defaults)
        await model.refresh()

        let first = Task { await model.answer(approval, option: option, session: "a") }
        await spin { activity.answerCount == 1 }
        #expect(model.answering == ["ap1"])
        await model.answer(approval, option: option, session: "a")
        await gate.open()
        await first.value

        #expect(activity.answerCount == 1)
        #expect(model.answering.isEmpty)
        #expect(model.view.needsYou.isEmpty)
    }

    @Test func thrownAnswerSetsErrorAndKeepsCard() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.pages["a"] = page([event(1, .approval, .approval(approval))])
        activity.answerFailure = APIError.http(status: 502)
        let model = makeModel([row("a")], activity, defaults)
        await model.refresh()

        await model.answer(approval, option: option, session: "a")

        #expect(model.answerErrors["ap1"] == .http(status: 502))
        #expect(model.answering.isEmpty)
        #expect(model.view.needsYou.map(\.session.id) == ["a"])

        activity.answerFailure = nil
        await model.answer(approval, option: option, session: "a")
        #expect(model.answerErrors["ap1"] == nil)
        #expect(model.view.needsYou.isEmpty)
    }

    @Test func alreadyResolvedApprovalHidesTheCard() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.pages["a"] = page([event(1, .approval, .approval(approval))])
        activity.answerFailure = APIError.server(status: 404, code: .noSuchApproval, message: "gone")
        let model = makeModel([row("a")], activity, defaults)
        await model.refresh()

        await model.answer(approval, option: option, session: "a")

        #expect(model.answerErrors["ap1"] == nil)
        #expect(model.view.needsYou.isEmpty)
    }

    @Test func networkFailureKeepsLastKnownActivity() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.sequences["a"] = [page([event(1, .approval, .approval(approval))]),
                                   .failure(URLError(.notConnectedToInternet))]
        let model = makeModel([row("a")], activity, defaults)

        await model.refresh()
        await model.refresh()

        #expect(model.view.needsYou.map(\.session.id) == ["a"])
    }

    @Test func snapshotsOnlyCandidates() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let rows = [
            row("busy-old", .busy, ago: 5000), row("recent", .idle, ago: 899), row("stale", .idle, ago: 901),
            row("terminal-recent", .openInTerminal, ago: 1), row("terminal-busy-old", .openInTerminal, ago: 5000)
        ]
        let activity = StubActivity()
        let model = makeModel(rows, activity, defaults)

        await model.refresh()

        #expect(Set(activity.requested) == ["busy-old", "recent"])
    }

    @Test func snapshotsAtMostTenNewestFirst() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let rows = (0..<15).map { row("s\($0)", .idle, ago: TimeInterval(10 + $0)) }
        let activity = StubActivity()
        let model = makeModel(rows, activity, defaults)

        await model.refresh()

        #expect(Set(activity.requested) == Set((0..<10).map { "s\($0)" }))
    }

    @Test func anApprovalInTheOldestOfManyBusySessionsStillNeedsYou() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let rows = (0..<12).map { row("b\($0)", .busy, ago: TimeInterval(10 + $0)) }
        let activity = StubActivity()
        activity.pages["b11"] = page([event(1, .approval, .approval(approval))])
        let model = makeModel(rows, activity, defaults)

        await model.refresh()

        #expect(model.view.needsYou.map(\.session.id) == ["b11"])
    }

    @Test func refusedSnapshotIsNoActivity() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.pages["gone"] = .failure(APIError.server(status: 404, code: .unknownSession, message: "no"))
        activity.pages["held"] = .failure(APIError.server(status: 409, code: .busy, message: "busy"))
        activity.pages["offline"] = .failure(URLError(.notConnectedToInternet))
        activity.pages["ok"] = page([event(1, .approval, .approval(approval))])
        let model = makeModel([row("gone", .busy), row("held", .busy), row("offline", .busy), row("ok")],
                              activity, defaults)

        await model.refresh()

        #expect(model.view.needsYou.map(\.session.id) == ["ok"])
        #expect(model.view.working.itemIDs == ["gone", "held", "offline"].sorted())
    }

    @Test func answeringRemovesTheCard() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.pages["a"] = page([event(1, .approval, .approval(approval))])
        let model = makeModel([row("a")], activity, defaults)
        await model.refresh()
        #expect(model.view.needsYou.map(\.session.id) == ["a"])

        await model.answer(approval, option: option, session: "a")

        #expect(activity.answers == [["ap1", "allow"]])
        #expect(model.view.needsYou.isEmpty)
    }

    private func failedRow(prompt: String?) -> Session {
        Session(id: "f", title: "T f", project: "/p/f", lastActive: now.addingTimeInterval(-30), state: .idle,
                lastPrompt: prompt)
    }

    @Test func retrySendsTheLastPromptWithAFreshKeyEachTap() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.pages["f"] = page([event(1, .turnError, .turnError(error: "exit 1"))])
        activity.lastUser["f"] = "run the tests again"
        let session = failedRow(prompt: "run the tests again")
        let model = makeModel([session], activity, defaults)
        await model.refresh()
        let snapshotsBefore = activity.requestCount

        await model.retry(session)
        await model.retry(session)

        #expect(activity.sent.map(\.text) == ["run the tests again", "run the tests again"])
        #expect(activity.sent.map(\.session) == ["f", "f"])
        let keys = activity.sent.map(\.key)
        #expect(keys.allSatisfy { !$0.isEmpty })
        #expect(Set(keys).count == 2)
        #expect(activity.requestCount == snapshotsBefore + 2)  // each sent retry refreshes Now
        #expect(model.retrying.isEmpty)
    }

    @Test func refusedRetrySetsItsErrorUntilTriedAgain() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.sendFailure = APIError.server(status: 409, code: .busy, message: "busy")
        activity.lastUser["f"] = "again"
        let session = failedRow(prompt: "again")
        let model = makeModel([session], activity, defaults)

        await model.retry(session)
        #expect(model.retryErrors["f"] == .server(status: 409, code: .busy, message: "busy"))
        #expect(model.retrying.isEmpty)

        activity.sendFailure = nil
        await model.retry(session)
        #expect(model.retryErrors["f"] == nil)
    }

    @Test func retrySendsTheWholeLastMessageFromTheHistoryNotThePreview() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        let long = String(repeating: "x", count: 300)
        activity.lastUser["f"] = "The user sent a new message while you were working:\n" + long
        let session = failedRow(prompt: String(long.prefix(160)) + "…")
        let model = makeModel([session], activity, defaults)

        await model.retry(session)

        #expect(activity.sent.map(\.text) == [long])
    }

    @Test func retrySendsNothingWhenTheHistoryCannotBeRead() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.detailFailure = APIError.http(status: 500)
        let session = failedRow(prompt: "go")
        let model = makeModel([session], activity, defaults)

        await model.retry(session)

        #expect(activity.sent.isEmpty)
        #expect(model.retryErrors["f"] == .http(status: 500))
        #expect(model.retrying.isEmpty)
    }

    @Test func showsTheListBeforeTheEventReadsAnswer() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let (activity, gate) = (StubActivity(), Gate())
        activity.firstSnapshotGate = gate
        activity.sequences["a"] = [page([])]
        let model = makeModel([row("a", .busy), row("m", .openInTerminal, ago: 5000)], activity, defaults)
        let refresh = Task { await model.refresh() }
        await spin { activity.requestCount == 1 }  // the event read is still out: the list shows already
        #expect(model.view.onMac.map(\.id) == ["m"])
        #expect(model.view.summary.working == 1)
        await gate.open()
        await refresh.value
    }
    @Test func retryWithoutAPromptSendsNothing() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        let model = makeModel([failedRow(prompt: nil)], activity, defaults)

        await model.retry(failedRow(prompt: nil))
        await model.retry(failedRow(prompt: "   "))

        #expect(activity.sent.isEmpty)
    }

    @Test func seenClearsFinished() async {
        let (defaults, suite) = makeDefaults()
        defer { defaults.removePersistentDomain(forName: suite) }
        let activity = StubActivity()
        activity.pages["a"] = page([event(1, .turnDone, .turnDone(stopReason: "end_turn"), ago: 60)])
        let model = makeModel([row("a")], activity, defaults)
        await model.refresh()
        #expect(model.view.finished.map(\.session.id) == ["a"])

        model.markSeen("a")

        #expect(model.view.finished.isEmpty)
        let reloaded = makeModel([row("a")], activity, defaults)
        await reloaded.refresh()
        #expect(reloaded.view.finished.isEmpty)
    }
}

private extension WorkingSection {
    var itemIDs: [String] {
        if case .list(let items) = self { return items.map(\.session.id).sorted() }
        return []
    }
}
