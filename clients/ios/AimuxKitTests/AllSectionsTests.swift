import Foundation
import Testing
@testable import AimuxKit

private let recentNames = clientFixtureNames("all")

private struct Fixture: Decodable {
    struct Section: Decodable, Equatable { let day: String; let ids: [String] }
    struct Expect: Decodable { let sections: [Section] }

    let name: String
    let now: Date
    let timeZone: String
    let project: String?
    let sort: String
    let sessions: [Session]
    let expect: Expect
}

/// Only the Recent files; the Folders files (`sort` is `folders`) are FolderTreeTests'.
private struct SortOnly: Decodable { let sort: String }

@Suite struct AllSectionsTests {
    @Test(arguments: recentNames) func matchesSharedFixture(name: String) throws {
        let sort: SortOnly = try loadClientFixture("all", name)
        guard sort.sort == "recent" else { return }
        let fixture: Fixture = try loadClientFixture("all", name)
        let zone = try #require(TimeZone(identifier: fixture.timeZone))
        let sections = allSections(fixture.sessions, project: fixture.project, sort: .newest,
                                   now: fixture.now, timeZone: zone)
        #expect(sections.map { Fixture.Section(day: $0.day, ids: $0.sessions.map(\.id)) } == fixture.expect.sections)
    }

    @Test func recentFilesExist() throws {
        let kinds = try clientFixtureNames("all").map { (try loadClientFixture("all", $0, as: SortOnly.self)).sort }
        #expect(kinds.filter { $0 == "recent" }.count >= 3)
    }

    @Test func emptyInputGivesNoSections() {
        #expect(allSections([], project: nil, sort: .newest, now: .now, timeZone: .gmt).isEmpty)
    }

    @Test func filterThatMatchesNothingGivesNoSections() throws {
        let fixture: Fixture = try loadClientFixture("all", "recent-by-day")
        let sections = allSections(fixture.sessions, project: "/nowhere", sort: .newest, now: fixture.now,
                                   timeZone: .gmt)
        #expect(sections.isEmpty)
    }
}
