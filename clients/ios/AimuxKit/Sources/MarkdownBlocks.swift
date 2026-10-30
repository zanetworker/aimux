import Foundation

// Markdown text to the block tree both clients render (api/testdata/clients/README.md, "Markdown"). Foundation
// parses (`AttributedString(markdown:)`, full syntax); this file rebuilds the blocks from the runs' presentation
// intents and fills in what Foundation leaves out (empty table cells and list items, task markers, HTML blocks as
// literal lines, the first word of a fence's info string). MarkdownInlines.swift normalizes the inlines.

/// Quotes and lists nest at most this deep; deeper containers are flattened into the deepest one (README: bounded,
/// not pinned beyond it).
public let maxMarkdownDepth = 100

/// Text longer than this (UTF-16 units, as the web client counts) is shown as plain preformatted text, unparsed:
/// a streaming reply is parsed again on every update.
public let maxFormattedCharacters = 256 * 1024

/// Whether to parse the text as Markdown, or show it as is.
public func formatsAsMarkdown(_ text: String) -> Bool { text.utf16.count <= maxFormattedCharacters }

/// Markdown text to the shared block tree. With `breaks`, a soft line break is a `.br` (a user's message keeps its
/// lines as typed); otherwise it is a space, as the fixtures pin.
public func markdownBlocks(_ src: String, breaks: Bool = false) -> [MDBlock] {
    guard !src.isEmpty else { return [] }
    let options = AttributedString.MarkdownParsingOptions(
        allowsExtendedAttributes: false, interpretedSyntax: .full, failurePolicy: .returnPartiallyParsedIfPossible)
    guard let parsed = try? AttributedString(markdown: src, options: options) else {
        return literalLines(src, breaks: breaks)
    }
    let builder = BlockTreeBuilder()
    for run in parsed.runs {
        builder.add(InlineRun(run, text: String(parsed[run.range].characters)), intent: run.presentationIntent)
    }
    return builder.root.blocks(breaks: breaks)
}

/// Text Foundation keeps as raw HTML (or could not parse): one paragraph per non-blank line, its inlines parsed
/// but no tag interpreted.
func literalLines(_ text: String, breaks: Bool) -> [MDBlock] {
    let options = AttributedString.MarkdownParsingOptions(interpretedSyntax: .inlineOnlyPreservingWhitespace)
    var out: [MDBlock] = []
    for line in text.split(whereSeparator: \.isNewline) {
        let trimmed = line.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { continue }
        var runs = [InlineRun(text: trimmed)]
        if let parsed = try? AttributedString(markdown: trimmed, options: options) {
            runs = parsed.runs.map { InlineRun($0, text: String(parsed[$0.range].characters)) }
        }
        let inlines = normalizeInlines(runs, breaks: breaks)
        if !inlines.isEmpty { out.append(.paragraph(inlines)) }
    }
    return out
}

// MARK: Tree from presentation intents

/// One node of the tree being rebuilt; `id` is the intent's identity.
final class BlockNode {
    enum Kind {
        case root, quote, list(ordered: Bool), item(ordinal: Int), table([MDAlign]), headerRow, row(Int)
        case cell(Int), paragraph, heading(Int), code(String?), rule, html

        /// Quotes and lists add a level of nesting.
        var nests: Bool {
            switch self {
            case .quote, .list: true
            default: false
            }
        }

        var isContainer: Bool {
            if case .item = self { return true }
            return nests
        }

        /// A block that holds runs.
        var isLeaf: Bool {
            switch self {
            case .paragraph, .heading, .code, .rule, .cell: true
            default: false
            }
        }
    }

    let id: Int
    let kind: Kind
    var children: [BlockNode] = []
    var runs: [InlineRun] = []

    init(id: Int, kind: Kind) {
        self.id = id
        self.kind = kind
    }

    func child(id: Int, kind: Kind) -> BlockNode {
        if let last = children.last, last.id == id { return last }
        let node = BlockNode(id: id, kind: kind)
        children.append(node)
        return node
    }
}

/// Places each run under the nodes its presentation intent names, outermost first.
final class BlockTreeBuilder {
    let root = BlockNode(id: -1, kind: .root)
    private static let htmlID = -2

