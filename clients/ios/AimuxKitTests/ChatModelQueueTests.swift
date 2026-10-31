import Foundation
import Testing
@testable import AimuxKit

private let base = Date(timeIntervalSince1970: 1_800_000_000)

private func item(_ id: String, _ state: String = "waiting") -> QueueItem {
    QueueItem(id: id, text: "text \(id)", state: state, created: base)
}

private func queue(_ ids: [String], state: String = "running", reason: String? = nil) -> QueueStatus {
    QueueStatus(state: state, reason: reason, items: ids.map { item($0) })
}

private func detail(_ state: SessionState = .idle, queue: QueueStatus? = nil,
                    terminal: TerminalStatus? = nil) -> SessionDetail {
    SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base, state: state, turns: [],
                  terminal: terminal, queue: queue)
}

private func event(_ seq: Int, _ kind: EventKind, _ data: EventData, turn: String = "t1") -> SessionEvent {
    SessionEvent(seq: seq, turn: turn, kind: kind, time: base.addingTimeInterval(Double(seq)), data: data)
}

private let busy = APIError.server(status: 409, code: .busy, message: "a turn is already running")

/// The chat's queue: read from the detail and the stream, changed through the server.
@MainActor @Suite struct ChatModelQueueTests {
    private func spin(until condition: () -> Bool) async {
        for _ in 0..<2000 where !condition() { await Task.yield() }
    }

    private func model(_ source: StubChat, _ streams: StubStreams = StubStreams(),
                       queueEnabled: Bool = true) -> ChatModel {
        ChatModel(sessionID: "s1", source: source, stream: streams, queueEnabled: queueEnabled,
                  sleep: { _ in try await Task.sleep(for: .seconds(3600)) }, now: { base })
    }

    @Test func addPostsWithAKeyThenReadsTheQueue() async throws {
        let source = StubChat()
        source.queueAnswer = queue(["q1"])
        let chat = model(source)

        #expect(await chat.addToQueue("  Run the tests  "))

        let calls = source.queueCallList
        #expect(calls.count == 2)
        #expect(calls[0].hasPrefix("add Run the tests "))
        #expect(calls[1] == "get")
        #expect(chat.queue == queue(["q1"]))
        #expect(chat.addNotice == nil)
    }

    @Test func aRetryOfTheSameTextKeepsItsKeyAndNewTextGetsANewOne() async throws {
        let source = StubChat()
        source.addFailures = [APIError.transport(.timedOut), nil, nil]
        let chat = model(source)

        #expect(await chat.addToQueue("Run the tests") == false)
        #expect(chat.addNotice
                == "Could not change the queue: your Mac didn't answer in time. It may have changed; check the queue.")
        #expect(await chat.addToQueue("Run the tests"))
        #expect(await chat.addToQueue("Run the tests"))

