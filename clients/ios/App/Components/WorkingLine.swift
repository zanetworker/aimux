import SwiftUI
import AimuxKit

/// The live line at the end of a terminal chat while Claude works on the Mac: a green dot that breathes, then
/// "Working · 1m 20s · go test ./…" (terminalWorkingLine), refreshed with every read of the session.
struct WorkingLine: View {
    let text: String
    @State private var dim = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        HStack(spacing: 8) {
            StatusDot(status: .working)
                .opacity(dim ? 0.35 : 1)
                .animation(reduceMotion ? nil : .easeInOut(duration: 0.9).repeatForever(autoreverses: true), value: dim)
                .onAppear { dim = !reduceMotion }
            Text(bidi: text)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("workingLine")
    }
}
