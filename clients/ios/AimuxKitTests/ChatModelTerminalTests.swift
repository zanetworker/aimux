import Foundation
import Testing
@testable import AimuxKit

private let base = Date(timeIntervalSince1970: 1_800_000_000)

private let yesNo = TerminalPrompt(promptId: "p-1", question: "Do you want to create a.txt?",
                                   options: [TerminalOption(key: "1", label: "Yes"),
                                             TerminalOption(key: "2", label: "No")])

private func detail(_ status: String = "idle", canType: Bool = true, prompt: TerminalPrompt? = nil,
                    turns: [Turn] = []) -> SessionDetail {
    SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base, state: .openInTerminal,
                  turns: turns, terminal: TerminalStatus(status: status, canType: canType, reason: nil, prompt: prompt))
}

private func turn(_ user: String, at seconds: TimeInterval) -> Turn {
    Turn(number: 1, time: base.addingTimeInterval(seconds), user: user, reply: "Done", tools: [])
}

/// A clock the test moves.
final class TestClock: @unchecked Sendable {
    private var value = base
    private let lock = NSLock()
    var now: Date { lock.withLock { value } }
    func advance(_ seconds: TimeInterval) { lock.withLock { value = value.addingTimeInterval(seconds) } }
}

/// A chat on a session open in a terminal on the Mac that the phone can type into.
@MainActor @Suite struct ChatModelTerminalTests {
    private func spin(until condition: () -> Bool) async {
        for _ in 0..<2000 where !condition() { await Task.yield() }
    }

    /// Polls wait until the chat closes, so only the reads the test causes happen.
    private func model(_ source: StubChat, _ sleeps: Sleeps = Sleeps(), clock: TestClock = TestClock()) -> ChatModel {
        ChatModel(sessionID: "s1", source: source, stream: StubStreams(),
                  sleep: { sleeps.record($0); try await Task.sleep(for: .seconds(3600)) }, now: { clock.now })
    }

    @Test func terminalSessionLoadsWithoutTheStream() async {
        let source = StubChat()
        source.details = [detail(turns: [turn("Hello", at: -60)])]
        let streams = StubStreams()
        let chat = ChatModel(sessionID: "s1", source: source, stream: streams,
                             sleep: { _ in try await Task.sleep(for: .seconds(3600)) }, now: { base })

        await chat.load()
        for _ in 0..<50 { await Task.yield() }

        #expect(chat.state == .terminal)
        #expect(chat.result.items == [.user("Hello"), .reply("Done")])
        #expect(streams.callList.isEmpty)
        #expect(source.count("since") == 0)
        chat.close()
    }

    @Test func sendTypesIntoTheTerminalAndARetryKeepsTheKey() async {
        let source = StubChat()
        source.details = [detail()]
        source.terminalFailures = [APIError.transport(.timedOut), nil]
        let chat = model(source)
        await chat.load()

        await chat.send("are you still blocked?")
        #expect(chat.pending?.failure == .transport(.timedOut))
        #expect(source.terminalSentList.count == 1, "a failed send is never retried on its own")
        #expect(source.sentList.isEmpty, "nothing goes to the aimux messages endpoint")

        await chat.send("are you still blocked?")
        let sent = source.terminalSentList
        #expect(sent.count == 2)
        #expect(sent[0] == StubChat.Sent(text: "are you still blocked?", key: sent[1].key))
        #expect(chat.pending?.failure == nil)
        #expect(chat.pending?.text == "are you still blocked?")
        chat.close()
    }

    @Test func notAtInputReadsTheDetailOnce() async {
        let source = StubChat()
        source.details = [detail(), detail("busy", canType: false, prompt: yesNo)]
        source.terminalFailures = [APIError.server(status: 409, code: .notAtInput, message: "not at input")]
        let chat = model(source)
        await chat.load()
        #expect(source.count("detail") == 1)

        await chat.send("go on")

        #expect(source.count("detail") == 2)
        #expect(chat.pending?.failure?.userMessage.title == "Claude isn't at its input box")
        #expect(chat.session?.terminal?.prompt == yesNo)
        #expect(chat.result.pendingApproval == "p-1")
        chat.close()
    }

