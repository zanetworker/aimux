import SwiftUI
import AimuxKit

/// A folder row: a working project's sessions under one heading. The folder icon, the name in semibold, the
/// count and a chevron; collapsed, the newest session's title follows the name.
struct ProjectGroupRow: View {
    let group: ProjectGroup
    let isExpanded: Bool
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let stacked = typeSize.isAccessibilitySize
        HStack(alignment: stacked ? .firstTextBaseline : .center, spacing: 10) {
            FolderIcon()
            // At accessibility text sizes the count and chevron move under the name, so the name keeps the width.
            TimeLayout(stacked: stacked) {
                nameLine
                    .lineLimit(stacked ? 3 : 1)
                .frame(maxWidth: .infinity, alignment: .leading)
                HStack(spacing: 10) {
                    Text("\(group.count)").foregroundStyle(.secondary).monospacedDigit()
                    Image(systemName: "chevron.right")
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(isExpanded ? 90 : 0))
                }
            }
        }
        .padding(.top, 4)
        .contentShape(Rectangle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(isolated(group.name) + ", " + (group.count == 1 ? "1 session" : "\(group.count) sessions"))
        .accessibilityValue(isExpanded ? "Expanded" : "Collapsed")
        .accessibilityHint(isExpanded ? "Collapses the folder" : "Expands the folder")
        .accessibilityAddTraits(.isHeader)
    }

    /// The name; collapsed, the newest session's title after it. One text, so it wraps as a line.
    private var nameLine: Text {
        let name = Text(bidi: isolated(group.name)).font(.body.weight(.semibold))
        guard !isExpanded else { return name }
        let newest = Text(bidi: " · " + isolated(group.newest.session.displayTitle))
            .font(.subheadline)
            .foregroundStyle(.secondary)
        return Text("\(name)\(newest)")
    }
}

/// The folder icon in place of a status dot.
struct FolderIcon: View {
    var body: some View {
        Image(systemName: "folder")
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .frame(width: FolderLayout.iconWidth)
            .accessibilityHidden(true)
    }
}

enum FolderLayout {
    static let iconWidth: CGFloat = 20
}

/// A folder of one session: the folder icon, "name · title", its description, the live step and the time;
/// it opens the session.
struct SingleFolderRow: View {
    let group: ProjectGroup
    let format: SessionRowFormat
    /// The live step line; off for On your Mac, whose sessions are not working.
    var showsStep = true
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let item = group.newest
        let stacked = typeSize.isAccessibilitySize
        HStack(alignment: stacked ? .firstTextBaseline : .center, spacing: 10) {
            FolderIcon()
            TimeLayout(stacked: stacked) {
                VStack(alignment: .leading, spacing: 2) {
                    // One text, so it wraps as a line at large sizes; read as "trips, Book the lake cabin".
                    let name = Text(bidi: isolated(group.name)).fontWeight(.semibold)
                    let separator = Text(" · ").foregroundStyle(.secondary)
                    Text("\(name)\(separator)\(Text(bidi: isolated(item.session.displayTitle)))")
                        .lineLimit(stacked ? 3 : 1)
                        .accessibilityLabel(isolated(group.name) + ", " + isolated(item.session.displayTitle))
                    if let description = item.session.descriptionLine {
                        DescriptionLine(text: description, stacked: stacked)
                    }
                    if showsStep {
                        StepLine(activity: item.activity, format: format)
                            .font(.subheadline)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                RowTime(date: item.session.lastActive, format: format)
            }
        }
        .padding(.top, 4)
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

/// A row inside an open folder: indented under a thin guide line that runs from the folder's icon.
struct FolderItem<Content: View>: View {
    @ViewBuilder let content: Content

    var body: some View {
        HStack(spacing: 0) {
            Rectangle()
                .fill(Color(.separator))
                .frame(width: 1)
                .padding(.leading, FolderLayout.iconWidth / 2)
                .padding(.trailing, FolderLayout.iconWidth / 2 + 6)
                .accessibilityHidden(true)
            content.padding(.vertical, 10)
        }
    }
}
