import Foundation

public struct NowItem: Equatable, Sendable {
    public let session: Session
    public let activity: Activity

    public init(session: Session, activity: Activity) {
        self.session = session
        self.activity = activity
    }
}

/// A session placed in the Last on your Mac row or in Pinned: it keeps its own status (the dot).
public struct PlacedItem: Equatable, Sendable {
    public let session: Session
    public let activity: Activity
    /// The Working rules select it: a green dot and its live step.
    public let isWorking: Bool
}

/// A folder row on Now: a working project's sessions. It starts expanded; collapsing is the view's state.
public struct ProjectGroup: Equatable, Sendable {
    public let project: String
    /// The folder's last path component.
    public let name: String
    public let count: Int
    public let newest: NowItem
    /// The project's working sessions, newest first, for expanding the group in place.
    public let items: [NowItem]

    public init(project: String, name: String, count: Int, newest: NowItem, items: [NowItem]) {
        self.project = project
        self.name = name
        self.count = count
        self.newest = newest
        self.items = items
    }
}

public struct MoreProjects: Equatable, Sendable {
    public let projects: Int
    public let sessions: Int
}

public enum WorkingSection: Equatable, Sendable {
    case list([NowItem])
    case groups([ProjectGroup], more: MoreProjects?)
}

public struct NowSummary: Equatable, Sendable {
    public let needYou: Int
    public let working: Int
    public let onMac: Int
}

public struct NowView: Equatable, Sendable {
    /// The session last in front on the Mac, unless it needs you; nil when unknown or older than 12 hours.
    public let lastOnMac: PlacedItem?
    /// When it was seen there (clamped to now); nil when the row is hidden.
    public let lastOnMacAt: Date?
    /// Pinned sessions other than the Last on your Mac one and those in Needs you, newest first.
    public let pinned: [PlacedItem]
    public let needsYou: [NowItem]
    public let finished: [NowItem]
    public let working: WorkingSection
    public let onMac: [Session]
    /// `onMac` grouped by project, the folder with the newest session first.
    public var onMacFolders: [ProjectGroup] = []
    public let onMacTotal: Int
    public let summary: NowSummary
}

/// The windows and caps of the shared ranking rules (api/testdata/clients/README.md).
private enum Rules {
    static let recent: TimeInterval = 900
    static let flatWorkingLimit = 5
    static let onMacLimit = 8
    static let onMacCollapseAbove = 5
    static let lastOnMacWindow: TimeInterval = 12 * 3600
}

private func newestEvent(_ lhs: (item: NowItem, time: Date), _ rhs: (item: NowItem, time: Date)) -> Bool {
    lhs.time == rhs.time ? lhs.item.session.id < rhs.item.session.id : lhs.time > rhs.time
}

/// Ranks Now by the shared rules (api/testdata/clients/README.md): Last on your Mac and Pinned take their
/// sessions first, so the sections below never repeat them; Needs you is never moved.
public func rankNow(sessions: [Session], activity: [String: Activity], seen: [String: Date], now: Date,
                    lastOnMac: LastOnMac? = nil) -> NowView {
    let ranking = Ranking(sessions: sessions, activity: activity, now: now)
    let found = ranking.sort(seen: seen)
    let needsYou = found.approvals + found.failures.map(\.item)
    // Counted before Last on your Mac and Pinned take their sessions: the header is an overview of state.
    let summary = NowSummary(needYou: needsYou.count, working: found.working.count, onMac: found.terminals.count)

    let placed = ranking.place(lastOnMac: lastOnMac, working: Set(found.working.map(\.session.id)),
                               urgent: Set(needsYou.map(\.session.id)))
    let taken = placed.taken
    let finished = found.finishes.filter { !taken.contains($0.item.session.id) }.sorted(by: newestEvent).map(\.item)
    let working = found.working.filter { !taken.contains($0.session.id) }
        .sorted { ranking.newestFirst($0.session, $1.session) }
    let onMac = found.terminals.filter { !taken.contains($0.id) }.sorted(by: ranking.newestFirst)
    let showOnMac = needsYou.count + finished.count + working.count <= Rules.onMacCollapseAbove
    let shownOnMac = showOnMac ? Array(onMac.prefix(Rules.onMacLimit)) : []

    return NowView(
        lastOnMac: placed.last,
        lastOnMacAt: placed.lastAt,
        pinned: placed.pinned,
        needsYou: needsYou,
        finished: finished,
        working: workingSection(working, lastActive: ranking.lastActive),
        onMac: shownOnMac,
        onMacFolders: byProject(shownOnMac.map { NowItem(session: $0, activity: activity[$0.id] ?? .none) }),
        onMacTotal: onMac.count,
        summary: summary
    )
}

