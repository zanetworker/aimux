import Foundation
import Testing
@testable import AimuxKit

private let start = Date(timeIntervalSince1970: 100_000)

@MainActor @Suite struct NowModelHoldTests {
    @Test func aListThatLosesEveryPlaceKeepsTheLayoutFor30Seconds() async {
        let place = MacPlace(window: MacWindow(id: "w1", index: 1, name: "Window 1"), tab: MacTab(index: 1, name: "t"),
                             pane: 1, panes: 1, lastInFront: true)
        let placed = Session(id: "a", title: "T a", project: "/p/a", lastActive: start, state: .openInTerminal,
                             mac: place)
        let source = ChangingSessions([placed])
        let clock = Clock(start)
        let model = NowModel(list: SessionListModel(source: source, cache: nil), source: NoActivity(),
                             now: { clock.time })
        await model.refresh()
        #expect(model.view.windows.map(\.id) == ["w1"])

        source.rows = [placed.placed(nil)]
        clock.time = start.addingTimeInterval(30)
        await model.refresh()
        #expect(model.view.windows.map(\.id) == ["w1"], "a bad read keeps the layout")
        #expect(model.view.elsewhere.isEmpty)

        clock.time = start.addingTimeInterval(31)
        await model.refresh()
        #expect(model.view.windows.isEmpty, "after 30 s it falls back to folders")
        #expect(model.view.elsewhere.flatMap(\.sessions).map(\.id) == ["a"])
    }
}

private struct NoActivity: ActivitySource {
    func eventSnapshot(sessionID: String) async throws -> EventPage { EventPage(events: [], log: "") }
    func answer(approvalID: String, optionID: String) async throws {}
    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        throw APIError.http(status: 500)
    }
    func session(id: String, turns: Int) async throws -> SessionDetail { throw APIError.http(status: 500) }
}

private final class ChangingSessions: SessionSource, @unchecked Sendable {
    private let lock = NSLock()
    private var current: [Session]
    init(_ rows: [Session]) { current = rows }
    var rows: [Session] {
        get { lock.withLock { current } }
        set { lock.withLock { current = newValue } }
    }
    func sessions(limit: Int) async throws -> [Session] { rows }
}

private final class Clock: @unchecked Sendable {
    private let lock = NSLock()
    private var current: Date
    init(_ time: Date) { current = time }
    var time: Date {
        get { lock.withLock { current } }
        set { lock.withLock { current = newValue } }
    }
}
