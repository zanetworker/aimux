import Foundation

public struct WorktreeNode: Equatable, Sendable {
    public let name: String
    public let count: Int
    public let working: Int
    /// Starts unfolded.
    public let open: Bool
    /// Newest first.
    public let sessions: [Session]
}

public struct RepoNode: Equatable, Sendable {
    public let repoPath: String
    public let name: String
    /// All its sessions, worktrees included.
    public let count: Int
    public let working: Int
    /// Starts unfolded.
    public let open: Bool
    /// The main checkout's sessions, newest first.
    public let sessions: [Session]
    public let worktrees: [WorktreeNode]
}

/// The All screen's Folders tree, by the shared rules (api/testdata/clients/README.md). `project` keeps only the
/// sessions with that full `project` path. Working here is `busy` or `working: true`; activity is ignored.
public func folderTree(_ sessions: [Session], now: Date, project: String? = nil) -> [RepoNode] {
    let shown = sessions.filter { project == nil || $0.project == project }
    func time(_ session: Session) -> Date { min(session.lastActive, now) }
    func newestFirst(_ lhs: Session, _ rhs: Session) -> Bool {
        time(lhs) == time(rhs) ? codePointsBefore(lhs.id, rhs.id) : time(lhs) > time(rhs)
    }
    func isWorking(_ session: Session) -> Bool { session.state == .busy || session.working }
    func lowerFirst(_ lhs: String, _ rhs: String) -> Bool {
        let (left, right) = (lhs.lowercased(), rhs.lowercased())
        return left == right ? codePointsBefore(lhs, rhs) : codePointsBefore(left, right)
    }
    let newest = shown.min(by: newestFirst)

    var byRepo: [String: [Session]] = [:]
    for session in shown { byRepo[session.folder?.repoPath ?? session.project, default: []].append(session) }
    let repos = byRepo.map { path, members -> RepoNode in
        let ordered = members.sorted(by: newestFirst)
        var byWorktree: [String: [Session]] = [:]
        for session in ordered {
            if let name = session.folder?.worktree { byWorktree[name, default: []].append(session) }
        }
        let worktrees = byWorktree.map { name, list in
            WorktreeNode(name: name, count: list.count, working: list.filter(isWorking).count,
                         open: list.contains { $0.id == newest?.id }, sessions: list)
        }.sorted { lowerFirst($0.name, $1.name) }
        return RepoNode(
            repoPath: path, name: ordered[0].folder?.repo ?? projectName(path), count: ordered.count,
            working: ordered.filter(isWorking).count, open: ordered.contains { $0.id == newest?.id },
            sessions: ordered.filter { $0.folder?.worktree == nil }, worktrees: worktrees)
    }
    return repos.sorted { $0.name.lowercased() == $1.name.lowercased() ? codePointsBefore($0.repoPath, $1.repoPath)
        : lowerFirst($0.name, $1.name) }
}
