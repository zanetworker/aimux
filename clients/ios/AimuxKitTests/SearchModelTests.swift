import Foundation
import Testing
@testable import AimuxKit

private final class StubSearch: SearchSource, @unchecked Sendable {
    var capabilities: [String]
    var results: [String: Result<[SearchHit], APIError>] = [:]
    private(set) var queries: [String] = []
    private(set) var healthCalls = 0
    /// Queries whose answer waits until `release()`; lets a test answer an old query late.
    var held: Set<String> = []
    private var waiters: [CheckedContinuation<Void, Never>] = []
    private let lock = NSLock()

    init(capabilities: [String]) { self.capabilities = capabilities }

    func health() async throws -> Health {
        lock.withLock { healthCalls += 1 }
        let json = #"{"status":"ok","login":"a","version":"v","acpProtocolVersion":1,"capabilities":[]}"#
        let base = try APIDecoder.decode(Health.self, from: Data(json.utf8))
        return Health(status: base.status, login: base.login, version: base.version,
                      acpProtocolVersion: 1, capabilities: capabilities)
    }

    func search(_ query: String, limit: Int) async throws -> [SearchHit] {
        let wait = lock.withLock { queries.append(query); return held.contains(query) }
        if wait { await withCheckedContinuation { cont in lock.withLock { waiters.append(cont) } } }
        return try (results[query] ?? .success([])).get()
    }

    func release() { lock.withLock { waiters.forEach { $0.resume() }; waiters = [] } }
    var waiting: Int { lock.withLock { waiters.count } }
}

private func hit(_ id: String) throws -> SearchHit {
    let json = #"{"id":"\#(id)","provider":"claude","title":"T","project":"/p","# +
        #""lastActive":"2026-10-25T18:00:00Z","state":"idle","snippet":"a [b]"}"#
    return try APIDecoder.decode(SearchHit.self, from: Data(json.utf8))
}

@MainActor @Suite struct SearchModelTests {
    @Test func runsSearchAndKeepsHits() async throws {
        let source = StubSearch(capabilities: ["sessions", "search"])
        source.results["gateway tls"] = .success([try hit("a"), try hit("b")])
        let model = SearchModel(source: source)
        await model.run("  gateway tls ")
        #expect(source.queries == ["gateway tls"])
        #expect(model.hits.map(\.id) == ["a", "b"])
        #expect(model.isAvailable)
        #expect(model.error == nil)
    }

    @Test func emptyQueryClearsWithoutCallingServer() async throws {
        let source = StubSearch(capabilities: ["search"])
        source.results["x"] = .success([try hit("a")])
        let model = SearchModel(source: source)
        await model.run("x")
        await model.run("   ")
        #expect(model.hits.isEmpty)
        #expect(source.queries == ["x"])
    }

    @Test func serverWithoutSearchCapabilityIsNeverSearched() async {
        let source = StubSearch(capabilities: ["sessions"])
        let model = SearchModel(source: source)
        await model.run("anything")
        await model.run("again")
        #expect(!model.isAvailable)
        #expect(source.queries.isEmpty)
        #expect(source.healthCalls == 1)   // asked once, remembered
    }

    @Test func failureIsReportedAndClearsHits() async throws {
        let source = StubSearch(capabilities: ["search"])
        source.results["ok"] = .success([try hit("a")])
        source.results["bad"] = .failure(.http(status: 502))
        let model = SearchModel(source: source)
        await model.run("ok")
        await model.run("bad")
        #expect(model.hits.isEmpty)
        #expect(model.error == .http(status: 502))
    }

    @Test func lateAnswerToAnOldQueryIsDropped() async throws {
        let source = StubSearch(capabilities: ["search"])
        source.held = ["old"]
        source.results["old"] = .success([try hit("stale")])
        source.results["new"] = .success([try hit("fresh")])
        let model = SearchModel(source: source)
        let slow = Task { await model.run("old") }
        while source.waiting == 0 { await Task.yield() }
        await model.run("new")
        source.release()
        await slow.value
        #expect(model.hits.map(\.id) == ["fresh"])
    }
}
