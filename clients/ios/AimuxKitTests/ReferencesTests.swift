import Foundation
import Testing
@testable import AimuxKit

private let referenceFixtureNames = ["commits", "false-positives", "gitlab", "jira", "no-context", "paths", "prs"]

private struct ReferenceFixture: Decodable {
    struct Context: Decodable {
        let repo: SessionRepo?
        let jiraBase: String?
        let jiraProjects: [String]
        let project: String?
    }

    struct Case: Decodable {
        let name: String
        let context: Context
        let inlines: [MDInline]
        let expect: [MDInline]
    }

    let name: String
    let cases: [Case]
}

private final class ReferencesBundleToken {}

private func loadReferenceFixture(_ name: String) throws -> ReferenceFixture {
    let bundle = Bundle(for: ReferencesBundleToken.self)
    let url = try #require(bundle.url(forResource: name, withExtension: "json", subdirectory: "clients/references"))
    return try JSONDecoder().decode(ReferenceFixture.self, from: Data(contentsOf: url))
}

@Suite struct ReferencesFixtureTests {
    @Test(arguments: referenceFixtureNames) func matchesSharedFixture(name: String) throws {
        let fixture = try loadReferenceFixture(name)
        #expect(!fixture.cases.isEmpty)
        for item in fixture.cases {
            let context = ReferenceContext(repo: item.context.repo, jiraBase: item.context.jiraBase,
                                           jiraProjects: item.context.jiraProjects, project: item.context.project)
            let got = linkReferences(item.inlines, context: context)
            #expect(got == item.expect, "\(name): \(item.name)\n got: \(treeJSON(got))\nwant: \(treeJSON(item.expect))")
        }
    }

    @Test func fixtureListCoversTheFolder() {
        let bundle = Bundle(for: ReferencesBundleToken.self)
        let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/references") ?? []
        #expect(Set(urls.map { $0.deletingPathExtension().lastPathComponent }) == Set(referenceFixtureNames))
    }
}

@Suite struct ReferencesTests {
    @Test func appliesInsideEveryBlockWithInlines() {
        let blocks = markdownBlocks("# Fix #12\n\n> - see #13\n\n| a |\n| - |\n| #14 |")
        let linked = linkReferences(blocks, context: demoContext)
        let pull = { (number: Int) in
            MDInline.ref(kind: .pr, text: "#\(number)", href: "https://github.com/acme/widget/issues/\(number)",
                         code: false)
        }
        #expect(linked == [
            .heading(level: 1, inlines: [.text("Fix "), pull(12)]),
            .quote([.list(MDList(ordered: false, start: nil, items: [
                MDListItem(task: nil, blocks: [.paragraph([.text("see "), pull(13)])])
            ]))]),
            .table(MDTable(align: [.left], header: [[.text("a")]], rows: [[[pull(14)]]]))
        ])
    }

    @Test func neverInsideLinksOrCommands() {
        let blocks = markdownBlocks("[see #12](https://example.com/x) and `git show a1b2c3d`")
        #expect(linkReferences(blocks, context: demoContext) == blocks)
    }

    @Test func branchAndPathAreEncodedPerSegment() {
        let context = ReferenceContext(repo: SessionRepo(web: "https://github.com/acme/widget", branch: "feature/a b"),
                                       jiraBase: nil, jiraProjects: [], project: nil)
        #expect(linkReferences([.text("pkg/v1@2+x.go")], context: context) == [
            .ref(kind: .path, text: "pkg/v1@2+x.go",
                 href: "https://github.com/acme/widget/blob/feature/a%20b/pkg/v1%402%2Bx.go", code: false)
        ])
    }

    @Test func contextComesFromTheSessionAndHealth() throws {
        let detail = """
        {"id":"s1","provider":"claude","title":"t","project":"/Users/dev/widget/api",
         "lastActive":"2026-10-27T10:00:00Z","state":"idle","turns":[],
         "repo":{"web":"https://github.com/acme/widget","branch":"main","root":"/Users/dev/widget"}}
        """
        let health = """
        {"status":"ok","login":"a","version":"v","acpProtocolVersion":1,"capabilities":[],
         "links":{"jiraBase":"https://jira.example.com","jiraProjects":["WID"]}}
        """
        let session = try APIDecoder.decode(SessionDetail.self, from: Data(detail.utf8))
        let links = try APIDecoder.decode(Health.self, from: Data(health.utf8)).links
        let context = ReferenceContext(session: session, links: links)
        #expect(context == ReferenceContext(
            repo: SessionRepo(web: "https://github.com/acme/widget", branch: "main", root: "/Users/dev/widget"),
            jiraBase: "https://jira.example.com", jiraProjects: ["WID"], project: "/Users/dev/widget/api"))
        #expect(ReferenceContext(session: nil, links: nil) == ReferenceContext(repo: nil, jiraBase: nil,
                                                                                jiraProjects: [], project: nil))
    }

    @Test func oldServersSendNoRepoOrLinks() throws {
        let detail = """
        {"id":"s1","provider":"claude","title":"t","project":"/p","lastActive":"2026-10-27T10:00:00Z",
         "state":"idle","turns":[],"repo":{"web":"https://github.com/acme/widget"}}
        """
        let session = try APIDecoder.decode(SessionDetail.self, from: Data(detail.utf8))
        #expect(session.repo == nil)
        let health = #"{"status":"ok","login":"a","version":"v","acpProtocolVersion":1,"capabilities":[]}"#
        #expect(try APIDecoder.decode(Health.self, from: Data(health.utf8)).links == nil)
    }
}
