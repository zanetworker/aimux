import Foundation

// The chat timeline: history turns (SessionDetail.turns) merged with the event log into display items.
// The rules are pinned by api/testdata/clients/chat/*.json and stated in api/testdata/clients/README.md;
// the web client's timeline.mjs implements the same. Pure: no clock, no I/O.

public enum StepKind: String, Equatable, Hashable, Sendable { case read, edit, search, run, other }
public enum StepStatus: String, Equatable, Hashable, Sendable {
    // swiftlint:disable:next identifier_name
    case ok, failed, running
}

public struct Step: Equatable, Hashable, Sendable {
    public let kind: StepKind
    /// What it worked on (a title, a path or a command); "" when none.
    public let target: String
    public let status: StepStatus
    /// What opening it shows; empty when there is nothing to open.
    public let detail: StepDetail

    public init(kind: StepKind, target: String, status: StepStatus, detail: StepDetail = .empty) {
        self.kind = kind
        self.target = target
        self.status = status
        self.detail = detail
    }
}

/// Tool calls shown together. `filesChanged` is the distinct targets of edit steps, in first-seen order.
public struct StepGroup: Equatable, Hashable, Sendable {
    public let steps: [Step]
    /// Whole seconds from the first call to the last change; nil for history.
    public let seconds: Double?
    public let filesChanged: [String]
    public var count: Int { steps.count }

    public init(steps: [Step], seconds: Double?) {
        self.steps = steps
        self.seconds = seconds
        var files: [String] = []
        for step in steps where step.kind == .edit && !step.target.isEmpty && !files.contains(step.target) {
            files.append(step.target)
        }
        filesChanged = files
    }
}

public struct PlanItem: Equatable, Hashable, Sendable {
    public struct Entry: Equatable, Hashable, Sendable {
        public let text: String
        /// As sent: `pending`, `in_progress`, `completed`, ...
        public let status: String

        public init(text: String, status: String) {
            self.text = text
            self.status = status
        }
    }

    public let entries: [Entry]
    public var total: Int { entries.count }
    public var done: Int { entries.filter { $0.status == "completed" }.count }

    public init(entries: [Entry]) { self.entries = entries }
}

public struct ApprovalItem: Equatable, Hashable, Sendable {
    public let approvalId: String
    public let title: String
    public let command: String?
    public let options: [PermissionOption]
    /// `allowed`, `denied`, `expired` (as sent) once resolved; nil while pending.
    public var outcome: String?

    public init(approvalId: String, title: String, command: String?, options: [PermissionOption], outcome: String?) {
        self.approvalId = approvalId
        self.title = title
        self.command = command
        self.options = options
        self.outcome = outcome
    }

    /// The request to answer.
    public var approval: Approval {
        Approval(approvalId: approvalId, title: title, options: options, command: command)
    }
}

public enum TimelineItem: Equatable, Hashable, Sendable {
    case user(String)
    /// `seconds` is nil while the thought is still running.
    case thought(text: String, seconds: Double?)
    case steps(StepGroup)
    case plan(PlanItem)
    case reply(String)
    case approval(ApprovalItem)
    case error(String)
    case stopped
}

public struct TimelineResult: Equatable, Sendable {
    public let items: [TimelineItem]
    /// A turn is running.
    public let working: Bool
    /// The last approval still waiting for an answer.
    public let pendingApproval: String?

    public init(items: [TimelineItem], working: Bool, pendingApproval: String?) {
        self.items = items
        self.working = working
        self.pendingApproval = pendingApproval
    }

    public static let empty = TimelineResult(items: [], working: false, pendingApproval: nil)
}

/// Seconds of slack between a turn's events and its history record.
private let window: TimeInterval = 2

/// Whole seconds between two times, halves rounded up, never below 0.
func timelineSeconds(from start: Date, to end: Date) -> Double {
    let elapsed = (end.timeIntervalSince(start) * 1_000_000).rounded() / 1_000_000
    return max(0, (elapsed + 0.5).rounded(.down))
}

/// Whether a history prompt (whitespace folded, perhaps cut short with "…") is the start of `text`.
func samePrompt(_ history: String, _ text: String) -> Bool {
    func fold(_ value: String) -> String { value.split(whereSeparator: \.isWhitespace).joined(separator: " ") }
    var head = fold(history)
    if head.hasSuffix("…") { head.removeLast() }
    return !head.isEmpty && fold(text).utf16.starts(with: head.utf16)
}

