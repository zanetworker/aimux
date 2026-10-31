import Foundation

/// What a piece of code is, for its colour in a code block.
public enum CodeTokenKind: String, Sendable, CaseIterable {
    case plain, keyword, string, comment, number, function, key, added, removed, meta
}

/// One coloured piece of a code block; the pieces' texts join back to the code exactly.
public struct CodeToken: Equatable, Sendable {
    public let kind: CodeTokenKind
    public let text: String

    public init(_ kind: CodeTokenKind, _ text: String) {
        self.kind = kind
        self.text = text
    }
}

/// Splits `code` into coloured pieces for `lang` (a fence's info string, case-insensitive). The rules are pinned by
/// api/testdata/clients/highlight/ and are the same as highlight.mjs in the web client; an unknown language, or
/// none, is one plain piece.
public func highlight(_ code: String, lang: String?) -> [CodeToken] {
    let name = lang.flatMap { HighlightLanguage.aliases[$0.trimmingCharacters(in: .whitespaces).lowercased()] }
    guard let name else { return merged([CodeToken(.plain, code)]) }
    if name == "diff" { return merged(diffTokens(code)) }
    guard let language = HighlightLanguage.all[name] else { return merged([CodeToken(.plain, code)]) }
    var scanner = HighlightScanner(code: code, language: language)
    return merged(scanner.tokens())
}

struct HighlightLanguage: Sendable {
    var line: [String] = []
    var block: (open: String, close: String)?
    var quotes: Set<Unicode.Scalar>
    var keys: Keys = .none
    var variables = false
    var keywords: Set<String>

    enum Keys: Sendable { case none, json, yaml }

    static let aliases: [String: String] = [
        "go": "go", "golang": "go", "swift": "swift",
        "js": "js", "javascript": "js", "jsx": "js", "mjs": "js", "ts": "js", "typescript": "js", "tsx": "js",
        "py": "python", "python": "python",
        "sh": "sh", "bash": "sh", "zsh": "sh", "shell": "sh", "console": "sh",
        "json": "json", "yaml": "yaml", "yml": "yaml", "diff": "diff", "patch": "diff"
    ]

    private static func words(_ list: String) -> Set<String> { Set(list.split(separator: " ").map(String.init)) }
    private static func quotes(_ list: String) -> Set<Unicode.Scalar> { Set(list.unicodeScalars) }

    static let all: [String: HighlightLanguage] = [
        "go": HighlightLanguage(line: ["//"], block: ("/*", "*/"), quotes: quotes("\"'`"), keywords: words(
            "break case chan const continue default defer else fallthrough for func go goto if import interface map "
                + "package range return select struct switch type var nil true false iota")),
        "swift": HighlightLanguage(line: ["//"], block: ("/*", "*/"), quotes: quotes("\""), keywords: words(
            "func let var if else guard return struct class enum protocol extension import for in while switch case "
                + "default break continue throw throws try catch do async await self Self nil true false public "
                + "private internal fileprivate static init some any where")),
        "js": HighlightLanguage(line: ["//"], block: ("/*", "*/"), quotes: quotes("\"'`"), keywords: words(
            "const let var function return if else for while do switch case default break continue new class "
                + "extends import export from async await try catch finally throw typeof instanceof in of this null "
                + "undefined true false interface type enum implements as")),
        "python": HighlightLanguage(line: ["#"], quotes: quotes("\"'"), keywords: words(
            "def class return if elif else for while in not and or is import from as with try except finally raise "
                + "lambda yield pass break continue None True False async await global nonlocal")),
        "sh": HighlightLanguage(line: ["#"], quotes: quotes("\"'"), variables: true, keywords: words(
            "if then else elif fi for do done case esac while until function in export local return")),
        "json": HighlightLanguage(quotes: quotes("\""), keys: .json, keywords: words("true false null")),
        "yaml": HighlightLanguage(line: ["#"], quotes: quotes("\"'"), keys: .yaml,
                                  keywords: words("true false null yes no"))
    ]
}

private func diffTokens(_ code: String) -> [CodeToken] {
    var out: [CodeToken] = []
    var rest = Substring(code)
    while !rest.isEmpty {
        let end = rest.firstIndex(of: "\n").map { rest.index(after: $0) } ?? rest.endIndex
        let line = String(rest[..<end])
        rest = rest[end...]
        let kind: CodeTokenKind = line.hasPrefix("+++") || line.hasPrefix("---") || line.hasPrefix("@@") ? .meta
            : line.hasPrefix("+") ? .added : line.hasPrefix("-") ? .removed : .plain
        out.append(CodeToken(kind, line))
    }
    return out
}

/// Joins neighbouring pieces of one kind and drops empty ones.
private func merged(_ tokens: [CodeToken]) -> [CodeToken] {
    var out: [CodeToken] = []
    for token in tokens where !token.text.isEmpty {
        if let last = out.last, last.kind == token.kind {
            out[out.count - 1] = CodeToken(last.kind, last.text + token.text)
        } else {
            out.append(token)
        }
    }
    return out
}

/// Scans code left to right by Unicode scalar, as highlight.mjs does by character.
private struct HighlightScanner {
    let scalars: [Unicode.Scalar]
    let language: HighlightLanguage
    private var out: [CodeToken] = []

    init(code: String, language: HighlightLanguage) {
        scalars = Array(code.unicodeScalars)
        self.language = language
    }

    private func text(_ start: Int, _ end: Int) -> String {
        var view = String.UnicodeScalarView()
        view.append(contentsOf: scalars[start..<end])
        return String(view)
    }

    private func starts(_ prefix: String, at index: Int) -> Bool {
        let wanted = Array(prefix.unicodeScalars)
        guard index + wanted.count <= scalars.count else { return false }
        return Array(scalars[index..<index + wanted.count]) == wanted
    }

