import SwiftUI
import UIKit
import AimuxKit

/// The only colour on a row: green working, amber needs you, red failed, grey idle.
enum DotStatus {
    case working, needsYou, failed, idle

    init(_ state: SessionState) {
        self = state == .busy ? .working : .idle
    }

    var color: Color {
        switch self {
        case .working: .green
        case .needsYou: .amber
        case .failed: .red
        case .idle: Color(.systemGray3)
        }
    }

    var label: String {
        switch self {
        case .working: "Working"
        case .needsYou: "Needs you"
        case .failed: "Failed"
        case .idle: "Idle"
        }
    }
}

struct StatusDot: View {
    let status: DotStatus

    var body: some View {
        Circle()
            .fill(status.color)
            .frame(width: 7, height: 7)
            .accessibilityLabel(status.label)
    }
}

extension Color {
    /// The "needs you" amber: the summary count, the card label and its dot.
    static let amber = Color(UIColor { traits in
        traits.userInterfaceStyle == .dark
            ? UIColor(red: 0xFF / 255, green: 0xB3 / 255, blue: 0x40 / 255, alpha: 1)  // #ffb340
            : UIColor(red: 0xC5 / 255, green: 0x6F / 255, blue: 0x00 / 255, alpha: 1)  // #c56f00
    })
}

/// Keeps a title or folder name from reordering the text around it (titles may be Arabic).
func isolated(_ text: String) -> String { "\u{2068}\(text)\u{2069}" }

extension Text {
    /// Shows `text` with its isolate marks (they only steer layout) and labels it without them,
    /// so VoiceOver and UI tests read the plain words. Use it wherever `isolated(_:)` is shown.
    init(bidi text: String) {
        self = Text(text).accessibilityLabel(text.filter { $0 != "\u{2068}" && $0 != "\u{2069}" })
    }
}
