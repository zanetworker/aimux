import Foundation
import Observation

public protocol SessionSource: Sendable {
    func sessions(limit: Int) async throws -> [Session]
    /// The sessions and the one last in front on the Mac.
    func sessionList(limit: Int) async throws -> SessionList
}

extension SessionSource {
    /// A source that knows nothing of the Mac's front session.
    public func sessionList(limit: Int) async throws -> SessionList {
        SessionList(sessions: try await sessions(limit: limit))
    }
}

extension APIClient: SessionSource {}

/// The session list screen's state: cached rows at once, then the live list.
@MainActor @Observable
public final class SessionListModel {
    public private(set) var sessions: [Session] = []
    /// The session last in front on the Mac, from the latest live list; never cached.
    public private(set) var lastOnMac: LastOnMac?
    /// When the rows on screen came from the cache, when they were saved; nil once live.
    public private(set) var cachedAt: Date?
    public private(set) var error: APIError?
    public private(set) var isLoading = false

    private let source: SessionSource
    private let cache: SessionCache?
    private var limit: Int
    /// The server's largest `limit`; it has no paging.
    public static let maxLimit = 500
    public private(set) var canShowMore = false
    private let now: @Sendable () -> Date

    public init(source: SessionSource, cache: SessionCache?, limit: Int = 50,
                now: @escaping @Sendable () -> Date = { .now }) {
        self.source = source
        self.cache = cache
        self.limit = limit
        self.now = now
    }

    public func start() async {
        showCached()
        await refresh()
    }

    /// Shows the saved list while nothing live has loaded yet; never asks the server.
    public func showCached() {
        guard sessions.isEmpty, let cached = cache?.load() else { return }
        sessions = cached.sessions
        cachedAt = cached.savedAt
    }

    /// Asks for 100 more sessions; the server re-sends the newest ones too.
    public func showMore() async {
        guard canShowMore else { return }
        limit = min(limit + 100, Self.maxLimit)
        await refresh()
    }

    public func refresh() async {
        guard !isLoading else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            let list = try await source.sessionList(limit: limit)
            let rows = list.sessions
            sessions = rows
            lastOnMac = list.lastOnMac
            canShowMore = rows.count >= limit && limit < Self.maxLimit
            cachedAt = nil
            error = nil
            try? cache?.save(rows, at: now())
        } catch APIError.transport(.cancelled) {
            // The view went away mid-request; nothing to report.
        } catch let failure as APIError {
            error = failure
        } catch {
            self.error = .transport(.unknown)
        }
    }

    /// Replaces one listed session (a pin, an owner edit, Update summary), until the next refresh.
    public func update(_ id: String, _ change: (Session) -> Session) {
        guard let index = sessions.firstIndex(where: { $0.id == id }) else { return }
        sessions[index] = change(sessions[index])
    }
}
