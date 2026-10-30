import Foundation

// What an opened step shows: the rules are "Step detail" in api/testdata/clients/README.md, pinned by
// chat/step-details-*.json; the web client's liveDetail (timeline.mjs) and stepDetail (render.mjs) do the same.

/// A step's detail; every field "" when absent. A detail with all four empty has nothing to open.
public struct StepDetail: Decodable, Equatable, Hashable, Sendable {
    /// What the tool was asked to do (a command, a pattern, a path).
    public let input: String
    /// Why it failed.
    public let error: String
    /// The text an edit replaced and wrote, whitespace kept.
    public let old: String
    public let new: String

    public var isEmpty: Bool { input.isEmpty && error.isEmpty && old.isEmpty && new.isEmpty }

    public static let empty = StepDetail(input: "", error: "", old: "", new: "")

    public init(input: String, error: String, old: String, new: String) {
        self.input = input
        self.error = error
        self.old = old
        self.new = new
    }

    private enum CodingKeys: String, CodingKey { case input, error, old, new }

    /// A field that is missing or not a string reads as "".
    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        func field(_ key: CodingKeys) -> String { (try? container.decode(String.self, forKey: key)) ?? "" }
        self.init(input: field(.input), error: field(.error), old: field(.old), new: field(.new))
    }
}

/// One item of a tool call's ACP `content` array, the fields a step detail reads; a field of the wrong type is nil.
public struct ToolContentItem: Decodable, Equatable, Hashable, Sendable {
    public let type: String?
    /// A `diff` item's texts.
    public let oldText: String?
    public let newText: String?
    /// A `content` item's `content.text`, when its `content.type` is `text` and the text is a string.
    public let text: String?

    public init(type: String?, oldText: String? = nil, newText: String? = nil, text: String? = nil) {
        self.type = type
        self.oldText = oldText
        self.newText = newText
        self.text = text
    }

    private enum CodingKeys: String, CodingKey { case type, oldText, newText, content }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        type = try? container.decode(String.self, forKey: .type)
        oldText = try? container.decode(String.self, forKey: .oldText)
        newText = try? container.decode(String.self, forKey: .newText)
        let inner = try? container.decode(ContentText.self, forKey: .content)
        text = inner?.type == "text" ? inner?.text : nil
    }
}

/// A `content` item's inner `content`: `{type, text}`, a field of the wrong type nil.
private struct ContentText: Decodable {
    let type: String?
    let text: String?

    private enum CodingKeys: String, CodingKey { case type, text }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        type = try? container.decode(String.self, forKey: .type)
        text = try? container.decode(String.self, forKey: .text)
    }
}

/// A value that may fail to decode, read as nil, so an array keeps going past an item of another shape.
struct Maybe<Value: Decodable>: Decodable {
    let value: Value?
    init(from decoder: Decoder) throws { value = try? Value(from: decoder) }
}

/// An object's string values; other values are left out. Decoding anything but an object fails.
struct StringFields: Decodable {
    let values: [String: String]

    private struct Key: CodingKey {
        let stringValue: String
        var intValue: Int? { nil }
        init(stringValue: String) { self.stringValue = stringValue }
        init?(intValue: Int) { nil }
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: Key.self)
        var values: [String: String] = [:]
        for key in container.allKeys {
            if let value = try? container.decode(String.self, forKey: key) { values[key.stringValue] = value }
        }
        self.values = values
    }
}

private let inputKeys = ["command", "pattern", "url", "query", "description", "file_path"]

/// A live step's detail from its events (the tool_call and its updates, in seq order) and its final status.
/// Each field comes from one event, the latest that qualifies; nothing is merged across events.
func liveStepDetail(_ updates: [SessionUpdate], status: StepStatus) -> StepDetail {
    var raw: [String: String]?
    var diff: ToolContentItem?
    var content: [ToolContentItem]?
    for update in updates {
        if let input = update.rawInput { raw = input }
        if let items = update.toolContent {
            content = items
            if let found = items.first(where: { $0.type == "diff" }) { diff = found }
        }
    }
    let input = raw.flatMap { fields in inputKeys.lazy.compactMap { fields[$0] }.first { !$0.isEmpty } } ?? ""
    var old = ""
    var new = ""
    if let diff {
        old = diff.oldText ?? ""
        new = diff.newText ?? ""
    } else if let raw {
        old = raw["old_string"] ?? ""
        new = raw["new_string"] ?? raw["content"] ?? ""
    }
    var error = ""
    if status == .failed, let content {
        error = content.filter { $0.type == "content" }.compactMap(\.text).joined(separator: "\n")
    }
    return StepDetail(input: input, error: error, old: old, new: new)
}

