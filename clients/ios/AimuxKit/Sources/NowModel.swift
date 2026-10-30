import Foundation
import Observation

public protocol ActivitySource: Sendable {
    func eventSnapshot(sessionID: String) async throws -> EventPage
    func answer(approvalID: String, optionID: String) async throws
    func sendMessage(sessionID: String, text: String, key: String) async throws -> TurnAccepted
    func session(id: String, turns: Int) async throws -> SessionDetail
}

extension APIClient: ActivitySource {}

/// The Now screen's state: the ranked view of the session list plus what each candidate is doing.
/// The view polls `refresh()`; this model does not schedule anything.
@MainActor @Observable
public final class NowModel {
    public private(set) var view: NowView
    /// Approvals whose answer is on its way; a second answer for one of them is ignored.
    public private(set) var answering: Set<String> = []
    /// Why the last answer for an approval failed; cleared when it is tried again.
    public private(set) var answerErrors: [String: APIError] = [:]
    /// Failed sessions whose Retry is on its way.
    public private(set) var retrying: Set<String> = []
    /// Why the last Retry of a session failed; cleared when it is tried again.
    public private(set) var retryErrors: [String: APIError] = [:]
    /// The session list's own error, for the Now view to show.
    public var loadError: APIError? { list.error }
    /// The server's capabilities and naming status; nil until read.
    public private(set) var health: Health?
    /// Pins and owner edits are offered: the server has the `describe` capability.
    public var canDescribe: Bool { meta != nil && health?.supports(.describe) == true }
    /// Sessions whose Pin or Unpin is on its way.
    public private(set) var pinning: Set<String> = []
    /// Why the last Pin or Unpin of a session failed, as a fixed line; cleared when it is tried again.
    public private(set) var pinErrors: [String: String] = [:]
    /// Sessions with an Update summary on its way: one at a time per session.
    public private(set) var describing: Set<String> = []
    /// The last Update summary's result or error line, per session, until its sheet clears it.
    public private(set) var describeNotes: [String: String] = [:]

    private let list: SessionListModel
    private let source: ActivitySource
    private let meta: (any SessionMetaSource)?
    private let defaults: UserDefaults
    private let now: @Sendable () -> Date
    private var activity: [String: Activity] = [:]
    private var answered: Set<String> = []
    private var seen: [String: Date]
    /// Refreshes run one after another, so an older result can never land after a newer one.
    private var tail: Task<Void, Never>?

    private static let seenKey = "now.seen"
    private static let recentWindow: TimeInterval = 900
    private static let snapshotLimit = 10

    public init(list: SessionListModel, source: ActivitySource, meta: (any SessionMetaSource)? = nil,
                defaults: UserDefaults = .standard, now: @escaping @Sendable () -> Date = { .now }) {
        self.list = list
        self.source = source
        self.meta = meta
        self.defaults = defaults
        self.now = now
        let stored = defaults.dictionary(forKey: Self.seenKey) as? [String: Double] ?? [:]
        seen = stored.mapValues { Date(timeIntervalSince1970: $0) }
        view = rankNow(sessions: [], activity: [:], seen: [:], now: now())
    }

    public func refresh() async {
        let previous = tail
        let task = Task { [self] in
            await previous?.value
            await performRefresh()
        }
        tail = task
        await task.value
    }

    private func performRefresh() async {
        // The cached or last list at once, then the new list, then its activity: the screen never waits
        // for every event read before showing anything.
        rebuild()
        async let healthRead: Void = readHealthOnce()
        await list.refresh()
        rebuild()
        let sessions = list.sessions
        let current = now()
        // Every busy session, since any may wait for an approval; the limit is for recent idle ones.
        let newest = sessions.filter { isCandidate($0, now: current) }.sorted { $0.lastActive > $1.lastActive }
        let candidates = newest.filter { $0.state == .busy }
            + newest.filter { $0.state != .busy }.prefix(Self.snapshotLimit)
        let source = source
        var fresh: [String: Activity] = [:]
        await withTaskGroup(of: (String, Activity?).self) { group in
            for session in candidates {
                group.addTask {
                    do {
                        let page = try await source.eventSnapshot(sessionID: session.id)
                        return (session.id, deriveActivity(page.events))
                    } catch APIError.server, APIError.http {
                        return (session.id, Activity.none)  // refused: nothing to show
                    } catch {
                        return (session.id, nil)  // no answer: keep what we knew
                    }
                }
            }
            for await (id, derived) in group { fresh[id] = derived ?? activity[id] ?? Activity.none }
        }
        activity = fresh
        rebuild()
        await healthRead
    }

    private func readHealthOnce() async {
        if health == nil { await readHealth() }
    }

    public func answer(_ approval: Approval, option: PermissionOption, session: String) async {
        let id = approval.approvalId
        guard !answering.contains(id) else { return }
        answering.insert(id)
        answerErrors[id] = nil
        defer { answering.remove(id) }
        do {
            try await source.answer(approvalID: id, optionID: option.optionId)
        } catch APIError.server(_, .noSuchApproval, _) {
            // Already answered elsewhere: the card is stale, not failed.
        } catch let failure as APIError {
            answerErrors[id] = failure
            return
        } catch {
            answerErrors[id] = .transport(.unknown)
            return
        }
        answered.insert(id)
        rebuild()
    }

