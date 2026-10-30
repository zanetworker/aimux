import Foundation
import Testing
@testable import AimuxKit

private let fixtureNames = [
    "newest-by-day", "project-filter", "sort-project", "sort-project-path-tie", "sort-most-active", "midnight-in-berlin"
]

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

private func loadFixture(_ name: String) throws -> Fixture {
    let bundle = Bundle(for: AllBundleToken.self)
    let url = try #require(bundle.url(forResource: name, withExtension: "json", subdirectory: "clients/all"))
    return try APIDecoder.decode(Fixture.self, from: Data(contentsOf: url))
}

@Suite struct AllSectionsTests {
    @Test(arguments: fixtureNames) func matchesSharedFixture(name: String) throws {
        let fixture = try loadFixture(name)
        let zone = try #require(TimeZone(identifier: fixture.timeZone))
        let sort = try #require(AllSort(rawValue: fixture.sort))
        let sections = allSections(fixture.sessions, project: fixture.project, sort: sort,
                                   now: fixture.now, timeZone: zone)
        #expect(sections.map { Fixture.Section(day: $0.day, ids: $0.sessions.map(\.id)) } == fixture.expect.sections)
    }

    @Test func fixtureListCoversTheFolder() throws {
        let bundle = Bundle(for: AllBundleToken.self)
        let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/all") ?? []
        #expect(Set(urls.map { $0.deletingPathExtension().lastPathComponent }) == Set(fixtureNames))
    }

    @Test func emptyInputGivesNoSections() {
        #expect(allSections([], project: nil, sort: .newest, now: .now, timeZone: .gmt).isEmpty)
    }

    @Test func filterThatMatchesNothingGivesNoSections() throws {
        let fixture = try loadFixture("newest-by-day")
        let sections = allSections(fixture.sessions, project: "/nowhere", sort: .project, now: fixture.now,
                                   timeZone: .gmt)
        #expect(sections.isEmpty)
    }
}

private final class AllBundleToken {}
