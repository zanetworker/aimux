import Foundation
import Testing
@testable import AimuxKit

private let base = Date(timeIntervalSince1970: 1_800_000_000)

private func detail() -> SessionDetail {
    SessionDetail(id: "s1", title: "Sample", project: "/src/sample", lastActive: base, state: .idle, turns: [])
}

@MainActor @Suite struct ChatDraftsTests {
    private let failed = PendingMessage(text: "Go", key: "k1", failure: .transport(.timedOut), turn: nil, sentAt: base)

    @Test func draftsArePerSession() {
        let drafts = ChatDrafts()
        drafts.setDraft("half a thought", for: "s1")
        #expect(drafts.draft(for: "s1") == "half a thought")
        #expect(drafts.draft(for: "s2") == "")
    }

    @Test func emptyDraftIsForgotten() {
        let drafts = ChatDrafts()
        drafts.setDraft("x", for: "s1")
        drafts.setDraft("", for: "s1")
        #expect(drafts.draft(for: "s1") == "")
        #expect(drafts.isEmpty)
    }

    @Test func keepsOnlyAFailedMessage() {
        let drafts = ChatDrafts()
        drafts.setPending(failed, for: "s1")
        #expect(drafts.pending(for: "s1") == failed)
        #expect(drafts.pending(for: "s2") == nil)

        var sending = failed
        sending.failure = nil
        drafts.setPending(sending, for: "s1")
        #expect(drafts.pending(for: "s1") == nil, "a message still sending or accepted is not kept")

        drafts.setPending(failed, for: "s1")
        drafts.setPending(nil, for: "s1")
        #expect(drafts.pending(for: "s1") == nil)
        #expect(drafts.isEmpty)
    }

    @Test func failedSendSurvivesClosingTheChat() async {
        let drafts = ChatDrafts()
        let source = StubChat()
        source.details = [detail()]
        source.sendFailures = [APIError.transport(.timedOut)]
        let first = ChatModel(sessionID: "s1", source: source, stream: StubStreams(), drafts: drafts,
                              sleep: { _ in }, now: { base })
        await first.load()
        await first.send("Run the tests")
        first.close()

        let again = ChatModel(sessionID: "s1", source: source, stream: StubStreams(), drafts: drafts,
                              sleep: { _ in }, now: { base })
        #expect(again.pending?.text == "Run the tests")
        #expect(again.pending?.failure == .transport(.timedOut))

        await again.load()
        await again.send("Run the tests")
        #expect(source.sentList.count == 2)
        #expect(source.sentList[0].key == source.sentList[1].key, "the retry keeps the key")
        #expect(drafts.pending(for: "s1") == nil, "an accepted message is no longer kept")
        again.close()
    }
}
