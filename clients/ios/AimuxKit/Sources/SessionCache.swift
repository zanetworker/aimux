import Foundation

public struct CachedSessions: Equatable, Sendable {
    public let sessions: [Session]
    public let savedAt: Date
}

/// The last session list, so the app shows something at once and offline.
/// It belongs to one Mac: after the address changes, the other Mac's list
/// is never shown.
public struct SessionCache: Sendable {
    public static let fileName = "sessions.json"
    private let file: URL
    private let server: String

    public init(directory: URL = .cachesDirectory, server: URL? = nil) {
        file = directory.appending(path: Self.fileName)
        self.server = server.map(Self.identity) ?? ""
    }

    /// identity: scheme, host and port, the way they name one Mac.
    private static func identity(_ url: URL) -> String {
        let host = (url.host() ?? "").lowercased()
        let port = url.port.map { ":\($0)" } ?? ""
        return (url.scheme ?? "").lowercased() + "://" + host + port
    }

    private struct Stored: Codable {
        let savedAt: Date
        let sessions: [Session]
        let server: String?
    }

    public func save(_ sessions: [Session], at savedAt: Date = .now) throws {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .secondsSince1970
        let data = try encoder.encode(Stored(savedAt: savedAt, sessions: sessions, server: server))
        try data.write(to: file, options: .atomic)
    }

    public func load() -> CachedSessions? {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .secondsSince1970
        guard let data = try? Data(contentsOf: file),
              let stored = try? decoder.decode(Stored.self, from: data),
              (stored.server ?? "") == server else { return nil }
        return CachedSessions(sessions: stored.sessions, savedAt: stored.savedAt)
    }
}