    func add(_ run: InlineRun, intent: PresentationIntent?) {
        var node = root
        var depth = 0
        var leaf = false
        for component in (intent?.components ?? []).reversed() {
            guard let kind = Self.kind(of: component.kind) else { continue }
            if kind.nests { depth += 1 }
            // Past the cap, quotes, lists and their items are flattened into the deepest container kept.
            if depth > maxMarkdownDepth && kind.isContainer { continue }
            leaf = leaf || kind.isLeaf
            node = node.child(id: component.identity, kind: kind)
        }
        if !leaf { node = node.child(id: Self.htmlID, kind: .html) }
        node.runs.append(run)
    }

    // A one-to-one mapping of Foundation's kinds.
    // swiftlint:disable:next cyclomatic_complexity
    private static func kind(of kind: PresentationIntent.Kind) -> BlockNode.Kind? {
        switch kind {
        case .paragraph: .paragraph
        case .header(let level): .heading(level)
        case .orderedList: .list(ordered: true)
        case .unorderedList: .list(ordered: false)
        case .listItem(let ordinal): .item(ordinal: ordinal)
        case .codeBlock(let hint): .code(hint)
        case .blockQuote: .quote
        case .thematicBreak: .rule
        case .table(let columns): .table(columns.map(align))
        case .tableHeaderRow: .headerRow
        case .tableRow(let index): .row(index)
        case .tableCell(let column): .cell(column)
        default: nil
        }
    }

    private static func align(_ column: PresentationIntent.TableColumn) -> MDAlign {
        switch column.alignment {
        case .center: .center
        case .right: .right
        default: .left
        }
    }
}

// MARK: Nodes to blocks

extension BlockNode {
    func blocks(breaks: Bool) -> [MDBlock] {
        var out: [MDBlock] = []
        for node in children { out.append(contentsOf: node.block(breaks: breaks)) }
        return out
    }

    private var text: String { runs.map(\.text).joined() }

    private func block(breaks: Bool) -> [MDBlock] {
        switch kind {
        case .paragraph:
            let inlines = imageOnlyLink(runs) ?? normalizeInlines(runs, breaks: breaks)
            return inlines.isEmpty ? [] : [.paragraph(inlines)]
        case .heading(let level): return [.heading(level: level, inlines: normalizeInlines(runs, breaks: breaks))]
        case .code(let hint): return codeBlock(hint)
        case .quote: return [.quote(blocks(breaks: breaks))]
        case .list(let ordered): return [.list(list(ordered: ordered, breaks: breaks))]
        case .table(let align): return [.table(table(align, breaks: breaks))]
        case .rule: return [.rule]
        case .html: return literalLines(text, breaks: breaks)
        default: return blocks(breaks: breaks)
        }
    }

    /// The info string's first word is the language; the content loses its last newline.
    private func codeBlock(_ hint: String?) -> [MDBlock] {
        var body = text
        if body.hasSuffix("\n") { body.removeLast() }
        guard !body.isEmpty || !runs.isEmpty else { return [] }
        let word = hint?.split(whereSeparator: { $0 == " " || $0 == "\t" }).first.map(String.init)
        return [.code(lang: word, text: body)]
    }

    /// Items in order; an empty item (no runs, so no node) is put back from the gap in the ordinals.
    private func list(ordered: Bool, breaks: Bool) -> MDList {
        var items: [MDListItem] = []
        var next: Int?
        var start: Int?
        for node in children {
            guard case .item(let ordinal) = node.kind else { continue }
            if start == nil { start = ordered ? ordinal : 1 }
            var expected = next ?? start ?? ordinal
            while expected < ordinal {
                items.append(MDListItem(task: nil, blocks: []))
                expected += 1
            }
            items.append(node.item(breaks: breaks))
            next = ordinal + 1
        }
        return MDList(ordered: ordered, start: ordered ? start : nil, items: items)
    }

    /// A first paragraph starting "[ ] ", "[x] " or "[X] " (or a tab) makes the item a task, without the marker.
    private func item(breaks: Bool) -> MDListItem {
        var blocks = blocks(breaks: breaks)
        guard case .paragraph(var inlines)? = blocks.first, case .text(let first)? = inlines.first else {
            return MDListItem(task: nil, blocks: blocks)
        }
        let marker = Array(first.prefix(4))
        guard marker.count == 4, marker[0] == "[", marker[2] == "]", marker[3] == " " || marker[3] == "\t",
              marker[1] == " " || marker[1] == "x" || marker[1] == "X" else {
            return MDListItem(task: nil, blocks: blocks)
        }
        let rest = String(first.dropFirst(4))
        if rest.isEmpty { inlines.removeFirst() } else { inlines[0] = .text(rest) }
        if inlines.isEmpty { blocks.removeFirst() } else { blocks[0] = .paragraph(inlines) }
        return MDListItem(task: marker[1] != " ", blocks: blocks)
    }

