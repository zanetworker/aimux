import SwiftUI
import AimuxKit

/// The chat's Open on Mac: a More menu in the toolbar while the server can open the session in a terminal, and
/// above the bottom bar "Opening on your Mac…" while an open (from here or a row) is on its way, else why the
/// last one failed. On success the chat reads the session again, now a terminal chat.
struct OpenOnMacChatActions: ViewModifier {
    let session: Session
    let context: ChatContext
    let model: ChatModel

    private var openOnMac: OpenOnMacModel { context.openOnMac }

    func body(content: Content) -> some View {
        content
            .toolbar {
                if canOpenOnMac(state: model.session?.state ?? session.state, health: context.now.health) {
                    ToolbarItem(placement: .topBarTrailing) { menu }
                }
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                if let line {
                    Text(line)
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 16)
                        .padding(.vertical, 6)
                        .background(Color(.systemBackground))
                        .accessibilityIdentifier("openOnMacLine")
                }
            }
    }

    private var line: String? {
        openOnMac.opening.contains(session.id) ? OpenOnMacCopy.progress : openOnMac.errors[session.id]
    }

    private var menu: some View {
        Menu {
            Button(OpenOnMacCopy.action, systemImage: "macwindow") { open() }
                .disabled(openOnMac.opening.contains(session.id))
        } label: {
            Image(systemName: "ellipsis.circle")
        }
        .accessibilityLabel("More")
        .accessibilityIdentifier("chatMenu")
    }

    private func open() {
        let id = session.id
        Task {
            guard let opened = await openOnMac.open(id) else { return }
            let text = opened ? OpenOnMacCopy.texts["done"] ?? "" : openOnMac.errors[id] ?? ""
            AccessibilityNotification.Announcement(text).post()
            guard opened else { return }
            await Interaction.perform { await model.load() }
            await context.now.refresh()
        }
    }
}