/// One line of an edit: removed (from `old`) or added (from `new`), without its line break.
public struct DiffLine: Equatable, Hashable, Sendable {
    public enum Kind: Equatable, Hashable, Sendable { case removed, added }
    public let kind: Kind
    public let text: String

    public init(kind: Kind, text: String) {
        self.kind = kind
        self.text = text
    }
}

/// An edit's removed lines then its added lines, at most `limit`, and how many more there are. A final line break
/// ends the last line rather than starting an empty one; "" has no lines. Like the web, lines split on "\n"
/// scalars only, so a "\r" of CRLF text stays part of its line (as a Character, "\r\n" would not be split).
public func diffLines(old: String, new: String, limit: Int) -> (lines: [DiffLine], hidden: Int) {
    func split(_ text: String) -> [Substring] {
        guard !text.isEmpty else { return [] }
        var body = Substring(text).unicodeScalars
        if body.last == "\n" { body.removeLast() }
        return body.split(separator: "\n", omittingEmptySubsequences: false).map { Substring($0) }
    }
    let removed = split(old)
    let added = split(new)
    let total = removed.count + added.count
    let all = removed.lazy.map { DiffLine(kind: .removed, text: String($0)) }
        + added.lazy.map { DiffLine(kind: .added, text: String($0)) }
    return (Array(all.prefix(max(0, limit))), max(0, total - max(0, limit)))
}

/// The first `limit` Unicode scalars of `text`, then "…"; text within the cap is returned whole. Scalars, like the
/// web's code points (`Array.from`), so both clients cut at the same place.
public func cappedScalars(_ text: String, _ limit: Int) -> String {
    let scalars = text.unicodeScalars
    guard let end = scalars.index(scalars.startIndex, offsetBy: max(0, limit), limitedBy: scalars.endIndex),
          end != scalars.endIndex else { return text }
    var out = String.UnicodeScalarView(scalars[..<end])
    out.append("…")
    return String(out)
}

/// What an opened step draws: live text from the event stream has no bound of its own, so the view caps it at
/// 4000 scalars of input, 2000 of error and of each diff line, each cut followed by "…".
public struct ShownStepDetail: Equatable, Sendable {
    public static let inputCap = 4000
    public static let errorCap = 2000
    public static let lineCap = 2000

    public let input: String
    public let error: String
    public let lines: [DiffLine]
    /// Diff lines past `diffLimit`.
    public let hidden: Int

    public init(_ detail: StepDetail, diffLimit: Int) {
        input = cappedScalars(detail.input, Self.inputCap)
        error = cappedScalars(detail.error, Self.errorCap)
        let diff = diffLines(old: detail.old, new: detail.new, limit: diffLimit)
        lines = diff.lines.map { DiffLine(kind: $0.kind, text: cappedScalars($0.text, Self.lineCap)) }
        hidden = diff.hidden
    }
}

/// A link taken out of a step's target, to offer in its open detail.
public struct StepLink: Equatable, Hashable, Sendable {
    public let href: String
    public let text: String
    /// nil for a plain Markdown link.
    public let kind: RefKind?

    public init(href: String, text: String, kind: RefKind?) {
        self.href = href
        self.text = text
        self.kind = kind
    }
}

/// The inlines with every reference and link made plain text (a code-look reference stays code), and the http(s)
/// links that were taken out, in order: a tappable row holds no link, its detail lists them instead.
public func unlinkedInlines(_ inlines: [MDInline]) -> (inlines: [MDInline], links: [StepLink]) {
    var links: [StepLink] = []
    func isWeb(_ href: String) -> Bool {
        let lower = href.lowercased()
        return lower.hasPrefix("http:") || lower.hasPrefix("https:")
    }
    func plainText(_ list: [MDInline]) -> String {
        list.map { inline in
            switch inline {
            case .text(let value), .code(let value), .ref(_, let value, _, _): value
            case .em(let inner), .strong(let inner), .del(let inner), .link(_, let inner): plainText(inner)
            case .br: ""
            }
        }.joined()
    }
    func flat(_ list: [MDInline]) -> [MDInline] {
        list.flatMap { inline -> [MDInline] in
            switch inline {
            case .ref(let kind, let text, let href, let code):
                if isWeb(href) { links.append(StepLink(href: href, text: text, kind: kind)) }
                return [code ? .code(text) : .text(text)]
            case .link(let href, let content):
                let inner = flat(content)
                if isWeb(href) { links.append(StepLink(href: href, text: plainText(inner), kind: nil)) }
                return inner
            case .em(let inner): return [.em(flat(inner))]
            case .strong(let inner): return [.strong(flat(inner))]
            case .del(let inner): return [.del(flat(inner))]
            case .text, .code, .br: return [inline]
            }
        }
    }
    let plain = flat(inlines)
    return (plain, links)
}
