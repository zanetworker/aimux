import SwiftUI
import AimuxKit

/// The signature element: the waiting request in large type, its answers as buttons on the card.
struct ApprovalCard: View {
    let session: Session
    let approval: Approval
    let isAnswering: Bool
    let error: APIError?
    let format: SessionRowFormat
    let answer: (PermissionOption) -> Void
    let open: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        // At accessibility text sizes the project, time and Open move under what they sat beside.
        let stacked = typeSize.isAccessibilitySize
        let under = AnyLayout(VStackLayout(alignment: .leading, spacing: 2))
        let header = stacked ? under : AnyLayout(HStackLayout(alignment: .center, spacing: 6))
        let titleLine = stacked ? under : AnyLayout(HStackLayout(alignment: .firstTextBaseline))
        VStack(alignment: .leading, spacing: 5) {
            header {
                HStack(spacing: 6) {
                    StatusDot(status: .needsYou)
                    Text("Needs you")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(Color.amber)
                }
                if !stacked { Spacer(minLength: 8) }
                Text(bidi: isolated(format.projectName(session.project)) + " · "
                     + format.relativeTime(session.lastActive))
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(stacked ? 2 : 1)
                    .monospacedDigit()
            }
            titleLine {
                Text(session.displayTitle)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: stacked)
                    .frame(maxWidth: .infinity, alignment: .leading)
                openButton
            }
            Text(approval.title)
                .font(.title3.weight(.semibold))
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
            if let command = approval.command, !command.isEmpty {
                Text(command)
                    .font(.system(.footnote, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .lineLimit(3)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            FlowLayout(spacing: 8) { options }
                .padding(.top, 3)
            if let error {
                Text(error.userMessage.title)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 2)
        .accessibilityIdentifier("approval-\(approval.approvalId)")
    }

    @ViewBuilder private var options: some View {
        let primary = approval.options.first { $0.kind.hasPrefix("allow") }
        ForEach(approval.options, id: \.optionId) { option in
            Button(option.name) { answer(option) }
                .buttonStyle(OptionStyle(kind: option == primary ? .primary : option.kind.hasPrefix("reject")
                                         ? .reject : .plain))
                .disabled(isAnswering)
        }
    }

    private var openButton: some View {
        Button("Open ›", action: open)
            .buttonStyle(.borderless)
            .font(.subheadline)
            .foregroundStyle(.secondary)
    }
}

struct OptionStyle: ButtonStyle {
    enum Kind { case primary, reject, plain }
    let kind: Kind
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.subheadline.weight(.semibold))
            .lineLimit(1)
            .fixedSize()
            .padding(.horizontal, 13)
            .padding(.vertical, 6)
            .foregroundStyle(foreground)
            .background(background, in: Capsule())
            .opacity(isEnabled ? (configuration.isPressed ? 0.6 : 1) : 0.4)
            .contentShape(Capsule())
    }

    private var foreground: Color {
        switch kind {
        case .primary: Color(.systemBackground)
        case .reject: .red
        case .plain: .primary
        }
    }

    private var background: Color {
        kind == .primary ? .primary : Color(.tertiarySystemFill)
    }
}

/// An approval as one row when Now is busy: amber dot, session title, the request on one line, time.
struct ApprovalRow: View {
    let session: Session
    let approval: Approval
    let format: SessionRowFormat

    var body: some View {
        DotRow(status: .needsYou, title: session.displayTitle, date: session.lastActive, format: format) {
            Text(bidi: isolated(approval.title)).foregroundStyle(.primary)
        }
    }
}

/// Places views left to right and starts a new line when the next one does not fit.
struct FlowLayout: Layout {
    let spacing: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = arrange(subviews, width: proposal.width ?? .infinity)
        let height = rows.map(\.height).reduce(0, +) + spacing * CGFloat(max(rows.count - 1, 0))
        let width = rows.map(\.width).max() ?? 0
        return CGSize(width: proposal.width ?? width, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var top = bounds.minY
        for row in arrange(subviews, width: bounds.width) {
            var left = bounds.minX
            for index in row.indices {
                let size = subviews[index].sizeThatFits(.unspecified)
                subviews[index].place(at: CGPoint(x: left, y: top + (row.height - size.height) / 2),
                                      proposal: ProposedViewSize(size))
                left += size.width + spacing
            }
            top += row.height + spacing
        }
    }

    private struct Row { var indices: [Int] = []; var width: CGFloat = 0; var height: CGFloat = 0 }

    private func arrange(_ subviews: Subviews, width: CGFloat) -> [Row] {
        var rows = [Row()]
        for index in subviews.indices {
            let size = subviews[index].sizeThatFits(.unspecified)
            let current = rows[rows.count - 1]
            if !current.indices.isEmpty && current.width + spacing + size.width > width {
                rows.append(Row())
            }
            var row = rows[rows.count - 1]
            row.width = row.indices.isEmpty ? size.width : row.width + spacing + size.width
            row.height = max(row.height, size.height)
            row.indices.append(index)
            rows[rows.count - 1] = row
        }
        return rows
    }
}
