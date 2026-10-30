import SwiftUI
import AimuxKit

/// What a chat needs from the app: the server, its event stream, Now (for the Next bar), the drafts and
/// failed messages kept per session, the server's Jira links for references, and whether it keeps a chat queue.
struct ChatContext {
    let source: any ChatSource
    let stream: any EventStreaming
    let now: NowModel
    let drafts: ChatDrafts
    let links: HealthLinks?
    var queueEnabled = false

    @MainActor func makeModel(_ sessionID: String) -> ChatModel {
        ChatModel(sessionID: sessionID, source: source, stream: stream, drafts: drafts, queueEnabled: queueEnabled)
    }
}

extension View {
    /// Pushes the chat for a session; its Next bar replaces the open chat with the next one.
    func chatDestination(_ context: ChatContext, path: Binding<[Session]>) -> some View {
        navigationDestination(for: Session.self) { session in
            ChatView(session: session, context: context) { next in
                guard !path.wrappedValue.isEmpty else { return }
                path.wrappedValue[path.wrappedValue.count - 1] = next
            }
            .id(session.id)
        }
    }
}
