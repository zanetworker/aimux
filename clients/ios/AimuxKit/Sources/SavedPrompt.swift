import Foundation

// A user message as Claude Code saved it: its wrappers, commands, output and image placeholders.

private let workingPrefix = "The user sent a new message while you were working:"

/// A prompt as Claude Code saved it, without the wrappers it adds: the prefix of a message typed while it
/// worked, and the markers around a long paste (the pasted text stays).
public func unwrapSavedPrompt(_ saved: String) -> String {
    var text = saved.trimmingCharacters(in: .whitespacesAndNewlines)
    if text.hasPrefix(workingPrefix) { text.removeFirst(workingPrefix.count) }
    text.replace(/<\/?pasted_content\b[^>]*>/, with: "")
    return text.trimmingCharacters(in: .whitespacesAndNewlines)
}

/// One piece of a saved user message, in order: text for the bubble renderer, a slash command (`/tidy`) or bash-mode
/// input (`!ls`) with its args, or a command's output (`error` for stderr).
public enum UserMessagePart: Hashable, Sendable {
    case text(String)
    case command(String, args: String)
    case output(String, error: Bool)
}

/// How a saved user message shows in its bubble: its parts, the text parts joined by a blank line, and one chip
/// label per image ("Image 2", or "Image" when it has no number), in order.
public struct UserMessageDisplay: Hashable, Sendable {
    public let text: String
    public let images: [String]
    public let parts: [UserMessagePart]

    public init(text: String, images: [String], parts: [UserMessagePart]) {
        self.text = text
        self.images = images
        self.parts = parts
    }
}

private func trimmed(_ text: some StringProtocol) -> String {
    text.trimmingCharacters(in: .whitespacesAndNewlines)
}

/// An unwrapped message split into text runs (as saved, untrimmed), commands and outputs. An unclosed or unknown tag
/// stays text; what is between two tags is never searched for more. Pinned by
/// api/testdata/clients/display/user-bubble.json.
func savedParts(_ message: String) -> [UserMessagePart] {
    var parts: [UserMessagePart] = []
    var run = ""
    var argsFor: Int? // the command from <command-name> that may still take <command-args>
    func endRun() {
        if !trimmed(run).isEmpty { parts.append(.text(run)) }
        run = ""
        argsFor = nil
    }
    // The tags Claude Code wraps a slash command, bash mode and their output in, written exactly so.
    let commandTag = #/
        <(command-name|command-message|command-args|local-command-stdout|local-command-stderr|command-stdout
        |command-stderr|bash-input|bash-stdout|bash-stderr|local-command-caveat)>
        /#
    var pos = message.startIndex
    var from = message.startIndex
    while from < message.endIndex, let match = message[from...].firstMatch(of: commandTag) {
        let tag = String(match.output.1)
        guard let close = message[match.range.upperBound...].range(of: "</\(tag)>") else {
            from = match.range.upperBound
            continue
        }
        let body = message[match.range.upperBound..<close.lowerBound]
        run += message[pos..<match.range.lowerBound]
        pos = close.upperBound
        from = pos
        switch tag {
        case "command-message", "local-command-caveat":
            break
        case "command-args":
            if let index = argsFor, trimmed(run).isEmpty, case .command(let name, _) = parts[index] {
                parts[index] = .command(name, args: trimmed(body))
                argsFor = nil
                run = ""
            } else {
                run += body
            }
        case "command-name", "bash-input":
            let name = trimmed(body)
            if name.isEmpty { break }
            endRun()
            parts.append(.command(tag == "bash-input" ? "!" + name : name, args: ""))
            if tag == "command-name" { argsFor = parts.count - 1 }
        default:
            let output = trimmed(String(body).replacing(/\x1B\[[0-9;]*m/, with: ""))
            if output.isEmpty { break }
            endRun()
            parts.append(.output(output, error: tag.hasSuffix("stderr")))
        }
    }
    run += message[pos...]
    endRun()
    return parts
}

/// A saved prompt as the person typed it: unwrapped, each command as its name and args ("/tidy docs only",
/// "!git status"), outputs left out.
public func savedPromptAsTyped(_ saved: String) -> String {
    savedParts(unwrapSavedPrompt(saved)).compactMap { part -> String? in
        switch part {
        case .text(let text): text
        case .command(let name, let args): args.isEmpty ? name : name + " " + args
        case .output: nil
        }
    }.joined(separator: "\n")
}

/// A text run with each image placeholder (`[Image #n]`, `[Image: …]`) and the spaces and tabs after it taken out
/// and its chip label added to `images`; a line left blank by that is dropped.
private func withoutImages(_ text: String, _ images: inout [String]) -> String {
    var lines: [String] = []
    for line in text.split(separator: "\n", omittingEmptySubsequences: false) {
        var found = false
        var rest = String(line).replacing(/\[Image(?: #([0-9]+)|:[^\]\n]*)\][ \t]*/) { match in
            found = true
            images.append(match.output.1.map { "Image \($0)" } ?? "Image")
            return ""
        }
        guard found else {
            lines.append(String(line))
            continue
        }
        while let last = rest.last, last == " " || last == "\t" { rest.removeLast() }
        if !rest.isEmpty { lines.append(rest) }
    }
    return trimmed(lines.joined(separator: "\n"))
}

/// The message unwrapped as above, split into parts, with the image placeholders taken out of its text. Pinned by
/// api/testdata/clients/display/user-bubble.json.
public func userMessageDisplay(_ saved: String) -> UserMessageDisplay {
    var images: [String] = []
    var parts: [UserMessagePart] = []
    for part in savedParts(unwrapSavedPrompt(saved)) {
        guard case .text(let raw) = part else {
            parts.append(part)
            continue
        }
        let text = withoutImages(raw, &images)
        if !text.isEmpty { parts.append(.text(text)) }
    }
    let texts = parts.compactMap { part -> String? in
        if case .text(let text) = part { return text }
        return nil
    }
    return UserMessageDisplay(text: texts.joined(separator: "\n\n"), images: images, parts: parts)
}

/// Whether a history prompt is the message the person sent, once Claude Code's wrappers are removed and its
/// commands are in the form they were typed.
public func pendingShownInHistory(_ history: String, _ text: String) -> Bool {
    samePrompt(savedPromptAsTyped(history), text)
}
