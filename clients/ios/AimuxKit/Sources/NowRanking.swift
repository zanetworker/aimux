import Foundation

public struct NowItem: Equatable, Sendable {
    public let session: Session
    public let activity: Activity

    public init(session: Session, activity: Activity) {
        self.session = session
        self.activity = activity
    }
}

/// One tab of an iTerm2 window: the sessions listed for it, in pane order.
public struct NowTab: Equatable, Sendable {
    public let index: Int
    public let name: String
    /// The panes the tab has on the Mac, shells included; not the number of sessions listed.
    public let panes: Int
    /// The working sessions it lists.
    public let working: Int
    /// Starts unfolded.
    public let open: Bool
    public let sessions: [Session]
}

public struct NowWindow: Equatable, Sendable {
    public let id: String
    public let name: String
    public let tabs: [NowTab]
}

/// Sessions sharing a folder label, newest first.
public struct FolderGroup: Equatable, Sendable {
    public let label: String
    public let sessions: [Session]
}

public struct NowView: Equatable, Sendable {
    public let needsYou: [NowItem]
    public let pinned: [NowItem]
    public let windows: [NowWindow]
    /// Tabs the query or Working chip left empty.
    public let hiddenTabs: Int
    public let elsewhere: [FolderGroup]
    /// With a query: matching sessions that are not open.
    public let other: [Session]
    /// All sessions, before the query and Working chip.
    public let working: Int
    public let open: Int

    public static let empty = NowView(needsYou: [], pinned: [], windows: [], hiddenTabs: 0, elsewhere: [], other: [],
                                      working: 0, open: 0)
}

private let recentFailure: TimeInterval = 900

/// Working: busy, working activity, or the server's `working` flag. All's Folders pass `.none` (no activity).
public func isWorking(_ session: Session, activity: Activity) -> Bool {
    if session.state == .busy || session.working { return true }
    if case .working = activity { return true }
    return false
}

/// The last path component of a project, trailing slashes trimmed first.
func projectName(_ project: String) -> String {
    var trimmed = Substring(project)
    while trimmed.hasSuffix("/") { trimmed = trimmed.dropLast() }
    let name = trimmed.split(separator: "/", omittingEmptySubsequences: true).last
    return name.map(String.init) ?? project
}

/// Ranks Now by the shared rules (api/testdata/clients/README.md): Needs you, Pinned, the Mac's windows,
/// Elsewhere and, with a query, Other sessions. A session appears once, in the first section that takes it.
public func rankNow(sessions: [Session], activity: [String: Activity], now: Date, query: String = "",
                    workingOnly: Bool = false) -> NowView {
    let ranking = Ranking(activity: activity, now: now)
    let words = queryWords(query)
    let hasQuery = !words.isEmpty

    let needsYouAll = ranking.needsYou(sessions)
    let urgent = Set(needsYouAll.map(\.session.id))
    let needsYou = needsYouAll.filter { matches($0.session, words: words) }
    let rest = sessions.filter { !urgent.contains($0.id) }
    let wanted: (Session) -> Bool = { matches($0, words: words) && (!workingOnly || ranking.isWorking($0)) }

    let pinned = rest.filter { $0.pinned && $0.mac == nil }
    let placed = rest.filter { $0.mac != nil }
    let unpinned = rest.filter { !($0.pinned && $0.mac == nil) }
    let elsewhere = unpinned.filter { $0.mac == nil && ranking.isOpen($0) }
    let other = unpinned.filter { !ranking.isOpen($0) }

    let (windows, hidden) = ranking.windows(placed, words: words, wanted: wanted)
    return NowView(
        needsYou: needsYou,
        pinned: pinned.filter(wanted).sorted(by: ranking.newestFirst).map(ranking.item),
        windows: windows,
        hiddenTabs: hidden,
        elsewhere: ranking.groups(elsewhere.filter(wanted)),
        other: hasQuery && !workingOnly ? other.filter(wanted).sorted(by: ranking.newestFirst) : [],
        working: sessions.filter(ranking.isWorking).count,
        open: sessions.filter(ranking.isOpen).count
    )
}

