import Foundation
import Testing
@testable import AimuxKit

private struct MatchCase: Decodable {
    let name: String
    let saved: String
    let pending: String
    let match: Bool
}

private struct MatchFixture: Decodable { let cases: [MatchCase] }

/// The shared cases in api/testdata/clients/terminal/saved-prompt-match.json.
private func matchCases() throws -> [MatchCase] {
    let bundle = Bundle(for: TerminalBundleToken.self)
    let url = try #require(bundle.url(forResource: "saved-prompt-match", withExtension: "json",
                                      subdirectory: "clients/terminal"))
    return try JSONDecoder().decode(MatchFixture.self, from: Data(contentsOf: url)).cases
}

@Suite struct SavedPromptMatchTests {
    @Test func matchesSharedFixture() throws {
        let cases = try matchCases()
        #expect(cases.count >= 6)
        for item in cases {
            #expect(pendingShownInHistory(item.saved, item.pending) == item.match, "\(item.name)")
        }
    }

    @Test func fixtureListCoversTheFolder() throws {
        let bundle = Bundle(for: TerminalBundleToken.self)
        let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/terminal") ?? []
        #expect(Set(urls.map { $0.deletingPathExtension().lastPathComponent }) == ["saved-prompt-match"])
    }

    @Test func unwrapKeepsTheMessage() {
        #expect(unwrapSavedPrompt("The user sent a new message while you were working: hi") == "hi")
        #expect(unwrapSavedPrompt("<pasted_content a=\"b\">x</pasted_content>") == "x")
        #expect(unwrapSavedPrompt("plain") == "plain")
    }
}

private final class TerminalBundleToken {}

@Suite struct TerminalPresentationTests {
    private let now = Date(timeIntervalSince1970: 1_792_951_200)

    private func detail(_ terminal: TerminalStatus?) -> SessionDetail {
        SessionDetail(id: "s1", title: "Sample", project: "/p", lastActive: now.addingTimeInterval(-14 * 3600),
                      state: .openInTerminal, turns: [], terminal: terminal)
    }

    private func terminal(_ status: String, canType: Bool = true, reason: String? = nil) -> TerminalStatus {
        TerminalStatus(status: status, canType: canType, reason: reason, prompt: nil)
    }

    @Test(arguments: [
        ("disabled", "Typing into your Mac's terminal is off"),
        ("not_claude", "Claude isn't running in that terminal"),
        ("mac_in_use", "Your Mac is in use right now"),
        ("no_terminal_driver", "This terminal isn't supported yet"),
        ("automation_denied", "Allow aimux to control iTerm2 on your Mac"),
        ("not_at_input", "Claude isn't at its input box"),
        ("not_in_terminal", "Can't reach the terminal"),
        ("error", "Can't reach the terminal"),
        ("from_a_newer_server", "Can't reach the terminal")
    ])
    func reasonTexts(reason: String, text: String) {
        #expect(terminalReasonText(reason) == text)
        #expect(readOnlyText(detail(terminal("idle", canType: false, reason: reason))) == text)
    }

    private func blocked(_ reason: String, queue: QueueStatus? = nil, prompt: TerminalPrompt? = nil) -> SessionDetail {
        SessionDetail(id: "s1", title: "Sample", project: "/p", lastActive: now, state: .openInTerminal, turns: [],
                      terminal: TerminalStatus(status: "idle", canType: false, reason: reason, prompt: prompt),
                      queue: queue)
    }