    @Test func notSubmittedReadsTheDetailOnce() async {
        let source = StubChat()
        source.details = [detail(), detail("busy", canType: false, prompt: yesNo)]
        source.terminalFailures = [APIError.server(status: 409, code: .notSubmitted, message: "not sent")]
        let chat = model(source)
        await chat.load()

        await chat.send("go on")

        #expect(source.count("detail") == 2)
        #expect(chat.pending?.failure?.userMessage.title
                == "Your message is in the box but not sent; a prompt appeared")
        #expect(chat.result.pendingApproval == "p-1")
        chat.close()
    }

    @Test func otherRefusalsReadNothing() async {
        let source = StubChat()
        source.details = [detail()]
        source.terminalFailures = [APIError.server(status: 409, code: .macInUse, message: "in use")]
        let chat = model(source)
        await chat.load()

        await chat.send("go on")

        #expect(source.count("detail") == 1)
        #expect(chat.pending?.failure?.userMessage.title == "Your Mac is in use right now")
        chat.close()
    }

    @Test func answeringThePromptPressesItsOption() async {
        let source = StubChat()
        source.details = [detail("busy", canType: false, prompt: yesNo), detail("busy", canType: false)]
        let chat = model(source)
        await chat.load()
        #expect(chat.result.pendingApproval == "p-1")

        await chat.answer(yesNo, option: yesNo.options[1])

        #expect(source.pressList.count == 1)
        #expect(source.pressList.first?.promptID == "p-1")
        #expect(source.pressList.first?.option == "2")
        #expect(source.pressList.first?.key.isEmpty == false)
        #expect(chat.answering.isEmpty)
        #expect(chat.result.pendingApproval == nil)
        #expect(source.count("detail") == 2, "the screen is read again after a press")
        #expect(chat.answeredPrompts.isEmpty, "the prompt left the screen, so the same one asked again shows")
        chat.close()
    }

    @Test(arguments: [APIErrorCode.promptChanged, .noPrompt])
    func changedPromptCollapses(code: APIErrorCode) async {
        let other = TerminalPrompt(promptId: "p-2", question: "Do you want to run make?",
                                   options: [TerminalOption(key: "1", label: "Yes")])
        let source = StubChat()
        source.details = [detail("busy", canType: false, prompt: yesNo), detail("busy", canType: false, prompt: other)]
        source.pressFailure = APIError.server(status: 409, code: code, message: "changed")
        let chat = model(source)
        await chat.load()

        await chat.answer(yesNo, option: yesNo.options[0])

        #expect(chat.answerErrors["p-1"] == nil, "a stale card is not a failure")
        #expect(chat.result.pendingApproval == "p-2", "the screen is read again and its prompt shows")
        chat.close()
    }

    @Test func theSamePromptShowsAgainAfterTheNextRead() async {
        let source = StubChat()
        source.details = [detail("busy", canType: false, prompt: yesNo)]
        let gate = ChatGate()
        source.detailGate = gate
        let chat = model(source)
        source.detailGate = nil
        await chat.load()
        source.detailGate = gate

        let answering = Task { await chat.answer(yesNo, option: yesNo.options[0]) }
        await spin { source.count("detail") == 2 }
        #expect(chat.result.pendingApproval == nil, "collapsed until the screen is read again")
        await gate.open()
        await answering.value

        #expect(chat.answeredPrompts.isEmpty)
        #expect(chat.result.pendingApproval == "p-1", "Claude asked the same thing again")
        chat.close()
    }

    @Test func retryWaitsUntilTheTerminalCanType() async {
        let source = StubChat()
        source.details = [detail(), detail(canType: false)]
        source.terminalFailures = [APIError.server(status: 409, code: .notAtInput, message: "not at input")]
        let chat = model(source)
        await chat.load()
        #expect(chat.canRetryPending)

        await chat.send("go on")

        #expect(chat.pending?.failure != nil)
        #expect(!chat.canRetryPending)
        chat.close()
    }

    @Test func failedPressKeepsTheCardAndARetryKeepsTheKey() async {
        let source = StubChat()
        source.details = [detail("busy", canType: false, prompt: yesNo)]
        source.pressFailure = APIError.transport(.timedOut)
        let chat = model(source)
        await chat.load()

        await chat.answer(yesNo, option: yesNo.options[0])
        #expect(chat.answerErrors["p-1"] == .transport(.timedOut))
        #expect(chat.result.pendingApproval == "p-1")

        source.pressFailure = nil
        await chat.answer(yesNo, option: yesNo.options[0])
        let presses = source.pressList
        #expect(presses.count == 2)
        #expect(presses[0].key == presses[1].key)
        #expect(chat.answerErrors["p-1"] == nil)
        chat.close()
    }

    @Test func pendingClearsOnAWrappedHistoryTurn() async {
        let source = StubChat()
        let saved = "The user sent a new message while you were working:\nare you still blocked?"
        source.details = [detail(turns: [turn("earlier", at: -60)]),
                          detail(turns: [turn("earlier", at: -60), turn(saved, at: 3)])]
        let chat = ChatModel(sessionID: "s1", source: source, stream: StubStreams(), sleep: { _ in }, now: { base })
        await chat.load()

        await chat.send("are you still blocked?")
        await spin { chat.pending == nil }

        #expect(chat.pending == nil)
        chat.close()
    }

    @Test func pollsEveryTwoSecondsWhileBusyElseFifteen() async {
        let source = StubChat()
        source.details = [detail("busy"), detail("idle")]
        let sleeps = Sleeps()
        let chat = ChatModel(sessionID: "s1", source: source, stream: StubStreams(),
                             sleep: { sleeps.record($0) }, now: { base.addingTimeInterval(-3600) })
        await chat.load()
        await spin { sleeps.all.count >= 3 }
        chat.close()

        #expect(Array(sleeps.all.prefix(3)) == [.seconds(2), .seconds(15), .seconds(15)])
    }

    @Test func pollsEveryTwoSecondsForTwoMinutesAfterASend() async {
        let source = StubChat()
        source.details = [detail()]
        let sleeps = Sleeps()
        let clock = TestClock()
        let chat = model(source, sleeps, clock: clock)
        await chat.load()
        await spin { sleeps.all.count == 1 }
        #expect(sleeps.all == [.seconds(15)])

        await chat.send("go on")
        await spin { sleeps.all.count == 2 }
        #expect(sleeps.all.last == .seconds(2), "a send starts the next read at 2 s")

        #expect(ChatModel.terminalPollInterval(busy: false, lastSend: clock.now, now: clock.now + 119) == .seconds(2))
        #expect(ChatModel.terminalPollInterval(busy: false, lastSend: clock.now, now: clock.now + 120) == .seconds(15))
        #expect(ChatModel.terminalPollInterval(busy: true, lastSend: nil, now: clock.now) == .seconds(2))
        #expect(ChatModel.terminalPollInterval(busy: false, lastSend: nil, now: clock.now) == .seconds(15))
        chat.close()
    }

    @Test func leavingTheTerminalLoadsTheLiveChat() async {
        let source = StubChat()
        source.details = [detail("idle"),
                          SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base,
                                        state: .idle, turns: [])]
        let chat = ChatModel(sessionID: "s1", source: source, stream: StubStreams(), sleep: { _ in }, now: { base })
        await chat.load()
        await spin { chat.state == .live }

        #expect(chat.state == .live)
        chat.close()
    }

    /// A read that fails keeps the timeline on screen and says the chat could not refresh, until a read works.
    @Test func aFailedReadKeepsTheTimelineAndSaysSo() async {
        let source = StubChat()
        source.details = [detail(turns: [turn("Hello", at: -60)])]
        let chat = model(source)
        await chat.load()
        let shown = chat.result

        source.detailFailures = [APIError.transport(.timedOut)]
        #expect(await chat.readTerminal())
        #expect(chat.result == shown)
        #expect(chat.state == .terminal)
        #expect(chat.refreshFailure == .transport(.timedOut))

        #expect(await chat.readTerminal())
        #expect(chat.refreshFailure == nil)
        #expect(chat.result == shown)
        chat.close()
    }

    /// The reload line's Retry reads again at once, as something the person asked for.
    @Test func retryRefreshReadsAgain() async {
        let source = StubChat()
        source.details = [detail(turns: [turn("Hello", at: -60)])]
        let chat = model(source)
        await chat.load()
        source.detailFailures = [APIError.transport(.networkConnectionLost)]
        _ = await chat.readTerminal()
        let reads = source.count("detail")

        await chat.retryRefresh()
        #expect(source.count("detail") == reads + 1)
        #expect(chat.refreshFailure == nil)
        chat.close()
    }

    @Test func pollingStopsOnClose() async {
        let source = StubChat()
        source.details = [detail("busy")]
        let chat = ChatModel(sessionID: "s1", source: source, stream: StubStreams(), sleep: { _ in }, now: { base })
        await chat.load()
        await spin { source.count("detail") >= 3 }
        chat.close()
        for _ in 0..<20 { await Task.yield() }
        let reads = source.count("detail")
        for _ in 0..<200 { await Task.yield() }

        #expect(source.count("detail") == reads)
    }
}
