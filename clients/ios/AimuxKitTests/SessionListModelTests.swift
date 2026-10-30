import Foundation
import Testing
@testable import AimuxKit

/// Answers each call with the next queued result.
private final class StubSource: SessionSource, @unchecked Sendable {
    private var results: [Result<[Session], APIError>]
    private(set) var calls: [Int] = []
    private let lock = NSLock()

    init(_ results: [Result<[Session], APIError>]) { self.results = results }

    func sessions(limit: Int) async throws -> [Session] {
        let next = lock.withLock {
            calls.append(limit)
            return results.removeFirst()
        }
        return try next.get()
    }
}

private func row(_ id: String, _ state: SessionState = .idle) -> Session {
    Session(id: id, provider: "claude", title: "Title \(id)", project: "/p/\(id)",
            lastActive: Date(timeIntervalSince1970: 1000), state: state, lastPrompt: nil)
}

private func tempCache() -> SessionCache {
    let dir = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
    try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    return SessionCache(directory: dir)
}

@MainActor @Suite struct SessionListModelTests {
    private let fixedNow = Date(timeIntervalSince1970: 9000)

    @Test func freshListReplacesCacheAndIsSaved() async throws {
        let cache = tempCache()
        try cache.save([row("old")], at: Date(timeIntervalSince1970: 10))
        let source = StubSource([.success([row("a"), row("b", .busy)])])
        let model = SessionListModel(source: source, cache: cache, limit: 25, now: { [fixedNow] in fixedNow })

        await model.start()

        #expect(model.sessions.map(\.id) == ["a", "b"])
        #expect(model.cachedAt == nil)
        #expect(model.error == nil)
        #expect(source.calls == [25])
        #expect(cache.load() == CachedSessions(sessions: [row("a"), row("b", .busy)], savedAt: fixedNow))
    }

    @Test func showCachedShowsTheSavedListWithoutAsking() throws {
        let cache = tempCache()
        let savedAt = Date(timeIntervalSince1970: 10)
        try cache.save([row("old")], at: savedAt)
        let source = StubSource([])
        let model = SessionListModel(source: source, cache: cache)

        model.showCached()

        #expect(model.sessions.map(\.id) == ["old"])
        #expect(model.cachedAt == savedAt)
        #expect(source.calls.isEmpty)
    }

    @Test func showCachedNeverReplacesLiveRows() async throws {
        let cache = tempCache()
        let model = SessionListModel(source: StubSource([.success([row("live")])]), cache: cache)
        await model.refresh()
        try cache.save([row("old")], at: Date(timeIntervalSince1970: 10))

        model.showCached()

        #expect(model.sessions.map(\.id) == ["live"])
        #expect(model.cachedAt == nil)
    }

    @Test func failureKeepsCachedRowsAndReportsError() async throws {
        let cache = tempCache()
        let savedAt = Date(timeIntervalSince1970: 10)
        try cache.save([row("old")], at: savedAt)
        let model = SessionListModel(source: StubSource([.failure(.http(status: 502))]), cache: cache)

        await model.start()

        #expect(model.sessions.map(\.id) == ["old"])
        #expect(model.cachedAt == savedAt)
        #expect(model.error == .http(status: 502))
        #expect(!model.isLoading)
    }

    @Test func failureWithoutCacheShowsOnlyTheError() async {
        let model = SessionListModel(source: StubSource([.failure(.transport(.cannotFindHost))]), cache: tempCache())
        await model.start()
        #expect(model.sessions.isEmpty)
        #expect(model.error == .transport(.cannotFindHost))
    }

    @Test func successfulRefreshClearsEarlierError() async {
        let source = StubSource([.failure(.http(status: 502)), .success([row("a")])])
        let model = SessionListModel(source: source, cache: nil)
        await model.start()
        #expect(model.error != nil)
        await model.refresh()
        #expect(model.error == nil)
        #expect(model.sessions.map(\.id) == ["a"])
    }

    @Test func cancelledRequestIsNotAnError() async {
        // Pull-to-refresh cancels the request when the view goes away; that is not a failure to show.
        let model = SessionListModel(source: StubSource([.failure(.transport(.cancelled))]), cache: nil)
        await model.start()
        #expect(model.error == nil)
    }

    @Test func showMoreAsksForAHundredMoreUpToFiveHundred() async {
        let full = (0..<50).map { row("r\($0)") }
        let source = StubSource([.success(full), .success(full + [row("x")]), .success(full)])
        let model = SessionListModel(source: source, cache: nil, limit: 50)
        await model.start()
        #expect(model.canShowMore)          // a full page means there may be more
        await model.showMore()
        #expect(source.calls == [50, 150])
        #expect(model.sessions.count == 51)
        #expect(!model.canShowMore)         // fewer than asked for: that was everything
    }

    @Test func showMoreStopsAtTheServerMaximum() async {
        let page = (0..<500).map { row("r\($0)") }
        let model = SessionListModel(source: StubSource([.success(page)]), cache: nil, limit: 500)
        await model.start()
        #expect(!model.canShowMore)
    }
}
