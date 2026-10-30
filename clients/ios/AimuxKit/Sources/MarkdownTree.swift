import Foundation

// swiftlint:disable identifier_name
// The case and key names are the fixtures' wire names (`em`, `br`, `pr`, `t`, `v`, `c`).

// The block tree both clients render (api/testdata/clients/README.md, "Markdown" and "References"). Codable in
// the fixtures' wire shape, so a fixture's `blocks` decode straight into it and compare with ==.

/// What a reference links to.
public enum RefKind: String, Codable, Hashable, Sendable { case pr, commit, jira, path }

/// One inline: text, a code span, emphasis, a link, a hard break, or a reference made by `linkReferences`.
public indirect enum MDInline: Hashable, Sendable {
    case text(String)
    case code(String)
    case em([MDInline])
    case strong([MDInline])
    case del([MDInline])
    /// Always http or https; `content` is exactly one `.text`.
    case link(href: String, content: [MDInline])
    case br
    /// `code` keeps the monospace look of a path that came from a code span.
    case ref(kind: RefKind, text: String, href: String, code: Bool)
}

/// A table column's alignment; a column with no colon is left.
public enum MDAlign: String, Codable, Hashable, Sendable { case left, center, right }

/// A list item: `task` is nil for a plain item, else whether it is ticked.
public struct MDListItem: Hashable, Sendable {
    public let task: Bool?
    public let blocks: [MDBlock]

    public init(task: Bool?, blocks: [MDBlock]) {
        self.task = task
        self.blocks = blocks
    }
}

/// A list: `start` is the first number of an ordered list, nil for bullets.
public struct MDList: Hashable, Sendable {
    public let ordered: Bool
    public let start: Int?
    public let items: [MDListItem]

    public init(ordered: Bool, start: Int?, items: [MDListItem]) {
        self.ordered = ordered
        self.start = start
        self.items = items
    }
}

/// A table: one alignment per column; every row has as many cells as the header.
public struct MDTable: Hashable, Sendable {
    public let align: [MDAlign]
    public let header: [[MDInline]]
    public let rows: [[[MDInline]]]

    public init(align: [MDAlign], header: [[MDInline]], rows: [[[MDInline]]]) {
        self.align = align
        self.header = header
        self.rows = rows
    }
}

/// What a media card shows (README, "Media").
public enum MediaKind: String, Codable, Hashable, Sendable { case youtube, vimeo, video, image, audio }

/// A paragraph that was only a link to a video, image or audio file, made by `mediaCards`.
public struct MDMedia: Hashable, Sendable {
    public let kind: MediaKind
    public let href: String
    public let title: String

    public init(kind: MediaKind, href: String, title: String) {
        self.kind = kind
        self.href = href
        self.title = title
    }
}

/// One block of the tree.
public indirect enum MDBlock: Hashable, Sendable {
    case heading(level: Int, inlines: [MDInline])
    case paragraph([MDInline])
    case list(MDList)
    case code(lang: String?, text: String)
    case quote([MDBlock])
    case table(MDTable)
    case rule
    case media(MDMedia)
}

// MARK: Wire shape

extension MDInline: Codable {
    private enum Keys: String, CodingKey { case t, v, c, href, kind, code }

    public init(from decoder: Decoder) throws {
        let box = try decoder.container(keyedBy: Keys.self)
        let type = try box.decode(String.self, forKey: .t)
        switch type {
        case "text": self = .text(try box.decode(String.self, forKey: .v))
        case "code": self = .code(try box.decode(String.self, forKey: .v))
        case "em": self = .em(try box.decode([MDInline].self, forKey: .c))
        case "strong": self = .strong(try box.decode([MDInline].self, forKey: .c))
        case "del": self = .del(try box.decode([MDInline].self, forKey: .c))
        case "link":
            self = .link(href: try box.decode(String.self, forKey: .href),
                         content: try box.decode([MDInline].self, forKey: .c))
        case "br": self = .br
        case "ref":
            self = .ref(kind: try box.decode(RefKind.self, forKey: .kind),
                        text: try box.decode(String.self, forKey: .v),
                        href: try box.decode(String.self, forKey: .href),
                        code: try box.decodeIfPresent(Bool.self, forKey: .code) ?? false)
        default:
            throw DecodingError.dataCorruptedError(forKey: .t, in: box, debugDescription: "unknown inline \(type)")
        }
    }

