import SwiftUI
import AimuxKit

/// A table on a `Grid` with hairline borders and an emphasised header row. It fills the width when it fits, and
/// scrolls sideways when it does not; a short cell stays on one line, a long one wraps at a readable width.
struct MarkdownTableView: View {
    let table: MDTable
    @Environment(\.displayScale) private var displayScale
    /// MarkdownView's action: opens http and https only.
    @Environment(\.openURL) private var openURL
    @ScaledMetric(relativeTo: .subheadline) private var wrapWidth: CGFloat = 220

    private var hairline: CGFloat { 1 / displayScale }

    var body: some View {
        ViewThatFits(in: .horizontal) {
            grid
            ScrollView(.horizontal) { grid }
                .scrollIndicators(.visible)
        }
        .environment(\.layoutDirection, .leftToRight)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Table, \(table.rows.count) rows")
        .accessibilityChildren {
            // One element per row, each cell read with its column's header.
            ForEach(Array(table.rows.enumerated()), id: \.offset) { _, row in
                // The row's links and references stay reachable: one action each, through the http(s)-only openURL.
                Text(rowLabel(row))
                    .accessibilityActions {
                        ForEach(Array(webLinks(in: row).enumerated()), id: \.offset) { _, link in
                            Button("Open \(link.text)") {
                                if let url = URL(string: link.href) { openURL(url) }
                            }
                        }
                    }
            }
        }
    }

    /// "Region: eu-west, Cluster: edge-1, ..."
    private func rowLabel(_ row: [[MDInline]]) -> String {
        row.enumerated().map { column, cell in
            let header = column < table.header.count ? plainText(table.header[column]) : ""
            return header.isEmpty ? plainText(cell) : header + ": " + plainText(cell)
        }.joined(separator: ", ")
    }

    private var grid: some View {
        Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
            GridRow {
                ForEach(Array(table.header.enumerated()), id: \.offset) { column, cell in
                    self.cell(cell, column: column, header: true)
                }
            }
            .background(Color(.secondarySystemBackground))
            ForEach(Array(table.rows.enumerated()), id: \.offset) { _, row in
                Rectangle().fill(Color(.separator)).frame(height: hairline)
                GridRow {
                    ForEach(Array(row.enumerated()), id: \.offset) { column, cell in
                        self.cell(cell, column: column, header: false)
                    }
                }
            }
        }
        // Rows at their tallest cell: offered a height (the sideways scroll offers its own), the grid would share
        // it among its rows, whose cells all stretch, and squeeze some rows to give others room.
        .fixedSize(horizontal: false, vertical: true)
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: 10, style: .continuous)
                .strokeBorder(Color(.separator), lineWidth: hairline)
        }
    }

    private func cell(_ inlines: [MDInline], column: Int, header: Bool) -> some View {
        let align = column < table.align.count ? table.align[column] : .left
        let short = plainLength(inlines) <= 32
        let text = Text(InlineStyle.attributed(inlines, codeFont: .system(.footnote, design: .monospaced)))
            .font(header ? .subheadline.weight(.semibold) : .subheadline)
            .multilineTextAlignment(align == .right ? .trailing : align == .center ? .center : .leading)
            .environment(\.layoutDirection, isRightToLeft(inlines) ? .rightToLeft : .leftToRight)
        return Group {
            if short {
                text.fixedSize()
            } else {
                WrappedCell(maxWidth: wrapWidth) { text }
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 9)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: frameAlignment(align))
        .overlay(alignment: .trailing) {
            if column < table.align.count - 1 {
                Rectangle().fill(Color(.separator)).frame(width: hairline)
            }
        }
    }

    private func frameAlignment(_ align: MDAlign) -> Alignment {
        switch align {
        case .left: .leading
        case .center: .center
        case .right: .trailing
        }
    }
}

/// A long cell: as wide as its text on one line, at most `maxWidth`, and as tall as the text wrapped at that width,
/// whatever width it is offered. In the sideways scroll the grid sizes its rows with no width, and a frame with a
/// maximum width then measured the text as one line, so the row stayed one line tall while the text drew wrapped
/// over the rows and paragraphs below.
private struct WrappedCell: Layout {
    let maxWidth: CGFloat

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard let text = subviews.first else { return .zero }
        let width = min(text.sizeThatFits(.unspecified).width, maxWidth, proposal.width ?? .infinity)
        return CGSize(width: width, height: text.sizeThatFits(ProposedViewSize(width: width, height: nil)).height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(at: bounds.origin, proposal: ProposedViewSize(width: bounds.width, height: nil))
    }
}

/// The visible length of inlines, to tell a short cell from prose.
func plainLength(_ inlines: [MDInline]) -> Int {
    inlines.reduce(0) { total, inline in
        switch inline {
        case .text(let value), .code(let value): total + value.count
        case .ref(_, let value, _, _): total + value.count
        case .em(let inner), .strong(let inner), .del(let inner), .link(_, let inner): total + plainLength(inner)
        case .br: total + 1
        }
    }
}

/// The visible text of inlines.
func plainText(_ inlines: [MDInline]) -> String {
    inlines.map { inline in
        switch inline {
        case .text(let value), .code(let value): value
        case .ref(_, let value, _, _): value
        case .em(let inner), .strong(let inner), .del(let inner), .link(_, let inner): plainText(inner)
        case .br: " "
        }
    }.joined()
}
