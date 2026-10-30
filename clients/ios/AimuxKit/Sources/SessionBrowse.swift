import Foundation

/// Narrows the loaded list: every word of the query must appear in the title, folder or last prompt.
public struct SessionFilter: Equatable, Sendable {
    public var query: String
    /// A session's full `project` path, or nil for all projects.
    public var project: String?

    public init(query: String = "", project: String? = nil) {
        self.query = query
        self.project = project
    }

    public var words: [String] {
        query.lowercased().split(whereSeparator: \.isWhitespace).map(String.init)
    }

    public func apply(_ sessions: [Session]) -> [Session] {
        let words = words
        return sessions.filter { session in
            if let project, session.project != project { return false }
            if words.isEmpty { return true }
            let haystack = [session.title, session.project, session.lastPrompt ?? ""]
                .joined(separator: "\n").lowercased()
            return words.allSatisfy { haystack.contains($0) }
        }
    }
}

public struct ProjectSummary: Equatable, Sendable, Identifiable {
    public let path: String
    public let name: String
    public let count: Int
    public var id: String { path }

    /// One entry per folder, most sessions first; ties keep the order of the newest session.
    public static func list(_ sessions: [Session]) -> [ProjectSummary] {
        var order: [String] = []
        var counts: [String: Int] = [:]
        for session in sessions {
            if counts[session.project] == nil { order.append(session.project) }
            counts[session.project, default: 0] += 1
        }
        let format = SessionRowFormat()
        return order.enumerated()
            .sorted { lhs, rhs in
                let (left, right) = (counts[lhs.element]!, counts[rhs.element]!)
                return left != right ? left > right : lhs.offset < rhs.offset
            }
            .map { ProjectSummary(path: $0.element, name: format.projectName($0.element), count: counts[$0.element]!) }
    }
}

/// A search snippet, with the server's [bracketed] matches split out for highlighting.
public enum Snippet {
    public struct Segment: Equatable, Sendable {
        public let text: String
        public let isMatch: Bool

        public init(text: String, isMatch: Bool) {
            self.text = text
            self.isMatch = isMatch
        }
    }

    public static func parse(_ snippet: String) -> [Segment] {
        var segments: [Segment] = []
        var rest = Substring(snippet)
        while let open = rest.firstIndex(of: "["),
              let close = rest[open...].firstIndex(of: "]") {
            if open > rest.startIndex { segments.append(Segment(text: String(rest[..<open]), isMatch: false)) }
            segments.append(Segment(text: String(rest[rest.index(after: open)..<close]), isMatch: true))
            rest = rest[rest.index(after: close)...]
        }
        if !rest.isEmpty { segments.append(Segment(text: String(rest), isMatch: false)) }
        return segments
    }
}
