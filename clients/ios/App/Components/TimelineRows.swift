import SwiftUI
import UIKit
import AimuxKit

/// "Thought for 8s ›" in grey; tap to read the thought under it.
struct ThoughtRow: View {
    let text: String
    let seconds: Double?
    /// The turn is still running, so a thought without a length is still going.
    let live: Bool
    @Binding var isOpen: Bool

    private var label: String {
        if let seconds { return "Thought for \(Int(seconds))s" }
        return live ? "Thinking…" : "Thought"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button { withAnimation(.snappy) { isOpen.toggle() } } label: {
                HStack(spacing: 5) {
                    Text(label)
                    Image(systemName: "chevron.right")
                        .font(.caption2.weight(.semibold))
                        .rotationEffect(.degrees(isOpen ? 90 : 0))
                }
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("thoughtToggle")
            if isOpen {
                InlineMarkdown(text: text)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .padding(.leading, 10)
                    .overlay(alignment: .leading) {
                        Rectangle().fill(Color(.separator)).frame(width: 2)
                    }
                    .textSelection(.enabled)
            }
        }
    }
}

/// "6 steps · 41s · 3 files changed" on one line; opened, one line per step.
struct StepsCard: View {
    let group: StepGroup
    /// The turn is running: a running step shows a spinner. Otherwise it is shown as plain, unfinished.
    let live: Bool
    @Binding var isOpen: Bool
    /// The steps (by index) whose detail is open; a step with nothing to show cannot open.
    @Binding var openDetails: Set<Int>
    @ScaledMetric(relativeTo: .footnote) private var monoSize: CGFloat = 13.5
    @Environment(\.referenceContext) private var references

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button { withAnimation(.snappy) { isOpen.toggle() } } label: {
                HStack(spacing: 6) {
                    summary.frame(maxWidth: .infinity, alignment: .leading)
                    Image(systemName: "chevron.down")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(isOpen ? 180 : 0))
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 11)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("stepsToggle")
            if isOpen {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(Array(group.steps.enumerated()), id: \.offset) { index, step in
                        stepRow(step, at: index)
                    }
                }
                .padding(.horizontal, 14)
                .padding(.top, 2)
                .padding(.bottom, 12)
            }
        }
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    private var summary: Text {
        var parts: [String] = []
        if let seconds = group.seconds { parts.append(StepsCard.duration(seconds)) }
        let files = group.filesChanged.count
        if files > 0 { parts.append(files == 1 ? "1 file changed" : "\(files) files changed") }
        let count = Text(group.count == 1 ? "1 step" : "\(group.count) steps").font(.subheadline.weight(.semibold))
        let rest = parts.map { " · " + $0 }.joined()
        return Text("\(count)\(Text(rest).font(.subheadline).foregroundStyle(.secondary))")
    }

    /// "41s", "2m 10s".
    static func duration(_ seconds: Double) -> String {
        let whole = Int(seconds)
        return whole < 60 ? "\(whole)s" : "\(whole / 60)m \(whole % 60)s"
    }

    /// A step with a detail is a button that opens it under the line; its target is then plain text (a button
    /// holds no link) and the target's links move into the detail.
    @ViewBuilder private func stepRow(_ step: Step, at index: Int) -> some View {
        if step.detail.isEmpty {
            stepLine(step, inlines: targetInlines(step), canOpen: false, isOpen: false)
        } else {
            let open = openDetails.contains(index)
            let (inlines, links) = unlinkedInlines(targetInlines(step))
            VStack(alignment: .leading, spacing: 8) {
                Button {
                    withAnimation(.snappy) {
                        if open { openDetails.remove(index) } else { openDetails.insert(index) }
                    }
                } label: {
                    stepLine(step, inlines: inlines, canOpen: true, isOpen: open).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("step-\(index)")
                .accessibilityValue(open ? "Expanded" : "Collapsed")
                .accessibilityHint(open ? "Hides the detail" : "Shows the detail")
                if open {
                    StepDetailView(step: step, links: links).padding(.leading, 28)
                }
            }
        }
    }

    private func stepLine(_ step: Step, inlines: [MDInline], canOpen: Bool, isOpen: Bool) -> some View {
        HStack(spacing: 10) {
            Image(systemName: icon(step.kind))
                .font(.footnote)
                .foregroundStyle(step.status == .failed ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary))
                .frame(width: 18)
            target(step, inlines: inlines)
                .font(.system(size: monoSize, design: .monospaced))
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(maxWidth: .infinity, alignment: .leading)
                .tint(Color(.link))
                .environment(\.openURL, OpenURLAction { url in
                    isWebLink(url.absoluteString) ? .systemAction : .discarded
                })
            stepStatus(step)
            if canOpen {
                Image(systemName: "chevron.right")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(isOpen ? 90 : 0))
                    .accessibilityHidden(true)
            }
        }
    }

    @ViewBuilder private func stepStatus(_ step: Step) -> some View {
        switch step.status {
        case .running where live:
            ProgressView().controlSize(.mini).accessibilityLabel("Running")
        case .failed:
            Image(systemName: "xmark")
                .font(.caption.weight(.bold))
                .foregroundStyle(.red)
                .accessibilityLabel("Failed")
        default:
            EmptyView()
        }
    }

    private func targetInlines(_ step: Step) -> [MDInline] {
        step.target.isEmpty ? [] : stepInlines(step.target, context: references)
    }

    /// The target as inline Markdown (a path as a whole links to the repository), or the kind's name.
    private func target(_ step: Step, inlines: [MDInline]) -> Text {
        if step.target.isEmpty { return Text(name(step.kind)) }
        return Text(InlineStyle.attributed(inlines, codeFont: nil))
    }

    private func icon(_ kind: StepKind) -> String {
        switch kind {
        case .read: "doc.text"
        case .edit: "pencil"
        case .search: "magnifyingglass"
        case .run: "terminal"
        case .other: "circle.dashed"
        }
    }

    private func name(_ kind: StepKind) -> String {
        switch kind {
        case .read: "Read"
        case .edit: "Edit"
        case .search: "Search"
        case .run: "Run"
        case .other: "Tool"
        }
    }
}

