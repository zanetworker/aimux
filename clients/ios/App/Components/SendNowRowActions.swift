import SwiftUI
import AimuxKit

/// Send now on a queued row that offers it: a swipe from the leading edge, and touch and hold for a menu.
/// Other rows keep the plain hold-to-reorder.
struct SendNowRowActions: ViewModifier {
    let sendNow: (() -> Void)?

    func body(content: Content) -> some View {
        if let sendNow {
            content
                .swipeActions(edge: .leading, allowsFullSwipe: true) {
                    Button(ChatQueue.sendNowLabel, systemImage: "bolt", action: sendNow)
                        .tint(.primary)
                        .accessibilityIdentifier("queueSwipeSendNow")
                }
                .contextMenu {
                    Button(ChatQueue.sendNowLabel, systemImage: "bolt", action: sendNow)
                        .accessibilityIdentifier("queueMenuSendNow")
                }
        } else {
            content
        }
    }
}
