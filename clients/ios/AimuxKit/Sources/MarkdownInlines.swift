import Foundation

// Foundation's inline runs to the tree's inlines (api/testdata/clients/README.md, "Inline rules"): the runs become
// leaves (text, code, link, br) each carrying its emphasis styles, links survive only for http(s) and never for
// Foundation's own autolinks (the README's bare URL pass finds those again, the same way the web client does), and
// the leaves are rebuilt with the canonical nesting.

/// One run of Foundation's parse: its text and the attributes the tree needs.
struct InlineRun {
    let text: String
    let intent: InlinePresentationIntent
    let link: URL?
    /// An image's source; its text is the alt text.
    let image: URL?

    var isImage: Bool { image != nil }

    init(text: String, intent: InlinePresentationIntent = [], link: URL? = nil, image: URL? = nil) {
        self.text = text
        self.intent = intent
        self.link = link
        self.image = image
    }

    init(_ run: AttributedString.Runs.Run, text: String) {
        self.init(text: text, intent: run.inlinePresentationIntent ?? [], link: run.link, image: run.imageURL)
    }

    /// The emphasis styles as `Leaf` bits.
    var styles: UInt8 {
        var mask: UInt8 = 0
        if intent.contains(.emphasized) { mask |= Leaf.emphasized }
        if intent.contains(.stronglyEmphasized) { mask |= Leaf.bold }
        if intent.contains(.strikethrough) { mask |= Leaf.struck }
        return mask
    }
}

/// A text, code, link or br inline with the emphasis styles around it.
struct Leaf {
    static let emphasized: UInt8 = 1
    static let bold: UInt8 = 2
    static let struck: UInt8 = 4
    /// Outermost first on a tie: em, strong, del.
    static let order: [UInt8] = [emphasized, bold, struck]

    var node: MDInline
    var styles: UInt8
}

/// A paragraph that is only one image with an http(s) source is that image as a link, so the media pass can make
/// it a card (README, "Links"); the alt text is the link's text, or the source when it is empty. Nil otherwise,
/// and for an image inside a link (a linked thumbnail), where the link decides.
func imageOnlyLink(_ runs: [InlineRun]) -> [MDInline]? {
    guard let source = runs.first?.image,
          runs.allSatisfy({ $0.image == source && $0.link == nil && $0.styles == 0 }) else {
        return nil
    }
    let href = source.absoluteString
    let lower = href.lowercased()
    guard lower.hasPrefix("http:") || lower.hasPrefix("https:") else { return nil }
    let alt = runs.map(\.text).joined().replacingOccurrences(of: "\u{FFFC}", with: "")
    return [.link(href: href, content: [.text(alt.isEmpty ? href : alt)])]
}

/// Runs to normalized inlines.
func normalizeInlines(_ runs: [InlineRun], breaks: Bool) -> [MDInline] {
    var leaves: [Leaf] = []
    var index = 0
    while index < runs.count {
        if let url = runs[index].link {
            var end = index + 1
            while end < runs.count, runs[end].link == url { end += 1 }
            appendLink(url, runs[index..<end], breaks: breaks, to: &leaves)
            index = end
        } else {
            appendPlain(runs[index], breaks: breaks, to: &leaves)
            index += 1
        }
    }
    var split: [Leaf] = []
    for leaf in leaves {
        if case .text(let value) = leaf.node, let parts = bareURLs(value) {
            split += parts.map { Leaf(node: $0, styles: leaf.styles) }
        } else {
            split.append(leaf)
        }
    }
    return rebuild(split, from: 0, to: split.count, open: 0)
}

private func appendPlain(_ run: InlineRun, breaks: Bool, to leaves: inout [Leaf]) {
    let node: MDInline
    if run.intent.contains(.softBreak) {
        node = breaks ? .br : .text(" ")
    } else if run.intent.contains(.lineBreak) {
        node = .br
    } else if run.isImage {
        node = .text(run.text.replacingOccurrences(of: "\u{FFFC}", with: ""))
    } else if run.intent.contains(.code) {
        node = .code(run.text)
    } else {
        node = .text(run.text)
    }
    appendLeaf(Leaf(node: node, styles: run.styles), to: &leaves)
}

/// Joins a text to the text before it when both have the same styles; drops empty text.
private func appendLeaf(_ leaf: Leaf, to leaves: inout [Leaf]) {
    guard case .text(let value) = leaf.node else {
        leaves.append(leaf)
        return
    }
    if value.isEmpty { return }
    if let last = leaves.last, last.styles == leaf.styles, case .text(let before) = last.node {
        leaves[leaves.count - 1].node = .text(before + value)
    } else {
        leaves.append(leaf)
    }
}

/// A link's runs: one link leaf with its text flattened, for an http(s) link written as a link; otherwise the
/// runs as ordinary inlines (other schemes, and Foundation's autolinks of bare URLs, `www.` and emails).
private func appendLink(_ url: URL, _ runs: ArraySlice<InlineRun>, breaks: Bool, to leaves: inout [Leaf]) {
    let href = url.absoluteString
    let text = runs.map { $0.intent.contains(.lineBreak) ? " " : $0.text }.joined()
    let lower = href.lowercased()
    let web = lower.hasPrefix("http:") || lower.hasPrefix("https:")
    let auto = text.lowercased() == lower || href.removingPercentEncoding == text || href == "http://" + text
    guard web && !auto else {
        for run in runs {
            // Foundation's autolinks keep entities as written; the README decodes them before finding bare URLs.
            let plain = auto && run.text.contains("&") ? InlineRun(text: decodeEntities(run.text), intent: run.intent)
                : run
            appendPlain(plain, breaks: breaks, to: &leaves)
        }
        return
    }
    let styles = runs.reduce(UInt8(7)) { $0 & $1.styles }
    leaves.append(Leaf(node: .link(href: href, content: [.text(text)]), styles: styles))
}

