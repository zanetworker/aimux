import Foundation

// The reference rules (README "References", "Scanning"). Each takes the text's scalars and a position and returns
// the match there, or nil. "Word character" is A-Z a-z 0-9 _.

private let notIssueWords: Set<String> = ["step", "item", "option", "line", "no.", "number"]
/// What `encodeURIComponent` leaves as is.
private let componentAllowed = CharacterSet(charactersIn:
    "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_.!~*'()")

private func isWord(_ char: Unicode.Scalar?) -> Bool { char.map(isWordScalar) ?? false }

private func isDigit(_ char: Unicode.Scalar) -> Bool { (0x30...0x39).contains(char.value) }
private func isUpper(_ char: Unicode.Scalar) -> Bool { (0x41...0x5A).contains(char.value) }
private func isLetter(_ char: Unicode.Scalar) -> Bool {
    (0x41...0x5A).contains(char.value) || (0x61...0x7A).contains(char.value)
}
private func isAlnum(_ char: Unicode.Scalar) -> Bool { isLetter(char) || isDigit(char) }
private func isSpace(_ char: Unicode.Scalar) -> Bool { char.properties.isWhitespace }

/// A-Z a-z 0-9 _ . @ + - /
private func isPathChar(_ char: Unicode.Scalar) -> Bool {
    switch char {
    case "_", ".", "@", "+", "-", "/": true
    default: isAlnum(char)
    }
}

/// What a path must not follow: a word character or / . - @ + ~ :
private func notBeforePath(_ char: Unicode.Scalar) -> Bool {
    char == "~" || char == ":" || isPathChar(char)
}

private func at(_ chars: [Unicode.Scalar], _ index: Int) -> Unicode.Scalar? {
    index >= 0 && index < chars.count ? chars[index] : nil
}

/// The next character is not a word character, or the text ends.
private func boundary(_ chars: [Unicode.Scalar], _ index: Int) -> Bool { !isWord(at(chars, index)) }

/// The run of digits at `index`, at most `limit` long.
private func digits(_ chars: [Unicode.Scalar], _ index: Int, limit: Int) -> Int {
    var end = index
    while end < chars.count, end - index < limit, isDigit(chars[end]) { end += 1 }
    return end - index
}

private func stripSlash(_ text: String) -> String {
    String(text.reversed().drop(while: { $0 == "/" }).reversed())
}

private func encodeSegments(_ path: String) -> String {
    path.split(separator: "/", omittingEmptySubsequences: false)
        .map { $0.addingPercentEncoding(withAllowedCharacters: componentAllowed) ?? String($0) }
        .joined(separator: "/")
}

extension ReferenceScanner {
    private func repoPath(_ part: String) -> String? {
        guard let repo = context.repo else { return nil }
        return stripSlash(repo.web) + (gitLab ? "/-/" : "/") + part
    }

    /// Issue starts: the text's start, whitespace or "(".
    private func issueStart(_ chars: [Unicode.Scalar], _ index: Int) -> Bool {
        index == 0 || chars[index - 1] == "(" || isSpace(chars[index - 1])
    }

    /// `#123`.
    func issue(_ chars: [Unicode.Scalar], _ index: Int) -> RefMatch? {
        guard chars[index] == "#", context.repo != nil, issueStart(chars, index) else { return nil }
        let count = digits(chars, index + 1, limit: 6)
        guard count > 0, boundary(chars, index + 1 + count) else { return nil }
        if index > 0 && chars[index - 1] != "(" && notIssueWords.contains(wordBefore(chars, index)) { return nil }
        let number = string(chars[index + 1..<index + 1 + count])
        return repoPath("issues/" + number).map { RefMatch(length: 1 + count, kind: .pr, href: $0) }
    }

    /// The whitespace-separated word before `index`, lowercased, leading ( [ " ' dropped; "" when long.
    private func wordBefore(_ chars: [Unicode.Scalar], _ index: Int) -> String {
        var end = index
        while end > 0 && isSpace(chars[end - 1]) { end -= 1 }
        var start = end
        while start > 0 && end - start <= 24 && !isSpace(chars[start - 1]) { start -= 1 }
        if start > 0 && !isSpace(chars[start - 1]) { return "" }
        return String(string(chars[start..<end]).drop(while: { "([\"'".contains($0) })).lowercased()
    }

    /// `owner/repo#123`.
    func otherRepo(_ chars: [Unicode.Scalar], _ index: Int) -> RefMatch? {
        guard let origin, issueStart(chars, index) else { return nil }
        guard let ownerEnd = repoName(chars, index), at(chars, ownerEnd) == "/",
              let nameEnd = repoName(chars, ownerEnd + 1), at(chars, nameEnd) == "#" else { return nil }
        let count = digits(chars, nameEnd + 1, limit: 6)
        let end = nameEnd + 1 + count
        guard count > 0, end - index <= 300, boundary(chars, end) else { return nil }
        let owner = string(chars[index..<ownerEnd])
        let name = string(chars[ownerEnd + 1..<nameEnd])
        let number = string(chars[nameEnd + 1..<end])
        let href = origin + "/" + owner + "/" + name + (gitLab ? "/-/" : "/") + "issues/" + number
        return RefMatch(length: end - index, kind: .pr, href: href)
    }

