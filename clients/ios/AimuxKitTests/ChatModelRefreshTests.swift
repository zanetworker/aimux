import Foundation
import Testing
@testable import AimuxKit

private let base = Date(timeIntervalSince1970: 1_800_000_000)

private func event(_ seq: Int, _ kind: EventKind, _ data: EventData, turn: String = "t1") -> SessionEvent {
    SessionEvent(seq: seq, turn: turn, kind: kind, time: base.addingTimeInterval(Double(seq)), data: data)
}

private func detail(_ state: SessionState = .idle) -> SessionDetail {
    SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base, state: state, turns: [])
}

private func turn(_ user: String, reply: String, at seconds: TimeInterval) -> Turn {
    Turn(number: 1, time: base.addingTimeInterval(seconds), user: user, reply: reply, tools: [])
}

/// What the chat does when a turn ends or the server says its view is out of date.
@MainActor @Suite struct ChatModelRefreshTests {
    private func spin(until condition: () -> Bool) async {
        for _ in 0..<2000 where !condition() { await Task.yield() }
    }

    private func model(_ source: StubChat, _ streams: StubStreams = StubStreams()) -> ChatModel {
        ChatModel(sessionID: "s1", source: source, stream: streams, sleep: { _ in }, now: { base })
    }

    @Test(arguments: [EventKind.turnDone, EventKind.turnError])
    func turnEndReadsTheSessionAgain(kind: EventKind) async {
        let stats = SessionStats(costUsd: 0.42, tokensIn: 1200, tokensOut: 300, cacheReadTokens: 0,
                                 cacheWriteTokens: 0, turns: 1, startTime: base, durationMin: 3,
                                 model: "sample-model", gitBranch: "main")
        let source = StubChat()
        source.details = [detail(.busy),
                          SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base,
                                        state: .idle, turns: [turn("Go", reply: "Done", at: 5)], stats: stats)]
        let chat = model(source, StubStreams())
        await chat.load()
        chat.receive(StreamEvent(event: event(1, .turnStarted, .turnStarted(text: "Go")), log: "L1"))
        #expect(source.count("detail") == 1, "a turn starting reads nothing")

        let end: EventData = kind == .turnDone ? .turnDone(stopReason: "end_turn") : .turnError(error: "boom")
        chat.receive(StreamEvent(event: event(2, kind, end), log: "L1"))
        await spin { chat.session?.stats != nil }

        #expect(source.count("detail") == 2)
        #expect(chat.session?.stats == stats)
        #expect(chat.session?.state == .idle)
        chat.close()
    }

    @Test func turnEndReadIsDroppedAfterClose() async {
        let source = StubChat()
        source.details = [detail(.busy), detail(.idle)]
        let chat = model(source, StubStreams())
        await chat.load()

        chat.receive(StreamEvent(event: event(1, .turnDone, .turnDone(stopReason: "end_turn")), log: "L1"))
        chat.close()
        await spin { source.count("detail") == 2 }
        for _ in 0..<50 { await Task.yield() }

        #expect(chat.session?.state == .busy)
    }

    @Test func sendRefusedAsOpenInTerminalLoadsAgain() async {
        let source = StubChat()
        source.details = [detail(), detail(.openInTerminal)]
        source.sendFailures = [APIError.server(status: 409, code: .openInTerminal, message: "open in a terminal")]
        let chat = model(source)
        await chat.load()

        await chat.send("Go")

        #expect(source.count("detail") == 2)
        #expect(chat.state == .readOnly)
        chat.close()
    }

    @Test func otherSendFailureDoesNotLoad() async {
        let source = StubChat()
        source.details = [detail()]
        source.sendFailures = [APIError.server(status: 409, code: .busy, message: "busy")]
        let chat = model(source)
        await chat.load()

        await chat.send("Go")

        #expect(source.count("detail") == 1)
        #expect(chat.pending?.failure == .server(status: 409, code: .busy, message: "busy"))
        chat.close()
    }

    @Test func stopWithNoTurnRunningLoadsAgain() async {
        let source = StubChat()
        source.details = [detail(.busy), detail(.idle)]
        source.cancelFailure = APIError.server(status: 409, code: .notRunning, message: "no turn")
        let chat = model(source)
        await chat.load()

        await chat.stop()

        #expect(source.count("detail") == 2)
        #expect(chat.stopError == nil)
        #expect(chat.session?.state == .idle)
        chat.close()
    }

    @Test func otherStopFailureIsShown() async {
        let source = StubChat()
        source.details = [detail(.busy)]
        source.cancelFailure = APIError.http(status: 502)
        let chat = model(source)
        await chat.load()

        await chat.stop()

        #expect(source.count("detail") == 1)
        #expect(chat.stopError == .http(status: 502))
        chat.close()
    }
}
