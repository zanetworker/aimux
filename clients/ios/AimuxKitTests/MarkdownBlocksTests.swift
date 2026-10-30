import Foundation
import Testing
@testable import AimuxKit

private let markdownFixtureNames = [
    "arabic", "claude-reply", "code-fences", "emphasis-canonical", "emphasis", "headings-setext", "headings",
    "hostile", "inline-code", "links", "lists-bullets", "lists-nested", "lists-numbered", "lists-tasks",
    "paragraphs-and-breaks", "quotes", "rules", "stream-empty-fence", "stream-open-emphasis",
    "stream-partial-table", "stream-unclosed-fence", "tables", "two-tables-with-text"
]

private struct MarkdownFixture: Decodable {
    let name: String
    let markdown: String
    let blocks: [MDBlock]
}

private final class MarkdownBundleToken {}

private func loadMarkdownFixture(_ name: String) throws -> MarkdownFixture {
    let bundle = Bundle(for: MarkdownBundleToken.self)
    let url = try #require(bundle.url(forResource: name, withExtension: "json", subdirectory: "clients/markdown"))
    return try JSONDecoder().decode(MarkdownFixture.self, from: Data(contentsOf: url))
}

/// The tree as sorted JSON, so a mismatch shows both sides readably.
func treeJSON<T: Encodable>(_ value: T) -> String {
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
    return (try? encoder.encode(value)).flatMap { String(bytes: $0, encoding: .utf8) } ?? "?"
}

@Suite struct MarkdownFixtureTests {
    @Test(arguments: markdownFixtureNames) func matchesSharedFixture(name: String) throws {
        let fixture = try loadMarkdownFixture(name)
        let got = markdownBlocks(fixture.markdown)
        #expect(got == fixture.blocks, "\(fixture.name)\n got: \(treeJSON(got))\nwant: \(treeJSON(fixture.blocks))")
    }

    @Test func fixtureListCoversTheFolder() {
        let bundle = Bundle(for: MarkdownBundleToken.self)
        let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/markdown") ?? []
        #expect(Set(urls.map { $0.deletingPathExtension().lastPathComponent }) == Set(markdownFixtureNames))
    }

    @Test func fixtureRoundTripsThroughTheWireShape() throws {
        let fixture = try loadMarkdownFixture("claude-reply")
        let again = try JSONDecoder().decode([MDBlock].self, from: Data(treeJSON(fixture.blocks).utf8))
        #expect(again == fixture.blocks)
    }
}

