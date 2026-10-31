import Foundation
import Testing
@testable import AimuxKit

@Suite struct OpenMemoryTests {
    @Test func nothingRememberedFallsBack() {
        let memory = OpenMemory("")
        #expect(memory.isOpen("Window 1|aimux", otherwise: true))
        #expect(!memory.isOpen("Window 1|aimux", otherwise: false))
    }

    @Test func anExplicitChoiceWinsBothWays() {
        let memory = OpenMemory("").setting("a", open: false).setting("b", open: true)
        #expect(!memory.isOpen("a", otherwise: true), "closed by hand stays closed")
        #expect(memory.isOpen("b", otherwise: false), "opened by hand stays open")
        #expect(memory.isOpen("c", otherwise: true))
    }

    @Test func survivesItsRawText() {
        let raw = OpenMemory("").setting("Reviews|reviews", open: true).setting("/src/aimux", open: false).raw
        let read = OpenMemory(raw)
        #expect(read.isOpen("Reviews|reviews", otherwise: false))
        #expect(!read.isOpen("/src/aimux", otherwise: true))
        #expect(read == OpenMemory(raw))
    }

    @Test func brokenTextIsForgotten() {
        #expect(OpenMemory("{not json").isOpen("a", otherwise: true))
        #expect(OpenMemory(#"{"a":"yes"}"#).isOpen("a", otherwise: true), "only true and false count")
        #expect(OpenMemory(#"[true]"#).raw == "{}")
    }

    @Test func keys() {
        #expect(OpenMemory.tabKey(window: "Window 1", tab: "aimux") == "Window 1|aimux")
        #expect(OpenMemory.folderKey(repoPath: "/src/aimux") == "/src/aimux")
        #expect(OpenMemory.folderKey(repoPath: "/src/aimux", worktree: "clients") == "/src/aimux|clients")
    }
}

@Suite struct RowLinesTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let format = SessionRowFormat(locale: Locale(identifier: "en_US"), timeZone: TimeZone(identifier: "UTC")!)

    private func session(_ state: SessionState, ago: TimeInterval = 240, working: Bool = false,
                         folder: Folder? = nil, pane: Int? = nil) -> Session {
        let place = pane.map {
            MacPlace(window: MacWindow(id: "w1", index: 1, name: "Window 1"), tab: MacTab(index: 3, name: "aimux"),
                     pane: $0, panes: 4, lastInFront: false)
        }
        return Session(id: "s", title: "T", project: "/src/aimux/", lastActive: now.addingTimeInterval(-ago),
                       state: state, folder: folder, mac: place, working: working)
    }

    @Test func workingShowsTheStep() {
        #expect(rowState(session(.busy), activity: .working(step: "go test ./..."), now: now, format: format)
            == .working(step: "go test ./..."))
        #expect(rowState(session(.openInTerminal, working: true), activity: .none, now: now, format: format)
            == .working(step: nil))
        #expect(RowState.working(step: "go test ./...").text == "Working · go test ./...")
        #expect(RowState.working(step: nil).text == "Working")
    }

    @Test func anOpenIdleSessionIsYourTurn() {
        let state = rowState(session(.openInTerminal), activity: .none, now: now, format: format)
        #expect(state == .yourTurn(since: "4 min"))
        #expect(state?.text == "Your turn · 4 min")
        #expect(rowState(session(.openInTerminal, ago: 20), activity: .none, now: now, format: format)?.text
            == "Your turn · just now")
        #expect(rowState(session(.idle, pane: 1), activity: .none, now: now, format: format)?.text
            == "Your turn · 4 min", "in a pane counts as open, whatever the state")
    }

    @Test func aClosedSessionHasNoStateLine() {
        #expect(rowState(session(.idle), activity: .none, now: now, format: format) == nil)
        #expect(rowState(session(.unavailable), activity: .finished(at: now), now: now, format: format) == nil)
    }

    @Test func placeLine() {
        let folder = Folder(repo: "aimux", repoPath: "/src/aimux", worktree: "clients", sub: "ios",
                            label: "aimux / clients · ios")
        #expect(rowPlace(session(.openInTerminal, folder: folder, pane: 2), withTab: false)
            == "aimux / clients · ios · pane 2")
        #expect(rowPlace(session(.openInTerminal, folder: folder, pane: 2), withTab: true)
            == "aimux / clients · ios · tab 3 · pane 2")
        #expect(rowPlace(session(.idle, folder: folder), withTab: true) == "aimux / clients · ios")
        #expect(rowPlace(session(.idle), withTab: true) == "aimux", "no folder: the project's last component")
    }
}
