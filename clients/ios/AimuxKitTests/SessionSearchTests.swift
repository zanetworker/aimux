import Foundation
import Testing
@testable import AimuxKit

private let searchNames = clientFixtureNames("search")

private struct Fixture: Decodable {
    struct Expect: Decodable { let open: [String]; let other: [String] }

    let now: Date
    let query: String
    let sessions: [Session]
    let events: [String: [SessionEvent]]?
    let expect: Expect
}

@Suite struct SessionSearchTests {
    @Test(arguments: searchNames) func matchesSharedFixture(name: String) throws {
        let fixture: Fixture = try loadClientFixture("search", name)
        var activity: [String: Activity] = [:]
        for (id, events) in fixture.events ?? [:] { activity[id] = deriveActivity(events) }
        let found = searchSessions(fixture.sessions, query: fixture.query, activity: activity, now: fixture.now)
        #expect(found.open.map(\.id) == fixture.expect.open)
        #expect(found.other.map(\.id) == fixture.expect.other)
    }

    @Test func fixtureFilesExist() {
        #expect(searchNames.count >= 5)
    }

    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func session(_ id: String, state: SessionState = .idle, tab: String? = nil, label: String? = nil,
                         mac: Bool = false) -> Session {
        let place = mac ? MacPlace(window: MacWindow(id: "w", index: 1, name: "Window 1"),
                                   tab: MacTab(index: 1, name: tab ?? ""), pane: 1, panes: 1, lastInFront: false) : nil
        let folder = label.map { Folder(repo: $0, repoPath: "/src/" + $0, label: $0) }
        return Session(id: id, title: id, project: "/src/x", lastActive: now, state: state, folder: folder, mac: place)
    }

    @Test func aSessionWithAPaneIsOpenWhateverItsState() {
        let found = searchSessions([session("a", mac: true), session("b")], query: "a")
        #expect(found.open.map(\.id) == ["a"])
        #expect(found.other.isEmpty)
    }

    @Test func tabAndFolderNamesMatchAndAllWordsMustMatch() {
        let sessions = [session("s1", tab: "Deploy", label: "blog", mac: true), session("s2", label: "blog")]
        #expect(searchSessions(sessions, query: "deploy blog").open.map(\.id) == ["s1"])
        #expect(searchSessions(sessions, query: "deploy nothing").open.isEmpty)
        #expect(searchSessions(sessions, query: "BLOG").other.map(\.id) == ["s2"])
    }

    @Test func blankQueryFindsNothing() {
        let found = searchSessions([session("a")], query: "  \t ")
        #expect(found.open.isEmpty && found.other.isEmpty)
    }

    @Test func matchesNeedsEveryWordInSomeField() {
        let one = Session(id: "x", title: "Fix login", project: "/p", lastActive: now, state: .idle,
                          lastPrompt: "check the redirect")
        #expect(matches(one, words: ["login", "redirect"]))
        #expect(!matches(one, words: ["login", "absent"]))
        #expect(matches(one, words: []))
    }
}
