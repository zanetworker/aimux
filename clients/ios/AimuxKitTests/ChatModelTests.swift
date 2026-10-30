import Foundation
import Testing
@testable import AimuxKit

private let base = Date(timeIntervalSince1970: 1_800_000_000)

private func event(_ seq: Int, _ kind: EventKind, _ data: EventData, turn: String = "t1") -> SessionEvent {
    SessionEvent(seq: seq, turn: turn, kind: kind, time: base.addingTimeInterval(Double(seq)), data: data)
}

private func detail(_ state: SessionState = .idle, turns: [Turn] = []) -> SessionDetail {
    SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base, state: state, turns: turns)
}

private func turn(_ user: String, reply: String, at seconds: TimeInterval) -> Turn {
    Turn(number: 1, time: base.addingTimeInterval(seconds), user: user, reply: reply, tools: [])
}

private let option = PermissionOption(optionId: "allow", name: "Allow", kind: "allow_once")
private let approval = Approval(approvalId: "ap1", title: "Run ls", options: [option], command: "ls")

/// Background reads after an interactive open (reconnects, a turn's end) are polls, not use.
@MainActor @Suite struct ChatModelInteractionTests {
    private func spin(until condition: () -> Bool) async {
        for _ in 0..<2000 where !condition() { await Task.yield() }
    }

    private func model(_ source: StubChat, _ streams: StubStreams) -> ChatModel {
        ChatModel(sessionID: "s1", source: source, stream: streams, sleep: { _ in }, now: { base })
    }

    @Test func reconnectsAreNotInteractionsEvenAfterAnInteractiveOpen() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [], log: "L1")]
        source.details = [detail()]
        let streams = StubStreams()
        streams.queue = [[]]  // the first stream ends at once, the second stays open
        let chat = model(source, streams)

        await Interaction.perform { await chat.load() }
        await spin { streams.callList.count == 2 }

        #expect(source.snapshotInteractionList == [true, false])
        chat.close()
    }
}

@MainActor @Suite struct ChatModelTests {
    private func spin(until condition: () -> Bool) async {
        for _ in 0..<2000 where !condition() { await Task.yield() }
    }

    private func model(_ source: StubChat, _ streams: StubStreams, _ sleeps: Sleeps = Sleeps()) -> ChatModel {
        ChatModel(sessionID: "s1", source: source, stream: streams, sleep: { sleeps.record($0) }, now: { base })
    }

