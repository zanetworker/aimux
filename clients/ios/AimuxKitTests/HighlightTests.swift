import Foundation
import Testing
@testable import AimuxKit

private final class HighlightBundleToken {}

private struct HighlightFixture: Decodable {
    let name: String
    let lang: String
    let code: String
    let tokens: [[String]]
}

private func highlightFixtures() throws -> [HighlightFixture] {
    let bundle = Bundle(for: HighlightBundleToken.self)
    let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/highlight") ?? []
    return try urls.sorted { $0.lastPathComponent < $1.lastPathComponent }
        .map { try JSONDecoder().decode(HighlightFixture.self, from: Data(contentsOf: $0)) }
}

/// Syntax colours in code blocks, pinned by the shared fixtures (the web client passes the same ones).
@Suite struct HighlightTests {
    @Test func matchesEverySharedFixture() throws {
        let fixtures = try highlightFixtures()
        #expect(fixtures.count >= 10)
        for fixture in fixtures {
            let got = highlight(fixture.code, lang: fixture.lang).map { [$0.kind.rawValue, $0.text] }
            #expect(got == fixture.tokens, "\(fixture.name)")
            #expect(got.map { $0[1] }.joined() == fixture.code, "\(fixture.name): lossless")
        }
    }

    @Test func languageNamesIgnoreCaseAndAliasesWork() {
        #expect(highlight("return 1", lang: "GO") == highlight("return 1", lang: "golang"))
        #expect(highlight("", lang: "go").isEmpty)
        #expect(highlight("x", lang: nil) == [CodeToken(.plain, "x")])
    }
}