/// Merges history turns and events into display items, in order. `now` is not used yet.
public func buildTimeline(turns: [Turn], events: [SessionEvent], now: Date) -> TimelineResult {
    var seen = Set<Int>()
    // An event of no turn (a queue snapshot) is not part of the timeline.
    let ordered = events.filter { !$0.turn.isEmpty && seen.insert($0.seq).inserted }.sorted { $0.seq < $1.seq }
    var byTurn: [String: [SessionEvent]] = [:]
    for event in ordered { byTurn[event.turn, default: []].append(event) }

    var running: [SessionEvent]?
    for event in ordered.reversed() where endOf(byTurn[event.turn] ?? []) == nil {
        running = byTurn[event.turn]
        break
    }

    var history = turns
    if let first = running?.first, case .turnStarted(let text) = first.data, let last = history.last,
       samePrompt(last.user, text), last.time >= first.time.addingTimeInterval(-window) {
        history.removeLast()
    }

    let approvals = ApprovalDrafts()
    var placement = Placement(history: history, approvals: approvals)
    let ended = byTurn.values.compactMap { turn in endOf(turn).map { (turn: turn, end: $0) } }
    for (turn, end) in ended.sorted(by: { $0.end.seq < $1.end.seq }) { placement.place(turn, end: end) }

    var drafts: [Draft] = []
    for (index, turn) in history.enumerated() {
        drafts += historyDrafts(turn) + placement.after[index]
    }
    drafts += placement.atEnd
    if let running { drafts += TurnDrawer(approvals: approvals).draw(running) }

    let items = drafts.map(\.item)
    var pending: String?
    for case .approval(let item) in items where item.outcome == nil { pending = item.approvalId }
    return TimelineResult(items: items, working: running != nil, pendingApproval: pending)
}

private func endOf(_ events: [SessionEvent]) -> SessionEvent? {
    events.first { $0.kind == .turnDone || $0.kind == .turnError }
}

private func startText(_ event: SessionEvent) -> String? {
    if case .turnStarted(let text) = event.data { return text }
    return nil
}

private let historyKinds: [String: StepKind] = [
    "Read": .read, "Edit": .edit, "Write": .edit, "MultiEdit": .edit, "NotebookEdit": .edit,
    "Grep": .search, "Glob": .search, "WebSearch": .search, "Bash": .run
]

private func historyDrafts(_ turn: Turn) -> [Draft] {
    var items: [Draft] = []
    if !turn.user.isEmpty { items.append(.fixed(.user(turn.user))) }
    if !turn.tools.isEmpty {
        let steps = turn.tools.map {
            Step(kind: historyKinds[$0.name] ?? .other, target: $0.snippet, status: $0.ok ? .ok : .failed,
                 detail: $0.detail)
        }
        items.append(.fixed(.steps(StepGroup(steps: steps, seconds: nil))))
    }
    if !turn.reply.isEmpty { items.append(.fixed(.reply(turn.reply))) }
    return items
}

/// Where ended turns go: after a history turn, or after all history.
private struct Placement {
    let history: [Turn]
    let approvals: ApprovalDrafts
    var after: [[Draft]]
    var atEnd: [Draft] = []
    private var claimed = Set<Int>()

    init(history: [Turn], approvals: ApprovalDrafts) {
        self.history = history
        self.approvals = approvals
        after = Array(repeating: [], count: history.count)
    }

    mutating func place(_ turn: [SessionEvent], end: SessionEvent) {
        let terminal = terminalDrafts(end)
        let start = turn.first { $0.kind == .turnStarted }
        if let index = claim(start: start, end: end) {
            claimed.insert(index)
            after[index] += terminal
        } else if let start {
            // Never written to history: drawn from its events, in time among the turns shown,
            // and left out when it is older than all of them.
            if let first = history.first, start.time < first.time { return }
            let drawn = TurnDrawer(approvals: approvals).draw(turn) + terminal
            if let later = history.firstIndex(where: { $0.time > start.time }), later > 0 {
                after[later - 1] += drawn
            } else {
                atEnd += drawn
            }
        } else if history.first.map({ end.time >= $0.time }) ?? true {
            atEnd += terminal
        }
    }

    private func claim(start: SessionEvent?, end: SessionEvent) -> Int? {
        let latest = end.time.addingTimeInterval(window)
        guard let start else {
            return history.indices.last { !claimed.contains($0) && history[$0].time <= latest }
        }
        let earliest = start.time.addingTimeInterval(-window)
        let text = startText(start) ?? ""
        return history.indices.first {
            let turn = history[$0]
            return !claimed.contains($0) && samePrompt(turn.user, text) && turn.time >= earliest && turn.time <= latest
        }
    }

    private func terminalDrafts(_ end: SessionEvent) -> [Draft] {
        switch end.data {
        case .turnError(let error): [.fixed(.error(error))]
        case .turnDone(let stopReason) where stopReason == "cancelled": [.fixed(.stopped)]
        default: []
        }
    }
}

/// One timeline item with an identity that follows its content, not its place: when the latest turns move on
/// (the oldest leaves the top, a new one joins at the bottom) the items both reads share keep their ids, so a
/// lazy list keeps its rows and its scroll place. An item that occurs several times counts its occurrences.
public struct TimelineRow: Identifiable, Sendable {
    public let id: String
    public let index: Int
    public let item: TimelineItem
}

public func timelineRows(_ items: [TimelineItem]) -> [TimelineRow] {
    var seen: [TimelineItem: Int] = [:]
    return items.enumerated().map { index, item in
        let nth = seen[item, default: 0]
        seen[item] = nth + 1
        return TimelineRow(id: "\(item.hashValue)/\(nth)", index: index, item: item)
    }
}
