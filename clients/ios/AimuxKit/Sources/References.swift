import Foundation

// The references pass (api/testdata/clients/README.md, "References"): issue and pull request numbers, commits,
// Jira keys and file paths in a parsed Markdown tree become `.ref` inlines linking to the session's repository or
// Jira. The visible text never changes. A port of the web client's references.mjs, rule for rule.

/// The repository a session's folder belongs to (`SessionDetail.repo`).
public struct SessionRepo: Decodable, Hashable, Sendable {
    /// The https address of the repository.
    public let web: String
    public let branch: String
    /// The repository's top folder on the Mac; absent from older servers.
    public let root: String?

    public init(web: String, branch: String, root: String? = nil) {
        self.web = web
        self.branch = branch
        self.root = root
    }
}

/// Jira settings from `/v1/health` (`links`).
public struct HealthLinks: Decodable, Equatable, Sendable {
    public let jiraBase: String
    public let jiraProjects: [String]

    public init(jiraBase: String, jiraProjects: [String]) {
        self.jiraBase = jiraBase
        self.jiraProjects = jiraProjects
    }
}

/// What the references pass knows about a session.
public struct ReferenceContext: Hashable, Sendable {
    public let repo: SessionRepo?
    public let jiraBase: String?
    public let jiraProjects: [String]
    /// The session's folder, below `repo.root` when started in a subfolder.
    public let project: String?

    public static let none = ReferenceContext(repo: nil, jiraBase: nil, jiraProjects: [], project: nil)

    public init(repo: SessionRepo?, jiraBase: String?, jiraProjects: [String], project: String?) {
        self.repo = repo
        self.jiraBase = jiraBase
        self.jiraProjects = jiraProjects
        self.project = project
    }

    /// From a session's detail (its repo and project) and the server's health links.
    public init(session: SessionDetail?, links: HealthLinks?) {
        self.init(repo: session?.repo, jiraBase: links?.jiraBase, jiraProjects: links?.jiraProjects ?? [],
                  project: session?.project)
    }

    var isEmpty: Bool { repo == nil && jiraBase == nil }
}

/// The blocks with references linked in every paragraph, heading and table cell.
public func linkReferences(_ blocks: [MDBlock], context: ReferenceContext) -> [MDBlock] {
    guard !context.isEmpty else { return blocks }
    let scanner = ReferenceScanner(context: context)
    return blocks.map { scanner.block($0) }
}

/// One inline list with references linked.
public func linkReferences(_ inlines: [MDInline], context: ReferenceContext) -> [MDInline] {
    guard !context.isEmpty else { return inlines }
    return ReferenceScanner(context: context).inlines(inlines)
}

/// A match at a position: its length in scalars, kind and target.
struct RefMatch {
    let length: Int
    let kind: RefKind
    let href: String
}

struct ReferenceScanner {
    let context: ReferenceContext
    let gitLab: Bool
    /// "https://host[:port]" of the repository.
    let origin: String?

    init(context: ReferenceContext) {
        self.context = context
        let url = context.repo.flatMap { URL(string: $0.web) }
        gitLab = url?.host?.contains("gitlab") ?? false
        if let url, let scheme = url.scheme, let host = url.host {
            origin = scheme + "://" + host + (url.port.map { ":\($0)" } ?? "")
        } else {
            origin = nil
        }
    }

    func block(_ block: MDBlock) -> MDBlock {
        switch block {
        case .paragraph(let list): .paragraph(inlines(list))
        case .heading(let level, let list): .heading(level: level, inlines: inlines(list))
        case .table(let table):
            .table(MDTable(align: table.align, header: table.header.map(inlines),
                           rows: table.rows.map { $0.map(inlines) }))
        case .list(let list):
            .list(MDList(ordered: list.ordered, start: list.start, items: list.items.map {
                MDListItem(task: $0.task, blocks: $0.blocks.map(self.block))
            }))
        case .quote(let inner): .quote(inner.map(self.block))
        case .code, .rule, .media: block
        }
    }

    func inlines(_ list: [MDInline]) -> [MDInline] {
        var out: [MDInline] = []
        for inline in list {
            switch inline {
            case .text(let value): out += scan(value)
            case .em(let inner): out.append(.em(inlines(inner)))
            case .strong(let inner): out.append(.strong(inlines(inner)))
            case .del(let inner): out.append(.del(inlines(inner)))
            case .code(let value):
                let chars = Array(value.unicodeScalars)
                if !chars.isEmpty, let match = path(chars, 0), match.length == chars.count {
                    out.append(.ref(kind: .path, text: value, href: match.href, code: true))
                } else {
                    out.append(inline)
                }
            default: out.append(inline)
            }
        }
        return out
    }

    /// One text split into text and refs: at each position the longest match (ties: rule order).
    func scan(_ value: String) -> [MDInline] {
        let chars = Array(value.unicodeScalars)
        var out: [MDInline] = []
        var from = 0
        var index = 0
        while index < chars.count {
            guard let best = longestMatch(chars, index) else {
                index += 1
                continue
            }
            if index > from { out.append(.text(string(chars[from..<index]))) }
            out.append(.ref(kind: best.kind, text: string(chars[index..<index + best.length]), href: best.href,
                            code: false))
            index += best.length
            from = index
        }
        if from == 0 { return [.text(value)] }
        if from < chars.count { out.append(.text(string(chars[from...]))) }
        return out
    }
}

extension ReferenceScanner {
    /// The longest match at `index` (ties: the rules' order). Every rule but the issue number needs a start that
    /// does not follow a word character, so most positions inside words are skipped at once.
    func longestMatch(_ chars: [Unicode.Scalar], _ index: Int) -> RefMatch? {
        if chars[index] == "#" { return issue(chars, index) }
        if index > 0 && isWordScalar(chars[index - 1]) { return nil }
        var best = otherRepo(chars, index)
        for match in [commit(chars, index), jira(chars, index), path(chars, index)] {
            if let match, match.length > (best?.length ?? 0) { best = match }
        }
        return best
    }
}

/// A-Z a-z 0-9 _.
func isWordScalar(_ char: Unicode.Scalar) -> Bool {
    switch char.value {
    case 0x30...0x39, 0x41...0x5A, 0x61...0x7A, 0x5F: true
    default: false
    }
}

func string(_ chars: ArraySlice<Unicode.Scalar>) -> String {
    String(String.UnicodeScalarView(chars))
}