    public func encode(to encoder: Encoder) throws {
        var box = encoder.container(keyedBy: Keys.self)
        switch self {
        case .text(let value): try box.encode("text", forKey: .t); try box.encode(value, forKey: .v)
        case .code(let value): try box.encode("code", forKey: .t); try box.encode(value, forKey: .v)
        case .em(let content): try box.encode("em", forKey: .t); try box.encode(content, forKey: .c)
        case .strong(let content): try box.encode("strong", forKey: .t); try box.encode(content, forKey: .c)
        case .del(let content): try box.encode("del", forKey: .t); try box.encode(content, forKey: .c)
        case .link(let href, let content):
            try box.encode("link", forKey: .t); try box.encode(href, forKey: .href); try box.encode(content, forKey: .c)
        case .br: try box.encode("br", forKey: .t)
        case .ref(let kind, let text, let href, let code):
            try box.encode("ref", forKey: .t)
            try box.encode(kind, forKey: .kind)
            try box.encode(text, forKey: .v)
            try box.encode(href, forKey: .href)
            if code { try box.encode(true, forKey: .code) }
        }
    }
}

extension MDListItem: Codable {}

extension MDBlock: Codable {
    private enum Keys: String, CodingKey {
        case type, level, inlines, ordered, start, items, lang, text, blocks, align, header, rows, kind, href, title
    }

    public init(from decoder: Decoder) throws {
        let box = try decoder.container(keyedBy: Keys.self)
        let type = try box.decode(String.self, forKey: .type)
        switch type {
        case "heading":
            self = .heading(level: try box.decode(Int.self, forKey: .level),
                            inlines: try box.decode([MDInline].self, forKey: .inlines))
        case "paragraph": self = .paragraph(try box.decode([MDInline].self, forKey: .inlines))
        case "list":
            self = .list(MDList(ordered: try box.decode(Bool.self, forKey: .ordered),
                                start: try box.decode(Int?.self, forKey: .start),
                                items: try box.decode([MDListItem].self, forKey: .items)))
        case "code":
            self = .code(lang: try box.decode(String?.self, forKey: .lang),
                         text: try box.decode(String.self, forKey: .text))
        case "quote": self = .quote(try box.decode([MDBlock].self, forKey: .blocks))
        case "table":
            self = .table(MDTable(align: try box.decode([MDAlign].self, forKey: .align),
                                  header: try box.decode([[MDInline]].self, forKey: .header),
                                  rows: try box.decode([[[MDInline]]].self, forKey: .rows)))
        case "rule": self = .rule
        case "media":
            self = .media(MDMedia(kind: try box.decode(MediaKind.self, forKey: .kind),
                                  href: try box.decode(String.self, forKey: .href),
                                  title: try box.decode(String.self, forKey: .title)))
        default:
            throw DecodingError.dataCorruptedError(forKey: .type, in: box, debugDescription: "unknown block \(type)")
        }
    }

    public func encode(to encoder: Encoder) throws {
        var box = encoder.container(keyedBy: Keys.self)
        switch self {
        case .heading(let level, let inlines):
            try box.encode("heading", forKey: .type); try box.encode(level, forKey: .level)
            try box.encode(inlines, forKey: .inlines)
        case .paragraph(let inlines):
            try box.encode("paragraph", forKey: .type); try box.encode(inlines, forKey: .inlines)
        case .list(let list):
            try box.encode("list", forKey: .type); try box.encode(list.ordered, forKey: .ordered)
            try box.encode(list.start, forKey: .start); try box.encode(list.items, forKey: .items)
        case .code(let lang, let text):
            try box.encode("code", forKey: .type); try box.encode(lang, forKey: .lang)
            try box.encode(text, forKey: .text)
        case .quote(let blocks): try box.encode("quote", forKey: .type); try box.encode(blocks, forKey: .blocks)
        case .table(let table):
            try box.encode("table", forKey: .type); try box.encode(table.align, forKey: .align)
            try box.encode(table.header, forKey: .header); try box.encode(table.rows, forKey: .rows)
        case .rule: try box.encode("rule", forKey: .type)
        case .media(let media):
            try box.encode("media", forKey: .type); try box.encode(media.kind, forKey: .kind)
            try box.encode(media.href, forKey: .href); try box.encode(media.title, forKey: .title)
        }
    }
}

// swiftlint:enable identifier_name
