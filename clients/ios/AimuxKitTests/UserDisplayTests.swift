import Foundation
import Testing
@testable import AimuxKit

private struct DisplayCase: Decodable {
    struct Part: Decodable {
        let kind: String
        let text: String
        let args: String?
        let error: Bool?

        var part: UserMessagePart? {
            switch kind {
            case "text": .text(text)
            case "command": .command(text, args: args ?? "")
            case "output": .output(text, error: error ?? false)
            default: nil
            }
        }
    }

    struct Display: Decodable {
        let text: String
        let images: Int
        let parts: [Part]?
    }

    let name: String
    let saved: String
    let display: Display
}

private struct DisplayFixture: Decodable { let cases: [DisplayCase] }

/// The shared cases in api/testdata/clients/display/user-bubble.json.
@Suite struct UserDisplayTests {
    @Test func matchesSharedFixture() throws {
        let bundle = Bundle(for: DisplayBundleToken.self)
        let url = try #require(bundle.url(forResource: "user-bubble", withExtension: "json",
                                          subdirectory: "clients/display"))
        let cases = try JSONDecoder().decode(DisplayFixture.self, from: Data(contentsOf: url)).cases
        #expect(cases.count >= 10)
        for item in cases {
            let got = userMessageDisplay(item.saved)
            #expect(got.text == item.display.text, "\(item.name)")
            #expect(got.images.count == item.display.images, "\(item.name)")
            let parts = item.display.parts.map { $0.compactMap(\.part) }
                ?? (item.display.text.isEmpty ? [] : [.text(item.display.text)])
            #expect(got.parts == parts, "\(item.name)")
        }
    }

    @Test func typedFormOfACommandMatchesWhatWasTyped() {
        let saved = "<command-name>/tidy</command-name>\n<command-message>tidy</command-message>\n"
            + "<command-args>docs</command-args>"
        #expect(savedPromptAsTyped(saved) == "/tidy docs")
        #expect(savedPromptAsTyped("<bash-input>ls</bash-input><bash-stdout>a</bash-stdout>") == "!ls")
        #expect(savedPromptAsTyped("<local-command-caveat>c</local-command-caveat>").isEmpty)
    }

    @Test func fixtureListCoversTheFolder() {
        let bundle = Bundle(for: DisplayBundleToken.self)
        let urls = bundle.urls(forResourcesWithExtension: "json", subdirectory: "clients/display") ?? []
        #expect(Set(urls.map { $0.deletingPathExtension().lastPathComponent }) == ["user-bubble"])
    }

    @Test func chipsAreLabelledInOrder() {
        let got = userMessageDisplay("[Image #2] and [Image: source: /tmp/a.png] then [Image #10]")
        #expect(got.images == ["Image 2", "Image", "Image 10"])
        #expect(userMessageDisplay("no pictures").images.isEmpty)
        #expect(userMessageDisplay("") == UserMessageDisplay(text: "", images: [], parts: []))
    }
}

/// Thoughts, plan entries, step targets and errors: inline-only Markdown with references.
@Suite struct ChatItemInlineTests {
    private let context = ReferenceContext(repo: SessionRepo(web: "https://github.com/acme/widget", branch: "main",
                                                             root: "/Users/me/widget"),
                                           jiraBase: nil, jiraProjects: [], project: "/Users/me/widget")
    private func href(_ path: String) -> String { "https://github.com/acme/widget/blob/main/" + path }

    @Test func itemTextIsInlineMarkdownWithReferences() {
        let blocks = MarkdownRender.bubble("Check `retryAfter` in *src/stream.go*", context: context)
        #expect(blocks == [.paragraph([.text("Check "), .code("retryAfter"), .text(" in "),
                                       .em([.ref(kind: .path, text: "src/stream.go", href: href("src/stream.go"),
                                                 code: false)])])])
    }

    @Test func aWholePathTargetIsThePathReference() {
        #expect(stepInlines("pkg/__init__.py", context: context)
                == [.ref(kind: .path, text: "pkg/__init__.py", href: href("pkg/__init__.py"), code: false)])
        #expect(stepInlines("/Users/me/widget/src/a.go:12", context: context)
                == [.ref(kind: .path, text: "/Users/me/widget/src/a.go:12", href: href("src/a.go") + "#L12",
                         code: false)])
        #expect(stepInlines("pkg/__init__.py", context: .none) == [.text("pkg/__init__.py")])
        #expect(stepInlines("**/*_test.go", context: context) == [.text("**/*_test.go")])
        #expect(stepInlines("retryAfter", context: context) == [.text("retryAfter")])
    }

    @Test func otherTargetsAreInlineMarkdownOnOneLine() {
        #expect(stepInlines("Read `src/a.go`", context: context)
                == [.text("Read "), .ref(kind: .path, text: "src/a.go", href: href("src/a.go"), code: true)])
        #expect(stepInlines("go test ./...\n- then vet", context: .none) == [.text("go test ./... • then vet")])
        #expect(stepInlines("`ls -la`", context: .none) == [.code("ls -la")])
        #expect(stepInlines("", context: context).isEmpty)
    }
}

private final class DisplayBundleToken {}
