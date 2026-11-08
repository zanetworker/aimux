import SwiftUI
import AimuxKit

/// "Latest": jumps to the newest content when it is out of view. Near-monochrome, floating over the timeline with
/// the composer's surface and a hairline, so it never reads as part of a reply.
struct LatestButton: View {
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Text("Latest")
                .font(.footnote.weight(.semibold))
                .foregroundStyle(.primary)
                .padding(.horizontal, 14)
                .padding(.vertical, 7)
                .background(Color(.secondarySystemBackground), in: Capsule())
                .overlay(Capsule().stroke(Color(.separator), lineWidth: 0.5))
                .shadow(color: .black.opacity(0.12), radius: 8, y: 3)
                .frame(minWidth: 44, minHeight: 44)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Jump to latest")
        .accessibilityIdentifier("jumpToLatest")
    }
}

/// The last read of a terminal session failed: what is shown is from the read before. Retry reads again.
struct RefreshFailedLine: View {
    let failure: APIError
    let retry: () -> Void

    var body: some View {
        HStack(spacing: 8) {
            Text("Couldn't refresh · " + failure.userMessage.title)
                .font(.caption)
                .foregroundStyle(.secondary)
                .lineLimit(2)
            Button("Retry", action: retry)
                .font(.caption.weight(.semibold))
                .foregroundStyle(.primary)
                .frame(minWidth: 44, minHeight: 44)
                .accessibilityIdentifier("refreshRetry")
        }
        .frame(maxWidth: .infinity)
        .background(Color(.systemBackground))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("refreshFailed")
    }
}

/// Under the chat's header: the stream is reconnecting, or a terminal read failed.
struct ChatTopLine: View {
    let model: ChatModel

    var body: some View {
        if model.state == .reconnecting {
            Text("Reconnecting…")
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity)
                .padding(.vertical, 4)
                .background(Color(.systemBackground))
                .accessibilityIdentifier("reconnecting")
        } else if model.state == .terminal, let failure = model.refreshFailure {
            RefreshFailedLine(failure: failure) { Task { await model.retryRefresh() } }
        }
    }
}
