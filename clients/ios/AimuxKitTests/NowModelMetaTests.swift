import Foundation
import Testing
@testable import AimuxKit

private let now = Date(timeIntervalSince1970: 200_000)

private final class ListStub: SessionSource, @unchecked Sendable {
    var list: SessionList
    init(_ list: SessionList) { self.list = list }
    func sessions(limit: Int) async throws -> [Session] { list.sessions }
    func sessionList(limit: Int) async throws -> SessionList { list }
}

private struct NoActivity: ActivitySource {
    func eventSnapshot(sessionID: String) async throws -> EventPage { EventPage(events: [], log: "") }
    func answer(approvalID: String, optionID: String) async throws {}
    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted {
        TurnAccepted(id: sessionID, turn: "t", after: 0, events: "")
    }
    func session(id: String, turns: Int) async throws -> SessionDetail {
        SessionDetail(id: id, title: "", project: "", lastActive: .now, state: .idle, turns: [])
    }
}

private actor Latch {
    private var open = false
    private var waiters: [CheckedContinuation<Void, Never>] = []
    func wait() async {
        if open { return }
        await withCheckedContinuation { waiters.append($0) }
    }
    func release() {
        open = true
        waiters.forEach { $0.resume() }
        waiters = []
    }
}

/// Records meta and describe calls; describe answers per session after its latch opens.
private final class MetaStub: SessionMetaSource, @unchecked Sendable {
    struct Edit: Equatable { let id: String; let change: MetaChange }
    private let lock = NSLock()
    private var edits: [Edit] = []
    private var describes: [String] = []
    var editFailure: APIError?
    var editLatch: Latch?
    /// Answer an empty title with the title as it was ("T <id>"), as the server does for one the owner did not write.
    var keepTitle = false
    var describeResults: [String: Result<DescribeResult, APIError>] = [:]
    var latches: [String: Latch] = [:]
    var health = Health(status: "ok", login: "me", version: "v", acpProtocolVersion: 1,
                        capabilities: ["sessions", "describe"],
                        naming: NamingStatus(today: 2, limit: 20, failing: false, reason: nil, paused: nil))

    var editList: [Edit] { lock.withLock { edits } }
    var describeList: [String] { lock.withLock { describes } }

    func health() async throws -> Health { health }

    func editMeta(sessionID: String, _ change: MetaChange) async throws -> SessionMeta {
        lock.withLock { edits.append(Edit(id: sessionID, change: change)) }
        await editLatch?.wait()
        if let editFailure { throw editFailure }
        let cleared = keepTitle ? "T \(sessionID)" : "Auto \(sessionID)"
        return SessionMeta(title: change.title.map { $0.isEmpty ? cleared : $0 } ?? "T \(sessionID)",
                           description: change.description, pinned: change.pinned ?? false)
    }

    func describe(sessionID: String) async throws -> DescribeResult {
        let latch = lock.withLock { () -> Latch? in
            describes.append(sessionID)
            return latches[sessionID]
        }
        await latch?.wait()
        return try (describeResults[sessionID] ?? .failure(.transport(.timedOut))).get()
    }
}

private func row(_ id: String, _ state: SessionState = .idle, ago: TimeInterval = 60, pinned: Bool = false,
                 description: String? = nil) -> Session {
    Session(id: id, title: "T \(id)", project: "/p/\(id)", lastActive: now.addingTimeInterval(-ago), state: state,
            description: description, pinned: pinned)
}

@MainActor
private func makeModel(_ list: SessionList, meta: MetaStub) -> NowModel {
    let sessions = SessionListModel(source: ListStub(list), cache: nil)
    let defaults = UserDefaults(suiteName: "NowModelMetaTests-\(UUID().uuidString)")!
    return NowModel(list: sessions, source: NoActivity(), meta: meta, defaults: defaults, now: { now })
}

