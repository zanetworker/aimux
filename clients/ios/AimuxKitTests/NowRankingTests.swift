import Foundation
import Testing
@testable import AimuxKit

/// The fixture files of one folder under api/testdata/clients, by name: new files are picked up.
func clientFixtureNames(_ folder: String) -> [String] {
    let urls = Bundle(for: ClientFixtureToken.self).urls(forResourcesWithExtension: "json",
                                                          subdirectory: "clients/" + folder) ?? []
    return urls.map { $0.deletingPathExtension().lastPathComponent }.sorted()
}

func loadClientFixture<T: Decodable>(_ folder: String, _ name: String, as type: T.Type = T.self) throws -> T {
    let bundle = Bundle(for: ClientFixtureToken.self)
    let url = try #require(bundle.url(forResource: name, withExtension: "json", subdirectory: "clients/" + folder))
    return try APIDecoder.decode(T.self, from: Data(contentsOf: url))
}

final class ClientFixtureToken {}

private let fixtureNames = clientFixtureNames("now")

private struct Fixture: Decodable {
    struct Tab: Decodable, Equatable {
        let index: Int
        let name: String
        let panes: Int
        let working: Int
        let open: Bool
        let sessions: [String]
    }
    struct Window: Decodable, Equatable { let id: String; let name: String; let tabs: [Tab] }
    struct Group: Decodable, Equatable { let label: String; let sessions: [String] }
    struct Expect: Decodable {
        let needsYou: [String]
        let pinned: [String]
        let other: [String]
        let windows: [Window]
        let hiddenTabs: Int
        let elsewhere: [Group]
        let working: Int
        let open: Int
        let steps: [String: String?]?
        let descriptions: [String: String?]?
        let rowStates: [String: String?]?
    }

    let name: String
    let now: Date
    let query: String?
    let workingOnly: Bool?
    let sessions: [Session]
    let events: [String: [SessionEvent]]
    let expect: Expect
}

@Suite struct NowRankingTests {
    @Test(arguments: fixtureNames) func matchesSharedFixture(name: String) throws {
        let fixture: Fixture = try loadClientFixture("now", name)
        var activity: [String: Activity] = [:]
        for session in fixture.sessions {
            activity[session.id] = deriveActivity(fixture.events[session.id] ?? [])
        }
        let view = rankNow(sessions: fixture.sessions, activity: activity, now: fixture.now,
                           query: fixture.query ?? "", workingOnly: fixture.workingOnly ?? false)
        let expect = fixture.expect

        #expect(view.needsYou.map(\.session.id) == expect.needsYou)
        #expect(view.pinned.map(\.session.id) == expect.pinned)
        #expect(view.other.map(\.id) == expect.other)
        let windows = view.windows.map { window in
            Fixture.Window(id: window.id, name: window.name, tabs: window.tabs.map { tab in
                Fixture.Tab(index: tab.index, name: tab.name, panes: tab.panes, working: tab.working,
                            open: tab.open, sessions: tab.sessions.map(\.id))
            })
        }
        #expect(windows == expect.windows)
        #expect(view.hiddenTabs == expect.hiddenTabs)
        #expect(view.elsewhere.map { Fixture.Group(label: $0.label, sessions: $0.sessions.map(\.id)) }
                == expect.elsewhere)
        #expect(view.working == expect.working)
        #expect(view.open == expect.open)
        for (id, step) in expect.steps ?? [:] {
            guard case .working(let got) = activity[id] else { Issue.record("\(id) is not working"); continue }
            #expect(got == step, "step of \(id)")
        }
        for (id, line) in expect.descriptions ?? [:] {
            let session = try #require(fixture.sessions.first { $0.id == id })
            #expect(session.descriptionLine == line, "description of \(id)")
        }
        let format = SessionRowFormat(locale: Locale(identifier: "en_US"), timeZone: TimeZone(identifier: "UTC")!)
        for (id, text) in expect.rowStates ?? [:] {
            let session = try #require(fixture.sessions.first { $0.id == id })
            let state = rowState(session, activity: activity[id] ?? .none, now: fixture.now, format: format)
            #expect(state?.text == text, "row state of \(id)")
        }
    }

    @Test func everyFixtureFileIsFound() {
        #expect(fixtureNames.count >= 20)
        #expect(Set(fixtureNames).count == fixtureNames.count)
    }
}

@Suite struct DeriveActivityTests {
    private func date(_ seconds: TimeInterval) -> Date { Date(timeIntervalSince1970: 1_800_000_000 + seconds) }

    private func event(_ seq: Int, _ kind: EventKind, _ data: EventData, turn: String = "t1") -> SessionEvent {
        SessionEvent(seq: seq, turn: turn, kind: kind, time: date(Double(seq)), data: data)
    }

    private let approval = Approval(approvalId: "p1", title: "Run it", options: [], command: nil)

    @Test func noTurnEventsIsNone() {
        #expect(deriveActivity([]) == .none)
        #expect(deriveActivity([event(1, .update, .update(sessionUpdate: "tool_call", title: "x"))]) == .none)
    }

