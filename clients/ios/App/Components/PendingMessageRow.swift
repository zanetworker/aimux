import SwiftUI
import AimuxKit

/// A message the person sent that the timeline does not show yet: dimmed while on its way, with why it failed and
/// Retry (or, refused busy with the chat queue, Add to queue) once it failed.
struct PendingMessageRow: View {
    let pending: PendingMessage
    let model: ChatModel

    var body: some View {
        VStack(alignment: .trailing, spacing: 6) {
            UserBubble(text: pending.text).opacity(pending.failure == nil ? 0.6 : 1)
            if pending.failure == nil && model.state == .terminal {
                // Changes to "no answer yet" after 2 minutes without the history showing it.
                TimelineView(.periodic(from: .now, by: 15)) { context in
                    Text(terminalPendingText(sentAt: pending.sentAt, now: context.date))
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .accessibilityIdentifier("terminalPending")
                }
            }
            if let failure = pending.failure {
                // Refused busy, with the queue: it is offered to the queue instead of sent again.
                let offersQueue = model.queueEnabled && ChatQueue.sendRefusal(failure) != nil
                HStack(spacing: 8) {
                    Text("Not sent · " + (offersQueue ? "the session is busy" : failure.userMessage.title))
                        .foregroundStyle(.secondary)
                    if offersQueue {
                        Button { Task { await model.queuePending() } } label: {
                            Text(ComposerAction.queue.label)
                                .frame(minHeight: 44)
                                .contentShape(Rectangle())
                        }
                        .fontWeight(.semibold)
                        .buttonStyle(.borderless)
                        .foregroundStyle(.primary)
                        .accessibilityIdentifier("pendingQueueButton")
                    } else if model.canReplaceBox {
                        // Refused for text in the Mac's input box: the person may replace it.
                        Button("Replace it") { Task { await model.send(pending.text, replacing: true) } }
                            .fontWeight(.semibold)
                            .buttonStyle(.borderless)
                            .foregroundStyle(.primary)
                            .accessibilityHint("Clears the text in the box on your Mac, then sends this message")
                            .accessibilityIdentifier("pendingReplaceButton")
                    } else {
                        Button("Retry") { Task { await model.send(pending.text) } }
                            .disabled(!model.canRetryPending)
                            .fontWeight(.semibold)
                            .buttonStyle(.borderless)
                            .foregroundStyle(.primary)
                    }
                }
                .font(.footnote)
            }
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}