    @Test func loadRetriesWhenATurnStartsMeanwhile() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [], log: "L1"),
                            EventPage(events: [event(1, .turnStarted, .turnStarted(text: "Go"))], log: "L1")]
        source.sinces = [EventPage(events: [event(1, .turnStarted, .turnStarted(text: "Go"))], log: "L1"),
                         EventPage(events: [], log: "L1")]
        source.details = [detail(), detail(.busy)]
        let streams = StubStreams()
        let chat = model(source, streams)

        await chat.load()

        #expect(source.count("snapshot") == 2)
        #expect(source.count("detail") == 2)
        #expect(source.count("since") == 2)
        #expect(chat.session?.state == .busy)
        #expect(chat.result.items == [.user("Go")])
        #expect(chat.state == .live)
        await spin { !streams.callList.isEmpty }
        #expect(streams.callList == [StubStreams.Call(after: 1, log: "L1")])
        chat.close()
    }

    @Test func loadStopsAfterThreeAttempts() async {
        let source = StubChat()
        source.sinces = [EventPage(events: [], log: "L2")]
        source.details = [detail()]
        let chat = model(source, StubStreams())

        await chat.load()

        #expect(source.count("snapshot") == 3)
        #expect(source.count("detail") == 3)
        #expect(chat.state == .live)
        chat.close()
    }

    @Test func reconnectsAfterStreamEnds() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [event(3, .turnStarted, .turnStarted(text: "Go"))], log: "L1"),
                            EventPage(events: [event(2, .turnStarted, .turnStarted(text: "Again"))], log: "L2"),
                            EventPage(events: [event(5, .turnStarted, .turnStarted(text: "Third"))], log: "L3")]
        source.sinces = [EventPage(events: [], log: "L1"), EventPage(events: [], log: "L2"),
                         EventPage(events: [], log: "L3")]
        source.details = [detail()]
        let streams = StubStreams()
        streams.queue = [[StreamEvent(event: event(4, .update, .update(sessionUpdate: "agent_message_chunk",
                                                                        title: nil)), log: "L1")], []]
        let sleeps = Sleeps()
        let chat = model(source, streams, sleeps)

        await chat.load()
        await spin { streams.callList.count == 3 }

        #expect(streams.callList == [StubStreams.Call(after: 3, log: "L1"), StubStreams.Call(after: 2, log: "L2"),
                                     StubStreams.Call(after: 5, log: "L3")])
        // Each successful load starts the delay over at 2 s.
        #expect(sleeps.all == [.seconds(2), .seconds(2)])
        #expect(chat.state == .live)
        #expect(chat.result.items == [.user("Third")])

        chat.close()
        for _ in 0..<50 { await Task.yield() }
        #expect(streams.callList.count == 3)
    }

    @Test func backoffDoublesUpToThirtySeconds() {
        #expect(ChatModel.backoff(after: .seconds(2)) == .seconds(4))
        #expect(ChatModel.backoff(after: .seconds(16)) == .seconds(30))
        #expect(ChatModel.backoff(after: .seconds(30)) == .seconds(30))
    }

    @Test func terminalSessionIsReadOnly() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [event(1, .turnError, .turnError(error: "boom"))], log: "L1")]
        source.details = [detail(.openInTerminal, turns: [turn("Hello", reply: "Hi", at: 10)])]
        let streams = StubStreams()
        let chat = model(source, streams)

        await chat.load()
        for _ in 0..<50 { await Task.yield() }

        #expect(chat.state == .readOnly)
        #expect(chat.result.items == [.user("Hello"), .reply("Hi")])
        #expect(streams.callList.isEmpty)
        #expect(source.count("since") == 0)
    }

    @Test func sessionThatNeverStartedIsReadOnly() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [event(1, .turnError, .turnError(error: "boom"))], log: "L1")]
        source.details = [detail(.unavailable, turns: [turn("Hello", reply: "", at: 10)])]
        let streams = StubStreams()
        let chat = model(source, streams)

        await chat.load()
        for _ in 0..<50 { await Task.yield() }
        await chat.send("Go")

        #expect(chat.state == .readOnly)
        #expect(chat.result.items == [.user("Hello")])
        #expect(streams.callList.isEmpty)
        #expect(source.count("since") == 0)
        #expect(source.sentList.isEmpty)
    }

    @Test func sendShowsPendingThenKeepsKeyOnRetry() async throws {
        let source = StubChat()
        source.details = [detail()]
        source.sendFailures = [APIError.transport(.timedOut), nil]
        let gate = ChatGate()
        source.sendGate = gate
        let chat = model(source, StubStreams())
        await chat.load()

        let first = Task { await chat.send("Run the tests") }
        await spin { source.sentList.count == 1 }
        #expect(chat.pending?.text == "Run the tests")
        #expect(chat.pending?.failure == nil)
        await gate.open()
        await first.value
        #expect(chat.pending?.failure == .transport(.timedOut))

        await chat.send("Run the tests")
        let sent = source.sentList
        #expect(sent.count == 2)
        #expect(sent[0].key == sent[1].key)
        #expect(chat.pending?.failure == nil)
        #expect(chat.pending?.turn == "t9")

        await chat.send("Something else")
        #expect(source.sentList.last?.key != sent[0].key)
        chat.close()
    }

    @Test func pendingClearsWhenItsTurnArrives() async {
        let source = StubChat()
        source.details = [detail()]
        let streams = StubStreams()
        let chat = model(source, streams)
        await chat.load()
        await chat.send("Go")
        #expect(chat.pending?.turn == "t9")

        chat.receive(StreamEvent(event: event(1, .turnStarted, .turnStarted(text: "Go"), turn: "t9"), log: "L1"))

        #expect(chat.pending == nil)
        #expect(chat.result.items == [.user("Go")])
        #expect(chat.result.working)
        chat.close()
    }

    @Test func pendingClearsWhenItsTurnArrivesInAReload() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [], log: "L1"),
                            EventPage(events: [event(1, .turnStarted, .turnStarted(text: "Go"), turn: "t9"),
                                               event(2, .turnDone, .turnDone(stopReason: "end_turn"), turn: "t9")],
                                      log: "L2")]
        source.sinces = [EventPage(events: [], log: "L1"), EventPage(events: [], log: "L2")]
        source.details = [detail()]
        let chat = model(source, StubStreams())
        await chat.load()
        await chat.send("Go")
        #expect(chat.pending?.turn == "t9")

        await chat.load()

        #expect(chat.pending == nil)
        #expect(chat.result.items == [.user("Go")])
        chat.close()
    }

    @Test func pendingClearsWhenTheHistoryShowsItsPromptAfterTheSend() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [], log: "L1"), EventPage(events: [], log: "L2")]
        source.sinces = [EventPage(events: [], log: "L1"), EventPage(events: [], log: "L2")]
        source.details = [detail(turns: [turn("Go", reply: "Earlier", at: -60)]),
                          detail(turns: [turn("Go", reply: "Earlier", at: -60), turn("Go", reply: "Done", at: 5)])]
        let chat = model(source, StubStreams())
        await chat.load()
        await chat.send("Go")

        #expect(chat.pending != nil, "an older turn with the same prompt does not count")
        await chat.load()

        #expect(chat.pending == nil)
        chat.close()
    }

    @Test func failedSendStaysPendingAcrossAReload() async {
        let source = StubChat()
        source.details = [detail(), detail(turns: [turn("Go", reply: "Done", at: 5)])]
        source.sendFailures = [APIError.transport(.timedOut)]
        let chat = model(source, StubStreams())
        await chat.load()
        await chat.send("Go")

        await chat.load()

        #expect(chat.pending?.failure == .transport(.timedOut))
        chat.close()
    }

    @Test func sendIsIgnoredWhenReadOnly() async {
        let source = StubChat()
        source.details = [detail(.openInTerminal)]
        let chat = model(source, StubStreams())
        await chat.load()

        await chat.send("Go")

        #expect(source.sentList.isEmpty)
        #expect(chat.pending == nil)
    }

    @Test func stopPostsCancel() async {
        let source = StubChat()
        source.details = [detail()]
        let chat = model(source, StubStreams())
        await chat.load()

        await chat.stop()

        #expect(source.cancels == 1)
        chat.close()
    }

    @Test func staleApprovalCollapses() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [event(1, .turnStarted, .turnStarted(text: "Go")),
                                               event(2, .approval, .approval(approval))], log: "L1")]
        source.details = [detail(.busy)]
        source.answerFailure = APIError.server(status: 404, code: .noSuchApproval, message: "gone")
        let chat = model(source, StubStreams())
        await chat.load()
        #expect(chat.result.pendingApproval == "ap1")

        await chat.answer(approval, option: option)

        #expect(chat.answerErrors["ap1"] == nil)
        #expect(chat.answered == ["ap1"])
        #expect(chat.answering.isEmpty)
        #expect(chat.result.pendingApproval == nil)
        chat.close()
    }

    @Test func failedAnswerKeepsTheCard() async {
        let source = StubChat()
        source.snapshots = [EventPage(events: [event(1, .turnStarted, .turnStarted(text: "Go")),
                                               event(2, .approval, .approval(approval))], log: "L1")]
        source.details = [detail(.busy)]
        source.answerFailure = APIError.http(status: 502)
        let chat = model(source, StubStreams())
        await chat.load()

        await chat.answer(approval, option: option)

        #expect(chat.answerErrors["ap1"] == .http(status: 502))
        #expect(chat.answered.isEmpty)
        #expect(chat.result.pendingApproval == "ap1")
        chat.close()
    }

    @Test func unknownSessionFailsWithoutRetry() async {
        let streams = StubStreams()
        let sleeps = Sleeps()
        let chat = ChatModel(sessionID: "s1", source: MissingSession(), stream: streams,
                             sleep: { sleeps.record($0) })

        await chat.load()
        for _ in 0..<50 { await Task.yield() }

        #expect(chat.state == .failed("No session with that id."))
        #expect(sleeps.all.isEmpty)
        #expect(streams.callList.isEmpty)
    }

    @Test func unreachableServerFailsThenRetries() async {
        let sleeps = Sleeps()
        let chat = ChatModel(sessionID: "s1", source: Unreachable(), stream: StubStreams(),
                             sleep: { sleeps.record($0) })

        await chat.load()
        await spin { sleeps.all.count >= 2 }

        #expect(chat.state == .failed(APIError.transport(.notConnectedToInternet).userMessage.title))
        #expect(Array(sleeps.all.prefix(2)) == [.seconds(2), .seconds(4)])
        chat.close()
    }
}
