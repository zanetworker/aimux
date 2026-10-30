import Foundation

/// The blocks the chat shows for a reply or a user's message, cached by text and context: a view's body runs
/// often, and a streaming reply is parsed again on every update, so an unchanged text is parsed only once.
public enum MarkdownRender {
    /// A reply's blocks with references linked and media links made cards; nil when the text is too long to parse
    /// (show it as is).
    public static func reply(_ text: String, context: ReferenceContext) -> [MDBlock]? {
        cached(text, context: context, bubble: false) {
            mediaCards(linkReferences(markdownBlocks(text), context: context))
        }
    }

    /// A user's message, inline-only with its typed lines kept; nil when too long to parse.
    public static func bubble(_ text: String, context: ReferenceContext) -> [BubbleBlock]? {
        cached(text, context: context, bubble: true) {
            linkReferences(markdownBlocks(text, breaks: true), context: context)
        }.map(bubbleBlocks)
    }

    private final class Entry {
        let blocks: [MDBlock]
        init(_ blocks: [MDBlock]) { self.blocks = blocks }
    }

    private final class Key: NSObject {
        let text: String
        let context: ReferenceContext
        let bubble: Bool

        init(text: String, context: ReferenceContext, bubble: Bool) {
            self.text = text
            self.context = context
            self.bubble = bubble
        }

        override var hash: Int {
            var hasher = Hasher()
            hasher.combine(text)
            hasher.combine(context)
            hasher.combine(bubble)
            return hasher.finalize()
        }

        override func isEqual(_ object: Any?) -> Bool {
            guard let other = object as? Key else { return false }
            return other.bubble == bubble && other.context == context && other.text == text
        }
    }

    /// NSCache is thread-safe.
    nonisolated(unsafe) private static let cache: NSCache<Key, Entry> = {
        let cache = NSCache<Key, Entry>()
        cache.countLimit = 300
        return cache
    }()

    private static func cached(_ text: String, context: ReferenceContext, bubble: Bool,
                               make: () -> [MDBlock]) -> [MDBlock]? {
        guard formatsAsMarkdown(text) else { return nil }
        let key = Key(text: text, context: context, bubble: bubble)
        if let hit = cache.object(forKey: key) { return hit.blocks }
        let blocks = make()
        cache.setObject(Entry(blocks), forKey: key)
        return blocks
    }
}

/// A step's target as one line of inlines. A target with no whitespace and no backtick (a path, a pattern, a word)
/// is not parsed, so `pkg/__init__.py` keeps its underscores and `**/*.go` its stars; when it is a path as a whole
/// it is that path reference, drawn in the step's monospace (so without the code look). Any other target is inline
/// Markdown, its lines and blocks joined by spaces.
public func stepInlines(_ target: String, context: ReferenceContext) -> [MDInline] {
    if !target.contains(where: { $0.isWhitespace || $0 == "`" }) {
        let whole: [MDInline] = linkReferences([MDInline.code(target)], context: context)
        if whole.count == 1, case .ref(let kind, let text, let href, _) = whole[0] {
            return [.ref(kind: kind, text: text, href: href, code: false)]
        }
        return target.isEmpty ? [] : [.text(target)]
    }
    guard let blocks = MarkdownRender.bubble(target, context: context) else { return [.text(target)] }
    var out: [MDInline] = []
    for block in blocks {
        if !out.isEmpty { out.append(.text(" ")) }
        switch block {
        case .paragraph(let inlines): out += inlines.map { $0 == .br ? .text(" ") : $0 }
        case .code(let code): out.append(.code(code))
        }
    }
    return mergeTexts(out)
}

/// Whether text reads right to left: its first strong letter is Hebrew or Arabic script. Code is skipped.
public func isRightToLeft(_ inlines: [MDInline]) -> Bool {
    for inline in inlines {
        if let rtl = direction(of: inline) { return rtl }
    }
    return false
}

private func direction(of inline: MDInline) -> Bool? {
    switch inline {
    case .text(let value), .ref(_, let value, _, false): return direction(of: value)
    case .em(let inner), .strong(let inner), .del(let inner), .link(_, let inner):
        for child in inner {
            if let rtl = direction(of: child) { return rtl }
        }
        return nil
    case .code, .br, .ref: return nil
    }
}

private func direction(of text: String) -> Bool? {
    for scalar in text.unicodeScalars {
        switch scalar.value {
        case 0x0590...0x08FF, 0xFB1D...0xFDFF, 0xFE70...0xFEFF, 0x10800...0x10FFF, 0x1E800...0x1EFFF: return true
        default: if scalar.properties.isAlphabetic { return false }
        }
    }
    return nil
}

/// While a reply streams in, it is parsed at most this often; the last update is always parsed.
public let streamingParseInterval = Duration.milliseconds(100)

/// How long to wait before parsing a changed reply, given the time since the last parse (nil: never parsed).
public func parseDelay(sinceLastParse elapsed: Duration?) -> Duration {
    guard let elapsed, elapsed < streamingParseInterval else { return .zero }
    return streamingParseInterval - elapsed
}

/// A link or reference to open: its visible text and target.
public struct LinkTarget: Hashable, Sendable {
    public let text: String
    public let href: String

    public init(text: String, href: String) {
        self.text = text
        self.href = href
    }
}

/// The http(s) links and references in a table row's cells, in reading order (for VoiceOver actions).
public func webLinks(in row: [[MDInline]]) -> [LinkTarget] {
    row.flatMap { webLinks(inInlines: $0) }
}

private func webLinks(inInlines inlines: [MDInline]) -> [LinkTarget] {
    inlines.flatMap { inline -> [LinkTarget] in
        switch inline {
        case .link(let href, let content):
            isWebHref(href) ? [LinkTarget(text: visibleText(content), href: href)] : []
        case .ref(_, let text, let href, _):
            isWebHref(href) ? [LinkTarget(text: text, href: href)] : []
        case .em(let inner), .strong(let inner), .del(let inner):
            webLinks(inInlines: inner)
        case .text, .code, .br:
            []
        }
    }
}

private func isWebHref(_ href: String) -> Bool {
    let lower = href.lowercased()
    return lower.hasPrefix("http:") || lower.hasPrefix("https:")
}

private func visibleText(_ inlines: [MDInline]) -> String {
    inlines.map { inline in
        switch inline {
        case .text(let value), .code(let value), .ref(_, let value, _, _): value
        case .em(let inner), .strong(let inner), .del(let inner), .link(_, let inner): visibleText(inner)
        case .br: " "
        }
    }.joined()
}