@MainActor @Suite struct NowModelMetaTests {
    private func spin(until condition: () -> Bool) async {
        for _ in 0..<2000 where !condition() { await Task.yield() }
    }

    @Test func lastOnMacFromTheListLeadsNow() async {
        let list = SessionList(sessions: [row("a", .openInTerminal, ago: 3600), row("b", pinned: true)],
                               lastOnMac: LastOnMac(id: "a", at: now.addingTimeInterval(-240)))
        let model = makeModel(list, meta: MetaStub())
        await model.refresh()
        #expect(model.view.lastOnMac?.session.id == "a")
        #expect(model.view.lastOnMacAt == now.addingTimeInterval(-240))
        #expect(model.view.pinned.map(\.session.id) == ["b"])
        #expect(model.view.onMacTotal == 0, "shown once")
    }

    @Test func pinMovesTheSessionAndUnpinMovesItBack() async {
        let meta = MetaStub()
        let model = makeModel(SessionList(sessions: [row("a"), row("b")]), meta: meta)
        await model.refresh()
        let session = model.view.pinned.first?.session ?? row("a")

        await model.setPinned(session, true)
        #expect(meta.editList == [.init(id: "a", change: MetaChange(pinned: true))])
        #expect(model.view.pinned.map(\.session.id) == ["a"])
        #expect(model.listedSession("a")?.pinned == true)
        #expect(model.pinErrors.isEmpty)

        await model.setPinned(row("a", pinned: true), false)
        #expect(model.view.pinned.isEmpty)
    }

    @Test func aSecondPinWhileOneRunsSendsNothingAndSaysSo() async {
        let meta = MetaStub()
        let latch = Latch()
        meta.editLatch = latch
        let model = makeModel(SessionList(sessions: [row("a")]), meta: meta)
        await model.refresh()

        let first = Task { await model.setPinned(row("a"), true) }
        await spin { meta.editList.count == 1 }
        #expect(model.pinning == ["a"])
        // The row menu or the sheet tapped again: skipped, so nothing is announced.
        #expect(await model.setPinned(row("a"), true) == false)
        #expect(meta.editList.count == 1)

        await latch.release()
        #expect(await first.value == true)
        #expect(model.pinning.isEmpty)
    }

    @Test func pinFailureIsKeptForTheRowUntilTheNextTry() async {
        let meta = MetaStub()
        meta.editFailure = .transport(.notConnectedToInternet)
        let model = makeModel(SessionList(sessions: [row("a")]), meta: meta)
        await model.refresh()
        await model.setPinned(row("a"), true)
        #expect(model.pinErrors["a"] == "Could not pin: your Mac could not be reached.")
        #expect(model.view.pinned.isEmpty)
        meta.editFailure = nil
        await model.setPinned(row("a"), true)
        #expect(model.pinErrors["a"] == nil)
    }

    @Test func unchangedSaveSendsNothing() async {
        let meta = MetaStub()
        let model = makeModel(SessionList(sessions: [row("a")]), meta: meta)
        await model.refresh()
        var edit = MetaEdit(field: .title, original: "T a")
        edit.draft = "  T a "
        #expect(await model.save(edit, sessionID: "a") == .unchanged)
        #expect(meta.editList.isEmpty)
    }

    @Test func saveSendsTheFieldAndTheListTakesTheAnswer() async {
        let meta = MetaStub()
        let model = makeModel(SessionList(sessions: [row("a"), row("b")]), meta: meta)
        await model.refresh()
        var edit = MetaEdit(field: .title, original: "T a")
        edit.draft = "Plan the allotment"
        #expect(await model.save(edit, sessionID: "a") == .saved("Saved"))
        #expect(meta.editList == [.init(id: "a", change: MetaChange(title: "Plan the allotment"))])
        #expect(model.listedSession("a")?.title == "Plan the allotment")
        #expect(model.listedSession("b")?.title == "T b")

        // Empty restores the automatic value: sent as "" and the answer is what shows.
        var clear = MetaEdit(field: .description, original: "Mine")
        clear.draft = "   "
        #expect(await model.save(clear, sessionID: "b") == .saved("Saved"))
        #expect(meta.editList.last == .init(id: "b", change: MetaChange(description: "")))
    }

