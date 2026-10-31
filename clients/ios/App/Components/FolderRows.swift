import SwiftUI
import AimuxKit

/// A line that folds what is under it: a tab on Now, a repo or worktree in All's Folders. The chevron, the name,
/// its position ("tab 3") and the counts on the right ("4 panes · 1 working").
struct FoldLine: View {
    let name: String
    var position: String?
    let counts: String
    let isOpen: Bool
    /// False when a search or the Working chip holds it open.
    var canFold = true
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let stacked = typeSize.isAccessibilitySize
        HStack(alignment: .firstTextBaseline, spacing: 0) {
            Image(systemName: "chevron.right")
                .font(.caption.weight(.semibold))
                .foregroundStyle(canFold ? .secondary : .tertiary)
                .rotationEffect(.degrees(isOpen ? 90 : 0))
                .frame(width: FolderLayout.iconWidth)
                .padding(.trailing, 6)
            TimeLayout(stacked: stacked) {
                HStack(alignment: .firstTextBaseline, spacing: 6) {
                    Text(bidi: isolated(name)).font(.body.weight(.semibold)).lineLimit(stacked ? 3 : 1)
                    if let position { Text(position).font(.footnote).foregroundStyle(.secondary).fixedSize() }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                Text(counts)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .monospacedDigit()
                    .lineLimit(1)
                    .fixedSize()
            }
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(([isolated(name)] + [position, counts].compactMap { $0 }).joined(separator: " · "))
        .accessibilityValue(isOpen ? "Expanded" : "Collapsed")
        .accessibilityHint(canFold ? (isOpen ? "Folds it" : "Unfolds it") : "")
        .accessibilityAddTraits(.isButton)
    }
}

enum FolderLayout {
    static let iconWidth: CGFloat = 20
}

/// A row inside an open fold: indented under a thin guide line per level (a worktree's rows sit two deep).
struct FolderItem<Content: View>: View {
    var depth = 1
    @ViewBuilder let content: Content

    var body: some View {
        HStack(spacing: 0) {
            ForEach(0..<depth, id: \.self) { _ in
                Rectangle()
                    .fill(Color(.separator))
                    .frame(width: 1)
                    .padding(.leading, FolderLayout.iconWidth / 2)
                    .padding(.trailing, FolderLayout.iconWidth / 2 + 6)
                    .accessibilityHidden(true)
            }
            content.padding(.vertical, 10)
        }
    }
}
