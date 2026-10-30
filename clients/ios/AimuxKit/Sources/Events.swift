import Foundation

public enum EventKind: Equatable, Hashable, Sendable {
    case turnStarted, update, approval, approvalResolved, turnDone, turnError
    /// A snapshot of the chat queue (turn ""), sent only to streams that ask for it.
    case queue
    case unknown(String)

    public init(rawValue: String) {
        switch rawValue {
        case "turn_started": self = .turnStarted
        case "update": self = .update
        case "approval": self = .approval
        case "approval_resolved": self = .approvalResolved
        case "turn_done": self = .turnDone
        case "turn_error": self = .turnError
        case "queue": self = .queue
        default: self = .unknown(rawValue)
        }
    }
}

public struct PermissionOption: Decodable, Equatable, Hashable, Sendable {
    public let optionId: String
    public let name: String
    public let kind: String

    public init(optionId: String, name: String, kind: String) {
        self.optionId = optionId
        self.name = name
        self.kind = kind
    }

    private enum CodingKeys: String, CodingKey { case optionId, name, kind }

    /// A missing field reads as "".
    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        optionId = (try? container.decodeIfPresent(String.self, forKey: .optionId)) ?? ""
        name = (try? container.decodeIfPresent(String.self, forKey: .name)) ?? ""
        kind = (try? container.decodeIfPresent(String.self, forKey: .kind)) ?? ""
    }
}

/// A permission request from the agent. `command` is the shell command it wants to run, when it is one.
public struct Approval: Decodable, Equatable, Hashable, Sendable {
    public let approvalId: String
    public let title: String
    public let options: [PermissionOption]
    public let command: String?

    public init(approvalId: String, title: String, options: [PermissionOption], command: String?) {
        self.approvalId = approvalId
        self.title = title
        self.options = options
        self.command = command
    }

    private enum CodingKeys: String, CodingKey { case approvalId, title, options, toolCall }
    private struct ToolCall: Decodable { let rawInput: ShellInput? }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        approvalId = try container.decode(String.self, forKey: .approvalId)
        title = (try? container.decodeIfPresent(String.self, forKey: .title)) ?? ""
        options = try container.decode(LossyList<PermissionOption>.self, forKey: .options).elements
        let toolCall = try? container.decode(ToolCall.self, forKey: .toolCall)
        command = toolCall?.rawInput?.command
    }
}

/// `rawInput` of a tool call; only a string `command` is read, whatever else the tool takes.
private struct ShellInput: Decodable {
    let command: String?

    private enum CodingKeys: String, CodingKey { case command }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        command = try? container.decode(String.self, forKey: .command)
    }
}

public enum EventData: Equatable, Hashable, Sendable {
    case turnStarted(text: String)
    case update(sessionUpdate: String, title: String?)
    case approval(Approval)
    /// `outcome` is nil when the server sent none.
    case approvalResolved(approvalId: String, outcome: String?)
    case turnDone(stopReason: String)
    case turnError(error: String)
    case queue(QueueStatus)
    /// Data of a kind this app does not know.
    case other
}

/// One entry of an ACP plan update. A missing field reads as "".
public struct UpdatePlanEntry: Decodable, Equatable, Hashable, Sendable {
    public let content: String
    public let status: String

    public init(content: String, status: String) {
        self.content = content
        self.status = status
    }

    private enum CodingKeys: String, CodingKey { case content, status }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        content = (try? container.decodeIfPresent(String.self, forKey: .content)) ?? ""
        status = (try? container.decodeIfPresent(String.self, forKey: .status)) ?? ""
    }
}

/// The ACP payload of an `update` event, the fields the chat timeline reads. A field of the wrong type reads as nil.
public struct SessionUpdate: Decodable, Equatable, Hashable, Sendable {
    public let sessionUpdate: String
    public let title: String?
    public let toolCallId: String?
    /// The tool kind (`read`, `edit`, `search`, `execute`, ...).
    public let kind: String?
    public let status: String?
    /// The chunk's text, when its `content` is `{type: "text"}` with a string `text`.
    public let text: String?
    /// Plan entries; nil when the update carries none.
    public let entries: [UpdatePlanEntry]?
    /// A tool call's `rawInput` string fields, when `rawInput` is an object (other values left out).
    public let rawInput: [String: String]?
    /// A tool call's `content`, when it is an array; items of another shape are left out.
    public let toolContent: [ToolContentItem]?

