import Foundation

/// The chat's Next bar target: the first other session in Now's Needs you, else in Working.
public func nextSession(in view: NowView, excluding id: String) -> NowItem? {
    if let item = view.needsYou.first(where: { $0.session.id != id }) { return item }
    let working: [NowItem]
    switch view.working {
    case .list(let items): working = items
    case .groups(let groups, _): working = groups.flatMap(\.items)
    }
    return working.first { $0.session.id != id }
}

/// The colour of the chat header's dot.
public enum ChatTone: Equatable, Sendable { case idle, needsYou, working, failed }

/// The chat header's status line: a dot and a label.
public struct ChatStatus: Equatable, Sendable {
    public let tone: ChatTone
    public let label: String

    public init(tone: ChatTone, label: String) {
        self.tone = tone
        self.label = label
    }
}

/// "Did not start", "Open on your Mac · 14h ago", "Your turn", "Working" or "Failed"; nil before the session loads.
public func chatStatus(detail: SessionDetail?, readOnly: Bool, result: TimelineResult, now: Date) -> ChatStatus? {
    guard let detail else { return nil }
    if detail.state == .openInTerminal, let terminal = detail.terminal, terminal.status != "unknown" {
        if result.pendingApproval != nil { return ChatStatus(tone: .needsYou, label: "On your Mac · Your turn") }
        if terminal.isBusy { return ChatStatus(tone: .working, label: "On your Mac · Working") }
        return ChatStatus(tone: .idle, label: "On your Mac · Your turn")
    }
    if readOnly && detail.state == .unavailable { return ChatStatus(tone: .failed, label: "Did not start") }
    if readOnly || detail.state == .openInTerminal {
        return ChatStatus(tone: .idle, label: "Open on your Mac · " + chatAgo(detail.lastActive, now: now))
    }
    if result.pendingApproval != nil { return ChatStatus(tone: .needsYou, label: "Your turn") }
    if result.working { return ChatStatus(tone: .working, label: "Working") }
    if case .error? = result.items.last { return ChatStatus(tone: .failed, label: "Failed") }
    return ChatStatus(tone: .idle, label: "Your turn")
}

/// The line in place of the composer for a read-only session.
public func readOnlyText(_ detail: SessionDetail?) -> String {
    if detail?.state == .unavailable {
        return "This session never started: its first message failed before Claude saved it. "
            + "Start a new session from Now."
    }
    if detail?.state == .openInTerminal, let terminal = detail?.terminal, !terminal.canType {
        return terminalReasonText(terminal.reason)
    }
    return "Read-only here while it's open in a terminal on your Mac"
}

/// The line above the composer for a terminal that cannot take typing now; nil when it can. The caller leaves it
/// out while a prompt's card says what to do (it knows the live prompt). With the chat queue (`queue`), the Mac in
/// use reads as the spec's line, and not at all while the queue's waiting note says it; `queueStatus` is the
/// chat's live queue (from events), not the detail's.
public func terminalReasonLine(_ detail: SessionDetail?, queue: Bool, queueStatus: QueueStatus?) -> String? {
    guard let detail, detail.state == .openInTerminal, let terminal = detail.terminal, !terminal.canType
    else { return nil }
    guard queue, terminal.reason == "mac_in_use" else { return terminalReasonText(terminal.reason) }
    let section = ChatQueue.section(queueStatus)
    if section.visible && section.state == "running" && section.line != nil { return nil }
    return "You're using this session on your Mac."
}

/// Why the phone cannot type into the terminal now, from `terminal.reason`.
public func terminalReasonText(_ reason: String?) -> String {
    switch reason {
    case "disabled": "Typing into your Mac's terminal is off"
    case "not_claude": "Claude isn't running in that terminal"
    case "mac_in_use": "Your Mac is in use right now"
    case "no_terminal_driver": "This terminal isn't supported yet"
    case "automation_denied": "Allow aimux to control iTerm2 on your Mac"
    case "not_at_input": "Claude isn't at its input box"
    default: "Can't reach the terminal"
    }
}

/// The live line at the end of a terminal chat while Claude works on the Mac: "Working · 1m 20s · go test ./…",
/// the time from Claude Code's working line and the step from the session's latest turn; nil when it is not working.
public func terminalWorkingLine(_ detail: SessionDetail?) -> String? {
    guard let detail, detail.state == .openInTerminal, let terminal = detail.terminal, terminal.working else {
        return nil
    }
    var parts = ["Working"]
    if terminal.workingSeconds > 0 { parts.append(elapsedText(terminal.workingSeconds)) }
    if let step = detail.turns.last?.tools.last {
        parts.append(step.snippet.isEmpty ? step.name : step.snippet)
    }
    return parts.joined(separator: " · ")
}

/// A duration as Claude Code's working line shows it: "42s", "1m 20s", "2h 3m".
public func elapsedText(_ seconds: Int) -> String {
    let (hours, minutes, secs) = (seconds / 3600, seconds % 3600 / 60, seconds % 60)
    if hours > 0 { return minutes > 0 ? "\(hours)h \(minutes)m" : "\(hours)h" }
    if minutes > 0 { return secs > 0 ? "\(minutes)m \(secs)s" : "\(minutes)m" }
    return "\(secs)s"
}

/// The line under a message typed into the terminal, until the session's history shows it.
public func terminalPendingText(sentAt: Date, now: Date) -> String {
    now.timeIntervalSince(sentAt) < 120 ? "Sent to your Mac's terminal" : "Sent; no answer yet"
}

/// "just now", "5m ago", "14h ago", "3d ago".
public func chatAgo(_ date: Date, now: Date) -> String {
    let minutes = Int(now.timeIntervalSince(date) / 60)
    if minutes < 1 { return "just now" }
    if minutes < 60 { return "\(minutes)m ago" }
    if minutes < 60 * 24 { return "\(minutes / 60)h ago" }
    return "\(minutes / (60 * 24))d ago"
}

/// The session info sheet's numbers.
public enum StatsFormat {
    /// "$0.42"; "<$0.01" for a cost below a cent.
    public static func cost(_ usd: Double) -> String {
        if usd > 0 && usd < 0.005 { return "<$0.01" }
        return String(format: "$%.2f", usd)
    }

    /// "999", "34k", "1.2M".
    public static func tokens(_ count: Int) -> String {
        if count < 1000 { return "\(count)" }
        if count < 999_500 { return "\(Int((Double(count) / 1000).rounded()))k" }
        let millions = (Double(count) / 100_000).rounded() / 10
        return millions == millions.rounded() ? "\(Int(millions))M" : String(format: "%.1fM", millions)
    }

    /// "Oct 26, 09:14"; nil when the server gave no time.
    public static func time(_ date: Date?) -> String? {
        date?.formatted(.dateTime.month(.abbreviated).day().hour().minute())
    }

    /// "12m", "1h 12m", "2h"; "<1m" under half a minute.
    public static func duration(minutes: Double) -> String {
        let whole = Int(minutes.rounded())
        if whole < 1 { return "<1m" }
        let (hours, rest) = (whole / 60, whole % 60)
        if hours == 0 { return "\(rest)m" }
        return rest == 0 ? "\(hours)h" : "\(hours)h \(rest)m"
    }
}