/// The plan as a checklist, "2 of 4", updated in place.
struct PlanCard: View {
    let plan: PlanItem

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack {
                Text("Plan")
                Spacer()
                Text("\(plan.done) of \(plan.total)").monospacedDigit()
            }
            .font(.footnote)
            .foregroundStyle(.secondary)
            ForEach(Array(plan.entries.enumerated()), id: \.offset) { _, entry in
                HStack(alignment: .firstTextBaseline, spacing: 9) {
                    Image(systemName: mark(entry.status))
                        .font(.footnote)
                        .foregroundStyle(entry.status == "pending" ? .tertiary : .secondary)
                    entryText(entry)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
        }
        .padding(14)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    private func entryText(_ entry: PlanItem.Entry) -> some View {
        let done = entry.status == "completed"
        return InlineMarkdown(text: entry.text)
            .font(entry.status == "in_progress" ? .subheadline.weight(.semibold) : .subheadline)
            .strikethrough(done)
            .foregroundStyle(done ? .secondary : .primary)
    }

    private func mark(_ status: String) -> String {
        switch status {
        case "completed": "checkmark.circle.fill"
        case "in_progress": "circle.inset.filled"
        default: "circle"
        }
    }
}

/// Claude's reply: Markdown with no bubble (headings, lists, code blocks, tables, quotes, reference links); text
/// too long to parse shows as it is, in monospace. While a reply streams in, it is parsed off the main thread at
/// most every 100 ms (the last update always), showing the previous parse meanwhile.
struct ReplyText: View {
    let text: String
    @Environment(\.referenceContext) private var references
    @State private var parsed: Parsed?
    @State private var parsedAt: ContinuousClock.Instant?

    private struct Parsed {
        let key: Key
        /// nil: too long to parse.
        let blocks: [MDBlock]?
    }

    private struct Key: Equatable {
        let text: String
        let context: ReferenceContext
    }

    var body: some View {
        let key = Key(text: text, context: references)
        // The first appearance parses at once (cached), so history never flashes unformatted.
        let shown = parsed ?? Parsed(key: key, blocks: MarkdownRender.reply(text, context: references))
        Group {
            if let blocks = shown.blocks {
                MarkdownView(blocks: blocks)
            } else {
                Text(text)
                    .font(.system(.footnote, design: .monospaced))
                    .environment(\.layoutDirection, .leftToRight)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
        .task(id: key) { await reparse(key, first: parsed == nil) }
    }

    private func reparse(_ key: Key, first: Bool) async {
        if !first {
            let delay = parseDelay(sinceLastParse: parsedAt.map { .now - $0 })
            if delay > .zero {
                do { try await Task.sleep(for: delay) } catch { return }
            }
        }
        let blocks = await Task.detached(priority: .userInitiated) {
            MarkdownRender.reply(key.text, context: key.context)
        }.value
        guard !Task.isCancelled else { return }
        parsed = Parsed(key: key, blocks: blocks)
        parsedAt = .now
    }
}

/// An approval in the chat: the request, the exact command, the server's options as buttons.
struct ChatApprovalCard: View {
    let item: ApprovalItem
    let isAnswering: Bool
    let error: APIError?
    let answer: (PermissionOption) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                StatusDot(status: .needsYou)
                Text("Needs your approval").font(.caption.weight(.semibold))
            }
            Text(item.title)
                .font(.headline)
                .fixedSize(horizontal: false, vertical: true)
            if let command = item.command, !command.isEmpty {
                Text(command)
                    .font(.system(.footnote, design: .monospaced))
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
            }
            FlowLayout(spacing: 8) { options }
                .padding(.top, 4)
            if let error {
                Text(error.userMessage.title).font(.footnote).foregroundStyle(.secondary)
            }
        }
        .padding(14)
        .background(Color(.secondarySystemBackground), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .accessibilityIdentifier("approval-\(item.approvalId)")
    }

    @ViewBuilder private var options: some View {
        // The one-time allow is the black button; "always" stays plain so it is not the easy tap.
        let primary = item.options.first { $0.kind == "allow_once" }
            ?? item.options.first { $0.kind.hasPrefix("allow") }
        ForEach(item.options, id: \.optionId) { option in
            Button(option.name) { answer(option) }
                .buttonStyle(OptionStyle(kind: option == primary ? .primary
                                         : option.kind.hasPrefix("reject") ? .reject : .plain))
                .disabled(isAnswering)
        }
    }
}

/// A resolved approval, folded to one line: "Allowed  go test ./...".
struct ResolvedApprovalLine: View {
    let outcome: String
    let detail: String

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Image(systemName: outcome == "Allowed" ? "checkmark" : "xmark")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
            Text(outcome).font(.subheadline.weight(.semibold))
            Text(detail)
                .font(.system(.footnote, design: .monospaced))
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
        }
    }
}

/// A turn that ended with an error: red dot and the error text.
struct TimelineErrorLine: View {
    let text: String

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            StatusDot(status: .failed)
            InlineMarkdown(text: text).font(.subheadline)
        }
    }
}