    /// Header and rows with one cell per column; a cell or row with no text has no runs, so is filled in empty.
    private func table(_ align: [MDAlign], breaks: Bool) -> MDTable {
        var header: [[MDInline]] = Array(repeating: [], count: align.count)
        var rows: [[[MDInline]]] = []
        for node in children {
            switch node.kind {
            case .headerRow:
                header = node.cells(align.count, breaks: breaks)
            case .row(let index):
                while rows.count < index - 1 { rows.append(Array(repeating: [], count: align.count)) }
                rows.append(node.cells(align.count, breaks: breaks))
            default:
                continue
            }
        }
        return MDTable(align: align, header: header, rows: rows)
    }

    private func cells(_ count: Int, breaks: Bool) -> [[MDInline]] {
        var out: [[MDInline]] = Array(repeating: [], count: count)
        for node in children {
            if case .cell(let column) = node.kind, column < count {
                out[column] = normalizeInlines(node.runs, breaks: breaks)
            }
        }
        return out
    }
}

// MARK: User bubbles

/// A block of a user's message: a paragraph of inlines, or a fenced code block shown as a box. A code span is
/// never a box, even alone on its line (the web client's `code` flag).
public enum BubbleBlock: Hashable, Sendable {
    case paragraph([MDInline])
    case code(String)
}

/// A user's message, inline-only like the web client: every block becomes paragraphs, keeping its words. A list
/// item keeps its marker as text, a quote its "> ", a table row its cells joined by " | "; a fenced block stays
/// code so pasted code still reads as code.
public func bubbleBlocks(_ blocks: [MDBlock]) -> [BubbleBlock] {
    var out: [BubbleBlock] = []
    for block in blocks { out.append(contentsOf: bubble(block)) }
    return out
}

private func bubble(_ block: MDBlock) -> [BubbleBlock] {
    switch block {
    case .heading(_, let inlines), .paragraph(let inlines): return bubbleParagraph(inlines)
    case .code(_, let text): return [.code(text)]
    case .list(let list):
        var out: [BubbleBlock] = []
        for (index, item) in list.items.enumerated() {
            let number = list.ordered ? "\((list.start ?? 1) + index). " : "• "
            let task = item.task.map { $0 ? "[x] " : "[ ] " } ?? ""
            out += prefixed(number + task, bubbleBlocks(item.blocks))
        }
        return out
    case .quote(let inner): return prefixed("> ", bubbleBlocks(inner))
    case .table(let table):
        return ([table.header] + table.rows).flatMap { row in
            bubbleParagraph(row.enumerated().flatMap { index, cell in index == 0 ? cell : [.text(" | ")] + cell })
        }
    case .rule: return []
    case .media(let media): return bubbleParagraph([.link(href: media.href, content: [.text(media.title)])])
    }
}

/// The marker joins the first paragraph; before a code block (or nothing) it stands on its own line.
private func prefixed(_ prefix: String, _ inner: [BubbleBlock]) -> [BubbleBlock] {
    if case .paragraph(let inlines)? = inner.first {
        return bubbleParagraph([.text(prefix)] + inlines) + inner.dropFirst()
    }
    let marker = String(prefix.reversed().drop(while: { $0 == " " }).reversed())
    return bubbleParagraph([.text(marker)]) + inner
}

private func bubbleParagraph(_ inlines: [MDInline]) -> [BubbleBlock] {
    let merged = mergeTexts(inlines)
    return merged.isEmpty ? [] : [.paragraph(merged)]
}

/// Adjacent texts joined, empty ones dropped.
func mergeTexts(_ inlines: [MDInline]) -> [MDInline] {
    var out: [MDInline] = []
    for inline in inlines {
        guard case .text(let value) = inline else {
            out.append(inline)
            continue
        }
        if value.isEmpty { continue }
        if case .text(let last)? = out.last { out[out.count - 1] = .text(last + value) } else { out.append(inline) }
    }
    return out
}
