import SwiftUI
import UIKit
import AimuxKit

// The block tree (AimuxKit's markdownBlocks, after linkReferences) as SwiftUI. Near-monochrome: links and
// references take the link tint, everything else is text colours and hairlines. Code always reads left to right;
// paragraphs, headings and list items take their direction from their own text, so Arabic reads right to left.

extension EnvironmentValues {
    /// What reference links in the chat point at: the open session's repository and the server's Jira.
    @Entry var referenceContext: ReferenceContext = .none
    /// How deep the list being drawn is nested, for its bullet.
    @Entry var markdownListDepth = 0
    /// How many quotes and list items the block being drawn sits in, for the indent cap.
    @Entry var markdownNesting = 0
}

/// Quotes and lists indent for this many levels; deeper ones line up with the last indented level.
let maxVisualIndent = 8

/// Blocks, one under the other; links open in the browser only for http and https.
struct MarkdownView: View {
    let blocks: [MDBlock]

    var body: some View {
        MarkdownBlockList(blocks: blocks, spacing: 12)
            .tint(Color(.link))
            .environment(\.openURL, OpenURLAction { url in
                isWebLink(url.absoluteString) ? .systemAction : .discarded
            })
    }
}

/// Whether a link may open: http and https only.
func isWebLink(_ href: String) -> Bool {
    let lower = href.lowercased()
    return lower.hasPrefix("http:") || lower.hasPrefix("https:")
}

struct MarkdownBlockList: View {
    let blocks: [MDBlock]
    var spacing: CGFloat = 12

    var body: some View {
        VStack(alignment: .leading, spacing: spacing) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { index, block in
                MarkdownBlockView(block: block, isFirst: index == 0)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct MarkdownBlockView: View {
    let block: MDBlock
    let isFirst: Bool

    var body: some View {
        switch block {
        case .heading(let level, let inlines):
            InlineTextView(inlines: inlines, font: Self.headingFont(level), codeFont: nil)
                .foregroundStyle(level == 6 ? .secondary : .primary)
                .padding(.top, isFirst ? 0 : (level <= 2 ? 10 : 6))
                .accessibilityAddTraits(.isHeader)
        case .paragraph(let inlines):
            InlineTextView(inlines: inlines, font: .body)
        case .code(let lang, let text):
            CodeBlockView(lang: lang, text: text)
        case .list(let list):
            MarkdownListView(list: list)
        case .quote(let blocks):
            QuoteView(blocks: blocks)
        case .table(let table):
            MarkdownTableView(table: table)
        case .rule:
            Rectangle().fill(Color(.separator)).frame(height: 1).padding(.vertical, 6)
        case .media(let media):
            MediaCardView(media: media)
        }
    }

    static func headingFont(_ level: Int) -> Font {
        switch level {
        case 1: .title2.weight(.bold)
        case 2: .title3.weight(.semibold)
        case 3: .headline
        case 4: .callout.weight(.semibold)
        default: .subheadline.weight(.semibold)
        }
    }
}

/// A paragraph or heading: one `Text`, laid out in its own text's direction.
struct InlineTextView: View {
    let inlines: [MDInline]
    let font: Font
    var codeFont: Font? = InlineStyle.bodyCode

    var body: some View {
        let rtl = isRightToLeft(inlines)
        Text(InlineStyle.attributed(inlines, codeFont: codeFont))
            .font(font)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
            .environment(\.layoutDirection, rtl ? .rightToLeft : .leftToRight)
    }
}

/// Inlines to an `AttributedString`: emphasis as presentation intents (so they keep the Dynamic Type font), code
/// spans in monospace on a faint fill, links and references tappable in the tint.
enum InlineStyle {
    static let codeFill = CodeColors.inlineFill

    /// The monospace for code spans in body text: a step smaller, since monospace runs wide and tall.
    static let bodyCode = Font.system(.callout, design: .monospaced)

    /// `codeFont` sizes code spans; nil keeps the surrounding font (headings).
    static func attributed(_ inlines: [MDInline], codeFont: Font? = bodyCode, codeFill: Color = codeFill,
                           codeInk: Color = CodeColors.inlineInk, underlineLinks: Bool = false) -> AttributedString {
        var out = AttributedString()
        let style = Style(codeFont: codeFont, codeFill: codeFill, codeInk: codeInk, underlineLinks: underlineLinks)
        append(inlines, intent: [], to: &out, style: style)
        return out
    }

    private struct Style {
        let codeFont: Font?
        let codeFill: Color
        let codeInk: Color
        let underlineLinks: Bool
    }

    private static func append(_ inlines: [MDInline], intent: InlinePresentationIntent,
                               to out: inout AttributedString, style: Style) {
        for inline in inlines {
            switch inline {
            case .text(let value): out += piece(value, intent: intent)
            case .code(let value): out += code(value, intent: intent, style: style)
            case .br: out += piece("\n", intent: intent)
            case .em(let inner): append(inner, intent: intent.union(.emphasized), to: &out, style: style)
            case .strong(let inner): append(inner, intent: intent.union(.stronglyEmphasized), to: &out, style: style)
            case .del(let inner): append(inner, intent: intent.union(.strikethrough), to: &out, style: style)
            case .link(let href, let content):
                var text = AttributedString()
                append(content, intent: intent, to: &text, style: style)
                out += linked(text, href: href, style: style)
            case .ref(_, let value, let href, let isCode):
                let text = isCode ? code(value, intent: intent, style: style) : piece(value, intent: intent)
                out += linked(text, href: href, style: style)
            }
        }
    }

    private static func piece(_ text: String, intent: InlinePresentationIntent) -> AttributedString {
        var piece = AttributedString(text)
        if !intent.isEmpty { piece.inlinePresentationIntent = intent }
        return piece
    }

    private static func code(_ text: String, intent: InlinePresentationIntent, style: Style) -> AttributedString {
        var piece = AttributedString(text)
        piece.inlinePresentationIntent = intent.union(.code)
        piece.backgroundColor = style.codeFill
        piece.foregroundColor = style.codeInk
        if let font = style.codeFont { piece.font = font }
        return piece
    }

    private static func linked(_ text: AttributedString, href: String, style: Style) -> AttributedString {
        guard isWebLink(href), let url = URL(string: href) else { return text }
        var text = text
        text.link = url
        text.foregroundColor = nil  // a link (a path, a commit) keeps the link colour, as on the web
        if style.underlineLinks { text.underlineStyle = .single }
        return text
    }
}

/// A fenced code block: the language and a Copy button over the code, which scrolls sideways and never wraps.
struct CodeBlockView: View {
    let lang: String?
    let text: String
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text(lang ?? "text")
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                Spacer(minLength: 12)
                copyButton
            }
            .padding(.leading, 14)
            .padding(.trailing, 6)
            .padding(.vertical, 2)
            Rectangle().fill(Color(.separator)).frame(height: 1 / 3)
            ScrollView(.horizontal) {
                Text(CodeColors.attributed(text, lang: lang))
                    .font(.system(.footnote, design: .monospaced))
                    .fixedSize()
                    .padding(.horizontal, 14)
                    .padding(.vertical, 12)
            }
            .scrollIndicators(.hidden)
        }
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .environment(\.layoutDirection, .leftToRight)
    }

