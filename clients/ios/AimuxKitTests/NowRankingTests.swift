import Foundation
import Testing
@testable import AimuxKit

private let fixtureNames = [
    "approval-pending", "approval-resolved", "failed-recent", "failed-old", "finished-unseen", "finished-seen",
    "terminal-active-and-idle", "five-working", "six-working", "twenty-working", "on-mac-cap",
    "same-folder-name", "clock-skew", "unknown-state", "boundary-900s", "ties", "ties-groups",
    "sub-millisecond", "steps", "descriptions", "folder-rows", "last-on-mac-12h-boundary", "last-on-mac-and-pinned",
    "last-on-mac-first", "last-on-mac-future", "last-on-mac-stale", "last-on-mac-unknown-session", "needs-you-wins",
    "pinned-needs-approval", "pinned-order", "pinned-shrinks-working", "pinned-working-once",
    "stale-last-on-mac-pinned"
]

private struct Fixture: Decodable {
    struct Group: Decodable, Equatable {
        let kind: String
        let project: String
        let name: String
        let count: Int
        let expanded: Bool
        let newest: String
        let sessions: [String]
    }
    struct More: Decodable, Equatable { let projects: Int; let sessions: Int }
    struct Working: Decodable { let list: [String]?; let groups: [Group]?; let more: More? }
    struct Summary: Decodable, Equatable { let needYou: Int; let working: Int; let onMac: Int }
    struct Expect: Decodable {
        let needsYou: [String]
        let finished: [String]
        let working: Working
        let onMac: [String]
        let onMacTotal: Int
        let summary: Summary
        /// Optional: the working step to pin per session id; null means no step.
        let steps: [String: String?]?
        /// Absent in the older fixtures: no Last on your Mac row and nothing pinned.
        let lastOnMac: String?
        let pinned: [String]?
        /// Optional: the row's second line to pin per session id; null means none.
        let descriptions: [String: String?]?
    }

    let name: String
    let now: Date
    let lastOnMac: LastOnMac?
    let seen: [String: Date]
    let sessions: [Session]
    let events: [String: [SessionEvent]]
    let expect: Expect
}

private func loadFixture(_ name: String) throws -> Fixture {
    let bundle = Bundle(for: NowBundleToken.self)
    let url = try #require(bundle.url(forResource: name, withExtension: "json", subdirectory: "clients/now"))
    return try APIDecoder.decode(Fixture.self, from: Data(contentsOf: url))
}

@Suite struct NowRankingTests {
    @Test(arguments: fixtureNames) func matchesSharedFixture(name: String) throws {
        let fixture = try loadFixture(name)
        var activity: [String: Activity] = [:]
        for session in fixture.sessions {
            activity[session.id] = deriveActivity(fixture.events[session.id] ?? [])
        }
        let view = rankNow(sessions: fixture.sessions, activity: activity, seen: fixture.seen, now: fixture.now,
                           lastOnMac: fixture.lastOnMac)
        let expect = fixture.expect

        #expect(view.lastOnMac?.session.id == expect.lastOnMac)
        #expect(view.pinned.map(\.session.id) == (expect.pinned ?? []))
        for (id, line) in expect.descriptions ?? [:] {
            let session = try #require(fixture.sessions.first { $0.id == id })
            #expect(session.descriptionLine == line, "description of \(id)")
        }

        #expect(view.needsYou.map(\.session.id) == expect.needsYou)
        #expect(view.finished.map(\.session.id) == expect.finished)
        #expect(view.onMac.map(\.id) == expect.onMac)
        #expect(view.onMacTotal == expect.onMacTotal)
        #expect(view.summary == NowSummary(needYou: expect.summary.needYou, working: expect.summary.working,
                                           onMac: expect.summary.onMac))
        for (id, step) in expect.steps ?? [:] {
            guard case .working(let got) = activity[id] else { Issue.record("\(id) is not working"); continue }
            #expect(got == step, "step of \(id)")
        }
        switch view.working {
        case .list(let items):
            #expect(items.map(\.session.id) == expect.working.list)
            #expect(expect.working.groups == nil)
        case .groups(let groups, let more):
            // A folder row starts expanded; collapsing it is the view's state.
            let got = groups.map {
                Fixture.Group(kind: "folder", project: $0.project, name: $0.name, count: $0.count, expanded: true,
                              newest: $0.newest.session.id, sessions: $0.items.map(\.session.id))
            }
            #expect(got == expect.working.groups)
            #expect(more.map { Fixture.More(projects: $0.projects, sessions: $0.sessions) } == expect.working.more)
            #expect(expect.working.list == nil)
        }
    }

    @Test func fixtureListCoversTheFolder() throws {
        let bundle = Bundle(for: NowBundleToken.self)
        let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/now") ?? []
        #expect(Set(urls.map { $0.deletingPathExtension().lastPathComponent }) == Set(fixtureNames))
        #expect(Set(fixtureNames).count == fixtureNames.count, "a name listed twice")
    }

    @Test func groupNameIsTheFolderName() throws {
        let fixture = try loadFixture("same-folder-name")
        let view = rankNow(sessions: fixture.sessions, activity: [:], seen: [:], now: fixture.now)
        guard case .groups(let groups, _) = view.working else { Issue.record("expected groups"); return }
        #expect(groups.map(\.name) == ["aimux", "aimux", "notes", "trips"])
    }
}

@Suite struct ProjectGroupItemsTests {
    @Test func groupCarriesItsSessionsNewestFirst() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        func busy(_ id: String, _ project: String, ago: TimeInterval) -> Session {
            Session(id: id, title: id, project: project, lastActive: now.addingTimeInterval(-ago), state: .busy)
        }
        let sessions = [
            busy("a1", "/src/a", ago: 30), busy("a2", "/src/a", ago: 10), busy("a3", "/src/a", ago: 20),
            busy("b1", "/src/b", ago: 5), busy("b2", "/src/b", ago: 40), busy("c1", "/src/c", ago: 1)
        ]
        let view = rankNow(sessions: sessions, activity: [:], seen: [:], now: now)
        guard case .groups(let groups, _) = view.working else { Issue.record("expected groups"); return }
        #expect(groups.map { $0.items.map(\.session.id) } == [["a2", "a3", "a1"], ["b1", "b2"], ["c1"]])
        #expect(groups.allSatisfy { $0.items.first == $0.newest && $0.items.count == $0.count })
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

private final class NowBundleToken {}
