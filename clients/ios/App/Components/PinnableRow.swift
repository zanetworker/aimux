import SwiftUI
import AimuxKit

/// A Now row that opens its session, with Pin or Unpin on a long press (and as a VoiceOver action) when the
/// server can pin, and why the last pin failed under it.
struct PinnableRow<Label: View>: View {
    let session: Session
    let model: NowModel
    let open: () -> Void
    @ViewBuilder let label: Label

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Button(action: open) { label }
                .buttonStyle(.plain)
                .modifier(PinMenu(session: session, model: model))
                .accessibilityIdentifier("session-\(session.id)")
            if let error = model.pinErrors[session.id] {
                Text(error)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .padding(.leading, 17)
            }
        }
    }
}

/// Pin or Unpin on a long press, and as a VoiceOver action, when the server can pin; the result is announced.
struct PinMenu: ViewModifier {
    let session: Session
    let model: NowModel

    func body(content: Content) -> some View {
        if model.canDescribe {
            let label = session.pinned ? "Unpin" : "Pin"
            content
                .contextMenu {
                    Button(label, systemImage: session.pinned ? "pin.slash" : "pin") { toggle() }
                }
                .accessibilityAction(named: label) { toggle() }
        } else {
            content
        }
    }

    private func toggle() {
        let pinned = !session.pinned
        let title = session.displayTitle
        let id = session.id
        Task {
            // A pin already on its way: nothing was sent, so nothing to announce.
            guard await model.setPinned(session, pinned) else { return }
            let text = model.pinErrors[id] ?? (pinned ? "Pinned " : "Unpinned ") + title
            AccessibilityNotification.Announcement(text).post()
        }
    }
}
