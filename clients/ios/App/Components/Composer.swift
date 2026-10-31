import SwiftUI
import AimuxKit

/// The message field and one send arrow, like any chat; send becomes stop while Claude works. With the chat
/// queue (`queueEnabled`) and `action` Add to queue (from the shared fixtures), the same arrow queues the message
/// instead, and Stop shows beside it while Claude works: the message goes once Claude finishes.
struct Composer: View {
    @Binding var text: String
    let isWorking: Bool
    let canSend: Bool
    var action: ComposerAction = .send
    var queueEnabled = false
    /// VoiceOver focus on the message field (a lost edit's text lands there).
    var fieldFocus: AccessibilityFocusState<Bool>.Binding
    let send: () -> Void
    var queue: () -> Void = {}
    let stop: () -> Void

    private var isEmpty: Bool { text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    private var queues: Bool { queueEnabled && action == .queue }

    var body: some View {
        HStack(alignment: .bottom, spacing: 8) {
            TextField("Message", text: $text, axis: .vertical)
                .lineLimit(1...5)
                .padding(.horizontal, 16)
                .padding(.vertical, 11)
                .background(Color(.secondarySystemBackground),
                            in: RoundedRectangle(cornerRadius: 22, style: .continuous))
                .accessibilityFocused(fieldFocus)
                .accessibilityIdentifier("messageField")
            if isWorking {
                Button(action: stop) { Image(systemName: "stop.fill").font(.footnote) }
                    .buttonStyle(RoundActionStyle())
                    .accessibilityLabel("Stop")
                    .accessibilityIdentifier("stopButton")
            }
            if queues || !isWorking {
                Button(action: queues ? queue : send) {
                    Image(systemName: "arrow.up").font(.body.weight(.semibold))
                }
                .buttonStyle(RoundActionStyle())
                .disabled(isEmpty || !canSend)
                .accessibilityLabel(queues ? "Send when Claude finishes" : "Send")
                .accessibilityIdentifier(queues ? "queueButton" : "sendButton")
            }
        }
    }
}

/// A black round button (white in dark mode), like the mockup's send and stop.
struct RoundActionStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .foregroundStyle(Color(.systemBackground))
            // The glyph stops growing at accessibility sizes so it stays inside the 44 pt circle.
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .frame(width: 44, height: 44)
            .background(Color.primary, in: Circle())
            .opacity(isEnabled ? (configuration.isPressed ? 0.6 : 1) : 0.25)
            .contentShape(Circle())
    }
}

/// Replaces the composer for a read-only session: open in a terminal on the Mac, or never started.
struct ReadOnlyLine: View {
    let text: String

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "lock")
                .font(.footnote)
                .foregroundStyle(.secondary)
                .accessibilityHidden(true)
            Text(text)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
                .accessibilityIdentifier("readOnlyLine")
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 12)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
    }
}
