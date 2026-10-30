import Foundation
import Testing
@testable import AimuxKit

private let chatFixtureNames = [
    "approval-pending", "approval-resolved", "arabic", "cancelled", "finished-not-in-history", "history-only",
    "long-content", "plan-updated", "replayed-events", "running-turn-in-history", "running-with-steps",
    "start-aged-out", "step-details-history", "step-details-live", "turn-error-after-history",
    "turn-error-never-written", "unknown-update-kind"
]

/// A JSON value, so a result is compared with the fixture field by field, extra or missing fields included.
private enum JSON: Decodable, Equatable {
    case null, bool(Bool), number(Double), string(String), array([JSON]), object([String: JSON])

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() {
            self = .null
        } else if let value = try? container.decode(Bool.self) {
            self = .bool(value)
        } else if let value = try? container.decode(Double.self) {
            self = .number(value)
        } else if let value = try? container.decode(String.self) {
            self = .string(value)
        } else if let value = try? container.decode([JSON].self) {
            self = .array(value)
        } else {
            self = .object(try container.decode([String: JSON].self))
        }
    }
}

private func json(_ text: String?) -> JSON { text.map(JSON.string) ?? .null }
private func json(_ number: Double?) -> JSON { number.map(JSON.number) ?? .null }
private func json(_ number: Int) -> JSON { .number(Double(number)) }

private func json(_ item: TimelineItem) -> JSON {
    switch item {
    case .user(let text): return .object(["kind": .string("user"), "text": .string(text)])
    case .thought(let text, let seconds):
        return .object(["kind": .string("thought"), "text": .string(text), "seconds": json(seconds)])
    case .reply(let text): return .object(["kind": .string("reply"), "text": .string(text)])
    case .error(let text): return .object(["kind": .string("error"), "text": .string(text)])
    case .stopped: return .object(["kind": .string("stopped")])
    case .steps(let group):
        let steps = group.steps.map { step -> JSON in
            .object(["kind": .string(step.kind.rawValue), "target": .string(step.target),
                     "status": .string(step.status.rawValue),
                     "detail": .object(["input": .string(step.detail.input), "error": .string(step.detail.error),
                                        "old": .string(step.detail.old), "new": .string(step.detail.new)])])
        }
        return .object(["kind": .string("steps"), "count": json(group.count), "seconds": json(group.seconds),
                        "filesChanged": .array(group.filesChanged.map(JSON.string)), "steps": .array(steps)])
    case .plan(let plan):
        let entries = plan.entries.map { JSON.object(["text": .string($0.text), "status": .string($0.status)]) }
        return .object(["kind": .string("plan"), "done": json(plan.done), "total": json(plan.total),
                        "entries": .array(entries)])
    case .approval(let approval):
        let options = approval.options.map { option -> JSON in
            .object(["optionId": .string(option.optionId), "name": .string(option.name),
                     "kind": .string(option.kind)])
        }
        return .object(["kind": .string("approval"), "approvalId": .string(approval.approvalId),
                        "title": .string(approval.title), "command": json(approval.command),
                        "options": .array(options), "outcome": json(approval.outcome)])
    }
}

private func json(_ result: TimelineResult) -> JSON {
    .object(["items": .array(result.items.map(json)), "working": .bool(result.working),
             "pendingApproval": json(result.pendingApproval)])
}

private struct ChatFixture: Decodable {
    let name: String
    let now: Date
    let turns: [Turn]
    let events: [SessionEvent]
    let expect: JSON
}

private func loadChatFixture(_ name: String) throws -> ChatFixture {
    let bundle = Bundle(for: ChatBundleToken.self)
    let url = try #require(bundle.url(forResource: name, withExtension: "json", subdirectory: "clients/chat"))
    return try APIDecoder.decode(ChatFixture.self, from: Data(contentsOf: url))
}

@Suite struct TimelineTests {
    @Test(arguments: chatFixtureNames) func matchesSharedFixture(name: String) throws {
        let fixture = try loadChatFixture(name)
        let result = buildTimeline(turns: fixture.turns, events: fixture.events, now: fixture.now)
        let got = json(result)
        guard case .object(let want) = fixture.expect, case .object(let have) = got,
              case .array(let wantItems)? = want["items"], case .array(let haveItems)? = have["items"] else {
            Issue.record("bad fixture shape"); return
        }
        #expect(haveItems.count == wantItems.count, "item count")
        for (index, pair) in zip(haveItems, wantItems).enumerated() {
            #expect(pair.0 == pair.1, "item \(index)")
        }
        #expect(have["working"] == want["working"])
        #expect(have["pendingApproval"] == want["pendingApproval"])
        #expect(got == fixture.expect)
    }

