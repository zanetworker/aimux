import Foundation

/// What a session is doing right now, read from its recent events.
public enum Activity: Equatable, Sendable {
    case none
    case working(step: String?)
    case needsApproval(Approval)
    // swiftlint:disable:next identifier_name
    case failed(error: String, at: Date)
    // swiftlint:disable:next identifier_name
    case finished(at: Date)
}

/// A pending approval wins; otherwise the last turn event decides. `step` is the title of the latest
/// tool call in the running turn, or "Thinking" when the latest update is a thought.
public func deriveActivity(_ events: [SessionEvent]) -> Activity {
    let ordered = events.sorted { $0.seq < $1.seq }

    var pending: [Approval] = []
    for event in ordered {
        switch event.data {
        case .approval(let approval):
            pending.append(approval)
        case .approvalResolved(let approvalId, _):
            pending.removeAll { $0.approvalId == approvalId }
        default:
            break
        }
    }
    if let approval = pending.last { return .needsApproval(approval) }

    guard let last = ordered.last(where: { isTurnEvent($0.kind) }) else { return .none }
    switch last.data {
    case .turnError(let error): return .failed(error: error, at: last.time)
    case .turnDone: return .finished(at: last.time)
    default: return .working(step: currentStep(in: ordered, turn: last.turn))
    }
}

private func isTurnEvent(_ kind: EventKind) -> Bool {
    kind == .turnStarted || kind == .turnDone || kind == .turnError
}

private func currentStep(in ordered: [SessionEvent], turn: String) -> String? {
    for event in ordered.reversed() where event.turn == turn {
        guard case .update(let sessionUpdate, let title) = event.data else { continue }
        if sessionUpdate == "agent_thought_chunk" { return "Thinking" }
        if sessionUpdate == "tool_call" || sessionUpdate == "tool_call_update", let title { return title }
    }
    return nil
}