    /// The end of `[A-Za-z0-9][A-Za-z0-9_.-]*` at `index`, or nil.
    private func repoName(_ chars: [Unicode.Scalar], _ index: Int) -> Int? {
        guard let first = at(chars, index), isAlnum(first) else { return nil }
        var end = index + 1
        while end < chars.count, isAlnum(chars[end]) || "_.-".unicodeScalars.contains(chars[end]) { end += 1 }
        return end
    }

    /// 7 to 40 lowercase hex characters as a whole word, with a digit and a letter.
    func commit(_ chars: [Unicode.Scalar], _ index: Int) -> RefMatch? {
        guard context.repo != nil, !isWord(at(chars, index - 1)) else { return nil }
        var end = index
        while end < chars.count, end - index < 40, isDigit(chars[end]) || ("a"..."f").contains(chars[end]) {
            end += 1
        }
        let sha = chars[index..<end]
        guard sha.count >= 7, boundary(chars, end), sha.contains(where: isDigit),
              sha.contains(where: { !isDigit($0) }) else { return nil }
        return repoPath("commit/" + string(sha)).map { RefMatch(length: sha.count, kind: .commit, href: $0) }
    }

    /// `KEY-123` whose project is in `jiraProjects`.
    func jira(_ chars: [Unicode.Scalar], _ index: Int) -> RefMatch? {
        guard let base = context.jiraBase, !context.jiraProjects.isEmpty, !isWord(at(chars, index - 1)),
              isUpper(chars[index]) else { return nil }
        var dash = index + 1
        while dash < chars.count, isUpper(chars[dash]) || isDigit(chars[dash]) { dash += 1 }
        guard dash > index + 1, at(chars, dash) == "-" else { return nil }
        let count = digits(chars, dash + 1, limit: Int.max)
        let end = dash + 1 + count
        guard count > 0, end - index <= 64, boundary(chars, end),
              context.jiraProjects.contains(string(chars[index..<dash])) else { return nil }
        let key = string(chars[index..<end])
        return RefMatch(length: end - index, kind: .jira, href: stripSlash(base) + "/browse/" + key)
    }

    /// A file path with an extension, with an optional `:line`.
    func path(_ chars: [Unicode.Scalar], _ index: Int) -> RefMatch? {
        guard let repo = context.repo, isPathChar(chars[index]) else { return nil }
        if index > 0 && notBeforePath(chars[index - 1]) { return nil }
        var end = index
        while end < chars.count, isPathChar(chars[end]) { end += 1 }
        guard chars[index..<end].contains("/") else { return nil }
        let run = string(chars[index..<end])
        let text = String(run.reversed().drop(while: { $0 == "." }).reversed())
        guard text.contains("/"), hasExtension(text), let rel = relativePath(text, repo: repo) else { return nil }
        var length = text.unicodeScalars.count
        var line = ""
        if text.count == run.count, at(chars, index + length) == ":" {
            let count = digits(chars, index + length + 1, limit: 6)
            if count > 0, boundary(chars, index + length + 1 + count) {
                line = "#L" + string(chars[index + length + 1..<index + length + 1 + count])
                length += 1 + count
            }
        }
        let href = repoPath("blob/" + encodeSegments(repo.branch) + "/" + encodeSegments(rel)).map { $0 + line }
        return href.map { RefMatch(length: length, kind: .path, href: $0) }
    }

    /// `.` then a letter and up to 9 letters or digits, at the end.
    private func hasExtension(_ text: String) -> Bool {
        guard let dot = text.lastIndex(of: ".") else { return false }
        let ext = Array(text[text.index(after: dot)...].unicodeScalars)
        guard let first = ext.first, isLetter(first), ext.count <= 10 else { return false }
        return ext.allSatisfy(isAlnum)
    }

    /// The repository-relative path, or nil when it is outside the repository or not a file path.
    private func relativePath(_ text: String, repo: SessionRepo) -> String? {
        var rel = text
        if rel.hasPrefix("/") {
            guard let base = repo.root ?? context.project, !base.isEmpty else { return nil }
            let prefix = stripSlash(base) + "/"
            guard rel.hasPrefix(prefix) else { return nil }
            rel = String(rel.dropFirst(prefix.count))
        } else if rel.hasPrefix("./") {
            rel = String(rel.dropFirst(2))
        }
        let segments = rel.split(separator: "/", omittingEmptySubsequences: false)
        if segments.contains(where: { $0.isEmpty || $0 == ".." }) { return nil }
        if segments.count > 1, let dot = segments[0].firstIndex(of: "."), dot > segments[0].startIndex { return nil }
        return rel
    }
}
