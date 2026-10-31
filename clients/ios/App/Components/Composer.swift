import SwiftUI
import AimuxKit

/// The message field and send; send becomes stop while Claude works. With the chat queue (`queueEnabled`), the
/// main action is Send or Add to queue (`action`, from the shared fixtures), Stop shows beside Add to queue while
/// Claude works, and a smaller Add to queue button sits beside Send so several messages can be lined up.
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

    /// Below this the field is too narrow to type in, so Add to queue becomes an icon.
    private static let minFieldWidth: CGFloat = 200
    /// About the width of the Add to queue capsule at this text size.
    @ScaledMetric(relativeTo: .subheadline) private var capsuleWidth: CGFloat = 124
    @State private var width: CGFloat = 0

    /// Whether the capsule's words fit beside a field at least `minFieldWidth` wide (and Stop, while working).
    private var wordsFit: Bool {
        width - capsuleWidth - (isWorking ? 52 : 0) - 8 >= Self.minFieldWidth
    }

    private var isEmpty: Bool { text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

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
            if queueEnabled && action == .queue { queueButton(words: wordsFit) } else { sendButtons }
        }
        .onGeometryChange(for: CGFloat.self, of: { $0.size.width }, action: { width = $0 })
    }

    /// Add to queue as the main action: the words, or an icon where they do not fit.
    @ViewBuilder private func queueButton(words: Bool) -> some View {
        if words {
            Button(action: queue) { Text(ComposerAction.queue.label).font(.subheadline.weight(.semibold)) }
                .buttonStyle(CapsuleActionStyle())
                .disabled(isEmpty || !canSend)
                .accessibilityIdentifier("queueButton")
        } else {
            Button(action: queue) { Image(systemName: "text.badge.plus").font(.body.weight(.semibold)) }
                .buttonStyle(RoundActionStyle())
                .disabled(isEmpty || !canSend)
                .accessibilityLabel(ComposerAction.queue.label)
                .accessibilityIdentifier("queueButton")
        }
    }

    @ViewBuilder private var sendButtons: some View {
        if !isWorking {
            if queueEnabled {
                Button(action: queue) { Image(systemName: "text.badge.plus").font(.body) }
                    .buttonStyle(OutlineActionStyle())
                    .disabled(isEmpty || !canSend)
                    .accessibilityLabel(ComposerAction.queue.label)
                    .accessibilityIdentifier("queueAddButton")
            }
            Button(action: send) { Image(systemName: "arrow.up").font(.body.weight(.semibold)) }
                .buttonStyle(RoundActionStyle())
                .disabled(isEmpty || !canSend)
                .accessibilityLabel("Send")
                .accessibilityIdentifier("sendButton")
        }
    }
}

/// The composer's Add to queue as its main action: a black capsule (white in dark mode) with the words.
struct CapsuleActionStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .foregroundStyle(Color(.systemBackground))
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .lineLimit(1)
            .fixedSize()
            .padding(.horizontal, 16)
            .frame(minHeight: 44)
            .background(Color.primary, in: Capsule())
            .opacity(isEnabled ? (configuration.isPressed ? 0.6 : 1) : 0.25)
            .contentShape(Capsule())
    }
}

/// A secondary round button: an outline in the text colour, beside the main one.
struct OutlineActionStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .foregroundStyle(Color.primary)
            .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
            .frame(width: 44, height: 44)
            .overlay(Circle().strokeBorder(Color.primary.opacity(0.35), lineWidth: 1))
            .opacity(isEnabled ? (configuration.isPressed ? 0.6 : 1) : 0.3)
            .contentShape(Circle())
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