    private func find(_ needle: String, from index: Int) -> Int? {
        var pos = index
        while pos < scalars.count {
            if starts(needle, at: pos) { return pos }
            pos += 1
        }
        return nil
    }

    private static func isIdentStart(_ scalar: Unicode.Scalar) -> Bool {
        scalar == "_" || ("A"..."Z").contains(scalar) || ("a"..."z").contains(scalar)
    }
    private static func isIdent(_ scalar: Unicode.Scalar) -> Bool { isIdentStart(scalar) || isDigit(scalar) }
    private static func isDigit(_ scalar: Unicode.Scalar) -> Bool { ("0"..."9").contains(scalar) }
    private static func isNumberPart(_ scalar: Unicode.Scalar) -> Bool {
        isDigit(scalar) || ("A"..."F").contains(scalar) || ("a"..."f").contains(scalar)
            || scalar == "x" || scalar == "X" || scalar == "." || scalar == "_"
    }

    mutating func tokens() -> [CodeToken] {
        var index = 0
        var lineStart = true
        while index < scalars.count {
            if language.keys == .yaml && lineStart, let end = yamlKey(at: index) {
                index = end
                lineStart = false
                continue
            }
            lineStart = false
            let scalar = scalars[index]
            if scalar == "\n" {
                out.append(CodeToken(.plain, "\n"))
                index += 1
                lineStart = true
            } else if language.line.contains(where: { starts($0, at: index) }) {
                let stop = find("\n", from: index) ?? scalars.count
                out.append(CodeToken(.comment, text(index, stop)))
                index = stop
            } else if let block = language.block, starts(block.open, at: index) {
                let close = find(block.close, from: index + block.open.unicodeScalars.count)
                let stop = close.map { $0 + block.close.unicodeScalars.count } ?? scalars.count
                out.append(CodeToken(.comment, text(index, stop)))
                index = stop
            } else if language.quotes.contains(scalar) {
                index = string(at: index)
            } else if language.variables, let stop = variable(at: index) {
                out.append(CodeToken(.key, text(index, stop)))
                index = stop
            } else {
                index = word(at: index)
            }
        }
        return out
    }

    /// A number (a digit not after a letter, digit or `_`), a word (keyword, function before `(`, or plain), or one
    /// plain scalar; returns the index past it.
    private mutating func word(at start: Int) -> Int {
        let scalar = scalars[start]
        if Self.isDigit(scalar) && (start == 0 || !Self.isIdent(scalars[start - 1])) {
            var stop = start + 1
            while stop < scalars.count && Self.isNumberPart(scalars[stop]) { stop += 1 }
            out.append(CodeToken(.number, text(start, stop)))
            return stop
        }
        guard Self.isIdentStart(scalar) else {
            out.append(CodeToken(.plain, String(scalar)))
            return start + 1
        }
        var stop = start + 1
        while stop < scalars.count && Self.isIdent(scalars[stop]) { stop += 1 }
        let word = text(start, stop)
        let kind: CodeTokenKind = language.keywords.contains(word) ? .keyword
            : stop < scalars.count && scalars[stop] == "(" ? .function : .plain
        out.append(CodeToken(kind, word))
        return stop
    }

    /// A string from its quote at `start`; in JSON one followed by optional spaces and `:` is a key.
    private mutating func string(at start: Int) -> Int {
        let quote = scalars[start]
        let backtick = quote == "`"
        var index = start + 1
        var stop = scalars.count
        while index < scalars.count {
            let scalar = scalars[index]
            if scalar == "\\" && !backtick {
                index += 2
                continue
            }
            if scalar == quote {
                stop = index + 1
                break
            }
            if scalar == "\n" && !backtick {
                stop = index
                break
            }
            index += 1
        }
        stop = min(stop, scalars.count)
        var after = stop
        while after < scalars.count && (scalars[after] == " " || scalars[after] == "\t") { after += 1 }
        let isKey = language.keys == .json && after < scalars.count && scalars[after] == ":"
        out.append(CodeToken(isKey ? .key : .string, text(start, stop)))
        return stop
    }

    /// `$NAME` or `${...}` in shell; nil when `$` starts neither.
    private func variable(at start: Int) -> Int? {
        guard scalars[start] == "$", start + 1 < scalars.count else { return nil }
        let next = scalars[start + 1]
        if next == "{" {
            return find("}", from: start + 1).map { $0 + 1 } ?? scalars.count
        }
        guard Self.isIdentStart(next) else { return nil }
        var stop = start + 1
        while stop < scalars.count && Self.isIdent(scalars[stop]) { stop += 1 }
        return stop
    }

    /// A YAML key at a line's start: indentation and an optional "- ", then a name of letters, digits and
    /// `_ . / -` followed by `:` and a space, tab, newline or the end. Returns the index past the name.
    private mutating func yamlKey(at start: Int) -> Int? {
        var index = start
        while index < scalars.count && (scalars[index] == " " || scalars[index] == "\t") { index += 1 }
        if starts("- ", at: index) { index += 2 }
        let nameStart = index
        while index < scalars.count, Self.isIdent(scalars[index]) || "./-".unicodeScalars.contains(scalars[index]) {
            index += 1
        }
        guard index > nameStart, index < scalars.count, scalars[index] == ":" else { return nil }
        let after = index + 1
        guard after == scalars.count || [" ", "\t", "\n"].contains(scalars[after]) else { return nil }
        if nameStart > start { out.append(CodeToken(.plain, text(start, nameStart))) }
        out.append(CodeToken(.key, text(nameStart, index)))
        return index
    }
}
