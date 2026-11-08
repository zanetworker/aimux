import Foundation

public struct DaySection: Equatable, Sendable {
    /// "today", "yesterday" or YYYY-MM-DD.
    public let day: String
    public let sessions: [Session]
}

/// The All screen's Recent sections, newest first by day, by the shared rules (api/testdata/clients/README.md).
public func allSections(_ sessions: [Session], project: String?, now: Date, timeZone: TimeZone) -> [DaySection] {
    func lastActive(_ session: Session) -> Date { min(session.lastActive, now) }
    // Equal times fall back to the session id, ascending, so the order is deterministic.
    func newestFirst(_ lhs: Session, _ rhs: Session) -> Bool {
        lastActive(lhs) == lastActive(rhs) ? lhs.id < rhs.id : lastActive(lhs) > lastActive(rhs)
    }

    let shown = sessions.filter { project == nil || $0.project == project }
    var calendar = Calendar(identifier: .gregorian)
    calendar.timeZone = timeZone
    func dayKey(_ date: Date) -> String {
        let parts = calendar.dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", parts.year ?? 0, parts.month ?? 0, parts.day ?? 0)
    }
    let today = dayKey(now)
    let yesterday = calendar.date(byAdding: .day, value: -1, to: now).map(dayKey)
    func label(_ session: Session) -> String {
        let key = dayKey(lastActive(session))
        if key == today { return "today" }
        return key == yesterday ? "yesterday" : key
    }

    var byDay: [String: [Session]] = [:]
    for session in shown { byDay[dayKey(lastActive(session)), default: []].append(session) }
    return byDay.keys.sorted(by: >).map { key in
        let rows = byDay[key, default: []].sorted(by: newestFirst)
        return DaySection(day: label(rows[0]), sessions: rows)
    }
}
