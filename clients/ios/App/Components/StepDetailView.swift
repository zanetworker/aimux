import SwiftUI
import UIKit
import AimuxKit

/// An opened step (the web client's stepDetail): the target's links, the input in monospace (a command after a
/// secondary `$`), the error under a red "Failed", then an edit's removed and added lines, at most 40, then
/// "N more lines". Text is capped as drawn (ShownStepDetail). Code reads left to right, whatever the language
/// around it.
struct StepDetailView: View {
    let step: Step
    /// The links taken out of the row's target; http(s) only.
    let links: [StepLink]
    @ScaledMetric(relativeTo: .footnote) private var monoSize: CGFloat = 12.5
    @ScaledMetric(relativeTo: .footnote) private var gutter: CGFloat = 20

    static let diffLimit = 40

    var body: some View {
        let detail = step.detail
        let shown = ShownStepDetail(detail, diffLimit: Self.diffLimit)
        // An edit's input is its path; when the row already shows that path, the lines say enough.
        let repeats = step.kind == .edit && detail.input == step.target && !shown.lines.isEmpty
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(links.enumerated()), id: \.offset) { _, link in
                if let url = URL(string: link.href), isWebLink(link.href) {
                    Link(destination: url) {
                        Text(link.text).multilineTextAlignment(.leading)
                    }
                    .foregroundStyle(Color(.link))
                }
            }
            if !shown.input.isEmpty && !repeats {
                input(shown.input).overlay(alignment: .topTrailing) { CopyIcon(text: detail.input) }
            }
            if !shown.error.isEmpty {
                error(shown.error).overlay(alignment: .topTrailing) { CopyIcon(text: detail.error) }
            }
            if !shown.lines.isEmpty { lines(shown.lines, hidden: shown.hidden) }
        }
        .font(.system(size: monoSize, design: .monospaced))
        .frame(maxWidth: .infinity, alignment: .leading)
        .environment(\.layoutDirection, .leftToRight)
        .textSelection(.enabled)
    }

    private func input(_ text: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 0) {
            if step.kind == .run {
                Text("$ ").foregroundStyle(.secondary).accessibilityHidden(true)
            }
            // A command reads as shell, with its colours; other inputs (a path, a pattern) stay plain.
            Text(CodeColors.attributed(Self.shown(text), lang: step.kind == .run ? "sh" : nil))
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.leading, 10)
        .padding(.trailing, 40)  // room for the Copy icon
        .padding(.vertical, 8)
        .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
    }

    private func error(_ text: String) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text("Failed")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.red)
            Text(Self.shown(text))
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.trailing, 40)  // room for the Copy icon
    }

    private func lines(_ lines: [DiffLine], hidden: Int) -> some View {
        // The lines take the colours of the file's language, on their red and green.
        let lang = languageForPath(step.target) ?? languageForPath(step.detail.input)
        return VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                HStack(alignment: .firstTextBaseline, spacing: 0) {
                    Text(line.kind == .removed ? "-" : "+")
                        .foregroundStyle(.secondary)
                        .frame(width: gutter)
                        .accessibilityHidden(true)
                    Text(CodeColors.attributed(Self.shown(line.text), lang: lang))
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(.trailing, 10)
                .background(line.kind == .removed ? Color.diffRemoved : Color.diffAdded)
                .accessibilityElement(children: .combine)
                .accessibilityLabel((line.kind == .removed ? "Removed: " : "Added: ") + line.text)
            }
            if hidden > 0 {
                Text(hidden == 1 ? "1 more line" : "\(hidden) more lines")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 4)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color(.tertiarySystemFill))
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
    }

    /// Text as drawn: a tab is four columns, not the eight a text view would give it.
    static func shown(_ text: String) -> String {
        text.replacing("\t", with: "    ")
    }
}

/// Copies a step's whole command or error (not just the part shown); "Copied" for a moment.
private struct CopyIcon: View {
    let text: String
    @State private var copied = false

    var body: some View {
        Button {
            UIPasteboard.general.string = text
            withAnimation(.snappy) { copied = true }
            Task {
                try? await Task.sleep(for: .seconds(1.5))
                withAnimation(.snappy) { copied = false }
            }
        } label: {
            Image(systemName: copied ? "checkmark" : "doc.on.doc")
                .font(.footnote)
                .foregroundStyle(.secondary)
                .contentTransition(.symbolEffect(.replace))
                .frame(width: 44, height: 36)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(copied ? "Copied" : "Copy")
        .accessibilityIdentifier("stepCopy")
    }
}

extension Color {
    /// An edit's removed and added lines: muted red and green, of the same weight in light and dark.
    static let diffRemoved = Color(UIColor { traits in
        traits.userInterfaceStyle == .dark
            ? UIColor(red: 255 / 255, green: 69 / 255, blue: 58 / 255, alpha: 0.10)
            : UIColor(red: 255 / 255, green: 59 / 255, blue: 48 / 255, alpha: 0.09)
    })
    static let diffAdded = Color(UIColor { traits in
        traits.userInterfaceStyle == .dark
            ? UIColor(red: 48 / 255, green: 209 / 255, blue: 88 / 255, alpha: 0.08)
            : UIColor(red: 52 / 255, green: 199 / 255, blue: 89 / 255, alpha: 0.11)
    })
}
