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

/// A session row on Now, All and in search: the dot, the title with its marks (pinned in its pane, last in front),
/// the state line when open ("Working · step", "Your turn · 4 min"), the quiet place line ("aimux / clients · pane 2")
/// and, when `showsTime`, the time. At accessibility text sizes the time moves under the title.
struct SessionLine: View {
    let session: Session
    let activity: Activity
    let state: RowState?
    let place: String
    var showsPin = false
    var showsTime = true
    let format: SessionRowFormat
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let stacked = typeSize.isAccessibilitySize
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            StatusDot(status: isWorking(session, activity: activity) ? .working : .idle)
                .alignmentGuide(.firstTextBaseline) { $0[VerticalAlignment.center] + 4 }
            TimeLayout(stacked: stacked) {
                VStack(alignment: .leading, spacing: 2) {
                    titleLine
                    if let state { stateText(state) }
                    if !place.isEmpty {
                        Text(bidi: isolated(place))
                            .font(.footnote)
                            .foregroundStyle(.secondary)
                            .lineLimit(stacked ? 3 : 1)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                if showsTime { RowTime(date: session.lastActive, format: format) }
            }
        }
        .contentShape(Rectangle())
        .accessibilityElement(children: .combine)
    }

    private var titleLine: some View {
        HStack(alignment: .firstTextBaseline, spacing: 5) {
            Text(bidi: isolated(session.displayTitle))
                .font(.body.weight(.semibold))
                .lineLimit(2)
                .fixedSize(horizontal: false, vertical: true)
            if showsPin { mark("pin.fill", label: "Pinned") }
            if session.mac?.lastInFront == true { mark("diamond.fill", label: "Last in front on your Mac") }
        }
    }

    private func mark(_ symbol: String, label: String) -> some View {
        Image(systemName: symbol)
            .font(.caption2)
            .foregroundStyle(.secondary)
            .accessibilityLabel(label)
    }

    /// "Working · " then the live step in monospace, or "Your turn · 4 min".
    private func stateText(_ state: RowState) -> some View {
        let text: Text
        if case .working(let step?) = state, step != "Thinking" {
            text = Text("Working · ") + Text(bidi: isolated(step)).font(.system(.subheadline, design: .monospaced))
        } else {
            text = Text(state.text)
        }
        return text
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .lineLimit(1)
    }
}

struct SearchHitRow: View {
    let hit: SearchHit
    let format: SessionRowFormat

    var body: some View {
        DotRow(status: DotStatus(hit.session.state), title: hit.session.displayTitle, date: hit.session.lastActive,
               format: format) {
            VStack(alignment: .leading, spacing: 2) {
                Text(bidi: isolated(rowPlace(hit.session, withTab: true)))
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
