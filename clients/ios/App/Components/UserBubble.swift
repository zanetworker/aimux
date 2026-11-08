import SwiftUI
import UIKit
import AimuxKit

/// A message from the history or the events, without Claude Code's wrappers (AimuxKit's `userMessageDisplay`);
/// nothing is shown when nothing is left.
struct SavedUserMessage: View {
    let text: String

    var body: some View {
        let display = userMessageDisplay(text)
        if !display.parts.isEmpty || !display.images.isEmpty {
            UserBubble(text: display.text, images: display.images, parts: display.parts)
        }
    }
}

/// Your message: a right-aligned near-black bubble (dark grey in dark mode). Inline Markdown only, like the web
/// client: typed lines stay lines, a list keeps its markers as text, pasted code reads as code. Images Claude Code
/// saved as placeholders show as small "Image" chips above the text. `parts`, when given, replace `text`: a slash
/// command in monospace with its args as plain text, a command's output as a plain monospace block.
struct UserBubble: View {
    let text: String
    var images: [String] = []
    var parts: [UserMessagePart]?
    @Environment(\.referenceContext) private var references

    static let fill = Color(UIColor { traits in
        traits.userInterfaceStyle == .dark ? UIColor(white: 0.23, alpha: 1) : UIColor(white: 0.11, alpha: 1)
    })

    var body: some View {
        content
            .foregroundStyle(.white)
            .tint(.white)
            .environment(\.openURL, OpenURLAction { url in isWebLink(url.absoluteString) ? .systemAction : .discarded })
            .padding(.horizontal, 14)
            .padding(.vertical, 10)
            .background(Self.fill, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .frame(maxWidth: .infinity, alignment: .trailing)
            .padding(.leading, 48)
            .textSelection(.enabled)
    }

    private var shownParts: [UserMessagePart] { parts ?? (text.isEmpty ? [] : [.text(text)]) }

    @ViewBuilder private var content: some View {
        if images.isEmpty, shownParts.count == 1, case .text(let only) = shownParts[0] {
            message(only)
        } else {
            VStack(alignment: .leading, spacing: 8) {
                if !images.isEmpty {
                    FlowLayout(spacing: 6) {
                        ForEach(Array(images.enumerated()), id: \.offset) { _, label in chip(label) }
                    }
                }
                ForEach(Array(shownParts.enumerated()), id: \.offset) { _, part in partView(part) }
            }
        }
    }

    @ViewBuilder private func partView(_ part: UserMessagePart) -> some View {
        switch part {
        case .text(let text):
            message(text)
        case .command(let name, let args):
            Text(commandLine(name, args))
                .environment(\.layoutDirection, .leftToRight)
        case .output(let output, let error):
            VStack(alignment: .leading, spacing: 4) {
                if error {
                    Text("Error output")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.white.opacity(0.7))
                }
                Text(output)
                    .font(.system(.footnote, design: .monospaced))
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(.white.opacity(0.1), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
            .environment(\.layoutDirection, .leftToRight)
        }
    }

    /// The command in monospace, then its args as plain text (never Markdown).
    private func commandLine(_ name: String, _ args: String) -> AttributedString {
        var line = AttributedString(name)
        line.font = .body.monospaced()
        if !args.isEmpty { line += AttributedString(" " + args) }
        return line
    }

    private func chip(_ label: String) -> some View {
        HStack(spacing: 4) {
            Image(systemName: "photo")
            Text(label)
        }
        .font(.footnote)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .background(.white.opacity(0.16), in: Capsule())
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private func message(_ text: String) -> some View {
        if let blocks = MarkdownRender.bubble(text, context: references) {
            VStack(alignment: .leading, spacing: 8) {
                ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in paragraph(block) }
            }
        } else {
            Text(text)
        }
    }

    /// Only a fenced block is a code box; a code span stays inline, as on the web.
    @ViewBuilder private func paragraph(_ block: BubbleBlock) -> some View {
        switch block {
        case .code(let code):
            Text(code)
                .font(.system(.footnote, design: .monospaced))
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(.white.opacity(0.1), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .environment(\.layoutDirection, .leftToRight)
        case .paragraph(let inlines):
            Text(InlineStyle.attributed(inlines, codeFill: .white.opacity(0.14), codeInk: CodeColors.inlineInkOnDark,
                                        underlineLinks: true))
                .environment(\.layoutDirection, isRightToLeft(inlines) ? .rightToLeft : .leftToRight)
        }
    }
}

/// A chat item's text (a thought, a plan entry, an error): inline-only Markdown with references, as in a bubble,
/// in the surrounding font and colour; links take the link tint.
struct InlineMarkdown: View {
    let text: String
    @Environment(\.referenceContext) private var references

    var body: some View {
        Group {
            if let blocks = MarkdownRender.bubble(text, context: references) {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in paragraph(block) }
                }
            } else {
                Text(text)
            }
        }
        .tint(Color(.link))
        .environment(\.openURL, OpenURLAction { url in isWebLink(url.absoluteString) ? .systemAction : .discarded })
    }

    @ViewBuilder private func paragraph(_ block: BubbleBlock) -> some View {
        switch block {
        case .code(let code):
            Text(code)
                .font(.system(.footnote, design: .monospaced))
                .environment(\.layoutDirection, .leftToRight)
        case .paragraph(let inlines):
            Text(InlineStyle.attributed(inlines, codeFont: nil))
                .fixedSize(horizontal: false, vertical: true)
                .environment(\.layoutDirection, isRightToLeft(inlines) ? .rightToLeft : .leftToRight)
        }
    }
}
