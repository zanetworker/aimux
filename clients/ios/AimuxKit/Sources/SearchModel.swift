import Foundation
import Observation

public protocol SearchSource: Sendable {
    func health() async throws -> Health
    func search(_ query: String, limit: Int) async throws -> [SearchHit]
}

extension APIClient: SearchSource {}

/// Server search over session content. The view debounces typing; this keeps only
/// the answer to the latest query and checks the `search` capability once.
@MainActor @Observable
public final class SearchModel {
    public private(set) var hits: [SearchHit] = []
    public private(set) var error: APIError?
    public private(set) var isSearching = false
    /// False once the server said it has no search; nil until asked.
    public private(set) var supported: Bool?
    public var isAvailable: Bool { supported ?? true }

    private let source: SearchSource
    private var latest = ""

    public init(source: SearchSource) {
        self.source = source
    }

    public func run(_ text: String) async {
        let query = text.trimmingCharacters(in: .whitespacesAndNewlines)
        latest = query
        guard !query.isEmpty else {
            hits = []
            error = nil
            isSearching = false
            return
        }
        if supported == nil {
            supported = (try? await source.health())?.supports(.search)
        }
        guard supported != false, latest == query else { return }
        isSearching = true
        defer { if latest == query { isSearching = false } }
        do {
            let found = try await source.search(query, limit: 30)
            guard latest == query else { return }
            hits = found
            error = nil
        } catch APIError.transport(.cancelled) {
            return
        } catch {
            guard latest == query else { return }
            hits = []
            self.error = error as? APIError ?? .transport(.unknown)
        }
    }
}

/// Search on this phone: sessions whose title, description, folder and last prompt hold every word of the query;
/// those matched by title and description alone first, the list order kept inside each part.
public func localMatches(_ sessions: [Session], query: String) -> [Session] {
    let words = SessionFilter(query: query).words
    guard !words.isEmpty else { return [] }
    func matches(_ parts: [String]) -> Bool {
        let haystack = parts.joined(separator: "\n").lowercased()
        return words.allSatisfy { haystack.contains($0) }
    }
    var named: [Session] = []
    var rest: [Session] = []
    for session in sessions {
        let own = [session.title, session.description ?? ""]
        if matches(own) {
            named.append(session)
        } else if matches(own + [session.project, session.lastPrompt ?? ""]) {
            rest.append(session)
        }
    }
    return named + rest
}
