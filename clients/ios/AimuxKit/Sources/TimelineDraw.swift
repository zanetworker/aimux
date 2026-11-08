import Foundation

// Drawing one turn of the chat timeline from its events (see Timeline.swift for the rules).

/// Items still being built; reference types so later events can change them in place.
enum Draft {
    case fixed(TimelineItem)
    case text(TextDraft)
    case group(GroupDraft)
    case plan(PlanDraft)
    case approval(ApprovalDraft)

    var item: TimelineItem {
        switch self {
        case .fixed(let item): item
        case .text(let text) where text.thought: .thought(text: text.text, seconds: text.seconds)
        case .text(let text): .reply(text.text)
        case .group(let group):
            .steps(StepGroup(steps: group.steps.map(\.step),
                             seconds: timelineSeconds(from: group.first, to: group.last)))
        case .plan(let plan): .plan(PlanItem(entries: plan.entries))
        case .approval(let approval): .approval(approval.item)
        }
    }
}

final class TextDraft {
    let thought: Bool
    let start: Date
    var text: String
    var seconds: Double?

    init(thought: Bool, text: String, start: Date) {
        self.thought = thought
        self.text = text
        self.start = start
    }
}

final class StepDraft {
    var kind: StepKind
    var target: String
    var status: StepStatus
    /// The tool_call and its updates, in seq order, which the detail is read from.
    var updates: [SessionUpdate]
    var step: Step {
        Step(kind: kind, target: target, status: status, detail: liveStepDetail(updates, status: status))
    }

    init(kind: StepKind, target: String, status: StepStatus, update: SessionUpdate) {
        self.kind = kind
        self.target = target
        self.status = status
        updates = [update]
    }
}

final class GroupDraft {
    var steps: [StepDraft] = []
    let first: Date
    var last: Date

    init(at time: Date) {
        first = time
        last = time
    }
}

final class PlanDraft {
    var entries: [PlanItem.Entry]
    init(entries: [PlanItem.Entry]) { self.entries = entries }
}

final class ApprovalDraft {
    var item: ApprovalItem
    init(item: ApprovalItem) { self.item = item }
}

/// Approval items across the whole timeline, by id, so a resolve finds its item wherever it is.
final class ApprovalDrafts {
    var byID: [String: ApprovalDraft] = [:]
}

/// What an event does to a turn; nil (from `action(for:)`) means it changes nothing.
private enum Action {
    case start(String)
    case chunk(thought: Bool, text: String)
    case toolCall(SessionUpdate)
    case toolUpdate(SessionUpdate)
    case plan(SessionUpdate)
    case approval(Approval)
    case resolved(id: String, outcome: String?)
    case end

    var isThought: Bool? {
        if case .chunk(let thought, _) = self { return thought }
        return nil
    }
}

private let acpKinds: [String: StepKind] = ["read": .read, "edit": .edit, "search": .search, "execute": .run]

private func stepStatus(_ status: String?) -> StepStatus {
    switch status {
    case "completed": .ok
    case "failed": .failed
    default: .running
    }
}

final class TurnDrawer {
    private let approvals: ApprovalDrafts
    private var items: [Draft] = []
    private var tools: [String?: (step: StepDraft, group: GroupDraft)] = [:]
    /// The steps group a tool call joins; none after a reply.
    private var group: GroupDraft?
    /// The open chunk run.
    private var run: TextDraft?
    private var plan: PlanDraft?

    init(approvals: ApprovalDrafts) { self.approvals = approvals }

    /// Draws one turn from its events (in seq order).
    func draw(_ events: [SessionEvent]) -> [Draft] {
        for event in events {
            guard let action = action(for: event) else { continue }
            if let open = run, open.thought != action.isThought {
                if open.thought { open.seconds = timelineSeconds(from: open.start, to: event.time) }
                run = nil
            }
            apply(action, at: event.time)
        }
        return items
    }

    private func action(for event: SessionEvent) -> Action? {
        switch event.data {
        case .turnStarted(let text): return .start(text)
        case .update: return event.update.flatMap(updateAction)
        case .approval(let approval): return .approval(approval)
        case .approvalResolved(let id, let outcome):
            return approvals.byID[id] == nil ? nil : .resolved(id: id, outcome: outcome)
        case .turnDone, .turnError: return .end
        case .queue, .other: return nil
        }
    }

    private func updateAction(_ update: SessionUpdate) -> Action? {
        switch update.sessionUpdate {
        case "agent_message_chunk", "agent_thought_chunk":
            guard let text = update.text, !text.isEmpty else { return nil }
            return .chunk(thought: update.sessionUpdate == "agent_thought_chunk", text: text)
        case "tool_call": return tools[update.toolCallId] == nil ? .toolCall(update) : .toolUpdate(update)
        case "tool_call_update": return tools[update.toolCallId] == nil ? nil : .toolUpdate(update)
        case "plan": return .plan(update)
        default: return nil
        }
    }

    private func apply(_ action: Action, at time: Date) {
        switch action {
        case .start(let text): if !text.isEmpty { items.append(.fixed(.user(text))) }
        case .chunk(let thought, let text): addChunk(thought: thought, text: text, at: time)
        case .toolCall(let update): addStep(update, at: time)
        case .toolUpdate(let update): changeStep(update, at: time)
        case .plan(let update): setPlan(update)
        case .approval(let approval):
            let draft = ApprovalDraft(item: ApprovalItem(approvalId: approval.approvalId, title: approval.title,
                                                         command: approval.command, options: approval.options,
                                                         outcome: nil))
            items.append(.approval(draft))
            approvals.byID[approval.approvalId] = draft
        case .resolved(let id, let outcome):
            if let outcome, !outcome.isEmpty { approvals.byID[id]?.item.outcome = outcome }
        case .end: break
        }
    }

    private func addChunk(thought: Bool, text: String, at time: Date) {
        if let run {
            run.text += text
            return
        }
        let draft = TextDraft(thought: thought, text: text, start: time)
        items.append(.text(draft))
        run = draft
        if !thought { group = nil }
    }

    private func addStep(_ update: SessionUpdate, at time: Date) {
        let current = group ?? {
            let made = GroupDraft(at: time)
            items.append(.group(made))
            group = made
            return made
        }()
        let step = StepDraft(kind: update.kind.flatMap { acpKinds[$0] } ?? .other, target: update.title ?? "",
                             status: stepStatus(update.status), update: update)
        current.steps.append(step)
        current.last = time
        tools[update.toolCallId] = (step, current)
    }

    /// A tool_call_update, or a tool_call repeated for a known id: the fields it carries win.
    private func changeStep(_ update: SessionUpdate, at time: Date) {
        guard let (step, owner) = tools[update.toolCallId] else { return }
        step.updates.append(update)
        if let title = update.title, !title.isEmpty { step.target = title }
        if let kind = update.kind, !kind.isEmpty { step.kind = acpKinds[kind] ?? .other }
        if let status = update.status, !status.isEmpty { step.status = stepStatus(status) }
        owner.last = time
    }

    private func setPlan(_ update: SessionUpdate) {
        let entries = (update.entries ?? []).map { PlanItem.Entry(text: $0.content, status: $0.status) }
        if let plan {
            plan.entries = entries
        } else {
            let draft = PlanDraft(entries: entries)
            items.append(.plan(draft))
            plan = draft
        }
    }
}
