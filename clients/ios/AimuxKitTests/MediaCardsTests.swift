import Foundation
import Testing
@testable import AimuxKit

private let mediaFixtureNames = ["kinds", "not-media"]

private struct MediaFixture: Decodable {
    struct Case: Decodable {
        let name: String
        let markdown: String
        let blocks: [MDBlock]
    }

    let name: String
    let cases: [Case]
}

private final class MediaBundleToken {}

private func loadMediaFixture(_ name: String) throws -> MediaFixture {
    let bundle = Bundle(for: MediaBundleToken.self)
    let url = try #require(bundle.url(forResource: name, withExtension: "json", subdirectory: "clients/media"))
    return try JSONDecoder().decode(MediaFixture.self, from: Data(contentsOf: url))
}

/// Foundation may write a `\` in an href as `%5C` (README, "Media"), so hrefs are compared with every `\` written that
/// way on both sides.
private func foundationHrefs(_ block: MDBlock) -> MDBlock {
    guard case .paragraph(let inlines) = block else { return block }
    return .paragraph(inlines.map { inline in
        guard case .link(let href, let content) = inline else { return inline }
        return .link(href: href.replacingOccurrences(of: "\\", with: "%5C"), content: content)
    })
}

@Suite struct MediaFixtureTests {
    @Test(arguments: mediaFixtureNames) func matchesSharedFixture(name: String) throws {
        let fixture = try loadMediaFixture(name)
        #expect(!fixture.cases.isEmpty)
        for item in fixture.cases {
            let got = mediaCards(linkReferences(markdownBlocks(item.markdown), context: .none)).map(foundationHrefs)
            let want = item.blocks.map(foundationHrefs)
            #expect(got == want, "\(name): \(item.name)\n got: \(treeJSON(got))\nwant: \(treeJSON(want))")
        }
    }

    @Test func fixtureListCoversTheFolder() {
        let bundle = Bundle(for: MediaBundleToken.self)
        let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/media") ?? []
        #expect(Set(urls.map { $0.deletingPathExtension().lastPathComponent }) == Set(mediaFixtureNames))
    }
}

@Suite struct MediaCardsTests {
    @Test func repliesGetCardsAndBubblesKeepTheLink() {
        let text = "https://example.com/demo.mp4"
        #expect(MarkdownRender.reply(text, context: .none) == [
            .media(MDMedia(kind: .video, href: text, title: "demo.mp4"))
        ])
        #expect(MarkdownRender.bubble(text, context: .none) == [
            .paragraph([.link(href: text, content: [.text(text)])])
        ])
    }

    @Test func aLinkedImageIsTheLinkNotTheImage() throws {
        // Should Foundation mark a thumbnail inside a link with both its image and its link, the link decides.
        let watch = try #require(URL(string: "https://www.youtube.com/watch?v=abc123"))
        let thumb = try #require(URL(string: "https://img.youtube.com/vi/abc123/0.jpg"))
        #expect(imageOnlyLink([InlineRun(text: "thumb", link: watch, image: thumb)]) == nil)
        #expect(imageOnlyLink([InlineRun(text: "thumb", image: thumb)]) == [
            .link(href: thumb.absoluteString, content: [.text("thumb")])
        ])
    }

    @Test func hostIsLowercasedWithoutUserOrPort() {
        #expect(mediaHost("HTTPS://me@Example.COM:8443/a.mp4?x#y") == "example.com")
        #expect(mediaHost("https://example.com") == "example.com")
        #expect(mediaHost("ftp://example.com/a.mp4") == nil)
    }

    @Test func foundationEncodesABackslashBeforeThePath() throws {
        // Why the README's rule names %5C too: URL(string:) keeps the address but writes the backslash encoded,
        // so the host it reports is the one after the @, while a browser would go to the one before it.
        let url = try #require(URL(string: "https://evil.example\\@youtube.com/watch?v=abc"))
        #expect(url.absoluteString == "https://evil.example%5C@youtube.com/watch?v=abc")
        #expect(url.host(percentEncoded: false) == "youtube.com")
        #expect(URL(string: "https://cdn.ex%61mple/a.png")?.host(percentEncoded: false) == "cdn.example")
    }

    @Test func backslashOrParsedHostMismatchIsNoMedia() {
        #expect(mediaKind("https://evil.example%5C@youtube.com/watch?v=abc") == nil)
        #expect(mediaKind("https://evil.example%5c@cdn.example/a.png") == nil)
        #expect(mediaKind("https://evil.example\\@cdn.example/a.png") == nil)
        #expect(mediaKind("https://cdn.ex%61mple/a.png") == nil)
        #expect(mediaKind("https://me@cdn.example:8443/a.png") == .image)
        #expect(mediaKind("https://[::1]:8443/a.png") == .image)
    }

    @Test func undecodableFileNameStaysAsWritten() {
        let href = "https://e.com/a%ZZ.png"
        #expect(mediaCards([.paragraph([.link(href: href, content: [.text(href)])])])
            == [.media(MDMedia(kind: .image, href: href, title: "a%ZZ.png"))])
    }
}

@Suite @MainActor struct MediaPlaybackTests {
    @Test func startingOneCardStopsTheOther() {
        let playback = MediaPlayback()
        let first = UUID()
        let second = UUID()
        playback.start(first)
        #expect(playback.isPlaying(first))
        playback.start(second)
        #expect(!playback.isPlaying(first) && playback.isPlaying(second))
    }

    @Test func aStoppedCardOnlyClearsItself() {
        let playback = MediaPlayback()
        let first = UUID()
        let second = UUID()
        playback.start(second)
        #expect(!playback.stop(first), "another card is playing: keep the audio session")
        #expect(playback.isPlaying(second))
        #expect(playback.stop(second), "nothing plays any more: release the audio session")
        #expect(playback.playing == nil)
        #expect(playback.stop(second), "stopping twice is harmless")
    }
}
