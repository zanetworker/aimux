import SwiftUI
import AimuxKit

/// One session's chat: owns its `ChatModel` (made on first appear), follows the session while visible,
/// and keeps Now's ranking fresh for the Next bar.
struct ChatView: View {
    let session: Session
    let context: ChatContext
    /// Opens another session in place of this one.
    let goTo: (Session) -> Void

    @State private var model: ChatModel?
    @State private var isClosed = false

    private static let nowRefresh: Duration = .seconds(15)

    var body: some View {
        Group {
            if let model {
                ChatContent(session: session, context: context, model: model, goTo: goTo)
            } else {
                Color(.systemBackground).toolbar(.hidden, for: .tabBar)
            }
        }
        .task {
            // First appear, or back after leaving (another tab, the chat after this one): a closed model
            // ignores load(), so follow the session again with a new one.
            if model == nil || isClosed {
                model = context.makeModel(session.id)
                isClosed = false
            }
            model?.queueEnabled = context.queueEnabled
            await Interaction.perform { await model?.load() }
        }
        .task {
            // Now stops polling while a chat covers it; the chat refreshes it, as often as Now does.
            while !Task.isCancelled {
                await context.now.refresh()
                do { try await Task.sleep(for: Self.nowRefresh) } catch { break }
            }
        }
        .onChange(of: context.queueEnabled) { _, enabled in model?.queueEnabled = enabled }
        .onDisappear {
            model?.close()
            isClosed = true
        }
    }
}
