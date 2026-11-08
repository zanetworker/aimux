import Foundation
import Testing
@testable import AimuxKit

private let foldersNames = clientFixtureNames("all")

private struct Fixture: Decodable {
    struct Worktree: Decodable, Equatable {
        let name: String
        let count: Int
        let working: Int
        let open: Bool
        let sessions: [String]
    }
    struct Repo: Decodable, Equatable {
        let repoPath: String
        let name: String
        let count: Int
        let working: Int
        let open: Bool
        let sessions: [String]
        let worktrees: [Worktree]
    }
    struct Expect: Decodable { let repos: [Repo]? }

    let now: Date
    let project: String?
    let sort: String
    let sessions: [Session]
    let expect: Expect
}

@Suite struct FolderTreeTests {
    @Test(arguments: foldersNames) func matchesSharedFixture(name: String) throws {
        let fixture: Fixture = try loadClientFixture("all", name)
        guard fixture.sort == "folders" else { return }
        let tree = folderTree(fixture.sessions, now: fixture.now, project: fixture.project)
        let got = tree.map { repo in
            Fixture.Repo(repoPath: repo.repoPath, name: repo.name, count: repo.count, working: repo.working,
                         open: repo.open, sessions: repo.sessions.map(\.id),
                         worktrees: repo.worktrees.map {
                             Fixture.Worktree(name: $0.name, count: $0.count, working: $0.working, open: $0.open,
                                              sessions: $0.sessions.map(\.id))
                         })
        }
        #expect(got == fixture.expect.repos)
    }

    @Test func foldersFilesExist() throws {
        let kinds = try foldersNames.map { (try loadClientFixture("all", $0, as: Fixture.self)).sort }
        #expect(kinds.filter { $0 == "folders" }.count >= 3)
    }

    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func session(_ id: String, _ project: String, state: SessionState = .idle, working: Bool = false,
                         ago: TimeInterval = 10) -> Session {
        Session(id: id, title: id, project: project, lastActive: now.addingTimeInterval(-ago), state: state,
                working: working)
    }

    @Test func emptyInputAndNoMatchGiveNoRepos() {
        #expect(folderTree([], now: now).isEmpty)
        #expect(folderTree([session("a", "/src/a")], now: now, project: "/nowhere").isEmpty)
    }

    @Test func trailingSlashIsTrimmedBeforeTheNameAndActivityIsIgnored() {
        let tree = folderTree([session("a", "/src/aimux//", state: .busy), session("b", "/src/aimux//")], now: now)
        #expect(tree.map(\.name) == ["aimux"])
        #expect(tree.first?.working == 1)
    }

    @Test func aFutureTimeCountsAsNow() {
        let tree = folderTree([session("late", "/src/z", ago: -500), session("early", "/src/a", ago: 1)], now: now)
        #expect(tree.filter(\.open).map(\.repoPath) == ["/src/z"])
    }
}