// MARK: Entities

private let namedEntities = ["amp": "&", "lt": "<", "gt": ">", "quot": "\"", "apos": "'", "nbsp": "\u{A0}"]

/// `&amp;`, `&#35;` and `&#x41;` decoded (the names a URL holds in practice); anything else stays as written.
func decodeEntities(_ text: String) -> String {
    var out = ""
    var rest = Substring(text)
    while let amp = rest.firstIndex(of: "&") {
        out += rest[..<amp]
        rest = rest[amp...]
        guard let semi = rest.prefix(34).firstIndex(of: ";"),
              let decoded = entity(String(rest[rest.index(after: rest.startIndex)..<semi])) else {
            out += "&"
            rest = rest.dropFirst()
            continue
        }
        out += decoded
        rest = rest[rest.index(after: semi)...]
    }
    return out + rest
}

private func entity(_ name: String) -> String? {
    if let named = namedEntities[name] { return named }
    guard name.hasPrefix("#") else { return nil }
    let number = name.dropFirst()
    let code = number.first == "x" || number.first == "X" ? UInt32(number.dropFirst(), radix: 16) : UInt32(number)
    guard let code, !number.isEmpty else { return nil }
    return String(Unicode.Scalar(code) ?? "\u{FFFD}")
}

// MARK: Bare URLs

private let urlTrail: Set<Unicode.Scalar> = [".", ",", ";", ":", "!", "?", "*", "_", "~", "'", "\""]

/// The text split into text and link pieces at bare http(s) URLs; nil when it has none.
func bareURLs(_ value: String) -> [MDInline]? {
    let chars = Array(value.unicodeScalars)
    var out: [MDInline] = []
    var from = 0
    var index = 0
    while index < chars.count {
        guard let prefix = schemePrefix(chars, at: index),
              index == 0 || !isASCIILetter(chars[index - 1]) else {
            index += 1
            continue
        }
        let end = urlEnd(chars, start: index, prefix: prefix)
        guard end > index + prefix else {
            index += 1
            continue
        }
        if index > from { out.append(.text(String(String.UnicodeScalarView(chars[from..<index])))) }
        let url = String(String.UnicodeScalarView(chars[index..<end]))
        out.append(.link(href: url, content: [.text(url)]))
        from = end
        index = end
    }
    guard from > 0 else { return nil }
    if from < chars.count { out.append(.text(String(String.UnicodeScalarView(chars[from...])))) }
    return out
}

/// The length of "http://" or "https://" (any case) at `index`, or nil.
private func schemePrefix(_ chars: [Unicode.Scalar], at index: Int) -> Int? {
    guard chars[index] == "h" || chars[index] == "H" else { return nil }
    for scheme in ["http://", "https://"] {
        let pattern = Array(scheme.unicodeScalars)
        guard index + pattern.count <= chars.count else { continue }
        let matches = zip(chars[index..<index + pattern.count], pattern).allSatisfy {
            Character($0).lowercased() == String($1)
        }
        if matches { return pattern.count }
    }
    return nil
}

/// Where the URL starting at `start` ends: at whitespace or `<`, less trailing punctuation and unbalanced `)`.
private func urlEnd(_ chars: [Unicode.Scalar], start: Int, prefix: Int) -> Int {
    var end = start + prefix
    var opens = 0
    var closes = 0
    while end < chars.count, !chars[end].properties.isWhitespace, chars[end] != "<" {
        if chars[end] == "(" { opens += 1 } else if chars[end] == ")" { closes += 1 }
        end += 1
    }
    while end > start + prefix {
        let last = chars[end - 1]
        if urlTrail.contains(last) {
            end -= 1
        } else if last == ")" && closes > opens {
            end -= 1
            closes -= 1
        } else {
            break
        }
    }
    return end
}

private func isASCIILetter(_ char: Unicode.Scalar) -> Bool {
    ("a"..."z").contains(char) || ("A"..."Z").contains(char)
}

// MARK: Canonical nesting

/// At each position, of the styles not yet open, the one whose run of leaves reaches furthest opens outermost
/// (ties: em, strong, del); inside it the same rule applies; texts are merged again.
func rebuild(_ leaves: [Leaf], from: Int, to end: Int, open: UInt8) -> [MDInline] {
    var out: [MDInline] = []
    var index = from
    while index < end {
        let remaining = leaves[index].styles & ~open
        guard remaining != 0 else {
            out.append(leaves[index].node)
            index += 1
            continue
        }
        var best: UInt8 = 0
        var bestEnd = -1
        for bit in Leaf.order where remaining & bit != 0 {
            var stop = index
            while stop < end, leaves[stop].styles & bit != 0 { stop += 1 }
            if stop > bestEnd { (best, bestEnd) = (bit, stop) }
        }
        let inner = rebuild(leaves, from: index, to: bestEnd, open: open | best)
        switch best {
        case Leaf.emphasized: out.append(.em(inner))
        case Leaf.bold: out.append(.strong(inner))
        default: out.append(.del(inner))
        }
        index = bestEnd
    }
    return mergeTexts(out)
}
