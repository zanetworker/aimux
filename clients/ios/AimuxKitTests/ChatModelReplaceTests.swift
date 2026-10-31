import Foundation
import Testing
@testable import AimuxKit

private let base = Date(timeIntervalSince1970: 1_800_000_000)

/// A terminal session whose input box holds text: a draft, or Claude Code's grey suggestion.
private func boxWithText(canType: Bool = false) -> SessionDetail {
    SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base, state: .openInTerminal,
                  turns: [], terminal: TerminalStatus(status: "idle", canType: canType, reason: "not_at_input",
                                                      prompt: nil, boxHasText: true))
}

/// Replace: the person asks to clear the text in the Mac's input box and send theirs.
@MainActor @Suite struct ChatModelReplaceTests {
    private func model(_ source: StubChat) -> ChatModel {
        ChatModel(sessionID: "s1", source: source, stream: StubStreams(),
                  sleep: { _ in try await Task.sleep(for: .seconds(3600)) }, now: { base })
    }

    @Test func replacingSendsWithReplaceAndNeverAPlainSend() async {
        let source = StubChat()
        source.details = [boxWithText()]
        let chat = model(source)
        await chat.load()
        #expect(chat.canReplaceBox)

        await chat.send("run the tests", replacing: true)

        #expect(source.terminalReplacedList.map(\.text) == ["run the tests"])
        #expect(source.terminalSentList.isEmpty)
        chat.close()
    }

    @Test func aFailedSendCanBeRetriedAsAReplace() async {
        let source = StubChat()
        source.details = [boxWithText()]
        source.terminalFailures = [APIError.server(status: 409, code: .notAtInput, message: "not at input")]
        let chat = model(source)
        await chat.load()
        await chat.send("run the tests")
        #expect(chat.pending?.failure != nil)

        await chat.send("run the tests", replacing: true)

        let replaced = source.terminalReplacedList
        #expect(replaced.map(\.text) == ["run the tests"])
        #expect(replaced.first?.key == source.terminalSentList.first?.key, "the retry keeps the key")
        chat.close()
    }

    @Test func noReplaceWithoutTextInTheBox() async {
        let source = StubChat()
        source.details = [SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base,
                                        state: .openInTerminal, turns: [],
                                        terminal: TerminalStatus(status: "idle", canType: false,
                                                                 reason: "mac_in_use", prompt: nil))]
        let chat = model(source)
        await chat.load()
        #expect(!chat.canReplaceBox)
        chat.close()
    }

    @Test func theLineSaysTheBoxHasText() {
        #expect(terminalReasonLine(boxWithText(), queue: false, queueStatus: nil)
                == "There's text in the box on your Mac")
        #expect(readOnlyText(boxWithText()) == "There's text in the box on your Mac")
    }

    @Test func decodesBoxHasTextAndDefaultsToFalse() throws {
        let with = try JSONDecoder().decode(TerminalStatus.self, from: Data(
            #"{"status":"idle","canType":false,"working":false,"reason":"not_at_input","boxHasText":true}"#.utf8))
        #expect(with.boxHasText)
        let without = try JSONDecoder().decode(TerminalStatus.self, from: Data(
            #"{"status":"idle","canType":true,"working":false}"#.utf8))
        #expect(!without.boxHasText)
    }
}
