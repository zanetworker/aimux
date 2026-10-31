import Foundation
import Testing
@testable import AimuxKit

private let base = Date(timeIntervalSince1970: 1_800_000_000)

private func detail(working: Bool, seconds: Int = 0, tools: [TurnTool] = []) -> SessionDetail {
    SessionDetail(id: "s1", title: "Sample", project: "/p", lastActive: base, state: .openInTerminal,
                  turns: [Turn(number: 1, time: base, user: "run the tests", reply: "", tools: tools)],
                  terminal: TerminalStatus(status: working ? "busy" : "idle", canType: true, reason: nil, prompt: nil,
                                           working: working, workingSeconds: seconds))
}

/// The live line at the end of a terminal chat while Claude works on the Mac.
@Suite struct TerminalWorkingLineTests {
    @Test func saysHowLongAndTheCurrentStep() {
        let tools = [TurnTool(name: "Read", snippet: "notes.md", ok: true),
                     TurnTool(name: "Bash", snippet: "go test ./remote/...", ok: true)]
        #expect(terminalWorkingLine(detail(working: true, seconds: 80, tools: tools))
                == "Working · 1m 20s · go test ./remote/...")
        #expect(terminalWorkingLine(detail(working: true, seconds: 7)) == "Working · 7s")
        #expect(terminalWorkingLine(detail(working: true)) == "Working")
        #expect(terminalWorkingLine(detail(working: true, tools: [TurnTool(name: "WebSearch", snippet: "", ok: true)]))
                == "Working · WebSearch")
    }

    @Test func nothingWhileIdleOrOutsideATerminal() {
        #expect(terminalWorkingLine(detail(working: false, seconds: 9)) == nil)
        #expect(terminalWorkingLine(nil) == nil)
    }

    @Test func elapsedReadsLikeTheTerminal() {
        #expect(elapsedText(0) == "0s")
        #expect(elapsedText(59) == "59s")
        #expect(elapsedText(60) == "1m")
        #expect(elapsedText(3599) == "59m 59s")
        #expect(elapsedText(3600) == "1h")
        #expect(elapsedText(7380) == "2h 3m")
    }

    @Test func decodesWorkingSeconds() throws {
        let status = try JSONDecoder().decode(TerminalStatus.self, from: Data(
            #"{"status":"busy","canType":true,"working":true,"workingSeconds":42}"#.utf8))
        #expect(status.workingSeconds == 42)
        let old = try JSONDecoder().decode(TerminalStatus.self, from: Data(#"{"status":"idle","canType":true}"#.utf8))
        #expect(old.workingSeconds == 0)
    }
}
