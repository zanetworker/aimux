import Foundation
import Testing
@testable import AimuxKit

@Suite struct NextSessionTests {
    private let now = Date(timeIntervalSince1970: 1_792_951_200)

    private func item(_ id: String, _ activity: Activity = .none) -> NowItem {
        NowItem(session: Session(id: id, title: id, project: "/p", lastActive: now, state: .busy), activity: activity)
    }

    private func view(needsYou: [NowItem], working: WorkingSection) -> NowView {
        NowView(lastOnMac: nil, lastOnMacAt: nil, pinned: [], needsYou: needsYou, finished: [], working: working,
                onMac: [], onMacTotal: 0,
                summary: NowSummary(needYou: needsYou.count, working: 0, onMac: 0))
    }

    private let approval = Activity.needsApproval(Approval(approvalId: "a", title: "t", options: [], command: nil))

    @Test func needsYouComesBeforeWorking() {
        let next = nextSession(in: view(needsYou: [item("n1", approval)], working: .list([item("w1")])),
                               excluding: "x")
        #expect(next?.session.id == "n1")
    }

    @Test func skipsTheOpenSession() {
        let next = nextSession(in: view(needsYou: [item("self", approval)], working: .list([item("w1")])),
                               excluding: "self")
        #expect(next?.session.id == "w1")
    }

    @Test func readsWorkingGroups() {
        let group = ProjectGroup(project: "/p", name: "p", count: 2, newest: item("self"),
                                 items: [item("self"), item("w2")])
        let next = nextSession(in: view(needsYou: [], working: .groups([group], more: nil)), excluding: "self")
        #expect(next?.session.id == "w2")
    }

    @Test func nilWhenNothingElse() {
        #expect(nextSession(in: view(needsYou: [item("self", approval)], working: .list([])),
                            excluding: "self") == nil)
    }
}

@Suite struct ChatStatusTests {
    private let now = Date(timeIntervalSince1970: 1_792_951_200)

    private func detail(_ state: SessionState) -> SessionDetail {
        SessionDetail(id: "s1", title: "Sample", project: "/p", lastActive: now.addingTimeInterval(-14 * 3600),
                      state: state, turns: [])
    }

    private let idle = TimelineResult(items: [.user("Hi"), .reply("Hello")], working: false, pendingApproval: nil)

    @Test func neverStartedSaysDidNotStart() {
        let status = chatStatus(detail: detail(.unavailable), readOnly: true, result: .empty, now: now)
        #expect(status == ChatStatus(tone: .failed, label: "Did not start"))
    }

    @Test func terminalSaysOpenOnYourMac() {
        let status = chatStatus(detail: detail(.openInTerminal), readOnly: true, result: .empty, now: now)
        #expect(status == ChatStatus(tone: .idle, label: "Open on your Mac · 14h ago"))
    }

    @Test func liveStates() {
        #expect(chatStatus(detail: nil, readOnly: false, result: idle, now: now) == nil)
        #expect(chatStatus(detail: detail(.idle), readOnly: false, result: idle, now: now)
                == ChatStatus(tone: .idle, label: "Your turn"))
        let working = TimelineResult(items: [.user("Hi")], working: true, pendingApproval: nil)
        #expect(chatStatus(detail: detail(.busy), readOnly: false, result: working, now: now)
                == ChatStatus(tone: .working, label: "Working"))
        let asking = TimelineResult(items: [.user("Hi")], working: true, pendingApproval: "ap1")
        #expect(chatStatus(detail: detail(.busy), readOnly: false, result: asking, now: now)
                == ChatStatus(tone: .needsYou, label: "Your turn"))
        let failed = TimelineResult(items: [.user("Hi"), .error("boom")], working: false, pendingApproval: nil)
        #expect(chatStatus(detail: detail(.idle), readOnly: false, result: failed, now: now)
                == ChatStatus(tone: .failed, label: "Failed"))
    }

    @Test func readOnlyLineText() {
        #expect(readOnlyText(detail(.unavailable)) == "This session never started: its first message failed "
                + "before Claude saved it. Start a new session from Now.")
        #expect(readOnlyText(detail(.openInTerminal)) == "Read-only here while it's open in a terminal on your Mac")
        #expect(readOnlyText(nil) == "Read-only here while it's open in a terminal on your Mac")
    }

    @Test(arguments: [(0.0, "just now"), (5 * 60.0, "5m ago"), (14 * 3600.0, "14h ago"), (3 * 86400.0, "3d ago")])
    func ago(seconds: Double, text: String) {
        #expect(chatAgo(now.addingTimeInterval(-seconds), now: now) == text)
    }
}

@Suite struct StatsFormatTests {
    @Test(arguments: [(0.42, "$0.42"), (12.5, "$12.50"), (0.004, "<$0.01"), (0.0, "$0.00"), (0.01, "$0.01")])
    func cost(value: Double, text: String) {
        #expect(StatsFormat.cost(value) == text)
    }

    @Test(arguments: [(0, "0"), (999, "999"), (1000, "1k"), (34_200, "34k"), (1_200_000, "1.2M"),
                      (2_000_000, "2M"), (999_999, "1M")])
    func tokens(value: Int, text: String) {
        #expect(StatsFormat.tokens(value) == text)
    }

    @Test func timeIsNilWhenMissing() {
        let date = Date(timeIntervalSince1970: 1_792_951_200)
        #expect(StatsFormat.time(nil) == nil)
        #expect(StatsFormat.time(date) == date.formatted(.dateTime.month(.abbreviated).day().hour().minute()))
    }

    @Test(arguments: [(72.0, "1h 12m"), (12.4, "12m"), (0.3, "<1m"), (120.0, "2h"), (59.6, "1h")])
    func duration(minutes: Double, text: String) {
        #expect(StatsFormat.duration(minutes: minutes) == text)
    }
}
