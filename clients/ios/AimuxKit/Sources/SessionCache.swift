import Foundation

public struct CachedSessions: Equatable, Sendable {
    public let sessions: [Session]
    public let savedAt: Date
}

/// The last session list, so the app shows something at once and offline.
public struct SessionCache: Sendable {
    public static let fileName = "sessions.json"
    private let file: URL

    public init(directory: URL = .cachesDirectory) {
        file = directory.appending(path: Self.fileName)
    }

    private struct Stored: Codable {
        let savedAt: Date
        let sessions: [Session]
    }

    public func save(_ sessions: [Session], at savedAt: Date = .now) throws {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .secondsSince1970
        try encoder.encode(Stored(savedAt: savedAt, sessions: sessions)).write(to: file, options: .atomic)
    }

    public func load() -> CachedSessions? {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .secondsSince1970
        guard let data = try? Data(contentsOf: file),
              let stored = try? decoder.decode(Stored.self, from: data) else { return nil }
        return CachedSessions(sessions: stored.sessions, savedAt: stored.savedAt)
    }
}