    public init(sessionUpdate: String, title: String? = nil, toolCallId: String? = nil, kind: String? = nil,
                status: String? = nil, text: String? = nil, entries: [UpdatePlanEntry]? = nil,
                rawInput: [String: String]? = nil, toolContent: [ToolContentItem]? = nil) {
        self.sessionUpdate = sessionUpdate
        self.title = title
        self.toolCallId = toolCallId
        self.kind = kind
        self.status = status
        self.text = text
        self.entries = entries
        self.rawInput = rawInput
        self.toolContent = toolContent
    }

    private enum CodingKeys: String, CodingKey {
        case sessionUpdate, title, toolCallId, kind, status, content, entries, rawInput
    }
    private struct Content: Decodable { let type: String?; let text: String? }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        sessionUpdate = try container.decode(String.self, forKey: .sessionUpdate)
        title = try? container.decodeIfPresent(String.self, forKey: .title)
        toolCallId = try? container.decodeIfPresent(String.self, forKey: .toolCallId)
        kind = try? container.decodeIfPresent(String.self, forKey: .kind)
        status = try? container.decodeIfPresent(String.self, forKey: .status)
        let content = try? container.decodeIfPresent(Content.self, forKey: .content)
        text = content?.type == "text" ? content?.text : nil
        entries = (try? container.decodeIfPresent(LossyList<UpdatePlanEntry>.self, forKey: .entries))?.elements
        rawInput = (try? container.decodeIfPresent(StringFields.self, forKey: .rawInput))?.values
        toolContent = (try? container.decodeIfPresent([Maybe<ToolContentItem>].self, forKey: .content))?
            .compactMap(\.value)
    }
}

public struct SessionEvent: Decodable, Equatable, Hashable, Sendable {
    public let seq: Int
    public let turn: String
    public let kind: EventKind
    public let time: Date
    public let data: EventData
    /// The full payload of an `update` event; nil for other kinds.
    public let update: SessionUpdate?

    /// `update` defaults to what `data` holds, for events built in code.
    public init(seq: Int, turn: String, kind: EventKind, time: Date, data: EventData, update: SessionUpdate? = nil) {
        self.seq = seq
        self.turn = turn
        self.kind = kind
        self.time = time
        self.data = data
        if let update {
            self.update = update
        } else if case .update(let sessionUpdate, let title) = data {
            self.update = SessionUpdate(sessionUpdate: sessionUpdate, title: title)
        } else {
            self.update = nil
        }
    }

    private enum CodingKeys: String, CodingKey { case seq, turn, kind, time, data }
    /// Missing `text` reads as "".
    private struct TurnStarted: Decodable { let text: String? }
    private struct Resolved: Decodable { let approvalId: String; let outcome: String? }
    private struct Done: Decodable { let stopReason: String }
    /// A missing `error` reads as "".
    private struct Failed: Decodable { let error: String? }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        seq = try container.decode(Int.self, forKey: .seq)
        turn = try container.decode(String.self, forKey: .turn)
        time = try container.decode(Date.self, forKey: .time)
        kind = EventKind(rawValue: try container.decode(String.self, forKey: .kind))
        var update: SessionUpdate?
        switch kind {
        case .turnStarted:
            // Missing `data` reads as no text.
            let started = try? container.decodeIfPresent(TurnStarted.self, forKey: .data)
            data = .turnStarted(text: started?.text ?? "")
        case .update:
            let decoded = try container.decode(SessionUpdate.self, forKey: .data)
            update = decoded
            data = .update(sessionUpdate: decoded.sessionUpdate, title: decoded.title)
        case .approval:
            data = .approval(try container.decode(Approval.self, forKey: .data))
        case .approvalResolved:
            let resolved = try container.decode(Resolved.self, forKey: .data)
            data = .approvalResolved(approvalId: resolved.approvalId, outcome: resolved.outcome)
        case .turnDone:
            data = .turnDone(stopReason: try container.decode(Done.self, forKey: .data).stopReason)
        case .turnError:
            data = .turnError(error: try container.decode(Failed.self, forKey: .data).error ?? "")
        case .queue:
            data = .queue(try container.decode(QueueStatus.self, forKey: .data))
        case .unknown:
            data = .other
        }
        self.update = update
    }
}

public struct EventPage: Decodable, Equatable, Sendable {
    public let events: [SessionEvent]
    public let log: String

    private enum CodingKeys: String, CodingKey { case events, log }

    public init(events: [SessionEvent], log: String) {
        self.events = events
        self.log = log
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        events = try container.decode(LossyList<SessionEvent>.self, forKey: .events).elements
        log = try container.decode(String.self, forKey: .log)
    }
}