    @Test func pendingApprovalWins() {
        let events = [
            event(1, .turnStarted, .turnStarted(text: "go")),
            event(2, .approval, .approval(approval)),
            event(3, .turnError, .turnError(error: "boom"))
        ]
        #expect(deriveActivity(events) == .needsApproval(approval))
    }

    @Test func resolvedApprovalNoLongerWins() {
        let events = [
            event(1, .turnStarted, .turnStarted(text: "go")),
            event(2, .approval, .approval(approval)),
            event(3, .approvalResolved, .approvalResolved(approvalId: "p1", outcome: "allow"))
        ]
        #expect(deriveActivity(events) == .working(step: nil))
    }

    @Test func latestPendingApprovalWins() {
        let later = Approval(approvalId: "p2", title: "Run it too", options: [], command: nil)
        let events = [
            event(1, .turnStarted, .turnStarted(text: "go")),
            event(2, .approval, .approval(approval)),
            event(3, .approval, .approval(later))
        ]
        #expect(deriveActivity(events) == .needsApproval(later))
    }

    @Test func lastTurnErrorIsFailed() {
        let events = [
            event(1, .turnStarted, .turnStarted(text: "go")),
            event(2, .turnError, .turnError(error: "boom"))
        ]
        #expect(deriveActivity(events) == .failed(error: "boom", at: date(2)))
    }

    @Test func lastTurnDoneIsFinished() {
        let events = [
            event(1, .turnStarted, .turnStarted(text: "go")),
            event(2, .turnDone, .turnDone(stopReason: "end_turn")),
            event(3, .turnStarted, .turnStarted(text: "again"), turn: "t2"),
            event(4, .turnDone, .turnDone(stopReason: "end_turn"), turn: "t2")
        ]
        #expect(deriveActivity(events) == .finished(at: date(4)))
    }

    @Test func toolCallTitleIsStep() {
        let events = [
            event(1, .turnStarted, .turnStarted(text: "go")),
            event(2, .update, .update(sessionUpdate: "tool_call", title: "Run tests")),
            event(3, .update, .update(sessionUpdate: "tool_call_update", title: "Edit main.go")),
            event(4, .update, .update(sessionUpdate: "tool_call_update", title: nil))
        ]
        #expect(deriveActivity(events) == .working(step: "Edit main.go"))
    }

    @Test func thoughtIsThinking() {
        let events = [
            event(1, .turnStarted, .turnStarted(text: "go")),
            event(2, .update, .update(sessionUpdate: "tool_call", title: "Run tests")),
            event(3, .update, .update(sessionUpdate: "agent_thought_chunk", title: nil))
        ]
        #expect(deriveActivity(events) == .working(step: "Thinking"))
    }

    @Test func stepsFromAnEarlierTurnAreIgnored() {
        let events = [
            event(1, .turnStarted, .turnStarted(text: "go")),
            event(2, .update, .update(sessionUpdate: "tool_call", title: "Old step")),
            event(3, .turnDone, .turnDone(stopReason: "end_turn")),
            event(4, .turnStarted, .turnStarted(text: "again"), turn: "t2")
        ]
        #expect(deriveActivity(events) == .working(step: nil))
    }
}

@Suite struct NowWindowKeyTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func placed(_ id: String, window name: String, index: Int, tab: Int, pane: Int) -> Session {
        let place = MacPlace(window: MacWindow(id: "w1", index: index, name: name), tab: MacTab(index: tab, name: "t"),
                             pane: pane, panes: 3, lastInFront: false)
        return Session(id: id, title: id, project: "/src/x", lastActive: now, state: .openInTerminal, mac: place)
    }

    @Test func oneWindowPerIdNamedByItsFirstSessionInMacOrder() {
        // The same window read with a name and index that changed between panes: still one window.
        let sessions = [placed("b", window: "Reviews", index: 2, tab: 2, pane: 1),
                        placed("a", window: "Window 1", index: 1, tab: 1, pane: 1)]
        let view = rankNow(sessions: sessions, activity: [:], now: now)
        #expect(view.windows.count == 1)
        #expect(view.windows.first?.id == "w1")
        #expect(view.windows.first?.name == "Window 1")
        #expect(view.windows.first?.tabs.map(\.index) == [1, 2])
    }

    @Test func windowsOrderByTheirFirstSessionsIndex() {
        var second = placed("z", window: "Window 2", index: 2, tab: 1, pane: 1)
        second = second.placed(MacPlace(window: MacWindow(id: "w0", index: 2, name: "Window 2"),
                                        tab: MacTab(index: 1, name: "t"), pane: 1, panes: 1, lastInFront: false))
        let view = rankNow(sessions: [second, placed("a", window: "Window 1", index: 1, tab: 1, pane: 1)],
                           activity: [:], now: now)
        #expect(view.windows.map(\.id) == ["w1", "w0"])
    }
}
