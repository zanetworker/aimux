import Foundation
import Testing
@testable import AimuxKit

/// One file of api/testdata/clients/queue/ (README "Queue").
private struct QueueFixture: Decodable {
    struct Session: Decodable {
        let state: SessionState
        let terminal: TerminalStatus?
        let queue: QueueStatus?
    }
    struct Refusal: Decodable { let error: String }
    struct Composer: Decodable, Equatable { let action: String; let label: String }
    struct Section: Decodable, Equatable {
        let visible: Bool
        let state: String?
        let line: String?
        let resume: Bool?
    }
    struct Row: Decodable, Equatable {
        let id: String
        let text: String
        let state: String
        let label: String
        let canEdit: Bool
        let canMove: Bool
        let canDelete: Bool
    }
    struct Offer: Decodable, Equatable {
        let action: String
        let label: String
        let keepText: Bool
        let message: String?
    }
    struct Expect: Decodable {
        let composer: Composer
        let queue: Section
        let items: [Row]
        let offer: Offer?
        let message: String?
        let keepText: Bool?
    }
    let name: String
    let session: Session
    let send: Refusal?
    let add: Refusal?
    let delete: Refusal?
    let expect: Expect
}

private final class QueueBundleToken {}

private func queueFixtures() throws -> [QueueFixture] {
    let bundle = Bundle(for: QueueBundleToken.self)
    let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/queue") ?? []
    return try urls.sorted { $0.lastPathComponent < $1.lastPathComponent }.map {
        try APIDecoder.decode(QueueFixture.self, from: Data(contentsOf: $0))
    }
}

private func refusal(_ code: String, status: Int = 409) -> APIError {
    .server(status: status, code: APIErrorCode(rawValue: code), message: "refused")
}

private func row(_ item: QueueRow) -> QueueFixture.Row {
    QueueFixture.Row(id: item.id, text: item.text, state: item.state, label: item.label, canEdit: item.canEdit,
                     canMove: item.canMove, canDelete: item.canDelete)
}

private func section(_ view: QueueSection) -> QueueFixture.Section {
    QueueFixture.Section(visible: view.visible, state: view.state, line: view.line,
                         resume: view.visible ? view.resume : nil)
}

private let created = Date(timeIntervalSince1970: 1_800_000_000)

private func item(_ id: String, _ state: String = "waiting", _ text: String? = nil) -> QueueItem {
    QueueItem(id: id, text: text ?? "text \(id)", state: state, created: created)
}

@Suite struct ChatQueueFixtureTests {
    @Test func everyFixtureIsRead() throws {
        #expect(try queueFixtures().count == 37)
    }

    @Test func composerSectionAndRowsMatchEveryFixture() throws {
        for fixture in try queueFixtures() {
            let action = ChatQueue.composerAction(state: fixture.session.state, terminal: fixture.session.terminal,
                                                  queue: fixture.session.queue,
                                                  refusedBusy: fixture.send?.error == "busy")
            #expect(QueueFixture.Composer(action: action.rawValue, label: action.label) == fixture.expect.composer,
                    "\(fixture.name): composer")
            #expect(section(ChatQueue.section(fixture.session.queue)) == fixture.expect.queue,
                    "\(fixture.name): section")
            #expect(ChatQueue.rows(fixture.session.queue).map(row) == fixture.expect.items, "\(fixture.name): items")
            if let send = fixture.send {
                let offer = ChatQueue.sendRefusal(refusal(send.error)).map {
                    QueueFixture.Offer(action: $0.action.rawValue, label: $0.action.label, keepText: $0.keepText,
                                       message: $0.message)
                }
                #expect(offer == fixture.expect.offer, "\(fixture.name): offer")
            }
            if let delete = fixture.delete {
                #expect(ChatQueue.errorText(refusal(delete.error)) == fixture.expect.message, "\(fixture.name)")
            }
        }
    }

    /// A refused add keeps the text in the composer and says why (the model, against a fake server).
    @MainActor @Test func refusedAddsKeepTheTextAndSayWhy() async throws {
        for fixture in try queueFixtures() {
            guard let add = fixture.add else { continue }
            let source = StubChat()
            source.addFailures = [refusal(add.error)]
            let chat = ChatModel(sessionID: "s1", source: source, stream: StubStreams(), queueEnabled: true,
                                 sleep: { _ in }, now: { created })
            let queued = await chat.addToQueue("Some text")
            #expect(queued == !(fixture.expect.keepText ?? false), "\(fixture.name)")
            #expect(chat.addNotice == fixture.expect.message, "\(fixture.name)")
        }
    }
}