    @Test func fixtureListCoversTheFolder() throws {
        let bundle = Bundle(for: ChatBundleToken.self)
        let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/chat") ?? []
        #expect(Set(urls.map { $0.deletingPathExtension().lastPathComponent }) == Set(chatFixtureNames))
    }

    @Test func emptyInputIsEmpty() {
        let result = buildTimeline(turns: [], events: [], now: .now)
        #expect(result == TimelineResult(items: [], working: false, pendingApproval: nil))
    }

    @Test(arguments: [
        ("Write the release notes", "Write  the\nrelease notes for 1.2", true),
        ("Write the rel…", "Write the release notes", true),
        ("", "anything", false),
        ("…", "anything", false),
        ("Write the notes", "Write the", false)
    ])
    func samePromptFoldsWhitespaceAndEllipsis(history: String, text: String, matches: Bool) {
        #expect(samePrompt(history, text) == matches)
    }

    @Test func halfSecondRoundsUpAndNegativeIsZero() {
        let start = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(timelineSeconds(from: start, to: start.addingTimeInterval(2.5)) == 3)
        #expect(timelineSeconds(from: start, to: start.addingTimeInterval(2.4)) == 2)
        #expect(timelineSeconds(from: start, to: start.addingTimeInterval(-4)) == 0)
    }

    @Test func turnStartedWithoutTextAddsNoUserItem() throws {
        let text = #"{"seq":1,"turn":"t","kind":"turn_started","time":"2026-10-26T08:00:00Z"}"#
        let event = try APIDecoder.decode(SessionEvent.self, from: Data(text.utf8))
        let result = buildTimeline(turns: [], events: [event], now: .now)
        #expect(result == TimelineResult(items: [], working: true, pendingApproval: nil))
    }

    @Test func resolveWithEmptyOutcomeLeavesApprovalPending() {
        let time = Date(timeIntervalSince1970: 1_800_000_000)
        let approval = Approval(approvalId: "a", title: "T", options: [], command: nil)
        let events = [
            SessionEvent(seq: 1, turn: "t", kind: .approval, time: time, data: .approval(approval)),
            SessionEvent(seq: 2, turn: "t", kind: .approvalResolved, time: time,
                         data: .approvalResolved(approvalId: "a", outcome: ""))
        ]
        let result = buildTimeline(turns: [], events: events, now: time)
        #expect(result.pendingApproval == "a")
    }

    @Test func resolveWithoutOutcomeLeavesApprovalPending() throws {
        let text = #"{"log":"L","events":["#
            + #"{"seq":1,"turn":"t","kind":"approval","time":"2026-10-26T08:00:00Z","#
            + #""data":{"approvalId":"a","title":"T","options":[{"optionId":"o"}]}},"#
            + #"{"seq":2,"turn":"t","kind":"approval_resolved","time":"2026-10-26T08:00:01Z","#
            + #""data":{"approvalId":"a"}}]}"#
        let page = try APIDecoder.decode(EventPage.self, from: Data(text.utf8))
        let result = buildTimeline(turns: [], events: page.events, now: .now)
        let option = PermissionOption(optionId: "o", name: "", kind: "")
        let item = ApprovalItem(approvalId: "a", title: "T", command: nil, options: [option], outcome: nil)
        #expect(result.items == [.approval(item)])
        #expect(result.pendingApproval == "a")
    }
}

private final class ChatBundleToken {}

/// Row identities follow content, so a moving window of turns keeps the rows both reads share.
@Suite struct TimelineRowTests {
    @Test func rowsBothReadsShareKeepTheirIDs() {
        let first: [TimelineItem] = [.user("one"), .reply("first answer"), .user("two"), .reply("second answer")]
        let moved: [TimelineItem] = [.user("two"), .reply("second answer"), .user("three"), .reply("third answer")]
        let before = timelineRows(first), after = timelineRows(moved)
        #expect(after[0].id == before[2].id)
        #expect(after[1].id == before[3].id)
        #expect(after.map(\.index) == [0, 1, 2, 3])
        #expect(after[0].item == .user("two"))
        #expect(!before.map(\.id).contains(after[2].id), "a new turn gets a new id")
    }

    @Test func repeatedItemsGetDistinctIDs() {
        let rows = timelineRows([.reply("Done"), .stopped, .reply("Done"), .reply("Done")])
        #expect(Set(rows.map(\.id)).count == 4)
    }

    @Test func noItemsNoRows() {
        #expect(timelineRows([]).isEmpty)
    }
}
