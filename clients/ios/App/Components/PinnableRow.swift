import SwiftUI
import AimuxKit

/// A Now row that opens its session, with Pin or Unpin and Open on Mac on a long press (and as VoiceOver actions)
/// when the server offers them, and why the last pin or open failed under it.
struct PinnableRow<Label: View>: View {
    let session: Session
    let model: NowModel
    let openOnMac: OpenOnMacModel
    let open: () -> Void
    @ViewBuilder let label: Label

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Button(action: open) { label }
                .buttonStyle(.plain)
                .modifier(PinMenu(session: session, model: model, openOnMac: openOnMac))
                .accessibilityIdentifier("session-\(session.id)")
            ForEach([model.pinErrors[session.id], openOnMac.errors[session.id]].compactMap { $0 }, id: \.self) {
                Text($0)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .padding(.leading, 17)
            }
        }
    }
}

/// Pin or Unpin when the server can pin, and Open on Mac when it can open the session in a terminal, on a long
/// press and as VoiceOver actions; the result is announced.
struct PinMenu: ViewModifier {
    let session: Session
    let model: NowModel
    let openOnMac: OpenOnMacModel

    func body(content: Content) -> some View {
        let canPin = model.canDescribe
        let canOpen = canOpenOnMac(session, health: model.health)
        if canPin || canOpen {
            let label = session.pinned ? "Unpin" : "Pin"
            content
                .contextMenu {
                    if canPin {
                        Button(label, systemImage: session.pinned ? "pin.slash" : "pin") { toggle() }
                    }
                    if canOpen {
                        OpenOnMacButton(session: session, now: model, openOnMac: openOnMac)
                    }
                }
                .modifier(NamedAction(name: label, isOffered: canPin) { toggle() })
                .modifier(NamedAction(name: OpenOnMacCopy.action, isOffered: canOpen) {
                    OpenOnMacButton.open(session, now: model, openOnMac: openOnMac)
                })
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

/// Open on Mac for a row's session, disabled while an open for it (from a row or its chat) is on its way; on
/// success Now reads the list again, so the session moves under the Mac's windows.
struct OpenOnMacButton: View {
    let session: Session
    let now: NowModel
    let openOnMac: OpenOnMacModel

    var body: some View {
        Button(OpenOnMacCopy.action, systemImage: "macwindow") { Self.open(session, now: now, openOnMac: openOnMac) }
            .disabled(openOnMac.opening.contains(session.id))
    }

    @MainActor static func open(_ session: Session, now: NowModel, openOnMac: OpenOnMacModel) {
        let id = session.id
        Task {
            guard let opened = await openOnMac.open(id) else { return }
            let text = opened ? OpenOnMacCopy.texts["done"] ?? "" : openOnMac.errors[id] ?? ""
            AccessibilityNotification.Announcement(text).post()
            if opened { await now.refresh() }
        }
    }
}

/// A named VoiceOver action, only when it is offered.
struct NamedAction: ViewModifier {
    let name: String
    let isOffered: Bool
    let action: () -> Void

    func body(content: Content) -> some View {
        if isOffered {
            content.accessibilityAction(named: name, action)
        } else {
            content
        }
    }
}