    /// Sends the session's last prompt again as a new message, whole and as typed from its
    /// history: the list's `lastPrompt` is a shortened preview. Each call is a new attempt
    /// by the person, so it gets a new idempotency key.
    public func retry(_ session: Session) async {
        let id = session.id
        guard let prompt = session.lastPrompt, !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
              !retrying.contains(id) else { return }
        retrying.insert(id)
        retryErrors[id] = nil
        defer { retrying.remove(id) }
        do {
            let detail = try await source.session(id: id, turns: 3)
            guard let text = Self.lastTyped(detail) else { return }
            _ = try await source.sendMessage(sessionID: id, text: text, key: UUID().uuidString)
        } catch {
            retryErrors[id] = error as? APIError ?? .transport(.unknown)
            return
        }
        await refresh()
    }

    /// The latest message the person sent in a session's history, as they typed it.
    static func lastTyped(_ detail: SessionDetail) -> String? {
        detail.turns.reversed().lazy
            .map { savedPromptAsTyped($0.user).trimmingCharacters(in: .whitespacesAndNewlines) }
            .first { !$0.isEmpty }
    }

    /// Reads the capabilities and today's naming again (they change during the day); a failure keeps what was read.
    public func readHealth() async {
        guard let meta, let fresh = try? await meta.health() else { return }
        health = fresh
    }

    /// The session as listed now (with any pin or edit taken since), or nil when it is not in the list.
    public func listedSession(_ id: String) -> Session? {
        list.sessions.first { $0.id == id }
    }

    /// Pin or Unpin; a refusal is kept in `pinErrors` for the row until the next try. Answers false when
    /// nothing was sent (one already runs for the session, or no server can pin), so there is nothing to announce.
    @discardableResult
    public func setPinned(_ session: Session, _ pinned: Bool) async -> Bool {
        let id = session.id
        guard let meta, !pinning.contains(id) else { return false }
        pinning.insert(id)
        pinErrors[id] = nil
        defer { pinning.remove(id) }
        do {
            take(id, try await meta.editMeta(sessionID: id, MetaChange(pinned: pinned)))
        } catch {
            pinErrors[id] = SessionMetaText.pinError(error, pinned: pinned)
        }
        return true
    }

    /// Saves an owner title or description; nothing is sent when it is unchanged. Answers the line to announce
    /// when it was saved, or why it failed.
    public func save(_ edit: MetaEdit, sessionID id: String) async -> MetaSaveOutcome {
        guard !edit.isUnchanged else { return .unchanged }
        guard let meta else { return .failed(SessionMetaText.saveError(APIError.transport(.unsupportedURL))) }
        do {
            let answer = try await meta.editMeta(sessionID: id, edit.change)
            take(id, answer)
            return .saved(SessionMetaText.saveNote(edit, answer: answer))
        } catch {
            return .failed(SessionMetaText.saveError(error))
        }
    }

    /// Update summary: one at a time per session, so a call while one runs sends nothing. The result or error
    /// line lands in `describeNotes` under this session only.
    public func describe(_ id: String) async {
        guard let meta, !describing.contains(id) else { return }
        describing.insert(id)
        describeNotes[id] = nil
        let note: String
        do {
            let result = try await meta.describe(sessionID: id)
            take(id, SessionMeta(title: result.title, description: result.description,
                                 pinned: listedSession(id)?.pinned ?? false))
            note = SessionMetaText.describeResult(changed: result.changed)
        } catch {
            note = SessionMetaText.describeError(error)
        }
        describing.remove(id)
        describeNotes[id] = note
        await readHealth()  // today's naming count moved on
    }

    /// The sheet of this session was opened or closed: its last result line is no longer news.
    public func clearDescribeNote(_ id: String) {
        describeNotes[id] = nil
    }

    private func take(_ id: String, _ answer: SessionMeta) {
        list.update(id) { $0.taking(title: answer.title, description: answer.description, pinned: answer.pinned) }
        rebuild()
    }

    public func markSeen(_ id: String) {
        seen[id] = now()
        defaults.set(seen.mapValues(\.timeIntervalSince1970), forKey: Self.seenKey)
        rebuild()
    }

    private func isCandidate(_ session: Session, now: Date) -> Bool {
        switch session.state {
        case .openInTerminal: false
        case .busy: true
        default: now.timeIntervalSince(session.lastActive) <= Self.recentWindow
        }
    }

    private func rebuild() {
        var shown = activity
        for (id, value) in activity {
            if case .needsApproval(let approval) = value, answered.contains(approval.approvalId) {
                shown[id] = Activity.none
            }
        }
        view = rankNow(sessions: list.sessions, activity: shown, seen: seen, now: now(), lastOnMac: list.lastOnMac)
    }
}
