import SwiftUI
import AimuxKit

/// A row's time: tabular digits in one right-aligned column.
struct RowTime: View {
    let date: Date
    let format: SessionRowFormat

    var body: some View {
        Text(format.relativeTime(date))
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .monospacedDigit()
            .lineLimit(1)
            .fixedSize()
    }
}

/// Dot, title, the description, one secondary line and the time: the shape every session row shares.
/// At accessibility text sizes the time (and lock) moves under the title so the title keeps the width.
struct DotRow<Detail: View>: View {
    let status: DotStatus
    let title: String
    /// The session's description as a quiet line under the title; nil for none.
    var description: String?
    /// Nil leaves out the time column (a failed row has Retry there instead).
    let date: Date?
    let format: SessionRowFormat
    var locked = false
    @ViewBuilder let detail: Detail
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let stacked = typeSize.isAccessibilitySize
        HStack(alignment: stacked ? .firstTextBaseline : .center, spacing: 10) {
            StatusDot(status: status)
            TimeLayout(stacked: stacked) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(bidi: isolated(title))
                        .font(.body.weight(.semibold))
                        .lineLimit(2)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let description { DescriptionLine(text: description, stacked: stacked) }
                    detail
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .lineLimit(stacked ? 3 : 1)
                }
                if locked || date != nil {
                    HStack(spacing: 6) {
                        if locked {
                            Image(systemName: "lock")
                                .font(.caption2)
                                .foregroundStyle(.tertiary)
                                .accessibilityLabel("Open on your Mac")
                        }
                        if let date { RowTime(date: date, format: format) }
                    }
                }
            }
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

/// The session's description: one secondary line, truncated (two at accessibility text sizes).
struct DescriptionLine: View {
    let text: String
    let stacked: Bool

    var body: some View {
        Text(bidi: isolated(text))
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .lineLimit(stacked ? 2 : 1)
    }
}

/// Main content and a row's time side by side, or the time under the content when `stacked`.
struct TimeLayout<Content: View>: View {
    let stacked: Bool
    @ViewBuilder let content: Content

    var body: some View {
        let layout = stacked ? AnyLayout(VStackLayout(alignment: .leading, spacing: 2))
            : AnyLayout(HStackLayout(alignment: .center, spacing: 10))
        layout { content }
    }
}

/// A small uppercase section label, with an optional count or link on the right.
struct SectionLabel<Trailing: View>: View {
    let title: String
    var detail: String?
    @ViewBuilder var trailing: Trailing

    var body: some View {
        HStack(alignment: .firstTextBaseline) {
            HStack(spacing: 0) {
                Text(title.uppercased()).fontWeight(.medium)
                if let detail { Text(" · " + detail) }
            }
            Spacer()
            trailing
        }
        .font(.footnote)
        .foregroundStyle(.secondary)
        .textCase(nil)
    }
}

extension SectionLabel where Trailing == EmptyView {
    init(_ title: String, detail: String? = nil) {
        self.init(title: title, detail: detail) { EmptyView() }
    }
}

/// "project · rest", with the project isolated so an Arabic name does not reorder the line.
func projectLine(_ project: String, _ rest: String?, format: SessionRowFormat) -> String {
    let name = isolated(format.projectName(project))
    guard let rest, !rest.isEmpty else { return name }
    return "\(name) · \(isolated(rest))"
}

/// A working session: "project · live step" (the step in monospace), "Thinking", or "Active".
struct WorkingRow: View {
    let item: NowItem
    let format: SessionRowFormat

    var body: some View {
        DotRow(status: .working, title: item.session.displayTitle, description: item.session.descriptionLine,
               date: item.session.lastActive, format: format) {
            StepLine(project: item.session.project, activity: item.activity, format: format)
        }
    }
}

/// "project · live step": the step in monospace, or "Thinking", or "Active"; without a project, the step alone.
struct StepLine: View {
    var project: String?
    let activity: Activity
    let format: SessionRowFormat

    var body: some View {
        HStack(spacing: 0) {
            if let project { Text(bidi: isolated(format.projectName(project)) + " · ") }
            stepText
        }
        // Read as "aimux, Run tests", not with the separator.
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([project.map { isolated(format.projectName($0)) }, stepLabel].compactMap { $0 }
            .joined(separator: ", "))
    }

    private var stepLabel: String {
        guard case .working(let step?) = activity else { return "Active" }
        return step == "Thinking" ? step : isolated(step)
    }

    private var stepText: Text {
        guard case .working(let step?) = activity else { return Text("Active") }
        return step == "Thinking" ? Text(step)
            : Text(bidi: isolated(step)).font(.system(.subheadline, design: .monospaced))
    }
}

/// A Last on your Mac or Pinned row: the session's own dot, its description, then "project · step" while it
/// works, else the project; the lock when it is open read-only on the Mac.
struct PlacedRow: View {
    let item: PlacedItem
    let format: SessionRowFormat