    @Test func clearingAValueTheOwnerDidNotWriteSaysNothingToClear() async {
        let meta = MetaStub()
        meta.keepTitle = true  // the model's title stays: an empty value clears only the owner's
        let model = makeModel(SessionList(sessions: [row("a")]), meta: meta)
        await model.refresh()
        var clear = MetaEdit(field: .title, original: "T a")
        clear.draft = ""
        #expect(await model.save(clear, sessionID: "a")
            == .saved("Nothing to clear: this title was not written by you."))
    }

    @Test func saveRefusalComesBackAsALineAndChangesNothing() async {
        let meta = MetaStub()
        meta.editFailure = .server(status: 400, code: .badRequest, message: "title: at most 80 characters")
        let model = makeModel(SessionList(sessions: [row("a")]), meta: meta)
        await model.refresh()
        var edit = MetaEdit(field: .title, original: "T a")
        edit.draft = "New"
        #expect(await model.save(edit, sessionID: "a") == .failed("Could not save: title: at most 80 characters"))
        #expect(model.listedSession("a")?.title == "T a")
    }

    @Test func describeRunsOncePerSessionAndKeepsItsResultToItsSession() async {
        let meta = MetaStub()
        let latchA = Latch()
        let latchB = Latch()
        meta.latches = ["a": latchA, "b": latchB]
        meta.describeResults = [
            "a": .success(DescribeResult(title: "Plan the allotment", description: "Lays out the beds.",
                                         changed: ["title", "description"], kept: [])),
            "b": .failure(.server(status: 409, code: .ownerSet, message: "x"))
        ]
        let model = makeModel(SessionList(sessions: [row("a"), row("b")]), meta: meta)
        await model.refresh()

        let first = Task { await model.describe("a") }
        await spin { meta.describeList.count == 1 }
        #expect(model.describing == ["a"])
        // A second tap, or the sheet reopened, while it runs: nothing is sent.
        await model.describe("a")
        #expect(meta.describeList == ["a"])

        let other = Task { await model.describe("b") }
        await spin { meta.describeList.count == 2 }
        #expect(model.describing == ["a", "b"])

        await latchB.release()
        await other.value
        #expect(model.describeNotes["b"]
                == "You wrote both the title and the description, so there is nothing to update.")
        #expect(model.describeNotes["a"] == nil, "b's answer never lands on a")
        #expect(model.describing == ["a"])

        await latchA.release()
        await first.value
        #expect(model.describeNotes["a"] == "Updated the title and description.")
        #expect(model.listedSession("a")?.title == "Plan the allotment")
        #expect(model.listedSession("a")?.description == "Lays out the beds.")
        #expect(model.listedSession("b")?.title == "T b")
        #expect(model.describing.isEmpty)

        model.clearDescribeNote("a")
        #expect(model.describeNotes["a"] == nil)
        #expect(model.describeNotes["b"] != nil)
    }

    @Test func healthGivesTheCapabilityAndTheNamingCount() async {
        let meta = MetaStub()
        let model = makeModel(SessionList(sessions: []), meta: meta)
        #expect(!model.canDescribe, "unknown until read")
        await model.readHealth()
        #expect(model.canDescribe)
        #expect(model.health?.naming?.today == 2)
    }

    @Test func withoutTheMetaSourceNothingIsOffered() async {
        let list = SessionListModel(source: ListStub(SessionList(sessions: [row("a")])), cache: nil)
        let model = NowModel(list: list, source: NoActivity(), now: { now })
        await model.readHealth()
        #expect(!model.canDescribe)
        await model.setPinned(row("a"), true)
        #expect(model.view.pinned.isEmpty)
    }
}
