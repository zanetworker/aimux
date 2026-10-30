import SwiftUI
import AimuxKit

/// "Next · <title> · your turn ›": the next session that needs you, or the next working one.
struct NextBar: View {
    let item: NowItem
    let action: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    private var state: (status: DotStatus, label: String) {
        switch item.activity {
        case .needsApproval: (.needsYou, "your turn")
        case .failed: (.failed, "failed")
        default: (.working, "working")
        }
    }

    var body: some View {
        Button(action: action) {
            HStack(spacing: 8) {
                StatusDot(status: state.status)
                // One text, so a long title truncates at the end; it wraps at accessibility sizes.
                Text("""
                    \(Text("Next · ").foregroundStyle(.secondary))\(Text(bidi: isolated(item.session.displayTitle)))\
                    \(Text(" · " + state.label).foregroundStyle(.secondary))
                    """)
                .lineLimit(typeSize.isAccessibilitySize ? 3 : 1)
                .frame(maxWidth: .infinity, alignment: .leading)
                Image(systemName: "chevron.right")
                    .font(.footnote.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
            .font(.subheadline)
            .padding(.horizontal, 16)
            .padding(.vertical, 6)
            .frame(minHeight: 44)
            .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 22, style: .continuous))
            .contentShape(RoundedRectangle(cornerRadius: 22, style: .continuous))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Next: \(item.session.displayTitle), \(state.label)")
        .accessibilityIdentifier("nextBar")
    }
}
