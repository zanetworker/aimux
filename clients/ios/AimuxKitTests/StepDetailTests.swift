import Foundation
import Testing
@testable import AimuxKit

@Suite struct StepDetailTests {
    private func numbered(_ count: Int, _ prefix: String) -> String {
        (1...count).map { "\(prefix)\($0)\n" }.joined()
    }

    @Test func sixtyLineEditShowsFortyAndHidesTwenty() {
        let (lines, hidden) = diffLines(old: numbered(30, "a"), new: numbered(30, "b"), limit: 40)
        #expect(lines.count == 40)
        #expect(hidden == 20)
        #expect(lines.first == DiffLine(kind: .removed, text: "a1"))
        #expect(lines[29] == DiffLine(kind: .removed, text: "a30"))
        #expect(lines[30] == DiffLine(kind: .added, text: "b1"))
        #expect(lines.last == DiffLine(kind: .added, text: "b10"))
    }

    @Test func exactlyAtTheLimitHidesNothing() {
        let (lines, hidden) = diffLines(old: numbered(20, "a"), new: numbered(20, "b"), limit: 40)
        #expect(lines.count == 40)
        #expect(hidden == 0)
        #expect(diffLines(old: numbered(41, "a"), new: "", limit: 40).hidden == 1)
    }

    @Test func trailingNewlineIsNotALineAndTabsStay() {
        let (lines, hidden) = diffLines(old: "\tlet a = 1\n", new: "\tlet a = 2\n\n", limit: 40)
        #expect(lines == [DiffLine(kind: .removed, text: "\tlet a = 1"),
                          DiffLine(kind: .added, text: "\tlet a = 2"), DiffLine(kind: .added, text: "")])
        #expect(hidden == 0)
    }

    @Test func crlfSplitsOnLineFeedsAndKeepsTheReturn() {
        // Like the web: split on "\n" only, one final "\n" dropped, a "\r" stays part of its line.
        let (lines, hidden) = diffLines(old: "a\r\nb\r\n", new: "c\r\n\r\nd", limit: 40)
        #expect(lines == [DiffLine(kind: .removed, text: "a\r"), DiffLine(kind: .removed, text: "b\r"),
                          DiffLine(kind: .added, text: "c\r"), DiffLine(kind: .added, text: "\r"),
                          DiffLine(kind: .added, text: "d")])
        #expect(hidden == 0)
        let many = String(repeating: "x\r\n", count: 45)
        #expect(diffLines(old: many, new: "", limit: 40) == (lines: Array(repeating: DiffLine(kind: .removed,
                text: "x\r"), count: 40), hidden: 5))
    }

    @Test func emptyTextHasNoLines() {
        #expect(diffLines(old: "", new: "", limit: 40).lines.isEmpty)
        #expect(diffLines(old: "", new: "x", limit: 40).lines == [DiffLine(kind: .added, text: "x")])
    }

    @Test func drawnTextIsCappedByUnicodeScalars() {
        // A 10 000-character single-line Write: its one added line is drawn as 2000 characters and "…".
        let write = StepDetail(input: "", error: "", old: "", new: String(repeating: "w", count: 10_000))
        let shown = ShownStepDetail(write, diffLimit: 40)
        #expect(shown.lines == [DiffLine(kind: .added, text: String(repeating: "w", count: 2000) + "…")])
        let long = StepDetail(input: String(repeating: "i", count: 10_000),
                              error: String(repeating: "e", count: 10_000), old: "", new: "")
        #expect(ShownStepDetail(long, diffLimit: 40).input == String(repeating: "i", count: 4000) + "…")
        #expect(ShownStepDetail(long, diffLimit: 40).error == String(repeating: "e", count: 2000) + "…")
        let atCap = StepDetail(input: String(repeating: "i", count: 4000), error: "",
                               old: String(repeating: "o", count: 2000), new: "")
        #expect(ShownStepDetail(atCap, diffLimit: 40).input == String(repeating: "i", count: 4000))
        #expect(ShownStepDetail(atCap, diffLimit: 40).lines.first?.text == String(repeating: "o", count: 2000))
        // Scalars, like the web's code points: 2000 emoji is at the cap, 2001 is over it.
        let emoji = StepDetail(input: "", error: "", old: String(repeating: "😀", count: 2000),
                               new: String(repeating: "😀", count: 2001))
        #expect(ShownStepDetail(emoji, diffLimit: 40).lines.map(\.text)
                == [String(repeating: "😀", count: 2000), String(repeating: "😀", count: 2000) + "…"])
        // A flag is two scalars: the cap may split it, as the web's does.
        #expect(cappedScalars("🇩🇪🇩🇪", 3) == "🇩🇪\u{1F1E9}…")
        #expect(ShownStepDetail(write, diffLimit: 0).hidden == 1)
    }

    @Test func detailIsEmptyOnlyWithAllFourEmpty() {
        #expect(StepDetail.empty.isEmpty)
        #expect(Step(kind: .run, target: "ls", status: .ok).detail.isEmpty)
        #expect(!StepDetail(input: "", error: "", old: "", new: "\n").isEmpty)
        #expect(!StepDetail(input: "ls", error: "", old: "", new: "").isEmpty)
    }

    @Test func historyDetailReadsNonStringsAsEmpty() throws {
        let text = #"{"name":"Bash","snippet":"ls","ok":false,"detail":{"input":"ls -la","error":7,"new":null}}"#
        let tool = try APIDecoder.decode(TurnTool.self, from: Data(text.utf8))
        #expect(tool.detail == StepDetail(input: "ls -la", error: "", old: "", new: ""))
        let bare = try APIDecoder.decode(TurnTool.self, from: Data(#"{"name":"Bash","ok":true,"detail":3}"#.utf8))
        #expect(bare.detail.isEmpty)
    }

    @Test func unlinkingKeepsTextAndCollectsWebLinks() {
        let inlines: [MDInline] = [
            .ref(kind: .path, text: "a/b.go", href: "https://github.com/acme/app/blob/main/a/b.go", code: false),
            .text(" and "),
            .link(href: "javascript:alert(1)", content: [.text("bad")]),
            .em([.ref(kind: .pr, text: "#4", href: "https://github.com/acme/app/pull/4", code: true)])
        ]
        let (plain, links) = unlinkedInlines(inlines)
        #expect(plain == [.text("a/b.go"), .text(" and "), .text("bad"), .em([.code("#4")])])
        #expect(links == [
            StepLink(href: "https://github.com/acme/app/blob/main/a/b.go", text: "a/b.go", kind: .path),
            StepLink(href: "https://github.com/acme/app/pull/4", text: "#4", kind: .pr)
        ])
    }
}