    private var copyButton: some View {
        Button {
            UIPasteboard.general.string = text
            withAnimation(.snappy) { copied = true }
            Task {
                try? await Task.sleep(for: .seconds(1.5))
                withAnimation(.snappy) { copied = false }
            }
        } label: {
            Label(copied ? "Copied" : "Copy", systemImage: copied ? "checkmark" : "doc.on.doc")
                .font(.caption.weight(.medium))
                .foregroundStyle(.secondary)
                .contentTransition(.symbolEffect(.replace))
                .padding(.horizontal, 8)
                .padding(.vertical, 8)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(copied ? "Copied" : "Copy code")
    }
}

/// A quote: secondary text beside a left rule (no further indent past `maxVisualIndent` levels).
struct QuoteView: View {
    let blocks: [MDBlock]
    @Environment(\.markdownNesting) private var nesting

    var body: some View {
        let indented = nesting < maxVisualIndent
        MarkdownBlockList(blocks: blocks, spacing: 10)
            .environment(\.markdownNesting, nesting + 1)
            .foregroundStyle(.secondary)
            .padding(.leading, indented ? 14 : 0)
            .overlay(alignment: .leading) {
                if indented { RoundedRectangle(cornerRadius: 1.5).fill(Color(.separator)).frame(width: 3) }
            }
    }
}

/// Bullets, numbers or task boxes beside each item's blocks; a nested list indents under its item's text.
struct MarkdownListView: View {
    let list: MDList
    @Environment(\.markdownListDepth) private var depth
    @Environment(\.markdownNesting) private var nesting

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(Array(list.items.enumerated()), id: \.offset) { index, item in
                Group {
                    if nesting < maxVisualIndent {
                        HStack(alignment: .firstTextBaseline, spacing: 8) {
                            marker(index, item)
                            MarkdownBlockList(blocks: item.blocks, spacing: 6)
                        }
                    } else {
                        // Too deep to indent: the marker leads the item's text instead.
                        MarkdownBlockList(blocks: inlineMarker(index, item), spacing: 6)
                    }
                }
                .environment(\.markdownListDepth, depth + 1)
                .environment(\.markdownNesting, nesting + 1)
                .environment(\.layoutDirection, itemDirection(item))
            }
        }
        .padding(.leading, depth == 0 ? 2 : 0)
    }

    private func inlineMarker(_ index: Int, _ item: MDListItem) -> [MDBlock] {
        let task = item.task.map { $0 ? "☑ " : "☐ " } ?? ""
        let marker = (list.ordered ? "\((list.start ?? 1) + index). " : "• ") + task
        guard case .paragraph(let inlines)? = item.blocks.first else {
            return [.paragraph([.text(marker)])] + item.blocks
        }
        return [.paragraph([.text(marker)] + inlines)] + item.blocks.dropFirst()
    }

    @ViewBuilder private func marker(_ index: Int, _ item: MDListItem) -> some View {
        if let done = item.task {
            Text(Image(systemName: done ? "checkmark.square.fill" : "square"))
                .foregroundStyle(done ? .primary : .secondary)
                .accessibilityLabel(done ? "Done" : "Not done")
        } else if list.ordered {
            let start = list.start ?? 1
            ZStack(alignment: .trailing) {
                Text("\(start + list.items.count - 1).").hidden()
                Text("\(start + index).").foregroundStyle(.secondary)
            }
            .monospacedDigit()
        } else {
            Text(depth == 0 ? "•" : depth == 1 ? "◦" : "▪")
                .foregroundStyle(.secondary)
                .frame(minWidth: 12)
        }
    }

    private func itemDirection(_ item: MDListItem) -> LayoutDirection {
        guard case .paragraph(let inlines)? = item.blocks.first else { return .leftToRight }
        return isRightToLeft(inlines) ? .rightToLeft : .leftToRight
    }
}