        let keys = source.queueCallList.filter { $0.hasPrefix("add") }.map { $0.split(separator: " ").last! }
        #expect(keys.count == 3)
        #expect(keys[0] == keys[1], "the retry after a failure keeps the key")
        #expect(keys[2] != keys[1], "once queued, the same text again is a new message")
    }

    @Test func emptyTextIsNotQueued() async {
        let source = StubChat()
        let chat = model(source)
        #expect(await chat.addToQueue("   ") == false)
        #expect(chat.addNotice == "A queued message needs some text.")
        #expect(source.queueCallList.isEmpty)
    }

    @Test func aFailedReadAfterTheAddStillCountsAsQueued() async {
        let source = StubChat()
        source.getFailures = [APIError.transport(.timedOut)]
        let chat = model(source)
        #expect(await chat.addToQueue("Run the tests"))
        #expect(chat.queue == nil)
        #expect(chat.addNotice == nil)
    }

    @Test func editSendsTheTrimmedTextAndShowsTheAnswer() async {
        let source = StubChat()
        source.queueAnswer = queue(["q1"])
        let chat = model(source)
        #expect(await chat.editQueueItem("q1", text: " New text "))
        #expect(source.queueCallList == ["edit q1 New text"])
        #expect(chat.queue == queue(["q1"]))
        #expect(await chat.editQueueItem("q1", text: "  ") == false)
        #expect(chat.queueError == "A queued message needs some text.")
        #expect(source.queueCallList.count == 1)
    }

    @Test func moveShowsTheNewOrderAtOnceAndRestoresItOnFailure() async {
        let source = StubChat()
        source.details = [detail(queue: queue(["q1", "q2", "q3"]))]
        source.changeFailures = [APIError.server(status: 409, code: .itemSending, message: "sending")]
        let gate = ChatGate()
        source.changeGate = gate
        let chat = model(source)
        await chat.load()

        let moving = Task { await chat.moveQueueItem(from: 2, to: 0) }
        await spin { source.queueCallList.count == 1 }
        #expect(source.queueCallList == ["move q3 0"])
        #expect(chat.queue?.items.map(\.id) == ["q3", "q1", "q2"], "shown before the answer")
        await gate.open()
        await moving.value

        #expect(chat.queue?.items.map(\.id) == ["q1", "q2", "q3"])
        #expect(chat.queueError == "This message is being sent and can't be changed.")
        chat.close()
    }

    @Test func moveByOneUsesThePositionAmongWaitingMessages() async {
        let source = StubChat()
        source.details = [detail(queue: QueueStatus(state: "running", items: [item("q1", "answering"), item("q2"),
                                                                              item("q3")]))]
        let chat = model(source)
        await chat.load()
        await chat.moveQueueItem("q3", by: -1)
        await chat.moveQueueItem("q3", by: 1)  // already last: nothing sent
        #expect(source.queueCallList == ["move q3 0"])
        chat.close()
    }

    @Test func deleteAndResumeShowTheAnswer() async {
        let source = StubChat()
        source.queueAnswer = queue(["q2"])
        let chat = model(source)
        await chat.deleteQueueItem("q1")
        #expect(chat.queue == queue(["q2"]))
        source.changeFailures = [APIError.server(status: 404, code: .unknownItem, message: "no such item")]
        await chat.resumeQueue()
        #expect(chat.queueError == "This message is no longer in the queue.")
        #expect(source.queueCallList == ["delete q1", "resume"])
    }

    @Test func aSecondChangeWhileOneRunsIsIgnored() async {
        let source = StubChat()
        let gate = ChatGate()
        source.changeGate = gate
        let chat = model(source)
        let first = Task { await chat.deleteQueueItem("q1") }
        await spin { source.queueCallList.count == 1 }
        await chat.deleteQueueItem("q2")
        await gate.open()
        await first.value
        #expect(source.queueCallList == ["delete q1"])
    }

    @Test func theDetailAndQueueEventsSetTheQueueOutsideTheTimeline() async {
        let source = StubChat()
        source.details = [detail(queue: queue(["q1"]))]
        let streams = StubStreams()
        let chat = model(source, streams)
        await chat.load()
        #expect(chat.queue == queue(["q1"]))

        chat.receive(StreamEvent(event: event(4, .queue, .queue(queue(["q1", "q2"])), turn: ""), log: "L1"))
        #expect(chat.queue == queue(["q1", "q2"]))
        #expect(chat.result.items.isEmpty)
        #expect(chat.result.working == false, "a queue event is of no turn, not a running one")
        chat.close()
    }

    @Test func theTimelineLeavesOutEventsOfNoTurn() {
        let snapshot = event(1, .queue, .queue(queue(["q1"])), turn: "")
        let other = event(2, .unknown("later"), .other, turn: "")
        let built = buildTimeline(turns: [], events: [snapshot, other], now: base)
        #expect(built.items.isEmpty)
        #expect(built.working == false)
    }

    @Test func aWorkingChatOffersTheQueueOnlyWhenTheServerHasOne() async {
        let source = StubChat()
        source.details = [detail(.busy)]
        source.snapshots = [EventPage(events: [event(1, .turnStarted, .turnStarted(text: "Go"))], log: "L1")]
        source.sinces = [EventPage(events: [], log: "L1")]
        let chat = model(source)
        await chat.load()
        #expect(chat.result.working)
        #expect(chat.composerAction == .queue)
        chat.close()

        let without = model(source, queueEnabled: false)
        await without.load()
        #expect(without.composerAction == .send)
        without.close()
    }

    @Test func aTerminalThatCannotTakeTypingOffersTheQueue() async {
        let source = StubChat()
        source.details = [detail(.openInTerminal, terminal: TerminalStatus(status: "idle", canType: false,
                                                                            reason: "mac_in_use", prompt: nil))]
        let chat = model(source)
        await chat.load()
        #expect(chat.state == .terminal)
        #expect(chat.composerAction == .queue)
        chat.close()
    }

    @Test func aSendRefusedBusyGoesIntoTheQueueByItself() async {
        let source = StubChat()
        source.details = [detail()]
        source.sendFailures = [busy]
        let chat = model(source)
        await chat.load()

        await chat.send("Run the tests")

        #expect(source.queueCallList.first?.hasPrefix("add Run the tests ") == true)
        #expect(chat.pending == nil, "queued: no failed message left to retry")
        #expect(chat.composerAction == .queue)
        chat.close()
    }

    @Test func theOfferEndsWithTheNextDetail() async {
        let source = StubChat()
        source.details = [detail()]
        source.sendFailures = [busy]
        let chat = model(source)
        await chat.load()
        await chat.send("Run the tests")
        #expect(chat.refusedBusy)
        await chat.load()
        #expect(chat.refusedBusy == false)
        chat.close()
    }

    @Test func withoutTheQueueABusyRefusalStaysAFailedSend() async {
        let source = StubChat()
        source.details = [detail()]
        source.sendFailures = [busy]
        let chat = model(source, queueEnabled: false)
        await chat.load()
        await chat.send("Run the tests")
        #expect(chat.refusedBusy == false)
        #expect(chat.reclaimRefusedMessage() == nil)
        #expect(chat.pending?.failure == busy)
        chat.close()
    }

    @Test func aLostEditGoesToAnEmptyComposerElseIsKeptWithItsOffer() async {
        let source = StubChat()
        let chat = model(source)
        #expect(chat.editLost("Edited", draftIsEmpty: true) == "Edited")
        #expect(chat.lostEdits.isEmpty)
        #expect(chat.editLost("First", draftIsEmpty: false) == nil)
        #expect(chat.editLost("First", draftIsEmpty: false) == nil)
        #expect(chat.editLost("Second", draftIsEmpty: false) == nil)
        #expect(chat.lostEdits.map(\.text) == ["First", "First", "Second"])
        #expect(Set(chat.lostEdits.map(\.id)).count == 3, "each offer has its own id, even for the same text")

        // Dismissing one of two equal texts removes that one only.
        let second = chat.lostEdits[1].id
        chat.dismissLostEdit(second)
        #expect(chat.lostEdits.map(\.id) == [chat.lostEdits[0].id, chat.lostEdits[1].id])
        #expect(!chat.lostEdits.contains { $0.id == second })

        let last = chat.lostEdits[1].id
        #expect(await chat.queueLostEdit(last))
        #expect(chat.lostEdits.map(\.text) == ["First"])
        #expect(source.queueCallList.first?.hasPrefix("add Second ") == true)

        // A refused add keeps the offer; an unknown id does nothing.
        source.addFailures = [APIError.server(status: 409, code: .queueFull, message: "full")]
        let first = chat.lostEdits[0].id
        #expect(await chat.queueLostEdit(first) == false)
        #expect(chat.lostEdits.map(\.id) == [first])
        chat.dismissLostEdit(UUID())
        #expect(chat.lostEdits.map(\.id) == [first])
    }

    @Test func aLostEditIsAnnouncedWithWhereItsTextWent() {
        #expect(ChatQueue.lostEditAnnouncement(inComposer: true)
                == "This message was sent before your edit was saved. Your text is in the message box.")
        #expect(ChatQueue.lostEditAnnouncement(inComposer: false)
                == "This message was sent before your edit was saved. Your text is kept above the message box.")
    }
}