/// Every session in the first section whose rule it matches (Needs you, Finished, Working, On your Mac).
private struct Found {
    var approvals: [NowItem] = []
    var failures: [(item: NowItem, time: Date)] = []
    var finishes: [(item: NowItem, time: Date)] = []
    var working: [NowItem] = []
    var terminals: [Session] = []
}

private struct Placement {
    let last: PlacedItem?
    let lastAt: Date?
    let pinned: [PlacedItem]
    var taken: Set<String> { Set(([last].compactMap { $0 } + pinned).map(\.session.id)) }
}

private struct Ranking {
    let sessions: [Session]
    let activity: [String: Activity]
    let now: Date

    func lastActive(_ session: Session) -> Date { min(session.lastActive, now) }
    func isRecent(_ date: Date) -> Bool { now.timeIntervalSince(date) <= Rules.recent }
    /// Equal times fall back to the session id, ascending, so the order is deterministic.
    func newestFirst(_ lhs: Session, _ rhs: Session) -> Bool {
        lastActive(lhs) == lastActive(rhs) ? lhs.id < rhs.id : lastActive(lhs) > lastActive(rhs)
    }

    func sort(seen: [String: Date]) -> Found {
        var found = Found()
        for session in sessions {
            let current = activity[session.id] ?? .none
            let item = NowItem(session: session, activity: current)
            switch current {
            case .needsApproval:
                found.approvals.append(item)
                continue
            case .failed(_, let time) where isRecent(time):
                found.failures.append((item, time))
                continue
            case .finished(let time) where isRecent(time) && time > (seen[session.id] ?? .distantPast):
                found.finishes.append((item, time))
                continue
            default:
                break
            }

            var isWorking = session.state == .busy
            if case .working = current { isWorking = true }
            if session.state == .openInTerminal {
                if isRecent(lastActive(session)) {
                    isWorking = true
                } else if !isWorking {
                    found.terminals.append(session)
                }
            }
            if isWorking { found.working.append(item) }
        }
        found.approvals.sort { newestFirst($0.session, $1.session) }
        found.failures.sort(by: newestEvent)
        return found
    }

    /// Last on your Mac (shown when known, listed and at most 12 hours old) and Pinned take their sessions first;
    /// a session in Needs you (`urgent`) is never moved.
    func place(lastOnMac: LastOnMac?, working: Set<String>, urgent: Set<String>) -> Placement {
        func placed(_ session: Session) -> PlacedItem {
            PlacedItem(session: session, activity: activity[session.id] ?? .none,
                       isWorking: working.contains(session.id))
        }
        var last: PlacedItem?
        var lastAt: Date?
        if let lastOnMac, !urgent.contains(lastOnMac.id),
           let session = sessions.first(where: { $0.id == lastOnMac.id }),
           now.timeIntervalSince(min(lastOnMac.at, now)) <= Rules.lastOnMacWindow {
            last = placed(session)
            lastAt = min(lastOnMac.at, now)
        }
        let pinned = sessions
            .filter { $0.pinned && $0.id != last?.session.id && !urgent.contains($0.id) }
            .sorted(by: newestFirst)
            .map(placed)
        return Placement(last: last, lastAt: lastAt, pinned: pinned)
    }
}

/// Groups items by project path into folder rows, in the order first met. Items come newest first, so each
/// folder's first item is its newest and folders follow their newest session.
private func byProject(_ items: [NowItem]) -> [ProjectGroup] {
    var order: [String] = []
    var grouped: [String: [NowItem]] = [:]
    for item in items {
        if grouped[item.session.project] == nil { order.append(item.session.project) }
        grouped[item.session.project, default: []].append(item)
    }
    return order.map { project -> ProjectGroup in
        let items = grouped[project] ?? []
        let name = project.split(separator: "/").last.map(String.init) ?? project
        return ProjectGroup(project: project, name: name, count: items.count, newest: items[0], items: items)
    }
}

private func workingSection(_ working: [NowItem], lastActive: (Session) -> Date) -> WorkingSection {
    guard working.count > Rules.flatWorkingLimit else { return .list(working) }
    let groups = byProject(working).sorted {
        if $0.count != $1.count { return $0.count > $1.count }
        let (lhs, rhs) = (lastActive($0.newest.session), lastActive($1.newest.session))
        return lhs == rhs ? $0.project < $1.project : lhs > rhs
    }
    let kept = Array(groups.prefix(Rules.flatWorkingLimit))
    let rest = groups.dropFirst(Rules.flatWorkingLimit)
    let more = rest.isEmpty ? nil : MoreProjects(projects: rest.count, sessions: rest.reduce(0) { $0 + $1.count })
    return .groups(kept, more: more)
}
