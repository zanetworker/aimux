import Foundation
import Testing
@testable import AimuxKit

private let start = Date(timeIntervalSince1970: 100_000)

/// Sessions whose approval the owner answered stay out of the Next bar's working fallback until a read shows
/// them not working.
@MainActor @Suite struct NowModelAnsweredTests {
    private func model(_ source: SettableActivity) -> NowModel {
        let busy = Session(id: "a", title: "T a", project: "/p/a", lastActive: start, state: .busy)
        return NowModel(list: SessionListModel(source: FixedSessions([busy]), cache: nil), source: source,
                        now: { start })
    }

    @Test func anAnswerKeepsTheSessionOutWhileItWorks() async {
        let source = SettableActivity()
        let now = model(source)
        now.noteAnswered("a")
        source.activity = .working(step: "go test")
        await now.refresh()
        #expect(now.answeredSessions == ["a"], "still the same run of work")
    }

    @Test func aReadThatIsNotWorkingLetsItBackIn() async {
        let source = SettableActivity()
        let now = model(source)
        now.noteAnswered("a")
        source.activity = .finished(at: start)
        await now.refresh()
        #expect(now.answeredSessions.isEmpty)
    }

    private func terminal(working: Bool) -> Session {
        Session(id: "t", title: "T t", project: "/p/t", lastActive: start, state: .openInTerminal, working: working)
    }

    @Test func aTerminalSessionIsReleasedByTheListOnceItIsNotWorking() async {
        let sessions = SwitchableSessions([terminal(working: true)])
        let now = NowModel(list: SessionListModel(source: sessions, cache: nil), source: SettableActivity(),
                           now: { start })
        now.noteAnswered("t")
        await now.refresh()
        #expect(now.answeredSessions == ["t"], "the list still shows it working")
        sessions.fails = true
        sessions.rows = [terminal(working: false)]
        await now.refresh()
        #expect(now.answeredSessions == ["t"], "a failed list changes nothing")
        sessions.fails = false
        await now.refresh()
        #expect(now.answeredSessions.isEmpty, "the list shows it not working")
    }

    @Test func anIdThatLeftTheListIsReleased() async {
        let sessions = SwitchableSessions([terminal(working: true)])
        let now = NowModel(list: SessionListModel(source: sessions, cache: nil), source: SettableActivity(),
                           now: { start })
        now.noteAnswered("t")
        sessions.rows = []
        await now.refresh()
        #expect(now.answeredSessions.isEmpty)
    }

    @Test func theNavigationViewIgnoresTheQueryAndTheWorkingChip() async {
        let rows = [Session(id: "a", title: "T a", project: "/p/a", lastActive: start, state: .busy),
                    Session(id: "b", title: "T b", project: "/p/b", lastActive: start, state: .busy),
                    Session(id: "zed", title: "T zed", project: "/p/zed", lastActive: start, state: .idle)]
        let source = SettableActivity()
        let now = NowModel(list: SessionListModel(source: FixedSessions(rows), cache: nil), source: source,
                           now: { start })
        await now.refresh()
        now.query = "zed"
        now.workingOnly = true
        #expect(now.view.elsewhere.flatMap(\.sessions).map(\.id) == [])
        let next = nextSession(in: now.navigationView, excluding: "a", activity: now.activity)
        #expect(next?.session.id == "b")
    }

    @Test func answeringOnNowNotesTheSession() async {
        let source = SettableActivity()
        let now = model(source)
        let approval = Approval(approvalId: "ap1", title: "Run ls", options: [], command: nil)
        await now.answer(approval, option: PermissionOption(optionId: "o", name: "Allow", kind: "allow_once"),
                         session: "a")
        #expect(now.answeredSessions == ["a"])
    }
}

private final class FixedSessions: SessionSource, @unchecked Sendable {
    let rows: [Session]
    init(_ rows: [Session]) { self.rows = rows }
    func sessions(limit: Int) async throws -> [Session] { rows }
}

private final class SwitchableSessions: SessionSource, @unchecked Sendable {
    var rows: [Session]
    var fails = false
    init(_ rows: [Session]) { self.rows = rows }
    func sessions(limit: Int) async throws -> [Session] {
        if fails { throw APIError.http(status: 500) }
        return rows
    }
}

private final class SettableActivity: ActivitySource, @unchecked Sendable {
    private let lock = NSLock()
    private var current: Activity = .none
    var activity: Activity {
        get { lock.withLock { current } }
        set { lock.withLock { current = newValue } }
    }

    func eventSnapshot(sessionID: String) async throws -> EventPage {
        let events: [SessionEvent]
        switch activity {
        case .working(let step):
            events = [SessionEvent(seq: 1, turn: "t", kind: .turnStarted, time: start, data: .turnStarted(text: "go")),
                      SessionEvent(seq: 2, turn: "t", kind: .update, time: start,
                                   data: .update(sessionUpdate: "tool_call", title: step))]
        case .finished:
            events = [SessionEvent(seq: 1, turn: "t", kind: .turnStarted, time: start, data: .turnStarted(text: "go")),
                      SessionEvent(seq: 2, turn: "t", kind: .turnDone, time: start,
                                   data: .turnDone(stopReason: "end_turn"))]
        default:
            events = []
        }
        return EventPage(events: events, log: "")
    }
    func answer(approvalID: String, optionID: String) async throws {}
    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        throw APIError.http(status: 500)
    }
    func session(id: String, turns: Int) async throws -> SessionDetail { throw APIError.http(status: 500) }
}