private struct Ranking {
    let activity: [String: Activity]
    let now: Date

    func current(_ session: Session) -> Activity { activity[session.id] ?? .none }
    func item(_ session: Session) -> NowItem { NowItem(session: session, activity: current(session)) }
    func isOpen(_ session: Session) -> Bool { AimuxKit.isOpen(session, activity: current(session)) }
    func isWorking(_ session: Session) -> Bool { AimuxKit.isWorking(session, activity: current(session)) }

    func lastActive(_ session: Session) -> Date { min(session.lastActive, now) }
    /// Equal times fall back to the session id, ascending, so the order is deterministic.
    func newestFirst(_ lhs: Session, _ rhs: Session) -> Bool {
        lastActive(lhs) == lastActive(rhs) ? codePointsBefore(lhs.id, rhs.id) : lastActive(lhs) > lastActive(rhs)
    }

    /// Pending approvals (newest session first), then failures of the last 900 s (newest failure first).
    func needsYou(_ sessions: [Session]) -> [NowItem] {
        var approvals: [Session] = []
        var failures: [(session: Session, time: Date)] = []
        for session in sessions {
            switch current(session) {
            case .needsApproval: approvals.append(session)
            case .failed(_, let time) where now.timeIntervalSince(time) <= recentFailure:
                failures.append((session, time))
            default: break
            }
        }
        failures.sort { $0.time == $1.time ? codePointsBefore($0.session.id, $1.session.id) : $0.time > $1.time }
        return approvals.sorted(by: newestFirst).map(item) + failures.map { item($0.session) }
    }

    /// Windows by index, tabs by index, panes by number; the count of tabs the filters left empty. A window is
    /// keyed by its id; its name and index come from its first session in Mac order.
    func windows(_ placed: [Session], words: [String], wanted: (Session) -> Bool) -> ([NowWindow], Int) {
        var existing = Set<TabKey>()
        var byTab: [TabKey: [Session]] = [:]
        var front = Set<TabKey>()
        for session in placed {
            guard let mac = session.mac else { continue }
            let key = TabKey(window: mac.window.id, tab: mac.tab.index)
            existing.insert(key)
            if mac.lastInFront { front.insert(key) }
            if wanted(session) { byTab[key, default: []].append(session) }
        }
        var byWindow: [String: (first: Session, tabs: [NowTab])] = [:]
        for (key, listed) in byTab {
            let ordered = listed.sorted(by: inMacOrder)
            guard let first = ordered.first, let mac = first.mac else { continue }
            let tab = NowTab(index: key.tab, name: mac.tab.name, panes: mac.panes,
                             working: ordered.filter(isWorking).count,
                             open: words.isEmpty ? front.contains(key) : true, sessions: ordered)
            var entry = byWindow[key.window] ?? (first, [])
            if inMacOrder(first, entry.first) { entry.first = first }
            entry.tabs.append(tab)
            byWindow[key.window] = entry
        }
        let windows = byWindow.values.sorted { inMacOrder($0.first, $1.first) }.compactMap { entry in
            entry.first.mac.map { mac in
                NowWindow(id: mac.window.id, name: mac.window.name, tabs: entry.tabs.sorted { $0.index < $1.index })
            }
        }
        return (windows, existing.count - byTab.count)
    }

    /// Elsewhere groups by folder label (or the project's last component), newest group first.
    func groups(_ sessions: [Session]) -> [FolderGroup] {
        var byLabel: [String: [Session]] = [:]
        for session in sessions {
            byLabel[folderLabel(session), default: []].append(session)
        }
        return byLabel.map { FolderGroup(label: $0.key, sessions: $0.value.sorted(by: newestFirst)) }.sorted {
            let (lhs, rhs) = ($0.sessions[0], $1.sessions[0])
            return lastActive(lhs) == lastActive(rhs) ? codePointsBefore($0.label, $1.label)
                : lastActive(lhs) > lastActive(rhs)
        }
    }
}

private struct TabKey: Hashable {
    let window: String
    let tab: Int
}