    var body: some View {
        let session = item.session
        DotRow(status: item.isWorking ? .working : .idle, title: session.displayTitle,
               description: session.descriptionLine, date: session.lastActive, format: format,
               locked: session.state == .openInTerminal && !item.isWorking) {
            if item.isWorking {
                StepLine(project: session.project, activity: item.activity, format: format)
            } else {
                Text(bidi: isolated(format.projectName(session.project)))
            }
        }
    }
}

/// A session whose last turn failed: red dot, the error in one line, and Retry when there is a prompt to resend.
struct FailedRow: View {
    let session: Session
    let error: String
    let isRetrying: Bool
    let retryError: APIError?
    let format: SessionRowFormat
    let open: () -> Void
    let retry: () -> Void

    private var canRetry: Bool {
        !(session.lastPrompt ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Button(action: open) {
                    DotRow(status: .failed, title: session.displayTitle, date: nil, format: format) {
                        Text(bidi: "Failed · " + isolated(error))
                    }
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("session-\(session.id)")
                if canRetry {
                    Button("Retry", action: retry)
                        .buttonStyle(.borderless)
                        .font(.subheadline.weight(.semibold))
                        .foregroundStyle(.primary)
                        .disabled(isRetrying)
                        .accessibilityIdentifier("retry-\(session.id)")
                }
            }
            if let retryError {
                Text(retryError.userMessage.title)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                    .padding(.leading, 17)
            }
        }
    }
}

/// A session inside an open folder: title then what it is doing, its description under them.
struct CompactWorkingRow: View {
    let item: NowItem
    let format: SessionRowFormat

    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let stacked = typeSize.isAccessibilitySize
        HStack(alignment: stacked ? .firstTextBaseline : .center, spacing: 10) {
            StatusDot(status: .working)
            TimeLayout(stacked: stacked) {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 0) {
                        Text(bidi: isolated(item.session.displayTitle)).font(.subheadline).layoutPriority(1)
                        Text(" · ").font(.caption).foregroundStyle(.secondary).accessibilityHidden(true)
                        stepText.foregroundStyle(.secondary)
                    }
                    .lineLimit(stacked ? 3 : 1)
                    if let description = item.session.descriptionLine {
                        DescriptionLine(text: description, stacked: stacked)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                RowTime(date: item.session.lastActive, format: format)
            }
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }

    /// The live step in monospace; "Thinking" and "Active" in the normal font.
    private var stepText: Text {
        guard case .working(let step?) = item.activity else { return Text("Active").font(.caption) }
        return step == "Thinking" ? Text(step).font(.caption)
            : Text(bidi: isolated(step)).font(.system(.caption, design: .monospaced))
    }
}

/// An idle session open in a terminal on the Mac: grey dot, title, idle time; one line.
struct OnMacRow: View {
    let session: Session
    let format: SessionRowFormat

    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let stacked = typeSize.isAccessibilitySize
        HStack(alignment: stacked ? .firstTextBaseline : .center, spacing: 10) {
            StatusDot(status: .idle)
            TimeLayout(stacked: stacked) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(bidi: isolated(session.displayTitle))
                        .lineLimit(stacked ? 3 : 1)
                    if let description = session.descriptionLine {
                        DescriptionLine(text: description, stacked: stacked)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                RowTime(date: session.lastActive, format: format)
            }
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }
}

/// A row in All and in search: title, then "project · last prompt"; a lock when open in a terminal.
struct SessionRow: View {
    let session: Session
    let format: SessionRowFormat

    var body: some View {
        DotRow(status: DotStatus(session.state), title: session.displayTitle, description: session.descriptionLine,
               date: session.lastActive, format: format, locked: session.state == .openInTerminal) {
            Text(bidi: projectLine(session.project, session.lastPrompt, format: format))
        }
    }
}

struct SearchHitRow: View {
    let hit: SearchHit
    let format: SessionRowFormat

    var body: some View {
        DotRow(status: DotStatus(hit.session.state), title: hit.session.displayTitle, date: hit.session.lastActive,
               format: format, locked: hit.session.state == .openInTerminal) {
            VStack(alignment: .leading, spacing: 2) {
                Text(bidi: isolated(format.projectName(hit.session.project)))
                Text(highlighted).lineLimit(3)
            }
        }
    }

    private var highlighted: AttributedString {
        Snippet.parse(hit.snippet).reduce(into: AttributedString()) { result, segment in
            var part = AttributedString(segment.text)
            if segment.isMatch {
                part.font = .subheadline.weight(.bold)
                part.foregroundColor = .primary
            }
            result += part
        }
    }
}

struct ErrorBanner: View {
    let message: UserMessage
    let retry: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Label(message.title, systemImage: "exclamationmark.triangle")
                .font(.headline)
            Text(message.detail)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Button("Try again", action: retry)
                .buttonStyle(.bordered)
                .tint(.primary)
                .padding(.top, 2)
        }
        .padding(.vertical, 4)
    }
}
