import Foundation
import Synchronization
import AimuxKit

#if DEBUG
/// `-demo chat-long`: every session a long one open on the Mac, Claude working there. demo-18's history gains a
/// turn on each read, so the latest 30 turns move on by one (as a long, busy session does); the others stay put.
/// Invented text, of the sizes such sessions have: long pasted messages and replies of a few paragraphs.
enum DemoLong {
    private static let reads = Mutex(0)

    static func detail(_ session: Session) -> SessionDetail {
        let read = session.id != "demo-18" ? 0 : reads.withLock { value in
            defer { value += 1 }
            return value
        }
        let last = 40 + read
        let turns = (last - 29...last).map(turn)
        return SessionDetail(id: session.id, title: session.title, project: session.project,
                             lastActive: session.lastActive, state: .openInTerminal, lastPrompt: session.lastPrompt,
                             turns: turns, stats: nil,
                             terminal: TerminalStatus(status: "busy", canType: false, reason: "mac_in_use",
                                                      prompt: nil, working: true, workingSeconds: 60 + 2 * read))
    }

    private static func turn(_ number: Int) -> Turn {
        let long = number % 3 == 0
        let user = long ? String(repeating: "Notes pasted for turn \(number), line after line. ", count: 120)
            : "Question \(number): what is next?"
        var reply = "Reply \(number) starts here.\n\n"
        for part in 1...4 {
            reply += "Part \(part) of reply \(number): " + String(repeating: "the plan holds and moves on. ", count: 9)
                + "\n\n"
        }
        reply += "- one point\n- another point\n\nReply \(number) ends here."
        return Turn(number: number, time: DemoData.start.addingTimeInterval(Double(number - 100) * 60), user: user,
                    reply: reply, tools: [TurnTool(name: "Read", snippet: "notes/turn-\(number).md", ok: true)])
    }
}
#endif