@Suite struct MarkdownBlocksTests {
    @Test func keepsWebLinksOnly() {
        let src = "[a](https://example.test/a) [b](javascript:alert(1)) [c](file:///etc/hosts) "
            + "[d](HTTP://example.test/d) [e](tel:123) <mailto:x@example.test> [f](data:text/html,hi) [g](/relative)"
        #expect(markdownBlocks(src) == [.paragraph([
            .link(href: "https://example.test/a", content: [.text("a")]), .text(" b c "),
            .link(href: "HTTP://example.test/d", content: [.text("d")]),
            .text(" e mailto:x@example.test f g")
        ])])
    }

    @Test func wwwAndEmailAreNeverLinks() {
        #expect(markdownBlocks("see www.example.com or me@example.com") == [
            .paragraph([.text("see www.example.com or me@example.com")])
        ])
    }

    @Test func bareURLInsideAWordIsText() {
        #expect(markdownBlocks("abchttps://example.com/x") == [.paragraph([.text("abchttps://example.com/x")])])
    }

    @Test func emptyTextHasNoBlocks() {
        #expect(markdownBlocks("").isEmpty)
        #expect(markdownBlocks("   \n\n").isEmpty)
    }

    @Test func breaksKeepTypedLines() {
        #expect(markdownBlocks("one\ntwo", breaks: true) == [.paragraph([.text("one"), .br, .text("two")])])
        #expect(markdownBlocks("one\ntwo") == [.paragraph([.text("one two")])])
    }

    @Test func missingTableCellsAndEmptyItemsAreKept() {
        #expect(markdownBlocks("| a | b |\n| --- | --- |\n| | x |") == [.table(MDTable(
            align: [.left, .left], header: [[.text("a")], [.text("b")]], rows: [[[], [.text("x")]]]))])
        #expect(markdownBlocks("- a\n-\n- c") == [.list(MDList(ordered: false, start: nil, items: [
            MDListItem(task: nil, blocks: [.paragraph([.text("a")])]), MDListItem(task: nil, blocks: []),
            MDListItem(task: nil, blocks: [.paragraph([.text("c")])])
        ]))])
    }

    @Test func imagesBecomeTheirAltText() {
        #expect(markdownBlocks("![a chart](x.png) and ![](y.png) end") == [.paragraph([.text("a chart and  end")])])
    }

    @Test func htmlBlockLinesAreLiteralParagraphs() {
        #expect(markdownBlocks("<div>\n*x*\n</div>") == [
            .paragraph([.text("<div>")]), .paragraph([.em([.text("x")])]), .paragraph([.text("</div>")])
        ])
    }

    @Test func codeLanguageIsTheFirstWord() {
        #expect(markdownBlocks("```py extra words\nx\n```") == [.code(lang: "py", text: "x")])
    }

    @Test func deepNestingIsCappedAtOneHundredLevels() {
        let blocks = markdownBlocks(String(repeating: ">", count: 300) + " deep")
        #expect(quoteDepth(blocks) == 100)
        let nested = (0..<150).map { String(repeating: "  ", count: $0) + "- x" }.joined(separator: "\n")
        #expect(listDepth(markdownBlocks(nested)) == 100)
    }

    @Test func eightKilobyteReplyParsesQuickly() {
        let reply = realisticReply(bytes: 8 * 1024)
        #expect(reply.utf8.count >= 8 * 1024)
        _ = markdownBlocks(reply) // warm up
        let clock = ContinuousClock()
        var best = Duration.seconds(10)
        for _ in 0..<10 {
            let took = clock.measure { _ = linkReferences(markdownBlocks(reply), context: demoContext) }
            best = min(best, took)
        }
        #if DEBUG
        let budget = Duration.milliseconds(40) // unoptimized build
        #else
        let budget = Duration.milliseconds(20)
        #endif
        #expect(best < budget, "took \(best)")
    }

    @Test func formatsOnlyUpToTheLimit() {
        #expect(formatsAsMarkdown(String(repeating: "a", count: maxFormattedCharacters)))
        #expect(!formatsAsMarkdown(String(repeating: "a", count: maxFormattedCharacters + 1)))
    }
}

@Suite struct BubbleBlocksTests {
    @Test func blocksFlattenToParagraphs() {
        let blocks = markdownBlocks("# Title\n- one\n- [x] done\n\n> quoted\n\n```\nls -la\n```", breaks: true)
        #expect(bubbleBlocks(blocks) == [
            .paragraph([.text("Title")]),
            .paragraph([.text("• one")]),
            .paragraph([.text("• [x] done")]),
            .paragraph([.text("> quoted")]),
            .code("ls -la")
        ])
    }

    @Test func codeSpansStayInlineLikeTheWeb() {
        #expect(bubbleBlocks(markdownBlocks("- `npm test`", breaks: true)) == [
            .paragraph([.text("• "), .code("npm test")])
        ])
        #expect(bubbleBlocks(markdownBlocks("`ls`", breaks: true)) == [.paragraph([.code("ls")])])
    }

    @Test func fencesInItemsAndQuotesFollowTheirMarker() {
        #expect(bubbleBlocks(markdownBlocks("- ```\n  make\n  ```", breaks: true)) == [
            .paragraph([.text("•")]), .code("make")
        ])
        #expect(bubbleBlocks(markdownBlocks("> ```\n> x\n> ```", breaks: true)) == [
            .paragraph([.text(">")]), .code("x")
        ])
    }

    @Test func tableRowsJoinWithPipes() {
        let blocks = markdownBlocks("| a | b |\n| - | - |\n| 1 | 2 |")
        #expect(bubbleBlocks(blocks) == [.paragraph([.text("a | b")]), .paragraph([.text("1 | 2")])])
    }

    @Test func orderedItemsKeepTheirNumbers() {
        #expect(bubbleBlocks(markdownBlocks("3. three\n4. four")) == [
            .paragraph([.text("3. three")]), .paragraph([.text("4. four")])
        ])
    }
}

let demoContext = ReferenceContext(
    repo: SessionRepo(web: "https://github.com/acme/widget", branch: "main", root: "/Users/dev/widget"),
    jiraBase: "https://jira.example.com", jiraProjects: ["WID"], project: "/Users/dev/widget")