/// What the live queue does to the composer's main button.
@MainActor @Suite struct ChatModelQueueComposerTests {
    private func queueModel(_ source: StubChat) -> ChatModel {
        ChatModel(sessionID: "s1", source: source, stream: StubStreams(), queueEnabled: true,
                  sleep: { _ in try await Task.sleep(for: .seconds(3600)) }, now: { base })
    }

    /// The live queue (from the detail or events) decides too: running with messages waiting, a new one queues.
    @Test func aRunningQueueWithWaitingItemsOffersTheQueue() async {
        let source = StubChat()
        source.details = [detail(queue: queue(["q1"]))]
        let chat = queueModel(source)
        await chat.load()
        #expect(chat.composerAction == .queue)
        chat.close()

        let paused = StubChat()
        paused.details = [detail(queue: queue(["q1"], state: "paused", reason: "No answer seen yet"))]
        let pausedChat = queueModel(paused)
        await pausedChat.load()
        #expect(pausedChat.composerAction == .send)
        pausedChat.close()
    }
}

/// A detail read that started before a queue change answers after it.
@MainActor @Suite struct ChatModelQueueRaceTests {
    private func spin(until condition: () -> Bool) async {
        for _ in 0..<2000 where !condition() { await Task.yield() }
    }

    @Test func aDetailReadBeforeAQueueEventKeepsTheNewerQueue() async {
        let source = StubChat()
        source.details = [detail(queue: queue(["q1"])), detail(queue: queue(["q1"]))]
        let chat = ChatModel(sessionID: "s1", source: source, stream: StubStreams(), queueEnabled: true,
                             sleep: { _ in try await Task.sleep(for: .seconds(3600)) }, now: { base })
        await chat.load()
        let gate = ChatGate()
        source.detailGate = gate

        // The turn ends: the detail is read again, and its answer is slow.
        chat.receive(StreamEvent(event: event(5, .turnDone, .turnDone(stopReason: "end_turn")), log: "L1"))
        await spin { source.count("detail") == 2 }
        let paused = queue(["q1"], state: "paused", reason: "Check the session, then resume.")
        chat.receive(StreamEvent(event: event(6, .queue, .queue(paused), turn: ""), log: "L1"))
        await gate.open()
        await spin { chat.queue != paused }
        #expect(chat.queue == paused, "the older detail's queue never replaces the event's")
        chat.close()
    }
}
