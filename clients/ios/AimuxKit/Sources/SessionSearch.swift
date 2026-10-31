import Foundation

/// A session is open when it is busy, open in a terminal, in an iTerm2 pane (`mac`, whatever its `state`) or its
/// activity is working (api/testdata/clients/README.md).
func isOpen(_ session: Session, activity: Activity) -> Bool {
    if session.state == .busy || session.state == .openInTerminal || session.mac != nil { return true }
    if case .working = activity { return true }
    return false
}

/// The query's words: lower-cased and split on whitespace; none means no query.
func queryWords(_ query: String) -> [String] {
    query.lowercased().split(whereSeparator: \.isWhitespace).map(String.init)
}

/// Every word is a substring of the lower-cased title, description, folder label, tab name, window name or
/// last prompt; no words matches everything.
public func matches(_ session: Session, words: [String]) -> Bool {
    let fields = [session.title, session.description, session.folder?.label, session.mac?.tab.name,
                  session.mac?.window.name, session.lastPrompt].map { ($0 ?? "").lowercased() }
    return words.allSatisfy { word in fields.contains { $0.contains(word) } }
}

/// Plain code point order, not locale collation.
func codePointsBefore(_ lhs: String, _ rhs: String) -> Bool {
    lhs.unicodeScalars.lexicographicallyPrecedes(rhs.unicodeScalars)
}

/// Mac order: window index, window id, tab index, pane, then id.
func inMacOrder(_ lhs: Session, _ rhs: Session) -> Bool {
    guard let left = lhs.mac, let right = rhs.mac else { return codePointsBefore(lhs.id, rhs.id) }
    if left.window.index != right.window.index { return left.window.index < right.window.index }
    if left.window.id != right.window.id { return codePointsBefore(left.window.id, right.window.id) }
    if left.tab.index != right.tab.index { return left.tab.index < right.tab.index }
    if left.pane != right.pane { return left.pane < right.pane }
    return codePointsBefore(lhs.id, rhs.id)
}

/// The one search of Now and All, on the device: the matching open sessions (in Mac order, then those with no pane
/// newest first), then every other match newest first. A query with no words finds nothing.
public func searchSessions(_ sessions: [Session], query: String, activity: [String: Activity] = [:],
                           now: Date = .distantFuture) -> (open: [Session], other: [Session]) {
    let words = queryWords(query)
    guard !words.isEmpty else { return ([], []) }
    func newestFirst(_ lhs: Session, _ rhs: Session) -> Bool {
        let (left, right) = (min(lhs.lastActive, now), min(rhs.lastActive, now))
        return left == right ? codePointsBefore(lhs.id, rhs.id) : left > right
    }
    let found = sessions.filter { matches($0, words: words) }
    let open = found.filter { isOpen($0, activity: activity[$0.id] ?? .none) }
    let placed = open.filter { $0.mac != nil }.sorted(by: inMacOrder)
    let unplaced = open.filter { $0.mac == nil }.sorted(by: newestFirst)
    let openIDs = Set(open.map(\.id))
    return (placed + unplaced, found.filter { !openIDs.contains($0.id) }.sorted(by: newestFirst))
}
