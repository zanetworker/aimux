import Foundation
import Testing
@testable import AimuxKit

private let base = Date(timeIntervalSince1970: 1_800_000_000)

private func item(_ id: String, _ state: String = "waiting") -> QueueItem {
    QueueItem(id: id, text: "text \(id)", state: state, created: base)
}

private func working(queue: QueueStatus?) -> SessionDetail {
    SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base, state: .openInTerminal,
                  turns: [], terminal: TerminalStatus(status: "busy", canType: true, reason: nil, prompt: nil,
                                                      working: true),
                  queue: queue)
}

private func refusal(_ code: APIErrorCode) -> APIError { .server(status: 409, code: code, message: "refused") }

/// Send now: a queued message or the composer's text goes to Claude during the turn (terminal sessions only).
@MainActor @Suite struct ChatModelSendNowTests {
    private func model(_ source: StubChat, sendNow: Bool = true) -> ChatModel {
        ChatModel(sessionID: "s1", source: source, stream: StubStreams(), queueEnabled: true, sendNowEnabled: sendNow,
                  sleep: { _ in try await Task.sleep(for: .seconds(3600)) }, now: { base })
    }

    @Test func offeredForATerminalSessionWithTheCapability() async {
        let source = StubChat()
        let waiting = QueueStatus(state: "running", items: [item("q1", "answering"), item("q2")])
        source.details = [working(queue: waiting)]
        let chat = model(source)
        await chat.load()
        #expect(chat.sendNowOffer == SendNowOffer(composer: true, items: ["q2"]))
        chat.close()

        let without = model(source, sendNow: false)
        await without.load()
        #expect(without.sendNowOffer == SendNowOffer(composer: false, items: []))
        without.close()
    }

    @Test func aQueuedMessageSentNowLeavesTheQueueAndSaysSo() async {
        let source = StubChat()
        source.details = [working(queue: QueueStatus(state: "running", items: [item("q1"), item("q2")]))]
        source.queueAnswer = QueueStatus(state: "running", items: [item("q1")])
        let chat = model(source)
        await chat.load()

        #expect(await chat.sendQueueItemNow("q2"))
        #expect(source.queueCallList.count == 1)
        #expect(source.queueCallList.first?.hasPrefix("now q2 ") == true)
        #expect(chat.queue == QueueStatus(state: "running", items: [item("q1")]))
        #expect(chat.sendNowNotice == "Sent now; Claude sees it during this turn")
        #expect(chat.queueError == nil)
        chat.close()
    }

    @Test func aRefusedSendNowKeepsTheMessageAndSaysWhy() async {
        let cases: [(APIError, String)] = [
            (refusal(.notSupported), "Send now works only for sessions open in a terminal on your Mac."),
            (refusal(.macInUse), "Your Mac is in use right now"),
            (refusal(.draftInBox), "Your draft on the Mac is kept; nothing was sent"),
            (APIError.server(status: 404, code: .unknownItem, message: "x"), "This message is no longer in the queue.")
        ]
        for (failure, line) in cases {
            let source = StubChat()
            let before = QueueStatus(state: "running", items: [item("q1"), item("q2")])
            source.details = [working(queue: before)]
            source.changeFailures = [failure]
            let chat = model(source)
            await chat.load()
            #expect(await chat.sendQueueItemNow("q2") == false)
            #expect(chat.queue == before, "\(line): the message stays in place")
            #expect(chat.queueError == line)
            #expect(chat.sendNowNotice == nil)
            chat.close()
        }
    }

    @Test func aLostAnswerRetriesWithTheSameKey() async {
        let source = StubChat()
        source.details = [working(queue: QueueStatus(state: "running", items: [item("q2")]))]
        source.changeFailures = [APIError.transport(.timedOut), nil]
        let chat = model(source)
        await chat.load()
        #expect(await chat.sendQueueItemNow("q2") == false)
        #expect(await chat.sendQueueItemNow("q2"))
        let calls = source.queueCallList
        #expect(calls.count == 2 && calls[0] == calls[1], "same key: \(calls)")
        chat.close()
    }

    @Test func theComposersTextSentNowIsTypedWithNow() async {
        let source = StubChat()
        source.details = [working(queue: nil)]
        let chat = model(source)
        await chat.load()

        await chat.sendNow("Stop, wrong file")

        #expect(source.terminalSentList.isEmpty, "not a plain terminal send")
        #expect(source.terminalNowList.map(\.text) == ["Stop, wrong file"])
        #expect(chat.sendNowNotice == "Sent now; Claude sees it during this turn")
        chat.close()
    }

    @Test func aFailedComposerSendNowRetriesAsSendNow() async {
        let source = StubChat()
        source.details = [working(queue: nil)]
        source.terminalFailures = [refusal(.macInUse), nil]
        let chat = model(source)
        await chat.load()

        await chat.sendNow("Stop, wrong file")
        #expect(chat.pending?.failure != nil)
        #expect(chat.sendNowNotice == nil)
        await chat.send("Stop, wrong file") // the failed message's Retry
        let sent = source.terminalNowList
        #expect(sent.count == 2 && sent[0].key == sent[1].key)
        #expect(source.terminalSentList.isEmpty)
        chat.close()
    }
}
