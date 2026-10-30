import Foundation

public enum AllSort: String, CaseIterable, Sendable {
    case newest, mostActive, project
}

public struct DaySection: Equatable, Sendable {
    /// "today", "yesterday" or YYYY-MM-DD; the project path for the `project` sort.
    public let day: String
    public let sessions: [Session]
}

/// The All screen's sections, by the shared rules (api/testdata/clients/README.md).
public func allSections(_ sessions: [Session], project: String?, sort: AllSort, now: Date,
                        timeZone: TimeZone) -> [DaySection] {
    func lastActive(_ session: Session) -> Date { min(session.lastActive, now) }
    // Equal times fall back to the session id, ascending, so the order is deterministic.
    func newestFirst(_ lhs: Session, _ rhs: Session) -> Bool {
        lastActive(lhs) == lastActive(rhs) ? lhs.id < rhs.id : lastActive(lhs) > lastActive(rhs)
    }
    func activityRank(_ session: Session) -> Int {
        switch session.state {
        case .busy: 0
        case .openInTerminal: 1
        default: 2
        }
    }

    let shown = sessions.filter { project == nil || $0.project == project }
    if sort == .project { return projectSections(shown, lastActive: lastActive, newestFirst: newestFirst) }

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

    let order: (Session, Session) -> Bool = sort == .mostActive
        ? { activityRank($0) != activityRank($1) ? activityRank($0) < activityRank($1) : newestFirst($0, $1) }
        : newestFirst
    var byDay: [String: [Session]] = [:]
    for session in shown { byDay[dayKey(lastActive(session)), default: []].append(session) }
    return byDay.keys.sorted(by: >).map { key in
        let rows = byDay[key, default: []].sorted(by: order)
        return DaySection(day: label(rows[0]), sessions: rows)
    }
}

private struct ProjectRows {
    let path: String
    /// Newest first.
    let rows: [Session]
}

private func projectSections(_ sessions: [Session], lastActive: (Session) -> Date,
                             newestFirst: (Session, Session) -> Bool) -> [DaySection] {
    var byProject: [String: [Session]] = [:]
    for session in sessions { byProject[session.project, default: []].append(session) }
    return byProject.map { ProjectRows(path: $0.key, rows: $0.value.sorted(by: newestFirst)) }.sorted {
        let (lhs, rhs) = (lastActive($0.rows[0]), lastActive($1.rows[0]))
        return lhs == rhs ? $0.path < $1.path : lhs > rhs
    }.map { DaySection(day: $0.path, sessions: $0.rows) }
}
