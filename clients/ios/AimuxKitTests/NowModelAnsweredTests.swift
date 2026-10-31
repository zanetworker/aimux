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