    /// With the chat queue, the Mac in use is one line: the spec's wording, or the queue's waiting note alone.
    /// The queue is the chat's live one (from events), not the detail's.
    @Test func withTheQueueTheMacInUseIsOneLine() {
        let note = QueueStatus(state: "running", note: "Waiting: you're using this session on your Mac",
                               items: [QueueItem(id: "q1", text: "Next", state: "waiting", created: now)])
        #expect(terminalReasonLine(blocked("mac_in_use"), queue: true, queueStatus: nil)
                == "You're using this session on your Mac.")
        #expect(terminalReasonLine(blocked("mac_in_use"), queue: true, queueStatus: note) == nil,
                "the queue's waiting note says it")
        #expect(terminalReasonLine(blocked("mac_in_use"), queue: false, queueStatus: note)
                == "Your Mac is in use right now", "a server without the queue keeps the old line")
        #expect(terminalReasonLine(blocked("disabled"), queue: true, queueStatus: note)
                == "Typing into your Mac's terminal is off")
        #expect(terminalReasonLine(blocked("mac_in_use", queue: note), queue: true, queueStatus: nil)
                == "You're using this session on your Mac.", "a stale detail queue is not the live one")
    }

    /// A prompt on screen is the caller's to check (ChatView shows the prompt's card instead, from the live
    /// prompt); the line itself only needs a terminal that cannot take typing.
    @Test func noReasonLineWhenItCanType() {
        #expect(terminalReasonLine(detail(terminal("idle")), queue: true, queueStatus: nil) == nil)
        #expect(terminalReasonLine(nil, queue: true, queueStatus: nil) == nil)
        let prompt = TerminalPrompt(promptId: "p", question: "Q?", options: [TerminalOption(key: "1", label: "Yes")])
        #expect(terminalReasonLine(blocked("not_at_input", prompt: prompt), queue: false, queueStatus: nil)
                == "Claude isn't at its input box", "a detail's stale prompt does not hide the line")
    }

    @Test func headerFollowsTheTerminal() {
        #expect(chatStatus(detail: detail(terminal("busy")), readOnly: false, result: .empty, now: now)
                == ChatStatus(tone: .working, label: "On your Mac · Working"))
        #expect(chatStatus(detail: detail(terminal("idle")), readOnly: false, result: .empty, now: now)
                == ChatStatus(tone: .idle, label: "On your Mac · Your turn"))
        let asking = TimelineResult(items: [], working: false, pendingApproval: "p-1")
        #expect(chatStatus(detail: detail(terminal("busy")), readOnly: false, result: asking, now: now)
                == ChatStatus(tone: .needsYou, label: "On your Mac · Your turn"))
        #expect(chatStatus(detail: detail(terminal("unknown")), readOnly: false, result: .empty, now: now)
                == ChatStatus(tone: .idle, label: "Open on your Mac · 14h ago"))
    }

    @Test func pendingLineChangesAfterTwoMinutes() {
        #expect(terminalPendingText(sentAt: now, now: now) == "Sent to your Mac's terminal")
        #expect(terminalPendingText(sentAt: now, now: now.addingTimeInterval(119)) == "Sent to your Mac's terminal")
        #expect(terminalPendingText(sentAt: now, now: now.addingTimeInterval(120)) == "Sent; no answer yet")
    }

    @Test func promptBecomesAnApprovalCard() {
        let prompt = TerminalPrompt(promptId: "p-1", question: "Do you want to create a.txt?", options: [
            TerminalOption(key: "1", label: "Yes"),
            TerminalOption(key: "2", label: "Yes, allow all edits during this session"),
            TerminalOption(key: "3", label: "No, and tell Claude what to do differently")
        ])
        let item = prompt.approvalItem
        #expect(item.approvalId == "p-1")
        #expect(item.title == "Do you want to create a.txt?")
        #expect(item.command == nil)
        #expect(item.options.map(\.optionId) == ["1", "2", "3"])
        #expect(item.options.map(\.kind) == ["allow_once", "allow_always", "reject_once"])
    }

    @Test func promptDetailIsTheCardCommand() {
        let detail = "Bash command\n\n  ls -la\n  List files in the current directory"
        let prompt = TerminalPrompt(promptId: "p-2", question: "Do you want to proceed?", detail: detail,
                                    options: [TerminalOption(key: "1", label: "Yes")])
        #expect(prompt.approvalItem.command == detail)
        let empty = TerminalPrompt(promptId: "p-3", question: "q", detail: "", options: [])
        #expect(empty.approvalItem.command == nil)
    }

    @Test func terminalRefusalsSayWhy() {
        let failure = APIError.server(status: 409, code: .notAtInput, message: "the input box is not ready")
        #expect(failure.userMessage.title == "Claude isn't at its input box")
        #expect(APIError.server(status: 403, code: .disabled, message: "off").userMessage.title
                == "Typing into your Mac's terminal is off")
    }
}
