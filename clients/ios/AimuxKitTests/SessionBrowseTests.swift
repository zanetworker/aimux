import Foundation
import Testing
@testable import AimuxKit

// Sunday 2026-10-25 18:00:00 UTC
private let now = Date(timeIntervalSince1970: 1_792_951_200)

private func row(_ id: String, hoursAgo: Double, _ state: SessionState = .idle,
                 project: String = "/src/research", title: String? = nil, prompt: String? = nil) -> Session {
    Session(id: id, title: title ?? "Title \(id)", project: project,
            lastActive: now.addingTimeInterval(-hoursAgo * 3600), state: state, lastPrompt: prompt)
}

@Suite struct SessionFilterTests {
    private let sessions = [
        row("1", hoursAgo: 1, project: "/src/aimux", title: "Fix SSE reconnect", prompt: "run the tests"),
        row("2", hoursAgo: 2, project: "/src/research", title: "Weekly report", prompt: "draft the OpenShell part"),
        row("3", hoursAgo: 3, project: "/src/OpenShell", title: "Gateway TLS", prompt: nil)
    ]

    @Test func emptyQueryAndNoProjectKeepsEverything() {
        #expect(SessionFilter(query: "  ", project: nil).apply(sessions).map(\.id) == ["1", "2", "3"])
    }

    @Test func matchesTitleFolderAndPromptIgnoringCase() {
        #expect(SessionFilter(query: "openshell", project: nil).apply(sessions).map(\.id) == ["2", "3"])
        #expect(SessionFilter(query: "SSE", project: nil).apply(sessions).map(\.id) == ["1"])
    }

    @Test func everyWordMustMatchSomewhere() {
        #expect(SessionFilter(query: "weekly openshell", project: nil).apply(sessions).map(\.id) == ["2"])
        #expect(SessionFilter(query: "weekly gateway", project: nil).apply(sessions).isEmpty)
    }

    @Test func projectFilterUsesFullPath() {
        #expect(SessionFilter(query: "", project: "/src/research").apply(sessions).map(\.id) == ["2"])
        #expect(SessionFilter(query: "gateway", project: "/src/research").apply(sessions).isEmpty)
    }
}

@Suite struct ProjectSummaryTests {
    @Test func countsByFolderMostUsedFirstThenNewest() {
        let sessions = [
            row("1", hoursAgo: 1, project: "/src/aimux"),
            row("2", hoursAgo: 2, project: "/src/research"),
            row("3", hoursAgo: 3, project: "/src/research"),
            row("4", hoursAgo: 4, project: "/src/OpenShell")
        ]
        let projects = ProjectSummary.list(sessions)
        #expect(projects.map(\.name) == ["research", "aimux", "OpenShell"])
        #expect(projects.map(\.count) == [2, 1, 1])
        #expect(projects[0].path == "/src/research")
    }

    @Test func sameFolderNameInTwoPlacesStaysSeparate() {
        let projects = ProjectSummary.list([row("1", hoursAgo: 1, project: "/a/app"),
                                            row("2", hoursAgo: 2, project: "/b/app")])
        #expect(projects.map(\.path) == ["/a/app", "/b/app"])
    }
}

@Suite struct SnippetTests {
    @Test func splitsBracketedMatches() {
        #expect(Snippet.parse("…check the current [OpenShell] architecture [details]") == [
            .init(text: "…check the current ", isMatch: false),
            .init(text: "OpenShell", isMatch: true),
            .init(text: " architecture ", isMatch: false),
            .init(text: "details", isMatch: true)
        ])
    }

    @Test func plainTextIsOneSegment() {
        #expect(Snippet.parse("no match marks") == [.init(text: "no match marks", isMatch: false)])
    }

    @Test func unclosedBracketStaysText() {
        #expect(Snippet.parse("a [b") == [.init(text: "a [b", isMatch: false)])
    }

    @Test func emptySnippetHasNoSegments() {
        #expect(Snippet.parse("").isEmpty)
    }
}