private func quoteDepth(_ blocks: [MDBlock]) -> Int {
    var depth = 0
    var level = blocks
    while case .quote(let inner)? = level.first {
        depth += 1
        level = inner
    }
    return depth
}

private func listDepth(_ blocks: [MDBlock]) -> Int {
    var depth = 0
    var level = blocks
    while case .list(let list)? = level.first {
        depth += 1
        level = list.items.first?.blocks.dropFirst().map { $0 } ?? []
    }
    return depth
}

/// A reply shaped like Claude's: headings, lists, a table, code, references; repeated to `bytes`.
private func realisticReply(bytes: Int) -> String {
    let part = """
    ## Summary

    I fixed the flaky test in `internal/store/store_test.go:42` and tightened the retry loop (see #128, a1b2c3d).

    ### What changed

    - **Retry loop**: now backs off from 50 ms to 800 ms instead of spinning; WID-12 tracks the rest.
    - **Test**: waits on the `ready` channel rather than sleeping. Docs: https://example.com/docs/retry.
    - [x] Removed the unused `legacyMode` flag in internal/store/options.go.

    | File | Change | Lines |
    | --- | :---: | ---: |
    | `store.go` | retry | 12 |
    | store_test.go | *wait* | 30 |

    ```go
    for attempt := 0; attempt < 5; attempt++ {
        if err := try(); err == nil { return nil }
        time.Sleep(backoff(attempt))
    }
    ```

    > Note: the ~~old~~ behaviour is gone.


    """
    var out = ""
    while out.utf8.count < bytes { out += part }
    return out
}

@Suite struct MarkdownDisplayTests {
    @Test func directionFollowsTheFirstStrongLetter() {
        #expect(isRightToLeft([.text("هذه "), .strong([.text("فقرة")])]))
        #expect(isRightToLeft([.code("x"), .text(" 12 "), .text("عنوان and English")]))
        #expect(!isRightToLeft([.text("Fix "), .text("عنوان")]))
        #expect(!isRightToLeft([.text("123 …")]))
        #expect(!isRightToLeft([]))
    }

    @Test func repliesAreParsedLinkedAndCached() {
        let text = "See #12 and **bold**"
        let blocks = MarkdownRender.reply(text, context: demoContext)
        #expect(blocks == linkReferences(markdownBlocks(text), context: demoContext))
        #expect(MarkdownRender.reply(text, context: .none) == markdownBlocks(text))
        #expect(MarkdownRender.reply(text, context: demoContext) == blocks)
    }

    @Test func streamingParsesAtMostEveryHundredMilliseconds() {
        #expect(parseDelay(sinceLastParse: nil) == .zero)
        #expect(parseDelay(sinceLastParse: .milliseconds(30)) == .milliseconds(70))
        #expect(parseDelay(sinceLastParse: .milliseconds(100)) == .zero)
        #expect(parseDelay(sinceLastParse: .seconds(5)) == .zero)
    }

    @Test func bubblesKeepTypedLines() {
        #expect(MarkdownRender.bubble("one\ntwo", context: .none) == [.paragraph([.text("one"), .br, .text("two")])])
    }

    @Test func hugeTextIsNotParsed() {
        let huge = String(repeating: "# x\n", count: maxFormattedCharacters / 4 + 1)
        #expect(MarkdownRender.reply(huge, context: .none) == nil)
        #expect(MarkdownRender.bubble(huge, context: .none) == nil)
    }
}

@Suite struct RowLinksTests {
    @Test func listsEveryWebLinkAndReferenceInOrder() {
        let row: [[MDInline]] = [
            [.text("see "),
             .ref(kind: .pr, text: "#42", href: "https://github.com/acme/widget/issues/42", code: false)],
            [.strong([.link(href: "https://example.com/a", content: [.text("the docs")])])],
            [.ref(kind: .path, text: "a/b.go", href: "https://github.com/acme/widget/blob/main/a/b.go", code: true)]
        ]
        #expect(webLinks(in: row) == [
            LinkTarget(text: "#42", href: "https://github.com/acme/widget/issues/42"),
            LinkTarget(text: "the docs", href: "https://example.com/a"),
            LinkTarget(text: "a/b.go", href: "https://github.com/acme/widget/blob/main/a/b.go")
        ])
    }

    @Test func skipsOtherSchemesAndPlainCells() {
        let row: [[MDInline]] = [[.text("plain")], [], [.link(href: "javascript:alert(1)", content: [.text("x")])]]
        #expect(webLinks(in: row).isEmpty)
    }
}