@Suite struct ChatQueueTests {
    @Test func freeSessionSendsAndNoDetailSends() {
        #expect(ChatQueue.composerAction(state: .idle, terminal: nil, queue: nil, refusedBusy: false) == .send)
        #expect(ChatQueue.composerAction(state: nil, terminal: nil, queue: nil, refusedBusy: false) == .send)
        #expect(ChatQueue.composerAction(state: .idle, terminal: nil, queue: nil, refusedBusy: true) == .queue)
        #expect(ComposerAction.queue.label == "Add to queue")
        #expect(ComposerAction.send.label == "Send")
    }

    /// The server refuses a direct send while the queue has one in flight or runs with some waiting.
    @Test func anActiveQueueOffersTheQueue() {
        let waiting = [item("q1")]
        #expect(ChatQueue.isActive(nil) == false)
        #expect(ChatQueue.isActive(QueueStatus(state: "running", items: [])) == false)
        #expect(ChatQueue.isActive(QueueStatus(state: "paused", items: waiting)) == false)
        #expect(ChatQueue.isActive(QueueStatus(state: "running", items: [item("q1", "done")])) == false)
        #expect(ChatQueue.isActive(QueueStatus(state: "running", items: waiting)))
        #expect(ChatQueue.isActive(QueueStatus(state: "paused", items: [item("q1", "answering")])))
        #expect(ChatQueue.composerAction(state: .idle, terminal: nil, queue: QueueStatus(state: "running",
                                                                                          items: waiting),
                                         refusedBusy: false) == .queue)
    }

    @Test func onlyBusyIsAnOfferToQueue() {
        #expect(ChatQueue.sendRefusal(refusal("in_use")) == nil)
        #expect(ChatQueue.sendRefusal(APIError.transport(.timedOut)) == nil)
    }

    @Test func noQueueOrOnlyDoneShowsNothing() {
        #expect(ChatQueue.section(nil) == QueueSection(visible: false))
        #expect(ChatQueue.rows(nil).isEmpty)
        let onlyDone = QueueStatus(state: "running", items: [item("q1", "done", "")])
        #expect(ChatQueue.section(onlyDone) == QueueSection(visible: false))
        #expect(ChatQueue.rows(onlyDone).isEmpty)
    }

    @Test func errorTextsAreFixedForQueueRefusals() {
        #expect(ChatQueue.errorText(refusal("unknown_item", status: 404)) == "This message is no longer in the queue.")
        #expect(ChatQueue.errorText(refusal("queue_full"))
                == "The queue is full (20 messages). Delete one to add another.")
        #expect(ChatQueue.errorText(APIError.transport(.notConnectedToInternet))
                == "Could not change the queue: You're offline")
    }

    /// A change that failed before the server answered says how: it may never have left the phone, or have
    /// been saved without the answer coming back.
    @Test func transportErrorTextsSayWhatFailed() {
        #expect(ChatQueue.errorText(APIError.transport(.timedOut))
                == "Could not change the queue: your Mac didn't answer in time. It may have changed; check the queue.")
        #expect(ChatQueue.errorText(APIError.transport(.networkConnectionLost))
                == "Could not change the queue: the connection to your Mac dropped. It may have changed; "
                    + "check the queue.")
        #expect(ChatQueue.errorText(CancellationError())
                == "Could not change the queue: the request was cancelled before it reached your Mac.")
        #expect(ChatQueue.errorText(APIError.transport(.cancelled))
                == "Could not change the queue: the request was cancelled before it reached your Mac.")
        #expect(ChatQueue.errorText(APIError.transport(.cannotConnectToHost))
                == "Could not change the queue: Can't reach your Mac")
    }

    private let mixed = QueueStatus(state: "running", items: [item("q1", "answering"), item("q2"), item("q3"),
                                                               item("q4")])

    @Test func moveByOneCountsOnlyMessagesNotInFlight() {
        #expect(ChatQueue.position(in: mixed, moving: "q3", by: -1) == 0)
        #expect(ChatQueue.position(in: mixed, moving: "q3", by: 1) == 2)
        #expect(ChatQueue.position(in: mixed, moving: "q2", by: -1) == nil, "never above the one in flight")
        #expect(ChatQueue.position(in: mixed, moving: "q4", by: 1) == nil, "the last cannot go down")
        #expect(ChatQueue.position(in: mixed, moving: "q1", by: 1) == nil, "the one in flight never moves")
        #expect(ChatQueue.position(in: mixed, moving: "nope", by: 1) == nil)
    }

    /// A drag in the list (List onMove offsets, among the rows shown) becomes the server's position.
    @Test func dragBecomesThePositionAmongWaitingMessages() {
        // q4 (row 3) dropped before q2 (row 1): first among the waiting ones.
        #expect(ChatQueue.move(in: mixed, from: 3, to: 1)?.id == "q4")
        #expect(ChatQueue.move(in: mixed, from: 3, to: 1)?.position == 0)
        // q2 dropped at the end.
        #expect(ChatQueue.move(in: mixed, from: 1, to: 4)?.position == 2)
        // Dropped above the one in flight: still first among the waiting, where q2 already is.
        #expect(ChatQueue.move(in: mixed, from: 1, to: 0) == nil)
        #expect(ChatQueue.move(in: mixed, from: 3, to: 0)?.position == 0)
        // Dropped where it was, or the one in flight dragged: nothing to change.
        #expect(ChatQueue.move(in: mixed, from: 2, to: 3) == nil)
        #expect(ChatQueue.move(in: mixed, from: 0, to: 4) == nil)
        #expect(ChatQueue.move(in: mixed, from: 9, to: 0) == nil)
    }

    @Test func movedQueueShowsTheNewOrderAtOnce() {
        let moved = ChatQueue.moved(mixed, id: "q4", to: 0)
        #expect(moved.items.map(\.id) == ["q1", "q4", "q2", "q3"])
        #expect(ChatQueue.moved(mixed, id: "q2", to: 9).items.map(\.id) == ["q1", "q3", "q4", "q2"], "clamped")
    }

    @Test func anEditStaysOpenWhileItsMessageWaits() {
        let edit = QueueEdit(id: "q2", original: "text q2", text: "changed")
        #expect(ChatQueue.editOutcome(edit, in: mixed) == .keep)
    }

    @Test func anEditWhoseMessageWasSentIsLostWithItsText() {
        let sent = QueueStatus(state: "running", items: [item("q2", "sending"), item("q3")])
        #expect(ChatQueue.editOutcome(QueueEdit(id: "q2", original: "text q2", text: "changed"), in: sent)
                == .lost("changed"))
        #expect(ChatQueue.editOutcome(QueueEdit(id: "q2", original: "text q2", text: "changed"), in: nil)
                == .lost("changed"), "the queue emptied")
        // Unchanged (or only spaces added): nothing to keep.
        #expect(ChatQueue.editOutcome(QueueEdit(id: "q2", original: "text q2", text: "text q2 "), in: sent)
                == .closed)
    }
}
