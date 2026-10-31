import SwiftUI
import AimuxKit

/// Syntax colours for code blocks (`highlight`) and the look of inline code, light and dark; GitHub-like, the same
/// palette as the web client's `.hl-*` classes.
enum CodeColors {
    private static func dynamic(light: UInt32, dark: UInt32, alpha: CGFloat = 1) -> Color {
        Color(UIColor { traits in
            let hex = traits.userInterfaceStyle == .dark ? dark : light
            return UIColor(red: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255,
                           blue: CGFloat(hex & 0xFF) / 255, alpha: alpha)
        })
    }

    /// The text colour of a token; nil keeps the code block's own colour.
    static func ink(_ kind: CodeTokenKind) -> Color? {
        switch kind {
        case .plain: nil
        case .keyword: dynamic(light: 0xCF222E, dark: 0xFF7B72)
        case .string: dynamic(light: 0x0A3069, dark: 0xA5D6FF)
        case .comment, .meta: dynamic(light: 0x6E7781, dark: 0x8B949E)
        case .number: dynamic(light: 0x0550AE, dark: 0x79C0FF)
        case .function: dynamic(light: 0x8250DF, dark: 0xD2A8FF)
        case .key, .added: dynamic(light: 0x116329, dark: 0x7EE787)
        case .removed: dynamic(light: 0x82071E, dark: 0xFFA198)
        }
    }

    /// The background of a diff line; nil for every other token.
    static func fill(_ kind: CodeTokenKind) -> Color? {
        switch kind {
        case .added: Color.green.opacity(0.15)
        case .removed: Color.red.opacity(0.15)
        default: nil
        }
    }

    /// Inline code: its text colour and tint, so a path or name reads as code in a sentence.
    static let inlineInk = dynamic(light: 0x953800, dark: 0xFFA657)
    static let inlineFill = dynamic(light: 0xFF8C00, dark: 0xFFA657, alpha: 0.13)
    /// Inline code inside the dark user bubble, in either mode.
    static let inlineInkOnDark = Color(red: 1, green: 0.65, blue: 0.34)

    /// `code` in `lang` as coloured text for a code block.
    static func attributed(_ code: String, lang: String?) -> AttributedString {
        var out = AttributedString()
        for token in highlight(code, lang: lang) {
            var piece = AttributedString(token.text)
            if let ink = ink(token.kind) { piece.foregroundColor = ink }
            if let fill = fill(token.kind) { piece.backgroundColor = fill }
            if token.kind == .comment { piece.inlinePresentationIntent = .emphasized }
            out += piece
        }
        return out
    }
}
